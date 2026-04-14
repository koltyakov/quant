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
| `--embed-provider` | auto-detected | Embedding backend: `ollama` or `openai`. Auto-detected from URL when not set. |
| `--embed-api-key` | - | API key for the embedding backend. Required for OpenAI and other authenticated providers. |
| `--config` | - | Path to a YAML config file |

### Indexing flags

| Flag | Default | Description |
|------|---------|-------------|
| `--chunk-size` | `512` | Target chunk size in approximate words (64--8192) |
| `--chunk-overlap` | `0.15` | Chunk overlap fraction (0--0.99) |
| `--embed-batch-size` | `16` | Number of chunks sent to the embedding backend per batch (1--128) |
| `--index-workers` | auto (2--8) | Parallel workers for startup and live indexing (1--64) |

### Reranker flags

| Flag | Default | Description |
|------|---------|-------------|
| `--reranker` | - | Reranker type. Only accepted value: `cross-encoder` (requires `--reranker-model`). |
| `--reranker-model` | - | Model used for cross-encoder reranking (e.g. `llama3.2`). Requires Ollama. |

### Summarizer flags

| Flag | Default | Description |
|------|---------|-------------|
| `--summarizer` | `false` | Enable LLM-powered chunk summarization at index time. |
| `--summarizer-model` | same as `--embed-model` | Model used for chunk summarization. Requires Ollama. |

### PDF flags

| Flag | Default | Description |
|------|---------|-------------|
| `--pdf-ocr-lang` | `eng` | Tesseract language(s) for scanned PDF OCR, e.g. `eng`, `spa`, `eng+spa` |

## Environment variables

| Environment variable | Corresponding flag |
|---|---|
| `QUANT_DIR` | `--dir` |
| `QUANT_DB` | `--db` |
| `QUANT_TRANSPORT` | `--transport` |
| `QUANT_LISTEN` | `--listen` |
| `QUANT_EMBED_URL` | `--embed-url` |
| `QUANT_EMBED_MODEL` | `--embed-model` |
| `QUANT_EMBED_PROVIDER` | `--embed-provider` |
| `QUANT_EMBED_API_KEY` | `--embed-api-key` |
| `QUANT_CHUNK_SIZE` | `--chunk-size` |
| `QUANT_CHUNK_OVERLAP` | `--chunk-overlap` |
| `QUANT_EMBED_BATCH_SIZE` | `--embed-batch-size` |
| `QUANT_INDEX_WORKERS` | `--index-workers` |
| `QUANT_PDF_OCR_LANG` | `--pdf-ocr-lang` |

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
embed_provider: ollama   # ollama (default) or openai
# embed_api_key: sk-...  # required for OpenAI and other authenticated providers
chunk_size: 512
chunk_overlap: 0.15
embed_batch_size: 16
index_workers: 4
pdf_ocr_lang: eng
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

## Auto-tuned internals

The following parameters are automatically configured based on system resources and do not need manual tuning:

- **HNSW graph** (M, efSearch, reoptimization threshold) — tuned for recall/memory balance
- **Vector search candidates** — max chunks for brute-force fallback
- **Search weights** — keyword/vector signal weights auto-selected per query
- **Concurrent tool calls** — scaled to available CPUs
- **Watcher event buffer** — internal channel sizing
- **PDF OCR timeout** — internal timeout for OCR fallback
- **Multi-instance locking** — automatic detection and coordination

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
