# Embedding Models

## Model choice

`quant` uses Ollama as the embedding backend. The config fields are named generically so an alternative backend can be added later without renaming the surface.

Practical defaults for local use:

| Model | Notes |
|---|---|
| `nomic-embed-text` | Best default balance of quality and footprint for local use |
| `all-minilm` | Smaller and cheaper to run; lower retrieval quality |
| `mxbai-embed-large` | Higher quality; requires more RAM |

If you later add a hosted backend, `text-embedding-3-small` is the cheapest widely-used OpenAI embedding option.

Pull a model before starting `quant`:

```bash
ollama pull nomic-embed-text
```

## Hardware guidance

These are practical recommendations, not hard requirements. Exact needs depend on your embedding model, document sizes, and how much concurrent indexing you run.

- **Best default on macOS:** Apple Silicon with 16 GB unified memory or more is a strong general-purpose local setup.
- **`nomic-embed-text` class models:** 8 GB RAM is a workable floor; 16 GB is a better default for smoother local indexing.
- **`mxbai-embed-large`:** plan for at least 16 GB RAM, often more if you also run other local tools.
- **GPU:** a discrete GPU can help on Linux or Windows, or for larger models and heavier concurrent indexing, but it is optional for typical `nomic-embed-text` usage.
- **CPU:** a modern 4-core or better CPU is a reasonable baseline for CPU-only local use.
- **Storage:** SSD is strongly recommended - model startup, SQLite I/O, and rescans are noticeably slower on spinning disks.

If Ollama runs on another machine and `quant` points at it via `--embed-url`, these hardware constraints apply to the Ollama host, not the machine running `quant`.
