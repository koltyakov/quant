package index

import (
	"context"
	"path/filepath"
	"testing"
	"time"
)

// seedSimilarChunks indexes three single-chunk documents whose embeddings sit
// at known angles from each other, and returns the chunk ID of the seed.
func seedSimilarChunks(t *testing.T, store *Store) int64 {
	t.Helper()
	ctx := context.Background()

	if _, err := store.EnsureEmbeddingMetadata(ctx, EmbeddingMetadata{Model: "test", Dimensions: 2, Normalized: true}); err != nil {
		t.Fatalf("EnsureEmbeddingMetadata() error: %v", err)
	}

	docs := []struct {
		path    string
		content string
		vec     []float32
	}{
		{"sim/seed.txt", "seed chunk", []float32{1, 0}},
		{"sim/near.txt", "near chunk", []float32{0.9, 0.1}},
		{"sim/far.txt", "far chunk", []float32{0, 1}},
	}
	for _, d := range docs {
		doc := &Document{Path: d.path, Hash: d.path, ModifiedAt: time.Now()}
		if err := store.ReindexDocument(ctx, doc, []ChunkRecord{{
			Content:    d.content,
			ChunkIndex: 0,
			Embedding:  EncodeFloat32(NormalizeFloat32(d.vec)),
		}}); err != nil {
			t.Fatalf("ReindexDocument(%s) error: %v", d.path, err)
		}
	}

	chunks, err := store.GetDocumentChunksByPath(ctx, "sim/seed.txt")
	if err != nil {
		t.Fatalf("GetDocumentChunksByPath() error: %v", err)
	}
	if len(chunks) != 1 {
		t.Fatalf("seed document has %d chunks, want 1", len(chunks))
	}
	for _, c := range chunks {
		return c.ID
	}
	return 0
}

// FindSimilar must keep working when the HNSW graph is not ready - at startup
// the graph is still building, and it is absent entirely in keyword-only mode.
// Without the brute-force fallback this silently returned zero results.
func TestFindSimilar_FallsBackWhenHNSWNotReady(t *testing.T) {
	dir := t.TempDir()
	store, err := NewStore(filepath.Join(dir, "quant.db"))
	if err != nil {
		t.Fatalf("NewStore() error: %v", err)
	}
	mustCloseStore(t, store)

	seedID := seedSimilarChunks(t, store)

	if store.hnsw != nil && store.hnsw.ready.Load() {
		t.Fatal("precondition failed: hnsw is ready without BuildHNSW")
	}

	results, err := store.FindSimilar(context.Background(), seedID, 5)
	if err != nil {
		t.Fatalf("FindSimilar() error: %v", err)
	}
	if len(results) == 0 {
		t.Fatal("FindSimilar() returned no results with hnsw unavailable; brute-force fallback did not run")
	}
	for _, r := range results {
		if r.ChunkID == seedID {
			t.Error("FindSimilar() returned the seed chunk itself")
		}
	}
	if results[0].DocumentPath != "sim/near.txt" {
		t.Errorf("FindSimilar() nearest = %q, want sim/near.txt", results[0].DocumentPath)
	}
	if results[0].ChunkContent == "" {
		t.Error("FindSimilar() result content was not hydrated")
	}
}

// The fallback shares the search budget: a zero candidate ceiling disables the
// linear scan rather than letting it run unbounded on a large index.
func TestFindSimilar_FallbackRespectsCandidateBudget(t *testing.T) {
	dir := t.TempDir()
	store, err := NewStore(filepath.Join(dir, "quant.db"))
	if err != nil {
		t.Fatalf("NewStore() error: %v", err)
	}
	mustCloseStore(t, store)

	seedID := seedSimilarChunks(t, store)
	store.SetMaxVectorSearchCandidates(0)

	results, err := store.FindSimilar(context.Background(), seedID, 5)
	if err != nil {
		t.Fatalf("FindSimilar() error: %v", err)
	}
	if len(results) != 0 {
		t.Fatalf("FindSimilar() returned %d results, want 0 when the candidate budget is exhausted", len(results))
	}
}

// With the graph built, results must match what the fallback produces.
func TestFindSimilar_HNSWAndFallbackAgree(t *testing.T) {
	dir := t.TempDir()
	store, err := NewStore(filepath.Join(dir, "quant.db"))
	if err != nil {
		t.Fatalf("NewStore() error: %v", err)
	}
	mustCloseStore(t, store)

	ctx := context.Background()
	seedID := seedSimilarChunks(t, store)

	fallback, err := store.FindSimilar(ctx, seedID, 5)
	if err != nil {
		t.Fatalf("FindSimilar() fallback error: %v", err)
	}

	if err := store.BuildHNSW(ctx); err != nil {
		t.Fatalf("BuildHNSW() error: %v", err)
	}
	if !store.hnsw.ready.Load() {
		t.Fatal("hnsw not ready after BuildHNSW")
	}

	graphed, err := store.FindSimilar(ctx, seedID, 5)
	if err != nil {
		t.Fatalf("FindSimilar() hnsw error: %v", err)
	}

	if len(graphed) != len(fallback) {
		t.Fatalf("result count: hnsw = %d, fallback = %d", len(graphed), len(fallback))
	}
	for i := range graphed {
		if graphed[i].ChunkID != fallback[i].ChunkID {
			t.Errorf("result %d: hnsw chunk %d, fallback chunk %d", i, graphed[i].ChunkID, fallback[i].ChunkID)
		}
	}
}
