package index

import (
	"context"
	"fmt"
	"math"
	"math/rand/v2"
	"testing"
)

func BenchmarkSearch_Small(b *testing.B) {
	benchmarkSearch(b, 50, 10) // 50 docs, 10 chunks each
}

func BenchmarkSearch_Medium(b *testing.B) {
	benchmarkSearch(b, 200, 20) // 200 docs, 20 chunks each
}

func benchmarkSearch(b *testing.B, numDocs, chunksPerDoc int) {
	b.Helper()

	dir := b.TempDir()
	store, err := NewStore(dir + "/bench.db")
	if err != nil {
		b.Fatalf("unexpected store error: %v", err)
	}
	b.Cleanup(func() { _ = store.Close() })

	const dims = 384
	ctx := context.Background()

	// Seed the database with documents and chunks.
	for d := range numDocs {
		doc := &Document{
			Path: fmt.Sprintf("doc%d.txt", d),
			Hash: fmt.Sprintf("hash%d", d),
		}
		chunks := make([]ChunkRecord, chunksPerDoc)
		for c := range chunksPerDoc {
			vec := randomVector(dims)
			chunks[c] = ChunkRecord{
				Content:    fmt.Sprintf("chunk %d of document %d with searchable content about topic %d", c, d, d%10),
				ChunkIndex: c,
				Embedding:  EncodeFloat32(NormalizeFloat32(vec)),
			}
		}
		if err := store.ReindexDocument(ctx, doc, chunks); err != nil {
			b.Fatalf("unexpected seed error: %v", err)
		}
	}

	queryVec := NormalizeFloat32(randomVector(dims))

	b.ResetTimer()
	for b.Loop() {
		_, err := store.Search(ctx, "searchable content topic", queryVec, 5, "")
		if err != nil {
			b.Fatalf("unexpected search error: %v", err)
		}
	}
}

func BenchmarkDotProductEncoded(b *testing.B) {
	const dims = 384
	a := NormalizeFloat32(randomVector(dims))
	bVec := EncodeFloat32(NormalizeFloat32(randomVector(dims)))

	b.ResetTimer()
	for b.Loop() {
		dotProductEncoded(a, bVec)
	}
}

func BenchmarkNormalizeFloat32(b *testing.B) {
	vec := randomVector(384)

	b.ResetTimer()
	for b.Loop() {
		NormalizeFloat32(vec)
	}
}

func randomVector(dims int) []float32 {
	vec := make([]float32, dims)
	for i := range vec {
		vec[i] = rand.Float32()*2 - 1
	}
	// Ensure non-zero norm.
	var norm float32
	for _, v := range vec {
		norm += v * v
	}
	if norm == 0 {
		vec[0] = 1
	}
	_ = math.Sqrt(float64(norm)) // force use
	return vec
}
