package index

import (
	"context"
	"database/sql"
	"fmt"
	"path/filepath"
	"testing"
	"time"
)

func TestMigrate_Idempotent(t *testing.T) {
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "quant.db")
	store, err := NewStore(dbPath)
	if err != nil {
		t.Fatalf("NewStore() error: %v", err)
	}
	mustCloseStore(t, store)

	if err := store.migrate(); err != nil {
		t.Fatalf("second migrate() error: %v", err)
	}
}

func TestMigrate_OldSchemaToNew(t *testing.T) {
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "quant.db")
	db, err := sql.Open("sqlite", dbPath)
	if err != nil {
		t.Fatalf("sql.Open error: %v", err)
	}
	_, err = db.Exec(`CREATE TABLE documents (
		id INTEGER PRIMARY KEY AUTOINCREMENT,
		path TEXT NOT NULL UNIQUE,
		hash TEXT NOT NULL,
		modified_at DATETIME NOT NULL,
		indexed_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP
	)`)
	if err != nil {
		t.Fatalf("create old documents table: %v", err)
	}
	_, err = db.Exec(`CREATE TABLE chunks (
		id INTEGER PRIMARY KEY AUTOINCREMENT,
		document_id INTEGER NOT NULL REFERENCES documents(id) ON DELETE CASCADE,
		content TEXT NOT NULL,
		chunk_index INTEGER NOT NULL,
		embedding BLOB NOT NULL
	)`)
	if err != nil {
		t.Fatalf("create old chunks table: %v", err)
	}
	_, err = db.Exec(`CREATE TABLE embedding_metadata (
		key TEXT PRIMARY KEY,
		value TEXT NOT NULL
	)`)
	if err != nil {
		t.Fatalf("create embedding_metadata table: %v", err)
	}
	_, err = db.Exec(`CREATE TABLE hnsw_state (
		id INTEGER PRIMARY KEY CHECK (id = 1),
		built_at DATETIME NOT NULL,
		node_count INTEGER NOT NULL
	)`)
	if err != nil {
		t.Fatalf("create old hnsw_state table: %v", err)
	}
	if err := db.Close(); err != nil {
		t.Fatalf("close old db: %v", err)
	}

	store, err := NewStore(dbPath)
	if err != nil {
		t.Fatalf("NewStore() on old schema error: %v", err)
	}
	mustCloseStore(t, store)

	ctx := context.Background()
	_, err = store.UpsertDocument(ctx, &Document{
		Path:       "migrate/old.txt",
		Hash:       "h1",
		ModifiedAt: time.Now(),
		Collection: "testcol",
		FileType:   "txt",
		Language:   "text",
	})
	if err != nil {
		t.Fatalf("UpsertDocument after migration error: %v", err)
	}

	doc, err := store.GetDocumentByPath(ctx, "migrate/old.txt")
	if err != nil {
		t.Fatalf("GetDocumentByPath error: %v", err)
	}
	if doc == nil {
		t.Fatal("expected document, got nil")
	}
	if doc.Collection != "testcol" {
		t.Fatalf("expected collection 'testcol', got %q", doc.Collection)
	}
}

func TestStore_CloseAfterOperations(t *testing.T) {
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "quant.db")
	store, err := NewStore(dbPath)
	if err != nil {
		t.Fatalf("NewStore() error: %v", err)
	}

	ctx := context.Background()
	if _, err := store.EnsureEmbeddingMetadata(ctx, EmbeddingMetadata{Model: "close-test", Dimensions: 2, Normalized: true}); err != nil {
		t.Fatalf("EnsureEmbeddingMetadata() error: %v", err)
	}
	doc := &Document{Path: "close/doc.txt", Hash: "h1", ModifiedAt: time.Now()}
	if err := store.ReindexDocument(ctx, doc, []ChunkRecord{{
		Content:    "close test content",
		ChunkIndex: 0,
		Embedding:  EncodeFloat32(NormalizeFloat32([]float32{1, 0})),
	}}); err != nil {
		t.Fatalf("ReindexDocument() error: %v", err)
	}

	if err := store.Close(); err != nil {
		t.Fatalf("Close() error: %v", err)
	}
}

func TestStore_CloseNilStore(t *testing.T) {
	var s *Store
	if err := s.Close(); err != nil {
		t.Fatalf("Close() on nil Store error: %v", err)
	}
}

func TestResetIndex(t *testing.T) {
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "quant.db")
	store, err := NewStore(dbPath)
	if err != nil {
		t.Fatalf("NewStore() error: %v", err)
	}
	mustCloseStore(t, store)

	ctx := context.Background()
	for i := 0; i < 3; i++ {
		path := fmt.Sprintf("reset/doc%d.txt", i)
		id, err := store.UpsertDocument(ctx, &Document{
			Path:       path,
			Hash:       fmt.Sprintf("h%d", i),
			ModifiedAt: time.Now(),
		})
		if err != nil {
			t.Fatalf("UpsertDocument(%s) error: %v", path, err)
		}
		if err := store.InsertChunk(ctx, &ChunkRecord{
			DocumentID: id,
			Content:    "chunk " + path,
			ChunkIndex: 0,
			Embedding:  EncodeFloat32([]float32{1}),
		}); err != nil {
			t.Fatalf("InsertChunk(%s) error: %v", path, err)
		}
	}

	docCount, chunkCount, err := store.Stats(ctx)
	if err != nil {
		t.Fatalf("Stats() before reset error: %v", err)
	}
	if docCount != 3 || chunkCount != 3 {
		t.Fatalf("expected 3 docs, 3 chunks before reset, got %d docs, %d chunks", docCount, chunkCount)
	}

	if err := store.resetIndex(ctx); err != nil {
		t.Fatalf("resetIndex() error: %v", err)
	}

	docCount, chunkCount, err = store.Stats(ctx)
	if err != nil {
		t.Fatalf("Stats() after reset error: %v", err)
	}
	if docCount != 0 || chunkCount != 0 {
		t.Fatalf("expected 0 docs, 0 chunks after reset, got %d docs, %d chunks", docCount, chunkCount)
	}
}

func TestAnyHasVectorScore(t *testing.T) {
	empty := anyHasVectorScore(nil)
	if empty {
		t.Fatal("expected false for nil candidates")
	}

	noScore := map[int]*searchCandidate{
		1: {id: 1, vectorScore: 0},
		2: {id: 2, vectorScore: 0},
	}
	if anyHasVectorScore(noScore) {
		t.Fatal("expected false when all scores are zero")
	}

	withScore := map[int]*searchCandidate{
		1: {id: 1, vectorScore: 0},
		2: {id: 2, vectorScore: 0.5},
	}
	if !anyHasVectorScore(withScore) {
		t.Fatal("expected true when at least one score is nonzero")
	}
}

func TestPathBoost(t *testing.T) {
	stage := pathBoost([]string{"main", "go"})

	candidates := []scoredCandidate{
		{result: SearchResult{DocumentPath: "src/main.go"}, score: 0.5},
		{result: SearchResult{DocumentPath: "docs/readme.md"}, score: 0.3},
	}
	result := stage(candidates)
	if result[0].score <= 0.5 {
		t.Fatalf("expected score boost for matching path, got %f", result[0].score)
	}
	if result[1].score != 0.3 {
		t.Fatalf("expected unchanged score for non-matching path, got %f", result[1].score)
	}

	noTokens := pathBoost(nil)
	unchanged := []scoredCandidate{{result: SearchResult{DocumentPath: "a.go"}, score: 1.0}}
	result2 := noTokens(unchanged)
	if result2[0].score != 1.0 {
		t.Fatalf("expected unchanged score with no tokens, got %f", result2[0].score)
	}
}

func TestDocEmbeddingWeight_VariousPositions(t *testing.T) {
	w0 := docEmbeddingWeight(0, 1)
	if w0 != 1.0 {
		t.Fatalf("expected 1.0 for single chunk, got %f", w0)
	}

	wFirst := docEmbeddingWeight(0, 3)
	if wFirst <= 1.0 {
		t.Fatalf("expected first chunk bonus > 1.0 for 3 chunks, got %f", wFirst)
	}

	wLast := docEmbeddingWeight(2, 3)
	if wLast <= 1.0 {
		t.Fatalf("expected last chunk bonus > 1.0 for 3 chunks, got %f", wLast)
	}

	wMid := docEmbeddingWeight(1, 3)
	if wMid != 1.0 {
		t.Fatalf("expected 1.0 for middle of 3 chunks, got %f", wMid)
	}

	wFirstBig := docEmbeddingWeight(0, 10)
	if wFirstBig <= 1.0 {
		t.Fatalf("expected first chunk bonus for 10 chunks, got %f", wFirstBig)
	}

	wMiddleBig := docEmbeddingWeight(4, 10)
	if wMiddleBig <= 1.0 {
		t.Fatalf("expected middle weight > 1.0 for 10 chunks, got %f", wMiddleBig)
	}

	wEndBig := docEmbeddingWeight(9, 10)
	if wEndBig <= 1.0 {
		t.Fatalf("expected end bonus for 10 chunks, got %f", wEndBig)
	}
}

func TestDocEmbeddingIndex_SetRemoveTopPaths(t *testing.T) {
	idx := newDocEmbeddingIndex()
	if idx.Len() != 0 {
		t.Fatalf("expected len 0, got %d", idx.Len())
	}

	idx.Set(1, "a.go", []float32{1, 0})
	idx.Set(2, "b.go", []float32{0, 1})
	if idx.Len() != 2 {
		t.Fatalf("expected len 2, got %d", idx.Len())
	}

	topPaths := idx.topDocPaths([]float32{1, 0}, 1)
	if len(topPaths) != 1 {
		t.Fatalf("expected 1 top path, got %d", len(topPaths))
	}
	if _, ok := topPaths["a.go"]; !ok {
		t.Fatal("expected a.go in top paths")
	}

	empty := idx.topDocPaths(nil, 5)
	if empty != nil {
		t.Fatalf("expected nil for nil query, got %v", empty)
	}

	idx.Remove(1, "a.go")
	if idx.Len() != 1 {
		t.Fatalf("expected len 1 after remove, got %d", idx.Len())
	}
}

func TestDocEmbeddingIndex_TopDocPaths_MoreThanTopK(t *testing.T) {
	idx := newDocEmbeddingIndex()
	for i := 0; i < 5; i++ {
		vec := make([]float32, 3)
		vec[i%3] = float32(i + 1)
		idx.Set(int64(i+1), fmt.Sprintf("doc%d.go", i), vec)
	}

	topPaths := idx.topDocPaths([]float32{1, 0, 0}, 2)
	if len(topPaths) != 2 {
		t.Fatalf("expected 2 top paths, got %d", len(topPaths))
	}
}

func TestComputeDocEmbedding_Empty(t *testing.T) {
	result := computeDocEmbedding(nil, 2)
	if result != nil {
		t.Fatalf("expected nil for empty chunks, got %v", result)
	}

	result = computeDocEmbedding([]ChunkRecord{{Embedding: nil, ChunkIndex: 0}}, 2)
	if result != nil {
		t.Fatalf("expected nil for chunks with no valid embeddings, got %v", result)
	}
}

func TestMigrateHNSWStateColumns_AlreadyHasColumns(t *testing.T) {
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "quant.db")
	store, err := NewStore(dbPath)
	if err != nil {
		t.Fatalf("NewStore() error: %v", err)
	}
	mustCloseStore(t, store)

	if err := store.migrateHNSWStateColumns(); err != nil {
		t.Fatalf("migrateHNSWStateColumns() idempotent error: %v", err)
	}
}

func TestEnsureEmbeddingMetadata_SameMeta(t *testing.T) {
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "quant.db")
	store, err := NewStore(dbPath)
	if err != nil {
		t.Fatalf("NewStore() error: %v", err)
	}
	mustCloseStore(t, store)

	ctx := context.Background()
	meta := EmbeddingMetadata{Model: "same-test", Dimensions: 3, Normalized: true}

	needsReset, err := store.EnsureEmbeddingMetadata(ctx, meta)
	if err != nil {
		t.Fatalf("first EnsureEmbeddingMetadata() error: %v", err)
	}
	if needsReset {
		t.Fatal("expected no reset for first call on empty DB")
	}

	needsReset2, err := store.EnsureEmbeddingMetadata(ctx, meta)
	if err != nil {
		t.Fatalf("second EnsureEmbeddingMetadata() error: %v", err)
	}
	if needsReset2 {
		t.Fatal("expected no reset for same meta")
	}
}

func TestEnsureEmbeddingMetadata_DifferentMetaTriggersReset(t *testing.T) {
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "quant.db")
	store, err := NewStore(dbPath)
	if err != nil {
		t.Fatalf("NewStore() error: %v", err)
	}
	mustCloseStore(t, store)

	ctx := context.Background()

	id, err := store.UpsertDocument(ctx, &Document{
		Path: "meta/before.txt", Hash: "h1", ModifiedAt: time.Now(),
	})
	if err != nil {
		t.Fatalf("UpsertDocument() error: %v", err)
	}
	if err := store.InsertChunk(ctx, &ChunkRecord{
		DocumentID: id, Content: "before meta change", ChunkIndex: 0, Embedding: EncodeFloat32([]float32{1}),
	}); err != nil {
		t.Fatalf("InsertChunk() error: %v", err)
	}

	meta1 := EmbeddingMetadata{Model: "model-a", Dimensions: 2, Normalized: true}
	_, err = store.EnsureEmbeddingMetadata(ctx, meta1)
	if err != nil {
		t.Fatalf("first EnsureEmbeddingMetadata() error: %v", err)
	}

	meta2 := EmbeddingMetadata{Model: "model-b", Dimensions: 4, Normalized: false}
	needsReset, err := store.EnsureEmbeddingMetadata(ctx, meta2)
	if err != nil {
		t.Fatalf("second EnsureEmbeddingMetadata() error: %v", err)
	}
	if !needsReset {
		t.Fatal("expected reset for different meta")
	}

	docCount, chunkCount, err := store.Stats(ctx)
	if err != nil {
		t.Fatalf("Stats() error: %v", err)
	}
	if docCount != 0 || chunkCount != 0 {
		t.Fatalf("expected 0 docs, 0 chunks after reset, got %d, %d", docCount, chunkCount)
	}
}

func TestCleanupOrphanedChunks_NoOrphans(t *testing.T) {
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "quant.db")
	store, err := NewStore(dbPath)
	if err != nil {
		t.Fatalf("NewStore() error: %v", err)
	}
	mustCloseStore(t, store)

	ctx := context.Background()
	id, err := store.UpsertDocument(ctx, &Document{
		Path: "clean/doc.txt", Hash: "h1", ModifiedAt: time.Now(),
	})
	if err != nil {
		t.Fatalf("UpsertDocument() error: %v", err)
	}
	if err := store.InsertChunk(ctx, &ChunkRecord{
		DocumentID: id, Content: "clean chunk", ChunkIndex: 0, Embedding: EncodeFloat32([]float32{1}),
	}); err != nil {
		t.Fatalf("InsertChunk() error: %v", err)
	}

	if err := store.cleanupOrphanedChunks(ctx); err != nil {
		t.Fatalf("cleanupOrphanedChunks() no orphans error: %v", err)
	}

	_, chunkCount, err := store.Stats(ctx)
	if err != nil {
		t.Fatalf("Stats() error: %v", err)
	}
	if chunkCount != 1 {
		t.Fatalf("expected 1 chunk unchanged, got %d", chunkCount)
	}
}
