# Configuration Reference

## Flags

All flags apply to `quant mcp`.

### Core flags

| Flag | Default | Description |
|------|---------|-------------|
| `--dir` | current working directory | Directory to watch and index |
| `--db` | `<dir>/.index/quant.db` | SQLite database path |
| `--transport` | `stdio` | MCP transport: `stdio`, `sse`, `http` |
| `--listen` | `:8080` | Listen address for SSE/HTTP transport |
| `--embed-url` | `http://localhost:11434` | Embedding API URL |
| `--embed-model` | `nomic-embed-text` | Embedding model name |
| `--config` | - | Path to a YAML config file |

### Indexing flags

| Flag | Default | Description |
|------|---------|-------------|
| `--chunk-size` | `512` | Target chunk size in approximate words (64--8192) |
| `--chunk-overlap` | `0.15` | Chunk overlap fraction (0--0.99) |
| `--embed-batch-size` | `16` | Number of chunks sent to the embedding backend per batch (1--128) |
| `--index-workers` | auto (2--8) | Parallel workers for startup and live indexing (1--64) |
| `--max-vector-candidates` | `20000` | Max chunks eligible for brute-force vector fallback when HNSW is not available; `0` disables the fallback entirely |

### Search tuning flags

| Flag | Default | Description |
|------|---------|-------------|
| `--keyword-weight` | `0` (auto) | Keyword search signal multiplier (0--10). When `0`, `quant` chooses weights automatically based on query shape |
| `--vector-weight` | `0` (auto) | Vector search signal multiplier (0--10). When `0`, `quant` chooses weights automatically based on query shape |
| `--max-concurrent-tools` | `4` | Maximum concurrent MCP tool calls (1--32) |

### Multi-instance flags

| Flag | Default | Description |
|------|---------|-------------|
| `--proxy-addr` | - | Address of the main-process proxy; when set, this instance runs in worker mode and delegates locking to the proxy |
| `--no-lock` | `false` | Disable multi-instance locking and run fully standalone |

### Watcher flags

| Flag | Default | Description |
|------|---------|-------------|
| `--watch-event-buffer` | `256` | Watcher event channel buffer size (1--4096) |

### PDF flags

| Flag | Default | Description |
|------|---------|-------------|
| `--pdf-ocr-lang` | `eng` | Tesseract language(s) for scanned PDF OCR, e.g. `eng`, `spa`, `eng+spa` |
| `--pdf-ocr-timeout` | `2m` | Timeout for the scanned PDF OCR fallback |

## Environment variables

All flags can be set via environment variables. The mapping is:

| Environment variable | Corresponding flag |
|---|---|
| `QUANT_DIR` | `--dir` |
| `QUANT_DB` | `--db` |
| `QUANT_TRANSPORT` | `--transport` |
| `QUANT_LISTEN` | `--listen` |
| `QUANT_EMBED_URL` | `--embed-url` |
| `QUANT_EMBED_MODEL` | `--embed-model` |
| `QUANT_CHUNK_SIZE` | `--chunk-size` |
| `QUANT_CHUNK_OVERLAP` | `--chunk-overlap` |
| `QUANT_EMBED_BATCH_SIZE` | `--embed-batch-size` |
| `QUANT_INDEX_WORKERS` | `--index-workers` |
| `QUANT_MAX_VECTOR_CANDIDATES` | `--max-vector-candidates` |
| `QUANT_KEYWORD_WEIGHT` | `--keyword-weight` |
| `QUANT_VECTOR_WEIGHT` | `--vector-weight` |
| `QUANT_MAX_CONCURRENT_TOOLS` | `--max-concurrent-tools` |
| `QUANT_WATCH_EVENT_BUFFER` | `--watch-event-buffer` |
| `QUANT_PDF_OCR_LANG` | `--pdf-ocr-lang` |
| `QUANT_PDF_OCR_TIMEOUT` | `--pdf-ocr-timeout` |
| `QUANT_PROXY_ADDR` | `--proxy-addr` |
| `QUANT_NO_LOCK` | `--no-lock` |

Auto-update is controlled separately:

| Environment variable | Description |
|---|---|
| `QUANT_AUTOUPDATE` | Enable automatic self-update on startup and every 30 minutes. Accepted values: `true`, `1`, `yes` |

## Configuration precedence

Settings are applied in this order, with later sources overriding earlier ones:

1. Built-in defaults
2. YAML config file (`--config`)
3. Environment variables
4. Explicit CLI flags

## YAML config file

Pass a config file with `--config <path>`. Relative paths in the file are resolved against the config file's directory.

```yaml
dir: ./my-project
db: ./.index/quant.db
transport: stdio
listen: ":8080"
embed_url: http://localhost:11434
embed_model: nomic-embed-text
chunk_size: 512
chunk_overlap: 0.15
embed_batch_size: 16
index_workers: 4
max_vector_candidates: 20000
keyword_weight: 0
vector_weight: 0
max_concurrent_tools: 4
watch_event_buffer: 256
pdf_ocr_lang: eng
pdf_ocr_timeout: 2m
proxy_addr: ""
no_lock: false
include:
  - "**/*.go"
  - "**/*.md"
exclude:
  - "vendor/**"
  - "node_modules/**"
```

### Include/exclude patterns

The `include` and `exclude` fields accept glob patterns that filter which files are indexed relative to the watch directory:

- **`include`** - if non-empty, a file must match at least one pattern to be indexed. When empty, all files are included by default.
- **`exclude`** - files matching any exclude pattern are skipped. Exclusions are applied after inclusions.

Patterns support `*` for single-level matching and `**` for recursive directory matching. For example:

```yaml
include:
  - "src/**"
  - "docs/**/*.md"
exclude:
  - "**/*_test.go"
  - "dist/**"
```

## Auto-update

`quant` can update its own binary from GitHub Releases.

- **Manual update:** run `quant update`
- **Automatic update:** set `QUANT_AUTOUPDATE=true`

When `QUANT_AUTOUPDATE=true` is set for `quant mcp`:

1. Checks for a newer release on startup.
2. Keeps checking every 30 minutes in the background.
3. If an update is applied, the process restarts automatically with the same arguments.

Development builds (`dev` or versions ending in `-dev`) never auto-update.

For self-update to work, the running user must have write access to the binary on disk. A user-owned install location such as `~/.local/bin/quant` is the safest default.
