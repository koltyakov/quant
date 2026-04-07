package extract

import (
	"archive/zip"
	"bytes"
	"context"
	"encoding/xml"
	"fmt"
	"path/filepath"
	"strconv"
	"strings"
)

type ODFExtractor struct{}

func (o *ODFExtractor) Extract(_ context.Context, path string) (string, error) {
	switch strings.ToLower(ext(path)) {
	case ".odt":
		return extractODT(path)
	case ".ods":
		return extractODS(path)
	case ".odp":
		return extractODP(path)
	default:
		return "", fmt.Errorf("unsupported OpenDocument format: %s", filepath.Ext(path))
	}
}

func (o *ODFExtractor) Supports(path string) bool {
	switch strings.ToLower(ext(path)) {
	case ".odt", ".ods", ".odp":
		return true
	}
	return false
}

func extractODT(path string) (string, error) {
	data, err := readODFContent(path)
	if err != nil {
		return "", err
	}

	text := strings.TrimSpace(extractODFText(data, odfDocumentMode))
	if text == "" {
		return "", nil
	}
	return "[Document]\n" + text, nil
}

func extractODS(path string) (string, error) {
	data, err := readODFContent(path)
	if err != nil {
		return "", err
	}
	return strings.Join(extractODFSheets(data), "\n\n"), nil
}

func extractODP(path string) (string, error) {
	data, err := readODFContent(path)
	if err != nil {
		return "", err
	}
	return strings.Join(extractODFSlides(data), "\n\n"), nil
}

func readODFContent(path string) ([]byte, error) {
	zr, err := zip.OpenReader(path)
	if err != nil {
		return nil, err
	}
	defer func() { _ = zr.Close() }()

	return readZipFile(zr.File, "content.xml")
}

type odfMode int

const (
	odfDocumentMode odfMode = iota
	odfSpreadsheetMode
	odfPresentationMode
)

func extractODFText(data []byte, mode odfMode) string {
	decoder := xml.NewDecoder(bytes.NewReader(data))
	var buf strings.Builder

	for {
		token, err := decoder.Token()
		if err != nil {
			break
		}

		switch se := token.(type) {
		case xml.StartElement:
			switch se.Name.Local {
			case "p", "h":
				writeODFParagraph(decoder, &buf, se)
			case "tab":
				buf.WriteByte('\t')
			case "line-break":
				buf.WriteByte('\n')
			case "s":
				count := 1
				for _, attr := range se.Attr {
					if attr.Name.Local == "c" {
						count = max(1, atoiDefault(attr.Value, 1))
						break
					}
				}
				buf.WriteString(strings.Repeat(" ", count))
			case "table-cell":
				if mode == odfSpreadsheetMode {
					repeat := odfRepeatedCount(se)
					value := strings.TrimSpace(extractODFTableCell(decoder, se))
					for i := 0; i < repeat; i++ {
						if value == "" {
							continue
						}
						if buf.Len() > 0 && !strings.HasSuffix(buf.String(), "\n") {
							buf.WriteString(" | ")
						}
						buf.WriteString(value)
					}
				}
			}
		case xml.EndElement:
			switch se.Name.Local {
			case "table-row":
				if mode == odfSpreadsheetMode {
					writeParagraphBreak(&buf)
				}
			case "page":
				if mode == odfPresentationMode {
					writeParagraphBreak(&buf)
					writeParagraphBreak(&buf)
				}
			}
		}
	}

	return strings.TrimSpace(cleanSpacing(buf.String()))
}

func extractODFSheets(data []byte) []string {
	decoder := xml.NewDecoder(bytes.NewReader(data))
	var sections []string

	for {
		token, err := decoder.Token()
		if err != nil {
			break
		}
		se, ok := token.(xml.StartElement)
		if !ok || se.Name.Local != "table" {
			continue
		}

		name := attrValue(se, "name")
		if name == "" {
			name = "Sheet"
		}

		content := strings.TrimSpace(extractODFElementText(decoder, se, odfSpreadsheetMode))
		if content == "" {
			continue
		}
		sections = append(sections, fmt.Sprintf("[Sheet %s]\n%s", name, content))
	}

	return sections
}

func extractODFSlides(data []byte) []string {
	decoder := xml.NewDecoder(bytes.NewReader(data))
	var slides []string
	index := 0

	for {
		token, err := decoder.Token()
		if err != nil {
			break
		}
		se, ok := token.(xml.StartElement)
		if !ok || se.Name.Local != "page" {
			continue
		}

		index++
		title := attrValue(se, "name")
		content := strings.TrimSpace(extractODFElementText(decoder, se, odfPresentationMode))
		if content == "" {
			continue
		}

		label := fmt.Sprintf("[Slide %d]", index)
		if title != "" {
			label = fmt.Sprintf("[Slide %d: %s]", index, title)
		}
		slides = append(slides, label+"\n"+content)
	}

	return slides
}

func extractODFElementText(decoder *xml.Decoder, root xml.StartElement, mode odfMode) string {
	var buf strings.Builder
	depth := 1

	for depth > 0 {
		token, err := decoder.Token()
		if err != nil {
			break
		}

		switch se := token.(type) {
		case xml.StartElement:
			depth++
			switch se.Name.Local {
			case "p", "h":
				writeODFParagraph(decoder, &buf, se)
				depth--
			case "tab":
				buf.WriteByte('\t')
			case "line-break":
				buf.WriteByte('\n')
			case "s":
				buf.WriteByte(' ')
			case "table-cell":
				if mode == odfSpreadsheetMode {
					repeat := odfRepeatedCount(se)
					value := strings.TrimSpace(extractODFTableCell(decoder, se))
					depth--
					for i := 0; i < repeat; i++ {
						if value == "" {
							continue
						}
						if buf.Len() > 0 && !strings.HasSuffix(buf.String(), "\n") {
							buf.WriteString(" | ")
						}
						buf.WriteString(value)
					}
				}
			}
		case xml.CharData:
			text := string(se)
			if strings.TrimSpace(text) != "" {
				buf.WriteString(text)
			}
		case xml.EndElement:
			depth--
			switch se.Name.Local {
			case "table-row":
				if mode == odfSpreadsheetMode {
					writeParagraphBreak(&buf)
				}
			case root.Name.Local:
				return strings.TrimSpace(cleanSpacing(buf.String()))
			}
		}
	}

	return strings.TrimSpace(cleanSpacing(buf.String()))
}

func writeODFParagraph(decoder *xml.Decoder, buf *strings.Builder, start xml.StartElement) {
	content := strings.TrimSpace(extractODFElementText(decoder, start, odfDocumentMode))
	if content == "" {
		return
	}
	if buf.Len() > 0 {
		writeParagraphBreak(buf)
	}
	buf.WriteString(content)
}

func extractODFTableCell(decoder *xml.Decoder, root xml.StartElement) string {
	depth := 1
	var buf strings.Builder

	if value := attrValue(root, "value"); value != "" {
		buf.WriteString(value)
	}
	if value := attrValue(root, "formula"); value != "" {
		if buf.Len() > 0 {
			buf.WriteString(" = ")
		}
		buf.WriteString(value)
	}

	for depth > 0 {
		token, err := decoder.Token()
		if err != nil {
			break
		}

		switch se := token.(type) {
		case xml.StartElement:
			depth++
			switch se.Name.Local {
			case "p", "h":
				content := strings.TrimSpace(extractODFElementText(decoder, se, odfDocumentMode))
				depth--
				if content != "" {
					if buf.Len() > 0 {
						buf.WriteString(" ")
					}
					buf.WriteString(content)
				}
			}
		case xml.CharData:
			if depth == 1 {
				text := string(se)
				if strings.TrimSpace(text) != "" {
					buf.WriteString(text)
				}
			}
		case xml.EndElement:
			depth--
			if se.Name.Local == root.Name.Local {
				return strings.TrimSpace(cleanSpacing(buf.String()))
			}
		}
	}

	return strings.TrimSpace(cleanSpacing(buf.String()))
}

func odfRepeatedCount(se xml.StartElement) int {
	return max(1, atoiDefault(attrValue(se, "number-columns-repeated"), 1))
}

func attrValue(se xml.StartElement, name string) string {
	for _, attr := range se.Attr {
		if attr.Name.Local == name {
			return attr.Value
		}
	}
	return ""
}

func atoiDefault(value string, fallback int) int {
	if value == "" {
		return fallback
	}
	parsed, err := strconv.Atoi(value)
	if err != nil || parsed <= 0 {
		return fallback
	}
	return parsed
}

func max(a, b int) int {
	if a > b {
		return a
	}
	return b
}
