package extract

import (
	"context"
	"os"
	"strings"
)

var textExtensions = map[string]bool{
	// Plain text and markup
	".txt": true, ".md": true, ".mdx": true, ".rst": true, ".adoc": true,
	".tex": true, ".latex": true, ".org": true, ".wiki": true,

	// Go
	".go": true, ".mod": true, ".sum": true, ".tmpl": true,

	// Python
	".py": true, ".pyi": true, ".pyx": true, ".pxd": true,

	// JavaScript / TypeScript
	".js": true, ".ts": true, ".jsx": true, ".tsx": true,
	".mjs": true, ".cjs": true, ".mts": true, ".cts": true,

	// Web
	".html": true, ".htm": true, ".css": true, ".scss": true, ".sass": true,
	".less": true, ".vue": true, ".svelte": true, ".astro": true,

	// JVM
	".java": true, ".kt": true, ".kts": true, ".scala": true, ".groovy": true,
	".gradle": true, ".clj": true, ".cljs": true,

	// .NET
	".cs": true, ".fs": true, ".fsx": true, ".vb": true, ".csx": true,

	// C / C++
	".c": true, ".h": true, ".cpp": true, ".cxx": true, ".cc": true,
	".hpp": true, ".hxx": true, ".hh": true, ".inl": true,

	// Systems
	".rs": true, ".zig": true, ".nim": true, ".v": true, ".odin": true,

	// Apple
	".swift": true, ".m": true, ".mm": true,

	// Scripting
	".rb": true, ".php": true, ".pl": true, ".pm": true, ".lua": true,
	".r": true, ".R": true, ".jl": true, ".tcl": true, ".awk": true,
	".sed": true, ".perl": true,

	// Shell
	".sh": true, ".bash": true, ".zsh": true, ".fish": true, ".ps1": true,
	".psm1": true, ".bat": true, ".cmd": true,

	// Functional
	".hs": true, ".ml": true, ".mli": true, ".ex": true, ".exs": true,
	".erl": true, ".hrl": true, ".elm": true, ".purs": true, ".rkt": true,
	".scm": true, ".lisp": true, ".cl": true,

	// Mobile
	".dart": true,

	// Config / Data
	".yaml": true, ".yml": true, ".json": true, ".jsonc": true, ".json5": true,
	".toml": true, ".xml": true, ".xsl": true, ".xslt": true,
	".ini": true, ".cfg": true, ".conf": true, ".env": true, ".properties": true,

	// Markup / Templates
	".graphql": true, ".gql": true, ".proto": true, ".thrift": true,
	".avsc": true, ".avdl": true,

	// Database / Query
	".sql": true, ".prisma": true, ".hql": true, ".cql": true,

	// Infrastructure
	".tf": true, ".hcl": true, ".nix": true, ".dhall": true,
	".dockerfile": true, ".containerfile": true,
	".makefile": true, ".cmake": true, ".just": true,
	".vagrantfile": true,

	// Data
	".csv": true, ".tsv": true, ".log": true,

	// Editor / IDE
	".vim": true, ".el": true, ".sublime-syntax": true,

	// Documentation
	".pod": true, ".man": true, ".roff": true,

	// Misc
	".diff": true, ".patch": true, ".gitignore": true, ".gitattributes": true,
	".editorconfig": true, ".babelrc": true, ".eslintrc": true,
	".prettierrc": true, ".stylelintrc": true,
}

// Special filenames (case-insensitive match).
var textBasenames = map[string]bool{
	"makefile": true, "dockerfile": true, "containerfile": true,
	"rakefile": true, "gemfile": true, "justfile": true,
	"vagrantfile": true, "procfile": true, "brewfile": true,
	"cmakelists.txt": true, "build.gradle": true, "build.sbt": true,
	"package.json": true, "tsconfig.json": true, "cargo.toml": true,
	"go.mod": true, "go.sum": true, "requirements.txt": true,
	"pipfile": true, "setup.py": true, "setup.cfg": true,
	"pyproject.toml": true, "tox.ini": true,
	".gitignore": true, ".gitattributes": true, ".editorconfig": true,
	".dockerignore": true, ".npmignore": true, ".eslintignore": true,
	"license": true, "licence": true, "copying": true, "authors": true,
	"changelog": true, "changes": true, "history": true, "news": true,
	"readme": true, "contributing": true, "todo": true,
}

type TextExtractor struct{}

func (t *TextExtractor) Extract(_ context.Context, path string) (string, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return "", err
	}
	return string(data), nil
}

func (t *TextExtractor) Supports(path string) bool {
	e := ext(path)
	if textExtensions[e] {
		return true
	}
	if textExtensions[strings.ToLower(e)] {
		return true
	}
	base := strings.ToLower(basename(path))
	return textBasenames[base]
}
