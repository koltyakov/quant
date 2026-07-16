package index

import (
	"context"
	"path/filepath"
	"strings"
	"testing"
	"time"
	"unicode/utf8"
)

func TestGetParentChunk(t *testing.T) {
	dir := t.TempDir()
	store, err := NewStore(filepath.Join(dir, "quant.db"))
	if err != nil {
		t.Fatalf("NewStore() error: %v", err)
	}
	mustCloseStore(t, store)

	ctx := context.Background()
	docID, err := store.UpsertDocument(ctx, &Document{
		Path:       "parent/doc.txt",
		Hash:       "h1",
		ModifiedAt: time.Now(),
	})
	if err != nil {
		t.Fatalf("UpsertDocument() error: %v", err)
	}

	if err := store.InsertChunk(ctx, &ChunkRecord{
		DocumentID: docID,
		Content:    "parent chunk content",
		ChunkIndex: 0,
		Embedding:  EncodeFloat32(NormalizeFloat32([]float32{1, 0})),
	}); err != nil {
		t.Fatalf("InsertChunk(parent) error: %v", err)
	}

	chunks, err := store.GetDocumentChunksByPath(ctx, "parent/doc.txt")
	if err != nil {
		t.Fatalf("GetDocumentChunksByPath() error: %v", err)
	}
	var parentID int64
	for _, c := range chunks {
		parentID = c.ID
		break
	}

	childChunk := &ChunkRecord{
		DocumentID: docID,
		Content:    "child chunk content",
		ChunkIndex: 1,
		Embedding:  EncodeFloat32(NormalizeFloat32([]float32{0, 1})),
		ParentID:   &parentID,
		Depth:      1,
	}
	if err := store.InsertChunk(ctx, childChunk); err != nil {
		t.Fatalf("InsertChunk(child) error: %v", err)
	}

	childChunks, err := store.GetDocumentChunksByPath(ctx, "parent/doc.txt")
	if err != nil {
		t.Fatalf("GetDocumentChunksByPath() after child: %v", err)
	}
	var childChunkID int64
	for key, c := range childChunks {
		if c.Content == "child chunk content" {
			childChunkID = c.ID
			_ = key
			break
		}
	}
	if childChunkID == 0 {
		t.Fatal("could not find child chunk")
	}

	parent, err := store.GetParentChunk(ctx, childChunkID)
	if err != nil {
		t.Fatalf("GetParentChunk() error: %v", err)
	}
	if parent == nil {
		t.Fatal("expected parent chunk, got nil")
	}
	if parent.ChunkContent != "parent chunk content" {
		t.Fatalf("expected parent content 'parent chunk content', got %q", parent.ChunkContent)
	}

	noParentResult, err := store.GetParentChunk(ctx, parentID)
	if err != nil {
		t.Fatalf("GetParentChunk() for root chunk error: %v", err)
	}
	if noParentResult != nil {
		t.Fatal("expected nil for chunk with no parent")
	}
}

func TestGetParentChunk_NonexistentChunk(t *testing.T) {
	dir := t.TempDir()
	store, err := NewStore(filepath.Join(dir, "quant.db"))
	if err != nil {
		t.Fatalf("NewStore() error: %v", err)
	}
	mustCloseStore(t, store)

	ctx := context.Background()
	result, _ := store.GetParentChunk(ctx, 999999)
	if result != nil {
		t.Fatal("expected nil for nonexistent chunk")
	}
}

func TestEnrichWithParentContext_SharedParentAndUTF8Truncation(t *testing.T) {
	store, err := NewStore(filepath.Join(t.TempDir(), "quant.db"))
	if err != nil {
		t.Fatalf("NewStore() error: %v", err)
	}
	mustCloseStore(t, store)

	ctx := context.Background()
	docID, err := store.UpsertDocument(ctx, &Document{
		Path: "parent/shared.txt", Hash: "h1", ModifiedAt: time.Now(),
	})
	if err != nil {
		t.Fatalf("UpsertDocument() error: %v", err)
	}
	parentContent := strings.Repeat("a", 498) + "€tail"
	if err := store.InsertChunk(ctx, &ChunkRecord{
		DocumentID: docID, Content: parentContent, ChunkIndex: 0, Embedding: EncodeFloat32([]float32{1}),
	}); err != nil {
		t.Fatalf("InsertChunk(parent) error: %v", err)
	}
	chunks, err := store.GetDocumentChunksByPath(ctx, "parent/shared.txt")
	if err != nil {
		t.Fatalf("GetDocumentChunksByPath() error: %v", err)
	}
	var parentID int64
	for _, chunk := range chunks {
		parentID = chunk.ID
	}

	results := store.EnrichWithParentContext(ctx, []SearchResult{
		{ChunkID: 101, ParentID: &parentID},
		{ChunkID: 102, ParentID: &parentID},
		{ChunkID: 103, ParentID: &parentID, ParentContext: "already set"},
	})
	want := strings.Repeat("a", 498) + "..."
	for i := 0; i < 2; i++ {
		if results[i].ParentContext != want {
			t.Fatalf("result %d parent context = %q, want %q", i, results[i].ParentContext, want)
		}
		if !utf8.ValidString(results[i].ParentContext) {
			t.Fatalf("result %d parent context is invalid UTF-8", i)
		}
	}
	if results[2].ParentContext != "already set" {
		t.Fatalf("existing parent context was overwritten: %q", results[2].ParentContext)
	}
}

func TestFTSDiagnostics_WithContent(t *testing.T) {
	dir := t.TempDir()
	store, err := NewStore(filepath.Join(dir, "quant.db"))
	if err != nil {
		t.Fatalf("NewStore() error: %v", err)
	}
	mustCloseStore(t, store)

	ctx := context.Background()
	docID, err := store.UpsertDocument(ctx, &Document{
		Path:       "diag/test.txt",
		Hash:       "h1",
		ModifiedAt: time.Now(),
	})
	if err != nil {
		t.Fatalf("UpsertDocument() error: %v", err)
	}
	if err := store.InsertChunk(ctx, &ChunkRecord{
		DocumentID: docID,
		Content:    "diagnostics content",
		ChunkIndex: 0,
		Embedding:  EncodeFloat32([]float32{1}),
	}); err != nil {
		t.Fatalf("InsertChunk() error: %v", err)
	}

	diag, err := store.FTSDiagnostics(ctx)
	if err != nil {
		t.Fatalf("FTSDiagnostics() error: %v", err)
	}
	if diag.Empty {
		t.Fatal("expected non-empty FTS diagnostics after inserting content")
	}
	if diag.LogicalRows != 1 {
		t.Fatalf("expected 1 logical row, got %d", diag.LogicalRows)
	}
	if diag.DataRows < 2 {
		t.Fatalf("expected data_rows >= 2, got %d", diag.DataRows)
	}
}

func TestClassifyQueryWeights(t *testing.T) {
	tests := []struct {
		name            string
		query           string
		keywordOverride float32
		vectorOverride  float32
		wantMoreKeyword bool
		wantMoreVector  bool
	}{
		{
			name:            "empty query uses defaults",
			query:           "",
			keywordOverride: 0,
			vectorOverride:  0,
			wantMoreKeyword: false,
			wantMoreVector:  false,
		},
		{
			name:            "single short token upweights keyword",
			query:           "auth",
			keywordOverride: 0,
			vectorOverride:  0,
			wantMoreKeyword: true,
			wantMoreVector:  false,
		},
		{
			name:            "camelCase identifier upweights keyword",
			query:           "getUserName",
			keywordOverride: 0,
			vectorOverride:  0,
			wantMoreKeyword: true,
			wantMoreVector:  false,
		},
		{
			name:            "snake_case identifier upweights keyword",
			query:           "parse_config",
			keywordOverride: 0,
			vectorOverride:  0,
			wantMoreKeyword: true,
			wantMoreVector:  false,
		},
		{
			name:            "long natural language upweights vector",
			query:           "how do I implement authentication in my application",
			keywordOverride: 0,
			vectorOverride:  0,
			wantMoreKeyword: false,
			wantMoreVector:  true,
		},
		{
			name:            "mixed short and long tokens uses defaults",
			query:           "test something",
			keywordOverride: 0,
			vectorOverride:  0,
			wantMoreKeyword: false,
			wantMoreVector:  false,
		},
		{
			name:            "keyword override scales both weights",
			query:           "getUserName",
			keywordOverride: 3.0,
			vectorOverride:  0,
			wantMoreKeyword: true,
			wantMoreVector:  false,
		},
		{
			name:            "vector override scales both weights",
			query:           "how do I implement authentication",
			vectorOverride:  5.0,
			keywordOverride: 0,
			wantMoreKeyword: false,
			wantMoreVector:  true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			w := classifyQueryWeights(tt.query, tt.keywordOverride, tt.vectorOverride)
			if tt.wantMoreKeyword && w.Keyword <= w.Vector {
				t.Errorf("expected keyword > vector, got keyword=%v vector=%v", w.Keyword, w.Vector)
			}
			if tt.wantMoreVector && w.Vector <= w.Keyword {
				t.Errorf("expected vector > keyword, got keyword=%v vector=%v", w.Keyword, w.Vector)
			}
			if !tt.wantMoreKeyword && !tt.wantMoreVector {
				if w.Keyword <= 0 || w.Vector <= 0 {
					t.Errorf("expected positive weights, got keyword=%v vector=%v", w.Keyword, w.Vector)
				}
			}
		})
	}
}

func TestHNSWLen_NotReady(t *testing.T) {
	dir := t.TempDir()
	store, err := NewStore(filepath.Join(dir, "quant.db"))
	if err != nil {
		t.Fatalf("NewStore() error: %v", err)
	}
	mustCloseStore(t, store)

	if store.HNSWLen() != 0 {
		t.Fatalf("expected HNSWLen 0 when not ready, got %d", store.HNSWLen())
	}
	if store.HNSWReady() {
		t.Fatal("expected HNSWReady false when not built")
	}
}

func TestHNSWLen_AfterBuild(t *testing.T) {
	dir := t.TempDir()
	store, err := NewStore(filepath.Join(dir, "quant.db"))
	if err != nil {
		t.Fatalf("NewStore() error: %v", err)
	}
	mustCloseStore(t, store)

	ctx := context.Background()
	if _, err := store.EnsureEmbeddingMetadata(ctx, EmbeddingMetadata{Model: "len-test", Dimensions: 2, Normalized: true}); err != nil {
		t.Fatalf("EnsureEmbeddingMetadata() error: %v", err)
	}

	for _, d := range []struct {
		path string
		vec  []float32
	}{
		{"len/a.txt", NormalizeFloat32([]float32{1, 0})},
		{"len/b.txt", NormalizeFloat32([]float32{0, 1})},
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

	if !store.HNSWReady() {
		t.Fatal("expected HNSWReady after build")
	}
	if store.HNSWLen() != 2 {
		t.Fatalf("expected HNSWLen 2, got %d", store.HNSWLen())
	}
}

func TestHNSWLen_NilHNSW(t *testing.T) {
	s := &Store{}
	if s.HNSWLen() != 0 {
		t.Fatalf("expected HNSWLen 0 for nil hnsw, got %d", s.HNSWLen())
	}
	if s.HNSWReady() {
		t.Fatal("expected HNSWReady false for nil hnsw")
	}
}

func TestMigrateCollectionColumn_Populated(t *testing.T) {
	dir := t.TempDir()
	store, err := NewStore(filepath.Join(dir, "quant.db"))
	if err != nil {
		t.Fatalf("NewStore() error: %v", err)
	}
	mustCloseStore(t, store)

	ctx := context.Background()
	_, err = store.UpsertDocument(ctx, &Document{
		Path:       "col/test.txt",
		Hash:       "h1",
		ModifiedAt: time.Now(),
		Collection: "testcol",
	})
	if err != nil {
		t.Fatalf("UpsertDocument() error: %v", err)
	}

	doc, err := store.GetDocumentByPath(ctx, "col/test.txt")
	if err != nil {
		t.Fatalf("GetDocumentByPath() error: %v", err)
	}
	if doc == nil {
		t.Fatal("expected document, got nil")
	}
	if doc.Collection != "testcol" {
		t.Fatalf("expected collection 'testcol', got %q", doc.Collection)
	}
}

func TestReindexDocumentWithDeferredHNSW_HNSWReady(t *testing.T) {
	dir := t.TempDir()
	store, err := NewStore(filepath.Join(dir, "quant.db"))
	if err != nil {
		t.Fatalf("NewStore() error: %v", err)
	}
	mustCloseStore(t, store)

	ctx := context.Background()
	if _, err := store.EnsureEmbeddingMetadata(ctx, EmbeddingMetadata{Model: "defer-hnsw", Dimensions: 2, Normalized: true}); err != nil {
		t.Fatalf("EnsureEmbeddingMetadata() error: %v", err)
	}

	if err := store.BuildHNSW(ctx); err != nil {
		t.Fatalf("BuildHNSW() error: %v", err)
	}

	callbackCalled := false
	doc := &Document{Path: "defer/hnsw.txt", Hash: "h1", ModifiedAt: time.Now()}
	if err := store.ReindexDocumentWithDeferredHNSW(ctx, doc, []ChunkRecord{{
		Content:    "deferred with hnsw",
		ChunkIndex: 0,
		Embedding:  EncodeFloat32(NormalizeFloat32([]float32{1, 0})),
	}}, func() {
		callbackCalled = true
	}); err != nil {
		t.Fatalf("ReindexDocumentWithDeferredHNSW() error: %v", err)
	}
	if !callbackCalled {
		t.Fatal("expected callback to be called")
	}

	got, err := store.GetDocumentByPath(ctx, "defer/hnsw.txt")
	if err != nil {
		t.Fatalf("GetDocumentByPath() error: %v", err)
	}
	if got == nil || got.Hash != "h1" {
		t.Fatalf("expected document with hash h1, got %+v", got)
	}
}

func TestVacuum_AfterReindex(t *testing.T) {
	dir := t.TempDir()
	store, err := NewStore(filepath.Join(dir, "quant.db"))
	if err != nil {
		t.Fatalf("NewStore() error: %v", err)
	}
	mustCloseStore(t, store)

	ctx := context.Background()
	if _, err := store.EnsureEmbeddingMetadata(ctx, EmbeddingMetadata{Model: "vac-reindex", Dimensions: 2, Normalized: true}); err != nil {
		t.Fatalf("EnsureEmbeddingMetadata() error: %v", err)
	}

	doc := &Document{Path: "vac/reindex.txt", Hash: "h1", ModifiedAt: time.Now()}
	if err := store.ReindexDocument(ctx, doc, []ChunkRecord{{
		Content:    "vacuum reindex test",
		ChunkIndex: 0,
		Embedding:  EncodeFloat32(NormalizeFloat32([]float32{1, 0})),
	}}); err != nil {
		t.Fatalf("ReindexDocument() error: %v", err)
	}

	if err := store.Vacuum(ctx); err != nil {
		t.Fatalf("Vacuum() after reindex error: %v", err)
	}

	docCount, chunkCount, err := store.Stats(ctx)
	if err != nil {
		t.Fatalf("Stats() error: %v", err)
	}
	if docCount != 1 || chunkCount != 1 {
		t.Fatalf("expected 1 doc, 1 chunk after vacuum, got %d docs, %d chunks", docCount, chunkCount)
	}
}

func TestCleanupOrphanedChunks_WithParent(t *testing.T) {
	dir := t.TempDir()
	store, err := NewStore(filepath.Join(dir, "quant.db"))
	if err != nil {
		t.Fatalf("NewStore() error: %v", err)
	}
	store.db.SetMaxOpenConns(1)
	store.db.SetMaxIdleConns(1)

	ctx := context.Background()
	if _, err := store.db.ExecContext(ctx, `PRAGMA foreign_keys = OFF`); err != nil {
		t.Fatalf("disable FK error: %v", err)
	}

	doc1ID, err := store.UpsertDocument(ctx, &Document{
		Path:       "orphan/a.txt",
		Hash:       "ha",
		ModifiedAt: time.Now(),
	})
	if err != nil {
		t.Fatalf("UpsertDocument() error: %v", err)
	}
	doc2ID, err := store.UpsertDocument(ctx, &Document{
		Path:       "orphan/b.txt",
		Hash:       "hb",
		ModifiedAt: time.Now(),
	})
	if err != nil {
		t.Fatalf("UpsertDocument() error: %v", err)
	}

	if err := store.InsertChunk(ctx, &ChunkRecord{
		DocumentID: doc1ID,
		Content:    "chunk a",
		ChunkIndex: 0,
		Embedding:  EncodeFloat32([]float32{1}),
	}); err != nil {
		t.Fatalf("InsertChunk() error: %v", err)
	}
	if err := store.InsertChunk(ctx, &ChunkRecord{
		DocumentID: doc2ID,
		Content:    "chunk b",
		ChunkIndex: 0,
		Embedding:  EncodeFloat32([]float32{1}),
	}); err != nil {
		t.Fatalf("InsertChunk() error: %v", err)
	}

	if _, err := store.db.ExecContext(ctx, `DELETE FROM documents WHERE id = ?`, doc1ID); err != nil {
		t.Fatalf("delete doc error: %v", err)
	}

	if err := store.cleanupOrphanedChunks(ctx); err != nil {
		t.Fatalf("cleanupOrphanedChunks() error: %v", err)
	}

	docCount, chunkCount, err := store.Stats(ctx)
	if err != nil {
		t.Fatalf("Stats() error: %v", err)
	}
	if docCount != 1 || chunkCount != 1 {
		t.Fatalf("expected 1 doc, 1 chunk after cleanup, got %d docs, %d chunks", docCount, chunkCount)
	}

	if err := store.Close(); err != nil {
		t.Fatalf("Close() error: %v", err)
	}
}

func TestDeleteChunksByDocument(t *testing.T) {
	dir := t.TempDir()
	store, err := NewStore(filepath.Join(dir, "quant.db"))
	if err != nil {
		t.Fatalf("NewStore() error: %v", err)
	}
	mustCloseStore(t, store)

	ctx := context.Background()
	docID, err := store.UpsertDocument(ctx, &Document{
		Path:       "delchunks/doc.txt",
		Hash:       "h1",
		ModifiedAt: time.Now(),
	})
	if err != nil {
		t.Fatalf("UpsertDocument() error: %v", err)
	}

	if err := store.InsertChunk(ctx, &ChunkRecord{
		DocumentID: docID,
		Content:    "chunk 1",
		ChunkIndex: 0,
		Embedding:  EncodeFloat32([]float32{1}),
	}); err != nil {
		t.Fatalf("InsertChunk() error: %v", err)
	}
	if err := store.InsertChunk(ctx, &ChunkRecord{
		DocumentID: docID,
		Content:    "chunk 2",
		ChunkIndex: 1,
		Embedding:  EncodeFloat32([]float32{1}),
	}); err != nil {
		t.Fatalf("InsertChunk() error: %v", err)
	}

	_, chunkCount, err := store.Stats(ctx)
	if err != nil {
		t.Fatalf("Stats() before delete error: %v", err)
	}
	if chunkCount != 2 {
		t.Fatalf("expected 2 chunks before delete, got %d", chunkCount)
	}

	if err := store.DeleteChunksByDocument(ctx, docID); err != nil {
		t.Fatalf("DeleteChunksByDocument() error: %v", err)
	}

	_, chunkCount, err = store.Stats(ctx)
	if err != nil {
		t.Fatalf("Stats() after delete error: %v", err)
	}
	if chunkCount != 0 {
		t.Fatalf("expected 0 chunks after delete, got %d", chunkCount)
	}
}

func TestRenameDocumentPath(t *testing.T) {
	dir := t.TempDir()
	store, err := NewStore(filepath.Join(dir, "quant.db"))
	if err != nil {
		t.Fatalf("NewStore() error: %v", err)
	}
	mustCloseStore(t, store)

	ctx := context.Background()
	_, err = store.UpsertDocument(ctx, &Document{
		Path:       "rename/old.txt",
		Hash:       "h1",
		ModifiedAt: time.Now(),
	})
	if err != nil {
		t.Fatalf("UpsertDocument() error: %v", err)
	}

	if err := store.RenameDocumentPath(ctx, "rename/old.txt", "rename/new.txt"); err != nil {
		t.Fatalf("RenameDocumentPath() error: %v", err)
	}

	doc, err := store.GetDocumentByPath(ctx, "rename/new.txt")
	if err != nil {
		t.Fatalf("GetDocumentByPath(new) error: %v", err)
	}
	if doc == nil {
		t.Fatal("expected document at new path")
	}

	oldDoc, err := store.GetDocumentByPath(ctx, "rename/old.txt")
	if err != nil {
		t.Fatalf("GetDocumentByPath(old) error: %v", err)
	}
	if oldDoc != nil {
		t.Fatal("expected nil at old path after rename")
	}
}

func TestClearQuarantine(t *testing.T) {
	dir := t.TempDir()
	store, err := NewStore(filepath.Join(dir, "quant.db"))
	if err != nil {
		t.Fatalf("NewStore() error: %v", err)
	}
	mustCloseStore(t, store)

	ctx := context.Background()
	if err := store.AddToQuarantine(ctx, "path/a.txt", "err a"); err != nil {
		t.Fatalf("AddToQuarantine() error: %v", err)
	}
	if err := store.AddToQuarantine(ctx, "path/b.txt", "err b"); err != nil {
		t.Fatalf("AddToQuarantine() error: %v", err)
	}

	if err := store.ClearQuarantine(ctx); err != nil {
		t.Fatalf("ClearQuarantine() error: %v", err)
	}

	entries, err := store.ListQuarantined(ctx)
	if err != nil {
		t.Fatalf("ListQuarantined() after clear error: %v", err)
	}
	if len(entries) != 0 {
		t.Fatalf("expected 0 entries after clear, got %d", len(entries))
	}
}

func TestDeleteCollection(t *testing.T) {
	dir := t.TempDir()
	store, err := NewStore(filepath.Join(dir, "quant.db"))
	if err != nil {
		t.Fatalf("NewStore() error: %v", err)
	}
	mustCloseStore(t, store)

	ctx := context.Background()
	for _, item := range []struct {
		path       string
		collection string
	}{
		{"dc/a.txt", "alpha"},
		{"dc/b.txt", "alpha"},
		{"dc/c.txt", "beta"},
	} {
		id, err := store.UpsertDocument(ctx, &Document{
			Path:       item.path,
			Hash:       "h-" + item.path,
			ModifiedAt: time.Now(),
			Collection: item.collection,
		})
		if err != nil {
			t.Fatalf("UpsertDocument(%s) error: %v", item.path, err)
		}
		if err := store.InsertChunk(ctx, &ChunkRecord{
			DocumentID: id,
			Content:    "chunk " + item.path,
			ChunkIndex: 0,
			Embedding:  EncodeFloat32([]float32{1}),
		}); err != nil {
			t.Fatalf("InsertChunk(%s) error: %v", item.path, err)
		}
	}

	if err := store.DeleteCollection(ctx, "alpha"); err != nil {
		t.Fatalf("DeleteCollection() error: %v", err)
	}

	docCount, chunkCount, err := store.Stats(ctx)
	if err != nil {
		t.Fatalf("Stats() error: %v", err)
	}
	if docCount != 1 || chunkCount != 1 {
		t.Fatalf("expected 1 doc, 1 chunk after collection delete, got %d docs, %d chunks", docCount, chunkCount)
	}

	collections, err := store.ListCollections(ctx)
	if err != nil {
		t.Fatalf("ListCollections() error: %v", err)
	}
	if len(collections) != 1 || collections[0] != "beta" {
		t.Fatalf("expected only 'beta' collection, got %v", collections)
	}
}

func TestSearchFiltered_WithCollectionFilter(t *testing.T) {
	dir := t.TempDir()
	store, err := NewStore(filepath.Join(dir, "quant.db"))
	if err != nil {
		t.Fatalf("NewStore() error: %v", err)
	}
	mustCloseStore(t, store)

	ctx := context.Background()

	id1, err := store.UpsertDocument(ctx, &Document{
		Path: "sf/a.txt", Hash: "h1", ModifiedAt: time.Now(),
		Collection: "myproject",
	})
	if err != nil {
		t.Fatalf("UpsertDocument() error: %v", err)
	}
	_, err = store.UpsertDocument(ctx, &Document{
		Path: "sf/b.txt", Hash: "h2", ModifiedAt: time.Now(),
		Collection: "other",
	})
	if err != nil {
		t.Fatalf("UpsertDocument() error: %v", err)
	}

	vecProject := NormalizeFloat32([]float32{1, 0})
	if err := store.InsertChunk(ctx, &ChunkRecord{
		DocumentID: id1, Content: "project alpha uniqueword", ChunkIndex: 0, Embedding: EncodeFloat32(vecProject),
	}); err != nil {
		t.Fatalf("InsertChunk() error: %v", err)
	}

	where, args := store.buildMetadataFilter(SearchFilter{Collection: "myproject"})
	if where == "" {
		t.Fatal("expected non-empty WHERE clause for collection filter")
	}
	if len(args) != 1 || args[0] != "myproject" {
		t.Fatalf("expected 1 arg 'myproject', got %v", args)
	}
}

func TestSearchFiltered_VectorCandidatesRespectCollectionFilter(t *testing.T) {
	dir := t.TempDir()
	store, err := NewStore(filepath.Join(dir, "quant.db"))
	if err != nil {
		t.Fatalf("NewStore() error: %v", err)
	}
	mustCloseStore(t, store)

	ctx := context.Background()
	if _, err := store.EnsureEmbeddingMetadata(ctx, EmbeddingMetadata{Model: "filter-vector", Dimensions: 2, Normalized: true}); err != nil {
		t.Fatalf("EnsureEmbeddingMetadata() error: %v", err)
	}

	for _, item := range []struct {
		path       string
		collection string
		vec        []float32
	}{
		{path: "filtered/alpha.txt", collection: "alpha", vec: NormalizeFloat32([]float32{1, 0})},
		{path: "filtered/beta.txt", collection: "beta", vec: NormalizeFloat32([]float32{0, 1})},
	} {
		doc := &Document{Path: item.path, Hash: item.path, ModifiedAt: time.Now(), Collection: item.collection}
		if err := store.ReindexDocument(ctx, doc, []ChunkRecord{{Content: item.path, ChunkIndex: 0, Embedding: EncodeFloat32(item.vec)}}); err != nil {
			t.Fatalf("ReindexDocument(%s) error: %v", item.path, err)
		}
	}

	query := NormalizeFloat32([]float32{0, 1})
	results, err := store.SearchFiltered(ctx, "", query, 1, "", SearchFilter{Collection: "alpha"})
	if err != nil {
		t.Fatalf("SearchFiltered() error: %v", err)
	}
	if len(results) != 1 || results[0].DocumentPath != "filtered/alpha.txt" {
		t.Fatalf("expected alpha result only, got %+v", results)
	}

	if err := store.BuildHNSW(ctx); err != nil {
		t.Fatalf("BuildHNSW() error: %v", err)
	}
	results, err = store.SearchFiltered(ctx, "", query, 1, "", SearchFilter{Collection: "alpha"})
	if err != nil {
		t.Fatalf("SearchFiltered() with HNSW error: %v", err)
	}
	if len(results) != 1 || results[0].DocumentPath != "filtered/alpha.txt" {
		t.Fatalf("expected alpha HNSW result only, got %+v", results)
	}
}

func TestDeleteDocumentsByPrefix_HNSWPreservesSiblingPrefixes(t *testing.T) {
	dir := t.TempDir()
	store, err := NewStore(filepath.Join(dir, "quant.db"))
	if err != nil {
		t.Fatalf("NewStore() error: %v", err)
	}
	mustCloseStore(t, store)

	ctx := context.Background()
	if _, err := store.EnsureEmbeddingMetadata(ctx, EmbeddingMetadata{Model: "prefix-hnsw", Dimensions: 2, Normalized: true}); err != nil {
		t.Fatalf("EnsureEmbeddingMetadata() error: %v", err)
	}
	for _, item := range []struct {
		path string
		vec  []float32
	}{
		{path: "foo/a.txt", vec: NormalizeFloat32([]float32{1, 0})},
		{path: "foo-bar/a.txt", vec: NormalizeFloat32([]float32{0, 1})},
	} {
		doc := &Document{Path: item.path, Hash: item.path, ModifiedAt: time.Now()}
		if err := store.ReindexDocument(ctx, doc, []ChunkRecord{{Content: item.path, ChunkIndex: 0, Embedding: EncodeFloat32(item.vec)}}); err != nil {
			t.Fatalf("ReindexDocument(%s) error: %v", item.path, err)
		}
	}
	if err := store.BuildHNSW(ctx); err != nil {
		t.Fatalf("BuildHNSW() error: %v", err)
	}

	if err := store.DeleteDocumentsByPrefix(ctx, "foo"); err != nil {
		t.Fatalf("DeleteDocumentsByPrefix() error: %v", err)
	}
	if got := store.HNSWLen(); got != 1 {
		t.Fatalf("expected one HNSW node to remain, got %d", got)
	}
	if doc, err := store.GetDocumentByPath(ctx, "foo-bar/a.txt"); err != nil || doc == nil {
		t.Fatalf("expected sibling prefix document to remain, doc=%+v err=%v", doc, err)
	}
}

func TestDeleteDocumentsByPrefix_ClearAllResetsRuntimeIndexes(t *testing.T) {
	dir := t.TempDir()
	store, err := NewStore(filepath.Join(dir, "quant.db"))
	if err != nil {
		t.Fatalf("NewStore() error: %v", err)
	}
	mustCloseStore(t, store)

	ctx := context.Background()
	if _, err := store.EnsureEmbeddingMetadata(ctx, EmbeddingMetadata{Model: "clear-runtime", Dimensions: 2, Normalized: true}); err != nil {
		t.Fatalf("EnsureEmbeddingMetadata() error: %v", err)
	}
	doc := &Document{Path: "runtime/a.txt", Hash: "h", ModifiedAt: time.Now()}
	if err := store.ReindexDocument(ctx, doc, []ChunkRecord{{Content: "runtime", ChunkIndex: 0, Embedding: EncodeFloat32(NormalizeFloat32([]float32{1, 0}))}}); err != nil {
		t.Fatalf("ReindexDocument() error: %v", err)
	}
	if err := store.BuildHNSW(ctx); err != nil {
		t.Fatalf("BuildHNSW() error: %v", err)
	}
	if store.docEmbeds.Len() == 0 || !store.HNSWReady() {
		t.Fatal("expected runtime indexes to be populated before delete-all")
	}

	if err := store.DeleteDocumentsByPrefix(ctx, "."); err != nil {
		t.Fatalf("DeleteDocumentsByPrefix(.) error: %v", err)
	}
	if store.docEmbeds.Len() != 0 {
		t.Fatalf("expected document embedding cache to be cleared, got %d", store.docEmbeds.Len())
	}
	if store.HNSWReady() {
		t.Fatal("expected HNSW to be marked not ready after delete-all")
	}
}

func TestEnrichWithParentContext(t *testing.T) {
	dir := t.TempDir()
	store, err := NewStore(filepath.Join(dir, "quant.db"))
	if err != nil {
		t.Fatalf("NewStore() error: %v", err)
	}
	mustCloseStore(t, store)

	ctx := context.Background()
	docID, err := store.UpsertDocument(ctx, &Document{
		Path:       "enrich/doc.txt",
		Hash:       "h1",
		ModifiedAt: time.Now(),
	})
	if err != nil {
		t.Fatalf("UpsertDocument() error: %v", err)
	}

	if err := store.InsertChunk(ctx, &ChunkRecord{
		DocumentID: docID,
		Content:    "parent section content",
		ChunkIndex: 0,
		Embedding:  EncodeFloat32(NormalizeFloat32([]float32{1, 0})),
	}); err != nil {
		t.Fatalf("InsertChunk(parent) error: %v", err)
	}

	chunks, err := store.GetDocumentChunksByPath(ctx, "enrich/doc.txt")
	if err != nil {
		t.Fatalf("GetDocumentChunksByPath() error: %v", err)
	}
	var parentID int64
	for _, c := range chunks {
		parentID = c.ID
		break
	}

	if err := store.InsertChunk(ctx, &ChunkRecord{
		DocumentID: docID,
		Content:    "child content",
		ChunkIndex: 1,
		Embedding:  EncodeFloat32(NormalizeFloat32([]float32{0, 1})),
		ParentID:   &parentID,
		Depth:      1,
	}); err != nil {
		t.Fatalf("InsertChunk(child) error: %v", err)
	}

	childChunks, err := store.GetDocumentChunksByPath(ctx, "enrich/doc.txt")
	if err != nil {
		t.Fatalf("GetDocumentChunksByPath() error: %v", err)
	}
	var childResult SearchResult
	for _, c := range childChunks {
		if c.Content == "child content" {
			childResult = SearchResult{
				DocumentPath: "enrich/doc.txt",
				ChunkContent: c.Content,
				ChunkIndex:   c.ChunkIndex,
				ChunkID:      c.ID,
				ParentID:     c.ParentID,
			}
			break
		}
	}

	enriched := store.EnrichWithParentContext(ctx, []SearchResult{childResult})
	if len(enriched) != 1 {
		t.Fatalf("expected 1 enriched result, got %d", len(enriched))
	}
	if enriched[0].ParentContext == "" {
		t.Fatal("expected ParentContext to be populated")
	}
	if enriched[0].ParentContext != "parent section content" {
		t.Fatalf("expected parent context 'parent section content', got %q", enriched[0].ParentContext)
	}

	noParentResults := []SearchResult{{DocumentPath: "enrich/doc.txt", ChunkContent: "parent section content"}}
	enrichedNoParent := store.EnrichWithParentContext(ctx, noParentResults)
	if len(enrichedNoParent) != 1 {
		t.Fatalf("expected 1 result, got %d", len(enrichedNoParent))
	}
	if enrichedNoParent[0].ParentContext != "" {
		t.Fatal("expected no ParentContext for result without ParentID")
	}
}

func TestSetWeightOverrides(t *testing.T) {
	dir := t.TempDir()
	store, err := NewStore(filepath.Join(dir, "quant.db"))
	if err != nil {
		t.Fatalf("NewStore() error: %v", err)
	}
	mustCloseStore(t, store)

	store.SetWeightOverrides(2.0, 0.5)
	if store.keywordWeightOverride != 2.0 {
		t.Fatalf("expected keywordWeightOverride 2.0, got %f", store.keywordWeightOverride)
	}
	if store.vectorWeightOverride != 0.5 {
		t.Fatalf("expected vectorWeightOverride 0.5, got %f", store.vectorWeightOverride)
	}
}

func TestSetReranker(t *testing.T) {
	dir := t.TempDir()
	store, err := NewStore(filepath.Join(dir, "quant.db"))
	if err != nil {
		t.Fatalf("NewStore() error: %v", err)
	}
	mustCloseStore(t, store)

	store.SetReranker(nil)
}

func TestHNSW_ResetAndRebuild(t *testing.T) {
	dir := t.TempDir()
	store, err := NewStore(filepath.Join(dir, "quant.db"))
	if err != nil {
		t.Fatalf("NewStore() error: %v", err)
	}
	mustCloseStore(t, store)

	ctx := context.Background()
	if _, err := store.EnsureEmbeddingMetadata(ctx, EmbeddingMetadata{Model: "reset-test", Dimensions: 2, Normalized: true}); err != nil {
		t.Fatalf("EnsureEmbeddingMetadata() error: %v", err)
	}

	doc := &Document{Path: "reset/a.txt", Hash: "h1", ModifiedAt: time.Now()}
	if err := store.ReindexDocument(ctx, doc, []ChunkRecord{{
		Content:    "reset content",
		ChunkIndex: 0,
		Embedding:  EncodeFloat32(NormalizeFloat32([]float32{1, 0})),
	}}); err != nil {
		t.Fatalf("ReindexDocument() error: %v", err)
	}

	if err := store.BuildHNSW(ctx); err != nil {
		t.Fatalf("BuildHNSW() error: %v", err)
	}
	if !store.HNSWReady() {
		t.Fatal("expected HNSW ready after build")
	}

	if err := store.DeleteDocument(ctx, "reset/a.txt"); err != nil {
		t.Fatalf("DeleteDocument() error: %v", err)
	}
	docCount, _, err := store.Stats(ctx)
	if err != nil {
		t.Fatalf("Stats() error: %v", err)
	}
	if docCount != 0 {
		t.Fatalf("expected 0 docs after delete, got %d", docCount)
	}
}

func TestEnrichWithParentContext_LongContent(t *testing.T) {
	dir := t.TempDir()
	store, err := NewStore(filepath.Join(dir, "quant.db"))
	if err != nil {
		t.Fatalf("NewStore() error: %v", err)
	}
	mustCloseStore(t, store)

	ctx := context.Background()
	docID, err := store.UpsertDocument(ctx, &Document{
		Path:       "longpar/doc.txt",
		Hash:       "h1",
		ModifiedAt: time.Now(),
	})
	if err != nil {
		t.Fatalf("UpsertDocument() error: %v", err)
	}

	longContent := make([]byte, 600)
	for i := range longContent {
		longContent[i] = 'x'
	}
	if err := store.InsertChunk(ctx, &ChunkRecord{
		DocumentID: docID,
		Content:    string(longContent),
		ChunkIndex: 0,
		Embedding:  EncodeFloat32(NormalizeFloat32([]float32{1, 0})),
	}); err != nil {
		t.Fatalf("InsertChunk(parent) error: %v", err)
	}

	chunks, err := store.GetDocumentChunksByPath(ctx, "longpar/doc.txt")
	if err != nil {
		t.Fatalf("GetDocumentChunksByPath() error: %v", err)
	}
	var parentID int64
	for _, c := range chunks {
		parentID = c.ID
		break
	}

	if err := store.InsertChunk(ctx, &ChunkRecord{
		DocumentID: docID,
		Content:    "child",
		ChunkIndex: 1,
		Embedding:  EncodeFloat32(NormalizeFloat32([]float32{0, 1})),
		ParentID:   &parentID,
		Depth:      1,
	}); err != nil {
		t.Fatalf("InsertChunk(child) error: %v", err)
	}

	childChunks, err := store.GetDocumentChunksByPath(ctx, "longpar/doc.txt")
	if err != nil {
		t.Fatalf("GetDocumentChunksByPath() error: %v", err)
	}
	var childResult SearchResult
	for _, c := range childChunks {
		if c.Content == "child" {
			childResult = SearchResult{
				DocumentPath: "longpar/doc.txt",
				ChunkContent: c.Content,
				ChunkIndex:   c.ChunkIndex,
				ChunkID:      c.ID,
				ParentID:     c.ParentID,
			}
			break
		}
	}

	enriched := store.EnrichWithParentContext(ctx, []SearchResult{childResult})
	if len(enriched) != 1 {
		t.Fatalf("expected 1 enriched result, got %d", len(enriched))
	}
	if len(enriched[0].ParentContext) > 504 {
		t.Fatalf("expected parent context to be truncated, got len %d", len(enriched[0].ParentContext))
	}
	if enriched[0].ParentContext == "" {
		t.Fatal("expected ParentContext to be populated")
	}
}

func TestEnrichWithParentContext_EmptyResults(t *testing.T) {
	dir := t.TempDir()
	store, err := NewStore(filepath.Join(dir, "quant.db"))
	if err != nil {
		t.Fatalf("NewStore() error: %v", err)
	}
	mustCloseStore(t, store)

	ctx := context.Background()
	enriched := store.EnrichWithParentContext(ctx, nil)
	if enriched != nil {
		t.Fatalf("expected nil for empty input, got %v", enriched)
	}

	enriched = store.EnrichWithParentContext(ctx, []SearchResult{})
	if len(enriched) != 0 {
		t.Fatalf("expected empty slice for empty input, got %d", len(enriched))
	}
}

func TestReindexDocumentWithDeferredHNSW_NilCallback(t *testing.T) {
	dir := t.TempDir()
	store, err := NewStore(filepath.Join(dir, "quant.db"))
	if err != nil {
		t.Fatalf("NewStore() error: %v", err)
	}
	mustCloseStore(t, store)

	ctx := context.Background()

	doc := &Document{Path: "nocb/doc.txt", Hash: "h1", ModifiedAt: time.Now()}
	if err := store.ReindexDocumentWithDeferredHNSW(ctx, doc, []ChunkRecord{{
		Content:    "no callback content",
		ChunkIndex: 0,
		Embedding:  EncodeFloat32(NormalizeFloat32([]float32{1})),
	}}, nil); err != nil {
		t.Fatalf("ReindexDocumentWithDeferredHNSW(nil cb) error: %v", err)
	}

	got, err := store.GetDocumentByPath(ctx, "nocb/doc.txt")
	if err != nil {
		t.Fatalf("GetDocumentByPath() error: %v", err)
	}
	if got == nil || got.Hash != "h1" {
		t.Fatalf("expected document with hash h1, got %+v", got)
	}

	docCount, chunkCount, err := store.Stats(ctx)
	if err != nil {
		t.Fatalf("Stats() error: %v", err)
	}
	if docCount != 1 || chunkCount != 1 {
		t.Fatalf("expected 1 doc, 1 chunk, got %d docs, %d chunks", docCount, chunkCount)
	}
}

func TestPingContext(t *testing.T) {
	dir := t.TempDir()
	store, err := NewStore(filepath.Join(dir, "quant.db"))
	if err != nil {
		t.Fatalf("NewStore() error: %v", err)
	}
	mustCloseStore(t, store)

	ctx := context.Background()
	if err := store.PingContext(ctx); err != nil {
		t.Fatalf("PingContext() error: %v", err)
	}
}

func TestSetHNSWParams(t *testing.T) {
	dir := t.TempDir()
	store, err := NewStore(filepath.Join(dir, "quant.db"))
	if err != nil {
		t.Fatalf("NewStore() error: %v", err)
	}
	mustCloseStore(t, store)

	store.SetHNSWParams(32, 200)
	if store.hnswM != 32 {
		t.Fatalf("expected hnswM 32, got %d", store.hnswM)
	}
	if store.hnswEfSearch != 200 {
		t.Fatalf("expected hnswEfSearch 200, got %d", store.hnswEfSearch)
	}

	store.SetHNSWParams(0, 0)
	if store.hnswM != 32 {
		t.Fatalf("expected hnswM unchanged at 32 after zero, got %d", store.hnswM)
	}
	if store.hnswEfSearch != 200 {
		t.Fatalf("expected hnswEfSearch unchanged at 200 after zero, got %d", store.hnswEfSearch)
	}
}

func TestDeleteDocumentsByPrefix_NonexistentPrefix(t *testing.T) {
	dir := t.TempDir()
	store, err := NewStore(filepath.Join(dir, "quant.db"))
	if err != nil {
		t.Fatalf("NewStore() error: %v", err)
	}
	mustCloseStore(t, store)

	ctx := context.Background()
	id, err := store.UpsertDocument(ctx, &Document{
		Path:       "exists/doc.txt",
		Hash:       "h1",
		ModifiedAt: time.Now(),
	})
	if err != nil {
		t.Fatalf("UpsertDocument() error: %v", err)
	}
	if err := store.InsertChunk(ctx, &ChunkRecord{
		DocumentID: id,
		Content:    "keep this",
		ChunkIndex: 0,
		Embedding:  EncodeFloat32([]float32{1}),
	}); err != nil {
		t.Fatalf("InsertChunk() error: %v", err)
	}

	if err := store.DeleteDocumentsByPrefix(ctx, "nonexistent"); err != nil {
		t.Fatalf("DeleteDocumentsByPrefix() error: %v", err)
	}

	docCount, chunkCount, err := store.Stats(ctx)
	if err != nil {
		t.Fatalf("Stats() error: %v", err)
	}
	if docCount != 1 || chunkCount != 1 {
		t.Fatalf("expected 1 doc, 1 chunk (unchanged), got %d docs, %d chunks", docCount, chunkCount)
	}
}

func TestReindexDocumentWithDeferredHNSW_UpdateExisting(t *testing.T) {
	dir := t.TempDir()
	store, err := NewStore(filepath.Join(dir, "quant.db"))
	if err != nil {
		t.Fatalf("NewStore() error: %v", err)
	}
	mustCloseStore(t, store)

	ctx := context.Background()
	if _, err := store.EnsureEmbeddingMetadata(ctx, EmbeddingMetadata{Model: "update-defer", Dimensions: 2, Normalized: true}); err != nil {
		t.Fatalf("EnsureEmbeddingMetadata() error: %v", err)
	}

	doc := &Document{Path: "defer/update.txt", Hash: "h1", ModifiedAt: time.Now()}
	if err := store.ReindexDocument(ctx, doc, []ChunkRecord{{
		Content:    "original content",
		ChunkIndex: 0,
		Embedding:  EncodeFloat32(NormalizeFloat32([]float32{1, 0})),
	}}); err != nil {
		t.Fatalf("ReindexDocument() error: %v", err)
	}

	updatedDoc := &Document{Path: "defer/update.txt", Hash: "h2", ModifiedAt: time.Now()}
	if err := store.ReindexDocumentWithDeferredHNSW(ctx, updatedDoc, []ChunkRecord{{
		Content:    "updated content",
		ChunkIndex: 0,
		Embedding:  EncodeFloat32(NormalizeFloat32([]float32{0, 1})),
	}}, nil); err != nil {
		t.Fatalf("ReindexDocumentWithDeferredHNSW() error: %v", err)
	}

	got, err := store.GetDocumentByPath(ctx, "defer/update.txt")
	if err != nil {
		t.Fatalf("GetDocumentByPath() error: %v", err)
	}
	if got == nil || got.Hash != "h2" {
		t.Fatalf("expected document with hash h2, got %+v", got)
	}

	results, err := store.Search(ctx, "updated", NormalizeFloat32([]float32{0, 1}), 5, "")
	if err != nil {
		t.Fatalf("Search() error: %v", err)
	}
	if len(results) == 0 {
		t.Fatal("expected search results for updated content")
	}
	if results[0].ChunkContent != "updated content" {
		t.Fatalf("expected 'updated content', got %q", results[0].ChunkContent)
	}
}

func TestDeleteCollection_WithHNSW(t *testing.T) {
	dir := t.TempDir()
	store, err := NewStore(filepath.Join(dir, "quant.db"))
	if err != nil {
		t.Fatalf("NewStore() error: %v", err)
	}
	mustCloseStore(t, store)

	ctx := context.Background()
	if _, err := store.EnsureEmbeddingMetadata(ctx, EmbeddingMetadata{Model: "delcol", Dimensions: 2, Normalized: true}); err != nil {
		t.Fatalf("EnsureEmbeddingMetadata() error: %v", err)
	}

	for _, d := range []struct {
		path       string
		collection string
		vec        []float32
	}{
		{"dc/alpha.txt", "alpha", NormalizeFloat32([]float32{1, 0})},
		{"dc/beta.txt", "beta", NormalizeFloat32([]float32{0, 1})},
	} {
		doc := &Document{Path: d.path, Hash: "h-" + d.path, ModifiedAt: time.Now(), Collection: d.collection}
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

	if err := store.DeleteCollection(ctx, "alpha"); err != nil {
		t.Fatalf("DeleteCollection() error: %v", err)
	}

	docCount, chunkCount, err := store.Stats(ctx)
	if err != nil {
		t.Fatalf("Stats() error: %v", err)
	}
	if docCount != 1 || chunkCount != 1 {
		t.Fatalf("expected 1 doc, 1 chunk after collection delete, got %d docs, %d chunks", docCount, chunkCount)
	}
}

func TestEncodeInt8_Format(t *testing.T) {
	vec := []float32{0.5, -0.3, 0.0, 1.0, -1.0}
	encoded := EncodeInt8(vec)
	if len(encoded) == 0 {
		t.Fatal("expected non-empty encoded data")
	}
	if len(encoded) != 8+len(vec) {
		t.Fatalf("expected %d bytes, got %d", 8+len(vec), len(encoded))
	}
}

func TestFTSDiagnostics_AfterDelete(t *testing.T) {
	dir := t.TempDir()
	store, err := NewStore(filepath.Join(dir, "quant.db"))
	if err != nil {
		t.Fatalf("NewStore() error: %v", err)
	}
	mustCloseStore(t, store)

	ctx := context.Background()
	docID, err := store.UpsertDocument(ctx, &Document{
		Path:       "ftsd/del.txt",
		Hash:       "h1",
		ModifiedAt: time.Now(),
	})
	if err != nil {
		t.Fatalf("UpsertDocument() error: %v", err)
	}
	if err := store.InsertChunk(ctx, &ChunkRecord{
		DocumentID: docID,
		Content:    "to be deleted",
		ChunkIndex: 0,
		Embedding:  EncodeFloat32([]float32{1}),
	}); err != nil {
		t.Fatalf("InsertChunk() error: %v", err)
	}

	if err := store.DeleteDocument(ctx, "ftsd/del.txt"); err != nil {
		t.Fatalf("DeleteDocument() error: %v", err)
	}

	diag, err := store.FTSDiagnostics(ctx)
	if err != nil {
		t.Fatalf("FTSDiagnostics() error: %v", err)
	}
	if !diag.Empty {
		t.Fatal("expected empty FTS after deletion")
	}
}
