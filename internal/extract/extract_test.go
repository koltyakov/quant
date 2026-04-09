package extract

import (
	"archive/zip"
	"context"
	"errors"
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

func TestPDFExtractor_ExtractPrefersNativeText(t *testing.T) {
	ext := &PDFExtractor{
		extractNative: func(path string) (string, error) {
			if path != "file.pdf" {
				t.Fatalf("unexpected path: %s", path)
			}
			return "[Page 1]\nhello world", nil
		},
		findOCRBinary: func() (string, bool) {
			t.Fatal("ocrmypdf lookup should not run when native PDF text exists")
			return "", false
		},
		runOCR: func(ctx context.Context, binaryPath, path, languages string) (string, error) {
			t.Fatal("ocrmypdf should not run when native PDF text exists")
			return "", nil
		},
	}

	text, err := ext.Extract(context.Background(), "file.pdf")
	if err != nil {
		t.Fatalf("unexpected extract error: %v", err)
	}
	if text != "[Page 1]\nhello world" {
		t.Fatalf("unexpected extracted text: %q", text)
	}
}

func TestPDFExtractor_ExtractUsesOCRFallbackWhenNativeTextMissing(t *testing.T) {
	ext := &PDFExtractor{
		extractNative: func(path string) (string, error) {
			if path != "scan.pdf" {
				t.Fatalf("unexpected path: %s", path)
			}
			return "", nil
		},
		findOCRBinary: func() (string, bool) {
			return "/usr/bin/ocrmypdf", true
		},
		runOCR: func(ctx context.Context, binaryPath, path, languages string) (string, error) {
			if binaryPath != "/usr/bin/ocrmypdf" {
				t.Fatalf("unexpected ocrmypdf path: %s", binaryPath)
			}
			if path != "scan.pdf" {
				t.Fatalf("unexpected pdf path: %s", path)
			}
			if languages != "eng" {
				t.Fatalf("unexpected OCR languages: %s", languages)
			}
			return "[Page 1]\nscanned text", nil
		},
	}

	text, err := ext.Extract(context.Background(), "scan.pdf")
	if err != nil {
		t.Fatalf("unexpected extract error: %v", err)
	}
	if text != "[Page 1]\nscanned text" {
		t.Fatalf("unexpected OCR text: %q", text)
	}
}

func TestPDFExtractor_ExtractSkipsOCRWhenBinaryUnavailable(t *testing.T) {
	ext := &PDFExtractor{
		extractNative: func(path string) (string, error) {
			return "", nil
		},
		findOCRBinary: func() (string, bool) {
			return "", false
		},
		runOCR: func(ctx context.Context, binaryPath, path, languages string) (string, error) {
			t.Fatal("ocrmypdf should not run when the binary is unavailable")
			return "", nil
		},
	}

	text, err := ext.Extract(context.Background(), "scan.pdf")
	if err != nil {
		t.Fatalf("unexpected extract error: %v", err)
	}
	if text != "" {
		t.Fatalf("expected empty text when OCR is unavailable, got %q", text)
	}
}

func TestPDFExtractor_ExtractIgnoresOCRFailure(t *testing.T) {
	ext := &PDFExtractor{
		extractNative: func(path string) (string, error) {
			return "", nil
		},
		findOCRBinary: func() (string, bool) {
			return "/usr/bin/ocrmypdf", true
		},
		runOCR: func(ctx context.Context, binaryPath, path, languages string) (string, error) {
			return "", errors.New("boom")
		},
	}

	text, err := ext.Extract(context.Background(), "scan.pdf")
	if err != nil {
		t.Fatalf("unexpected extract error: %v", err)
	}
	if text != "" {
		t.Fatalf("expected empty text after OCR failure, got %q", text)
	}
}

func TestPDFExtractor_ExtractPropagatesCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	ext := &PDFExtractor{
		extractNative: func(path string) (string, error) {
			return "", nil
		},
		findOCRBinary: func() (string, bool) {
			return "/usr/bin/ocrmypdf", true
		},
		runOCR: func(ctx context.Context, binaryPath, path, languages string) (string, error) {
			return "", ctx.Err()
		},
	}

	_, err := ext.Extract(ctx, "scan.pdf")
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("expected context cancellation, got %v", err)
	}
}

func TestPDFExtractor_ExtractUsesConfiguredLanguages(t *testing.T) {
	ext := &PDFExtractor{
		ocrLanguages: "rus+eng",
		extractNative: func(path string) (string, error) {
			return "", nil
		},
		findOCRBinary: func() (string, bool) {
			return "/usr/bin/ocrmypdf", true
		},
		runOCR: func(ctx context.Context, binaryPath, path, languages string) (string, error) {
			if languages != "rus+eng" {
				t.Fatalf("unexpected OCR languages: %s", languages)
			}
			return "text", nil
		},
	}

	text, err := ext.Extract(context.Background(), "scan.pdf")
	if err != nil {
		t.Fatalf("unexpected extract error: %v", err)
	}
	if text != "text" {
		t.Fatalf("unexpected OCR text: %q", text)
	}
}

func TestRouter_SupportsOOXML(t *testing.T) {
	r := NewRouter()
	for _, path := range []string{
		"file.docx", "file.docm", "file.dotx", "file.dotm",
		"file.pptx", "file.pptm", "file.ppsx", "file.ppsm", "file.potx", "file.potm",
		"file.xlsx", "file.xlsm", "file.xltx", "file.xltm", "file.xlam",
	} {
		if !r.Supports(path) {
			t.Errorf("expected support for %s", path)
		}
	}
}

func TestRouter_SupportsNotebookAndODF(t *testing.T) {
	r := NewRouter()
	for _, path := range []string{"file.ipynb", "file.odt", "file.ods", "file.odp"} {
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
	if err := os.WriteFile(path, []byte(content), 0644); err != nil {
		t.Fatalf("unexpected write error: %v", err)
	}

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
	if err := os.WriteFile(path, []byte(content), 0644); err != nil {
		t.Fatalf("unexpected write error: %v", err)
	}

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
	if err := os.WriteFile(path, []byte(content), 0644); err != nil {
		t.Fatalf("unexpected write error: %v", err)
	}

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
	if err := os.WriteFile(path, []byte(content), 0644); err != nil {
		t.Fatalf("unexpected write error: %v", err)
	}

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

func TestOOXMLExtractor_ExtractPPTX(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "slides.pptx")

	file, err := os.Create(path)
	if err != nil {
		t.Fatalf("unexpected error creating pptx: %v", err)
	}

	zw := zip.NewWriter(file)
	writeZipEntry := func(name, content string) {
		w, err := zw.Create(name)
		if err != nil {
			t.Fatalf("unexpected error creating zip entry %s: %v", name, err)
		}
		if _, err := w.Write([]byte(content)); err != nil {
			t.Fatalf("unexpected error writing zip entry %s: %v", name, err)
		}
	}

	writeZipEntry("ppt/slides/slide1.xml", `
		<p:sld xmlns:p="http://schemas.openxmlformats.org/presentationml/2006/main"
		       xmlns:a="http://schemas.openxmlformats.org/drawingml/2006/main">
			<p:cSld>
				<p:spTree>
					<p:sp>
						<p:txBody>
							<a:p><a:r><a:t>Slide body text.</a:t></a:r></a:p>
						</p:txBody>
					</p:sp>
				</p:spTree>
			</p:cSld>
		</p:sld>
	`)
	writeZipEntry("ppt/notesSlides/notesSlide1.xml", `
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

	if err := zw.Close(); err != nil {
		t.Fatalf("unexpected error closing zip writer: %v", err)
	}
	if err := file.Close(); err != nil {
		t.Fatalf("unexpected error closing pptx: %v", err)
	}

	ext := &OOXMLExtractor{}
	text, err := ext.Extract(context.Background(), path)
	if err != nil {
		t.Fatalf("unexpected extract error: %v", err)
	}

	want := "[Slide 1]\nSlide body text.\n\n[Notes]\nSpeaker notes text here."
	if text != want {
		t.Fatalf("unexpected pptx extraction: %q", text)
	}
}

func TestNotebookExtractor_ExtractIPYNB(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "notebook.ipynb")

	content := `{
	  "cells": [
	    {
	      "cell_type": "markdown",
	      "source": ["# Title\n", "Notebook intro."]
	    },
	    {
	      "cell_type": "code",
	      "source": ["print('hi')\n"],
	      "outputs": [
	        {
	          "text": ["hi\n"]
	        }
	      ]
	    }
	  ]
	}`
	if err := os.WriteFile(path, []byte(content), 0644); err != nil {
		t.Fatalf("unexpected notebook write error: %v", err)
	}

	ext := &NotebookExtractor{}
	text, err := ext.Extract(context.Background(), path)
	if err != nil {
		t.Fatalf("unexpected notebook extract error: %v", err)
	}

	want := "[Markdown Cell 1]\n# Title\nNotebook intro.\n\n[Code Cell 2]\nprint('hi')\n\n[Output]\nhi"
	if text != want {
		t.Fatalf("unexpected notebook extraction: %q", text)
	}
}

func TestODFExtractor_ExtractODT(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "document.odt")
	writeZipArchive(t, path, map[string]string{
		"content.xml": `
			<office:document-content xmlns:office="office" xmlns:text="text">
				<office:body>
					<office:text>
						<text:h>Heading</text:h>
						<text:p>Paragraph one.</text:p>
						<text:p>Paragraph two.</text:p>
					</office:text>
				</office:body>
			</office:document-content>
		`,
	})

	ext := &ODFExtractor{}
	text, err := ext.Extract(context.Background(), path)
	if err != nil {
		t.Fatalf("unexpected odt extract error: %v", err)
	}

	want := "[Document]\nHeading\n\nParagraph one.\n\nParagraph two."
	if text != want {
		t.Fatalf("unexpected odt extraction: %q", text)
	}
}

func TestODFExtractor_ExtractODS(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "sheet.ods")
	writeZipArchive(t, path, map[string]string{
		"content.xml": `
			<office:document-content xmlns:office="office" xmlns:table="table" xmlns:text="text">
				<office:body>
					<office:spreadsheet>
						<table:table table:name="Budget">
							<table:table-row>
								<table:table-cell><text:p>Q1</text:p></table:table-cell>
								<table:table-cell office:value="42"><text:p>Total</text:p></table:table-cell>
							</table:table-row>
						</table:table>
					</office:spreadsheet>
				</office:body>
			</office:document-content>
		`,
	})

	ext := &ODFExtractor{}
	text, err := ext.Extract(context.Background(), path)
	if err != nil {
		t.Fatalf("unexpected ods extract error: %v", err)
	}

	want := "[Sheet Budget]\nQ1 | 42 Total"
	if text != want {
		t.Fatalf("unexpected ods extraction: %q", text)
	}
}

func TestODFExtractor_ExtractODP(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "slides.odp")
	writeZipArchive(t, path, map[string]string{
		"content.xml": `
			<office:document-content xmlns:office="office" xmlns:draw="draw" xmlns:text="text">
				<office:body>
					<office:presentation>
						<draw:page draw:name="Overview">
							<draw:frame>
								<draw:text-box>
									<text:p>Slide body text.</text:p>
								</draw:text-box>
							</draw:frame>
						</draw:page>
					</office:presentation>
				</office:body>
			</office:document-content>
		`,
	})

	ext := &ODFExtractor{}
	text, err := ext.Extract(context.Background(), path)
	if err != nil {
		t.Fatalf("unexpected odp extract error: %v", err)
	}

	want := "[Slide 1: Overview]\nSlide body text."
	if text != want {
		t.Fatalf("unexpected odp extraction: %q", text)
	}
}

func writeZipArchive(t *testing.T, path string, entries map[string]string) {
	t.Helper()

	file, err := os.Create(path)
	if err != nil {
		t.Fatalf("unexpected error creating archive: %v", err)
	}
	t.Cleanup(func() {
		if err := file.Close(); err != nil {
			t.Fatalf("unexpected file close error: %v", err)
		}
	})

	zw := zip.NewWriter(file)
	for name, content := range entries {
		w, err := zw.Create(name)
		if err != nil {
			t.Fatalf("unexpected error creating zip entry %s: %v", name, err)
		}
		if _, err := w.Write([]byte(content)); err != nil {
			t.Fatalf("unexpected error writing zip entry %s: %v", name, err)
		}
	}
	if err := zw.Close(); err != nil {
		t.Fatalf("unexpected error closing zip writer: %v", err)
	}
}
