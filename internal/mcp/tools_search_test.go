package mcp

import (
	"context"
	"fmt"
	"math"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/koltyakov/quant/internal/index"
	"github.com/koltyakov/quant/internal/testutil"
	mcplib "github.com/mark3labs/mcp-go/mcp"
)

func TestHandleFindSimilar(t *testing.T) {
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "quant.db")
	store, err := index.NewStore(dbPath)
	if err != nil {
		t.Fatalf("unexpected store open error: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })

	if err := store.ReindexDocument(context.Background(), &index.Document{
		Path:       "notes/alpha.txt",
		Hash:       "alpha-hash",
		ModifiedAt: testTime(),
	}, []index.ChunkRecord{{
		Content:    "alpha search phrase",
		ChunkIndex: 0,
		Embedding:  index.EncodeFloat32(index.NormalizeFloat32([]float32{1})),
	}}); err != nil {
		t.Fatalf("unexpected seed error: %v", err)
	}

	chunkMap, err := store.GetDocumentChunksByPath(context.Background(), "notes/alpha.txt")
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

	result, err := s.handleFindSimilar(context.Background(), mcplib.CallToolRequest{
		Params: mcplib.CallToolParams{
			Name: "find_similar",
			Arguments: map[string]any{
				"chunk_id": float64(chunkID),
			},
		},
	})
	if err != nil {
		t.Fatalf("unexpected handleFindSimilar error: %v", err)
	}
	text := extractToolText(t, result)
	if !strings.Contains(text, "alpha.txt") && !strings.Contains(text, "No similar chunks found") {
		t.Fatalf("expected source document or no-results message, got %q", text)
	}
}

func TestHandleFindSimilar_MissingChunkID(t *testing.T) {
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "quant.db")
	store, err := index.NewStore(dbPath)
	if err != nil {
		t.Fatalf("unexpected store open error: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })

	s := newTestServer(dir, dbPath, store)
	suppressLogs(t)

	_, err = s.handleFindSimilar(context.Background(), mcplib.CallToolRequest{
		Params: mcplib.CallToolParams{
			Name:      "find_similar",
			Arguments: map[string]any{},
		},
	})
	if err == nil {
		t.Fatal("expected error for missing chunk_id")
	}
}

func TestHandleFindSimilar_InvalidChunkID(t *testing.T) {
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "quant.db")
	store, err := index.NewStore(dbPath)
	if err != nil {
		t.Fatalf("unexpected store open error: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })

	s := newTestServer(dir, dbPath, store)

	for _, chunkID := range []float64{-1, 0, 1.5, math.NaN(), math.Inf(1)} {
		_, err := s.handleFindSimilar(context.Background(), mcplib.CallToolRequest{
			Params: mcplib.CallToolParams{
				Name: "find_similar",
				Arguments: map[string]any{
					"chunk_id": chunkID,
				},
			},
		})
		if err == nil {
			t.Fatalf("expected error for chunk_id %v", chunkID)
		}
	}
}

func TestHandleListCollections(t *testing.T) {
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "quant.db")
	store, err := index.NewStore(dbPath)
	if err != nil {
		t.Fatalf("unexpected store open error: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })

	if err := store.ReindexDocument(context.Background(), &index.Document{
		Path:       "col/a.txt",
		Hash:       "col-a-hash",
		ModifiedAt: testTime(),
		Collection: "mycol",
	}, []index.ChunkRecord{{
		Content:    "collection content",
		ChunkIndex: 0,
		Embedding:  index.EncodeFloat32(index.NormalizeFloat32([]float32{1})),
	}}); err != nil {
		t.Fatalf("unexpected seed error: %v", err)
	}

	s := newTestServer(dir, dbPath, store)
	suppressLogs(t)

	result, err := s.handleListCollections(context.Background(), mcplib.CallToolRequest{
		Params: mcplib.CallToolParams{Name: "list_collections"},
	})
	if err != nil {
		t.Fatalf("unexpected handleListCollections error: %v", err)
	}
	text := extractToolText(t, result)
	if !strings.Contains(text, "mycol") {
		t.Fatalf("expected collection name in output, got %q", text)
	}
}

func TestHandleListCollections_Empty(t *testing.T) {
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "quant.db")
	store, err := index.NewStore(dbPath)
	if err != nil {
		t.Fatalf("unexpected store open error: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })

	s := newTestServer(dir, dbPath, store)
	suppressLogs(t)

	result, err := s.handleListCollections(context.Background(), mcplib.CallToolRequest{
		Params: mcplib.CallToolParams{Name: "list_collections"},
	})
	if err != nil {
		t.Fatalf("unexpected handleListCollections error: %v", err)
	}
	text := extractToolText(t, result)
	if !strings.Contains(text, "(none)") {
		t.Fatalf("expected empty collections output, got %q", text)
	}
}

func TestHandleDeleteCollection(t *testing.T) {
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "quant.db")
	store, err := index.NewStore(dbPath)
	if err != nil {
		t.Fatalf("unexpected store open error: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })

	if err := store.ReindexDocument(context.Background(), &index.Document{
		Path:       "col/a.txt",
		Hash:       "col-a-hash",
		ModifiedAt: testTime(),
		Collection: "todelete",
	}, []index.ChunkRecord{{
		Content:    "will be deleted",
		ChunkIndex: 0,
		Embedding:  index.EncodeFloat32(index.NormalizeFloat32([]float32{1})),
	}}); err != nil {
		t.Fatalf("unexpected seed error: %v", err)
	}

	s := newTestServer(dir, dbPath, store)
	suppressLogs(t)

	result, err := s.handleDeleteCollection(context.Background(), mcplib.CallToolRequest{
		Params: mcplib.CallToolParams{
			Name:      "delete_collection",
			Arguments: map[string]any{"collection": "todelete"},
		},
	})
	if err != nil {
		t.Fatalf("unexpected handleDeleteCollection error: %v", err)
	}
	text := extractToolText(t, result)
	if !strings.Contains(text, "todelete") {
		t.Fatalf("expected collection name in output, got %q", text)
	}

	docCount, _, err := store.Stats(context.Background())
	if err != nil {
		t.Fatalf("unexpected stats error: %v", err)
	}
	if docCount != 0 {
		t.Fatalf("expected 0 documents after delete, got %d", docCount)
	}
}

func TestHandleDeleteCollection_MissingCollection(t *testing.T) {
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "quant.db")
	store, err := index.NewStore(dbPath)
	if err != nil {
		t.Fatalf("unexpected store open error: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })

	s := newTestServer(dir, dbPath, store)

	_, err = s.handleDeleteCollection(context.Background(), mcplib.CallToolRequest{
		Params: mcplib.CallToolParams{
			Name:      "delete_collection",
			Arguments: map[string]any{},
		},
	})
	if err == nil {
		t.Fatal("expected error for missing collection argument")
	}
}

func TestHandleDrillDown(t *testing.T) {
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "quant.db")
	store, err := index.NewStore(dbPath)
	if err != nil {
		t.Fatalf("unexpected store open error: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })

	if err := store.ReindexDocument(context.Background(), &index.Document{
		Path:       "docs/alpha.md",
		Hash:       "alpha-hash",
		ModifiedAt: testTime(),
	}, []index.ChunkRecord{{
		Content:    "alpha content for drill down",
		ChunkIndex: 0,
		Embedding:  index.EncodeFloat32(index.NormalizeFloat32([]float32{1})),
	}}); err != nil {
		t.Fatalf("unexpected seed error: %v", err)
	}

	chunkMap, err := store.GetDocumentChunksByPath(context.Background(), "docs/alpha.md")
	if err != nil || len(chunkMap) == 0 {
		t.Fatalf("unexpected chunks lookup error: %v", err)
	}
	var drillChunkID int64
	for _, ch := range chunkMap {
		drillChunkID = ch.ID
		break
	}

	s := newTestServer(dir, dbPath, store)
	suppressLogs(t)

	result, err := s.handleDrillDown(context.Background(), mcplib.CallToolRequest{
		Params: mcplib.CallToolParams{
			Name: "drill_down",
			Arguments: map[string]any{
				"chunk_id": float64(drillChunkID),
			},
		},
	})
	if err != nil {
		t.Fatalf("unexpected handleDrillDown error: %v", err)
	}
	text := extractToolText(t, result)
	if !strings.Contains(text, "Drill-down") {
		t.Fatalf("expected drill-down header in output, got %q", text)
	}
}

func TestHandleDrillDown_MissingChunkID(t *testing.T) {
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "quant.db")
	store, err := index.NewStore(dbPath)
	if err != nil {
		t.Fatalf("unexpected store open error: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })

	s := newTestServer(dir, dbPath, store)

	_, err = s.handleDrillDown(context.Background(), mcplib.CallToolRequest{
		Params: mcplib.CallToolParams{
			Name:      "drill_down",
			Arguments: map[string]any{},
		},
	})
	if err == nil {
		t.Fatal("expected error for missing chunk_id")
	}
}

func TestHandleSummarizeMatches(t *testing.T) {
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "quant.db")
	store, err := index.NewStore(dbPath)
	if err != nil {
		t.Fatalf("unexpected store open error: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })

	if err := store.ReindexDocument(context.Background(), &index.Document{
		Path:       "notes/guide.md",
		Hash:       "guide-hash",
		ModifiedAt: testTime(),
	}, []index.ChunkRecord{{
		Content:    "installation guide for the project setup",
		ChunkIndex: 0,
		Embedding:  index.EncodeFloat32(index.NormalizeFloat32([]float32{1})),
	}}); err != nil {
		t.Fatalf("unexpected seed error: %v", err)
	}

	s := newTestServer(dir, dbPath, store)
	suppressLogs(t)

	result, err := s.handleSummarizeMatches(context.Background(), mcplib.CallToolRequest{
		Params: mcplib.CallToolParams{
			Name: "summarize_matches",
			Arguments: map[string]any{
				"query": "installation",
			},
		},
	})
	if err != nil {
		t.Fatalf("unexpected handleSummarizeMatches error: %v", err)
	}
	text := extractToolText(t, result)
	if !strings.Contains(text, "guide.md") {
		t.Fatalf("expected document path in summary, got %q", text)
	}
}

func TestHandleSummarizeMatches_MissingQuery(t *testing.T) {
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "quant.db")
	store, err := index.NewStore(dbPath)
	if err != nil {
		t.Fatalf("unexpected store open error: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })

	s := newTestServer(dir, dbPath, store)

	_, err = s.handleSummarizeMatches(context.Background(), mcplib.CallToolRequest{
		Params: mcplib.CallToolParams{
			Name:      "summarize_matches",
			Arguments: map[string]any{},
		},
	})
	if err == nil {
		t.Fatal("expected error for missing query")
	}
}

func TestAcquireReleaseToolSlot(t *testing.T) {
	s := &Server{maxToolSlots: 2}
	ctx := context.Background()

	if err := s.acquireToolSlot(ctx); err != nil {
		t.Fatalf("unexpected acquire error: %v", err)
	}
	if err := s.acquireToolSlot(ctx); err != nil {
		t.Fatalf("unexpected acquire error: %v", err)
	}

	thirdCtx, thirdCancel := context.WithTimeout(ctx, 2*time.Second)
	defer thirdCancel()

	done := make(chan struct{})
	go func() {
		_ = s.acquireToolSlot(thirdCtx)
		close(done)
	}()

	s.releaseToolSlot()

	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("expected acquire to succeed after release")
	}

	s.releaseToolSlot()
	s.releaseToolSlot()
}

func TestAcquireToolSlot_CancelledContext(t *testing.T) {
	s := &Server{maxToolSlots: 1}
	ctx := context.Background()

	if err := s.acquireToolSlot(ctx); err != nil {
		t.Fatalf("first acquire should succeed: %v", err)
	}

	cancelCtx, cancel := context.WithCancel(context.Background())
	cancel()

	if err := s.acquireToolSlot(cancelCtx); err == nil {
		t.Fatal("expected error for cancelled context with full slots")
	}
}

func TestReleaseToolSlot_NilServer(t *testing.T) {
	var s *Server
	s.releaseToolSlot()
}

func TestCachedEmbed_WithEmbedder(t *testing.T) {
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "quant.db")
	store, err := index.NewStore(dbPath)
	if err != nil {
		t.Fatalf("unexpected store open error: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })

	embedder := &testutil.QueryCountingEmbedder{}
	s := &Server{store: store, embedder: embedder}

	vec, err := s.cachedEmbed(context.Background(), "hello")
	if err != nil {
		t.Fatalf("unexpected cachedEmbed error: %v", err)
	}
	if len(vec) == 0 {
		t.Fatal("expected non-empty embedding")
	}

	embedder.Mu.Lock()
	calls := embedder.Calls
	embedder.Mu.Unlock()
	if calls["hello"] != 1 {
		t.Fatalf("expected 1 embed call for hello, got %d", calls["hello"])
	}
}

func TestCachedEmbed_NilEmbedder(t *testing.T) {
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "quant.db")
	store, err := index.NewStore(dbPath)
	if err != nil {
		t.Fatalf("unexpected store open error: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })

	s := &Server{store: store, embedder: nil}

	_, err = s.cachedEmbed(context.Background(), "hello")
	if err == nil {
		t.Fatal("expected error when embedder is nil")
	}
}

func TestEmbeddingStatus(t *testing.T) {
	if embeddingStatus(nil) != "hybrid" {
		t.Fatal("expected hybrid status for nil error")
	}
	if embeddingStatus(fmt.Errorf("fail")) != "keyword_only" {
		t.Fatal("expected keyword_only status for non-nil error")
	}
}

func TestEmbeddingStatus_Server(t *testing.T) {
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "quant.db")
	store, err := index.NewStore(dbPath)
	if err != nil {
		t.Fatalf("unexpected store open error: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })

	s := newTestServer(dir, dbPath, store)
	status := s.embeddingStatus(context.Background())
	if status != "available" {
		t.Fatalf("expected available status, got %q", status)
	}

	sNoEmbed := &Server{store: store, embedder: nil}
	status = sNoEmbed.embeddingStatus(context.Background())
	if status == "" {
		t.Fatal("expected non-empty status when embedder is nil")
	}
}
