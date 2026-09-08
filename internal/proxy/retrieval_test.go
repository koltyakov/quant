package proxy

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"

	"github.com/koltyakov/quant/internal/index"
)

type retrievalSearcher struct {
	index.Searcher
	diagnostics index.RetrievalDiagnostics
	err         error
}

func (s *retrievalSearcher) SearchFiltered(ctx context.Context, _ string, _ []float32, _ int, _ string, _ index.SearchFilter) ([]index.SearchResult, error) {
	index.RecordRetrievalDiagnostics(ctx, &s.diagnostics)
	return nil, s.err
}

func (s *retrievalSearcher) FindSimilar(ctx context.Context, _ int64, _ int) ([]index.SearchResult, error) {
	index.RecordRetrievalDiagnostics(ctx, &s.diagnostics)
	return nil, s.err
}

func retrievalCalls(client *Client) map[string]func(context.Context) error {
	return map[string]func(context.Context) error{
		"search": func(ctx context.Context) error {
			_, err := client.Search(ctx, "query", []float32{1}, 5, "")
			return err
		},
		"search_filtered": func(ctx context.Context) error {
			_, err := client.SearchFiltered(ctx, "query", []float32{1}, 5, "", index.SearchFilter{})
			return err
		},
		"find_similar": func(ctx context.Context) error { _, err := client.FindSimilar(ctx, 1, 5); return err },
	}
}

func TestRetrievalDiagnosticsRoundTrip(t *testing.T) {
	for _, tc := range []struct {
		name        string
		diagnostics index.RetrievalDiagnostics
		err         error
	}{
		{name: "complete", diagnostics: index.RetrievalDiagnostics{Semantic: "complete"}},
		{name: "budget_skipped", diagnostics: index.RetrievalDiagnostics{Semantic: "budget_skipped", BudgetSkipped: true}},
		{name: "incomplete", diagnostics: index.RetrievalDiagnostics{Semantic: "incomplete", BudgetSkipped: true}},
		{name: "degraded", diagnostics: index.RetrievalDiagnostics{Semantic: "failed", Errors: []string{"vector loading failed"}, RerankError: "invalid scores"}},
		{name: "fatal", diagnostics: index.RetrievalDiagnostics{Semantic: "failed", Errors: []string{"vector scan failed"}}, err: errors.New("vector scan failed")},
	} {
		t.Run(tc.name, func(t *testing.T) {
			server := NewServer(&retrievalSearcher{diagnostics: tc.diagnostics, err: tc.err}, nil, nil)
			mux := http.NewServeMux()
			mux.HandleFunc("/proxy/search", server.handleSearch)
			mux.HandleFunc("/proxy/find_similar", server.handleFindSimilar)
			httpServer := httptest.NewServer(server.withAuth(mux))
			defer httpServer.Close()
			client := NewClient(strings.TrimPrefix(httpServer.URL, "http://"), server.Token())
			for name, call := range retrievalCalls(client) {
				t.Run(name, func(t *testing.T) {
					ctx, got := index.WithRetrievalDiagnostics(context.Background())
					err := call(ctx)
					if (err != nil) != (tc.err != nil) {
						t.Fatalf("error=%v want=%v", err, tc.err)
					}
					if tc.err != nil && !strings.Contains(err.Error(), "proxy error (500): "+tc.err.Error()) {
						t.Fatalf("error contract changed: %v", err)
					}
					if !reflect.DeepEqual(*got, tc.diagnostics) {
						t.Fatalf("diagnostics=%+v want=%+v", got, tc.diagnostics)
					}
					_, separate := index.WithRetrievalDiagnostics(context.Background())
					if separate.Semantic != "unknown" {
						t.Fatal("diagnostics leaked between requests")
					}
				})
			}
		})
	}
}

func TestRetrievalDiagnosticsOlderServer(t *testing.T) {
	for _, status := range []int{http.StatusOK, http.StatusInternalServerError} {
		httpServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			w.WriteHeader(status)
			if status == http.StatusOK {
				_, _ = w.Write([]byte(`{"results":[]}`))
			} else {
				_, _ = w.Write([]byte(`{"error":"old server error"}`))
			}
		}))
		client := NewClient(strings.TrimPrefix(httpServer.URL, "http://"), "")
		for name, call := range retrievalCalls(client) {
			ctx, diagnostics := index.WithRetrievalDiagnostics(context.Background())
			err := call(ctx)
			if (err != nil) != (status != http.StatusOK) {
				t.Fatalf("%s status=%d err=%v", name, status, err)
			}
			if diagnostics.Semantic != "unknown" {
				t.Fatalf("%s missing diagnostics reported as %+v", name, diagnostics)
			}
		}
		httpServer.Close()
	}
}

func TestRealStoreBudgetDiagnosticsAcrossProxy(t *testing.T) {
	store, client, _ := newProxyTestHarness(t)
	ctx := context.Background()
	if _, err := store.EnsureEmbeddingMetadata(ctx, index.EmbeddingMetadata{Model: "test", Dimensions: 1, Normalized: true}); err != nil {
		t.Fatal(err)
	}
	if err := store.ReindexDocument(ctx, &index.Document{Path: "seed.txt", Hash: "seed", ModifiedAt: testTime()}, []index.ChunkRecord{{Content: "query", Embedding: index.EncodeFloat32([]float32{1})}}); err != nil {
		t.Fatal(err)
	}
	chunks, err := store.GetDocumentChunksByPath(ctx, "seed.txt")
	if err != nil {
		t.Fatal(err)
	}
	var id int64
	for _, chunk := range chunks {
		id = chunk.ID
	}
	store.SetMaxVectorSearchCandidates(0)
	embedding, err := client.Embed(ctx, "query")
	if err != nil || len(embedding) == 0 {
		t.Fatalf("embedding=%v err=%v", embedding, err)
	}
	searchCtx, searchDiag := index.WithRetrievalDiagnostics(ctx)
	if _, err := client.Search(searchCtx, "query", embedding, 5, ""); err != nil {
		t.Fatal(err)
	}
	similarCtx, similarDiag := index.WithRetrievalDiagnostics(ctx)
	if _, err := client.FindSimilar(similarCtx, id, 5); err != nil {
		t.Fatal(err)
	}
	for _, diag := range []*index.RetrievalDiagnostics{searchDiag, similarDiag} {
		if diag.Semantic != "budget_skipped" || !diag.BudgetSkipped {
			t.Fatalf("diagnostics=%+v", diag)
		}
	}
}
