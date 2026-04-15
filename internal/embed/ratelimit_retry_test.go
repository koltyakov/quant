package embed

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func TestAcquire_ContextCancellationWhileWaiting(t *testing.T) {
	rl := NewRateLimiter(RateLimiterConfig{
		MaxTokens:     1,
		RefillRate:    0,
		MaxWaiters:    10,
		MaxConcurrent: 10,
	})

	if err := rl.Acquire(context.Background()); err != nil {
		t.Fatalf("first Acquire() error = %v", err)
	}
	defer rl.Release()

	var wg sync.WaitGroup
	var canceledCount atomic.Int32

	for i := 0; i < 5; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
			defer cancel()
			if err := rl.Acquire(ctx); err != nil {
				if errors.Is(err, context.DeadlineExceeded) {
					canceledCount.Add(1)
				}
			}
		}()
	}

	wg.Wait()

	count := canceledCount.Load()
	if count == 0 {
		t.Fatal("expected at least one goroutine to be canceled while waiting")
	}

	stats := rl.Stats()
	if stats.CurrentWaiters != 0 {
		t.Fatalf("CurrentWaiters = %d, want 0 after all goroutines done", stats.CurrentWaiters)
	}
}

func TestAcquire_ConcurrentGoroutines(t *testing.T) {
	rl := NewRateLimiter(RateLimiterConfig{
		MaxTokens:     10,
		RefillRate:    100,
		MaxWaiters:    50,
		MaxConcurrent: 0,
	})

	var wg sync.WaitGroup
	var successCount atomic.Int32

	for i := 0; i < 20; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			if err := rl.Acquire(ctx); err != nil {
				return
			}
			successCount.Add(1)
			rl.Release()
		}()
	}

	wg.Wait()

	if successCount.Load() == 0 {
		t.Fatal("expected at least one successful acquire")
	}
}

func TestAcquire_MaxWaitersExceeded(t *testing.T) {
	rl := NewRateLimiter(RateLimiterConfig{
		MaxTokens:     0,
		RefillRate:    0,
		MaxWaiters:    2,
		MaxConcurrent: 0,
	})

	var wg sync.WaitGroup
	var exceededCount atomic.Int32

	for i := 0; i < 5; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
			defer cancel()
			err := rl.Acquire(ctx)
			if errors.Is(err, ErrRateLimitExceeded) {
				exceededCount.Add(1)
			}
		}()
	}

	wg.Wait()

	if exceededCount.Load() == 0 {
		t.Fatal("expected at least one ErrRateLimitExceeded when max waiters exceeded")
	}
}

func TestAcquire_NoConcurrencyLimit(t *testing.T) {
	rl := NewRateLimiter(RateLimiterConfig{
		MaxTokens:     5,
		RefillRate:    0,
		MaxWaiters:    10,
		MaxConcurrent: 0,
	})

	for i := 0; i < 5; i++ {
		if err := rl.Acquire(context.Background()); err != nil {
			t.Fatalf("Acquire() %d error = %v", i, err)
		}
	}

	stats := rl.Stats()
	if stats.Concurrency != 5 {
		t.Fatalf("Concurrency = %d, want 5 when MaxConcurrent is 0", stats.Concurrency)
	}
	if stats.MaxConcurrent != 0 {
		t.Fatalf("MaxConcurrent = %d, want 0", stats.MaxConcurrent)
	}

	for i := 0; i < 5; i++ {
		rl.Release()
	}
}

func TestRateLimitedEmbedder_EmbedRateLimitExceeded(t *testing.T) {
	base := stubEmbedder{dimensions: 3}
	rl := NewRateLimitedEmbedder(base, RateLimiterConfig{
		MaxTokens:     0,
		RefillRate:    0,
		MaxWaiters:    0,
		MaxConcurrent: 1,
	})

	_, err := rl.Embed(context.Background(), "hello")
	if !errors.Is(err, ErrRateLimitExceeded) {
		t.Fatalf("Embed() error = %v, want ErrRateLimitExceeded", err)
	}

	stats := rl.Stats()
	if stats.Concurrency != 0 {
		t.Fatalf("Concurrency after rate limit error = %d, want 0", stats.Concurrency)
	}
}

func TestRateLimitedEmbedder_EmbedBatchRateLimitExceeded(t *testing.T) {
	base := stubEmbedder{dimensions: 3}
	rl := NewRateLimitedEmbedder(base, RateLimiterConfig{
		MaxTokens:     0,
		RefillRate:    0,
		MaxWaiters:    0,
		MaxConcurrent: 1,
	})

	_, err := rl.EmbedBatch(context.Background(), []string{"a", "b"})
	if !errors.Is(err, ErrRateLimitExceeded) {
		t.Fatalf("EmbedBatch() error = %v, want ErrRateLimitExceeded", err)
	}
}

func TestRateLimitedEmbedder_EmbedBatchPassThrough(t *testing.T) {
	base := stubEmbedder{
		embedBatchFn: func(ctx context.Context, texts []string) ([][]float32, error) {
			result := make([][]float32, len(texts))
			for i := range texts {
				result[i] = []float32{float32(i + 1)}
			}
			return result, nil
		},
		dimensions: 3,
	}
	rl := NewRateLimitedEmbedder(base, RateLimiterConfig{
		MaxTokens:     5,
		RefillRate:    0,
		MaxWaiters:    1,
		MaxConcurrent: 0,
	})

	vecs, err := rl.EmbedBatch(context.Background(), []string{"x", "y", "z"})
	if err != nil {
		t.Fatalf("EmbedBatch() error = %v", err)
	}
	if len(vecs) != 3 {
		t.Fatalf("len(EmbedBatch()) = %d, want 3", len(vecs))
	}

	stats := rl.Stats()
	if stats.Concurrency != 0 {
		t.Fatalf("Concurrency after EmbedBatch = %d, want 0", stats.Concurrency)
	}
}

func TestRateLimitedEmbedder_Dimensions(t *testing.T) {
	base := stubEmbedder{dimensions: 128}
	rl := NewRateLimitedEmbedder(base, RateLimiterConfig{
		MaxTokens:     1,
		RefillRate:    0,
		MaxWaiters:    0,
		MaxConcurrent: 0,
	})

	if got := rl.Dimensions(); got != 128 {
		t.Fatalf("Dimensions() = %d, want 128", got)
	}
}

func TestRateLimitedEmbedder_Close(t *testing.T) {
	base := stubEmbedder{closeErr: errors.New("close error")}
	rl := NewRateLimitedEmbedder(base, RateLimiterConfig{
		MaxTokens:     1,
		RefillRate:    0,
		MaxWaiters:    0,
		MaxConcurrent: 0,
	})

	if err := rl.Close(); err == nil || err.Error() != "close error" {
		t.Fatalf("Close() error = %v, want close error", err)
	}
}

func TestRateLimitedEmbedder_EmbedContextCancellation(t *testing.T) {
	base := stubEmbedder{dimensions: 3}
	rl := NewRateLimitedEmbedder(base, RateLimiterConfig{
		MaxTokens:     0,
		RefillRate:    0,
		MaxWaiters:    5,
		MaxConcurrent: 1,
	})

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	_, err := rl.Embed(ctx, "hello")
	if err == nil {
		t.Fatal("expected error from canceled context")
	}
}

func TestRateLimiter_RefillOverTime(t *testing.T) {
	rl := NewRateLimiter(RateLimiterConfig{
		MaxTokens:     10,
		RefillRate:    1000,
		MaxWaiters:    10,
		MaxConcurrent: 0,
	})

	stats := rl.Stats()
	if stats.AvailableTokens != 10 {
		t.Fatalf("initial AvailableTokens = %v, want 10", stats.AvailableTokens)
	}

	rl.mu.Lock()
	rl.tokens = 0
	rl.lastRefill = time.Now().Add(-10 * time.Millisecond)
	rl.mu.Unlock()

	stats = rl.Stats()
	if stats.AvailableTokens <= 0 {
		t.Fatalf("AvailableTokens after refill = %v, want > 0", stats.AvailableTokens)
	}
}

func TestRateLimiter_ReleaseSignalsWaiter(t *testing.T) {
	rl := NewRateLimiter(RateLimiterConfig{
		MaxTokens:     1,
		RefillRate:    0,
		MaxWaiters:    5,
		MaxConcurrent: 0,
	})

	if err := rl.Acquire(context.Background()); err != nil {
		t.Fatalf("first Acquire() error = %v", err)
	}

	acquired := make(chan struct{})
	go func() {
		if err := rl.Acquire(context.Background()); err != nil {
			return
		}
		close(acquired)
		rl.Release()
	}()

	time.Sleep(50 * time.Millisecond)
	rl.Release()

	select {
	case <-acquired:
	case <-time.After(2 * time.Second):
		t.Fatal("expected waiting goroutine to acquire token after release")
	}

	rl.Release()
}
