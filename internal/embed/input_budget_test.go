package embed

import (
	"strings"
	"testing"
	"unicode/utf8"
)

// referenceInputPrefix preserves the pre-optimization behavior for comparison.
func referenceInputPrefix(text string, budget int) (string, int) {
	if budget <= 0 || utf8.RuneCountInString(text) <= budget {
		return text, utf8.RuneCountInString(text)
	}
	runes := []rune(text)
	windowStart := max(budget-256, 0)
	window := string(runes[windowStart:budget])
	for _, marker := range []string{"\n\n", "\n", ". ", "! ", "? ", "; ", ", ", " "} {
		if idx := strings.LastIndex(window, marker); idx >= 0 {
			consumed := windowStart + utf8.RuneCountInString(window[:idx]) + len(marker)
			candidate := strings.TrimSpace(string(runes[:consumed]))
			if utf8.RuneCountInString(candidate) >= budget/2 {
				return candidate, consumed
			}
		}
	}
	return strings.TrimSpace(string(runes[:budget])), budget
}

func FuzzPrefixWithinInputBudgetMatchesReference(f *testing.F) {
	f.Add("", 0)
	f.Add("  a\xffé  ", -1)
	f.Add("  a\xffé  ", 7) // Exactly at the budget: preserve the original bytes.
	f.Add(" αβ𐐷\n\n日本語. next sentence ", 24)
	f.Add(" \x80\xffé. 日本語\xed\xa0\x80tail", 10)
	f.Add("alpha beta gamma. \xffsuffix", 18)
	f.Add(strings.Repeat(" ", 25)+"a. "+strings.Repeat("z", 50), 40)
	f.Add(strings.Repeat("é", 264)+". "+strings.Repeat("日", 400), 520)
	f.Add(strings.Repeat("é", 263)+". "+strings.Repeat("日", 400), 520)
	f.Add(strings.Repeat("résultats d'un calcul. PDFtextwithoutspaces ", 3200), MaxInputRunes)
	f.Add(strings.Repeat("日𐐷", 64_000), MaxInputRunes)

	f.Fuzz(func(t *testing.T, text string, budget int) {
		want, wantConsumed := referenceInputPrefix(text, budget)
		got, consumed := PrefixWithinInputBudget(text, budget)
		if got != want || consumed != wantConsumed {
			t.Fatalf("budget %d: prefix or consumed runes differ: got %d bytes/%d runes, want %d bytes/%d runes",
				budget, len(got), consumed, len(want), wantConsumed)
		}
	})
}

func BenchmarkPrefixWithinInputBudgetLong(b *testing.B) {
	input := strings.Repeat("résultats d'un calcul. PDFtextwithoutspaces ", 3200)
	b.Run("reference", func(b *testing.B) {
		b.ReportAllocs()
		for b.Loop() {
			_, _ = referenceInputPrefix(input, MaxInputRunes)
		}
	})
	b.Run("bounded", func(b *testing.B) {
		b.ReportAllocs()
		for b.Loop() {
			_, _ = PrefixWithinInputBudget(input, MaxInputRunes)
		}
	})
}
