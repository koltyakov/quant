package embed

import (
	"context"
	"errors"
	"testing"
	"time"
)

type stubEmbedder struct {
	embedFn      func(ctx context.Context, text string) ([]float32, error)
	embedBatchFn func(ctx context.Context, texts []string) ([][]float32, error)
	dimensions   int
	closeErr     error
}

func (s stubEmbedder) Embed(ctx context.Context, text string) ([]float32, error) {
	if s.embedFn != nil {
		return s.embedFn(ctx, text)
	}
	return []float32{1}, nil
}

func (s stubEmbedder) EmbedBatch(ctx context.Context, texts []string) ([][]float32, error) {
	if s.embedBatchFn != nil {
		return s.embedBatchFn(ctx, texts)
	}
	return [][]float32{{1}}, nil
}

func (s stubEmbedder) Dimensions() int {
	return s.dimensions
}

func (s stubEmbedder) Close() error {
	return s.closeErr
}

func TestDefaultRateLimiterConfig(t *testing.T) {
	t.Parallel()

	cfg := DefaultRateLimiterConfig()
	if cfg.MaxTokens != 10 {
		t.Fatalf("MaxTokens = %v, want 10", cfg.MaxTokens)
	}
	if cfg.RefillRate != 5 {
		t.Fatalf("RefillRate = %v, want 5", cfg.RefillRate)
	}
	if cfg.MaxWaiters != 100 {
		t.Fatalf("MaxWaiters = %d, want 100", cfg.MaxWaiters)
	}
	if cfg.MaxConcurrent != 4 {
		t.Fatalf("MaxConcurrent = %d, want 4", cfg.MaxConcurrent)
	}
}

func TestRateLimiterAcquireReleaseAndStats(t *testing.T) {
	t.Parallel()

	rl := NewRateLimiter(RateLimiterConfig{
		MaxTokens:     2,
		RefillRate:    0,
		MaxWaiters:    1,
		MaxConcurrent: 1,
	})

	if err := rl.Acquire(context.Background()); err != nil {
		t.Fatalf("Acquire() error = %v", err)
	}

	stats := rl.Stats()
	if stats.AvailableTokens != 1 {
		t.Fatalf("AvailableTokens = %v, want 1", stats.AvailableTokens)
	}
	if stats.Concurrency != 1 {
		t.Fatalf("Concurrency = %d, want 1", stats.Concurrency)
	}
	if stats.MaxConcurrent != 1 {
		t.Fatalf("MaxConcurrent = %d, want 1", stats.MaxConcurrent)
	}

	rl.Release()

	stats = rl.Stats()
	if stats.Concurrency != 0 {
		t.Fatalf("Concurrency after release = %d, want 0", stats.Concurrency)
	}
}

func TestRateLimiterAcquireHonorsContextCancellation(t *testing.T) {
	t.Parallel()

	rl := NewRateLimiter(RateLimiterConfig{
		MaxTokens:     1,
		RefillRate:    0,
		MaxWaiters:    1,
		MaxConcurrent: 1,
	})
	if err := rl.Acquire(context.Background()); err != nil {
		t.Fatalf("first Acquire() error = %v", err)
	}
	defer rl.Release()

	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()

	err := rl.Acquire(ctx)
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("Acquire() error = %v, want %v", err, context.DeadlineExceeded)
	}

	stats := rl.Stats()
	if stats.CurrentWaiters != 0 {
		t.Fatalf("CurrentWaiters = %d, want 0", stats.CurrentWaiters)
	}
	if stats.Concurrency != 1 {
		t.Fatalf("Concurrency = %d, want 1", stats.Concurrency)
	}
}

func TestRateLimiterRejectsWhenMaxWaitersExceeded(t *testing.T) {
	t.Parallel()

	rl := NewRateLimiter(RateLimiterConfig{
		MaxTokens:     0,
		RefillRate:    0,
		MaxWaiters:    0,
		MaxConcurrent: 1,
	})

	err := rl.Acquire(context.Background())
	if !errors.Is(err, ErrRateLimitExceeded) {
		t.Fatalf("Acquire() error = %v, want %v", err, ErrRateLimitExceeded)
	}

	stats := rl.Stats()
	if stats.Concurrency != 0 {
		t.Fatalf("Concurrency = %d, want 0", stats.Concurrency)
	}
}

func TestRateLimiterWaitDurationAndRefill(t *testing.T) {
	t.Parallel()

	rl := NewRateLimiter(RateLimiterConfig{
		MaxTokens:     3,
		RefillRate:    2,
		MaxWaiters:    1,
		MaxConcurrent: 0,
	})

	rl.mu.Lock()
	rl.tokens = 0.25
	rl.lastRefill = time.Now().Add(-600 * time.Millisecond)
	rl.mu.Unlock()

	stats := rl.Stats()
	if stats.AvailableTokens <= 1 {
		t.Fatalf("AvailableTokens after refill = %v, want > 1", stats.AvailableTokens)
	}

	rl.mu.Lock()
	rl.tokens = 0.25
	rl.mu.Unlock()

	wait := rl.waitDuration()
	if wait < 350*time.Millisecond || wait > 380*time.Millisecond {
		t.Fatalf("waitDuration() = %s, want about 375ms", wait)
	}

	rl.mu.Lock()
	rl.refillRate = 0
	rl.mu.Unlock()
	if got := rl.waitDuration(); got != time.Second {
		t.Fatalf("waitDuration() with zero refill = %s, want %s", got, time.Second)
	}
}

func TestRateLimiterReleaseWithoutAcquireIsSafe(t *testing.T) {
	t.Parallel()

	rl := NewRateLimiter(RateLimiterConfig{
		MaxTokens:     1,
		RefillRate:    0,
		MaxWaiters:    1,
		MaxConcurrent: 1,
	})

	rl.Release()

	stats := rl.Stats()
	if stats.Concurrency != 0 {
		t.Fatalf("Concurrency = %d, want 0", stats.Concurrency)
	}
}

func TestRateLimitedEmbedderForwardsCallsAndReleasesSlotsOnError(t *testing.T) {
	t.Parallel()

	base := stubEmbedder{
		dimensions: 7,
		closeErr:   errors.New("close failed"),
		embedFn: func(ctx context.Context, text string) ([]float32, error) {
			if text != "hello" {
				t.Fatalf("Embed text = %q, want %q", text, "hello")
			}
			return []float32{1, 2}, nil
		},
		embedBatchFn: func(ctx context.Context, texts []string) ([][]float32, error) {
			if len(texts) != 2 || texts[0] != "a" || texts[1] != "b" {
				t.Fatalf("EmbedBatch texts = %v, want [a b]", texts)
			}
			return nil, errors.New("batch failed")
		},
	}

	rl := NewRateLimitedEmbedder(base, RateLimiterConfig{
		MaxTokens:     3,
		RefillRate:    0,
		MaxWaiters:    1,
		MaxConcurrent: 1,
	})

	vec, err := rl.Embed(context.Background(), "hello")
	if err != nil {
		t.Fatalf("Embed() error = %v", err)
	}
	if len(vec) != 2 {
		t.Fatalf("len(Embed()) = %d, want 2", len(vec))
	}

	_, err = rl.EmbedBatch(context.Background(), []string{"a", "b"})
	if err == nil || err.Error() != "batch failed" {
		t.Fatalf("EmbedBatch() error = %v, want batch failed", err)
	}

	if got := rl.Dimensions(); got != 7 {
		t.Fatalf("Dimensions() = %d, want 7", got)
	}
	if err := rl.Close(); err == nil || err.Error() != "close failed" {
		t.Fatalf("Close() error = %v, want close failed", err)
	}

	stats := rl.Stats()
	if stats.Concurrency != 0 {
		t.Fatalf("Concurrency after forwarded calls = %d, want 0", stats.Concurrency)
	}
	if stats.AvailableTokens != 3 {
		t.Fatalf("AvailableTokens after two calls = %v, want 3", stats.AvailableTokens)
	}
}

func TestRateLimitErrorMessage(t *testing.T) {
	t.Parallel()

	if got := ErrRateLimitExceeded.Error(); got != "embedding rate limit exceeded: too many pending requests" {
		t.Fatalf("ErrRateLimitExceeded.Error() = %q", got)
	}
}
