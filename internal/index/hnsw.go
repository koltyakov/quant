package index

import (
	"bufio"
	"context"
	"database/sql"
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
	// flushedMods is the mods value at the time of the last successful graph
	// file save; periodic flushes are skipped while the two are equal.
	flushedMods atomic.Int64
	generation  atomic.Int64
}

func newHNSWIndex() *hnswIndex {
	h := &hnswIndex{}
	h.generation.Store(-1)
	return h
}

func (h *hnswIndex) modCount() int64 {
	return h.mods.Load()
}

func (h *hnswIndex) resetMods() {
	h.mods.Store(0)
	// Mark the graph dirty so the next save is never skipped: a rebuilt or
	// reconstructed graph may not match whatever graph file is on disk.
	h.flushedMods.Store(-1)
}

func (h *hnswIndex) Add(id int, vec []float32) {
	if !h.ready.Load() {
		return
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.graph == nil || h.graph.Len() == 0 {
		m, efSearch := defaultHNSWM, defaultHNSWEfSearch
		if h.graph != nil {
			m, efSearch = h.graph.M, h.graph.EfSearch
		}
		h.graph = newGraph(m, efSearch)
	}
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
	if h.graph == nil || h.graph.Len() == 0 {
		m, efSearch := defaultHNSWM, defaultHNSWEfSearch
		if h.graph != nil {
			m, efSearch = h.graph.M, h.graph.EfSearch
		}
		h.graph = newGraph(m, efSearch)
	}
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
	if h.graph == nil || h.graph.Len() == 0 {
		return nil
	}
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
	s.writeMu.Lock()
	defer s.writeMu.Unlock()

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

	tx, err := s.db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return fmt.Errorf("beginning hnsw snapshot: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	generation, err := chunkGenerationTx(ctx, tx)
	if err != nil {
		return err
	}
	rows, err := tx.QueryContext(ctx, `SELECT id, embedding FROM chunks`)
	if err != nil {
		return fmt.Errorf("querying chunks for hnsw build: %w", err)
	}

	g := newGraph(s.hnswM, s.hnswEfSearch)

	count := 0
	for rows.Next() {
		var id int
		var embBytes []byte
		if err := rows.Scan(&id, &embBytes); err != nil {
			_ = rows.Close()
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
		_ = rows.Close()
		return fmt.Errorf("reading chunks for hnsw: %w", err)
	}
	if err := rows.Close(); err != nil {
		return fmt.Errorf("closing hnsw chunk snapshot: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("committing hnsw snapshot: %w", err)
	}

	s.hnsw.mu.Lock()
	s.hnsw.graph = g
	s.hnsw.mu.Unlock()
	s.hnsw.generation.Store(generation)
	s.hnsw.ready.Store(true)
	s.hnsw.resetMods()
	logx.Info("hnsw graph built", "chunks", count, "M", s.hnswM, "EfSearch", s.hnswEfSearch)

	// hnsw_state is the validity marker for the graph sidecar. Publish it only
	// after the graph has been replaced successfully.
	if err := s.saveHNSWGraphToFile(); err != nil {
		logx.Warn("failed to persist hnsw graph file", "err", err)
	} else if published, err := s.saveHNSWState(ctx, count, generation); err != nil {
		logx.Warn("failed to persist hnsw metadata snapshot", "err", err)
	} else if !published {
		s.resetRuntimeIndexes(false)
		return fmt.Errorf("chunk generation changed while building hnsw graph")
	}

	if err := s.LoadDocEmbeddings(ctx); err != nil {
		logx.Warn("failed to load document embeddings", "err", err)
	}

	return nil
}

func (s *Store) saveHNSWState(ctx context.Context, nodeCount int, generation int64) (bool, error) {
	meta, err := s.embeddingMetadata(ctx)
	if err != nil || meta == nil {
		return false, fmt.Errorf("reading embedding metadata for hnsw state: %w", err)
	}
	result, err := s.db.ExecContext(ctx,
		`INSERT INTO hnsw_state (id, built_at, node_count, model, dimensions, generation)
		 SELECT 1, CURRENT_TIMESTAMP, ?, ?, ?, ?
		 FROM index_state WHERE id = 1 AND chunk_generation = ?
		 ON CONFLICT(id) DO UPDATE SET
			built_at = excluded.built_at,
			node_count = excluded.node_count,
			model = excluded.model,
			dimensions = excluded.dimensions,
			generation = excluded.generation`,
		nodeCount, meta.Model, meta.Dimensions, generation, generation,
	)
	if err != nil {
		return false, err
	}
	rowsAffected, err := result.RowsAffected()
	if err != nil {
		return false, err
	}
	return rowsAffected == 1, nil
}

func (s *Store) LoadHNSWFromState(ctx context.Context) bool {
	s.writeMu.Lock()
	defer s.writeMu.Unlock()

	loaded := s.loadHNSWGraphFromFile(ctx)
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

func (s *Store) loadHNSWGraphFromFile(ctx context.Context) bool {
	if s.hnswGraphPath == "" {
		return false
	}

	var nodeCount int
	var storedModel string
	var storedDims int
	var storedGeneration int64
	if err := s.db.QueryRowContext(ctx,
		`SELECT node_count, model, dimensions, generation FROM hnsw_state WHERE id = 1`,
	).Scan(&nodeCount, &storedModel, &storedDims, &storedGeneration); err != nil || nodeCount == 0 {
		return false
	}
	currentGeneration, err := s.chunkGeneration(ctx)
	if err != nil || currentGeneration != storedGeneration {
		return false
	}

	meta, err := s.embeddingMetadata(ctx)
	if err != nil || meta == nil || meta.Dimensions == 0 {
		return false
	}
	if storedModel != meta.Model || storedDims != meta.Dimensions {
		logx.Info("hnsw file metadata mismatch, skipping graph file",
			"stored_model", storedModel, "current_model", meta.Model,
			"stored_dims", storedDims, "current_dims", meta.Dimensions)
		return false
	}

	var chunkCount int
	if err := s.db.QueryRowContext(ctx, embeddedChunkCountQuery).Scan(&chunkCount); err != nil || chunkCount != nodeCount {
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

	var magic, version uint32
	if err := binary.Read(f, binary.LittleEndian, &magic); err != nil {
		return false
	}
	if magic != hnswGraphFileMagic {
		logx.Warn("hnsw graph file has bad magic", "expected", hnswGraphFileMagic, "got", magic)
		return false
	}
	if err := binary.Read(f, binary.LittleEndian, &version); err != nil {
		return false
	}
	if version != hnswGraphFileVersion {
		logx.Warn("hnsw graph file has incompatible version", "expected", hnswGraphFileVersion, "got", version)
		return false
	}
	var fileGeneration int64
	if err := binary.Read(f, binary.LittleEndian, &fileGeneration); err != nil {
		return false
	}
	if fileGeneration != storedGeneration {
		logx.Warn("hnsw graph file generation mismatch", "file_generation", fileGeneration, "state_generation", storedGeneration)
		return false
	}

	g := newGraph(s.hnswM, s.hnswEfSearch)
	if err := g.Import(bufio.NewReader(f)); err != nil {
		logx.Warn("failed to import hnsw graph file", "path", s.hnswGraphPath, "err", err)
		return false
	}
	if g.Len() != nodeCount {
		logx.Warn("hnsw graph file node count mismatch", "path", s.hnswGraphPath, "file_nodes", g.Len(), "state_nodes", nodeCount)
		return false
	}
	currentGeneration, err = s.chunkGeneration(ctx)
	if err != nil || currentGeneration != storedGeneration {
		return false
	}

	s.hnsw.mu.Lock()
	s.hnsw.graph = g
	s.hnsw.mu.Unlock()
	s.hnsw.generation.Store(storedGeneration)
	s.hnsw.ready.Store(true)
	s.hnsw.resetMods()
	// The in-memory graph came from the file, so the two are in sync.
	s.hnsw.flushedMods.Store(0)
	logx.Info("hnsw graph loaded from file", "path", s.hnswGraphPath, "nodes", g.Len())
	return true
}

func (s *Store) loadHNSWFromSQLite(ctx context.Context) bool {
	var nodeCount int
	var storedModel string
	var storedDims int
	var storedGeneration int64
	err := s.db.QueryRowContext(ctx,
		`SELECT node_count, model, dimensions, generation FROM hnsw_state WHERE id = 1`,
	).Scan(&nodeCount, &storedModel, &storedDims, &storedGeneration)
	if err != nil {
		return false
	}
	if nodeCount == 0 {
		return false
	}
	currentGeneration, err := s.chunkGeneration(ctx)
	if err != nil || currentGeneration != storedGeneration {
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
	if err := s.db.QueryRowContext(ctx, embeddedChunkCountQuery).Scan(&chunkCount); err != nil {
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
	currentGeneration, err = s.chunkGeneration(ctx)
	if err != nil || currentGeneration != storedGeneration {
		return false
	}

	s.hnsw.mu.Lock()
	s.hnsw.graph = g
	s.hnsw.mu.Unlock()
	s.hnsw.generation.Store(storedGeneration)
	s.hnsw.ready.Store(true)
	s.hnsw.resetMods()
	logx.Info("hnsw graph reconstructed from chunk embeddings using metadata snapshot", "chunks", loaded)

	if err := s.saveHNSWGraphToFile(); err != nil {
		logx.Warn("failed to save hnsw graph after reconstruction", "err", err)
	}

	return true
}

const hnswGraphFileMagic uint32 = 0x514E5347 // "QNSG"
const hnswGraphFileVersion uint32 = 2

// embeddedChunkCountQuery counts only chunks that carry an embedding: chunks
// indexed during keyword-only fallback have none and are never HNSW nodes, so
// comparing against the total chunk count would force a rebuild every start.
const embeddedChunkCountQuery = `SELECT COUNT(*) FROM chunks WHERE embedding IS NOT NULL AND length(embedding) > 0`

func (s *Store) saveHNSWGraphToFile() error {
	if s.hnswGraphPath == "" {
		return nil
	}
	if !s.hnsw.ready.Load() {
		return nil
	}

	// Skip the export when nothing changed since the last successful save and
	// the graph file is still on disk.
	if s.hnsw.flushedMods.Load() == s.hnsw.modCount() {
		if _, err := os.Stat(s.hnswGraphPath); err == nil {
			return nil
		}
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
	if err := binary.Write(w, binary.LittleEndian, s.hnsw.generation.Load()); err != nil {
		_ = f.Close()
		_ = os.Remove(tmpPath)
		return err
	}

	// Hold the read lock for the whole export: BatchAdd/BatchDelete mutate the
	// graph under the write lock, and exporting concurrently is a data race.
	s.hnsw.mu.RLock()
	g := s.hnsw.graph
	modsAtExport := s.hnsw.modCount()
	var exportErr error
	if g != nil {
		exportErr = g.Export(w)
	}
	s.hnsw.mu.RUnlock()

	if g == nil {
		_ = f.Close()
		_ = os.Remove(tmpPath)
		return nil
	}
	if exportErr != nil {
		_ = f.Close()
		_ = os.Remove(tmpPath)
		return fmt.Errorf("exporting hnsw graph: %w", exportErr)
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
	s.hnsw.flushedMods.Store(modsAtExport)
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

func (s *Store) FlushHNSW() error {
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	if s.hnsw == nil || !s.hnsw.ready.Load() {
		return nil
	}
	if err := s.saveHNSWGraphToFile(); err != nil {
		return err
	}
	generation := s.hnsw.generation.Load()
	published, err := s.saveHNSWState(context.Background(), s.hnsw.Len(), generation)
	if err != nil {
		return err
	}
	if !published {
		s.resetRuntimeIndexes(false)
		return fmt.Errorf("chunk generation changed before hnsw flush")
	}
	return nil
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
