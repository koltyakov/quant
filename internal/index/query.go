package index

import (
	"path/filepath"
	"strings"
)

var fileTypeExtensions = map[string]string{
	"go": ".go", "python": ".py", "py": ".py",
	"javascript": ".js", "js": ".js", "typescript": ".ts", "ts": ".ts",
	"rust": ".rs", "rs": ".rs", "java": ".java",
	"ruby": ".rb", "rb": ".rb", "cpp": ".cpp", "c": ".c",
	"swift": ".swift", "kotlin": ".kt", "kt": ".kt",
	"markdown": ".md", "md": ".md",
}

var canonicalFileTypes = func() map[string]string {
	types := make(map[string]string, len(fileTypeExtensions))
	for name, ext := range fileTypeExtensions {
		if _, exists := types[ext]; !exists || len(name) > len(types[ext]) {
			types[ext] = name
		}
	}
	return types
}()

// DocumentFileType returns the canonical filter value for a file path.
func DocumentFileType(path string) string {
	ext := strings.ToLower(filepath.Ext(path))
	if fileType := canonicalFileTypes[ext]; fileType != "" {
		return fileType
	}
	return strings.TrimPrefix(ext, ".")
}

// CanonicalFileType normalizes user-facing file type filters to the same
// values stored by DocumentFileType.
func CanonicalFileType(value string) string {
	value = strings.TrimPrefix(strings.ToLower(strings.TrimSpace(value)), ".")
	if ext := fileTypeExtensions[value]; ext != "" {
		return canonicalFileTypes[ext]
	}
	return value
}
