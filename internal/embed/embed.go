package embed

import "context"

// Embedder produces vector embeddings from text using an external model.
type Embedder interface {
	Embed(ctx context.Context, text string) ([]float32, error)
	EmbedBatch(ctx context.Context, texts []string) ([][]float32, error)
	Dimensions() int
	Close() error
}
