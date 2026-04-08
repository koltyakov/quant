package main

import (
	"context"
	"fmt"
	"log"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/andrew/quant/internal/chunk"
	"github.com/andrew/quant/internal/config"
	"github.com/andrew/quant/internal/embed"
	"github.com/andrew/quant/internal/extract"
	"github.com/andrew/quant/internal/index"
	"github.com/andrew/quant/internal/mcp"
	"github.com/andrew/quant/internal/scan"
	"github.com/andrew/quant/internal/watch"
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

func main() {
	cfg, err := config.Parse()
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		os.Exit(1)
	}

	ctx, cancel := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer cancel()

	embedder, err := embed.NewOllama(cfg.EmbedURL, cfg.EmbedModel)
	if err != nil {
		fmt.Fprintf(os.Stderr, "error connecting to ollama: %v\n", err)
		os.Exit(1)
	}
	defer func() {
		if err := embedder.Close(); err != nil {
			log.Printf("Error closing embedder: %v", err)
		}
	}()

	log.Printf("Connected to embedding backend via Ollama (model: %s, dimensions: %d)", cfg.EmbedModel, embedder.Dimensions())

	store, err := index.NewStore(cfg.DBPath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "error opening database: %v\n", err)
		os.Exit(1)
	}
	defer func() {
		if err := store.Close(); err != nil {
			log.Printf("Error closing store: %v", err)
		}
	}()

	log.Printf("Database opened: %s", cfg.DBPath)

	rebuild, err := store.EnsureEmbeddingMetadata(ctx, index.EmbeddingMetadata{
		Model:      cfg.EmbedModel,
		Dimensions: embedder.Dimensions(),
		Normalized: true,
	})
	if err != nil {
		fmt.Fprintf(os.Stderr, "error configuring embedding metadata: %v\n", err)
		os.Exit(1)
	}
	if rebuild {
		log.Printf("Embedding metadata changed; rebuilding index from filesystem projection")
	}

	gi, err := scan.LoadGitIgnore(cfg.WatchDir)
	if err != nil {
		fmt.Fprintf(os.Stderr, "error loading gitignore: %v\n", err)
		os.Exit(1)
	}

	idx := &indexer{
		cfg:        cfg,
		store:      store,
		embedder:   embedder,
		extractor:  extract.NewRouter(),
		pathStates: make(map[string]*pathState),
	}

	watcher, err := watch.New(cfg.WatchDir, gi)
	if err != nil {
		fmt.Fprintf(os.Stderr, "error starting watcher: %v\n", err)
		os.Exit(1)
	}
	defer func() {
		if err := watcher.Close(); err != nil {
			log.Printf("Error closing watcher: %v", err)
		}
	}()

	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		idx.watchLoop(ctx, watcher)
	}()

	wg.Add(1)
	go func() {
		defer wg.Done()
		idx.runInitialSync(ctx)
	}()

	mcpServer := mcp.NewServer(cfg, store, embedder)
	log.Printf("Starting MCP server (transport: %s)", cfg.Transport)

	if err := mcpServer.Serve(ctx, cfg); err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		cancel()
		wg.Wait()
		os.Exit(1)
	}

	cancel()
	wg.Wait()
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

	results, err := scan.Scan(idx.cfg.WatchDir, gi)
	if err != nil {
		return fmt.Errorf("scanning directory: %w", err)
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

	scannedPaths := make(map[string]bool, len(results))
	pending := make([]pendingItem, 0, len(results))
	for _, r := range results {
		key, err := documentKey(idx.cfg.WatchDir, r.Path)
		if err != nil {
			return fmt.Errorf("computing document key for %s: %w", r.Path, err)
		}
		scannedPaths[key] = true
		if !idx.extractor.Supports(r.Path) {
			continue
		}
		doc := docByPath[key]
		if doc != nil && sameModTime(doc.ModifiedAt, r.ModifiedAt) {
			continue
		}
		pending = append(pending, pendingItem{key: key, result: r, doc: doc})
	}

	type indexResult struct {
		path   string
		action indexAction
		err    error
	}

	workers := idx.cfg.IndexWorkers
	if workers > len(pending) && len(pending) > 0 {
		workers = len(pending)
	}
	if workers < 1 {
		workers = 1
	}

	jobs := make(chan pendingItem)
	indexResults := make(chan indexResult, workers)

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
		for _, item := range pending {
			select {
			case <-ctx.Done():
				close(jobs)
				wg.Wait()
				close(indexResults)
				return
			case jobs <- item:
			}
		}
		close(jobs)
		wg.Wait()
		close(indexResults)
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
	if err := ctx.Err(); err != nil {
		return err
	}

	for _, doc := range docs {
		if !scannedPaths[doc.Path] {
			absPath := filepath.Join(idx.cfg.WatchDir, doc.Path)
			if shouldIndex, err := idx.shouldIndexExistingPath(absPath); err != nil {
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
	if modTime == nil {
		return true
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
	if modTime != nil {
		effectiveModTime = *modTime
	}

	doc, err = idx.loadDocument(ctx, key, doc)
	if err != nil {
		return indexNoop, err
	}
	if doc != nil && sameModTime(doc.ModifiedAt, effectiveModTime) {
		return indexNoop, nil
	}

	return idx.indexFileCore(ctx, key, path, effectiveModTime, doc, version)
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

func (idx *indexer) shouldIndexExistingPath(path string) (bool, error) {
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
	if scan.IsHiddenName(filepath.Base(path)) || !idx.extractor.Supports(path) {
		return false, nil
	}

	rootIgnore, err := scan.LoadGitIgnore(idx.cfg.WatchDir)
	if err != nil {
		return false, fmt.Errorf("loading gitignore: %w", err)
	}
	matcher := scan.NewGitIgnoreMatcher(idx.cfg.WatchDir, rootIgnore)

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

			switch event.Op {
			case watch.Resync:
				idx.requestResync(ctx)

			case watch.Create, watch.Write:
				info, err := os.Stat(event.Path)
				if err != nil {
					if os.IsNotExist(err) {
						continue
					}
					log.Printf("Error stating %s: %v", event.Path, err)
					continue
				}
				if info.IsDir() {
					continue
				}

				action, err := idx.indexFile(ctx, event.Path, info.ModTime())
				if err != nil {
					log.Printf("Error indexing %s: %v", event.Path, err)
					continue
				}
				switch action {
				case indexUpdated:
					log.Printf("Indexed: %s", event.Path)
				case indexRemoved:
					log.Printf("Removed from index: %s", event.Path)
				}

			case watch.Remove:
				key, err := documentKey(idx.cfg.WatchDir, event.Path)
				if err != nil {
					log.Printf("Error removing %s: %v", event.Path, err)
					continue
				}
				if event.IsDir {
					idx.invalidatePrefix(key)
					if err := idx.store.DeleteDocumentsByPrefix(ctx, key); err != nil {
						log.Printf("Error removing directory %s: %v", event.Path, err)
						continue
					}
				} else {
					action, err := idx.syncDocument(ctx, key, event.Path, nil, nil)
					if err != nil {
						log.Printf("Error removing %s: %v", event.Path, err)
						continue
					}
					if action == indexNoop {
						continue
					}
				}
				log.Printf("Removed from index: %s", event.Path)
			}
		}
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
func (idx *indexer) indexFileCore(ctx context.Context, key, path string, modTime time.Time, doc *index.Document, version uint64) (indexAction, error) {
	hash, err := scan.FileHash(path)
	if err != nil {
		return indexNoop, fmt.Errorf("hashing file: %w", err)
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

		for i, c := range batch {
			if i >= len(embeddings) {
				break
			}
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
