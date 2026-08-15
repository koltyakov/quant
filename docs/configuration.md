# Configuration Reference

## Flags

All flags apply to `quant mcp`.

### Core flags

| Flag | Default | Description |
|------|---------|-------------|
| `--dir` | current working directory | Directory to watch and index |
| `--db` | `<dir>/.index/quant.db` | SQLite database path |
| `--transport` | `stdio` | MCP transport: `stdio`, `sse`, `http` |
| `--listen` | `127.0.0.1:8080` | Listen address for SSE/HTTP transport |
| `--mcp-token` | - | Bearer token required on SSE/HTTP MCP endpoints. Required for non-loopback listeners. |
| `--embed-url` | `http://localhost:11434` | Embedding API URL |
| `--embed-model` | `nomic-embed-text` | Embedding model name |
| `--embed-provider` | auto-detected | Embedding backend: `ollama` or `openai`. Auto-detection recognizes loopback URLs as Ollama and OpenAI hostnames as OpenAI. |
| `--embed-api-key` | - | API key for the embedding backend. Required for OpenAI and other authenticated providers. |
| `--config` | - | Path to a YAML config file |
| `--allow-insecure-model-http` | `false` | Allow plaintext model HTTP on non-loopback hosts. Use only on a trusted network. |

### LLM flags

| Flag | Default | Description |
|------|---------|-------------|
| `--llm-url` | `http://localhost:11434` | LLM API URL used by reranking and summarization |
| `--llm-model` | - | Default LLM model for reranking and summarization |
| `--llm-provider` | auto-detected | LLM backend: `ollama` or `openai`. Auto-detection recognizes loopback URLs as Ollama and OpenAI hostnames as OpenAI. |
| `--llm-api-key` | - | API key for the LLM backend. Required for OpenAI and other authenticated providers. |

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
| `--reranker-model` | `--llm-model` | Model used for cross-encoder reranking (e.g. `llama3.2`). Overrides the shared LLM model for this feature. |

Cross-encoder reranking adds a second-pass LLM reranking step after the initial hybrid retrieval. The model scores up to the first 20 `(query, chunk)` pairs. `quant` normalizes the original and LLM scores, blends them 50/50, and reorders the candidates. Results currently retain `score_kind: "rrf"`.

**When to use:** When retrieval precision matters more than latency. Reranking runs at query time and adds one LLM call per candidate batch, so it noticeably increases response time. Good for research workspaces where you want the single best result to be highly accurate.

**Backend choice:** Reranking is configured independently from embeddings. You can embed with one provider and rerank with another by pointing `--llm-*` at a different service.

**Model choice:** A small instruction-following model works well (e.g. `llama3.2`). If `--reranker-model` is omitted, `quant` falls back to `--llm-model`.

### Summarizer flags

| Flag | Default | Description |
|------|---------|-------------|
| `--summarizer` | `false` | Enable LLM-powered chunk summarization at index time. |
| `--summarizer-model` | `--llm-model` | Model used for chunk summarization. Overrides the shared LLM model for this feature. |

The experimental summarizer generates a concise summary of each chunk at index time using the configured LLM backend. Summaries are stored alongside chunk text, but current search ranking and MCP responses do not consume them. Enabling this option therefore does not currently change retrieval results.

**Cost:** Runs at index time, not query time — so search latency is unaffected. The tradeoff is significantly longer initial indexing and higher compute during reindexing. Large corpora with frequent updates can become expensive to maintain.

**Backend choice:** Summarization is configured independently from embeddings through `--llm-*`.

**Model choice:** A small generative model (e.g. `llama3.2`) works well and is faster than a large model. If `--summarizer-model` is omitted, `quant` falls back to `--llm-model`.

### PDF flags

| Flag | Default | Description |
|------|---------|-------------|
| `--pdf-ocr-lang` | `eng` | Tesseract language(s) for scanned PDF OCR, e.g. `eng`, `spa`, `eng+spa` |
| `--pdf-ocr-timeout` | `2m` | OCR timeout per PDF file; must be greater than zero |

### Search and runtime flags

| Flag | Default | Description |
|------|---------|-------------|
| `--max-vector-candidates` | `20000` | Maximum chunks scanned by exact vector fallback; `-1` is unlimited and `0` disables fallback |
| `--max-concurrent-tools` | auto (2--8) | Maximum concurrent MCP tool calls; explicitly setting `0` uses the server fallback of 4 |

`--pdf-ocr-timeout`, `--max-vector-candidates`, and `--max-concurrent-tools` are CLI-only. They do not have YAML fields or `QUANT_*` environment variables.

## Environment variables

| Environment variable | Corresponding flag |
|---|---|
| `QUANT_DIR` | `--dir` |
| `QUANT_DB` | `--db` |
| `QUANT_TRANSPORT` | `--transport` |
| `QUANT_LISTEN` | `--listen` |
| `QUANT_MCP_TOKEN` | `--mcp-token` |
| `QUANT_EMBED_URL` | `--embed-url` |
| `QUANT_EMBED_MODEL` | `--embed-model` |
| `QUANT_EMBED_PROVIDER` | `--embed-provider` |
| `QUANT_EMBED_API_KEY` | `--embed-api-key` |
| `QUANT_LLM_URL` | `--llm-url` |
| `QUANT_LLM_MODEL` | `--llm-model` |
| `QUANT_LLM_PROVIDER` | `--llm-provider` |
| `QUANT_LLM_API_KEY` | `--llm-api-key` |
| `QUANT_CHUNK_SIZE` | `--chunk-size` |
| `QUANT_CHUNK_OVERLAP` | `--chunk-overlap` |
| `QUANT_EMBED_BATCH_SIZE` | `--embed-batch-size` |
| `QUANT_INDEX_WORKERS` | `--index-workers` |
| `QUANT_PDF_OCR_LANG` | `--pdf-ocr-lang` |
| `QUANT_RERANKER` | `--reranker` |
| `QUANT_RERANKER_MODEL` | `--reranker-model` |
| `QUANT_SUMMARIZER` | `--summarizer` |
| `QUANT_SUMMARIZER_MODEL` | `--summarizer-model` |
| `QUANT_ALLOW_INSECURE_MODEL_HTTP` | `--allow-insecure-model-http` |

Auto-update is controlled separately:

| Environment variable | Description |
|---|---|
| `QUANT_AUTOUPDATE` | Enable automatic self-update on startup and every 30 minutes. Accepted values: `true`, `1`, `yes` |
| `QUANT_UPDATE_REPO` | Advanced: override the GitHub `owner/repository` used for update checks |

## Configuration precedence

Settings are applied with the following precedence, from lowest to highest:

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
listen: "127.0.0.1:8080"
# mcp_token: replace-with-a-random-token
embed_url: http://localhost:11434
embed_model: nomic-embed-text
embed_provider: ollama   # ollama or openai
# embed_api_key: sk-...  # required for OpenAI and other authenticated providers
llm_url: http://localhost:11434
llm_model: llama3.2
llm_provider: ollama     # ollama or openai
# llm_api_key: sk-...    # required for OpenAI and other authenticated providers
allow_insecure_model_http: false
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

SSE and HTTP transports accept an optional bearer token through `--mcp-token`, `QUANT_MCP_TOKEN`, or `mcp_token`. Non-loopback listen addresses are rejected unless a token is configured. Clients must send `Authorization: Bearer <token>` on MCP endpoints; health and readiness probes remain unauthenticated. Browser requests with an `Origin` header are rejected. Use TLS or a trusted TLS-terminating proxy when crossing an untrusted network. This does not affect the default `stdio` transport.

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

Scanning and live updates also respect nested `.gitignore` files, reject symlinks and paths resolving outside the watch root, and skip hidden directories. Include patterns cannot override those exclusions.

## Defaults and auto-tuning

Most internal search settings use fixed defaults:

- **HNSW graph:** M=16, efSearch=100, reoptimization threshold=0.2
- **Exact vector fallback:** at most 20,000 chunks, configurable with `--max-vector-candidates`
- **Watcher event buffer:** 256 events
- **PDF OCR timeout:** two minutes, configurable with `--pdf-ocr-timeout`
- **Search weights:** keyword/vector weights are selected from the query shape

Indexing workers and concurrent MCP tool calls are derived from available CPUs, each capped at 8 by default. The Go runtime memory soft limit is derived from physical memory. Multi-instance locking and proxy coordination are automatic.

## Auto-update

`quant` can update its own binary from GitHub Releases.

- **Manual update:** run `quant update`
- **Automatic update:** set `QUANT_AUTOUPDATE=true`

When `QUANT_AUTOUPDATE=true` is set for `quant mcp`:

1. Checks for a newer release on startup.
2. Keeps checking every 30 minutes in the background.
3. If an update is applied, the process restarts automatically with the same arguments.

Development builds (`dev` or versions ending in `-dev`) never auto-update.

Release installers and self-update verify the downloaded archive against the release's SHA-256 `checksums.txt` before extraction or replacement.

For self-update to work, the running user must have write access to the binary on disk. A user-owned install location such as `~/.local/bin/quant` is the safest default.
