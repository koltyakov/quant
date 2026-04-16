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

func (o *ODFExtractor) Extract(ctx context.Context, path string) (string, error) {
	if err := checkContext(ctx); err != nil {
		return "", err
	}
	if err := ensureFileSize(path, maxExtractorFileSize); err != nil {
		return "", err
	}
	if err := ensureNotOLE2(path); err != nil {
		return "", err
	}

	switch strings.ToLower(ext(path)) {
	case ".odt":
		return extractODT(ctx, path)
	case ".ods":
		return extractODS(ctx, path)
	case ".odp":
		return extractODP(ctx, path)
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

func extractODT(ctx context.Context, path string) (string, error) {
	data, err := readODFContent(ctx, path)
	if err != nil {
		return "", err
	}

	text, err := extractODFText(ctx, data, odfDocumentMode)
	if err != nil {
		return "", err
	}
	text = strings.TrimSpace(text)
	if text == "" {
		return "", nil
	}
	return "[Document]\n" + text, nil
}

func extractODS(ctx context.Context, path string) (string, error) {
	data, err := readODFContent(ctx, path)
	if err != nil {
		return "", err
	}
	sheets, err := extractODFSheets(ctx, data)
	if err != nil {
		return "", err
	}
	return strings.Join(sheets, "\n\n"), nil
}

func extractODP(ctx context.Context, path string) (string, error) {
	data, err := readODFContent(ctx, path)
	if err != nil {
		return "", err
	}
	slides, err := extractODFSlides(ctx, data)
	if err != nil {
		return "", err
	}
	return strings.Join(slides, "\n\n"), nil
}

func readODFContent(ctx context.Context, path string) ([]byte, error) {
	zr, err := zip.OpenReader(path)
	if err != nil {
		return nil, err
	}
	defer func() { _ = zr.Close() }()

	return readZipFile(ctx, zr.File, "content.xml")
}

type odfMode int

const (
	odfDocumentMode odfMode = iota
	odfSpreadsheetMode
	odfPresentationMode
)

func extractODFText(ctx context.Context, data []byte, mode odfMode) (string, error) {
	decoder := xml.NewDecoder(bytes.NewReader(data))
	var buf strings.Builder

	for {
		token, err := nextXMLToken(ctx, decoder)
		if err != nil {
			break
		}

		switch se := token.(type) {
		case xml.StartElement:
			switch se.Name.Local {
			case "p", "h":
				if err := writeODFParagraph(ctx, decoder, &buf, se); err != nil {
					return "", err
				}
			case "tab":
				buf.WriteByte('\t')
			case "line-break":
				buf.WriteByte('\n')
			case "s":
				count := 1
				for _, attr := range se.Attr {
					if attr.Name.Local == "c" {
						count = min(1000, max(1, atoiDefault(attr.Value, 1)))
						break
					}
				}
				buf.WriteString(strings.Repeat(" ", count))
			case "table-cell":
				if mode == odfSpreadsheetMode {
					repeat := odfRepeatedCount(se)
					value, err := extractODFTableCell(ctx, decoder, se)
					if err != nil {
						return "", err
					}
					value = strings.TrimSpace(value)
					for range repeat {
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

	if err := checkContext(ctx); err != nil {
		return "", err
	}
	return strings.TrimSpace(cleanSpacing(buf.String())), nil
}

func extractODFSheets(ctx context.Context, data []byte) ([]string, error) {
	decoder := xml.NewDecoder(bytes.NewReader(data))
	var sections []string

	for {
		token, err := nextXMLToken(ctx, decoder)
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

		content, err := extractODFElementText(ctx, decoder, se, odfSpreadsheetMode)
		if err != nil {
			return nil, err
		}
		content = strings.TrimSpace(content)
		if content == "" {
			continue
		}
		sections = append(sections, fmt.Sprintf("[Sheet %s]\n%s", name, content))
	}

	if err := checkContext(ctx); err != nil {
		return nil, err
	}
	return sections, nil
}

func extractODFSlides(ctx context.Context, data []byte) ([]string, error) {
	decoder := xml.NewDecoder(bytes.NewReader(data))
	var slides []string
	index := 0

	for {
		token, err := nextXMLToken(ctx, decoder)
		if err != nil {
			break
		}
		se, ok := token.(xml.StartElement)
		if !ok || se.Name.Local != "page" {
			continue
		}

		index++
		title := attrValue(se, "name")
		content, err := extractODFElementText(ctx, decoder, se, odfPresentationMode)
		if err != nil {
			return nil, err
		}
		content = strings.TrimSpace(content)
		if content == "" {
			continue
		}

		label := fmt.Sprintf("[Slide %d]", index)
		if title != "" {
			label = fmt.Sprintf("[Slide %d: %s]", index, title)
		}
		slides = append(slides, label+"\n"+content)
	}

	if err := checkContext(ctx); err != nil {
		return nil, err
	}
	return slides, nil
}

func extractODFElementText(ctx context.Context, decoder *xml.Decoder, root xml.StartElement, mode odfMode) (string, error) {
	var buf strings.Builder
	depth := 1

	for depth > 0 {
		token, err := nextXMLToken(ctx, decoder)
		if err != nil {
			break
		}

		switch se := token.(type) {
		case xml.StartElement:
			depth++
			switch se.Name.Local {
			case "p", "h":
				if err := writeODFParagraph(ctx, decoder, &buf, se); err != nil {
					return "", err
				}
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
					value, err := extractODFTableCell(ctx, decoder, se)
					if err != nil {
						return "", err
					}
					value = strings.TrimSpace(value)
					depth--
					for range repeat {
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
				return strings.TrimSpace(cleanSpacing(buf.String())), nil
			}
		}
	}

	if err := checkContext(ctx); err != nil {
		return "", err
	}
	return strings.TrimSpace(cleanSpacing(buf.String())), nil
}

func writeODFParagraph(ctx context.Context, decoder *xml.Decoder, buf *strings.Builder, start xml.StartElement) error {
	content, err := extractODFElementText(ctx, decoder, start, odfDocumentMode)
	if err != nil {
		return err
	}
	content = strings.TrimSpace(content)
	if content == "" {
		return nil
	}
	if buf.Len() > 0 {
		writeParagraphBreak(buf)
	}
	if start.Name.Local == "h" {
		level := min(6, max(1, atoiDefault(attrValue(start, "outline-level"), 1)))
		buf.WriteString(strings.Repeat("#", level))
		buf.WriteByte(' ')
	}
	buf.WriteString(content)
	return nil
}

func extractODFTableCell(ctx context.Context, decoder *xml.Decoder, root xml.StartElement) (string, error) {
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
		token, err := nextXMLToken(ctx, decoder)
		if err != nil {
			break
		}

		switch se := token.(type) {
		case xml.StartElement:
			depth++
			switch se.Name.Local {
			case "p", "h":
				content, err := extractODFElementText(ctx, decoder, se, odfDocumentMode)
				if err != nil {
					return "", err
				}
				content = strings.TrimSpace(content)
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
				return strings.TrimSpace(cleanSpacing(buf.String())), nil
			}
		}
	}

	if err := checkContext(ctx); err != nil {
		return "", err
	}
	return strings.TrimSpace(cleanSpacing(buf.String())), nil
}

const maxODFRepeatedColumns = 1000

func odfRepeatedCount(se xml.StartElement) int {
	return min(maxODFRepeatedColumns, max(1, atoiDefault(attrValue(se, "number-columns-repeated"), 1)))
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
