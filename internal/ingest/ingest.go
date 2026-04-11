package ingest

import (
	"context"
	"fmt"
	"strings"

	"github.com/koltyakov/quant/internal/chunk"
	"github.com/koltyakov/quant/internal/embed"
	"github.com/koltyakov/quant/internal/extract"
	"github.com/koltyakov/quant/internal/index"
)

type Pipeline struct {
	Extractor extract.Extractor
	Embedder  embed.Embedder
	Store     index.DocumentWriter
	ChunkSize int
	Overlap   float64
	BatchSize int
}

type Result struct {
	Chunks   int
	Reused   int
	Embedded int
}

func (p *Pipeline) Process(ctx context.Context, docKey, filePath string, existingChunks map[string]index.ChunkRecord) (*index.Document, []index.ChunkRecord, error) {
	text, err := p.Extractor.Extract(ctx, filePath)
	if err != nil {
		return nil, nil, fmt.Errorf("extracting text: %w", err)
	}

	if text == "" {
		return nil, nil, nil
	}

	chunks := chunk.SplitWithPath(text, filePath, p.ChunkSize, p.Overlap)
	if len(chunks) == 0 {
		return nil, nil, nil
	}

	records, toEmbed, embedPositions := p.diffChunks(chunks, existingChunks)

	if err := p.embedChunks(ctx, docKey, toEmbed, embedPositions, records); err != nil {
		return nil, nil, err
	}

	return &index.Document{Path: docKey}, records, nil
}

type pendingEmbed struct {
	chunkIdx int
	batchPos int
}

func (p *Pipeline) diffChunks(chunks []chunk.Chunk, existing map[string]index.ChunkRecord) ([]index.ChunkRecord, []chunk.Chunk, []pendingEmbed) {
	records := make([]index.ChunkRecord, 0, len(chunks))
	var toEmbed []chunk.Chunk
	var positions []pendingEmbed

	for i, c := range chunks {
		key := index.ChunkDiffKey(c.Content)
		if existing, ok := existing[key]; ok {
			records = append(records, index.ChunkRecord{
				Content:    c.Content,
				ChunkIndex: c.Index,
				Embedding:  existing.Embedding,
			})
		} else {
			positions = append(positions, pendingEmbed{chunkIdx: i, batchPos: len(toEmbed)})
			toEmbed = append(toEmbed, c)
			records = append(records, index.ChunkRecord{})
		}
	}
	return records, toEmbed, positions
}

func (p *Pipeline) embedChunks(ctx context.Context, docKey string, toEmbed []chunk.Chunk, positions []pendingEmbed, records []index.ChunkRecord) error {
	if len(toEmbed) == 0 {
		return nil
	}

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
				texts[i] = buildEmbedInput(docKey, c.Heading, c.Content)
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
		for i, c := range batch {
			globalIdx := positions[result.batchStart+i].chunkIdx
			records[globalIdx] = index.ChunkRecord{
				Content:    c.Content,
				ChunkIndex: c.Index,
				Embedding:  index.EncodeInt8(index.NormalizeFloat32(result.embeddings[i])),
			}
		}
	}
	return ctx.Err()
}

func buildEmbedInput(docKey, heading string, content string) string {
	if heading != "" {
		return heading + "\n\n" + content
	}
	return content
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
