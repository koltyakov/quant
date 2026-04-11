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
      "args": ["mcp", "--dir", "/path/to/project"]
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
      "args": ["mcp", "--dir", "/path/to/project"]
    }
  }
}
```

## Codex

Add a local stdio MCP with the Codex CLI:

```bash
codex mcp add quant -- quant mcp --dir /path/to/project
```

For a domain-specific name:

```bash
codex mcp add research-notes -- quant mcp --dir /path/to/research-notes
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
      "enabled": true
    }
  }
}
```

## SSE / HTTP transport

For remote access or when the MCP client requires an HTTP endpoint, start `quant` with `--transport sse` or `--transport http` and point the client at the listen address:

```bash
quant mcp --dir ./my-project --transport sse --listen :9090
```

Most MCP clients that support remote servers accept a URL like `http://localhost:9090/sse` (SSE transport) or `http://localhost:9090/mcp` (streamable HTTP transport). Refer to your client's documentation for the exact connection format.
