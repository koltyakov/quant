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
	ChunkID      int64
}

type Searcher interface {
	Search(ctx context.Context, query string, queryEmbedding []float32, limit int, pathPrefix string) ([]SearchResult, error)
	FindSimilar(ctx context.Context, chunkID int64, limit int) ([]SearchResult, error)
	GetChunkByID(ctx context.Context, chunkID int64) (*SearchResult, error)
	ListDocumentsLimit(ctx context.Context, limit int) ([]Document, error)
	Stats(ctx context.Context) (docCount int, chunkCount int, err error)
	PingContext(ctx context.Context) error
}

type DocumentWriter interface {
	ReindexDocument(ctx context.Context, doc *Document, chunks []ChunkRecord) error
	DeleteDocument(ctx context.Context, path string) error
	DeleteDocumentsByPrefix(ctx context.Context, prefix string) error
	GetDocumentByPath(ctx context.Context, path string) (*Document, error)
	GetDocumentChunksByPath(ctx context.Context, path string) (map[string]ChunkRecord, error)
	ListDocuments(ctx context.Context) ([]Document, error)
	RenameDocumentPath(ctx context.Context, oldPath, newPath string) error
	Stats(ctx context.Context) (docCount int, chunkCount int, err error)
}

type HNSWBuilder interface {
	HNSWReady() bool
	BuildHNSW(ctx context.Context) error
	RemoveBackup()
	LoadHNSWFromState(ctx context.Context) bool
}
