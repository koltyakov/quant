package ingest

import (
	"context"
	"fmt"
	"strings"
	"unicode/utf8"

	"github.com/koltyakov/quant/internal/chunk"
	"github.com/koltyakov/quant/internal/embed"
	"github.com/koltyakov/quant/internal/index"
)

type ContentDedupStore interface {
	LookupContentDedup(ctx context.Context, contentHash string) ([]byte, bool)
	StoreContentDedup(ctx context.Context, contentHash string, embedding []byte) error
}

type Pipeline struct {
	Embedder   embed.Embedder
	ChunkSize  int
	Overlap    float64
	BatchSize  int
	DedupStore ContentDedupStore
	Summarizer ChunkSummarizer
}

type ChunkSummarizer interface {
	SummarizeBatch(ctx context.Context, contents []string) ([]*ChunkSummary, error)
}

type ChunkSummary struct {
	Summary string
	Topics  []string
}

type PendingEmbed struct {
	ChunkIdx int
	BatchPos int
}

func (p *Pipeline) DiffChunks(ctx context.Context, chunks []chunk.Chunk, existing map[string]index.ChunkRecord) ([]index.ChunkRecord, []chunk.Chunk, []PendingEmbed, error) {
	records := make([]index.ChunkRecord, 0, len(chunks))
	var toEmbed []chunk.Chunk
	var positions []PendingEmbed

	for i, c := range chunks {
		key := index.ChunkDiffKey(c.Content)
		if existingRecord, ok := existing[key]; ok {
			records = append(records, index.ChunkRecord{
				Content:    c.Content,
				ChunkIndex: c.Index,
				Embedding:  existingRecord.Embedding,
			})
		} else if p.DedupStore != nil {
			if embedding, found := p.DedupStore.LookupContentDedup(ctx, key); found {
				records = append(records, index.ChunkRecord{
					Content:    c.Content,
					ChunkIndex: c.Index,
					Embedding:  embedding,
				})
			} else {
				positions = append(positions, PendingEmbed{ChunkIdx: i, BatchPos: len(toEmbed)})
				toEmbed = append(toEmbed, c)
				records = append(records, index.ChunkRecord{})
			}
		} else {
			positions = append(positions, PendingEmbed{ChunkIdx: i, BatchPos: len(toEmbed)})
			toEmbed = append(toEmbed, c)
			records = append(records, index.ChunkRecord{})
		}
	}
	return records, toEmbed, positions, nil
}

func (p *Pipeline) EmbedChunks(ctx context.Context, docKey string, toEmbed []chunk.Chunk, positions []PendingEmbed, records []index.ChunkRecord) error {
	if len(toEmbed) == 0 {
		return nil
	}
	if p.Embedder == nil {
		// No embedding backend available; chunks are stored keyword-searchable only.
		return nil
	}

	// Cancel the producer goroutine if we return early (e.g. on error).
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()

	batchSize := p.BatchSize
	if batchSize < 1 {
		batchSize = 16
	}

	type batchResult struct {
		batchStart int
		batch      []chunk.Chunk
		embeddings [][]float32
		err        error
	}

	numBatches := (len(toEmbed) + batchSize - 1) / batchSize
	resultCh := make(chan batchResult, min(numBatches, 4))

	go func() {
		defer close(resultCh)
		for batchStart := 0; batchStart < len(toEmbed); batchStart += batchSize {
			batchEnd := batchStart + batchSize
			if batchEnd > len(toEmbed) {
				batchEnd = len(toEmbed)
			}
			batch := toEmbed[batchStart:batchEnd]
			texts := make([]string, len(batch))
			for i, c := range batch {
				texts[i] = BuildEmbedInput(docKey, c.Heading, c.Content)
			}
			embeddings, err := p.Embedder.EmbedBatch(ctx, texts)
			select {
			case <-ctx.Done():
				return
			case resultCh <- batchResult{batchStart: batchStart, batch: batch, embeddings: embeddings, err: err}:
			}
		}
	}()

	for result := range resultCh {
		if result.err != nil {
			return fmt.Errorf("embedding chunks from %d: %w", result.batchStart, result.err)
		}
		batch := result.batch
		if len(result.embeddings) != len(batch) {
			return fmt.Errorf(
				"embedding chunks %d-%d: embedder returned %d embeddings for %d chunks",
				result.batchStart, result.batchStart+len(batch)-1, len(result.embeddings), len(batch),
			)
		}

		var summaries []*ChunkSummary
		if p.Summarizer != nil {
			contents := make([]string, len(batch))
			for i, c := range batch {
				contents[i] = c.Content
			}
			var sumErr error
			summaries, sumErr = p.Summarizer.SummarizeBatch(ctx, contents)
			if sumErr != nil {
				// Log but don't fail indexing on summary errors
				summaries = nil
			}
		}

		for i, c := range batch {
			globalIdx := positions[result.batchStart+i].ChunkIdx
			emb := index.EncodeInt8(index.NormalizeFloat32(result.embeddings[i]))
			summary := ""
			if summaries != nil && i < len(summaries) && summaries[i] != nil {
				summary = summaries[i].Summary
			}
			records[globalIdx] = index.ChunkRecord{
				Content:      c.Content,
				ChunkIndex:   c.Index,
				Embedding:    emb,
				Depth:        c.Depth,
				SectionTitle: c.SectionTitle,
				Summary:      summary,
			}
			if p.DedupStore != nil {
				key := index.ChunkDiffKey(c.Content)
				_ = p.DedupStore.StoreContentDedup(ctx, key, emb)
			}
		}
	}
	return ctx.Err()
}

func BuildEmbedInput(docKey, heading string, content string) string {
	if heading != "" {
		return heading + "\n\n" + content
	}
	return content
}

func PrepareChunks(text, filePath string, chunkSize int, overlap float64) []chunk.Chunk {
	chunks := chunk.SplitWithPath(text, filePath, chunkSize, overlap)
	if len(chunks) == 0 {
		return nil
	}

	prepared := make([]chunk.Chunk, 0, len(chunks))
	for _, c := range chunks {
		prepared = append(prepared, splitChunkForEmbeddingBudget(c)...)
	}
	for i := range prepared {
		prepared[i].Index = i
	}
	return prepared
}

func splitChunkForEmbeddingBudget(c chunk.Chunk) []chunk.Chunk {
	contentBudget := embedContentBudget(c.Heading)
	if contentBudget < 1 {
		contentBudget = 1
	}
	if utf8.RuneCountInString(BuildEmbedInput("", c.Heading, c.Content)) <= embed.MaxInputRunes {
		return []chunk.Chunk{c}
	}

	remaining := strings.TrimSpace(c.Content)
	parts := make([]chunk.Chunk, 0, 2)
	for remaining != "" {
		piece, consumed := embed.PrefixWithinInputBudget(remaining, contentBudget)
		if piece == "" || consumed <= 0 {
			piece = embed.TruncateForInput(remaining, contentBudget)
			consumed = min(utf8.RuneCountInString(remaining), contentBudget)
		}
		parts = append(parts, chunk.Chunk{Content: piece, Heading: c.Heading})
		remainingRunes := []rune(remaining)
		if consumed >= len(remainingRunes) {
			break
		}
		remaining = strings.TrimSpace(string(remainingRunes[consumed:]))
	}
	return parts
}

func embedContentBudget(heading string) int {
	budget := embed.MaxInputRunes
	if heading != "" {
		budget -= utf8.RuneCountInString(heading) + 2
	}
	return budget
}

func CodeSignature(block string) string {
	for _, line := range strings.Split(block, "\n") {
		trimmed := strings.TrimSpace(line)
		if trimmed != "" {
			if len(trimmed) > 120 {
				return trimmed[:120] + "..."
			}
			return trimmed
		}
	}
	return ""
}
