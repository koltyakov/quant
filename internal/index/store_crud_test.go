package index

import (
	"context"
	"path/filepath"
	"testing"
	"time"
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

func TestNewColBERTIndex(t *testing.T) {
	idx := NewColBERTIndex(ColBERTConfig{Enabled: true})
	if idx == nil {
		t.Fatal("expected non-nil ColBERTIndex")
	}
	if !idx.config.Enabled {
		t.Fatal("expected enabled config")
	}
	if idx.config.MaxTokens != colBERTMaxTokens {
		t.Fatalf("expected default MaxTokens %d, got %d", colBERTMaxTokens, idx.config.MaxTokens)
	}
}

func TestNewColBERTIndex_DefaultMaxTokens(t *testing.T) {
	idx := NewColBERTIndex(ColBERTConfig{Enabled: true, MaxTokens: 0})
	if idx.config.MaxTokens != colBERTMaxTokens {
		t.Fatalf("expected default MaxTokens %d when set to 0, got %d", colBERTMaxTokens, idx.config.MaxTokens)
	}
}

func TestNewColBERTIndex_CustomMaxTokens(t *testing.T) {
	idx := NewColBERTIndex(ColBERTConfig{Enabled: true, MaxTokens: 64})
	if idx.config.MaxTokens != 64 {
		t.Fatalf("expected MaxTokens 64, got %d", idx.config.MaxTokens)
	}
}

func TestNewColBERTIndex_AddSearchRemove(t *testing.T) {
	idx := NewColBERTIndex(ColBERTConfig{Enabled: true, MaxTokens: 10})
	idx.SetReady(true)

	tokens := [][]float32{{0.1, 0.2}, {0.3, 0.4}}
	idx.Add(1, tokens)
	if idx.Len() != 1 {
		t.Fatalf("expected len 1 after Add, got %d", idx.Len())
	}

	results := idx.Search([][]float32{{0.1, 0.2}}, 5)
	if len(results) != 1 {
		t.Fatalf("expected 1 search result, got %d", len(results))
	}
	if results[0].ChunkID != 1 {
		t.Fatalf("expected ChunkID 1, got %d", results[0].ChunkID)
	}

	idx.Remove(1)
	if idx.Len() != 0 {
		t.Fatalf("expected len 0 after Remove, got %d", idx.Len())
	}
}

func TestColBERT_Disabled(t *testing.T) {
	idx := NewColBERTIndex(ColBERTConfig{Enabled: false})
	idx.Add(1, [][]float32{{0.1, 0.2}})
	if idx.Len() != 0 {
		t.Fatalf("expected len 0 when disabled, got %d", idx.Len())
	}
}

func TestColBERT_SearchNotReady(t *testing.T) {
	idx := NewColBERTIndex(ColBERTConfig{Enabled: true, MaxTokens: 10})
	results := idx.Search([][]float32{{0.1, 0.2}}, 5)
	if results != nil {
		t.Fatalf("expected nil results when not ready, got %v", results)
	}
}

func TestColBERT_SearchEmptyQuery(t *testing.T) {
	idx := NewColBERTIndex(ColBERTConfig{Enabled: true, MaxTokens: 10})
	idx.SetReady(true)
	results := idx.Search(nil, 5)
	if results != nil {
		t.Fatalf("expected nil results for nil query, got %v", results)
	}
}

func TestNewFeedbackStore(t *testing.T) {
	fs := NewFeedbackStore(100)
	if fs == nil {
		t.Fatal("expected non-nil FeedbackStore")
	}
	totalEvents, totalSelected := fs.Stats()
	if totalEvents != 0 || totalSelected != 0 {
		t.Fatalf("expected empty store, got %d events, %d selected", totalEvents, totalSelected)
	}
}

func TestNewFeedbackStore_DefaultCapacity(t *testing.T) {
	fs := NewFeedbackStore(0)
	if fs == nil {
		t.Fatal("expected non-nil FeedbackStore")
	}
	if fs.maxCap != 10000 {
		t.Fatalf("expected default maxCap 10000, got %d", fs.maxCap)
	}
}

func TestFeedbackStore_RecordAndStats(t *testing.T) {
	fs := NewFeedbackStore(100)

	fs.Record(FeedbackEvent{Query: "test", DocPath: "a.go", Selected: true, Position: 1})
	fs.Record(FeedbackEvent{Query: "test", DocPath: "b.go", Selected: false, Position: 2})

	totalEvents, totalSelected := fs.Stats()
	if totalEvents != 2 {
		t.Fatalf("expected 2 events, got %d", totalEvents)
	}
	if totalSelected != 1 {
		t.Fatalf("expected 1 selected, got %d", totalSelected)
	}
}

func TestFeedbackStore_ComputePathBoosts(t *testing.T) {
	fs := NewFeedbackStore(100)

	boosts := fs.ComputePathBoosts()
	if boosts != nil {
		t.Fatal("expected nil boosts with no events")
	}

	fs.Record(FeedbackEvent{Query: "q", DocPath: "a.go", Selected: true})
	fs.Record(FeedbackEvent{Query: "q", DocPath: "a.go", Selected: true})
	fs.Record(FeedbackEvent{Query: "q", DocPath: "b.go", Selected: false})

	boosts = fs.ComputePathBoosts()
	if len(boosts) != 1 {
		t.Fatalf("expected 1 boost entry, got %d", len(boosts))
	}
	if boosts[0].Path != "a.go" {
		t.Fatalf("expected path a.go, got %s", boosts[0].Path)
	}
}

func TestFeedbackStore_CapacityOverflow(t *testing.T) {
	fs := NewFeedbackStore(2)

	fs.Record(FeedbackEvent{Query: "q1", DocPath: "a.go", Selected: true})
	fs.Record(FeedbackEvent{Query: "q2", DocPath: "b.go", Selected: true})
	fs.Record(FeedbackEvent{Query: "q3", DocPath: "c.go", Selected: false})

	totalEvents, _ := fs.Stats()
	if totalEvents != 2 {
		t.Fatalf("expected 2 events after overflow, got %d", totalEvents)
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

func TestEncodeDecodeTokenEmbeddings(t *testing.T) {
	tokenEmbs := [][]float32{{0.1, 0.2, 0.3}, {0.4, 0.5, 0.6}}
	encoded := EncodeTokenEmbeddings(tokenEmbs)
	if len(encoded) == 0 {
		t.Fatal("expected non-empty encoded data")
	}

	decoded := DecodeTokenEmbeddings(encoded)
	if decoded == nil {
		t.Fatal("expected non-nil decoded data")
	}
	if len(decoded) != 2 {
		t.Fatalf("expected 2 tokens, got %d", len(decoded))
	}
	if len(decoded[0]) != 3 {
		t.Fatalf("expected 3 dims per token, got %d", len(decoded[0]))
	}
}

func TestDecodeTokenEmbeddings_InvalidData(t *testing.T) {
	tests := []struct {
		name string
		data []byte
	}{
		{"nil data", nil},
		{"too short", []byte{0, 0, 0}},
		{"bad magic", []byte{0, 0, 0, 0, 1, 0, 0, 0, 1, 0, 0, 0}},
		{"bad version", []byte{0x42, 0x4C, 0x4F, 0x43, 0x02, 0, 0, 0, 1, 0, 0, 0}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := DecodeTokenEmbeddings(tt.data)
			if result != nil {
				t.Fatalf("expected nil for %s, got %v", tt.name, result)
			}
		})
	}
}

func TestEncodeTokenEmbeddings_Empty(t *testing.T) {
	result := EncodeTokenEmbeddings(nil)
	if result != nil {
		t.Fatalf("expected nil for empty token embeddings, got %v", result)
	}
	result = EncodeTokenEmbeddings([][]float32{})
	if result != nil {
		t.Fatalf("expected nil for empty slice, got %v", result)
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

func TestFeedbackStore_RecordAutoTimestamp(t *testing.T) {
	fs := NewFeedbackStore(100)

	fs.Record(FeedbackEvent{Query: "q", DocPath: "a.go"})
	totalEvents, _ := fs.Stats()
	if totalEvents != 1 {
		t.Fatalf("expected 1 event, got %d", totalEvents)
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

func TestSignalNamesAndWeights(t *testing.T) {
	ks := &KeywordSignal{}
	if ks.Name() != "keyword" {
		t.Fatalf("expected keyword signal name 'keyword', got %q", ks.Name())
	}
	if ks.Weight() != 1.0 {
		t.Fatalf("expected keyword default weight 1.0, got %f", ks.Weight())
	}
	ksWithOverride := &KeywordSignal{WeightOverride: 2.5}
	if ksWithOverride.Weight() != 2.5 {
		t.Fatalf("expected keyword override weight 2.5, got %f", ksWithOverride.Weight())
	}

	vs := &VectorSignal{}
	if vs.Name() != "vector" {
		t.Fatalf("expected vector signal name 'vector', got %q", vs.Name())
	}
	if vs.Weight() != 1.0 {
		t.Fatalf("expected vector default weight 1.0, got %f", vs.Weight())
	}

	rs := &RecencySignal{HalfLife: 24 * time.Hour}
	if rs.Name() != "recency" {
		t.Fatalf("expected recency signal name 'recency', got %q", rs.Name())
	}
	if rs.Weight() != recencyBoostWeight {
		t.Fatalf("expected recency default weight %f, got %f", recencyBoostWeight, rs.Weight())
	}

	rsOverride := &RecencySignal{HalfLife: 24 * time.Hour, WeightOverride: 0.5}
	if rsOverride.Weight() != 0.5 {
		t.Fatalf("expected recency override weight 0.5, got %f", rsOverride.Weight())
	}

	ps := &PathMatchSignal{}
	if ps.Name() != "path_match" {
		t.Fatalf("expected path_match signal name, got %q", ps.Name())
	}
	if ps.Weight() != 1.0 {
		t.Fatalf("expected path_match default weight 1.0, got %f", ps.Weight())
	}

	fs := &FileTypeSignal{Extensions: map[string]float32{".go": 1.5}, Default: 0.5}
	if fs.Name() != "file_type" {
		t.Fatalf("expected file_type signal name, got %q", fs.Name())
	}
	if fs.Weight() != 1.0 {
		t.Fatalf("expected file_type weight 1.0, got %f", fs.Weight())
	}
}

func TestRecencySignal_ScoreWithZeroDate(t *testing.T) {
	rs := &RecencySignal{HalfLife: 24 * time.Hour}
	ctx := &SignalContext{
		Query:       "test",
		QueryTokens: []string{"test"},
		Now:         time.Now(),
		Weights:     QuerySignalWeights{Keyword: 1, Vector: 1},
	}
	candidate := &ScoredCandidate{modifiedAt: time.Time{}}
	score := rs.Score(ctx, candidate)
	if score != 0 {
		t.Fatalf("expected 0 score for zero modifiedAt, got %f", score)
	}
}

func TestRecencySignal_ScoreWithFutureDate(t *testing.T) {
	rs := &RecencySignal{HalfLife: 24 * time.Hour}
	ctx := &SignalContext{
		Query:       "test",
		QueryTokens: []string{"test"},
		Now:         time.Now(),
		Weights:     QuerySignalWeights{Keyword: 1, Vector: 1},
	}
	candidate := &ScoredCandidate{modifiedAt: time.Now().Add(24 * time.Hour)}
	score := rs.Score(ctx, candidate)
	if score <= 0 {
		t.Fatalf("expected positive score for future modifiedAt, got %f", score)
	}
}

func TestRecencySignal_ScoreWithOverride(t *testing.T) {
	rs := &RecencySignal{HalfLife: 24 * time.Hour, WeightOverride: 2.0}
	ctx := &SignalContext{
		Query:       "test",
		QueryTokens: []string{"test"},
		Now:         time.Now(),
		Weights:     QuerySignalWeights{Keyword: 1, Vector: 1},
	}
	candidate := &ScoredCandidate{modifiedAt: time.Now().Add(-1 * time.Hour)}
	score := rs.Score(ctx, candidate)
	if score <= 0 {
		t.Fatalf("expected positive score with override, got %f", score)
	}
}

func TestPathMatchSignal_ScoreWithMatch(t *testing.T) {
	ps := &PathMatchSignal{}
	ctx := &SignalContext{
		Query:       "main.go",
		QueryTokens: []string{"main", "go"},
		Now:         time.Now(),
		Weights:     QuerySignalWeights{Keyword: 1, Vector: 1},
	}
	candidate := &ScoredCandidate{result: SearchResult{DocumentPath: "src/main.go"}}
	score := ps.Score(ctx, candidate)
	if score <= 0 {
		t.Fatalf("expected positive score for path match, got %f", score)
	}
}

func TestPathMatchSignal_ScoreNoMatch(t *testing.T) {
	ps := &PathMatchSignal{}
	ctx := &SignalContext{
		Query:       "readme",
		QueryTokens: []string{"readme"},
		Now:         time.Now(),
		Weights:     QuerySignalWeights{Keyword: 1, Vector: 1},
	}
	candidate := &ScoredCandidate{result: SearchResult{DocumentPath: "src/main.go"}}
	score := ps.Score(ctx, candidate)
	if score != 0 {
		t.Fatalf("expected 0 score for no path match, got %f", score)
	}
}

func TestPathMatchSignal_EmptyTokens(t *testing.T) {
	ps := &PathMatchSignal{}
	ctx := &SignalContext{
		Query:       "",
		QueryTokens: nil,
		Now:         time.Now(),
		Weights:     QuerySignalWeights{Keyword: 1, Vector: 1},
	}
	candidate := &ScoredCandidate{result: SearchResult{DocumentPath: "src/main.go"}}
	score := ps.Score(ctx, candidate)
	if score != 0 {
		t.Fatalf("expected 0 score for empty tokens, got %f", score)
	}
}

func TestFileTypeSignal_Score(t *testing.T) {
	fs := &FileTypeSignal{
		Extensions: map[string]float32{".go": 1.5, ".py": 1.2},
		Default:    0.5,
	}
	ctx := &SignalContext{
		Query:       "test",
		QueryTokens: []string{"test"},
		Now:         time.Now(),
		Weights:     QuerySignalWeights{Keyword: 1, Vector: 1},
	}

	goScore := fs.Score(ctx, &ScoredCandidate{result: SearchResult{DocumentPath: "src/main.go"}})
	pyScore := fs.Score(ctx, &ScoredCandidate{result: SearchResult{DocumentPath: "src/app.py"}})
	txtScore := fs.Score(ctx, &ScoredCandidate{result: SearchResult{DocumentPath: "docs/readme.txt"}})

	if goScore <= 0 {
		t.Fatalf("expected positive score for .go, got %f", goScore)
	}
	if pyScore <= 0 {
		t.Fatalf("expected positive score for .py, got %f", pyScore)
	}
	if txtScore <= 0 {
		t.Fatalf("expected positive score for .txt default, got %f", txtScore)
	}
	if goScore <= txtScore {
		t.Fatalf("expected .go boost > default, got go=%f default=%f", goScore, txtScore)
	}
}

func TestKeywordSignal_ScoreWithOverride(t *testing.T) {
	ks := &KeywordSignal{WeightOverride: 3.0}
	ctx := &SignalContext{
		Query:       "test",
		QueryTokens: []string{"test"},
		Weights:     QuerySignalWeights{Keyword: 1.0, Vector: 1.0},
	}
	candidate := scoredCandidate{keywordRank: 1}
	score := ks.Score(ctx, &candidate)
	if score <= 0 {
		t.Fatalf("expected positive score with override, got %f", score)
	}
}

func TestRegisterSignal_Custom(t *testing.T) {
	custom := &FileTypeSignal{Extensions: map[string]float32{".rs": 2.0}, Default: 0.3}
	RegisterSignal(custom)

	signals := DefaultSignalRegistry.List()
	found := false
	for _, s := range signals {
		if s.Name() == "file_type" {
			found = true
		}
	}
	if !found {
		t.Fatal("expected file_type signal in registry after RegisterSignal")
	}
}

func TestSortByScore(t *testing.T) {
	candidates := []scoredCandidate{
		{result: SearchResult{ChunkContent: "low"}, score: 0.1},
		{result: SearchResult{ChunkContent: "high"}, score: 0.9},
		{result: SearchResult{ChunkContent: "mid"}, score: 0.5},
	}
	SortByScore(candidates)
	if candidates[0].score != 0.9 {
		t.Fatalf("expected highest score first, got %f", candidates[0].score)
	}
	if candidates[2].score != 0.1 {
		t.Fatalf("expected lowest score last, got %f", candidates[2].score)
	}
}

func TestProjectionLayer_RoundTrip(t *testing.T) {
	proj := NewRandomProjection(4, 2)
	if proj.InDims() != 4 {
		t.Fatalf("expected InDims 4, got %d", proj.InDims())
	}
	if proj.OutDims() != 2 {
		t.Fatalf("expected OutDims 2, got %d", proj.OutDims())
	}

	encoded := proj.Encode()
	loaded, err := LoadProjection(encoded)
	if err != nil {
		t.Fatalf("LoadProjection() error: %v", err)
	}
	if loaded.InDims() != 4 {
		t.Fatalf("expected loaded InDims 4, got %d", loaded.InDims())
	}
	if loaded.OutDims() != 2 {
		t.Fatalf("expected loaded OutDims 2, got %d", loaded.OutDims())
	}

	input := []float32{0.5, 0.3, 0.1, -0.2}
	output := loaded.Project(input)
	if len(output) != 2 {
		t.Fatalf("expected 2 output dims, got %d", len(output))
	}
}

func TestProjection_LayerProjectWrongDims(t *testing.T) {
	proj := NewRandomProjection(4, 2)
	result := proj.Project([]float32{1, 2})
	if result != nil {
		t.Fatalf("expected nil for wrong dims, got %v", result)
	}
}

func TestLoadProjection_Errors(t *testing.T) {
	_, err := LoadProjection([]byte{0, 0, 0})
	if err == nil {
		t.Fatal("expected error for too-small data")
	}

	_, err = LoadProjection([]byte{4, 0, 0, 0, 2, 0, 0, 0})
	if err == nil {
		t.Fatal("expected error for truncated data")
	}
}

func TestVectorSignal_WeightOverride(t *testing.T) {
	vs := &VectorSignal{WeightOverride: 2.0}
	if vs.Weight() != 2.0 {
		t.Fatalf("expected 2.0, got %f", vs.Weight())
	}

	vsDefault := &VectorSignal{}
	if vsDefault.Weight() != 1.0 {
		t.Fatalf("expected default weight 1.0, got %f", vsDefault.Weight())
	}
}

func TestKeywordSignal_WeightOverride(t *testing.T) {
	ks := &KeywordSignal{WeightOverride: 3.0}
	if ks.Weight() != 3.0 {
		t.Fatalf("expected 3.0, got %f", ks.Weight())
	}

	ksDefault := &KeywordSignal{}
	if ksDefault.Weight() != 1.0 {
		t.Fatalf("expected default weight 1.0, got %f", ksDefault.Weight())
	}
}

func TestPathMatchSignal_WeightOverride(t *testing.T) {
	ps := &PathMatchSignal{WeightOverride: 0.5}
	if ps.Weight() != 0.5 {
		t.Fatalf("expected 0.5, got %f", ps.Weight())
	}
}

func TestColBERT_SearchWithMultipleTokens(t *testing.T) {
	idx := NewColBERTIndex(ColBERTConfig{Enabled: true, MaxTokens: 128})
	idx.SetReady(true)

	idx.Add(1, [][]float32{{0.5, 0.5}, {0.3, 0.7}})
	idx.Add(2, [][]float32{{0.1, 0.9}, {0.8, 0.2}})

	results := idx.Search([][]float32{{0.5, 0.5}}, 5)
	if len(results) != 2 {
		t.Fatalf("expected 2 results, got %d", len(results))
	}
}

func TestColBERT_RemoveNonexistent(t *testing.T) {
	idx := NewColBERTIndex(ColBERTConfig{Enabled: true, MaxTokens: 10})
	idx.Remove(999)
	if idx.Len() != 0 {
		t.Fatalf("expected len 0 after removing nonexistent, got %d", idx.Len())
	}
}

func TestProjection_SaveAndLoad(t *testing.T) {
	dir := t.TempDir()
	store, err := NewStore(filepath.Join(dir, "quant.db"))
	if err != nil {
		t.Fatalf("NewStore() error: %v", err)
	}
	mustCloseStore(t, store)

	ctx := context.Background()
	proj := NewRandomProjection(4, 2)
	if err := store.SaveProjection(ctx, proj); err != nil {
		t.Fatalf("SaveProjection() error: %v", err)
	}

	loaded, err := store.LoadProjection(ctx)
	if err != nil {
		t.Fatalf("LoadProjection() error: %v", err)
	}
	if loaded.InDims() != 4 {
		t.Fatalf("expected InDims 4, got %d", loaded.InDims())
	}
	if loaded.OutDims() != 2 {
		t.Fatalf("expected OutDims 2, got %d", loaded.OutDims())
	}
}

func TestLoadProjection_NotFound(t *testing.T) {
	dir := t.TempDir()
	store, err := NewStore(filepath.Join(dir, "quant.db"))
	if err != nil {
		t.Fatalf("NewStore() error: %v", err)
	}
	mustCloseStore(t, store)

	ctx := context.Background()
	_, err = store.LoadProjection(ctx)
	if err == nil {
		t.Fatal("expected error when no projection exists")
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

func TestMigrateEmbeddingsWithProjection(t *testing.T) {
	dir := t.TempDir()
	store, err := NewStore(filepath.Join(dir, "quant.db"))
	if err != nil {
		t.Fatalf("NewStore() error: %v", err)
	}
	mustCloseStore(t, store)

	ctx := context.Background()
	if _, err := store.EnsureEmbeddingMetadata(ctx, EmbeddingMetadata{Model: "proj-test", Dimensions: 4, Normalized: true}); err != nil {
		t.Fatalf("EnsureEmbeddingMetadata() error: %v", err)
	}

	doc := &Document{Path: "proj/a.txt", Hash: "h1", ModifiedAt: time.Now()}
	if err := store.ReindexDocument(ctx, doc, []ChunkRecord{{
		Content:    "projection test content",
		ChunkIndex: 0,
		Embedding:  EncodeFloat32(NormalizeFloat32([]float32{1, 0, 0, 0})),
	}}); err != nil {
		t.Fatalf("ReindexDocument() error: %v", err)
	}

	proj := NewRandomProjection(4, 2)
	if err := store.MigrateEmbeddingsWithProjection(ctx, proj); err != nil {
		t.Fatalf("MigrateEmbeddingsWithProjection() error: %v", err)
	}

	if _, err := store.EnsureEmbeddingMetadata(ctx, EmbeddingMetadata{Model: "proj-test", Dimensions: 2, Normalized: true}); err != nil {
		t.Fatalf("EnsureEmbeddingMetadata() after projection error: %v", err)
	}
}

func TestMigrateEmbeddingsWithProjection_DimsMismatch(t *testing.T) {
	dir := t.TempDir()
	store, err := NewStore(filepath.Join(dir, "quant.db"))
	if err != nil {
		t.Fatalf("NewStore() error: %v", err)
	}
	mustCloseStore(t, store)

	ctx := context.Background()
	if _, err := store.EnsureEmbeddingMetadata(ctx, EmbeddingMetadata{Model: "proj-mismatch", Dimensions: 4, Normalized: true}); err != nil {
		t.Fatalf("EnsureEmbeddingMetadata() error: %v", err)
	}

	proj := NewRandomProjection(8, 2)
	err = store.MigrateEmbeddingsWithProjection(ctx, proj)
	if err == nil {
		t.Fatal("expected error for dims mismatch")
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
