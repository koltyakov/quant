package index

import (
	"context"
	"encoding/binary"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"

	"github.com/coder/hnsw"
	"github.com/koltyakov/quant/internal/logx"
)

const (
	hnswM         = 16  // max neighbors per node
	hnswEfSearch  = 100 // ef during search (>95% recall typical)
	hnswMinChunks = 500 // below this, brute-force is fine
)

// hnswIndex manages a persistent, in-memory HNSW approximate nearest-neighbor graph.
// It is built lazily after initial sync and updated incrementally on chunk add/delete.
type hnswIndex struct {
	mu    sync.RWMutex
	graph *hnsw.Graph[int]
	path  string
	ready atomic.Bool
}

func newHNSWIndex(dbPath string) *hnswIndex {
	return &hnswIndex{
		path: dbPath + ".hnsw",
	}
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

// Delete removes a chunk from the graph.
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

// load restores a previously persisted graph from the sidecar file.
// Returns false if the file does not exist.
func (h *hnswIndex) load() (bool, error) {
	f, err := os.Open(h.path)
	if err != nil {
		if os.IsNotExist(err) {
			return false, nil
		}
		return false, fmt.Errorf("opening hnsw sidecar: %w", err)
	}
	defer func() { _ = f.Close() }()

	g := hnsw.NewGraph[int]()
	g.M = hnswM
	g.EfSearch = hnswEfSearch
	if err := g.Import(f); err != nil {
		return false, fmt.Errorf("importing hnsw graph: %w", err)
	}
	h.mu.Lock()
	h.graph = g
	h.mu.Unlock()
	h.ready.Store(true)
	return true, nil
}

// persist writes the current graph to the sidecar file atomically.
func (h *hnswIndex) persist() error {
	if !h.ready.Load() {
		return nil
	}
	dir := filepath.Dir(h.path)
	if err := os.MkdirAll(dir, 0750); err != nil {
		return fmt.Errorf("creating hnsw directory: %w", err)
	}
	f, err := os.CreateTemp(dir, ".hnsw-*.tmp")
	if err != nil {
		return fmt.Errorf("creating hnsw temp file: %w", err)
	}
	tmpPath := f.Name()

	h.mu.RLock()
	writeErr := h.graph.Export(f)
	h.mu.RUnlock()

	_ = f.Close()
	if writeErr != nil {
		_ = os.Remove(tmpPath)
		return fmt.Errorf("exporting hnsw graph: %w", writeErr)
	}
	if err := os.Rename(tmpPath, h.path); err != nil {
		_ = os.Remove(tmpPath)
		return fmt.Errorf("persisting hnsw graph: %w", err)
	}
	return nil
}

// BuildHNSW loads all embeddings from the database and builds the in-memory HNSW graph.
// Call this after the initial index sync completes. Skips if corpus is below hnswMinChunks.
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

	if count < hnswMinChunks {
		logx.Info("skipping hnsw build (corpus too small)", "chunks", count, "min", hnswMinChunks)
		return nil
	}

	s.hnsw.mu.Lock()
	s.hnsw.graph = g
	s.hnsw.mu.Unlock()
	s.hnsw.ready.Store(true)
	logx.Info("hnsw graph built", "chunks", count)

	if err := s.hnsw.persist(); err != nil {
		logx.Warn("failed to persist hnsw graph", "err", err)
	}
	return nil
}

// HNSWReady reports whether the HNSW graph is built and ready for queries.
func (s *Store) HNSWReady() bool {
	return s.hnsw != nil && s.hnsw.ready.Load()
}

// LoadHNSW attempts to restore a previously persisted HNSW graph.
// No-op if the sidecar file does not exist.
func (s *Store) LoadHNSW() {
	if s.hnsw == nil {
		return
	}
	ok, err := s.hnsw.load()
	if err != nil {
		logx.Warn("failed to load hnsw graph, will rebuild on next sync", "err", err)
		return
	}
	if ok {
		logx.Info("hnsw graph loaded from disk", "nodes", s.hnsw.Len())
	}
}

// hnswAdd adds a chunk vector to the index (incremental update after insert).
func (s *Store) hnswAdd(id int, vec []float32) {
	if s.hnsw != nil {
		s.hnsw.Add(id, vec)
	}
}

// searchVectorHNSW runs an ANN query via the HNSW graph and loads the chunk data
// for the returned IDs. It fetches more candidates than needed to account for
// exclusions, then returns the top-limit results by vector score.
func (s *Store) searchVectorHNSW(ctx context.Context, queryEmbedding []float32, limit int, exclude map[int]*searchCandidate) ([]SearchResult, error) {
	// Fetch extra candidates to account for excluded IDs.
	fetchK := limit + len(exclude) + 10
	ids := s.hnsw.Search(queryEmbedding, fetchK)
	if len(ids) == 0 {
		return nil, nil
	}

	// Build IN clause and load chunk data.
	placeholders := make([]string, len(ids))
	args := make([]any, len(ids))
	for i, id := range ids {
		placeholders[i] = "?"
		args[i] = id
	}
	//nolint:gosec // placeholders are all literal "?" — no user input in the query string
	query := `SELECT c.id, c.content, c.chunk_index, c.embedding, d.path
	          FROM chunks c JOIN documents d ON c.document_id = d.id
	          WHERE c.id IN (` + strings.Join(placeholders, ",") + `)`
	rows, err := s.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()

	return rerankByVector(rows, queryEmbedding, limit, exclude)
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
