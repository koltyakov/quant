package ingest

import (
	"context"
	"errors"
	"testing"

	"github.com/koltyakov/quant/internal/chunk"
	"github.com/koltyakov/quant/internal/index"
)

type mockExtractor struct {
	text string
	err  error
}

func (m *mockExtractor) Extract(_ context.Context, _ string) (string, error) {
	return m.text, m.err
}

func (m *mockExtractor) Supports(_ string) bool { return true }

type mockEmbedder struct {
	vectors [][]float32
	err     error
	called  int
}

func (m *mockEmbedder) Embed(_ context.Context, _ string) ([]float32, error) {
	m.called++
	if m.err != nil {
		return nil, m.err
	}
	if len(m.vectors) > 0 {
		return m.vectors[0], nil
	}
	return []float32{1.0}, nil
}

func (m *mockEmbedder) EmbedBatch(_ context.Context, texts []string) ([][]float32, error) {
	m.called++
	if m.err != nil {
		return nil, m.err
	}
	out := make([][]float32, len(texts))
	for i := range out {
		if len(m.vectors) > i {
			out[i] = m.vectors[i]
		} else {
			out[i] = []float32{1.0}
		}
	}
	return out, nil
}

func (m *mockEmbedder) Dimensions() int { return 1 }
func (m *mockEmbedder) Close() error    { return nil }

func TestProcess_EmptyText(t *testing.T) {
	p := &Pipeline{
		Extractor: &mockExtractor{text: ""},
		ChunkSize: 100,
		Overlap:   0.15,
	}
	doc, records, err := p.Process(context.Background(), "key", "file.txt", nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if doc != nil || records != nil {
		t.Fatalf("expected nil for empty text, got doc=%v records=%v", doc, records)
	}
}

func TestProcess_ExtractError(t *testing.T) {
	p := &Pipeline{
		Extractor: &mockExtractor{err: errors.New("extract failed")},
		ChunkSize: 100,
		Overlap:   0.15,
	}
	_, _, err := p.Process(context.Background(), "key", "file.txt", nil)
	if err == nil {
		t.Fatal("expected extract error")
	}
}

func TestProcess_Success(t *testing.T) {
	p := &Pipeline{
		Extractor: &mockExtractor{text: "hello world foo bar baz"},
		Embedder:  &mockEmbedder{vectors: [][]float32{{1.0}}},
		ChunkSize: 5,
		Overlap:   0,
		BatchSize: 10,
	}
	doc, records, err := p.Process(context.Background(), "key", "file.txt", nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if doc == nil {
		t.Fatal("expected non-nil doc")
	}
	if doc.Path != "key" {
		t.Errorf("expected doc path 'key', got %q", doc.Path)
	}
	if len(records) == 0 {
		t.Fatal("expected records")
	}
}

func TestDiffChunks_AllNew(t *testing.T) {
	p := &Pipeline{ChunkSize: 100, Overlap: 0.15}
	chunks := []chunk.Chunk{
		{Content: "alpha", Index: 0},
		{Content: "beta", Index: 1},
	}
	records, toEmbed, positions, err := p.DiffChunks(chunks, nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(records) != 2 {
		t.Fatalf("expected 2 records, got %d", len(records))
	}
	if len(toEmbed) != 2 {
		t.Fatalf("expected 2 to embed, got %d", len(toEmbed))
	}
	if len(positions) != 2 {
		t.Fatalf("expected 2 positions, got %d", len(positions))
	}
}

func TestDiffChunks_ReusesExisting(t *testing.T) {
	p := &Pipeline{ChunkSize: 100, Overlap: 0.15}
	chunks := []chunk.Chunk{
		{Content: "alpha", Index: 0},
		{Content: "beta", Index: 1},
	}
	existing := map[string]index.ChunkRecord{
		index.ChunkDiffKey("alpha"): {Content: "alpha", Embedding: []byte{1, 2, 3}},
	}
	records, toEmbed, _, err := p.DiffChunks(chunks, existing)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(records) != 2 {
		t.Fatalf("expected 2 records, got %d", len(records))
	}
	if len(toEmbed) != 1 {
		t.Fatalf("expected 1 to embed (beta), got %d", len(toEmbed))
	}
	if toEmbed[0].Content != "beta" {
		t.Errorf("expected to embed 'beta', got %q", toEmbed[0].Content)
	}
	if string(records[0].Embedding) != "\x01\x02\x03" {
		t.Errorf("expected reused embedding for alpha")
	}
}

func TestEmbedChunks_Empty(t *testing.T) {
	p := &Pipeline{Embedder: &mockEmbedder{}}
	err := p.EmbedChunks(context.Background(), "key", nil, nil, nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestEmbedChunks_ShortBatch(t *testing.T) {
	p := &Pipeline{
		Embedder:  &mockEmbedder{vectors: [][]float32{{1.0}, {1.0}}},
		BatchSize: 10,
	}
	chunks := []chunk.Chunk{
		{Content: "alpha", Index: 0},
		{Content: "beta", Index: 1},
	}
	positions := []PendingEmbed{
		{ChunkIdx: 0, BatchPos: 0},
		{ChunkIdx: 1, BatchPos: 1},
	}
	records := make([]index.ChunkRecord, 2)
	err := p.EmbedChunks(context.Background(), "key", chunks, positions, records)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if records[0].Content != "alpha" {
		t.Errorf("expected record 0 content 'alpha', got %q", records[0].Content)
	}
	if records[1].Content != "beta" {
		t.Errorf("expected record 1 content 'beta', got %q", records[1].Content)
	}
}

func TestBuildEmbedInput(t *testing.T) {
	tests := []struct {
		docKey, heading, content, want string
	}{
		{"k", "h", "c", "h\n\nc"},
		{"k", "", "c", "c"},
	}
	for _, tt := range tests {
		got := BuildEmbedInput(tt.docKey, tt.heading, tt.content)
		if got != tt.want {
			t.Errorf("BuildEmbedInput(%q,%q,%q) = %q, want %q", tt.docKey, tt.heading, tt.content, got, tt.want)
		}
	}
}

func TestCodeSignature(t *testing.T) {
	tests := []struct {
		input string
		want  string
	}{
		{"func main() {}", "func main() {}"},
		{"", ""},
	}
	for _, tt := range tests {
		got := CodeSignature(tt.input)
		if got != tt.want {
			t.Errorf("CodeSignature(%q) = %q, want %q", tt.input, got, tt.want)
		}
	}
}
