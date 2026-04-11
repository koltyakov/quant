package index

import (
	"context"
	"encoding/binary"
	"fmt"
	"math"
	"sync"
	"sync/atomic"

	"github.com/coder/hnsw"
	"github.com/koltyakov/quant/internal/logx"
)

const (
	hnswM        = 16  // max neighbors per node
	hnswEfSearch = 100 // ef during search (>95% recall typical)
)

// hnswIndex manages an in-memory HNSW approximate nearest-neighbor graph.
// It is rebuilt from SQLite on startup and updated incrementally on chunk add/delete.
type hnswIndex struct {
	mu    sync.RWMutex
	graph *hnsw.Graph[int]
	ready atomic.Bool
}

func newHNSWIndex() *hnswIndex {
	return &hnswIndex{}
}

// Add inserts or replaces a chunk vector in the graph.
func (h *hnswIndex) Add(id int, vec []float32) {
	if !h.ready.Load() {
		return
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	h.graph.Add(hnsw.MakeNode(id, vec))
}

func (h *hnswIndex) Delete(id int) {
	if !h.ready.Load() {
		return
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	h.graph.Delete(id)
}

// Search performs an approximate k-nearest-neighbor query.
// Returns chunk IDs. Returns nil if the graph is not ready.
func (h *hnswIndex) Search(query []float32, k int) []int {
	if !h.ready.Load() {
		return nil
	}
	h.mu.RLock()
	defer h.mu.RUnlock()
	nodes := h.graph.Search(query, k)
	ids := make([]int, len(nodes))
	for i, n := range nodes {
		ids[i] = n.Key
	}
	return ids
}

// Len returns the number of nodes in the graph.
func (h *hnswIndex) Len() int {
	if !h.ready.Load() {
		return 0
	}
	h.mu.RLock()
	defer h.mu.RUnlock()
	return h.graph.Len()
}

// BuildHNSW loads all embeddings from the database and builds the in-memory HNSW graph.
// Call this after the initial index sync completes.
func (s *Store) BuildHNSW(ctx context.Context) error {
	if s.hnsw == nil {
		return nil
	}

	meta, err := s.embeddingMetadata(ctx)
	if err != nil {
		return fmt.Errorf("reading embedding metadata for hnsw build: %w", err)
	}
	if meta == nil || meta.Dimensions == 0 {
		return nil
	}
	dims := meta.Dimensions

	rows, err := s.db.QueryContext(ctx, `SELECT id, embedding FROM chunks`)
	if err != nil {
		return fmt.Errorf("querying chunks for hnsw build: %w", err)
	}
	defer func() { _ = rows.Close() }()

	g := hnsw.NewGraph[int]()
	g.M = hnswM
	g.EfSearch = hnswEfSearch
	g.Distance = hnsw.CosineDistance

	count := 0
	for rows.Next() {
		var id int
		var embBytes []byte
		if err := rows.Scan(&id, &embBytes); err != nil {
			return fmt.Errorf("scanning chunk for hnsw: %w", err)
		}
		vec := decodeEmbeddingForHNSW(embBytes, dims)
		if len(vec) == 0 {
			continue
		}
		g.Add(hnsw.MakeNode(id, vec))
		count++
	}
	if err := rows.Err(); err != nil {
		return fmt.Errorf("reading chunks for hnsw: %w", err)
	}

	s.hnsw.mu.Lock()
	s.hnsw.graph = g
	s.hnsw.mu.Unlock()
	s.hnsw.ready.Store(true)
	logx.Info("hnsw graph built", "chunks", count)

	return nil
}

// HNSWReady reports whether the HNSW graph is built and ready for queries.
func (s *Store) HNSWReady() bool {
	return s.hnsw != nil && s.hnsw.ready.Load()
}

// HNSWLen returns the number of nodes in the HNSW graph, or 0 if not ready.
func (s *Store) HNSWLen() int {
	if s.hnsw == nil {
		return 0
	}
	return s.hnsw.Len()
}

// hnswAdd adds a chunk vector to the index (incremental update after insert).
func (s *Store) hnswAdd(id int, vec []float32) {
	if s.hnsw != nil {
		s.hnsw.Add(id, vec)
	}
}

// hnswDeleteChunks removes all chunks belonging to a document from the HNSW graph.
func (s *Store) hnswDeleteChunks(ctx context.Context, docID int64) {
	if s.hnsw == nil || !s.hnsw.ready.Load() {
		return
	}
	rows, err := s.db.QueryContext(ctx, `SELECT id FROM chunks WHERE document_id = ?`, docID)
	if err != nil {
		return
	}
	defer func() { _ = rows.Close() }()
	for rows.Next() {
		var id int
		if err := rows.Scan(&id); err != nil {
			return
		}
		s.hnsw.Delete(id)
	}
}

// decodeEmbeddingForHNSW converts a stored embedding blob to []float32 for use in HNSW.
// Supports both float32 (dims*4 bytes) and int8 quantized (8+dims bytes) formats.
func decodeEmbeddingForHNSW(data []byte, dims int) []float32 {
	switch len(data) {
	case dims * 4:
		return decodeFloat32(data)
	case 8 + dims:
		minVal := math.Float32frombits(binary.LittleEndian.Uint32(data[0:]))
		scale := math.Float32frombits(binary.LittleEndian.Uint32(data[4:]))
		vec := make([]float32, dims)
		for i := range vec {
			vec[i] = float32(data[8+i])*scale + minVal
		}
		return vec
	default:
		return nil
	}
}
