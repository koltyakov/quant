package index

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/koltyakov/quant/internal/logx"
)

func sqlLikePrefixPattern(prefix string) string {
	replacer := strings.NewReplacer(
		`\`, `\\`,
		`%`, `\%`,
		`_`, `\_`,
	)
	return replacer.Replace(prefix) + "%"
}

// truncateUTF8 returns s shortened to at most n bytes, ending on a rune
// boundary so the result never splits a multi-byte UTF-8 sequence.
func truncateUTF8(s string, n int) string {
	if len(s) <= n {
		return s
	}
	end := n
	for end > 0 && !utf8.RuneStart(s[end]) {
		end--
	}
	return s[:end]
}

func upsertDocumentTx(ctx context.Context, tx *sql.Tx, doc *Document) (int64, error) {
	tagsJSON := ""
	if doc.Tags != nil {
		tj, _ := json.Marshal(doc.Tags)
		tagsJSON = string(tj)
	}

	var id int64
	err := tx.QueryRowContext(ctx,
		`INSERT INTO documents (path, hash, modified_at, indexed_at, file_size, file_type, language, title, tags, collection)
		 VALUES (?, ?, ?, CURRENT_TIMESTAMP, ?, ?, ?, ?, ?, ?)
		 ON CONFLICT(path) DO UPDATE SET
			hash = excluded.hash,
			modified_at = excluded.modified_at,
			indexed_at = CURRENT_TIMESTAMP,
			file_size = excluded.file_size,
			file_type = excluded.file_type,
			language = excluded.language,
			title = excluded.title,
			tags = excluded.tags,
			collection = excluded.collection
		 RETURNING id`,
		doc.Path, doc.Hash, doc.ModifiedAt, doc.FileSize, doc.FileType, doc.Language, doc.Title, tagsJSON, doc.Collection,
	).Scan(&id)
	if err != nil {
		return 0, fmt.Errorf("upserting document: %w", err)
	}
	return id, nil
}

// embeddingMetadata returns the stored embedding metadata, caching it after the
// first read. Every reindexed document and every vector operation needs it, so
// the uncached version issued a query per indexed file.
func (s *Store) embeddingMetadata(ctx context.Context) (*EmbeddingMetadata, error) {
	s.metaMu.RLock()
	cached, ok := s.embeddingMet, s.metaCached
	s.metaMu.RUnlock()
	if ok {
		return cached, nil
	}

	meta, err := s.loadEmbeddingMetadata(ctx)
	if err != nil {
		return nil, err
	}

	s.metaMu.Lock()
	s.embeddingMet = meta
	s.metaCached = true
	s.metaMu.Unlock()
	return meta, nil
}

// invalidateEmbeddingMetadata drops the cache after a metadata write.
func (s *Store) invalidateEmbeddingMetadata() {
	s.metaMu.Lock()
	s.embeddingMet = nil
	s.metaCached = false
	s.metaMu.Unlock()
}

func (s *Store) loadEmbeddingMetadata(ctx context.Context) (*EmbeddingMetadata, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT key, value FROM embedding_metadata`)
	if err != nil {
		return nil, fmt.Errorf("querying embedding metadata: %w", err)
	}
	defer func() { _ = rows.Close() }()

	values := make(map[string]string)
	for rows.Next() {
		var key string
		var value string
		if err := rows.Scan(&key, &value); err != nil {
			return nil, fmt.Errorf("scanning embedding metadata: %w", err)
		}
		values[key] = value
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("reading embedding metadata: %w", err)
	}
	if len(values) == 0 {
		return nil, nil
	}

	dims, err := strconv.Atoi(values["dimensions"])
	if err != nil {
		return nil, fmt.Errorf("parsing embedding dimensions: %w", err)
	}
	inputVersion := 0
	if value, ok := values["input_version"]; ok {
		inputVersion, err = strconv.Atoi(value)
		if err != nil {
			return nil, fmt.Errorf("parsing embedding input version: %w", err)
		}
	}

	return &EmbeddingMetadata{
		Model:        values["model"],
		Dimensions:   dims,
		Normalized:   values["normalized"] == "true",
		InputVersion: inputVersion,
	}, nil
}

func (s *Store) putEmbeddingMetadata(ctx context.Context, meta EmbeddingMetadata) error {
	// Invalidate on both success and failure: a rolled-back write still leaves
	// the cache untrusted if the transaction partially applied before erroring.
	defer s.invalidateEmbeddingMetadata()

	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("beginning metadata transaction: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	if _, err := tx.ExecContext(ctx, `DELETE FROM embedding_metadata`); err != nil {
		return fmt.Errorf("clearing embedding metadata: %w", err)
	}

	values := map[string]string{
		"model":         meta.Model,
		"dimensions":    strconv.Itoa(meta.Dimensions),
		"normalized":    strconv.FormatBool(meta.Normalized),
		"input_version": strconv.Itoa(meta.InputVersion),
		"schema":        "1",
	}
	for key, value := range values {
		if _, err := tx.ExecContext(ctx, `INSERT INTO embedding_metadata(key, value) VALUES(?, ?)`, key, value); err != nil {
			return fmt.Errorf("writing embedding metadata %s: %w", key, err)
		}
	}

	if err := tx.Commit(); err != nil {
		return fmt.Errorf("committing metadata transaction: %w", err)
	}
	return nil
}

func (s *Store) resetIndex(ctx context.Context) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("beginning reset transaction: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	if _, err := tx.ExecContext(ctx, `DELETE FROM chunks`); err != nil {
		return fmt.Errorf("clearing chunks: %w", err)
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM documents`); err != nil {
		return fmt.Errorf("clearing documents: %w", err)
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM content_dedup`); err != nil {
		return fmt.Errorf("clearing content dedup: %w", err)
	}
	if err := clearHNSWStateTx(ctx, tx); err != nil {
		return err
	}

	if err := tx.Commit(); err != nil {
		return fmt.Errorf("committing reset transaction: %w", err)
	}
	s.resetRuntimeIndexes(true)
	return nil
}

func deleteChunksByDocumentIDTx(ctx context.Context, tx *sql.Tx, docID int64) error {
	if _, err := tx.ExecContext(ctx, `DELETE FROM chunks WHERE document_id = ?`, docID); err != nil {
		return fmt.Errorf("deleting document chunks: %w", err)
	}
	return nil
}

func (s *Store) chunkGeneration(ctx context.Context) (int64, error) {
	var generation int64
	if err := s.db.QueryRowContext(ctx,
		`SELECT chunk_generation FROM index_state WHERE id = 1`,
	).Scan(&generation); err != nil {
		return 0, fmt.Errorf("reading chunk generation: %w", err)
	}
	return generation, nil
}

func chunkGenerationTx(ctx context.Context, tx *sql.Tx) (int64, error) {
	var generation int64
	if err := tx.QueryRowContext(ctx,
		`SELECT chunk_generation FROM index_state WHERE id = 1`,
	).Scan(&generation); err != nil {
		return 0, fmt.Errorf("reading transaction chunk generation: %w", err)
	}
	return generation, nil
}

func clearHNSWStateTx(ctx context.Context, tx *sql.Tx) error {
	if _, err := tx.ExecContext(ctx, `DELETE FROM hnsw_state`); err != nil {
		return fmt.Errorf("clearing hnsw state: %w", err)
	}
	return nil
}

func (s *Store) Vacuum(ctx context.Context) error {
	if _, err := s.db.ExecContext(ctx, `PRAGMA wal_checkpoint(TRUNCATE)`); err != nil {
		logx.Warn("wal checkpoint before vacuum failed", "err", err)
	}

	if _, err := s.db.ExecContext(ctx, `VACUUM`); err != nil {
		return fmt.Errorf("vacuuming database: %w", err)
	}

	logx.Info("database vacuum completed")
	return nil
}

func (s *Store) cleanupOrphanedChunks(ctx context.Context) error {
	var orphanCount int
	if err := s.db.QueryRowContext(ctx,
		`SELECT COUNT(*)
		 FROM chunks c
		 LEFT JOIN documents d ON d.id = c.document_id
		 WHERE d.id IS NULL`,
	).Scan(&orphanCount); err != nil {
		return fmt.Errorf("counting orphaned chunks: %w", err)
	}
	if orphanCount == 0 {
		return nil
	}

	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("beginning orphan cleanup transaction: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	if _, err := tx.ExecContext(ctx,
		`DELETE FROM chunks
		 WHERE NOT EXISTS (
		 	SELECT 1 FROM documents d WHERE d.id = chunks.document_id
		 )`,
	); err != nil {
		return fmt.Errorf("deleting orphaned chunks: %w", err)
	}
	if err := clearHNSWStateTx(ctx, tx); err != nil {
		return err
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("committing orphan cleanup transaction: %w", err)
	}

	logx.Warn("removed orphaned chunks from index", "chunks", orphanCount)
	return nil
}
