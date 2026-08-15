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

	"github.com/koltyakov/quant/internal/chunk"
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

// IndexAction describes the outcome of a single file sync operation.
type IndexAction string

const (
	IndexNoop    IndexAction = "noop"
	IndexUpdated IndexAction = "updated"
	IndexRemoved IndexAction = "removed"
)

// IndexerConfig holds the dependencies needed to construct an Indexer.
type IndexerConfig struct {
	Cfg        *config.Config
	Store      index.DocumentWriter
	HNSWStore  index.HNSWBuilder
	Embedder   embed.Embedder
	Extractor  extract.Extractor
	Quarantine index.QuarantineRepository
	DedupStore ingest.ContentDedupStore
	Summarizer ingest.ChunkSummarizer
}

// Indexer coordinates file extraction, chunking, embedding, and storage for the index.
type Indexer struct {
	cfg        *config.Config
	store      index.DocumentWriter
	hnswStore  index.HNSWBuilder
	embedder   embed.Embedder
	extractor  extract.Extractor
	pipeline   *ingest.Pipeline
	quarantine index.QuarantineRepository
	dedupStore ingest.ContentDedupStore

	paths   *PathSyncTracker
	live    *LiveIndexQueue
	retries *RetryScheduler

	quarantineMu    sync.RWMutex
	quarantineCache map[string]bool
	quarantineGen   uint64

	IndexState *runtimestate.IndexStateTracker
	Resync     *ResyncCoordinator
}

func NewIndexer(ic IndexerConfig) *Indexer {
	cfg := ic.Cfg
	idx := &Indexer{
		cfg:        cfg,
		store:      ic.Store,
		hnswStore:  ic.HNSWStore,
		embedder:   ic.Embedder,
		extractor:  ic.Extractor,
		quarantine: ic.Quarantine,
		dedupStore: ic.DedupStore,
		pipeline: &ingest.Pipeline{
			Embedder:   ic.Embedder,
			ChunkSize:  cfg.ChunkSize,
			Overlap:    cfg.ChunkOverlap,
			BatchSize:  cfg.EmbedBatchSize,
			DedupStore: ic.DedupStore,
			Summarizer: ic.Summarizer,
		},
		paths:      NewPathSyncTracker(),
		live:       NewLiveIndexQueue(LiveQueueSizeForWorkers(cfg.IndexWorkers)),
		retries:    NewRetryScheduler(),
		IndexState: runtimestate.NewIndexStateTracker(),
	}
	idx.initResyncCoordinator()
	return idx
}

func (idx *Indexer) initResyncCoordinator() {
	if idx == nil || idx.Resync != nil {
		return
	}
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
	idx.initResyncCoordinator()
	idx.Resync.RunInitialSync(ctx)
}

func (idx *Indexer) RequestResync(ctx context.Context) {
	idx.initResyncCoordinator()
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
		ref, err := ResolveStoredDocumentRef(idx.cfg.WatchDir, docs[i].Path)
		if err != nil {
			continue
		}
		if ref.Key != docs[i].Path {
			if err := idx.store.RenameDocumentPath(ctx, docs[i].Path, ref.Key); err != nil {
				return report, fmt.Errorf("renaming indexed document %s to %s: %w", docs[i].Path, ref.Key, err)
			}
			docs[i].Path = ref.Key
		}
		docByPath[docs[i].Path] = &docs[i]
	}

	type pendingItem struct {
		ref    DocumentRef
		result scan.Result
		doc    *index.Document
	}

	type indexResult struct {
		ref     DocumentRef
		modTime time.Time
		action  IndexAction
		err     error
	}

	// Snapshot the skip list once instead of querying it per scanned file. A
	// file quarantined by a worker mid-scan is one this pass has already
	// visited, so the snapshot cannot let a quarantined file slip through.
	quarantined := idx.quarantinedKeys(ctx)

	workers := idx.workerCount(0)

	jobs := make(chan pendingItem)
	indexResults := make(chan indexResult, workers)
	walkDone := make(chan error, 1)
	scannedPaths := make(map[string]bool)

	var wg sync.WaitGroup
	for range workers {
		wg.Go(func() {
			for item := range jobs {
				if quarantined[item.ref.Key] {
					continue
				}
				modTime := item.result.ModifiedAt
				action, err := idx.SyncDocumentRef(ctx, item.ref, &modTime, item.doc)
				indexResults <- indexResult{ref: item.ref, modTime: modTime, action: action, err: err}
			}
		})
	}

	go func() {
		err := scan.Walk(idx.cfg.WatchDir, gi, func(r scan.Result) error {
			if idx.shouldIgnorePath(r.Path) {
				return nil
			}
			ref, err := ResolveDocumentRef(idx.cfg.WatchDir, r.Path)
			if err != nil {
				return fmt.Errorf("computing document key for %s: %w", r.Path, err)
			}
			scannedPaths[ref.Key] = true
			if !idx.extractor.Supports(r.Path) {
				return nil
			}
			doc := docByPath[ref.Key]

			select {
			case <-ctx.Done():
				return ctx.Err()
			case jobs <- pendingItem{ref: ref, result: r, doc: doc}:
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
			logx.Error("indexing failed", "path", result.ref.AbsPath, "err", result.err)
			idx.scheduleIndexRetryRef(ctx, result.ref, result.modTime, result.err)
			continue
		}
		if idx.retries != nil {
			idx.retries.Clear(result.ref.Key)
		}
		switch result.action {
		case IndexUpdated:
			logx.Info("indexed document", "path", result.ref.AbsPath)
		case IndexRemoved:
			logx.Info("removed document from index", "path", result.ref.AbsPath)
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

	reconcileMatcher := scan.NewGitIgnoreMatcher(idx.cfg.WatchDir, gi)

	for _, doc := range docs {
		if !scannedPaths[doc.Path] {
			ref, refErr := ResolveStoredDocumentRef(idx.cfg.WatchDir, doc.Path)
			if refErr != nil {
				logx.Error("resolving stored document path failed", "path", doc.Path, "err", refErr)
				continue
			}
			if shouldIndex, err := idx.shouldIndexExistingPath(reconcileMatcher, ref.AbsPath); err != nil {
				logx.Error("reconciling stale document failed", "path", doc.Path, "err", err)
				continue
			} else if shouldIndex {
				action, err := idx.SyncDocumentRef(ctx, ref, nil, &doc)
				if err != nil {
					report.HadIndexFailures = true
					logx.Error("reconciling existing document failed", "path", doc.Path, "err", err)
					continue
				}
				switch action {
				case IndexUpdated:
					logx.Info("indexed document", "path", ref.AbsPath)
				case IndexRemoved:
					logx.Info("removed document from index", "path", ref.AbsPath)
				}
				continue
			}
			if err := idx.store.DeleteDocument(ctx, ref.Key); err != nil {
				logx.Error("removing stale document failed", "path", doc.Path, "err", err)
				continue
			}
			logx.Info("removed document from index", "path", ref.AbsPath)
		}
	}

	return report, nil
}

func (idx *Indexer) workerCount(maxPending int) int {
	workers := max(idx.cfg.IndexWorkers, 1)
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
	for range workers {
		wg.Go(func() {
			for {
				select {
				case <-ctx.Done():
					return
				case key := <-idx.live.Jobs:
					idx.processLiveIndexRequestKey(ctx, key)
				}
			}
		})
	}
}

func (idx *Indexer) EnqueueLiveIndex(ctx context.Context, path string, modTime time.Time) bool {
	ref, err := ResolveDocumentRef(idx.cfg.WatchDir, path)
	if err != nil {
		logx.Error("computing document key failed", "path", path, "err", err)
		return false
	}
	return idx.enqueueLiveDocument(ctx, ref, modTime)
}

func (idx *Indexer) enqueueLiveDocument(ctx context.Context, ref DocumentRef, modTime time.Time) bool {
	if idx.live == nil {
		idx.processLiveIndexDocumentDirect(ctx, ref, modTime)
		return true
	}

	if !idx.live.MarkPending(ref.Key, modTime) {
		return true
	}

	select {
	case idx.live.Jobs <- ref.Key:
		return true
	default:
		idx.live.Cancel(ref.Key)
		idx.setIndexState(runtimestate.IndexStateDegraded, "live index queue overflow; full resync scheduled")
		logx.Warn("live index queue full; scheduling resync", "path", ref.AbsPath)
		idx.RequestResync(ctx)
		return false
	}
}

func (idx *Indexer) processLiveIndexRequestKey(ctx context.Context, key string) {
	modTime, ok := idx.live.StartProcessing(key)
	if !ok {
		return
	}

	ref, err := ResolveDocumentRefFromKey(idx.cfg.WatchDir, key)
	if err != nil {
		logx.Error("resolving live index document failed", "key", key, "err", err)
		idx.live.CancelPrefix(key)
		idx.live.FinishProcessing(key)
		return
	}

	idx.processLiveIndexDocumentDirect(ctx, ref, modTime)

	if idx.live.FinishProcessing(key) {
		select {
		case idx.live.Jobs <- key:
		default:
			idx.live.Cancel(key)
			idx.setIndexState(runtimestate.IndexStateDegraded, "live index queue overflow; full resync scheduled")
			logx.Warn("live index queue full; scheduling resync", "path", ref.AbsPath)
			idx.RequestResync(ctx)
		}
	}
}

func (idx *Indexer) processLiveIndexDocumentDirect(ctx context.Context, ref DocumentRef, modTime time.Time) {
	action, err := idx.IndexDocument(ctx, ref, modTime)
	if err != nil {
		idx.setIndexState(runtimestate.IndexStateDegraded, "live indexing failed; some files may be stale")
		logx.Error("indexing failed", "path", ref.AbsPath, "err", err)
		idx.scheduleIndexRetryRef(ctx, ref, modTime, err)
	} else {
		if idx.retries != nil {
			idx.retries.Clear(ref.Key)
		}
		switch action {
		case IndexUpdated:
			logx.Info("indexed document", "path", ref.AbsPath)
		case IndexRemoved:
			logx.Info("removed document from index", "path", ref.AbsPath)
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
		info, err := statDocumentPath(idx.cfg.WatchDir, event.Path)
		if err != nil {
			if os.IsNotExist(err) {
				return
			}
			if errors.Is(err, errUnsafeDocumentPath) {
				logx.Warn("skipping unsafe watched path", "path", event.Path, "err", err)
				return
			}
			logx.Error("stating watched path failed", "path", event.Path, "err", err)
			return
		}
		if info.IsDir() {
			return
		}
		ref, err := ResolveDocumentRef(idx.cfg.WatchDir, event.Path)
		if err != nil {
			logx.Error("computing document key failed", "path", event.Path, "err", err)
			return
		}
		if idx.isQuarantined(ctx, ref.Key) {
			return
		}
		idx.enqueueLiveDocument(ctx, ref, info.ModTime())

	case watch.Remove:
		ref, err := ResolveDocumentRef(idx.cfg.WatchDir, event.Path)
		if err != nil {
			logx.Error("removing document failed", "path", event.Path, "err", err)
			return
		}
		if idx.isQuarantined(ctx, ref.Key) {
			return
		}
		if event.IsDir {
			idx.paths.InvalidatePrefix(ref.Key)
			if idx.live != nil {
				idx.live.CancelPrefix(ref.Key)
			}
			if idx.retries != nil {
				idx.retries.ClearPrefix(ref.Key)
			}
			if err := idx.store.DeleteDocumentsByPrefix(ctx, ref.Key); err != nil {
				logx.Error("removing directory from index failed", "path", event.Path, "err", err)
				return
			}
		} else {
			action, err := idx.SyncDocumentRef(ctx, ref, nil, nil)
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

	notReadyLogged := false
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			if !store.HNSWReady() {
				// The graph can be lost at runtime (e.g. a transient error
				// during indexing reset it). Rebuild instead of degrading
				// to brute-force search for the rest of the process.
				if !notReadyLogged {
					logx.Info("hnsw graph not ready; attempting rebuild")
					notReadyLogged = true
				}
				if err := store.BuildHNSW(ctx); err != nil {
					logx.Warn("hnsw rebuild failed", "err", err)
				}
				continue
			}
			notReadyLogged = false
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

func (idx *Indexer) RunPeriodicVacuum(ctx context.Context, store vacuumStore) {
	ticker := time.NewTicker(1 * time.Hour)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			if err := store.Vacuum(ctx); err != nil {
				logx.Warn("periodic vacuum failed", "err", err)
			}
		}
	}
}

type vacuumStore interface {
	Vacuum(ctx context.Context) error
}

func (idx *Indexer) RunHNSWPeriodicFlush(ctx context.Context, store hnswFlushStore) {
	ticker := time.NewTicker(2 * time.Minute)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			if store.HNSWReady() {
				if err := store.FlushHNSW(); err != nil {
					logx.Warn("hnsw final flush failed", "err", err)
				}
			}
			return
		case <-ticker.C:
			if !store.HNSWReady() {
				continue
			}
			if err := store.FlushHNSW(); err != nil {
				logx.Warn("hnsw periodic flush failed", "err", err)
			}
		}
	}
}

type hnswFlushStore interface {
	HNSWReady() bool
	FlushHNSW() error
}

func (idx *Indexer) setIndexState(state runtimestate.IndexState, message string) {
	if idx == nil || idx.IndexState == nil {
		return
	}
	idx.IndexState.Set(state, message)
}

func (idx *Indexer) SyncDocumentRef(ctx context.Context, ref DocumentRef, modTime *time.Time, doc *index.Document) (IndexAction, error) {
	if ref.AbsPath == "" {
		var err error
		ref, err = ResolveDocumentRefFromKey(idx.cfg.WatchDir, ref.Key)
		if err != nil {
			return IndexNoop, fmt.Errorf("resolving document key: %w", err)
		}
	}
	version, started := idx.paths.Begin(ref.Key, modTime)
	if !started {
		return IndexNoop, nil
	}

	currentDoc := doc
	currentVersion := version
	reruns := 0
	for {
		action, err := idx.syncDocumentOnceRef(ctx, ref, currentDoc, currentVersion)
		nextVersion, rerun := idx.paths.Finish(ref.Key)
		if !rerun {
			return action, err
		}
		reruns++
		if reruns >= maxConsecutiveSyncReruns {
			// A continuously modified file (e.g. an appended log) would
			// otherwise pin this worker in a rerun loop forever. Requeue
			// through the live queue so other paths make progress.
			idx.paths.Reset(ref.Key)
			if idx.live != nil {
				idx.enqueueLiveDocument(ctx, ref, time.Now())
			}
			return action, err
		}
		currentDoc = nil
		currentVersion = nextVersion
	}
}

func (idx *Indexer) syncDocumentOnceRef(ctx context.Context, ref DocumentRef, doc *index.Document, version uint64) (IndexAction, error) {
	info, err := statDocumentPath(idx.cfg.WatchDir, ref.AbsPath)
	if err != nil {
		if os.IsNotExist(err) {
			doc, err = idx.loadDocument(ctx, ref.Key, doc)
			if err != nil {
				return IndexNoop, err
			}
			return removeDocumentIfPresent(ctx, idx.store, doc, ref.Key)
		}
		if errors.Is(err, errUnsafeDocumentPath) {
			doc, loadErr := idx.loadDocument(ctx, ref.Key, doc)
			if loadErr != nil {
				return IndexNoop, loadErr
			}
			return removeDocumentIfPresent(ctx, idx.store, doc, ref.Key)
		}
		return IndexNoop, fmt.Errorf("stating file: %w", err)
	}
	if info.IsDir() {
		return IndexNoop, nil
	}

	if !idx.extractor.Supports(ref.AbsPath) {
		doc, err = idx.loadDocument(ctx, ref.Key, doc)
		if err != nil {
			return IndexNoop, err
		}
		return removeDocumentIfPresent(ctx, idx.store, doc, ref.Key)
	}

	effectiveModTime := info.ModTime()

	doc, err = idx.loadDocument(ctx, ref.Key, doc)
	if err != nil {
		return IndexNoop, err
	}
	precomputedHash := ""
	if doc != nil && SameModTime(doc.ModifiedAt, effectiveModTime) {
		precomputedHash, err = scan.FileHash(ref.AbsPath)
		if err != nil {
			return IndexNoop, fmt.Errorf("hashing file: %w", err)
		}
		if doc.Hash == precomputedHash {
			idx.refreshDocumentStat(ctx, ref.Key, effectiveModTime, info.Size())
			return IndexNoop, nil
		}
	}

	return idx.indexFileCoreRef(ctx, ref, effectiveModTime, info.Size(), precomputedHash, doc, version)
}

// refreshDocumentStat keeps the stored filesystem metadata current when the
// content hash already matches. Failures are non-fatal.
func (idx *Indexer) refreshDocumentStat(ctx context.Context, key string, modTime time.Time, size int64) {
	if err := idx.store.UpdateDocumentStat(ctx, key, modTime, size); err != nil {
		logx.Warn("failed to persist document stat", "path", key, "err", err)
	}
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
	info, err := statDocumentPath(idx.cfg.WatchDir, path)
	if err != nil {
		if os.IsNotExist(err) {
			return false, nil
		}
		if errors.Is(err, errUnsafeDocumentPath) {
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
		for part := range strings.SplitSeq(relDir, string(filepath.Separator)) {
			current = filepath.Join(current, part)
			matcher.Load(current)
		}
	}

	return !matcher.Matches(path), nil
}

func (idx *Indexer) IndexDocument(ctx context.Context, ref DocumentRef, modTime time.Time) (IndexAction, error) {
	return idx.SyncDocumentRef(ctx, ref, &modTime, nil)
}

func (idx *Indexer) getPipeline() *ingest.Pipeline {
	if idx.pipeline == nil {
		batchSize := idx.cfg.EmbedBatchSize
		if batchSize < 1 {
			batchSize = 16
		}
		idx.pipeline = &ingest.Pipeline{
			Embedder:   idx.embedder,
			ChunkSize:  idx.cfg.ChunkSize,
			Overlap:    idx.cfg.ChunkOverlap,
			BatchSize:  batchSize,
			DedupStore: idx.dedupStore,
		}
	}
	return idx.pipeline
}

func (idx *Indexer) indexFileCoreRef(ctx context.Context, ref DocumentRef, modTime time.Time, fileSize int64, precomputedHash string, doc *index.Document, version uint64) (IndexAction, error) {
	hash := precomputedHash
	if hash == "" {
		var err error
		hash, err = scan.FileHash(ref.AbsPath)
		if err != nil {
			return IndexNoop, fmt.Errorf("hashing file: %w", err)
		}
	}
	if doc != nil && doc.Hash == hash {
		idx.refreshDocumentStat(ctx, ref.Key, modTime, fileSize)
		return IndexNoop, nil
	}

	if !idx.paths.IsCurrent(ref.Key, version) {
		return IndexNoop, nil
	}

	text, err := idx.extractor.Extract(ctx, ref.AbsPath)
	if err != nil {
		if errors.Is(err, ErrOCRFailed) {
			return IndexNoop, err
		}
		return IndexNoop, fmt.Errorf("extracting text: %w", err)
	}

	if text == "" {
		return removeDocumentIfPresent(ctx, idx.store, doc, ref.Key)
	}

	chunks := ingest.PrepareChunks(text, ref.AbsPath, idx.cfg.ChunkSize, idx.cfg.ChunkOverlap)
	if len(chunks) == 0 {
		return removeDocumentIfPresent(ctx, idx.store, doc, ref.Key)
	}

	existingByContent, err := idx.store.GetDocumentChunksByPath(ctx, ref.Key)
	if err != nil {
		// A transient read failure here would silently drop the embedding
		// cache for the whole document and re-embed every chunk, so fail
		// the file and let the retry scheduler handle it.
		return IndexNoop, fmt.Errorf("loading existing chunks: %w", err)
	}

	chunkRecords, toEmbed, embedPositions, err := idx.getPipeline().DiffChunks(ctx, chunks, existingByContent)
	if err != nil {
		return IndexNoop, err
	}

	if err := idx.getPipeline().EmbedChunks(ctx, toEmbed, embedPositions, chunkRecords); err != nil {
		return IndexNoop, err
	}

	fileType, language := documentMetadata(ref.AbsPath)
	indexedDoc := &index.Document{
		Path:       ref.Key,
		Hash:       hash,
		ModifiedAt: modTime,
		FileSize:   fileSize,
		FileType:   fileType,
		Language:   language,
	}

	if err := idx.store.ReindexDocument(ctx, indexedDoc, chunkRecords); err != nil {
		return IndexNoop, err
	}

	return IndexUpdated, nil
}

func documentMetadata(path string) (fileType, language string) {
	fileType = index.DocumentFileType(path)
	if chunker := chunk.DefaultRegistry.Get(path); chunker != nil {
		name := chunker.Name()
		switch name {
		case "generic", "semantic":
		case "code":
			language = fileType
		default:
			language = name
		}
	}
	return fileType, language
}

func (idx *Indexer) shouldIgnorePath(path string) bool {
	if idx == nil || idx.cfg == nil {
		return false
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

// quarantinedKeys returns the current skip list as a set. A read failure yields
// an empty set, matching isQuarantined's fail-open behavior.
func (idx *Indexer) quarantinedKeys(ctx context.Context) map[string]bool {
	if idx.quarantine == nil {
		return nil
	}
	entries, err := idx.quarantine.ListQuarantined(ctx)
	if err != nil {
		logx.Warn("failed to load quarantine list", "err", err)
		return nil
	}
	keys := make(map[string]bool, len(entries))
	for _, entry := range entries {
		keys[entry.Path] = true
	}
	return keys
}

func (idx *Indexer) isQuarantined(ctx context.Context, key string) bool {
	if idx.quarantine == nil {
		return false
	}
	for {
		idx.quarantineMu.RLock()
		cache := idx.quarantineCache
		generation := idx.quarantineGen
		idx.quarantineMu.RUnlock()
		if cache != nil {
			return cache[key]
		}
		// Retry if invalidation races this load so even the current lookup cannot
		// act on a stale quarantine snapshot.
		entries, err := idx.quarantine.ListQuarantined(ctx)
		if err != nil {
			logx.Warn("failed to check quarantine status", "path", key, "err", err)
			return false
		}
		fresh := make(map[string]bool, len(entries))
		for _, entry := range entries {
			fresh[entry.Path] = true
		}
		idx.quarantineMu.Lock()
		if idx.quarantineGen == generation {
			idx.quarantineCache = fresh
			idx.quarantineMu.Unlock()
			return fresh[key]
		}
		idx.quarantineMu.Unlock()
		if err := ctx.Err(); err != nil {
			return false
		}
	}
}

func (idx *Indexer) invalidateQuarantineCache() {
	idx.quarantineMu.Lock()
	idx.quarantineCache = nil
	idx.quarantineGen++
	idx.quarantineMu.Unlock()
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
	return filepath.ToSlash(rel), nil
}

func SameModTime(a, b time.Time) bool {
	return normalizeModTime(a).UnixMicro() == normalizeModTime(b).UnixMicro()
}

func normalizeModTime(t time.Time) time.Time {
	return t.UTC().Round(0)
}

func (idx *Indexer) scheduleIndexRetryRef(ctx context.Context, ref DocumentRef, modTime time.Time, err error) {
	if idx.retries == nil {
		return
	}
	if !shouldRetryIndexError(err) {
		idx.retries.Clear(ref.Key)
		logx.Warn("not retrying path", "path", ref.AbsPath, "err", err)
		if shouldQuarantineIndexError(err) {
			idx.quarantineFailedRef(ctx, ref, err)
		}
		return
	}
	result := idx.retries.Schedule(ref.Key, modTime, func(retryModTime time.Time) {
		select {
		case <-ctx.Done():
			idx.retries.Clear(ref.Key)
			return
		default:
		}
		idx.enqueueLiveDocument(ctx, ref, retryModTime)
	})
	if result == RetryScheduleGaveUp && shouldQuarantineIndexError(err) {
		idx.quarantineFailedRef(ctx, ref, err)
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

func (idx *Indexer) quarantineFailedRef(ctx context.Context, ref DocumentRef, failure error) {
	if idx == nil || idx.cfg == nil || ref.Key == "" || failure == nil {
		return
	}
	if idx.quarantine == nil {
		logx.Warn("no quarantine store available; skipping quarantine", "path", ref.AbsPath)
		return
	}

	if err := idx.quarantine.AddToQuarantine(ctx, ref.Key, failure.Error()); err != nil {
		logx.Warn("adding path to quarantine failed", "path", ref.AbsPath, "err", err)
		return
	}
	idx.invalidateQuarantineCache()

	if delErr := idx.store.DeleteDocument(ctx, ref.Key); delErr != nil {
		logx.Warn("removing quarantined document from index failed", "path", ref.AbsPath, "err", delErr)
	}

	logx.Warn("quarantined path in skip list", "path", ref.AbsPath, "error", failure.Error())
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
