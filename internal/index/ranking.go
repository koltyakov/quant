package index

import (
	"math"
	"sort"
	"strings"
	"time"
)

type searchCandidate struct {
	id          int
	result      SearchResult
	keywordRank int
	vectorScore float32
	hasVector   bool
	modifiedAt  time.Time
}

// QuerySignalWeights controls the relative contribution of keyword vs vector
// signals in RRF fusion. The weights multiply the respective 1/(K+rank) terms.
type QuerySignalWeights struct {
	Keyword float32
	Vector  float32
}

// classifyQueryWeights returns signal weights based on query shape.
// Identifier-like queries (camelCase, snake_case, short single tokens)
// upweight keyword search; longer natural-language queries upweight vector search.
func classifyQueryWeights(query string, keywordOverride, vectorOverride float32) QuerySignalWeights {
	tokens := strings.Fields(query)
	if len(tokens) == 0 {
		return applyWeightOverrides(QuerySignalWeights{Keyword: 1.0, Vector: 1.0}, keywordOverride, vectorOverride)
	}

	identifierTokens := 0
	for _, tok := range tokens {
		if camelCasePattern.MatchString(tok) || (strings.Contains(tok, "_") && tok != "_") || strings.Contains(tok, ".") {
			identifierTokens++
		}
	}

	var weights QuerySignalWeights
	switch {
	case len(tokens) <= 2 && identifierTokens == len(tokens):
		weights = QuerySignalWeights{Keyword: 1.5, Vector: 0.6}
	case len(tokens) == 1:
		weights = QuerySignalWeights{Keyword: 1.2, Vector: 0.9}
	case identifierTokens > len(tokens)/2:
		weights = QuerySignalWeights{Keyword: 1.3, Vector: 0.8}
	case len(tokens) >= 4:
		weights = QuerySignalWeights{Keyword: 0.7, Vector: 1.4}
	default:
		weights = QuerySignalWeights{Keyword: 1.0, Vector: 1.0}
	}

	return applyWeightOverrides(weights, keywordOverride, vectorOverride)
}

func applyWeightOverrides(w QuerySignalWeights, keywordOverride, vectorOverride float32) QuerySignalWeights {
	if keywordOverride > 0 {
		ratio := keywordOverride / w.Keyword
		w.Keyword = keywordOverride
		w.Vector = w.Vector * ratio
	}
	if vectorOverride > 0 {
		ratio := vectorOverride / w.Vector
		w.Vector = vectorOverride
		w.Keyword = w.Keyword * ratio
	}
	return w
}

// pathQueryTokens extracts lowercased path-segment tokens from the raw query for
// path-match boosting. It uses the raw token pattern (no stemming, no expansion) so that
// identifiers like "auth" match path segments like "auth/middleware.go" exactly.
func pathQueryTokens(query string) []string {
	matches := ftsTokenPattern.FindAllString(strings.ToLower(query), -1)
	seen := make(map[string]bool, len(matches))
	var tokens []string
	for _, m := range matches {
		if !seen[m] {
			seen[m] = true
			tokens = append(tokens, m)
		}
	}
	return tokens
}

func searchCandidateLimit(limit int) int {
	candidateLimit := limit * 20
	if candidateLimit < 50 {
		candidateLimit = 50
	}
	return candidateLimit
}

const rrfK = 60
const noKeywordRank = 0

// recencyHalfLife controls how fast the recency boost decays. Documents
// modified within this duration get ~half the maximum recency bonus.
const recencyHalfLife = 7 * 24 * time.Hour // 7 days

// recencyBoostWeight scales the recency signal relative to keyword/vector RRF terms.
const recencyBoostWeight float32 = 0.3

// scoredCandidate holds a candidate with its accumulated score during ranking.
type scoredCandidate struct {
	result      SearchResult
	score       float32
	keywordRank int
	vectorRank  int
	vectorScore float32
	modifiedAt  time.Time
}

// rankingStage transforms a slice of scored candidates.
type rankingStage func([]scoredCandidate) []scoredCandidate

// mergeCandidates collects keyword and vector-only candidates into a unified
// slice, assigning vector ranks by sorting on vectorScore descending.
func mergeCandidates(keywordCandidates, vectorOnlyCandidates map[int]*searchCandidate) []scoredCandidate {
	all := make([]*searchCandidate, 0, len(keywordCandidates)+len(vectorOnlyCandidates))
	for _, c := range keywordCandidates {
		all = append(all, c)
	}
	for _, c := range vectorOnlyCandidates {
		all = append(all, c)
	}

	if len(all) == 0 {
		return nil
	}

	// Candidates without a computed vector score sort after vector candidates
	// and keep vectorRank=0, so keyword-only search receives no vector signal.
	sort.Slice(all, func(i, j int) bool {
		if all[i].hasVector != all[j].hasVector {
			return all[i].hasVector
		}
		if all[i].vectorScore == all[j].vectorScore {
			if (all[i].keywordRank > 0) != (all[j].keywordRank > 0) {
				return all[i].keywordRank > 0
			}
			if all[i].keywordRank != all[j].keywordRank {
				return all[i].keywordRank < all[j].keywordRank
			}
			if all[i].result.DocumentPath != all[j].result.DocumentPath {
				return all[i].result.DocumentPath < all[j].result.DocumentPath
			}
			if all[i].result.ChunkIndex != all[j].result.ChunkIndex {
				return all[i].result.ChunkIndex < all[j].result.ChunkIndex
			}
			return all[i].id < all[j].id
		}
		return all[i].vectorScore > all[j].vectorScore
	})

	out := make([]scoredCandidate, len(all))
	vectorRank := 0
	for i, c := range all {
		candidateVectorRank := 0
		if c.hasVector {
			vectorRank++
			candidateVectorRank = vectorRank
		}
		out[i] = scoredCandidate{
			result:      c.result,
			keywordRank: c.keywordRank,
			vectorRank:  candidateVectorRank,
			vectorScore: c.vectorScore,
			modifiedAt:  c.modifiedAt,
		}
		out[i].result.ChunkID = int64(c.id)
	}
	return out
}

// rrfBaseScore assigns base RRF scores from keyword and vector ranks.
func rrfBaseScore(weights QuerySignalWeights) rankingStage {
	return func(candidates []scoredCandidate) []scoredCandidate {
		for i := range candidates {
			c := &candidates[i]
			if c.vectorRank > 0 {
				c.score += weights.Vector / float32(rrfK+c.vectorRank)
			}
			if c.keywordRank > noKeywordRank {
				c.score += weights.Keyword / float32(rrfK+c.keywordRank)
			}
		}
		return candidates
	}
}

// recencyBoost adds a recency-decay bonus based on document modification time.
func recencyBoost(halfLife time.Duration, weight float32) rankingStage {
	return func(candidates []scoredCandidate) []scoredCandidate {
		now := time.Now()
		for i := range candidates {
			c := &candidates[i]
			if c.modifiedAt.IsZero() {
				continue
			}
			age := now.Sub(c.modifiedAt)
			if age < 0 {
				age = 0
			}
			decay := float32(math.Exp(-0.693 * float64(age) / float64(halfLife)))
			c.score += weight * decay / float32(rrfK+1)
		}
		return candidates
	}
}

// pathBoost adds a bonus when the document path has an exact query token.
func pathBoost(pathTokens []string) rankingStage {
	return func(candidates []scoredCandidate) []scoredCandidate {
		if len(pathTokens) == 0 {
			return candidates
		}
		lowered := make([]string, len(pathTokens))
		for i, token := range pathTokens {
			lowered[i] = strings.ToLower(token)
		}
		// Chunks from the same document share a path; tokenize each unique
		// path once instead of per candidate.
		matchByPath := make(map[string]bool)
		for i := range candidates {
			c := &candidates[i]
			match, ok := matchByPath[c.result.DocumentPath]
			if !ok {
				match = pathMatchesAnyToken(c.result.DocumentPath, lowered)
				matchByPath[c.result.DocumentPath] = match
			}
			if match {
				c.score += 1.0 / float32(rrfK+1)
			}
		}
		return candidates
	}
}

func pathMatchesAnyToken(path string, queryTokens []string) bool {
	pathTokens := make(map[string]struct{})
	for _, token := range ftsTokenPattern.FindAllString(path, -1) {
		pathTokens[strings.ToLower(token)] = struct{}{}
		for _, component := range splitIdentifier(token) {
			pathTokens[strings.ToLower(component)] = struct{}{}
		}
	}
	for _, token := range queryTokens {
		if _, ok := pathTokens[strings.ToLower(token)]; ok {
			return true
		}
	}
	return false
}

// documentDiversity reorders results so the best chunk per unique document is
// selected first, then fills remaining slots with secondary chunks.
func documentDiversity(limit int) rankingStage {
	return func(candidates []scoredCandidate) []scoredCandidate {
		// Sort by score descending.
		sort.Slice(candidates, func(i, j int) bool { return scoredCandidateBefore(candidates[i], candidates[j]) })

		results := make([]scoredCandidate, 0, limit)
		seen := make(map[string]bool)

		// Pass 1: best chunk per unique document.
		for _, c := range candidates {
			if len(results) >= limit {
				break
			}
			if seen[c.result.DocumentPath] {
				continue
			}
			seen[c.result.DocumentPath] = true
			results = append(results, c)
		}

		// Pass 2: fill remaining slots with secondary chunks.
		if len(results) < limit {
			selected := make(map[string]map[int]bool)
			for _, r := range results {
				if selected[r.result.DocumentPath] == nil {
					selected[r.result.DocumentPath] = make(map[int]bool)
				}
				selected[r.result.DocumentPath][r.result.ChunkIndex] = true
			}

			for _, c := range candidates {
				if len(results) >= limit {
					break
				}
				if selected[c.result.DocumentPath] != nil && selected[c.result.DocumentPath][c.result.ChunkIndex] {
					continue
				}
				results = append(results, c)
				if selected[c.result.DocumentPath] == nil {
					selected[c.result.DocumentPath] = make(map[int]bool)
				}
				selected[c.result.DocumentPath][c.result.ChunkIndex] = true
			}
		}

		return results
	}
}

func scoredCandidateBefore(a, b scoredCandidate) bool {
	if a.score != b.score {
		return a.score > b.score
	}
	if a.result.DocumentPath != b.result.DocumentPath {
		return a.result.DocumentPath < b.result.DocumentPath
	}
	if a.result.ChunkIndex != b.result.ChunkIndex {
		return a.result.ChunkIndex < b.result.ChunkIndex
	}
	return a.result.ChunkID < b.result.ChunkID
}

// runRankingPipeline executes ranking stages in order and returns final results.
func runRankingPipeline(candidates []scoredCandidate, stages ...rankingStage) []SearchResult {
	for _, stage := range stages {
		candidates = stage(candidates)
	}
	results := make([]SearchResult, len(candidates))
	for i, c := range candidates {
		c.result.Score = c.score
		results[i] = c.result
	}
	return results
}

// unifiedRRF merges keyword and vector-only candidates using a composable ranking pipeline:
// base RRF scoring, recency boost, path boost, and document diversity.
func unifiedRRF(keywordCandidates, vectorOnlyCandidates map[int]*searchCandidate, limit int, pathTokens []string, weights QuerySignalWeights) []SearchResult {
	if limit <= 0 {
		return nil
	}
	candidates := mergeCandidates(keywordCandidates, vectorOnlyCandidates)
	if len(candidates) == 0 {
		return nil
	}
	hasKeyword := len(keywordCandidates) > 0
	hasVector := len(vectorOnlyCandidates) > 0 || anyHasVectorCandidate(keywordCandidates)
	return runRankingPipeline(candidates,
		rrfBaseScore(weights),
		recencyBoost(recencyHalfLife, recencyBoostWeight),
		pathBoost(pathTokens),
		normalizeScores(weights, hasKeyword, hasVector, len(pathTokens) > 0),
		documentDiversity(limit),
	)
}

func anyHasVectorCandidate(candidates map[int]*searchCandidate) bool {
	for _, c := range candidates {
		if c.hasVector {
			return true
		}
	}
	return false
}

// normalizeScores divides each score by the theoretical maximum so that
// the top candidate scores approach 1.0 and the threshold parameter becomes
// intuitive. The max theoretical score assumes rank 1 for both keyword and
// vector plus the maximum recency and path bonuses.
func normalizeScores(weights QuerySignalWeights, hasKeyword, hasVector, hasPathTokens bool) rankingStage {
	maxScore := float32(0)
	if hasKeyword {
		maxScore += weights.Keyword / float32(rrfK+1)
	}
	if hasVector {
		maxScore += weights.Vector / float32(rrfK+1)
	}
	maxScore += recencyBoostWeight / float32(rrfK+1)
	if hasPathTokens {
		maxScore += 1.0 / float32(rrfK+1)
	}
	if maxScore == 0 {
		maxScore = 1
	}
	return func(candidates []scoredCandidate) []scoredCandidate {
		for i := range candidates {
			candidates[i].score /= maxScore
		}
		return candidates
	}
}

type scoredResult struct {
	score        float32
	id           int
	path         string
	chunkIndex   int
	modifiedAt   time.Time
	parentID     *int64
	depth        int
	sectionTitle string
}

type candidateHeap []scoredResult

func (h candidateHeap) Len() int           { return len(h) }
func (h candidateHeap) Less(i, j int) bool { return scoredResultBetter(h[j], h[i]) }
func (h candidateHeap) Swap(i, j int)      { h[i], h[j] = h[j], h[i] }

func (h *candidateHeap) Push(x any) {
	*h = append(*h, x.(scoredResult))
}

func (h *candidateHeap) Pop() any {
	old := *h
	n := len(old)
	item := old[n-1]
	*h = old[:n-1]
	return item
}

func scoredResultBetter(a, b scoredResult) bool {
	if a.score != b.score {
		return a.score > b.score
	}
	if a.path != b.path {
		return a.path < b.path
	}
	if a.chunkIndex != b.chunkIndex {
		return a.chunkIndex < b.chunkIndex
	}
	return a.id < b.id
}
