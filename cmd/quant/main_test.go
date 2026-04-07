package main

import (
	"context"
	"flag"
	"os"
	"path/filepath"
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

func (f fakeExtractor) Supports(string) bool { return true }

type countingExtractor struct {
	text  string
	calls atomic.Int32
}

func (f *countingExtractor) Extract(context.Context, string) (string, error) {
	f.calls.Add(1)
	return f.text, nil
}

func (f *countingExtractor) Supports(string) bool { return true }

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

	flag.CommandLine = flag.NewFlagSet(os.Args[0], flag.ContinueOnError)
	os.Args = []string{"quant"}

	cfg, err := config.Parse()
	if err != nil {
		t.Fatalf("unexpected parse error: %v", err)
	}
	if cfg.WatchDir != dir {
		t.Fatalf("expected watch dir %q, got %q", dir, cfg.WatchDir)
	}
	if cfg.DBPath != filepath.Join(dir, ".quant.db") {
		t.Fatalf("expected db path %q, got %q", filepath.Join(dir, ".quant.db"), cfg.DBPath)
	}
}
