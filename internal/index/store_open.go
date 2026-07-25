package index

import (
	"context"
	"database/sql"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"net/url"
	"os"
	"path/filepath"
	"runtime"
	"strings"

	"github.com/koltyakov/quant/internal/logx"
	"modernc.org/sqlite"
	sqlite3 "modernc.org/sqlite/lib"
)

// Store opening: path classification, foreign-database detection, and
// backup/recovery of incompatible databases.

func resolveStorePath(dbPath string) (string, error) {
	info, err := os.Lstat(dbPath)
	if os.IsNotExist(err) {
		return dbPath, nil
	}
	if err != nil {
		return "", fmt.Errorf("inspecting database path: %w", err)
	}
	if info.Mode()&os.ModeSymlink == 0 {
		return dbPath, nil
	}
	resolved, err := filepath.EvalSymlinks(dbPath)
	if err != nil {
		return "", fmt.Errorf("resolving database symlink: %w", err)
	}
	return resolved, nil
}

func classifyStorePath(dbPath string) (storePathKind, error) {
	info, err := os.Stat(dbPath)
	if os.IsNotExist(err) {
		return storePathFresh, nil
	}
	if err != nil {
		return storePathFresh, fmt.Errorf("stating database: %w", err)
	}
	if !info.Mode().IsRegular() {
		return storePathFresh, fmt.Errorf("%w: %q is not a regular file", ErrNotQuantDatabase, dbPath)
	}
	if info.Size() == 0 {
		return storePathFresh, nil
	}
	headerApplicationID, err := sqliteHeaderApplicationID(dbPath)
	if err != nil {
		return storePathFresh, err
	}
	headerClaimsQuant := headerApplicationID == quantApplicationID
	if headerApplicationID != 0 && !headerClaimsQuant {
		return storePathFresh, fmt.Errorf("%w: %q has application_id %d", ErrNotQuantDatabase, dbPath, headerApplicationID)
	}

	db, err := sql.Open("sqlite", readOnlySQLiteDSN(dbPath))
	if err != nil {
		return storePathFresh, fmt.Errorf("opening %q read-only: %w", dbPath, err)
	}
	defer func() { _ = db.Close() }()
	db.SetMaxOpenConns(1)

	ctx := context.Background()
	var applicationID int64
	if err := db.QueryRowContext(ctx, `PRAGMA application_id`).Scan(&applicationID); err != nil {
		if headerClaimsQuant {
			return storePathQuant, nil
		}
		var sqliteErr *sqlite.Error
		if errors.As(err, &sqliteErr) && sqliteErr.Code()&0xff == sqlite3.SQLITE_NOTADB {
			return storePathFresh, fmt.Errorf("%w: inspecting %q: %w", ErrNotQuantDatabase, dbPath, err)
		}
		return storePathFresh, fmt.Errorf("inspecting database identity in %q: %w", dbPath, err)
	}
	if applicationID == quantApplicationID || (applicationID == 0 && headerClaimsQuant) {
		return storePathQuant, nil
	}
	if applicationID != 0 {
		return storePathFresh, fmt.Errorf("%w: %q has application_id %d", ErrNotQuantDatabase, dbPath, applicationID)
	}

	legacy, err := hasLegacyQuantSchema(ctx, db)
	if err != nil {
		return storePathFresh, fmt.Errorf("inspecting legacy schema in %q: %w", dbPath, err)
	}
	if !legacy {
		return storePathFresh, fmt.Errorf("%w: %q has no quant schema marker", ErrNotQuantDatabase, dbPath)
	}
	return storePathQuant, nil
}

func sqliteHeaderApplicationID(dbPath string) (int64, error) {
	//nolint:gosec // The database path is explicitly configured and inspected before opening.
	f, err := os.Open(dbPath)
	if err != nil {
		return 0, fmt.Errorf("reading database header: %w", err)
	}
	defer func() { _ = f.Close() }()

	var header [72]byte
	if _, err := io.ReadFull(f, header[:]); err != nil {
		return 0, fmt.Errorf("%w: %q has an invalid SQLite header: %w", ErrNotQuantDatabase, dbPath, err)
	}
	if string(header[:16]) != "SQLite format 3\x00" {
		return 0, fmt.Errorf("%w: %q has an invalid SQLite header", ErrNotQuantDatabase, dbPath)
	}
	return int64(binary.BigEndian.Uint32(header[68:72])), nil
}

func readOnlySQLiteDSN(dbPath string) string {
	absPath, err := filepath.Abs(dbPath)
	if err != nil {
		absPath = dbPath
	}
	path := filepath.ToSlash(absPath)
	if runtime.GOOS == "windows" && !strings.HasPrefix(path, "/") {
		path = "/" + path
	}
	u := url.URL{Scheme: "file", Path: path}
	query := u.Query()
	query.Set("mode", "ro")
	u.RawQuery = query.Encode()
	return u.String()
}

func hasLegacyQuantSchema(ctx context.Context, db *sql.DB) (bool, error) {
	required := []struct {
		table   string
		columns []string
	}{
		{table: "documents", columns: []string{"id", "path", "hash", "modified_at", "indexed_at"}},
		{table: "chunks", columns: []string{"id", "document_id", "content", "chunk_index", "embedding"}},
		{table: "embedding_metadata", columns: []string{"key", "value"}},
	}

	for _, schema := range required {
		var tableCount int
		if err := db.QueryRowContext(ctx,
			`SELECT COUNT(*) FROM sqlite_master WHERE type = 'table' AND name = ?`, schema.table,
		).Scan(&tableCount); err != nil {
			return false, err
		}
		if tableCount != 1 {
			return false, nil
		}

		rows, err := db.QueryContext(ctx, `SELECT name FROM pragma_table_info(?)`, schema.table)
		if err != nil {
			return false, err
		}
		columns := make(map[string]struct{}, len(schema.columns))
		for rows.Next() {
			var column string
			if err := rows.Scan(&column); err != nil {
				_ = rows.Close()
				return false, err
			}
			columns[column] = struct{}{}
		}
		rowsErr := rows.Err()
		_ = rows.Close()
		if rowsErr != nil {
			return false, rowsErr
		}
		for _, column := range schema.columns {
			if _, ok := columns[column]; !ok {
				return false, nil
			}
		}
	}

	var ftsSQL string
	if err := db.QueryRowContext(ctx,
		`SELECT sql FROM sqlite_master WHERE type = 'table' AND name = 'chunks_fts'`,
	).Scan(&ftsSQL); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return false, nil
		}
		return false, err
	}
	normalizedFTSSQL := strings.ToLower(ftsSQL)
	if !strings.Contains(normalizedFTSSQL, "virtual table") ||
		!strings.Contains(normalizedFTSSQL, "using fts5") ||
		!strings.Contains(normalizedFTSSQL, "content='chunks'") {
		return false, nil
	}
	var triggerCount int
	if err := db.QueryRowContext(ctx, `
		SELECT COUNT(*) FROM sqlite_master
		WHERE type = 'trigger' AND tbl_name = 'chunks'
		AND name IN ('chunks_ai', 'chunks_ad', 'chunks_au')
	`).Scan(&triggerCount); err != nil {
		return false, err
	}
	if triggerCount != 3 {
		return false, nil
	}
	return true, nil
}

func (s *Store) ensureQuantApplicationID(ctx context.Context) error {
	var applicationID int64
	if err := s.db.QueryRowContext(ctx, `PRAGMA application_id`).Scan(&applicationID); err != nil {
		return fmt.Errorf("reading sqlite application id: %w", err)
	}
	if applicationID == quantApplicationID {
		return nil
	}
	if applicationID != 0 {
		return fmt.Errorf("%w: unexpected application_id %d", ErrNotQuantDatabase, applicationID)
	}
	if _, err := s.db.ExecContext(ctx, fmt.Sprintf(`PRAGMA application_id = %d`, quantApplicationID)); err != nil {
		return fmt.Errorf("setting sqlite application id: %w", err)
	}
	return nil
}

func isRecoverableStoreError(err error) bool {
	var sqliteErr *sqlite.Error
	if !errors.As(err, &sqliteErr) {
		return false
	}

	switch sqliteErr.Code() & 0xff {
	case sqlite3.SQLITE_ERROR, sqlite3.SQLITE_CORRUPT, sqlite3.SQLITE_SCHEMA, sqlite3.SQLITE_NOTADB:
		return true
	default:
		return false
	}
}

func backupStoreFiles(dbPath, backupPath string) error {
	suffixes := []string{"", "-wal", "-shm"}
	for _, suffix := range suffixes {
		if err := os.Remove(backupPath + suffix); err != nil && !os.IsNotExist(err) {
			return fmt.Errorf("removing stale backup %q: %w", backupPath+suffix, err)
		}
	}
	renamed := make([]string, 0, len(suffixes))
	for _, suffix := range suffixes {
		if err := os.Rename(dbPath+suffix, backupPath+suffix); err != nil {
			if suffix != "" && os.IsNotExist(err) {
				continue
			}
			backupErr := fmt.Errorf("renaming %q to %q: %w", dbPath+suffix, backupPath+suffix, err)
			for i := len(renamed) - 1; i >= 0; i-- {
				movedSuffix := renamed[i]
				if rollbackErr := os.Rename(backupPath+movedSuffix, dbPath+movedSuffix); rollbackErr != nil {
					backupErr = errors.Join(backupErr, fmt.Errorf("restoring %q: %w", dbPath+movedSuffix, rollbackErr))
				}
			}
			return backupErr
		}
		renamed = append(renamed, suffix)
	}
	return nil
}

// RemoveBackup deletes the backup created during NewStore, if any.
func (s *Store) RemoveBackup() {
	if s.backup == "" {
		return
	}
	for _, suffix := range []string{"", "-wal", "-shm"} {
		_ = os.Remove(s.backup + suffix)
	}
	logx.Info("removed database backup", "path", s.backup)
	s.backup = ""
}

func openStore(dbPath string) (*Store, error) {
	if err := os.MkdirAll(filepath.Dir(dbPath), 0750); err != nil {
		return nil, fmt.Errorf("creating database directory: %w", err)
	}

	db, err := sql.Open("sqlite", dbPath)
	if err != nil {
		return nil, fmt.Errorf("opening database: %w", err)
	}
	conns := runtime.GOMAXPROCS(0)
	if conns < 4 {
		conns = 4
	}
	if conns > 16 {
		conns = 16
	}
	db.SetMaxOpenConns(conns)
	db.SetMaxIdleConns(conns / 2)
	db.SetConnMaxLifetime(defaultSQLiteConnMaxLifetime)
	db.SetConnMaxIdleTime(defaultSQLiteConnMaxIdleTime)

	s := &Store{
		db:                        db,
		dbPath:                    dbPath,
		maxVectorSearchCandidates: defaultMaxVectorSearchCandidates,
		hnsw:                      newHNSWIndex(),
		hnswM:                     defaultHNSWM,
		hnswEfSearch:              defaultHNSWEfSearch,
		hnswGraphPath:             dbPath + ".hnsw",
		docEmbeds:                 newDocEmbeddingIndex(),
	}
	for _, pragma := range []string{
		`PRAGMA journal_mode = WAL`,
		`PRAGMA synchronous = NORMAL`,
		`PRAGMA temp_store = MEMORY`,
		`PRAGMA busy_timeout = 5000`,
		`PRAGMA foreign_keys = ON`,
		`PRAGMA mmap_size = 268435456`,
		`PRAGMA cache_size = -64000`,
	} {
		if _, err := s.db.Exec(pragma); err != nil {
			_ = db.Close()
			return nil, fmt.Errorf("configuring sqlite pragma %q: %w", pragma, err)
		}
	}
	if err := s.migrate(); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("running migrations: %w", err)
	}
	if err := s.cleanupOrphanedChunks(context.Background()); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("cleaning orphaned chunks: %w", err)
	}

	return s, nil
}
