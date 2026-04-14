package proxy

import (
	"time"

	"github.com/koltyakov/quant/internal/index"
)

type SearchRequest struct {
	Query          string             `json:"query"`
	QueryEmbedding []float32          `json:"query_embedding"`
	Limit          int                `json:"limit"`
	PathPrefix     string             `json:"path_prefix"`
	Filter         index.SearchFilter `json:"filter"`
}

type SearchResponse struct {
	Results []index.SearchResult `json:"results"`
	Error   string               `json:"error,omitempty"`
}

type FindSimilarRequest struct {
	ChunkID int64 `json:"chunk_id"`
	Limit   int   `json:"limit"`
}

type FindSimilarResponse struct {
	Results []index.SearchResult `json:"results"`
	Error   string               `json:"error,omitempty"`
}

type ListSourcesRequest struct {
	Limit int `json:"limit"`
}

type ListSourcesResponse struct {
	Documents []index.Document `json:"documents"`
	Total     int              `json:"total"`
	Error     string           `json:"error,omitempty"`
}

type IndexStatusResponse struct {
	Documents       int                   `json:"documents"`
	Chunks          int                   `json:"chunks"`
	EmbeddingStatus string                `json:"embedding_status,omitempty"`
	FTS             *index.FTSDiagnostics `json:"fts,omitempty"`
	State           string                `json:"state,omitempty"`
	StateMessage    string                `json:"state_message,omitempty"`
	StateUpdatedAt  time.Time             `json:"state_updated_at,omitempty"`
}

type PingResponse struct {
	Status string `json:"status"`
}

type ChunkByIDRequest struct {
	ChunkID int64 `json:"chunk_id"`
}

type ChunkByIDResponse struct {
	Chunk index.SearchResult `json:"chunk"`
	Error string             `json:"error,omitempty"`
}

type StatsResponse struct {
	DocCount   int    `json:"doc_count"`
	ChunkCount int    `json:"chunk_count"`
	Error      string `json:"error,omitempty"`
}

type EmbedRequest struct {
	Text string `json:"text"`
}

type EmbedResponse struct {
	Embedding []float32 `json:"embedding"`
	Error     string    `json:"error,omitempty"`
}

type ListCollectionsResponse struct {
	Collections []string `json:"collections"`
	Error       string   `json:"error,omitempty"`
}

type CollectionStatsRequest struct {
	Collection string `json:"collection"`
}

type CollectionStatsResponse struct {
	Documents int    `json:"documents"`
	Chunks    int    `json:"chunks"`
	Error     string `json:"error,omitempty"`
}

type DeleteCollectionRequest struct {
	Collection string `json:"collection"`
}

type DeleteCollectionResponse struct {
	Deleted bool   `json:"deleted"`
	Error   string `json:"error,omitempty"`
}
