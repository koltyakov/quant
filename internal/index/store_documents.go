package index

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"path"
	"strings"
	"time"

	"github.com/coder/hnsw"
	"github.com/koltyakov/quant/internal/logx"
)

// Document and chunk CRUD: upsert, reindex, delete, rename, and lookups.

func (s *Store) UpsertDocument(ctx context.Context, doc *Document) (int64, error) {
	s.writeMu.Lock()
	defer s.writeMu.Unlock()

	tagsJSON := ""
	if doc.Tags != nil {
		tj, _ := json.Marshal(doc.Tags)
		tagsJSON = string(tj)
	}
	var id int64
	err := s.db.QueryRowContext(ctx,
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

func (s *Store) InsertChunk(ctx context.Context, chunk *ChunkRecord) error {
	s.writeMu.Lock()
	defer s.writeMu.Unlock()

	_, err := s.db.ExecContext(ctx,
		`INSERT INTO chunks (document_id, content, chunk_index, embedding, parent_id, depth, section_title, summary) VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
		chunk.DocumentID, chunk.Content, chunk.ChunkIndex, chunk.Embedding, chunk.ParentID, chunk.Depth, chunk.SectionTitle, chunk.Summary,
	)
	if err == nil && s.hnsw != nil && s.hnsw.ready.Load() {
		s.resetRuntimeIndexes(false)
	}
	return err
}

func (s *Store) ReindexDocument(ctx context.Context, doc *Document, chunks []ChunkRecord) error {
	return s.ReindexDocumentWithDeferredHNSW(ctx, doc, chunks, nil)
}

func (s *Store) ReindexDocumentWithDeferredHNSW(ctx context.Context, doc *Document, chunks []ChunkRecord, deferredHNSW func()) error {
	s.writeMu.Lock()
	defer s.writeMu.Unlock()

	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("beginning transaction: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	docID, err := upsertDocumentTx(ctx, tx, doc)
	if err != nil {
		return err
	}

	hnswReady := s.hnsw != nil && s.hnsw.ready.Load()
	hnswDeleteComplete := !hnswReady
	var hnswDeleteIDs []int
	if hnswReady {
		rows, err := tx.QueryContext(ctx, `SELECT id FROM chunks WHERE document_id = ?`, docID)
		if err != nil {
			logx.Warn("failed to collect chunk ids for hnsw delete; graph may retain stale nodes", "doc_id", docID, "err", err)
		} else {
			for rows.Next() {
				var id int
				if rows.Scan(&id) == nil {
					hnswDeleteIDs = append(hnswDeleteIDs, id)
				}
			}
			_ = rows.Close()
			if rowsErr := rows.Err(); rowsErr != nil {
				logx.Warn("failed to read chunk ids for hnsw delete; graph may retain stale nodes", "doc_id", docID, "err", rowsErr)
				hnswDeleteIDs = nil
			} else {
				hnswDeleteComplete = true
			}
		}
	}

	if _, err := tx.ExecContext(ctx, `DELETE FROM chunks WHERE document_id = ?`, docID); err != nil {
		return fmt.Errorf("deleting existing chunks: %w", err)
	}

	meta, _ := s.embeddingMetadata(ctx)
	dims := 0
	if meta != nil {
		dims = meta.Dimensions
	}

	stmt, err := tx.PrepareContext(ctx, `INSERT INTO chunks (document_id, content, chunk_index, embedding, parent_id, depth, section_title, summary) VALUES (?, ?, ?, ?, ?, ?, ?, ?) RETURNING id`)
	if err != nil {
		return fmt.Errorf("preparing chunk insert: %w", err)
	}
	defer func() { _ = stmt.Close() }()

	type insertedChunk struct {
		id        int
		embedding []byte
	}
	inserted := make([]insertedChunk, 0, len(chunks))

	for _, chunk := range chunks {
		var newID int
		if err := stmt.QueryRowContext(ctx,
			docID, chunk.Content, chunk.ChunkIndex, chunk.Embedding, chunk.ParentID, chunk.Depth, chunk.SectionTitle, chunk.Summary,
		).Scan(&newID); err != nil {
			return fmt.Errorf("inserting chunk %d: %w", chunk.ChunkIndex, err)
		}
		inserted = append(inserted, insertedChunk{id: newID, embedding: chunk.Embedding})
	}

	var docEmb []byte
	if dims > 0 {
		if len(chunks) > 0 {
			docEmb = computeDocEmbedding(chunks, dims)
		}
		if err := s.updateDocEmbeddingTx(ctx, tx, docID, docEmb); err != nil {
			logx.Warn("failed to store document embedding", "doc_id", docID, "err", err)
		}
	}
	committedGeneration, err := chunkGenerationTx(ctx, tx)
	if err != nil {
		return err
	}

	if err := tx.Commit(); err != nil {
		return fmt.Errorf("committing transaction: %w", err)
	}

	// HNSW mutations happen only after the transaction commits, so a failed
	// commit never leaves the graph out of sync with the database.
	if hnswReady {
		if !hnswDeleteComplete {
			s.resetRuntimeIndexes(false)
			hnswReady = false
		} else {
			s.hnsw.BatchDelete(hnswDeleteIDs)
		}
	}

	if deferredHNSW != nil {
		deferredHNSW()
	}

	if hnswReady && s.hnsw.ready.Load() {
		meta2, metaErr := s.embeddingMetadata(ctx)
		if metaErr != nil {
			logx.Warn("failed to read embedding metadata for HNSW update", "err", metaErr)
			s.resetRuntimeIndexes(false)
		} else if meta2 != nil && meta2.Dimensions > 0 {
			var nodes []hnsw.Node[int]
			for _, ic := range inserted {
				vec := decodeEmbeddingForHNSW(ic.embedding, meta2.Dimensions)
				if len(vec) > 0 {
					nodes = append(nodes, hnsw.MakeNode(ic.id, vec))
				}
			}
			s.hnsw.BatchAdd(nodes)
			s.hnsw.generation.Store(committedGeneration)
		} else {
			s.resetRuntimeIndexes(false)
		}
	}

	if dims > 0 {
		if docEmb != nil {
			vec := decodeEmbeddingForHNSW(docEmb, dims)
			if len(vec) > 0 {
				s.docEmbeds.Set(docID, doc.Path, NormalizeFloat32(vec))
			}
		} else {
			s.docEmbeds.Remove(docID, doc.Path)
		}
	}

	return nil
}

// GetDocumentChunksByPath returns all existing chunks for the document at path,
// keyed by a compound of content and chunk index. Used for incremental reindex diffing.
func (s *Store) GetDocumentChunksByPath(ctx context.Context, path string) (map[string]ChunkRecord, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT c.id, c.chunk_index, c.content, c.embedding, c.parent_id, c.depth, c.section_title, c.summary
		 FROM chunks c
		 JOIN documents d ON c.document_id = d.id
		 WHERE d.path = ?`,
		path,
	)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()

	result := make(map[string]ChunkRecord)
	for rows.Next() {
		var cr ChunkRecord
		if err := rows.Scan(&cr.ID, &cr.ChunkIndex, &cr.Content, &cr.Embedding, &cr.ParentID, &cr.Depth, &cr.SectionTitle, &cr.Summary); err != nil {
			return nil, err
		}
		key := ChunkDiffKey(EmbedInputText(cr.SectionTitle, cr.Content))
		result[key] = cr
	}
	return result, rows.Err()
}

func ChunkDiffKey(content string) string {
	h := sha256.Sum256([]byte(content))
	return fmt.Sprintf("%x", h[:])
}

// EmbeddingInputVersion identifies the serialization performed by
// EmbedInputText. Increment it whenever that model input changes.
const EmbeddingInputVersion = 1

// EmbedInputText returns the exact text sent to the embedding model for a
// chunk: its heading context joined to the content, unless the content
// already carries that context (the chunker prepends it in that case).
// Diffing and dedup keying must use this text so an embedding is only reused
// for the identical model input.
func EmbedInputText(heading, content string) string {
	if heading == "" || strings.HasPrefix(content, heading+"\n\n") {
		return content
	}
	return heading + "\n\n" + content
}

func (s *Store) DeleteChunksByDocument(ctx context.Context, docID int64) error {
	s.writeMu.Lock()
	defer s.writeMu.Unlock()

	_, err := s.db.ExecContext(ctx, `DELETE FROM chunks WHERE document_id = ?`, docID)
	if err == nil && s.hnsw != nil && s.hnsw.ready.Load() {
		s.resetRuntimeIndexes(false)
	}
	return err
}

func (s *Store) DeleteDocument(ctx context.Context, path string) error {
	s.writeMu.Lock()
	defer s.writeMu.Unlock()

	hnswReady := s.hnsw != nil && s.hnsw.ready.Load()
	hnswDeleteComplete := !hnswReady
	var hnswDeleteIDs []int
	var docID int64
	doc, err := s.GetDocumentByPath(ctx, path)
	if err != nil {
		return err
	}
	if doc == nil {
		return nil
	}
	docID = doc.ID
	if hnswReady {
		rows, err := s.db.QueryContext(ctx, `SELECT id FROM chunks WHERE document_id = ?`, docID)
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
		return fmt.Errorf("beginning delete transaction: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	if err := deleteChunksByDocumentIDTx(ctx, tx, docID); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM documents WHERE id = ?`, docID); err != nil {
		return fmt.Errorf("deleting document: %w", err)
	}
	if err := clearHNSWStateTx(ctx, tx); err != nil {
		return err
	}
	committedGeneration, err := chunkGenerationTx(ctx, tx)
	if err != nil {
		return err
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("committing delete transaction: %w", err)
	}

	if hnswReady {
		if !hnswDeleteComplete {
			s.resetRuntimeIndexes(false)
		} else {
			s.hnsw.BatchDelete(hnswDeleteIDs)
			s.hnsw.generation.Store(committedGeneration)
		}
	}

	s.docEmbeds.Remove(docID, path)

	return nil
}

func (s *Store) DeleteDocumentsByPrefix(ctx context.Context, prefix string) error {
	s.writeMu.Lock()
	defer s.writeMu.Unlock()

	prefix = path.Clean(strings.ReplaceAll(prefix, `\`, "/"))
	likePrefix := sqlLikePrefixPattern(prefix + "/")
	if prefix == "." || prefix == "" {
		tx, err := s.db.BeginTx(ctx, nil)
		if err != nil {
			return fmt.Errorf("beginning delete-all transaction: %w", err)
		}
		defer func() { _ = tx.Rollback() }()

		if _, err := tx.ExecContext(ctx, `DELETE FROM chunks`); err != nil {
			return fmt.Errorf("clearing chunks: %w", err)
		}
		if _, err := tx.ExecContext(ctx, `DELETE FROM documents`); err != nil {
			return fmt.Errorf("clearing documents: %w", err)
		}
		if err := clearHNSWStateTx(ctx, tx); err != nil {
			return err
		}
		if err := tx.Commit(); err != nil {
			return fmt.Errorf("committing delete-all transaction: %w", err)
		}

		s.resetRuntimeIndexes(true)
		return nil
	}

	hnswReady := s.hnsw != nil && s.hnsw.ready.Load()
	hnswDeleteComplete := !hnswReady
	var hnswDeleteIDs []int
	if hnswReady {
		rows, err := s.db.QueryContext(ctx,
			`SELECT c.id
			 FROM chunks c
			 JOIN documents d ON c.document_id = d.id
			 WHERE d.path = ? OR d.path LIKE ? ESCAPE '\'`,
			prefix, likePrefix,
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
		return fmt.Errorf("beginning delete-by-prefix transaction: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	if _, err := tx.ExecContext(ctx,
		`DELETE FROM chunks
		 WHERE document_id IN (
		 	SELECT id FROM documents WHERE path = ? OR path LIKE ? ESCAPE '\'
		 )`,
		prefix, likePrefix,
	); err != nil {
		return fmt.Errorf("deleting chunks by prefix: %w", err)
	}
	if _, err := tx.ExecContext(ctx,
		`DELETE FROM documents WHERE path = ? OR path LIKE ? ESCAPE '\'`,
		prefix, likePrefix,
	); err != nil {
		return fmt.Errorf("deleting documents by prefix: %w", err)
	}
	if err := clearHNSWStateTx(ctx, tx); err != nil {
		return err
	}
	committedGeneration, err := chunkGenerationTx(ctx, tx)
	if err != nil {
		return err
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("committing delete-by-prefix transaction: %w", err)
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
			logx.Warn("failed to reload document embeddings after prefix delete", "err", err)
		}
	}
	return nil
}

func (s *Store) RenameDocumentPath(ctx context.Context, oldPath, newPath string) error {
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	_, err := s.db.ExecContext(ctx, `UPDATE documents SET path = ? WHERE path = ?`, newPath, oldPath)
	return err
}

// UpdateDocumentStat refreshes the stored modification time and size for a
// document whose content hash already matches the file on disk, so future
// syncs can skip re-hashing it.
func (s *Store) UpdateDocumentStat(ctx context.Context, path string, modTime time.Time, size int64) error {
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	_, err := s.db.ExecContext(ctx,
		`UPDATE documents SET modified_at = ?, file_size = ? WHERE path = ?`,
		modTime, size, path,
	)
	if err != nil {
		return fmt.Errorf("updating document stat: %w", err)
	}
	return nil
}

func (s *Store) GetDocumentByPath(ctx context.Context, path string) (*Document, error) {
	doc := &Document{}
	var tagsJSON string
	err := s.db.QueryRowContext(ctx,
		`SELECT id, path, hash, modified_at, indexed_at, file_size, file_type, language, title, tags, collection FROM documents WHERE path = ?`,
		path,
	).Scan(&doc.ID, &doc.Path, &doc.Hash, &doc.ModifiedAt, &doc.IndexedAt, &doc.FileSize, &doc.FileType, &doc.Language, &doc.Title, &tagsJSON, &doc.Collection)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	if tagsJSON != "" && tagsJSON != "{}" {
		_ = json.Unmarshal([]byte(tagsJSON), &doc.Tags)
	}
	return doc, nil
}

func (s *Store) ListDocuments(ctx context.Context) ([]Document, error) {
	return s.ListDocumentsLimit(ctx, 0)
}

func (s *Store) ListDocumentsLimit(ctx context.Context, limit int) ([]Document, error) {
	return s.listDocuments(ctx, limit)
}

func (s *Store) listDocuments(ctx context.Context, limit int) ([]Document, error) {
	query := `SELECT id, path, hash, modified_at, indexed_at, file_size, file_type, language, title, tags, collection FROM documents ORDER BY path`
	args := []any{}
	if limit > 0 {
		query += ` LIMIT ?`
		args = append(args, limit)
	}

	rows, err := s.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()

	docs := make([]Document, 0, min(limit, 256))
	for rows.Next() {
		var doc Document
		var tagsJSON string
		if err := rows.Scan(&doc.ID, &doc.Path, &doc.Hash, &doc.ModifiedAt, &doc.IndexedAt, &doc.FileSize, &doc.FileType, &doc.Language, &doc.Title, &tagsJSON, &doc.Collection); err != nil {
			return nil, err
		}
		if tagsJSON != "" && tagsJSON != "{}" {
			_ = json.Unmarshal([]byte(tagsJSON), &doc.Tags)
		}
		docs = append(docs, doc)
	}
	return docs, rows.Err()
}

func (s *Store) GetParentChunk(ctx context.Context, chunkID int64) (*SearchResult, error) {
	var parentID *int64
	if err := s.db.QueryRowContext(ctx,
		`SELECT parent_id FROM chunks WHERE id = ?`, chunkID,
	).Scan(&parentID); err != nil {
		return nil, err
	}
	if parentID == nil {
		return nil, nil
	}
	return s.GetChunkByID(ctx, *parentID)
}

func (s *Store) EnrichWithParentContext(ctx context.Context, results []SearchResult) []SearchResult {
	needsParent := make(map[int64][]int)
	for i, r := range results {
		if r.ParentID != nil && r.ParentContext == "" {
			needsParent[*r.ParentID] = append(needsParent[*r.ParentID], i)
		}
	}
	if len(needsParent) == 0 {
		return results
	}

	for parentID, resultIndexes := range needsParent {
		parent, err := s.GetChunkByID(ctx, parentID)
		if err == nil && parent != nil {
			content := parent.ChunkContent
			if len(content) > 500 {
				content = truncateUTF8(content, 500) + "..."
			}
			for _, resultIdx := range resultIndexes {
				results[resultIdx].ParentContext = content
			}
		}
	}
	return results
}
