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
  └──► Merge candidates ──► RRF fusion ──► Normalize
                           │
                           └──► Optional rerank ──► Threshold ──► Diversity ──► Limit
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

The query is embedded via the configured backend and used to search the in-memory HNSW graph (M=16, EfSearch=100). This returns approximate nearest neighbors ranked by cosine distance.

When a path or metadata filter is active, HNSW results are intersected with the matching set from SQLite. If the intersection is too small, a bounded exact scan over matching chunks is used as a fallback (controlled by `--max-vector-candidates`). Exceptionally broad filters cap the in-memory intersection at 500,000 chunk IDs.

The vector candidate budget is independent of the output count: small searches retain up to 100 semantic candidates, with a 1,000-candidate cap for larger internal requests. This lets later ranking and diversity choose among more than the requested number of hits. HNSW probes request at most 10,000 neighbors. These are upper bounds, not guarantees that enough matching chunks exist.

## Stage 3: RRF fusion

Keyword and vector candidates are merged and scored using Reciprocal Rank Fusion:

```
score = (keyword_weight / (K + keyword_rank)) + (vector_weight / (K + vector_rank)) + bonuses
```

where K = 60 (standard RRF constant).

### Adaptive weighting

`quant` classifies the query shape and selects weights automatically:

| Query shape | Keyword weight | Vector weight | Rationale |
|-------------|---------------|---------------|-----------|
| 1-2 short identifier-like tokens (camelCase, dotted) | 1.5 | 0.6 | Identifiers match better via exact keyword |
| Single non-identifier token | 1.2 | 0.9 | Slight keyword bias |
| Mostly identifier tokens | 1.3 | 0.8 | Code search patterns |
| 4+ tokens, natural language | 0.7 | 1.4 | Longer queries benefit from semantic matching |
| Everything else | 1.0 | 1.0 | Balanced |

Weights are chosen per query and are not user-configurable.

## Stage 4: Bonus signals

After base RRF scoring, additional signals are applied:

| Signal | Description |
|--------|-------------|
| **Recency boost** | Recently modified documents get a bonus that decays with a 7-day half-life (`e^(-0.693 * age / halfLife)`). Scaled by a weight of 0.3 relative to the RRF terms. |
| **Path match** | If query tokens appear in the document file path, a flat bonus is added. This helps queries like "auth middleware" match `auth/middleware.go`. |

## Stage 5: Normalization

RRF scores are divided by the theoretical maximum, using rank 1 for both keyword and vector plus maximum recency and path bonuses. These scores are ranking signals, not relevance probabilities. Optional reranking uses a different normalization, described below.

## Stage 6: Optional LLM reranking

With `--reranker cross-encoder`, the configured LLM scores up to the first 20 fused candidates, before thresholding or diversity. The original and LLM scores are each normalized against the maximum in that scored pool, then blended 50/50. Successful results use `score_kind: "rerank_blend"`.

Only the scored pool is returned after successful reranking. Unscored candidates are not mixed in using incomparable RRF scores, so a request for more than 20 results can return fewer than its limit. If any scoring batch fails or returns malformed, missing, nonfinite, or wrong-length scores, the entire original candidate pool retains its scores and order. The response reports `retrieval.rerank_error`.

## Stage 7: Threshold, diversity, and limit

Candidates below `threshold` are removed first. Among eligible candidates, the single best chunk per document appears first, then remaining slots are filled with secondary chunks. Finally, results are limited to the requested count. A low-scoring chunk from a different document cannot displace an eligible secondary chunk and then leave a gap when thresholding runs.

A threshold applies to the result's score kind. RRF and rerank-blend scores are not interchangeable, and neither is a relevance probability.

Search can be restricted by path prefix, canonical file type, programming language, or exact collection name. File type aliases and language names are normalized case-insensitively; collection names remain case-sensitive.

Filesystem indexing does not currently assign collection names, so collection filters only affect indexes populated with collection metadata through another integration.

## Fallback behavior

**At startup:** if local Ollama is unavailable when `quant` starts, it attempts automatic recovery by starting Ollama or pulling the model. If local Ollama remains unavailable or the model cannot be pulled, `quant` starts in keyword-only mode. Other provider, authentication, URL, or protocol errors can remain fatal.

**At query time:** if the embedding backend becomes unavailable after a successful start:

1. The circuit breaker opens after 5 consecutive failures.
2. The search proceeds with keyword-only candidates.
3. The response includes `embedding_status: "keyword_only"` and a note explaining the fallback.
4. The circuit breaker resets after 30 seconds, allowing retry.

When the server starts in keyword-only mode, new chunks can lack embeddings. Restarting with the backend available backfills those chunks even if their source files and model name have not changed.

## Retrieval diagnostics

Search and similarity tool responses include a request-level `retrieval` object, also carried through the local worker proxy. It is independent of `embedding_status`, which retains its existing embedding-availability meaning.

| `retrieval.semantic` | Meaning |
|----------------------|---------|
| `complete` | The configured semantic retrieval strategy completed. HNSW is approximate, so this does not promise exhaustive recall. |
| `unavailable` | No query or source embedding was available for semantic retrieval. |
| `budget_skipped` | The exact fallback was skipped because it was disabled or exceeded its candidate budget. |
| `incomplete` | Semantic retrieval examined a restricted subset or could not complete a filtered fallback. |
| `failed` | A semantic retrieval operation failed; partial results may still be present. |
| `unknown` | The backend did not supply diagnostics, for example an older proxy server. |

`budget_skipped: true` records a skipped fallback even when the overall status is `incomplete`. `errors` contains retrieval failures, and `rerank_error` records optional reranker failures. An empty result set with unavailable or incomplete retrieval is not evidence that nothing in the corpus matches.

## find_similar

The `find_similar` tool bypasses keyword search entirely. It loads the stored embedding for the given chunk ID, then queries HNSW for nearest neighbors. While HNSW is unavailable, it uses a bounded exact scan controlled by `--max-vector-candidates`. This is useful for "more like this" exploration when you already have a relevant chunk from a previous search.

## get_context

The `get_context` tool retrieves a search result in source order with neighboring chunks from the same document. `before` and `after` default to one and allow up to five chunks in either direction. Unlike similarity search, context expansion never crosses a document boundary and preserves duplicate chunks and chunk-index ordering.

## drill_down

The `drill_down` tool is like `find_similar` but prioritizes diversity across documents. It spreads results across different source files to help explore a topic broadly rather than staying within one file. Useful when a single search result is a good entry point but you want to map the surrounding territory across multiple documents.

## summarize_matches

The `summarize_matches` tool runs a bounded search and returns a non-exhaustive overview of the top matching source documents with a small set of excerpts. Structured output includes the effective limit, matched chunk and document counts, and embedding mode. Use it for a quick map before drilling into specific chunks, not as proof that every matching document was examined.

## Score kind

Each result includes a `score_kind` field that indicates how the score was produced:

| Value | Source |
|-------|--------|
| `"rrf"` | Search score from theoretical-maximum-normalized RRF fusion with path and recency bonuses |
| `"rerank_blend"` | Successful LLM reranking, using a 50/50 blend of pool-maximum-normalized original and model scores |
| `"similar"` | Similarity search (`find_similar`, `drill_down` tools) - score is raw vector similarity |

Each scored result also includes `score_semantics`, a description of its score calculation.
