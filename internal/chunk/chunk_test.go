package chunk

import (
	"strings"
	"testing"
)

func TestSplit_Empty(t *testing.T) {
	result := Split("", 512, 0.15)
	if result != nil {
		t.Fatalf("expected nil for empty input, got %v", result)
	}
}

func TestSplit_SingleChunk(t *testing.T) {
	text := "hello world this is a test"
	result := Split(text, 100, 0.15)
	if len(result) != 1 {
		t.Fatalf("expected 1 chunk, got %d", len(result))
	}
	if result[0].Content != text {
		t.Fatalf("expected %q, got %q", text, result[0].Content)
	}
	if result[0].Index != 0 {
		t.Fatalf("expected index 0, got %d", result[0].Index)
	}
}

func TestSplit_MultipleChunks(t *testing.T) {
	words := make([]string, 100)
	for i := range words {
		words[i] = "word"
	}
	text := strings.Join(words, " ")

	result := Split(text, 10, 0.2)
	if len(result) < 2 {
		t.Fatalf("expected at least 2 chunks, got %d", len(result))
	}

	for i, c := range result {
		if c.Index != i {
			t.Errorf("chunk %d has wrong index %d", i, c.Index)
		}
	}
}

func TestSplit_Overlap(t *testing.T) {
	words := make([]string, 30)
	for i := range words {
		words[i] = "word"
	}
	text := strings.Join(words, " ")

	result := Split(text, 10, 0.2)

	if len(result) < 2 {
		t.Fatalf("expected at least 2 chunks, got %d", len(result))
	}

	for _, c := range result {
		chunkWords := strings.Fields(c.Content)
		if len(chunkWords) > 12 {
			t.Errorf("chunk has too many words: %d", len(chunkWords))
		}
	}
}

func TestSplit_WhitespaceOnly(t *testing.T) {
	result := Split("   \t\n  ", 512, 0.15)
	if result != nil {
		t.Fatalf("expected nil for whitespace-only input, got %v", result)
	}
}

func TestSplit_InvalidChunkSize(t *testing.T) {
	result := Split("hello", 0, 0.15)
	if result != nil {
		t.Fatalf("expected nil for zero chunk size, got %v", result)
	}
}

func TestSplit_PreservesStructureMarkers(t *testing.T) {
	text := strings.Join([]string{
		"# Heading",
		"",
		"First paragraph with enough words to force chunking across structural boundaries.",
		"",
		"```go",
		"func main() {",
		"    println(\"hello\")",
		"}",
		"```",
		"",
		"[Page 2]",
		"Second paragraph with more words to keep going.",
	}, "\n")

	result := Split(text, 12, 0.2)
	if len(result) < 2 {
		t.Fatalf("expected multiple chunks, got %d", len(result))
	}

	joined := strings.Join(func() []string {
		out := make([]string, 0, len(result))
		for _, chunk := range result {
			out = append(out, chunk.Content)
		}
		return out
	}(), "\n")

	for _, marker := range []string{"# Heading", "```go", "[Page 2]"} {
		if !strings.Contains(joined, marker) {
			t.Fatalf("expected chunked output to preserve marker %q", marker)
		}
	}
}

func TestSplit_HeadingContextPropagation(t *testing.T) {
	text := strings.Join([]string{
		"# Introduction",
		"",
		"First paragraph about the introduction topic with several words to fill the chunk.",
		"",
		"Second paragraph continues the introduction with more content that will spill to next chunk.",
	}, "\n")

	result := Split(text, 12, 0)
	if len(result) < 2 {
		t.Fatalf("expected at least 2 chunks, got %d", len(result))
	}

	// The first chunk should start with the heading.
	if !strings.HasPrefix(result[0].Content, "# Introduction") {
		t.Errorf("first chunk should start with heading, got: %q", result[0].Content)
	}

	// Later chunks that don't start with a heading should have the heading prepended.
	for i := 1; i < len(result); i++ {
		if !strings.Contains(result[i].Content, "# Introduction") {
			t.Errorf("chunk %d should contain heading context, got: %q", i, result[i].Content)
		}
	}
}

func TestSplit_DocumentMarkers(t *testing.T) {
	text := "[Document]\nSome document content.\n\n[Header 1]\nHeader content."

	result := Split(text, 100, 0)
	if len(result) != 1 {
		t.Fatalf("expected 1 chunk, got %d", len(result))
	}
	if !strings.Contains(result[0].Content, "[Document]") {
		t.Error("expected [Document] marker preserved")
	}
	if !strings.Contains(result[0].Content, "[Header 1]") {
		t.Error("expected [Header 1] marker preserved")
	}
}

func TestSplitSentences_Abbreviations(t *testing.T) {
	tests := []struct {
		input    string
		expected int
	}{
		{"Dr. Smith went to Washington. He arrived Tuesday.", 2},
		{"The U.S. is a country. It is large.", 2},
		{"E.g. this is an example. Another sentence.", 2},
		{"Hello! How are you? I am fine.", 3},
		{"Version 3.14 is ready. Download now.", 2},
	}

	for _, tt := range tests {
		got := splitSentences(tt.input)
		if len(got) != tt.expected {
			t.Errorf("splitSentences(%q) = %d sentences %v, want %d", tt.input, len(got), got, tt.expected)
		}
	}
}
