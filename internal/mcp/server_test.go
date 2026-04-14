package mcp

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"math"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/koltyakov/quant/internal/config"
	"github.com/koltyakov/quant/internal/index"
	"github.com/koltyakov/quant/internal/logx"
	runtimestate "github.com/koltyakov/quant/internal/runtime"
	"github.com/koltyakov/quant/internal/testutil"
	mcplib "github.com/mark3labs/mcp-go/mcp"
)

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
		store:    store,
		embedder: &testutil.QueryCountingEmbedder{},
	}

	var buf bytes.Buffer
	logx.SetOutput(&buf)
	t.Cleanup(func() {
		logx.SetOutput(io.Discard)
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
	if !strings.Contains(logOutput, `msg="MCP search request"`) || !strings.Contains(logOutput, `query="alpha search phrase"`) {
		t.Fatalf("expected request log, got %q", logOutput)
	}
	if !strings.Contains(logOutput, `msg="MCP search result"`) || !strings.Contains(logOutput, `query="alpha search phrase"`) {
		t.Fatalf("expected result log, got %q", logOutput)
	}
	if !strings.Contains(logOutput, `notes/alpha.txt#0`) {
		t.Fatalf("expected spotlight path in log, got %q", logOutput)
	}
	if !strings.Contains(logOutput, `alpha search phrase with useful spotlight text`) {
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
	structured := extractSearchStructured(t, result)
	if structured.Query != "compiled language" {
		t.Fatalf("expected structured query to be preserved, got %+v", structured)
	}
	if len(structured.Results) == 0 {
		t.Fatalf("expected structured results, got %+v", structured)
	}
}

func TestHandleSearch_TruncatesLargeResultsToOutputBudget(t *testing.T) {
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "quant.db")

	store, err := index.NewStore(dbPath)
	if err != nil {
		t.Fatalf("unexpected store open error: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })

	longChunk := strings.Repeat("alpha ", maxSearchOutputRunes)
	for i := 0; i < 16; i++ {
		path := fmt.Sprintf("notes/%d.txt", i)
		if err := store.ReindexDocument(context.Background(), &index.Document{
			Path:       path,
			Hash:       path + "-hash",
			ModifiedAt: testTime(),
		}, []index.ChunkRecord{{
			Content:    longChunk,
			ChunkIndex: 0,
			Embedding:  index.EncodeFloat32(index.NormalizeFloat32([]float32{1})),
		}}); err != nil {
			t.Fatalf("unexpected seed error: %v", err)
		}
	}

	s := newTestServer(dir, dbPath, store)
	suppressLogs(t)

	result, err := s.handleSearch(context.Background(), mcplib.CallToolRequest{
		Params: mcplib.CallToolParams{
			Name:      "search",
			Arguments: map[string]any{"query": "alpha", "limit": float64(16)},
		},
	})
	if err != nil {
		t.Fatalf("unexpected search error: %v", err)
	}

	text := extractToolText(t, result)
	if !strings.Contains(text, "[chunk content truncated]") {
		t.Fatalf("expected chunk truncation marker, got %q", text)
	}
	if !strings.Contains(text, "[omitted") {
		t.Fatalf("expected output budget marker, got %q", text)
	}
	if len([]rune(text)) > maxSearchOutputRunes+128 {
		t.Fatalf("expected bounded output, got %d runes", len([]rune(text)))
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

func TestNewStreamableHTTPServer_ExposesHealthAndReadiness(t *testing.T) {
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "quant.db")

	store, err := index.NewStore(dbPath)
	if err != nil {
		t.Fatalf("unexpected store open error: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })

	s := newTestServer(dir, dbPath, store)
	_, httpServer := s.newStreamableHTTPServer(":0")

	for _, tc := range []struct {
		path string
		body string
	}{
		{path: healthPath, body: "ok\n"},
		{path: readinessPath, body: "ready\n"},
	} {
		req := httptest.NewRequest(http.MethodGet, tc.path, nil)
		rec := httptest.NewRecorder()
		httpServer.Handler.ServeHTTP(rec, req)

		if rec.Code != http.StatusOK {
			t.Fatalf("%s returned status %d, want %d", tc.path, rec.Code, http.StatusOK)
		}
		if rec.Body.String() != tc.body {
			t.Fatalf("%s returned body %q, want %q", tc.path, rec.Body.String(), tc.body)
		}
	}
}

func TestNewSSEServer_ExposesHealthAndReadiness(t *testing.T) {
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "quant.db")

	store, err := index.NewStore(dbPath)
	if err != nil {
		t.Fatalf("unexpected store open error: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })

	s := newTestServer(dir, dbPath, store)
	_, httpServer := s.newSSEServer(":0")

	for _, tc := range []struct {
		path string
		body string
	}{
		{path: healthPath, body: "ok\n"},
		{path: readinessPath, body: "ready\n"},
	} {
		req := httptest.NewRequest(http.MethodGet, tc.path, nil)
		rec := httptest.NewRecorder()
		httpServer.Handler.ServeHTTP(rec, req)

		if rec.Code != http.StatusOK {
			t.Fatalf("%s returned status %d, want %d", tc.path, rec.Code, http.StatusOK)
		}
		if rec.Body.String() != tc.body {
			t.Fatalf("%s returned body %q, want %q", tc.path, rec.Body.String(), tc.body)
		}
	}
}

func TestHandleReadiness_ReturnsServiceUnavailableWhenDependenciesAreMissing(t *testing.T) {
	s := &Server{}

	req := httptest.NewRequest(http.MethodGet, readinessPath, nil)
	rec := httptest.NewRecorder()
	s.handleReadiness(rec, req)

	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("expected status %d, got %d", http.StatusServiceUnavailable, rec.Code)
	}
	if rec.Body.String() != "not ready\n" {
		t.Fatalf("expected not-ready body, got %q", rec.Body.String())
	}
}

func TestHandleReadiness_ReturnsServiceUnavailableWhileIndexStarting(t *testing.T) {
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "quant.db")

	store, err := index.NewStore(dbPath)
	if err != nil {
		t.Fatalf("unexpected store open error: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })

	tracker := runtimestate.NewIndexStateTracker()
	s := &Server{
		cfg:      &config.Config{WatchDir: dir, DBPath: dbPath},
		store:    store,
		embedder: &testutil.QueryCountingEmbedder{},
		state:    tracker,
	}

	req := httptest.NewRequest(http.MethodGet, readinessPath, nil)
	rec := httptest.NewRecorder()
	s.handleReadiness(rec, req)

	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("expected status %d, got %d", http.StatusServiceUnavailable, rec.Code)
	}
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
	if !strings.Contains(text, "FTS: empty=false, logical_rows=2") {
		t.Fatalf("expected FTS diagnostics in status, got %q", text)
	}
	structured := extractIndexStatusStructured(t, result)
	if structured.State != string(runtimestate.IndexStateReady) {
		t.Fatalf("expected ready structured state, got %+v", structured)
	}
	if structured.FTS == nil || structured.FTS.Empty || structured.FTS.LogicalRows != 2 {
		t.Fatalf("expected populated FTS diagnostics, got %+v", structured)
	}
}

func TestHandleIndexStatus_IncludesSQLiteSidecars(t *testing.T) {
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "quant.db")

	store, err := index.NewStore(dbPath)
	if err != nil {
		t.Fatalf("unexpected store open error: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })

	if err := store.ReindexDocument(context.Background(), &index.Document{
		Path:       "sidecars.txt",
		Hash:       "sidecars-hash",
		ModifiedAt: testTime(),
	}, []index.ChunkRecord{{
		Content:    "forces sqlite wal sidecars to exist",
		ChunkIndex: 0,
		Embedding:  index.EncodeFloat32(index.NormalizeFloat32([]float32{1})),
	}}); err != nil {
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
	if _, err := os.Stat(dbPath + "-wal"); err != nil {
		t.Fatalf("expected sqlite wal sidecar to exist, got %v", err)
	}
	if !strings.Contains(text, formatBytes(sqliteDiskUsage(dbPath))) {
		t.Fatalf("expected combined sqlite sidecar size in status, got %q", text)
	}
}

func TestNewServer_UsesRuntimeVersion(t *testing.T) {
	s := NewServer(&config.Config{}, nil, &testutil.QueryCountingEmbedder{}, "v9.9.9", nil)
	if s.version != "v9.9.9" {
		t.Fatalf("expected server version to match runtime version, got %q", s.version)
	}
}

func newTestServer(dir, dbPath string, store *index.Store) *Server {
	tracker := runtimestate.NewIndexStateTracker()
	tracker.Set(runtimestate.IndexStateReady, "test ready")
	return &Server{
		cfg: &config.Config{
			WatchDir:   dir,
			DBPath:     dbPath,
			EmbedModel: "test-model",
		},
		store:    store,
		embedder: &testutil.QueryCountingEmbedder{},
		version:  "test-version",
		state:    tracker,
	}
}

func suppressLogs(t *testing.T) {
	t.Helper()
	logx.SetOutput(io.Discard)
	t.Cleanup(func() {
		logx.SetOutput(io.Discard)
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

func extractSearchStructured(t *testing.T, result *mcplib.CallToolResult) searchToolResponse {
	t.Helper()
	structured, ok := result.StructuredContent.(searchToolResponse)
	if !ok {
		t.Fatalf("expected search structured content, got %T", result.StructuredContent)
	}
	return structured
}

func extractIndexStatusStructured(t *testing.T, result *mcplib.CallToolResult) indexStatusToolResponse {
	t.Helper()
	structured, ok := result.StructuredContent.(indexStatusToolResponse)
	if !ok {
		t.Fatalf("expected index_status structured content, got %T", result.StructuredContent)
	}
	return structured
}

func testTime() time.Time {
	return time.Unix(1_700_000_000, 0).UTC()
}
