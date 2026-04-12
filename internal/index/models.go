package index

import (
	"context"
	"time"
)

// Document represents an indexed document in the store.
type Document struct {
	ID         int64
	Path       string
	Hash       string
	ModifiedAt time.Time
	IndexedAt  time.Time
}

// ChunkRecord represents a chunk of content with its embedding.
type ChunkRecord struct {
	ID         int64
	DocumentID int64
	Content    string
	ChunkIndex int
	Embedding  []byte
}

// EmbeddingMetadata stores information about the embedding model configuration.
type EmbeddingMetadata struct {
	Model      string
	Dimensions int
	Normalized bool
}

// FTSDiagnostics describes the logical and physical state of the FTS index.
type FTSDiagnostics struct {
	LogicalRows int  `json:"logical_rows"`
	DataRows    int  `json:"data_rows"`
	IdxRows     int  `json:"idx_rows"`
	Empty       bool `json:"empty"`
}

// SearchResult represents a search result with scoring information.
type SearchResult struct {
	DocumentPath string
	ChunkContent string
	ChunkIndex   int
	Score        float32
	ScoreKind    string
	ChunkID      int64
}

// -----------------------------------------------------------------------------
// Repository Interfaces
// -----------------------------------------------------------------------------

// DocumentRepository provides read operations for documents.
type DocumentRepository interface {
	GetDocumentByPath(ctx context.Context, path string) (*Document, error)
	ListDocuments(ctx context.Context) ([]Document, error)
	ListDocumentsLimit(ctx context.Context, limit int) ([]Document, error)
}

// ChunkRepository provides read operations for chunks.
type ChunkRepository interface {
	GetChunkByID(ctx context.Context, chunkID int64) (*SearchResult, error)
	GetDocumentChunksByPath(ctx context.Context, path string) (map[string]ChunkRecord, error)
}

// EmbeddingMetadataRepository manages embedding model metadata.
type EmbeddingMetadataRepository interface {
	EnsureEmbeddingMetadata(ctx context.Context, meta EmbeddingMetadata) (rebuild bool, err error)
}

// StatsProvider provides index statistics.
type StatsProvider interface {
	Stats(ctx context.Context) (docCount int, chunkCount int, err error)
}

// FTSDiagnosticsProvider exposes FTS logical/physical diagnostics.
type FTSDiagnosticsProvider interface {
	FTSDiagnostics(ctx context.Context) (FTSDiagnostics, error)
}

// HealthProvider provides health check capabilities.
type HealthProvider interface {
	PingContext(ctx context.Context) error
}

// -----------------------------------------------------------------------------
// Composite Interfaces
// -----------------------------------------------------------------------------

// Searcher provides search capabilities over the index.
type Searcher interface {
	Search(ctx context.Context, query string, queryEmbedding []float32, limit int, pathPrefix string) ([]SearchResult, error)
	FindSimilar(ctx context.Context, chunkID int64, limit int) ([]SearchResult, error)
	ChunkRepository
	DocumentRepository
	StatsProvider
	HealthProvider
}

// DocumentWriter provides write operations for documents and chunks.
type DocumentWriter interface {
	ReindexDocument(ctx context.Context, doc *Document, chunks []ChunkRecord) error
	DeleteDocument(ctx context.Context, path string) error
	DeleteDocumentsByPrefix(ctx context.Context, prefix string) error
	RenameDocumentPath(ctx context.Context, oldPath, newPath string) error
	DocumentRepository
	ChunkRepository
	StatsProvider
}

// HNSWBuilder provides HNSW index management capabilities.
type HNSWBuilder interface {
	HNSWReady() bool
	BuildHNSW(ctx context.Context) error
	RemoveBackup()
	LoadHNSWFromState(ctx context.Context) bool
}

// Store combines all repository interfaces for a complete index store.
// This interface is useful for dependency injection in tests.
type StoreInterface interface {
	Searcher
	DocumentWriter
	HNSWBuilder
	EmbeddingMetadataRepository
	Close() error
}
