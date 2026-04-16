# Troubleshooting

## Embedding backend unavailable

**Symptom:** `index_status` reports `embedding_status: "keyword_only"`, or startup logs show `embedding backend unavailable`.

**Cause:** `quant` cannot reach the embedding backend (Ollama or OpenAI-compatible API).

**Fix:**

For Ollama:
```bash
# Check whether Ollama is running
curl http://localhost:11434/api/tags

# If not running, start it
ollama serve

# Check whether the embedding model is pulled
ollama list
ollama pull nomic-embed-text
```

`quant` attempts these recovery steps automatically on startup. If recovery fails, it starts in keyword-only mode — search still works, but results rely on FTS5 keyword matching only.

For remote or OpenAI-compatible backends, verify the URL, API key, and network connectivity:
```bash
curl -H "Authorization: Bearer $QUANT_EMBED_API_KEY" \
  "$QUANT_EMBED_URL/v1/models"
```

---

## Slow initial indexing

**Symptom:** Startup takes a long time before the MCP server is ready. `index_status` shows `state: "indexing"`.

**Cause:** Large number of files, slow embedding backend, or insufficient parallelism.

**Steps:**

1. **Check embedding throughput.** The bottleneck is usually the embedding backend, not disk I/O. Run `ollama ps` to see GPU/CPU utilization.

2. **Increase workers.** If you have spare CPU/memory:
   ```bash
   quant mcp --index-workers 8
   ```
   Default is `cpus/2` capped at 8. Higher values help when the embedding backend can handle concurrent requests.

3. **Increase embed batch size.** Sending more chunks per Ollama call reduces round-trip overhead:
   ```bash
   quant mcp --embed-batch-size 32
   ```

4. **Narrow the watch scope.** Use `include` patterns to index only what you need:
   ```yaml
   include:
     - "src/**"
     - "docs/**/*.md"
   exclude:
     - "**/*_test.go"
   ```

5. **Check for large files.** The text extractor skips files over 8 MB. If you have many large files that aren't being indexed, check `index_status` for the document count.

---

## Index grows unexpectedly large

**Symptom:** The SQLite database at `.index/quant.db` is consuming significant disk space.

**Cause:** Many large files indexed, or many reindex cycles accumulating stale data.

**Steps:**

1. **Check what's indexed.** Use the `list_sources` MCP tool to see which files are in the index.

2. **Add exclude patterns** for generated files, build artifacts, or vendor directories:
   ```yaml
   exclude:
     - "node_modules/**"
     - "vendor/**"
     - "dist/**"
     - "**/*.min.js"
   ```

3. **SQLite vacuum.** `quant` runs periodic vacuuming automatically to reclaim freed space. If the database is large after many deletes, restart `quant` to trigger a vacuum cycle.

---

## Files not appearing in search results

**Symptom:** A file exists in the watched directory but doesn't show up in `list_sources` or search results.

**Possible causes and fixes:**

1. **Unsupported file type.** Check [docs/file-types.md](file-types.md). Binary files and unknown extensions are silently skipped.

2. **Excluded by pattern.** Check your `include`/`exclude` config. A file must match at least one `include` pattern (if any are set) and must not match any `exclude` pattern.

3. **Excluded by `.gitignore`.** `quant` respects `.gitignore` files. If a file is gitignored, it won't be indexed.

4. **File too large.** The text extractor reads up to 8 MB per file. Larger files are skipped.

5. **Index not yet up to date.** After adding files, `quant` may still be processing the queue. Check `index_status` to see the current `state`.

6. **Embedding failure during indexing.** If the embedding backend was unavailable when the file was indexed, the file may be quarantined. Restart `quant` with the embedding backend available to trigger reindexing.

---

## Search returns irrelevant results

**Symptom:** Search results don't match the expected files or content.

**Steps:**

1. **Check embedding status.** If `embedding_status` in the response is `"keyword_only"`, the embedding backend is unavailable and results are keyword-only. Fix the backend (see above) and restart `quant`.

2. **Try a different query shape.** The hybrid pipeline weights signals based on query shape. For code identifiers, use exact names (`getUserById`). For conceptual questions, use natural language phrases.

3. **Use `find_similar` or `drill_down`** to explore from a relevant result rather than a new query. Once you have one good chunk ID, these tools can surface related content more reliably.

4. **Use the `path` filter** to restrict results to a specific subtree:
   ```
   search(query="...", path="src/auth/")
   ```

5. **Check `threshold`.** The default threshold filters out low-confidence results. Lower it to see more candidates:
   ```
   search(query="...", threshold=0.1)
   ```

---

## PDF files not indexed

**Symptom:** PDF files exist in the watch directory but don't appear in `list_sources`.

**Cause:** PDFs with no embedded text (scanned) require OCR. `quant` only attempts OCR if `ocrmypdf` is installed.

**Fix:**

For scanned PDFs, install `ocrmypdf`:
```bash
# macOS
brew install ocrmypdf

# Ubuntu/Debian
apt-get install ocrmypdf
```

For multi-language PDFs:
```bash
quant mcp --pdf-ocr-lang eng+fra
```

PDFs that contain embedded text are indexed without OCR.

---

## MCP client shows "quant not found" or connection error

**Symptom:** The MCP client cannot start or connect to `quant`.

**Steps:**

1. **Verify the binary is on PATH:**
   ```bash
   which quant
   quant version
   ```

2. **Check the MCP config path.** The `command` in your client's MCP config must resolve to the `quant` binary. Use the absolute path if needed:
   ```json
   { "command": "/home/user/.local/bin/quant" }
   ```

3. **Check the watch directory.** The `--dir` path in the MCP config must exist and be readable. Use absolute paths to avoid ambiguity.

4. **For SSE/HTTP transport**, verify `quant` is running and the port is accessible:
   ```bash
   curl http://localhost:8080/healthz
   # Should return: ok
   ```

---

## Embedding model changes don't take effect

**Symptom:** After changing `--embed-model`, search quality doesn't improve or results seem off.

**Cause:** The index contains embeddings from the previous model. Mixing embeddings from different models produces incorrect similarity scores.

**Fix:** `quant` detects model changes automatically on startup and rebuilds the index from scratch. This happens when:
- The model name changes
- The embedding dimensions change

Allow the initial reindexing to complete before querying. Check `index_status` for progress.

If you need to force a rebuild manually, delete the database:
```bash
rm -rf .index/
```

Then restart `quant`. The index will be rebuilt from the current files.

---

## High memory usage

**Symptom:** `quant` consumes more RAM than expected.

**Cause:** The in-memory HNSW graph scales with the number of indexed chunks. Large corpora with small chunk sizes produce more nodes.

**Steps:**

1. **Increase chunk size** to reduce the number of chunks for the same content:
   ```bash
   quant mcp --chunk-size 1024
   ```
   Note: this requires reindexing (delete `.index/` first).

2. **Narrow the index scope** with `include`/`exclude` patterns to exclude large directories that aren't needed for search.

3. **Check system memory limit.** `quant` sets a Go runtime memory soft limit based on available RAM (25% of system memory, capped at 4 GB). On systems with limited RAM, use `include` patterns to keep the corpus small.
