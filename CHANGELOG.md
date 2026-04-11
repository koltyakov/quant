# Changelog

## v0.5.0 (2026-04-11)

### Features

- **Code-aware chunk splitting and HNSW indexing** - Chunking is now structure-aware for source code and uses HNSW for vector similarity search, improving retrieval quality and speed.
- **Max vector candidates configuration** - New `max_vector_candidates` setting with validation lets you control how many vector candidates are considered during hybrid search.
- **Health and readiness endpoints** - The MCP server now exposes `/health` and `/ready` HTTP endpoints for monitoring and orchestrators.
- **MCP server version support** - Server version is now reported through the MCP protocol for easier debugging and compatibility checks.
- **Improved CLI error messages** - Unknown commands and unexpected arguments now produce clear, actionable error messages.

### Improvements

- Configuration and embedding model documentation added; README reorganized for clarity.
- Logging refactored for consistency and improved error handling across modules.
- Text extractors refactored to support context propagation and better error reporting.
- Text extraction now uses truthiness checks for more robust content handling.

## v0.4.0 (2026-04-10)

### Features

- **Retry mechanism for transient indexing failures** - Indexing operations now automatically retry on transient errors, making the indexing pipeline more resilient.
- **Enhanced initial sync with failure reporting** - The initial filesystem scan now reports per-file failures so you can see what was skipped and why.
- **YAML config path resolution** - Relative and absolute paths in YAML configuration files are now resolved correctly.
- **Ollama integration with context support** - Ollama API calls now respect context deadlines and cancellation signals.
- **Graceful server shutdown with timeout** - The MCP server shuts down gracefully with a configurable timeout, draining in-flight requests.

### Improvements

- Benchmarks added for chunk splitting and indexing performance.
- PDF OCR support with configurable language options via `ocrmypdf`.
- Improved error handling in indexer and scan packages with expanded edge-case tests.
- Config, watcher, and embedding edge cases handled more robustly.
- Tests added for PPTX extraction order and notebook output deduplication.

## v0.3.0 (2026-04-09)

### Features

- **Auto-update functionality** - Quant can now check for and apply the latest GitHub release automatically via `quant update`.
- **Rotating log writer** - Logs rotate automatically with configurable size limits and retention.
- **Search request and result logging** - Search queries and their results are now logged for debugging and auditing.
- **Logging configuration** - Configurable log levels and output paths; log files are excluded from indexing.

### Improvements

- Refactored CLI commands; enhanced README documentation; implemented dedicated MCP command structure.
- Version command added with updated version handling across the codebase.
- Database path updated to use `.index` directory; parent directories are created automatically.
- Permission configuration added for quant commands.
- Install target added to Makefile for easier binary installation.

## v0.2.0 (2026-04-08)

### Features

- **PDF OCR support** - Automatic OCR fallback for scanned PDFs using `ocrmypdf` with configurable language options.

### Improvements

- Refactored OOXML extraction logic to use type-based handling with improved test coverage.
- README updated with enhanced runtime requirements and deployment model sections.

## v0.1.0 (2026-04-08)

Initial release.

- Core document indexing and semantic search pipeline - file extraction, chunking, embedding via Ollama, SQLite storage, and MCP server.
- Live indexing with filesystem watching, event debouncing, and LRU cache for embeddings.
- Parallel indexing with configurable worker count.
- Jupyter notebook and OpenDocument file extraction.
- Hybrid retrieval using SQLite FTS5 prefilter with normalized vector rerank.
- Nested `.gitignore` support during indexing.
- CI and release workflows with GoReleaser.
- Background initial scan with relative path storage.
- WAL-mode SQLite with busy timeout and multi-connection pool for concurrent reads during writes.
