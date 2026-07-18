package index

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"os"
	"sync"
	"time"

	"github.com/koltyakov/quant/internal/logx"
)

// Store is a SQLite-backed index holding documents, chunks, embeddings, and an HNSW graph for vector search.
type Store struct {
	db                        *sql.DB
	dbPath                    string
	backup                    string
	maxVectorSearchCandidates int
	hnsw                      *hnswIndex
	hnswM                     int
	hnswEfSearch              int
	hnswGraphPath             string
	keywordWeightOverride     float32
	vectorWeightOverride      float32
	docEmbeds                 *docEmbeddingIndex
	reranker                  Reranker

	writeMu sync.Mutex
}

const defaultMaxVectorSearchCandidates = 20000

const (
	defaultHNSWM        = 16
	defaultHNSWEfSearch = 100
)

const (
	defaultSQLiteConnMaxLifetime = time.Hour
	defaultSQLiteConnMaxIdleTime = 15 * time.Minute
)

const quantApplicationID int64 = 0x514E5431 // "QNT1"

// ErrNotQuantDatabase indicates that an existing path is not owned by quant.
var ErrNotQuantDatabase = errors.New("existing database is not a quant database")

type storePathKind uint8

const (
	storePathFresh storePathKind = iota
	storePathQuant
)

// NewStore opens (or creates) a SQLite database at dbPath.
// If a recognized quant database exists but migration fails, the old file is
// backed up and a fresh database is created. Unknown files are never replaced.
func NewStore(dbPath string) (*Store, error) {
	effectiveDBPath, err := resolveStorePath(dbPath)
	if err != nil {
		return nil, err
	}
	pathKind, err := classifyStorePath(effectiveDBPath)
	if err != nil {
		return nil, err
	}

	s, err := openStore(effectiveDBPath)
	if err == nil {
		return s, nil
	}
	if pathKind != storePathQuant || !isRecoverableStoreError(err) {
		return nil, err
	}

	if _, statErr := os.Stat(effectiveDBPath); statErr != nil {
		return nil, errors.Join(err, fmt.Errorf("stating database before recovery: %w", statErr))
	}

	// Back up the broken DB and start fresh.
	backupPath := effectiveDBPath + ".bak"
	logx.Warn("migration failed; backing up existing database", "backup_path", backupPath, "err", err)

	if err := backupStoreFiles(effectiveDBPath, backupPath); err != nil {
		return nil, fmt.Errorf("backing up database after migration failure: %w", err)
	}

	s, err = openStore(effectiveDBPath)
	if err != nil {
		return nil, fmt.Errorf("creating fresh database after backup: %w", err)
	}
	s.backup = backupPath
	return s, nil
}

func (s *Store) Close() error {
	var err error
	if s != nil && s.db != nil {
		if flushErr := s.FlushHNSW(); flushErr != nil {
			logx.Warn("failed to flush hnsw graph on close", "err", flushErr)
		}
		if _, checkpointErr := s.db.Exec(`PRAGMA wal_checkpoint(TRUNCATE)`); checkpointErr != nil {
			err = errors.Join(err, fmt.Errorf("checkpointing sqlite wal: %w", checkpointErr))
		}
		err = errors.Join(err, s.db.Close())
	}
	return err
}

func (s *Store) PingContext(ctx context.Context) error {
	return s.db.PingContext(ctx)
}

func (s *Store) SetMaxVectorSearchCandidates(max int) {
	s.maxVectorSearchCandidates = max
}

func (s *Store) SetHNSWParams(m, efSearch int) {
	if m > 0 {
		s.hnswM = m
	}
	if efSearch > 0 {
		s.hnswEfSearch = efSearch
	}
}

func (s *Store) HNSWReoptimizationNeeded(threshold float64) bool {
	if s.hnsw == nil || !s.hnsw.ready.Load() {
		return false
	}
	total := s.hnsw.Len()
	if total == 0 {
		return false
	}
	return float64(s.hnsw.modCount())/float64(total) > threshold
}

func (s *Store) SetWeightOverrides(keyword, vector float32) {
	s.keywordWeightOverride = keyword
	s.vectorWeightOverride = vector
}

func (s *Store) SetReranker(r Reranker) {
	s.reranker = r
}

func (s *Store) resetRuntimeIndexes(removeGraphFile bool) {
	if s.hnsw != nil {
		s.hnsw.ready.Store(false)
		s.hnsw.mu.Lock()
		s.hnsw.graph = newGraph(s.hnswM, s.hnswEfSearch)
		s.hnsw.mu.Unlock()
		s.hnsw.generation.Store(-1)
		s.hnsw.resetMods()
	}
	if s.docEmbeds != nil {
		s.docEmbeds.Clear()
	}
	if removeGraphFile && s.hnswGraphPath != "" {
		_ = os.Remove(s.hnswGraphPath)
	}
}
