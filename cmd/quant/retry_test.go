package main

import (
	"os"
	"path/filepath"
	"testing"
)

func TestParseRetryArgs(t *testing.T) {
	root := t.TempDir()
	t.Setenv("QUANT_DIR", root)
	t.Setenv("QUANT_DB", filepath.Join(root, "env.db"))
	cfg, path, err := parseRetryArgs([]string{"--path", "sub/a.txt"})
	if err != nil || cfg.WatchDir != root || cfg.DBPath != filepath.Join(root, "env.db") || path != "sub/a.txt" {
		t.Fatalf("cfg = %+v, path = %q, err = %v", cfg, path, err)
	}
	yaml := filepath.Join(root, "quant.yaml")
	if err := os.WriteFile(yaml, []byte("dir: .\ndb: file.db\n"), 0600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("QUANT_DIR", "")
	t.Setenv("QUANT_DB", "")
	cfg, path, err = parseRetryArgs([]string{"--config", yaml})
	if err != nil || cfg.WatchDir != root || cfg.DBPath != filepath.Join(root, "file.db") || path != "" {
		t.Fatalf("cfg = %+v, path = %q, err = %v", cfg, path, err)
	}
	cfg, _, err = parseRetryArgs([]string{"--config", yaml, "--db", filepath.Join(root, "cli.db")})
	if err != nil || cfg.DBPath != filepath.Join(root, "cli.db") {
		t.Fatalf("CLI override: cfg = %+v, err = %v", cfg, err)
	}
	for _, args := range [][]string{{"--path="}, {"a.txt"}, {"--unknown"}, {"--path"}} {
		if _, _, err := parseRetryArgs(args); err == nil {
			t.Fatalf("expected error for %v", args)
		}
	}
}

func TestRetryCommandRouting(t *testing.T) {
	for _, args := range [][]string{{"retry", "--help"}, {"help", "retry"}} {
		command, _ := resolveCommand(args)
		if command != "retry-help" {
			t.Fatalf("resolveCommand(%v) = %q", args, command)
		}
	}
	command, args := resolveCommand([]string{"retry", "--path", "a.txt"})
	if command != "retry" || len(args) != 2 {
		t.Fatalf("route = %q, %v", command, args)
	}
}
