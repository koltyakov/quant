package chunk

import (
	"fmt"
	"strings"
	"testing"
)

func TestSplitCode_BasicPython(t *testing.T) {
	var lines []string
	lines = append(lines, "import os")
	lines = append(lines, "")
	for i := 0; i < 5; i++ {
		lines = append(lines, fmt.Sprintf("def function_%d():", i))
		for j := 0; j < 10; j++ {
			lines = append(lines, fmt.Sprintf("    print(\"line %d from function %d\")", j, i))
		}
		lines = append(lines, "")
	}
	src := strings.Join(lines, "\n")

	chunks := splitCode(src, 100, 0.15)
	if len(chunks) < 2 {
		t.Fatalf("expected at least 2 chunks, got %d", len(chunks))
	}
	joined := strings.Join(func() []string {
		out := make([]string, len(chunks))
		for i, c := range chunks {
			out[i] = c.Content
		}
		return out
	}(), "\n")
	if !strings.Contains(joined, "def function_0()") {
		t.Errorf("expected 'def function_0()' in chunks, got: %q", joined[:min(len(joined), 200)])
	}
	if !strings.Contains(joined, "def function_4()") {
		t.Errorf("expected 'def function_4()' in chunks, got: %q", joined[:min(len(joined), 200)])
	}
}

func TestSplitCode_SingleBlock(t *testing.T) {
	src := `some text without top level declarations`
	chunks := splitCode(src, 100, 0.15)
	if chunks != nil {
		t.Fatalf("expected nil for <2 boundaries, got %d chunks", len(chunks))
	}
}

func TestSplitCode_MergesAdjacentSmallBlocks(t *testing.T) {
	src := `func a() {}
func b() {}
func c() {}
func d() {}
`
	chunks := splitCode(src, 10, 0.15)
	if len(chunks) < 1 {
		t.Fatalf("expected at least 1 chunk, got %d", len(chunks))
	}
}

func TestSplitCode_LargeBlockSplit(t *testing.T) {
	var lines []string
	for i := 0; i < 5; i++ {
		lines = append(lines, fmt.Sprintf("func func%d() {", i))
		for j := 0; j < 200; j++ {
			lines = append(lines, fmt.Sprintf("    x = %s", strings.Repeat("word ", 20)))
		}
		lines = append(lines, "}")
		lines = append(lines, "")
	}
	src := strings.Join(lines, "\n")

	chunks := splitCode(src, 10, 0.15)
	if len(chunks) < 2 {
		t.Fatalf("expected large block to be split, got %d chunks", len(chunks))
	}
}

func TestCodeSignature(t *testing.T) {
	tests := []struct {
		input string
		want  string
	}{
		{"func main() {}", "func main() {}"},
		{"  \nfunc main() {}", "func main() {}"},
		{strings.Repeat("x", 150), strings.Repeat("x", 120) + "..."},
		{"", ""},
	}
	for _, tt := range tests {
		got := codeSignature(tt.input)
		if got != tt.want {
			t.Errorf("codeSignature(%q) = %q, want %q", tt.input, got, tt.want)
		}
	}
}

func TestCodeBlockBoundaries(t *testing.T) {
	src := `func a() {}
not a declaration
func b() {}
    indented line
class MyClass:
    pass
`
	lines := strings.Split(src, "\n")
	bounds := codeBlockBoundaries(lines)
	if len(bounds) < 2 {
		t.Fatalf("expected at least 2 boundaries, got %d: %v", len(bounds), bounds)
	}
}

func TestSplitWithPath_GoFile(t *testing.T) {
	var lines []string
	lines = append(lines, "package main")
	lines = append(lines, "")
	lines = append(lines, "import \"fmt\"")
	lines = append(lines, "")
	for i := 0; i < 5; i++ {
		lines = append(lines, fmt.Sprintf("func function%d() {", i))
		for j := 0; j < 10; j++ {
			lines = append(lines, fmt.Sprintf("    fmt.Println(\"line %d from function %d\")", j, i))
		}
		lines = append(lines, "}")
		lines = append(lines, "")
	}
	src := strings.Join(lines, "\n")

	chunks := SplitWithPath(src, "test.go", 50, 0.15)
	if len(chunks) < 1 {
		t.Fatalf("expected Go splitting, got %d chunks", len(chunks))
	}
	joined := ""
	for _, c := range chunks {
		joined += c.Content
	}
	if !strings.Contains(joined, "package main") {
		t.Errorf("expected preamble with package declaration, got: %q", joined[:min(len(joined), 200)])
	}
}

func TestSplitWithPath_PythonFile(t *testing.T) {
	var lines []string
	lines = append(lines, "import os")
	lines = append(lines, "")
	for i := 0; i < 5; i++ {
		lines = append(lines, fmt.Sprintf("def function_%d():", i))
		for j := 0; j < 10; j++ {
			lines = append(lines, fmt.Sprintf("    print(\"line %d from function %d\")", j, i))
		}
		lines = append(lines, "")
	}
	src := strings.Join(lines, "\n")

	chunks := SplitWithPath(src, "test.py", 50, 0.15)
	if len(chunks) < 1 {
		t.Fatalf("expected code splitting for .py, got %d chunks", len(chunks))
	}
}

func TestSplitWithPath_FallbackToGeneric(t *testing.T) {
	text := "hello world this is a test"
	chunks := SplitWithPath(text, "test.txt", 100, 0.15)
	if len(chunks) != 1 {
		t.Fatalf("expected generic split fallback, got %d chunks", len(chunks))
	}
}

func TestSplitWithPath_EmptyPath(t *testing.T) {
	text := "hello world"
	chunks := SplitWithPath(text, "", 100, 0.15)
	if len(chunks) != 1 {
		t.Fatalf("expected generic split for empty path, got %d chunks", len(chunks))
	}
}

func TestRuneCount(t *testing.T) {
	tests := []struct {
		input string
		want  int
	}{
		{"hello", 5},
		{"", 0},
		{"日本語", 3},
	}
	for _, tt := range tests {
		got := runeCount(tt.input)
		if got != tt.want {
			t.Errorf("runeCount(%q) = %d, want %d", tt.input, got, tt.want)
		}
	}
}

func TestCodeCharBudget(t *testing.T) {
	got := codeCharBudget(100)
	if got != 500 {
		t.Errorf("codeCharBudget(100) = %d, want 500", got)
	}
}
