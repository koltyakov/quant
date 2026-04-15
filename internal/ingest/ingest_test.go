package ingest

import (
	"context"
	"errors"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/koltyakov/quant/internal/chunk"
	"github.com/koltyakov/quant/internal/embed"
	"github.com/koltyakov/quant/internal/index"
)

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

type mockDedupStore struct {
	store    map[string][]byte
	lookups  int
	storeErr error
}

func newMockDedupStore() *mockDedupStore {
	return &mockDedupStore{store: make(map[string][]byte)}
}

func (m *mockDedupStore) LookupContentDedup(_ context.Context, key string) ([]byte, bool) {
	m.lookups++
	v, ok := m.store[key]
	return v, ok
}

func (m *mockDedupStore) StoreContentDedup(_ context.Context, key string, embedding []byte) error {
	if m.storeErr != nil {
		return m.storeErr
	}
	m.store[key] = embedding
	return nil
}

type mockSummarizer struct {
	summaries []*ChunkSummary
	err       error
}

func (m *mockSummarizer) SummarizeBatch(_ context.Context, _ []string) ([]*ChunkSummary, error) {
	if m.err != nil {
		return nil, m.err
	}
	return m.summaries, nil
}

func TestDiffChunks_AllNew(t *testing.T) {
	t.Parallel()
	p := &Pipeline{ChunkSize: 100, Overlap: 0.15}
	chunks := []chunk.Chunk{
		{Content: "alpha", Index: 0},
		{Content: "beta", Index: 1},
	}
	records, toEmbed, positions, err := p.DiffChunks(context.Background(), chunks, nil)
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
	t.Parallel()
	p := &Pipeline{ChunkSize: 100, Overlap: 0.15}
	chunks := []chunk.Chunk{
		{Content: "alpha", Index: 0},
		{Content: "beta", Index: 1},
	}
	existing := map[string]index.ChunkRecord{
		index.ChunkDiffKey("alpha"): {Content: "alpha", Embedding: []byte{1, 2, 3}},
	}
	records, toEmbed, _, err := p.DiffChunks(context.Background(), chunks, existing)
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

func TestDiffChunks_DedupHit(t *testing.T) {
	t.Parallel()
	dedup := newMockDedupStore()
	dedup.store[index.ChunkDiffKey("existing")] = []byte("dedup-emb")
	p := &Pipeline{DedupStore: dedup}
	ctx := context.Background()

	chunks := []chunk.Chunk{
		{Content: "existing", Index: 0},
		{Content: "new", Index: 1},
	}
	records, toEmbed, _, err := p.DiffChunks(ctx, chunks, nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(toEmbed) != 1 {
		t.Fatalf("expected 1 chunk to embed, got %d", len(toEmbed))
	}
	if toEmbed[0].Content != "new" {
		t.Fatalf("expected new chunk content 'new', got %q", toEmbed[0].Content)
	}
	if string(records[0].Embedding) != "dedup-emb" {
		t.Fatalf("expected dedup-emb for existing chunk, got %q", string(records[0].Embedding))
	}
	if dedup.lookups != 2 {
		t.Fatalf("expected 2 dedup lookups, got %d", dedup.lookups)
	}
}

func TestDiffChunks_DedupMiss(t *testing.T) {
	t.Parallel()
	dedup := newMockDedupStore()
	p := &Pipeline{DedupStore: dedup}
	ctx := context.Background()

	chunks := []chunk.Chunk{{Content: "novel", Index: 0}}
	records, toEmbed, positions, err := p.DiffChunks(ctx, chunks, nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(toEmbed) != 1 {
		t.Fatalf("expected 1 chunk to embed for dedup miss, got %d", len(toEmbed))
	}
	if len(positions) != 1 {
		t.Fatalf("expected 1 position, got %d", len(positions))
	}
	if records[0].Content != "" {
		t.Fatalf("expected empty record for dedup miss, got %q", records[0].Content)
	}
}

func TestDiffChunks_Empty(t *testing.T) {
	t.Parallel()
	p := &Pipeline{}
	records, toEmbed, positions, err := p.DiffChunks(context.Background(), nil, nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(records) != 0 {
		t.Fatalf("expected no records, got %d", len(records))
	}
	if len(toEmbed) != 0 {
		t.Fatalf("expected no chunks to embed, got %d", len(toEmbed))
	}
	if len(positions) != 0 {
		t.Fatalf("expected no positions, got %d", len(positions))
	}
}

func TestDiffChunks_MixedExistingAndNew(t *testing.T) {
	t.Parallel()
	p := &Pipeline{}
	ctx := context.Background()
	existing := map[string]index.ChunkRecord{
		index.ChunkDiffKey("first"): {Content: "first", ChunkIndex: 0, Embedding: []byte("e1")},
		index.ChunkDiffKey("third"): {Content: "third", ChunkIndex: 2, Embedding: []byte("e3")},
	}
	chunks := []chunk.Chunk{
		{Content: "first", Index: 0},
		{Content: "second", Index: 1},
		{Content: "third", Index: 2},
	}
	records, toEmbed, positions, err := p.DiffChunks(ctx, chunks, existing)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(records) != 3 {
		t.Fatalf("expected 3 records, got %d", len(records))
	}
	if string(records[0].Embedding) != "e1" {
		t.Fatalf("expected reused embedding for first")
	}
	if records[1].Content != "" {
		t.Fatalf("expected empty record for new chunk, got %q", records[1].Content)
	}
	if string(records[2].Embedding) != "e3" {
		t.Fatalf("expected reused embedding for third")
	}
	if len(toEmbed) != 1 {
		t.Fatalf("expected 1 chunk to embed, got %d", len(toEmbed))
	}
	if toEmbed[0].Content != "second" {
		t.Fatalf("expected 'second' to embed, got %q", toEmbed[0].Content)
	}
	if positions[0].ChunkIdx != 1 {
		t.Fatalf("expected position ChunkIdx=1, got %d", positions[0].ChunkIdx)
	}
}

func TestEmbedChunks_Empty(t *testing.T) {
	t.Parallel()
	p := &Pipeline{Embedder: &mockEmbedder{}}
	err := p.EmbedChunks(context.Background(), "doc", nil, nil, nil)
	if err != nil {
		t.Fatalf("unexpected error for empty embed: %v", err)
	}
}

func TestEmbedChunks_NoEmbedder(t *testing.T) {
	t.Parallel()
	p := &Pipeline{Embedder: nil}
	chunks := []chunk.Chunk{{Content: "text", Index: 0}}
	records := make([]index.ChunkRecord, 1)
	err := p.EmbedChunks(context.Background(), "doc", chunks, []PendingEmbed{{ChunkIdx: 0, BatchPos: 0}}, records)
	if err != nil {
		t.Fatalf("expected no error when no embedder, got %v", err)
	}
}

func TestEmbedChunks_ShortBatch(t *testing.T) {
	t.Parallel()
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

func TestEmbedChunks_WithDepthAndSectionTitle(t *testing.T) {
	t.Parallel()
	p := &Pipeline{
		Embedder:  &mockEmbedder{},
		BatchSize: 16,
	}
	chunks := []chunk.Chunk{
		{Content: "alpha", Index: 0, Heading: "H1", Depth: 2, SectionTitle: "Section A"},
	}
	positions := []PendingEmbed{{ChunkIdx: 0, BatchPos: 0}}
	records := make([]index.ChunkRecord, 1)

	err := p.EmbedChunks(context.Background(), "doc.txt", chunks, positions, records)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if records[0].Depth != 2 {
		t.Fatalf("expected depth 2, got %d", records[0].Depth)
	}
	if records[0].SectionTitle != "Section A" {
		t.Fatalf("expected section title 'Section A', got %q", records[0].SectionTitle)
	}
}

func TestEmbedChunks_WithDedupStore(t *testing.T) {
	t.Parallel()
	dedup := newMockDedupStore()
	p := &Pipeline{
		Embedder:   &mockEmbedder{},
		DedupStore: dedup,
		BatchSize:  16,
	}
	chunks := []chunk.Chunk{{Content: "unique", Index: 0}}
	positions := []PendingEmbed{{ChunkIdx: 0, BatchPos: 0}}
	records := make([]index.ChunkRecord, 1)

	err := p.EmbedChunks(context.Background(), "doc.txt", chunks, positions, records)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	key := index.ChunkDiffKey("unique")
	if _, ok := dedup.store[key]; !ok {
		t.Fatal("expected dedup store to contain key for embedded chunk")
	}
}

func TestEmbedChunks_DedupStoreError_SilentlyIgnored(t *testing.T) {
	t.Parallel()
	dedup := &mockDedupStore{store: make(map[string][]byte), storeErr: errors.New("store fail")}
	p := &Pipeline{
		Embedder:   &mockEmbedder{},
		DedupStore: dedup,
		BatchSize:  16,
	}
	chunks := []chunk.Chunk{{Content: "unique", Index: 0}}
	positions := []PendingEmbed{{ChunkIdx: 0, BatchPos: 0}}
	records := make([]index.ChunkRecord, 1)

	err := p.EmbedChunks(context.Background(), "doc.txt", chunks, positions, records)
	if err != nil {
		t.Fatalf("dedup store error should be silently ignored: %v", err)
	}
}

func TestEmbedChunks_EmbedderError(t *testing.T) {
	t.Parallel()
	p := &Pipeline{
		Embedder:  &mockEmbedder{err: errors.New("embed fail")},
		BatchSize: 16,
	}
	chunks := []chunk.Chunk{{Content: "text", Index: 0}}
	positions := []PendingEmbed{{ChunkIdx: 0, BatchPos: 0}}
	records := make([]index.ChunkRecord, 1)

	err := p.EmbedChunks(context.Background(), "doc.txt", chunks, positions, records)
	if err == nil {
		t.Fatal("expected error from embedder")
	}
	if !strings.Contains(err.Error(), "embed fail") {
		t.Fatalf("expected error to contain 'embed fail', got %v", err)
	}
}

type shortEmbedder struct{}

func (s *shortEmbedder) Embed(_ context.Context, _ string) ([]float32, error) {
	return []float32{0.5}, nil
}

func (s *shortEmbedder) EmbedBatch(_ context.Context, texts []string) ([][]float32, error) {
	return [][]float32{{0.5}}, nil
}
func (s *shortEmbedder) Dimensions() int { return 1 }
func (s *shortEmbedder) Close() error    { return nil }

func TestEmbedChunks_MismatchedEmbeddingCount(t *testing.T) {
	t.Parallel()
	p := &Pipeline{
		Embedder:  &shortEmbedder{},
		BatchSize: 16,
	}
	ctx := context.Background()

	chunks := []chunk.Chunk{{Content: "a", Index: 0}, {Content: "b", Index: 1}}
	positions := []PendingEmbed{{ChunkIdx: 0, BatchPos: 0}, {ChunkIdx: 1, BatchPos: 1}}
	records := make([]index.ChunkRecord, 2)

	err := p.EmbedChunks(ctx, "doc.txt", chunks, positions, records)
	if err == nil {
		t.Fatal("expected error for embedding count mismatch")
	}
	if !strings.Contains(err.Error(), "embedder returned") {
		t.Fatalf("expected mismatch error, got %v", err)
	}
}

func TestEmbedChunks_WithSummarizer(t *testing.T) {
	t.Parallel()
	p := &Pipeline{
		Embedder:   &mockEmbedder{},
		Summarizer: &mockSummarizer{summaries: []*ChunkSummary{{Summary: "test summary", Topics: []string{"topic1"}}}},
		BatchSize:  16,
	}
	chunks := []chunk.Chunk{{Content: "hello", Index: 0}}
	positions := []PendingEmbed{{ChunkIdx: 0, BatchPos: 0}}
	records := make([]index.ChunkRecord, 1)

	err := p.EmbedChunks(context.Background(), "doc.txt", chunks, positions, records)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if records[0].Summary != "test summary" {
		t.Fatalf("expected summary 'test summary', got %q", records[0].Summary)
	}
}

func TestEmbedChunks_SummarizerError_DoesNotFailEmbed(t *testing.T) {
	t.Parallel()
	p := &Pipeline{
		Embedder:   &mockEmbedder{},
		Summarizer: &mockSummarizer{err: errors.New("summarize fail")},
		BatchSize:  16,
	}
	chunks := []chunk.Chunk{{Content: "hello", Index: 0}}
	positions := []PendingEmbed{{ChunkIdx: 0, BatchPos: 0}}
	records := make([]index.ChunkRecord, 1)

	err := p.EmbedChunks(context.Background(), "doc.txt", chunks, positions, records)
	if err != nil {
		t.Fatalf("summarizer error should not fail embedding: %v", err)
	}
	if records[0].Summary != "" {
		t.Fatalf("expected empty summary on error, got %q", records[0].Summary)
	}
}

func TestEmbedChunks_ContextCancellation(t *testing.T) {
	t.Parallel()
	p := &Pipeline{
		Embedder:  &mockEmbedder{err: context.Canceled},
		BatchSize: 16,
	}
	chunks := []chunk.Chunk{{Content: "text", Index: 0}}
	positions := []PendingEmbed{{ChunkIdx: 0, BatchPos: 0}}
	records := make([]index.ChunkRecord, 1)

	err := p.EmbedChunks(context.Background(), "doc.txt", chunks, positions, records)
	if err == nil {
		t.Fatal("expected error from context cancellation")
	}
}

func TestEmbedChunks_DefaultBatchSize(t *testing.T) {
	t.Parallel()
	emb := &mockEmbedder{}
	p := &Pipeline{Embedder: emb, BatchSize: 0}
	chunks := []chunk.Chunk{{Content: "text", Index: 0}}
	positions := []PendingEmbed{{ChunkIdx: 0, BatchPos: 0}}
	records := make([]index.ChunkRecord, 1)

	err := p.EmbedChunks(context.Background(), "doc.txt", chunks, positions, records)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if emb.called == 0 {
		t.Fatal("expected embedder to be called")
	}
}

func TestEmbedChunks_EmbedInputWithHeading(t *testing.T) {
	t.Parallel()
	emb := &mockEmbedder{}
	p := &Pipeline{Embedder: emb, BatchSize: 16}
	ctx := context.Background()

	chunks := []chunk.Chunk{{Content: "body", Heading: "My Heading", Index: 0}}
	positions := []PendingEmbed{{ChunkIdx: 0, BatchPos: 0}}
	records := make([]index.ChunkRecord, 1)

	err := p.EmbedChunks(ctx, "doc.txt", chunks, positions, records)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestBuildEmbedInput(t *testing.T) {
	t.Parallel()
	tests := []struct {
		docKey, heading, content, want string
	}{
		{"k", "h", "c", "h\n\nc"},
		{"k", "", "c", "c"},
		{"", "", "just text", "just text"},
		{"", "H", "c", "H\n\nc"},
	}
	for _, tt := range tests {
		got := BuildEmbedInput(tt.docKey, tt.heading, tt.content)
		if got != tt.want {
			t.Errorf("BuildEmbedInput(%q, %q, %q) = %q, want %q", tt.docKey, tt.heading, tt.content, got, tt.want)
		}
	}
}

func TestPrepareChunks_Basic(t *testing.T) {
	t.Parallel()
	text := "This is a simple text chunk for testing purposes."
	chunks := PrepareChunks(text, "test.txt", 512, 0.15)
	if len(chunks) == 0 {
		t.Fatal("expected at least one chunk")
	}
	for i, c := range chunks {
		if c.Index != i {
			t.Errorf("chunk %d has Index=%d, want %d", i, c.Index, i)
		}
	}
}

func TestPrepareChunks_Empty(t *testing.T) {
	t.Parallel()
	chunks := PrepareChunks("", "empty.txt", 512, 0.15)
	if chunks != nil {
		t.Fatalf("expected nil for empty text, got %d chunks", len(chunks))
	}
}

func TestPrepareChunks_WhitespaceOnly(t *testing.T) {
	t.Parallel()
	chunks := PrepareChunks("   \n\n  \t  ", "ws.txt", 512, 0.15)
	if chunks != nil {
		t.Fatalf("expected nil for whitespace text, got %d chunks", len(chunks))
	}
}

func TestPrepareChunks_IndicesAreSequential(t *testing.T) {
	t.Parallel()
	longText := strings.Repeat("word ", 800)
	chunks := PrepareChunks(longText, "long.txt", 512, 0.15)
	if len(chunks) < 2 {
		t.Fatalf("expected at least 2 chunks for long text, got %d", len(chunks))
	}
	for i, c := range chunks {
		if c.Index != i {
			t.Errorf("chunk at position %d has Index=%d", i, c.Index)
		}
	}
}

func TestPrepareChunks_RespectsEmbeddingBudget(t *testing.T) {
	t.Parallel()
	text := "# Heading\n\n" + strings.Repeat("alpha beta gamma delta ", 500)
	chunks := PrepareChunks(text, "notes.md", 10_000, 0)
	if len(chunks) < 2 {
		t.Fatalf("expected oversized chunk to be split for embedding budget, got %d chunk(s)", len(chunks))
	}
	for i, c := range chunks {
		input := BuildEmbedInput("doc", c.Heading, c.Content)
		if len([]rune(input)) > embed.MaxInputRunes {
			t.Fatalf("chunk %d exceeds embedding budget: %d", i, len([]rune(input)))
		}
	}
}

func TestSplitChunkForEmbeddingBudget_FitsInBudget(t *testing.T) {
	t.Parallel()
	c := chunk.Chunk{
		Content: "short content",
		Heading: "Title",
	}
	parts := splitChunkForEmbeddingBudget(c)
	if len(parts) != 1 {
		t.Fatalf("expected 1 part for short content, got %d", len(parts))
	}
	if parts[0].Content != "short content" {
		t.Fatalf("expected content 'short content', got %q", parts[0].Content)
	}
	if parts[0].Heading != "Title" {
		t.Fatalf("expected heading 'Title', got %q", parts[0].Heading)
	}
}

func TestSplitChunkForEmbeddingBudget_LargeContent(t *testing.T) {
	t.Parallel()
	content := strings.Repeat("word ", (embed.MaxInputRunes/4)+5)
	c := chunk.Chunk{
		Content: content,
		Heading: "",
	}
	parts := splitChunkForEmbeddingBudget(c)
	if len(parts) < 2 {
		t.Fatalf("expected multiple parts for large content, got %d", len(parts))
	}
}

func TestSplitChunkForEmbeddingBudget_LargeContentWithHeading(t *testing.T) {
	t.Parallel()
	content := strings.Repeat("word ", (embed.MaxInputRunes/4)+5)
	c := chunk.Chunk{
		Content: content,
		Heading: "A Very Long Heading That Takes Budget",
	}
	parts := splitChunkForEmbeddingBudget(c)
	if len(parts) < 2 {
		t.Fatalf("expected multiple parts for large content with heading, got %d", len(parts))
	}
	for _, p := range parts {
		if p.Heading != c.Heading {
			t.Fatalf("expected heading %q on split part, got %q", c.Heading, p.Heading)
		}
	}
}

func TestCodeSignature_Basic(t *testing.T) {
	t.Parallel()
	tests := []struct {
		input string
		want  string
	}{
		{"func main() {}", "func main() {}"},
		{"\n  \nfunc main() {", "func main() {"},
		{"", ""},
		{"\n\n", ""},
		{strings.Repeat("x", 150), strings.Repeat("x", 120) + "..."},
		{"func Foo()", "func Foo()"},
	}
	for _, tt := range tests {
		got := CodeSignature(tt.input)
		if got != tt.want {
			t.Errorf("CodeSignature(%q) = %q, want %q", tt.input, got, tt.want)
		}
	}
}

func TestEmbedContentBudget(t *testing.T) {
	t.Parallel()
	budgetNoHeading := embedContentBudget("")
	if budgetNoHeading != embed.MaxInputRunes {
		t.Fatalf("expected budget %d for no heading, got %d", embed.MaxInputRunes, budgetNoHeading)
	}
	budgetWithHeading := embedContentBudget("Short")
	expectedBudget := embed.MaxInputRunes - utf8.RuneCountInString("Short") - 2
	if budgetWithHeading != expectedBudget {
		t.Fatalf("expected budget %d for heading, got %d", expectedBudget, budgetWithHeading)
	}
}
