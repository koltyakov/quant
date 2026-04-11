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

### Advanced flags

| Flag | Default | Description |
|------|---------|-------------|
| `--chunk-size` | `512` | Target chunk size in approximate words (64–8192) |
| `--chunk-overlap` | `0.15` | Chunk overlap fraction (0–0.99) |
| `--index-workers` | auto (2–8) | Parallel workers for startup and live indexing |
| `--max-vector-candidates` | `20000` | Maximum chunks eligible for brute-force vector fallback; `0` disables it |
| `--watch-event-buffer` | `256` | Watcher event channel buffer size (1–4096) |
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
| `QUANT_INDEX_WORKERS` | `--index-workers` |
| `QUANT_MAX_VECTOR_CANDIDATES` | `--max-vector-candidates` |
| `QUANT_WATCH_EVENT_BUFFER` | `--watch-event-buffer` |
| `QUANT_PDF_OCR_LANG` | `--pdf-ocr-lang` |
| `QUANT_PDF_OCR_TIMEOUT` | `--pdf-ocr-timeout` |

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
listen: :8080
embed_url: http://localhost:11434
embed_model: nomic-embed-text
chunk_size: 512
chunk_overlap: 0.15
index_workers: 4
max_vector_candidates: 20000
watch_event_buffer: 256
pdf_ocr_lang: eng
pdf_ocr_timeout: 2m
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
