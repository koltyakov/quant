package embed

import (
	"context"
	"sync"
	"time"
)

// RateLimiter provides rate limiting and backpressure for embedding requests.
// It implements a token bucket algorithm with configurable capacity and refill rate.
type RateLimiter struct {
	mu            sync.Mutex
	tokens        float64
	maxTokens     float64
	refillRate    float64 // tokens per second
	lastRefill    time.Time
	maxWaiters    int
	waiters       int
	waiterCh      chan struct{}
	concurrency   int
	maxConcurrent int
	concurrencyCh chan struct{}
}

// RateLimiterConfig configures the rate limiter.
type RateLimiterConfig struct {
	// MaxTokens is the maximum number of tokens (requests) that can be accumulated.
	MaxTokens float64

	// RefillRate is the number of tokens added per second.
	RefillRate float64

	// MaxWaiters is the maximum number of goroutines that can wait for a token.
	// Additional requests will be rejected immediately.
	MaxWaiters int

	// MaxConcurrent is the maximum number of concurrent embedding requests.
	// Use 0 for unlimited concurrency.
	MaxConcurrent int
}

// DefaultRateLimiterConfig returns sensible defaults for rate limiting.
func DefaultRateLimiterConfig() RateLimiterConfig {
	return RateLimiterConfig{
		MaxTokens:     10,
		RefillRate:    5, // 5 requests per second
		MaxWaiters:    100,
		MaxConcurrent: 4,
	}
}

// NewRateLimiter creates a new rate limiter with the given configuration.
func NewRateLimiter(cfg RateLimiterConfig) *RateLimiter {
	rl := &RateLimiter{
		tokens:        cfg.MaxTokens,
		maxTokens:     cfg.MaxTokens,
		refillRate:    cfg.RefillRate,
		lastRefill:    time.Now(),
		maxWaiters:    cfg.MaxWaiters,
		waiterCh:      make(chan struct{}, 1),
		maxConcurrent: cfg.MaxConcurrent,
	}
	if cfg.MaxConcurrent > 0 {
		rl.concurrencyCh = make(chan struct{}, cfg.MaxConcurrent)
	}
	return rl
}

// Acquire attempts to acquire a token for an embedding request.
// It blocks until a token is available or the context is cancelled.
// Returns an error if the context is cancelled or max waiters is exceeded.
func (r *RateLimiter) Acquire(ctx context.Context) error {
	// First, handle concurrency limiting if configured
	if r.concurrencyCh != nil {
		select {
		case r.concurrencyCh <- struct{}{}:
			// Got a concurrency slot
		case <-ctx.Done():
			return ctx.Err()
		}
	}

	for {
		r.mu.Lock()
		r.refill()

		if r.tokens >= 1 {
			r.tokens--
			r.concurrency++
			r.mu.Unlock()
			return nil
		}

		// Check if we can wait
		if r.waiters >= r.maxWaiters {
			r.mu.Unlock()
			if r.concurrencyCh != nil {
				<-r.concurrencyCh // Release concurrency slot
			}
			return ErrRateLimitExceeded
		}

		r.waiters++
		r.mu.Unlock()

		// Wait for token or context cancellation
		select {
		case <-ctx.Done():
			r.mu.Lock()
			r.waiters--
			r.mu.Unlock()
			if r.concurrencyCh != nil {
				<-r.concurrencyCh // Release concurrency slot
			}
			return ctx.Err()
		case <-r.waiterCh:
			r.mu.Lock()
			r.waiters--
			r.mu.Unlock()
			// Try again to acquire token
		case <-time.After(r.waitDuration()):
			r.mu.Lock()
			r.waiters--
			r.mu.Unlock()
			// Tokens may have refilled, try again
		}
	}
}

// Release releases a token back to the pool.
// Should be called after the embedding request completes.
func (r *RateLimiter) Release() {
	r.mu.Lock()
	if r.concurrency > 0 {
		r.concurrency--
	}
	r.mu.Unlock()

	if r.concurrencyCh != nil {
		select {
		case <-r.concurrencyCh:
			// Released concurrency slot
		default:
			// Channel was empty, nothing to release
		}
	}

	// Signal waiting goroutines
	select {
	case r.waiterCh <- struct{}{}:
	default:
	}
}

// Stats returns current rate limiter statistics.
func (r *RateLimiter) Stats() RateLimiterStats {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.refill()
	return RateLimiterStats{
		AvailableTokens: r.tokens,
		MaxTokens:       r.maxTokens,
		CurrentWaiters:  r.waiters,
		MaxWaiters:      r.maxWaiters,
		Concurrency:     r.concurrency,
		MaxConcurrent:   r.maxConcurrent,
	}
}

// RateLimiterStats contains rate limiter statistics.
type RateLimiterStats struct {
	AvailableTokens float64
	MaxTokens       float64
	CurrentWaiters  int
	MaxWaiters      int
	Concurrency     int
	MaxConcurrent   int
}

func (r *RateLimiter) refill() {
	now := time.Now()
	elapsed := now.Sub(r.lastRefill).Seconds()
	r.tokens += elapsed * r.refillRate
	if r.tokens > r.maxTokens {
		r.tokens = r.maxTokens
	}
	r.lastRefill = now
}

func (r *RateLimiter) waitDuration() time.Duration {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.refillRate <= 0 {
		return time.Second
	}
	// Time to wait for one token
	tokensNeeded := 1.0 - r.tokens
	if tokensNeeded <= 0 {
		return 0
	}
	return time.Duration(tokensNeeded/r.refillRate*1000) * time.Millisecond
}

// ErrRateLimitExceeded is returned when too many requests are waiting.
var ErrRateLimitExceeded = rateLimitError{}

type rateLimitError struct{}

func (rateLimitError) Error() string {
	return "embedding rate limit exceeded: too many pending requests"
}

// RateLimitedEmbedder wraps an Embedder with rate limiting.
type RateLimitedEmbedder struct {
	embedder    Embedder
	rateLimiter *RateLimiter
}

// NewRateLimitedEmbedder creates a new rate-limited embedder.
func NewRateLimitedEmbedder(embedder Embedder, cfg RateLimiterConfig) *RateLimitedEmbedder {
	return &RateLimitedEmbedder{
		embedder:    embedder,
		rateLimiter: NewRateLimiter(cfg),
	}
}

func (r *RateLimitedEmbedder) Embed(ctx context.Context, text string) ([]float32, error) {
	if err := r.rateLimiter.Acquire(ctx); err != nil {
		return nil, err
	}
	defer r.rateLimiter.Release()
	return r.embedder.Embed(ctx, text)
}

func (r *RateLimitedEmbedder) EmbedBatch(ctx context.Context, texts []string) ([][]float32, error) {
	if err := r.rateLimiter.Acquire(ctx); err != nil {
		return nil, err
	}
	defer r.rateLimiter.Release()
	return r.embedder.EmbedBatch(ctx, texts)
}

func (r *RateLimitedEmbedder) Dimensions() int {
	return r.embedder.Dimensions()
}

func (r *RateLimitedEmbedder) Close() error {
	return r.embedder.Close()
}

// Stats returns the rate limiter statistics.
func (r *RateLimitedEmbedder) Stats() RateLimiterStats {
	return r.rateLimiter.Stats()
}
