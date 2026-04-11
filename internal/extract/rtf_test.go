package extract

import (
	"context"
	"testing"
)

func backgroundCtx() context.Context { return context.Background() }

func TestHexToByte(t *testing.T) {
	tests := []struct {
		input string
		want  byte
	}{
		{"00", 0x00},
		{"ff", 0xFF},
		{"41", 0x41},
		{"7f", 0x7F},
		{"AB", 0xAB},
		{"cd", 0xCD},
		{"", 0},
		{"a", 0},
		{"xy", 0},
	}
	for _, tt := range tests {
		got := hexToByte(tt.input)
		if got != tt.want {
			t.Errorf("hexToByte(%q) = 0x%02X, want 0x%02X", tt.input, got, tt.want)
		}
	}
}

func TestHexDigit(t *testing.T) {
	tests := []struct {
		input byte
		want  byte
	}{
		{'0', 0},
		{'9', 9},
		{'a', 10},
		{'f', 15},
		{'A', 10},
		{'F', 15},
		{'g', 0},
		{'z', 0},
	}
	for _, tt := range tests {
		got := hexDigit(tt.input)
		if got != tt.want {
			t.Errorf("hexDigit(%c) = %d, want %d", tt.input, got, tt.want)
		}
	}
}

func TestExtractRTFText_HexEscapes(t *testing.T) {
	ctx := backgroundCtx()
	text, err := extractRTFText(ctx, `{\rtf1\ansi Hello \'41\'42\'43}`)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if text != "Hello ABC" {
		t.Fatalf("unexpected RTF hex extraction: %q", text)
	}
}

func TestExtractRTFText_SkipsFontTable(t *testing.T) {
	ctx := backgroundCtx()
	text, err := extractRTFText(ctx, `{\rtf1\ansi{\fonttbl\f0 Arial;}Hello}`)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if text != "Hello" {
		t.Fatalf("expected font table skipped, got %q", text)
	}
}

func TestExtractRTFText_EscapedBraces(t *testing.T) {
	ctx := backgroundCtx()
	text, err := extractRTFText(ctx, `{\rtf1\ansi a\{b\}c}`)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if text != "a{b}c" {
		t.Fatalf("expected escaped braces, got %q", text)
	}
}

func TestExtractRTFText_SpecialCharacters(t *testing.T) {
	ctx := backgroundCtx()
	text, err := extractRTFText(ctx, `{\rtf1\ansi \lquote\rquote\ldblquote\rdblquote\endash\emdash\bullet}`)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	want := "\u2018\u2019\u201C\u201D\u2013\u2014\u2022"
	if text != want {
		t.Fatalf("expected %q, got %q", want, text)
	}
}
