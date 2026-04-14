package app

import (
	"context"

	"github.com/koltyakov/quant/internal/index"
	"github.com/koltyakov/quant/internal/ingest"
)

type summarizerAdapter struct {
	inner *index.ChunkSummarizer
}

func newSummarizerAdapter(inner *index.ChunkSummarizer) *summarizerAdapter {
	return &summarizerAdapter{inner: inner}
}

func (a *summarizerAdapter) SummarizeBatch(ctx context.Context, contents []string) ([]*ingest.ChunkSummary, error) {
	results, err := a.inner.SummarizeBatch(ctx, contents)
	if err != nil {
		return nil, err
	}
	out := make([]*ingest.ChunkSummary, len(results))
	for i, r := range results {
		if r != nil {
			out[i] = &ingest.ChunkSummary{Summary: r.Summary, Topics: r.Topics}
		}
	}
	return out, nil
}
