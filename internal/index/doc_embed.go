package index

import (
	"context"
	"database/sql"
	"sync"

	"github.com/koltyakov/quant/internal/logx"
)

const docFilterTopK = 100

type docEmbeddingIndex struct {
	mu      sync.RWMutex
	byDocID map[int64][]float32
	byPath  map[string]int64
}

func newDocEmbeddingIndex() *docEmbeddingIndex {
	return &docEmbeddingIndex{
		byDocID: make(map[int64][]float32),
		byPath:  make(map[string]int64),
	}
}

func (d *docEmbeddingIndex) Set(docID int64, path string, vec []float32) {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.byDocID[docID] = vec
	d.byPath[path] = docID
}

func (d *docEmbeddingIndex) Remove(docID int64, path string) {
	d.mu.Lock()
	defer d.mu.Unlock()
	delete(d.byDocID, docID)
	delete(d.byPath, path)
}

func (d *docEmbeddingIndex) Len() int {
	d.mu.RLock()
	defer d.mu.RUnlock()
	return len(d.byDocID)
}

func (d *docEmbeddingIndex) topDocPaths(queryEmbed []float32, topK int) map[string]float32 {
	d.mu.RLock()
	defer d.mu.RUnlock()

	if len(d.byDocID) == 0 || len(queryEmbed) == 0 {
		return nil
	}

	type scored struct {
		path string
		vec  []float32
		dot  float32
	}

	idToPath := make(map[int64]string, len(d.byPath))
	for p, id := range d.byPath {
		idToPath[id] = p
	}

	all := make([]scored, 0, len(d.byDocID))
	for docID, vec := range d.byDocID {
		path := idToPath[docID]
		if path == "" {
			continue
		}
		dot := dotProduct(queryEmbed, vec)
		all = append(all, scored{path: path, vec: vec, dot: dot})
	}

	if topK > len(all) {
		topK = len(all)
	}

	for i := 0; i < topK; i++ {
		for j := len(all) - 1; j > i; j-- {
			if all[j].dot > all[j-1].dot {
				all[j], all[j-1] = all[j-1], all[j]
			}
		}
	}

	result := make(map[string]float32, topK)
	for i := 0; i < topK; i++ {
		result[all[i].path] = all[i].dot
	}
	return result
}

func (s *Store) migrateDocEmbeddingColumn() error {
	var colCount int
	err := s.db.QueryRowContext(context.Background(),
		`SELECT COUNT(*) FROM pragma_table_info('documents') WHERE name='doc_embedding'`,
	).Scan(&colCount)
	if err != nil {
		return err
	}
	if colCount == 0 {
		if _, err := s.db.ExecContext(context.Background(),
			`ALTER TABLE documents ADD COLUMN doc_embedding BLOB`,
		); err != nil {
			return err
		}
	}
	return nil
}

func computeDocEmbedding(chunkRecords []ChunkRecord, dims int) []byte {
	var sum []float32
	totalWeight := float32(0)

	for i, cr := range chunkRecords {
		vec := decodeEmbeddingForHNSW(cr.Embedding, dims)
		if len(vec) == 0 {
			continue
		}
		if sum == nil {
			sum = make([]float32, len(vec))
		}
		weight := docEmbeddingWeight(i, len(chunkRecords))
		for j := range vec {
			sum[j] += vec[j] * weight
		}
		totalWeight += weight
	}
	if totalWeight == 0 {
		return nil
	}
	for i := range sum {
		sum[i] /= totalWeight
	}
	return EncodeInt8(NormalizeFloat32(sum))
}

func docEmbeddingWeight(chunkIndex, totalChunks int) float32 {
	positionWeight := float32(1.0)
	if totalChunks > 1 && chunkIndex == 0 {
		positionWeight = 1.5
	}
	if totalChunks > 2 && chunkIndex == totalChunks-1 {
		positionWeight = 1.2
	}
	middleWeight := float32(1.0)
	if totalChunks > 4 {
		fraction := float32(chunkIndex) / float32(totalChunks-1)
		middleWeight = 1.0 + 0.3*(1.0-2.0*abs32(fraction-0.5))
	}
	return positionWeight * middleWeight
}

func abs32(x float32) float32 {
	if x < 0 {
		return -x
	}
	return x
}

func (s *Store) LoadDocEmbeddings(ctx context.Context) error {
	meta, err := s.embeddingMetadata(ctx)
	if err != nil || meta == nil || meta.Dimensions == 0 {
		return nil
	}
	dims := meta.Dimensions

	rows, err := s.db.QueryContext(ctx,
		`SELECT d.id, d.path, d.doc_embedding FROM documents d WHERE d.doc_embedding IS NOT NULL`,
	)
	if err != nil {
		return err
	}
	defer func() { _ = rows.Close() }()

	loaded := 0
	for rows.Next() {
		var docID int64
		var path string
		var embBytes []byte
		if err := rows.Scan(&docID, &path, &embBytes); err != nil {
			return err
		}
		vec := decodeEmbeddingForHNSW(embBytes, dims)
		if len(vec) == 0 {
			continue
		}
		normalized := NormalizeFloat32(vec)
		s.docEmbeds.Set(docID, path, normalized)
		loaded++
	}
	if loaded > 0 {
		logx.Info("loaded document embeddings", "count", loaded)
	}
	return rows.Err()
}

func (s *Store) updateDocEmbeddingTx(ctx context.Context, tx *sql.Tx, docID int64, embedding []byte) error {
	if embedding == nil {
		_, err := tx.ExecContext(ctx, `UPDATE documents SET doc_embedding = NULL WHERE id = ?`, docID)
		return err
	}
	_, err := tx.ExecContext(ctx, `UPDATE documents SET doc_embedding = ? WHERE id = ?`, embedding, docID)
	return err
}
