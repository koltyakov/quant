package mcp

import (
	"context"
	"errors"
	"fmt"
	"math"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"github.com/koltyakov/quant/internal/config"
	"github.com/koltyakov/quant/internal/index"
	runtimestate "github.com/koltyakov/quant/internal/runtime"
	"github.com/koltyakov/quant/internal/testutil"
	mcplib "github.com/mark3labs/mcp-go/mcp"
)

func TestEmbeddingStatus_NilError(t *testing.T) {
	t.Parallel()
	if embeddingStatus(nil) != "hybrid" {
		t.Fatal("expected hybrid for nil error")
	}
}

func TestEmbeddingStatus_NonNilError(t *testing.T) {
	t.Parallel()
	if embeddingStatus(fmt.Errorf("fail")) != "keyword_only" {
		t.Fatal("expected keyword_only for non-nil error")
	}
}

func TestEmbeddingNote(t *testing.T) {
	t.Parallel()
	if note := embeddingNote(nil); note != "" {
		t.Fatalf("expected empty note for nil error, got %q", note)
	}
	if note := embeddingNote(fmt.Errorf("unavailable")); note == "" {
		t.Fatal("expected non-empty note for non-nil error")
	}
	if note := embeddingNote(fmt.Errorf("unavailable")); !strings.Contains(note, "keyword-only") {
		t.Fatalf("expected note to mention keyword-only, got %q", note)
	}
}

func TestFormatSearchResults_SingleResult(t *testing.T) {
	t.Parallel()
	results := []index.SearchResult{
		{DocumentPath: "a.md", ChunkIndex: 0, Score: 0.99, ScoreKind: "hybrid", ChunkID: 1, ChunkContent: "hello"},
	}
	output := formatSearchResults(results)
	if !strings.Contains(output, "a.md") || !strings.Contains(output, "hello") {
		t.Fatalf("expected result content in output, got %q", output)
	}
}

func TestFormatSearchResults_MultipleResults(t *testing.T) {
	t.Parallel()
	results := []index.SearchResult{
		{DocumentPath: "a.md", ChunkIndex: 0, Score: 0.95, ScoreKind: "hybrid", ChunkID: 1, ChunkContent: "first"},
		{DocumentPath: "b.md", ChunkIndex: 1, Score: 0.85, ScoreKind: "hybrid", ChunkID: 2, ChunkContent: "second"},
	}
	output := formatSearchResults(results)
	if !strings.Contains(output, "a.md") || !strings.Contains(output, "b.md") {
		t.Fatalf("expected both documents in output, got %q", output)
	}
	if !strings.Contains(output, "Result 1") || !strings.Contains(output, "Result 2") {
		t.Fatalf("expected numbered results, got %q", output)
	}
}

func TestFormatSearchResults_Truncation(t *testing.T) {
	t.Parallel()
	var results []index.SearchResult
	for i := 0; i < 20; i++ {
		results = append(results, index.SearchResult{
			DocumentPath: fmt.Sprintf("file%d.md", i),
			ChunkIndex:   i,
			Score:        0.9,
			ScoreKind:    "hybrid",
			ChunkID:      int64(i),
			ChunkContent: strings.Repeat("content word ", 500),
		})
	}
	output := formatSearchResults(results)
	if !strings.Contains(output, "omitted") {
		t.Fatalf("expected truncation marker, got output length %d", len(output))
	}
}

func TestHandleSearch_WithFileTypeFilter(t *testing.T) {
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "quant.db")
	store, err := index.NewStore(dbPath)
	if err != nil {
		t.Fatalf("unexpected store open error: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })

	if err := store.ReindexDocument(context.Background(), &index.Document{
		Path: "test.txt", Hash: "test-hash", ModifiedAt: testTime(),
	}, []index.ChunkRecord{{
		Content: "unique searchable content", ChunkIndex: 0,
		Embedding: index.EncodeFloat32(index.NormalizeFloat32([]float32{1})),
	}}); err != nil {
		t.Fatalf("unexpected seed error: %v", err)
	}

	s := newTestServer(dir, dbPath, store)
	suppressLogs(t)

	result, err := s.handleSearch(context.Background(), mcplib.CallToolRequest{
		Params: mcplib.CallToolParams{
			Name: "search",
			Arguments: map[string]any{
				"query":     "unique searchable content",
				"file_type": "txt",
			},
		},
	})
	if err != nil {
		t.Fatalf("unexpected search error with file_type filter: %v", err)
	}
	text := extractToolText(t, result)
	if !strings.Contains(text, "test.txt") {
		t.Fatalf("expected test.txt in results, got %q", text)
	}
}

func TestHandleSearch_EmbeddingFallback(t *testing.T) {
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
	}, []index.ChunkRecord{{
		Content:    "test content",
		ChunkIndex: 0,
		Embedding:  index.EncodeFloat32(index.NormalizeFloat32([]float32{1})),
	}}); err != nil {
		t.Fatalf("unexpected seed error: %v", err)
	}

	s := &Server{
		cfg:      &config.Config{WatchDir: dir, DBPath: dbPath, EmbedModel: "test-model"},
		store:    store,
		embedder: nil,
		state:    runtimestate.NewIndexStateTracker(),
	}

	suppressLogs(t)
	result, err := s.handleSearch(context.Background(), mcplib.CallToolRequest{
		Params: mcplib.CallToolParams{
			Name:      "search",
			Arguments: map[string]any{"query": "test content"},
		},
	})
	if err != nil {
		t.Fatalf("unexpected search error: %v", err)
	}
	text := extractToolText(t, result)
	if !strings.Contains(text, "test.txt") {
		t.Fatalf("expected results in keyword-only mode, got %q", text)
	}
}

func TestHandleSearch_QueryTruncation(t *testing.T) {
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "quant.db")
	store, err := index.NewStore(dbPath)
	if err != nil {
		t.Fatalf("unexpected store open error: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })

	s := newTestServer(dir, dbPath, store)
	suppressLogs(t)

	longQuery := strings.Repeat("word ", 2000)
	_, err = s.handleSearch(context.Background(), mcplib.CallToolRequest{
		Params: mcplib.CallToolParams{
			Name:      "search",
			Arguments: map[string]any{"query": longQuery},
		},
	})
	if err != nil {
		t.Fatalf("unexpected error for long query: %v", err)
	}
}

func TestHandleListSources_NoDocuments(t *testing.T) {
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "quant.db")
	store, err := index.NewStore(dbPath)
	if err != nil {
		t.Fatalf("unexpected store open error: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })

	s := newTestServer(dir, dbPath, store)
	suppressLogs(t)

	result, err := s.handleListSources(context.Background(), mcplib.CallToolRequest{
		Params: mcplib.CallToolParams{Name: "list_sources"},
	})
	if err != nil {
		t.Fatalf("unexpected list_sources error: %v", err)
	}
	text := extractToolText(t, result)
	if !strings.Contains(text, "No documents indexed") {
		t.Fatalf("expected no documents message, got %q", text)
	}
}

func TestHandleDeleteCollection_EmptyCollection(t *testing.T) {
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "quant.db")
	store, err := index.NewStore(dbPath)
	if err != nil {
		t.Fatalf("unexpected store open error: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })

	s := newTestServer(dir, dbPath, store)
	suppressLogs(t)

	result, err := s.handleDeleteCollection(context.Background(), mcplib.CallToolRequest{
		Params: mcplib.CallToolParams{
			Name:      "delete_collection",
			Arguments: map[string]any{"collection": "nonexistent"},
		},
	})
	if err != nil {
		t.Fatalf("unexpected delete error: %v", err)
	}
	text := extractToolText(t, result)
	if !strings.Contains(text, "nonexistent") {
		t.Fatalf("expected collection name in output, got %q", text)
	}
}

func TestHandleDrillDown_InvalidChunkID(t *testing.T) {
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "quant.db")
	store, err := index.NewStore(dbPath)
	if err != nil {
		t.Fatalf("unexpected store open error: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })

	s := newTestServer(dir, dbPath, store)

	for _, chunkID := range []float64{-1, 0, math.NaN(), math.Inf(1)} {
		_, err := s.handleDrillDown(context.Background(), mcplib.CallToolRequest{
			Params: mcplib.CallToolParams{
				Name:      "drill_down",
				Arguments: map[string]any{"chunk_id": chunkID},
			},
		})
		if err == nil {
			t.Fatalf("expected error for invalid chunk_id %v", chunkID)
		}
	}
}

func TestHandleDrillDown_WithLimit(t *testing.T) {
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "quant.db")
	store, err := index.NewStore(dbPath)
	if err != nil {
		t.Fatalf("unexpected store open error: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })

	if err := store.ReindexDocument(context.Background(), &index.Document{
		Path: "docs/alpha.md", Hash: "alpha-hash", ModifiedAt: testTime(),
	}, []index.ChunkRecord{{
		Content: "alpha content", ChunkIndex: 0, Embedding: index.EncodeFloat32(index.NormalizeFloat32([]float32{1})),
	}}); err != nil {
		t.Fatalf("unexpected seed error: %v", err)
	}

	chunkMap, err := store.GetDocumentChunksByPath(context.Background(), "docs/alpha.md")
	if err != nil || len(chunkMap) == 0 {
		t.Fatalf("unexpected chunks lookup error: %v", err)
	}
	var chunkID int64
	for _, ch := range chunkMap {
		chunkID = ch.ID
		break
	}

	s := newTestServer(dir, dbPath, store)
	suppressLogs(t)

	result, err := s.handleDrillDown(context.Background(), mcplib.CallToolRequest{
		Params: mcplib.CallToolParams{
			Name: "drill_down",
			Arguments: map[string]any{
				"chunk_id": float64(chunkID),
				"limit":    float64(3),
			},
		},
	})
	if err != nil {
		t.Fatalf("unexpected drill_down error: %v", err)
	}
	text := extractToolText(t, result)
	if !strings.Contains(text, "Drill-down") {
		t.Fatalf("expected drill-down header, got %q", text)
	}
}

func TestHandleFindSimilar_NonIntegerChunkID(t *testing.T) {
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "quant.db")
	store, err := index.NewStore(dbPath)
	if err != nil {
		t.Fatalf("unexpected store open error: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })

	s := newTestServer(dir, dbPath, store)

	_, err = s.handleFindSimilar(context.Background(), mcplib.CallToolRequest{
		Params: mcplib.CallToolParams{
			Name:      "find_similar",
			Arguments: map[string]any{"chunk_id": 1.5},
		},
	})
	if err == nil {
		t.Fatal("expected error for non-integer chunk_id")
	}
}

func TestHandleIndexStatus_WithoutState(t *testing.T) {
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "quant.db")
	store, err := index.NewStore(dbPath)
	if err != nil {
		t.Fatalf("unexpected store open error: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })

	s := &Server{
		cfg:      &config.Config{WatchDir: dir, DBPath: dbPath, EmbedModel: "test-model"},
		store:    store,
		embedder: &testutil.QueryCountingEmbedder{},
		state:    nil,
	}
	suppressLogs(t)

	result, err := s.handleIndexStatus(context.Background(), mcplib.CallToolRequest{
		Params: mcplib.CallToolParams{Name: "index_status"},
	})
	if err != nil {
		t.Fatalf("unexpected index_status error: %v", err)
	}
	text := extractToolText(t, result)
	if !strings.Contains(text, "Documents: 0") {
		t.Fatalf("expected 0 documents, got %q", text)
	}
}

func TestHandleIndexStatus_WithDegradedState(t *testing.T) {
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "quant.db")
	store, err := index.NewStore(dbPath)
	if err != nil {
		t.Fatalf("unexpected store open error: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })

	tracker := runtimestate.NewIndexStateTracker()
	tracker.Set(runtimestate.IndexStateDegraded, "embedding issue")
	s := &Server{
		cfg:      &config.Config{WatchDir: dir, DBPath: dbPath, EmbedModel: "test-model"},
		store:    store,
		embedder: &testutil.QueryCountingEmbedder{},
		state:    tracker,
	}
	suppressLogs(t)

	result, err := s.handleIndexStatus(context.Background(), mcplib.CallToolRequest{
		Params: mcplib.CallToolParams{Name: "index_status"},
	})
	if err != nil {
		t.Fatalf("unexpected index_status error: %v", err)
	}
	text := extractToolText(t, result)
	if !strings.Contains(text, "degraded") {
		t.Fatalf("expected degraded state in output, got %q", text)
	}
}

func TestEmbeddingStatus_Server_WithEmbedderProvider(t *testing.T) {
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "quant.db")
	store, err := index.NewStore(dbPath)
	if err != nil {
		t.Fatalf("unexpected store open error: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })

	s := &Server{store: store, embedder: &testutil.QueryCountingEmbedder{}}
	status := s.embeddingStatus(context.Background())
	if status != "available" {
		t.Fatalf("expected available, got %q", status)
	}
}

func TestEmbeddingStatus_Server_NilEmbedderNoProvider(t *testing.T) {
	s := &Server{store: nil, embedder: nil}
	status := s.embeddingStatus(context.Background())
	if !strings.Contains(status, "keyword-only") {
		t.Fatalf("expected keyword-only status, got %q", status)
	}
}

type fakeEmbeddingStatusProvider struct {
	status string
	err    error
}

func (f *fakeEmbeddingStatusProvider) PingContext(context.Context) error { return nil }
func (f *fakeEmbeddingStatusProvider) EmbeddingStatus(context.Context) (string, error) {
	return f.status, f.err
}
func (f *fakeEmbeddingStatusProvider) Search(context.Context, string, []float32, int, string) ([]index.SearchResult, error) {
	return nil, nil
}
func (f *fakeEmbeddingStatusProvider) SearchFiltered(context.Context, string, []float32, int, string, index.SearchFilter) ([]index.SearchResult, error) {
	return nil, nil
}
func (f *fakeEmbeddingStatusProvider) FindSimilar(context.Context, int64, int) ([]index.SearchResult, error) {
	return nil, nil
}
func (f *fakeEmbeddingStatusProvider) GetChunkByID(context.Context, int64) (*index.SearchResult, error) {
	return nil, nil
}
func (f *fakeEmbeddingStatusProvider) GetDocumentByPath(context.Context, string) (*index.Document, error) {
	return nil, nil
}
func (f *fakeEmbeddingStatusProvider) GetDocumentChunksByPath(context.Context, string) (map[string]index.ChunkRecord, error) {
	return nil, nil
}
func (f *fakeEmbeddingStatusProvider) ListDocuments(context.Context) ([]index.Document, error) {
	return nil, nil
}
func (f *fakeEmbeddingStatusProvider) ListDocumentsLimit(context.Context, int) ([]index.Document, error) {
	return nil, nil
}
func (f *fakeEmbeddingStatusProvider) Stats(context.Context) (int, int, error) {
	return 0, 0, nil
}
func (f *fakeEmbeddingStatusProvider) ListCollections(context.Context) ([]string, error) {
	return nil, nil
}
func (f *fakeEmbeddingStatusProvider) CollectionStats(context.Context, string) (int, int, error) {
	return 0, 0, nil
}
func (f *fakeEmbeddingStatusProvider) DeleteCollection(context.Context, string) error {
	return nil
}

func TestEmbeddingStatus_Server_WithProvider(t *testing.T) {
	t.Parallel()
	fake := &fakeEmbeddingStatusProvider{status: "running"}
	s := &Server{store: fake, embedder: nil}
	status := s.embeddingStatus(context.Background())
	if status != "running" {
		t.Fatalf("expected running from provider, got %q", status)
	}
}

func TestEmbeddingStatus_Server_WithProviderEmpty(t *testing.T) {
	t.Parallel()
	fake := &fakeEmbeddingStatusProvider{status: ""}
	s := &Server{store: fake, embedder: nil}
	status := s.embeddingStatus(context.Background())
	if !strings.Contains(status, "keyword-only") {
		t.Fatalf("expected keyword-only fallback for empty provider status, got %q", status)
	}
}

func TestEmbeddingStatus_Server_WithProviderError(t *testing.T) {
	t.Parallel()
	fake := &fakeEmbeddingStatusProvider{status: "would-be-ignored", err: errors.New("fail")}
	s := &Server{store: fake, embedder: nil}
	status := s.embeddingStatus(context.Background())
	if !strings.Contains(status, "keyword-only") {
		t.Fatalf("expected keyword-only fallback on provider error, got %q", status)
	}
}

func TestWriteProbeResponse_MethodNotAllowed(t *testing.T) {
	t.Parallel()
	s := &Server{}
	for _, method := range []string{http.MethodPost, http.MethodPut, http.MethodDelete} {
		req := httptest.NewRequest(method, healthPath, nil)
		rec := httptest.NewRecorder()
		s.writeProbeResponse(rec, req, http.StatusOK, "ok\n")
		if rec.Code != http.StatusMethodNotAllowed {
			t.Fatalf("expected status %d for method %s, got %d", http.StatusMethodNotAllowed, method, rec.Code)
		}
		if rec.Header().Get("Allow") != "GET, HEAD" {
			t.Fatalf("expected Allow header, got %q", rec.Header().Get("Allow"))
		}
	}
}

func TestWriteProbeResponse_HeadMethod(t *testing.T) {
	t.Parallel()
	s := &Server{}
	req := httptest.NewRequest(http.MethodHead, healthPath, nil)
	rec := httptest.NewRecorder()
	s.writeProbeResponse(rec, req, http.StatusOK, "ok\n")
	if rec.Code != http.StatusOK {
		t.Fatalf("expected status %d, got %d", http.StatusOK, rec.Code)
	}
	if rec.Body.Len() != 0 {
		t.Fatalf("expected empty body for HEAD request, got %q", rec.Body.String())
	}
}

func TestWriteProbeResponse_GetMethod(t *testing.T) {
	t.Parallel()
	s := &Server{}
	req := httptest.NewRequest(http.MethodGet, healthPath, nil)
	rec := httptest.NewRecorder()
	s.writeProbeResponse(rec, req, http.StatusOK, "ok\n")
	if rec.Code != http.StatusOK {
		t.Fatalf("expected status %d, got %d", http.StatusOK, rec.Code)
	}
	if rec.Body.String() != "ok\n" {
		t.Fatalf("expected body 'ok\\n', got %q", rec.Body.String())
	}
	if ct := rec.Header().Get("Content-Type"); !strings.Contains(ct, "text/plain") {
		t.Fatalf("expected text/plain content type, got %q", ct)
	}
}

func TestReadinessError_NilStore(t *testing.T) {
	t.Parallel()
	s := &Server{store: nil}
	err := s.readinessError(context.Background())
	if err == nil {
		t.Fatal("expected error when store is nil")
	}
}

func TestReadinessError_StartupState(t *testing.T) {
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "quant.db")
	store, err := index.NewStore(dbPath)
	if err != nil {
		t.Fatalf("unexpected store open error: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })

	tracker := runtimestate.NewIndexStateTracker()
	s := &Server{store: store, state: tracker}
	err = s.readinessError(context.Background())
	if err == nil {
		t.Fatal("expected error when state is starting")
	}
}

func TestReadinessError_ReadyState(t *testing.T) {
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "quant.db")
	store, err := index.NewStore(dbPath)
	if err != nil {
		t.Fatalf("unexpected store open error: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })

	tracker := runtimestate.NewIndexStateTracker()
	tracker.Set(runtimestate.IndexStateReady, "ready")
	s := &Server{store: store, state: tracker}
	err = s.readinessError(context.Background())
	if err != nil {
		t.Fatalf("expected no error when ready, got %v", err)
	}
}

func TestReadinessError_IndexingStateWithMessage(t *testing.T) {
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "quant.db")
	store, err := index.NewStore(dbPath)
	if err != nil {
		t.Fatalf("unexpected store open error: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })

	tracker := runtimestate.NewIndexStateTracker()
	tracker.Set(runtimestate.IndexStateIndexing, "scanning files")
	s := &Server{store: store, state: tracker}
	err = s.readinessError(context.Background())
	if err == nil {
		t.Fatal("expected error when indexing")
	}
	if !strings.Contains(err.Error(), "scanning files") {
		t.Fatalf("expected error to contain message, got %v", err)
	}
}

func TestNormalizeSearchPathPrefix(t *testing.T) {
	t.Parallel()
	tests := []struct {
		watchDir string
		raw      string
		want     string
		wantErr  bool
	}{
		{"/project", "", "", false},
		{"/project", "src", "src", false},
		{"/project", "src/", "src/", false},
		{"/project", "/project/src", "src", false},
		{"/project", "../outside", "", true},
	}
	for _, tt := range tests {
		got, err := normalizeSearchPathPrefix(tt.watchDir, tt.raw)
		if (err != nil) != tt.wantErr {
			t.Errorf("normalizeSearchPathPrefix(%q, %q) error=%v, wantErr=%v", tt.watchDir, tt.raw, err, tt.wantErr)
		}
		if !tt.wantErr && got != tt.want {
			t.Errorf("normalizeSearchPathPrefix(%q, %q) = %q, want %q", tt.watchDir, tt.raw, got, tt.want)
		}
	}
}

func TestFormatSearchSpotlights(t *testing.T) {
	t.Parallel()
	if got := formatSearchSpotlights(nil, 3); got != "none" {
		t.Fatalf("expected 'none' for nil results, got %q", got)
	}
	if got := formatSearchSpotlights(nil, 0); got != "none" {
		t.Fatalf("expected 'none' for nil results with limit 0, got %q", got)
	}
	results := []index.SearchResult{
		{DocumentPath: "a.md", ChunkIndex: 0, Score: 0.5, ScoreKind: "hybrid", ChunkContent: "content"},
	}
	spotlight := formatSearchSpotlights(results, 1)
	if !strings.Contains(spotlight, "a.md") {
		t.Fatalf("expected spotlight to contain path, got %q", spotlight)
	}
}

func TestFormatDocumentSpotlights(t *testing.T) {
	t.Parallel()
	if got := formatDocumentSpotlights(nil, 3); got != "none" {
		t.Fatalf("expected 'none' for nil docs, got %q", got)
	}
	docs := []index.Document{
		{Path: "test.md"},
	}
	got := formatDocumentSpotlights(docs, 1)
	if !strings.Contains(got, "test.md") {
		t.Fatalf("expected doc path in spotlight, got %q", got)
	}
}

func TestSearchRows(t *testing.T) {
	t.Parallel()
	results := []index.SearchResult{
		{DocumentPath: "a.md", ChunkIndex: 0, Score: 0.5, ScoreKind: "hybrid", ChunkID: 1, ChunkContent: "hello"},
	}
	rows := searchRows(results)
	if len(rows) != 1 {
		t.Fatalf("expected 1 row, got %d", len(rows))
	}
	if rows[0].Path != "a.md" {
		t.Fatalf("expected path a.md, got %q", rows[0].Path)
	}
}

func TestListSourceRows(t *testing.T) {
	t.Parallel()
	docs := []index.Document{
		{Path: "test.md", IndexedAt: testTime()},
	}
	rows := listSourceRows(docs)
	if len(rows) != 1 {
		t.Fatalf("expected 1 row, got %d", len(rows))
	}
	if rows[0].Path != "test.md" {
		t.Fatalf("expected path test.md, got %q", rows[0].Path)
	}
}

func TestSqliteDiskUsage(t *testing.T) {
	t.Parallel()
	size := sqliteDiskUsage("/nonexistent/path.db")
	if size != 0 {
		t.Fatalf("expected 0 for nonexistent path, got %d", size)
	}
}

func TestHandleSearch_LanguageFilter(t *testing.T) {
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "quant.db")
	store, err := index.NewStore(dbPath)
	if err != nil {
		t.Fatalf("unexpected store open error: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })

	s := newTestServer(dir, dbPath, store)
	suppressLogs(t)

	result, err := s.handleSearch(context.Background(), mcplib.CallToolRequest{
		Params: mcplib.CallToolParams{
			Name: "search",
			Arguments: map[string]any{
				"query":    "test",
				"language": "go",
			},
		},
	})
	if err != nil {
		t.Fatalf("unexpected search error: %v", err)
	}
	text := extractToolText(t, result)
	if !strings.Contains(text, "No results found") {
		t.Fatalf("expected no results for language filter on empty index, got %q", text)
	}
}
