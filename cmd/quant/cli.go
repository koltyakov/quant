package main

import (
	"bytes"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/andrew/quant/internal/config"
)

func run(args []string) int {
	command, commandArgs := resolveCommand(args)

	switch command {
	case "mcp":
		return runMCPCommand(commandArgs)
	case "version":
		printVersion()
		return 0
	case "help":
		printUsage()
		return 0
	case "mcp-help":
		printMCPUsage()
		return 0
	default:
		fmt.Fprintf(os.Stderr, "error: unknown command %q\n\n", args[0])
		printUsage()
		return 1
	}
}

func resolveCommand(args []string) (string, []string) {
	if len(args) == 0 {
		return "help", nil
	}
	if isVersionRequest(args) {
		return "version", nil
	}
	if isHelpRequest(args) {
		if len(args) > 1 && args[1] == "mcp" {
			return "mcp-help", nil
		}
		return "help", nil
	}

	switch args[0] {
	case "mcp":
		if len(args) > 1 && isHelpRequest(args[1:]) {
			return "mcp-help", nil
		}
		return "mcp", args[1:]
	default:
		return "unknown", nil
	}
}

func runMCPCommand(args []string) int {
	cfg, err := config.ParseArgs(args)
	if err != nil {
		if errors.Is(err, flag.ErrHelp) {
			printMCPUsage()
			return 0
		}
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		return 1
	}

	if err := runMCP(cfg); err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		return 1
	}

	return 0
}

func printUsage() {
	fmt.Println(`quant - filesystem-backed MCP index

Usage:
  quant mcp [flags]      Start the MCP server
  quant version          Print version
  quant help             Show help

Run 'quant mcp --help' for MCP flags.`)
}

func printMCPUsage() {
	fmt.Println("Usage:")
	fmt.Println("  quant mcp [flags]")
	fmt.Println()
	fmt.Println("Flags:")
	printMCPDefaults(os.Stdout)
}

func printMCPDefaults(w io.Writer) {
	flagSet, _ := config.NewFlagSet("quant mcp")
	var buf bytes.Buffer
	flagSet.SetOutput(&buf)
	flagSet.PrintDefaults()

	for _, line := range strings.SplitAfter(buf.String(), "\n") {
		if strings.HasPrefix(line, "  -") {
			line = "  --" + strings.TrimPrefix(line, "  -")
		}
		_, _ = io.WriteString(w, line)
	}
}
