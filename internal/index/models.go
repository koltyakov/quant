package index

import "time"

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
}
