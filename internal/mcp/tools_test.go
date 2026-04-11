package mcp

import (
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

func TestNormalizeEmbeddingCacheKey(t *testing.T) {
	tests := []struct {
		input string
		want  string
	}{
		{"  hello   world  ", "hello world"},
		{"single", "single"},
		{"", ""},
	}
	for _, tt := range tests {
		got := normalizeEmbeddingCacheKey(tt.input)
		if got != tt.want {
			t.Errorf("normalizeEmbeddingCacheKey(%q) = %q, want %q", tt.input, got, tt.want)
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
