package ingest

import (
	"context"
	"fmt"
	"math"
	"strings"
	"unicode/utf8"

	"github.com/koltyakov/quant/internal/chunk"
	"github.com/koltyakov/quant/internal/embed"
	"github.com/koltyakov/quant/internal/index"
	"github.com/koltyakov/quant/internal/logx"
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
				Content:      c.Content,
				ChunkIndex:   c.Index,
				Embedding:    existingRecord.Embedding,
				Depth:        c.Depth,
				SectionTitle: c.SectionTitle,
				Summary:      existingRecord.Summary,
			})
		} else if p.DedupStore != nil {
			if embedding, found := p.DedupStore.LookupContentDedup(ctx, key); found {
				records = append(records, index.ChunkRecord{
					Content:      c.Content,
					ChunkIndex:   c.Index,
					Embedding:    embedding,
					Depth:        c.Depth,
					SectionTitle: c.SectionTitle,
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

func (p *Pipeline) EmbedChunks(ctx context.Context, toEmbed []chunk.Chunk, positions []PendingEmbed, records []index.ChunkRecord) error {
	if len(toEmbed) == 0 {
		return nil
	}
	if len(positions) != len(toEmbed) {
		return fmt.Errorf("embedding %d chunks with %d positions", len(toEmbed), len(positions))
	}
	for _, position := range positions {
		if position.ChunkIdx < 0 || position.ChunkIdx >= len(records) {
			return fmt.Errorf("embedding chunk position %d outside %d records", position.ChunkIdx, len(records))
		}
	}
	if p.Embedder == nil {
		// No embedding backend available; chunks are stored keyword-searchable only.
		for _, c := range toEmbed {
			globalIdx := positions[0].ChunkIdx
			positions = positions[1:]
			records[globalIdx] = index.ChunkRecord{
				Content:      c.Content,
				ChunkIndex:   c.Index,
				Embedding:    []byte{},
				Depth:        c.Depth,
				SectionTitle: c.SectionTitle,
			}
		}
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
			batchEnd := min(batchStart+batchSize, len(toEmbed))
			batch := toEmbed[batchStart:batchEnd]
			texts := make([]string, len(batch))
			for i, c := range batch {
				texts[i] = BuildEmbedInput(c.Heading, c.Content)
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
		expectedDimensions := p.Embedder.Dimensions()
		for i, vector := range result.embeddings {
			if len(vector) == 0 {
				return fmt.Errorf("embedding chunk %d: embedder returned an empty vector", result.batchStart+i)
			}
			if expectedDimensions > 0 && len(vector) != expectedDimensions {
				return fmt.Errorf("embedding chunk %d: vector has %d dimensions, want %d", result.batchStart+i, len(vector), expectedDimensions)
			}
			for _, value := range vector {
				if math.IsNaN(float64(value)) || math.IsInf(float64(value), 0) {
					return fmt.Errorf("embedding chunk %d: vector contains a non-finite value", result.batchStart+i)
				}
			}
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
				// Summaries are best-effort; indexing proceeds without them.
				logx.Warn("chunk summarization failed", "chunks", len(contents), "err", sumErr)
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

func BuildEmbedInput(heading, content string) string {
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
	contentBudget := max(embedContentBudget(c.Heading), 1)
	if utf8.RuneCountInString(BuildEmbedInput(c.Heading, c.Content)) <= embed.MaxInputRunes {
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
		part := c
		part.Content = piece
		parts = append(parts, part)
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
	for line := range strings.SplitSeq(block, "\n") {
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
