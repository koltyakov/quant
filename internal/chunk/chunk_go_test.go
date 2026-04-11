package chunk

import (
	"fmt"
	"strings"
	"testing"
)

func TestSplitGo_Basic(t *testing.T) {
	var lines []string
	lines = append(lines, "package main")
	lines = append(lines, "")
	lines = append(lines, "import \"fmt\"")
	lines = append(lines, "")
	for i := 0; i < 5; i++ {
		lines = append(lines, fmt.Sprintf("func function%d() {", i))
		for j := 0; j < 10; j++ {
			lines = append(lines, fmt.Sprintf("    fmt.Println(\"line %d\")", j))
		}
		lines = append(lines, "}")
		lines = append(lines, "")
	}
	src := strings.Join(lines, "\n")

	chunks := splitGo(src, 50, 0.15)
	if len(chunks) < 2 {
		t.Fatalf("expected at least 2 chunks, got %d", len(chunks))
	}
	joined := ""
	for _, c := range chunks {
		joined += c.Content
	}
	if !strings.Contains(joined, "package main") {
		t.Errorf("expected preamble in chunks, got: %q", joined[:min(len(joined), 200)])
	}
	if !strings.Contains(joined, "func function0()") {
		t.Errorf("expected function0 in chunks")
	}
	if !strings.Contains(joined, "func function4()") {
		t.Errorf("expected function4 in chunks")
	}
}

func TestSplitGo_InvalidGo(t *testing.T) {
	src := "this is not valid go code"
	chunks := splitGo(src, 100, 0.15)
	if chunks != nil {
		t.Fatalf("expected nil for invalid Go, got %d chunks", len(chunks))
	}
}

func TestSplitGo_SingleFunc(t *testing.T) {
	src := `package main

func main() {
	println("hi")
}
`
	chunks := splitGo(src, 50, 0.15)
	if len(chunks) < 1 {
		t.Fatalf("expected at least 1 chunk for single func, got %d", len(chunks))
	}
}

func TestSplitGo_LargeDeclaration(t *testing.T) {
	lines := []string{"package main", "", "func big() {"}
	for i := 0; i < 200; i++ {
		lines = append(lines, fmt.Sprintf("    x := %d // %s", i, strings.Repeat("word", 20)))
	}
	lines = append(lines, "}")
	src := strings.Join(lines, "\n")

	chunks := splitGo(src, 10, 0.15)
	if len(chunks) < 2 {
		t.Fatalf("expected large func to be split, got %d chunks", len(chunks))
	}
}

func TestGoDeclSignature(t *testing.T) {
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
		got := goDeclSignature(tt.input)
		if got != tt.want {
			t.Errorf("goDeclSignature(%q) = %q, want %q", tt.input, got, tt.want)
		}
	}
}
