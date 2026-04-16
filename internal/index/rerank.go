package index

import (
	"context"
)

type Reranker interface {
	Rerank(ctx context.Context, query string, queryEmbedding []float32, results []SearchResult) ([]SearchResult, error)
	Name() string
}

type NoopReranker struct{}

func (r *NoopReranker) Rerank(_ context.Context, _ string, _ []float32, results []SearchResult) ([]SearchResult, error) {
	return results, nil
}

func (r *NoopReranker) Name() string { return "noop" }
