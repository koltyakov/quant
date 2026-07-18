package index

import (
	"context"
	"fmt"
)

// Schema creation and incremental migrations.

func (s *Store) migrate() error {
	schema := `
	CREATE TABLE IF NOT EXISTS documents (
		id          INTEGER PRIMARY KEY AUTOINCREMENT,
		path        TEXT NOT NULL UNIQUE,
		hash        TEXT NOT NULL,
		modified_at DATETIME NOT NULL,
		indexed_at  DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP
	);
	CREATE TABLE IF NOT EXISTS chunks (
		id          INTEGER PRIMARY KEY AUTOINCREMENT,
		document_id INTEGER NOT NULL REFERENCES documents(id) ON DELETE CASCADE,
		content     TEXT NOT NULL,
		chunk_index INTEGER NOT NULL,
		embedding   BLOB NOT NULL
	);
	CREATE UNIQUE INDEX IF NOT EXISTS idx_chunks_document_chunk ON chunks(document_id, chunk_index);
	CREATE INDEX IF NOT EXISTS idx_chunks_document_id ON chunks(document_id);
	CREATE TABLE IF NOT EXISTS embedding_metadata (
		key   TEXT PRIMARY KEY,
		value TEXT NOT NULL
	);
	CREATE TABLE IF NOT EXISTS hnsw_state (
		id          INTEGER PRIMARY KEY CHECK (id = 1),
		built_at    DATETIME NOT NULL,
		node_count  INTEGER NOT NULL,
		model       TEXT NOT NULL DEFAULT '',
		dimensions  INTEGER NOT NULL DEFAULT 0,
		generation  INTEGER NOT NULL DEFAULT 0
	);
	CREATE TABLE IF NOT EXISTS index_state (
		id               INTEGER PRIMARY KEY CHECK (id = 1),
		chunk_generation INTEGER NOT NULL DEFAULT 0
	);
	INSERT OR IGNORE INTO index_state (id, chunk_generation) VALUES (1, 0);
	CREATE TABLE IF NOT EXISTS quarantine (
		path        TEXT PRIMARY KEY,
		error_msg   TEXT NOT NULL,
		created_at  DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
		attempts    INTEGER NOT NULL DEFAULT 0
	);
	CREATE TABLE IF NOT EXISTS content_dedup (
		content_hash TEXT PRIMARY KEY,
		embedding    BLOB NOT NULL,
		created_at   DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP
	);
	CREATE VIRTUAL TABLE IF NOT EXISTS chunks_fts USING fts5(
		content,
		content='chunks',
		content_rowid='id',
		tokenize='porter unicode61'
	);
	CREATE TRIGGER IF NOT EXISTS chunks_ai AFTER INSERT ON chunks BEGIN
		INSERT INTO chunks_fts(rowid, content) VALUES (new.id, new.content);
	END;
	CREATE TRIGGER IF NOT EXISTS chunks_ad AFTER DELETE ON chunks BEGIN
		INSERT INTO chunks_fts(chunks_fts, rowid, content) VALUES('delete', old.id, old.content);
	END;
	CREATE TRIGGER IF NOT EXISTS chunks_au AFTER UPDATE ON chunks BEGIN
		INSERT INTO chunks_fts(chunks_fts, rowid, content) VALUES('delete', old.id, old.content);
		INSERT INTO chunks_fts(rowid, content) VALUES (new.id, new.content);
	END;
	`
	_, err := s.db.Exec(schema)
	if err != nil {
		return err
	}
	if err := s.migrateHNSWStateColumns(); err != nil {
		return err
	}
	if err := s.migrateHNSWGenerationColumn(); err != nil {
		return err
	}
	if err := s.migrateDocEmbeddingColumn(); err != nil {
		return err
	}
	if err := s.migrateHierarchicalChunks(); err != nil {
		return err
	}
	if err := s.migrateSummaryColumn(); err != nil {
		return err
	}
	if err := s.migrateDocumentMetadata(); err != nil {
		return err
	}
	if err := s.migrateCollectionColumn(); err != nil {
		return err
	}
	if err := s.migrateHNSWInvalidationTriggers(); err != nil {
		return err
	}
	return s.ensureQuantApplicationID(context.Background())
}

const hnswInvalidationTriggers = `
CREATE TRIGGER IF NOT EXISTS hnsw_chunks_ai AFTER INSERT ON chunks BEGIN
	UPDATE index_state SET chunk_generation = chunk_generation + 1 WHERE id = 1;
	DELETE FROM hnsw_state;
END;
CREATE TRIGGER IF NOT EXISTS hnsw_chunks_ad AFTER DELETE ON chunks BEGIN
	UPDATE index_state SET chunk_generation = chunk_generation + 1 WHERE id = 1;
	DELETE FROM hnsw_state;
END;
CREATE TRIGGER IF NOT EXISTS hnsw_chunks_au AFTER UPDATE ON chunks BEGIN
	UPDATE index_state SET chunk_generation = chunk_generation + 1 WHERE id = 1;
	DELETE FROM hnsw_state;
END;
`

func (s *Store) migrateHNSWInvalidationTriggers() error {
	ctx := context.Background()
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("beginning hnsw trigger migration: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	var compatible int
	if err := tx.QueryRowContext(ctx, `
		SELECT COUNT(*) FROM sqlite_master
		WHERE type = 'trigger' AND name IN ('hnsw_chunks_ai', 'hnsw_chunks_ad', 'hnsw_chunks_au')
		AND instr(lower(sql), 'chunk_generation') > 0
	`).Scan(&compatible); err != nil {
		return fmt.Errorf("checking hnsw invalidation triggers: %w", err)
	}
	if compatible != 3 {
		if _, err := tx.ExecContext(ctx, `
			DROP TRIGGER IF EXISTS hnsw_chunks_ai;
			DROP TRIGGER IF EXISTS hnsw_chunks_ad;
			DROP TRIGGER IF EXISTS hnsw_chunks_au;
		`); err != nil {
			return fmt.Errorf("replacing hnsw invalidation triggers: %w", err)
		}
	}
	if _, err := tx.ExecContext(ctx, hnswInvalidationTriggers); err != nil {
		return fmt.Errorf("creating hnsw invalidation triggers: %w", err)
	}
	if compatible != 3 {
		if err := clearHNSWStateTx(ctx, tx); err != nil {
			return fmt.Errorf("invalidating pre-trigger hnsw state: %w", err)
		}
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("committing hnsw trigger migration: %w", err)
	}
	return nil
}

func (s *Store) migrateCollectionColumn() error {
	var colCount int
	err := s.db.QueryRowContext(context.Background(),
		`SELECT COUNT(*) FROM pragma_table_info('documents') WHERE name='collection'`,
	).Scan(&colCount)
	if err != nil {
		return fmt.Errorf("checking documents schema for collection column: %w", err)
	}
	if colCount == 0 {
		if _, err := s.db.ExecContext(context.Background(),
			`ALTER TABLE documents ADD COLUMN collection TEXT NOT NULL DEFAULT ''`,
		); err != nil {
			return fmt.Errorf("adding documents.collection column: %w", err)
		}
		if _, err := s.db.ExecContext(context.Background(),
			`CREATE INDEX IF NOT EXISTS idx_documents_collection ON documents(collection)`,
		); err != nil {
			return fmt.Errorf("creating collection index: %w", err)
		}
	}
	return nil
}

func (s *Store) migrateSummaryColumn() error {
	var colCount int
	err := s.db.QueryRowContext(context.Background(),
		`SELECT COUNT(*) FROM pragma_table_info('chunks') WHERE name='summary'`,
	).Scan(&colCount)
	if err != nil {
		return fmt.Errorf("checking chunks schema for summary column: %w", err)
	}
	if colCount == 0 {
		if _, err := s.db.ExecContext(context.Background(),
			`ALTER TABLE chunks ADD COLUMN summary TEXT NOT NULL DEFAULT ''`,
		); err != nil {
			return fmt.Errorf("adding chunks.summary column: %w", err)
		}
	}
	return nil
}

func (s *Store) migrateHNSWStateColumns() error {
	var modelCount int
	err := s.db.QueryRowContext(context.Background(),
		`SELECT COUNT(*) FROM pragma_table_info('hnsw_state') WHERE name='model'`,
	).Scan(&modelCount)
	if err != nil {
		return fmt.Errorf("checking hnsw_state schema: %w", err)
	}
	if modelCount == 0 {
		if _, err := s.db.ExecContext(context.Background(),
			`ALTER TABLE hnsw_state ADD COLUMN model TEXT NOT NULL DEFAULT ''`,
		); err != nil {
			return fmt.Errorf("adding hnsw_state.model column: %w", err)
		}
		if _, err := s.db.ExecContext(context.Background(),
			`ALTER TABLE hnsw_state ADD COLUMN dimensions INTEGER NOT NULL DEFAULT 0`,
		); err != nil {
			return fmt.Errorf("adding hnsw_state.dimensions column: %w", err)
		}
	}
	return nil
}

func (s *Store) migrateHNSWGenerationColumn() error {
	var generationCount int
	if err := s.db.QueryRowContext(context.Background(),
		`SELECT COUNT(*) FROM pragma_table_info('hnsw_state') WHERE name='generation'`,
	).Scan(&generationCount); err != nil {
		return fmt.Errorf("checking hnsw_state generation schema: %w", err)
	}
	if generationCount == 0 {
		if _, err := s.db.ExecContext(context.Background(),
			`ALTER TABLE hnsw_state ADD COLUMN generation INTEGER NOT NULL DEFAULT 0`,
		); err != nil {
			return fmt.Errorf("adding hnsw_state.generation column: %w", err)
		}
	}
	return nil
}

func (s *Store) migrateDocumentMetadata() error {
	var colCount int
	err := s.db.QueryRowContext(context.Background(),
		`SELECT COUNT(*) FROM pragma_table_info('documents') WHERE name='file_type'`,
	).Scan(&colCount)
	if err != nil {
		return fmt.Errorf("checking documents schema for metadata columns: %w", err)
	}
	if colCount == 0 {
		if _, err := s.db.ExecContext(context.Background(),
			`ALTER TABLE documents ADD COLUMN file_type TEXT NOT NULL DEFAULT ''`,
		); err != nil {
			return fmt.Errorf("adding documents.file_type column: %w", err)
		}
		if _, err := s.db.ExecContext(context.Background(),
			`ALTER TABLE documents ADD COLUMN language TEXT NOT NULL DEFAULT ''`,
		); err != nil {
			return fmt.Errorf("adding documents.language column: %w", err)
		}
		if _, err := s.db.ExecContext(context.Background(),
			`ALTER TABLE documents ADD COLUMN title TEXT NOT NULL DEFAULT ''`,
		); err != nil {
			return fmt.Errorf("adding documents.title column: %w", err)
		}
		if _, err := s.db.ExecContext(context.Background(),
			`ALTER TABLE documents ADD COLUMN tags TEXT NOT NULL DEFAULT ''`,
		); err != nil {
			return fmt.Errorf("adding documents.tags column: %w", err)
		}
	}
	return nil
}

func (s *Store) migrateHierarchicalChunks() error {
	var colCount int
	err := s.db.QueryRowContext(context.Background(),
		`SELECT COUNT(*) FROM pragma_table_info('chunks') WHERE name='parent_id'`,
	).Scan(&colCount)
	if err != nil {
		return fmt.Errorf("checking chunks schema for hierarchical columns: %w", err)
	}
	if colCount == 0 {
		if _, err := s.db.ExecContext(context.Background(),
			`ALTER TABLE chunks ADD COLUMN parent_id INTEGER REFERENCES chunks(id) ON DELETE SET NULL`,
		); err != nil {
			return fmt.Errorf("adding chunks.parent_id column: %w", err)
		}
		if _, err := s.db.ExecContext(context.Background(),
			`ALTER TABLE chunks ADD COLUMN depth INTEGER NOT NULL DEFAULT 0`,
		); err != nil {
			return fmt.Errorf("adding chunks.depth column: %w", err)
		}
		if _, err := s.db.ExecContext(context.Background(),
			`ALTER TABLE chunks ADD COLUMN section_title TEXT NOT NULL DEFAULT ''`,
		); err != nil {
			return fmt.Errorf("adding chunks.section_title column: %w", err)
		}
	}
	return nil
}
