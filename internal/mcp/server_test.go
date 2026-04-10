package mcp

import (
	"bytes"
	"context"
	"errors"
	"log"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/andrew/quant/internal/config"
	"github.com/andrew/quant/internal/index"
	mcplib "github.com/mark3labs/mcp-go/mcp"
)

type countingEmbedder struct {
	mu    sync.Mutex
	calls map[string]int
}

func (e *countingEmbedder) Embed(_ context.Context, text string) ([]float32, error) {
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.calls == nil {
		e.calls = make(map[string]int)
	}
	e.calls[text]++
	return []float32{1}, nil
}

func (e *countingEmbedder) EmbedBatch(_ context.Context, texts []string) ([][]float32, error) {
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

func (e *countingEmbedder) Dimensions() int { return 1 }

func (e *countingEmbedder) Close() error { return nil }

type cancelAwareEmbedder struct {
	started chan struct{}
	release chan struct{}

	mu    sync.Mutex
	calls map[string]int
	once  sync.Once
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

func (e *cancelAwareEmbedder) Close() error { return nil }

func TestCachedEmbed_UsesLRUEviction(t *testing.T) {
	embedder := &countingEmbedder{}
	s := &Server{
		embedder: embedder,
		embCache: newEmbeddingLRU(2),
	}

	ctx := context.Background()
	if _, err := s.cachedEmbed(ctx, "a"); err != nil {
		t.Fatalf("unexpected embed error: %v", err)
	}
	if _, err := s.cachedEmbed(ctx, "b"); err != nil {
		t.Fatalf("unexpected embed error: %v", err)
	}
	if _, err := s.cachedEmbed(ctx, "a"); err != nil {
		t.Fatalf("unexpected embed error: %v", err)
	}
	if _, err := s.cachedEmbed(ctx, "c"); err != nil {
		t.Fatalf("unexpected embed error: %v", err)
	}
	if _, err := s.cachedEmbed(ctx, "a"); err != nil {
		t.Fatalf("unexpected embed error: %v", err)
	}
	if _, err := s.cachedEmbed(ctx, "b"); err != nil {
		t.Fatalf("unexpected embed error: %v", err)
	}

	embedder.mu.Lock()
	defer embedder.mu.Unlock()
	if embedder.calls["a"] != 1 {
		t.Fatalf("expected a to stay cached, got %d embed calls", embedder.calls["a"])
	}
	if embedder.calls["b"] != 2 {
		t.Fatalf("expected b to be evicted and recomputed, got %d embed calls", embedder.calls["b"])
	}
	if embedder.calls["c"] != 1 {
		t.Fatalf("expected c to be embedded once, got %d embed calls", embedder.calls["c"])
	}
}

func TestCachedEmbed_DeduplicatesConcurrentRequests(t *testing.T) {
	embedder := &countingEmbedder{}
	s := &Server{
		embedder:   embedder,
		embCache:   newEmbeddingLRU(2),
		embFlights: make(map[string]*embeddingFlight),
	}

	ctx := context.Background()
	var wg sync.WaitGroup
	errCh := make(chan error, 8)
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, err := s.cachedEmbed(ctx, "same-query")
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

	embedder.mu.Lock()
	defer embedder.mu.Unlock()
	if embedder.calls["same-query"] != 1 {
		t.Fatalf("expected one embed call, got %d", embedder.calls["same-query"])
	}
}

func TestCachedEmbed_LeaderCancellationDoesNotAbortSharedFlight(t *testing.T) {
	embedder := &cancelAwareEmbedder{
		started: make(chan struct{}),
		release: make(chan struct{}),
	}
	s := &Server{
		embedder:   embedder,
		embCache:   newEmbeddingLRU(2),
		embFlights: make(map[string]*embeddingFlight),
	}

	leaderCtx, cancelLeader := context.WithCancel(context.Background())
	leaderErrCh := make(chan error, 1)
	go func() {
		_, err := s.cachedEmbed(leaderCtx, "shared-query")
		leaderErrCh <- err
	}()

	<-embedder.started

	followerErrCh := make(chan error, 1)
	go func() {
		_, err := s.cachedEmbed(context.Background(), "shared-query")
		followerErrCh <- err
	}()

	cancelLeader()
	if err := <-leaderErrCh; !errors.Is(err, context.Canceled) {
		t.Fatalf("expected leader to observe cancellation, got %v", err)
	}

	close(embedder.release)

	if err := <-followerErrCh; err != nil {
		t.Fatalf("expected follower to reuse successful shared flight, got %v", err)
	}

	embedder.mu.Lock()
	defer embedder.mu.Unlock()
	if embedder.calls["shared-query"] != 1 {
		t.Fatalf("expected one shared embed call, got %d", embedder.calls["shared-query"])
	}
}

func TestHandleSearch_LogsRequestAndSpotlight(t *testing.T) {
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "quant.db")

	store, err := index.NewStore(dbPath)
	if err != nil {
		t.Fatalf("unexpected store open error: %v", err)
	}
	t.Cleanup(func() {
		if err := store.Close(); err != nil {
			t.Fatalf("unexpected close error: %v", err)
		}
	})

	if err := store.ReindexDocument(context.Background(), &index.Document{
		Path:       "notes/alpha.txt",
		Hash:       "alpha-hash",
		ModifiedAt: testTime(),
	}, []index.ChunkRecord{{
		Content:    "alpha search phrase with useful spotlight text",
		ChunkIndex: 0,
		Embedding:  index.EncodeFloat32(index.NormalizeFloat32([]float32{1})),
	}}); err != nil {
		t.Fatalf("unexpected seed error: %v", err)
	}

	s := &Server{
		cfg: &config.Config{
			WatchDir:   dir,
			DBPath:     dbPath,
			EmbedModel: "test-model",
		},
		store:      store,
		embedder:   &countingEmbedder{},
		embCache:   newEmbeddingLRU(2),
		embFlights: make(map[string]*embeddingFlight),
	}

	var buf bytes.Buffer
	oldWriter := log.Writer()
	oldFlags := log.Flags()
	oldPrefix := log.Prefix()
	log.SetOutput(&buf)
	log.SetFlags(0)
	log.SetPrefix("")
	t.Cleanup(func() {
		log.SetOutput(oldWriter)
		log.SetFlags(oldFlags)
		log.SetPrefix(oldPrefix)
	})

	_, err = s.handleSearch(context.Background(), mcplib.CallToolRequest{
		Params: mcplib.CallToolParams{
			Name: "search",
			Arguments: map[string]any{
				"query": "alpha search phrase",
				"limit": float64(3),
			},
		},
	})
	if err != nil {
		t.Fatalf("unexpected handleSearch error: %v", err)
	}

	logOutput := buf.String()
	if !strings.Contains(logOutput, `MCP search request: query="alpha search phrase"`) {
		t.Fatalf("expected request log, got %q", logOutput)
	}
	if !strings.Contains(logOutput, `MCP search result: query="alpha search phrase"`) {
		t.Fatalf("expected result log, got %q", logOutput)
	}
	if !strings.Contains(logOutput, `notes/alpha.txt#0`) {
		t.Fatalf("expected spotlight path in log, got %q", logOutput)
	}
	if !strings.Contains(logOutput, `snippet="alpha search phrase with useful spotlight text"`) {
		t.Fatalf("expected spotlight snippet in log, got %q", logOutput)
	}
}

func testTime() time.Time {
	return time.Unix(1_700_000_000, 0).UTC()
}
