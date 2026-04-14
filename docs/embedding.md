# Embedding Models

## Backend

`quant` supports two embedding backends, selected via the `embed_provider` config field or `QUANT_EMBED_PROVIDER` environment variable:

| Provider | Value | Notes |
|---|---|---|
| Ollama | `ollama` (default) | Local embedding via `/api/embed`. Auto-detected when `embed_url` does not contain `openai.com`. |
| OpenAI-compatible | `openai` | Any API that follows the OpenAI embeddings contract. Set `embed_url` to the API base URL and configure authentication as required by the provider. |

The config fields are named generically (`embed_url`, `embed_model`) so the same configuration surface works for both backends.

## Authentication

Ollama (local) requires no authentication. For OpenAI-compatible providers that require an API key, set it via `--embed-api-key` or the `QUANT_EMBED_API_KEY` environment variable:

```bash
QUANT_EMBED_API_KEY=sk-... quant mcp \
  --embed-provider openai \
  --embed-url https://api.openai.com \
  --embed-model text-embedding-3-small
```

Or in YAML config:

```yaml
embed_provider: openai
embed_url: https://api.openai.com
embed_model: text-embedding-3-small
embed_api_key: sk-...
```

The key is sent as `Authorization: Bearer <key>` on every embedding request. Providers that use a different authentication scheme (e.g. custom headers) are not currently supported.

## Model choice

Practical defaults for Ollama local use:

| Model | Dimensions | Notes |
|---|---|---|
| `nomic-embed-text` | 768 | Best default balance of quality and footprint for local use |
| `all-minilm` | 384 | Smaller and cheaper to run; lower retrieval quality |
| `mxbai-embed-large` | 1024 | Higher quality; requires more RAM |

## Auto-setup

`quant` recovers from common Ollama setup problems automatically on startup:

1. **Ollama not running** — if `ollama` is installed and the embed URL points to localhost, `quant` runs `ollama serve` in the background and waits up to ~7 seconds for it to become ready.
2. **Model not pulled** — if Ollama is running but the configured model is missing, `quant` runs `ollama pull <model>` and streams download progress to the terminal.
3. **Still unavailable** — if both recovery steps fail (wrong URL, not installed, network error), `quant` starts in keyword-only mode. The MCP server is fully operational; search falls back to FTS5 keyword results and `index_status` reports the embedding backend status and the fix needed.

To set up manually instead:

```bash
ollama serve
ollama pull nomic-embed-text
```

## How embeddings are used

### During indexing

1. Extracted text is chunked and each chunk is embedded via `EmbedBatch`.
2. Chunks are sent to Ollama in batches of `--embed-batch-size` (default 16).
3. Each embedding is L2-normalized and then quantized to int8 (1 byte per dimension) before storage in SQLite.
4. Unchanged chunks reuse their stored embeddings (incremental reindexing).

### During search

1. The search query is embedded via the `CachingEmbedder`, which wraps the Ollama backend with:
   - **LRU cache** (128 entries) - repeated or similar queries reuse cached embeddings.
   - **In-flight deduplication** - concurrent searches for the same text share a single backend request.
   - **Circuit breaker** - after 5 consecutive embedding failures, the breaker opens for 30 seconds and search falls back to keyword-only results.
2. The normalized query embedding is compared against stored chunk embeddings using dot product.

## Int8 quantization

Embeddings are quantized from float32 (4 bytes/dim) to int8 (1 byte/dim + 8-byte header) using per-vector min/max scaling:

```
quantized[i] = round((value[i] - min) / scale * 255)
```

This reduces storage by ~4x with less than 1% recall loss on L2-normalized vectors. The HNSW graph reconstructs float32 vectors from the quantized form on startup.

## Embedding metadata

The store records the embedding model name, dimensions, and normalization flag. If you change the model or the dimensions change, the store detects the mismatch and rebuilds the index from scratch on next startup.

## Input truncation

Ollama has a maximum input length per embedding call. `quant` truncates chunk content to fit within 4000 runes, preferring natural boundaries (paragraphs, sentences, words) over hard cuts.

## Hardware guidance

These are practical recommendations, not hard requirements. Exact needs depend on your embedding model, document sizes, and how much concurrent indexing you run.

- **Best default on macOS:** Apple Silicon with 16 GB unified memory or more is a strong general-purpose local setup.
- **`nomic-embed-text` class models:** 8 GB RAM is a workable floor; 16 GB is a better default for smoother local indexing.
- **`mxbai-embed-large`:** plan for at least 16 GB RAM, often more if you also run other local tools.
- **GPU:** a discrete GPU can help on Linux or Windows, or for larger models and heavier concurrent indexing, but it is optional for typical `nomic-embed-text` usage.
- **CPU:** a modern 4-core or better CPU is a reasonable baseline for CPU-only local use.
- **Storage:** SSD is strongly recommended - model startup, SQLite I/O, and rescans are noticeably slower on spinning disks.

If Ollama runs on another machine and `quant` points at it via `--embed-url`, these hardware constraints apply to the Ollama host, not the machine running `quant`.
