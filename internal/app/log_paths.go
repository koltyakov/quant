package app

import (
	"path/filepath"
	"strings"
)

func LogPathForDB(dbPath string) string {
	ext := filepath.Ext(dbPath)
	if ext == "" {
		return dbPath + ".log"
	}
	return strings.TrimSuffix(dbPath, ext) + ".log"
}

func IsCompanionLogPathForDB(dbPath, path string) bool {
	base := filepath.Clean(LogPathForDB(dbPath))
	path = filepath.Clean(path)
	if path == base {
		return true
	}
	if !strings.HasPrefix(path, base+".") {
		return false
	}
	suffix := strings.TrimPrefix(path, base+".")
	if suffix == "" {
		return false
	}
	for _, r := range suffix {
		if r < '0' || r > '9' {
			return false
		}
	}
	return true
}
