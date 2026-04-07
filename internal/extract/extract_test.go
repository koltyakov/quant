package extract

import (
	"context"
	"os"
	"path/filepath"
	"testing"
)

func TestRouter_SupportsTextFiles(t *testing.T) {
	r := NewRouter()

	paths := []string{
		"file.txt", "file.md", "file.go", "file.py", "file.js", "file.ts",
		"file.rs", "file.json", "file.yaml", "file.html",
		// New formats
		"file.mdx", "file.rst", "file.scss", "file.less", "file.astro",
		"file.kt", "file.groovy", "file.fs", "file.nim", "file.swift",
		"file.php", "file.jl", "file.fish", "file.elm", "file.graphql",
		"file.prisma", "file.tf", "file.nix", "file.diff", "file.patch",
	}
	for _, path := range paths {
		if !r.Supports(path) {
			t.Errorf("expected support for %s", path)
		}
	}
}

func TestRouter_SupportsPDF(t *testing.T) {
	r := NewRouter()
	if !r.Supports("file.pdf") {
		t.Error("expected support for .pdf")
	}
}

func TestRouter_SupportsOOXML(t *testing.T) {
	r := NewRouter()
	for _, path := range []string{"file.docx", "file.pptx", "file.xlsx"} {
		if !r.Supports(path) {
			t.Errorf("expected support for %s", path)
		}
	}
}

func TestRouter_SupportsRTF(t *testing.T) {
	r := NewRouter()
	if !r.Supports("file.rtf") {
		t.Error("expected support for .rtf")
	}
	if !r.Supports("FILE.RTF") {
		t.Error("expected case-insensitive support for .RTF")
	}
}

func TestRouter_Unsupported(t *testing.T) {
	r := NewRouter()
	if r.Supports("file.bin") {
		t.Error("expected no support for .bin")
	}
}

func TestRouter_SupportsBasenameOnlyFiles(t *testing.T) {
	r := NewRouter()
	for _, path := range []string{
		"Dockerfile", "Makefile", "Rakefile", "Gemfile",
		// New basenames
		"Justfile", "Vagrantfile", "Procfile",
		"go.mod", "go.sum", "requirements.txt",
		"LICENSE", "README", "CHANGELOG",
	} {
		if !r.Supports(path) {
			t.Errorf("expected support for %s", path)
		}
	}
}

func TestTextExtractor_Extract(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "test.txt")
	content := "hello world"
	os.WriteFile(path, []byte(content), 0644)

	ext := &TextExtractor{}
	text, err := ext.Extract(context.Background(), path)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if text != content {
		t.Errorf("expected %q, got %q", content, text)
	}
}

func TestRouter_ExtractTextFile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "test.go")
	content := "package main\nfunc main() {}"
	os.WriteFile(path, []byte(content), 0644)

	r := NewRouter()
	text, err := r.Extract(context.Background(), path)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if text != content {
		t.Errorf("expected %q, got %q", content, text)
	}
}

func TestExt(t *testing.T) {
	tests := []struct {
		path string
		want string
	}{
		{"file.txt", ".txt"},
		{"file.go", ".go"},
		{"/path/to/file.pdf", ".pdf"},
		{"file", ""},
		{"/path/to/file", ""},
	}
	for _, tt := range tests {
		got := ext(tt.path)
		if got != tt.want {
			t.Errorf("ext(%q) = %q, want %q", tt.path, got, tt.want)
		}
	}
}

func TestExtractWordMLText_PreservesParagraphs(t *testing.T) {
	xmlData := []byte(`
		<w:document xmlns:w="w">
			<w:body>
				<w:p><w:r><w:t>First paragraph.</w:t></w:r></w:p>
				<w:p><w:r><w:t>Second paragraph.</w:t></w:r></w:p>
			</w:body>
		</w:document>
	`)

	text := extractWordMLText(xmlData)
	if text != "First paragraph.\n\nSecond paragraph." {
		t.Fatalf("unexpected WordML extraction: %q", text)
	}
}

func TestParseSheetCells_IncludesRefsAndFormulas(t *testing.T) {
	xmlData := []byte(`
		<worksheet xmlns="x">
			<sheetData>
				<row r="1">
					<c r="A1" t="s"><v>0</v></c>
					<c r="B1"><v>42</v></c>
					<c r="C1"><f>SUM(B1:B2)</f><v>84</v></c>
				</row>
			</sheetData>
		</worksheet>
	`)

	rows := parseSheetCells(xmlData, []string{"Policy Number"})
	if len(rows) != 3 {
		t.Fatalf("expected 3 rows, got %d", len(rows))
	}
	if rows[0] != "A1: Policy Number" {
		t.Fatalf("unexpected shared-string row: %q", rows[0])
	}
	if rows[2] != "C1 = SUM(B1:B2) -> 84" {
		t.Fatalf("unexpected formula row: %q", rows[2])
	}
}

func TestRTFExtractor_Extract(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "test.rtf")
	// Simple RTF document.
	content := `{\rtf1\ansi Hello World.\par Second paragraph.}`
	os.WriteFile(path, []byte(content), 0644)

	ext := &RTFExtractor{}
	text, err := ext.Extract(context.Background(), path)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if text != "Hello World.\nSecond paragraph." {
		t.Fatalf("unexpected RTF extraction: %q", text)
	}
}

func TestRTFExtractor_Unicode(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "test.rtf")
	// RTF with unicode escape.
	content := `{\rtf1\ansi Smart \u8220"quotes\u8221" here.}`
	os.WriteFile(path, []byte(content), 0644)

	ext := &RTFExtractor{}
	text, err := ext.Extract(context.Background(), path)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if text != "Smart \u201Cquotes\u201D here." {
		t.Fatalf("unexpected RTF unicode extraction: %q", text)
	}
}

func TestExtractNotesText(t *testing.T) {
	// Minimal notesSlide XML with speaker notes content.
	xmlData := []byte(`
		<p:notes xmlns:p="http://schemas.openxmlformats.org/presentationml/2006/main"
		         xmlns:a="http://schemas.openxmlformats.org/drawingml/2006/main">
			<p:cSld>
				<p:spTree>
					<p:sp>
						<p:nvSpPr><p:nvPr><p:ph type="body" idx="1"/></p:nvPr></p:nvSpPr>
						<p:txBody>
							<a:p><a:r><a:t>Speaker notes text here.</a:t></a:r></a:p>
						</p:txBody>
					</p:sp>
				</p:spTree>
			</p:cSld>
		</p:notes>
	`)

	text := extractNotesText(xmlData)
	if text != "Speaker notes text here." {
		t.Fatalf("unexpected notes extraction: %q", text)
	}
}
