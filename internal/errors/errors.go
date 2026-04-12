// Package errors provides structured error types for the quant indexer.
// These sentinel errors enable consistent error handling and better
// programmatic error responses throughout the codebase.
package errors

import "errors"

// Embedding errors
var (
	// ErrEmbeddingUnavailable indicates the embedding service is not reachable.
	ErrEmbeddingUnavailable = errors.New("embedding service unavailable")

	// ErrEmbeddingTimeout indicates an embedding request timed out.
	ErrEmbeddingTimeout = errors.New("embedding request timed out")

	// ErrEmbeddingDimensionMismatch indicates stored embeddings have different dimensions.
	ErrEmbeddingDimensionMismatch = errors.New("embedding dimension mismatch")

	// ErrEmbeddingModelChanged indicates the embedding model configuration changed.
	ErrEmbeddingModelChanged = errors.New("embedding model changed")
)

// Index errors
var (
	// ErrIndexCorrupted indicates the index database is in an invalid state.
	ErrIndexCorrupted = errors.New("index corruption detected")

	// ErrIndexLocked indicates the index is locked by another process.
	ErrIndexLocked = errors.New("index locked by another process")

	// ErrIndexMigrationFailed indicates a database migration failed.
	ErrIndexMigrationFailed = errors.New("index migration failed")

	// ErrDocumentNotFound indicates the requested document doesn't exist.
	ErrDocumentNotFound = errors.New("document not found")

	// ErrChunkNotFound indicates the requested chunk doesn't exist.
	ErrChunkNotFound = errors.New("chunk not found")
)

// Configuration errors
var (
	// ErrConfigInvalid indicates the configuration is invalid.
	ErrConfigInvalid = errors.New("invalid configuration")

	// ErrConfigWatchDirNotFound indicates the watch directory doesn't exist.
	ErrConfigWatchDirNotFound = errors.New("watch directory not found")

	// ErrConfigDBPathInvalid indicates the database path is invalid.
	ErrConfigDBPathInvalid = errors.New("database path invalid")
)

// Extraction errors
var (
	// ErrExtractionFailed indicates text extraction from a file failed.
	ErrExtractionFailed = errors.New("text extraction failed")

	// ErrExtractionUnsupported indicates the file type is not supported.
	ErrExtractionUnsupported = errors.New("file type not supported for extraction")

	// ErrOCRFailed indicates OCR processing failed.
	ErrOCRFailed = errors.New("OCR processing failed")

	// ErrOCRUnavailable indicates OCR tools are not installed.
	ErrOCRUnavailable = errors.New("OCR tools not available")
)

// Search errors
var (
	// ErrSearchFailed indicates a search operation failed.
	ErrSearchFailed = errors.New("search failed")

	// ErrSearchQueryEmpty indicates the search query was empty.
	ErrSearchQueryEmpty = errors.New("search query is empty")

	// ErrSearchQueryTooLong indicates the search query exceeded maximum length.
	ErrSearchQueryTooLong = errors.New("search query too long")
)

// Filesystem errors
var (
	// ErrPathOutsideWatchDir indicates a path is outside the watch directory.
	ErrPathOutsideWatchDir = errors.New("path outside watch directory")

	// ErrWatcherOverflow indicates the filesystem watcher event buffer overflowed.
	ErrWatcherOverflow = errors.New("watcher event buffer overflow")
)

// Circuit breaker errors
var (
	// ErrCircuitOpen indicates the circuit breaker is open.
	ErrCircuitOpen = errors.New("circuit breaker open")
)

// Is wraps errors.Is for convenience.
func Is(err, target error) bool {
	return errors.Is(err, target)
}

// As wraps errors.As for convenience.
func As(err error, target any) bool {
	return errors.As(err, target)
}

// Join wraps errors.Join for convenience.
func Join(errs ...error) error {
	return errors.Join(errs...)
}

// New wraps errors.New for convenience.
func New(text string) error {
	return errors.New(text)
}
