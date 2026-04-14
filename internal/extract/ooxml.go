package extract

import (
	"archive/zip"
	"bytes"
	"context"
	"encoding/xml"
	"fmt"
	pathpkg "path"
	"path/filepath"
	"sort"
	"strconv"
	"strings"

	"github.com/koltyakov/quant/internal/logx"
)

type OOXMLExtractor struct{}

const officeDocumentRelationshipNS = "http://schemas.openxmlformats.org/officeDocument/2006/relationships"

type pptxSlide struct {
	Number      int
	Target      string
	NotesTarget string
}

func (o *OOXMLExtractor) Extract(ctx context.Context, path string) (string, error) {
	if err := checkContext(ctx); err != nil {
		return "", err
	}
	if err := ensureFileSize(path, maxExtractorFileSize); err != nil {
		return "", err
	}
	if err := ensureNotOLE2(path); err != nil {
		return "", err
	}

	switch ooxmlKind(path) {
	case "word":
		return extractDOCX(ctx, path)
	case "presentation":
		return extractPPTX(ctx, path)
	case "spreadsheet":
		return extractXLSX(ctx, path)
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

func extractDOCX(ctx context.Context, path string) (string, error) {
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
		if err := checkContext(ctx); err != nil {
			return "", err
		}

		data, err := readZipFile(ctx, zr.File, name)
		if err != nil {
			return "", err
		}
		text, err := extractWordMLText(ctx, data)
		if err != nil {
			return "", err
		}
		text = strings.TrimSpace(text)
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

func extractPPTX(ctx context.Context, path string) (string, error) {
	zr, err := zip.OpenReader(path)
	if err != nil {
		return "", err
	}
	defer func() { _ = zr.Close() }()

	slides, err := parsePPTXSlides(ctx, zr.File)
	if err != nil {
		return "", err
	}

	var parts []string
	for _, slide := range slides {
		if err := checkContext(ctx); err != nil {
			return "", err
		}

		data, err := readZipFile(ctx, zr.File, slide.Target)
		if err != nil {
			return "", err
		}
		text, err := extractDrawingMLText(ctx, data)
		if err != nil {
			return "", err
		}
		text = strings.TrimSpace(text)
		notes := ""
		if slide.NotesTarget != "" {
			if notesData, err := readZipFile(ctx, zr.File, slide.NotesTarget); err == nil {
				notes, err = extractNotesText(ctx, notesData)
				if err != nil {
					return "", err
				}
				notes = strings.TrimSpace(notes)
			}
		}

		var section strings.Builder
		_, _ = fmt.Fprintf(&section, "[Slide %d]", slide.Number)
		if text != "" {
			section.WriteString("\n")
			section.WriteString(text)
		}
		if notes != "" {
			section.WriteString("\n\n[Notes]\n")
			section.WriteString(notes)
		}
		if text != "" || notes != "" {
			parts = append(parts, section.String())
		}
	}

	return strings.Join(parts, "\n\n"), nil
}

func extractXLSX(ctx context.Context, path string) (string, error) {
	zr, err := zip.OpenReader(path)
	if err != nil {
		return "", err
	}
	defer func() { _ = zr.Close() }()

	sharedStrings := []string{}
	if data, err := readZipFile(ctx, zr.File, "xl/sharedStrings.xml"); err == nil {
		sharedStrings, err = parseSharedStrings(ctx, data)
		if err != nil {
			return "", err
		}
	}

	workbookData, err := readZipFile(ctx, zr.File, "xl/workbook.xml")
	if err != nil {
		return "", err
	}
	relsData, err := readZipFile(ctx, zr.File, "xl/_rels/workbook.xml.rels")
	if err != nil {
		return "", err
	}

	sheets, err := parseWorkbookSheets(ctx, workbookData, relsData)
	if err != nil {
		return "", err
	}
	if len(sheets) == 0 {
		return "", nil
	}

	var parts []string
	for _, sheet := range sheets {
		if err := checkContext(ctx); err != nil {
			return "", err
		}

		data, err := readZipFile(ctx, zr.File, sheet.Target)
		if err != nil {
			logx.Warn("xlsx sheet extraction skipped", "sheet", sheet.Name, "target", sheet.Target, "err", err)
			continue
		}
		rows, err := parseSheetCells(ctx, data, sharedStrings)
		if err != nil {
			return "", err
		}
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

func parsePPTXSlides(ctx context.Context, files []*zip.File) ([]pptxSlide, error) {
	if slides, err := parsePPTXSlidesFromPresentation(ctx, files); err == nil && len(slides) > 0 {
		return slides, nil
	}
	return fallbackPPTXSlides(ctx, files)
}

func parsePPTXSlidesFromPresentation(ctx context.Context, files []*zip.File) ([]pptxSlide, error) {
	presentationData, err := readZipFile(ctx, files, "ppt/presentation.xml")
	if err != nil {
		return nil, err
	}
	relsData, err := readZipFile(ctx, files, "ppt/_rels/presentation.xml.rels")
	if err != nil {
		return nil, err
	}

	rels, err := parseRelationships(ctx, relsData, "ppt")
	if err != nil {
		return nil, err
	}
	decoder := xml.NewDecoder(bytes.NewReader(presentationData))
	var slides []pptxSlide

	for {
		token, err := nextXMLToken(ctx, decoder)
		if err != nil {
			break
		}
		se, ok := token.(xml.StartElement)
		if !ok || se.Name.Local != "sldId" {
			continue
		}

		relID := ""
		for _, attr := range se.Attr {
			if attr.Name.Local == "id" && attr.Name.Space == officeDocumentRelationshipNS {
				relID = attr.Value
				break
			}
		}
		target := rels[relID]
		slideNum, ok := parsePPTXSlideNumber(target)
		if relID == "" || target == "" || !ok {
			continue
		}
		slides = append(slides, pptxSlide{
			Number:      slideNum,
			Target:      target,
			NotesTarget: parsePPTXNotesTarget(ctx, files, target),
		})
	}

	if err := checkContext(ctx); err != nil {
		return nil, err
	}
	return slides, nil
}

func fallbackPPTXSlides(ctx context.Context, files []*zip.File) ([]pptxSlide, error) {
	var slides []pptxSlide
	for _, f := range files {
		if err := checkContext(ctx); err != nil {
			return nil, err
		}
		if !isPPTXSlide(f.Name) {
			continue
		}
		slideNum, ok := parsePPTXSlideNumber(f.Name)
		if !ok {
			continue
		}
		slides = append(slides, pptxSlide{
			Number:      slideNum,
			Target:      f.Name,
			NotesTarget: parsePPTXNotesTarget(ctx, files, f.Name),
		})
	}
	sort.Slice(slides, func(i, j int) bool {
		return slides[i].Number < slides[j].Number
	})
	return slides, nil
}

func parsePPTXSlideNumber(name string) (int, bool) {
	base := filepath.Base(name)
	if !strings.HasPrefix(base, "slide") || !strings.HasSuffix(base, ".xml") {
		return 0, false
	}
	numStr := strings.TrimSuffix(strings.TrimPrefix(base, "slide"), ".xml")
	n, err := strconv.Atoi(numStr)
	if err != nil {
		return 0, false
	}
	return n, true
}

func parsePPTXNotesTarget(ctx context.Context, files []*zip.File, slideTarget string) string {
	relsPath := pathpkg.Join(pathpkg.Dir(slideTarget), "_rels", pathpkg.Base(slideTarget)+".rels")
	relsData, err := readZipFile(ctx, files, relsPath)
	if err == nil {
		slideDir := pathpkg.Dir(slideTarget)
		rels, parseErr := parseRelationshipEntries(ctx, relsData, slideDir)
		if parseErr == nil {
			for _, rel := range rels {
				if strings.HasSuffix(rel.Type, "/notesSlide") {
					return rel.Target
				}
			}
		}
	}

	if slideNum, ok := parsePPTXSlideNumber(slideTarget); ok {
		fallback := pathpkg.Join("ppt", "notesSlides", fmt.Sprintf("notesSlide%d.xml", slideNum))
		if hasZipEntry(files, fallback) {
			return fallback
		}
	}
	if err := checkContext(ctx); err != nil {
		return ""
	}
	return ""
}

type relationshipEntry struct {
	ID     string
	Target string
	Type   string
}

func parseRelationships(ctx context.Context, data []byte, baseDir string) (map[string]string, error) {
	entries, err := parseRelationshipEntries(ctx, data, baseDir)
	if err != nil {
		return nil, err
	}
	rels := make(map[string]string, len(entries))
	for _, rel := range entries {
		rels[rel.ID] = rel.Target
	}
	return rels, nil
}

func hasZipEntry(files []*zip.File, name string) bool {
	for _, f := range files {
		if f.Name == name {
			return true
		}
	}
	return false
}

func parseRelationshipEntries(ctx context.Context, data []byte, baseDir string) ([]relationshipEntry, error) {
	decoder := xml.NewDecoder(bytes.NewReader(data))
	var rels []relationshipEntry

	for {
		token, err := nextXMLToken(ctx, decoder)
		if err != nil {
			break
		}
		se, ok := token.(xml.StartElement)
		if !ok || se.Name.Local != "Relationship" {
			continue
		}

		var rel relationshipEntry
		for _, attr := range se.Attr {
			switch attr.Name.Local {
			case "Id":
				rel.ID = attr.Value
			case "Target":
				rel.Target = pathpkg.Clean(pathpkg.Join(baseDir, attr.Value))
			case "Type":
				rel.Type = attr.Value
			}
		}
		if rel.Target != "" {
			rels = append(rels, rel)
		}
	}

	if err := checkContext(ctx); err != nil {
		return nil, err
	}
	return rels, nil
}

func sectionOrdinal(name string) string {
	base := filepath.Base(name)
	digits := strings.TrimSuffix(strings.TrimLeftFunc(base, func(r rune) bool { return r < '0' || r > '9' }), ".xml")
	if digits == "" {
		return "1"
	}
	return digits
}

func extractWordMLText(ctx context.Context, data []byte) (string, error) {
	decoder := xml.NewDecoder(bytes.NewReader(data))
	var buf strings.Builder
	var inTable, inCell bool
	cellIndex := 0
	cellHasText := false

	for {
		token, err := nextXMLToken(ctx, decoder)
		if err != nil {
			break
		}

		switch se := token.(type) {
		case xml.StartElement:
			switch se.Name.Local {
			case "tbl":
				inTable = true
			case "tc":
				inCell = true
				cellHasText = false
				if cellIndex > 0 {
					buf.WriteString(" | ")
				}
				cellIndex++
			case "t":
				var content string
				if err := decoder.DecodeElement(&content, &se); err == nil {
					if inCell && cellHasText {
						buf.WriteByte(' ')
					}
					buf.WriteString(content)
					if inCell {
						cellHasText = true
					}
				}
			case "tab":
				buf.WriteByte('\t')
			case "br", "cr":
				buf.WriteByte('\n')
			}
		case xml.EndElement:
			switch se.Name.Local {
			case "p":
				if !inCell {
					writeParagraphBreak(&buf)
				}
			case "tc":
				inCell = false
			case "tr":
				cellIndex = 0
				buf.WriteByte('\n')
			case "tbl":
				inTable = false
				writeParagraphBreak(&buf)
			}
		}
	}

	_ = inTable

	if err := checkContext(ctx); err != nil {
		return "", err
	}
	return strings.TrimSpace(cleanSpacing(buf.String())), nil
}

func extractDrawingMLText(ctx context.Context, data []byte) (string, error) {
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

	if err := checkContext(ctx); err != nil {
		return "", err
	}
	return strings.TrimSpace(cleanSpacing(buf.String())), nil
}

// extractNotesText extracts speaker notes from a PPTX notesSlide XML.
// Notes use DrawingML but we filter out the slide number placeholder text
// (which typically just contains the slide number digit).
func extractNotesText(ctx context.Context, data []byte) (string, error) {
	decoder := xml.NewDecoder(bytes.NewReader(data))
	var buf strings.Builder
	inPlaceholder := false
	placeholderType := ""

	for {
		token, err := nextXMLToken(ctx, decoder)
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

	if err := checkContext(ctx); err != nil {
		return "", err
	}
	return strings.TrimSpace(cleanSpacing(buf.String())), nil
}

func parseSharedStrings(ctx context.Context, data []byte) ([]string, error) {
	decoder := xml.NewDecoder(bytes.NewReader(data))
	var stringsOut []string

	for {
		token, err := nextXMLToken(ctx, decoder)
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
			token, err := nextXMLToken(ctx, decoder)
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

	if err := checkContext(ctx); err != nil {
		return nil, err
	}
	return stringsOut, nil
}

type xlsxSheet struct {
	Name   string
	Target string
}

func parseWorkbookSheets(ctx context.Context, workbookData, relsData []byte) ([]xlsxSheet, error) {
	rels, err := parseWorkbookRelationships(ctx, relsData)
	if err != nil {
		return nil, err
	}
	decoder := xml.NewDecoder(bytes.NewReader(workbookData))
	var sheets []xlsxSheet

	for {
		token, err := nextXMLToken(ctx, decoder)
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

	if err := checkContext(ctx); err != nil {
		return nil, err
	}
	return sheets, nil
}

func parseWorkbookRelationships(ctx context.Context, data []byte) (map[string]string, error) {
	decoder := xml.NewDecoder(bytes.NewReader(data))
	rels := make(map[string]string)

	for {
		token, err := nextXMLToken(ctx, decoder)
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

	if err := checkContext(ctx); err != nil {
		return nil, err
	}
	return rels, nil
}

func parseSheetCells(ctx context.Context, data []byte, sharedStrings []string) ([]string, error) {
	decoder := xml.NewDecoder(bytes.NewReader(data))
	var rows []string

	for {
		token, err := nextXMLToken(ctx, decoder)
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

		value, formula, err := parseSheetCell(ctx, decoder, cellType, sharedStrings)
		if err != nil {
			return nil, err
		}
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

	if err := checkContext(ctx); err != nil {
		return nil, err
	}
	return rows, nil
}

func parseSheetCell(ctx context.Context, decoder *xml.Decoder, cellType string, sharedStrings []string) (string, string, error) {
	var value string
	var formula string
	depth := 1

	for depth > 0 {
		token, err := nextXMLToken(ctx, decoder)
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
				value, err = parseInlineString(ctx, decoder)
				if err != nil {
					return "", "", err
				}
				depth--
			}
		case xml.EndElement:
			depth--
		}
	}

	if err := checkContext(ctx); err != nil {
		return "", "", err
	}
	return cleanSpacing(value), cleanSpacing(formula), nil
}

func parseInlineString(ctx context.Context, decoder *xml.Decoder) (string, error) {
	var buf strings.Builder
	depth := 1

	for depth > 0 {
		token, err := nextXMLToken(ctx, decoder)
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

	if err := checkContext(ctx); err != nil {
		return "", err
	}
	return buf.String(), nil
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
	s := buf.String()
	n := len(s)
	if n >= 2 && s[n-1] == '\n' && s[n-2] == '\n' {
		return
	}
	if n >= 1 && s[n-1] == '\n' {
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
