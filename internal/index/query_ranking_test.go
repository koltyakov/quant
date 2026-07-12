package index

import (
	"context"
	"math"
	"reflect"
	"slices"
	"testing"
	"time"
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

func TestPathBoostAndSignalShareTokenSemantics(t *testing.T) {
	t.Parallel()

	candidates := []scoredCandidate{
		{result: SearchResult{DocumentPath: "src/auth_handler.go"}},
		{result: SearchResult{DocumentPath: "docs/oauth.md"}},
	}
	boosted := pathBoost([]string{"auth"})(candidates)
	if boosted[0].score <= 0 || boosted[1].score != 0 {
		t.Fatalf("unexpected path boost scores: %+v", boosted)
	}

	ctx := &SignalContext{QueryTokens: []string{"auth"}}
	signal := &PathMatchSignal{}
	if signal.Score(ctx, &candidates[0]) <= 0 || signal.Score(ctx, &candidates[1]) != 0 {
		t.Fatal("PathMatchSignal does not match pathBoost token semantics")
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
	SortByScore(tied)
	for i, want := range []int64{1, 2, 3} {
		if tied[i].result.ChunkID != want {
			t.Fatalf("sorted[%d].ChunkID = %d, want %d", i, tied[i].result.ChunkID, want)
		}
	}
}

func TestAnalyzeQueryAndHelpers(t *testing.T) {
	t.Parallel()

	identifier := AnalyzeQuery("HTTPServer config.go")
	if !identifier.IsIdentifier || identifier.Intent != IntentDefinition {
		t.Fatalf("expected identifier definition query, got %+v", identifier)
	}
	if identifier.PathPrefix != "" {
		t.Fatalf("unexpected path prefix: %q", identifier.PathPrefix)
	}

	natural := AnalyzeQuery("how to update python config in internal/app")
	if !natural.IsNaturalLang || natural.Intent != IntentSearch {
		t.Fatalf("expected natural language search intent, got %+v", natural)
	}
	if got, want := natural.FileTypeFilter, []string{".py"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("file type filters mismatch: got %v want %v", got, want)
	}
	if natural.PathPrefix != "internal/app" {
		t.Fatalf("unexpected path prefix: %q", natural.PathPrefix)
	}

	reference := AnalyzeQuery("auth_handler middleware.go loginFlow")
	if !reference.IsIdentifier || reference.Intent != IntentReference {
		t.Fatalf("expected reference intent, got %+v", reference)
	}

	if !isIdentifierToken("snake_case") || !isIdentifierToken("camelCase") || !isIdentifierToken("config.go") {
		t.Fatal("expected identifier tokens to be detected")
	}
	if isIdentifierToken("plain") {
		t.Fatal("plain token should not be treated as identifier")
	}

	filters := extractFileTypeFilters([]string{"Go,", "python.", "md", "GO"})
	if got, want := filters, []string{".go", ".py", ".md"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("extractFileTypeFilters mismatch: got %v want %v", got, want)
	}

	if prefix := extractPathPrefix("find config inside docs,"); prefix != "docs" {
		t.Fatalf("unexpected extracted prefix: %q", prefix)
	}
	if prefix := extractPathPrefix("search everywhere now please"); prefix != "" {
		t.Fatalf("unexpected prefix for unqualified query: %q", prefix)
	}

	expanded := ExpandQuery("update auth test")
	for _, want := range []string{"modify", "authentication", "spec"} {
		if !slices.Contains(expanded, want) {
			t.Fatalf("expected expanded query to contain %q: %v", want, expanded)
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

func TestSignalRegistryAndHelpers(t *testing.T) {
	t.Parallel()

	registry := NewSignalRegistry()
	registry.Register(&KeywordSignal{})
	registry.Register(&PathMatchSignal{WeightOverride: 2})

	signals := registry.List()
	if len(signals) != 2 {
		t.Fatalf("expected 2 signals, got %d", len(signals))
	}
	signals[0] = nil
	if registry.List()[0] == nil {
		t.Fatal("List should return a copy")
	}

	now := time.Date(2026, 4, 14, 12, 0, 0, 0, time.UTC)
	ctx := &SignalContext{
		Query:       "Auth Service",
		QueryTokens: []string{"auth", "service"},
		Now:         now,
		Weights:     QuerySignalWeights{Keyword: 1.5, Vector: 0.8},
	}
	candidates := []scoredCandidate{
		{
			result:      SearchResult{DocumentPath: "internal/auth/service.go"},
			keywordRank: 2,
			vectorRank:  3,
			modifiedAt:  now.Add(-24 * time.Hour),
		},
		{
			result:      SearchResult{DocumentPath: "docs/readme.md"},
			keywordRank: noKeywordRank,
			vectorRank:  0,
		},
	}

	scored := registry.ApplySignals(ctx, candidates)
	if scored[0].score <= 0 {
		t.Fatalf("expected first candidate to receive signal score, got %f", scored[0].score)
	}
	if scored[1].score != 0 {
		t.Fatalf("expected second candidate score to stay zero, got %f", scored[1].score)
	}

	recency := (&RecencySignal{HalfLife: 48 * time.Hour}).Score(ctx, &candidates[0])
	if recency <= 0 {
		t.Fatalf("expected positive recency score, got %f", recency)
	}
	if score := (&VectorSignal{}).Score(ctx, &scoredCandidate{vectorRank: 0}); score != 0 {
		t.Fatalf("expected zero vector score for missing rank, got %f", score)
	}

	fileType := &FileTypeSignal{
		Extensions: map[string]float32{".go": 3},
		Default:    1,
	}
	if score := fileType.Score(ctx, &ScoredCandidate{result: SearchResult{DocumentPath: "internal/auth/service.go"}}); score <= 0 {
		t.Fatalf("expected file type signal score, got %f", score)
	}
	if score := fileType.Score(ctx, &ScoredCandidate{result: SearchResult{DocumentPath: "README"}}); score <= 0 {
		t.Fatalf("expected default file type score, got %f", score)
	}

	if got := toLower("Auth/HTTP.go"); got != "auth/http.go" {
		t.Fatalf("unexpected toLower result: %q", got)
	}
	if !containsString("service.go", "vice") || containsString("service.go", "VICE") {
		t.Fatal("containsString should perform literal matching")
	}
	if idx := searchString("service.go", "ice"); idx != 4 {
		t.Fatalf("unexpected substring index: %d", idx)
	}
	if ext := fileExtension("docs/Guide.MD"); ext != ".md" {
		t.Fatalf("unexpected extension: %q", ext)
	}

	created := CreateSignalContext("Auth Service", QuerySignalWeights{Keyword: 1, Vector: 1}, true, false)
	if !reflect.DeepEqual(created.QueryTokens, []string{"auth", "service"}) {
		t.Fatalf("unexpected query tokens: %v", created.QueryTokens)
	}

	out := []scoredCandidate{{score: 0.1}, {score: 0.4}, {score: 0.2}}
	SortByScore(out)
	if out[0].score < out[1].score || out[1].score < out[2].score {
		t.Fatalf("expected descending order after SortByScore: %+v", out)
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
