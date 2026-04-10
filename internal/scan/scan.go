package scan

import (
	"crypto/sha256"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	ignore "github.com/sabhiram/go-gitignore"
)

type Result struct {
	Path       string
	ModifiedAt time.Time
}

type Visitor func(Result) error

// Scan walks the directory tree, respecting .gitignore files at every level
// and skipping hidden files/directories.
func Scan(dir string, gi *ignore.GitIgnore) ([]Result, error) {
	var results []Result
	err := Walk(dir, gi, func(result Result) error {
		results = append(results, result)
		return nil
	})
	return results, err
}

// Walk streams file results to visit without materializing the full result set.
// It respects .gitignore files at every level and skips hidden files/directories.
func Walk(dir string, gi *ignore.GitIgnore, visit Visitor) error {
	if _, err := os.Stat(dir); err != nil {
		return fmt.Errorf("walking %s: %w", dir, err)
	}

	matcher := NewGitIgnoreMatcher(dir, gi)

	return filepath.WalkDir(dir, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			if d != nil && d.IsDir() {
				return filepath.SkipDir
			}
			return nil
		}

		if d.IsDir() {
			if path == dir {
				return nil
			}
			if IsHiddenName(d.Name()) {
				return filepath.SkipDir
			}
			if matcher.Matches(path) {
				return filepath.SkipDir
			}
			matcher.Load(path)
			return nil
		}

		if IsHiddenName(d.Name()) {
			return nil
		}

		if matcher.Matches(path) {
			return nil
		}

		info, err := d.Info()
		if err != nil {
			return nil
		}

		return visit(Result{
			Path:       path,
			ModifiedAt: info.ModTime(),
		})
	})
}

// matchesAnyGitIgnore checks the path against all applicable .gitignore matchers
// from the root down to the deepest parent directory.
func matchesAnyGitIgnore(matchers map[string]*ignore.GitIgnore, rootDir, relPath string) bool {
	// Check root-level gitignore.
	if gi, ok := matchers[rootDir]; ok && gi.MatchesPath(relPath) {
		return true
	}

	// Check nested gitignore files for each parent directory.
	parts := strings.Split(filepath.Dir(relPath), string(filepath.Separator))
	current := rootDir
	for _, part := range parts {
		if part == "." {
			continue
		}
		current = filepath.Join(current, part)
		gi, ok := matchers[current]
		if !ok {
			continue
		}
		// Compute path relative to the gitignore's directory.
		nestedRel, err := filepath.Rel(current, filepath.Join(rootDir, relPath))
		if err != nil {
			continue
		}
		if gi.MatchesPath(nestedRel) {
			return true
		}
	}

	return false
}

// LoadGitIgnore loads the root .gitignore file if it exists.
func LoadGitIgnore(dir string) (*ignore.GitIgnore, error) {
	gitignorePath := filepath.Join(dir, ".gitignore")
	if _, err := os.Stat(gitignorePath); os.IsNotExist(err) {
		return nil, nil
	}
	return ignore.CompileIgnoreFile(gitignorePath)
}

func FileHash(path string) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer func() { _ = f.Close() }()

	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return "", err
	}
	return fmt.Sprintf("%x", h.Sum(nil)), nil
}
