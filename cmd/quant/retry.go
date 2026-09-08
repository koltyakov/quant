package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"

	"github.com/koltyakov/quant/internal/app"
	"github.com/koltyakov/quant/internal/config"
)

func parseRetryArgs(args []string) (*config.Config, string, error) {
	fs := flag.NewFlagSet("quant retry", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	path := fs.String("path", "", "Single quarantined file, relative to --dir")
	fs.String("dir", "", "Directory to index")
	fs.String("db", "", "Path to SQLite database")
	fs.String("config", "", "Path to YAML config file")
	if err := fs.Parse(args); err != nil {
		return nil, "", err
	}
	if fs.NArg() != 0 {
		return nil, "", fmt.Errorf("unexpected arguments; use --path <path> for a single file")
	}
	var configArgs []string
	pathSet := false
	fs.Visit(func(f *flag.Flag) {
		if f.Name == "path" {
			pathSet = true
			return
		}
		configArgs = append(configArgs, "--"+f.Name, f.Value.String())
	})
	if pathSet && *path == "" {
		return nil, "", fmt.Errorf("--path must not be empty; omit it to retry all quarantined files")
	}
	cfg, err := config.ParseArgs(configArgs)
	return cfg, *path, err
}

func runRetryCommand(ctx context.Context, args []string) int {
	cfg, path, err := parseRetryArgs(args)
	if errors.Is(err, flag.ErrHelp) {
		printRetryUsage()
		return 0
	}
	if err == nil {
		err = app.RetryQuarantined(ctx, cfg, path)
	}
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		return 1
	}
	fmt.Fprintln(os.Stdout, "Quarantine cleared. Start quant mcp with the same --dir/--db/--config settings to rescan and retry; no indexing was performed by this command.")
	return 0
}

func printRetryUsage() {
	fmt.Println(`Usage:
  quant retry [--path <path>] [--dir <dir>] [--db <db>] [--config <yaml>]

Clear one file's quarantine, or all quarantine entries when --path is omitted.
Paths are relative to the configured watch directory; absolute paths inside it
are also accepted. A single path must already be quarantined.
Uses the same config and QUANT_* environment conventions as quant mcp.

Stop all quant MCP instances for this database first, including workers that
could become primary. A running primary's lock makes this command fail safely.
Instances started without locking cannot be detected and must also be stopped.
Then start quant mcp with the same settings to rescan. This command does not
index files or delete .index. Files that still fail may be quarantined again.`)
}
