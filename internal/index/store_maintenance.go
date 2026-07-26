package index

import (
	"context"
	"database/sql"
	"fmt"
	"slices"
	"strings"

	"github.com/koltyakov/quant/internal/logx"
)

// Embedding metadata, stats, quarantine, content dedup, and collections.

func (s *Store) EnsureEmbeddingMetadata(ctx context.Context, meta EmbeddingMetadata) (bool, error) {
	s.writeMu.Lock()
	defer s.writeMu.Unlock()

	current, err := s.embeddingMetadata(ctx)
	if err != nil {
		return false, err
	}

	if current == nil {
		docCount, chunkCount, err := s.Stats(ctx)
		if err != nil {
			return false, err
		}
		needsReset := docCount > 0 || chunkCount > 0
		if needsReset {
			if err := s.resetIndex(ctx); err != nil {
				return false, err
			}
		}
		if err := s.putEmbeddingMetadata(ctx, meta); err != nil {
			return false, err
		}
		return needsReset, nil
	}

	if *current == meta {
		return false, nil
	}

	if err := s.resetIndex(ctx); err != nil {
		return false, err
	}
	if err := s.putEmbeddingMetadata(ctx, meta); err != nil {
		return false, err
	}
	return true, nil
}

func (s *Store) Stats(ctx context.Context) (docCount int, chunkCount int, err error) {
	err = s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM documents`).Scan(&docCount)
	if err != nil {
		return 0, 0, err
	}
	err = s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM chunks`).Scan(&chunkCount)
	return docCount, chunkCount, err
}

func (s *Store) FTSDiagnostics(ctx context.Context) (FTSDiagnostics, error) {
	var diag FTSDiagnostics

	if err := s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM chunks_fts`).Scan(&diag.LogicalRows); err != nil {
		return FTSDiagnostics{}, fmt.Errorf("counting chunks_fts rows: %w", err)
	}
	if err := s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM chunks_fts_data`).Scan(&diag.DataRows); err != nil {
		return FTSDiagnostics{}, fmt.Errorf("counting chunks_fts_data rows: %w", err)
	}
	if err := s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM chunks_fts_idx`).Scan(&diag.IdxRows); err != nil {
		return FTSDiagnostics{}, fmt.Errorf("counting chunks_fts_idx rows: %w", err)
	}

	diag.Empty = diag.LogicalRows == 0
	return diag, nil
}

func (s *Store) AddToQuarantine(ctx context.Context, path, errMsg string) error {
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	_, err := s.db.ExecContext(ctx,
		`INSERT INTO quarantine (path, error_msg, created_at, attempts)
		 VALUES (?, ?, CURRENT_TIMESTAMP, 1)
		 ON CONFLICT(path) DO UPDATE SET error_msg = excluded.error_msg, created_at = CURRENT_TIMESTAMP, attempts = attempts + 1`,
		path, errMsg,
	)
	return err
}

func (s *Store) RemoveFromQuarantine(ctx context.Context, path string) error {
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	_, err := s.db.ExecContext(ctx, `DELETE FROM quarantine WHERE path = ?`, path)
	return err
}

func (s *Store) IsQuarantined(ctx context.Context, path string) (bool, error) {
	var count int
	err := s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM quarantine WHERE path = ?`, path).Scan(&count)
	if err != nil {
		return false, err
	}
	return count > 0, nil
}

func (s *Store) ListQuarantined(ctx context.Context) ([]QuarantineEntry, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT path, error_msg, created_at, attempts FROM quarantine ORDER BY created_at DESC`)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()

	var entries []QuarantineEntry
	for rows.Next() {
		var e QuarantineEntry
		if err := rows.Scan(&e.Path, &e.ErrorMsg, &e.CreatedAt, &e.Attempts); err != nil {
			return nil, err
		}
		entries = append(entries, e)
	}
	return entries, rows.Err()
}

func (s *Store) ClearQuarantine(ctx context.Context) error {
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	_, err := s.db.ExecContext(ctx, `DELETE FROM quarantine`)
	return err
}

func (s *Store) LookupContentDedup(ctx context.Context, contentHash string) ([]byte, bool) {
	var embedding []byte
	err := s.db.QueryRowContext(ctx,
		`SELECT embedding FROM content_dedup WHERE content_hash = ?`, contentHash,
	).Scan(&embedding)
	if err != nil {
		return nil, false
	}
	return embedding, true
}

// LookupContentDedupBatch resolves many content hashes in one round trip.
// Ingestion looks up every chunk of a document, so the single-hash variant
// turned each document into an N+1 against the dedup table.
func (s *Store) LookupContentDedupBatch(ctx context.Context, contentHashes []string) (map[string][]byte, error) {
	if len(contentHashes) == 0 {
		return nil, nil
	}

	found := make(map[string][]byte, len(contentHashes))
	for batch := range slices.Chunk(contentHashes, maxIDsPerQuery) {
		args := make([]any, len(batch))
		for i, hash := range batch {
			args[i] = hash
		}
		//nolint:gosec // placeholders are all literal "?" - no user input in the query string
		query := `SELECT content_hash, embedding FROM content_dedup WHERE content_hash IN (` + sqlPlaceholders(len(batch)) + `)`
		if err := s.forEachRow(ctx, query, args, func(rows *sql.Rows) error {
			var hash string
			var embedding []byte
			if err := rows.Scan(&hash, &embedding); err != nil {
				return err
			}
			found[hash] = embedding
			return nil
		}); err != nil {
			return nil, fmt.Errorf("looking up content dedup entries: %w", err)
		}
	}
	return found, nil
}

// StoreContentDedupBatch writes many dedup entries in a single transaction,
// replacing one write transaction per chunk.
func (s *Store) StoreContentDedupBatch(ctx context.Context, entries map[string][]byte) error {
	if len(entries) == 0 {
		return nil
	}

	s.writeMu.Lock()
	defer s.writeMu.Unlock()

	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("beginning content dedup transaction: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	stmt, err := tx.PrepareContext(ctx,
		`INSERT INTO content_dedup (content_hash, embedding) VALUES (?, ?)
		 ON CONFLICT(content_hash) DO UPDATE SET embedding = excluded.embedding`)
	if err != nil {
		return fmt.Errorf("preparing content dedup insert: %w", err)
	}
	defer func() { _ = stmt.Close() }()

	for hash, embedding := range entries {
		if _, err := stmt.ExecContext(ctx, hash, embedding); err != nil {
			return fmt.Errorf("writing content dedup entry: %w", err)
		}
	}

	if err := tx.Commit(); err != nil {
		return fmt.Errorf("committing content dedup transaction: %w", err)
	}
	return nil
}

func (s *Store) StoreContentDedup(ctx context.Context, contentHash string, embedding []byte) error {
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	_, err := s.db.ExecContext(ctx,
		`INSERT INTO content_dedup (content_hash, embedding) VALUES (?, ?)
		 ON CONFLICT(content_hash) DO UPDATE SET embedding = excluded.embedding`,
		contentHash, embedding,
	)
	return err
}

func (s *Store) RemoveContentDedup(ctx context.Context, contentHash string) error {
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	_, err := s.db.ExecContext(ctx, `DELETE FROM content_dedup WHERE content_hash = ?`, contentHash)
	return err
}

func (s *Store) ListCollections(ctx context.Context) ([]string, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT DISTINCT collection FROM documents WHERE collection != '' ORDER BY collection`,
	)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()

	var collections []string
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			return nil, err
		}
		collections = append(collections, name)
	}
	return collections, rows.Err()
}

func (s *Store) CollectionStats(ctx context.Context, collection string) (docCount int, chunkCount int, err error) {
	err = s.db.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM documents WHERE collection = ?`, collection,
	).Scan(&docCount)
	if err != nil {
		return 0, 0, err
	}
	err = s.db.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM chunks WHERE document_id IN (SELECT id FROM documents WHERE collection = ?)`, collection,
	).Scan(&chunkCount)
	return
}

func (s *Store) DeleteCollection(ctx context.Context, collection string) error {
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	collection = strings.TrimSpace(collection)
	if collection == "" {
		return fmt.Errorf("collection is required")
	}

	hnswReady := s.hnsw != nil && s.hnsw.ready.Load()
	hnswDeleteComplete := !hnswReady
	var hnswDeleteIDs []int
	if hnswReady {
		rows, err := s.db.QueryContext(ctx,
			`SELECT c.id FROM chunks c JOIN documents d ON c.document_id = d.id WHERE d.collection = ?`,
			collection,
		)
		if err == nil {
			for rows.Next() {
				var id int
				if rows.Scan(&id) == nil {
					hnswDeleteIDs = append(hnswDeleteIDs, id)
				}
			}
			_ = rows.Close()
			if rows.Err() != nil {
				hnswDeleteIDs = nil
			} else {
				hnswDeleteComplete = true
			}
		}
	}

	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("beginning delete-collection transaction: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	if _, err := tx.ExecContext(ctx,
		`DELETE FROM chunks WHERE document_id IN (SELECT id FROM documents WHERE collection = ?)`, collection,
	); err != nil {
		return fmt.Errorf("deleting chunks for collection %s: %w", collection, err)
	}
	if _, err := tx.ExecContext(ctx,
		`DELETE FROM documents WHERE collection = ?`, collection,
	); err != nil {
		return fmt.Errorf("deleting collection %s: %w", collection, err)
	}
	if err := clearHNSWStateTx(ctx, tx); err != nil {
		return err
	}
	committedGeneration, err := chunkGenerationTx(ctx, tx)
	if err != nil {
		return err
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("committing delete-collection transaction: %w", err)
	}

	if hnswReady {
		if !hnswDeleteComplete {
			s.resetRuntimeIndexes(false)
		} else {
			s.hnsw.BatchDelete(hnswDeleteIDs)
			s.hnsw.generation.Store(committedGeneration)
		}
	}
	if s.docEmbeds != nil {
		s.docEmbeds.Clear()
		if err := s.LoadDocEmbeddings(ctx); err != nil {
			logx.Warn("failed to reload document embeddings after collection delete", "err", err)
		}
	}
	return nil
}
