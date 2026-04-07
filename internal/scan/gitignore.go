package scan

import (
	"os"
	"path/filepath"
	"strings"

	ignore "github.com/sabhiram/go-gitignore"
)

// GitIgnoreMatcher applies the root .gitignore plus any nested .gitignore files
// discovered while walking or watching the tree.
type GitIgnoreMatcher struct {
	rootDir  string
	matchers map[string]*ignore.GitIgnore
}

func NewGitIgnoreMatcher(rootDir string, root *ignore.GitIgnore) *GitIgnoreMatcher {
	matchers := make(map[string]*ignore.GitIgnore)
	if root != nil {
		matchers[rootDir] = root
	}

	return &GitIgnoreMatcher{
		rootDir:  rootDir,
		matchers: matchers,
	}
}

func (m *GitIgnoreMatcher) Load(dir string) {
	if dir == m.rootDir {
		return
	}
	if _, ok := m.matchers[dir]; ok {
		return
	}

	nestedPath := filepath.Join(dir, ".gitignore")
	if _, err := os.Stat(nestedPath); err != nil {
		return
	}

	if matcher, err := ignore.CompileIgnoreFile(nestedPath); err == nil {
		m.matchers[dir] = matcher
	}
}

func (m *GitIgnoreMatcher) Matches(path string) bool {
	rel, err := filepath.Rel(m.rootDir, path)
	if err != nil || rel == "." {
		return false
	}
	return matchesAnyGitIgnore(m.matchers, m.rootDir, rel)
}

func IsHiddenName(name string) bool {
	return strings.HasPrefix(name, ".")
}
