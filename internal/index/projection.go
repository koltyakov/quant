package index

import (
	"context"
	"encoding/binary"
	"fmt"
	"math"
	"math/rand"

	"github.com/koltyakov/quant/internal/logx"
)

type ProjectionLayer struct {
	weight  []float32
	bias    []float32
	inDims  int
	outDims int
}

func NewRandomProjection(inDims, outDims int) *ProjectionLayer {
	w := make([]float32, inDims*outDims)
	scale := float32(math.Sqrt(2.0 / float64(inDims)))
	for i := range w {
		w[i] = float32(rand.NormFloat64()) * scale // #nosec G404
	}
	bias := make([]float32, outDims)
	return &ProjectionLayer{weight: w, bias: bias, inDims: inDims, outDims: outDims}
}

func LoadProjection(data []byte) (*ProjectionLayer, error) {
	if len(data) < 12 {
		return nil, fmt.Errorf("projection data too small")
	}

	inDims := int(binary.LittleEndian.Uint32(data[0:]))
	outDims := int(binary.LittleEndian.Uint32(data[4:]))
	expected := 8 + inDims*outDims*4 + outDims*4
	if len(data) < expected {
		return nil, fmt.Errorf("projection data truncated: have %d, need %d", len(data), expected)
	}

	w := make([]float32, inDims*outDims)
	offset := 8
	for i := range w {
		w[i] = math.Float32frombits(binary.LittleEndian.Uint32(data[offset:]))
		offset += 4
	}

	bias := make([]float32, outDims)
	for i := range bias {
		bias[i] = math.Float32frombits(binary.LittleEndian.Uint32(data[offset:]))
		offset += 4
	}

	return &ProjectionLayer{weight: w, bias: bias, inDims: inDims, outDims: outDims}, nil
}

func (p *ProjectionLayer) Project(input []float32) []float32 {
	if len(input) != p.inDims {
		return nil
	}
	output := make([]float32, p.outDims)
	for j := range p.outDims {
		var sum float32
		for i, v := range input {
			sum += v * p.weight[i*p.outDims+j]
		}
		output[j] = sum + p.bias[j]
	}
	return NormalizeFloat32(output)
}

func (p *ProjectionLayer) Encode() []byte {
	totalSize := 8 + p.inDims*p.outDims*4 + p.outDims*4
	buf := make([]byte, totalSize)

	binary.LittleEndian.PutUint32(buf[0:], uint32(p.inDims))  // #nosec G115
	binary.LittleEndian.PutUint32(buf[4:], uint32(p.outDims)) // #nosec G115

	offset := 8
	for _, v := range p.weight {
		binary.LittleEndian.PutUint32(buf[offset:], math.Float32bits(v))
		offset += 4
	}
	for _, v := range p.bias {
		binary.LittleEndian.PutUint32(buf[offset:], math.Float32bits(v))
		offset += 4
	}
	return buf
}

func (p *ProjectionLayer) InDims() int  { return p.inDims }
func (p *ProjectionLayer) OutDims() int { return p.outDims }

type chunkUpdate struct {
	id     int64
	newEmb []byte
}

func (s *Store) MigrateEmbeddingsWithProjection(ctx context.Context, proj *ProjectionLayer) error {
	s.writeMu.Lock()
	defer s.writeMu.Unlock()

	meta, err := s.embeddingMetadata(ctx)
	if err != nil || meta == nil {
		return fmt.Errorf("reading embedding metadata for projection migration: %w", err)
	}

	if meta.Dimensions != proj.inDims {
		return fmt.Errorf("projection input dims (%d) don't match current embedding dims (%d)", proj.inDims, meta.Dimensions)
	}

	rows, err := s.db.QueryContext(ctx, `SELECT id, embedding FROM chunks`)
	if err != nil {
		return fmt.Errorf("querying chunks for projection migration: %w", err)
	}
	defer func() { _ = rows.Close() }()

	var updates []chunkUpdate
	migrated := 0

	for rows.Next() {
		var id int64
		var embBytes []byte
		if err := rows.Scan(&id, &embBytes); err != nil {
			return fmt.Errorf("scanning chunk for projection migration: %w", err)
		}

		vec := decodeEmbeddingForHNSW(embBytes, proj.inDims)
		if len(vec) == 0 {
			continue
		}

		projected := proj.Project(vec)
		newEmb := EncodeInt8(projected)
		updates = append(updates, chunkUpdate{id: id, newEmb: newEmb})
		migrated++

		if len(updates) >= 500 {
			if err := s.applyProjectionBatch(ctx, updates); err != nil {
				return err
			}
			logx.Info("projection migration batch applied", "count", migrated)
			updates = updates[:0]
		}
	}
	if err := rows.Err(); err != nil {
		return err
	}

	if len(updates) > 0 {
		if err := s.applyProjectionBatch(ctx, updates); err != nil {
			return err
		}
	}

	logx.Info("projection migration complete", "total_migrated", migrated)
	return nil
}

func (s *Store) applyProjectionBatch(ctx context.Context, updates []chunkUpdate) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("beginning projection batch: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	stmt, err := tx.PrepareContext(ctx, `UPDATE chunks SET embedding = ? WHERE id = ?`)
	if err != nil {
		return fmt.Errorf("preparing projection update: %w", err)
	}
	defer func() { _ = stmt.Close() }()

	for _, u := range updates {
		if _, err := stmt.ExecContext(ctx, u.newEmb, u.id); err != nil {
			return fmt.Errorf("updating chunk %d in projection batch: %w", u.id, err)
		}
	}

	return tx.Commit()
}

func (s *Store) SaveProjection(ctx context.Context, proj *ProjectionLayer) error {
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	data := proj.Encode()
	_, err := s.db.ExecContext(ctx,
		`INSERT INTO embedding_metadata (key, value) VALUES ('projection', ?)
		 ON CONFLICT(key) DO UPDATE SET value = excluded.value`,
		string(data),
	)
	return err
}

func (s *Store) LoadProjection(ctx context.Context) (*ProjectionLayer, error) {
	var data string
	err := s.db.QueryRowContext(ctx,
		`SELECT value FROM embedding_metadata WHERE key = 'projection'`,
	).Scan(&data)
	if err != nil {
		return nil, err
	}
	return LoadProjection([]byte(data))
}
