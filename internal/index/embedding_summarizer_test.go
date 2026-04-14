package index

import (
	"context"
	"encoding/binary"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"
)

func TestColbertEncodingProjectionAndSummary(t *testing.T) {
	t.Parallel()

	colbert := NewColBERTIndex(ColBERTConfig{Enabled: true, MaxTokens: 1})
	colbert.Add(1, [][]float32{{1, 0}, {0.2, 0.8}})
	colbert.Add(2, [][]float32{{0, 1}})
	if colbert.Len() != 2 {
		t.Fatalf("unexpected colbert len: %d", colbert.Len())
	}
	if got := colbert.Search([][]float32{{1, 0}}, 2); got != nil {
		t.Fatalf("expected nil results before ready, got %+v", got)
	}
	colbert.SetReady(true)
	results := colbert.Search([][]float32{{1, 0}}, 2)
	if len(results) != 2 || results[0].ChunkID != 1 || results[0].Score <= results[1].Score {
		t.Fatalf("unexpected colbert search results: %+v", results)
	}
	colbert.Remove(2)
	if colbert.Len() != 1 || !colbert.Ready() {
		t.Fatalf("unexpected colbert state after remove: len=%d ready=%v", colbert.Len(), colbert.Ready())
	}

	encodedTokens := EncodeTokenEmbeddings([][]float32{{1, 2}, {3, 4}})
	decodedTokens := DecodeTokenEmbeddings(encodedTokens)
	if len(decodedTokens) != 2 || len(decodedTokens[0]) != 2 || decodedTokens[1][1] != 4 {
		t.Fatalf("unexpected decoded token embeddings: %+v", decodedTokens)
	}
	if DecodeTokenEmbeddings([]byte("bad")) != nil {
		t.Fatal("expected invalid token embeddings to decode as nil")
	}

	proj := &ProjectionLayer{
		weight:  []float32{1, 0, 0, 2},
		bias:    []float32{0.5, -0.5},
		inDims:  2,
		outDims: 2,
	}
	projected := proj.Project([]float32{3, 4})
	if len(projected) != 2 {
		t.Fatalf("unexpected projected dims: %v", projected)
	}
	if proj.Project([]float32{1}) != nil {
		t.Fatal("expected nil for mismatched projection dims")
	}
	roundTrip, err := LoadProjection(proj.Encode())
	if err != nil {
		t.Fatalf("LoadProjection returned error: %v", err)
	}
	if !reflect.DeepEqual(roundTrip.weight, proj.weight) || !reflect.DeepEqual(roundTrip.bias, proj.bias) {
		t.Fatalf("projection roundtrip mismatch: got %+v want %+v", roundTrip, proj)
	}
	if _, err := LoadProjection([]byte{1, 2, 3}); err == nil {
		t.Fatal("expected short projection decode to fail")
	}

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

	var bodies []string
	attempts := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/chat" || r.Method != http.MethodPost {
			t.Fatalf("unexpected request: %s %s", r.Method, r.URL.Path)
		}
		var payload map[string]any
		if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
			t.Fatalf("decode request: %v", err)
		}
		rawMessages, _ := payload["messages"].([]any)
		if len(rawMessages) != 2 {
			t.Fatalf("unexpected message count: %d", len(rawMessages))
		}
		second := rawMessages[1].(map[string]any)
		bodies = append(bodies, second["content"].(string))
		attempts++
		if attempts == 1 {
			http.Error(w, "retry later", http.StatusBadGateway)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		var resp ollamaChatResponse
		resp.Message.Content = `{"summary":"brief","topics":["x","y"]}`
		_ = json.NewEncoder(w).Encode(resp)
	}))
	defer server.Close()

	summarizer := NewChunkSummarizer(SummarizerConfig{
		BaseURL:    server.URL,
		Model:      "mini",
		Timeout:    time.Second,
		MaxRetries: 1,
	})
	longContent := strings.Repeat("a", 2200)
	result, err := summarizer.Summarize(context.Background(), longContent)
	if err != nil {
		t.Fatalf("Summarize returned error: %v", err)
	}
	if result.Summary != "brief" || !reflect.DeepEqual(result.Topics, []string{"x", "y"}) {
		t.Fatalf("unexpected summary result: %+v", result)
	}
	if attempts != 2 {
		t.Fatalf("expected one retry, got %d attempts", attempts)
	}
	if len(bodies) == 0 || len(bodies[0]) >= 2300 {
		t.Fatalf("expected truncated prompt body, got length %d", len(bodies[0]))
	}

	batchServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var payload map[string]any
		if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
			t.Fatalf("decode batch request: %v", err)
		}
		second := payload["messages"].([]any)[1].(map[string]any)
		content := second["content"].(string)
		w.Header().Set("Content-Type", "application/json")
		if strings.Contains(content, "bad") {
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		var resp ollamaChatResponse
		resp.Message.Content = `{"summary":"ok","topics":["topic"]}`
		_ = json.NewEncoder(w).Encode(resp)
	}))
	defer batchServer.Close()

	batchSummarizer := NewChunkSummarizer(SummarizerConfig{BaseURL: batchServer.URL, Model: "mini", MaxRetries: -1})
	if batchSummarizer.maxRetries != 1 {
		t.Fatalf("expected negative retries to normalize to 1, got %d", batchSummarizer.maxRetries)
	}
	batch, err := batchSummarizer.SummarizeBatch(context.Background(), []string{"good", "bad"})
	if err != nil {
		t.Fatalf("SummarizeBatch returned error: %v", err)
	}
	if batch[0].Summary != "ok" || batch[1].Summary != "" || batch[1].Topics != nil {
		t.Fatalf("unexpected batch results: %+v", batch)
	}
}

func TestDocEmbeddingsAndProjectionMigration(t *testing.T) {
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

	if err := store.SaveProjection(ctx, &ProjectionLayer{
		weight:  []float32{1, 0, 0, 1},
		bias:    []float32{0, 0},
		inDims:  2,
		outDims: 2,
	}); err != nil {
		t.Fatalf("SaveProjection returned error: %v", err)
	}
	loadedProj, err := store.LoadProjection(ctx)
	if err != nil {
		t.Fatalf("LoadProjection returned error: %v", err)
	}
	if loadedProj.InDims() != 2 || loadedProj.OutDims() != 2 {
		t.Fatalf("unexpected stored projection dims: in=%d out=%d", loadedProj.InDims(), loadedProj.OutDims())
	}

	if err := store.MigrateEmbeddingsWithProjection(ctx, loadedProj); err != nil {
		t.Fatalf("MigrateEmbeddingsWithProjection returned error: %v", err)
	}
	var embedding []byte
	if err := store.db.QueryRowContext(ctx, `SELECT embedding FROM chunks WHERE chunk_index = 0`).Scan(&embedding); err != nil {
		t.Fatalf("query migrated embedding: %v", err)
	}
	if len(embedding) == 0 {
		t.Fatal("expected migrated embedding bytes")
	}
	if got, want := len(embedding), 8+loadedProj.OutDims(); got != want {
		t.Fatalf("unexpected encoded embedding length: got %d want %d", got, want)
	}
	if scale := binary.LittleEndian.Uint32(embedding[4:8]); scale == 0 {
		t.Fatal("expected non-zero quantization scale after migration")
	}
}
