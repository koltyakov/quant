package main

import (
	"bytes"
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/signal"
	"strings"
	"syscall"

	"github.com/koltyakov/quant/internal/app"
	"github.com/koltyakov/quant/internal/config"
)

func run(args []string) int {
	ctx, cancel := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer cancel()

	command, commandArgs := resolveCommand(args)

	switch command {
	case "mcp":
		return runMCPCommand(commandArgs)
	case "init":
		return runInitCommand(commandArgs)
	case "launch":
		return runLaunchCommand(ctx, commandArgs)
	case "update":
		return runUpdateCommand(ctx, commandArgs)
	case "version":
		printVersion()
		return 0
	case "help":
		printUsage()
		return 0
	case "mcp-help":
		printMCPUsage()
		return 0
	case "init-help":
		printInitUsage()
		return 0
	case "launch-help":
		printLaunchUsage()
		return 0
	case "update-help":
		printUpdateUsage()
		return 0
	default:
		fmt.Fprintln(os.Stderr, "error: unknown command")
		fmt.Fprintln(os.Stderr)
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
		if len(args) > 1 && args[1] == "init" {
			return "init-help", nil
		}
		if len(args) > 1 && args[1] == "launch" {
			return "launch-help", nil
		}
		if len(args) > 1 && args[1] == "update" {
			return "update-help", nil
		}
		return "help", nil
	}

	switch args[0] {
	case "mcp":
		if len(args) > 1 && isHelpRequest(args[1:]) {
			return "mcp-help", nil
		}
		return "mcp", args[1:]
	case "init":
		if len(args) > 1 && isHelpRequest(args[1:]) {
			return "init-help", nil
		}
		return "init", args[1:]
	case "launch":
		if len(args) > 1 && isHelpRequest(args[1:]) {
			return "launch-help", nil
		}
		return "launch", args[1:]
	case "update":
		if len(args) > 1 && isHelpRequest(args[1:]) {
			return "update-help", nil
		}
		return "update", args[1:]
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
		if errors.Is(err, errRestartRequired) || errors.Is(err, app.ErrRestartRequired) {
			return restartProcess()
		}
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		return 1
	}

	return 0
}

func printUsage() {
	fmt.Println(`quant - filesystem-backed MCP index

Usage:
  quant mcp [flags]      Start the MCP server
  quant init [client]    Scaffold a project MCP config
  quant launch <client>  Launch an agent with quant MCP for this session
  quant update           Update to the latest release
  quant version          Print version
  quant help             Show help

Run 'quant mcp --help' for MCP flags.
Run 'quant init --help' for init flags.
Run 'quant launch --help' for launch flags.`)
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
