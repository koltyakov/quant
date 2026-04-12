# MCP Client Integration

## Recommended deployment model

`quant` works best when scoped to the work you are doing right now rather than acting as one giant universal index.

- **Per project:** one server per repository or docs folder
- **Per domain:** one server for a bounded area such as `frontend-docs`, `architecture-rfcs`, `research-notes`, or `customer-evidence`
- **Per research set:** one server over a hand-picked folder of papers, exports, meeting notes, or source material for a specific investigation

This keeps tool selection clearer for the agent, reduces irrelevant retrieval noise, and makes it easier to control which documents are in scope for a task.

## Claude Code

Project-scoped MCPs are the right default:

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
      "args": ["mcp", "--dir", "/path/to/project"],
      "env": { "QUANT_AUTOUPDATE": "true" }
    }
  }
}
```

## GitHub Copilot (VS Code)

Add a project-level `.vscode/mcp.json`:

```json
{
  "servers": {
    "quant": {
      "type": "stdio",
      "command": "quant",
      "args": ["mcp", "--dir", "/path/to/project"],
      "env": { "QUANT_AUTOUPDATE": "true" }
    }
  }
}
```

## Codex

Add a local stdio MCP with the Codex CLI:

```bash
codex mcp add quant -- quant mcp --dir /path/to/project
```

Or add to `codex.json` / `.codex/config.json`:

```json
{
  "mcpServers": {
    "quant": {
      "type": "stdio",
      "command": "quant",
      "args": ["mcp", "--dir", "/path/to/project"],
      "env": { "QUANT_AUTOUPDATE": "true" }
    }
  }
}
```

## OpenCode

Add a local MCP in `opencode.json` or `opencode.jsonc`:

```jsonc
{
  "$schema": "https://opencode.ai/config.json",
  "mcp": {
    "quant": {
      "type": "local",
      "command": ["quant", "mcp", "--dir", "/path/to/project"],
      "enabled": true,
      "env": { "QUANT_AUTOUPDATE": "true" }
    }
  }
}
```

## Choosing a transport

`quant` supports three MCP transports:

| Transport | Default | How it works | Best for |
|-----------|---------|-------------|----------|
| `stdio` | Yes | Communicates over stdin/stdout. The client launches `quant` as a child process. | Local, single-client use. Lowest latency, no port management. Works with Claude Code, Codex, OpenCode, VS Code Copilot, and any client that can spawn a process. |
| `sse` | No | HTTP-based Server-Sent Events. Client connects to `/sse` for streaming and sends messages to `/message`. | Remote or multi-client access when the client requires an HTTP URL rather than a child process. |
| `http` | No | Streamable HTTP over a single `/mcp` endpoint. Same use cases as SSE but uses the newer MCP streamable HTTP transport. | Same scenarios as SSE. Prefer this over SSE if your client supports it - one endpoint instead of two. |

**When in doubt, use `stdio`.** Only switch to `sse` or `http` when you need remote access or your client doesn't support stdio.

## SSE / HTTP transport

When using `sse` or `http`, start `quant` with `--transport` and `--listen`:

```bash
quant mcp --dir ./my-project --transport sse --listen :9090
```

### Endpoints

| Transport | Endpoint | Description |
|-----------|----------|-------------|
| SSE | `http://localhost:9090/sse` | SSE stream for MCP events |
| SSE | `http://localhost:9090/message` | Message endpoint for SSE transport |
| HTTP | `http://localhost:9090/mcp` | Streamable HTTP MCP endpoint |
| Both | `http://localhost:9090/healthz` | Liveness probe (always returns `ok`) |
| Both | `http://localhost:9090/readyz` | Readiness probe (returns `ready` when index is initialized, `503` otherwise) |

Most MCP clients that support remote servers accept a URL like `http://localhost:9090/sse` (SSE transport) or `http://localhost:9090/mcp` (streamable HTTP transport). Refer to your client's documentation for the exact connection format.
