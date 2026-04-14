package proxy

import (
	"context"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/koltyakov/quant/internal/embed"
	"github.com/koltyakov/quant/internal/index"
	"github.com/koltyakov/quant/internal/testutil"
)

func TestClientSearchFilteredAndEmbedProxyToMain(t *testing.T) {
	ctx := context.Background()
	store, client, embedder := newProxyTestHarness(t)

	for _, doc := range []index.Document{
		{
			Path:       "code/main.go",
			Hash:       "go-hash",
			ModifiedAt: testTime(),
			FileType:   "go",
		},
		{
			Path:       "docs/guide.md",
			Hash:       "md-hash",
			ModifiedAt: testTime(),
			FileType:   "markdown",
		},
	} {
		if err := store.ReindexDocument(ctx, &doc, []index.ChunkRecord{{
			Content:    "shared proxy token",
			ChunkIndex: 0,
			Embedding:  index.EncodeFloat32(index.NormalizeFloat32([]float32{1})),
		}}); err != nil {
			t.Fatalf("unexpected seed error for %s: %v", doc.Path, err)
		}
	}

	results, err := client.SearchFiltered(ctx, "shared proxy token", nil, 10, "", index.SearchFilter{
		FileTypes: []string{"go"},
	})
	if err != nil {
		t.Fatalf("unexpected proxied search error: %v", err)
	}
	if len(results) != 1 || results[0].DocumentPath != "code/main.go" {
		t.Fatalf("expected only Go document, got %+v", results)
	}

	embedding, err := client.Embed(ctx, "proxied query")
	if err != nil {
		t.Fatalf("unexpected proxied embed error: %v", err)
	}
	if !reflect.DeepEqual(embedding, []float32{1}) {
		t.Fatalf("unexpected embedding: %+v", embedding)
	}
	embedder.Mu.Lock()
	calls := embedder.Calls["proxied query"]
	embedder.Mu.Unlock()
	if calls != 1 {
		t.Fatalf("expected main embedder to handle query once, got %d", calls)
	}

	status, err := client.EmbeddingStatus(ctx)
	if err != nil {
		t.Fatalf("unexpected proxied embedding status error: %v", err)
	}
	if status != "available" {
		t.Fatalf("expected available embedding status, got %q", status)
	}
}

func TestClientCollectionOperationsProxyToMain(t *testing.T) {
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
			Path:       "alpha/two.md",
			Hash:       "alpha-two",
			ModifiedAt: testTime(),
			Collection: "alpha",
		},
		{
			Path:       "beta/one.md",
			Hash:       "beta-one",
			ModifiedAt: testTime(),
			Collection: "beta",
		},
	} {
		if err := store.ReindexDocument(ctx, &doc, []index.ChunkRecord{{
			Content:    doc.Path,
			ChunkIndex: 0,
			Embedding:  index.EncodeFloat32(index.NormalizeFloat32([]float32{1})),
		}}); err != nil {
			t.Fatalf("unexpected seed error for %s: %v", doc.Path, err)
		}
	}

	collections, err := client.ListCollections(ctx)
	if err != nil {
		t.Fatalf("unexpected proxied collections error: %v", err)
	}
	if !reflect.DeepEqual(collections, []string{"alpha", "beta"}) {
		t.Fatalf("unexpected collections: %+v", collections)
	}

	docs, chunks, err := client.CollectionStats(ctx, "alpha")
	if err != nil {
		t.Fatalf("unexpected proxied collection stats error: %v", err)
	}
	if docs != 2 || chunks != 2 {
		t.Fatalf("unexpected alpha stats: docs=%d chunks=%d", docs, chunks)
	}

	if err := client.DeleteCollection(ctx, "alpha"); err != nil {
		t.Fatalf("unexpected proxied delete collection error: %v", err)
	}

	collections, err = client.ListCollections(ctx)
	if err != nil {
		t.Fatalf("unexpected proxied collections error after delete: %v", err)
	}
	if !reflect.DeepEqual(collections, []string{"beta"}) {
		t.Fatalf("unexpected collections after delete: %+v", collections)
	}
}

func TestClientEmbeddingStatusReflectsKeywordOnlyMain(t *testing.T) {
	ctx := context.Background()
	_, client, _ := newProxyTestHarness(t, true)

	status, err := client.EmbeddingStatus(ctx)
	if err != nil {
		t.Fatalf("unexpected proxied embedding status error: %v", err)
	}
	if status != "unavailable (keyword-only mode) — start Ollama with: ollama serve" {
		t.Fatalf("unexpected keyword-only status: %q", status)
	}

	if _, err := client.Embed(ctx, "needs embedding"); err == nil {
		t.Fatal("expected proxied embed failure when main is keyword-only")
	}
}

func newProxyTestHarness(t *testing.T, keywordOnly ...bool) (*index.Store, *Client, *testutil.QueryCountingEmbedder) {
	t.Helper()

	dir := t.TempDir()
	dbPath := filepath.Join(dir, "quant.db")

	store, err := index.NewStore(dbPath)
	if err != nil {
		t.Fatalf("unexpected store open error: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })

	var embedder *testutil.QueryCountingEmbedder
	var proxyEmbedder embed.Embedder
	if len(keywordOnly) > 0 && keywordOnly[0] {
		embedder = nil
		proxyEmbedder = nil
	} else {
		embedder = &testutil.QueryCountingEmbedder{}
		proxyEmbedder = embedder
	}

	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)

	server := NewServer(store, nil, proxyEmbedder)
	addr, err := server.Start(ctx)
	if err != nil {
		if strings.Contains(err.Error(), "bind: operation not permitted") || strings.Contains(err.Error(), "bind: permission denied") {
			t.Skipf("proxy listener unavailable in this environment: %v", err)
		}
		t.Fatalf("unexpected proxy start error: %v", err)
	}

	return store, NewClient(addr), embedder
}

func testTime() time.Time {
	return time.Unix(1_700_000_000, 0).UTC()
}
