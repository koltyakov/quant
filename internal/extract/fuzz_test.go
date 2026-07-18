package extract

import (
	"context"
	"strings"
	"testing"
)

// The fuzz targets below exercise the hand-written parsers that consume
// untrusted file content. They act as crash and hang detectors: any panic,
// index-out-of-range, or non-termination on arbitrary input is a bug.
// Returned errors are expected and ignored.

func FuzzExtractRTFText(f *testing.F) {
	f.Add(`{\rtf1\ansi Hello \b World\b0\par}`)
	f.Add(`{\rtf1{\fonttbl{\f0 Arial;}}Body \'e9 text}`)
	f.Add(`{\rtf1 ၕ?\u-10179?\u-8704? \lquote x\rquote\par}`)
	f.Add(`{\rtf1 \tab\line\endash\emdash\bullet}`)
	f.Add(`\{\`)
	f.Add(`{\rtf1 \'`)
	f.Add(`{\rtf1{\pict deadbeef}visible}`)

	f.Fuzz(func(t *testing.T, rtf string) {
		_, _ = extractRTFText(context.Background(), rtf)
	})
}

func FuzzExtractHTMLText(f *testing.F) {
	f.Add(`<html><body><p>Hello <b>world</b></p><script>skip()</script></body></html>`)
	f.Add(`<div><style>p{color:red}</style><p>text`)
	f.Add(`<p>a<br>b</p><!-- comment --><table><tr><td>cell</td></tr></table>`)

	f.Fuzz(func(t *testing.T, data string) {
		_, _ = extractHTMLText(context.Background(), strings.NewReader(data))
	})
}

func FuzzExtractODF(f *testing.F) {
	f.Add([]byte(`<office:document-content xmlns:office="o" xmlns:text="t"><office:body><office:text><text:p>Hello</text:p><text:h>Title</text:h></office:text></office:body></office:document-content>`))
	f.Add([]byte(`<office:document-content xmlns:table="tb"><table:table table:name="Sheet1"><table:table-row table:number-rows-repeated="3"><table:table-cell table:number-columns-repeated="2"><text:p>v</text:p></table:table-cell></table:table-row></table:table></office:document-content>`))
	f.Add([]byte(`<office:presentation><draw:page draw:name="Slide 1"><text:p>Bullet</text:p></draw:page></office:presentation>`))
	f.Add([]byte(`<table:table-cell table:number-columns-repeated="999999999">`))

	f.Fuzz(func(t *testing.T, data []byte) {
		ctx := context.Background()
		for _, mode := range []odfMode{odfDocumentMode, odfSpreadsheetMode, odfPresentationMode} {
			_, _ = extractODFText(ctx, data, mode)
		}
		_, _ = extractODFSheets(ctx, data)
		_, _ = extractODFSlides(ctx, data)
	})
}

func FuzzExtractOOXML(f *testing.F) {
	f.Add([]byte(`<w:document xmlns:w="w"><w:body><w:p><w:r><w:t>Hello</w:t></w:r></w:p><w:tbl><w:tr><w:tc><w:p><w:r><w:t>cell</w:t></w:r></w:p></w:tc></w:tr></w:tbl></w:body></w:document>`))
	f.Add([]byte(`<p:sld xmlns:a="a"><p:txBody><a:p><a:r><a:t>Slide text</a:t></a:r></a:p></p:txBody></p:sld>`))
	f.Add([]byte(`<sst><si><t>shared</t></si><si><r><t>rich</t></r></si></sst>`))
	f.Add([]byte(`<worksheet><sheetData><row r="1"><c r="A1" t="s"><v>0</v></c><c r="B1"><v>42</v></c></row></sheetData></worksheet>`))
	f.Add([]byte(`<Relationships><Relationship Id="rId1" Type="slide" Target="slides/slide1.xml"/></Relationships>`))

	f.Fuzz(func(t *testing.T, data []byte) {
		ctx := context.Background()
		_, _ = extractWordMLText(ctx, data)
		_, _ = extractDrawingMLText(ctx, data)
		_, _ = extractNotesText(ctx, data)
		shared, _ := parseSharedStrings(ctx, data)
		_, _ = parseSheetCells(ctx, data, shared)
		_, _ = parseRelationshipEntries(ctx, data, "ppt/slides")
	})
}

func FuzzInterpretPDFContent(f *testing.F) {
	f.Add([]byte(`BT /F1 12 Tf (Hello) Tj ET`))
	f.Add([]byte(`BT [(a) -20 (b\)c) 5 <48656C6C6F>] TJ T* (line) ' 1 2 (x) " ET`))
	f.Add([]byte(`<< /Type /Page /Kids [1 0 R [2 (nested)]] >> % comment`))
	f.Add([]byte(`(unclosed \( paren`))
	f.Add([]byte(`<< /Deep << /Deeper << /Deepest [[[[]]]] >> >> >>`))
	f.Add([]byte("(octal \\101\\10\\1) Tj"))

	f.Fuzz(func(t *testing.T, data []byte) {
		_ = pdfContentStreamEndsMidToken(data)

		var out strings.Builder
		_ = interpretPDFContent(data, func(stk *pdfContentStack, op string) error {
			for stk.Len() > 0 {
				v := stk.Pop()
				if v.Kind() == pdfContentStringKind {
					out.WriteString(v.RawString())
				}
			}
			return nil
		})
	})
}
