package mcp

import (
	"context"
	"sync"
	"testing"
)

type countingEmbedder struct {
	mu    sync.Mutex
	calls map[string]int
}

func (e *countingEmbedder) Embed(_ context.Context, text string) ([]float32, error) {
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.calls == nil {
		e.calls = make(map[string]int)
	}
	e.calls[text]++
	return []float32{1}, nil
}

func (e *countingEmbedder) EmbedBatch(_ context.Context, texts []string) ([][]float32, error) {
	out := make([][]float32, len(texts))
	for i, text := range texts {
		vec, err := e.Embed(context.Background(), text)
		if err != nil {
			return nil, err
		}
		out[i] = vec
	}
	return out, nil
}

func (e *countingEmbedder) Dimensions() int { return 1 }

func (e *countingEmbedder) Close() error { return nil }

func TestCachedEmbed_UsesLRUEviction(t *testing.T) {
	embedder := &countingEmbedder{}
	s := &Server{
		embedder: embedder,
		embCache: newEmbeddingLRU(2),
	}

	ctx := context.Background()
	if _, err := s.cachedEmbed(ctx, "a"); err != nil {
		t.Fatalf("unexpected embed error: %v", err)
	}
	if _, err := s.cachedEmbed(ctx, "b"); err != nil {
		t.Fatalf("unexpected embed error: %v", err)
	}
	if _, err := s.cachedEmbed(ctx, "a"); err != nil {
		t.Fatalf("unexpected embed error: %v", err)
	}
	if _, err := s.cachedEmbed(ctx, "c"); err != nil {
		t.Fatalf("unexpected embed error: %v", err)
	}
	if _, err := s.cachedEmbed(ctx, "a"); err != nil {
		t.Fatalf("unexpected embed error: %v", err)
	}
	if _, err := s.cachedEmbed(ctx, "b"); err != nil {
		t.Fatalf("unexpected embed error: %v", err)
	}

	embedder.mu.Lock()
	defer embedder.mu.Unlock()
	if embedder.calls["a"] != 1 {
		t.Fatalf("expected a to stay cached, got %d embed calls", embedder.calls["a"])
	}
	if embedder.calls["b"] != 2 {
		t.Fatalf("expected b to be evicted and recomputed, got %d embed calls", embedder.calls["b"])
	}
	if embedder.calls["c"] != 1 {
		t.Fatalf("expected c to be embedded once, got %d embed calls", embedder.calls["c"])
	}
}
