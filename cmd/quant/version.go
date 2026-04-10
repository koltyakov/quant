package main

import (
	"fmt"
	"os/exec"
	"strings"
)

// Version is set at build time via -ldflags.
var Version = "dev"

func init() {
	if Version == "dev" {
		if desc, err := exec.Command("git", "describe", "--tags", "--always").Output(); err == nil {
			if v := strings.TrimSpace(string(desc)); v != "" {
				Version = v + "-dev"
			}
		}
	}

	// GoReleaser passes bare semver without the leading v.
	if Version != "dev" && !strings.HasPrefix(Version, "v") {
		Version = "v" + Version
	}
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

func printVersion() {
	fmt.Println("quant", Version)
}
