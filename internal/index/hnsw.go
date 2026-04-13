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

func (h *hnswIndex) BatchDelete(ids []int) {
	if !h.ready.Load() || len(ids) == 0 {
		return
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	for _, id := range ids {
		h.graph.Delete(id)
	}
}

func (h *hnswIndex) BatchAdd(nodes []hnsw.Node[int]) {
	if !h.ready.Load() || len(nodes) == 0 {
		return
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	for _, node := range nodes {
		h.graph.Add(node)
	}
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

	if err := s.saveHNSWState(ctx, count); err != nil {
		logx.Warn("failed to persist hnsw metadata snapshot", "err", err)
	}

	return nil
}

func (s *Store) saveHNSWState(ctx context.Context, nodeCount int) error {
	meta, err := s.embeddingMetadata(ctx)
	if err != nil || meta == nil {
		return fmt.Errorf("reading embedding metadata for hnsw state: %w", err)
	}
	_, err = s.db.ExecContext(ctx,
		`INSERT INTO hnsw_state (id, built_at, node_count, model, dimensions) VALUES (1, CURRENT_TIMESTAMP, ?, ?, ?)
		 ON CONFLICT(id) DO UPDATE SET built_at = CURRENT_TIMESTAMP, node_count = ?, model = ?, dimensions = ?`,
		nodeCount, meta.Model, meta.Dimensions,
		nodeCount, meta.Model, meta.Dimensions,
	)
	return err
}

// LoadHNSWFromState reconstructs the in-memory graph from stored chunk
// embeddings after validating the recorded metadata snapshot.
// It does not deserialize a persisted HNSW graph structure.
func (s *Store) LoadHNSWFromState(ctx context.Context) bool {
	var nodeCount int
	var storedModel string
	var storedDims int
	err := s.db.QueryRowContext(ctx,
		`SELECT node_count, model, dimensions FROM hnsw_state WHERE id = 1`,
	).Scan(&nodeCount, &storedModel, &storedDims)
	if err != nil {
		return false
	}
	if nodeCount == 0 {
		return false
	}

	meta, err := s.embeddingMetadata(ctx)
	if err != nil || meta == nil || meta.Dimensions == 0 {
		return false
	}

	if storedModel != meta.Model || storedDims != meta.Dimensions {
		logx.Info("hnsw metadata snapshot mismatch, skipping graph reconstruction",
			"stored_model", storedModel, "current_model", meta.Model,
			"stored_dims", storedDims, "current_dims", meta.Dimensions)
		return false
	}

	var chunkCount int
	if err := s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM chunks`).Scan(&chunkCount); err != nil {
		return false
	}
	if chunkCount != nodeCount {
		return false
	}

	rows, err := s.db.QueryContext(ctx, `SELECT id, embedding FROM chunks`)
	if err != nil {
		return false
	}
	defer func() { _ = rows.Close() }()

	g := hnsw.NewGraph[int]()
	g.M = hnswM
	g.EfSearch = hnswEfSearch
	g.Distance = hnsw.CosineDistance

	loaded := 0
	for rows.Next() {
		var id int
		var embBytes []byte
		if err := rows.Scan(&id, &embBytes); err != nil {
			return false
		}
		vec := decodeEmbeddingForHNSW(embBytes, meta.Dimensions)
		if len(vec) == 0 {
			continue
		}
		g.Add(hnsw.MakeNode(id, vec))
		loaded++
	}
	if err := rows.Err(); err != nil {
		return false
	}

	s.hnsw.mu.Lock()
	s.hnsw.graph = g
	s.hnsw.mu.Unlock()
	s.hnsw.ready.Store(true)
	logx.Info("hnsw graph reconstructed from chunk embeddings using metadata snapshot", "chunks", loaded)
	return true
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
