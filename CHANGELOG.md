# Changelog

## v0.10.0 (2026-04-15)

### Features

- **Tree-sitter chunkers for multiple languages** - New structure-aware chunkers for C, Java, JavaScript, Python, and Rust using Tree-sitter, producing higher-quality chunks that respect code syntax (functions, classes, imports, etc.) rather than splitting on plain newlines.
- **HTML file extraction** - A new `HTMLExtractor` processes `.html` and `.htm` files, extracting readable text content while stripping tags, scripts, and style blocks.
- **Periodic database vacuuming** - The indexer now vacuums the SQLite database on a configurable schedule to reclaim disk space and optimize query performance after heavy indexing or deletion workloads.
- **Proxy server embedding support** - The proxy server now handles embedding and collection management requests, allowing remote proxy clients to trigger embeddings and manage collections through the proxy layer.

### Improvements

- **Comprehensive test coverage** - Added extensive unit tests across the codebase covering the indexer (initial sync, resync, watch, live indexing), embedding (caching, rate limiting, backoff, retry, providers), extraction (PDF, OOXML, ODF, RTF, HTML), MCP server lifecycle and tools, proxy client/server, locking (contention, timeout), HNSW operations, store CRUD/search/migration/dedup, self-update, and more.
- **Install script** - New `scripts/install.sh` provides a one-command installation experience documented in the README.

## v0.9.0 (2026-04-13)

### Features

- **Ollama auto-start and auto-pull** - When the embedding backend is unavailable at startup, `quant` automatically starts Ollama (`ollama serve`) if it is installed locally and the embed URL points to localhost, then pulls the configured model if it is missing. Both recovery steps happen before the MCP server opens, so clients connect to a fully operational server without manual setup.
- **Keyword-only degraded mode** - If embedding recovery fails (Ollama not installed, remote URL unreachable, or network error), `quant` starts without an embedding backend. The MCP server is fully operational; search falls back to FTS5 keyword results and `index_status` reports the embedding status and the fix needed.
- **OpenAI-compatible embedding provider** - A new `openai` provider supports any API that follows the OpenAI embeddings contract. Select it via `--embed-provider openai` (or `QUANT_EMBED_PROVIDER=openai`); auto-detected when `embed_url` contains `openai.com`.
- **Embedding API key support** - New `--embed-api-key` flag and `QUANT_EMBED_API_KEY` environment variable pass a Bearer token to the embedding backend, enabling authenticated providers such as OpenAI.
- **`drill_down` MCP tool** - Explores a topic by finding diverse chunks related to a seed chunk, spreading results across different source files rather than staying within one document.
- **`summarize_matches` MCP tool** - Returns a high-level overview of which documents match a query and what they contain, without returning individual chunks.
- **`list_collections` and `delete_collection` MCP tools** - List named collections with document and chunk counts; delete all content in a named collection.
- **Embedding status in `index_status`** - The `index_status` tool now reports whether the embedding backend is available or the server is running in keyword-only mode.
- **Chunk summarization** - New `--summarizer` flag enables LLM-powered summarization of indexed chunks at ingest time. The summarization model is configurable via `--summarizer-model`.
- **Cross-encoder reranking** - New `--reranker cross-encoder` flag adds a cross-encoder reranking pass after hybrid retrieval. The reranker model is configurable via `--reranker-model`.

### Improvements

- **Actionable Ollama error messages** - Connection errors and 404 model-not-found responses now carry human-readable messages with the exact `ollama serve` / `ollama pull` commands to run.
- **Ollama process isolation** - When `quant` starts Ollama automatically, the child process is placed in its own process group (`Setpgid: true` on Linux/macOS) so Ctrl+C in the terminal does not kill Ollama along with `quant`.

## v0.8.0 (2026-04-12)

### Features

- **Proxy server for multi-process locking** - A new proxy server and client enable coordinated access to a shared Quant index across multiple processes, replacing the previous heartbeat-based lock with a simpler RPC mechanism.
- **Dynamic memory management** - Memory limits are now computed dynamically based on platform and available system memory, and integrated into the indexing pipeline to prevent OOM conditions during large batch operations.
- **PDF content extraction** - PDF files are now inspected and extracted with structure-aware logic that preserves text from illustrated narratives and other complex layouts, with dedicated test coverage.
- **Oversized file handling** - Files exceeding configurable size limits are skipped during indexing with a new `ErrFileTooLarge` sentinel error; the Ollama embed batch also trims oversized content to stay within token budgets.
- **Quarantine for permanent failures** - Documents that fail with permanent errors (e.g., retry budget exceeded, embedding failures) are quarantined and excluded from future indexing attempts, preventing wasteful retries.
- **FTS diagnostics** - A new `FTSDiagnostics` struct and provider expose FTS index state for monitoring and debugging.
- **Quarantine-aware path matching** - The default path matcher now excludes quarantine directories from indexing.

### Improvements

- **Simplified lock management** - Heartbeat functionality removed from the locking mechanism in favor of the new proxy-based approach.
- **FTS rebuild refactor** - FTS rebuilding logic extracted into a dedicated function for clarity and maintainability.
- **Log file permissions** - Log files are now created with appropriate permissions and improved context propagation in PDF extraction.
- **Orphaned chunk cleanup** - Deleting a document now also cleans up any orphaned chunks left in the database.

## v0.7.0 (2026-04-11)

### Features

- **Index state tracking** - The indexer now tracks its lifecycle (idle, syncing, live) and exposes it through the `index_status` MCP tool, giving clients real-time visibility into whether indexing is in progress.
- **Structured MCP tool responses** - `search`, `list_sources`, and `index_status` return typed JSON objects instead of plain text, making results easier to parse and display in tool-calling clients.
- **Rate-limited embedding** - Embedding requests are now rate-limited internally to avoid overwhelming the Ollama backend during bulk indexing.
- **Health and readiness endpoints** - `/health` and `/ready` HTTP endpoints are now served alongside the MCP server for use by orchestrators and process monitors.
- **Pluggable chunk splitter registry** - Chunk splitters are now registered centrally, making it straightforward to add new language-aware splitters without modifying the core pipeline.
- **Configurable file pattern filtering** - Include and exclude glob patterns can be specified in config to control which files are indexed.

### Improvements

- **`CachingEmbedder` decorator** - LRU cache, single-flight deduplication, and circuit breaker for embedding requests are now encapsulated in a reusable `embed.CachingEmbedder` wrapper rather than scattered across the MCP server.
- **Indexer constructor with private fields** - `NewIndexer(IndexerConfig)` now wires all internal components (pipeline, path tracker, live queue, retry scheduler, state tracker) internally; callers supply only external dependencies.
- **Retriever collapsed into Store** - The `Retriever` indirection layer was removed; hybrid search logic lives directly in `Store.Search` and `Store.FindSimilar`.
- **Batch index operations** - Documents can be added and deleted in batches, reducing per-document overhead during initial sync.
- **Score normalization** - Search scores are normalized before RRF fusion for more consistent ranking across result sets of different sizes.
- **Embedding budget enforcement** - Chunks are trimmed to fit within the embedding model's token budget during ingest, preventing silent truncation at the API level.
- **int8 quantization fix** - Corrected `dotProductEncoded` for int8-quantized vectors, fixing potential scoring errors on quantized embeddings.

## v0.6.0 (2026-04-11)

### Features

- **Embedding metadata management** - Index now tracks embedding model, dimensions, and normalization state, automatically triggering a rebuild when metadata changes.
- **Path synchronization and retry mechanisms** - Document path renames are handled correctly during indexing, and transient failures are retried automatically.
- **Debounced HNSW graph flush** - HNSW graph writes to SQLite are debounced to reduce disk I/O during rapid indexing while preserving crash recovery via a dirty flag.

### Improvements

- **Enhanced test coverage** - Comprehensive new tests for chunk splitting (Go, code-aware), ingest pipeline, encoding, ranking, semver, RTF extraction, and MCP tool formatting. Coverage improved from 56.7% to 64.1%.
- **Improved chunk breadcrumb context** - Heading context propagation during chunking is more robust for deeply nested markdown structures.
- **Better search fallback handling** - Vector search fallback is more resilient when HNSW is unavailable or partially built.

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
