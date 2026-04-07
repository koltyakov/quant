package main

import (
	"context"
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
		Path:       path,
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

	cfg := &config.Config{ChunkSize: 128, ChunkOverlap: 0}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("unexpected error stating file: %v", err)
	}

	action, err := indexFile(ctx, cfg, store, fakeEmbedder{}, fakeExtractor{text: ""}, path, info.ModTime())
	if err != nil {
		t.Fatalf("unexpected error indexing file: %v", err)
	}
	if action != indexRemoved {
		t.Fatalf("expected removal action, got %s", action)
	}

	doc, err := store.GetDocumentByPath(ctx, path)
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
		Path:       path,
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

	cfg := &config.Config{ChunkSize: 128, ChunkOverlap: 0}
	embedder := &countingEmbedder{}
	extractor := &countingExtractor{text: "replacement chunk"}

	action, err := indexFile(ctx, cfg, store, embedder, extractor, path, info.ModTime())
	if err != nil {
		t.Fatalf("unexpected error indexing file: %v", err)
	}
	if action != indexNoop {
		t.Fatalf("expected noop action, got %s", action)
	}
	if extractor.calls.Load() != 0 {
		t.Fatalf("expected extractor to be skipped, got %d calls", extractor.calls.Load())
	}
	if embedder.batchCalls.Load() != 0 {
		t.Fatalf("expected embedder to be skipped, got %d batch calls", embedder.batchCalls.Load())
	}
}
