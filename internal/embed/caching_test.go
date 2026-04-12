package embed

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"
)

type queryCountingEmbedder struct {
	mu    sync.Mutex
	calls map[string]int
}

func (e *queryCountingEmbedder) Embed(_ context.Context, text string) ([]float32, error) {
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.calls == nil {
		e.calls = make(map[string]int)
	}
	e.calls[text]++
	return []float32{1}, nil
}

func (e *queryCountingEmbedder) EmbedBatch(_ context.Context, texts []string) ([][]float32, error) {
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

func (e *queryCountingEmbedder) Dimensions() int { return 1 }
func (e *queryCountingEmbedder) Close() error    { return nil }

type cancelAwareEmbedder struct {
	started    chan struct{}
	release    chan struct{}
	canceled   chan struct{}
	mu         sync.Mutex
	calls      map[string]int
	once       sync.Once
	cancelOnce sync.Once
}

func (e *cancelAwareEmbedder) Embed(ctx context.Context, text string) ([]float32, error) {
	e.mu.Lock()
	if e.calls == nil {
		e.calls = make(map[string]int)
	}
	e.calls[text]++
	e.mu.Unlock()

	e.once.Do(func() { close(e.started) })

	select {
	case <-ctx.Done():
		if e.canceled != nil {
			e.cancelOnce.Do(func() { close(e.canceled) })
		}
		return nil, ctx.Err()
	case <-e.release:
		return []float32{1}, nil
	}
}

func (e *cancelAwareEmbedder) EmbedBatch(_ context.Context, texts []string) ([][]float32, error) {
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

func (e *cancelAwareEmbedder) Dimensions() int { return 1 }
func (e *cancelAwareEmbedder) Close() error    { return nil }

func newTestCachingEmbedder(inner Embedder, cacheSize int) *CachingEmbedder {
	return NewCachingEmbedder(inner, CachingConfig{CacheSize: cacheSize})
}

func TestCachingEmbedder_UsesLRUEviction(t *testing.T) {
	inner := &queryCountingEmbedder{}
	c := newTestCachingEmbedder(inner, 2)

	ctx := context.Background()
	for _, q := range []string{"a", "b", "a", "c", "a", "b"} {
		if _, err := c.Embed(ctx, q); err != nil {
			t.Fatalf("unexpected embed error for %q: %v", q, err)
		}
	}

	inner.mu.Lock()
	defer inner.mu.Unlock()
	if inner.calls["a"] != 1 {
		t.Fatalf("expected a to stay cached, got %d embed calls", inner.calls["a"])
	}
	if inner.calls["b"] != 2 {
		t.Fatalf("expected b to be evicted and recomputed, got %d embed calls", inner.calls["b"])
	}
	if inner.calls["c"] != 1 {
		t.Fatalf("expected c to be embedded once, got %d embed calls", inner.calls["c"])
	}
}

func TestCachingEmbedder_DeduplicatesConcurrentRequests(t *testing.T) {
	inner := &queryCountingEmbedder{}
	c := newTestCachingEmbedder(inner, 2)

	ctx := context.Background()
	var wg sync.WaitGroup
	errCh := make(chan error, 8)
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, err := c.Embed(ctx, "same-query")
			errCh <- err
		}()
	}
	wg.Wait()
	close(errCh)

	for err := range errCh {
		if err != nil {
			t.Fatalf("unexpected embed error: %v", err)
		}
	}

	inner.mu.Lock()
	defer inner.mu.Unlock()
	if inner.calls["same-query"] != 1 {
		t.Fatalf("expected one embed call, got %d", inner.calls["same-query"])
	}
}

func TestCachingEmbedder_NormalizesWhitespaceInCacheKey(t *testing.T) {
	inner := &queryCountingEmbedder{}
	c := newTestCachingEmbedder(inner, 2)

	if _, err := c.Embed(context.Background(), "alpha   beta"); err != nil {
		t.Fatalf("unexpected embed error: %v", err)
	}
	if _, err := c.Embed(context.Background(), " alpha beta "); err != nil {
		t.Fatalf("unexpected embed error: %v", err)
	}

	inner.mu.Lock()
	defer inner.mu.Unlock()
	if inner.calls["alpha   beta"] != 1 {
		t.Fatalf("expected the first normalized query to embed once, got %d", inner.calls["alpha   beta"])
	}
	if inner.calls[" alpha beta "] != 0 {
		t.Fatalf("expected normalized cache hit for whitespace-only variant, got %d embeds", inner.calls[" alpha beta "])
	}
}

func TestCachingEmbedder_LeaderCancellationDoesNotAbortSharedFlight(t *testing.T) {
	inner := &cancelAwareEmbedder{
		started: make(chan struct{}),
		release: make(chan struct{}),
	}
	c := newTestCachingEmbedder(inner, 2)

	leaderCtx, cancelLeader := context.WithCancel(context.Background())
	leaderErrCh := make(chan error, 1)
	go func() {
		_, err := c.Embed(leaderCtx, "shared-query")
		leaderErrCh <- err
	}()

	<-inner.started

	followerErrCh := make(chan error, 1)
	go func() {
		_, err := c.Embed(context.Background(), "shared-query")
		followerErrCh <- err
	}()

	cancelLeader()
	if err := <-leaderErrCh; !errors.Is(err, context.Canceled) {
		t.Fatalf("expected leader to observe cancellation, got %v", err)
	}

	close(inner.release)

	if err := <-followerErrCh; err != nil {
		t.Fatalf("expected follower to reuse successful shared flight, got %v", err)
	}

	inner.mu.Lock()
	defer inner.mu.Unlock()
	if inner.calls["shared-query"] != 1 {
		t.Fatalf("expected one shared embed call, got %d", inner.calls["shared-query"])
	}
}

func TestCachingEmbedder_CanceledFlightCachesResultForNextCaller(t *testing.T) {
	inner := &cancelAwareEmbedder{
		started: make(chan struct{}),
		release: make(chan struct{}),
	}
	c := newTestCachingEmbedder(inner, 2)

	ctx, cancel := context.WithCancel(context.Background())
	firstErrCh := make(chan error, 1)
	go func() {
		_, err := c.Embed(ctx, "abandoned-query")
		firstErrCh <- err
	}()

	<-inner.started
	cancel()

	if err := <-firstErrCh; !errors.Is(err, context.Canceled) {
		t.Fatalf("expected cancellation error, got %v", err)
	}

	close(inner.release)

	// Allow the background goroutine to finish and cache the result.
	time.Sleep(50 * time.Millisecond)

	vec, err := c.Embed(context.Background(), "abandoned-query")
	if err != nil {
		t.Fatalf("expected cached result to be available, got %v", err)
	}
	if len(vec) == 0 {
		t.Fatal("expected non-empty embedding from cache")
	}

	inner.mu.Lock()
	defer inner.mu.Unlock()
	if inner.calls["abandoned-query"] != 1 {
		t.Fatalf("expected one embed call (not restarted), got %d calls", inner.calls["abandoned-query"])
	}
}

func TestCachingEmbedder_NormalizeFuncApplied(t *testing.T) {
	inner := &queryCountingEmbedder{}
	c := NewCachingEmbedder(inner, CachingConfig{
		CacheSize:     2,
		NormalizeFunc: func(v []float32) []float32 { return []float32{v[0] * 2} },
	})

	vec, err := c.Embed(context.Background(), "test")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if vec[0] != 2 {
		t.Fatalf("expected normalize to double the value, got %v", vec)
	}

	// Second call should return cached (already normalized) value.
	vec2, _ := c.Embed(context.Background(), "test")
	if vec2[0] != 2 {
		t.Fatalf("expected cached normalized value, got %v", vec2)
	}
}

func TestCachingEmbedder_EmbedBatchPassesThrough(t *testing.T) {
	inner := &queryCountingEmbedder{}
	c := newTestCachingEmbedder(inner, 2)

	vecs, err := c.EmbedBatch(context.Background(), []string{"a", "b"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(vecs) != 2 {
		t.Fatalf("expected 2 vectors, got %d", len(vecs))
	}
}
