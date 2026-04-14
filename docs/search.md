# Search and Ranking

`quant` uses a hybrid search pipeline that combines keyword (full-text) and vector (semantic) signals, then applies a multi-stage ranking pipeline to produce final scores on a normalized 0-1 scale.

## Pipeline overview

```
Query text
  │
  ├──► FTS5 keyword search (AND, OR, NEAR queries)
  │     │
  │     └──► Keyword candidates with BM25 ranks
  │
  ├──► Embed query ──► HNSW vector search
  │     │
  │     └──► Vector-only candidates with similarity scores
  │
  └──► Merge candidates ──► RRF fusion ──► Normalize ──► Results
```

## Stage 1: Keyword search

The query is compiled into multiple FTS5 expressions:

| Query type | Purpose |
|-----------|---------|
| **AND** | All terms must match. Narrowest, tried first. |
| **OR** | Any term may match. Broader fallback. |
| **NEAR** | Terms must appear within 10 tokens of each other. Only built for 2-4 bare tokens. |

Identifier expansion splits camelCase (`getUserName` -> `get`, `user`, `name`) and snake_case tokens so that code identifiers match their component words. The last bare token gets a `*` suffix for prefix matching.

Results are collected with their BM25 rank and initial vector similarity score (dot product against the query embedding, computed during the same scan).

## Stage 2: Vector search

The query is embedded via Ollama and used to search the in-memory HNSW graph (M=16, EfSearch=100). This returns approximate nearest neighbors ranked by cosine distance.

When a `path` prefix filter is active, HNSW results are intersected with the prefix set from SQLite. If the intersection is too small, a bounded brute-force scan over prefix-matched chunks is used as a fallback (controlled by `--max-vector-candidates`).

## Stage 3: RRF fusion

Keyword and vector candidates are merged and scored using Reciprocal Rank Fusion:

```
score = (keyword_weight / (K + keyword_rank)) + (vector_weight / (K + vector_rank)) + bonuses
```

where K = 60 (standard RRF constant).

### Adaptive weighting

When `--keyword-weight` and `--vector-weight` are both 0 (the default), `quant` classifies the query and chooses weights automatically:

| Query shape | Keyword weight | Vector weight | Rationale |
|-------------|---------------|---------------|-----------|
| 1-2 short identifier-like tokens (camelCase, dotted) | 1.5 | 0.6 | Identifiers match better via exact keyword |
| Single non-identifier token | 1.2 | 0.9 | Slight keyword bias |
| Mostly identifier tokens | 1.3 | 0.8 | Code search patterns |
| 4+ tokens, natural language | 0.7 | 1.4 | Longer queries benefit from semantic matching |
| Everything else | 1.0 | 1.0 | Balanced |

Setting explicit weights via `--keyword-weight` / `--vector-weight` overrides the auto-classification while preserving the other signal's proportional contribution.

## Stage 4: Bonus signals

After base RRF scoring, additional signals are applied:

| Signal | Description |
|--------|-------------|
| **Recency boost** | Recently modified documents get a bonus that decays with a 7-day half-life (`e^(-0.693 * age / halfLife)`). Scaled by a weight of 0.3 relative to the RRF terms. |
| **Path match** | If query tokens appear in the document file path, a flat bonus is added. This helps queries like "auth middleware" match `auth/middleware.go`. |

## Stage 5: Normalization

Scores are divided by the theoretical maximum (rank 1 for both keyword and vector, plus maximum recency and path bonuses) so the top candidate approaches 1.0. This makes the `threshold` parameter intuitive: a threshold of 0.5 means "at least half the best possible score."

## Stage 6: Document diversity

Results are reordered so the single best chunk per unique document appears first, then remaining slots are filled with secondary chunks from the same documents. This prevents one large file from dominating all results.

## Fallback behavior

**At startup:** if the embedding backend is unavailable when `quant` starts, it attempts automatic recovery (start Ollama, pull model). If recovery fails, `quant` starts in keyword-only mode — the MCP server is fully operational and `index_status` reports the embedding status and the fix required.

**At query time:** if the embedding backend becomes unavailable after a successful start:

1. The circuit breaker opens after 5 consecutive failures.
2. The search proceeds with keyword-only candidates.
3. The response includes `embedding_status: "keyword_only"` and a note explaining the fallback.
4. The circuit breaker resets after 30 seconds, allowing retry.

## find_similar

The `find_similar` tool bypasses keyword search entirely. It loads the stored embedding for the given chunk ID, then queries the HNSW graph for nearest neighbors. This is useful for "more like this" exploration when you already have a relevant chunk from a previous search.

## drill_down

The `drill_down` tool is like `find_similar` but prioritizes diversity across documents. It spreads results across different source files to help explore a topic broadly rather than staying within one file. Useful when a single search result is a good entry point but you want to map the surrounding territory across multiple documents.

## summarize_matches

The `summarize_matches` tool runs a hybrid search and returns a high-level overview of which documents matched and what they contain, without returning individual chunks. It groups results by source document and produces a per-document summary. Useful when you want a quick map of what the index contains on a subject before drilling into specific chunks.

## Score kind

Each result includes a `score_kind` field that indicates how the score was produced:

| Value | Source |
|-------|--------|
| `"rrf"` | Hybrid search (`search` tool) - score is from RRF fusion of keyword and vector signals |
| `"similar"` | Similarity search (`find_similar`, `drill_down` tools) - score is raw vector similarity |
