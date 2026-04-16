package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

const (
	defaultLaunchDataDir = "data"
	launchServerName     = "quant"
	launchDirMode        = 0700
)

var (
	errLaunchUsage       = errors.New("launch usage")
	errLaunchUnsupported = errors.New("unsupported launch client")

	launchRunner            = runLaunchProcess
	launchExecutable        = os.Executable
	launchLookPath          = exec.LookPath
	launchCursorUserDataDir = newCursorUserDataDir
)

type launchOptions struct {
	Client       string
	IndexDir     string
	WorkspaceDir string
	ClientArgs   []string
}

type launchMCPConfig struct {
	Command string
	Args    []string
	Env     map[string]string
}

type launchInvocation struct {
	Command string
	Args    []string
	Dir     string
	Env     map[string]string
	Cleanup func()
}

func runLaunchCommand(ctx context.Context, args []string) int {
	opts, err := parseLaunchArgs(args)
	if err != nil {
		if errors.Is(err, flag.ErrHelp) {
			printLaunchUsage()
			return 0
		}
		if errors.Is(err, errLaunchUnsupported) {
			fmt.Fprintf(os.Stderr, "error: unsupported client %q\n", opts.Client)
			fmt.Fprintf(os.Stderr, "supported clients: %s\n", strings.Join(supportedInitClients, ", "))
			return 1
		}
		if errors.Is(err, errLaunchUsage) {
			fmt.Fprintln(os.Stderr, "error:", err)
			fmt.Fprintln(os.Stderr)
			printLaunchUsage()
			return 1
		}
		fmt.Fprintln(os.Stderr, "error:", err)
		return 1
	}

	invocation, err := buildLaunchInvocation(opts)
	if err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		return 1
	}
	if invocation.Cleanup != nil {
		defer invocation.Cleanup()
	}

	code, err := launchRunner(ctx, invocation)
	if err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		return 1
	}
	return code
}

func parseLaunchArgs(args []string) (launchOptions, error) {
	opts := launchOptions{
		IndexDir: defaultLaunchDataDir,
	}

	launchArgs, clientArgs := splitLaunchClientArgs(args)

	flagSet := flag.NewFlagSet("quant launch", flag.ContinueOnError)
	flagSet.SetOutput(new(bytes.Buffer))
	flagSet.StringVar(&opts.IndexDir, "dir", opts.IndexDir, "Directory for quant to index")

	clientArg := ""
	parseArgs := launchArgs
	if len(launchArgs) > 0 && !strings.HasPrefix(launchArgs[0], "-") {
		clientArg = launchArgs[0]
		parseArgs = launchArgs[1:]
	}

	if err := flagSet.Parse(parseArgs); err != nil {
		return opts, err
	}

	remaining := flagSet.Args()
	if clientArg != "" {
		if len(remaining) > 0 {
			return opts, fmt.Errorf("%w: expected exactly one client argument", errLaunchUsage)
		}
		opts.Client = strings.ToLower(strings.TrimSpace(clientArg))
	} else {
		if len(remaining) != 1 {
			return opts, fmt.Errorf("%w: expected exactly one client argument", errLaunchUsage)
		}
		opts.Client = strings.ToLower(strings.TrimSpace(remaining[0]))
	}
	opts.ClientArgs = append([]string(nil), clientArgs...)

	if err := normalizeLaunchOptions(&opts); err != nil {
		return opts, err
	}
	return opts, nil
}

func splitLaunchClientArgs(args []string) ([]string, []string) {
	for i, arg := range args {
		if arg == "--" {
			return args[:i], args[i+1:]
		}
	}
	return args, nil
}

func normalizeLaunchOptions(opts *launchOptions) error {
	if !isSupportedInitClient(opts.Client) {
		return fmt.Errorf("%w", errLaunchUnsupported)
	}

	workspaceDir, err := os.Getwd()
	if err != nil {
		return fmt.Errorf("getting current directory: %w", err)
	}
	workspaceDir, err = filepath.Abs(workspaceDir)
	if err != nil {
		return fmt.Errorf("resolving workspace dir: %w", err)
	}
	opts.WorkspaceDir = filepath.Clean(workspaceDir)

	indexDir := strings.TrimSpace(opts.IndexDir)
	if indexDir == "" {
		return fmt.Errorf("dir must not be empty")
	}
	if !filepath.IsAbs(indexDir) {
		indexDir = filepath.Join(opts.WorkspaceDir, indexDir)
	}
	indexDir, err = filepath.Abs(indexDir)
	if err != nil {
		return fmt.Errorf("resolving dir: %w", err)
	}
	indexDir = filepath.Clean(indexDir)
	info, err := os.Stat(indexDir)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) && filepath.Base(indexDir) == defaultLaunchDataDir {
			return fmt.Errorf("default data directory %s does not exist; run 'quant init %s', create ./data, or pass --dir .", indexDir, opts.Client) //nolint:staticcheck // the trailing period is part of the flag example, not sentence punctuation
		}
		return fmt.Errorf("cannot access dir %s: %w", indexDir, err)
	}
	if !info.IsDir() {
		return fmt.Errorf("--dir must be a directory")
	}
	opts.IndexDir = indexDir
	return nil
}

func buildLaunchInvocation(opts launchOptions) (launchInvocation, error) {
	mcp := launchMCPConfig{
		Command: launchQuantCommand(),
		Args:    []string{"mcp", "--dir", opts.IndexDir},
		Env:     map[string]string{"QUANT_AUTOUPDATE": "true"},
	}

	switch opts.Client {
	case "codex":
		return buildCodexLaunchInvocation(opts, mcp)
	case "opencode":
		return buildOpenCodeLaunchInvocation(opts, mcp)
	case "claude":
		return buildClaudeLaunchInvocation(opts, mcp)
	case "cursor":
		return buildCursorLaunchInvocation(opts, mcp)
	case "copilot":
		return buildCopilotLaunchInvocation(opts, mcp)
	case "gemini":
		return buildGeminiLaunchInvocation(opts, mcp)
	default:
		return launchInvocation{}, fmt.Errorf("%w", errLaunchUnsupported)
	}
}

func launchQuantCommand() string {
	path, err := launchExecutable()
	if err != nil || strings.TrimSpace(path) == "" {
		return "quant"
	}
	return path
}

func buildCodexLaunchInvocation(opts launchOptions, mcp launchMCPConfig) (launchInvocation, error) {
	if err := requireLaunchBinary("codex"); err != nil {
		return launchInvocation{}, err
	}
	args := []string{
		"-C", opts.WorkspaceDir,
		"-c", "mcp_servers.quant.command=" + strconv.Quote(mcp.Command),
		"-c", "mcp_servers.quant.args=" + tomlStringArray(mcp.Args),
		"-c", `mcp_servers.quant.env={QUANT_AUTOUPDATE="true"}`,
		"-c", `mcp_servers.quant.tools."*".approval_mode="approve"`,
	}
	args = append(args, opts.ClientArgs...)
	return launchInvocation{Command: "codex", Args: args, Dir: opts.WorkspaceDir}, nil
}

func buildOpenCodeLaunchInvocation(opts launchOptions, mcp launchMCPConfig) (launchInvocation, error) {
	if err := requireLaunchBinary("opencode"); err != nil {
		return launchInvocation{}, err
	}
	cfg := map[string]any{
		"$schema": "https://opencode.ai/config.json",
		"mcp": map[string]any{
			launchServerName: map[string]any{
				"type":        "local",
				"command":     append([]string{mcp.Command}, mcp.Args...),
				"enabled":     true,
				"environment": mcp.Env,
			},
		},
		"permission": map[string]string{
			"quant_*": "allow",
		},
	}
	data, err := json.Marshal(cfg)
	if err != nil {
		return launchInvocation{}, fmt.Errorf("rendering opencode config: %w", err)
	}
	args := append([]string{opts.WorkspaceDir}, opts.ClientArgs...)
	return launchInvocation{
		Command: "opencode",
		Args:    args,
		Dir:     opts.WorkspaceDir,
		Env:     map[string]string{"OPENCODE_CONFIG_CONTENT": string(data)},
	}, nil
}

func buildClaudeLaunchInvocation(opts launchOptions, mcp launchMCPConfig) (launchInvocation, error) {
	if err := requireLaunchBinary("claude"); err != nil {
		return launchInvocation{}, err
	}
	cfg := map[string]any{"mcpServers": map[string]any{launchServerName: launchStdioServerConfig(mcp, true)}}
	data, err := json.Marshal(cfg)
	if err != nil {
		return launchInvocation{}, fmt.Errorf("rendering claude MCP config: %w", err)
	}
	args := append([]string{"--mcp-config", string(data), "--allowedTools", "mcp__quant__*"}, opts.ClientArgs...)
	return launchInvocation{Command: "claude", Args: args, Dir: opts.WorkspaceDir}, nil
}

func buildCopilotLaunchInvocation(opts launchOptions, mcp launchMCPConfig) (launchInvocation, error) {
	if err := requireLaunchBinary("copilot"); err != nil {
		return launchInvocation{}, err
	}
	cfg := map[string]any{"mcpServers": map[string]any{launchServerName: launchCopilotServerConfig(mcp)}}
	data, err := json.Marshal(cfg)
	if err != nil {
		return launchInvocation{}, fmt.Errorf("rendering copilot MCP config: %w", err)
	}
	args := append([]string{"--additional-mcp-config", string(data), "--allow-tool=quant"}, opts.ClientArgs...)
	return launchInvocation{Command: "copilot", Args: args, Dir: opts.WorkspaceDir}, nil
}

func buildCursorLaunchInvocation(opts launchOptions, mcp launchMCPConfig) (launchInvocation, error) {
	if err := requireLaunchBinary("cursor"); err != nil {
		return launchInvocation{}, err
	}
	userDataDir, err := launchCursorUserDataDir()
	if err != nil {
		return launchInvocation{}, err
	}
	cfg := map[string]any{
		"name":    launchServerName,
		"command": mcp.Command,
		"args":    mcp.Args,
		"env":     mcp.Env,
	}
	data, err := json.Marshal(cfg)
	if err != nil {
		return launchInvocation{}, fmt.Errorf("rendering cursor MCP config: %w", err)
	}
	args := []string{"--user-data-dir", userDataDir, "--add-mcp", string(data), opts.WorkspaceDir}
	args = append(args, opts.ClientArgs...)
	return launchInvocation{Command: "cursor", Args: args, Dir: opts.WorkspaceDir}, nil
}

func buildGeminiLaunchInvocation(opts launchOptions, mcp launchMCPConfig) (launchInvocation, error) {
	if err := requireLaunchBinary("gemini"); err != nil {
		return launchInvocation{}, err
	}
	cfg := map[string]any{"mcpServers": map[string]any{launchServerName: launchStdioServerConfig(mcp, false)}}
	data, err := json.MarshalIndent(cfg, "", "  ")
	if err != nil {
		return launchInvocation{}, fmt.Errorf("rendering gemini settings: %w", err)
	}
	file, err := os.CreateTemp("", "quant-gemini-settings-*.json")
	if err != nil {
		return launchInvocation{}, fmt.Errorf("creating temporary gemini settings: %w", err)
	}
	settingsPath := file.Name()
	if _, err := file.Write(data); err != nil {
		_ = file.Close()
		_ = os.Remove(settingsPath)
		return launchInvocation{}, fmt.Errorf("writing temporary gemini settings: %w", err)
	}
	if err := file.Close(); err != nil {
		_ = os.Remove(settingsPath)
		return launchInvocation{}, fmt.Errorf("closing temporary gemini settings: %w", err)
	}
	return launchInvocation{
		Command: "gemini",
		Args:    append([]string(nil), opts.ClientArgs...),
		Dir:     opts.WorkspaceDir,
		Env:     map[string]string{"GEMINI_CLI_SYSTEM_SETTINGS_PATH": settingsPath},
		Cleanup: func() { _ = os.Remove(settingsPath) },
	}, nil
}

func launchStdioServerConfig(mcp launchMCPConfig, includeType bool) map[string]any {
	cfg := map[string]any{
		"command": mcp.Command,
		"args":    mcp.Args,
		"env":     mcp.Env,
	}
	if includeType {
		cfg["type"] = "stdio"
	}
	return cfg
}

func launchCopilotServerConfig(mcp launchMCPConfig) map[string]any {
	return map[string]any{
		"type":    "local",
		"command": mcp.Command,
		"args":    mcp.Args,
		"env":     mcp.Env,
		"tools":   []string{"*"},
	}
}

func requireLaunchBinary(name string) error {
	if _, err := launchLookPath(name); err != nil {
		return fmt.Errorf("%s executable not found on PATH; install %s or use 'quant init %s' for durable MCP config", name, name, name)
	}
	return nil
}

func runLaunchProcess(ctx context.Context, invocation launchInvocation) (int, error) {
	cmd := exec.CommandContext(ctx, invocation.Command, invocation.Args...) //nolint:gosec
	cmd.Dir = invocation.Dir
	cmd.Stdin = os.Stdin
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	cmd.Env = os.Environ()
	for key, value := range invocation.Env {
		cmd.Env = append(cmd.Env, key+"="+value)
	}

	if err := cmd.Run(); err != nil {
		var exitErr *exec.ExitError
		if errors.As(err, &exitErr) {
			return exitErr.ExitCode(), nil
		}
		return 1, err
	}
	return 0, nil
}

func newCursorUserDataDir() (string, error) {
	base, err := os.UserCacheDir()
	if err != nil || strings.TrimSpace(base) == "" {
		base = os.TempDir()
	}
	base = filepath.Join(base, "quant", "launch")
	if err := os.MkdirAll(base, launchDirMode); err != nil {
		return "", fmt.Errorf("creating cursor launch cache: %w", err)
	}
	cleanupOldLaunchDirs(base, "cursor-", 24*time.Hour)
	dir, err := os.MkdirTemp(base, "cursor-")
	if err != nil {
		return "", fmt.Errorf("creating cursor user data dir: %w", err)
	}
	return dir, nil
}

func cleanupOldLaunchDirs(base, prefix string, maxAge time.Duration) {
	entries, err := os.ReadDir(base)
	if err != nil {
		return
	}
	cutoff := time.Now().Add(-maxAge)
	for _, entry := range entries {
		if !entry.IsDir() || !strings.HasPrefix(entry.Name(), prefix) {
			continue
		}
		info, err := entry.Info()
		if err != nil || info.ModTime().After(cutoff) {
			continue
		}
		_ = os.RemoveAll(filepath.Join(base, entry.Name()))
	}
}

func printLaunchUsage() {
	fmt.Println(`Usage:
  quant launch <client> [flags] [-- <client args...>]

Supported clients:
  opencode, codex, claude, cursor, copilot, gemini

Flags:
  --dir <path>    Directory for quant to index (default "./data")

Examples:
  quant launch codex
  quant launch codex --dir .
  quant launch opencode --dir ../docs
  quant launch claude -- --permission-mode plan
  quant launch copilot -- --allow-all-tools`)
}
