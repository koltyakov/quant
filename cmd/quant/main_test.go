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
