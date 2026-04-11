package index

import (
	"context"
	"database/sql"
	"fmt"
	"strconv"
	"strings"
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

	if _, err := tx.ExecContext(ctx, `DELETE FROM documents`); err != nil {
		return fmt.Errorf("clearing documents: %w", err)
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO chunks_fts(chunks_fts) VALUES('rebuild')`); err != nil {
		return fmt.Errorf("rebuilding chunks fts: %w", err)
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM hnsw_state`); err != nil {
		return fmt.Errorf("clearing hnsw state: %w", err)
	}

	if err := tx.Commit(); err != nil {
		return fmt.Errorf("committing reset transaction: %w", err)
	}
	return nil
}
