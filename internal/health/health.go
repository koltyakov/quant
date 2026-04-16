// Package health provides health check abstractions for the quant indexer.
// Health checks allow monitoring the status of various system components
// and enable graceful degradation when dependencies become unavailable.
package health

import (
	"context"
	"sync"
	"time"
)

// Status represents the health status of a component.
type Status string

const (
	// StatusHealthy indicates the component is functioning normally.
	StatusHealthy Status = "healthy"

	// StatusDegraded indicates the component is partially functional.
	StatusDegraded Status = "degraded"

	// StatusUnhealthy indicates the component is not functioning.
	StatusUnhealthy Status = "unhealthy"

	// StatusUnknown indicates the health status could not be determined.
	StatusUnknown Status = "unknown"
)

// CheckResult contains the result of a health check.
type CheckResult struct {
	Name      string        `json:"name"`
	Status    Status        `json:"status"`
	Message   string        `json:"message,omitempty"`
	Details   any           `json:"details,omitempty"`
	Duration  time.Duration `json:"duration_ms"`
	Timestamp time.Time     `json:"timestamp"`
}

// Checker defines the interface for health checks.
type Checker interface {
	// Name returns the name of this health check.
	Name() string

	// Check performs the health check and returns the result.
	Check(ctx context.Context) CheckResult
}

// CheckerFunc is an adapter to allow ordinary functions to be used as Checkers.
type CheckerFunc struct {
	name string
	fn   func(ctx context.Context) CheckResult
}

// NewCheckerFunc creates a new CheckerFunc.
func NewCheckerFunc(name string, fn func(ctx context.Context) CheckResult) *CheckerFunc {
	return &CheckerFunc{name: name, fn: fn}
}

func (c *CheckerFunc) Name() string {
	return c.name
}

func (c *CheckerFunc) Check(ctx context.Context) CheckResult {
	return c.fn(ctx)
}

// Registry manages health checkers and aggregates their results.
type Registry struct {
	mu       sync.RWMutex
	checkers []Checker
}

// NewRegistry creates a new health check registry.
func NewRegistry() *Registry {
	return &Registry{
		checkers: make([]Checker, 0),
	}
}

// Register adds a health checker to the registry.
func (r *Registry) Register(c Checker) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.checkers = append(r.checkers, c)
}

// Check runs all registered health checks and returns their results.
func (r *Registry) Check(ctx context.Context) []CheckResult {
	r.mu.RLock()
	checkers := make([]Checker, len(r.checkers))
	copy(checkers, r.checkers)
	r.mu.RUnlock()

	results := make([]CheckResult, len(checkers))
	var wg sync.WaitGroup

	for i, checker := range checkers {
		wg.Go(func() {
			start := time.Now()
			result := checker.Check(ctx)
			result.Duration = time.Since(start)
			result.Timestamp = start
			results[i] = result
		})
	}

	wg.Wait()
	return results
}

// OverallStatus returns the worst status from all checks.
func (r *Registry) OverallStatus(ctx context.Context) Status {
	results := r.Check(ctx)
	if len(results) == 0 {
		return StatusUnknown
	}

	worst := StatusHealthy
	for _, result := range results {
		if statusSeverity(result.Status) > statusSeverity(worst) {
			worst = result.Status
		}
	}
	return worst
}

func statusSeverity(s Status) int {
	switch s {
	case StatusHealthy:
		return 0
	case StatusDegraded:
		return 1
	case StatusUnhealthy:
		return 2
	case StatusUnknown:
		return 3
	default:
		return 4
	}
}

// AggregateResult contains the aggregated health check results.
type AggregateResult struct {
	Status    Status        `json:"status"`
	Checks    []CheckResult `json:"checks"`
	Timestamp time.Time     `json:"timestamp"`
}

// Aggregate runs all checks and returns an aggregated result.
func (r *Registry) Aggregate(ctx context.Context) AggregateResult {
	checks := r.Check(ctx)
	return AggregateResult{
		Status:    r.OverallStatus(ctx),
		Checks:    checks,
		Timestamp: time.Now(),
	}
}

// Common health check implementations

// DatabaseChecker checks database connectivity.
type DatabaseChecker struct {
	name    string
	pingFn  func(ctx context.Context) error
	timeout time.Duration
}

// NewDatabaseChecker creates a new database health checker.
func NewDatabaseChecker(name string, pingFn func(ctx context.Context) error, timeout time.Duration) *DatabaseChecker {
	if timeout == 0 {
		timeout = 5 * time.Second
	}
	return &DatabaseChecker{name: name, pingFn: pingFn, timeout: timeout}
}

func (c *DatabaseChecker) Name() string { return c.name }

func (c *DatabaseChecker) Check(ctx context.Context) CheckResult {
	ctx, cancel := context.WithTimeout(ctx, c.timeout)
	defer cancel()

	if err := c.pingFn(ctx); err != nil {
		return CheckResult{
			Name:    c.name,
			Status:  StatusUnhealthy,
			Message: "database ping failed: " + err.Error(),
		}
	}

	return CheckResult{
		Name:    c.name,
		Status:  StatusHealthy,
		Message: "database is responsive",
	}
}

// EmbeddingChecker checks embedding service availability.
type EmbeddingChecker struct {
	name      string
	embedFn   func(ctx context.Context, text string) ([]float32, error)
	timeout   time.Duration
	testInput string
}

// NewEmbeddingChecker creates a new embedding health checker.
func NewEmbeddingChecker(name string, embedFn func(ctx context.Context, text string) ([]float32, error), timeout time.Duration) *EmbeddingChecker {
	if timeout == 0 {
		timeout = 10 * time.Second
	}
	return &EmbeddingChecker{
		name:      name,
		embedFn:   embedFn,
		timeout:   timeout,
		testInput: "health check",
	}
}

func (c *EmbeddingChecker) Name() string { return c.name }

func (c *EmbeddingChecker) Check(ctx context.Context) CheckResult {
	ctx, cancel := context.WithTimeout(ctx, c.timeout)
	defer cancel()

	vec, err := c.embedFn(ctx, c.testInput)
	if err != nil {
		return CheckResult{
			Name:    c.name,
			Status:  StatusUnhealthy,
			Message: "embedding failed: " + err.Error(),
		}
	}

	return CheckResult{
		Name:   c.name,
		Status: StatusHealthy,
		Details: map[string]any{
			"dimensions": len(vec),
		},
	}
}

// HNSWChecker checks HNSW index readiness.
type HNSWChecker struct {
	name    string
	readyFn func() bool
}

// NewHNSWChecker creates a new HNSW health checker.
func NewHNSWChecker(name string, readyFn func() bool) *HNSWChecker {
	return &HNSWChecker{name: name, readyFn: readyFn}
}

func (c *HNSWChecker) Name() string { return c.name }

func (c *HNSWChecker) Check(ctx context.Context) CheckResult {
	if c.readyFn() {
		return CheckResult{
			Name:    c.name,
			Status:  StatusHealthy,
			Message: "HNSW index is ready",
		}
	}

	return CheckResult{
		Name:    c.name,
		Status:  StatusDegraded,
		Message: "HNSW index is building or not available; falling back to brute-force search",
	}
}

// IndexStateChecker checks the indexer state.
type IndexStateChecker struct {
	name    string
	stateFn func() (state string, message string)
}

// NewIndexStateChecker creates a new index state checker.
func NewIndexStateChecker(name string, stateFn func() (state string, message string)) *IndexStateChecker {
	return &IndexStateChecker{name: name, stateFn: stateFn}
}

func (c *IndexStateChecker) Name() string { return c.name }

func (c *IndexStateChecker) Check(ctx context.Context) CheckResult {
	state, message := c.stateFn()

	var status Status
	switch state {
	case "ready":
		status = StatusHealthy
	case "indexing", "starting":
		status = StatusDegraded
	case "degraded":
		status = StatusDegraded
	default:
		status = StatusUnknown
	}

	return CheckResult{
		Name:    c.name,
		Status:  status,
		Message: message,
		Details: map[string]any{
			"state": state,
		},
	}
}
