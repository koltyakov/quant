package index

import (
	"context"
	"errors"
	"path/filepath"
	"testing"
	"time"

	"github.com/coder/hnsw"
)

func TestHNSWIndexOperations(t *testing.T) {
	t.Parallel()

	idx := newHNSWIndex()
	idx.Add(1, []float32{1, 0})
	if idx.Len() != 0 || idx.Search([]float32{1, 0}, 1) != nil {
		t.Fatal("unready HNSW index should ignore mutations and searches")
	}

	idx.graph = newGraph(8, 16)
	idx.ready.Store(true)
	idx.Add(1, []float32{1, 0})
	idx.BatchAdd([]hnsw.Node[int]{
		hnsw.MakeNode(2, []float32{0.8, 0.2}),
		hnsw.MakeNode(3, []float32{0, 1}),
	})
	if idx.Len() != 3 {
		t.Fatalf("unexpected HNSW len after add: %d", idx.Len())
	}
	if idx.modCount() != 3 {
		t.Fatalf("unexpected HNSW mod count: %d", idx.modCount())
	}

	results := idx.Search([]float32{1, 0}, 2)
	if len(results) != 2 || results[0] != 1 {
		t.Fatalf("unexpected HNSW search results: %v", results)
	}

	idx.Delete(1)
	idx.BatchDelete([]int{2})
	if idx.Len() != 1 {
		t.Fatalf("unexpected HNSW len after deletes: %d", idx.Len())
	}
	idx.Delete(3)
	if results := idx.Search([]float32{1, 0}, 1); len(results) != 0 {
		t.Fatalf("empty HNSW search returned %v", results)
	}
	idx.Add(4, []float32{1, 0})
	if results := idx.Search([]float32{1, 0}, 1); len(results) != 1 || results[0] != 4 {
		t.Fatalf("HNSW did not recover after deleting all nodes: %v", results)
	}
	if idx.modCount() != 7 {
		t.Fatalf("unexpected HNSW mod count after delete and add: %d", idx.modCount())
	}

	idx.resetMods()
	if idx.modCount() != 0 {
		t.Fatalf("resetMods should clear mod count, got %d", idx.modCount())
	}

	graph := newGraph(4, 12)
	if graph.M != 4 || graph.EfSearch != 12 {
		t.Fatalf("unexpected graph tuning: M=%d EfSearch=%d", graph.M, graph.EfSearch)
	}
}

func TestBuildHNSW_WaitingBuildHonorsContext(t *testing.T) {
	store, err := NewStore(filepath.Join(t.TempDir(), "quant.db"))
	if err != nil {
		t.Fatalf("NewStore() error: %v", err)
	}
	mustCloseStore(t, store)

	store.hnsw.build <- struct{}{}
	defer func() { <-store.hnsw.build }()
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	if err := store.BuildHNSW(ctx); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("BuildHNSW() error = %v, want context deadline exceeded", err)
	}
}

func TestBuildHNSW_RetriesAfterGenerationChange(t *testing.T) {
	store, err := NewStore(filepath.Join(t.TempDir(), "quant.db"))
	if err != nil {
		t.Fatalf("NewStore() error: %v", err)
	}
	mustCloseStore(t, store)

	ctx := context.Background()
	if _, err := store.EnsureEmbeddingMetadata(ctx, EmbeddingMetadata{Model: "generation-retry", Dimensions: 2, Normalized: true}); err != nil {
		t.Fatalf("EnsureEmbeddingMetadata() error: %v", err)
	}
	docID, err := store.UpsertDocument(ctx, &Document{Path: "retry.txt", Hash: "h1", ModifiedAt: time.Now()})
	if err != nil {
		t.Fatalf("UpsertDocument() error: %v", err)
	}
	if err := store.InsertChunk(ctx, &ChunkRecord{
		DocumentID: docID, Content: "first", ChunkIndex: 0, Embedding: EncodeFloat32([]float32{1, 0}),
	}); err != nil {
		t.Fatalf("InsertChunk() error: %v", err)
	}

	// Hold publication after the snapshot has had time to complete, then make
	// that snapshot stale without taking writeMu.
	store.writeMu.Lock()
	locked := true
	defer func() {
		if locked {
			store.writeMu.Unlock()
		}
	}()
	errCh := make(chan error, 1)
	go func() { errCh <- store.BuildHNSW(ctx) }()
	deadline := time.Now().Add(time.Second)
	for len(store.hnsw.build) == 0 && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	if len(store.hnsw.build) == 0 {
		store.writeMu.Unlock()
		locked = false
		t.Fatal("BuildHNSW did not start")
	}
	time.Sleep(20 * time.Millisecond)
	if _, err := store.db.ExecContext(ctx,
		`INSERT INTO chunks (document_id, content, chunk_index, embedding) VALUES (?, ?, ?, ?)`,
		docID, "second", 1, EncodeFloat32([]float32{0, 1}),
	); err != nil {
		store.writeMu.Unlock()
		locked = false
		t.Fatalf("concurrent chunk insert error: %v", err)
	}
	store.writeMu.Unlock()
	locked = false

	if err := <-errCh; err != nil {
		t.Fatalf("BuildHNSW() error after generation change: %v", err)
	}
	if !store.HNSWReady() || store.HNSWLen() != 2 {
		t.Fatalf("rebuilt HNSW ready=%v len=%d, want ready with 2 nodes", store.HNSWReady(), store.HNSWLen())
	}
}
