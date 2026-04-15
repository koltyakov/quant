package embed

import (
	"context"
	"errors"
	"testing"
)

func TestNewRateLimitedEmbedder_Construction(t *testing.T) {
	t.Parallel()

	base := stubEmbedder{dimensions: 64}
	cfg := DefaultRateLimiterConfig()
	rl := NewRateLimitedEmbedder(base, cfg)

	if rl == nil {
		t.Fatal("expected non-nil RateLimitedEmbedder")
	}
	if rl.Dimensions() != 64 {
		t.Fatalf("expected dimensions 64, got %d", rl.Dimensions())
	}

	stats := rl.Stats()
	if stats.MaxTokens != cfg.MaxTokens {
		t.Fatalf("expected MaxTokens %v, got %v", cfg.MaxTokens, stats.MaxTokens)
	}
	if stats.MaxConcurrent != cfg.MaxConcurrent {
		t.Fatalf("expected MaxConcurrent %d, got %d", cfg.MaxConcurrent, stats.MaxConcurrent)
	}
}

func TestDefaultRateLimiterConfig_Values(t *testing.T) {
	t.Parallel()

	cfg := DefaultRateLimiterConfig()
	if cfg.MaxTokens <= 0 {
		t.Fatalf("MaxTokens should be positive, got %v", cfg.MaxTokens)
	}
	if cfg.RefillRate <= 0 {
		t.Fatalf("RefillRate should be positive, got %v", cfg.RefillRate)
	}
	if cfg.MaxWaiters <= 0 {
		t.Fatalf("MaxWaiters should be positive, got %d", cfg.MaxWaiters)
	}
	if cfg.MaxConcurrent < 0 {
		t.Fatalf("MaxConcurrent should be non-negative, got %d", cfg.MaxConcurrent)
	}
}

func TestRateLimitError_ErrorMethod(t *testing.T) {
	t.Parallel()

	err := rateLimitError{}
	msg := err.Error()
	if msg != "embedding rate limit exceeded: too many pending requests" {
		t.Fatalf("unexpected error message: %q", msg)
	}
}

func TestNewRateLimiter_ZeroConcurrency(t *testing.T) {
	t.Parallel()

	rl := NewRateLimiter(RateLimiterConfig{
		MaxTokens:     5,
		RefillRate:    1,
		MaxWaiters:    10,
		MaxConcurrent: 0,
	})

	stats := rl.Stats()
	if stats.MaxConcurrent != 0 {
		t.Fatalf("expected MaxConcurrent 0, got %d", stats.MaxConcurrent)
	}
}

func TestRateLimitedEmbedder_EmbedError(t *testing.T) {
	t.Parallel()

	base := stubEmbedder{
		dimensions: 3,
		embedFn: func(ctx context.Context, text string) ([]float32, error) {
			return nil, errors.New("embed failed")
		},
	}
	rl := NewRateLimitedEmbedder(base, RateLimiterConfig{
		MaxTokens:     5,
		RefillRate:    0,
		MaxWaiters:    1,
		MaxConcurrent: 1,
	})

	_, err := rl.Embed(context.Background(), "test")
	if err == nil || err.Error() != "embed failed" {
		t.Fatalf("expected embed error, got %v", err)
	}

	stats := rl.Stats()
	if stats.Concurrency != 0 {
		t.Fatalf("expected Concurrency 0 after error, got %d", stats.Concurrency)
	}
}

func TestRateLimitedEmbedder_EmbedBatchError(t *testing.T) {
	t.Parallel()

	base := stubEmbedder{
		dimensions: 3,
		embedBatchFn: func(ctx context.Context, texts []string) ([][]float32, error) {
			return nil, errors.New("batch failed")
		},
	}
	rl := NewRateLimitedEmbedder(base, RateLimiterConfig{
		MaxTokens:     5,
		RefillRate:    0,
		MaxWaiters:    1,
		MaxConcurrent: 1,
	})

	_, err := rl.EmbedBatch(context.Background(), []string{"a"})
	if err == nil || err.Error() != "batch failed" {
		t.Fatalf("expected batch error, got %v", err)
	}
}
