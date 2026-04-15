package extract

import (
	"context"
	"encoding/xml"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestAtoiDefault(t *testing.T) {
	tests := []struct {
		name     string
		value    string
		fallback int
		want     int
	}{
		{"valid number", "42", 1, 42},
		{"empty string uses fallback", "", 10, 10},
		{"non-numeric uses fallback", "abc", 5, 5},
		{"zero uses fallback", "0", 3, 3},
		{"negative uses fallback", "-5", 7, 7},
		{"large number", "1000", 1, 1000},
		{"float string uses fallback", "3.14", 2, 2},
		{"whitespace number uses fallback", " 42 ", 1, 1},
		{"one", "1", 10, 1},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := atoiDefault(tt.value, tt.fallback)
			if got != tt.want {
				t.Errorf("atoiDefault(%q, %d) = %d, want %d", tt.value, tt.fallback, got, tt.want)
			}
		})
	}
}

func TestExtractODFText_DocumentMode(t *testing.T) {
	data := []byte(`<office:document-content xmlns:office="office" xmlns:text="text">
		<office:body>
			<office:text>
				<text:p>Hello world.</text:p>
				<text:h text:outline-level="2">A heading</text:h>
			</office:text>
		</office:body>
	</office:document-content>`)

	text, err := extractODFText(context.Background(), data, odfDocumentMode)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !strings.Contains(text, "Hello world.") {
		t.Errorf("expected text to contain 'Hello world.', got %q", text)
	}
	if !strings.Contains(text, "## A heading") {
		t.Errorf("expected heading with level 2, got %q", text)
	}
}

func TestExtractODFText_SpreadsheetMode(t *testing.T) {
	data := []byte(`<office:document-content xmlns:office="office" xmlns:table="table" xmlns:text="text">
		<office:body>
			<office:spreadsheet>
				<table:table table:name="Data">
					<table:table-row>
						<table:table-cell office:value="42"><text:p>Total</text:p></table:table-cell>
					</table:table-row>
				</table:table>
			</office:spreadsheet>
		</office:body>
	</office:document-content>`)

	text, err := extractODFText(context.Background(), data, odfSpreadsheetMode)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !strings.Contains(text, "42 Total") {
		t.Errorf("expected spreadsheet cell content, got %q", text)
	}
}

func TestExtractODFText_PresentationMode(t *testing.T) {
	data := []byte(`<office:document-content xmlns:office="office" xmlns:draw="draw" xmlns:text="text">
		<office:body>
			<office:presentation>
				<draw:page>
					<text:p>Slide content.</text:p>
				</draw:page>
			</office:presentation>
		</office:body>
	</office:document-content>`)

	text, err := extractODFText(context.Background(), data, odfPresentationMode)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !strings.Contains(text, "Slide content.") {
		t.Errorf("expected presentation text, got %q", text)
	}
}

func TestExtractODFText_TabElement(t *testing.T) {
	data := []byte(`<office:document-content xmlns:office="office" xmlns:text="text">
		<office:body>
			<office:text>
				<text:p>Col1<text:tab/>Col2</text:p>
			</office:text>
		</office:body>
	</office:document-content>`)

	text, err := extractODFText(context.Background(), data, odfDocumentMode)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !strings.Contains(text, "Col1\tCol2") {
		t.Errorf("expected tab between columns, got %q", text)
	}
}

func TestExtractODFText_LineBreakElement(t *testing.T) {
	data := []byte(`<office:document-content xmlns:office="office" xmlns:text="text">
		<office:body>
			<office:text>
				<text:p>Line1<text:line-break/>Line2</text:p>
			</office:text>
		</office:body>
	</office:document-content>`)

	text, err := extractODFText(context.Background(), data, odfDocumentMode)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !strings.Contains(text, "Line1\nLine2") {
		t.Errorf("expected line break between lines, got %q", text)
	}
}

func TestExtractODFText_SpacesElement(t *testing.T) {
	data := []byte(`<office:document-content xmlns:office="office" xmlns:text="text">
		<office:body>
			<office:text>
				<text:p>Hello<text:s/>world</text:p>
			</office:text>
		</office:body>
	</office:document-content>`)

	text, err := extractODFText(context.Background(), data, odfDocumentMode)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !strings.Contains(text, "Hello world") {
		t.Errorf("expected 'Hello world' with space, got %q", text)
	}
}

func TestExtractODFText_SpacesWithCount(t *testing.T) {
	data := []byte(`<office:document-content xmlns:office="office" xmlns:text="text">
		<office:body>
			<office:text>
				<text:p>A<text:s text:c="3"/>B</text:p>
			</office:text>
		</office:body>
	</office:document-content>`)

	text, err := extractODFText(context.Background(), data, odfDocumentMode)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !strings.Contains(text, "A B") {
		t.Errorf("expected spaces between A and B, got %q", text)
	}
}

func TestExtractODFText_LineBreakDirect(t *testing.T) {
	data := []byte(`<office:document-content xmlns:office="office" xmlns:text="text">
		<office:body>
			<office:text>
				<text:line-break/>
			</office:text>
		</office:body>
	</office:document-content>`)

	text, err := extractODFText(context.Background(), data, odfDocumentMode)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if strings.TrimSpace(text) != "" {
		t.Errorf("expected trimmed empty for standalone line-break, got %q", text)
	}
}

func TestExtractODFText_HeadingDefaultLevel(t *testing.T) {
	data := []byte(`<office:document-content xmlns:office="office" xmlns:text="text">
		<office:body>
			<office:text>
				<text:h>Default heading</text:h>
			</office:text>
		</office:body>
	</office:document-content>`)

	text, err := extractODFText(context.Background(), data, odfDocumentMode)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !strings.Contains(text, "# Default heading") {
		t.Errorf("expected level-1 heading, got %q", text)
	}
}

func TestExtractODFText_SpreadsheetTableCell(t *testing.T) {
	data := []byte(`<office:document-content xmlns:office="office" xmlns:table="table" xmlns:text="text">
		<office:body>
			<office:spreadsheet>
				<table:table table:name="Sheet1">
					<table:table-row>
						<table:table-cell office:value="42"><text:p>Total</text:p></table:table-cell>
						<table:table-cell office:value="100"><text:p>Sum</text:p></table:table-cell>
					</table:table-row>
				</table:table>
			</office:spreadsheet>
		</office:body>
	</office:document-content>`)

	text, err := extractODFText(context.Background(), data, odfSpreadsheetMode)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !strings.Contains(text, "42 Total") {
		t.Errorf("expected cell with value and text, got %q", text)
	}
	if !strings.Contains(text, "100 Sum") {
		t.Errorf("expected cell with value and text, got %q", text)
	}
}

func TestExtractODFText_SpreadsheetRepeatedCells(t *testing.T) {
	data := []byte(`<office:document-content xmlns:office="office" xmlns:table="table" xmlns:text="text">
		<office:body>
			<office:spreadsheet>
				<table:table table:name="Data">
					<table:table-row>
						<table:table-cell office:value="5" table:number-columns-repeated="3"><text:p>X</text:p></table:table-cell>
					</table:table-row>
				</table:table>
			</office:spreadsheet>
		</office:body>
	</office:document-content>`)

	text, err := extractODFText(context.Background(), data, odfSpreadsheetMode)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !strings.Contains(text, "5 X") {
		t.Errorf("expected repeated cell content, got %q", text)
	}
}

func TestExtractODFText_SpreadsheetEmptyCells(t *testing.T) {
	data := []byte(`<office:document-content xmlns:office="office" xmlns:table="table" xmlns:text="text">
		<office:body>
			<office:spreadsheet>
				<table:table table:name="Data">
					<table:table-row>
						<table:table-cell table:number-columns-repeated="2"/>
						<table:table-cell office:value="1"><text:p>A</text:p></table:table-cell>
					</table:table-row>
				</table:table>
			</office:spreadsheet>
		</office:body>
	</office:document-content>`)

	text, err := extractODFText(context.Background(), data, odfSpreadsheetMode)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !strings.Contains(text, "1 A") {
		t.Errorf("expected non-empty cell content, got %q", text)
	}
}

func TestExtractODFText_PresentationPageBreak(t *testing.T) {
	data := []byte(`<office:document-content xmlns:office="office" xmlns:draw="draw" xmlns:text="text">
		<office:body>
			<office:presentation>
				<draw:page>
					<text:p>Slide one.</text:p>
				</draw:page>
				<draw:page>
					<text:p>Slide two.</text:p>
				</draw:page>
			</office:presentation>
		</office:body>
	</office:document-content>`)

	text, err := extractODFText(context.Background(), data, odfPresentationMode)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !strings.Contains(text, "Slide one.") || !strings.Contains(text, "Slide two.") {
		t.Errorf("expected both slides, got %q", text)
	}
}

func TestExtractODFText_CellWithFormula(t *testing.T) {
	data := []byte(`<office:document-content xmlns:office="office" xmlns:table="table" xmlns:text="text">
		<office:body>
			<office:spreadsheet>
				<table:table table:name="Data">
					<table:table-row>
						<table:table-cell office:value="42" office:formula="=SUM(A1:B1)"><text:p>Total</text:p></table:table-cell>
					</table:table-row>
				</table:table>
			</office:spreadsheet>
		</office:body>
	</office:document-content>`)

	text, err := extractODFText(context.Background(), data, odfSpreadsheetMode)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !strings.Contains(text, "42") {
		t.Errorf("expected cell value 42, got %q", text)
	}
}

func TestOdfRepeatedCount(t *testing.T) {
	t.Run("with repeated count", func(t *testing.T) {
		se := xml.StartElement{Name: xml.Name{Local: "table-cell"}, Attr: []xml.Attr{
			{Name: xml.Name{Local: "number-columns-repeated"}, Value: "3"},
		}}
		got := odfRepeatedCount(se)
		if got != 3 {
			t.Errorf("expected 3, got %d", got)
		}
	})

	t.Run("default count", func(t *testing.T) {
		se := xml.StartElement{Name: xml.Name{Local: "table-cell"}}
		got := odfRepeatedCount(se)
		if got != 1 {
			t.Errorf("expected default 1, got %d", got)
		}
	})

	t.Run("zero uses default", func(t *testing.T) {
		se := xml.StartElement{Name: xml.Name{Local: "table-cell"}, Attr: []xml.Attr{
			{Name: xml.Name{Local: "number-columns-repeated"}, Value: "0"},
		}}
		got := odfRepeatedCount(se)
		if got != 1 {
			t.Errorf("expected 1 for zero value, got %d", got)
		}
	})

	t.Run("clamped to max", func(t *testing.T) {
		se := xml.StartElement{Name: xml.Name{Local: "table-cell"}, Attr: []xml.Attr{
			{Name: xml.Name{Local: "number-columns-repeated"}, Value: "9999"},
		}}
		got := odfRepeatedCount(se)
		if got != 1000 {
			t.Errorf("expected 1000 for huge value, got %d", got)
		}
	})

	t.Run("invalid uses default", func(t *testing.T) {
		se := xml.StartElement{Name: xml.Name{Local: "table-cell"}, Attr: []xml.Attr{
			{Name: xml.Name{Local: "number-columns-repeated"}, Value: "abc"},
		}}
		got := odfRepeatedCount(se)
		if got != 1 {
			t.Errorf("expected 1 for invalid value, got %d", got)
		}
	})
}

func TestAttrValue(t *testing.T) {
	se := xml.StartElement{
		Name: xml.Name{Local: "tag"},
		Attr: []xml.Attr{
			{Name: xml.Name{Local: "id"}, Value: "123"},
			{Name: xml.Name{Local: "name"}, Value: "test"},
		},
	}
	if attrValue(se, "id") != "123" {
		t.Errorf("expected id=123, got %q", attrValue(se, "id"))
	}
	if attrValue(se, "name") != "test" {
		t.Errorf("expected name=test, got %q", attrValue(se, "name"))
	}
	if attrValue(se, "missing") != "" {
		t.Errorf("expected empty for missing attr, got %q", attrValue(se, "missing"))
	}
}

func TestODFExtractor_ExtractUnsupportedFormat(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "test.odg")
	if err := os.WriteFile(path, []byte{}, 0644); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	ext := &ODFExtractor{}
	_, err := ext.Extract(context.Background(), path)
	if err == nil {
		t.Fatal("expected error for unsupported format, got nil")
	}
}

func TestODFExtractor_ExtractContextCanceled(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	ext := &ODFExtractor{}
	_, err := ext.Extract(ctx, "/some/path.odt")
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("expected context.Canceled, got %v", err)
	}
}

func TestExtractODFText_ContextCanceled(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	data := []byte(`<office:document-content xmlns:office="office"><office:body><office:text><text:p>Hello</text:p></office:text></office:body></office:document-content>`)
	_, err := extractODFText(ctx, data, odfDocumentMode)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("expected context.Canceled, got %v", err)
	}
}

func TestODFExtractor_ExtractODSWithEmptySheet(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "empty.ods")
	writeZipArchive(t, path, map[string]string{
		"content.xml": `
			<office:document-content xmlns:office="office" xmlns:table="table" xmlns:text="text">
				<office:body>
					<office:spreadsheet>
						<table:table table:name="Empty">
							<table:table-row>
								<table:table-cell/>
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
		t.Fatalf("unexpected error: %v", err)
	}
	if strings.Contains(text, "Empty") {
		t.Logf("result: %q", text)
	}
}

func TestODFExtractor_ExtractODTWithEmptyDocument(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "empty.odt")
	writeZipArchive(t, path, map[string]string{
		"content.xml": `
			<office:document-content xmlns:office="office" xmlns:text="text">
				<office:body>
					<office:text>
					</office:text>
				</office:body>
			</office:document-content>
		`,
	})

	ext := &ODFExtractor{}
	text, err := ext.Extract(context.Background(), path)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if text != "" {
		t.Errorf("expected empty text for empty document, got %q", text)
	}
}

func TestODFExtractor_ExtractUnsupportedExtension(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "test.odg")
	if err := os.WriteFile(path, []byte{}, 0644); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	ext := &ODFExtractor{}
	_, err := ext.Extract(context.Background(), path)
	if err == nil {
		t.Fatal("expected error for unsupported format, got nil")
	}
}
