package index

import (
	"context"
	"time"
)

type Document struct {
	ID         int64
	Path       string
	Hash       string
	ModifiedAt time.Time
	IndexedAt  time.Time
}

type ChunkRecord struct {
	ID         int64
	DocumentID int64
	Content    string
	ChunkIndex int
	Embedding  []byte
}

type EmbeddingMetadata struct {
	Model      string
	Dimensions int
	Normalized bool
}

type SearchResult struct {
	DocumentPath string
	ChunkContent string
	ChunkIndex   int
	Score        float32
	ScoreKind    string
}

type Searcher interface {
	Search(ctx context.Context, query string, queryEmbedding []float32, limit int, pathPrefix string) ([]SearchResult, error)
	ListDocumentsLimit(ctx context.Context, limit int) ([]Document, error)
	Stats(ctx context.Context) (docCount int, chunkCount int, err error)
	PingContext(ctx context.Context) error
}
