package index

import (
	"strings"
	"unicode"
)

type QueryAnalysis struct {
	Original       string
	Tokens         []string
	IsIdentifier   bool
	IsNaturalLang  bool
	Intent         QueryIntent
	FileTypeFilter []string
	PathPrefix     string
}

type QueryIntent int

const (
	IntentSearch QueryIntent = iota
	IntentDefinition
	IntentReference
	IntentExploration
)

func AnalyzeQuery(query string) *QueryAnalysis {
	qa := &QueryAnalysis{Original: query}
	tokens := strings.Fields(query)
	qa.Tokens = tokens

	if len(tokens) == 0 {
		return qa
	}

	identifierCount := 0
	for _, tok := range tokens {
		if isIdentifierToken(tok) {
			identifierCount++
		}
	}

	if len(tokens) <= 2 && identifierCount == len(tokens) {
		qa.IsIdentifier = true
		qa.Intent = IntentDefinition
	} else if len(tokens) >= 4 && identifierCount < len(tokens)/2 {
		qa.IsNaturalLang = true
		qa.Intent = IntentSearch
	} else if identifierCount > len(tokens)/2 {
		qa.IsIdentifier = true
		qa.Intent = IntentReference
	} else {
		qa.Intent = IntentExploration
	}

	qa.FileTypeFilter = extractFileTypeFilters(tokens)
	qa.PathPrefix = extractPathPrefix(query)

	return qa
}

func isIdentifierToken(token string) bool {
	hasUpper := false
	hasLower := false
	hasUnderscore := false
	hasDot := false

	for _, r := range token {
		switch {
		case unicode.IsUpper(r):
			hasUpper = true
		case unicode.IsLower(r):
			hasLower = true
		case r == '_':
			hasUnderscore = true
		case r == '.':
			hasDot = true
		}
	}

	return (hasUpper && hasLower) || hasUnderscore || hasDot
}

func extractFileTypeFilters(tokens []string) []string {
	extMap := map[string]string{
		"go": ".go", "python": ".py", "py": ".py",
		"javascript": ".js", "js": ".js", "typescript": ".ts", "ts": ".ts",
		"rust": ".rs", "rs": ".rs", "java": ".java",
		"ruby": ".rb", "rb": ".rb", "cpp": ".cpp", "c": ".c",
		"swift": ".swift", "kotlin": ".kt", "kt": ".kt",
		"markdown": ".md", "md": ".md",
	}

	var filters []string
	seen := make(map[string]bool)
	for _, tok := range tokens {
		lower := strings.ToLower(strings.TrimSuffix(strings.TrimSuffix(tok, ","), "."))
		if ext, ok := extMap[lower]; ok && !seen[ext] {
			seen[ext] = true
			filters = append(filters, ext)
		}
	}
	return filters
}

func extractPathPrefix(query string) string {
	lower := strings.ToLower(query)
	qualifiers := []string{" in ", " from ", " under ", " inside ", " within "}
	for _, q := range qualifiers {
		if idx := strings.LastIndex(lower, q); idx >= 0 {
			prefix := strings.TrimSpace(query[idx+len(q):])
			prefix = strings.TrimSuffix(prefix, ".")
			prefix = strings.TrimSuffix(prefix, ",")
			if prefix != "" && !strings.Contains(prefix, " ") {
				return prefix
			}
		}
	}
	return ""
}

func ExpandQuery(query string) []string {
	expanded := []string{query}
	tokens := strings.Fields(query)

	synonyms := map[string][]string{
		"test":    {"spec", "testing", "unit test", "integration test"},
		"error":   {"err", "failure", "exception", "panic", "fault"},
		"config":  {"configuration", "settings", "options", "prefs"},
		"auth":    {"authentication", "login", "signin", "authorization"},
		"create":  {"new", "add", "insert", "make", "init"},
		"delete":  {"remove", "destroy", "drop", "erase"},
		"update":  {"modify", "change", "edit", "patch", "set"},
		"get":     {"fetch", "retrieve", "find", "read", "load"},
		"start":   {"begin", "launch", "run", "init", "boot"},
		"stop":    {"halt", "end", "terminate", "shutdown", "kill"},
		"connect": {"link", "join", "attach", "bind"},
		"handle":  {"process", "manage", "deal", "serve"},
	}

	for _, tok := range tokens {
		lower := strings.ToLower(tok)
		if syns, ok := synonyms[lower]; ok {
			expanded = append(expanded, syns...)
		}
	}

	return expanded
}
