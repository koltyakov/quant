package extract

import (
	"fmt"
	"testing"
)

func TestSkipWhitespaceAndComments(t *testing.T) {
	tests := []struct {
		name  string
		input string
		pos   int
		want  int
	}{
		{"empty", "", 0, 0},
		{"no whitespace", "abc", 0, 0},
		{"spaces only", "   ", 0, 3},
		{"spaces before token", "  abc", 0, 2},
		{"tab and newline", "\t\nabc", 0, 2},
		{"null byte", "\x00abc", 0, 1},
		{"form feed", "\fabc", 0, 1},
		{"carriage return", "\rabc", 0, 1},
		{"comment only", "%hello", 0, 6},
		{"comment then newline", "%hello\nabc", 0, 7},
		{"comment then CR", "%hello\rabc", 0, 7},
		{"comment then CRLF", "%hello\r\nabc", 0, 8},
		{"spaces and comment", "  %comment\n  abc", 0, 13},
		{"multiple comments", "%a\n%b\ncd", 0, 6},
		{"no consumable at position", "abc   def", 3, 6},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			p := &pdfContentParser{data: []byte(tt.input), pos: tt.pos}
			if err := p.skipWhitespaceAndComments(); err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if p.pos != tt.want {
				t.Errorf("pos = %d, want %d", p.pos, tt.want)
			}
		})
	}
}

func TestReadLiteralString(t *testing.T) {
	tests := []struct {
		name  string
		input string
		want  string
	}{
		{"empty string", ")", ""},
		{"simple string", "hello)", "hello"},
		{"plain text", "Hello World)", "Hello World"},
		{"nested parens", "((a)(b)))", "((a)(b))"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			p := &pdfContentParser{data: []byte(tt.input), pos: 0}
			got, err := p.readLiteralString()
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if got != tt.want {
				t.Errorf("got %q, want %q", got, tt.want)
			}
		})
	}

	t.Run("escape sequences", func(t *testing.T) {
		input := []byte("line1\\nline2)")
		p := &pdfContentParser{data: input, pos: 0}
		got, err := p.readLiteralString()
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if got != "line1\nline2" {
			t.Errorf("got %q, want %q", got, "line1\nline2")
		}
	})

	t.Run("escape r", func(t *testing.T) {
		input := []byte("text\\rmore)")
		p := &pdfContentParser{data: input, pos: 0}
		got, err := p.readLiteralString()
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if got != "text\rmore" {
			t.Errorf("got %q, want %q", got, "text\rmore")
		}
	})

	t.Run("escape b", func(t *testing.T) {
		p := &pdfContentParser{data: []byte("back\\bspace)"), pos: 0}
		got, err := p.readLiteralString()
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		want := "back\bspace"
		if got != want {
			t.Errorf("got %q, want %q", got, want)
		}
	})

	t.Run("escape t", func(t *testing.T) {
		p := &pdfContentParser{data: []byte("tab\\tsep)"), pos: 0}
		got, err := p.readLiteralString()
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		want := "tab\tsep"
		if got != want {
			t.Errorf("got %q, want %q", got, want)
		}
	})

	t.Run("escape f", func(t *testing.T) {
		p := &pdfContentParser{data: []byte("form\\ffeed)"), pos: 0}
		got, err := p.readLiteralString()
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		want := "form\ffeed"
		if got != want {
			t.Errorf("got %q, want %q", got, want)
		}
	})

	t.Run("escape paren", func(t *testing.T) {
		p := &pdfContentParser{data: []byte("\\(text\\))"), pos: 0}
		got, err := p.readLiteralString()
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if got != "(text)" {
			t.Errorf("got %q, want %q", got, "(text)")
		}
	})

	t.Run("escape backslash", func(t *testing.T) {
		p := &pdfContentParser{data: []byte("back\\\\slash)"), pos: 0}
		got, err := p.readLiteralString()
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if got != "back\\slash" {
			t.Errorf("got %q, want %q", got, "back\\slash")
		}
	})

	t.Run("octal escape", func(t *testing.T) {
		p := &pdfContentParser{data: []byte("\\101\\102)"), pos: 0}
		got, err := p.readLiteralString()
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if got != "AB" {
			t.Errorf("got %q, want %q", got, "AB")
		}
	})

	t.Run("octal escape zero", func(t *testing.T) {
		p := &pdfContentParser{data: []byte("\\000)"), pos: 0}
		got, err := p.readLiteralString()
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		want := "\x00"
		if got != want {
			t.Errorf("got %q, want %q", got, want)
		}
	})

	t.Run("octal escape max", func(t *testing.T) {
		p := &pdfContentParser{data: []byte("\\377)"), pos: 0}
		got, err := p.readLiteralString()
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		want := "\xff"
		if got != want {
			t.Errorf("got %q, want %q", got, want)
		}
	})

	t.Run("escape CR with LF", func(t *testing.T) {
		p := &pdfContentParser{data: []byte("before\\\r\nafter)"), pos: 0}
		got, err := p.readLiteralString()
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if got != "beforeafter" {
			t.Errorf("got %q, want %q", got, "beforeafter")
		}
	})

	t.Run("escape LF", func(t *testing.T) {
		p := &pdfContentParser{data: []byte("before\\\nafter)"), pos: 0}
		got, err := p.readLiteralString()
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if got != "beforeafter" {
			t.Errorf("got %q, want %q", got, "beforeafter")
		}
	})

	t.Run("nested parens", func(t *testing.T) {
		p := &pdfContentParser{data: []byte("(inner)outer)"), pos: 0}
		got, err := p.readLiteralString()
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if got != "(inner)outer" {
			t.Errorf("got %q, want %q", got, "(inner)outer")
		}
	})
}

func TestReadLiteralString_Errors(t *testing.T) {
	t.Run("unterminated string", func(t *testing.T) {
		p := &pdfContentParser{data: []byte("unterminated"), pos: 0}
		_, err := p.readLiteralString()
		if err == nil {
			t.Fatal("expected error for unterminated literal string")
		}
	})

	t.Run("unterminated escape", func(t *testing.T) {
		p := &pdfContentParser{data: []byte("trail\\"), pos: 0}
		_, err := p.readLiteralString()
		if err == nil {
			t.Fatal("expected error for unterminated escape")
		}
	})

	t.Run("invalid escape character", func(t *testing.T) {
		p := &pdfContentParser{data: []byte("\\z)"), pos: 0}
		_, err := p.readLiteralString()
		if err == nil {
			t.Fatal("expected error for invalid escape character")
		}
	})

	t.Run("octal escape > 255", func(t *testing.T) {
		p := &pdfContentParser{data: []byte("\\400)"), pos: 0}
		_, err := p.readLiteralString()
		if err == nil {
			t.Fatal("expected error for octal escape > 255")
		}
	})
}

func TestReadName(t *testing.T) {
	tests := []struct {
		name    string
		input   string
		want    string
		wantErr bool
	}{
		{"simple name", "Type ", "Type", false},
		{"name before slash", "Type/", "Type", false},
		{"name before array close", "Name]", "Name", false},
		{"name before dict close", "Name>>", "Name", false},
		{"name at end of data", "End", "End", false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			p := &pdfContentParser{data: []byte(tt.input), pos: 0}
			got, err := p.readName()
			if tt.wantErr {
				if err == nil {
					t.Fatalf("expected error, got nil")
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if string(got) != tt.want {
				t.Errorf("got %q, want %q", string(got), tt.want)
			}
		})
	}

	t.Run("hex escape", func(t *testing.T) {
		p := &pdfContentParser{data: []byte("Test#20Name "), pos: 0}
		got, err := p.readName()
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if string(got) != "Test Name" {
			t.Errorf("got %q, want %q", string(got), "Test Name")
		}
	})

	t.Run("multiple hex escapes", func(t *testing.T) {
		p := &pdfContentParser{data: []byte("A#20B#20C "), pos: 0}
		got, err := p.readName()
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if string(got) != "A B C" {
			t.Errorf("got %q, want %q", string(got), "A B C")
		}
	})

	t.Run("hex escape at end", func(t *testing.T) {
		p := &pdfContentParser{data: []byte("Bad#2"), pos: 0}
		_, err := p.readName()
		if err == nil {
			t.Fatal("expected error for malformed hex escape at end")
		}
	})

	t.Run("empty name", func(t *testing.T) {
		p := &pdfContentParser{data: []byte(" "), pos: 0}
		_, err := p.readName()
		if err == nil {
			t.Fatal("expected error for empty name")
		}
	})
}

func TestIsPDFContentDelimiter(t *testing.T) {
	for _, c := range []byte("()<>{}/%[]") {
		if !isPDFContentDelimiter(c) {
			t.Errorf("expected %q to be a delimiter", c)
		}
	}
	for _, c := range []byte("abcdefghijklmnopqrstuvwxyz0123456789@#$^&*_+=-~`|'.,;?!") {
		if isPDFContentDelimiter(c) {
			t.Errorf("expected %q not to be a delimiter", c)
		}
	}
}

func TestIsPDFContentSpace(t *testing.T) {
	for _, c := range []byte{0, '\t', '\n', '\f', '\r', ' '} {
		if !isPDFContentSpace(c) {
			t.Errorf("expected byte %d to be a space", c)
		}
	}
	for _, c := range []byte("abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ0123456789") {
		if isPDFContentSpace(c) {
			t.Errorf("expected %q not to be a space", c)
		}
	}
}

func TestReadArrayDepth(t *testing.T) {
	t.Run("simple array", func(t *testing.T) {
		content := []byte("[ 1 2 3 ] Do")
		err := interpretPDFContent(content, func(stk *pdfContentStack, op string) error {
			if op == "Do" && stk.Len() > 0 {
				val := stk.Pop()
				if val.Kind() != pdfContentArrayKind {
					t.Errorf("expected array kind, got %v", val.Kind())
				}
				if val.Len() != 3 {
					t.Errorf("expected 3 elements, got %d", val.Len())
				}
			}
			return nil
		})
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
	})

	t.Run("nested arrays", func(t *testing.T) {
		content := []byte("[ [ 1 2 ] [ 3 4 ] ] Do")
		var outer pdfContentValue
		err := interpretPDFContent(content, func(stk *pdfContentStack, op string) error {
			if op == "Do" && stk.Len() > 0 {
				outer = stk.Pop()
			}
			return nil
		})
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if outer.Kind() != pdfContentArrayKind || outer.Len() != 2 {
			t.Fatalf("expected array of 2, got kind=%v len=%d", outer.Kind(), outer.Len())
		}
		inner0 := outer.Index(0)
		if inner0.Kind() != pdfContentArrayKind || inner0.Len() != 2 {
			t.Errorf("expected inner array of 2, got kind=%v len=%d", inner0.Kind(), inner0.Len())
		}
		if inner0.Index(0).i != 1 || inner0.Index(1).i != 2 {
			t.Errorf("expected [1,2], got [%d,%d]", inner0.Index(0).i, inner0.Index(1).i)
		}
	})

	t.Run("unterminated array", func(t *testing.T) {
		content := []byte("[ 1 2 3")
		err := interpretPDFContent(content, func(stk *pdfContentStack, op string) error { return nil })
		if err == nil {
			t.Fatal("expected error for unterminated array")
		}
	})
}

func TestReadObjectDepth_Dict(t *testing.T) {
	t.Run("simple dict", func(t *testing.T) {
		content := []byte("<< /Type /Page /Count 5 >> Do")
		var got pdfContentValue
		err := interpretPDFContent(content, func(stk *pdfContentStack, op string) error {
			if op == "Do" && stk.Len() > 0 {
				got = stk.Pop()
			}
			return nil
		})
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if got.Kind() != pdfContentDictKind {
			t.Fatalf("expected dict kind, got %v", got.Kind())
		}
		if got.dict["Type"].Name() != "Page" {
			t.Errorf("expected Type=Page, got %q", got.dict["Type"].Name())
		}
		if got.dict["Count"].i != 5 {
			t.Errorf("expected Count=5, got %d", got.dict["Count"].i)
		}
	})

	t.Run("nested dict", func(t *testing.T) {
		content := []byte("<< /Inner << /Key /Val >> /Num 42 >> Do")
		var got pdfContentValue
		err := interpretPDFContent(content, func(stk *pdfContentStack, op string) error {
			if op == "Do" && stk.Len() > 0 {
				got = stk.Pop()
			}
			return nil
		})
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if got.Kind() != pdfContentDictKind {
			t.Fatalf("expected dict kind, got %v", got.Kind())
		}
		inner := got.dict["Inner"]
		if inner.Kind() != pdfContentDictKind {
			t.Fatalf("expected nested dict, got %v", inner.Kind())
		}
		if inner.dict["Key"].Name() != "Val" {
			t.Errorf("expected Key=Val, got %q", inner.dict["Key"].Name())
		}
		if got.dict["Num"].i != 42 {
			t.Errorf("expected Num=42, got %d", got.dict["Num"].i)
		}
	})

	t.Run("unterminated dict", func(t *testing.T) {
		content := []byte("<< /Key")
		err := interpretPDFContent(content, func(stk *pdfContentStack, op string) error { return nil })
		if err == nil {
			t.Fatal("expected error for unterminated dict")
		}
	})

	t.Run("dict with non-name key", func(t *testing.T) {
		content := []byte("<< 42 /Val >>")
		p := &pdfContentParser{data: content, pos: 0}
		_, err := p.readObject()
		if err == nil {
			t.Fatal("expected error for non-name dict key")
		}
	})
}

func TestReadObjectDepth_NullBoolReal(t *testing.T) {
	t.Run("null", func(t *testing.T) {
		p := &pdfContentParser{data: []byte("null"), pos: 0}
		val, err := p.readObject()
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if val.Kind() != pdfContentNullKind {
			t.Errorf("expected null kind, got %v", val.Kind())
		}
	})

	t.Run("true false", func(t *testing.T) {
		p := &pdfContentParser{data: []byte("true false"), pos: 0}
		val, err := p.readObject()
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if val.Kind() != pdfContentBoolKind || !val.b {
			t.Errorf("expected true bool, got kind=%v b=%v", val.Kind(), val.b)
		}
		val, err = p.readObject()
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if val.Kind() != pdfContentBoolKind || val.b {
			t.Errorf("expected false bool, got kind=%v b=%v", val.Kind(), val.b)
		}
	})

	t.Run("real", func(t *testing.T) {
		p := &pdfContentParser{data: []byte("3.14"), pos: 0}
		val, err := p.readObject()
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if val.Kind() != pdfContentRealKind {
			t.Errorf("expected real kind, got %v", val.Kind())
		}
		if val.f != 3.14 {
			t.Errorf("expected 3.14, got %f", val.f)
		}
	})

	t.Run("EOF", func(t *testing.T) {
		p := &pdfContentParser{data: []byte(""), pos: 0}
		_, err := p.readObject()
		if err == nil {
			t.Fatal("expected error for empty content")
		}
	})

	t.Run("too deep", func(t *testing.T) {
		var content []byte
		for i := 0; i < maxPDFContentNestingDepth+1; i++ {
			content = append(content, []byte("<< /A ")...)
		}
		for i := 0; i < maxPDFContentNestingDepth+1; i++ {
			content = append(content, []byte(">>")...)
		}
		p := &pdfContentParser{data: content, pos: 0}
		_, err := p.readObject()
		if err == nil {
			t.Fatal("expected error for deeply nested content")
		}
	})
}

func TestReadHexString(t *testing.T) {
	t.Run("simple hex", func(t *testing.T) {
		content := []byte("<48656C6C6F> Tj")
		var got string
		err := interpretPDFContent(content, func(stk *pdfContentStack, op string) error {
			if op == "Tj" && stk.Len() > 0 {
				got = stk.Pop().RawString()
			}
			return nil
		})
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if got != "Hello" {
			t.Errorf("got %q, want %q", got, "Hello")
		}
	})

	t.Run("odd length hex", func(t *testing.T) {
		p := &pdfContentParser{data: []byte("41B>"), pos: 0}
		got, err := p.readHexString()
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		want := "\x41\xb0"
		if got != want {
			t.Errorf("got %q, want %q", got, want)
		}
	})

	t.Run("hex with spaces", func(t *testing.T) {
		p := &pdfContentParser{data: []byte("48 65 6C 6C 6F>"), pos: 0}
		got, err := p.readHexString()
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if got != "Hello" {
			t.Errorf("got %q, want %q", got, "Hello")
		}
	})

	t.Run("empty hex", func(t *testing.T) {
		p := &pdfContentParser{data: []byte(">"), pos: 0}
		got, err := p.readHexString()
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if got != "" {
			t.Errorf("got %q, want empty string", got)
		}
	})

	t.Run("single nibble", func(t *testing.T) {
		p := &pdfContentParser{data: []byte("4>"), pos: 0}
		got, err := p.readHexString()
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		want := "\x40"
		if got != want {
			t.Errorf("got %q, want %q", got, want)
		}
	})

	t.Run("lowercase hex", func(t *testing.T) {
		p := &pdfContentParser{data: []byte("48656c6c6f>"), pos: 0}
		got, err := p.readHexString()
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if got != "Hello" {
			t.Errorf("got %q, want %q", got, "Hello")
		}
	})

	t.Run("unterminated", func(t *testing.T) {
		p := &pdfContentParser{data: []byte("4865"), pos: 0}
		_, err := p.readHexString()
		if err == nil {
			t.Fatal("expected error for unterminated hex string")
		}
	})

	t.Run("malformed", func(t *testing.T) {
		p := &pdfContentParser{data: []byte("4G>"), pos: 0}
		_, err := p.readHexString()
		if err == nil {
			t.Fatal("expected error for malformed hex string")
		}
	})
}

func TestReadToken_UnexpectedDelimiter(t *testing.T) {
	p := &pdfContentParser{data: []byte{'}'}, pos: 0}
	_, err := p.readToken()
	if err == nil {
		t.Fatal("expected error for unexpected delimiter '}'")
	}
}

func TestReadToken_UnmatchedCloseBrackets(t *testing.T) {
	t.Run("unmatched ]", func(t *testing.T) {
		content := []byte("] Tj")
		err := interpretPDFContent(content, func(stk *pdfContentStack, op string) error { return nil })
		if err == nil {
			t.Fatal("expected error for unexpected ']'")
		}
	})

	t.Run("unmatched >>", func(t *testing.T) {
		content := []byte(">> Tj")
		err := interpretPDFContent(content, func(stk *pdfContentStack, op string) error { return nil })
		if err == nil {
			t.Fatal("expected error for unexpected '>>'")
		}
	})
}

func TestPdfContentValue_Accessors(t *testing.T) {
	v := pdfContentValue{}
	if v.Name() != "" {
		t.Errorf("expected empty name for non-name kind, got %q", v.Name())
	}
	if v.RawString() != "" {
		t.Errorf("expected empty string for non-string kind, got %q", v.RawString())
	}
	if v.Len() != 0 {
		t.Errorf("expected 0 len for non-array kind, got %d", v.Len())
	}
	if v.Index(0).Kind() != pdfContentNullKind {
		t.Errorf("expected null kind for out-of-bounds index, got %v", v.Index(0).Kind())
	}
	if v.Index(-1).Kind() != pdfContentNullKind {
		t.Errorf("expected null kind for negative index, got %v", v.Index(-1).Kind())
	}
}

func TestPdfContentStack(t *testing.T) {
	var stk pdfContentStack
	if stk.Len() != 0 {
		t.Errorf("expected empty stack, got len %d", stk.Len())
	}
	v := stk.Pop()
	if v.Kind() != pdfContentNullKind {
		t.Errorf("expected null from empty pop, got %v", v.Kind())
	}
	stk.Push(pdfContentValue{kind: pdfContentIntKind, i: 42})
	stk.Push(pdfContentValue{kind: pdfContentStringKind, s: "hello"})
	if stk.Len() != 2 {
		t.Errorf("expected len 2, got %d", stk.Len())
	}
	v = stk.Pop()
	if v.Kind() != pdfContentStringKind || v.s != "hello" {
		t.Errorf("expected string 'hello', got %v %q", v.Kind(), v.s)
	}
	v = stk.Pop()
	if v.Kind() != pdfContentIntKind || v.i != 42 {
		t.Errorf("expected int 42, got %v %d", v.Kind(), v.i)
	}
}

func TestMatchByte(t *testing.T) {
	p := &pdfContentParser{data: []byte("<hello"), pos: 0}
	if p.matchByte('<') != true {
		t.Error("expected matchByte('<') to return true")
	}
	if p.pos != 1 {
		t.Errorf("expected pos=1 after match, got %d", p.pos)
	}
	if p.matchByte('<') != false {
		t.Error("expected matchByte('<') on 'h' to return false")
	}
	if p.pos != 1 {
		t.Errorf("expected pos unchanged after failed match, got %d", p.pos)
	}
	p = &pdfContentParser{data: []byte(""), pos: 0}
	if p.matchByte('<') != false {
		t.Error("expected matchByte on empty data to return false")
	}
}

func TestReadKeyword(t *testing.T) {
	content := []byte("true false null 42 3.14 gs")
	p := &pdfContentParser{data: content, pos: 0}

	tok, err := p.readKeyword()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if b, ok := tok.(bool); !ok || !b {
		t.Errorf("expected true, got %T %v", tok, tok)
	}

	if err := p.skipWhitespaceAndComments(); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	tok, err = p.readKeyword()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if b, ok := tok.(bool); !ok || b {
		t.Errorf("expected false, got %T %v", tok, tok)
	}

	if err := p.skipWhitespaceAndComments(); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	tok, err = p.readKeyword()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if kw, ok := tok.(pdfContentKeyword); !ok || kw != "null" {
		t.Errorf("expected null keyword, got %T %v", tok, tok)
	}

	if err := p.skipWhitespaceAndComments(); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	tok, err = p.readKeyword()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if i, ok := tok.(int64); !ok || i != 42 {
		t.Errorf("expected int64 42, got %T %v", tok, tok)
	}

	if err := p.skipWhitespaceAndComments(); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	tok, err = p.readKeyword()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if f, ok := tok.(float64); !ok || f != 3.14 {
		t.Errorf("expected float64 3.14, got %T %v", tok, tok)
	}

	if err := p.skipWhitespaceAndComments(); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	tok, err = p.readKeyword()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if kw, ok := tok.(pdfContentKeyword); !ok || kw != "gs" {
		t.Errorf("expected gs keyword, got %T %v", tok, tok)
	}
}

func TestPdfContentStreamEndsMidToken_Additional(t *testing.T) {
	tests := []struct {
		name string
		data []byte
		want bool
	}{
		{"complete literal with nested", []byte("(Hello (World))"), false},
		{"complete hex with spaces", []byte("<48 65 6C 6C 6F>"), false},
		{"trailing token", []byte("BT "), false},
		{"mid-token keyword", []byte("B"), true},
		{"mid-name after /", []byte("/Font"), true},
		{"closed array and dict", []byte("]>"), false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := pdfContentStreamEndsMidToken(tt.data)
			if got != tt.want {
				t.Errorf("pdfContentStreamEndsMidToken(%q) = %v, want %v", tt.data, got, tt.want)
			}
		})
	}
}

func TestInterpretPDFContent_CallbackError(t *testing.T) {
	content := []byte("BT (Hello)Tj ET")
	err := interpretPDFContent(content, func(stk *pdfContentStack, op string) error {
		return fmt.Errorf("callback error")
	})
	if err == nil {
		t.Fatal("expected error from callback")
	}
	if err.Error() != "callback error" {
		t.Errorf("expected 'callback error', got %v", err)
	}
}

func TestReadObjectDepth_UnexpectedKeyword(t *testing.T) {
	content := []byte("]")
	p := &pdfContentParser{data: content, pos: 0}
	_, err := p.readObject()
	if err == nil {
		t.Fatal("expected error for unexpected keyword")
	}
}

func TestReadObjectDepth_NestedArrayInDict(t *testing.T) {
	content := []byte("<< /Arr [ 1 2 3 ] /Key /Val >> Do")
	var got pdfContentValue
	err := interpretPDFContent(content, func(stk *pdfContentStack, op string) error {
		if op == "Do" && stk.Len() > 0 {
			got = stk.Pop()
		}
		return nil
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got.Kind() != pdfContentDictKind {
		t.Fatalf("expected dict, got %v", got.Kind())
	}
	arr := got.dict["Arr"]
	if arr.Kind() != pdfContentArrayKind || arr.Len() != 3 {
		t.Errorf("expected array of 3 elements, got kind=%v len=%d", arr.Kind(), arr.Len())
	}
}

func TestReadDictDepth_DictWithStringKey(t *testing.T) {
	p := &pdfContentParser{data: []byte("<< (key) /Val >>"), pos: 0}
	_, err := p.readObject()
	if err == nil {
		t.Fatal("expected error for string key in dict")
	}
}
