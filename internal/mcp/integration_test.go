package mcp

import (
	"context"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/koltyakov/quant/internal/chunk"
	"github.com/koltyakov/quant/internal/config"
	"github.com/koltyakov/quant/internal/extract"
	"github.com/koltyakov/quant/internal/index"
	"github.com/koltyakov/quant/internal/ingest"
	"github.com/koltyakov/quant/internal/logx"
	runtimestate "github.com/koltyakov/quant/internal/runtime"
	"github.com/koltyakov/quant/internal/testutil"
	mcplib "github.com/mark3labs/mcp-go/mcp"
)

// TestIntegration_IndexAndSearch exercises the full pipeline:
// write files, extract, chunk, embed, store, and search via MCP tool handlers.
func TestIntegration_IndexAndSearch(t *testing.T) {
	logx.SetOutput(io.Discard)
	t.Cleanup(func() { logx.SetOutput(io.Discard) })

	dir := t.TempDir()
	dbPath := filepath.Join(dir, "quant.db")

	writeTestFile(t, filepath.Join(dir, "notes", "architecture.md"), `# Architecture

The system uses a layered architecture with clear separation of concerns.
The indexing pipeline extracts text, splits it into chunks, and computes
vector embeddings using a local Ollama instance.

## Search

Queries are processed using hybrid search combining FTS5 keyword matching
with HNSW approximate nearest neighbor vector search. Results are fused
using reciprocal rank fusion.
`)

	writeTestFile(t, filepath.Join(dir, "src", "main.go"), `package main

import "fmt"

func main() {
	fmt.Println("hello quant")
}

func processDocument(path string) error {
	return nil
}
`)

	writeTestFile(t, filepath.Join(dir, "docs", "setup.html"), `<!DOCTYPE html>
<html><body>
<h1>Setup Guide</h1>
<p>Install quant using the provided install script. The server watches
a directory and automatically indexes supported file types.</p>
</body></html>
`)

	store, err := index.NewStore(dbPath)
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })

	embedder := &testutil.StaticEmbedder{}
	extractor := extract.NewRouter()
	pipeline := &ingest.Pipeline{
		Embedder:  embedder,
		ChunkSize: 128,
		Overlap:   0.1,
		BatchSize: 16,
	}

	indexFiles(t, store, extractor, pipeline, dir, []string{
		"notes/architecture.md",
		"src/main.go",
		"docs/setup.html",
	})

	tracker := runtimestate.NewIndexStateTracker()
	tracker.Set(runtimestate.IndexStateReady, "integration test")

	s := &Server{
		cfg: &config.Config{
			WatchDir:   dir,
			DBPath:     dbPath,
			EmbedModel: "test-model",
		},
		store:    store,
		embedder: embedder,
		version:  "test",
		state:    tracker,
	}

	t.Run("search returns relevant results", func(t *testing.T) {
		result, err := s.handleSearch(context.Background(), mcplib.CallToolRequest{
			Params: mcplib.CallToolParams{
				Name:      "search",
				Arguments: map[string]any{"query": "architecture indexing pipeline", "limit": float64(10)},
			},
		})
		if err != nil {
			t.Fatalf("handleSearch: %v", err)
		}

		text := extractToolText(t, result)
		if !strings.Contains(text, "architecture.md") {
			t.Errorf("expected architecture.md in results, got:\n%s", text)
		}

		structured := extractSearchStructured(t, result)
		if structured.Query != "architecture indexing pipeline" {
			t.Errorf("structured.Query = %q, want %q", structured.Query, "architecture indexing pipeline")
		}
		if len(structured.Results) == 0 {
			t.Fatal("expected at least one structured result")
		}
		if structured.EmbeddingStatus != "hybrid" {
			t.Errorf("EmbeddingStatus = %q, want hybrid", structured.EmbeddingStatus)
		}
	})

	t.Run("search with path filter", func(t *testing.T) {
		result, err := s.handleSearch(context.Background(), mcplib.CallToolRequest{
			Params: mcplib.CallToolParams{
				Name:      "search",
				Arguments: map[string]any{"query": "quant", "path": "src/"},
			},
		})
		if err != nil {
			t.Fatalf("handleSearch: %v", err)
		}

		text := extractToolText(t, result)
		if strings.Contains(text, "architecture.md") {
			t.Error("path filter should exclude notes/architecture.md")
		}
		if strings.Contains(text, "setup.html") {
			t.Error("path filter should exclude docs/setup.html")
		}
	})

	t.Run("list_sources shows all indexed files", func(t *testing.T) {
		result, err := s.handleListSources(context.Background(), mcplib.CallToolRequest{
			Params: mcplib.CallToolParams{Name: "list_sources"},
		})
		if err != nil {
			t.Fatalf("handleListSources: %v", err)
		}

		text := extractToolText(t, result)
		for _, path := range []string{"notes/architecture.md", "src/main.go", "docs/setup.html"} {
			if !strings.Contains(text, path) {
				t.Errorf("expected %s in list_sources output", path)
			}
		}
	})

	t.Run("index_status reflects correct counts", func(t *testing.T) {
		result, err := s.handleIndexStatus(context.Background(), mcplib.CallToolRequest{
			Params: mcplib.CallToolParams{Name: "index_status"},
		})
		if err != nil {
			t.Fatalf("handleIndexStatus: %v", err)
		}

		text := extractToolText(t, result)
		if !strings.Contains(text, "Documents: 3") {
			t.Errorf("expected 3 documents in status, got:\n%s", text)
		}
		if !strings.Contains(text, "test-model") {
			t.Errorf("expected model name in status, got:\n%s", text)
		}

		structured := extractIndexStatusStructured(t, result)
		if structured.Documents != 3 {
			t.Errorf("structured.Documents = %d, want 3", structured.Documents)
		}
		if structured.Chunks == 0 {
			t.Error("expected non-zero chunk count")
		}
		if structured.State != string(runtimestate.IndexStateReady) {
			t.Errorf("structured.State = %q, want ready", structured.State)
		}
	})

	t.Run("delete and re-search", func(t *testing.T) {
		if err := store.DeleteDocument(context.Background(), "src/main.go"); err != nil {
			t.Fatalf("DeleteDocument: %v", err)
		}

		result, err := s.handleSearch(context.Background(), mcplib.CallToolRequest{
			Params: mcplib.CallToolParams{
				Name:      "search",
				Arguments: map[string]any{"query": "processDocument main", "limit": float64(10)},
			},
		})
		if err != nil {
			t.Fatalf("handleSearch after delete: %v", err)
		}

		text := extractToolText(t, result)
		if strings.Contains(text, "main.go") {
			t.Error("deleted document should not appear in results")
		}
	})

	t.Run("reindex updates content", func(t *testing.T) {
		writeTestFile(t, filepath.Join(dir, "notes", "architecture.md"), `# Architecture v2

The system now supports ColBERT-based reranking for improved precision.
`)

		indexFiles(t, store, extractor, pipeline, dir, []string{"notes/architecture.md"})

		result, err := s.handleSearch(context.Background(), mcplib.CallToolRequest{
			Params: mcplib.CallToolParams{
				Name:      "search",
				Arguments: map[string]any{"query": "ColBERT reranking precision"},
			},
		})
		if err != nil {
			t.Fatalf("handleSearch after reindex: %v", err)
		}

		structured := extractSearchStructured(t, result)
		found := false
		for _, r := range structured.Results {
			if strings.Contains(r.ChunkContent, "ColBERT") {
				found = true
				break
			}
		}
		if !found {
			t.Error("reindexed content with ColBERT not found in search results")
		}
	})
}

// TestIntegration_MultiFormatExtraction verifies that the pipeline correctly
// handles different file formats end-to-end.
func TestIntegration_MultiFormatExtraction(t *testing.T) {
	logx.SetOutput(io.Discard)
	t.Cleanup(func() { logx.SetOutput(io.Discard) })

	dir := t.TempDir()
	dbPath := filepath.Join(dir, "quant.db")

	writeTestFile(t, filepath.Join(dir, "readme.md"), `# Project Overview

This project provides semantic search over local files.
It supports markdown, plain text, HTML, and source code.
`)

	writeTestFile(t, filepath.Join(dir, "config.yaml"), `server:
  port: 8080
  transport: stdio
embedding:
  model: nomic-embed-text
  url: http://localhost:11434
`)

	writeTestFile(t, filepath.Join(dir, "utils.py"), `"""Utility functions for data processing."""

def normalize_text(text: str) -> str:
    """Remove extra whitespace and normalize unicode."""
    return " ".join(text.split())

def chunk_text(text: str, size: int = 512) -> list[str]:
    """Split text into chunks of approximately size words."""
    words = text.split()
    return [" ".join(words[i:i+size]) for i in range(0, len(words), size)]
`)

	store, err := index.NewStore(dbPath)
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })

	embedder := &testutil.StaticEmbedder{}
	extractor := extract.NewRouter()
	pipeline := &ingest.Pipeline{
		Embedder:  embedder,
		ChunkSize: 128,
		Overlap:   0.1,
		BatchSize: 16,
	}

	indexFiles(t, store, extractor, pipeline, dir, []string{
		"readme.md",
		"config.yaml",
		"utils.py",
	})

	docCount, chunkCount, err := store.Stats(context.Background())
	if err != nil {
		t.Fatalf("Stats: %v", err)
	}
	if docCount != 3 {
		t.Errorf("docCount = %d, want 3", docCount)
	}
	if chunkCount == 0 {
		t.Error("expected non-zero chunk count")
	}

	tracker := runtimestate.NewIndexStateTracker()
	tracker.Set(runtimestate.IndexStateReady, "test")

	s := &Server{
		cfg: &config.Config{
			WatchDir:   dir,
			DBPath:     dbPath,
			EmbedModel: "test-model",
		},
		store:    store,
		embedder: embedder,
		state:    tracker,
	}

	result, err := s.handleSearch(context.Background(), mcplib.CallToolRequest{
		Params: mcplib.CallToolParams{
			Name:      "search",
			Arguments: map[string]any{"query": "normalize text whitespace"},
		},
	})
	if err != nil {
		t.Fatalf("handleSearch: %v", err)
	}

	text := extractToolText(t, result)
	if !strings.Contains(text, "utils.py") {
		t.Errorf("expected utils.py in results, got:\n%s", text)
	}
}

func writeTestFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0750); err != nil {
		t.Fatalf("creating directory for %s: %v", path, err)
	}
	if err := os.WriteFile(path, []byte(content), 0644); err != nil {
		t.Fatalf("writing %s: %v", path, err)
	}
}

func indexFiles(t *testing.T, store *index.Store, extractor extract.Extractor, pipeline *ingest.Pipeline, baseDir string, relPaths []string) {
	t.Helper()
	ctx := context.Background()

	for _, relPath := range relPaths {
		absPath := filepath.Join(baseDir, relPath)

		text, err := extractor.Extract(ctx, absPath)
		if err != nil {
			t.Fatalf("extracting %s: %v", relPath, err)
		}
		if text == "" {
			t.Fatalf("extractor returned empty text for %s", relPath)
		}

		chunks := ingest.PrepareChunks(text, relPath, pipeline.ChunkSize, pipeline.Overlap)
		if len(chunks) == 0 {
			t.Fatalf("no chunks produced for %s", relPath)
		}

		records := make([]index.ChunkRecord, len(chunks))
		toEmbed := chunks
		positions := make([]ingest.PendingEmbed, len(chunks))
		for i, c := range chunks {
			positions[i] = ingest.PendingEmbed{ChunkIdx: i, BatchPos: i}
			records[i] = index.ChunkRecord{
				Content:    c.Content,
				ChunkIndex: c.Index,
			}
			_ = c
		}

		if err := pipeline.EmbedChunks(ctx, relPath, toEmbed, positions, records); err != nil {
			t.Fatalf("embedding chunks for %s: %v", relPath, err)
		}

		info, err := os.Stat(absPath)
		if err != nil {
			t.Fatalf("stat %s: %v", relPath, err)
		}
		doc := &index.Document{
			Path:       relPath,
			Hash:       relPath + "-hash",
			ModifiedAt: info.ModTime(),
		}

		if err := store.ReindexDocument(ctx, doc, records); err != nil {
			t.Fatalf("storing %s: %v", relPath, err)
		}
	}
}

// TestIntegration_ChunkBoundaries verifies that search works correctly across
// chunk boundaries; content split across two chunks should still be findable.
func TestIntegration_ChunkBoundaries(t *testing.T) {
	logx.SetOutput(io.Discard)
	t.Cleanup(func() { logx.SetOutput(io.Discard) })

	dir := t.TempDir()
	dbPath := filepath.Join(dir, "quant.db")

	store, err := index.NewStore(dbPath)
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })

	embedder := &testutil.QueryCountingEmbedder{}

	longText := strings.Join([]string{
		"one two three four five six seven ALPHA_BOUNDARY",
		"OMEGA_BOUNDARY eight nine ten eleven twelve thirteen fourteen",
	}, "\n\n")

	chunks := chunk.Split(longText, 8, 0)
	if len(chunks) < 2 {
		t.Fatalf("expected multiple chunks, got %d", len(chunks))
	}
	if !strings.Contains(chunks[0].Content, "ALPHA_BOUNDARY") || !strings.Contains(chunks[1].Content, "OMEGA_BOUNDARY") {
		t.Fatalf("expected sentinel terms in adjacent chunks, got: %#v", chunks)
	}

	records := make([]index.ChunkRecord, len(chunks))
	for i, c := range chunks {
		records[i] = index.ChunkRecord{
			Content:    c.Content,
			ChunkIndex: c.Index,
			Embedding:  index.EncodeFloat32(index.NormalizeFloat32([]float32{1})),
		}
	}

	if err := store.ReindexDocument(context.Background(), &index.Document{
		Path:       "long-document.txt",
		Hash:       "long-hash",
		ModifiedAt: time.Now(),
	}, records); err != nil {
		t.Fatalf("ReindexDocument: %v", err)
	}

	tracker := runtimestate.NewIndexStateTracker()
	tracker.Set(runtimestate.IndexStateReady, "test")

	s := &Server{
		cfg: &config.Config{
			WatchDir:   dir,
			DBPath:     dbPath,
			EmbedModel: "test-model",
		},
		store:    store,
		embedder: embedder,
		state:    tracker,
	}

	result, err := s.handleSearch(context.Background(), mcplib.CallToolRequest{
		Params: mcplib.CallToolParams{
			Name:      "search",
			Arguments: map[string]any{"query": "ALPHA_BOUNDARY OMEGA_BOUNDARY", "limit": float64(10)},
		},
	})
	if err != nil {
		t.Fatalf("handleSearch: %v", err)
	}

	structured := extractSearchStructured(t, result)
	if len(structured.Results) == 0 {
		t.Fatal("expected results for sentinel phrase")
	}

	found := false
	for _, r := range structured.Results {
		if strings.Contains(r.ChunkContent, "ALPHA_BOUNDARY") {
			found = true
			break
		}
	}
	if !found {
		t.Error("first boundary sentinel not found in any result chunk content")
	}

	found = false
	for _, r := range structured.Results {
		if strings.Contains(r.ChunkContent, "OMEGA_BOUNDARY") {
			found = true
			break
		}
	}
	if !found {
		t.Error("second boundary sentinel not found in any result chunk content")
	}
}
