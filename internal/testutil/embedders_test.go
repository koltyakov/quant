package testutil

import (
	"context"
	"testing"
	"time"
)

func TestStaticEmbedder(t *testing.T) {
	t.Parallel()

	var embedder StaticEmbedder
	vec, err := embedder.Embed(context.Background(), "hello")
	if err != nil {
		t.Fatalf("Embed() error = %v", err)
	}
	if len(vec) != 1 || vec[0] != 1 {
		t.Fatalf("Embed() = %#v, want [1]", vec)
	}

	batch, err := embedder.EmbedBatch(context.Background(), []string{"a", "b"})
	if err != nil {
		t.Fatalf("EmbedBatch() error = %v", err)
	}
	if len(batch) != 2 || len(batch[0]) != 1 || batch[0][0] != 1 || batch[1][0] != 1 {
		t.Fatalf("EmbedBatch() = %#v, want [[1] [1]]", batch)
	}
	if got := embedder.Dimensions(); got != 1 {
		t.Fatalf("Dimensions() = %d, want 1", got)
	}
	if err := embedder.Close(); err != nil {
		t.Fatalf("Close() error = %v", err)
	}
}

func TestBatchCountingEmbedder(t *testing.T) {
	t.Parallel()

	embedder := &BatchCountingEmbedder{}
	_, err := embedder.EmbedBatch(context.Background(), []string{"a", "b"})
	if err != nil {
		t.Fatalf("EmbedBatch() error = %v", err)
	}
	if got := embedder.BatchCalls.Load(); got != 1 {
		t.Fatalf("BatchCalls = %d, want 1", got)
	}
	if got := embedder.Dimensions(); got != 1 {
		t.Fatalf("Dimensions() = %d, want 1", got)
	}
}

func TestShortBatchEmbedder(t *testing.T) {
	t.Parallel()

	var embedder ShortBatchEmbedder
	batch, err := embedder.EmbedBatch(context.Background(), nil)
	if err != nil {
		t.Fatalf("EmbedBatch(nil) error = %v", err)
	}
	if batch != nil {
		t.Fatalf("EmbedBatch(nil) = %#v, want nil", batch)
	}

	batch, err = embedder.EmbedBatch(context.Background(), []string{"a", "b"})
	if err != nil {
		t.Fatalf("EmbedBatch(non-empty) error = %v", err)
	}
	if len(batch) != 1 || len(batch[0]) != 1 || batch[0][0] != 1 {
		t.Fatalf("EmbedBatch(non-empty) = %#v, want [[1]]", batch)
	}
}

func TestQueryCountingEmbedder(t *testing.T) {
	t.Parallel()

	embedder := &QueryCountingEmbedder{}
	_, err := embedder.Embed(context.Background(), "hello")
	if err != nil {
		t.Fatalf("Embed() error = %v", err)
	}
	_, err = embedder.EmbedBatch(context.Background(), []string{"hello", "world"})
	if err != nil {
		t.Fatalf("EmbedBatch() error = %v", err)
	}

	embedder.Mu.Lock()
	defer embedder.Mu.Unlock()
	if embedder.Calls["hello"] != 2 {
		t.Fatalf("Calls[hello] = %d, want 2", embedder.Calls["hello"])
	}
	if embedder.Calls["world"] != 1 {
		t.Fatalf("Calls[world] = %d, want 1", embedder.Calls["world"])
	}
}

func TestBatchCountingEmbedderEmbedAndClose(t *testing.T) {
	t.Parallel()

	embedder := &BatchCountingEmbedder{}
	vec, err := embedder.Embed(context.Background(), "x")
	if err != nil {
		t.Fatalf("Embed() error = %v", err)
	}
	if len(vec) != 1 || vec[0] != 1 {
		t.Fatalf("Embed() = %#v, want [1]", vec)
	}
	if err := embedder.Close(); err != nil {
		t.Fatalf("Close() error = %v", err)
	}
}

func TestShortBatchEmbedderEmbedDimensionsClose(t *testing.T) {
	t.Parallel()

	var embedder ShortBatchEmbedder
	vec, err := embedder.Embed(context.Background(), "hello")
	if err != nil {
		t.Fatalf("Embed() error = %v", err)
	}
	if len(vec) != 1 || vec[0] != 1 {
		t.Fatalf("Embed() = %#v, want [1]", vec)
	}
	if got := embedder.Dimensions(); got != 1 {
		t.Fatalf("Dimensions() = %d, want 1", got)
	}
	if err := embedder.Close(); err != nil {
		t.Fatalf("Close() error = %v", err)
	}
}

func TestQueryCountingEmbedderDimensionsAndClose(t *testing.T) {
	t.Parallel()

	embedder := &QueryCountingEmbedder{}
	if got := embedder.Dimensions(); got != 1 {
		t.Fatalf("Dimensions() = %d, want 1", got)
	}
	if err := embedder.Close(); err != nil {
		t.Fatalf("Close() error = %v", err)
	}
}

func TestCancelAwareEmbedderBatchDimensionsClose(t *testing.T) {
	t.Parallel()

	embedder := &CancelAwareEmbedder{
		Started: make(chan struct{}),
		Release: make(chan struct{}),
	}

	go func() {
		<-embedder.Started
		close(embedder.Release)
	}()

	batch, err := embedder.EmbedBatch(context.Background(), []string{"a"})
	if err != nil {
		t.Fatalf("EmbedBatch() error = %v", err)
	}
	if len(batch) != 1 || len(batch[0]) != 1 || batch[0][0] != 1 {
		t.Fatalf("EmbedBatch() = %#v, want [[1]]", batch)
	}
	if got := embedder.Dimensions(); got != 1 {
		t.Fatalf("Dimensions() = %d, want 1", got)
	}
	if err := embedder.Close(); err != nil {
		t.Fatalf("Close() error = %v", err)
	}
}

func TestCancelAwareEmbedderReleaseAndCancel(t *testing.T) {
	t.Run("release", func(t *testing.T) {
		started := make(chan struct{})
		release := make(chan struct{})
		embedder := &CancelAwareEmbedder{
			Started: started,
			Release: release,
		}

		done := make(chan error, 1)
		go func() {
			_, err := embedder.Embed(context.Background(), "release-me")
			done <- err
		}()

		select {
		case <-started:
		case <-time.After(time.Second):
			t.Fatal("timed out waiting for embedder start")
		}

		close(release)
		if err := <-done; err != nil {
			t.Fatalf("Embed() release path error = %v", err)
		}

		embedder.Mu.Lock()
		defer embedder.Mu.Unlock()
		if embedder.Calls["release-me"] != 1 {
			t.Fatalf("Calls[release-me] = %d, want 1", embedder.Calls["release-me"])
		}
	})

	t.Run("cancel", func(t *testing.T) {
		started := make(chan struct{})
		release := make(chan struct{})
		canceled := make(chan struct{})
		embedder := &CancelAwareEmbedder{
			Started:  started,
			Release:  release,
			Canceled: canceled,
		}

		ctx, cancel := context.WithCancel(context.Background())
		done := make(chan error, 1)
		go func() {
			_, err := embedder.Embed(ctx, "cancel-me")
			done <- err
		}()

		select {
		case <-started:
		case <-time.After(time.Second):
			t.Fatal("timed out waiting for embedder start")
		}

		cancel()

		select {
		case <-canceled:
		case <-time.After(time.Second):
			t.Fatal("timed out waiting for cancel signal")
		}

		if err := <-done; err == nil {
			t.Fatal("Embed() cancel path error = nil, want context cancellation")
		}

		embedder.Mu.Lock()
		defer embedder.Mu.Unlock()
		if embedder.Calls["cancel-me"] != 1 {
			t.Fatalf("Calls[cancel-me] = %d, want 1", embedder.Calls["cancel-me"])
		}
	})
}
