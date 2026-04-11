package index

import (
	"context"
	"encoding/binary"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"time"

	"github.com/coder/hnsw"
	"github.com/koltyakov/quant/internal/logx"
)

const (
	hnswM                = 16  // max neighbors per node
	hnswEfSearch         = 100 // ef during search (>95% recall typical)
	hnswPersistMinChunks = 500 // below this, graph is in-memory only (not persisted to disk)
	hnswFlushDelayMs     = 500 // debounce window for batching HNSW disk flushes
)

// hnswIndex manages a persistent, in-memory HNSW approximate nearest-neighbor graph.
// It is built lazily after initial sync and updated incrementally on chunk add/delete.
type hnswIndex struct {
	mu         sync.RWMutex
	graph      *hnsw.Graph[int]
	path       string
	ready      atomic.Bool
	dirty      atomic.Bool
	flushMu    sync.Mutex
	flushTimer *time.Timer
}

func newHNSWIndex(dbPath string) *hnswIndex {
	dbPath = filepath.Clean(dbPath)
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
	h.dirty.Store(true)
}

func (h *hnswIndex) Delete(id int) {
	if !h.ready.Load() {
		return
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	h.graph.Delete(id)
	h.dirty.Store(true)
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
		//nolint:gosec // Temp file was created in the target sidecar directory for this specific index file.
		_ = os.Remove(tmpPath)
		return fmt.Errorf("exporting hnsw graph: %w", writeErr)
	}
	//nolint:gosec // The sidecar path is derived from the configured DB path and kept on the local filesystem.
	if err := os.Rename(tmpPath, h.path); err != nil {
		//nolint:gosec // Temp file was created in the target sidecar directory for this specific index file.
		_ = os.Remove(tmpPath)
		return fmt.Errorf("persisting hnsw graph: %w", err)
	}
	h.dirty.Store(false)
	return nil
}

// flush persists the graph to disk if it has been modified since the last persist.
func (h *hnswIndex) flush() error {
	if !h.dirty.Load() {
		return nil
	}
	return h.persist()
}

func (h *hnswIndex) scheduleFlush() {
	h.flushMu.Lock()
	defer h.flushMu.Unlock()

	if h.flushTimer != nil {
		h.flushTimer.Stop()
	}
	h.flushTimer = time.AfterFunc(hnswFlushDelayMs*time.Millisecond, func() {
		h.flushMu.Lock()
		h.flushTimer = nil
		h.flushMu.Unlock()

		if err := h.flush(); err != nil {
			logx.Warn("failed to flush hnsw graph", "err", err)
		}
	})
}

func (h *hnswIndex) stopFlushTimer() {
	h.flushMu.Lock()
	defer h.flushMu.Unlock()

	if h.flushTimer != nil {
		h.flushTimer.Stop()
		h.flushTimer = nil
	}
}

// BuildHNSW loads all embeddings from the database and builds the in-memory HNSW graph.
// Call this after the initial index sync completes. The graph is always built regardless of
// corpus size but is only persisted to disk when the corpus reaches hnswPersistMinChunks.
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

	if count >= hnswPersistMinChunks {
		if err := s.hnsw.persist(); err != nil {
			logx.Warn("failed to persist hnsw graph", "err", err)
		}
	}
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

// ResetHNSW marks the in-memory HNSW graph as not-ready so it will be rebuilt
// on the next BuildHNSW call. Used when staleness is detected after a crash.
func (s *Store) ResetHNSW() {
	if s.hnsw == nil {
		return
	}
	s.hnsw.mu.Lock()
	s.hnsw.graph = hnsw.NewGraph[int]()
	s.hnsw.graph.M = hnswM
	s.hnsw.graph.EfSearch = hnswEfSearch
	s.hnsw.ready.Store(false)
	s.hnsw.dirty.Store(false)
	s.hnsw.mu.Unlock()
}

// RepairHNSW incrementally synchronizes the in-memory HNSW graph with the database.
// It adds missing chunks without discarding the existing graph structure, avoiding
// a costly full rebuild for small drifts. Falls back to ResetHNSW if more than
// half the chunks are missing or if orphaned nodes are detected.
func (s *Store) RepairHNSW(ctx context.Context) error {
	if s.hnsw == nil || !s.hnsw.ready.Load() {
		return nil
	}

	meta, err := s.embeddingMetadata(ctx)
	if err != nil || meta == nil || meta.Dimensions == 0 {
		return fmt.Errorf("reading embedding metadata for hnsw repair: %w", err)
	}
	dims := meta.Dimensions

	hnswNodes := s.hnsw.Len()
	rows, err := s.db.QueryContext(ctx, `SELECT id, embedding FROM chunks`)
	if err != nil {
		return fmt.Errorf("querying chunks for hnsw repair: %w", err)
	}
	defer func() { _ = rows.Close() }()

	type chunkInfo struct {
		id  int
		vec []float32
	}
	var missing []chunkInfo
	dbCount := 0
	for rows.Next() {
		var id int
		var embBytes []byte
		if err := rows.Scan(&id, &embBytes); err != nil {
			return fmt.Errorf("scanning chunk for hnsw repair: %w", err)
		}
		dbCount++
		if _, found := s.hnsw.graph.Lookup(id); !found {
			vec := decodeEmbeddingForHNSW(embBytes, dims)
			if len(vec) > 0 {
				missing = append(missing, chunkInfo{id: id, vec: vec})
			}
		}
	}
	if err := rows.Err(); err != nil {
		return fmt.Errorf("reading chunks for hnsw repair: %w", err)
	}

	orphanedCount := hnswNodes - (dbCount - len(missing))
	if orphanedCount < 0 {
		orphanedCount = 0
	}

	totalDrift := len(missing) + orphanedCount
	if dbCount > 0 && totalDrift > dbCount/2 {
		logx.Warn("hnsw drift too large; falling back to full rebuild", "missing", len(missing), "orphaned", orphanedCount, "total_chunks", dbCount)
		s.ResetHNSW()
		return nil
	}

	for _, c := range missing {
		s.hnsw.Add(c.id, c.vec)
	}

	if totalDrift > 0 {
		logx.Info("hnsw graph repaired incrementally", "added", len(missing), "orphaned_estimate", orphanedCount)
		if err := s.hnsw.persist(); err != nil {
			logx.Warn("failed to persist repaired hnsw graph", "err", err)
		}
	}

	return nil
}

// FlushHNSW schedules a debounced HNSW graph flush. Multiple calls within the
// flush window are coalesced into a single disk write.
func (s *Store) FlushHNSW() {
	if s.hnsw == nil {
		return
	}
	s.hnsw.scheduleFlush()
}

// FlushHNSWNow flushes the HNSW graph to disk immediately, cancelling any
// pending debounced flush. Use for critical paths like bulk deletes.
func (s *Store) FlushHNSWNow() {
	if s.hnsw == nil {
		return
	}
	s.hnsw.stopFlushTimer()
	if err := s.hnsw.flush(); err != nil {
		logx.Warn("failed to flush hnsw graph", "err", err)
	}
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

const hnswPrefixMaxRetries = 2

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
