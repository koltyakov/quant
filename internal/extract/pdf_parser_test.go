package extract

import (
	"testing"
)

func TestFromHex(t *testing.T) {
	tests := []struct {
		input byte
		want  int
	}{
		{'0', 0},
		{'5', 5},
		{'9', 9},
		{'a', 10},
		{'f', 15},
		{'A', 10},
		{'F', 15},
		{'g', -1},
		{'z', -1},
		{'G', -1},
		{'\x00', -1},
		{' ', -1},
	}
	for _, tt := range tests {
		got := fromHex(tt.input)
		if got != tt.want {
			t.Errorf("fromHex(%q) = %d, want %d", tt.input, got, tt.want)
		}
	}
}

func TestInterpretPDFContent_ReadDictDepth(t *testing.T) {
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
}

func TestInterpretPDFContent_ReadDictDepth_Nested(t *testing.T) {
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
		t.Fatalf("expected nested dict kind, got %v", inner.Kind())
	}
	if inner.dict["Key"].Name() != "Val" {
		t.Errorf("expected nested Key=Val, got %q", inner.dict["Key"].Name())
	}
	if got.dict["Num"].i != 42 {
		t.Errorf("expected Num=42, got %d", got.dict["Num"].i)
	}
}

func TestInterpretPDFContent_ReadHexString(t *testing.T) {
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
		t.Fatalf("expected 'Hello', got %q", got)
	}
}

func TestInterpretPDFContent_ReadHexString_OddLength(t *testing.T) {
	content := []byte("<41B> Tj")

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
	if got != "A\xB0" {
		t.Fatalf("expected 'A\\xB0', got %q", got)
	}
}

func TestInterpretPDFContent_MatchByte(t *testing.T) {
	content := []byte("<< /A /B >> Do <48656C6C6F> Tj")

	var dictVal pdfContentValue
	var hexStr string
	err := interpretPDFContent(content, func(stk *pdfContentStack, op string) error {
		switch op {
		case "Do":
			if stk.Len() > 0 {
				dictVal = stk.Pop()
			}
		case "Tj":
			if stk.Len() > 0 {
				hexStr = stk.Pop().RawString()
			}
		}
		return nil
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if dictVal.Kind() != pdfContentDictKind {
		t.Errorf("expected dict, got %v", dictVal.Kind())
	}
	if hexStr != "Hello" {
		t.Errorf("expected 'Hello', got %q", hexStr)
	}
}

func TestPDFContentStreamEndsMidToken_EdgeCases(t *testing.T) {
	tests := []struct {
		name string
		data []byte
		want bool
	}{
		{"empty", []byte{}, false},
		{"complete operator", []byte("/GS0 gs\n"), false},
		{"whitespace only", []byte("  \n"), false},
		{"mid literal", []byte("(Hello"), true},
		{"mid hex", []byte("<4865"), true},
		{"mid comment", []byte("% comment"), true},
		{"mid array", []byte("[ 1 2"), true},
		{"mid dict", []byte("<< /Key"), true},
		{"mid name", []byte("/Partial"), true},
		{"open array", []byte("0 Tw [  "), true},
		{"nested literal", []byte("(Hello (world)"), true},
		{"complete literal", []byte("(Hello)"), false},
		{"complete hex", []byte("<4865>"), false},
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
