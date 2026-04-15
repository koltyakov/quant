package health

import (
	"context"
	"errors"
	"testing"
	"time"
)

func TestCheckerFuncUsesProvidedNameAndFunction(t *testing.T) {
	t.Parallel()

	checker := NewCheckerFunc("embed", func(ctx context.Context) CheckResult {
		return CheckResult{Name: "embed", Status: StatusHealthy, Message: "ok"}
	})

	if checker.Name() != "embed" {
		t.Fatalf("Name() = %q, want %q", checker.Name(), "embed")
	}

	got := checker.Check(context.Background())
	if got.Status != StatusHealthy {
		t.Fatalf("Check().Status = %q, want %q", got.Status, StatusHealthy)
	}
	if got.Message != "ok" {
		t.Fatalf("Check().Message = %q, want %q", got.Message, "ok")
	}
}

func TestRegistryCheckPreservesRegistrationOrderAndAnnotatesResults(t *testing.T) {
	t.Parallel()

	registry := NewRegistry()
	registry.Register(NewCheckerFunc("slow", func(ctx context.Context) CheckResult {
		time.Sleep(20 * time.Millisecond)
		return CheckResult{Name: "slow", Status: StatusHealthy}
	}))
	registry.Register(NewCheckerFunc("fast", func(ctx context.Context) CheckResult {
		return CheckResult{Name: "fast", Status: StatusDegraded}
	}))

	results := registry.Check(context.Background())
	if len(results) != 2 {
		t.Fatalf("len(Check()) = %d, want 2", len(results))
	}
	if results[0].Name != "slow" || results[1].Name != "fast" {
		t.Fatalf("Check() preserved wrong order: got [%q, %q]", results[0].Name, results[1].Name)
	}
	for i, result := range results {
		if result.Duration <= 0 {
			t.Fatalf("result %d duration = %s, want > 0", i, result.Duration)
		}
		if result.Timestamp.IsZero() {
			t.Fatalf("result %d timestamp was zero", i)
		}
	}
}

func TestRegistryOverallStatusUsesWorstStatus(t *testing.T) {
	t.Parallel()

	registry := NewRegistry()
	if got := registry.OverallStatus(context.Background()); got != StatusUnknown {
		t.Fatalf("OverallStatus() with no checks = %q, want %q", got, StatusUnknown)
	}

	registry.Register(NewCheckerFunc("healthy", func(ctx context.Context) CheckResult {
		return CheckResult{Name: "healthy", Status: StatusHealthy}
	}))
	registry.Register(NewCheckerFunc("degraded", func(ctx context.Context) CheckResult {
		return CheckResult{Name: "degraded", Status: StatusDegraded}
	}))
	registry.Register(NewCheckerFunc("unhealthy", func(ctx context.Context) CheckResult {
		return CheckResult{Name: "unhealthy", Status: StatusUnhealthy}
	}))

	if got := registry.OverallStatus(context.Background()); got != StatusUnhealthy {
		t.Fatalf("OverallStatus() = %q, want %q", got, StatusUnhealthy)
	}
}

func TestRegistryAggregateReturnsChecksAndStatus(t *testing.T) {
	t.Parallel()

	registry := NewRegistry()
	registry.Register(NewCheckerFunc("index", func(ctx context.Context) CheckResult {
		return CheckResult{Name: "index", Status: StatusDegraded}
	}))

	got := registry.Aggregate(context.Background())
	if got.Status != StatusDegraded {
		t.Fatalf("Aggregate().Status = %q, want %q", got.Status, StatusDegraded)
	}
	if len(got.Checks) != 1 {
		t.Fatalf("len(Aggregate().Checks) = %d, want 1", len(got.Checks))
	}
	if got.Checks[0].Name != "index" {
		t.Fatalf("Aggregate().Checks[0].Name = %q, want %q", got.Checks[0].Name, "index")
	}
	if got.Timestamp.IsZero() {
		t.Fatal("Aggregate().Timestamp was zero")
	}
}

func TestDatabaseCheckerUsesDefaultTimeoutAndReportsStatus(t *testing.T) {
	t.Parallel()

	checker := NewDatabaseChecker("db", func(ctx context.Context) error {
		deadline, ok := ctx.Deadline()
		if !ok {
			t.Fatal("expected database checker context to have a deadline")
		}
		if remaining := time.Until(deadline); remaining <= 0 || remaining > 5*time.Second {
			t.Fatalf("database checker deadline remaining = %s, want between 0 and 5s", remaining)
		}
		return nil
	}, 0)

	got := checker.Check(context.Background())
	if got.Status != StatusHealthy {
		t.Fatalf("Check().Status = %q, want %q", got.Status, StatusHealthy)
	}
	if got.Message != "database is responsive" {
		t.Fatalf("Check().Message = %q, want %q", got.Message, "database is responsive")
	}

	errChecker := NewDatabaseChecker("db", func(ctx context.Context) error {
		return errors.New("dial failed")
	}, time.Second)

	got = errChecker.Check(context.Background())
	if got.Status != StatusUnhealthy {
		t.Fatalf("error Check().Status = %q, want %q", got.Status, StatusUnhealthy)
	}
	if got.Message != "database ping failed: dial failed" {
		t.Fatalf("error Check().Message = %q", got.Message)
	}
}

func TestEmbeddingCheckerUsesDefaultTimeoutAndReportsDetails(t *testing.T) {
	t.Parallel()

	checker := NewEmbeddingChecker("embed", func(ctx context.Context, text string) ([]float32, error) {
		if text != "health check" {
			t.Fatalf("embed text = %q, want %q", text, "health check")
		}
		deadline, ok := ctx.Deadline()
		if !ok {
			t.Fatal("expected embedding checker context to have a deadline")
		}
		if remaining := time.Until(deadline); remaining <= 0 || remaining > 10*time.Second {
			t.Fatalf("embedding checker deadline remaining = %s, want between 0 and 10s", remaining)
		}
		return []float32{1, 2, 3}, nil
	}, 0)

	got := checker.Check(context.Background())
	if got.Status != StatusHealthy {
		t.Fatalf("Check().Status = %q, want %q", got.Status, StatusHealthy)
	}
	details, ok := got.Details.(map[string]any)
	if !ok {
		t.Fatalf("Check().Details type = %T, want map[string]any", got.Details)
	}
	if details["dimensions"] != 3 {
		t.Fatalf("Check().Details[dimensions] = %v, want 3", details["dimensions"])
	}

	errChecker := NewEmbeddingChecker("embed", func(ctx context.Context, text string) ([]float32, error) {
		return nil, errors.New("backend down")
	}, time.Second)

	got = errChecker.Check(context.Background())
	if got.Status != StatusUnhealthy {
		t.Fatalf("error Check().Status = %q, want %q", got.Status, StatusUnhealthy)
	}
	if got.Message != "embedding failed: backend down" {
		t.Fatalf("error Check().Message = %q", got.Message)
	}
}

func TestHNSWCheckerReportsHealthyAndDegraded(t *testing.T) {
	t.Parallel()

	ready := NewHNSWChecker("hnsw", func() bool { return true })
	if got := ready.Check(context.Background()); got.Status != StatusHealthy {
		t.Fatalf("ready Check().Status = %q, want %q", got.Status, StatusHealthy)
	}

	notReady := NewHNSWChecker("hnsw", func() bool { return false })
	if got := notReady.Check(context.Background()); got.Status != StatusDegraded {
		t.Fatalf("notReady Check().Status = %q, want %q", got.Status, StatusDegraded)
	}
}

func TestIndexStateCheckerMapsStatesToStatuses(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name        string
		state       string
		wantStatus  Status
		wantMessage string
	}{
		{name: "ready", state: "ready", wantStatus: StatusHealthy, wantMessage: "done"},
		{name: "indexing", state: "indexing", wantStatus: StatusDegraded, wantMessage: "working"},
		{name: "starting", state: "starting", wantStatus: StatusDegraded, wantMessage: "booting"},
		{name: "degraded", state: "degraded", wantStatus: StatusDegraded, wantMessage: "fallback"},
		{name: "unknown", state: "mystery", wantStatus: StatusUnknown, wantMessage: "??"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			checker := NewIndexStateChecker("index", func() (string, string) {
				return tt.state, tt.wantMessage
			})

			got := checker.Check(context.Background())
			if got.Status != tt.wantStatus {
				t.Fatalf("Check().Status = %q, want %q", got.Status, tt.wantStatus)
			}
			if got.Message != tt.wantMessage {
				t.Fatalf("Check().Message = %q, want %q", got.Message, tt.wantMessage)
			}
			details, ok := got.Details.(map[string]any)
			if !ok {
				t.Fatalf("Check().Details type = %T, want map[string]any", got.Details)
			}
			if details["state"] != tt.state {
				t.Fatalf("Check().Details[state] = %v, want %q", details["state"], tt.state)
			}
		})
	}
}

func TestDatabaseChecker_Name(t *testing.T) {
	t.Parallel()

	checker := NewDatabaseChecker("db", func(ctx context.Context) error { return nil }, time.Second)
	if checker.Name() != "db" {
		t.Fatalf("Name() = %q, want %q", checker.Name(), "db")
	}
}

func TestEmbeddingChecker_Name(t *testing.T) {
	t.Parallel()

	checker := NewEmbeddingChecker("embed", func(ctx context.Context, text string) ([]float32, error) {
		return nil, nil
	}, time.Second)
	if checker.Name() != "embed" {
		t.Fatalf("Name() = %q, want %q", checker.Name(), "embed")
	}
}

func TestHNSWChecker_Name(t *testing.T) {
	t.Parallel()

	checker := NewHNSWChecker("hnsw", func() bool { return true })
	if checker.Name() != "hnsw" {
		t.Fatalf("Name() = %q, want %q", checker.Name(), "hnsw")
	}
}

func TestIndexStateChecker_Name(t *testing.T) {
	t.Parallel()

	checker := NewIndexStateChecker("index", func() (string, string) { return "", "" })
	if checker.Name() != "index" {
		t.Fatalf("Name() = %q, want %q", checker.Name(), "index")
	}
}
