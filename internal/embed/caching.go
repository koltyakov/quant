package embed

import (
	"container/list"
	"context"
	"fmt"
	"strings"
	"sync"
	"time"
)

// CachingConfig configures the CachingEmbedder.
type CachingConfig struct {
	// CacheSize is the maximum number of query embeddings to cache (LRU).
	CacheSize int

	// CircuitFailureLimit is how many consecutive failures open the circuit.
	CircuitFailureLimit int

	// CircuitResetTimeout is how long the circuit stays open before retrying.
	CircuitResetTimeout time.Duration

	// NormalizeFunc, if set, is applied to every embedding before caching.
	NormalizeFunc func([]float32) []float32

	// ModelID, if set, is included in cache keys so that switching models
	// invalidates cached embeddings rather than returning stale vectors.
	ModelID string
}

// CachingEmbedder wraps an Embedder with an LRU cache, in-flight request
// deduplication, and a circuit breaker for Embed calls. EmbedBatch passes
// through to the underlying embedder unchanged (used for bulk indexing, not
// interactive queries).
type CachingEmbedder struct {
	inner     Embedder
	normalize func([]float32) []float32
	modelID   string

	mu      sync.Mutex
	cache   *embeddingLRU
	flights map[string]*embeddingFlight

	circuitBreaker *embedCircuitBreaker
}

// NewCachingEmbedder creates a CachingEmbedder wrapping inner.
func NewCachingEmbedder(inner Embedder, cfg CachingConfig) *CachingEmbedder {
	cacheSize := cfg.CacheSize
	if cacheSize < 1 {
		cacheSize = 128
	}
	failureLimit := cfg.CircuitFailureLimit
	if failureLimit < 1 {
		failureLimit = 5
	}
	resetTimeout := cfg.CircuitResetTimeout
	if resetTimeout <= 0 {
		resetTimeout = 30 * time.Second
	}
	return &CachingEmbedder{
		inner:          inner,
		normalize:      cfg.NormalizeFunc,
		modelID:        cfg.ModelID,
		cache:          newEmbeddingLRU(cacheSize),
		flights:        make(map[string]*embeddingFlight),
		circuitBreaker: newEmbedCircuitBreaker(failureLimit, resetTimeout),
	}
}

// Embed returns a cached (or freshly computed) embedding for text.
// Concurrent calls for the same text share a single backend request.
func (c *CachingEmbedder) Embed(ctx context.Context, text string) ([]float32, error) {
	cacheKey := c.cacheKey(text)

	c.mu.Lock()
	if vec, ok := c.cache.Get(cacheKey); ok {
		c.mu.Unlock()
		c.circuitBreaker.RecordSuccess()
		return vec, nil
	}
	if flight, ok := c.flights[cacheKey]; ok {
		flight.waiters++
		c.mu.Unlock()
		return c.waitForFlight(ctx, flight)
	}
	flight := &embeddingFlight{
		done:    make(chan struct{}),
		waiters: 1,
	}
	c.flights[cacheKey] = flight
	c.mu.Unlock()

	if !c.circuitBreaker.Allow() {
		c.mu.Lock()
		flight.err = fmt.Errorf("embedding circuit breaker open")
		delete(c.flights, cacheKey)
		c.mu.Unlock()
		select {
		case <-flight.done:
		default:
			close(flight.done)
		}
		return nil, flight.err
	}

	//nolint:gosec // G118: intentional background goroutine - must complete embedding even if caller cancels.
	go c.runFlight(cacheKey, text, flight)

	return c.waitForFlight(ctx, flight)
}

func (c *CachingEmbedder) waitForFlight(ctx context.Context, flight *embeddingFlight) ([]float32, error) {
	select {
	case <-ctx.Done():
		c.releaseFlight(flight)
		return nil, ctx.Err()
	case <-flight.done:
		return flight.vec, flight.err
	}
}

func (c *CachingEmbedder) runFlight(cacheKey, text string, flight *embeddingFlight) {
	vec, err := c.inner.Embed(context.Background(), text)
	if err == nil {
		if c.normalize != nil {
			vec = c.normalize(vec)
		}
		c.circuitBreaker.RecordSuccess()
	} else {
		c.circuitBreaker.RecordFailure()
	}

	c.mu.Lock()
	if err == nil {
		c.cache.Put(cacheKey, vec)
	}
	delete(c.flights, cacheKey)
	flight.vec = vec
	flight.err = err
	select {
	case <-flight.done:
	default:
		close(flight.done)
	}
	c.mu.Unlock()
}

func (c *CachingEmbedder) releaseFlight(flight *embeddingFlight) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if flight.waiters > 0 {
		flight.waiters--
	}
}

// EmbedBatch passes through to the underlying embedder (no caching).
func (c *CachingEmbedder) EmbedBatch(ctx context.Context, texts []string) ([][]float32, error) {
	return c.inner.EmbedBatch(ctx, texts)
}

func (c *CachingEmbedder) Dimensions() int { return c.inner.Dimensions() }
func (c *CachingEmbedder) Close() error    { return c.inner.Close() }

// ---------------------------------------------------------------------------
// LRU cache
// ---------------------------------------------------------------------------

type embeddingCacheEntry struct {
	key   string
	value []float32
}

type embeddingLRU struct {
	capacity int
	ll       *list.List
	items    map[string]*list.Element
}

func newEmbeddingLRU(capacity int) *embeddingLRU {
	if capacity < 1 {
		capacity = 1
	}
	return &embeddingLRU{
		capacity: capacity,
		ll:       list.New(),
		items:    make(map[string]*list.Element, capacity),
	}
}

func (c *embeddingLRU) Get(key string) ([]float32, bool) {
	elem, ok := c.items[key]
	if !ok {
		return nil, false
	}
	c.ll.MoveToFront(elem)
	return elem.Value.(*embeddingCacheEntry).value, true
}

func (c *embeddingLRU) Put(key string, value []float32) {
	if elem, ok := c.items[key]; ok {
		entry := elem.Value.(*embeddingCacheEntry)
		entry.value = value
		c.ll.MoveToFront(elem)
		return
	}

	elem := c.ll.PushFront(&embeddingCacheEntry{key: key, value: value})
	c.items[key] = elem
	if c.ll.Len() <= c.capacity {
		return
	}

	tail := c.ll.Back()
	if tail == nil {
		return
	}
	c.ll.Remove(tail)
	delete(c.items, tail.Value.(*embeddingCacheEntry).key)
}

// ---------------------------------------------------------------------------
// Flight deduplication
// ---------------------------------------------------------------------------

type embeddingFlight struct {
	done    chan struct{}
	waiters int
	vec     []float32
	err     error
}

// ---------------------------------------------------------------------------
// Circuit breaker
// ---------------------------------------------------------------------------

type circuitState int

const (
	circuitClosed circuitState = iota
	circuitOpen
)

type embedCircuitBreaker struct {
	mu           sync.RWMutex
	failures     int
	lastFailure  time.Time
	state        circuitState
	failureLimit int
	resetTimeout time.Duration
}

func newEmbedCircuitBreaker(failureLimit int, resetTimeout time.Duration) *embedCircuitBreaker {
	return &embedCircuitBreaker{
		failureLimit: failureLimit,
		resetTimeout: resetTimeout,
		state:        circuitClosed,
	}
}

func (cb *embedCircuitBreaker) Allow() bool {
	if cb == nil {
		return true
	}
	cb.mu.Lock()
	defer cb.mu.Unlock()

	switch cb.state {
	case circuitClosed:
		return true
	case circuitOpen:
		if time.Since(cb.lastFailure) >= cb.resetTimeout {
			cb.state = circuitClosed
			cb.failures = 0
			return true
		}
		return false
	}
	return false
}

func (cb *embedCircuitBreaker) RecordFailure() {
	if cb == nil {
		return
	}
	cb.mu.Lock()
	defer cb.mu.Unlock()

	cb.failures++
	cb.lastFailure = time.Now()
	if cb.failures >= cb.failureLimit {
		cb.state = circuitOpen
	}
}

func (cb *embedCircuitBreaker) RecordSuccess() {
	if cb == nil {
		return
	}
	cb.mu.Lock()
	defer cb.mu.Unlock()

	if cb.state == circuitClosed {
		cb.failures = 0
	}
}

// ---------------------------------------------------------------------------
// Helpers
// ---------------------------------------------------------------------------

func (c *CachingEmbedder) cacheKey(text string) string {
	normalized := strings.Join(strings.Fields(strings.TrimSpace(text)), " ")
	if normalized == "" {
		normalized = text
	}
	if c.modelID != "" {
		return c.modelID + "\x00" + normalized
	}
	return normalized
}
