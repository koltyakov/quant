package main

import (
	"context"
	"flag"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/andrew/quant/internal/config"
	"github.com/andrew/quant/internal/index"
	"github.com/andrew/quant/internal/scan"
	"github.com/andrew/quant/internal/watch"
)

type fakeEmbedder struct{}

func (f fakeEmbedder) Embed(context.Context, string) ([]float32, error) {
	return []float32{1}, nil
}

func (f fakeEmbedder) EmbedBatch(_ context.Context, texts []string) ([][]float32, error) {
	out := make([][]float32, len(texts))
	for i := range texts {
		out[i] = []float32{1}
	}
	return out, nil
}

func (f fakeEmbedder) Dimensions() int { return 1 }

func (f fakeEmbedder) Close() error { return nil }

type countingEmbedder struct {
	batchCalls atomic.Int32
}

func (f *countingEmbedder) Embed(context.Context, string) ([]float32, error) {
	return []float32{1}, nil
}

func (f *countingEmbedder) EmbedBatch(_ context.Context, texts []string) ([][]float32, error) {
	f.batchCalls.Add(1)
	out := make([][]float32, len(texts))
	for i := range texts {
		out[i] = []float32{1}
	}
	return out, nil
}

func (f *countingEmbedder) Dimensions() int { return 1 }

func (f *countingEmbedder) Close() error { return nil }

type fakeExtractor struct {
	text string
}

func (f fakeExtractor) Extract(context.Context, string) (string, error) {
	return f.text, nil
}

func (f fakeExtractor) Supports(path string) bool { return filepath.Ext(path) == ".txt" }

type fileExtractor struct{}

func (fileExtractor) Extract(_ context.Context, path string) (string, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return "", err
	}
	return string(data), nil
}

func (fileExtractor) Supports(path string) bool { return filepath.Ext(path) == ".txt" }

type countingExtractor struct {
	text  string
	calls atomic.Int32
}

func (f *countingExtractor) Extract(context.Context, string) (string, error) {
	f.calls.Add(1)
	return f.text, nil
}

func (f *countingExtractor) Supports(path string) bool { return filepath.Ext(path) == ".txt" }

type blockingExtractor struct {
	text    string
	started chan struct{}
	release chan struct{}
	calls   atomic.Int32
	once    sync.Once
}

func (f *blockingExtractor) Extract(context.Context, string) (string, error) {
	f.calls.Add(1)
	f.once.Do(func() { close(f.started) })
	<-f.release
	return f.text, nil
}

func (f *blockingExtractor) Supports(path string) bool { return filepath.Ext(path) == ".txt" }

type selectiveBlockingExtractor struct {
	texts       map[string]string
	blockPath   string
	started     chan struct{}
	release     chan struct{}
	blockedOnce sync.Once
}

func (f *selectiveBlockingExtractor) Extract(_ context.Context, path string) (string, error) {
	if path == f.blockPath {
		f.blockedOnce.Do(func() { close(f.started) })
		<-f.release
	}
	return f.texts[path], nil
}

func (f *selectiveBlockingExtractor) Supports(path string) bool { return filepath.Ext(path) == ".txt" }

func TestIndexFile_RemovesDocumentWhenExtractionIsEmpty(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "sample.txt")
	if err := os.WriteFile(path, []byte("new contents"), 0644); err != nil {
		t.Fatalf("unexpected error writing file: %v", err)
	}

	store, err := index.NewStore(filepath.Join(dir, "test.db"))
	if err != nil {
		t.Fatalf("unexpected error opening store: %v", err)
	}
	t.Cleanup(func() {
		if err := store.Close(); err != nil {
			t.Fatalf("unexpected close error: %v", err)
		}
	})

	ctx := context.Background()
	seeded := &index.Document{
		Path:       "sample.txt",
		Hash:       "stale-hash",
		ModifiedAt: time.Now().Add(-time.Hour),
	}
	if err := store.ReindexDocument(ctx, seeded, []index.ChunkRecord{{
		Content:    "stale chunk",
		ChunkIndex: 0,
		Embedding:  index.EncodeFloat32([]float32{1}),
	}}); err != nil {
		t.Fatalf("unexpected error seeding document: %v", err)
	}

	idx := &indexer{
		cfg:       &config.Config{WatchDir: dir, ChunkSize: 128, ChunkOverlap: 0},
		store:     store,
		embedder:  fakeEmbedder{},
		extractor: fakeExtractor{text: ""},
	}

	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("unexpected error stating file: %v", err)
	}

	action, err := idx.indexFile(ctx, path, info.ModTime())
	if err != nil {
		t.Fatalf("unexpected error indexing file: %v", err)
	}
	if action != indexRemoved {
		t.Fatalf("expected removal action, got %s", action)
	}

	doc, err := store.GetDocumentByPath(ctx, "sample.txt")
	if err != nil {
		t.Fatalf("unexpected error loading document: %v", err)
	}
	if doc != nil {
		t.Fatalf("expected document to be removed, got %+v", doc)
	}
}

func TestIndexFile_SkipsAlreadyIndexedDocumentWithSameModTime(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "sample.txt")
	if err := os.WriteFile(path, []byte("same contents"), 0644); err != nil {
		t.Fatalf("unexpected error writing file: %v", err)
	}

	modTime := time.Unix(1_700_000_000, 123_456_789)
	if err := os.Chtimes(path, modTime, modTime); err != nil {
		t.Fatalf("unexpected error setting file time: %v", err)
	}

	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("unexpected error stating file: %v", err)
	}

	hash, err := scan.FileHash(path)
	if err != nil {
		t.Fatalf("unexpected error hashing file: %v", err)
	}

	store, err := index.NewStore(filepath.Join(dir, "test.db"))
	if err != nil {
		t.Fatalf("unexpected error opening store: %v", err)
	}
	t.Cleanup(func() {
		if err := store.Close(); err != nil {
			t.Fatalf("unexpected close error: %v", err)
		}
	})

	ctx := context.Background()
	seeded := &index.Document{
		Path:       "sample.txt",
		Hash:       hash,
		ModifiedAt: info.ModTime().UTC().Truncate(time.Microsecond),
	}
	if err := store.ReindexDocument(ctx, seeded, []index.ChunkRecord{{
		Content:    "existing chunk",
		ChunkIndex: 0,
		Embedding:  index.EncodeFloat32([]float32{1}),
	}}); err != nil {
		t.Fatalf("unexpected error seeding document: %v", err)
	}

	emb := &countingEmbedder{}
	ext := &countingExtractor{text: "replacement chunk"}
	idx := &indexer{
		cfg:       &config.Config{WatchDir: dir, ChunkSize: 128, ChunkOverlap: 0},
		store:     store,
		embedder:  emb,
		extractor: ext,
	}

	action, err := idx.indexFile(ctx, path, info.ModTime())
	if err != nil {
		t.Fatalf("unexpected error indexing file: %v", err)
	}
	if action != indexNoop {
		t.Fatalf("expected noop action, got %s", action)
	}
	if ext.calls.Load() != 0 {
		t.Fatalf("expected extractor to be skipped, got %d calls", ext.calls.Load())
	}
	if emb.batchCalls.Load() != 0 {
		t.Fatalf("expected embedder to be skipped, got %d batch calls", emb.batchCalls.Load())
	}
}

func TestIndexFile_ReindexesSameModTimeContentChange(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "sample.txt")
	if err := os.WriteFile(path, []byte("first version"), 0644); err != nil {
		t.Fatalf("unexpected error writing file: %v", err)
	}

	modTime := time.Unix(1_700_000_000, 123_456_789)
	if err := os.Chtimes(path, modTime, modTime); err != nil {
		t.Fatalf("unexpected error setting file time: %v", err)
	}

	store, err := index.NewStore(filepath.Join(dir, "test.db"))
	if err != nil {
		t.Fatalf("unexpected error opening store: %v", err)
	}
	t.Cleanup(func() {
		if err := store.Close(); err != nil {
			t.Fatalf("unexpected close error: %v", err)
		}
	})

	idx := &indexer{
		cfg:       &config.Config{WatchDir: dir, ChunkSize: 128, ChunkOverlap: 0},
		store:     store,
		embedder:  fakeEmbedder{},
		extractor: fileExtractor{},
	}

	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("unexpected error stating file: %v", err)
	}

	action, err := idx.indexFile(context.Background(), path, info.ModTime())
	if err != nil {
		t.Fatalf("unexpected initial indexing error: %v", err)
	}
	if action != indexUpdated {
		t.Fatalf("expected initial update action, got %s", action)
	}

	if err := os.WriteFile(path, []byte("second version"), 0644); err != nil {
		t.Fatalf("unexpected error overwriting file: %v", err)
	}
	if err := os.Chtimes(path, modTime, modTime); err != nil {
		t.Fatalf("unexpected error restoring file time: %v", err)
	}

	info, err = os.Stat(path)
	if err != nil {
		t.Fatalf("unexpected error restating file: %v", err)
	}

	action, err = idx.indexFile(context.Background(), path, info.ModTime())
	if err != nil {
		t.Fatalf("unexpected reindex error: %v", err)
	}
	if action != indexUpdated {
		t.Fatalf("expected content change with same modtime to reindex, got %s", action)
	}

	doc, err := store.GetDocumentByPath(context.Background(), "sample.txt")
	if err != nil {
		t.Fatalf("unexpected error loading document: %v", err)
	}
	if doc == nil {
		t.Fatal("expected document to remain indexed")
	}

	wantHash, err := scan.FileHash(path)
	if err != nil {
		t.Fatalf("unexpected error hashing updated file: %v", err)
	}
	if doc.Hash != wantHash {
		t.Fatalf("expected updated hash %q, got %q", wantHash, doc.Hash)
	}

	results, err := store.Search(context.Background(), "second", index.NormalizeFloat32([]float32{1}), 1, "")
	if err != nil {
		t.Fatalf("unexpected error searching updated content: %v", err)
	}
	if len(results) != 1 || results[0].ChunkContent != "second version" {
		t.Fatalf("expected updated content to be searchable, got %+v", results)
	}
}

func TestIndexFile_StoresPathRelativeToWatchDir(t *testing.T) {
	dir := t.TempDir()
	subdir := filepath.Join(dir, "nested")
	if err := os.MkdirAll(subdir, 0755); err != nil {
		t.Fatalf("unexpected error creating subdir: %v", err)
	}
	path := filepath.Join(subdir, "sample.txt")
	if err := os.WriteFile(path, []byte("hello world"), 0644); err != nil {
		t.Fatalf("unexpected error writing file: %v", err)
	}

	store, err := index.NewStore(filepath.Join(dir, "test.db"))
	if err != nil {
		t.Fatalf("unexpected error opening store: %v", err)
	}
	t.Cleanup(func() {
		if err := store.Close(); err != nil {
			t.Fatalf("unexpected close error: %v", err)
		}
	})

	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("unexpected error stating file: %v", err)
	}

	idx := &indexer{
		cfg:       &config.Config{WatchDir: dir, ChunkSize: 128, ChunkOverlap: 0},
		store:     store,
		embedder:  fakeEmbedder{},
		extractor: fakeExtractor{text: "hello world"},
	}

	action, err := idx.indexFile(context.Background(), path, info.ModTime())
	if err != nil {
		t.Fatalf("unexpected error indexing file: %v", err)
	}
	if action != indexUpdated {
		t.Fatalf("expected update action, got %s", action)
	}

	docs, err := store.ListDocuments(context.Background())
	if err != nil {
		t.Fatalf("unexpected error listing documents: %v", err)
	}
	if len(docs) != 1 {
		t.Fatalf("expected 1 document, got %d", len(docs))
	}
	if docs[0].Path != filepath.Join("nested", "sample.txt") {
		t.Fatalf("expected relative path, got %q", docs[0].Path)
	}
}

func TestIndexFile_CoalescesConcurrentRequests(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "sample.txt")
	if err := os.WriteFile(path, []byte("hello world"), 0644); err != nil {
		t.Fatalf("unexpected error writing file: %v", err)
	}

	store, err := index.NewStore(filepath.Join(dir, "test.db"))
	if err != nil {
		t.Fatalf("unexpected error opening store: %v", err)
	}
	t.Cleanup(func() {
		if err := store.Close(); err != nil {
			t.Fatalf("unexpected close error: %v", err)
		}
	})

	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("unexpected error stating file: %v", err)
	}

	emb := &countingEmbedder{}
	ext := &blockingExtractor{
		text:    "hello world",
		started: make(chan struct{}),
		release: make(chan struct{}),
	}
	idx := &indexer{
		cfg:        &config.Config{WatchDir: dir, ChunkSize: 128, ChunkOverlap: 0},
		store:      store,
		embedder:   emb,
		extractor:  ext,
		pathStates: make(map[string]*pathState),
	}

	errCh := make(chan error, 2)
	go func() {
		_, err := idx.indexFile(context.Background(), path, info.ModTime())
		errCh <- err
	}()

	select {
	case <-ext.started:
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for first extraction to start")
	}

	go func() {
		_, err := idx.indexFile(context.Background(), path, info.ModTime())
		errCh <- err
	}()

	close(ext.release)

	for i := 0; i < 2; i++ {
		if err := <-errCh; err != nil {
			t.Fatalf("unexpected indexing error: %v", err)
		}
	}

	if ext.calls.Load() != 1 {
		t.Fatalf("expected one extraction, got %d", ext.calls.Load())
	}
	if emb.batchCalls.Load() != 1 {
		t.Fatalf("expected one embedding batch call, got %d", emb.batchCalls.Load())
	}
}

func TestHandleWatchEvent_QueuesLiveIndexWork(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "sample.txt")
	if err := os.WriteFile(path, []byte("hello world"), 0644); err != nil {
		t.Fatalf("unexpected error writing file: %v", err)
	}

	store, err := index.NewStore(filepath.Join(dir, "test.db"))
	if err != nil {
		t.Fatalf("unexpected error opening store: %v", err)
	}
	t.Cleanup(func() {
		if err := store.Close(); err != nil {
			t.Fatalf("unexpected close error: %v", err)
		}
	})

	ext := &blockingExtractor{
		text:    "hello world",
		started: make(chan struct{}),
		release: make(chan struct{}),
	}
	idx := &indexer{
		cfg:        &config.Config{WatchDir: dir, ChunkSize: 128, ChunkOverlap: 0, IndexWorkers: 1},
		store:      store,
		embedder:   fakeEmbedder{},
		extractor:  ext,
		pathStates: make(map[string]*pathState),
	}

	ctx, cancel := context.WithCancel(context.Background())
	var wg sync.WaitGroup
	idx.startLiveIndexWorkers(ctx, &wg)
	t.Cleanup(func() {
		close(ext.release)
		cancel()
		wg.Wait()
	})

	start := time.Now()
	idx.handleWatchEvent(ctx, watch.Event{Path: path, Op: watch.Write})
	if elapsed := time.Since(start); elapsed > 100*time.Millisecond {
		t.Fatalf("expected watch event handling to enqueue quickly, took %s", elapsed)
	}

	select {
	case <-ext.started:
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for live worker to start extraction")
	}
}

func TestEnqueueLiveIndex_CoalescesByPath(t *testing.T) {
	idx := &indexer{
		cfg:        &config.Config{IndexWorkers: 1},
		liveJobs:   make(chan string, 4),
		liveStates: make(map[string]*livePathState),
	}

	first := time.Unix(100, 0)
	second := time.Unix(200, 0)
	if !idx.enqueueLiveIndex(context.Background(), "a.txt", first) {
		t.Fatal("expected first enqueue to succeed")
	}
	if !idx.enqueueLiveIndex(context.Background(), "a.txt", second) {
		t.Fatal("expected second enqueue to be coalesced")
	}
	if got := len(idx.liveJobs); got != 1 {
		t.Fatalf("expected one queued path, got %d", got)
	}

	path := <-idx.liveJobs
	if path != "a.txt" {
		t.Fatalf("expected queued path a.txt, got %s", path)
	}
	modTime, ok := idx.startLiveProcessing(path)
	if !ok {
		t.Fatal("expected live processing to start")
	}
	if !modTime.Equal(second) {
		t.Fatalf("expected latest modtime %v, got %v", second, modTime)
	}
}

func TestInitialSync_ReloadsRootGitIgnore(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "sample.txt")
	if err := os.WriteFile(path, []byte("hello world"), 0644); err != nil {
		t.Fatalf("unexpected error writing file: %v", err)
	}

	store, err := index.NewStore(filepath.Join(dir, "test.db"))
	if err != nil {
		t.Fatalf("unexpected error opening store: %v", err)
	}
	t.Cleanup(func() {
		if err := store.Close(); err != nil {
			t.Fatalf("unexpected close error: %v", err)
		}
	})

	idx := &indexer{
		cfg:        &config.Config{WatchDir: dir, ChunkSize: 128, ChunkOverlap: 0, IndexWorkers: 1},
		store:      store,
		embedder:   fakeEmbedder{},
		extractor:  fakeExtractor{text: "hello world"},
		pathStates: make(map[string]*pathState),
	}

	if err := idx.initialSync(context.Background()); err != nil {
		t.Fatalf("unexpected initial sync error: %v", err)
	}

	docs, err := store.ListDocuments(context.Background())
	if err != nil {
		t.Fatalf("unexpected list error: %v", err)
	}
	if len(docs) != 1 {
		t.Fatalf("expected 1 document after initial sync, got %d", len(docs))
	}

	if err := os.WriteFile(filepath.Join(dir, ".gitignore"), []byte("*.txt\n"), 0644); err != nil {
		t.Fatalf("unexpected gitignore write error: %v", err)
	}
	if err := idx.initialSync(context.Background()); err != nil {
		t.Fatalf("unexpected resync error: %v", err)
	}

	docs, err = store.ListDocuments(context.Background())
	if err != nil {
		t.Fatalf("unexpected list error: %v", err)
	}
	if len(docs) != 0 {
		t.Fatalf("expected ignored document to be removed, got %d docs", len(docs))
	}
}

func TestIndexFile_RemoveDuringInFlightIndexDoesNotResurrectDocument(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "sample.txt")
	if err := os.WriteFile(path, []byte("hello world"), 0644); err != nil {
		t.Fatalf("unexpected error writing file: %v", err)
	}

	store, err := index.NewStore(filepath.Join(dir, "test.db"))
	if err != nil {
		t.Fatalf("unexpected error opening store: %v", err)
	}
	t.Cleanup(func() {
		if err := store.Close(); err != nil {
			t.Fatalf("unexpected close error: %v", err)
		}
	})

	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("unexpected error stating file: %v", err)
	}

	ext := &blockingExtractor{
		text:    "hello world",
		started: make(chan struct{}),
		release: make(chan struct{}),
	}
	idx := &indexer{
		cfg:        &config.Config{WatchDir: dir, ChunkSize: 128, ChunkOverlap: 0},
		store:      store,
		embedder:   fakeEmbedder{},
		extractor:  ext,
		pathStates: make(map[string]*pathState),
	}

	indexErrCh := make(chan error, 1)
	go func() {
		_, err := idx.indexFile(context.Background(), path, info.ModTime())
		indexErrCh <- err
	}()

	select {
	case <-ext.started:
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for extraction to start")
	}

	if err := os.Remove(path); err != nil {
		t.Fatalf("unexpected remove error: %v", err)
	}

	removeErrCh := make(chan error, 1)
	go func() {
		_, err := idx.syncDocument(context.Background(), "sample.txt", path, nil, nil)
		removeErrCh <- err
	}()

	close(ext.release)

	if err := <-indexErrCh; err != nil {
		t.Fatalf("unexpected indexing error: %v", err)
	}
	if err := <-removeErrCh; err != nil {
		t.Fatalf("unexpected remove sync error: %v", err)
	}

	doc, err := store.GetDocumentByPath(context.Background(), "sample.txt")
	if err != nil {
		t.Fatalf("unexpected load error: %v", err)
	}
	if doc != nil {
		t.Fatalf("expected document to stay deleted, got %+v", doc)
	}
}

func TestInitialSync_ReconcilesFileRecreatedDuringScanWindow(t *testing.T) {
	dir := t.TempDir()
	blockerPath := filepath.Join(dir, "blocker.txt")
	recreatedPath := filepath.Join(dir, "recreated.txt")
	if err := os.WriteFile(blockerPath, []byte("blocker"), 0644); err != nil {
		t.Fatalf("unexpected blocker write error: %v", err)
	}

	store, err := index.NewStore(filepath.Join(dir, "test.db"))
	if err != nil {
		t.Fatalf("unexpected error opening store: %v", err)
	}
	t.Cleanup(func() {
		if err := store.Close(); err != nil {
			t.Fatalf("unexpected close error: %v", err)
		}
	})

	if err := store.ReindexDocument(context.Background(), &index.Document{
		Path:       "recreated.txt",
		Hash:       "old-hash",
		ModifiedAt: time.Now().Add(-time.Hour),
	}, []index.ChunkRecord{{
		Content:    "stale chunk",
		ChunkIndex: 0,
		Embedding:  index.EncodeFloat32([]float32{1}),
	}}); err != nil {
		t.Fatalf("unexpected seed error: %v", err)
	}

	ext := &selectiveBlockingExtractor{
		texts: map[string]string{
			blockerPath:   "blocker contents",
			recreatedPath: "recreated contents",
		},
		blockPath: blockerPath,
		started:   make(chan struct{}),
		release:   make(chan struct{}),
	}
	idx := &indexer{
		cfg:        &config.Config{WatchDir: dir, ChunkSize: 128, ChunkOverlap: 0, IndexWorkers: 1},
		store:      store,
		embedder:   fakeEmbedder{},
		extractor:  ext,
		pathStates: make(map[string]*pathState),
	}

	errCh := make(chan error, 1)
	go func() {
		errCh <- idx.initialSync(context.Background())
	}()

	select {
	case <-ext.started:
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for blocking extraction")
	}

	if err := os.WriteFile(recreatedPath, []byte("recreated"), 0644); err != nil {
		t.Fatalf("unexpected recreated write error: %v", err)
	}

	close(ext.release)

	if err := <-errCh; err != nil {
		t.Fatalf("unexpected initial sync error: %v", err)
	}

	docs, err := store.ListDocuments(context.Background())
	if err != nil {
		t.Fatalf("unexpected list error: %v", err)
	}
	if len(docs) != 2 {
		t.Fatalf("expected both documents to remain indexed, got %d", len(docs))
	}

	recreatedDoc, err := store.GetDocumentByPath(context.Background(), "recreated.txt")
	if err != nil {
		t.Fatalf("unexpected recreated load error: %v", err)
	}
	if recreatedDoc == nil {
		t.Fatal("expected recreated document to be indexed")
	}
}

func TestInitialSync_MigratesStoredPathAndSkipsReindex(t *testing.T) {
	dir := t.TempDir()
	subdir := filepath.Join(dir, "nested")
	if err := os.MkdirAll(subdir, 0755); err != nil {
		t.Fatalf("unexpected error creating subdir: %v", err)
	}
	path := filepath.Join(subdir, "sample.txt")
	if err := os.WriteFile(path, []byte("hello world"), 0644); err != nil {
		t.Fatalf("unexpected error writing file: %v", err)
	}

	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("unexpected error stating file: %v", err)
	}
	hash, err := scan.FileHash(path)
	if err != nil {
		t.Fatalf("unexpected error hashing file: %v", err)
	}

	store, err := index.NewStore(filepath.Join(dir, "test.db"))
	if err != nil {
		t.Fatalf("unexpected error opening store: %v", err)
	}
	t.Cleanup(func() {
		if err := store.Close(); err != nil {
			t.Fatalf("unexpected close error: %v", err)
		}
	})

	oldStoredPath := filepath.Join("..", filepath.Base(dir), "nested", "sample.txt")
	if err := store.ReindexDocument(context.Background(), &index.Document{
		Path:       oldStoredPath,
		Hash:       hash,
		ModifiedAt: info.ModTime().UTC().Truncate(time.Microsecond),
	}, []index.ChunkRecord{{
		Content:    "existing chunk",
		ChunkIndex: 0,
		Embedding:  index.EncodeFloat32([]float32{1}),
	}}); err != nil {
		t.Fatalf("unexpected error seeding document: %v", err)
	}

	emb := &countingEmbedder{}
	ext := &countingExtractor{text: "replacement chunk"}
	idx := &indexer{
		cfg:       &config.Config{WatchDir: dir, ChunkSize: 128, ChunkOverlap: 0, IndexWorkers: 2},
		store:     store,
		embedder:  emb,
		extractor: ext,
	}

	if err := idx.initialSync(context.Background()); err != nil {
		t.Fatalf("unexpected initial sync error: %v", err)
	}
	if ext.calls.Load() != 0 {
		t.Fatalf("expected extractor to be skipped, got %d calls", ext.calls.Load())
	}
	if emb.batchCalls.Load() != 0 {
		t.Fatalf("expected embedder to be skipped, got %d batch calls", emb.batchCalls.Load())
	}

	docs, err := store.ListDocuments(context.Background())
	if err != nil {
		t.Fatalf("unexpected error listing documents: %v", err)
	}
	if len(docs) != 1 {
		t.Fatalf("expected 1 document, got %d", len(docs))
	}
	if docs[0].Path != filepath.Join("nested", "sample.txt") {
		t.Fatalf("expected migrated relative path, got %q", docs[0].Path)
	}
}

func TestDocumentKey(t *testing.T) {
	root := filepath.Join(string(filepath.Separator), "tmp", "root")
	got, err := documentKey(root, filepath.Join(root, "nested", "file.txt"))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got != filepath.Join("nested", "file.txt") {
		t.Fatalf("unexpected key %q", got)
	}
}

func TestDocumentKey_RejectsOutsideRoot(t *testing.T) {
	root := filepath.Join(string(filepath.Separator), "tmp", "root")
	_, err := documentKey(root, filepath.Join(string(filepath.Separator), "tmp", "other", "file.txt"))
	if err == nil {
		t.Fatal("expected error for path outside root")
	}
}

func TestConfigParse_DefaultsDirToCurrentFolder(t *testing.T) {
	oldArgs := os.Args
	oldCommandLine := flag.CommandLine
	oldWD, err := os.Getwd()
	if err != nil {
		t.Fatalf("unexpected getwd error: %v", err)
	}
	t.Cleanup(func() {
		os.Args = oldArgs
		flag.CommandLine = oldCommandLine
		if err := os.Chdir(oldWD); err != nil {
			t.Fatalf("unexpected restore chdir error: %v", err)
		}
	})

	dir := t.TempDir()
	if err := os.Chdir(dir); err != nil {
		t.Fatalf("unexpected chdir error: %v", err)
	}
	expectedDir, err := os.Getwd()
	if err != nil {
		t.Fatalf("unexpected getwd error after chdir: %v", err)
	}

	flag.CommandLine = flag.NewFlagSet(os.Args[0], flag.ContinueOnError)
	os.Args = []string{"quant"}

	cfg, err := config.Parse()
	if err != nil {
		t.Fatalf("unexpected parse error: %v", err)
	}
	if cfg.WatchDir != expectedDir {
		t.Fatalf("expected watch dir %q, got %q", expectedDir, cfg.WatchDir)
	}
	expectedDBPath := filepath.Join(expectedDir, "quant.db")
	if cfg.DBPath != expectedDBPath {
		t.Fatalf("expected db path %q, got %q", expectedDBPath, cfg.DBPath)
	}
}
