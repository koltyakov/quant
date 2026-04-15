package index

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestRemoveBackup(t *testing.T) {
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "quant.db")

	origData := []byte("not a real sqlite db")
	if err := os.WriteFile(dbPath, origData, 0600); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}

	store, err := NewStore(dbPath)
	if err != nil {
		t.Fatalf("NewStore() error = %v", err)
	}

	if store.backup == "" {
		t.Skip("no backup was created, schema was already compatible")
	}

	backupPath := store.backup
	if _, err := os.Stat(backupPath); err != nil {
		t.Fatalf("expected backup file at %s: %v", backupPath, err)
	}

	store.RemoveBackup()

	if _, err := os.Stat(backupPath); !os.IsNotExist(err) {
		t.Fatalf("expected backup to be removed, got stat err: %v", err)
	}

	mustCloseStore(t, store)
}

func TestRemoveBackup_NoBackup(t *testing.T) {
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "quant.db")
	store, err := NewStore(dbPath)
	if err != nil {
		t.Fatalf("NewStore() error = %v", err)
	}
	mustCloseStore(t, store)

	s := &Store{backup: ""}
	s.RemoveBackup()
}

func TestStoreContentDedup_CRUD(t *testing.T) {
	dir := t.TempDir()
	store, err := NewStore(filepath.Join(dir, "quant.db"))
	if err != nil {
		t.Fatalf("NewStore() error = %v", err)
	}
	mustCloseStore(t, store)

	ctx := context.Background()

	embedding := EncodeFloat32([]float32{0.5, 0.3, 0.1})
	hash := "sha256:abc123"

	if _, found := store.LookupContentDedup(ctx, hash); found {
		t.Fatal("expected content dedup to not exist yet")
	}

	if err := store.StoreContentDedup(ctx, hash, embedding); err != nil {
		t.Fatalf("StoreContentDedup() error = %v", err)
	}

	got, found := store.LookupContentDedup(ctx, hash)
	if !found {
		t.Fatal("expected content dedup to exist after storing")
	}
	if len(got) == 0 {
		t.Fatal("expected non-empty embedding data")
	}

	if err := store.RemoveContentDedup(ctx, hash); err != nil {
		t.Fatalf("RemoveContentDedup() error = %v", err)
	}

	if _, found := store.LookupContentDedup(ctx, hash); found {
		t.Fatal("expected content dedup to be removed")
	}
}

func TestStoreContentDedup_Upsert(t *testing.T) {
	dir := t.TempDir()
	store, err := NewStore(filepath.Join(dir, "quant.db"))
	if err != nil {
		t.Fatalf("NewStore() error = %v", err)
	}
	mustCloseStore(t, store)

	ctx := context.Background()

	embedding1 := EncodeFloat32([]float32{1, 0})
	embedding2 := EncodeFloat32([]float32{0, 1})
	hash := "sha256:upsert-test"

	if err := store.StoreContentDedup(ctx, hash, embedding1); err != nil {
		t.Fatalf("StoreContentDedup() first error = %v", err)
	}
	if err := store.StoreContentDedup(ctx, hash, embedding2); err != nil {
		t.Fatalf("StoreContentDedup() second error = %v", err)
	}

	got, found := store.LookupContentDedup(ctx, hash)
	if !found {
		t.Fatal("expected content dedup to exist")
	}
	decoded := decodeFloat32(got)
	if len(decoded) != 2 || decoded[0] != 0 || decoded[1] != 1 {
		t.Fatalf("expected upserted embedding, got %v", decoded)
	}
}

func TestBuildMetadataFilter(t *testing.T) {
	dir := t.TempDir()
	store, err := NewStore(filepath.Join(dir, "quant.db"))
	if err != nil {
		t.Fatalf("NewStore() error = %v", err)
	}
	mustCloseStore(t, store)

	tests := []struct {
		name      string
		filter    SearchFilter
		wantEmpty bool
		wantIn    []string
		wantArgs  int
	}{
		{
			name:      "empty filter",
			filter:    SearchFilter{},
			wantEmpty: true,
		},
		{
			name:     "file_types only",
			filter:   SearchFilter{FileTypes: []string{"go"}},
			wantIn:   []string{"d.file_type"},
			wantArgs: 1,
		},
		{
			name:     "languages only",
			filter:   SearchFilter{Languages: []string{"python"}},
			wantIn:   []string{"d.language"},
			wantArgs: 1,
		},
		{
			name:     "tags only",
			filter:   SearchFilter{Tags: map[string]string{"framework": "react"}},
			wantIn:   []string{"d.tags"},
			wantArgs: 1,
		},
		{
			name:     "collection only",
			filter:   SearchFilter{Collection: "mycol"},
			wantIn:   []string{"d.collection"},
			wantArgs: 1,
		},
		{
			name:     "combined file_types and languages",
			filter:   SearchFilter{FileTypes: []string{"go", "python"}, Languages: []string{"go"}},
			wantIn:   []string{"d.file_type", "d.language"},
			wantArgs: 3,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			where, args := store.buildMetadataFilter(tt.filter)
			if tt.wantEmpty {
				if where != "" {
					t.Fatalf("expected empty WHERE clause, got %q", where)
				}
				if args != nil {
					t.Fatalf("expected nil args, got %v", args)
				}
				return
			}
			if !strings.HasPrefix(where, " AND ") {
				t.Fatalf("expected WHERE clause to start with ' AND ', got %q", where)
			}
			for _, want := range tt.wantIn {
				if !strings.Contains(where, want) {
					t.Fatalf("expected WHERE clause to contain %q, got %q", want, where)
				}
			}
			if len(args) != tt.wantArgs {
				t.Fatalf("expected %d args, got %d", tt.wantArgs, len(args))
			}
		})
	}
}

func TestSearchFiltered_WithFileTypeFilter(t *testing.T) {
	dir := t.TempDir()
	store, err := NewStore(filepath.Join(dir, "quant.db"))
	if err != nil {
		t.Fatalf("NewStore() error = %v", err)
	}
	mustCloseStore(t, store)

	ctx := context.Background()

	doc1 := &Document{Path: "src/main.go", Hash: "h1", ModifiedAt: time.Now(), FileType: "go", Language: "go"}
	doc2 := &Document{Path: "docs/readme.md", Hash: "h2", ModifiedAt: time.Now(), FileType: "markdown", Language: "markdown"}

	id1, err := store.UpsertDocument(ctx, doc1)
	if err != nil {
		t.Fatalf("UpsertDocument() error = %v", err)
	}
	id2, err := store.UpsertDocument(ctx, doc2)
	if err != nil {
		t.Fatalf("UpsertDocument() error = %v", err)
	}

	embedding := NormalizeFloat32([]float32{1})
	for _, ch := range []struct {
		docID   int64
		content string
	}{
		{id1, "package main func"},
		{id2, "readme documentation guide"},
	} {
		if err := store.InsertChunk(ctx, &ChunkRecord{DocumentID: ch.docID, Content: ch.content, ChunkIndex: 0, Embedding: EncodeFloat32(embedding)}); err != nil {
			t.Fatalf("InsertChunk() error = %v", err)
		}
	}

	results, err := store.SearchFiltered(ctx, "package", embedding, 10, "", SearchFilter{FileTypes: []string{"go"}})
	if err != nil {
		t.Fatalf("SearchFiltered() error = %v", err)
	}

	found := false
	for _, r := range results {
		if r.DocumentPath == "src/main.go" {
			found = true
		}
	}
	if !found {
		t.Fatal("expected src/main.go in search results")
	}
}

func TestSearchFiltered_WithLanguageFilter(t *testing.T) {
	dir := t.TempDir()
	store, err := NewStore(filepath.Join(dir, "quant.db"))
	if err != nil {
		t.Fatalf("NewStore() error = %v", err)
	}
	mustCloseStore(t, store)

	ctx := context.Background()

	doc1 := &Document{Path: "src/app.py", Hash: "h1", ModifiedAt: time.Now(), FileType: "python", Language: "python"}
	doc2 := &Document{Path: "src/util.go", Hash: "h2", ModifiedAt: time.Now(), FileType: "go", Language: "go"}

	id1, err := store.UpsertDocument(ctx, doc1)
	if err != nil {
		t.Fatalf("UpsertDocument() error = %v", err)
	}
	id2, err := store.UpsertDocument(ctx, doc2)
	if err != nil {
		t.Fatalf("UpsertDocument() error = %v", err)
	}

	embedding := NormalizeFloat32([]float32{1})
	for _, ch := range []struct {
		docID   int64
		content string
	}{
		{id1, "python function def"},
		{id2, "go struct interface"},
	} {
		if err := store.InsertChunk(ctx, &ChunkRecord{DocumentID: ch.docID, Content: ch.content, ChunkIndex: 0, Embedding: EncodeFloat32(embedding)}); err != nil {
			t.Fatalf("InsertChunk() error = %v", err)
		}
	}

	results, err := store.SearchFiltered(ctx, "function", embedding, 10, "", SearchFilter{Languages: []string{"python"}})
	if err != nil {
		t.Fatalf("SearchFiltered() error = %v", err)
	}

	found := false
	for _, r := range results {
		if r.DocumentPath == "src/app.py" {
			found = true
		}
	}
	if !found {
		t.Fatal("expected src/app.py in search results")
	}
}

func TestRegisterSignal_AndApplySignalsToRanking(t *testing.T) {
	registry := NewSignalRegistry()
	registry.Register(&KeywordSignal{})
	registry.Register(&VectorSignal{})

	ctx := &SignalContext{
		Query:       "test",
		QueryTokens: []string{"test"},
		Weights:     QuerySignalWeights{Keyword: 1, Vector: 1},
	}

	candidates := []scoredCandidate{
		{result: SearchResult{DocumentPath: "a.go"}, keywordRank: 1, vectorRank: 2},
		{result: SearchResult{DocumentPath: "b.go"}, keywordRank: 2, vectorRank: 1},
	}

	scored := registry.ApplySignals(ctx, candidates)
	if scored[0].score <= 0 && scored[1].score <= 0 {
		t.Fatal("expected at least one candidate with positive score")
	}

	scored = ApplySignalsToRanking(candidates, ctx)
	if scored[0].score <= 0 && scored[1].score <= 0 {
		t.Fatal("expected global ApplySignalsToRanking to produce positive scores")
	}

	DefaultSignalRegistry.List()
}

func TestCreateSignalContext(t *testing.T) {
	ctx := CreateSignalContext("hello world", QuerySignalWeights{Keyword: 1.5, Vector: 0.8}, true, false)
	if ctx.Query != "hello world" {
		t.Fatalf("expected query 'hello world', got %q", ctx.Query)
	}
	if len(ctx.QueryTokens) == 0 {
		t.Fatal("expected non-empty query tokens")
	}
	if !ctx.HasKeyword {
		t.Fatal("expected HasKeyword=true")
	}
	if ctx.HasVector {
		t.Fatal("expected HasVector=false")
	}
	if ctx.Weights.Keyword != 1.5 || ctx.Weights.Vector != 0.8 {
		t.Fatalf("unexpected weights: %+v", ctx.Weights)
	}
	if ctx.Now.IsZero() {
		t.Fatal("expected non-zero Now")
	}
}

func TestVectorSignal_ScoreWithPositiveRank(t *testing.T) {
	ctx := &SignalContext{
		Query:       "test",
		QueryTokens: []string{"test"},
		Weights:     QuerySignalWeights{Keyword: 1, Vector: 1},
	}

	candidate := &scoredCandidate{vectorRank: 3}
	score := (&VectorSignal{}).Score(ctx, candidate)
	if score <= 0 {
		t.Fatalf("expected positive score for vectorRank=3, got %f", score)
	}

	candidateZero := &scoredCandidate{vectorRank: 0}
	scoreZero := (&VectorSignal{}).Score(ctx, candidateZero)
	if scoreZero != 0 {
		t.Fatalf("expected zero score for vectorRank=0, got %f", scoreZero)
	}
}

func TestVectorSignal_ScoreWithWeightOverride(t *testing.T) {
	ctx := &SignalContext{
		Query:       "test",
		QueryTokens: []string{"test"},
		Weights:     QuerySignalWeights{Keyword: 1, Vector: 0.5},
	}

	candidate := &scoredCandidate{vectorRank: 2}
	defaultScore := (&VectorSignal{}).Score(ctx, candidate)

	overrideScore := (&VectorSignal{WeightOverride: 3.0}).Score(ctx, candidate)
	if overrideScore <= defaultScore {
		t.Fatalf("expected override score > default score, got override=%f default=%f", overrideScore, defaultScore)
	}
}
