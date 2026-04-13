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
var ErrFileTooLarge = extract.ErrFileTooLarge

const quarantineDirName = ".quarantine"

type IndexAction string

const (
	IndexNoop    IndexAction = "noop"
	IndexUpdated IndexAction = "updated"
	IndexRemoved IndexAction = "removed"
)

type IndexerConfig struct {
	Cfg       *config.Config
	Store     index.DocumentWriter
	HNSWStore index.HNSWBuilder
	Embedder  embed.Embedder
	Extractor extract.Extractor
}

type Indexer struct {
	cfg       *config.Config
	store     index.DocumentWriter
	hnswStore index.HNSWBuilder
	embedder  embed.Embedder
	extractor extract.Extractor
	pipeline  *ingest.Pipeline

	paths   *PathSyncTracker
	live    *LiveIndexQueue
	retries *RetryScheduler

	IndexState *runtimestate.IndexStateTracker
	Resync     *ResyncCoordinator
}

func NewIndexer(ic IndexerConfig) *Indexer {
	cfg := ic.Cfg
	return &Indexer{
		cfg:       cfg,
		store:     ic.Store,
		hnswStore: ic.HNSWStore,
		embedder:  ic.Embedder,
		extractor: ic.Extractor,
		pipeline: &ingest.Pipeline{
			Embedder:  ic.Embedder,
			ChunkSize: cfg.ChunkSize,
			Overlap:   cfg.ChunkOverlap,
			BatchSize: cfg.EmbedBatchSize,
		},
		paths:      NewPathSyncTracker(),
		live:       NewLiveIndexQueue(LiveQueueSizeForWorkers(cfg.IndexWorkers)),
		retries:    NewRetryScheduler(),
		IndexState: runtimestate.NewIndexStateTracker(),
	}
}

func (idx *Indexer) initResyncCoordinator() {
	idx.Resync = NewResyncCoordinator(ResyncCallbacks{
		OnStartup: func(ctx context.Context) (SyncReport, error) {
			idx.setIndexState(runtimestate.IndexStateIndexing, "initial filesystem scan in progress")
			logx.Info("starting initial scan", "watch_dir", idx.cfg.WatchDir)
			return idx.InitialSyncWithReport(ctx)
		},
		OnResync: func(ctx context.Context) (SyncReport, error) {
			logx.Info("starting filesystem resync", "watch_dir", idx.cfg.WatchDir)
			return idx.InitialSyncWithReport(ctx)
		},
		OnState: idx.setIndexState,
		OnReady: func(ctx context.Context, report SyncReport) {
			docCount, _, err := idx.store.Stats(ctx)
			if err != nil {
				logx.Error("fetching index stats failed", "err", err)
			} else {
				logx.Info("initial scan complete", "documents", docCount)
				if report.HadIndexFailures {
					logx.Warn("initial scan completed with indexing failures", "action", "keeping database backup until a clean rebuild succeeds")
				} else {
					idx.hnswStore.RemoveBackup()
				}
			}
			if !idx.hnswStore.HNSWReady() {
				idx.hnswStore.LoadHNSWFromState(ctx)
			}
			if !idx.hnswStore.HNSWReady() {
				go func() {
					if err := idx.hnswStore.BuildHNSW(ctx); err != nil {
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

	gi, err := scan.LoadGitIgnore(idx.cfg.WatchDir)
	if err != nil {
		return report, fmt.Errorf("loading gitignore: %w", err)
	}

	docs, err := idx.store.ListDocuments(ctx)
	if err != nil {
		return report, fmt.Errorf("listing indexed documents: %w", err)
	}
	docByPath := make(map[string]*index.Document, len(docs))
	for i := range docs {
		key, err := NormalizeStoredDocumentPath(idx.cfg.WatchDir, docs[i].Path)
		if err != nil {
			continue
		}
		if key != docs[i].Path {
			if err := idx.store.RenameDocumentPath(ctx, docs[i].Path, key); err != nil {
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
		err := scan.Walk(idx.cfg.WatchDir, gi, func(r scan.Result) error {
			if idx.shouldIgnorePath(r.Path) {
				return nil
			}
			key, err := DocumentKey(idx.cfg.WatchDir, r.Path)
			if err != nil {
				return fmt.Errorf("computing document key for %s: %w", r.Path, err)
			}
			scannedPaths[key] = true
			if !idx.extractor.Supports(r.Path) {
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
			idx.scheduleIndexRetry(ctx, result.path, result.modTime, result.err)
			reclaimProcessMemory()
			continue
		}
		if idx.retries != nil {
			idx.retries.Clear(result.path)
		}
		switch result.action {
		case IndexUpdated:
			logx.Info("indexed document", "path", result.path)
		case IndexRemoved:
			logx.Info("removed document from index", "path", result.path)
		}
		reclaimProcessMemory()
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

	reconcileMatcher := scan.NewGitIgnoreMatcher(idx.cfg.WatchDir, gi)

	for _, doc := range docs {
		if !scannedPaths[doc.Path] {
			absPath := filepath.Join(idx.cfg.WatchDir, doc.Path)
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
				reclaimProcessMemory()
				continue
			}
			if err := idx.store.DeleteDocument(ctx, doc.Path); err != nil {
				logx.Error("removing stale document failed", "path", doc.Path, "err", err)
				continue
			}
			logx.Info("removed document from index", "path", doc.Path)
		}
	}

	return report, nil
}

func (idx *Indexer) workerCount(maxPending int) int {
	workers := idx.cfg.IndexWorkers
	if workers < 1 {
		workers = 1
	}
	if maxPending > 0 && workers > maxPending {
		workers = maxPending
	}
	return workers
}

func (idx *Indexer) liveQueueSize() int {
	return LiveQueueSizeForWorkers(idx.cfg.IndexWorkers)
}

func (idx *Indexer) StartLiveIndexWorkers(ctx context.Context, wg *sync.WaitGroup) {
	if idx.live == nil {
		idx.live = NewLiveIndexQueue(idx.liveQueueSize())
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
				case path := <-idx.live.Jobs:
					idx.processLiveIndexRequest(ctx, path)
				}
			}
		}()
	}
}

func (idx *Indexer) EnqueueLiveIndex(ctx context.Context, path string, modTime time.Time) bool {
	if idx.live == nil {
		idx.processLiveIndexRequestDirect(ctx, path, modTime)
		return true
	}

	if !idx.live.MarkPending(path, modTime) {
		return true
	}

	select {
	case idx.live.Jobs <- path:
		return true
	default:
		idx.live.Cancel(path)
		idx.setIndexState(runtimestate.IndexStateDegraded, "live index queue overflow; full resync scheduled")
		logx.Warn("live index queue full; scheduling resync", "path", path)
		idx.RequestResync(ctx)
		return false
	}
}

func (idx *Indexer) processLiveIndexRequest(ctx context.Context, path string) {
	modTime, ok := idx.live.StartProcessing(path)
	if !ok {
		return
	}

	idx.processLiveIndexRequestDirect(ctx, path, modTime)

	if idx.live.FinishProcessing(path) {
		select {
		case idx.live.Jobs <- path:
		default:
			idx.live.Cancel(path)
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
		idx.scheduleIndexRetry(ctx, path, modTime, err)
	} else {
		if idx.retries != nil {
			idx.retries.Clear(path)
		}
		switch action {
		case IndexUpdated:
			logx.Info("indexed document", "path", path)
		case IndexRemoved:
			logx.Info("removed document from index", "path", path)
		}
	}
	reclaimProcessMemory()
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
		key, err := DocumentKey(idx.cfg.WatchDir, event.Path)
		if err != nil {
			logx.Error("removing document failed", "path", event.Path, "err", err)
			return
		}
		if event.IsDir {
			idx.paths.InvalidatePrefix(key)
			if err := idx.store.DeleteDocumentsByPrefix(ctx, key); err != nil {
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

// LiveQueue returns the live index queue for testing.
func (idx *Indexer) LiveQueue() *LiveIndexQueue { return idx.live }

func (idx *Indexer) RunHNSWReoptimizer(ctx context.Context, store reoptimizeStore, threshold float64) {
	ticker := time.NewTicker(5 * time.Minute)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			if !store.HNSWReady() {
				continue
			}
			if !store.HNSWReoptimizationNeeded(threshold) {
				continue
			}
			logx.Info("hnsw re-optimization triggered", "threshold", threshold)
			if err := store.BuildHNSW(ctx); err != nil {
				logx.Warn("hnsw re-optimization failed", "err", err)
			} else {
				logx.Info("hnsw re-optimization complete")
			}
		}
	}
}

type reoptimizeStore interface {
	HNSWReady() bool
	HNSWReoptimizationNeeded(threshold float64) bool
	BuildHNSW(ctx context.Context) error
}

func (idx *Indexer) setIndexState(state runtimestate.IndexState, message string) {
	if idx == nil || idx.IndexState == nil {
		return
	}
	idx.IndexState.Set(state, message)
}

func (idx *Indexer) SyncDocument(ctx context.Context, key, path string, modTime *time.Time, doc *index.Document) (IndexAction, error) {
	version, started := idx.paths.Begin(key, modTime)
	if !started {
		return IndexNoop, nil
	}

	currentModTime := modTime
	currentDoc := doc
	currentVersion := version
	for {
		action, err := idx.syncDocumentOnce(ctx, key, path, currentModTime, currentDoc, currentVersion)
		nextVersion, rerun := idx.paths.Finish(key)
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
			return removeDocumentIfPresent(ctx, idx.store, doc, key)
		}
		return IndexNoop, fmt.Errorf("stating file: %w", err)
	}
	if info.IsDir() {
		return IndexNoop, nil
	}

	if !idx.extractor.Supports(path) {
		doc, err = idx.loadDocument(ctx, key, doc)
		if err != nil {
			return IndexNoop, err
		}
		return removeDocumentIfPresent(ctx, idx.store, doc, key)
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
	doc, err := idx.store.GetDocumentByPath(ctx, key)
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
	if !idx.extractor.Supports(path) {
		return false, nil
	}

	relDir, err := filepath.Rel(idx.cfg.WatchDir, filepath.Dir(path))
	if err != nil {
		return false, fmt.Errorf("computing relative directory: %w", err)
	}
	current := idx.cfg.WatchDir
	if relDir != "." {
		for _, part := range strings.Split(relDir, string(filepath.Separator)) {
			current = filepath.Join(current, part)
			matcher.Load(current)
		}
	}

	return !matcher.Matches(path), nil
}

func (idx *Indexer) IndexFile(ctx context.Context, path string, modTime time.Time) (IndexAction, error) {
	key, err := DocumentKey(idx.cfg.WatchDir, path)
	if err != nil {
		return IndexNoop, fmt.Errorf("computing document key: %w", err)
	}
	return idx.SyncDocument(ctx, key, path, &modTime, nil)
}

func (idx *Indexer) getPipeline() *ingest.Pipeline {
	if idx.pipeline == nil {
		batchSize := idx.cfg.EmbedBatchSize
		if batchSize < 1 {
			batchSize = 16
		}
		idx.pipeline = &ingest.Pipeline{
			Embedder:  idx.embedder,
			ChunkSize: idx.cfg.ChunkSize,
			Overlap:   idx.cfg.ChunkOverlap,
			BatchSize: batchSize,
		}
	}
	return idx.pipeline
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

	if !idx.paths.IsCurrent(key, version) {
		return IndexNoop, nil
	}

	text, err := idx.extractor.Extract(ctx, path)
	if err != nil {
		if errors.Is(err, ErrOCRFailed) {
			return IndexNoop, err
		}
		return IndexNoop, fmt.Errorf("extracting text: %w", err)
	}

	if text == "" {
		return removeDocumentIfPresent(ctx, idx.store, doc, key)
	}

	chunks := ingest.PrepareChunks(text, path, idx.cfg.ChunkSize, idx.cfg.ChunkOverlap)
	if len(chunks) == 0 {
		return removeDocumentIfPresent(ctx, idx.store, doc, key)
	}

	existingByContent, _ := idx.store.GetDocumentChunksByPath(ctx, key)

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

	if err := idx.store.ReindexDocument(ctx, indexedDoc, chunkRecords); err != nil {
		return IndexNoop, err
	}

	return IndexUpdated, nil
}

func (idx *Indexer) shouldIgnorePath(path string) bool {
	if idx == nil || idx.cfg == nil {
		return false
	}

	if isQuarantinePath(idx.cfg.WatchDir, path) {
		return true
	}

	if idx.cfg.DBPath != "" && IsCompanionLogPathForDB(idx.cfg.DBPath, path) {
		return true
	}

	matcher := idx.cfg.PathMatcher()
	if matcher == nil {
		return false
	}

	relPath, err := filepath.Rel(idx.cfg.WatchDir, path)
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

func (idx *Indexer) scheduleIndexRetry(ctx context.Context, path string, modTime time.Time, err error) {
	if idx.retries == nil {
		return
	}
	if !shouldRetryIndexError(err) {
		idx.retries.Clear(path)
		logx.Warn("not retrying path", "path", path, "err", err)
		if shouldQuarantineIndexError(err) {
			idx.quarantineFailedPath(ctx, path, err)
		}
		return
	}
	result := idx.retries.Schedule(path, modTime, func(retryModTime time.Time) {
		select {
		case <-ctx.Done():
			idx.retries.Clear(path)
			return
		default:
		}
		idx.EnqueueLiveIndex(ctx, path, retryModTime)
	})
	if result == RetryScheduleGaveUp && shouldQuarantineIndexError(err) {
		idx.quarantineFailedPath(ctx, path, err)
	}
}

func shouldRetryIndexError(err error) bool {
	switch {
	case err == nil:
		return false
	case errors.Is(err, context.Canceled), errors.Is(err, context.DeadlineExceeded):
		return false
	case errors.Is(err, ErrOCRFailed):
		return false
	case errors.Is(err, ErrFileTooLarge):
		return false
	case errors.Is(err, embed.ErrPermanent):
		return false
	default:
		return true
	}
}

func shouldQuarantineIndexError(err error) bool {
	switch {
	case err == nil:
		return false
	case errors.Is(err, context.Canceled), errors.Is(err, context.DeadlineExceeded):
		return false
	case errors.Is(err, ErrOCRFailed):
		return true
	case errors.Is(err, ErrFileTooLarge):
		return true
	case errors.Is(err, embed.ErrPermanent):
		return true
	default:
		return false
	}
}

func isQuarantinePath(root, path string) bool {
	rel, err := filepath.Rel(root, path)
	if err != nil {
		return false
	}
	rel = filepath.Clean(rel)
	return rel == quarantineDirName || strings.HasPrefix(rel, quarantineDirName+string(filepath.Separator))
}

func quarantineDestination(root, path string) (string, error) {
	rel, err := filepath.Rel(root, path)
	if err != nil {
		return "", err
	}
	rel = filepath.Clean(rel)
	if rel == "." || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return "", fmt.Errorf("path %q is outside watch root %q", path, root)
	}
	if rel == quarantineDirName || strings.HasPrefix(rel, quarantineDirName+string(filepath.Separator)) {
		return filepath.Join(root, rel), nil
	}
	return filepath.Join(root, quarantineDirName, rel), nil
}

func (idx *Indexer) quarantineFailedPath(ctx context.Context, path string, failure error) {
	if idx == nil || idx.cfg == nil || path == "" || failure == nil {
		return
	}

	dst, err := quarantineDestination(idx.cfg.WatchDir, path)
	if err != nil {
		logx.Warn("quarantining failed path failed", "path", path, "err", err)
		return
	}
	if path == dst {
		return
	}
	if err := os.MkdirAll(filepath.Dir(dst), 0750); err != nil {
		logx.Warn("quarantining failed path failed", "path", path, "dst", dst, "err", err)
		return
	}
	_ = os.Remove(dst)
	if err := os.Rename(path, dst); err != nil {
		if os.IsNotExist(err) {
			return
		}
		logx.Warn("quarantining failed path failed", "path", path, "dst", dst, "err", err)
		return
	}
	logPath := dst + ".log"
	if writeErr := os.WriteFile(logPath, []byte(failure.Error()+"\n"), 0600); writeErr != nil {
		logx.Warn("writing quarantine log failed", "path", path, "log", logPath, "err", writeErr)
	}

	key, keyErr := DocumentKey(idx.cfg.WatchDir, path)
	if keyErr != nil {
		logx.Warn("removing quarantined document from index failed", "path", path, "err", keyErr)
	} else if delErr := idx.store.DeleteDocument(ctx, key); delErr != nil {
		logx.Warn("removing quarantined document from index failed", "path", path, "err", delErr)
	}

	logx.Warn("quarantined path", "path", path, "dst", dst, "log", logPath)
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
