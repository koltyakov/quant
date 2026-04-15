package proxy

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/koltyakov/quant/internal/index"
	runtimestate "github.com/koltyakov/quant/internal/runtime"
)

func TestHandlePing_Success(t *testing.T) {
	t.Parallel()
	store := &fakeSearcher{}
	s := NewServer(store, nil, nil)
	req := httptest.NewRequest(http.MethodGet, "/proxy/ping", nil)
	rec := httptest.NewRecorder()
	s.handlePing(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("expected status %d, got %d", http.StatusOK, rec.Code)
	}
	var resp map[string]string
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("unexpected json error: %v", err)
	}
	if resp["status"] != "ok" {
		t.Fatalf("expected status ok, got %q", resp["status"])
	}
}

func TestHandlePing_StoreError(t *testing.T) {
	t.Parallel()
	store := &pingErrorSearcher{err: errors.New("unreachable")}
	s := NewServer(store, nil, nil)
	req := httptest.NewRequest(http.MethodGet, "/proxy/ping", nil)
	rec := httptest.NewRecorder()
	s.handlePing(rec, req)
	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("expected status %d, got %d", http.StatusServiceUnavailable, rec.Code)
	}
}

type pingErrorSearcher struct {
	fakeSearcher
	err error
}

func (f *pingErrorSearcher) PingContext(context.Context) error {
	return f.err
}

func TestHandleStats(t *testing.T) {
	t.Parallel()
	store := &fakeSearcher{statsDoc: 5, statsChunk: 10}
	s := NewServer(store, nil, nil)
	req := httptest.NewRequest(http.MethodGet, "/proxy/stats", nil)
	rec := httptest.NewRecorder()
	s.handleStats(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("expected status %d, got %d", http.StatusOK, rec.Code)
	}
	var resp StatsResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("unexpected json error: %v", err)
	}
	if resp.DocCount != 5 || resp.ChunkCount != 10 {
		t.Fatalf("expected 5 docs and 10 chunks, got docs=%d chunks=%d", resp.DocCount, resp.ChunkCount)
	}
}

func TestHandleStats_Error(t *testing.T) {
	t.Parallel()
	store := &statsErrorSearcher{err: errors.New("db fail")}
	s := NewServer(store, nil, nil)
	req := httptest.NewRequest(http.MethodGet, "/proxy/stats", nil)
	rec := httptest.NewRecorder()
	s.handleStats(rec, req)
	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("expected status %d, got %d", http.StatusInternalServerError, rec.Code)
	}
}

type statsErrorSearcher struct {
	fakeSearcher
	err error
}

func (s *statsErrorSearcher) Stats(context.Context) (int, int, error) {
	return 0, 0, s.err
}

func TestHandleListCollections(t *testing.T) {
	t.Parallel()
	store := &fakeSearcher{collections: []string{"alpha", "beta"}}
	s := NewServer(store, nil, nil)
	req := httptest.NewRequest(http.MethodGet, "/proxy/list_collections", nil)
	rec := httptest.NewRecorder()
	s.handleListCollections(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("expected status %d, got %d", http.StatusOK, rec.Code)
	}
	var resp ListCollectionsResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("unexpected json error: %v", err)
	}
	if len(resp.Collections) != 2 {
		t.Fatalf("expected 2 collections, got %d", len(resp.Collections))
	}
}

func TestHandleListCollections_Error(t *testing.T) {
	t.Parallel()
	store := &collectionsErrorSearcher{err: errors.New("db fail")}
	s := NewServer(store, nil, nil)
	req := httptest.NewRequest(http.MethodGet, "/proxy/list_collections", nil)
	rec := httptest.NewRecorder()
	s.handleListCollections(rec, req)
	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("expected status %d, got %d", http.StatusInternalServerError, rec.Code)
	}
}

type collectionsErrorSearcher struct {
	fakeSearcher
	err error
}

func (s *collectionsErrorSearcher) ListCollections(context.Context) ([]string, error) {
	return nil, s.err
}

func TestHandleDeleteCollection_Success(t *testing.T) {
	t.Parallel()
	store := &fakeSearcher{}
	s := NewServer(store, nil, nil)
	body := `{"collection":"test-collection"}`
	req := httptest.NewRequest(http.MethodPost, "/proxy/delete_collection", strings.NewReader(body))
	rec := httptest.NewRecorder()
	s.handleDeleteCollection(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("expected status %d, got %d; body: %s", http.StatusOK, rec.Code, rec.Body.String())
	}
	var resp DeleteCollectionResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("unexpected json error: %v", err)
	}
	if !resp.Deleted {
		t.Fatal("expected deleted=true")
	}
}

func TestHandleDeleteCollection_WrongMethod(t *testing.T) {
	t.Parallel()
	store := &fakeSearcher{}
	s := NewServer(store, nil, nil)
	req := httptest.NewRequest(http.MethodGet, "/proxy/delete_collection", nil)
	rec := httptest.NewRecorder()
	s.handleDeleteCollection(rec, req)
	if rec.Code != http.StatusMethodNotAllowed {
		t.Fatalf("expected status %d, got %d", http.StatusMethodNotAllowed, rec.Code)
	}
}

func TestHandleDeleteCollection_StoreError(t *testing.T) {
	t.Parallel()
	store := &deleteCollErrorSearcher{err: errors.New("db fail")}
	s := NewServer(store, nil, nil)
	body := `{"collection":"test-collection"}`
	req := httptest.NewRequest(http.MethodPost, "/proxy/delete_collection", strings.NewReader(body))
	rec := httptest.NewRecorder()
	s.handleDeleteCollection(rec, req)
	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("expected status %d, got %d", http.StatusInternalServerError, rec.Code)
	}
}

type deleteCollErrorSearcher struct {
	fakeSearcher
	err error
}

func (s *deleteCollErrorSearcher) DeleteCollection(context.Context, string) error {
	return s.err
}

func TestHandleChunkByID(t *testing.T) {
	t.Parallel()
	store := &fakeSearcher{
		chunkByID: &index.SearchResult{ChunkID: 7, DocumentPath: "docs/a.md", ChunkContent: "hello"},
	}
	s := NewServer(store, nil, nil)
	body := `{"chunk_id":7}`
	req := httptest.NewRequest(http.MethodPost, "/proxy/chunk_by_id", strings.NewReader(body))
	rec := httptest.NewRecorder()
	s.handleChunkByID(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("expected status %d, got %d; body: %s", http.StatusOK, rec.Code, rec.Body.String())
	}
	var resp ChunkByIDResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("unexpected json error: %v", err)
	}
	if resp.Chunk.ChunkID != 7 {
		t.Fatalf("expected chunk_id 7, got %d", resp.Chunk.ChunkID)
	}
}

func TestHandleChunkByID_NotFound(t *testing.T) {
	t.Parallel()
	store := &chunkByIDErrorSearcher{err: errors.New("not found")}
	s := NewServer(store, nil, nil)
	body := `{"chunk_id":999}`
	req := httptest.NewRequest(http.MethodPost, "/proxy/chunk_by_id", strings.NewReader(body))
	rec := httptest.NewRecorder()
	s.handleChunkByID(rec, req)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("expected status %d, got %d", http.StatusNotFound, rec.Code)
	}
}

type chunkByIDErrorSearcher struct {
	fakeSearcher
	err error
}

func (s *chunkByIDErrorSearcher) GetChunkByID(context.Context, int64) (*index.SearchResult, error) {
	return nil, s.err
}

func TestHandleCollectionStats(t *testing.T) {
	t.Parallel()
	store := &fakeSearcher{statsDoc: 3, statsChunk: 7}
	s := NewServer(store, nil, nil)
	body := `{"collection":"alpha"}`
	req := httptest.NewRequest(http.MethodPost, "/proxy/collection_stats", strings.NewReader(body))
	rec := httptest.NewRecorder()
	s.handleCollectionStats(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("expected status %d, got %d; body: %s", http.StatusOK, rec.Code, rec.Body.String())
	}
	var resp CollectionStatsResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("unexpected json error: %v", err)
	}
	if resp.Documents != 3 || resp.Chunks != 7 {
		t.Fatalf("expected 3 docs/7 chunks, got docs=%d chunks=%d", resp.Documents, resp.Chunks)
	}
}

func TestHandleCollectionStats_WrongMethod(t *testing.T) {
	t.Parallel()
	store := &fakeSearcher{statsDoc: 3, statsChunk: 7}
	s := NewServer(store, nil, nil)
	req := httptest.NewRequest(http.MethodGet, "/proxy/collection_stats", nil)
	rec := httptest.NewRecorder()
	s.handleCollectionStats(rec, req)
	if rec.Code != http.StatusMethodNotAllowed {
		t.Fatalf("expected status %d, got %d", http.StatusMethodNotAllowed, rec.Code)
	}
}

func TestHandleCollectionStats_StoreError(t *testing.T) {
	t.Parallel()
	store := &collStatsErrorSearcher{err: errors.New("fail")}
	s := NewServer(store, nil, nil)
	body := `{"collection":"alpha"}`
	req := httptest.NewRequest(http.MethodPost, "/proxy/collection_stats", strings.NewReader(body))
	rec := httptest.NewRecorder()
	s.handleCollectionStats(rec, req)
	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("expected status %d, got %d", http.StatusInternalServerError, rec.Code)
	}
}

type collStatsErrorSearcher struct {
	fakeSearcher
	err error
}

func (s *collStatsErrorSearcher) CollectionStats(context.Context, string) (int, int, error) {
	return 0, 0, s.err
}

func TestHandleSearch_Success(t *testing.T) {
	t.Parallel()
	store := &fakeSearcher{
		searchResults: []index.SearchResult{
			{ChunkID: 1, DocumentPath: "a.md", ChunkContent: "hello"},
		},
	}
	s := NewServer(store, nil, nil)
	body := `{"query":"test","query_embedding":null,"limit":5}`
	req := httptest.NewRequest(http.MethodPost, "/proxy/search", strings.NewReader(body))
	rec := httptest.NewRecorder()
	s.handleSearch(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("expected status %d, got %d; body: %s", http.StatusOK, rec.Code, rec.Body.String())
	}
	var resp SearchResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("unexpected json error: %v", err)
	}
	if len(resp.Results) != 1 {
		t.Fatalf("expected 1 result, got %d", len(resp.Results))
	}
}

func TestHandleSearch_StoreError(t *testing.T) {
	t.Parallel()
	store := &searchErrorSearcher{err: errors.New("search fail")}
	s := NewServer(store, nil, nil)
	body := `{"query":"test","limit":5}`
	req := httptest.NewRequest(http.MethodPost, "/proxy/search", strings.NewReader(body))
	rec := httptest.NewRecorder()
	s.handleSearch(rec, req)
	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("expected status %d, got %d", http.StatusInternalServerError, rec.Code)
	}
}

type searchErrorSearcher struct {
	fakeSearcher
	err error
}

func (s *searchErrorSearcher) SearchFiltered(context.Context, string, []float32, int, string, index.SearchFilter) ([]index.SearchResult, error) {
	return nil, s.err
}

func TestHandleFindSimilar_Success(t *testing.T) {
	t.Parallel()
	store := &fakeSearcher{
		searchResults: []index.SearchResult{
			{ChunkID: 2, DocumentPath: "b.md", ChunkContent: "similar"},
		},
	}
	s := NewServer(store, nil, nil)
	body := `{"chunk_id":1,"limit":5}`
	req := httptest.NewRequest(http.MethodPost, "/proxy/find_similar", strings.NewReader(body))
	rec := httptest.NewRecorder()
	s.handleFindSimilar(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("expected status %d, got %d; body: %s", http.StatusOK, rec.Code, rec.Body.String())
	}
	var resp FindSimilarResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("unexpected json error: %v", err)
	}
	if len(resp.Results) != 1 {
		t.Fatalf("expected 1 result, got %d", len(resp.Results))
	}
}

func TestHandleFindSimilar_StoreError(t *testing.T) {
	t.Parallel()
	store := &findSimilarErrorSearcher{err: errors.New("fail")}
	s := NewServer(store, nil, nil)
	body := `{"chunk_id":1,"limit":5}`
	req := httptest.NewRequest(http.MethodPost, "/proxy/find_similar", strings.NewReader(body))
	rec := httptest.NewRecorder()
	s.handleFindSimilar(rec, req)
	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("expected status %d, got %d", http.StatusInternalServerError, rec.Code)
	}
}

type findSimilarErrorSearcher struct {
	fakeSearcher
	err error
}

func (s *findSimilarErrorSearcher) FindSimilar(context.Context, int64, int) ([]index.SearchResult, error) {
	return nil, s.err
}

func TestHandleEmbed_Available(t *testing.T) {
	t.Parallel()
	store := &fakeSearcher{}
	s := NewServer(store, nil, &embedStub{})
	body := `{"text":"hello world"}`
	req := httptest.NewRequest(http.MethodPost, "/proxy/embed", strings.NewReader(body))
	rec := httptest.NewRecorder()
	s.handleEmbed(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("expected status %d, got %d; body: %s", http.StatusOK, rec.Code, rec.Body.String())
	}
	var resp EmbedResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("unexpected json error: %v", err)
	}
	if len(resp.Embedding) != 1 {
		t.Fatalf("expected 1 embedding dimension, got %d", len(resp.Embedding))
	}
}

func TestHandleEmbed_Unavailable(t *testing.T) {
	t.Parallel()
	store := &fakeSearcher{}
	s := NewServer(store, nil, nil)
	body := `{"text":"hello world"}`
	req := httptest.NewRequest(http.MethodPost, "/proxy/embed", strings.NewReader(body))
	rec := httptest.NewRecorder()
	s.handleEmbed(rec, req)
	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("expected status %d, got %d", http.StatusServiceUnavailable, rec.Code)
	}
}

func TestHandleEmbed_WrongMethod(t *testing.T) {
	t.Parallel()
	store := &fakeSearcher{}
	s := NewServer(store, nil, &embedStub{})
	req := httptest.NewRequest(http.MethodGet, "/proxy/embed", nil)
	rec := httptest.NewRecorder()
	s.handleEmbed(rec, req)
	if rec.Code != http.StatusMethodNotAllowed {
		t.Fatalf("expected status %d, got %d", http.StatusMethodNotAllowed, rec.Code)
	}
}

func TestHandleEmbed_EmbedError(t *testing.T) {
	t.Parallel()
	store := &fakeSearcher{}
	s := NewServer(store, nil, &embedErrorStub{err: errors.New("embed fail")})
	body := `{"text":"hello"}`
	req := httptest.NewRequest(http.MethodPost, "/proxy/embed", strings.NewReader(body))
	rec := httptest.NewRecorder()
	s.handleEmbed(rec, req)
	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("expected status %d, got %d", http.StatusServiceUnavailable, rec.Code)
	}
}

type embedErrorStub struct {
	err error
}

func (e *embedErrorStub) Embed(context.Context, string) ([]float32, error) {
	return nil, e.err
}
func (e *embedErrorStub) EmbedBatch(_ context.Context, texts []string) ([][]float32, error) {
	return nil, e.err
}
func (e *embedErrorStub) Dimensions() int { return 1 }
func (e *embedErrorStub) Close() error    { return nil }

func TestHandleIndexStatus_WithState(t *testing.T) {
	t.Parallel()
	state := runtimestate.NewIndexStateTracker()
	state.Set(runtimestate.IndexStateReady, "ready")
	store := &fakeSearcher{statsDoc: 1, statsChunk: 2, fts: &index.FTSDiagnostics{LogicalRows: 2, Empty: false}}
	s := NewServer(store, state, &embedStub{})
	req := httptest.NewRequest(http.MethodGet, "/proxy/index_status", nil)
	rec := httptest.NewRecorder()
	s.handleIndexStatus(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("expected status %d, got %d; body: %s", http.StatusOK, rec.Code, rec.Body.String())
	}
	var resp IndexStatusResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("unexpected json error: %v", err)
	}
	if resp.Documents != 1 || resp.Chunks != 2 {
		t.Fatalf("expected 1 doc/2 chunks, got docs=%d chunks=%d", resp.Documents, resp.Chunks)
	}
	if resp.EmbeddingStatus != "available" {
		t.Fatalf("expected available, got %q", resp.EmbeddingStatus)
	}
	if resp.State != string(runtimestate.IndexStateReady) {
		t.Fatalf("expected ready state, got %q", resp.State)
	}
	if resp.FTS == nil {
		t.Fatal("expected FTS diagnostics")
	}
}

func TestHandleIndexStatus_NoEmbedder(t *testing.T) {
	t.Parallel()
	state := runtimestate.NewIndexStateTracker()
	state.Set(runtimestate.IndexStateReady, "ready")
	store := &fakeSearcher{statsDoc: 0, statsChunk: 0}
	s := NewServer(store, state, nil)
	req := httptest.NewRequest(http.MethodGet, "/proxy/index_status", nil)
	rec := httptest.NewRecorder()
	s.handleIndexStatus(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("expected status %d, got %d", http.StatusOK, rec.Code)
	}
	var resp IndexStatusResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("unexpected json error: %v", err)
	}
	if !strings.Contains(resp.EmbeddingStatus, "keyword-only") {
		t.Fatalf("expected keyword-only in status, got %q", resp.EmbeddingStatus)
	}
}

func TestHandleIndexStatus_NilState(t *testing.T) {
	t.Parallel()
	store := &fakeSearcher{statsDoc: 1, statsChunk: 1}
	s := NewServer(store, nil, &embedStub{})
	req := httptest.NewRequest(http.MethodGet, "/proxy/index_status", nil)
	rec := httptest.NewRecorder()
	s.handleIndexStatus(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("expected status %d, got %d", http.StatusOK, rec.Code)
	}
	var resp IndexStatusResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("unexpected json error: %v", err)
	}
	if resp.State != "" {
		t.Fatalf("expected empty state, got %q", resp.State)
	}
}

func TestHandleIndexStatus_StatsError(t *testing.T) {
	t.Parallel()
	store := &statsErrorSearcher{err: errors.New("db fail")}
	s := NewServer(store, nil, nil)
	req := httptest.NewRequest(http.MethodGet, "/proxy/index_status", nil)
	rec := httptest.NewRecorder()
	s.handleIndexStatus(rec, req)
	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("expected status %d, got %d", http.StatusInternalServerError, rec.Code)
	}
}

func TestHandleListSources_WithLimit(t *testing.T) {
	t.Parallel()
	store := &fakeSearcher{
		documents: []index.Document{
			{ID: 1, Path: "a.md"},
			{ID: 2, Path: "b.md"},
		},
		statsDoc: 2,
	}
	s := NewServer(store, nil, nil)
	body := `{"limit":1}`
	req := httptest.NewRequest(http.MethodPost, "/proxy/list_sources", strings.NewReader(body))
	rec := httptest.NewRecorder()
	s.handleListSources(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("expected status %d, got %d; body: %s", http.StatusOK, rec.Code, rec.Body.String())
	}
	var resp ListSourcesResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("unexpected json error: %v", err)
	}
	if resp.Total != 2 {
		t.Fatalf("expected total 2, got %d", resp.Total)
	}
	if len(resp.Documents) != 1 {
		t.Fatalf("expected 1 document shown, got %d", len(resp.Documents))
	}
}

func TestHandleListSources_InvalidJSON(t *testing.T) {
	t.Parallel()
	store := &fakeSearcher{}
	s := NewServer(store, nil, nil)
	req := httptest.NewRequest(http.MethodPost, "/proxy/list_sources", strings.NewReader("{invalid"))
	rec := httptest.NewRecorder()
	s.handleListSources(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("expected status %d, got %d", http.StatusBadRequest, rec.Code)
	}
}

func TestHandleListSources_StoreError(t *testing.T) {
	t.Parallel()
	store := &listDocsErrorSearcher{err: errors.New("fail")}
	s := NewServer(store, nil, nil)
	body := `{"limit":10}`
	req := httptest.NewRequest(http.MethodPost, "/proxy/list_sources", strings.NewReader(body))
	rec := httptest.NewRecorder()
	s.handleListSources(rec, req)
	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("expected status %d, got %d", http.StatusInternalServerError, rec.Code)
	}
}

type listDocsErrorSearcher struct {
	fakeSearcher
	err error
}

func (s *listDocsErrorSearcher) ListDocumentsLimit(context.Context, int) ([]index.Document, error) {
	return nil, s.err
}

func TestWriteJSON_Error(t *testing.T) {
	t.Parallel()
	s := NewServer(&fakeSearcher{}, nil, nil)
	rec := httptest.NewRecorder()
	s.writeJSON(rec, http.StatusOK, func() {})
	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("expected status %d for unmarshallable value, got %d", http.StatusInternalServerError, rec.Code)
	}
}

var _ index.Searcher = (*pingErrorSearcher)(nil)
var _ index.Searcher = (*statsErrorSearcher)(nil)
var _ index.Searcher = (*searchErrorSearcher)(nil)
var _ index.Searcher = (*findSimilarErrorSearcher)(nil)
var _ index.Searcher = (*chunkByIDErrorSearcher)(nil)
var _ index.Searcher = (*deleteCollErrorSearcher)(nil)
var _ index.Searcher = (*collectionsErrorSearcher)(nil)
var _ index.Searcher = (*collStatsErrorSearcher)(nil)
var _ index.Searcher = (*listDocsErrorSearcher)(nil)
