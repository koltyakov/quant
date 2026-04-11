package main

import (
	"fmt"
	"strings"
)

// Version is set at build time via -ldflags.
var Version = "dev"

func init() {
	Version = normalizedVersion(Version)
}

func isVersionRequest(args []string) bool {
	if len(args) == 0 {
		return false
	}

	switch args[0] {
	case "version", "--version", "-v":
		return true
	default:
		return false
	}
}

func isHelpRequest(args []string) bool {
	if len(args) == 0 {
		return false
	}

	switch args[0] {
	case "help", "--help", "-h":
		return true
	default:
		return false
	}
}

func printVersion() {
	fmt.Println("quant", Version)
}

func normalizedVersion(s string) string {
	s = strings.TrimSpace(s)
	if s == "" {
		return "dev"
	}
	if s == "dev" || strings.HasSuffix(s, "-dev") {
		return s
	}
	return ensureVPrefix(s)
}

func ensureVPrefix(s string) string {
	if s != "" && !strings.HasPrefix(s, "v") {
		return "v" + s
	}
	return s
}
