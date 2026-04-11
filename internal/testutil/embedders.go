package testutil

import (
	"context"
	"sync"
	"sync/atomic"
)

type StaticEmbedder struct{}

func (StaticEmbedder) Embed(context.Context, string) ([]float32, error) {
	return []float32{1}, nil
}

func (StaticEmbedder) EmbedBatch(_ context.Context, texts []string) ([][]float32, error) {
	out := make([][]float32, len(texts))
	for i := range texts {
		out[i] = []float32{1}
	}
	return out, nil
}

func (StaticEmbedder) Dimensions() int { return 1 }

func (StaticEmbedder) Close() error { return nil }

type BatchCountingEmbedder struct {
	BatchCalls atomic.Int32
}

func (e *BatchCountingEmbedder) Embed(context.Context, string) ([]float32, error) {
	return []float32{1}, nil
}

func (e *BatchCountingEmbedder) EmbedBatch(_ context.Context, texts []string) ([][]float32, error) {
	e.BatchCalls.Add(1)
	out := make([][]float32, len(texts))
	for i := range texts {
		out[i] = []float32{1}
	}
	return out, nil
}

func (e *BatchCountingEmbedder) Dimensions() int { return 1 }

func (e *BatchCountingEmbedder) Close() error { return nil }

type ShortBatchEmbedder struct{}

func (ShortBatchEmbedder) Embed(context.Context, string) ([]float32, error) {
	return []float32{1}, nil
}

func (ShortBatchEmbedder) EmbedBatch(_ context.Context, texts []string) ([][]float32, error) {
	if len(texts) == 0 {
		return nil, nil
	}
	return [][]float32{{1}}, nil
}

func (ShortBatchEmbedder) Dimensions() int { return 1 }

func (ShortBatchEmbedder) Close() error { return nil }

type QueryCountingEmbedder struct {
	Mu    sync.Mutex
	Calls map[string]int
}

func (e *QueryCountingEmbedder) Embed(_ context.Context, text string) ([]float32, error) {
	e.Mu.Lock()
	defer e.Mu.Unlock()
	if e.Calls == nil {
		e.Calls = make(map[string]int)
	}
	e.Calls[text]++
	return []float32{1}, nil
}

func (e *QueryCountingEmbedder) EmbedBatch(_ context.Context, texts []string) ([][]float32, error) {
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

func (e *QueryCountingEmbedder) Dimensions() int { return 1 }

func (e *QueryCountingEmbedder) Close() error { return nil }

type CancelAwareEmbedder struct {
	Started  chan struct{}
	Release  chan struct{}
	Canceled chan struct{}

	Mu         sync.Mutex
	Calls      map[string]int
	once       sync.Once
	cancelOnce sync.Once
}

func (e *CancelAwareEmbedder) Embed(ctx context.Context, text string) ([]float32, error) {
	e.Mu.Lock()
	if e.Calls == nil {
		e.Calls = make(map[string]int)
	}
	e.Calls[text]++
	e.Mu.Unlock()

	e.once.Do(func() { close(e.Started) })

	select {
	case <-ctx.Done():
		if e.Canceled != nil {
			e.cancelOnce.Do(func() { close(e.Canceled) })
		}
		return nil, ctx.Err()
	case <-e.Release:
		return []float32{1}, nil
	}
}

func (e *CancelAwareEmbedder) EmbedBatch(_ context.Context, texts []string) ([][]float32, error) {
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

func (e *CancelAwareEmbedder) Dimensions() int { return 1 }

func (e *CancelAwareEmbedder) Close() error { return nil }
