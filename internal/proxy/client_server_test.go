package proxy

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/koltyakov/quant/internal/index"
	runtimestate "github.com/koltyakov/quant/internal/runtime"
)

func TestClientAdditionalMethodsProxyToMain(t *testing.T) {
	ctx := context.Background()
	store, client, _ := newProxyTestHarness(t)

	for _, doc := range []index.Document{
		{
			Path:       "alpha/one.md",
			Hash:       "alpha-one",
			ModifiedAt: testTime(),
			Collection: "alpha",
		},
		{
			Path:       "beta/two.md",
			Hash:       "beta-two",
			ModifiedAt: testTime().Add(time.Second),
			Collection: "beta",
		},
	} {
		if err := store.ReindexDocument(ctx, &doc, []index.ChunkRecord{{
			Content:    "shared similarity token",
			ChunkIndex: 0,
			Embedding:  index.EncodeFloat32(index.NormalizeFloat32([]float32{1})),
		}}); err != nil {
			t.Fatalf("seed error for %s: %v", doc.Path, err)
		}
	}

	if !strings.HasPrefix(client.Addr(), "http://") {
		t.Fatalf("client address should include scheme, got %q", client.Addr())
	}
	if !client.Alive(ctx) {
		t.Fatal("expected proxy client to report alive")
	}
	if err := client.PingContext(ctx); err != nil {
		t.Fatalf("PingContext returned error: %v", err)
	}

	results, err := client.Search(ctx, "shared similarity token", nil, 10, "")
	if err != nil {
		t.Fatalf("Search returned error: %v", err)
	}
	if len(results) < 2 {
		t.Fatalf("expected search results, got %+v", results)
	}

	chunk, err := client.GetChunkByID(ctx, results[0].ChunkID)
	if err != nil {
		t.Fatalf("GetChunkByID returned error: %v", err)
	}
	if chunk.ChunkID != results[0].ChunkID {
		t.Fatalf("unexpected chunk by id result: %+v", chunk)
	}

	similar, err := client.FindSimilar(ctx, results[0].ChunkID, 5)
	if err != nil {
		t.Fatalf("FindSimilar returned error: %v", err)
	}
	if len(similar) > 5 {
		t.Fatalf("unexpected similar result count: %d", len(similar))
	}

	docs, err := client.ListDocuments(ctx)
	if err != nil {
		t.Fatalf("ListDocuments returned error: %v", err)
	}
	if len(docs) != 2 {
		t.Fatalf("unexpected document count: %d", len(docs))
	}

	limited, err := client.ListDocumentsLimit(ctx, 1)
	if err != nil {
		t.Fatalf("ListDocumentsLimit returned error: %v", err)
	}
	if len(limited) != 1 {
		t.Fatalf("expected limited document list, got %+v", limited)
	}

	docCount, chunkCount, err := client.Stats(ctx)
	if err != nil {
		t.Fatalf("Stats returned error: %v", err)
	}
	if docCount != 2 || chunkCount != 2 {
		t.Fatalf("unexpected stats: docs=%d chunks=%d", docCount, chunkCount)
	}

	batch, err := client.EmbedBatch(ctx, []string{"one", "two"})
	if err != nil {
		t.Fatalf("EmbedBatch returned error: %v", err)
	}
	if len(batch) != 2 || len(batch[0]) != 1 {
		t.Fatalf("unexpected embedding batch: %+v", batch)
	}

	if client.Dimensions() != 0 {
		t.Fatalf("expected proxy client dimensions to be zero, got %d", client.Dimensions())
	}
	if err := client.Close(); err != nil {
		t.Fatalf("expected no-op close, got %v", err)
	}
	if _, err := client.GetDocumentByPath(ctx, "alpha/one.md"); err == nil {
		t.Fatal("expected GetDocumentByPath proxy error")
	}
	if _, err := client.GetDocumentChunksByPath(ctx, "alpha/one.md"); err == nil {
		t.Fatal("expected GetDocumentChunksByPath proxy error")
	}
}

func TestClientDoRequestAndServerHandlers(t *testing.T) {
	t.Parallel()

	client := &Client{addr: "http://127.0.0.1:1", httpClient: &http.Client{Timeout: 20 * time.Millisecond}}
	if client.Alive(context.Background()) {
		t.Fatal("expected Alive to fail for unreachable server")
	}
	if err := client.PingContext(context.Background()); err == nil {
		t.Fatal("expected PingContext error for unreachable server")
	}

	httpServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/bad-status":
			http.Error(w, "boom", http.StatusBadGateway)
		case "/bad-json":
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte("{"))
		default:
			_ = json.NewEncoder(w).Encode(map[string]string{"status": "ok"})
		}
	}))
	defer httpServer.Close()

	client = &Client{addr: httpServer.URL, httpClient: httpServer.Client()}
	var okResp map[string]string
	if err := client.doGet(context.Background(), "/ok", &okResp); err != nil || okResp["status"] != "ok" {
		t.Fatalf("unexpected doGet result: resp=%v err=%v", okResp, err)
	}
	if err := client.doGet(context.Background(), "/bad-status", &okResp); err == nil {
		t.Fatal("expected non-200 proxy error")
	}
	if err := client.doGet(context.Background(), "/bad-json", &okResp); err == nil {
		t.Fatal("expected decode error for malformed json")
	}

	state := runtimestate.NewIndexStateTracker()
	state.Set(runtimestate.IndexStateReady, "ready")
	store := &fakeSearcher{
		searchResults: []index.SearchResult{{ChunkID: 7, DocumentPath: "docs/a.md"}},
		documents:     []index.Document{{ID: 1, Path: "docs/a.md"}},
		statsDoc:      1,
		statsChunk:    2,
		chunkByID:     &index.SearchResult{ChunkID: 7, DocumentPath: "docs/a.md"},
		collections:   []string{"alpha"},
		fts:           &index.FTSDiagnostics{LogicalRows: 1, Empty: false},
	}
	server := NewServer(store, state, &embedStub{})

	req := httptest.NewRequest(http.MethodGet, "/proxy/search", nil)
	rr := httptest.NewRecorder()
	var reqBody SearchRequest
	if server.readBody(rr, req, &reqBody) {
		t.Fatal("GET should not be accepted by readBody")
	}
	if rr.Code != http.StatusMethodNotAllowed {
		t.Fatalf("unexpected readBody status for GET: %d", rr.Code)
	}

	req = httptest.NewRequest(http.MethodPost, "/proxy/search", strings.NewReader("{"))
	rr = httptest.NewRecorder()
	if server.readBody(rr, req, &reqBody) {
		t.Fatal("invalid JSON should not be accepted")
	}
	if rr.Code != http.StatusBadRequest {
		t.Fatalf("unexpected readBody status for invalid JSON: %d", rr.Code)
	}

	rr = httptest.NewRecorder()
	server.writeJSON(rr, http.StatusOK, func() {})
	if rr.Code != http.StatusInternalServerError {
		t.Fatalf("unexpected writeJSON status: %d", rr.Code)
	}

	postJSON := func(target string, body string, handler func(http.ResponseWriter, *http.Request)) *httptest.ResponseRecorder {
		t.Helper()
		req := httptest.NewRequest(http.MethodPost, target, strings.NewReader(body))
		rr := httptest.NewRecorder()
		handler(rr, req)
		return rr
	}

	rr = postJSON("/proxy/find_similar", `{"chunk_id":7,"limit":3}`, server.handleFindSimilar)
	if rr.Code != http.StatusOK || !strings.Contains(rr.Body.String(), `docs/a.md`) {
		t.Fatalf("unexpected find similar response: code=%d body=%s", rr.Code, rr.Body.String())
	}

	rr = postJSON("/proxy/list_sources", `{"limit":1}`, server.handleListSources)
	if rr.Code != http.StatusOK || !strings.Contains(rr.Body.String(), `"total":1`) {
		t.Fatalf("unexpected list sources response: code=%d body=%s", rr.Code, rr.Body.String())
	}

	req = httptest.NewRequest(http.MethodGet, "/proxy/index_status", nil)
	rr = httptest.NewRecorder()
	server.handleIndexStatus(rr, req)
	if rr.Code != http.StatusOK || !strings.Contains(rr.Body.String(), `"embedding_status":"available"`) {
		t.Fatalf("unexpected index status response: code=%d body=%s", rr.Code, rr.Body.String())
	}

	req = httptest.NewRequest(http.MethodGet, "/proxy/ping", nil)
	rr = httptest.NewRecorder()
	server.handlePing(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("unexpected ping response: %d", rr.Code)
	}

	rr = postJSON("/proxy/chunk_by_id", `{"chunk_id":7}`, server.handleChunkByID)
	if rr.Code != http.StatusOK || !strings.Contains(rr.Body.String(), `docs/a.md`) {
		t.Fatalf("unexpected chunk by id response: code=%d body=%s", rr.Code, rr.Body.String())
	}

	req = httptest.NewRequest(http.MethodGet, "/proxy/stats", nil)
	rr = httptest.NewRecorder()
	server.handleStats(rr, req)
	if rr.Code != http.StatusOK || !strings.Contains(rr.Body.String(), `"doc_count":1`) {
		t.Fatalf("unexpected stats response: code=%d body=%s", rr.Code, rr.Body.String())
	}

	req = httptest.NewRequest(http.MethodGet, "/proxy/list_collections", nil)
	rr = httptest.NewRecorder()
	server.handleListCollections(rr, req)
	if rr.Code != http.StatusOK || !strings.Contains(rr.Body.String(), `"alpha"`) {
		t.Fatalf("unexpected list collections response: code=%d body=%s", rr.Code, rr.Body.String())
	}

	rr = postJSON("/proxy/collection_stats", `{"collection":"alpha"}`, server.handleCollectionStats)
	if rr.Code != http.StatusOK || !strings.Contains(rr.Body.String(), `"documents":1`) {
		t.Fatalf("unexpected collection stats response: code=%d body=%s", rr.Code, rr.Body.String())
	}

	rr = postJSON("/proxy/delete_collection", `{"collection":"alpha"}`, server.handleDeleteCollection)
	if rr.Code != http.StatusOK || !strings.Contains(rr.Body.String(), `"deleted":true`) {
		t.Fatalf("unexpected delete collection response: code=%d body=%s", rr.Code, rr.Body.String())
	}

	if server.Addr() != "" {
		t.Fatalf("expected direct test server Addr to be empty before Start, got %q", server.Addr())
	}
}

type fakeSearcher struct {
	searchResults []index.SearchResult
	documents     []index.Document
	statsDoc      int
	statsChunk    int
	chunkByID     *index.SearchResult
	collections   []string
	fts           *index.FTSDiagnostics
}

func (f *fakeSearcher) Search(context.Context, string, []float32, int, string) ([]index.SearchResult, error) {
	return f.searchResults, nil
}

func (f *fakeSearcher) SearchFiltered(context.Context, string, []float32, int, string, index.SearchFilter) ([]index.SearchResult, error) {
	return f.searchResults, nil
}

func (f *fakeSearcher) FindSimilar(context.Context, int64, int) ([]index.SearchResult, error) {
	return f.searchResults, nil
}

func (f *fakeSearcher) GetChunkByID(context.Context, int64) (*index.SearchResult, error) {
	if f.chunkByID == nil {
		return nil, errors.New("missing chunk")
	}
	return f.chunkByID, nil
}

func (f *fakeSearcher) GetDocumentChunksByPath(context.Context, string) (map[string]index.ChunkRecord, error) {
	return map[string]index.ChunkRecord{}, nil
}

func (f *fakeSearcher) GetDocumentByPath(context.Context, string) (*index.Document, error) {
	if len(f.documents) == 0 {
		return nil, nil
	}
	return &f.documents[0], nil
}

func (f *fakeSearcher) ListDocuments(context.Context) ([]index.Document, error) {
	return f.documents, nil
}

func (f *fakeSearcher) ListDocumentsLimit(_ context.Context, limit int) ([]index.Document, error) {
	if limit > 0 && limit < len(f.documents) {
		return f.documents[:limit], nil
	}
	return f.documents, nil
}

func (f *fakeSearcher) Stats(context.Context) (int, int, error) {
	return f.statsDoc, f.statsChunk, nil
}

func (f *fakeSearcher) PingContext(context.Context) error {
	return nil
}

func (f *fakeSearcher) ListCollections(context.Context) ([]string, error) {
	return f.collections, nil
}

func (f *fakeSearcher) CollectionStats(context.Context, string) (int, int, error) {
	return f.statsDoc, f.statsChunk, nil
}

func (f *fakeSearcher) DeleteCollection(context.Context, string) error {
	return nil
}

func (f *fakeSearcher) FTSDiagnostics(context.Context) (index.FTSDiagnostics, error) {
	if f.fts == nil {
		return index.FTSDiagnostics{}, errors.New("missing fts")
	}
	return *f.fts, nil
}

type embedStub struct{}

func (e *embedStub) Embed(context.Context, string) ([]float32, error) { return []float32{1}, nil }
func (e *embedStub) EmbedBatch(context.Context, []string) ([][]float32, error) {
	return [][]float32{{1}}, nil
}
func (e *embedStub) Dimensions() int { return 1 }
func (e *embedStub) Close() error    { return nil }

var _ index.Searcher = (*fakeSearcher)(nil)
var _ index.FTSDiagnosticsProvider = (*fakeSearcher)(nil)
