package index

import (
	"context"
	"path/filepath"
	"reflect"
	"testing"

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
	if idx.modCount() != 5 {
		t.Fatalf("unexpected HNSW mod count after deletes: %d", idx.modCount())
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

func TestDiskBackedHNSWFlushAndLoad(t *testing.T) {
	t.Parallel()

	graphPath := filepath.Join(t.TempDir(), "graph.bin")
	disk := NewDiskBackedHNSW(graphPath, 8, 16, 1)
	if disk.flushThresh != 1 {
		t.Fatalf("unexpected flush threshold: %d", disk.flushThresh)
	}
	if disk.Ready() {
		t.Fatal("new disk-backed HNSW should not be ready")
	}

	graph := newGraph(8, 16)
	graph.Add(hnsw.MakeNode(1, []float32{1, 0}))
	disk.SetGraph(graph)
	if !disk.Ready() || disk.Len() != 1 || disk.ModCount() != 0 {
		t.Fatalf("unexpected disk-backed HNSW initial state: ready=%v len=%d mods=%d", disk.Ready(), disk.Len(), disk.ModCount())
	}

	disk.Add(2, []float32{0.8, 0.2})
	if disk.Len() != 2 || disk.ModCount() != 1 {
		t.Fatalf("unexpected state after Add: len=%d mods=%d", disk.Len(), disk.ModCount())
	}
	if got := disk.Search([]float32{1, 0}, 2); len(got) != 2 || got[0] != 1 {
		t.Fatalf("unexpected disk-backed search results: %v", got)
	}

	loader := NewDiskBackedHNSW(graphPath, 8, 16, 1)
	if err := loader.LoadFromDisk(context.Background()); err != nil {
		t.Fatalf("LoadFromDisk returned error: %v", err)
	}
	if !loader.Ready() || loader.Len() != 2 || loader.ModCount() != 0 {
		t.Fatalf("unexpected loaded graph state: ready=%v len=%d mods=%d", loader.Ready(), loader.Len(), loader.ModCount())
	}

	loader.BatchAdd([]hnsw.Node[int]{hnsw.MakeNode(3, []float32{0, 1})})
	loader.BatchDelete([]int{1})
	loader.Delete(99)
	if loader.Len() != 2 {
		t.Fatalf("unexpected len after batch add/delete: %d", loader.Len())
	}
	if got := loader.Search([]float32{0, 1}, 2); !reflect.DeepEqual(got, []int{3, 2}) && !reflect.DeepEqual(got, []int{3, 1}) {
		t.Fatalf("unexpected post-update search results: %v", got)
	}

	empty := NewDiskBackedHNSW("", 8, 16, 0)
	if err := empty.LoadFromDisk(context.Background()); err != nil {
		t.Fatalf("LoadFromDisk with empty path should succeed, got %v", err)
	}
}
