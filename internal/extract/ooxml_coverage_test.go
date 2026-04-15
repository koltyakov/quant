package extract

import (
	"bytes"
	"context"
	"encoding/xml"
	"path/filepath"
	"strings"
	"testing"
)

func TestExtractDOCX(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "test.docx")
	writeZipArchive(t, path, map[string]string{
		"word/document.xml": `<w:document xmlns:w="w"><w:body><w:p><w:r><w:t>Main text.</w:t></w:r></w:p></w:body></w:document>`,
		"word/header1.xml":  `<w:document xmlns:w="w"><w:body><w:p><w:r><w:t>Header text.</w:t></w:r></w:p></w:body></w:document>`,
		"word/footer1.xml":  `<w:document xmlns:w="w"><w:body><w:p><w:r><w:t>Footer text.</w:t></w:r></w:p></w:body></w:document>`,
	})

	text, err := extractDOCX(context.Background(), path)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if !strings.Contains(text, "[Document]") {
		t.Errorf("expected [Document] label, got %q", text)
	}
	if !strings.Contains(text, "[Header 1]") {
		t.Errorf("expected [Header 1] label, got %q", text)
	}
	if !strings.Contains(text, "[Footer 1]") {
		t.Errorf("expected [Footer 1] label, got %q", text)
	}
	if !strings.Contains(text, "Main text.") {
		t.Errorf("expected main text, got %q", text)
	}
	if !strings.Contains(text, "Header text.") {
		t.Errorf("expected header text, got %q", text)
	}
	if !strings.Contains(text, "Footer text.") {
		t.Errorf("expected footer text, got %q", text)
	}
}

func TestExtractDOCX_DocumentOnly(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "doc.docx")
	writeZipArchive(t, path, map[string]string{
		"word/document.xml": `<w:document xmlns:w="w"><w:body><w:p><w:r><w:t>Just the body.</w:t></w:r></w:p></w:body></w:document>`,
	})

	text, err := extractDOCX(context.Background(), path)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !strings.Contains(text, "[Document]") {
		t.Errorf("expected [Document] label, got %q", text)
	}
	if !strings.Contains(text, "Just the body.") {
		t.Errorf("expected body text, got %q", text)
	}
	if strings.Contains(text, "[Header") || strings.Contains(text, "[Footer") {
		t.Errorf("expected no header/footer labels, got %q", text)
	}
}

func TestParseSharedStrings(t *testing.T) {
	data := []byte(`<sst xmlns="http://schemas.openxmlformats.org/spreadsheetml/2006/main">
		<si><t>Policy Number</t></si>
		<si><t>Name</t></si>
		<si><t>Value</t></si>
	</sst>`)

	result, err := parseSharedStrings(context.Background(), data)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(result) != 3 {
		t.Fatalf("expected 3 strings, got %d", len(result))
	}
	if result[0] != "Policy Number" {
		t.Errorf("expected 'Policy Number', got %q", result[0])
	}
	if result[1] != "Name" {
		t.Errorf("expected 'Name', got %q", result[1])
	}
	if result[2] != "Value" {
		t.Errorf("expected 'Value', got %q", result[2])
	}
}

func TestParseSharedStrings_MultiText(t *testing.T) {
	data := []byte(`<sst xmlns="http://schemas.openxmlformats.org/spreadsheetml/2006/main">
		<si><t>Hello</t><t> World</t></si>
	</sst>`)

	result, err := parseSharedStrings(context.Background(), data)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(result) != 1 {
		t.Fatalf("expected 1 string, got %d", len(result))
	}
	if result[0] != "Hello World" {
		t.Errorf("expected 'Hello World', got %q", result[0])
	}
}

func TestParseWorkbookSheets(t *testing.T) {
	workbookData := []byte(`<workbook xmlns="http://schemas.openxmlformats.org/spreadsheetml/2006/main"
		xmlns:r="http://schemas.openxmlformats.org/officeDocument/2006/relationships">
		<sheets>
			<sheet name="Data" r:id="rId1"/>
			<sheet name="Summary" r:id="rId2"/>
		</sheets>
	</workbook>`)

	relsData := []byte(`<Relationships xmlns="http://schemas.openxmlformats.org/package/2006/relationships">
		<Relationship Id="rId1" Type="http://schemas.openxmlformats.org/officeDocument/2006/relationships/worksheet" Target="worksheets/sheet1.xml"/>
		<Relationship Id="rId2" Type="http://schemas.openxmlformats.org/officeDocument/2006/relationships/worksheet" Target="worksheets/sheet2.xml"/>
	</Relationships>`)

	sheets, err := parseWorkbookSheets(context.Background(), workbookData, relsData)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(sheets) != 2 {
		t.Fatalf("expected 2 sheets, got %d", len(sheets))
	}
	if sheets[0].Name != "Data" {
		t.Errorf("expected sheet name 'Data', got %q", sheets[0].Name)
	}
	if sheets[0].Target != "xl/worksheets/sheet1.xml" {
		t.Errorf("expected target 'xl/worksheets/sheet1.xml', got %q", sheets[0].Target)
	}
	if sheets[1].Name != "Summary" {
		t.Errorf("expected sheet name 'Summary', got %q", sheets[1].Name)
	}
	if sheets[1].Target != "xl/worksheets/sheet2.xml" {
		t.Errorf("expected target 'xl/worksheets/sheet2.xml', got %q", sheets[1].Target)
	}
}

func TestParseWorkbookRelationships(t *testing.T) {
	data := []byte(`<Relationships xmlns="http://schemas.openxmlformats.org/package/2006/relationships">
		<Relationship Id="rId1" Type="http://schemas.openxmlformats.org/officeDocument/2006/relationships/worksheet" Target="worksheets/sheet1.xml"/>
		<Relationship Id="rId2" Type="http://schemas.openxmlformats.org/officeDocument/2006/relationships/worksheet" Target="worksheets/sheet2.xml"/>
	</Relationships>`)

	rels, err := parseWorkbookRelationships(context.Background(), data)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(rels) != 2 {
		t.Fatalf("expected 2 relationships, got %d", len(rels))
	}
	if rels["rId1"] != "xl/worksheets/sheet1.xml" {
		t.Errorf("expected rId1 -> 'xl/worksheets/sheet1.xml', got %q", rels["rId1"])
	}
	if rels["rId2"] != "xl/worksheets/sheet2.xml" {
		t.Errorf("expected rId2 -> 'xl/worksheets/sheet2.xml', got %q", rels["rId2"])
	}
}

func TestParseInlineString(t *testing.T) {
	xmlData := []byte(`<is><t>Hello</t><t> World</t></is>`)
	decoder := xml.NewDecoder(bytes.NewReader(xmlData))

	for {
		tok, err := decoder.Token()
		if err != nil {
			t.Fatalf("unexpected error advancing decoder: %v", err)
		}
		if se, ok := tok.(xml.StartElement); ok && se.Name.Local == "is" {
			break
		}
	}

	result, err := parseInlineString(context.Background(), decoder)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result != "Hello World" {
		t.Fatalf("expected 'Hello World', got %q", result)
	}
}

func TestParseInlineString_SingleText(t *testing.T) {
	xmlData := []byte(`<is><t>Solo</t></is>`)
	decoder := xml.NewDecoder(bytes.NewReader(xmlData))

	for {
		tok, err := decoder.Token()
		if err != nil {
			t.Fatalf("unexpected error advancing decoder: %v", err)
		}
		if se, ok := tok.(xml.StartElement); ok && se.Name.Local == "is" {
			break
		}
	}

	result, err := parseInlineString(context.Background(), decoder)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result != "Solo" {
		t.Fatalf("expected 'Solo', got %q", result)
	}
}

func TestSectionOrdinal(t *testing.T) {
	tests := []struct {
		input string
		want  string
	}{
		{"word/header3.xml", "3"},
		{"word/footer1.xml", "1"},
		{"word/footer.xml", "1"},
		{"word/header10.xml", "10"},
	}
	for _, tt := range tests {
		got := sectionOrdinal(tt.input)
		if got != tt.want {
			t.Errorf("sectionOrdinal(%q) = %q, want %q", tt.input, got, tt.want)
		}
	}
}

func TestExtractXLSX(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "test.xlsx")
	writeZipArchive(t, path, map[string]string{
		"xl/sharedStrings.xml": `<sst xmlns="http://schemas.openxmlformats.org/spreadsheetml/2006/main">
			<si><t>Policy Number</t></si>
			<si><t>Name</t></si>
		</sst>`,
		"xl/workbook.xml": `<workbook xmlns="http://schemas.openxmlformats.org/spreadsheetml/2006/main"
			xmlns:r="http://schemas.openxmlformats.org/officeDocument/2006/relationships">
			<sheets>
				<sheet name="Data" r:id="rId1"/>
			</sheets>
		</workbook>`,
		"xl/_rels/workbook.xml.rels": `<Relationships xmlns="http://schemas.openxmlformats.org/package/2006/relationships">
			<Relationship Id="rId1" Type="http://schemas.openxmlformats.org/officeDocument/2006/relationships/worksheet" Target="worksheets/sheet1.xml"/>
		</Relationships>`,
		"xl/worksheets/sheet1.xml": `<worksheet xmlns="http://schemas.openxmlformats.org/spreadsheetml/2006/main">
			<sheetData>
				<row r="1">
					<c r="A1" t="s"><v>0</v></c>
					<c r="B1" t="s"><v>1</v></c>
				</row>
			</sheetData>
		</worksheet>`,
	})

	text, err := extractXLSX(context.Background(), path)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !strings.Contains(text, "[Sheet Data]") {
		t.Errorf("expected [Sheet Data] label, got %q", text)
	}
	if !strings.Contains(text, "A1: Policy Number") {
		t.Errorf("expected 'A1: Policy Number', got %q", text)
	}
	if !strings.Contains(text, "B1: Name") {
		t.Errorf("expected 'B1: Name', got %q", text)
	}
}

func TestExtractXLSX_NoSharedStrings(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "test.xlsx")
	writeZipArchive(t, path, map[string]string{
		"xl/workbook.xml": `<workbook xmlns="http://schemas.openxmlformats.org/spreadsheetml/2006/main"
			xmlns:r="http://schemas.openxmlformats.org/officeDocument/2006/relationships">
			<sheets>
				<sheet name="Numbers" r:id="rId1"/>
			</sheets>
		</workbook>`,
		"xl/_rels/workbook.xml.rels": `<Relationships xmlns="http://schemas.openxmlformats.org/package/2006/relationships">
			<Relationship Id="rId1" Type="http://schemas.openxmlformats.org/officeDocument/2006/relationships/worksheet" Target="worksheets/sheet1.xml"/>
		</Relationships>`,
		"xl/worksheets/sheet1.xml": `<worksheet xmlns="http://schemas.openxmlformats.org/spreadsheetml/2006/main">
			<sheetData>
				<row r="1">
					<c r="A1"><v>42</v></c>
				</row>
			</sheetData>
		</worksheet>`,
	})

	text, err := extractXLSX(context.Background(), path)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !strings.Contains(text, "[Sheet Numbers]") {
		t.Errorf("expected [Sheet Numbers] label, got %q", text)
	}
	if !strings.Contains(text, "42") {
		t.Errorf("expected numeric value 42, got %q", text)
	}
}

func TestIsWordSection(t *testing.T) {
	tests := []struct {
		name   string
		prefix string
		suffix string
		want   bool
	}{
		{"word/header1.xml", "word/header", ".xml", true},
		{"word/footer1.xml", "word/footer", ".xml", true},
		{"word/document.xml", "word/header", ".xml", false},
		{"word/header1.xml", "word/footer", ".xml", false},
		{"word/header", "word/header", ".xml", false},
		{"word/header1.txt", "word/header", ".xml", false},
	}
	for _, tt := range tests {
		got := isWordSection(tt.name, tt.prefix, tt.suffix)
		if got != tt.want {
			t.Errorf("isWordSection(%q, %q, %q) = %v, want %v", tt.name, tt.prefix, tt.suffix, got, tt.want)
		}
	}
}
