package extract

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestParsePPTXSlideNumber(t *testing.T) {
	tests := []struct {
		name  string
		input string
		want  int
		ok    bool
	}{
		{"slide1.xml", "ppt/slides/slide1.xml", 1, true},
		{"slide10.xml", "ppt/slides/slide10.xml", 10, true},
		{"slide99.xml", "ppt/slides/slide99.xml", 99, true},
		{"notes slide still parses", "ppt/notes/slide1.xml", 1, true},
		{"no number", "ppt/slides/slide.xml", 0, false},
		{"no xml suffix", "ppt/slides/slide1.txt", 0, false},
		{"non-numeric", "ppt/slides/slideA.xml", 0, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, ok := parsePPTXSlideNumber(tt.input)
			if got != tt.want || ok != tt.ok {
				t.Errorf("parsePPTXSlideNumber(%q) = (%d, %v), want (%d, %v)", tt.input, got, ok, tt.want, tt.ok)
			}
		})
	}
}

func TestNotebookText(t *testing.T) {
	tests := []struct {
		name string
		raw  json.RawMessage
		want string
	}{
		{"nil", nil, ""},
		{"empty", json.RawMessage{}, ""},
		{"string", json.RawMessage(`"hello world"`), "hello world"},
		{"string array", json.RawMessage(`["hello\n","world"]`), "hello\nworld"},
		{"invalid json", json.RawMessage(`{invalid}`), ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := notebookText(tt.raw)
			if got != tt.want {
				t.Errorf("notebookText(%s) = %q, want %q", string(tt.raw), got, tt.want)
			}
		})
	}
}

func TestAppendNotebookOutput(t *testing.T) {
	seen := make(map[string]struct{})
	parts := appendNotebookOutput(nil, seen, "hello")
	parts = appendNotebookOutput(parts, seen, "world")
	parts = appendNotebookOutput(parts, seen, "hello")
	if len(parts) != 2 {
		t.Errorf("expected 2 parts, got %d", len(parts))
	}
	if parts[0] != "hello" || parts[1] != "world" {
		t.Errorf("expected [hello, world], got %v", parts)
	}
	parts = appendNotebookOutput(parts, seen, "")
	if len(parts) != 2 {
		t.Errorf("expected 2 parts for empty append, got %d", len(parts))
	}
}

func TestRenderNotebookCell(t *testing.T) {
	tests := []struct {
		name   string
		source json.RawMessage
		want   string
	}{
		{"source only", json.RawMessage(`"print('hi')"`), "print('hi')"},
		{"empty cell", json.RawMessage(`""`), ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cell := notebookCell{CellType: "code", Source: tt.source}
			got, err := renderNotebookCell(context.TODO(), cell)
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			got = strings.TrimSpace(got)
			if tt.want == "" && got != "" {
				if !strings.HasPrefix(got, "[Code Cell") {
					t.Errorf("expected empty or labeled, got %q", got)
				}
			}
		})
	}
}

func TestNotebookExtractor_Supports(t *testing.T) {
	ext := &NotebookExtractor{}
	if !ext.Supports("file.ipynb") {
		t.Error("expected .ipynb to be supported")
	}
	if ext.Supports("file.txt") {
		t.Error("expected .txt not to be supported")
	}
}

func TestOoxmlKind(t *testing.T) {
	tests := []struct {
		path string
		want string
	}{
		{"file.docx", "word"},
		{"file.docm", "word"},
		{"file.dotx", "word"},
		{"file.dotm", "word"},
		{"file.pptx", "presentation"},
		{"file.pptm", "presentation"},
		{"file.ppsx", "presentation"},
		{"file.xlsx", "spreadsheet"},
		{"file.xlsm", "spreadsheet"},
		{"file.txt", ""},
		{"file.pdf", ""},
	}
	for _, tt := range tests {
		got := ooxmlKind(tt.path)
		if got != tt.want {
			t.Errorf("ooxmlKind(%q) = %q, want %q", tt.path, got, tt.want)
		}
	}
}

func TestPdfContentStack_ClearsReferences(t *testing.T) {
	var stk pdfContentStack
	stk.Push(pdfContentValue{kind: pdfContentStringKind, s: "test"})
	v := stk.Pop()
	if v.s != "test" {
		t.Errorf("expected 'test', got %q", v.s)
	}
	if stk.Len() != 0 {
		t.Errorf("expected empty stack, got len %d", stk.Len())
	}
}

func TestNewRouterDefaults(t *testing.T) {
	r := NewRouter()
	if r == nil {
		t.Fatal("expected non-nil router")
	}
	if len(r.extractors) == 0 {
		t.Fatal("expected extractors to be registered")
	}
}

func TestNewRouterWithOptions(t *testing.T) {
	r := NewRouter(Options{PDFOCRLang: "fra", PDFOCRTimeout: 5 * time.Minute})
	if r == nil {
		t.Fatal("expected non-nil router")
	}
}

func TestRouterExtractUnsupported(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "test.xyz")
	if err := os.WriteFile(path, []byte("data"), 0644); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	r := NewRouter()
	text, err := r.Extract(context.Background(), path)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if text != "" {
		t.Errorf("expected empty text for unsupported file, got %q", text)
	}
}

func TestPDFExtractor_Timeout(t *testing.T) {
	p := &PDFExtractor{}
	if p.timeout() != defaultPDFOCRTimeout {
		t.Errorf("expected default timeout, got %v", p.timeout())
	}
	p = &PDFExtractor{ocrTimeout: 5 * time.Minute}
	if p.timeout() != 5*time.Minute {
		t.Errorf("expected custom timeout, got %v", p.timeout())
	}
}

func TestPDFExtractor_PdfInspector(t *testing.T) {
	custom := func(ctx context.Context, path string) (pdfInspection, error) {
		return pdfInspection{}, nil
	}
	p := &PDFExtractor{inspectPDF: custom}
	if p.pdfInspector() == nil {
		t.Error("expected non-nil custom inspector")
	}
	p = &PDFExtractor{}
	if p.pdfInspector() == nil {
		t.Error("expected non-nil default inspector")
	}
}

func TestPDFExtractor_OcrRunner(t *testing.T) {
	p := &PDFExtractor{}
	if p.ocrRunner() == nil {
		t.Error("expected non-nil default runner")
	}
	p = &PDFExtractor{runOCR: func(ctx context.Context, binaryPath, path, languages string, timeout time.Duration) (string, error) {
		return "", nil
	}}
	if p.ocrRunner() == nil {
		t.Error("expected non-nil custom runner")
	}
}

func TestPDFExtractor_Languages(t *testing.T) {
	p := &PDFExtractor{}
	if p.languages() != "eng" {
		t.Errorf("expected default 'eng', got %q", p.languages())
	}
	p = &PDFExtractor{ocrLanguages: "fra+deu"}
	if p.languages() != "fra+deu" {
		t.Errorf("expected 'fra+deu', got %q", p.languages())
	}
	p = &PDFExtractor{ocrLanguages: "  "}
	if p.languages() != "eng" {
		t.Errorf("expected default 'eng' for whitespace, got %q", p.languages())
	}
}

func TestODFExtractor_Supports(t *testing.T) {
	o := &ODFExtractor{}
	for _, ext := range []string{".odt", ".ods", ".odp", ".ODT", ".ODS", ".ODP"} {
		if !o.Supports("file" + ext) {
			t.Errorf("expected support for %s", ext)
		}
	}
	if o.Supports("file.docx") {
		t.Error("expected no support for .docx")
	}
}

func TestPDFExtractor_Supports(t *testing.T) {
	p := &PDFExtractor{}
	if !p.Supports("file.pdf") {
		t.Error("expected support for .pdf")
	}
	if !p.Supports("FILE.PDF") {
		t.Error("expected case-insensitive support for .PDF")
	}
	if p.Supports("file.txt") {
		t.Error("expected no support for .txt")
	}
}

func TestNotebookCell_SourceAndOutputs(t *testing.T) {
	cell := notebookCell{
		CellType: "code",
		Source:   json.RawMessage(`"print('hello')"`),
		Outputs: []notebookOutput{
			{Text: json.RawMessage(`"hello\n"`)},
		},
	}
	got, err := renderNotebookCell(context.Background(), cell)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !strings.Contains(got, "print('hello')") {
		t.Errorf("expected source content, got %q", got)
	}
	if !strings.Contains(got, "[Output]") {
		t.Errorf("expected output label, got %q", got)
	}
	if !strings.Contains(got, "hello") {
		t.Errorf("expected output content, got %q", got)
	}
}

func TestNotebookCell_OutputOnly(t *testing.T) {
	cell := notebookCell{
		CellType: "code",
		Source:   json.RawMessage(`""`),
		Outputs: []notebookOutput{
			{Text: json.RawMessage(`"result"`)},
		},
	}
	got, err := renderNotebookCell(context.Background(), cell)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !strings.Contains(got, "[Output]") {
		t.Errorf("expected output label, got %q", got)
	}
}

func TestNotebookCell_RawCell(t *testing.T) {
	cell := notebookCell{
		CellType: "raw",
		Source:   json.RawMessage(`"raw content"`),
	}
	got, err := renderNotebookCell(context.Background(), cell)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !strings.Contains(got, "raw content") {
		t.Errorf("expected raw content, got %q", got)
	}
}

func TestNotebookCell_OutputDataDedup(t *testing.T) {
	cell := notebookCell{
		CellType: "code",
		Source:   json.RawMessage(`""`),
		Outputs: []notebookOutput{
			{
				Text: json.RawMessage(`"same output"`),
				Data: map[string]json.RawMessage{
					"text/plain":    json.RawMessage(`"same output"`),
					"text/markdown": json.RawMessage(`"formatted output"`),
				},
			},
		},
	}
	got, err := renderNotebookCell(context.Background(), cell)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	occurrences := strings.Count(got, "same output")
	if occurrences != 1 {
		t.Errorf("expected dedup (1 occurrence of 'same output'), got %d occurrences in %q", occurrences, got)
	}
	if !strings.Contains(got, "formatted output") {
		t.Errorf("expected markdown output, got %q", got)
	}
}

func TestOOXMLExtractor_ExtractContextCanceled(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	ext := &OOXMLExtractor{}
	_, err := ext.Extract(ctx, "/some/path.docx")
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("expected context.Canceled, got %v", err)
	}
}

func TestRTFExtractor_ExtractContextCanceled(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	ext := &RTFExtractor{}
	_, err := ext.Extract(ctx, "/some/path.rtf")
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("expected context.Canceled, got %v", err)
	}
}

func TestHTMLExtractor_ExtractContextCanceled(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	ext := &HTMLExtractor{}
	_, err := ext.Extract(ctx, "/some/path.html")
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("expected context.Canceled, got %v", err)
	}
}

func TestLooksBinaryTextSample(t *testing.T) {
	tests := []struct {
		name   string
		data   []byte
		binary bool
	}{
		{"empty", []byte{}, false},
		{"text", []byte("hello world"), false},
		{"null byte", []byte("hello\x00world"), true},
		{"many control bytes", []byte{0x01, 0x02, 0x03, 0x04, 0x05, 0x06, 0x07, 0x08, 0x09, 0x0a, 0x0b, 0x7f}, true},
		{"normal text", []byte("The quick brown fox\njumps over the lazy dog."), false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := looksBinaryTextSample(tt.data)
			if got != tt.binary {
				t.Errorf("looksBinaryTextSample(%q) = %v, want %v", tt.data, got, tt.binary)
			}
		})
	}
}

func TestPdfContentValueKindMethods(t *testing.T) {
	v := pdfContentValue{kind: pdfContentNullKind}
	if v.Kind() != pdfContentNullKind {
		t.Errorf("expected null kind, got %v", v.Kind())
	}
	v = pdfContentValue{kind: pdfContentBoolKind, b: true}
	if v.Kind() != pdfContentBoolKind {
		t.Errorf("expected bool kind, got %v", v.Kind())
	}
	v = pdfContentValue{kind: pdfContentIntKind, i: 42}
	if v.Kind() != pdfContentIntKind {
		t.Errorf("expected int kind, got %v", v.Kind())
	}
	v = pdfContentValue{kind: pdfContentRealKind, f: 3.14}
	if v.Kind() != pdfContentRealKind {
		t.Errorf("expected real kind, got %v", v.Kind())
	}
	v = pdfContentValue{kind: pdfContentStringKind, s: "hello"}
	if v.Kind() != pdfContentStringKind {
		t.Errorf("expected string kind, got %v", v.Kind())
	}
	v = pdfContentValue{kind: pdfContentNameKind, s: "Type"}
	if v.Kind() != pdfContentNameKind {
		t.Errorf("expected name kind, got %v", v.Kind())
	}
}

func TestPdfContentKeywordType(t *testing.T) {
	kw := pdfContentKeyword("BT")
	if string(kw) != "BT" {
		t.Errorf("expected 'BT', got %q", string(kw))
	}
}

func TestPdfContentNameType(t *testing.T) {
	n := pdfContentName("Type")
	if string(n) != "Type" {
		t.Errorf("expected 'Type', got %q", string(n))
	}
}

func TestPDFExtractor_OcrBinaryWithCustomLookup(t *testing.T) {
	ext := &PDFExtractor{
		findOCRBinary: func() (string, bool) {
			return "/custom/ocrmypdf", true
		},
	}
	path, ok := ext.ocrBinary()
	if !ok || path != "/custom/ocrmypdf" {
		t.Errorf("expected /custom/ocrmypdf true, got %q %v", path, ok)
	}

	ext2 := &PDFExtractor{
		findOCRBinary: func() (string, bool) {
			return "", false
		},
	}
	path, ok = ext2.ocrBinary()
	if ok || path != "" {
		t.Errorf("expected empty false, got %q %v", path, ok)
	}
}
