package app

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/koltyakov/quant/internal/config"
	"github.com/koltyakov/quant/internal/lock"
)

type stubProcessServer struct {
	run func(ctx context.Context, cfg *config.Config) error
}

func (s stubProcessServer) Serve(ctx context.Context, cfg *config.Config) error {
	return s.run(ctx, cfg)
}

type stubAliveChecker struct {
	alive bool
}

func (s stubAliveChecker) Alive(context.Context) bool {
	return s.alive
}

func TestProcessRunnerRun_RestartRequired(t *testing.T) {
	t.Parallel()

	runner := newProcessRunner(context.Background())
	errCh := make(chan error, 1)

	go func() {
		errCh <- runner.Run(stubProcessServer{
			run: func(ctx context.Context, _ *config.Config) error {
				<-ctx.Done()
				return nil
			},
		}, &config.Config{})
	}()

	time.Sleep(20 * time.Millisecond)
	runner.requestRestart()

	select {
	case err := <-errCh:
		if !errors.Is(err, ErrRestartRequired) {
			t.Fatalf("expected ErrRestartRequired, got %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for process runner to stop")
	}
}

func TestProcessRunnerRun_ReturnsServerError(t *testing.T) {
	t.Parallel()

	runner := newProcessRunner(context.Background())
	wantErr := errors.New("boom")

	err := runner.Run(stubProcessServer{
		run: func(context.Context, *config.Config) error {
			return wantErr
		},
	}, &config.Config{})
	if !errors.Is(err, wantErr) {
		t.Fatalf("expected %v, got %v", wantErr, err)
	}
}

func TestMainProcessClose_Idempotent(t *testing.T) {
	t.Parallel()

	calls := 0
	proc := &mainProcess{
		cancel: func() {
			calls++
		},
	}

	proc.Close()
	proc.Close()

	if calls != 1 {
		t.Fatalf("expected cancel to run once, got %d", calls)
	}
}

func TestWatchMainAndPromoteInterval_PromotesWhenLockAvailable(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	cfg := &config.Config{WatchDir: dir}
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()

	restarted := make(chan struct{}, 1)
	go watchMainAndPromoteInterval(ctx, cfg, stubAliveChecker{alive: false}, "127.0.0.1:9000", func() {
		restarted <- struct{}{}
	}, 10*time.Millisecond)

	select {
	case <-restarted:
	case <-ctx.Done():
		t.Fatal("timed out waiting for worker promotion")
	}

	if info, err := lock.ReadLock(dir); err == nil && info != nil {
		t.Fatalf("expected temporary promotion lock to be released, got %+v", *info)
	}
}

func TestWatchMainAndPromoteInterval_RestartsOnNewMain(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	cfg := &config.Config{WatchDir: dir}
	lk, err := lock.TryAcquire(dir, "main-instance", "127.0.0.1:9001")
	if err != nil {
		t.Fatalf("unexpected lock acquire error: %v", err)
	}
	defer func() { _ = lk.Release() }()

	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()

	restarted := make(chan struct{}, 1)
	go watchMainAndPromoteInterval(ctx, cfg, stubAliveChecker{alive: false}, "127.0.0.1:9000", func() {
		restarted <- struct{}{}
	}, 10*time.Millisecond)

	select {
	case <-restarted:
	case <-ctx.Done():
		t.Fatal("timed out waiting for worker restart on new main")
	}
}
