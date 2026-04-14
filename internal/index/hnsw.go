package index

import (
	"bufio"
	"context"
	"encoding/binary"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"

	"github.com/coder/hnsw"
	"github.com/koltyakov/quant/internal/logx"
)

type hnswIndex struct {
	mu    sync.RWMutex
	graph *hnsw.Graph[int]
	ready atomic.Bool
	mods  atomic.Int64
}

func newHNSWIndex() *hnswIndex {
	return &hnswIndex{}
}

func (h *hnswIndex) modCount() int64 {
	return h.mods.Load()
}

func (h *hnswIndex) resetMods() {
	h.mods.Store(0)
}

func (h *hnswIndex) Add(id int, vec []float32) {
	if !h.ready.Load() {
		return
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	h.graph.Add(hnsw.MakeNode(id, vec))
	h.mods.Add(1)
}

func (h *hnswIndex) Delete(id int) {
	if !h.ready.Load() {
		return
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	h.graph.Delete(id)
	h.mods.Add(1)
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
	h.mods.Add(int64(len(ids)))
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
	h.mods.Add(int64(len(nodes)))
}

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

func (h *hnswIndex) Len() int {
	if !h.ready.Load() {
		return 0
	}
	h.mu.RLock()
	defer h.mu.RUnlock()
	return h.graph.Len()
}

func newGraph(m, efSearch int) *hnsw.Graph[int] {
	g := hnsw.NewGraph[int]()
	g.M = m
	g.EfSearch = efSearch
	g.Distance = hnsw.CosineDistance
	return g
}

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

	g := newGraph(s.hnswM, s.hnswEfSearch)

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
	s.hnsw.resetMods()
	logx.Info("hnsw graph built", "chunks", count, "M", s.hnswM, "EfSearch", s.hnswEfSearch)

	if err := s.saveHNSWState(ctx, count); err != nil {
		logx.Warn("failed to persist hnsw metadata snapshot", "err", err)
	}

	if err := s.saveHNSWGraphToFile(); err != nil {
		logx.Warn("failed to persist hnsw graph file", "err", err)
	}

	if err := s.LoadDocEmbeddings(ctx); err != nil {
		logx.Warn("failed to load document embeddings", "err", err)
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

func (s *Store) LoadHNSWFromState(ctx context.Context) bool {
	loaded := s.loadHNSWGraphFromFile()
	if !loaded {
		loaded = s.loadHNSWFromSQLite(ctx)
	}

	if loaded {
		if err := s.LoadDocEmbeddings(ctx); err != nil {
			logx.Warn("failed to load document embeddings", "err", err)
		}
	}
	return loaded
}

func (s *Store) loadHNSWGraphFromFile() bool {
	if s.hnswGraphPath == "" {
		return false
	}

	if _, err := os.Stat(s.hnswGraphPath); err != nil {
		legacyPath := s.dbPath + ".hnswgraph"
		if info, err := os.Stat(legacyPath); err == nil && info.Size() > 0 {
			if err := os.Rename(legacyPath, s.hnswGraphPath); err != nil {
				logx.Warn("failed to migrate hnsw graph file", "from", legacyPath, "to", s.hnswGraphPath, "err", err)
			}
		}
	}

	info, err := os.Stat(s.hnswGraphPath)
	if err != nil || info.Size() == 0 {
		return false
	}

	f, err := os.Open(s.hnswGraphPath)
	if err != nil {
		return false
	}
	defer func() { _ = f.Close() }()

	g := newGraph(s.hnswM, s.hnswEfSearch)
	if err := g.Import(bufio.NewReader(f)); err != nil {
		logx.Warn("failed to import hnsw graph file", "path", s.hnswGraphPath, "err", err)
		return false
	}

	s.hnsw.mu.Lock()
	s.hnsw.graph = g
	s.hnsw.mu.Unlock()
	s.hnsw.ready.Store(true)
	s.hnsw.resetMods()
	logx.Info("hnsw graph loaded from file", "path", s.hnswGraphPath, "nodes", g.Len())
	return true
}

func (s *Store) loadHNSWFromSQLite(ctx context.Context) bool {
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

	g := newGraph(s.hnswM, s.hnswEfSearch)

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
	s.hnsw.resetMods()
	logx.Info("hnsw graph reconstructed from chunk embeddings using metadata snapshot", "chunks", loaded)

	if err := s.saveHNSWGraphToFile(); err != nil {
		logx.Warn("failed to save hnsw graph after reconstruction", "err", err)
	}

	return true
}

const hnswGraphFileMagic uint32 = 0x514E5347 // "QNSG"
const hnswGraphFileVersion uint32 = 1

func (s *Store) saveHNSWGraphToFile() error {
	if s.hnswGraphPath == "" {
		return nil
	}
	s.hnsw.mu.RLock()
	g := s.hnsw.graph
	ready := s.hnsw.ready.Load()
	s.hnsw.mu.RUnlock()

	if !ready || g == nil {
		return nil
	}

	f, err := os.CreateTemp(filepath.Dir(s.hnswGraphPath), ".quant-hnsw-*")
	if err != nil {
		return fmt.Errorf("creating temp file for hnsw graph: %w", err)
	}
	tmpPath := f.Name()

	w := bufio.NewWriter(f)
	if err := binary.Write(w, binary.LittleEndian, hnswGraphFileMagic); err != nil {
		_ = f.Close()
		_ = os.Remove(tmpPath)
		return err
	}
	if err := binary.Write(w, binary.LittleEndian, hnswGraphFileVersion); err != nil {
		_ = f.Close()
		_ = os.Remove(tmpPath)
		return err
	}
	if err := g.Export(w); err != nil {
		_ = f.Close()
		_ = os.Remove(tmpPath)
		return fmt.Errorf("exporting hnsw graph: %w", err)
	}
	if err := w.Flush(); err != nil {
		_ = f.Close()
		_ = os.Remove(tmpPath)
		return err
	}
	if err := f.Close(); err != nil {
		_ = os.Remove(tmpPath)
		return err
	}
	if err := os.Rename(tmpPath, s.hnswGraphPath); err != nil {
		_ = os.Remove(tmpPath)
		return err
	}
	return nil
}

func (s *Store) HNSWReady() bool {
	return s.hnsw != nil && s.hnsw.ready.Load()
}

func (s *Store) HNSWLen() int {
	if s.hnsw == nil {
		return 0
	}
	return s.hnsw.Len()
}

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
