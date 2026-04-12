package index

import (
	"context"
	"fmt"
)

// Retriever owns retrieval policy and ranking while Store remains focused on
// persistence concerns.
type Retriever struct {
	store *Store
}

func NewRetriever(store *Store) *Retriever {
	return &Retriever{store: store}
}

func (r *Retriever) Search(ctx context.Context, query string, queryEmbedding []float32, limit int, pathPrefix string) ([]SearchResult, error) {
	if limit <= 0 {
		return nil, nil
	}

	andQuery, orQuery, nearQuery := buildFTSQueries(query)
	keywordCandidates := make(map[int]*searchCandidate)
	rankOffset := 0
	candidateLimit := searchCandidateLimit(limit)

	if andQuery != "" {
		collected, err := r.store.collectFTSCandidates(ctx, andQuery, queryEmbedding, candidateLimit, pathPrefix, rankOffset, keywordCandidates)
		if err != nil {
			return nil, err
		}
		rankOffset += collected

		if len(keywordCandidates) >= candidateLimit {
			orQuery = ""
			nearQuery = ""
		}
	}

	if orQuery != "" && orQuery != andQuery {
		collected, err := r.store.collectFTSCandidates(ctx, orQuery, queryEmbedding, candidateLimit, pathPrefix, rankOffset, keywordCandidates)
		if err != nil {
			return nil, err
		}
		rankOffset += collected
	}

	if nearQuery != "" {
		_, err := r.store.collectFTSCandidates(ctx, nearQuery, queryEmbedding, candidateLimit, pathPrefix, rankOffset, keywordCandidates)
		if err != nil {
			return nil, err
		}
	}

	var vectorOnlyCandidates map[int]*searchCandidate
	if queryEmbedding != nil {
		var err error
		vectorOnlyCandidates, err = r.store.collectVectorCandidates(ctx, queryEmbedding, limit, pathPrefix, keywordCandidates)
		if err != nil {
			return nil, err
		}
	}

	weights := classifyQueryWeights(query, r.store.keywordWeightOverride, r.store.vectorWeightOverride)
	return unifiedRRF(keywordCandidates, vectorOnlyCandidates, limit, pathQueryTokens(query), weights), nil
}

func (r *Retriever) FindSimilar(ctx context.Context, chunkID int64, limit int) ([]SearchResult, error) {
	if limit <= 0 {
		return nil, nil
	}

	var embeddingBytes []byte
	if err := r.store.db.QueryRowContext(ctx, `SELECT embedding FROM chunks WHERE id = ?`, chunkID).Scan(&embeddingBytes); err != nil {
		return nil, fmt.Errorf("loading chunk %d: %w", chunkID, err)
	}

	meta, err := r.store.embeddingMetadata(ctx)
	if err != nil {
		return nil, err
	}
	if meta == nil || meta.Dimensions == 0 {
		return nil, nil
	}

	vec := decodeEmbeddingForHNSW(embeddingBytes, meta.Dimensions)
	if len(vec) == 0 {
		return nil, nil
	}

	queryEmbed := NormalizeFloat32(vec)
	vectorOnly := make(map[int]*searchCandidate)

	if r.store.hnsw != nil && r.store.hnsw.ready.Load() {
		fetchK := limit + 1
		ids := r.store.hnsw.Search(queryEmbed, fetchK)
		filtered := make([]int, 0, len(ids))
		for _, id := range ids {
			if id != int(chunkID) {
				filtered = append(filtered, id)
			}
		}
		if len(filtered) > 0 {
			r.store.loadHNSWChunkRows(ctx, filtered, queryEmbed, limit, nil, vectorOnly)
		}
	}

	candidates := mergeCandidates(nil, vectorOnly)
	for i := range candidates {
		if candidates[i].result.ChunkID == chunkID {
			candidates = append(candidates[:i], candidates[i+1:]...)
			break
		}
	}

	results := make([]SearchResult, 0, min(len(candidates), limit))
	for i, c := range candidates {
		if i >= limit {
			break
		}
		c.result.ScoreKind = "similar"
		results = append(results, c.result)
	}
	return results, nil
}
