# quant

<p align="center">
  <img src="./assets/logo.png" alt="expose logo" width="220" />
</p>

A lightweight, developer-focused RAG index exposed as an MCP server. Point it at a folder and it watches the filesystem, extracts supported files, chunks them with basic structure awareness, embeds them via Ollama, stores them in SQLite, and serves semantic search over MCP.

The index is a projection of the filesystem. Files added, changed, or removed on disk are reflected in the index. There is no separate feed mode, manual injection path, or index-only delete API.

In practice, `quant` is usually most useful as a project-scoped MCP, not a single global MCP for everything on your machine. A common setup is one `quant` server per repository, documentation set, or research workspace. It also works well as a named MCP for a domain-specific slice of context, such as architecture docs, product research, RFCs, customer notes, or other curated document sets you want available to an agent for a specific task. That makes it especially useful for research-heavy workflows where you want to connect a bounded corpus into Claude Desktop, the Codex app, Claude Code, or another MCP client and ask focused research questions against your own material.

Zero CGO. Pure Go.

## Runtime requirements

- A `quant` binary for your platform, either downloaded from GitHub Releases or built from source
- A coding agent or other MCP-capable client of your choice, such as Claude, Codex, OpenCode, or GitHub Copilot
- [Ollama](https://ollama.ai) running locally with an embedding model pulled:
  ```
  ollama pull nomic-embed-text
  ```
- Optional for scanned PDFs: [ocrmypdf](https://ocrmypdf.readthedocs.io/) installed on your system `PATH`. If present, `quant` will automatically use it as a best-effort OCR sidecar for PDFs that contain no extractable text.

### Hardware guidance for Ollama

These are practical recommendations, not hard requirements. Exact needs depend on your embedding model, document sizes, and how much concurrent indexing you run.

- Best default on macOS: Apple Silicon with 16 GB unified memory or more is a strong general-purpose local setup for Ollama
- `nomic-embed-text` class models: 8 GB RAM is a workable floor, but 16 GB is a better default for smoother local indexing
- Larger embedding models such as `mxbai-embed-large`: plan for at least 16 GB RAM, often more if you also run other local tools
- GPU guidance: a discrete GPU can help on Linux or Windows, or for larger models and heavier concurrent indexing, but it is optional for typical `nomic-embed-text` usage
- CPU: modern 4-core or better is a reasonable baseline for CPU-heavy local-only use
- Storage: SSD strongly recommended because model startup, SQLite I/O, and rescans are noticeably slower on spinning disks
- If Ollama runs on another machine and `quant` points at it via `--embed-url`, these hardware constraints apply to the Ollama host, not necessarily the MCP client machine

## Build from source

You only need Go if you are building `quant` yourself instead of using a release binary.

- Go 1.26.1+

```
make build
```

Or directly:

```
mkdir -p bin && go build -o bin/quant ./cmd/quant
```

## Usage

```
./bin/quant mcp [--dir <path>] [options]
./bin/quant update
./bin/quant version
```

**Commands:**
| Command | Description |
|---------|-------------|
| `mcp` | Start the MCP server |
| `update` | Check for and apply the latest GitHub release |
| `version` | Print the quant version and exit |
| `help` | Show top-level CLI help |

**MCP Flags:**
| Flag | Default | Description |
|------|---------|-------------|
| `--dir` | current working directory | Directory to watch and index |
| `--db` | `<dir>/.index/quant.db` | SQLite database path |
| `--transport` | `stdio` | MCP transport: `stdio`, `sse`, `http` |
| `--listen` | `:8080` | Listen address for SSE/HTTP |
| `--embed-url` | `http://localhost:11434` | Embedding API URL |
| `--embed-model` | `nomic-embed-text` | Embedding model |
| `--pdf-ocr-lang` | `eng` | Tesseract language(s) for scanned PDF OCR, e.g. `eng`, `spa`, or `eng+spa` |
| `--chunk-size` | `512` | Target chunk size in approximate words |
| `--chunk-overlap` | `0.15` | Chunk overlap fraction (0–1) |
| `--index-workers` | auto (`2-8`) | Parallel workers for startup indexing |
| `--config` | - | YAML config file path |

All flags can also be set via env vars:
`QUANT_DIR`, `QUANT_DB`, `QUANT_TRANSPORT`, `QUANT_LISTEN`, `QUANT_EMBED_URL`, `QUANT_EMBED_MODEL`, `QUANT_PDF_OCR_LANG`, `QUANT_CHUNK_SIZE`, `QUANT_CHUNK_OVERLAP`.
Also `QUANT_INDEX_WORKERS`.

Automatic self-update is controlled separately via `QUANT_AUTOUPDATE=true`.

Configuration precedence is:

1. Defaults
2. YAML config file
3. Environment variables
4. Explicit CLI flags

### Examples

```bash
# Basic - index a folder over stdio
./bin/quant mcp --dir ./my-project

# SSE transport for remote access
./bin/quant mcp --dir ./my-project --transport sse --listen :9090

# Custom embedding endpoint via Ollama
./bin/quant mcp --dir ./docs \
  --embed-url http://gpu-server:11434 \
  --embed-model mxbai-embed-large

# Manually update to the latest release
./bin/quant update
```

## Auto-Update

`quant` can update its own binary from GitHub Releases.

- Manual update: run `quant update`
- Automatic update: set `QUANT_AUTOUPDATE=true`
- Accepted truthy values: `true`, `1`, `yes`

When auto-update is enabled for `quant mcp`:

1. It checks for a newer release on startup.
2. It keeps checking every 30 minutes in the background.
3. If an update is applied, the process restarts with the same arguments.

Development builds (`dev` or versions ending in `-dev`) do not auto-update.

For self-update to work, the running user must be able to replace the binary on disk. A writable user-owned install location such as `~/.local/bin/quant` is the safest default.

### Config file

```yaml
dir: ./my-project
db: ./.index/quant.db
transport: stdio
listen: :8080
embed_url: http://localhost:11434
embed_model: nomic-embed-text
pdf_ocr_lang: eng
chunk_size: 512
chunk_overlap: 0.15
index_workers: 4
```

Relative paths in the YAML file are resolved against the config file's directory.

## Recommended Deployment Model

`quant` is typically better when scoped to the work you are doing right now instead of acting as one giant universal index.

- Per project: one server per repository or docs folder
- Per domain: one server for a bounded area such as `frontend-docs`, `architecture-rfcs`, `research-notes`, or `customer-evidence`
- Per research set: one server over a hand-picked folder of papers, exports, meeting notes, or source material for a specific investigation

This keeps tool selection clearer for the agent, reduces irrelevant retrieval noise, and makes it easier to control which documents are actually in scope for a task.

## Embedding Model Choice

Current code supports Ollama as the embedding backend, but the config names are generic so another backend can be added later without renaming the whole surface.

Practical defaults:

- `nomic-embed-text`: best default local balance of quality and footprint
- `all-minilm`: smaller and cheaper to run locally if you care more about CPU/RAM than retrieval quality
- `mxbai-embed-large`: higher-quality local option if you can afford a larger model

If you later add a hosted backend, the cheapest widely-used OpenAI embedding option is currently `text-embedding-3-small`.

## MCP Tools

| Tool           | Description                                                                           |
| -------------- | ------------------------------------------------------------------------------------- |
| `search`       | Semantic search over indexed chunks. Params: `query` (required), `limit`, `threshold`, `path` |
| `list_sources` | List indexed documents. Params: `limit`                                               |
| `index_status` | Stats: total docs, chunks, DB size, watch dir, model                                  |

`search` embeds the query with the configured embedding model, uses SQLite FTS5 to prefilter candidate chunks, then reranks those candidates with normalized vector similarity.

### Research workflow

`quant` is a strong fit for research tasks. Point it at a folder of papers, notes, interview transcripts, exported docs, or RFCs, connect it to an MCP-capable client such as Claude Desktop or the Codex app, and ask research questions against that specific corpus instead of relying on a single broad workspace index.

### Claude Code config

For Claude Code, project-scoped MCPs are usually the right default:

```bash
claude mcp add --transport stdio --scope project quant -- quant mcp --dir /path/to/project
```

Or commit a project-level `.mcp.json`:

```json
{
  "mcpServers": {
    "quant": {
      "type": "stdio",
      "command": "quant",
      "args": ["mcp", "--dir", "/path/to/project"]
    }
  }
}
```

### GitHub Copilot config

For VS Code with GitHub Copilot Agent mode, add a project-level `.vscode/mcp.json`:

```json
{
  "servers": {
    "quant": {
      "type": "stdio",
      "command": "quant",
      "args": ["mcp", "--dir", "/path/to/project"]
    }
  }
}
```

### Codex config

Add a local stdio MCP with the Codex CLI:

```bash
codex mcp add quant -- quant mcp --dir /path/to/project
```

If you prefer a domain-specific name:

```bash
codex mcp add research-notes -- quant mcp --dir /path/to/research-notes
```

### OpenCode config

Add a local MCP in `opencode.json` or `opencode.jsonc`:

```jsonc
{
  "$schema": "https://opencode.ai/config.json",
  "mcp": {
    "quant": {
      "type": "local",
      "command": ["quant", "mcp", "--dir", "/path/to/project"],
      "enabled": true,
    },
  },
}
```

## Supported File Types

`quant` indexes common plain-text inputs by default, including source code, markup, config, data, and filename-only project files such as `Dockerfile`, `Makefile`, and similar repo metadata. The exact text-extension list is intentionally broad and lives in [`internal/extract/text.go`](internal/extract/text.go).

For document-style content, current support includes:

- Jupyter notebooks, with cell markers and captured text outputs when present
- PDF, with page markers like `[Page N]`
- Scanned PDF OCR via optional `ocrmypdf` fallback when a PDF has no embedded text and `ocrmypdf` is installed
- Rich text via RTF
- Modern Office/Open XML word-processing, presentation, and spreadsheet files, including common macro-enabled and template variants
- OpenDocument text, spreadsheet, and presentation files

Unsupported files are skipped.

## Indexing Behavior

- MCP startup is immediate; the initial filesystem scan runs in the background after the server is already accepting requests.
- Initial startup scans the target directory and indexes supported files that are new or changed.
- Stored document paths are relative to the watch directory, so the index remains stable across different launch working directories.
- Initial startup indexing runs with a bounded worker pool to overlap hashing, extraction, embedding, and database writes across files.
- Concurrent startup and live updates are coalesced per path so the same file is not re-indexed twice at once.
- Search results become richer as background indexing completes; already-indexed content is queryable immediately.
- A filesystem watcher keeps the index in sync after startup.
- Directory renames/removals delete the full indexed subtree, not just the top-level path.
- Deleting a file from disk removes it from the index.
- The index stores embedding metadata in SQLite. If the configured embedding model or dimensions change, the existing index is cleared and rebuilt from the filesystem projection.
- Reindexing a document is transactional, so partial failures do not leave a document half-indexed.
- Root and nested `.gitignore` files are applied during both initial scan and live watch updates.
- If the watcher overflows during a burst of filesystem events, quant schedules a full resync instead of silently trusting dropped events.

## Architecture

```mermaid
flowchart TD
    WD([Watched directory]) --> INDEX[Initial scan and watch updates]
    INDEX --> PROC[Extract, chunk, and embed]
    PROC --> OLLAMA[/Ollama API/]
    PROC --> DB[(SQLite index)]

    CLIENT([MCP client]) --> MCP[MCP server]
    MCP --> QUERY[Embed query]
    QUERY --> OLLAMA
    MCP --> SEARCH[Hybrid search]
    DB --> SEARCH
    SEARCH --> MCP
```

- **No CGO** - uses `modernc.org/sqlite` (pure Go SQLite)
- **Hybrid retrieval** - SQLite FTS5 prefilter + normalized vector rerank
- **Bounded-memory rerank** - top-k heap keeps vector reranking memory stable as candidate sets grow
- **SQLite tuned for concurrency** - WAL + busy timeout + multi-connection pool allow reads during writes
- **Embedding metadata** - stored in SQLite; index is rebuilt if model settings change
- **Transactional indexing** - chunk replacement happens in a single SQLite transaction per document
- **Office docs** parsed with stdlib `archive/zip` + `encoding/xml`, preserving more document structure
- **File watching** via `fsnotify` with 500ms debounce
- **Self-healing sync** - watcher overflow or `.gitignore` changes trigger a full filesystem resync

## Contributing

- Fork, branch, add tests, submit a pull request.

## License

MIT - see [LICENSE](./LICENSE).
