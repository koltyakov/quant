package index

import (
	"context"
	"path/filepath"
	"testing"
	"time"
)

func TestVacuum(t *testing.T) {
	dir := t.TempDir()
	store, err := NewStore(filepath.Join(dir, "quant.db"))
	if err != nil {
		t.Fatalf("NewStore() error: %v", err)
	}
	mustCloseStore(t, store)

	ctx := context.Background()
	_, err = store.UpsertDocument(ctx, &Document{
		Path:       "vac/test.txt",
		Hash:       "v1",
		ModifiedAt: time.Now(),
	})
	if err != nil {
		t.Fatalf("UpsertDocument() error: %v", err)
	}

	if err := store.Vacuum(ctx); err != nil {
		t.Fatalf("Vacuum() error: %v", err)
	}
}

func TestVacuum_EmptyStore(t *testing.T) {
	dir := t.TempDir()
	store, err := NewStore(filepath.Join(dir, "quant.db"))
	if err != nil {
		t.Fatalf("NewStore() error: %v", err)
	}
	mustCloseStore(t, store)

	ctx := context.Background()
	if err := store.Vacuum(ctx); err != nil {
		t.Fatalf("Vacuum() on empty store error: %v", err)
	}
}

func TestDeleteDocument_WithHNSW(t *testing.T) {
	dir := t.TempDir()
	store, err := NewStore(filepath.Join(dir, "quant.db"))
	if err != nil {
		t.Fatalf("NewStore() error: %v", err)
	}
	mustCloseStore(t, store)

	ctx := context.Background()
	if _, err := store.EnsureEmbeddingMetadata(ctx, EmbeddingMetadata{Model: "test", Dimensions: 2, Normalized: true}); err != nil {
		t.Fatalf("EnsureEmbeddingMetadata() error: %v", err)
	}

	doc := &Document{Path: "del/hnsw.txt", Hash: "h1", ModifiedAt: time.Now()}
	if err := store.ReindexDocument(ctx, doc, []ChunkRecord{{
		Content:    "hnsw chunk to delete",
		ChunkIndex: 0,
		Embedding:  EncodeFloat32(NormalizeFloat32([]float32{1, 0})),
	}}); err != nil {
		t.Fatalf("ReindexDocument() error: %v", err)
	}

	if err := store.BuildHNSW(ctx); err != nil {
		t.Fatalf("BuildHNSW() error: %v", err)
	}

	chunks, err := store.GetDocumentChunksByPath(ctx, "del/hnsw.txt")
	if err != nil {
		t.Fatalf("GetDocumentChunksByPath() error: %v", err)
	}
	var chunkID int64
	for _, c := range chunks {
		chunkID = c.ID
		break
	}

	similar, err := store.FindSimilar(ctx, chunkID, 2)
	if err != nil {
		t.Fatalf("FindSimilar before delete error: %v", err)
	}
	if len(similar) != 0 {
		t.Fatalf("expected 0 similar results with only one chunk, got %d", len(similar))
	}

	if err := store.DeleteDocument(ctx, "del/hnsw.txt"); err != nil {
		t.Fatalf("DeleteDocument() error: %v", err)
	}

	got, err := store.GetDocumentByPath(ctx, "del/hnsw.txt")
	if err != nil {
		t.Fatalf("GetDocumentByPath() error: %v", err)
	}
	if got != nil {
		t.Fatal("expected nil document after deletion")
	}

	docCount, chunkCount, err := store.Stats(ctx)
	if err != nil {
		t.Fatalf("Stats() error: %v", err)
	}
	if docCount != 0 || chunkCount != 0 {
		t.Fatalf("expected empty store, got %d docs, %d chunks", docCount, chunkCount)
	}
}

func TestDeleteDocument_Nonexistent(t *testing.T) {
	dir := t.TempDir()
	store, err := NewStore(filepath.Join(dir, "quant.db"))
	if err != nil {
		t.Fatalf("NewStore() error: %v", err)
	}
	mustCloseStore(t, store)

	ctx := context.Background()
	if err := store.DeleteDocument(ctx, "nonexistent.txt"); err != nil {
		t.Fatalf("DeleteDocument on nonexistent path should not error, got: %v", err)
	}
}

func TestDeleteDocumentsByPrefix_WithHNSW(t *testing.T) {
	dir := t.TempDir()
	store, err := NewStore(filepath.Join(dir, "quant.db"))
	if err != nil {
		t.Fatalf("NewStore() error: %v", err)
	}
	mustCloseStore(t, store)

	ctx := context.Background()
	if _, err := store.EnsureEmbeddingMetadata(ctx, EmbeddingMetadata{Model: "test", Dimensions: 2, Normalized: true}); err != nil {
		t.Fatalf("EnsureEmbeddingMetadata() error: %v", err)
	}

	docs := []struct {
		path string
		vec  []float32
	}{
		{"src/a.go", []float32{1, 0}},
		{"src/b.go", []float32{0.9, 0.1}},
		{"docs/readme.md", []float32{0, 1}},
	}
	for _, d := range docs {
		doc := &Document{Path: d.path, Hash: "h-" + d.path, ModifiedAt: time.Now()}
		if err := store.ReindexDocument(ctx, doc, []ChunkRecord{{
			Content:    d.path + " content",
			ChunkIndex: 0,
			Embedding:  EncodeFloat32(NormalizeFloat32(d.vec)),
		}}); err != nil {
			t.Fatalf("ReindexDocument(%s) error: %v", d.path, err)
		}
	}

	if err := store.BuildHNSW(ctx); err != nil {
		t.Fatalf("BuildHNSW() error: %v", err)
	}

	if err := store.DeleteDocumentsByPrefix(ctx, "src"); err != nil {
		t.Fatalf("DeleteDocumentsByPrefix() error: %v", err)
	}

	docs2, err := store.ListDocuments(ctx)
	if err != nil {
		t.Fatalf("ListDocuments() error: %v", err)
	}
	if len(docs2) != 1 || docs2[0].Path != "docs/readme.md" {
		t.Fatalf("expected only docs/readme.md, got %+v", docs2)
	}

	docCount, chunkCount, err := store.Stats(ctx)
	if err != nil {
		t.Fatalf("Stats() error: %v", err)
	}
	if docCount != 1 || chunkCount != 1 {
		t.Fatalf("expected 1 doc, 1 chunk, got %d docs, %d chunks", docCount, chunkCount)
	}
}

func TestDeleteDocumentsByPrefix_DeleteAll(t *testing.T) {
	dir := t.TempDir()
	store, err := NewStore(filepath.Join(dir, "quant.db"))
	if err != nil {
		t.Fatalf("NewStore() error: %v", err)
	}
	mustCloseStore(t, store)

	ctx := context.Background()
	for _, path := range []string{"a.txt", "b.txt", "c.txt"} {
		id, err := store.UpsertDocument(ctx, &Document{
			Path:       path,
			Hash:       "h-" + path,
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

	if err := store.DeleteDocumentsByPrefix(ctx, "."); err != nil {
		t.Fatalf("DeleteDocumentsByPrefix('.') error: %v", err)
	}

	docCount, chunkCount, err := store.Stats(ctx)
	if err != nil {
		t.Fatalf("Stats() error: %v", err)
	}
	if docCount != 0 || chunkCount != 0 {
		t.Fatalf("expected empty store after delete-all, got %d docs, %d chunks", docCount, chunkCount)
	}
}

func TestEnsureEmbeddingMetadata_EmptyStore(t *testing.T) {
	dir := t.TempDir()
	store, err := NewStore(filepath.Join(dir, "quant.db"))
	if err != nil {
		t.Fatalf("NewStore() error: %v", err)
	}
	mustCloseStore(t, store)

	ctx := context.Background()
	rebuild, err := store.EnsureEmbeddingMetadata(ctx, EmbeddingMetadata{
		Model:      "test-model",
		Dimensions: 128,
		Normalized: true,
	})
	if err != nil {
		t.Fatalf("EnsureEmbeddingMetadata() error: %v", err)
	}
	if rebuild {
		t.Fatal("expected no rebuild on empty store")
	}

	got, err := store.embeddingMetadata(ctx)
	if err != nil {
		t.Fatalf("embeddingMetadata() error: %v", err)
	}
	if got == nil || got.Model != "test-model" || got.Dimensions != 128 || !got.Normalized {
		t.Fatalf("unexpected metadata: %+v", got)
	}
}

func TestEnsureEmbeddingMetadata_SameMetadata(t *testing.T) {
	dir := t.TempDir()
	store, err := NewStore(filepath.Join(dir, "quant.db"))
	if err != nil {
		t.Fatalf("NewStore() error: %v", err)
	}
	mustCloseStore(t, store)

	ctx := context.Background()
	meta := EmbeddingMetadata{Model: "mymodel", Dimensions: 64, Normalized: true}

	rebuild1, err := store.EnsureEmbeddingMetadata(ctx, meta)
	if err != nil {
		t.Fatalf("first EnsureEmbeddingMetadata() error: %v", err)
	}

	rebuild2, err := store.EnsureEmbeddingMetadata(ctx, meta)
	if err != nil {
		t.Fatalf("second EnsureEmbeddingMetadata() error: %v", err)
	}
	if rebuild2 {
		t.Fatalf("expected no rebuild when metadata unchanged, rebuild1=%v rebuild2=%v", rebuild1, rebuild2)
	}
}

func TestEnsureEmbeddingMetadata_ChangedMetadata(t *testing.T) {
	dir := t.TempDir()
	store, err := NewStore(filepath.Join(dir, "quant.db"))
	if err != nil {
		t.Fatalf("NewStore() error: %v", err)
	}
	mustCloseStore(t, store)

	ctx := context.Background()

	_, err = store.EnsureEmbeddingMetadata(ctx, EmbeddingMetadata{Model: "v1", Dimensions: 32, Normalized: true})
	if err != nil {
		t.Fatalf("first EnsureEmbeddingMetadata() error: %v", err)
	}

	rebuild, err := store.EnsureEmbeddingMetadata(ctx, EmbeddingMetadata{Model: "v2", Dimensions: 64, Normalized: true})
	if err != nil {
		t.Fatalf("second EnsureEmbeddingMetadata() error: %v", err)
	}
	if !rebuild {
		t.Fatal("expected rebuild when metadata changed")
	}

	got, err := store.embeddingMetadata(ctx)
	if err != nil {
		t.Fatalf("embeddingMetadata() error: %v", err)
	}
	if got.Model != "v2" || got.Dimensions != 64 {
		t.Fatalf("unexpected metadata after change: %+v", got)
	}
}

func TestGetDocumentByPath_NotFound(t *testing.T) {
	dir := t.TempDir()
	store, err := NewStore(filepath.Join(dir, "quant.db"))
	if err != nil {
		t.Fatalf("NewStore() error: %v", err)
	}
	mustCloseStore(t, store)

	ctx := context.Background()
	doc, err := store.GetDocumentByPath(ctx, "nonexistent.txt")
	if err != nil {
		t.Fatalf("GetDocumentByPath() error: %v", err)
	}
	if doc != nil {
		t.Fatal("expected nil for nonexistent path")
	}
}

func TestGetDocumentByPath_WithTags(t *testing.T) {
	dir := t.TempDir()
	store, err := NewStore(filepath.Join(dir, "quant.db"))
	if err != nil {
		t.Fatalf("NewStore() error: %v", err)
	}
	mustCloseStore(t, store)

	ctx := context.Background()
	_, err = store.UpsertDocument(ctx, &Document{
		Path:       "tagged/file.txt",
		Hash:       "ht",
		ModifiedAt: time.Now(),
		Tags:       map[string]string{"env": "prod", "team": "backend"},
	})
	if err != nil {
		t.Fatalf("UpsertDocument() error: %v", err)
	}

	doc, err := store.GetDocumentByPath(ctx, "tagged/file.txt")
	if err != nil {
		t.Fatalf("GetDocumentByPath() error: %v", err)
	}
	if doc == nil {
		t.Fatal("expected document, got nil")
	}
	if doc.Tags == nil {
		t.Fatal("expected tags to be non-nil")
	}
	if doc.Tags["env"] != "prod" {
		t.Fatalf("expected env=prod, got %v", doc.Tags)
	}
	if doc.Tags["team"] != "backend" {
		t.Fatalf("expected team=backend, got %v", doc.Tags)
	}
}

func TestGetDocumentByPath_WithMetadata(t *testing.T) {
	dir := t.TempDir()
	store, err := NewStore(filepath.Join(dir, "quant.db"))
	if err != nil {
		t.Fatalf("NewStore() error: %v", err)
	}
	mustCloseStore(t, store)

	ctx := context.Background()
	_, err = store.UpsertDocument(ctx, &Document{
		Path:       "meta/doc.go",
		Hash:       "hm",
		ModifiedAt: time.Now(),
		FileType:   "go",
		Language:   "go",
		Title:      "My Go Doc",
		Collection: "gocode",
	})
	if err != nil {
		t.Fatalf("UpsertDocument() error: %v", err)
	}

	doc, err := store.GetDocumentByPath(ctx, "meta/doc.go")
	if err != nil {
		t.Fatalf("GetDocumentByPath() error: %v", err)
	}
	if doc == nil {
		t.Fatal("expected document, got nil")
	}
	if doc.FileType != "go" {
		t.Fatalf("expected FileType=go, got %q", doc.FileType)
	}
	if doc.Language != "go" {
		t.Fatalf("expected Language=go, got %q", doc.Language)
	}
	if doc.Title != "My Go Doc" {
		t.Fatalf("expected Title='My Go Doc', got %q", doc.Title)
	}
	if doc.Collection != "gocode" {
		t.Fatalf("expected Collection='gocode', got %q", doc.Collection)
	}
}

func TestFindSimilar_NoMetadata(t *testing.T) {
	dir := t.TempDir()
	store, err := NewStore(filepath.Join(dir, "quant.db"))
	if err != nil {
		t.Fatalf("NewStore() error: %v", err)
	}
	mustCloseStore(t, store)

	ctx := context.Background()
	docID, err := store.UpsertDocument(ctx, &Document{
		Path:       "sim/nometa.txt",
		Hash:       "h1",
		ModifiedAt: time.Now(),
	})
	if err != nil {
		t.Fatalf("UpsertDocument() error: %v", err)
	}
	if err := store.InsertChunk(ctx, &ChunkRecord{
		DocumentID: docID,
		Content:    "no metadata chunk",
		ChunkIndex: 0,
		Embedding:  EncodeFloat32([]float32{1}),
	}); err != nil {
		t.Fatalf("InsertChunk() error: %v", err)
	}

	chunks, err := store.GetDocumentChunksByPath(ctx, "sim/nometa.txt")
	if err != nil {
		t.Fatalf("GetDocumentChunksByPath() error: %v", err)
	}
	var chunkID int64
	for _, c := range chunks {
		chunkID = c.ID
		break
	}

	results, err := store.FindSimilar(ctx, chunkID, 5)
	if err != nil {
		t.Fatalf("FindSimilar() error: %v", err)
	}
	if len(results) != 0 {
		t.Fatalf("expected no similar results without metadata, got %d", len(results))
	}
}

func TestFindSimilar_ZeroLimit(t *testing.T) {
	dir := t.TempDir()
	store, err := NewStore(filepath.Join(dir, "quant.db"))
	if err != nil {
		t.Fatalf("NewStore() error: %v", err)
	}
	mustCloseStore(t, store)

	ctx := context.Background()
	results, err := store.FindSimilar(ctx, 1, 0)
	if err != nil {
		t.Fatalf("FindSimilar() with limit=0 error: %v", err)
	}
	if len(results) != 0 {
		t.Fatalf("expected nil results with limit=0, got %d", len(results))
	}
}

func TestFindSimilar_WithHNSW(t *testing.T) {
	dir := t.TempDir()
	store, err := NewStore(filepath.Join(dir, "quant.db"))
	if err != nil {
		t.Fatalf("NewStore() error: %v", err)
	}
	mustCloseStore(t, store)

	ctx := context.Background()
	if _, err := store.EnsureEmbeddingMetadata(ctx, EmbeddingMetadata{Model: "sim-test", Dimensions: 2, Normalized: true}); err != nil {
		t.Fatalf("EnsureEmbeddingMetadata() error: %v", err)
	}

	docs := []struct {
		path string
		vec  []float32
	}{
		{"sim/a.txt", []float32{1, 0}},
		{"sim/b.txt", []float32{0.9, 0.1}},
		{"sim/c.txt", []float32{0, 1}},
	}
	for _, d := range docs {
		doc := &Document{Path: d.path, Hash: "h-" + d.path, ModifiedAt: time.Now()}
		if err := store.ReindexDocument(ctx, doc, []ChunkRecord{{
			Content:    d.path + " content",
			ChunkIndex: 0,
			Embedding:  EncodeFloat32(NormalizeFloat32(d.vec)),
		}}); err != nil {
			t.Fatalf("ReindexDocument(%s) error: %v", d.path, err)
		}
	}

	if err := store.BuildHNSW(ctx); err != nil {
		t.Fatalf("BuildHNSW() error: %v", err)
	}

	chunks, err := store.GetDocumentChunksByPath(ctx, "sim/a.txt")
	if err != nil {
		t.Fatalf("GetDocumentChunksByPath() error: %v", err)
	}
	var aChunkID int64
	for _, c := range chunks {
		aChunkID = c.ID
		break
	}

	results, err := store.FindSimilar(ctx, aChunkID, 2)
	if err != nil {
		t.Fatalf("FindSimilar() error: %v", err)
	}
	if len(results) == 0 {
		t.Fatal("expected similar results from HNSW, got none")
	}

	for _, r := range results {
		if r.ChunkID == aChunkID {
			t.Fatal("FindSimilar should not include the source chunk")
		}
		if r.ScoreKind != "similar" {
			t.Fatalf("expected ScoreKind='similar', got %q", r.ScoreKind)
		}
		if r.DocumentPath == "" || r.ChunkContent == "" {
			t.Fatalf("expected populated result, got %+v", r)
		}
	}
}

func TestStats_AfterOperations(t *testing.T) {
	dir := t.TempDir()
	store, err := NewStore(filepath.Join(dir, "quant.db"))
	if err != nil {
		t.Fatalf("NewStore() error: %v", err)
	}
	mustCloseStore(t, store)

	ctx := context.Background()

	docCount, chunkCount, err := store.Stats(ctx)
	if err != nil {
		t.Fatalf("Stats() on empty error: %v", err)
	}
	if docCount != 0 || chunkCount != 0 {
		t.Fatalf("expected empty store, got %d docs, %d chunks", docCount, chunkCount)
	}

	d1ID, _ := store.UpsertDocument(ctx, &Document{Path: "s/a.txt", Hash: "h1", ModifiedAt: time.Now()})
	d2ID, _ := store.UpsertDocument(ctx, &Document{Path: "s/b.txt", Hash: "h2", ModifiedAt: time.Now()})
	if err := store.InsertChunk(ctx, &ChunkRecord{DocumentID: d1ID, Content: "chunk a", ChunkIndex: 0, Embedding: EncodeFloat32([]float32{1})}); err != nil {
		t.Fatalf("InsertChunk(a) error: %v", err)
	}
	if err := store.InsertChunk(ctx, &ChunkRecord{DocumentID: d2ID, Content: "chunk b", ChunkIndex: 0, Embedding: EncodeFloat32([]float32{1})}); err != nil {
		t.Fatalf("InsertChunk(b) error: %v", err)
	}

	docCount, chunkCount, err = store.Stats(ctx)
	if err != nil {
		t.Fatalf("Stats() after inserts error: %v", err)
	}
	if docCount != 2 {
		t.Fatalf("expected 2 docs, got %d", docCount)
	}
	if chunkCount != 2 {
		t.Fatalf("expected 2 chunks, got %d", chunkCount)
	}
}

func TestCollectionStats(t *testing.T) {
	dir := t.TempDir()
	store, err := NewStore(filepath.Join(dir, "quant.db"))
	if err != nil {
		t.Fatalf("NewStore() error: %v", err)
	}
	mustCloseStore(t, store)

	ctx := context.Background()

	docCount, chunkCount, err := store.CollectionStats(ctx, "nonexistent")
	if err != nil {
		t.Fatalf("CollectionStats() for nonexistent error: %v", err)
	}
	if docCount != 0 || chunkCount != 0 {
		t.Fatalf("expected 0 for nonexistent collection, got %d docs, %d chunks", docCount, chunkCount)
	}

	for i, path := range []string{"col/x.txt", "col/y.txt"} {
		id, err := store.UpsertDocument(ctx, &Document{
			Path:       path,
			Hash:       "h" + path,
			ModifiedAt: time.Now(),
			Collection: "mycol",
		})
		if err != nil {
			t.Fatalf("UpsertDocument() error: %v", err)
		}
		if err := store.InsertChunk(ctx, &ChunkRecord{
			DocumentID: id,
			Content:    "content " + string(rune('A'+i)),
			ChunkIndex: 0,
			Embedding:  EncodeFloat32([]float32{1}),
		}); err != nil {
			t.Fatalf("InsertChunk() error: %v", err)
		}
	}

	docCount, chunkCount, err = store.CollectionStats(ctx, "mycol")
	if err != nil {
		t.Fatalf("CollectionStats() error: %v", err)
	}
	if docCount != 2 {
		t.Fatalf("expected 2 docs in mycol, got %d", docCount)
	}
	if chunkCount != 2 {
		t.Fatalf("expected 2 chunks in mycol, got %d", chunkCount)
	}
}

func TestListQuarantined_Empty(t *testing.T) {
	dir := t.TempDir()
	store, err := NewStore(filepath.Join(dir, "quant.db"))
	if err != nil {
		t.Fatalf("NewStore() error: %v", err)
	}
	mustCloseStore(t, store)

	ctx := context.Background()
	entries, err := store.ListQuarantined(ctx)
	if err != nil {
		t.Fatalf("ListQuarantined() error: %v", err)
	}
	if len(entries) != 0 {
		t.Fatalf("expected empty quarantine list, got %d entries", len(entries))
	}
}

func TestListQuarantined_Multiple(t *testing.T) {
	dir := t.TempDir()
	store, err := NewStore(filepath.Join(dir, "quant.db"))
	if err != nil {
		t.Fatalf("NewStore() error: %v", err)
	}
	mustCloseStore(t, store)

	ctx := context.Background()

	if err := store.AddToQuarantine(ctx, "path/a.txt", "err a"); err != nil {
		t.Fatalf("AddToQuarantine(a) error: %v", err)
	}
	if err := store.AddToQuarantine(ctx, "path/b.txt", "err b"); err != nil {
		t.Fatalf("AddToQuarantine(b) error: %v", err)
	}

	entries, err := store.ListQuarantined(ctx)
	if err != nil {
		t.Fatalf("ListQuarantined() error: %v", err)
	}
	if len(entries) != 2 {
		t.Fatalf("expected 2 quarantined entries, got %d", len(entries))
	}

	paths := map[string]bool{}
	for _, e := range entries {
		paths[e.Path] = true
		if e.Attempts != 1 {
			t.Fatalf("expected 1 attempt for %s, got %d", e.Path, e.Attempts)
		}
	}
	if !paths["path/a.txt"] || !paths["path/b.txt"] {
		t.Fatalf("expected both paths in quarantine, got %v", paths)
	}
}

func TestIsQuarantined_NotQuarantined(t *testing.T) {
	dir := t.TempDir()
	store, err := NewStore(filepath.Join(dir, "quant.db"))
	if err != nil {
		t.Fatalf("NewStore() error: %v", err)
	}
	mustCloseStore(t, store)

	ctx := context.Background()
	quarantined, err := store.IsQuarantined(ctx, "nothing.txt")
	if err != nil {
		t.Fatalf("IsQuarantined() error: %v", err)
	}
	if quarantined {
		t.Fatal("expected not quarantined")
	}
}

func TestIsQuarantined_AfterAdd(t *testing.T) {
	dir := t.TempDir()
	store, err := NewStore(filepath.Join(dir, "quant.db"))
	if err != nil {
		t.Fatalf("NewStore() error: %v", err)
	}
	mustCloseStore(t, store)

	ctx := context.Background()

	if err := store.AddToQuarantine(ctx, "quarantined.txt", "bad"); err != nil {
		t.Fatalf("AddToQuarantine() error: %v", err)
	}

	quarantined, err := store.IsQuarantined(ctx, "quarantined.txt")
	if err != nil {
		t.Fatalf("IsQuarantined() error: %v", err)
	}
	if !quarantined {
		t.Fatal("expected path to be quarantined")
	}

	quarantined, err = store.IsQuarantined(ctx, "other.txt")
	if err != nil {
		t.Fatalf("IsQuarantined(other) error: %v", err)
	}
	if quarantined {
		t.Fatal("expected other path not to be quarantined")
	}
}

func TestIsQuarantined_AfterRemove(t *testing.T) {
	dir := t.TempDir()
	store, err := NewStore(filepath.Join(dir, "quant.db"))
	if err != nil {
		t.Fatalf("NewStore() error: %v", err)
	}
	mustCloseStore(t, store)

	ctx := context.Background()

	if err := store.AddToQuarantine(ctx, "r.txt", "err"); err != nil {
		t.Fatalf("AddToQuarantine() error: %v", err)
	}
	if err := store.RemoveFromQuarantine(ctx, "r.txt"); err != nil {
		t.Fatalf("RemoveFromQuarantine() error: %v", err)
	}

	quarantined, err := store.IsQuarantined(ctx, "r.txt")
	if err != nil {
		t.Fatalf("IsQuarantined() error: %v", err)
	}
	if quarantined {
		t.Fatal("expected path to not be quarantined after removal")
	}
}

func TestSearch_WithHNSWAndPrefix(t *testing.T) {
	dir := t.TempDir()
	store, err := NewStore(filepath.Join(dir, "quant.db"))
	if err != nil {
		t.Fatalf("NewStore() error: %v", err)
	}
	mustCloseStore(t, store)

	ctx := context.Background()
	if _, err := store.EnsureEmbeddingMetadata(ctx, EmbeddingMetadata{Model: "search-test", Dimensions: 2, Normalized: true}); err != nil {
		t.Fatalf("EnsureEmbeddingMetadata() error: %v", err)
	}

	vecA := NormalizeFloat32([]float32{1, 0})
	vecB := NormalizeFloat32([]float32{0.9, 0.1})
	vecC := NormalizeFloat32([]float32{0, 1})

	type docDef struct {
		path string
		vec  []float32
	}
	docs := []docDef{
		{"src/main.go", vecA},
		{"src/util.go", vecB},
		{"docs/readme.md", vecC},
	}
	for _, d := range docs {
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

	allResults, err := store.Search(ctx, "content", vecA, 10, "")
	if err != nil {
		t.Fatalf("Search() error: %v", err)
	}
	if len(allResults) == 0 {
		t.Fatal("expected search results with HNSW, got none")
	}

	srcResults, err := store.Search(ctx, "content", vecA, 10, "src")
	if err != nil {
		t.Fatalf("Search() with prefix error: %v", err)
	}
	if len(srcResults) == 0 {
		t.Fatal("expected prefix-filtered search results, got none")
	}
	for _, r := range srcResults {
		if r.DocumentPath != "src/main.go" && r.DocumentPath != "src/util.go" {
			t.Fatalf("expected src/* result, got %s", r.DocumentPath)
		}
	}
}

func TestSearch_WithDocEmbedFilter(t *testing.T) {
	dir := t.TempDir()
	store, err := NewStore(filepath.Join(dir, "quant.db"))
	if err != nil {
		t.Fatalf("NewStore() error: %v", err)
	}
	mustCloseStore(t, store)

	ctx := context.Background()
	if _, err := store.EnsureEmbeddingMetadata(ctx, EmbeddingMetadata{Model: "doc-embed-test", Dimensions: 2, Normalized: true}); err != nil {
		t.Fatalf("EnsureEmbeddingMetadata() error: %v", err)
	}

	vecA := NormalizeFloat32([]float32{1, 0})
	vecB := NormalizeFloat32([]float32{0, 1})

	for i, d := range []struct {
		path string
		vec  []float32
	}{
		{"de/a.txt", vecA},
		{"de/b.txt", vecB},
	} {
		doc := &Document{Path: d.path, Hash: "h-" + d.path, ModifiedAt: time.Now()}
		if err := store.ReindexDocument(ctx, doc, []ChunkRecord{{
			Content:    d.path + " content",
			ChunkIndex: 0,
			Embedding:  EncodeFloat32(d.vec),
		}}); err != nil {
			t.Fatalf("ReindexDocument(%s) error: %v", d.path, err)
		}
		_ = i
	}

	if err := store.BuildHNSW(ctx); err != nil {
		t.Fatalf("BuildHNSW() error: %v", err)
	}

	results, err := store.Search(ctx, "xyznonexistent", vecA, 10, "")
	if err != nil {
		t.Fatalf("Search() error: %v", err)
	}
	if len(results) == 0 {
		t.Fatal("expected search results with doc embed filter, got none")
	}
}

func TestReindexDocumentWithDeferredHNSW(t *testing.T) {
	dir := t.TempDir()
	store, err := NewStore(filepath.Join(dir, "quant.db"))
	if err != nil {
		t.Fatalf("NewStore() error: %v", err)
	}
	mustCloseStore(t, store)

	ctx := context.Background()
	if _, err := store.EnsureEmbeddingMetadata(ctx, EmbeddingMetadata{Model: "deferred-test", Dimensions: 2, Normalized: true}); err != nil {
		t.Fatalf("EnsureEmbeddingMetadata() error: %v", err)
	}

	doc := &Document{Path: "deferred/doc.txt", Hash: "h1", ModifiedAt: time.Now()}
	chunks := []ChunkRecord{{
		Content:    "deferred content",
		ChunkIndex: 0,
		Embedding:  EncodeFloat32(NormalizeFloat32([]float32{1, 0})),
	}}

	if err := store.ReindexDocument(ctx, doc, chunks); err != nil {
		t.Fatalf("ReindexDocument() error: %v", err)
	}

	got, err := store.GetDocumentByPath(ctx, "deferred/doc.txt")
	if err != nil {
		t.Fatalf("GetDocumentByPath() error: %v", err)
	}
	if got == nil || got.Hash != "h1" {
		t.Fatalf("expected hash h1, got %+v", got)
	}

	docCount, chunkCount, err := store.Stats(ctx)
	if err != nil {
		t.Fatalf("Stats() error: %v", err)
	}
	if docCount != 1 || chunkCount != 1 {
		t.Fatalf("expected 1 doc, 1 chunk, got %d, %d", docCount, chunkCount)
	}
}

func TestUpsertDocument_WithTags(t *testing.T) {
	dir := t.TempDir()
	store, err := NewStore(filepath.Join(dir, "quant.db"))
	if err != nil {
		t.Fatalf("NewStore() error: %v", err)
	}
	mustCloseStore(t, store)

	ctx := context.Background()

	id, err := store.UpsertDocument(ctx, &Document{
		Path:       "tags/doc.txt",
		Hash:       "ht1",
		ModifiedAt: time.Now(),
		Tags:       map[string]string{"env": "staging", "team": "api"},
		FileType:   "txt",
		Language:   "text",
		Title:      "Tagged Doc",
		Collection: "mycol",
	})
	if err != nil {
		t.Fatalf("UpsertDocument() error: %v", err)
	}
	if id <= 0 {
		t.Fatalf("expected positive id, got %d", id)
	}

	doc, err := store.GetDocumentByPath(ctx, "tags/doc.txt")
	if err != nil {
		t.Fatalf("GetDocumentByPath() error: %v", err)
	}
	if doc.Tags == nil {
		t.Fatal("expected tags to be non-nil")
	}
	if doc.Tags["env"] != "staging" {
		t.Fatalf("expected env=staging, got %v", doc.Tags)
	}
	if doc.FileType != "txt" {
		t.Fatalf("expected FileType=txt, got %q", doc.FileType)
	}
	if doc.Collection != "mycol" {
		t.Fatalf("expected Collection=mycol, got %q", doc.Collection)
	}
}

func TestVacuum_AfterDeletions(t *testing.T) {
	dir := t.TempDir()
	store, err := NewStore(filepath.Join(dir, "quant.db"))
	if err != nil {
		t.Fatalf("NewStore() error: %v", err)
	}
	mustCloseStore(t, store)

	ctx := context.Background()
	for _, path := range []string{"vac/a.txt", "vac/b.txt", "vac/c.txt"} {
		_, err := store.UpsertDocument(ctx, &Document{
			Path:       path,
			Hash:       "h-" + path,
			ModifiedAt: time.Now(),
		})
		if err != nil {
			t.Fatalf("UpsertDocument() error: %v", err)
		}
	}

	if err := store.DeleteDocumentsByPrefix(ctx, "vac"); err != nil {
		t.Fatalf("DeleteDocumentsByPrefix() error: %v", err)
	}

	if err := store.Vacuum(ctx); err != nil {
		t.Fatalf("Vacuum() after deletion error: %v", err)
	}
}

func TestListDocuments_WithCollection(t *testing.T) {
	dir := t.TempDir()
	store, err := NewStore(filepath.Join(dir, "quant.db"))
	if err != nil {
		t.Fatalf("NewStore() error: %v", err)
	}
	mustCloseStore(t, store)

	ctx := context.Background()

	_, err = store.UpsertDocument(ctx, &Document{
		Path:       "col/a.txt",
		Hash:       "h1",
		ModifiedAt: time.Now(),
		Collection: "alpha",
	})
	if err != nil {
		t.Fatalf("UpsertDocument() error: %v", err)
	}
	_, err = store.UpsertDocument(ctx, &Document{
		Path:       "col/b.txt",
		Hash:       "h2",
		ModifiedAt: time.Now(),
		Collection: "beta",
	})
	if err != nil {
		t.Fatalf("UpsertDocument() error: %v", err)
	}

	collections, err := store.ListCollections(ctx)
	if err != nil {
		t.Fatalf("ListCollections() error: %v", err)
	}
	if len(collections) != 2 {
		t.Fatalf("expected 2 collections, got %d", len(collections))
	}
}

func TestEnsureEmbeddingMetadata_EmptyStoreSetsMetadata(t *testing.T) {
	dir := t.TempDir()
	store, err := NewStore(filepath.Join(dir, "quant.db"))
	if err != nil {
		t.Fatalf("NewStore() error: %v", err)
	}
	mustCloseStore(t, store)

	ctx := context.Background()
	meta := EmbeddingMetadata{Model: "empty-store-model", Dimensions: 64, Normalized: false}

	rebuild, err := store.EnsureEmbeddingMetadata(ctx, meta)
	if err != nil {
		t.Fatalf("EnsureEmbeddingMetadata() error: %v", err)
	}
	if rebuild {
		t.Fatal("expected no rebuild on empty store")
	}

	stored, err := store.embeddingMetadata(ctx)
	if err != nil {
		t.Fatalf("embeddingMetadata() error: %v", err)
	}
	if stored.Model != "empty-store-model" {
		t.Fatalf("expected model 'empty-store-model', got %q", stored.Model)
	}
	if stored.Dimensions != 64 {
		t.Fatalf("expected dimensions 64, got %d", stored.Dimensions)
	}
	if stored.Normalized {
		t.Fatal("expected Normalized=false")
	}
}

func TestLoadHNSWFromState(t *testing.T) {
	dir := t.TempDir()
	store, err := NewStore(filepath.Join(dir, "quant.db"))
	if err != nil {
		t.Fatalf("NewStore() error: %v", err)
	}
	mustCloseStore(t, store)

	ctx := context.Background()
	if _, err := store.EnsureEmbeddingMetadata(ctx, EmbeddingMetadata{Model: "load-test", Dimensions: 2, Normalized: true}); err != nil {
		t.Fatalf("EnsureEmbeddingMetadata() error: %v", err)
	}

	doc := &Document{Path: "load/a.txt", Hash: "h1", ModifiedAt: time.Now()}
	if err := store.ReindexDocument(ctx, doc, []ChunkRecord{{
		Content:    "load test content",
		ChunkIndex: 0,
		Embedding:  EncodeFloat32(NormalizeFloat32([]float32{1, 0})),
	}}); err != nil {
		t.Fatalf("ReindexDocument() error: %v", err)
	}

	if err := store.BuildHNSW(ctx); err != nil {
		t.Fatalf("BuildHNSW() error: %v", err)
	}

	if !store.HNSWReady() {
		t.Fatal("expected HNSW to be ready after build")
	}
	if store.HNSWLen() != 1 {
		t.Fatalf("expected HNSW len 1, got %d", store.HNSWLen())
	}

	if err := store.FlushHNSW(); err != nil {
		t.Fatalf("FlushHNSW() error: %v", err)
	}

	if loaded := store.LoadHNSWFromState(ctx); !loaded {
		t.Fatal("expected LoadHNSWFromState to return true")
	}
	if !store.HNSWReady() {
		t.Fatal("expected HNSW to be ready after load")
	}
	if store.HNSWLen() != 1 {
		t.Fatalf("expected HNSW len 1 after load, got %d", store.HNSWLen())
	}
}

func TestReindexDocumentWithDeferredHNSW_Callback(t *testing.T) {
	dir := t.TempDir()
	store, err := NewStore(filepath.Join(dir, "quant.db"))
	if err != nil {
		t.Fatalf("NewStore() error: %v", err)
	}
	mustCloseStore(t, store)

	ctx := context.Background()
	if _, err := store.EnsureEmbeddingMetadata(ctx, EmbeddingMetadata{Model: "defer-cb", Dimensions: 2, Normalized: true}); err != nil {
		t.Fatalf("EnsureEmbeddingMetadata() error: %v", err)
	}

	callbackCalled := false
	doc := &Document{Path: "defer/callback.txt", Hash: "h1", ModifiedAt: time.Now()}
	if err := store.ReindexDocumentWithDeferredHNSW(ctx, doc, []ChunkRecord{{
		Content:    "deferred callback content",
		ChunkIndex: 0,
		Embedding:  EncodeFloat32(NormalizeFloat32([]float32{1, 0})),
	}}, func() {
		callbackCalled = true
	}); err != nil {
		t.Fatalf("ReindexDocumentWithDeferredHNSW() error: %v", err)
	}
	if !callbackCalled {
		t.Fatal("expected deferred callback to be called")
	}

	got, err := store.GetDocumentByPath(ctx, "defer/callback.txt")
	if err != nil {
		t.Fatalf("GetDocumentByPath() error: %v", err)
	}
	if got == nil || got.Hash != "h1" {
		t.Fatalf("expected document with hash h1, got %+v", got)
	}
}

func TestSearch_WithHNSWNoPrefix(t *testing.T) {
	dir := t.TempDir()
	store, err := NewStore(filepath.Join(dir, "quant.db"))
	if err != nil {
		t.Fatalf("NewStore() error: %v", err)
	}
	mustCloseStore(t, store)

	ctx := context.Background()
	if _, err := store.EnsureEmbeddingMetadata(ctx, EmbeddingMetadata{Model: "hnsw-nopfx", Dimensions: 2, Normalized: true}); err != nil {
		t.Fatalf("EnsureEmbeddingMetadata() error: %v", err)
	}

	type docDef struct {
		path string
		vec  []float32
	}
	docs := []docDef{
		{"hnsw/a.txt", NormalizeFloat32([]float32{1, 0})},
		{"hnsw/b.txt", NormalizeFloat32([]float32{0.9, 0.1})},
		{"hnsw/c.txt", NormalizeFloat32([]float32{0, 1})},
	}
	for _, d := range docs {
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

	query := NormalizeFloat32([]float32{1, 0})
	results, err := store.Search(ctx, "content", query, 10, "")
	if err != nil {
		t.Fatalf("Search() error: %v", err)
	}
	if len(results) == 0 {
		t.Fatal("expected search results via HNSW, got none")
	}
}

func TestSearch_WithHNSWAndPrefix_InsufficientFallback(t *testing.T) {
	dir := t.TempDir()
	store, err := NewStore(filepath.Join(dir, "quant.db"))
	if err != nil {
		t.Fatalf("NewStore() error: %v", err)
	}
	mustCloseStore(t, store)

	ctx := context.Background()
	if _, err := store.EnsureEmbeddingMetadata(ctx, EmbeddingMetadata{Model: "hnsw-pfx-test", Dimensions: 2, Normalized: true}); err != nil {
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

	store.SetMaxVectorSearchCandidates(1)

	query := NormalizeFloat32([]float32{1, 0})
	results, err := store.Search(ctx, "xyznoexist", query, 10, "src")
	if err != nil {
		t.Fatalf("Search() with prefix error: %v", err)
	}
	_ = results
}

func TestSearch_VectorFallbackWithDocFilter(t *testing.T) {
	dir := t.TempDir()
	store, err := NewStore(filepath.Join(dir, "quant.db"))
	if err != nil {
		t.Fatalf("NewStore() error: %v", err)
	}
	mustCloseStore(t, store)

	ctx := context.Background()
	if _, err := store.EnsureEmbeddingMetadata(ctx, EmbeddingMetadata{Model: "docfilter", Dimensions: 2, Normalized: true}); err != nil {
		t.Fatalf("EnsureEmbeddingMetadata() error: %v", err)
	}

	for _, d := range []struct {
		path string
		vec  []float32
	}{
		{"df/a.txt", NormalizeFloat32([]float32{1, 0})},
		{"df/b.txt", NormalizeFloat32([]float32{0, 1})},
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

	query := NormalizeFloat32([]float32{1, 0})
	results, err := store.Search(ctx, "xyznoexist", query, 10, "")
	if err != nil {
		t.Fatalf("Search() error: %v", err)
	}
	if len(results) == 0 {
		t.Fatal("expected search results using doc embedding filter, got none")
	}
	found := false
	for _, r := range results {
		if r.DocumentPath == "df/a.txt" {
			found = true
		}
	}
	if !found {
		t.Fatal("expected df/a.txt in results via doc embedding filter")
	}
}

func TestVacuum_AfterDeleteDocument(t *testing.T) {
	dir := t.TempDir()
	store, err := NewStore(filepath.Join(dir, "quant.db"))
	if err != nil {
		t.Fatalf("NewStore() error: %v", err)
	}
	mustCloseStore(t, store)

	ctx := context.Background()
	_, err = store.UpsertDocument(ctx, &Document{
		Path:       "vac/del.txt",
		Hash:       "vh1",
		ModifiedAt: time.Now(),
	})
	if err != nil {
		t.Fatalf("UpsertDocument() error: %v", err)
	}

	if err := store.DeleteDocument(ctx, "vac/del.txt"); err != nil {
		t.Fatalf("DeleteDocument() error: %v", err)
	}

	if err := store.Vacuum(ctx); err != nil {
		t.Fatalf("Vacuum() error: %v", err)
	}
}

func TestCanRunVectorFallback_ZeroCap(t *testing.T) {
	dir := t.TempDir()
	store, err := NewStore(filepath.Join(dir, "quant.db"))
	if err != nil {
		t.Fatalf("NewStore() error: %v", err)
	}
	mustCloseStore(t, store)
	store.SetMaxVectorSearchCandidates(0)

	ctx := context.Background()
	ok, err := store.canRunVectorFallback(ctx, "")
	if err != nil {
		t.Fatalf("canRunVectorFallback() error: %v", err)
	}
	if ok {
		t.Fatal("expected false when max vector search candidates is 0")
	}
}

func TestCleanupOrphanedChunks_Direct(t *testing.T) {
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "quant.db")
	store, err := NewStore(dbPath)
	if err != nil {
		t.Fatalf("NewStore() error: %v", err)
	}
	store.db.SetMaxOpenConns(1)
	store.db.SetMaxIdleConns(1)

	ctx := context.Background()
	if _, err := store.db.ExecContext(ctx, `PRAGMA foreign_keys = OFF`); err != nil {
		t.Fatalf("disable FK error: %v", err)
	}

	docID, err := store.UpsertDocument(ctx, &Document{
		Path:       "orphan/orphan.txt",
		Hash:       "oh",
		ModifiedAt: time.Now(),
	})
	if err != nil {
		t.Fatalf("UpsertDocument() error: %v", err)
	}
	if err := store.InsertChunk(ctx, &ChunkRecord{
		DocumentID: docID,
		Content:    "orphan chunk",
		ChunkIndex: 0,
		Embedding:  EncodeFloat32([]float32{1}),
	}); err != nil {
		t.Fatalf("InsertChunk() error: %v", err)
	}
	if _, err := store.db.ExecContext(ctx, `DELETE FROM documents WHERE id = ?`, docID); err != nil {
		t.Fatalf("delete document error: %v", err)
	}

	if err := store.cleanupOrphanedChunks(ctx); err != nil {
		t.Fatalf("cleanupOrphanedChunks() error: %v", err)
	}

	docCount, chunkCount, err := store.Stats(ctx)
	if err != nil {
		t.Fatalf("Stats() error: %v", err)
	}
	if docCount != 0 || chunkCount != 0 {
		t.Fatalf("expected orphan cleanup, got %d docs, %d chunks", docCount, chunkCount)
	}

	if err := store.Close(); err != nil {
		t.Fatalf("Close() error: %v", err)
	}
}

func TestSearch_QueryChunksByDocPaths(t *testing.T) {
	dir := t.TempDir()
	store, err := NewStore(filepath.Join(dir, "quant.db"))
	if err != nil {
		t.Fatalf("NewStore() error: %v", err)
	}
	mustCloseStore(t, store)

	ctx := context.Background()
	if _, err := store.EnsureEmbeddingMetadata(ctx, EmbeddingMetadata{Model: "docpath-test", Dimensions: 2, Normalized: true}); err != nil {
		t.Fatalf("EnsureEmbeddingMetadata() error: %v", err)
	}

	for _, d := range []struct {
		path string
		vec  []float32
	}{
		{"dp/alpha.txt", NormalizeFloat32([]float32{1, 0})},
		{"dp/beta.txt", NormalizeFloat32([]float32{0, 1})},
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

	store.hnsw.ready.Store(false)

	query := NormalizeFloat32([]float32{1, 0})
	results, err := store.Search(ctx, "xyznoexist", query, 10, "")
	if err != nil {
		t.Fatalf("Search() with doc embed filter (no HNSW) error: %v", err)
	}
	if len(results) == 0 {
		t.Fatal("expected search results via doc embed filter path, got none")
	}
}

func TestHNSWReoptimizationNeeded(t *testing.T) {
	dir := t.TempDir()
	store, err := NewStore(filepath.Join(dir, "quant.db"))
	if err != nil {
		t.Fatalf("NewStore() error: %v", err)
	}
	mustCloseStore(t, store)

	if store.HNSWReoptimizationNeeded(0.5) {
		t.Fatal("expected no reoptimization needed when HNSW is not ready")
	}

	ctx := context.Background()
	if _, err := store.EnsureEmbeddingMetadata(ctx, EmbeddingMetadata{Model: "reopt", Dimensions: 2, Normalized: true}); err != nil {
		t.Fatalf("EnsureEmbeddingMetadata() error: %v", err)
	}

	doc := &Document{Path: "reopt/a.txt", Hash: "h1", ModifiedAt: time.Now()}
	if err := store.ReindexDocument(ctx, doc, []ChunkRecord{{
		Content:    "reopt content",
		ChunkIndex: 0,
		Embedding:  EncodeFloat32(NormalizeFloat32([]float32{1, 0})),
	}}); err != nil {
		t.Fatalf("ReindexDocument() error: %v", err)
	}

	if err := store.BuildHNSW(ctx); err != nil {
		t.Fatalf("BuildHNSW() error: %v", err)
	}

	if store.HNSWReoptimizationNeeded(0.9) {
		t.Fatal("expected no reoptimization needed with clean graph")
	}

	store.hnsw.Add(9999, []float32{0.5, 0.5})
	if !store.HNSWReoptimizationNeeded(0.01) {
		t.Fatal("expected reoptimization needed after mutation")
	}
}
