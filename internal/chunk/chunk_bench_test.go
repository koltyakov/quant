package chunk

import (
	"strings"
	"testing"
)

func BenchmarkSplit_Short(b *testing.B) {
	text := "The quick brown fox jumps over the lazy dog. " +
		strings.Repeat("This is a sentence for benchmarking purposes. ", 20)

	b.ResetTimer()
	for b.Loop() {
		Split(text, 512, 0.15)
	}
}

func BenchmarkSplit_Medium(b *testing.B) {
	// ~5000 words across mixed paragraphs and headings.
	var sb strings.Builder
	for i := range 50 {
		if i%10 == 0 {
			sb.WriteString("## Section Heading\n\n")
		}
		sb.WriteString("This is paragraph number ")
		sb.WriteString(strings.Repeat("word ", 100))
		sb.WriteString(".\n\n")
	}
	text := sb.String()

	b.ResetTimer()
	for b.Loop() {
		Split(text, 512, 0.15)
	}
}

func BenchmarkSplit_LargeCodeBlock(b *testing.B) {
	var sb strings.Builder
	sb.WriteString("# Code Example\n\n```go\n")
	for range 200 {
		sb.WriteString("func doWork(i int) int { return i * 2 }\n")
	}
	sb.WriteString("```\n\n")
	sb.WriteString("Some trailing explanation text with more words to fill things up.\n")
	text := sb.String()

	b.ResetTimer()
	for b.Loop() {
		Split(text, 512, 0.15)
	}
}

func BenchmarkSplitSentences(b *testing.B) {
	text := "Dr. Smith went to Washington D.C. on Jan. 5th. " +
		"He met with Prof. Jones at 3.14 p.m. to discuss the U.S. policy. " +
		"The meeting was productive! Was it worth it? Yes, absolutely. " +
		strings.Repeat("Another sentence here. ", 50)

	b.ResetTimer()
	for b.Loop() {
		splitSentences(text)
	}
}
