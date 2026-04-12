package app

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/koltyakov/quant/internal/config"
	"github.com/koltyakov/quant/internal/embed"
	"github.com/koltyakov/quant/internal/extract"
	"github.com/koltyakov/quant/internal/index"
	"github.com/koltyakov/quant/internal/ingest"
	"github.com/koltyakov/quant/internal/logx"
	runtimestate "github.com/koltyakov/quant/internal/runtime"
	"github.com/koltyakov/quant/internal/scan"
	"github.com/koltyakov/quant/internal/watch"
)

var ErrOCRFailed = extract.ErrOCRFailed

type IndexAction string

const (
	IndexNoop    IndexAction = "noop"
	IndexUpdated IndexAction = "updated"
	IndexRemoved IndexAction = "removed"
)

type Indexer struct {
	Cfg       *config.Config
	Store     index.DocumentWriter
	HNSWStore index.HNSWBuilder
	Embedder  embed.Embedder
	Extractor extract.Extractor
	Pipeline  *ingest.Pipeline

	Paths   *PathSyncTracker
	Live    *LiveIndexQueue
	Retries *RetryScheduler

	IndexState *runtimestate.IndexStateTracker
	Resync     *ResyncCoordinator
}

func (idx *Indexer) initResyncCoordinator() {
	idx.Resync = NewResyncCoordinator(ResyncCallbacks{
		OnStartup: func(ctx context.Context) (SyncReport, error) {
			idx.setIndexState(runtimestate.IndexStateIndexing, "initial filesystem scan in progress")
			logx.Info("starting initial scan", "watch_dir", idx.Cfg.WatchDir)
			return idx.InitialSyncWithReport(ctx)
		},
		OnResync: func(ctx context.Context) (SyncReport, error) {
			logx.Info("starting filesystem resync", "watch_dir", idx.Cfg.WatchDir)
			return idx.InitialSyncWithReport(ctx)
		},
		OnState: idx.setIndexState,
		OnReady: func(ctx context.Context, report SyncReport) {
			docCount, _, err := idx.Store.Stats(ctx)
			if err != nil {
				logx.Error("fetching index stats failed", "err", err)
			} else {
				logx.Info("initial scan complete", "documents", docCount)
				if report.HadIndexFailures {
					logx.Warn("initial scan completed with indexing failures", "action", "keeping database backup until a clean rebuild succeeds")
				} else {
					idx.HNSWStore.RemoveBackup()
				}
			}
			if !idx.HNSWStore.HNSWReady() {
				idx.HNSWStore.LoadHNSWFromState(ctx)
			}
			if !idx.HNSWStore.HNSWReady() {
				go func() {
					if err := idx.HNSWStore.BuildHNSW(ctx); err != nil {
						logx.Warn("hnsw build failed", "err", err)
					}
				}()
			}
		},
	})
}

func (idx *Indexer) RunInitialSync(ctx context.Context) {
	if idx.Resync == nil {
		idx.initResyncCoordinator()
	}
	idx.Resync.RunInitialSync(ctx)
}

func (idx *Indexer) RequestResync(ctx context.Context) {
	if idx.Resync == nil {
		idx.initResyncCoordinator()
	}
	idx.Resync.RequestResync(ctx)
}

func (idx *Indexer) InitialSync(ctx context.Context) error {
	_, err := idx.InitialSyncWithReport(ctx)
	return err
}

func (idx *Indexer) InitialSyncWithReport(ctx context.Context) (SyncReport, error) {
	report := SyncReport{}

	gi, err := scan.LoadGitIgnore(idx.Cfg.WatchDir)
	if err != nil {
		return report, fmt.Errorf("loading gitignore: %w", err)
	}

	docs, err := idx.Store.ListDocuments(ctx)
	if err != nil {
		return report, fmt.Errorf("listing indexed documents: %w", err)
	}
	docByPath := make(map[string]*index.Document, len(docs))
	for i := range docs {
		key, err := NormalizeStoredDocumentPath(idx.Cfg.WatchDir, docs[i].Path)
		if err != nil {
			continue
		}
		if key != docs[i].Path {
			if err := idx.Store.RenameDocumentPath(ctx, docs[i].Path, key); err != nil {
				return report, fmt.Errorf("renaming indexed document %s to %s: %w", docs[i].Path, key, err)
			}
			docs[i].Path = key
		}
		docByPath[docs[i].Path] = &docs[i]
	}

	type pendingItem struct {
		key    string
		result scan.Result
		doc    *index.Document
	}

	type indexResult struct {
		path    string
		modTime time.Time
		action  IndexAction
		err     error
	}

	workers := idx.workerCount(0)

	jobs := make(chan pendingItem)
	indexResults := make(chan indexResult, workers)
	walkDone := make(chan error, 1)
	scannedPaths := make(map[string]bool)

	var wg sync.WaitGroup
	for i := 0; i < workers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for item := range jobs {
				modTime := item.result.ModifiedAt
				action, err := idx.SyncDocument(ctx, item.key, item.result.Path, &modTime, item.doc)
				indexResults <- indexResult{path: item.result.Path, modTime: modTime, action: action, err: err}
			}
		}()
	}

	go func() {
		err := scan.Walk(idx.Cfg.WatchDir, gi, func(r scan.Result) error {
			if idx.shouldIgnorePath(r.Path) {
				return nil
			}
			key, err := DocumentKey(idx.Cfg.WatchDir, r.Path)
			if err != nil {
				return fmt.Errorf("computing document key for %s: %w", r.Path, err)
			}
			scannedPaths[key] = true
			if !idx.Extractor.Supports(r.Path) {
				return nil
			}
			doc := docByPath[key]

			select {
			case <-ctx.Done():
				return ctx.Err()
			case jobs <- pendingItem{key: key, result: r, doc: doc}:
				return nil
			}
		})
		close(jobs)
		wg.Wait()
		close(indexResults)
		walkDone <- err
	}()

	for result := range indexResults {
		if result.err != nil {
			report.HadIndexFailures = true
			logx.Error("indexing failed", "path", result.path, "err", result.err)
			if idx.Retries != nil {
				idx.Retries.Schedule(result.path, result.modTime, func(retryModTime time.Time) {
					select {
					case <-ctx.Done():
						idx.Retries.Clear(result.path)
						return
					default:
					}
					idx.EnqueueLiveIndex(ctx, result.path, retryModTime)
				})
			}
			continue
		}
		if idx.Retries != nil {
			idx.Retries.Clear(result.path)
		}
		switch result.action {
		case IndexUpdated:
			logx.Info("indexed document", "path", result.path)
		case IndexRemoved:
			logx.Info("removed document from index", "path", result.path)
		}
	}
	if walkErr := <-walkDone; walkErr != nil {
		if ctx.Err() != nil {
			return report, ctx.Err()
		}
		return report, fmt.Errorf("scanning directory: %w", walkErr)
	}
	if err := ctx.Err(); err != nil {
		return report, err
	}

	reconcileMatcher := scan.NewGitIgnoreMatcher(idx.Cfg.WatchDir, gi)

	for _, doc := range docs {
		if !scannedPaths[doc.Path] {
			absPath := filepath.Join(idx.Cfg.WatchDir, doc.Path)
			if shouldIndex, err := idx.shouldIndexExistingPath(reconcileMatcher, absPath); err != nil {
				logx.Error("reconciling stale document failed", "path", doc.Path, "err", err)
				continue
			} else if shouldIndex {
				action, err := idx.SyncDocument(ctx, doc.Path, absPath, nil, &doc)
				if err != nil {
					report.HadIndexFailures = true
					logx.Error("reconciling existing document failed", "path", doc.Path, "err", err)
					continue
				}
				switch action {
				case IndexUpdated:
					logx.Info("indexed document", "path", absPath)
				case IndexRemoved:
					logx.Info("removed document from index", "path", absPath)
				}
				continue
			}
			if err := idx.Store.DeleteDocument(ctx, doc.Path); err != nil {
				logx.Error("removing stale document failed", "path", doc.Path, "err", err)
				continue
			}
			logx.Info("removed document from index", "path", doc.Path)
		}
	}

	return report, nil
}

func (idx *Indexer) workerCount(maxPending int) int {
	workers := idx.Cfg.IndexWorkers
	if workers < 1 {
		workers = 1
	}
	if maxPending > 0 && workers > maxPending {
		workers = maxPending
	}
	return workers
}

func (idx *Indexer) liveQueueSize() int {
	return LiveQueueSizeForWorkers(idx.Cfg.IndexWorkers)
}

func (idx *Indexer) StartLiveIndexWorkers(ctx context.Context, wg *sync.WaitGroup) {
	if idx.Live == nil {
		idx.Live = NewLiveIndexQueue(idx.liveQueueSize())
	}

	workers := idx.workerCount(0)
	for i := 0; i < workers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for {
				select {
				case <-ctx.Done():
					return
				case path := <-idx.Live.Jobs:
					idx.processLiveIndexRequest(ctx, path)
				}
			}
		}()
	}
}

func (idx *Indexer) EnqueueLiveIndex(ctx context.Context, path string, modTime time.Time) bool {
	if idx.Live == nil {
		idx.processLiveIndexRequestDirect(ctx, path, modTime)
		return true
	}

	if !idx.Live.MarkPending(path, modTime) {
		return true
	}

	select {
	case idx.Live.Jobs <- path:
		return true
	default:
		idx.Live.Cancel(path)
		idx.setIndexState(runtimestate.IndexStateDegraded, "live index queue overflow; full resync scheduled")
		logx.Warn("live index queue full; scheduling resync", "path", path)
		idx.RequestResync(ctx)
		return false
	}
}

func (idx *Indexer) processLiveIndexRequest(ctx context.Context, path string) {
	modTime, ok := idx.Live.StartProcessing(path)
	if !ok {
		return
	}

	idx.processLiveIndexRequestDirect(ctx, path, modTime)

	if idx.Live.FinishProcessing(path) {
		select {
		case idx.Live.Jobs <- path:
		default:
			idx.Live.Cancel(path)
			idx.setIndexState(runtimestate.IndexStateDegraded, "live index queue overflow; full resync scheduled")
			logx.Warn("live index queue full; scheduling resync", "path", path)
			idx.RequestResync(ctx)
		}
	}
}

func (idx *Indexer) processLiveIndexRequestDirect(ctx context.Context, path string, modTime time.Time) {
	action, err := idx.IndexFile(ctx, path, modTime)
	if err != nil {
		idx.setIndexState(runtimestate.IndexStateDegraded, "live indexing failed; some files may be stale")
		logx.Error("indexing failed", "path", path, "err", err)
		if idx.Retries != nil {
			idx.Retries.Schedule(path, modTime, func(retryModTime time.Time) {
				select {
				case <-ctx.Done():
					idx.Retries.Clear(path)
					return
				default:
				}
				idx.EnqueueLiveIndex(ctx, path, retryModTime)
			})
		}
	} else {
		if idx.Retries != nil {
			idx.Retries.Clear(path)
		}
		switch action {
		case IndexUpdated:
			logx.Info("indexed document", "path", path)
		case IndexRemoved:
			logx.Info("removed document from index", "path", path)
		}
	}
}

func (idx *Indexer) WatchLoop(ctx context.Context, watcher *watch.Watcher) {
	for {
		select {
		case <-ctx.Done():
			return
		case event, ok := <-watcher.Events():
			if !ok {
				return
			}
			idx.HandleWatchEvent(ctx, event)
		}
	}
}

func (idx *Indexer) HandleWatchEvent(ctx context.Context, event watch.Event) {
	if idx.shouldIgnorePath(event.Path) {
		return
	}

	switch event.Op {
	case watch.Resync:
		idx.RequestResync(ctx)

	case watch.Create, watch.Write:
		info, err := os.Stat(event.Path)
		if err != nil {
			if os.IsNotExist(err) {
				return
			}
			logx.Error("stating watched path failed", "path", event.Path, "err", err)
			return
		}
		if info.IsDir() {
			return
		}
		idx.EnqueueLiveIndex(ctx, event.Path, info.ModTime())

	case watch.Remove:
		key, err := DocumentKey(idx.Cfg.WatchDir, event.Path)
		if err != nil {
			logx.Error("removing document failed", "path", event.Path, "err", err)
			return
		}
		if event.IsDir {
			idx.Paths.InvalidatePrefix(key)
			if err := idx.Store.DeleteDocumentsByPrefix(ctx, key); err != nil {
				logx.Error("removing directory from index failed", "path", event.Path, "err", err)
				return
			}
		} else {
			action, err := idx.SyncDocument(ctx, key, event.Path, nil, nil)
			if err != nil {
				logx.Error("removing document failed", "path", event.Path, "err", err)
				return
			}
			if action == IndexNoop {
				return
			}
		}
		logx.Info("removed document from index", "path", event.Path)
	}
}

func (idx *Indexer) SetIndexState(state runtimestate.IndexState, message string) {
	idx.setIndexState(state, message)
}

func (idx *Indexer) setIndexState(state runtimestate.IndexState, message string) {
	if idx == nil || idx.IndexState == nil {
		return
	}
	idx.IndexState.Set(state, message)
}

func (idx *Indexer) SyncDocument(ctx context.Context, key, path string, modTime *time.Time, doc *index.Document) (IndexAction, error) {
	version, started := idx.Paths.Begin(key, modTime)
	if !started {
		return IndexNoop, nil
	}

	currentModTime := modTime
	currentDoc := doc
	currentVersion := version
	for {
		action, err := idx.syncDocumentOnce(ctx, key, path, currentModTime, currentDoc, currentVersion)
		nextVersion, rerun := idx.Paths.Finish(key)
		if !rerun {
			return action, err
		}
		currentModTime = nil
		currentDoc = nil
		currentVersion = nextVersion
	}
}

func (idx *Indexer) syncDocumentOnce(ctx context.Context, key, path string, modTime *time.Time, doc *index.Document, version uint64) (IndexAction, error) {
	info, err := os.Stat(path)
	if err != nil {
		if os.IsNotExist(err) {
			doc, err = idx.loadDocument(ctx, key, doc)
			if err != nil {
				return IndexNoop, err
			}
			return removeDocumentIfPresent(ctx, idx.Store, doc, key)
		}
		return IndexNoop, fmt.Errorf("stating file: %w", err)
	}
	if info.IsDir() {
		return IndexNoop, nil
	}

	if !idx.Extractor.Supports(path) {
		doc, err = idx.loadDocument(ctx, key, doc)
		if err != nil {
			return IndexNoop, err
		}
		return removeDocumentIfPresent(ctx, idx.Store, doc, key)
	}

	effectiveModTime := info.ModTime()

	doc, err = idx.loadDocument(ctx, key, doc)
	if err != nil {
		return IndexNoop, err
	}
	precomputedHash := ""
	if doc != nil && SameModTime(doc.ModifiedAt, effectiveModTime) {
		precomputedHash, err = scan.FileHash(path)
		if err != nil {
			return IndexNoop, fmt.Errorf("hashing file: %w", err)
		}
		if doc.Hash == precomputedHash {
			return IndexNoop, nil
		}
	}

	return idx.indexFileCore(ctx, key, path, effectiveModTime, precomputedHash, doc, version)
}

func (idx *Indexer) loadDocument(ctx context.Context, key string, doc *index.Document) (*index.Document, error) {
	if doc != nil {
		return doc, nil
	}
	doc, err := idx.Store.GetDocumentByPath(ctx, key)
	if err != nil {
		return nil, fmt.Errorf("loading indexed document: %w", err)
	}
	return doc, nil
}

func (idx *Indexer) shouldIndexExistingPath(matcher *scan.GitIgnoreMatcher, path string) (bool, error) {
	info, err := os.Stat(path)
	if err != nil {
		if os.IsNotExist(err) {
			return false, nil
		}
		return false, fmt.Errorf("stating path: %w", err)
	}
	if info.IsDir() {
		return false, nil
	}
	if idx.shouldIgnorePath(path) {
		return false, nil
	}
	if !idx.Extractor.Supports(path) {
		return false, nil
	}

	relDir, err := filepath.Rel(idx.Cfg.WatchDir, filepath.Dir(path))
	if err != nil {
		return false, fmt.Errorf("computing relative directory: %w", err)
	}
	current := idx.Cfg.WatchDir
	if relDir != "." {
		for _, part := range strings.Split(relDir, string(filepath.Separator)) {
			current = filepath.Join(current, part)
			matcher.Load(current)
		}
	}

	return !matcher.Matches(path), nil
}

func (idx *Indexer) IndexFile(ctx context.Context, path string, modTime time.Time) (IndexAction, error) {
	key, err := DocumentKey(idx.Cfg.WatchDir, path)
	if err != nil {
		return IndexNoop, fmt.Errorf("computing document key: %w", err)
	}
	return idx.SyncDocument(ctx, key, path, &modTime, nil)
}

func (idx *Indexer) getPipeline() *ingest.Pipeline {
	if idx.Pipeline == nil {
		batchSize := idx.Cfg.EmbedBatchSize
		if batchSize < 1 {
			batchSize = 16
		}
		idx.Pipeline = &ingest.Pipeline{
			Embedder:  idx.Embedder,
			ChunkSize: idx.Cfg.ChunkSize,
			Overlap:   idx.Cfg.ChunkOverlap,
			BatchSize: batchSize,
		}
	}
	return idx.Pipeline
}

func (idx *Indexer) indexFileCore(ctx context.Context, key, path string, modTime time.Time, precomputedHash string, doc *index.Document, version uint64) (IndexAction, error) {
	hash := precomputedHash
	if hash == "" {
		var err error
		hash, err = scan.FileHash(path)
		if err != nil {
			return IndexNoop, fmt.Errorf("hashing file: %w", err)
		}
	}
	if doc != nil && doc.Hash == hash {
		return IndexNoop, nil
	}

	if !idx.Paths.IsCurrent(key, version) {
		return IndexNoop, nil
	}

	text, err := idx.Extractor.Extract(ctx, path)
	if err != nil {
		if errors.Is(err, ErrOCRFailed) {
			return IndexNoop, err
		}
		return IndexNoop, fmt.Errorf("extracting text: %w", err)
	}

	if text == "" {
		return removeDocumentIfPresent(ctx, idx.Store, doc, key)
	}

	chunks := ingest.PrepareChunks(text, path, idx.Cfg.ChunkSize, idx.Cfg.ChunkOverlap)
	if len(chunks) == 0 {
		return removeDocumentIfPresent(ctx, idx.Store, doc, key)
	}

	existingByContent, _ := idx.Store.GetDocumentChunksByPath(ctx, key)

	chunkRecords, toEmbed, embedPositions, err := idx.getPipeline().DiffChunks(chunks, existingByContent)
	if err != nil {
		return IndexNoop, err
	}

	if err := idx.getPipeline().EmbedChunks(ctx, key, toEmbed, embedPositions, chunkRecords); err != nil {
		return IndexNoop, err
	}

	indexedDoc := &index.Document{
		Path:       key,
		Hash:       hash,
		ModifiedAt: modTime,
	}

	if err := idx.Store.ReindexDocument(ctx, indexedDoc, chunkRecords); err != nil {
		return IndexNoop, err
	}

	return IndexUpdated, nil
}

func (idx *Indexer) shouldIgnorePath(path string) bool {
	if idx == nil || idx.Cfg == nil {
		return false
	}

	if idx.Cfg.DBPath != "" && IsCompanionLogPathForDB(idx.Cfg.DBPath, path) {
		return true
	}

	matcher := idx.Cfg.PathMatcher()
	if matcher == nil {
		return false
	}

	relPath, err := filepath.Rel(idx.Cfg.WatchDir, path)
	if err != nil {
		return false
	}

	return !matcher.ShouldIndex(relPath)
}

func DocumentKey(root, path string) (string, error) {
	rel, err := filepath.Rel(root, path)
	if err != nil {
		return "", err
	}
	rel = filepath.Clean(rel)
	if rel == "." {
		return "", fmt.Errorf("path %q resolves to watch root %q", path, root)
	}
	if rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return "", fmt.Errorf("path %q is outside watch root %q", path, root)
	}
	return rel, nil
}

func NormalizeStoredDocumentPath(root, storedPath string) (string, error) {
	return DocumentKey(root, filepath.Join(root, storedPath))
}

func SameModTime(a, b time.Time) bool {
	return normalizeModTime(a).UnixMicro() == normalizeModTime(b).UnixMicro()
}

func normalizeModTime(t time.Time) time.Time {
	return t.UTC().Round(0)
}

func removeDocumentIfPresent(ctx context.Context, store index.DocumentWriter, doc *index.Document, path string) (IndexAction, error) {
	if doc == nil {
		return IndexNoop, nil
	}
	if err := store.DeleteDocument(ctx, path); err != nil {
		return IndexNoop, fmt.Errorf("deleting empty document: %w", err)
	}
	return IndexRemoved, nil
}
