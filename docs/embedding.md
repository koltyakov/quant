# Embedding Models

## Backend

`quant` uses Ollama as the embedding backend via its `/api/embed` endpoint. The config fields are named generically (`embed_url`, `embed_model`) so an alternative backend can be added later without renaming the surface.

## Model choice

Practical defaults for local use:

| Model | Dimensions | Notes |
|---|---|---|
| `nomic-embed-text` | 768 | Best default balance of quality and footprint for local use |
| `all-minilm` | 384 | Smaller and cheaper to run; lower retrieval quality |
| `mxbai-embed-large` | 1024 | Higher quality; requires more RAM |

Pull a model before starting `quant`:

```bash
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
