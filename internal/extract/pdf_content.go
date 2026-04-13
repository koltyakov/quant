package extract

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"strconv"

	"github.com/ledongthuc/pdf"
)

const maxPDFPageContentBytes int64 = 8 << 20

type pdfContentValueKind int

const (
	pdfContentNullKind pdfContentValueKind = iota
	pdfContentBoolKind
	pdfContentIntKind
	pdfContentRealKind
	pdfContentStringKind
	pdfContentNameKind
	pdfContentDictKind
	pdfContentArrayKind
)

type pdfContentValue struct {
	kind  pdfContentValueKind
	b     bool
	i     int64
	f     float64
	s     string
	dict  map[string]pdfContentValue
	array []pdfContentValue
}

func (v pdfContentValue) Kind() pdfContentValueKind {
	return v.kind
}

func (v pdfContentValue) Name() string {
	if v.kind != pdfContentNameKind {
		return ""
	}
	return v.s
}

func (v pdfContentValue) RawString() string {
	if v.kind != pdfContentStringKind {
		return ""
	}
	return v.s
}

func (v pdfContentValue) Len() int {
	if v.kind != pdfContentArrayKind {
		return 0
	}
	return len(v.array)
}

func (v pdfContentValue) Index(i int) pdfContentValue {
	if v.kind != pdfContentArrayKind || i < 0 || i >= len(v.array) {
		return pdfContentValue{}
	}
	return v.array[i]
}

type pdfContentStack struct {
	values []pdfContentValue
}

func (s *pdfContentStack) Len() int {
	return len(s.values)
}

func (s *pdfContentStack) Push(v pdfContentValue) {
	s.values = append(s.values, v)
}

func (s *pdfContentStack) Pop() pdfContentValue {
	if len(s.values) == 0 {
		return pdfContentValue{}
	}
	last := len(s.values) - 1
	v := s.values[last]
	s.values[last] = pdfContentValue{}
	s.values = s.values[:last]
	return v
}

type pdfContentKeyword string
type pdfContentName string

type pdfContentParser struct {
	data   []byte
	pos    int
	unread []any
}

func extractPDFPlainText(page pdf.Page) (string, error) {
	if !needsSafePDFTextExtraction(page.V.Key("Contents")) {
		return page.GetPlainText(nil)
	}

	content, err := readPDFPageContent(page.V.Key("Contents"))
	if err != nil {
		return "", err
	}
	if len(content) == 0 {
		return "", nil
	}

	fonts := make(map[string]*pdf.Font)
	for _, fontName := range page.Fonts() {
		font := page.Font(fontName)
		fonts[fontName] = &font
	}

	var textBuilder bytes.Buffer
	var enc pdf.TextEncoding = rawPDFTextEncoding{}

	showText := func(s string) {
		textBuilder.WriteString(s)
	}
	showEncodedText := func(s string) {
		for _, ch := range enc.Decode(s) {
			if _, err := textBuilder.WriteRune(ch); err != nil {
				panic(err)
			}
		}
	}

	if err := interpretPDFContent(content, func(stk *pdfContentStack, op string) {
		n := stk.Len()
		args := make([]pdfContentValue, n)
		for i := n - 1; i >= 0; i-- {
			args[i] = stk.Pop()
		}

		switch op {
		default:
			return
		case "BT":
			showText("\n")
		case "T*":
			showEncodedText("\n")
		case "Tf":
			if len(args) != 2 {
				panic("bad Tf")
			}
			if font, ok := fonts[args[0].Name()]; ok {
				enc = font.Encoder()
			} else {
				enc = rawPDFTextEncoding{}
			}
		case "\"":
			if len(args) != 3 {
				panic("bad \" operator")
			}
			showEncodedText(args[2].RawString())
		case "'":
			if len(args) != 1 {
				panic("bad ' operator")
			}
			showEncodedText(args[0].RawString())
		case "Tj":
			if len(args) != 1 {
				panic("bad Tj operator")
			}
			showEncodedText(args[0].RawString())
		case "TJ":
			if len(args) != 1 {
				panic("bad TJ operator")
			}
			v := args[0]
			for i := 0; i < v.Len(); i++ {
				x := v.Index(i)
				if x.Kind() == pdfContentStringKind {
					showEncodedText(x.RawString())
				}
			}
		}
	}); err != nil {
		return "", err
	}

	return textBuilder.String(), nil
}

type rawPDFTextEncoding struct{}

func (rawPDFTextEncoding) Decode(raw string) string {
	return raw
}

func readPDFPageContent(contents pdf.Value) ([]byte, error) {
	switch contents.Kind() {
	case pdf.Null:
		return nil, nil
	case pdf.Stream:
		return readPDFContentStream(contents, maxPDFPageContentBytes, "pdf page content")
	case pdf.Array:
		var joined bytes.Buffer
		remaining := maxPDFPageContentBytes
		for i := 0; i < contents.Len(); i++ {
			part, err := readPDFContentStream(contents.Index(i), remaining, fmt.Sprintf("pdf page content stream %d", i))
			if err != nil {
				return nil, err
			}
			joined.Write(part)
			remaining -= int64(len(part))
			if remaining < 0 {
				return nil, fmt.Errorf("%w: pdf page content (%s > %s)", ErrFileTooLarge, formatExtractBytes(maxPDFPageContentBytes+1), formatExtractBytes(maxPDFPageContentBytes))
			}
		}
		return joined.Bytes(), nil
	default:
		return nil, fmt.Errorf("unsupported pdf page contents kind: %v", contents.Kind())
	}
}

func needsSafePDFTextExtraction(contents pdf.Value) bool {
	if contents.Kind() != pdf.Array {
		return false
	}

	for i := 0; i < contents.Len()-1; i++ {
		stream, err := readPDFContentStream(contents.Index(i), maxPDFPageContentBytes, fmt.Sprintf("pdf page content stream %d", i))
		if err != nil {
			return false
		}
		if pdfContentStreamEndsMidToken(stream) {
			return true
		}
	}
	return false
}

func readPDFContentStream(stream pdf.Value, limit int64, name string) ([]byte, error) {
	if stream.Kind() != pdf.Stream {
		return nil, fmt.Errorf("pdf page content stream missing: %s", name)
	}
	rc := stream.Reader()
	defer func() { _ = rc.Close() }()

	return readAllLimited(context.TODO(), rc, limit, name)
}

func pdfContentStreamEndsMidToken(data []byte) bool {
	var (
		literalDepth int
		inHex        bool
		inComment    bool
		tokenOpen    bool
		escape       bool
		arrayDepth   int
		dictDepth    int
	)

	for i := 0; i < len(data); i++ {
		c := data[i]

		if inComment {
			if c == '\n' || c == '\r' {
				inComment = false
			}
			continue
		}

		if literalDepth > 0 {
			if escape {
				escape = false
				continue
			}
			switch c {
			case '\\':
				escape = true
			case '(':
				literalDepth++
			case ')':
				literalDepth--
			}
			continue
		}

		if inHex {
			if c == '>' {
				inHex = false
			}
			continue
		}

		switch {
		case isPDFContentSpace(c):
			tokenOpen = false
		case c == '%':
			inComment = true
			tokenOpen = false
		case c == '(':
			literalDepth = 1
			tokenOpen = false
		case c == '<':
			if i+1 < len(data) && data[i+1] == '<' {
				i++
				dictDepth++
				tokenOpen = false
				continue
			}
			inHex = true
			tokenOpen = false
		case c == '>':
			if i+1 < len(data) && data[i+1] == '>' {
				i++
				if dictDepth > 0 {
					dictDepth--
				}
			}
			tokenOpen = false
		case c == '[':
			arrayDepth++
			tokenOpen = false
		case c == ']':
			if arrayDepth > 0 {
				arrayDepth--
			}
			tokenOpen = false
		case c == '{' || c == '}':
			tokenOpen = false
		case c == '/':
			tokenOpen = true
		default:
			tokenOpen = true
		}
	}

	return literalDepth > 0 || inHex || inComment || tokenOpen || arrayDepth > 0 || dictDepth > 0
}

func interpretPDFContent(data []byte, do func(stk *pdfContentStack, op string)) (err error) {
	parser := &pdfContentParser{data: data}
	var stk pdfContentStack

	defer func() {
		if r := recover(); r != nil {
			err = fmt.Errorf("%v", r)
		}
	}()

	for {
		tok, err := parser.readToken()
		if err != nil {
			return err
		}
		if tok == nil {
			return nil
		}
		if kw, ok := tok.(pdfContentKeyword); ok {
			switch kw {
			case "null", "[", "<<":
				parser.unreadToken(tok)
			case "]", ">>":
				return fmt.Errorf("unexpected content keyword %q", string(kw))
			default:
				do(&stk, string(kw))
				continue
			}
		} else {
			parser.unreadToken(tok)
		}
		obj, err := parser.readObject()
		if err != nil {
			return err
		}
		stk.Push(obj)
	}
}

func (p *pdfContentParser) readObject() (pdfContentValue, error) {
	tok, err := p.readToken()
	if err != nil {
		return pdfContentValue{}, err
	}
	if tok == nil {
		return pdfContentValue{}, io.EOF
	}

	switch v := tok.(type) {
	case pdfContentKeyword:
		switch v {
		case "null":
			return pdfContentValue{kind: pdfContentNullKind}, nil
		case "<<":
			return p.readDict()
		case "[":
			return p.readArray()
		default:
			return pdfContentValue{}, fmt.Errorf("unexpected keyword %q parsing object", string(v))
		}
	case bool:
		return pdfContentValue{kind: pdfContentBoolKind, b: v}, nil
	case int64:
		return pdfContentValue{kind: pdfContentIntKind, i: v}, nil
	case float64:
		return pdfContentValue{kind: pdfContentRealKind, f: v}, nil
	case string:
		return pdfContentValue{kind: pdfContentStringKind, s: v}, nil
	case pdfContentName:
		return pdfContentValue{kind: pdfContentNameKind, s: string(v)}, nil
	default:
		return pdfContentValue{}, fmt.Errorf("unexpected token %T parsing object", tok)
	}
}

func (p *pdfContentParser) readArray() (pdfContentValue, error) {
	var values []pdfContentValue
	for {
		tok, err := p.readToken()
		if err != nil {
			return pdfContentValue{}, err
		}
		if tok == nil {
			return pdfContentValue{}, fmt.Errorf("unterminated pdf content array")
		}
		if kw, ok := tok.(pdfContentKeyword); ok && kw == "]" {
			return pdfContentValue{kind: pdfContentArrayKind, array: values}, nil
		}
		p.unreadToken(tok)
		value, err := p.readObject()
		if err != nil {
			return pdfContentValue{}, err
		}
		values = append(values, value)
	}
}

func (p *pdfContentParser) readDict() (pdfContentValue, error) {
	values := make(map[string]pdfContentValue)
	for {
		tok, err := p.readToken()
		if err != nil {
			return pdfContentValue{}, err
		}
		if tok == nil {
			return pdfContentValue{}, fmt.Errorf("unterminated pdf content dictionary")
		}
		if kw, ok := tok.(pdfContentKeyword); ok && kw == ">>" {
			return pdfContentValue{kind: pdfContentDictKind, dict: values}, nil
		}

		name, ok := tok.(pdfContentName)
		if !ok {
			return pdfContentValue{}, fmt.Errorf("unexpected dictionary key %T", tok)
		}

		value, err := p.readObject()
		if err != nil {
			return pdfContentValue{}, err
		}
		values[string(name)] = value
	}
}

func (p *pdfContentParser) readToken() (any, error) {
	if n := len(p.unread); n > 0 {
		tok := p.unread[n-1]
		p.unread = p.unread[:n-1]
		return tok, nil
	}

	if err := p.skipWhitespaceAndComments(); err != nil {
		return nil, err
	}
	if p.pos >= len(p.data) {
		return nil, nil
	}

	c := p.data[p.pos]
	p.pos++

	switch c {
	case '<':
		if p.matchByte('<') {
			return pdfContentKeyword("<<"), nil
		}
		return p.readHexString()
	case '>':
		if p.matchByte('>') {
			return pdfContentKeyword(">>"), nil
		}
		return nil, fmt.Errorf("unexpected delimiter %q", c)
	case '(':
		return p.readLiteralString()
	case '[':
		return pdfContentKeyword("["), nil
	case ']':
		return pdfContentKeyword("]"), nil
	case '/':
		return p.readName()
	default:
		if isPDFContentDelimiter(c) {
			return nil, fmt.Errorf("unexpected delimiter %q", c)
		}
		p.pos--
		return p.readKeyword()
	}
}

func (p *pdfContentParser) readLiteralString() (string, error) {
	var out []byte
	depth := 1

	for p.pos < len(p.data) {
		c := p.data[p.pos]
		p.pos++

		switch c {
		case '(':
			depth++
			out = append(out, c)
		case ')':
			depth--
			if depth == 0 {
				return string(out), nil
			}
			out = append(out, c)
		case '\\':
			if p.pos >= len(p.data) {
				return "", fmt.Errorf("unterminated escape in literal string")
			}
			c = p.data[p.pos]
			p.pos++
			switch c {
			case 'n':
				out = append(out, '\n')
			case 'r':
				out = append(out, '\r')
			case 'b':
				out = append(out, '\b')
			case 't':
				out = append(out, '\t')
			case 'f':
				out = append(out, '\f')
			case '(', ')', '\\':
				out = append(out, c)
			case '\r':
				if p.pos < len(p.data) && p.data[p.pos] == '\n' {
					p.pos++
				}
			case '\n':
			case '0', '1', '2', '3', '4', '5', '6', '7':
				x := int(c - '0')
				for i := 0; i < 2 && p.pos < len(p.data); i++ {
					next := p.data[p.pos]
					if next < '0' || next > '7' {
						break
					}
					p.pos++
					x = x*8 + int(next-'0')
				}
				if x > 255 {
					return "", fmt.Errorf("invalid octal escape \\%03o", x)
				}
				out = append(out, byte(x))
			default:
				return "", fmt.Errorf("invalid escape sequence \\%c", c)
			}
		default:
			out = append(out, c)
		}
	}

	return "", fmt.Errorf("unterminated literal string")
}

func (p *pdfContentParser) readHexString() (string, error) {
	var out []byte
	var nibble byte
	haveNibble := false

	for p.pos < len(p.data) {
		c := p.data[p.pos]
		p.pos++
		if isPDFContentSpace(c) {
			continue
		}
		if c == '>' {
			if haveNibble {
				out = append(out, nibble<<4)
			}
			return string(out), nil
		}
		v := fromHex(c)
		if v < 0 {
			return "", fmt.Errorf("malformed hex string")
		}
		if !haveNibble {
			nibble = byte(v) //nolint:gosec // fromHex returns 0-15
			haveNibble = true
			continue
		}
		out = append(out, nibble<<4|byte(v)) //nolint:gosec // fromHex returns 0-15
		haveNibble = false
	}

	return "", fmt.Errorf("unterminated hex string")
}

func (p *pdfContentParser) readName() (pdfContentName, error) {
	start := p.pos
	var out []byte
	for p.pos < len(p.data) {
		c := p.data[p.pos]
		if isPDFContentDelimiter(c) || isPDFContentSpace(c) {
			break
		}
		p.pos++
		if c == '#' {
			if p.pos+1 >= len(p.data) {
				return "", fmt.Errorf("malformed name escape")
			}
			hi := fromHex(p.data[p.pos])
			lo := fromHex(p.data[p.pos+1])
			if hi < 0 || lo < 0 {
				return "", fmt.Errorf("malformed name escape")
			}
			out = append(out, byte(hi<<4|lo)) //nolint:gosec // fromHex returns 0-15
			p.pos += 2
			continue
		}
		out = append(out, c)
	}
	if len(out) == 0 && p.pos == start {
		return "", fmt.Errorf("empty name")
	}
	return pdfContentName(out), nil
}

func (p *pdfContentParser) readKeyword() (any, error) {
	start := p.pos
	for p.pos < len(p.data) {
		c := p.data[p.pos]
		if isPDFContentDelimiter(c) || isPDFContentSpace(c) {
			break
		}
		p.pos++
	}
	if p.pos == start {
		return nil, fmt.Errorf("empty keyword")
	}
	s := string(p.data[start:p.pos])
	switch s {
	case "true":
		return true, nil
	case "false":
		return false, nil
	case "null":
		return pdfContentKeyword("null"), nil
	}
	if i, err := strconv.ParseInt(s, 10, 64); err == nil {
		return i, nil
	}
	if f, err := strconv.ParseFloat(s, 64); err == nil {
		return f, nil
	}
	return pdfContentKeyword(s), nil
}

func (p *pdfContentParser) skipWhitespaceAndComments() error {
	for p.pos < len(p.data) {
		switch c := p.data[p.pos]; {
		case isPDFContentSpace(c):
			p.pos++
		case c == '%':
			p.pos++
			for p.pos < len(p.data) {
				if p.data[p.pos] == '\n' || p.data[p.pos] == '\r' {
					break
				}
				p.pos++
			}
		default:
			return nil
		}
	}
	return nil
}

func (p *pdfContentParser) unreadToken(tok any) {
	p.unread = append(p.unread, tok)
}

func (p *pdfContentParser) matchByte(want byte) bool {
	if p.pos >= len(p.data) || p.data[p.pos] != want {
		return false
	}
	p.pos++
	return true
}

func isPDFContentSpace(c byte) bool {
	switch c {
	case 0, '\t', '\n', '\f', '\r', ' ':
		return true
	default:
		return false
	}
}

func isPDFContentDelimiter(c byte) bool {
	switch c {
	case '(', ')', '<', '>', '[', ']', '{', '}', '/', '%':
		return true
	default:
		return false
	}
}

func fromHex(c byte) int {
	switch {
	case '0' <= c && c <= '9':
		return int(c - '0')
	case 'a' <= c && c <= 'f':
		return int(c-'a') + 10
	case 'A' <= c && c <= 'F':
		return int(c-'A') + 10
	default:
		return -1
	}
}
