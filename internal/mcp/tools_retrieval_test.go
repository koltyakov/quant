package mcp

import (
	"context"
	"database/sql"
	"path/filepath"
	"strings"
	"testing"

	"github.com/koltyakov/quant/internal/index"
	mcplib "github.com/mark3labs/mcp-go/mcp"
)

type thresholdReranker struct{ seen int }

func (r *thresholdReranker) Name() string { return "test" }
func (r *thresholdReranker) Rerank(_ context.Context, _ string, _ []float32, results []index.SearchResult) ([]index.SearchResult, error) {
	r.seen = len(results)
	for i := range results {
		results[i].Score = .2
		if results[i].DocumentPath == "A" {
			results[i].Score = .9 - float32(results[i].ChunkIndex)*.1
		}
	}
	return results, nil
}

func TestSearchThresholdPrecedesDiversityAtAPI(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "quant.db")
	store, err := index.NewStore(path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	ctx := context.Background()
	for _, doc := range []string{"A", "B"} {
		chunks := []index.ChunkRecord{{Content: "alpha first", Embedding: []byte{}}}
		if doc == "A" {
			chunks = append(chunks, index.ChunkRecord{Content: "alpha second", ChunkIndex: 1, Embedding: []byte{}})
		}
		if err := store.ReindexDocument(ctx, &index.Document{Path: doc, Hash: doc, ModifiedAt: testTime()}, chunks); err != nil {
			t.Fatal(err)
		}
	}
	r := &thresholdReranker{}
	store.SetReranker(r)
	s := newTestServer(dir, path, store)
	result, err := s.handleSearch(ctx, mcplib.CallToolRequest{Params: mcplib.CallToolParams{Arguments: map[string]any{"query": "alpha", "limit": float64(2), "threshold": float64(.5)}}})
	if err != nil {
		t.Fatal(err)
	}
	out := extractSearchStructured(t, result)
	if r.seen != 3 || len(out.Results) != 2 || out.Results[0].Path != "A" || out.Results[1].Path != "A" {
		t.Fatalf("rerank candidates=%d response=%+v", r.seen, out)
	}
}

func TestSearchRetrievalDiagnosticsWithHealthyEmbedding(t *testing.T) {
	for _, failed := range []bool{false, true} {
		t.Run(map[bool]string{false: "budget", true: "failed"}[failed], func(t *testing.T) {
			dir := t.TempDir()
			path := filepath.Join(dir, "quant.db")
			store, err := index.NewStore(path)
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = store.Close() })
			ctx := context.Background()
			if _, err := store.EnsureEmbeddingMetadata(ctx, index.EmbeddingMetadata{Model: "test", Dimensions: 1, Normalized: true}); err != nil {
				t.Fatal(err)
			}
			if err := store.ReindexDocument(ctx, &index.Document{Path: "a.txt", Hash: "a", ModifiedAt: testTime()}, []index.ChunkRecord{{Content: "alpha", Embedding: index.EncodeFloat32([]float32{1})}}); err != nil {
				t.Fatal(err)
			}
			store.SetMaxVectorSearchCandidates(0)
			if failed {
				if err := store.BuildHNSW(ctx); err != nil {
					t.Fatal(err)
				}
				db, err := sql.Open("sqlite", path)
				if err != nil {
					t.Fatal(err)
				}
				t.Cleanup(func() {
					if err := db.Close(); err != nil {
						t.Errorf("closing diagnostic database: %v", err)
					}
				})
				if _, err := db.Exec("ALTER TABLE documents RENAME COLUMN path TO broken_path"); err != nil {
					t.Fatal(err)
				}
			}
			s := newTestServer(dir, path, store)
			result, err := s.handleSearch(ctx, mcplib.CallToolRequest{Params: mcplib.CallToolParams{Arguments: map[string]any{"query": "---", "path": "a"}}})
			if err != nil {
				t.Fatal(err)
			}
			out := extractSearchStructured(t, result)
			want := "budget_skipped"
			if failed {
				want = "failed"
			}
			if out.EmbeddingStatus != "hybrid" || out.Retrieval == nil || out.Retrieval.Semantic != want {
				t.Fatalf("response=%+v retrieval=%+v", out, out.Retrieval)
			}
			if failed && len(out.Retrieval.Errors) == 0 {
				t.Fatal("missing retrieval error")
			}
			if !failed && !out.Retrieval.BudgetSkipped {
				t.Fatal("missing skipped budget")
			}
		})
	}
}

func TestFindSimilarUnavailableVersusNoMatches(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "quant.db")
	store, err := index.NewStore(path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	ctx := context.Background()
	if _, err := store.EnsureEmbeddingMetadata(ctx, index.EmbeddingMetadata{Model: "test", Dimensions: 1, Normalized: true}); err != nil {
		t.Fatal(err)
	}
	if err := store.ReindexDocument(ctx, &index.Document{Path: "a.txt", Hash: "a", ModifiedAt: testTime()}, []index.ChunkRecord{{Content: "alpha", Embedding: index.EncodeFloat32([]float32{1})}}); err != nil {
		t.Fatal(err)
	}
	chunks, err := store.GetDocumentChunksByPath(ctx, "a.txt")
	if err != nil {
		t.Fatal(err)
	}
	var id int64
	for _, c := range chunks {
		id = c.ID
	}
	s := newTestServer(dir, path, store)
	for _, budget := range []int{0, -1} {
		store.SetMaxVectorSearchCandidates(budget)
		result, err := s.handleFindSimilar(ctx, mcplib.CallToolRequest{Params: mcplib.CallToolParams{Arguments: map[string]any{"chunk_id": float64(id)}}})
		if err != nil {
			t.Fatal(err)
		}
		out := result.StructuredContent.(findSimilarToolResponse)
		text := extractToolText(t, result)
		if budget == 0 && (out.Retrieval.Semantic != "budget_skipped" || !strings.Contains(text, "unavailable")) {
			t.Fatalf("%+v %s", out, text)
		}
		if budget == -1 && (out.Retrieval.Semantic != "complete" || text != "No similar chunks found.") {
			t.Fatalf("%+v %s", out, text)
		}
	}
}
