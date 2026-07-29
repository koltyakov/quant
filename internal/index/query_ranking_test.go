package index

import (
	"context"
	"math"
	"reflect"
	"sort"
	"testing"
)

func TestUnifiedRRFKeywordOnlyOmitsVectorSignal(t *testing.T) {
	weights := QuerySignalWeights{Keyword: 0.7, Vector: 1.4}
	results := unifiedRRF(map[int]*searchCandidate{
		1: {id: 1, result: SearchResult{DocumentPath: "docs/auth.md"}, keywordRank: 1},
	}, nil, 1, nil, weights)
	if len(results) != 1 {
		t.Fatalf("expected one result, got %d", len(results))
	}

	want := (weights.Keyword / float32(rrfK+1)) /
		((weights.Keyword + recencyBoostWeight) / float32(rrfK+1))
	if math.Abs(float64(results[0].Score-want)) > 1e-6 {
		t.Fatalf("keyword-only score = %f, want %f", results[0].Score, want)
	}
	if results[0].Score < 0 || results[0].Score > 1 {
		t.Fatalf("keyword-only score must remain in [0,1], got %f", results[0].Score)
	}
}

func TestMergeCandidatesRanksComputedZeroVectorScore(t *testing.T) {
	merged := mergeCandidates(map[int]*searchCandidate{
		1: {id: 1, keywordRank: 1, vectorScore: 0, hasVector: true},
		2: {id: 2, keywordRank: 2},
	}, nil)
	if len(merged) != 2 {
		t.Fatalf("expected two candidates, got %d", len(merged))
	}
	if merged[0].vectorRank != 1 {
		t.Fatalf("computed zero-cosine candidate vector rank = %d, want 1", merged[0].vectorRank)
	}
	if merged[1].vectorRank != 0 {
		t.Fatalf("keyword-only candidate vector rank = %d, want 0", merged[1].vectorRank)
	}
}

func TestPathMatchingUsesExactComponents(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct {
		path  string
		query []string
		want  bool
	}{
		{path: "internal/auth_handler.go", query: []string{"auth"}, want: true},
		{path: "auth/middleware.go", query: []string{"middleware"}, want: true},
		{path: "src/authHandler.go", query: []string{"handler"}, want: true},
		{path: "src/main.go", query: []string{"go"}, want: true},
		{path: "assets/logo.md", query: []string{"go"}, want: false},
		{path: "docs/oauth.md", query: []string{"auth"}, want: false},
	} {
		if got := pathMatchesAnyToken(tc.path, tc.query); got != tc.want {
			t.Errorf("pathMatchesAnyToken(%q, %v) = %t, want %t", tc.path, tc.query, got, tc.want)
		}
	}
}

func TestPathBoostUsesExactComponentSemantics(t *testing.T) {
	t.Parallel()

	candidates := []scoredCandidate{
		{result: SearchResult{DocumentPath: "src/auth_handler.go"}},
		{result: SearchResult{DocumentPath: "docs/oauth.md"}},
	}
	boosted := pathBoost([]string{"auth"})(candidates)
	if boosted[0].score <= 0 || boosted[1].score != 0 {
		t.Fatalf("unexpected path boost scores: %+v", boosted)
	}
}

func TestRankingUsesDeterministicTieBreakers(t *testing.T) {
	t.Parallel()

	keyword := map[int]*searchCandidate{
		3: {id: 3, result: SearchResult{DocumentPath: "b.md", ChunkIndex: 0}, keywordRank: 1},
		2: {id: 2, result: SearchResult{DocumentPath: "a.md", ChunkIndex: 1}, keywordRank: 1},
		1: {id: 1, result: SearchResult{DocumentPath: "a.md", ChunkIndex: 0}, keywordRank: 1},
	}
	merged := mergeCandidates(keyword, nil)
	for i, want := range []int64{1, 2, 3} {
		if merged[i].result.ChunkID != want {
			t.Fatalf("merged[%d].ChunkID = %d, want %d", i, merged[i].result.ChunkID, want)
		}
	}

	tied := []scoredCandidate{
		{result: SearchResult{ChunkID: 3, DocumentPath: "b.md"}, score: 1},
		{result: SearchResult{ChunkID: 2, DocumentPath: "a.md", ChunkIndex: 1}, score: 1},
		{result: SearchResult{ChunkID: 1, DocumentPath: "a.md", ChunkIndex: 0}, score: 1},
	}
	sort.Slice(tied, func(i, j int) bool { return scoredCandidateBefore(tied[i], tied[j]) })
	for i, want := range []int64{1, 2, 3} {
		if tied[i].result.ChunkID != want {
			t.Fatalf("sorted[%d].ChunkID = %d, want %d", i, tied[i].result.ChunkID, want)
		}
	}
}

func TestCanonicalFileType(t *testing.T) {
	t.Parallel()

	for input, want := range map[string]string{
		" .GO ":    "go",
		"py":       "python",
		".md":      "markdown",
		" CUSTOM ": "custom",
	} {
		if got := CanonicalFileType(input); got != want {
			t.Errorf("CanonicalFileType(%q) = %q, want %q", input, got, want)
		}
	}
}

func TestNoopReranker(t *testing.T) {
	t.Parallel()

	noop := &NoopReranker{}
	input := []SearchResult{{ChunkID: 1, Score: 0.2}, {ChunkID: 2, Score: 0.8}}
	out, err := noop.Rerank(context.Background(), "", nil, input)
	if err != nil || !reflect.DeepEqual(out, input) || noop.Name() != "noop" {
		t.Fatalf("unexpected noop rerank result: out=%v err=%v", out, err)
	}
}
