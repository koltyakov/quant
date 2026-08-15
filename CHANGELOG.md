# Changelog

## Unreleased

### Security

- **Authenticated remote MCP transports** - SSE and streamable HTTP accept bearer authentication and refuse non-loopback listeners without a configured token.
- **Verified release downloads** - Installers and self-update verify release archives against `checksums.txt` before extraction or binary replacement.
- **Safer filesystem and document parsing** - Live indexing rejects symlinks and root escapes; ZIP-based extractors enforce entry, expansion, compression-ratio, and output budgets.
- **Secure network defaults** - Non-loopback model endpoints require HTTPS unless explicitly overridden, HTTP servers bound slow request reads, and CI/release actions use least privilege with commit-pinned dependencies.
- **Current Go security fixes** - Release builds require Go 1.26.6 and CI runs `govulncheck` on pushes, pull requests, and a weekly schedule.

## v0.15.4 (2026-07-29)

### Security

- **Authenticated internal proxy** - Main/worker proxy routes now require a per-process random bearer token shared through database lock metadata, reject browser-origin requests, and cap requests at 1 MiB.
- **Restricted lock metadata** - Unix lock files use `0600` permissions, including existing files tightened before proxy credentials are written.
- **Safer provider detection and extraction** - OpenAI LLM auto-detection accepts only OpenAI domains, extractor panics become indexing errors, and HTML reads remain bounded.

### Improvements

- **CLI-first configuration precedence** - Explicit command-line flags now override environment variables, which override YAML and built-in defaults.
- **More precise backend retries** - Embedding and LLM clients retry rate limits, server failures, and transient transport errors while returning permanent client errors immediately for indexer handling.
- **Exact embedding reuse and migration** - Deduplication keys now include the complete heading-plus-content model input. The input format is versioned so older indexes receive a one-time rebuild when embeddings are available.
- **More available and accurate HNSW search** - Graph builds use generation-checked snapshots, unavailable graphs are rebuilt by periodic maintenance, and ready HNSW searches no longer discard nearest chunks through document prefiltering.
- **More reliable live indexing** - Populated directories moved or created in scope emit file events, temporarily unwatched subtrees are retried and resynced, and continuously changing files are requeued instead of monopolizing a worker.
- **Reliable startup readiness** - Resyncs racing initial startup no longer bypass completion, readiness publication, backup cleanup, or initial HNSW loading.
- **Consistent SQLite connections** - Required busy timeout, foreign-key, cache, memory-map, and related pragmas are applied to every pooled connection.
- **UTF-8-safe LLM inputs** - Reranker and summarizer truncation stops at valid UTF-8 boundaries.

## v0.15.2 (2026-07-26)

### Improvements

- **`find_similar` fallback without HNSW** - Similarity lookup uses a bounded exact vector scan while HNSW is unavailable and respects `--max-vector-candidates`.
- **Scalable filtered vector search** - Candidate queries are batched below SQLite's variable limit, individual HNSW probes are bounded, and exact filtered fallback fills missing results when allowed.
- **Faster embedding reuse** - Content-deduplication reads and writes are batched per document; cache failures fall back to embedding affected chunks instead of failing indexing.
- **Lower indexing overhead** - Embedding metadata, quarantine snapshots, chunk splitting, word counting, and file hashing avoid repeated work and allocations.
- **Portable stored paths** - New document keys consistently use forward slashes across platforms while legacy Windows-form paths remain resolvable.
- **More reliable Windows watching and locking** - Lock regions no longer overlap metadata writes, and debounced directory removals correctly remove descendants.
- **Dependency updates** - Upgraded `mcp-go` to v0.57.0 and SQLite to v1.54.0.

## v0.15.1 (2026-07-17)

### Security

- **Protection for unrelated databases** - Existing database paths are identified as Quant indexes before recovery. Unknown files and non-Quant SQLite databases are rejected instead of replaced.
- **Database-scoped process locking** - Canonical database paths, including symlink aliases, share one lock while separate databases can run independently.
- **Configuration compatibility checks** - Workers refuse to attach to a process serving the same database with incompatible index-affecting configuration.

### Improvements

- **Generation-safe HNSW persistence** - Every chunk mutation advances a transactional generation, and persisted graphs load only when their generation matches SQLite.
- **Safer incremental graph updates** - Runtime HNSW changes occur only after successful commits and invalidate the graph when a complete update cannot be guaranteed.
- **Reliable empty-graph recovery** - HNSW accepts new nodes after all previous nodes have been deleted.
- **Less expensive deletion** - FTS triggers preserve search consistency without rebuilding the full-text index after each document, prefix, collection, or reset deletion.

## v0.15.0 (2026-07-12)

### Features

- **Ordered context retrieval** - Added the `get_context` MCP tool for expanding a search hit with preceding and following chunks from the same document. Context retrieval preserves source order, duplicate chunks, document boundaries, structured metadata, and proxy-mode support.
- **Collection-aware MCP search** - The `search` tool now accepts an exact collection filter and normalizes common file type aliases and language values. Structured responses echo the effective path, file type, language, and collection filters.
- **Stricter MCP tool contracts** - All tools now publish explicit safety annotations and constrained input schemas, reject unknown arguments, and use server-side schema validation and panic recovery.

### Improvements

- **Correct retrieval score semantics** - Keyword-only searches no longer receive a synthetic vector contribution, normalized scores remain within their documented range, and `find_similar` returns actual vector similarity scores.
- **Better filtered vector recall** - Valid HNSW neighbors are retained when fewer than the requested limit survive a filter and exact fallback is disabled or exceeds its candidate cap.
- **Deterministic ranking** - Equal-score keyword, vector, document, and heap candidates now use stable path, chunk index, and chunk ID tie-breakers; FTS queries also use deterministic secondary ordering.
- **More precise path ranking** - Path boosts match exact path and identifier components rather than arbitrary substrings, preventing queries such as `go` or `auth` from boosting unrelated names such as `logo` or `oauth`.
- **More diverse topic exploration** - `drill_down` overfetches candidates and prioritizes one result per new document before filling with secondary or seed-document chunks. Fractional IDs and invalid limits are rejected consistently.
- **Clearer match overviews** - `summarize_matches` now describes its bounded, non-exhaustive behavior accurately and reports the effective limit, chunk and document counts, embedding mode, and keyword-only fallback note.
- **Safer embedding ingestion** - Empty, wrong-dimension, NaN, and infinite embedding vectors are rejected before quantization or storage. Embedding-budget splits preserve chunk hierarchy and section metadata.
- **UTF-8-safe truncation** - Oversized text extraction and code signature limits now stop at valid rune boundaries instead of potentially storing malformed multibyte text.
- **Coherent health snapshots** - Aggregated health checks execute each dependency probe once, derive status from that same result set, and report durations as actual milliseconds.

### Security

- **Hardened local HTTP transports** - Streamable HTTP and legacy SSE reject browser-origin requests before tool dispatch, cap request bodies at 1 MiB, and use `mcp-go` v0.56.0 for default localhost DNS-rebinding protection and safer SSE session shutdown.

## v0.14.1 (2026-07-10)

### Security

- **Loopback-only HTTP defaults** - SSE and streamable HTTP now listen on `127.0.0.1:8080` by default; documentation warns that deliberate network exposure requires external authentication.
- **Safer filesystem scans** - Initial scans skip symbolic-link entries.
- **Stronger process locking** - Lock ownership relies on operating-system locks rather than replacing lock files based on stale metadata, with corrected Windows behavior.

### Improvements

- **Working filesystem metadata filters** - Indexed documents persist canonical file type and language metadata for `search` filters.
- **Complete keyword-only ingestion** - Chunks retain content and structural metadata when no embedding backend is available.
- **Faster candidate processing** - Search hydrates full content only for final results and improves document preselection and vector scoring.
- **Correct shared parent context** - Results sharing a parent all receive UTF-8-safe parent context.
- **Safer database recovery** - Automatic backup and recreation is limited to recognized corruption or schema failures and rolls back partial file moves.
- **Correct embedding-cache invalidation** - Embedding metadata changes clear content-deduplication entries from the previous model configuration.

## v0.14.0 (2026-07-02)

### Features

- **Expanded runtime configuration flags** - Added CLI coverage for PDF OCR timeout, vector fallback candidate limits, and MCP tool concurrency limits, with validation for embedding providers and runtime bounds.
- **More robust filtered hybrid search** - Vector fallback and HNSW candidate collection now honor metadata filters consistently, including path, file type, language, tags, and collection filters.

### Improvements

- **HNSW graph persistence hardening** - HNSW graph files are validated against stored model metadata, dimensions, and embedded chunk counts before loading; unchanged graphs skip redundant saves and exports are protected against concurrent mutation.
- **Cleaner index consistency after deletes and rewrites** - Document, prefix, and collection deletion paths now clear stale HNSW state and reload document embeddings so runtime indexes stay aligned with SQLite after mutations.
- **Safer incremental reindexing** - HNSW updates are deferred until after document transactions commit, and reused chunks preserve depth, section titles, and summaries during deduplication.
- **Exact tag filtering** - Tag filters now use SQLite JSON matching instead of substring patterns, avoiding wildcard and JSON-escaping false positives.
- **Embedding input cleanup** - Chunk embedding inputs now use heading and content only, avoiding document-path noise while keeping heading context.
- **Multibyte truncation fix** - Ollama input truncation now counts rune offsets correctly for multibyte text, preventing over-budget prefixes and panics.
- **Rate limiter accounting fix** - Embedding rate limiting now treats tokens and concurrency slots separately, so completed requests no longer refund consumed rate-limit tokens.
- **Client initialization polish** - `quant init` preserves quoted command paths with spaces and emits the current OpenCode `environment` field for auto-update configuration.
- **Collection delete validation** - Empty collection names are rejected before delete operations in both proxy and store paths.

### Removed

- **Unused experimental ranking components** - Removed dormant ColBERT, feedback boost, weight profile, and projection migration code and tests to simplify the index package around the active hybrid search path.

## v0.12.1 (2026-05-08)

### Improvements

- **Dependency updates** - Updated project dependencies to keep the release current with upstream fixes and improvements.
- **Uninstall scripts** - New `scripts/uninstall.sh` and `scripts/uninstall.ps1` remove release-installed binaries while leaving user data, MCP client configuration, and Ollama untouched.

## v0.12.0 (2026-04-16)

### Features

- **Shared LLM configuration for reranking and summarization** - New `--llm-url`, `--llm-model`, `--llm-provider`, and `--llm-api-key` settings let cross-encoder reranking and chunk summarization share a configurable LLM backend, with per-feature model overrides still available via `--reranker-model` and `--summarizer-model`.
- **OpenAI-compatible LLM support for search enhancements** - Reranking and summarization now work with either Ollama or OpenAI-compatible chat backends, making it possible to run embeddings and LLM-based search features against different providers.
- **Stronger configuration surface for search features** - Reranker and summarizer settings are now available consistently across CLI flags, YAML config, and environment variables, including explicit provider selection and API keys.
- **Expanded integration and troubleshooting docs** - Documentation now includes manual MCP setup examples for Cursor and Gemini plus a dedicated troubleshooting guide.

### Improvements

- **Cleaner configuration precedence** - Config loading now applies defaults, YAML, CLI flags, and environment variables in a predictable order, with broader coverage for provider, reranker, and summarizer settings.
- **Indexer path handling refactor** - A canonical `DocumentRef` flow now keeps document keys, absolute paths, retries, live queue entries, and quarantine behavior aligned across initial sync and watch-driven updates.
- **Runtime bootstrap cleanup** - Main-process startup now uses explicit bootstrap and runner types instead of a single oversized orchestration path, making lifecycle management and promotion behavior easier to reason about.
- **More robust shutdown behavior** - Main-process teardown and proxy shutdown are now idempotent, reducing duplicate shutdown races during promotion, restart, and test flows.

## v0.11.0 (2026-04-16)

### Features

- **Project initialization command** - `quant init <client>` generates MCP configuration files, instruction templates (AGENTS.md, CLAUDE.md), and skills for supported clients (opencode, codex, claude, cursor, copilot, gemini). Generated configs include tool permission rules so `quant` MCP tools are allowed without prompting.
- **Launch command** - `quant launch <client>` starts a supported agent with the `quant` MCP server injected for that process only, without writing persistent config. Supports session-level tool permission flags for clients that require them.
- **Windows install script** - New `scripts/install.ps1` provides a one-command PowerShell installation experience alongside the existing `scripts/install.sh`.
- **Ollama installation prompt** - When the embedding backend is unavailable, `quant` now prompts to install Ollama if it is not detected on the system.

### Improvements

- **MCP tool permissions** - `quant init` and `quant launch` now configure tool permissions for all supported clients, allowing `quant` MCP tools without manual approval prompts.
- **Self-update skips git describe builds** - Versions produced by `make install` (e.g. `v0.10.0-3-gecb7c99`) are now recognized as newer than the corresponding release tag, preventing unnecessary downgrade prompts.
- **No auto-update during launch** - `quant launch` no longer sets `QUANT_AUTOUPDATE`, so launched agents will not trigger self-update checks.

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
