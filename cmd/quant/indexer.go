package main

import (
	"context"
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
	"github.com/koltyakov/quant/internal/logx"
	"github.com/koltyakov/quant/internal/scan"
	"github.com/koltyakov/quant/internal/watch"
)

type indexAction string

const (
	indexNoop    indexAction = "noop"
	indexUpdated indexAction = "updated"
	indexRemoved indexAction = "removed"
)

type indexer struct {
	cfg       *config.Config
	store     *index.Store
	embedder  embed.Embedder
	extractor extract.Extractor

	paths   *pathSyncTracker
	live    *liveIndexQueue
	retries *retryScheduler

	resyncMu      sync.Mutex
	resyncRunning bool
	resyncPending bool
}

type pendingEmbed struct {
	chunkIdx int
	batchPos int
}

type syncReport struct {
	hadIndexFailures bool
}

var (
	indexRetryBaseDelay   = 2 * time.Second
	maxIndexRetryAttempts = 3
)

const (
	defaultEmbedBatchSize = 16
	liveQueueMultiplier   = 8
	minLiveQueueSize      = 16
	maxLiveQueueSize      = 512
)

func (idx *indexer) runInitialSync(ctx context.Context) {
	if !idx.beginResync() {
		return
	}
	idx.store.LoadHNSW()
	idx.checkHNSWStaleness(ctx)
	idx.runResyncLoop(ctx, true)
}

func (idx *indexer) checkHNSWStaleness(ctx context.Context) {
	if !idx.store.HNSWReady() {
		return
	}
	_, chunkCount, err := idx.store.Stats(ctx)
	if err != nil {
		logx.Warn("could not verify hnsw staleness", "err", err)
		return
	}
	hnswNodes := idx.store.HNSWLen()
	if hnswNodes != chunkCount {
		logx.Warn("hnsw graph stale, discarding for rebuild", "hnsw_nodes", hnswNodes, "db_chunks", chunkCount)
		idx.store.ResetHNSW()
	}
}

func (idx *indexer) requestResync(ctx context.Context) {
	if !idx.beginResync() {
		return
	}
	go idx.runResyncLoop(ctx, false)
}

func (idx *indexer) beginResync() bool {
	idx.resyncMu.Lock()
	defer idx.resyncMu.Unlock()

	if idx.resyncRunning {
		idx.resyncPending = true
		return false
	}
	idx.resyncRunning = true
	return true
}

func (idx *indexer) runResyncLoop(ctx context.Context, startup bool) {
	first := startup
	for {
		if first {
			logx.Info("starting initial scan", "watch_dir", idx.cfg.WatchDir)
		} else {
			logx.Info("starting filesystem resync", "watch_dir", idx.cfg.WatchDir)
		}

		report, err := idx.initialSyncWithReport(ctx)
		if err != nil {
			if ctx.Err() != nil {
				idx.finishResync(false)
				return
			}
			logx.Error("filesystem resync failed", "err", err)
		} else if first {
			docCount, chunkCount, err := idx.store.Stats(ctx)
			if err != nil {
				logx.Error("fetching index stats failed", "err", err)
			} else {
				logx.Info("initial scan complete", "documents", docCount, "chunks", chunkCount)
				if report.hadIndexFailures {
					logx.Warn("initial scan completed with indexing failures", "action", "keeping database backup until a clean rebuild succeeds")
				} else {
					idx.store.RemoveBackup()
				}
			}
			if !idx.store.HNSWReady() {
				go func() {
					if err := idx.store.BuildHNSW(ctx); err != nil {
						logx.Warn("hnsw build failed", "err", err)
					}
				}()
			}
		}

		first = false
		if !idx.finishResync(true) {
			return
		}
	}
}

func (idx *indexer) finishResync(retryAllowed bool) bool {
	idx.resyncMu.Lock()
	defer idx.resyncMu.Unlock()

	if retryAllowed && idx.resyncPending {
		idx.resyncPending = false
		return true
	}

	idx.resyncRunning = false
	idx.resyncPending = false
	return false
}

func (idx *indexer) initialSync(ctx context.Context) error {
	_, err := idx.initialSyncWithReport(ctx)
	return err
}

func (idx *indexer) initialSyncWithReport(ctx context.Context) (syncReport, error) {
	report := syncReport{}

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
		key, err := normalizeStoredDocumentPath(idx.cfg.WatchDir, docs[i].Path)
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
		action  indexAction
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
				action, err := idx.syncDocument(ctx, item.key, item.result.Path, &modTime, item.doc)
				indexResults <- indexResult{path: item.result.Path, modTime: modTime, action: action, err: err}
			}
		}()
	}

	go func() {
		err := scan.Walk(idx.cfg.WatchDir, gi, func(r scan.Result) error {
			if idx.shouldIgnorePath(r.Path) {
				return nil
			}
			key, err := documentKey(idx.cfg.WatchDir, r.Path)
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
			report.hadIndexFailures = true
			logx.Error("indexing failed", "path", result.path, "err", result.err)
			if idx.retries != nil {
				idx.retries.schedule(result.path, result.modTime, func(retryModTime time.Time) {
					select {
					case <-ctx.Done():
						idx.retries.clear(result.path)
						return
					default:
					}
					idx.enqueueLiveIndex(ctx, result.path, retryModTime)
				})
			}
			continue
		}
		if idx.retries != nil {
			idx.retries.clear(result.path)
		}
		switch result.action {
		case indexUpdated:
			logx.Info("indexed document", "path", result.path)
		case indexRemoved:
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

	reconcileMatcher, err := idx.newReconcileMatcher()
	if err != nil {
		return report, err
	}

	for _, doc := range docs {
		if !scannedPaths[doc.Path] {
			absPath := filepath.Join(idx.cfg.WatchDir, doc.Path)
			if shouldIndex, err := idx.shouldIndexExistingPath(reconcileMatcher, absPath); err != nil {
				logx.Error("reconciling stale document failed", "path", doc.Path, "err", err)
				continue
			} else if shouldIndex {
				action, err := idx.syncDocument(ctx, doc.Path, absPath, nil, &doc)
				if err != nil {
					report.hadIndexFailures = true
					logx.Error("reconciling existing document failed", "path", doc.Path, "err", err)
					continue
				}
				switch action {
				case indexUpdated:
					logx.Info("indexed document", "path", absPath)
				case indexRemoved:
					logx.Info("removed document from index", "path", absPath)
				}
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

func (idx *indexer) workerCount(maxPending int) int {
	workers := idx.cfg.IndexWorkers
	if workers < 1 {
		workers = 1
	}
	if maxPending > 0 && workers > maxPending {
		workers = maxPending
	}
	return workers
}

func (idx *indexer) liveQueueSize() int {
	return liveQueueSizeForWorkers(idx.cfg.IndexWorkers)
}

func liveQueueSizeForWorkers(workers int) int {
	if workers < 1 {
		workers = 1
	}
	size := workers * liveQueueMultiplier
	if size < minLiveQueueSize {
		size = minLiveQueueSize
	}
	if size > maxLiveQueueSize {
		size = maxLiveQueueSize
	}
	return size
}

func (idx *indexer) startLiveIndexWorkers(ctx context.Context, wg *sync.WaitGroup) {
	if idx.live == nil {
		idx.live = newLiveIndexQueue(idx.liveQueueSize())
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
				case path := <-idx.live.jobs:
					idx.processLiveIndexRequest(ctx, path)
				}
			}
		}()
	}
}

func (idx *indexer) enqueueLiveIndex(ctx context.Context, path string, modTime time.Time) bool {
	if idx.live == nil {
		idx.processLiveIndexRequestDirect(ctx, path, modTime)
		return true
	}

	if !idx.live.markPending(path, modTime) {
		return true
	}

	select {
	case idx.live.jobs <- path:
		return true
	default:
		idx.live.cancel(path)
		logx.Warn("live index queue full; scheduling resync", "path", path)
		idx.requestResync(ctx)
		return false
	}
}

func (idx *indexer) processLiveIndexRequest(ctx context.Context, path string) {
	modTime, ok := idx.live.startProcessing(path)
	if !ok {
		return
	}

	idx.processLiveIndexRequestDirect(ctx, path, modTime)

	if idx.live.finishProcessing(path) {
		select {
		case idx.live.jobs <- path:
		default:
			idx.live.cancel(path)
			logx.Warn("live index queue full; scheduling resync", "path", path)
			idx.requestResync(ctx)
		}
	}
}

func (idx *indexer) processLiveIndexRequestDirect(ctx context.Context, path string, modTime time.Time) {
	action, err := idx.indexFile(ctx, path, modTime)
	if err != nil {
		logx.Error("indexing failed", "path", path, "err", err)
		if idx.retries != nil {
			idx.retries.schedule(path, modTime, func(retryModTime time.Time) {
				select {
				case <-ctx.Done():
					idx.retries.clear(path)
					return
				default:
				}
				idx.enqueueLiveIndex(ctx, path, retryModTime)
			})
		}
	} else {
		if idx.retries != nil {
			idx.retries.clear(path)
		}
		switch action {
		case indexUpdated:
			logx.Info("indexed document", "path", path)
		case indexRemoved:
			logx.Info("removed document from index", "path", path)
		}
	}
}

func (idx *indexer) syncDocument(ctx context.Context, key, path string, modTime *time.Time, doc *index.Document) (indexAction, error) {
	version, started := idx.paths.begin(key, modTime)
	if !started {
		return indexNoop, nil
	}

	currentModTime := modTime
	currentDoc := doc
	currentVersion := version
	for {
		action, err := idx.syncDocumentOnce(ctx, key, path, currentModTime, currentDoc, currentVersion)
		nextVersion, rerun := idx.paths.finish(key)
		if !rerun {
			return action, err
		}
		currentModTime = nil
		currentDoc = nil
		currentVersion = nextVersion
	}
}

func (idx *indexer) syncDocumentOnce(ctx context.Context, key, path string, modTime *time.Time, doc *index.Document, version uint64) (indexAction, error) {
	info, err := os.Stat(path)
	if err != nil {
		if os.IsNotExist(err) {
			doc, err = idx.loadDocument(ctx, key, doc)
			if err != nil {
				return indexNoop, err
			}
			return removeDocumentIfPresent(ctx, idx.store, doc, key)
		}
		return indexNoop, fmt.Errorf("stating file: %w", err)
	}
	if info.IsDir() {
		return indexNoop, nil
	}

	if !idx.extractor.Supports(path) {
		doc, err = idx.loadDocument(ctx, key, doc)
		if err != nil {
			return indexNoop, err
		}
		return removeDocumentIfPresent(ctx, idx.store, doc, key)
	}

	effectiveModTime := info.ModTime()

	doc, err = idx.loadDocument(ctx, key, doc)
	if err != nil {
		return indexNoop, err
	}
	precomputedHash := ""
	if doc != nil && sameModTime(doc.ModifiedAt, effectiveModTime) {
		precomputedHash, err = scan.FileHash(path)
		if err != nil {
			return indexNoop, fmt.Errorf("hashing file: %w", err)
		}
		if doc.Hash == precomputedHash {
			return indexNoop, nil
		}
	}

	return idx.indexFileCore(ctx, key, path, effectiveModTime, precomputedHash, doc, version)
}

func (idx *indexer) loadDocument(ctx context.Context, key string, doc *index.Document) (*index.Document, error) {
	if doc != nil {
		return doc, nil
	}
	doc, err := idx.store.GetDocumentByPath(ctx, key)
	if err != nil {
		return nil, fmt.Errorf("loading indexed document: %w", err)
	}
	return doc, nil
}

func (idx *indexer) newReconcileMatcher() (*scan.GitIgnoreMatcher, error) {
	rootIgnore, err := scan.LoadGitIgnore(idx.cfg.WatchDir)
	if err != nil {
		return nil, fmt.Errorf("loading gitignore: %w", err)
	}
	return scan.NewGitIgnoreMatcher(idx.cfg.WatchDir, rootIgnore), nil
}

func (idx *indexer) shouldIndexExistingPath(matcher *scan.GitIgnoreMatcher, path string) (bool, error) {
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

func (idx *indexer) watchLoop(ctx context.Context, watcher *watch.Watcher) {
	for {
		select {
		case <-ctx.Done():
			return
		case event, ok := <-watcher.Events():
			if !ok {
				return
			}
			idx.handleWatchEvent(ctx, event)
		}
	}
}

func (idx *indexer) handleWatchEvent(ctx context.Context, event watch.Event) {
	if idx.shouldIgnorePath(event.Path) {
		return
	}

	switch event.Op {
	case watch.Resync:
		idx.requestResync(ctx)

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
		idx.enqueueLiveIndex(ctx, event.Path, info.ModTime())

	case watch.Remove:
		key, err := documentKey(idx.cfg.WatchDir, event.Path)
		if err != nil {
			logx.Error("removing document failed", "path", event.Path, "err", err)
			return
		}
		if event.IsDir {
			idx.paths.invalidatePrefix(key)
			if err := idx.store.DeleteDocumentsByPrefix(ctx, key); err != nil {
				logx.Error("removing directory from index failed", "path", event.Path, "err", err)
				return
			}
		} else {
			action, err := idx.syncDocument(ctx, key, event.Path, nil, nil)
			if err != nil {
				logx.Error("removing document failed", "path", event.Path, "err", err)
				return
			}
			if action == indexNoop {
				return
			}
		}
		logx.Info("removed document from index", "path", event.Path)
	}
}

func (idx *indexer) indexFile(ctx context.Context, path string, modTime time.Time) (indexAction, error) {
	key, err := documentKey(idx.cfg.WatchDir, path)
	if err != nil {
		return indexNoop, fmt.Errorf("computing document key: %w", err)
	}
	return idx.syncDocument(ctx, key, path, &modTime, nil)
}

func (idx *indexer) indexFileCore(ctx context.Context, key, path string, modTime time.Time, precomputedHash string, doc *index.Document, version uint64) (indexAction, error) {
	hash := precomputedHash
	if hash == "" {
		var err error
		hash, err = scan.FileHash(path)
		if err != nil {
			return indexNoop, fmt.Errorf("hashing file: %w", err)
		}
	}
	if doc != nil && doc.Hash == hash {
		return indexNoop, nil
	}

	if !idx.paths.isCurrent(key, version) {
		return indexNoop, nil
	}

	text, err := idx.extractor.Extract(ctx, path)
	if err != nil {
		return indexNoop, fmt.Errorf("extracting text: %w", err)
	}

	if text == "" {
		return removeDocumentIfPresent(ctx, idx.store, doc, key)
	}

	chunks := chunk.SplitWithPath(text, path, idx.cfg.ChunkSize, idx.cfg.ChunkOverlap)
	if len(chunks) == 0 {
		return removeDocumentIfPresent(ctx, idx.store, doc, key)
	}

	existingByContent, _ := idx.store.GetDocumentChunksByPath(ctx, key)

	chunkRecords, toEmbed, embedPositions, err := idx.diffChunks(chunks, existingByContent)
	if err != nil {
		return indexNoop, err
	}

	if err := idx.embedChunks(ctx, key, toEmbed, embedPositions, chunkRecords); err != nil {
		return indexNoop, err
	}

	indexedDoc := &index.Document{
		Path:       key,
		Hash:       hash,
		ModifiedAt: modTime,
	}

	if err := idx.store.ReindexDocument(ctx, indexedDoc, chunkRecords); err != nil {
		return indexNoop, err
	}

	return indexUpdated, nil
}

func (idx *indexer) diffChunks(chunks []chunk.Chunk, existingByContent map[string]index.ChunkRecord) (
	[]index.ChunkRecord, []chunk.Chunk, []pendingEmbed, error) {

	chunkRecords := make([]index.ChunkRecord, 0, len(chunks))
	var toEmbed []chunk.Chunk
	var embedPositions []pendingEmbed

	for i, c := range chunks {
		key := index.ChunkDiffKey(c.Content, c.Index)
		if existing, ok := existingByContent[key]; ok {
			chunkRecords = append(chunkRecords, index.ChunkRecord{
				Content:    c.Content,
				ChunkIndex: c.Index,
				Embedding:  existing.Embedding,
			})
		} else {
			embedPositions = append(embedPositions, pendingEmbed{chunkIdx: i, batchPos: len(toEmbed)})
			toEmbed = append(toEmbed, c)
			chunkRecords = append(chunkRecords, index.ChunkRecord{})
		}
	}
	return chunkRecords, toEmbed, embedPositions, nil
}

func (idx *indexer) embedChunks(ctx context.Context, docKey string, toEmbed []chunk.Chunk, embedPositions []pendingEmbed, chunkRecords []index.ChunkRecord) error {
	if len(toEmbed) == 0 {
		return nil
	}

	type batchResult struct {
		batchStart int
		embeddings [][]float32
		err        error
	}
	resultCh := make(chan batchResult, 2)

	go func() {
		defer close(resultCh)
		for batchStart := 0; batchStart < len(toEmbed); batchStart += defaultEmbedBatchSize {
			batchEnd := batchStart + defaultEmbedBatchSize
			if batchEnd > len(toEmbed) {
				batchEnd = len(toEmbed)
			}
			batch := toEmbed[batchStart:batchEnd]
			texts := make([]string, len(batch))
			for i, c := range batch {
				texts[i] = buildEmbedInput(docKey, c.Heading, c.Content)
			}
			embeddings, err := idx.embedder.EmbedBatch(ctx, texts)
			select {
			case <-ctx.Done():
				return
			case resultCh <- batchResult{batchStart: batchStart, embeddings: embeddings, err: err}:
			}
		}
	}()

	for result := range resultCh {
		if result.err != nil {
			return fmt.Errorf("embedding chunks from %d: %w", result.batchStart, result.err)
		}
		batchStart := result.batchStart
		batchEnd := batchStart + defaultEmbedBatchSize
		if batchEnd > len(toEmbed) {
			batchEnd = len(toEmbed)
		}
		batch := toEmbed[batchStart:batchEnd]
		if len(result.embeddings) != len(batch) {
			return fmt.Errorf(
				"embedding chunks %d-%d: embedder returned %d embeddings for %d chunks",
				batchStart, batchEnd-1, len(result.embeddings), len(batch),
			)
		}
		for i, c := range batch {
			globalIdx := embedPositions[batchStart+i].chunkIdx
			chunkRecords[globalIdx] = index.ChunkRecord{
				Content:    c.Content,
				ChunkIndex: c.Index,
				Embedding:  index.EncodeFloat32(index.NormalizeFloat32(result.embeddings[i])),
			}
		}
	}
	return ctx.Err()
}

func (idx *indexer) shouldIgnorePath(path string) bool {
	if idx == nil || idx.cfg == nil || idx.cfg.DBPath == "" {
		return false
	}
	return isCompanionLogPathForDB(idx.cfg.DBPath, path)
}

func buildEmbedInput(_, _ string, content string) string {
	return content
}

func documentKey(root, path string) (string, error) {
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

func normalizeStoredDocumentPath(root, storedPath string) (string, error) {
	return documentKey(root, filepath.Join(root, storedPath))
}

func sameModTime(a, b time.Time) bool {
	return normalizeModTime(a).UnixMicro() == normalizeModTime(b).UnixMicro()
}

func normalizeModTime(t time.Time) time.Time {
	return t.UTC().Round(0)
}

func removeDocumentIfPresent(ctx context.Context, store *index.Store, doc *index.Document, path string) (indexAction, error) {
	if doc == nil {
		return indexNoop, nil
	}
	if err := store.DeleteDocument(ctx, path); err != nil {
		return indexNoop, fmt.Errorf("deleting empty document: %w", err)
	}
	return indexRemoved, nil
}
