package index

import (
	"regexp"
	"sort"
	"strings"
)

var ftsTokenPattern = regexp.MustCompile(`[\pL\pN_]+`)

var ftsOperatorPattern = regexp.MustCompile(`\b(AND|OR|NOT|NEAR)\b`)

func ftsSanitizePhrase(phrase string) string {
	return ftsOperatorPattern.ReplaceAllString(phrase, "")
}

// camelCasePattern matches camelCase identifiers (at least one lowercase letter followed by an uppercase).
var camelCasePattern = regexp.MustCompile(`[a-z][A-Z]`)

// splitIdentifier expands a camelCase or snake_case identifier into its component words.
// Returns nil if the token is neither camelCase nor snake_case (no expansion needed).
func splitIdentifier(token string) []string {
	isCamel := camelCasePattern.MatchString(token)
	isSnake := strings.Contains(token, "_") && token != "_"
	if !isCamel && !isSnake {
		return nil
	}

	var words []string
	if isSnake {
		for _, part := range strings.Split(token, "_") {
			if part != "" {
				words = append(words, strings.ToLower(part))
			}
		}
	} else {
		// camelCase: split before each uppercase letter that follows a lowercase.
		var current []rune
		runes := []rune(token)
		for i, r := range runes {
			if i > 0 && r >= 'A' && r <= 'Z' && runes[i-1] >= 'a' && runes[i-1] <= 'z' {
				if len(current) > 0 {
					words = append(words, strings.ToLower(string(current)))
				}
				current = []rune{r}
			} else {
				current = append(current, r)
			}
		}
		if len(current) > 0 {
			words = append(words, strings.ToLower(string(current)))
		}
	}
	if len(words) <= 1 {
		return nil
	}
	return words
}

// buildFTSQueries converts a natural-language query into AND, OR, and NEAR FTS5 queries.
// The AND query requires all terms to match (tighter); the OR query matches any term.
// The NEAR query (non-empty for 2-4 raw tokens) adds a proximity bonus within 10 tokens.
// Identifier expansion: camelCase and snake_case tokens are expanded so that
// "getUserName" also matches chunks containing the individual words.
// Prefix matching: the last bare token gets a "*" suffix for autocomplete-style matching.
func buildFTSQueries(query string) (andQuery, orQuery, nearQuery string) {
	// Extract quoted phrases first and sanitize them for FTS5.
	var phrases []string
	remaining := query
	for {
		start := strings.Index(remaining, `"`)
		if start == -1 {
			break
		}
		end := strings.Index(remaining[start+1:], `"`)
		if end == -1 {
			break
		}
		phrase := strings.TrimSpace(remaining[start+1 : start+1+end])
		if phrase != "" {
			phrase = ftsSanitizePhrase(phrase)
			phrases = append(phrases, `"`+phrase+`"`)
		}
		remaining = remaining[:start] + " " + remaining[start+1+end+1:]
	}

	// Tokenize the remaining (non-phrase) text.
	// Extract original-case tokens for identifier detection, then lowercase for FTS.
	originalMatches := ftsTokenPattern.FindAllString(remaining, -1)
	rawMatches := make([]string, len(originalMatches))
	for i, m := range originalMatches {
		rawMatches[i] = strings.ToLower(m)
	}

	seen := make(map[string]bool, len(rawMatches)+len(phrases))
	var tokens []string
	// originalByLower maps lowercased token back to original case for identifier detection.
	originalByLower := make(map[string]string, len(rawMatches))
	for i, token := range rawMatches {
		originalByLower[token] = originalMatches[i]
	}

	for _, phrase := range phrases {
		if !seen[phrase] {
			seen[phrase] = true
			tokens = append(tokens, phrase)
		}
	}

	for _, token := range rawMatches {
		if seen[token] {
			continue
		}
		seen[token] = true
		tokens = append(tokens, token)
	}

	if len(tokens) == 0 {
		return "", "", ""
	}

	// NEAR query: built from raw bare tokens only (no phrases, no expanded tokens).
	// Only valid when there are 2-4 simple terms; FTS5 NEAR() does not support phrases.
	var bareTokens []string
	for _, t := range tokens {
		if t[0] != '"' {
			bareTokens = append(bareTokens, t)
		}
	}
	if len(bareTokens) >= 2 && len(bareTokens) <= 4 {
		nearQuery = "NEAR(" + strings.Join(bareTokens, " ") + ", 10)"
	}

	// Prefix matching: add lastToken* variant for the last bare token.
	lastToken := tokens[len(tokens)-1]
	if len(lastToken) > 0 && lastToken[0] != '"' {
		prefixToken := lastToken + "*"
		if !seen[prefixToken] {
			seen[prefixToken] = true
			tokens = append(tokens, prefixToken)
		}
	}

	// Identifier expansion: for each camelCase/snake_case token, add its component
	// words as an OR expansion. Keep the original as a quoted phrase for exact matches.
	var expandedTokens []string
	for _, token := range tokens {
		expandedTokens = append(expandedTokens, token)
		if token[0] == '"' {
			continue
		}
		// Strip trailing * before checking for identifier expansion.
		// Use the original case (if available) for camelCase detection.
		bare := strings.TrimSuffix(token, "*")
		originalBare := originalByLower[bare]
		if originalBare == "" {
			originalBare = bare
		}
		parts := splitIdentifier(originalBare)
		if len(parts) == 0 {
			continue
		}
		// Add the original as a quoted phrase for exact match.
		quoted := `"` + bare + `"`
		if !seen[quoted] {
			seen[quoted] = true
			expandedTokens = append(expandedTokens, quoted)
		}
		// Add each component word.
		for _, part := range parts {
			if !seen[part] {
				seen[part] = true
				expandedTokens = append(expandedTokens, part)
			}
		}
	}
	tokens = expandedTokens

	sort.Strings(tokens)
	orQuery = strings.Join(tokens, " OR ")
	if len(tokens) == 1 {
		return orQuery, orQuery, nearQuery
	}
	andQuery = strings.Join(tokens, " AND ")
	return andQuery, orQuery, nearQuery
}
