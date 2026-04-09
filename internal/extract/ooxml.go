package extract

import (
	"archive/zip"
	"bytes"
	"context"
	"encoding/xml"
	"fmt"
	"io"
	pathpkg "path"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
)

type OOXMLExtractor struct{}

func (o *OOXMLExtractor) Extract(_ context.Context, path string) (string, error) {
	switch ooxmlKind(path) {
	case "word":
		return extractDOCX(path)
	case "presentation":
		return extractPPTX(path)
	case "spreadsheet":
		return extractXLSX(path)
	default:
		return "", fmt.Errorf("unsupported ooxml format: %s", filepath.Ext(path))
	}
}

func (o *OOXMLExtractor) Supports(path string) bool {
	return ooxmlKind(path) != ""
}

func ooxmlKind(path string) string {
	switch strings.ToLower(ext(path)) {
	case ".docx", ".docm", ".dotx", ".dotm":
		return "word"
	case ".pptx", ".pptm", ".ppsx", ".ppsm", ".potx", ".potm":
		return "presentation"
	case ".xlsx", ".xlsm", ".xltx", ".xltm", ".xlam":
		return "spreadsheet"
	default:
		return ""
	}
}

func extractDOCX(path string) (string, error) {
	zr, err := zip.OpenReader(path)
	if err != nil {
		return "", err
	}
	defer func() { _ = zr.Close() }()

	var sectionNames []string
	for _, f := range zr.File {
		if f.Name == "word/document.xml" || isWordSection(f.Name, "word/header", ".xml") || isWordSection(f.Name, "word/footer", ".xml") {
			sectionNames = append(sectionNames, f.Name)
		}
	}
	sort.Strings(sectionNames)

	var parts []string
	for _, name := range sectionNames {
		data, err := readZipFile(zr.File, name)
		if err != nil {
			return "", err
		}
		text := strings.TrimSpace(extractWordMLText(data))
		if text == "" {
			continue
		}

		label := "[Document]"
		switch {
		case strings.HasPrefix(name, "word/header"):
			label = "[Header " + sectionOrdinal(name) + "]"
		case strings.HasPrefix(name, "word/footer"):
			label = "[Footer " + sectionOrdinal(name) + "]"
		}
		parts = append(parts, label+"\n"+text)
	}

	return strings.Join(parts, "\n\n"), nil
}

func extractPPTX(path string) (string, error) {
	zr, err := zip.OpenReader(path)
	if err != nil {
		return "", err
	}
	defer func() { _ = zr.Close() }()

	var slides []string
	notesMap := make(map[int]string)

	for _, f := range zr.File {
		if isPPTXSlide(f.Name) {
			slides = append(slides, f.Name)
		}
	}
	sort.Strings(slides)

	// Extract speaker notes from notesSlideN.xml files.
	for _, f := range zr.File {
		if !strings.HasPrefix(f.Name, "ppt/notesSlides/notesSlide") || !strings.HasSuffix(f.Name, ".xml") {
			continue
		}
		// Parse slide number from notesSlideN.xml.
		base := filepath.Base(f.Name)
		numStr := strings.TrimPrefix(base, "notesSlide")
		numStr = strings.TrimSuffix(numStr, ".xml")
		slideNum, err := strconv.Atoi(numStr)
		if err != nil {
			continue
		}
		data, err := readZipFile(zr.File, f.Name)
		if err != nil {
			continue
		}
		notes := strings.TrimSpace(extractNotesText(data))
		if notes != "" {
			notesMap[slideNum] = notes
		}
	}

	var parts []string
	for i, name := range slides {
		slideNum := i + 1
		data, err := readZipFile(zr.File, name)
		if err != nil {
			return "", err
		}
		text := strings.TrimSpace(extractDrawingMLText(data))

		var section strings.Builder
		_, _ = fmt.Fprintf(&section, "[Slide %d]", slideNum)
		if text != "" {
			section.WriteString("\n")
			section.WriteString(text)
		}
		if notes, ok := notesMap[slideNum]; ok {
			section.WriteString("\n\n[Notes]\n")
			section.WriteString(notes)
		}
		if text != "" || notesMap[slideNum] != "" {
			parts = append(parts, section.String())
		}
	}

	return strings.Join(parts, "\n\n"), nil
}

func extractXLSX(path string) (string, error) {
	zr, err := zip.OpenReader(path)
	if err != nil {
		return "", err
	}
	defer func() { _ = zr.Close() }()

	sharedStrings := []string{}
	if data, err := readZipFile(zr.File, "xl/sharedStrings.xml"); err == nil {
		sharedStrings = parseSharedStrings(data)
	}

	workbookData, err := readZipFile(zr.File, "xl/workbook.xml")
	if err != nil {
		return "", err
	}
	relsData, err := readZipFile(zr.File, "xl/_rels/workbook.xml.rels")
	if err != nil {
		return "", err
	}

	sheets := parseWorkbookSheets(workbookData, relsData)
	if len(sheets) == 0 {
		return "", nil
	}

	var parts []string
	for _, sheet := range sheets {
		data, err := readZipFile(zr.File, sheet.Target)
		if err != nil {
			continue
		}
		rows := parseSheetCells(data, sharedStrings)
		if len(rows) == 0 {
			continue
		}
		parts = append(parts, fmt.Sprintf("[Sheet %s]\n%s", sheet.Name, strings.Join(rows, "\n")))
	}

	return strings.Join(parts, "\n\n"), nil
}

func isWordSection(name, prefix, suffix string) bool {
	return strings.HasPrefix(name, prefix) && strings.HasSuffix(name, suffix)
}

func isPPTXSlide(name string) bool {
	return strings.HasPrefix(name, "ppt/slides/slide") &&
		strings.HasSuffix(name, ".xml") &&
		strings.Count(name, "/") == 2
}

func sectionOrdinal(name string) string {
	base := filepath.Base(name)
	digits := strings.TrimSuffix(strings.TrimLeftFunc(base, func(r rune) bool { return r < '0' || r > '9' }), ".xml")
	if digits == "" {
		return "1"
	}
	return digits
}

func readZipFile(files []*zip.File, name string) ([]byte, error) {
	for _, f := range files {
		if f.Name != name {
			continue
		}
		rc, err := f.Open()
		if err != nil {
			return nil, err
		}
		defer func() { _ = rc.Close() }()
		return io.ReadAll(rc)
	}
	return nil, fmt.Errorf("zip entry not found: %s", name)
}

func extractWordMLText(data []byte) string {
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
			case "t":
				var content string
				if err := decoder.DecodeElement(&content, &se); err == nil {
					buf.WriteString(content)
				}
			case "tab":
				buf.WriteByte('\t')
			case "br", "cr":
				buf.WriteByte('\n')
			}
		case xml.EndElement:
			if se.Name.Local == "p" {
				writeParagraphBreak(&buf)
			}
		}
	}

	return strings.TrimSpace(cleanSpacing(buf.String()))
}

func extractDrawingMLText(data []byte) string {
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
			case "t":
				var content string
				if err := decoder.DecodeElement(&content, &se); err == nil {
					buf.WriteString(content)
				}
			case "br":
				buf.WriteByte('\n')
			}
		case xml.EndElement:
			if se.Name.Local == "p" {
				writeParagraphBreak(&buf)
			}
		}
	}

	return strings.TrimSpace(cleanSpacing(buf.String()))
}

// extractNotesText extracts speaker notes from a PPTX notesSlide XML.
// Notes use DrawingML but we filter out the slide number placeholder text
// (which typically just contains the slide number digit).
func extractNotesText(data []byte) string {
	decoder := xml.NewDecoder(bytes.NewReader(data))
	var buf strings.Builder
	inPlaceholder := false
	placeholderType := ""

	for {
		token, err := decoder.Token()
		if err != nil {
			break
		}

		switch se := token.(type) {
		case xml.StartElement:
			switch se.Name.Local {
			case "ph":
				for _, attr := range se.Attr {
					if attr.Name.Local == "type" {
						placeholderType = attr.Value
					}
				}
				// Skip slide number and date/time placeholders.
				if placeholderType == "sldNum" || placeholderType == "dt" {
					inPlaceholder = true
				}
			case "sp":
				// Reset placeholder tracking for each shape.
				inPlaceholder = false
				placeholderType = ""
			case "t":
				if !inPlaceholder {
					var content string
					if err := decoder.DecodeElement(&content, &se); err == nil {
						buf.WriteString(content)
					}
				}
			case "br":
				if !inPlaceholder {
					buf.WriteByte('\n')
				}
			}
		case xml.EndElement:
			if se.Name.Local == "p" && !inPlaceholder {
				writeParagraphBreak(&buf)
			}
			if se.Name.Local == "sp" {
				inPlaceholder = false
				placeholderType = ""
			}
		}
	}

	return strings.TrimSpace(cleanSpacing(buf.String()))
}

func parseSharedStrings(data []byte) []string {
	decoder := xml.NewDecoder(bytes.NewReader(data))
	var stringsOut []string

	for {
		token, err := decoder.Token()
		if err != nil {
			break
		}
		se, ok := token.(xml.StartElement)
		if !ok || se.Name.Local != "si" {
			continue
		}
		var buf strings.Builder
		depth := 1
		for depth > 0 {
			token, err := decoder.Token()
			if err != nil {
				break
			}
			switch t := token.(type) {
			case xml.StartElement:
				depth++
				if t.Name.Local == "t" {
					var content string
					if err := decoder.DecodeElement(&content, &t); err == nil {
						buf.WriteString(content)
					}
					depth--
				}
			case xml.EndElement:
				depth--
			}
		}
		stringsOut = append(stringsOut, cleanSpacing(buf.String()))
	}

	return stringsOut
}

type xlsxSheet struct {
	Name   string
	Target string
}

func parseWorkbookSheets(workbookData, relsData []byte) []xlsxSheet {
	rels := parseWorkbookRelationships(relsData)
	decoder := xml.NewDecoder(bytes.NewReader(workbookData))
	var sheets []xlsxSheet

	for {
		token, err := decoder.Token()
		if err != nil {
			break
		}
		se, ok := token.(xml.StartElement)
		if !ok || se.Name.Local != "sheet" {
			continue
		}

		var name string
		var relID string
		for _, attr := range se.Attr {
			switch attr.Name.Local {
			case "name":
				name = attr.Value
			case "id":
				relID = attr.Value
			}
		}
		target := rels[relID]
		if name == "" || target == "" {
			continue
		}
		sheets = append(sheets, xlsxSheet{Name: name, Target: target})
	}

	return sheets
}

func parseWorkbookRelationships(data []byte) map[string]string {
	decoder := xml.NewDecoder(bytes.NewReader(data))
	rels := make(map[string]string)

	for {
		token, err := decoder.Token()
		if err != nil {
			break
		}
		se, ok := token.(xml.StartElement)
		if !ok || se.Name.Local != "Relationship" {
			continue
		}

		var id string
		var target string
		for _, attr := range se.Attr {
			switch attr.Name.Local {
			case "Id":
				id = attr.Value
			case "Target":
				target = attr.Value
			}
		}
		if id == "" || target == "" {
			continue
		}
		rels[id] = pathpkg.Clean(pathpkg.Join("xl", target))
	}

	return rels
}

func parseSheetCells(data []byte, sharedStrings []string) []string {
	decoder := xml.NewDecoder(bytes.NewReader(data))
	var rows []string

	for {
		token, err := decoder.Token()
		if err != nil {
			break
		}
		se, ok := token.(xml.StartElement)
		if !ok || se.Name.Local != "c" {
			continue
		}

		ref := ""
		cellType := ""
		for _, attr := range se.Attr {
			switch attr.Name.Local {
			case "r":
				ref = attr.Value
			case "t":
				cellType = attr.Value
			}
		}

		value, formula := parseSheetCell(decoder, cellType, sharedStrings)
		value = strings.TrimSpace(value)
		formula = strings.TrimSpace(formula)
		if ref == "" || (value == "" && formula == "") {
			continue
		}

		switch {
		case formula != "" && value != "":
			rows = append(rows, fmt.Sprintf("%s = %s -> %s", ref, formula, value))
		case formula != "":
			rows = append(rows, fmt.Sprintf("%s = %s", ref, formula))
		default:
			rows = append(rows, fmt.Sprintf("%s: %s", ref, value))
		}
	}

	return rows
}

func parseSheetCell(decoder *xml.Decoder, cellType string, sharedStrings []string) (string, string) {
	var value string
	var formula string
	depth := 1

	for depth > 0 {
		token, err := decoder.Token()
		if err != nil {
			break
		}

		switch t := token.(type) {
		case xml.StartElement:
			depth++
			switch t.Name.Local {
			case "v":
				var content string
				if err := decoder.DecodeElement(&content, &t); err == nil {
					value = resolveCellValue(cellType, content, sharedStrings)
				}
				depth--
			case "f":
				var content string
				if err := decoder.DecodeElement(&content, &t); err == nil {
					formula = content
				}
				depth--
			case "is":
				value = parseInlineString(decoder)
				depth--
			}
		case xml.EndElement:
			depth--
		}
	}

	return cleanSpacing(value), cleanSpacing(formula)
}

func parseInlineString(decoder *xml.Decoder) string {
	var buf strings.Builder
	depth := 1

	for depth > 0 {
		token, err := decoder.Token()
		if err != nil {
			break
		}

		switch t := token.(type) {
		case xml.StartElement:
			depth++
			if t.Name.Local == "t" {
				var content string
				if err := decoder.DecodeElement(&content, &t); err == nil {
					buf.WriteString(content)
				}
				depth--
			}
		case xml.EndElement:
			depth--
		}
	}

	return buf.String()
}

func resolveCellValue(cellType, content string, sharedStrings []string) string {
	switch cellType {
	case "s":
		idx, err := strconv.Atoi(strings.TrimSpace(content))
		if err != nil || idx < 0 || idx >= len(sharedStrings) {
			return content
		}
		return sharedStrings[idx]
	case "b":
		if content == "1" {
			return "TRUE"
		}
		return "FALSE"
	default:
		return content
	}
}

func writeParagraphBreak(buf *strings.Builder) {
	if buf.Len() == 0 {
		return
	}
	text := buf.String()
	if strings.HasSuffix(text, "\n\n") {
		return
	}
	if strings.HasSuffix(text, "\n") {
		buf.WriteByte('\n')
		return
	}
	buf.WriteString("\n\n")
}

func cleanSpacing(text string) string {
	lines := strings.Split(text, "\n")
	for i, line := range lines {
		lines[i] = strings.TrimRight(line, " \t")
	}
	return strings.TrimSpace(strings.Join(lines, "\n"))
}
