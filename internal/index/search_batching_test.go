package index

import (
	"context"
	"path/filepath"
	"testing"
	"time"
)

// sqliteMaxVariables is the hard limit modernc.org/sqlite enforces on bound
// variables in a single statement. Any generated `IN (?, ...)` list longer than
// this fails the statement outright, so id lists must be batched.
const sqliteMaxVariables = 32766

func TestSqlPlaceholders(t *testing.T) {
	for _, tc := range []struct {
		n    int
		want string
	}{
		{-1, ""},
		{0, ""},
		{1, "?"},
		{3, "?,?,?"},
	} {
		if got := sqlPlaceholders(tc.n); got != tc.want {
			t.Fatalf("sqlPlaceholders(%d) = %q, want %q", tc.n, got, tc.want)
		}
	}
}

// TestFilterChunkIDs_ExceedsSQLVariableLimit covers an id list larger than
// SQLite's bound-variable ceiling. Before batching, the generated statement was
// rejected with "too many SQL variables" and filtered vector search silently
// degraded to keyword-only results.
func TestFilterChunkIDs_ExceedsSQLVariableLimit(t *testing.T) {
	dir := t.TempDir()
	store, err := NewStore(filepath.Join(dir, "quant.db"))
	if err != nil {
		t.Fatalf("NewStore() error: %v", err)
	}
	mustCloseStore(t, store)

	ctx := context.Background()
	doc := &Document{Path: "docs/a.md", Hash: "h1", ModifiedAt: time.Now()}
	if err := store.ReindexDocument(ctx, doc, []ChunkRecord{{
		Content:    "batched content",
		ChunkIndex: 0,
		Embedding:  EncodeFloat32([]float32{1, 0}),
	}}); err != nil {
		t.Fatalf("ReindexDocument() error: %v", err)
	}

	var storedID int
	if err := store.db.QueryRowContext(ctx, `SELECT id FROM chunks LIMIT 1`).Scan(&storedID); err != nil {
		t.Fatalf("reading stored chunk id: %v", err)
	}

	ids := make([]int, 0, sqliteMaxVariables+10)
	ids = append(ids, storedID)
	for i := range sqliteMaxVariables + 9 {
		ids = append(ids, storedID+i+1)
	}

	got, err := store.filterChunkIDs(ctx, ids, "docs/", "", nil)
	if err != nil {
		t.Fatalf("filterChunkIDs() with %d ids error: %v", len(ids), err)
	}
	if len(got) != 1 || got[0] != storedID {
		t.Fatalf("filterChunkIDs() = %v, want [%d]", got, storedID)
	}
}

// TestLoadHNSWChunkRows_ExceedsSQLVariableLimit is the same regression for the
// candidate-row loader, which swallowed the error and returned no vectors.
func TestLoadHNSWChunkRows_ExceedsSQLVariableLimit(t *testing.T) {
	dir := t.TempDir()
	store, err := NewStore(filepath.Join(dir, "quant.db"))
	if err != nil {
		t.Fatalf("NewStore() error: %v", err)
	}
	mustCloseStore(t, store)

	ctx := context.Background()
	if _, err := store.EnsureEmbeddingMetadata(ctx, EmbeddingMetadata{Model: "batch-test", Dimensions: 2, Normalized: true}); err != nil {
		t.Fatalf("EnsureEmbeddingMetadata() error: %v", err)
	}
	doc := &Document{Path: "docs/a.md", Hash: "h1", ModifiedAt: time.Now()}
	if err := store.ReindexDocument(ctx, doc, []ChunkRecord{{
		Content:    "batched content",
		ChunkIndex: 0,
		Embedding:  EncodeFloat32(NormalizeFloat32([]float32{1, 0})),
	}}); err != nil {
		t.Fatalf("ReindexDocument() error: %v", err)
	}

	var storedID int
	if err := store.db.QueryRowContext(ctx, `SELECT id FROM chunks LIMIT 1`).Scan(&storedID); err != nil {
		t.Fatalf("reading stored chunk id: %v", err)
	}

	ids := make([]int, 0, sqliteMaxVariables+10)
	ids = append(ids, storedID)
	for i := range sqliteMaxVariables + 9 {
		ids = append(ids, storedID+i+1)
	}

	vectorOnly := make(map[int]*searchCandidate)
	store.loadHNSWChunkRows(ctx, ids, NormalizeFloat32([]float32{1, 0}), 10, nil, vectorOnly, nil)

	if len(vectorOnly) != 1 {
		t.Fatalf("loadHNSWChunkRows() collected %d candidates, want 1", len(vectorOnly))
	}
	if _, ok := vectorOnly[storedID]; !ok {
		t.Fatalf("loadHNSWChunkRows() missing chunk %d, got %v", storedID, vectorOnly)
	}
}

// TestSearch_UncappedVectorCandidatesWithFilter exercises the brute-force
// filtered fallback with max_vector_candidates=-1 (unlimited), the documented
// config value that used to feed an unbounded id list into a single statement.
func TestSearch_UncappedVectorCandidatesWithFilter(t *testing.T) {
	dir := t.TempDir()
	store, err := NewStore(filepath.Join(dir, "quant.db"))
	if err != nil {
		t.Fatalf("NewStore() error: %v", err)
	}
	mustCloseStore(t, store)

	ctx := context.Background()
	if _, err := store.EnsureEmbeddingMetadata(ctx, EmbeddingMetadata{Model: "uncapped-test", Dimensions: 2, Normalized: true}); err != nil {
		t.Fatalf("EnsureEmbeddingMetadata() error: %v", err)
	}

	for _, d := range []struct {
		path string
		vec  []float32
	}{
		{"src/a.go", NormalizeFloat32([]float32{1, 0})},
		{"src/b.go", NormalizeFloat32([]float32{0.9, 0.1})},
		{"docs/readme.md", NormalizeFloat32([]float32{0, 1})},
	} {
		doc := &Document{Path: d.path, Hash: "h-" + d.path, ModifiedAt: time.Now()}
		if err := store.ReindexDocument(ctx, doc, []ChunkRecord{{
			Content:    d.path + " content",
			ChunkIndex: 0,
			Embedding:  EncodeFloat32(d.vec),
		}}); err != nil {
			t.Fatalf("ReindexDocument(%s) error: %v", d.path, err)
		}
	}

	if err := store.BuildHNSW(ctx); err != nil {
		t.Fatalf("BuildHNSW() error: %v", err)
	}
	store.SetMaxVectorSearchCandidates(-1)

	results, err := store.Search(ctx, "xyznoexist", NormalizeFloat32([]float32{1, 0}), 10, "src")
	if err != nil {
		t.Fatalf("Search() error: %v", err)
	}
	if len(results) != 2 {
		t.Fatalf("expected 2 src/* vector results, got %d", len(results))
	}
	for _, result := range results {
		if got := result.DocumentPath; got != "src/a.go" && got != "src/b.go" {
			t.Fatalf("unexpected result path %q", got)
		}
	}
}
