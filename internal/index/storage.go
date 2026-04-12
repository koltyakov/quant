package index

import (
	"context"
	"database/sql"
	"fmt"
	"strconv"
	"strings"

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

func upsertDocumentTx(ctx context.Context, tx *sql.Tx, doc *Document) (int64, error) {
	var id int64
	err := tx.QueryRowContext(ctx,
		`INSERT INTO documents (path, hash, modified_at, indexed_at)
		 VALUES (?, ?, ?, CURRENT_TIMESTAMP)
		 ON CONFLICT(path) DO UPDATE SET
			hash = excluded.hash,
			modified_at = excluded.modified_at,
			indexed_at = CURRENT_TIMESTAMP
		 RETURNING id`,
		doc.Path, doc.Hash, doc.ModifiedAt,
	).Scan(&id)
	if err != nil {
		return 0, fmt.Errorf("upserting document: %w", err)
	}
	return id, nil
}

func (s *Store) embeddingMetadata(ctx context.Context) (*EmbeddingMetadata, error) {
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

	return &EmbeddingMetadata{
		Model:      values["model"],
		Dimensions: dims,
		Normalized: values["normalized"] == "true",
	}, nil
}

func (s *Store) putEmbeddingMetadata(ctx context.Context, meta EmbeddingMetadata) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("beginning metadata transaction: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	if _, err := tx.ExecContext(ctx, `DELETE FROM embedding_metadata`); err != nil {
		return fmt.Errorf("clearing embedding metadata: %w", err)
	}

	values := map[string]string{
		"model":      meta.Model,
		"dimensions": strconv.Itoa(meta.Dimensions),
		"normalized": strconv.FormatBool(meta.Normalized),
		"schema":     "1",
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
	if _, err := tx.ExecContext(ctx, `INSERT INTO chunks_fts(chunks_fts) VALUES('rebuild')`); err != nil {
		return fmt.Errorf("rebuilding chunks fts: %w", err)
	}
	if err := clearHNSWStateTx(ctx, tx); err != nil {
		return err
	}

	if err := tx.Commit(); err != nil {
		return fmt.Errorf("committing reset transaction: %w", err)
	}
	return nil
}

func deleteChunksByDocumentIDTx(ctx context.Context, tx *sql.Tx, docID int64) error {
	if _, err := tx.ExecContext(ctx, `DELETE FROM chunks WHERE document_id = ?`, docID); err != nil {
		return fmt.Errorf("deleting document chunks: %w", err)
	}
	return nil
}

func clearHNSWStateTx(ctx context.Context, tx *sql.Tx) error {
	if _, err := tx.ExecContext(ctx, `DELETE FROM hnsw_state`); err != nil {
		return fmt.Errorf("clearing hnsw state: %w", err)
	}
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
