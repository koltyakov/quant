package index

import (
	"context"
	"fmt"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/koltyakov/quant/internal/llm"
)

func TestParseSummaryResponse(t *testing.T) {
	t.Parallel()

	plain, err := parseSummaryResponse("plain text summary")
	if err != nil || plain.Summary != "plain text summary" || plain.Topics != nil {
		t.Fatalf("unexpected plain summary parse: %+v err=%v", plain, err)
	}
	jsonSummary, err := parseSummaryResponse("prefix {\"summary\":\"ok\",\"topics\":[\"a\",\"b\"]} suffix")
	if err != nil || jsonSummary.Summary != "ok" || !reflect.DeepEqual(jsonSummary.Topics, []string{"a", "b"}) {
		t.Fatalf("unexpected json summary parse: %+v err=%v", jsonSummary, err)
	}
}

func TestChunkSummarizerAndBatch(t *testing.T) {
	t.Parallel()

	var prompts []string
	stub := &stubLLMCompleter{
		fn: func(_ context.Context, req llm.CompleteRequest) (llm.CompleteResponse, error) {
			prompts = append(prompts, req.Messages[len(req.Messages)-1].Content)
			return llm.CompleteResponse{Content: `{"summary":"brief","topics":["x","y"]}`}, nil
		},
	}

	summarizer := NewChunkSummarizer(SummarizerConfig{
		Completer: stub,
		Model:     "mini",
	})
	longContent := strings.Repeat("a", 2200)
	result, err := summarizer.Summarize(context.Background(), longContent)
	if err != nil {
		t.Fatalf("Summarize returned error: %v", err)
	}
	if result.Summary != "brief" || !reflect.DeepEqual(result.Topics, []string{"x", "y"}) {
		t.Fatalf("unexpected summary result: %+v", result)
	}
	if len(prompts) != 1 {
		t.Fatalf("expected 1 call, got %d", len(prompts))
	}
	if len(prompts[0]) >= 2300 {
		t.Fatalf("expected truncated prompt body, got length %d", len(prompts[0]))
	}

	calls := 0
	batchStub := &stubLLMCompleter{
		fn: func(_ context.Context, req llm.CompleteRequest) (llm.CompleteResponse, error) {
			calls++
			content := req.Messages[len(req.Messages)-1].Content
			if strings.Contains(content, "Texts:\n") {
				return llm.CompleteResponse{}, fmt.Errorf("batch summarizer error")
			}
			if strings.Contains(content, "bad") {
				return llm.CompleteResponse{}, fmt.Errorf("summarizer error")
			}
			return llm.CompleteResponse{Content: `{"summary":"ok","topics":["topic"]}`}, nil
		},
	}

	batchSummarizer := NewChunkSummarizer(SummarizerConfig{
		Completer: batchStub,
		Model:     "mini",
	})
	batch, err := batchSummarizer.SummarizeBatch(context.Background(), []string{"good", "bad"})
	if err != nil {
		t.Fatalf("SummarizeBatch returned error: %v", err)
	}
	if calls != 3 {
		t.Fatalf("expected batch attempt plus 2 fallback calls, got %d", calls)
	}
	if batch[0].Summary != "ok" || batch[1].Summary != "" {
		t.Fatalf("expected per-item fallback results, got %+v", batch)
	}
}

type stubLLMCompleter struct {
	fn func(context.Context, llm.CompleteRequest) (llm.CompleteResponse, error)
}

func (s *stubLLMCompleter) Complete(ctx context.Context, req llm.CompleteRequest) (llm.CompleteResponse, error) {
	return s.fn(ctx, req)
}

func TestDocEmbeddings(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	store, err := NewStore(filepath.Join(t.TempDir(), "quant.db"))
	if err != nil {
		t.Fatalf("NewStore returned error: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })

	if _, err := store.EnsureEmbeddingMetadata(ctx, EmbeddingMetadata{Model: "test", Dimensions: 2, Normalized: true}); err != nil {
		t.Fatalf("EnsureEmbeddingMetadata returned error: %v", err)
	}

	doc := &Document{
		Path:       "docs/guide.md",
		Hash:       "hash",
		ModifiedAt: time.Now().UTC(),
		FileType:   "markdown",
	}
	docID, err := store.UpsertDocument(ctx, doc)
	if err != nil {
		t.Fatalf("UpsertDocument returned error: %v", err)
	}

	chunks := []ChunkRecord{
		{DocumentID: docID, ChunkIndex: 0, Content: "intro", Embedding: EncodeInt8(NormalizeFloat32([]float32{1, 0}))},
		{DocumentID: docID, ChunkIndex: 1, Content: "body", Embedding: EncodeInt8(NormalizeFloat32([]float32{0, 1}))},
	}
	for i := range chunks {
		if err := store.InsertChunk(ctx, &chunks[i]); err != nil {
			t.Fatalf("InsertChunk(%d) returned error: %v", i, err)
		}
	}

	docIndex := newDocEmbeddingIndex()
	docIndex.Set(docID, doc.Path, []float32{1, 0})
	docIndex.Set(docID+1, "docs/other.md", []float32{0, 1})
	top := docIndex.topDocPaths([]float32{1, 0}, 1)
	if len(top) != 1 || top[doc.Path] <= 0 {
		t.Fatalf("unexpected top doc paths: %+v", top)
	}
	if docIndex.Len() != 2 {
		t.Fatalf("unexpected doc embedding len: %d", docIndex.Len())
	}
	docIndex.Remove(docID+1, "docs/other.md")
	if docIndex.Len() != 1 {
		t.Fatalf("unexpected doc embedding len after remove: %d", docIndex.Len())
	}

	docEmbedding := computeDocEmbedding(chunks, 2)
	if len(docEmbedding) == 0 {
		t.Fatal("expected non-empty computed document embedding")
	}
	if docEmbeddingWeight(0, 5) <= docEmbeddingWeight(2, 5) {
		t.Fatal("expected first chunk to receive stronger weight than middle chunk")
	}
	if abs32(-3.5) != 3.5 {
		t.Fatal("expected abs32 to return absolute value")
	}

	tx, err := store.db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatalf("BeginTx returned error: %v", err)
	}
	if err := store.updateDocEmbeddingTx(ctx, tx, docID, docEmbedding); err != nil {
		t.Fatalf("updateDocEmbeddingTx returned error: %v", err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatalf("Commit returned error: %v", err)
	}
	if err := store.LoadDocEmbeddings(ctx); err != nil {
		t.Fatalf("LoadDocEmbeddings returned error: %v", err)
	}
	if store.docEmbeds.Len() != 1 {
		t.Fatalf("expected one loaded document embedding, got %d", store.docEmbeds.Len())
	}
}
