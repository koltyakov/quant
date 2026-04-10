package mcp

import (
	"bytes"
	"context"
	"errors"
	"log"
	"math"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/koltyakov/quant/internal/config"
	"github.com/koltyakov/quant/internal/index"
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
	started  chan struct{}
	release  chan struct{}
	canceled chan struct{}

	mu         sync.Mutex
	calls      map[string]int
	once       sync.Once
	cancelOnce sync.Once
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
		if e.canceled != nil {
			e.cancelOnce.Do(func() { close(e.canceled) })
		}
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

type shutdownRecorder struct {
	startErr    error
	startBlock  chan struct{}
	shutdownErr error
	shutdownCtx context.Context
}

func (s *shutdownRecorder) Start(string) error {
	if s.startBlock != nil {
		<-s.startBlock
	}
	return s.startErr
}

func (s *shutdownRecorder) Shutdown(ctx context.Context) error {
	s.shutdownCtx = ctx
	return s.shutdownErr
}

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

func TestCachedEmbed_CanceledFlightRestartsForNextCaller(t *testing.T) {
	embedder := &cancelAwareEmbedder{
		started:  make(chan struct{}),
		release:  make(chan struct{}),
		canceled: make(chan struct{}),
	}
	s := &Server{
		embedder:   embedder,
		embCache:   newEmbeddingLRU(2),
		embFlights: make(map[string]*embeddingFlight),
	}

	ctx, cancel := context.WithCancel(context.Background())
	firstErrCh := make(chan error, 1)
	go func() {
		_, err := s.cachedEmbed(ctx, "abandoned-query")
		firstErrCh <- err
	}()

	<-embedder.started
	cancel()

	if err := <-firstErrCh; !errors.Is(err, context.Canceled) {
		t.Fatalf("expected cancellation error, got %v", err)
	}

	select {
	case <-embedder.canceled:
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for embedder cancellation")
	}

	close(embedder.release)

	if _, err := s.cachedEmbed(context.Background(), "abandoned-query"); err != nil {
		t.Fatalf("expected restarted flight to succeed, got %v", err)
	}

	embedder.mu.Lock()
	defer embedder.mu.Unlock()
	if embedder.calls["abandoned-query"] != 2 {
		t.Fatalf("expected restarted flight to re-embed, got %d calls", embedder.calls["abandoned-query"])
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

func TestHandleSearch_ReturnsMatchingResults(t *testing.T) {
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "quant.db")

	store, err := index.NewStore(dbPath)
	if err != nil {
		t.Fatalf("unexpected store open error: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })

	// Seed two documents with distinct content.
	for _, doc := range []struct {
		path    string
		content string
	}{
		{"notes/golang.txt", "Go is a statically typed compiled programming language"},
		{"notes/python.txt", "Python is a dynamically typed interpreted language"},
	} {
		if err := store.ReindexDocument(context.Background(), &index.Document{
			Path:       doc.path,
			Hash:       doc.path + "-hash",
			ModifiedAt: testTime(),
		}, []index.ChunkRecord{{
			Content:    doc.content,
			ChunkIndex: 0,
			Embedding:  index.EncodeFloat32(index.NormalizeFloat32([]float32{1})),
		}}); err != nil {
			t.Fatalf("unexpected seed error for %s: %v", doc.path, err)
		}
	}

	s := newTestServer(dir, dbPath, store)
	suppressLogs(t)

	// Search should return results.
	result, err := s.handleSearch(context.Background(), mcplib.CallToolRequest{
		Params: mcplib.CallToolParams{
			Name:      "search",
			Arguments: map[string]any{"query": "compiled language", "limit": float64(5)},
		},
	})
	if err != nil {
		t.Fatalf("unexpected search error: %v", err)
	}
	text := extractToolText(t, result)
	if !strings.Contains(text, "notes/golang.txt") && !strings.Contains(text, "notes/python.txt") {
		t.Fatalf("expected at least one document in results, got %q", text)
	}
	if !strings.Contains(text, "score:") {
		t.Fatalf("expected score in results, got %q", text)
	}
}

func TestHandleSearch_PathFilter(t *testing.T) {
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "quant.db")

	store, err := index.NewStore(dbPath)
	if err != nil {
		t.Fatalf("unexpected store open error: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })

	for _, doc := range []struct {
		path    string
		content string
	}{
		{"src/main.go", "package main func main"},
		{"docs/guide.md", "installation guide for the project"},
	} {
		if err := store.ReindexDocument(context.Background(), &index.Document{
			Path:       doc.path,
			Hash:       doc.path + "-hash",
			ModifiedAt: testTime(),
		}, []index.ChunkRecord{{
			Content:    doc.content,
			ChunkIndex: 0,
			Embedding:  index.EncodeFloat32(index.NormalizeFloat32([]float32{1})),
		}}); err != nil {
			t.Fatalf("unexpected seed error: %v", err)
		}
	}

	s := newTestServer(dir, dbPath, store)
	suppressLogs(t)

	// Search with path filter should only return docs/ results.
	result, err := s.handleSearch(context.Background(), mcplib.CallToolRequest{
		Params: mcplib.CallToolParams{
			Name:      "search",
			Arguments: map[string]any{"query": "guide project", "path": "docs/"},
		},
	})
	if err != nil {
		t.Fatalf("unexpected search error: %v", err)
	}
	text := extractToolText(t, result)
	if strings.Contains(text, "src/main.go") {
		t.Fatalf("expected path filter to exclude src/ results, got %q", text)
	}
}

func TestHandleSearch_PathFilterNormalizesRelativeAndAbsolutePaths(t *testing.T) {
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "quant.db")

	store, err := index.NewStore(dbPath)
	if err != nil {
		t.Fatalf("unexpected store open error: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })

	for _, doc := range []struct {
		path    string
		content string
	}{
		{"docs/guide.md", "installation guide for the project"},
		{"src/main.go", "package main func main"},
	} {
		if err := store.ReindexDocument(context.Background(), &index.Document{
			Path:       doc.path,
			Hash:       doc.path + "-hash",
			ModifiedAt: testTime(),
		}, []index.ChunkRecord{{
			Content:    doc.content,
			ChunkIndex: 0,
			Embedding:  index.EncodeFloat32(index.NormalizeFloat32([]float32{1})),
		}}); err != nil {
			t.Fatalf("unexpected seed error: %v", err)
		}
	}

	s := newTestServer(dir, dbPath, store)
	suppressLogs(t)

	for _, pathArg := range []string{"./docs", filepath.Join(dir, "docs")} {
		result, err := s.handleSearch(context.Background(), mcplib.CallToolRequest{
			Params: mcplib.CallToolParams{
				Name:      "search",
				Arguments: map[string]any{"query": "guide project", "path": pathArg},
			},
		})
		if err != nil {
			t.Fatalf("unexpected search error for %q: %v", pathArg, err)
		}
		text := extractToolText(t, result)
		if !strings.Contains(text, "docs/guide.md") || strings.Contains(text, "src/main.go") {
			t.Fatalf("unexpected filtered results for %q: %q", pathArg, text)
		}
	}
}

func TestHandleSearch_PathFilterRejectsPathsOutsideWatchDir(t *testing.T) {
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "quant.db")

	store, err := index.NewStore(dbPath)
	if err != nil {
		t.Fatalf("unexpected store open error: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })

	s := newTestServer(dir, dbPath, store)

	_, err = s.handleSearch(context.Background(), mcplib.CallToolRequest{
		Params: mcplib.CallToolParams{
			Name:      "search",
			Arguments: map[string]any{"query": "alpha", "path": "../outside"},
		},
	})
	if err == nil {
		t.Fatal("expected error for relative path outside watch dir")
	}

	_, err = s.handleSearch(context.Background(), mcplib.CallToolRequest{
		Params: mcplib.CallToolParams{
			Name:      "search",
			Arguments: map[string]any{"query": "alpha", "path": filepath.Join(filepath.Dir(dir), "outside")},
		},
	})
	if err == nil {
		t.Fatal("expected error for absolute path outside watch dir")
	}
}

func TestHandleSearch_EmptyQuery(t *testing.T) {
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "quant.db")

	store, err := index.NewStore(dbPath)
	if err != nil {
		t.Fatalf("unexpected store open error: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })

	s := newTestServer(dir, dbPath, store)

	_, err = s.handleSearch(context.Background(), mcplib.CallToolRequest{
		Params: mcplib.CallToolParams{
			Name:      "search",
			Arguments: map[string]any{"query": ""},
		},
	})
	if err == nil {
		t.Fatal("expected error for empty query")
	}

	_, err = s.handleSearch(context.Background(), mcplib.CallToolRequest{
		Params: mcplib.CallToolParams{
			Name:      "search",
			Arguments: map[string]any{"query": "   "},
		},
	})
	if err == nil {
		t.Fatal("expected error for whitespace-only query")
	}
}

func TestHandleSearch_InvalidLimit(t *testing.T) {
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "quant.db")

	store, err := index.NewStore(dbPath)
	if err != nil {
		t.Fatalf("unexpected store open error: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })

	s := newTestServer(dir, dbPath, store)

	for _, limit := range []float64{0, -1, float64(maxSearchLimit + 1)} {
		_, err := s.handleSearch(context.Background(), mcplib.CallToolRequest{
			Params: mcplib.CallToolParams{
				Name:      "search",
				Arguments: map[string]any{"query": "alpha", "limit": limit},
			},
		})
		if err == nil {
			t.Fatalf("expected error for invalid limit %v", limit)
		}
	}
}

func TestHandleSearch_InvalidThreshold(t *testing.T) {
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "quant.db")

	store, err := index.NewStore(dbPath)
	if err != nil {
		t.Fatalf("unexpected store open error: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })

	s := newTestServer(dir, dbPath, store)

	for _, threshold := range []float64{math.NaN(), math.Inf(1), math.Inf(-1)} {
		_, err := s.handleSearch(context.Background(), mcplib.CallToolRequest{
			Params: mcplib.CallToolParams{
				Name:      "search",
				Arguments: map[string]any{"query": "alpha", "threshold": threshold},
			},
		})
		if err == nil {
			t.Fatalf("expected error for invalid threshold %v", threshold)
		}
	}
}

func TestServeWithShutdown_UsesTimeoutContext(t *testing.T) {
	s := &Server{}
	recorder := &shutdownRecorder{startBlock: make(chan struct{})}

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	err := s.serveWithShutdown(ctx, recorder, ":0")
	if err != nil {
		t.Fatalf("unexpected shutdown error: %v", err)
	}
	if recorder.shutdownCtx == nil {
		t.Fatal("expected shutdown to be called")
	}
	deadline, ok := recorder.shutdownCtx.Deadline()
	if !ok {
		t.Fatal("expected shutdown context to have a deadline")
	}
	remaining := time.Until(deadline)
	if remaining <= 0 || remaining > shutdownTimeout {
		t.Fatalf("expected shutdown deadline within %s, got %s", shutdownTimeout, remaining)
	}

	close(recorder.startBlock)
}

func TestHandleListSources(t *testing.T) {
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "quant.db")

	store, err := index.NewStore(dbPath)
	if err != nil {
		t.Fatalf("unexpected store open error: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })

	if err := store.ReindexDocument(context.Background(), &index.Document{
		Path:       "readme.md",
		Hash:       "readme-hash",
		ModifiedAt: testTime(),
	}, []index.ChunkRecord{{
		Content:    "project readme",
		ChunkIndex: 0,
		Embedding:  index.EncodeFloat32(index.NormalizeFloat32([]float32{1})),
	}}); err != nil {
		t.Fatalf("unexpected seed error: %v", err)
	}

	s := newTestServer(dir, dbPath, store)
	suppressLogs(t)

	result, err := s.handleListSources(context.Background(), mcplib.CallToolRequest{
		Params: mcplib.CallToolParams{Name: "list_sources"},
	})
	if err != nil {
		t.Fatalf("unexpected list_sources error: %v", err)
	}
	text := extractToolText(t, result)
	if !strings.Contains(text, "readme.md") {
		t.Fatalf("expected readme.md in list_sources output, got %q", text)
	}
}

func TestHandleListSources_LimitAndTruncation(t *testing.T) {
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "quant.db")

	store, err := index.NewStore(dbPath)
	if err != nil {
		t.Fatalf("unexpected store open error: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })

	for _, path := range []string{"a.md", "b.md"} {
		if err := store.ReindexDocument(context.Background(), &index.Document{
			Path:       path,
			Hash:       path + "-hash",
			ModifiedAt: testTime(),
		}, []index.ChunkRecord{{
			Content:    path,
			ChunkIndex: 0,
			Embedding:  index.EncodeFloat32(index.NormalizeFloat32([]float32{1})),
		}}); err != nil {
			t.Fatalf("unexpected seed error: %v", err)
		}
	}

	s := newTestServer(dir, dbPath, store)
	suppressLogs(t)

	result, err := s.handleListSources(context.Background(), mcplib.CallToolRequest{
		Params: mcplib.CallToolParams{
			Name:      "list_sources",
			Arguments: map[string]any{"limit": float64(1)},
		},
	})
	if err != nil {
		t.Fatalf("unexpected list_sources error: %v", err)
	}
	text := extractToolText(t, result)
	if !strings.Contains(text, "showing first 1") {
		t.Fatalf("expected truncation header, got %q", text)
	}
	if !strings.Contains(text, "... and 1 more") {
		t.Fatalf("expected truncation summary, got %q", text)
	}
}

func TestHandleListSources_InvalidLimit(t *testing.T) {
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "quant.db")

	store, err := index.NewStore(dbPath)
	if err != nil {
		t.Fatalf("unexpected store open error: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })

	s := newTestServer(dir, dbPath, store)

	for _, limit := range []float64{0, -1, math.NaN(), math.Inf(1), float64(maxSourcesLimit + 1)} {
		_, err := s.handleListSources(context.Background(), mcplib.CallToolRequest{
			Params: mcplib.CallToolParams{
				Name:      "list_sources",
				Arguments: map[string]any{"limit": limit},
			},
		})
		if err == nil {
			t.Fatalf("expected error for invalid limit %v", limit)
		}
	}
}

func TestHandleIndexStatus(t *testing.T) {
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "quant.db")

	store, err := index.NewStore(dbPath)
	if err != nil {
		t.Fatalf("unexpected store open error: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })

	if err := store.ReindexDocument(context.Background(), &index.Document{
		Path:       "test.txt",
		Hash:       "test-hash",
		ModifiedAt: testTime(),
	}, []index.ChunkRecord{
		{Content: "chunk one", ChunkIndex: 0, Embedding: index.EncodeFloat32(index.NormalizeFloat32([]float32{1}))},
		{Content: "chunk two", ChunkIndex: 1, Embedding: index.EncodeFloat32(index.NormalizeFloat32([]float32{1}))},
	}); err != nil {
		t.Fatalf("unexpected seed error: %v", err)
	}

	s := newTestServer(dir, dbPath, store)
	suppressLogs(t)

	result, err := s.handleIndexStatus(context.Background(), mcplib.CallToolRequest{
		Params: mcplib.CallToolParams{Name: "index_status"},
	})
	if err != nil {
		t.Fatalf("unexpected index_status error: %v", err)
	}
	text := extractToolText(t, result)
	if !strings.Contains(text, "Documents: 1") {
		t.Fatalf("expected 1 document in status, got %q", text)
	}
	if !strings.Contains(text, "Chunks: 2") {
		t.Fatalf("expected 2 chunks in status, got %q", text)
	}
	if !strings.Contains(text, "test-model") {
		t.Fatalf("expected model name in status, got %q", text)
	}
}

func newTestServer(dir, dbPath string, store *index.Store) *Server {
	return &Server{
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
}

func suppressLogs(t *testing.T) {
	t.Helper()
	oldWriter := log.Writer()
	oldFlags := log.Flags()
	oldPrefix := log.Prefix()
	log.SetOutput(&bytes.Buffer{})
	log.SetFlags(0)
	log.SetPrefix("")
	t.Cleanup(func() {
		log.SetOutput(oldWriter)
		log.SetFlags(oldFlags)
		log.SetPrefix(oldPrefix)
	})
}

func extractToolText(t *testing.T, result *mcplib.CallToolResult) string {
	t.Helper()
	if result == nil || len(result.Content) == 0 {
		t.Fatal("expected non-empty tool result")
	}
	txt, ok := mcplib.AsTextContent(result.Content[0])
	if !ok {
		t.Fatalf("expected text content, got %T", result.Content[0])
	}
	return txt.Text
}

func testTime() time.Time {
	return time.Unix(1_700_000_000, 0).UTC()
}
