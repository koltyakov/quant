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
	FileType   string            `json:"file_type"`
	Language   string            `json:"language"`
	Title      string            `json:"title"`
	Tags       map[string]string `json:"tags,omitempty"`
	Collection string            `json:"collection,omitempty"`
}

// ChunkRecord represents a chunk of content with its embedding.
type ChunkRecord struct {
	ID           int64  `json:"id"`
	DocumentID   int64  `json:"document_id"`
	Content      string `json:"content"`
	ChunkIndex   int    `json:"chunk_index"`
	Embedding    []byte `json:"-"`
	ParentID     *int64 `json:"parent_id,omitempty"`
	Depth        int    `json:"depth"`
	SectionTitle string `json:"section_title,omitempty"`
	Summary      string `json:"summary,omitempty"`
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
	DocumentPath  string
	ChunkContent  string
	ChunkIndex    int
	Score         float32
	ScoreKind     string
	ChunkID       int64
	ParentID      *int64 `json:"parent_id,omitempty"`
	Depth         int    `json:"depth"`
	SectionTitle  string `json:"section_title,omitempty"`
	ParentContext string `json:"parent_context,omitempty"`
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

// QuarantineEntry represents a quarantined path in the skip list.
type QuarantineEntry struct {
	Path      string
	ErrorMsg  string
	CreatedAt time.Time
	Attempts  int
}

// QuarantineRepository manages the non-destructive skip list.
type QuarantineRepository interface {
	AddToQuarantine(ctx context.Context, path, errMsg string) error
	RemoveFromQuarantine(ctx context.Context, path string) error
	IsQuarantined(ctx context.Context, path string) (bool, error)
	ListQuarantined(ctx context.Context) ([]QuarantineEntry, error)
	ClearQuarantine(ctx context.Context) error
}

// FTSDiagnosticsProvider exposes FTS logical/physical diagnostics.
type FTSDiagnosticsProvider interface {
	FTSDiagnostics(ctx context.Context) (FTSDiagnostics, error)
}

// CollectionRepository provides collection management operations.
type CollectionRepository interface {
	ListCollections(ctx context.Context) ([]string, error)
	CollectionStats(ctx context.Context, collection string) (docCount int, chunkCount int, err error)
	DeleteCollection(ctx context.Context, collection string) error
}

// HealthProvider provides health check capabilities.
type HealthProvider interface {
	PingContext(ctx context.Context) error
}

// -----------------------------------------------------------------------------
// Composite Interfaces
// -----------------------------------------------------------------------------

// Searcher provides search capabilities over the index.
type SearchFilter struct {
	FileTypes  []string
	Languages  []string
	Tags       map[string]string
	Collection string
}

type Searcher interface {
	Search(ctx context.Context, query string, queryEmbedding []float32, limit int, pathPrefix string) ([]SearchResult, error)
	SearchFiltered(ctx context.Context, query string, queryEmbedding []float32, limit int, pathPrefix string, filter SearchFilter) ([]SearchResult, error)
	FindSimilar(ctx context.Context, chunkID int64, limit int) ([]SearchResult, error)
	ChunkRepository
	DocumentRepository
	StatsProvider
	HealthProvider
	CollectionRepository
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
	FlushHNSW() error
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
