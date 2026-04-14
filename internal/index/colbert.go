package index

import (
	"context"
	"encoding/binary"
	"fmt"
	"math"
	"sort"

	"github.com/koltyakov/quant/internal/logx"
)

const (
	colBERTMagic     uint32 = 0x434F4C42 // "COLB"
	colBERTVersion   uint32 = 1
	colBERTMaxTokens        = 128
)

type ColBERTConfig struct {
	Enabled     bool
	MaxTokens   int
	CompressDim int
}

type ColBERTIndex struct {
	config    ColBERTConfig
	tokenEmbs map[int][][]float32
	ready     bool
}

func NewColBERTIndex(cfg ColBERTConfig) *ColBERTIndex {
	if cfg.MaxTokens <= 0 {
		cfg.MaxTokens = colBERTMaxTokens
	}
	return &ColBERTIndex{
		config:    cfg,
		tokenEmbs: make(map[int][][]float32),
	}
}

func (c *ColBERTIndex) Add(chunkID int, tokenEmbeddings [][]float32) {
	if !c.config.Enabled {
		return
	}
	if len(tokenEmbeddings) > c.config.MaxTokens {
		tokenEmbeddings = tokenEmbeddings[:c.config.MaxTokens]
	}
	c.tokenEmbs[chunkID] = tokenEmbeddings
}

func (c *ColBERTIndex) Remove(chunkID int) {
	delete(c.tokenEmbs, chunkID)
}

func (c *ColBERTIndex) Search(queryTokens [][]float32, k int) []colBERTResult {
	if !c.ready || len(queryTokens) == 0 {
		return nil
	}

	type scored struct {
		id    int
		score float32
	}

	results := make([]scored, 0, len(c.tokenEmbs))
	for chunkID, docTokens := range c.tokenEmbs {
		score := maxSim(queryTokens, docTokens)
		results = append(results, scored{id: chunkID, score: score})
	}

	sort.Slice(results, func(i, j int) bool {
		return results[i].score > results[j].score
	})

	if k > len(results) {
		k = len(results)
	}

	out := make([]colBERTResult, k)
	for i := 0; i < k; i++ {
		out[i] = colBERTResult{ChunkID: results[i].id, Score: results[i].score}
	}
	return out
}

func (c *ColBERTIndex) Len() int {
	return len(c.tokenEmbs)
}

func (c *ColBERTIndex) Ready() bool {
	return c.ready
}

func (c *ColBERTIndex) SetReady(r bool) {
	c.ready = r
}

type colBERTResult struct {
	ChunkID int
	Score   float32
}

func maxSim(queryTokens [][]float32, docTokens [][]float32) float32 {
	if len(queryTokens) == 0 || len(docTokens) == 0 {
		return 0
	}

	var total float32
	for _, qToken := range queryTokens {
		var maxScore float32
		for _, dToken := range docTokens {
			s := dotProduct(qToken, dToken)
			if s > maxScore {
				maxScore = s
			}
		}
		total += maxScore
	}
	return total / float32(len(queryTokens))
}

func EncodeTokenEmbeddings(tokenEmbs [][]float32) []byte {
	if len(tokenEmbs) == 0 {
		return nil
	}
	dims := len(tokenEmbs[0])
	nTokens := len(tokenEmbs)

	totalSize := 4 + 4 + 4 + nTokens*dims*4
	buf := make([]byte, totalSize)
	offset := 0

	binary.LittleEndian.PutUint32(buf[offset:], colBERTMagic)
	offset += 4
	binary.LittleEndian.PutUint32(buf[offset:], colBERTVersion)
	offset += 4
	binary.LittleEndian.PutUint32(buf[offset:], uint32(nTokens)) // #nosec G115 -- nTokens is token count, always fits in uint32
	offset += 4

	for _, emb := range tokenEmbs {
		for _, v := range emb {
			binary.LittleEndian.PutUint32(buf[offset:], math.Float32bits(v))
			offset += 4
		}
	}
	return buf
}

func DecodeTokenEmbeddings(data []byte) [][]float32 {
	if len(data) < 12 {
		return nil
	}

	magic := binary.LittleEndian.Uint32(data[0:])
	if magic != colBERTMagic {
		return nil
	}

	version := binary.LittleEndian.Uint32(data[4:])
	if version != colBERTVersion {
		logx.Warn("colbert: unknown version", "version", version)
		return nil
	}

	nTokens := int(binary.LittleEndian.Uint32(data[8:]))
	if nTokens == 0 {
		return nil
	}

	remaining := len(data) - 12
	dims := remaining / (nTokens * 4)
	if dims == 0 {
		return nil
	}

	result := make([][]float32, nTokens)
	offset := 12
	for i := 0; i < nTokens; i++ {
		result[i] = make([]float32, dims)
		for j := 0; j < dims; j++ {
			result[i][j] = math.Float32frombits(binary.LittleEndian.Uint32(data[offset:]))
			offset += 4
		}
	}
	return result
}

func (s *Store) MigrateColBERTColumn() error {
	var colCount int
	err := s.db.QueryRowContext(context.Background(),
		`SELECT COUNT(*) FROM pragma_table_info('chunks') WHERE name='token_embeddings'`,
	).Scan(&colCount)
	if err != nil {
		return fmt.Errorf("checking chunks schema for token_embeddings column: %w", err)
	}
	if colCount == 0 {
		if _, err := s.db.ExecContext(context.Background(),
			`ALTER TABLE chunks ADD COLUMN token_embeddings BLOB`,
		); err != nil {
			return fmt.Errorf("adding chunks.token_embeddings column: %w", err)
		}
	}
	return nil
}

func (s *Store) SearchColBERT(ctx context.Context, queryTokens [][]float32, limit int) ([]SearchResult, error) {
	if s.colbert == nil || !s.colbert.Ready() {
		return nil, fmt.Errorf("colbert index not available")
	}

	results := s.colbert.Search(queryTokens, limit)
	if len(results) == 0 {
		return nil, nil
	}

	ids := make([]int, len(results))
	for i, r := range results {
		ids[i] = r.ChunkID
	}

	placeholders := make([]string, len(ids))
	args := make([]any, len(ids))
	for i, id := range ids {
		placeholders[i] = "?"
		args[i] = id
	}

	query := `SELECT c.id, c.content, c.chunk_index, d.path
	          FROM chunks c JOIN documents d ON c.document_id = d.id
	          WHERE c.id IN (` + joinInts(ids, ",") + `)` // #nosec G202

	scoreMap := make(map[int]float32)
	for _, r := range results {
		scoreMap[r.ChunkID] = r.Score
	}

	rows, err := s.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()

	var searchResults []SearchResult
	for rows.Next() {
		var id int
		var content string
		var chunkIndex int
		var docPath string
		if err := rows.Scan(&id, &content, &chunkIndex, &docPath); err != nil {
			return nil, err
		}
		score, ok := scoreMap[id]
		if !ok {
			continue
		}
		searchResults = append(searchResults, SearchResult{
			ChunkID:      int64(id),
			ChunkContent: content,
			ChunkIndex:   chunkIndex,
			DocumentPath: docPath,
			Score:        score,
			ScoreKind:    "colbert",
		})
	}

	sort.Slice(searchResults, func(i, j int) bool {
		return searchResults[i].Score > searchResults[j].Score
	})

	return searchResults, rows.Err()
}

func joinInts(ids []int, sep string) string {
	if len(ids) == 0 {
		return ""
	}
	s := fmt.Sprintf("%d", ids[0])
	for _, id := range ids[1:] {
		s += fmt.Sprintf("%s%d", sep, id)
	}
	return s
}
