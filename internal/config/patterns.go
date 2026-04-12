package config

import (
	"path/filepath"
	"strings"
)

// PathMatcher determines whether a file path should be indexed based on
// include and exclude patterns. Patterns use glob syntax.
type PathMatcher struct {
	// IncludePatterns specifies patterns that paths must match to be indexed.
	// If empty, all paths are included by default.
	IncludePatterns []string

	// ExcludePatterns specifies patterns that cause paths to be excluded.
	// Exclusions are applied after inclusions.
	ExcludePatterns []string
}

// DefaultPathMatcher returns a matcher with sensible defaults for code indexing.
func DefaultPathMatcher() *PathMatcher {
	return &PathMatcher{
		IncludePatterns: nil, // include all by default
		ExcludePatterns: []string{
			// Version control
			".git",
			".git/**",
			".svn/**",
			".hg/**",

			// Dependencies
			"node_modules/**",
			"vendor/**",
			"**/vendor/**",

			// Build artifacts
			"dist/**",
			"build/**",
			"target/**",
			"out/**",
			"bin/**",

			// IDE and editor
			".idea/**",
			".vscode/**",
			"*.swp",
			"*.swo",
			"*~",

			// OS files
			".DS_Store",
			"Thumbs.db",

			// Compiled files
			"*.pyc",
			"*.pyo",
			"__pycache__/**",
			"*.class",
			"*.o",
			"*.so",
			"*.dylib",

			// Logs and temp
			"*.log",
			"tmp/**",
			"temp/**",
			".quarantine",
			".quarantine/**",

			// Coverage and test artifacts
			"coverage/**",
			".coverage",
			"htmlcov/**",
		},
	}
}

// ShouldIndex returns true if the given path should be indexed.
// The path should be relative to the watch directory.
func (m *PathMatcher) ShouldIndex(relPath string) bool {
	if m == nil {
		return true
	}

	// Normalize path separators for consistent matching
	relPath = filepath.ToSlash(relPath)

	// Check inclusions first
	if len(m.IncludePatterns) > 0 {
		included := false
		for _, pattern := range m.IncludePatterns {
			if matchPattern(pattern, relPath) {
				included = true
				break
			}
		}
		if !included {
			return false
		}
	}

	// Check exclusions
	for _, pattern := range m.ExcludePatterns {
		if matchPattern(pattern, relPath) {
			return false
		}
	}

	return true
}

// matchPattern checks if a path matches a glob pattern.
// It supports ** for recursive directory matching.
func matchPattern(pattern, path string) bool {
	// Normalize separators
	pattern = filepath.ToSlash(pattern)
	path = filepath.ToSlash(path)

	// Handle ** patterns (recursive match)
	if strings.Contains(pattern, "**") {
		return matchDoubleStarPattern(pattern, path)
	}

	// Simple glob match
	matched, _ := filepath.Match(pattern, path)
	if matched {
		return true
	}

	// Also check against each path component for patterns like ".git"
	parts := strings.Split(path, "/")
	for _, part := range parts {
		if matched, _ := filepath.Match(pattern, part); matched {
			return true
		}
	}

	return false
}

// matchDoubleStarPattern handles patterns containing **.
// Supports patterns like:
//   - "dir/**" - matches anything under dir
//   - "**/file" - matches file in any directory
//   - "**/dir/**" - matches anything under dir at any level
func matchDoubleStarPattern(pattern, path string) bool {
	pattern = filepath.ToSlash(pattern)
	path = filepath.ToSlash(path)

	// Split pattern into segments
	patternParts := strings.Split(pattern, "/")
	pathParts := strings.Split(path, "/")

	return matchPartsRecursive(patternParts, pathParts)
}

// matchPartsRecursive recursively matches pattern parts against path parts.
func matchPartsRecursive(patternParts, pathParts []string) bool {
	pi := 0 // pattern index
	pa := 0 // path index

	for pi < len(patternParts) {
		if pi >= len(patternParts) {
			break
		}

		patternPart := patternParts[pi]

		if patternPart == "**" {
			// ** at the end matches everything
			if pi == len(patternParts)-1 {
				return true
			}

			// Try matching remaining pattern against all suffixes of path
			for i := pa; i <= len(pathParts); i++ {
				if matchPartsRecursive(patternParts[pi+1:], pathParts[i:]) {
					return true
				}
			}
			return false
		}

		// No more path parts but we have pattern parts (not **)
		if pa >= len(pathParts) {
			return false
		}

		// Try to match current pattern part against current path part
		matched, _ := filepath.Match(patternPart, pathParts[pa])
		if !matched {
			return false
		}

		pi++
		pa++
	}

	// Pattern exhausted - path must also be exhausted for a match
	return pa >= len(pathParts)
}

// AddInclude adds an include pattern.
func (m *PathMatcher) AddInclude(pattern string) {
	m.IncludePatterns = append(m.IncludePatterns, pattern)
}

// AddExclude adds an exclude pattern.
func (m *PathMatcher) AddExclude(pattern string) {
	m.ExcludePatterns = append(m.ExcludePatterns, pattern)
}

// Merge combines another PathMatcher's patterns into this one.
func (m *PathMatcher) Merge(other *PathMatcher) {
	if other == nil {
		return
	}
	m.IncludePatterns = append(m.IncludePatterns, other.IncludePatterns...)
	m.ExcludePatterns = append(m.ExcludePatterns, other.ExcludePatterns...)
}
