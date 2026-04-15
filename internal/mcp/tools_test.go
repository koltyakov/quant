package mcp

import (
	"fmt"
	"testing"

	"github.com/koltyakov/quant/internal/index"
)

func TestTruncateRunes(t *testing.T) {
	tests := []struct {
		text  string
		limit int
		want  string
	}{
		{"hello", 3, "hel"},
		{"hello", 10, "hello"},
		{"hello", 0, ""},
		{"日本語テスト", 3, "日本語"},
	}
	for _, tt := range tests {
		got := truncateRunes(tt.text, tt.limit)
		if got != tt.want {
			t.Errorf("truncateRunes(%q, %d) = %q, want %q", tt.text, tt.limit, got, tt.want)
		}
	}
}

func TestTruncateRunesWithFlag(t *testing.T) {
	truncated, wasTruncated := truncateRunesWithFlag("hello", 3)
	if truncated != "hel" || !wasTruncated {
		t.Errorf("expected truncation, got %q truncated=%v", truncated, wasTruncated)
	}

	full, wasTruncated := truncateRunesWithFlag("hi", 10)
	if full != "hi" || wasTruncated {
		t.Errorf("expected no truncation, got %q truncated=%v", full, wasTruncated)
	}

	empty, wasTruncated := truncateRunesWithFlag("hello", 0)
	if empty != "" || !wasTruncated {
		t.Errorf("expected empty with truncation flag, got %q truncated=%v", empty, wasTruncated)
	}
}

func TestEntrySnippetBudget(t *testing.T) {
	result := index.SearchResult{
		DocumentPath: "test.go", ChunkIndex: 0, Score: 0.5, ScoreKind: "rrf", ChunkContent: "content",
	}
	budget := entrySnippetBudget(result, 200)
	if budget <= 0 {
		t.Errorf("expected positive budget, got %d", budget)
	}
	if budget >= 200 {
		t.Errorf("budget should be less than total after header deduction, got %d", budget)
	}
}

func TestEntrySnippetBudget_TooSmall(t *testing.T) {
	result := index.SearchResult{
		DocumentPath: "test.go", ChunkIndex: 0, Score: 0.5, ScoreKind: "rrf", ChunkContent: "content",
	}
	budget := entrySnippetBudget(result, 1)
	if budget != 0 {
		t.Errorf("expected 0 for tiny budget, got %d", budget)
	}
}

func TestFormatSearchResults_Empty(t *testing.T) {
	got := formatSearchResults(nil)
	if got != "No results found." {
		t.Errorf("expected empty message, got %q", got)
	}
}

func TestSummarizeLogText(t *testing.T) {
	tests := []struct {
		text  string
		limit int
		want  string
	}{
		{"hello world", 5, "he..."},
		{"  hello   world  ", 11, "hello world"},
		{"short", 100, "short"},
		{"test", 0, "test"},
	}
	for _, tt := range tests {
		got := summarizeLogText(tt.text, tt.limit)
		if got != tt.want {
			t.Errorf("summarizeLogText(%q, %d) = %q, want %q", tt.text, tt.limit, got, tt.want)
		}
	}
}

func TestFormatBytes(t *testing.T) {
	tests := []struct {
		input int64
		want  string
	}{
		{0, "0 B"},
		{512, "512 B"},
		{1024, "1.0 KB"},
		{1048576, "1.0 MB"},
	}
	for _, tt := range tests {
		got := formatBytes(tt.input)
		if got != tt.want {
			t.Errorf("formatBytes(%d) = %q, want %q", tt.input, got, tt.want)
		}
	}
}

func TestFormatSearchResults_OneResult(t *testing.T) {
	results := []index.SearchResult{
		{DocumentPath: "single.go", ChunkIndex: 0, Score: 0.95, ScoreKind: "hybrid", ChunkID: 1, ChunkContent: "hello world"},
	}
	output := formatSearchResults(results)
	if output == "" {
		t.Fatal("expected non-empty output")
	}
	if !contains(output, "single.go") || !contains(output, "hello world") {
		t.Fatalf("expected path and content in output, got %q", output)
	}
	if !contains(output, "Result 1") {
		t.Fatalf("expected numbered result header, got %q", output)
	}
}

func TestFormatSearchResults_LargeContentTruncated(t *testing.T) {
	longContent := make([]byte, 2000)
	for i := range longContent {
		longContent[i] = 'x'
	}
	results := []index.SearchResult{
		{DocumentPath: "big.txt", ChunkIndex: 0, Score: 0.9, ScoreKind: "rrf", ChunkID: 1, ChunkContent: string(longContent)},
	}
	output := formatSearchResults(results)
	if !contains(output, "big.txt") {
		t.Fatalf("expected path in output, got %q", output)
	}
}

func TestFormatSearchResults_OmittedResults(t *testing.T) {
	var results []index.SearchResult
	longContent := make([]byte, maxSearchOutputRunes)
	for i := range longContent {
		longContent[i] = 'x'
	}
	for i := 0; i < 20; i++ {
		results = append(results, index.SearchResult{
			DocumentPath: fmt.Sprintf("file%d.txt", i),
			ChunkIndex:   i,
			Score:        0.8,
			ScoreKind:    "rrf",
			ChunkID:      int64(i),
			ChunkContent: string(longContent),
		})
	}
	output := formatSearchResults(results)
	if !contains(output, "omitted") {
		t.Fatalf("expected omitted footer for many results, got output len %d", len(output))
	}
}

func TestRenderSearchResultEntry(t *testing.T) {
	result := index.SearchResult{
		DocumentPath: "entry.go",
		ChunkIndex:   3,
		Score:        0.77,
		ScoreKind:    "rrf",
		ChunkID:      42,
		ChunkContent: "func main()",
	}
	entry := renderSearchResultEntry(1, result, 1200)
	if !contains(entry, "entry.go") || !contains(entry, "func main()") {
		t.Fatalf("expected path and content in entry, got %q", entry)
	}
	if !contains(entry, "score: 0.7700") {
		t.Fatalf("expected score in entry, got %q", entry)
	}
	if !contains(entry, "chunk_id: 42") {
		t.Fatalf("expected chunk_id in entry, got %q", entry)
	}
}

func contains(s, sub string) bool {
	return len(s) >= len(sub) && (s == sub || len(sub) == 0 || containsSubstring(s, sub))
}

func containsSubstring(s, sub string) bool {
	for i := 0; i <= len(s)-len(sub); i++ {
		if s[i:i+len(sub)] == sub {
			return true
		}
	}
	return false
}
