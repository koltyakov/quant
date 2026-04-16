package index

import (
	"context"
	"reflect"
	"slices"
	"testing"
	"time"
)

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

func TestFeedbackProfilesAndRerankers(t *testing.T) {
	t.Parallel()

	store := NewFeedbackStore(2)
	first := FeedbackEvent{DocPath: "docs/a.md", Selected: true, Timestamp: time.Date(2026, 4, 14, 12, 0, 0, 0, time.UTC)}
	store.Record(first)
	store.Record(FeedbackEvent{DocPath: "docs/a.md", Selected: true})
	store.Record(FeedbackEvent{DocPath: "docs/b.md", Selected: false})

	total, selected := store.Stats()
	if total != 2 || selected != 1 {
		t.Fatalf("unexpected stats: total=%d selected=%d", total, selected)
	}
	boosts := store.ComputePathBoosts()
	if len(boosts) != 0 {
		t.Fatalf("expected no boosts after capacity eviction, got %+v", boosts)
	}

	store = NewFeedbackStore(10)
	store.Record(FeedbackEvent{DocPath: "docs/a.md", Selected: true})
	store.Record(FeedbackEvent{DocPath: "docs/a.md", Selected: true})
	store.Record(FeedbackEvent{DocPath: "docs/b.md", Selected: true})
	boosts = store.ComputePathBoosts()
	if len(boosts) != 1 || boosts[0].Path != "docs/a.md" || boosts[0].Boost != 1 {
		t.Fatalf("unexpected boosts: %+v", boosts)
	}

	profile, err := GetWeightProfile("code")
	if err != nil {
		t.Fatalf("GetWeightProfile returned error: %v", err)
	}
	weights := QuerySignalWeights{Keyword: 1, Vector: 1}
	profile.ApplyWeights(&weights)
	if weights.Keyword != ProfileCode.KeywordWeight || weights.Vector != ProfileCode.VectorWeight {
		t.Fatalf("unexpected applied weights: %+v", weights)
	}
	if _, err := GetWeightProfile("missing"); err == nil {
		t.Fatal("expected unknown profile error")
	}
	names := ListWeightProfiles()
	for _, want := range []string{"balanced", "code", "mixed", "prose"} {
		if !slices.Contains(names, want) {
			t.Fatalf("missing weight profile %q in %v", want, names)
		}
	}

	noop := &NoopReranker{}
	input := []SearchResult{{ChunkID: 1, Score: 0.2}, {ChunkID: 2, Score: 0.8}}
	out, err := noop.Rerank(context.Background(), "", nil, input)
	if err != nil || !reflect.DeepEqual(out, input) || noop.Name() != "noop" {
		t.Fatalf("unexpected noop rerank result: out=%v err=%v", out, err)
	}
}
