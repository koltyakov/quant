package config

import "testing"

func TestPathMatcher_ShouldIndex(t *testing.T) {
	tests := []struct {
		name     string
		matcher  *PathMatcher
		path     string
		expected bool
	}{
		{
			name:     "nil matcher allows all",
			matcher:  nil,
			path:     "any/path/file.go",
			expected: true,
		},
		{
			name:     "empty patterns allow all",
			matcher:  &PathMatcher{},
			path:     "any/path/file.go",
			expected: true,
		},
		{
			name: "include pattern matches",
			matcher: &PathMatcher{
				IncludePatterns: []string{"*.go"},
			},
			path:     "main.go",
			expected: true,
		},
		{
			name: "include pattern rejects non-matching",
			matcher: &PathMatcher{
				IncludePatterns: []string{"*.go"},
			},
			path:     "readme.md",
			expected: false,
		},
		{
			name: "exclude pattern rejects matching",
			matcher: &PathMatcher{
				ExcludePatterns: []string{"*.log"},
			},
			path:     "server.log",
			expected: false,
		},
		{
			name: "exclude pattern allows non-matching",
			matcher: &PathMatcher{
				ExcludePatterns: []string{"*.log"},
			},
			path:     "main.go",
			expected: true,
		},
		{
			name: "double star matches subdirectories",
			matcher: &PathMatcher{
				ExcludePatterns: []string{"node_modules/**"},
			},
			path:     "node_modules/lodash/index.js",
			expected: false,
		},
		{
			name: "double star allows other paths",
			matcher: &PathMatcher{
				ExcludePatterns: []string{"node_modules/**"},
			},
			path:     "src/index.js",
			expected: true,
		},
		{
			name: "directory name exclusion",
			matcher: &PathMatcher{
				ExcludePatterns: []string{".git"},
			},
			path:     ".git/config",
			expected: false,
		},
		{
			name: "nested double star pattern",
			matcher: &PathMatcher{
				ExcludePatterns: []string{"**/vendor/**"},
			},
			path:     "pkg/vendor/lib/file.go",
			expected: false,
		},
		{
			name: "include and exclude combined",
			matcher: &PathMatcher{
				IncludePatterns: []string{"*.go", "*.ts"},
				ExcludePatterns: []string{"*_test.go"},
			},
			path:     "main.go",
			expected: true,
		},
		{
			name: "include and exclude - test file rejected",
			matcher: &PathMatcher{
				IncludePatterns: []string{"*.go", "*.ts"},
				ExcludePatterns: []string{"*_test.go"},
			},
			path:     "main_test.go",
			expected: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := tt.matcher.ShouldIndex(tt.path)
			if result != tt.expected {
				t.Errorf("ShouldIndex(%q) = %v, want %v", tt.path, result, tt.expected)
			}
		})
	}
}

func TestDefaultPathMatcher(t *testing.T) {
	m := DefaultPathMatcher()

	tests := []struct {
		path     string
		expected bool
	}{
		{"src/main.go", true},
		{".git/config", false},
		{".git/objects/abc", false},
		{"node_modules/lodash/index.js", false},
		{"vendor/github.com/pkg/errors/errors.go", false},
		{"dist/bundle.js", false},
		{"build/output/main", false},
		{".DS_Store", false},
		{"main.pyc", false},
		{"__pycache__/module.cpython-39.pyc", false},
		{"logs/app.log", true},
		{".quarantine/books/sample.txt", false},
		{".quarantine/books/sample.txt.log", false},
		{"src/app.ts", true},
		{"README.md", true},
	}

	for _, tt := range tests {
		t.Run(tt.path, func(t *testing.T) {
			result := m.ShouldIndex(tt.path)
			if result != tt.expected {
				t.Errorf("ShouldIndex(%q) = %v, want %v", tt.path, result, tt.expected)
			}
		})
	}
}

func TestPathMatcher_AddPatterns(t *testing.T) {
	m := &PathMatcher{}

	m.AddInclude("*.go")
	m.AddExclude("vendor/**")

	if len(m.IncludePatterns) != 1 || m.IncludePatterns[0] != "*.go" {
		t.Errorf("AddInclude failed: got %v", m.IncludePatterns)
	}
	if len(m.ExcludePatterns) != 1 || m.ExcludePatterns[0] != "vendor/**" {
		t.Errorf("AddExclude failed: got %v", m.ExcludePatterns)
	}
}

func TestPathMatcher_Merge(t *testing.T) {
	m1 := &PathMatcher{
		IncludePatterns: []string{"*.go"},
		ExcludePatterns: []string{"vendor/**"},
	}
	m2 := &PathMatcher{
		IncludePatterns: []string{"*.ts"},
		ExcludePatterns: []string{"node_modules/**"},
	}

	m1.Merge(m2)

	if len(m1.IncludePatterns) != 2 {
		t.Errorf("Merge include failed: got %v", m1.IncludePatterns)
	}
	if len(m1.ExcludePatterns) != 2 {
		t.Errorf("Merge exclude failed: got %v", m1.ExcludePatterns)
	}
}
