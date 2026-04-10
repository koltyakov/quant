package main

import (
	"context"
	"fmt"
	"log"
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

	liveJobs chan string

	liveMu     sync.Mutex
	liveStates map[string]*livePathState

	pathMu     sync.Mutex
	pathStates map[string]*pathState

	resyncMu      sync.Mutex
	resyncRunning bool
	resyncPending bool
}

type pathState struct {
	running bool
	dirty   bool
	version uint64

	hasModTime   bool
	requestedMod time.Time
}

type livePathState struct {
	modTime    time.Time
	hasPending bool
	queued     bool
	running    bool
}

func (idx *indexer) runInitialSync(ctx context.Context) {
	if !idx.beginResync() {
		return
	}
	idx.runResyncLoop(ctx, true)
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
			log.Printf("Starting initial scan of %s", idx.cfg.WatchDir)
		} else {
			log.Printf("Starting filesystem resync of %s", idx.cfg.WatchDir)
		}

		if err := idx.initialSync(ctx); err != nil {
			if ctx.Err() != nil {
				idx.finishResync(false)
				return
			}
			log.Printf("Error during resync: %v", err)
		} else if first {
			docCount, chunkCount, err := idx.store.Stats(ctx)
			if err != nil {
				log.Printf("Error fetching index stats: %v", err)
			} else {
				log.Printf("Initial scan complete: %d documents, %d chunks", docCount, chunkCount)
				idx.store.RemoveBackup()
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
	gi, err := scan.LoadGitIgnore(idx.cfg.WatchDir)
	if err != nil {
		return fmt.Errorf("loading gitignore: %w", err)
	}

	docs, err := idx.store.ListDocuments(ctx)
	if err != nil {
		return fmt.Errorf("listing indexed documents: %w", err)
	}
	docByPath := make(map[string]*index.Document, len(docs))
	for i := range docs {
		key, err := normalizeStoredDocumentPath(idx.cfg.WatchDir, docs[i].Path)
		if err != nil {
			continue
		}
		if key != docs[i].Path {
			if err := idx.store.RenameDocumentPath(ctx, docs[i].Path, key); err != nil {
				return fmt.Errorf("renaming indexed document %s to %s: %w", docs[i].Path, key, err)
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
		path   string
		action indexAction
		err    error
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
				indexResults <- indexResult{path: item.result.Path, action: action, err: err}
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
			log.Printf("Error indexing %s: %v", result.path, result.err)
			continue
		}
		switch result.action {
		case indexUpdated:
			log.Printf("Indexed: %s", result.path)
		case indexRemoved:
			log.Printf("Removed from index: %s", result.path)
		}
	}
	if walkErr := <-walkDone; walkErr != nil {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		return fmt.Errorf("scanning directory: %w", walkErr)
	}
	if err := ctx.Err(); err != nil {
		return err
	}

	reconcileMatcher, err := idx.newReconcileMatcher()
	if err != nil {
		return err
	}

	for _, doc := range docs {
		if !scannedPaths[doc.Path] {
			absPath := filepath.Join(idx.cfg.WatchDir, doc.Path)
			if shouldIndex, err := idx.shouldIndexExistingPath(reconcileMatcher, absPath); err != nil {
				log.Printf("Error reconciling stale document %s: %v", doc.Path, err)
				continue
			} else if shouldIndex {
				action, err := idx.syncDocument(ctx, doc.Path, absPath, nil, &doc)
				if err != nil {
					log.Printf("Error reconciling existing document %s: %v", doc.Path, err)
					continue
				}
				switch action {
				case indexUpdated:
					log.Printf("Indexed: %s", absPath)
				case indexRemoved:
					log.Printf("Removed from index: %s", absPath)
				}
				continue
			}
			if err := idx.store.DeleteDocument(ctx, doc.Path); err != nil {
				log.Printf("Error removing stale document %s: %v", doc.Path, err)
				continue
			}
			log.Printf("Removed from index: %s", doc.Path)
		}
	}

	return nil
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
	size := idx.workerCount(0) * 8
	if size < 16 {
		size = 16
	}
	if size > 512 {
		size = 512
	}
	return size
}

func (idx *indexer) startLiveIndexWorkers(ctx context.Context, wg *sync.WaitGroup) {
	if idx.liveJobs == nil {
		idx.liveJobs = make(chan string, idx.liveQueueSize())
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
				case path := <-idx.liveJobs:
					idx.processLiveIndexRequest(ctx, path)
				}
			}
		}()
	}
}

func (idx *indexer) enqueueLiveIndex(ctx context.Context, path string, modTime time.Time) bool {
	if idx.liveJobs == nil {
		idx.processLiveIndexRequest(ctx, path)
		return true
	}

	if !idx.markLivePending(path, modTime) {
		return true
	}

	select {
	case idx.liveJobs <- path:
		return true
	default:
		idx.cancelLiveQueue(path)
		log.Printf("warning: live index queue full for %s; scheduling resync", path)
		idx.requestResync(ctx)
		return false
	}
}

func (idx *indexer) markLivePending(path string, modTime time.Time) bool {
	idx.liveMu.Lock()
	defer idx.liveMu.Unlock()

	if idx.liveStates == nil {
		idx.liveStates = make(map[string]*livePathState)
	}

	state, ok := idx.liveStates[path]
	if !ok {
		state = &livePathState{}
		idx.liveStates[path] = state
	}
	if !state.hasPending || modTime.After(state.modTime) {
		state.modTime = modTime
	}
	state.hasPending = true
	if state.queued || state.running {
		return false
	}
	state.queued = true
	return true
}

func (idx *indexer) cancelLiveQueue(path string) {
	idx.liveMu.Lock()
	defer idx.liveMu.Unlock()

	state, ok := idx.liveStates[path]
	if !ok || !state.queued || state.running {
		return
	}
	state.queued = false
	if !state.running {
		delete(idx.liveStates, path)
	}
}

func (idx *indexer) startLiveProcessing(path string) (time.Time, bool) {
	idx.liveMu.Lock()
	defer idx.liveMu.Unlock()

	state, ok := idx.liveStates[path]
	if !ok || !state.queued {
		return time.Time{}, false
	}
	state.queued = false
	state.running = true
	modTime := state.modTime
	state.hasPending = false
	return modTime, true
}

func (idx *indexer) finishLiveProcessing(path string) bool {
	idx.liveMu.Lock()
	defer idx.liveMu.Unlock()

	state, ok := idx.liveStates[path]
	if !ok {
		return false
	}
	state.running = false
	if state.hasPending && !state.queued {
		state.queued = true
		return true
	}
	delete(idx.liveStates, path)
	return false
}

func (idx *indexer) processLiveIndexRequest(ctx context.Context, path string) {
	modTime, ok := idx.startLiveProcessing(path)
	if !ok {
		return
	}

	action, err := idx.indexFile(ctx, path, modTime)
	if err != nil {
		log.Printf("Error indexing %s: %v", path, err)
	} else {
		switch action {
		case indexUpdated:
			log.Printf("Indexed: %s", path)
		case indexRemoved:
			log.Printf("Removed from index: %s", path)
		}
	}

	if idx.finishLiveProcessing(path) {
		select {
		case idx.liveJobs <- path:
		default:
			idx.cancelLiveQueue(path)
			log.Printf("warning: live index queue full for %s; scheduling resync", path)
			idx.requestResync(ctx)
		}
	}
}

func (idx *indexer) beginPathSync(key string, modTime *time.Time) (uint64, bool) {
	idx.pathMu.Lock()
	defer idx.pathMu.Unlock()

	if idx.pathStates == nil {
		idx.pathStates = make(map[string]*pathState)
	}

	state, ok := idx.pathStates[key]
	if !ok {
		state = &pathState{}
		idx.pathStates[key] = state
	}
	if state.running {
		if idx.pathRequestInvalidates(state, modTime) {
			state.version++
			state.dirty = true
			state.hasModTime = modTime != nil
			if modTime != nil {
				state.requestedMod = *modTime
			}
		}
		return state.version, false
	}
	state.version++
	state.running = true
	state.hasModTime = modTime != nil
	if modTime != nil {
		state.requestedMod = *modTime
	}
	return state.version, true
}

func (idx *indexer) pathRequestInvalidates(state *pathState, modTime *time.Time) bool {
	if state == nil {
		return true
	}
	if modTime == nil {
		return state.hasModTime
	}
	if !state.hasModTime {
		return true
	}
	return !sameModTime(state.requestedMod, *modTime)
}

func (idx *indexer) finishPathSync(key string) (uint64, bool) {
	idx.pathMu.Lock()
	defer idx.pathMu.Unlock()

	state, ok := idx.pathStates[key]
	if !ok {
		return 0, false
	}
	if state.dirty {
		state.dirty = false
		return state.version, true
	}
	delete(idx.pathStates, key)
	return 0, false
}

func (idx *indexer) isCurrentPathGeneration(key string, version uint64) bool {
	idx.pathMu.Lock()
	defer idx.pathMu.Unlock()

	state, ok := idx.pathStates[key]
	return ok && state.running && state.version == version
}

func (idx *indexer) invalidatePrefix(prefix string) {
	idx.pathMu.Lock()
	defer idx.pathMu.Unlock()

	for key, state := range idx.pathStates {
		if key == prefix || strings.HasPrefix(key, prefix+string(filepath.Separator)) {
			state.version++
			state.dirty = true
		}
	}
}

func (idx *indexer) syncDocument(ctx context.Context, key, path string, modTime *time.Time, doc *index.Document) (indexAction, error) {
	version, started := idx.beginPathSync(key, modTime)
	if !started {
		return indexNoop, nil
	}

	currentModTime := modTime
	currentDoc := doc
	currentVersion := version
	for {
		action, err := idx.syncDocumentOnce(ctx, key, path, currentModTime, currentDoc, currentVersion)
		nextVersion, rerun := idx.finishPathSync(key)
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
	if scan.IsHiddenName(filepath.Base(path)) || !idx.extractor.Supports(path) {
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
			log.Printf("Error stating %s: %v", event.Path, err)
			return
		}
		if info.IsDir() {
			return
		}
		idx.enqueueLiveIndex(ctx, event.Path, info.ModTime())

	case watch.Remove:
		key, err := documentKey(idx.cfg.WatchDir, event.Path)
		if err != nil {
			log.Printf("Error removing %s: %v", event.Path, err)
			return
		}
		if event.IsDir {
			idx.invalidatePrefix(key)
			if err := idx.store.DeleteDocumentsByPrefix(ctx, key); err != nil {
				log.Printf("Error removing directory %s: %v", event.Path, err)
				return
			}
		} else {
			action, err := idx.syncDocument(ctx, key, event.Path, nil, nil)
			if err != nil {
				log.Printf("Error removing %s: %v", event.Path, err)
				return
			}
			if action == indexNoop {
				return
			}
		}
		log.Printf("Removed from index: %s", event.Path)
	}
}

// indexFile is the full entry point used by watchLoop. It checks whether the
// file type is supported, loads any existing document from the store, and
// short-circuits when the modification time is unchanged.
func (idx *indexer) indexFile(ctx context.Context, path string, modTime time.Time) (indexAction, error) {
	key, err := documentKey(idx.cfg.WatchDir, path)
	if err != nil {
		return indexNoop, fmt.Errorf("computing document key: %w", err)
	}
	return idx.syncDocument(ctx, key, path, &modTime, nil)
}

// indexFileCore performs the actual indexing work: hashing, extracting,
// chunking, embedding, and storing. It accepts an optional pre-loaded document
// so that callers who already have it (e.g. initialSync) can skip the extra
// database lookup.
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

	text, err := idx.extractor.Extract(ctx, path)
	if err != nil {
		return indexNoop, fmt.Errorf("extracting text: %w", err)
	}

	if text == "" {
		return removeDocumentIfPresent(ctx, idx.store, doc, key)
	}

	chunks := chunk.Split(text, idx.cfg.ChunkSize, idx.cfg.ChunkOverlap)
	if len(chunks) == 0 {
		return removeDocumentIfPresent(ctx, idx.store, doc, key)
	}

	if !idx.isCurrentPathGeneration(key, version) {
		return indexNoop, nil
	}

	indexedDoc := &index.Document{
		Path:       key,
		Hash:       hash,
		ModifiedAt: modTime,
	}

	const embedBatchSize = 16
	chunkRecords := make([]index.ChunkRecord, 0, len(chunks))

	for batchStart := 0; batchStart < len(chunks); batchStart += embedBatchSize {
		batchEnd := batchStart + embedBatchSize
		if batchEnd > len(chunks) {
			batchEnd = len(chunks)
		}

		batch := chunks[batchStart:batchEnd]
		texts := make([]string, len(batch))
		for i, c := range batch {
			texts[i] = c.Content
		}

		embeddings, err := idx.embedder.EmbedBatch(ctx, texts)
		if err != nil {
			return indexNoop, fmt.Errorf("embedding chunks %d-%d: %w", batchStart, batchEnd-1, err)
		}
		if len(embeddings) != len(batch) {
			return indexNoop, fmt.Errorf(
				"embedding chunks %d-%d: embedder returned %d embeddings for %d chunks",
				batchStart,
				batchEnd-1,
				len(embeddings),
				len(batch),
			)
		}

		for i, c := range batch {
			chunkRecords = append(chunkRecords, index.ChunkRecord{
				Content:    c.Content,
				ChunkIndex: c.Index,
				Embedding:  index.EncodeFloat32(index.NormalizeFloat32(embeddings[i])),
			})
		}
	}

	if err := idx.store.ReindexDocument(ctx, indexedDoc, chunkRecords); err != nil {
		return indexNoop, err
	}

	return indexUpdated, nil
}

func (idx *indexer) shouldIgnorePath(path string) bool {
	if idx == nil || idx.cfg == nil || idx.cfg.DBPath == "" {
		return false
	}
	return isCompanionLogPathForDB(idx.cfg.DBPath, path)
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
