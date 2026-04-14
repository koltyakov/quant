package index

import (
	"context"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"
)

func TestStoreChunkLookupCollectionsAndQuarantine(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	store, err := NewStore(filepath.Join(t.TempDir(), "quant.db"))
	if err != nil {
		t.Fatalf("NewStore returned error: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })

	if _, err := store.EnsureEmbeddingMetadata(ctx, EmbeddingMetadata{Model: "test", Dimensions: 2, Normalized: true}); err != nil {
		t.Fatalf("EnsureEmbeddingMetadata returned error: %v", err)
	}

	docs := []Document{
		{
			Path:       "alpha/one.md",
			Hash:       "alpha-one",
			ModifiedAt: time.Unix(1_700_000_000, 0).UTC(),
			Collection: "alpha",
		},
		{
			Path:       "alpha/two.md",
			Hash:       "alpha-two",
			ModifiedAt: time.Unix(1_700_000_100, 0).UTC(),
			Collection: "alpha",
		},
		{
			Path:       "beta/one.md",
			Hash:       "beta-one",
			ModifiedAt: time.Unix(1_700_000_200, 0).UTC(),
			Collection: "beta",
		},
	}
	vectors := [][]float32{{1, 0}, {0.9, 0.1}, {0, 1}}
	for i := range docs {
		if err := store.ReindexDocument(ctx, &docs[i], []ChunkRecord{{
			Content:    docs[i].Path + " content",
			ChunkIndex: 0,
			Embedding:  EncodeFloat32(NormalizeFloat32(vectors[i])),
		}}); err != nil {
			t.Fatalf("ReindexDocument(%s) returned error: %v", docs[i].Path, err)
		}
	}

	chunks, err := store.GetDocumentChunksByPath(ctx, "alpha/one.md")
	if err != nil || len(chunks) != 1 {
		t.Fatalf("GetDocumentChunksByPath returned chunks=%v err=%v", chunks, err)
	}
	var firstChunk ChunkRecord
	for _, chunk := range chunks {
		firstChunk = chunk
	}

	lookup, err := store.GetChunkByID(ctx, firstChunk.ID)
	if err != nil {
		t.Fatalf("GetChunkByID returned error: %v", err)
	}
	if lookup.DocumentPath != "alpha/one.md" || lookup.ChunkContent == "" {
		t.Fatalf("unexpected chunk lookup: %+v", lookup)
	}

	if got, want := ChunkDiffKey("same"), ChunkDiffKey("same"); got != want {
		t.Fatalf("ChunkDiffKey should be stable: %q vs %q", got, want)
	}

	store.hnsw = newHNSWIndex()
	store.hnswM = 8
	store.hnswEfSearch = 16
	store.hnsw.graph = newGraph(8, 16)
	store.hnsw.ready.Store(true)
	if err := store.BuildHNSW(ctx); err != nil {
		t.Fatalf("BuildHNSW returned error: %v", err)
	}
	if err := store.PingContext(ctx); err != nil {
		t.Fatalf("PingContext returned error: %v", err)
	}
	store.SetHNSWParams(12, 24)
	if store.hnswM != 12 || store.hnswEfSearch != 24 {
		t.Fatalf("unexpected HNSW params: m=%d ef=%d", store.hnswM, store.hnswEfSearch)
	}
	store.SetWeightOverrides(1.2, 0.8)
	if store.keywordWeightOverride != 1.2 || store.vectorWeightOverride != 0.8 {
		t.Fatalf("unexpected weight overrides: keyword=%f vector=%f", store.keywordWeightOverride, store.vectorWeightOverride)
	}
	store.SetReranker(&NoopReranker{})
	if store.reranker == nil {
		t.Fatal("expected reranker to be stored")
	}
	similar, err := store.FindSimilar(ctx, firstChunk.ID, 2)
	if err != nil {
		t.Fatalf("FindSimilar returned error: %v", err)
	}
	if len(similar) == 0 {
		t.Fatal("expected similar results after building HNSW")
	}
	store.hnsw.Add(999, []float32{0.7, 0.3})
	if !store.HNSWReoptimizationNeeded(0.1) {
		t.Fatal("expected HNSW reoptimization to be needed after mutations")
	}

	store.colbert = NewColBERTIndex(ColBERTConfig{Enabled: true, MaxTokens: 4})
	store.colbert.SetReady(true)
	store.colbert.Add(int(firstChunk.ID), [][]float32{{1, 0}})
	for key, chunk := range chunks {
		if key == ChunkDiffKey(firstChunk.Content) {
			continue
		}
		store.colbert.Add(int(chunk.ID), [][]float32{{0.8, 0.2}})
	}
	colbertResults, err := store.SearchColBERT(ctx, [][]float32{{1, 0}}, 2)
	if err != nil {
		t.Fatalf("SearchColBERT returned error: %v", err)
	}
	if len(colbertResults) == 0 || colbertResults[0].ChunkID != firstChunk.ID {
		t.Fatalf("unexpected ColBERT results: %+v", colbertResults)
	}
	if joined := joinInts([]int{1, 2, 3}, ","); joined != "1,2,3" {
		t.Fatalf("unexpected joinInts result: %q", joined)
	}

	collections, err := store.ListCollections(ctx)
	if err != nil {
		t.Fatalf("ListCollections returned error: %v", err)
	}
	if !reflect.DeepEqual(collections, []string{"alpha", "beta"}) {
		t.Fatalf("unexpected collections: %+v", collections)
	}

	docCount, chunkCount, err := store.CollectionStats(ctx, "alpha")
	if err != nil {
		t.Fatalf("CollectionStats returned error: %v", err)
	}
	if docCount != 2 || chunkCount != 2 {
		t.Fatalf("unexpected alpha stats: docs=%d chunks=%d", docCount, chunkCount)
	}

	if err := store.RenameDocumentPath(ctx, "beta/one.md", "beta/renamed.md"); err != nil {
		t.Fatalf("RenameDocumentPath returned error: %v", err)
	}
	if doc, err := store.GetDocumentByPath(ctx, "beta/renamed.md"); err != nil || doc == nil {
		t.Fatalf("expected renamed document lookup, got doc=%+v err=%v", doc, err)
	}

	if err := store.AddToQuarantine(ctx, "alpha/one.md", "failed once"); err != nil {
		t.Fatalf("AddToQuarantine returned error: %v", err)
	}
	if err := store.AddToQuarantine(ctx, "alpha/one.md", "failed twice"); err != nil {
		t.Fatalf("AddToQuarantine second call returned error: %v", err)
	}
	quarantined, err := store.IsQuarantined(ctx, "alpha/one.md")
	if err != nil || !quarantined {
		t.Fatalf("expected path to be quarantined, got %v err=%v", quarantined, err)
	}
	entries, err := store.ListQuarantined(ctx)
	if err != nil || len(entries) != 1 || entries[0].Attempts != 2 {
		t.Fatalf("unexpected quarantine entries: %+v err=%v", entries, err)
	}
	if err := store.RemoveFromQuarantine(ctx, "alpha/one.md"); err != nil {
		t.Fatalf("RemoveFromQuarantine returned error: %v", err)
	}
	if err := store.AddToQuarantine(ctx, "beta/renamed.md", "failed once"); err != nil {
		t.Fatalf("re-adding quarantine entry returned error: %v", err)
	}
	if err := store.ClearQuarantine(ctx); err != nil {
		t.Fatalf("ClearQuarantine returned error: %v", err)
	}
	if quarantined, err := store.IsQuarantined(ctx, "beta/renamed.md"); err != nil || quarantined {
		t.Fatalf("expected quarantine to be cleared, got %v err=%v", quarantined, err)
	}

	alphaTwo, err := store.GetDocumentByPath(ctx, "alpha/two.md")
	if err != nil || alphaTwo == nil {
		t.Fatalf("expected alpha/two document lookup, got %+v err=%v", alphaTwo, err)
	}
	parentDoc := &Document{
		Path:       "beta/parent.md",
		Hash:       "parent",
		ModifiedAt: time.Unix(1_700_000_300, 0).UTC(),
	}
	parentID, err := store.UpsertDocument(ctx, parentDoc)
	if err != nil {
		t.Fatalf("UpsertDocument(parent) returned error: %v", err)
	}
	if err := store.InsertChunk(ctx, &ChunkRecord{
		DocumentID: parentID,
		Content:    strings.Repeat("parent context ", 40),
		ChunkIndex: 0,
		Embedding:  EncodeFloat32(NormalizeFloat32([]float32{1, 0})),
	}); err != nil {
		t.Fatalf("InsertChunk(parent) returned error: %v", err)
	}
	parentChunks, err := store.GetDocumentChunksByPath(ctx, "beta/parent.md")
	if err != nil || len(parentChunks) != 1 {
		t.Fatalf("expected parent chunk, got %v err=%v", parentChunks, err)
	}
	var parent ChunkRecord
	for _, chunk := range parentChunks {
		parent = chunk
	}
	if err := store.InsertChunk(ctx, &ChunkRecord{
		DocumentID: alphaTwo.ID,
		Content:    "child",
		ChunkIndex: 1,
		Embedding:  EncodeFloat32(NormalizeFloat32([]float32{1, 0})),
		ParentID:   &parent.ID,
		Depth:      1,
	}); err != nil {
		t.Fatalf("InsertChunk(child) returned error: %v", err)
	}
	alphaTwoChunks, err := store.GetDocumentChunksByPath(ctx, "alpha/two.md")
	if err != nil {
		t.Fatalf("GetDocumentChunksByPath(alpha/two) returned error: %v", err)
	}
	var child ChunkRecord
	for _, chunk := range alphaTwoChunks {
		if chunk.ParentID != nil {
			child = chunk
		}
	}
	parentResult, err := store.GetParentChunk(ctx, child.ID)
	if err != nil || parentResult == nil || parentResult.ChunkID != parent.ID {
		t.Fatalf("unexpected parent chunk lookup: parent=%+v err=%v", parentResult, err)
	}
	enriched := store.EnrichWithParentContext(ctx, []SearchResult{{ChunkID: child.ID, ParentID: &parent.ID}})
	if len(enriched) != 1 || enriched[0].ParentContext == "" {
		t.Fatalf("expected parent context enrichment, got %+v", enriched)
	}
	if err := store.DeleteChunksByDocument(ctx, alphaTwo.ID); err != nil {
		t.Fatalf("DeleteChunksByDocument returned error: %v", err)
	}
	if remaining, err := store.GetDocumentChunksByPath(ctx, "alpha/two.md"); err != nil || len(remaining) != 0 {
		t.Fatalf("expected chunk deletion to leave no chunks, got %v err=%v", remaining, err)
	}

	if err := store.DeleteCollection(ctx, "alpha"); err != nil {
		t.Fatalf("DeleteCollection returned error: %v", err)
	}
	collections, err = store.ListCollections(ctx)
	if err != nil {
		t.Fatalf("ListCollections after delete returned error: %v", err)
	}
	if !reflect.DeepEqual(collections, []string{"beta"}) {
		t.Fatalf("unexpected collections after delete: %+v", collections)
	}
}
