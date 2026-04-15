package extract

import (
	"strings"
	"testing"
)

func TestWriteParagraphBreak(t *testing.T) {
	tests := []struct {
		name string
		init string
		want string
	}{
		{"empty buffer", "", ""},
		{"no trailing newline", "hello", "hello\n\n"},
		{"single trailing newline", "hello\n", "hello\n\n"},
		{"double trailing newline", "hello\n\n", "hello\n\n"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var buf strings.Builder
			buf.WriteString(tt.init)
			writeParagraphBreak(&buf)
			got := buf.String()
			if got != tt.want {
				t.Errorf("writeParagraphBreak after %q = %q, want %q", tt.init, got, tt.want)
			}
		})
	}
}

func TestResolveCellValue(t *testing.T) {
	sharedStrings := []string{"Policy Number", "Name", "Value"}

	tests := []struct {
		name          string
		cellType      string
		content       string
		sharedStrings []string
		want          string
	}{
		{"shared string valid index", "s", "0", sharedStrings, "Policy Number"},
		{"shared string index 2", "s", "2", sharedStrings, "Value"},
		{"shared string out of bounds", "s", "5", sharedStrings, "5"},
		{"shared string negative", "s", "-1", sharedStrings, "-1"},
		{"shared string non-numeric", "s", "abc", sharedStrings, "abc"},
		{"shared string with spaces", "s", " 0 ", sharedStrings, "Policy Number"},
		{"shared string nil", "s", "0", nil, "0"},
		{"shared string empty slice", "s", "0", []string{}, "0"},
		{"boolean true", "b", "1", nil, "TRUE"},
		{"boolean false", "b", "0", nil, "FALSE"},
		{"boolean other value", "b", "2", nil, "FALSE"},
		{"default type returns content", "", "42", nil, "42"},
		{"default type with text", "str", "hello", nil, "hello"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := resolveCellValue(tt.cellType, tt.content, tt.sharedStrings)
			if got != tt.want {
				t.Errorf("resolveCellValue(%q, %q, %v) = %q, want %q", tt.cellType, tt.content, tt.sharedStrings, got, tt.want)
			}
		})
	}
}

func TestCleanSpacing(t *testing.T) {
	tests := []struct {
		name  string
		input string
		want  string
	}{
		{"trailing spaces", "hello   \nworld  \n", "hello\nworld"},
		{"trailing tabs", "hello\t\nworld\t\n", "hello\nworld"},
		{"leading spaces trimmed", "  indent\n  more", "indent\n  more"},
		{"multiple trailing newlines", "hello\n\n\n", "hello"},
		{"empty string", "", ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := cleanSpacing(tt.input)
			if got != tt.want {
				t.Errorf("cleanSpacing(%q) = %q, want %q", tt.input, got, tt.want)
			}
		})
	}
}
