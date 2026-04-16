package main

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func TestParseLaunchArgs(t *testing.T) {
	t.Run("default dir resolves to data", func(t *testing.T) {
		dir := t.TempDir()
		withTestWD(t, dir)
		wd := mustGetwd(t)
		if err := os.Mkdir(filepath.Join(dir, "data"), 0755); err != nil {
			t.Fatalf("Mkdir(data) error = %v", err)
		}

		opts, err := parseLaunchArgs([]string{"codex"})
		if err != nil {
			t.Fatalf("parseLaunchArgs() error = %v", err)
		}
		if opts.Client != "codex" {
			t.Fatalf("client = %q, want codex", opts.Client)
		}
		if opts.WorkspaceDir != wd {
			t.Fatalf("workspace dir = %q, want %q", opts.WorkspaceDir, wd)
		}
		if opts.IndexDir != filepath.Join(wd, "data") {
			t.Fatalf("index dir = %q, want data dir", opts.IndexDir)
		}
	})

	t.Run("missing default data dir", func(t *testing.T) {
		dir := t.TempDir()
		withTestWD(t, dir)

		_, err := parseLaunchArgs([]string{"codex"})
		if err == nil || !strings.Contains(err.Error(), "default data directory") || !strings.Contains(err.Error(), "quant init codex") || !strings.Contains(err.Error(), "--dir .") {
			t.Fatalf("expected actionable missing data dir error, got %v", err)
		}
	})

	t.Run("explicit current dir", func(t *testing.T) {
		dir := t.TempDir()
		withTestWD(t, dir)
		wd := mustGetwd(t)

		opts, err := parseLaunchArgs([]string{"codex", "--dir", "."})
		if err != nil {
			t.Fatalf("parseLaunchArgs() error = %v", err)
		}
		if opts.IndexDir != wd {
			t.Fatalf("index dir = %q, want cwd %q", opts.IndexDir, wd)
		}
	})

	t.Run("client after flags", func(t *testing.T) {
		dir := t.TempDir()
		withTestWD(t, dir)
		wd := mustGetwd(t)

		opts, err := parseLaunchArgs([]string{"--dir", ".", "opencode"})
		if err != nil {
			t.Fatalf("parseLaunchArgs() error = %v", err)
		}
		if opts.Client != "opencode" || opts.IndexDir != wd {
			t.Fatalf("unexpected opts: %+v", opts)
		}
	})

	t.Run("pass through args", func(t *testing.T) {
		dir := t.TempDir()
		withTestWD(t, dir)

		opts, err := parseLaunchArgs([]string{"claude", "--dir", ".", "--", "--permission-mode", "plan"})
		if err != nil {
			t.Fatalf("parseLaunchArgs() error = %v", err)
		}
		want := []string{"--permission-mode", "plan"}
		if !reflect.DeepEqual(opts.ClientArgs, want) {
			t.Fatalf("client args = %v, want %v", opts.ClientArgs, want)
		}
	})

	t.Run("unsupported client", func(t *testing.T) {
		_, err := parseLaunchArgs([]string{"wat"})
		if !errors.Is(err, errLaunchUnsupported) {
			t.Fatalf("expected errLaunchUnsupported, got %v", err)
		}
	})

	t.Run("unknown flag", func(t *testing.T) {
		_, err := parseLaunchArgs([]string{"codex", "--bad"})
		if err == nil {
			t.Fatal("expected unknown flag error")
		}
	})
}

func TestBuildLaunchInvocationAdapters(t *testing.T) {
	restore := stubLaunchDeps(t)
	defer restore()

	opts := launchOptions{
		Client:       "codex",
		WorkspaceDir: filepath.Clean("/tmp/workspace"),
		IndexDir:     filepath.Clean("/tmp/workspace/data"),
		ClientArgs:   []string{"hello"},
	}

	t.Run("codex", func(t *testing.T) {
		inv, err := buildLaunchInvocation(opts)
		if err != nil {
			t.Fatalf("buildLaunchInvocation(codex) error = %v", err)
		}
		want := []string{
			"-C", opts.WorkspaceDir,
			"-c", `mcp_servers.quant.command="/tmp/quant"`,
			"-c", `mcp_servers.quant.args=["mcp", "--dir", "` + opts.IndexDir + `"]`,
			"-c", `mcp_servers.quant.env={QUANT_AUTOUPDATE="true"}`,
			"-c", `mcp_servers.quant.tools."*".approval_mode="approve"`,
			"hello",
		}
		assertInvocation(t, inv, "codex", opts.WorkspaceDir, want)
	})

	t.Run("opencode", func(t *testing.T) {
		opts := opts
		opts.Client = "opencode"
		inv, err := buildLaunchInvocation(opts)
		if err != nil {
			t.Fatalf("buildLaunchInvocation(opencode) error = %v", err)
		}
		assertInvocation(t, inv, "opencode", opts.WorkspaceDir, []string{opts.WorkspaceDir, "hello"})
		var cfg map[string]any
		if err := json.Unmarshal([]byte(inv.Env["OPENCODE_CONFIG_CONTENT"]), &cfg); err != nil {
			t.Fatalf("opencode config JSON error = %v", err)
		}
		quant := cfg["mcp"].(map[string]any)["quant"].(map[string]any)
		if quant["type"] != "local" || quant["enabled"] != true {
			t.Fatalf("unexpected opencode quant config: %+v", quant)
		}
		command := quant["command"].([]any)
		if command[0] != "/tmp/quant" || command[1] != "mcp" || command[3] != opts.IndexDir {
			t.Fatalf("unexpected opencode command: %+v", command)
		}
		env := quant["environment"].(map[string]any)
		if env["QUANT_AUTOUPDATE"] != "true" {
			t.Fatalf("unexpected opencode environment: %+v", env)
		}
		permission := cfg["permission"].(map[string]any)
		if permission["quant_*"] != "allow" {
			t.Fatalf("unexpected opencode permission: %+v", permission)
		}
	})

	t.Run("claude", func(t *testing.T) {
		opts := opts
		opts.Client = "claude"
		inv, err := buildLaunchInvocation(opts)
		if err != nil {
			t.Fatalf("buildLaunchInvocation(claude) error = %v", err)
		}
		assertInvocation(t, inv, "claude", opts.WorkspaceDir, nil)
		if inv.Args[0] != "--mcp-config" || !strings.Contains(inv.Args[1], `"mcpServers"`) || !strings.Contains(inv.Args[1], `"type":"stdio"`) {
			t.Fatalf("unexpected claude args: %v", inv.Args)
		}
		if inv.Args[2] != "--allowedTools" || inv.Args[3] != "mcp__quant__*" {
			t.Fatalf("missing claude quant MCP allow args: %v", inv.Args)
		}
		if inv.Args[len(inv.Args)-1] != "hello" {
			t.Fatalf("missing claude pass-through args: %v", inv.Args)
		}
	})

	t.Run("cursor", func(t *testing.T) {
		opts := opts
		opts.Client = "cursor"
		inv, err := buildLaunchInvocation(opts)
		if err != nil {
			t.Fatalf("buildLaunchInvocation(cursor) error = %v", err)
		}
		assertInvocation(t, inv, "cursor", opts.WorkspaceDir, nil)
		wantPrefix := []string{"--user-data-dir", "/tmp/quant-cursor", "--add-mcp"}
		if !reflect.DeepEqual(inv.Args[:3], wantPrefix) {
			t.Fatalf("cursor args prefix = %v, want %v", inv.Args[:3], wantPrefix)
		}
		if inv.Args[4] != opts.WorkspaceDir || inv.Args[5] != "hello" {
			t.Fatalf("unexpected cursor args: %v", inv.Args)
		}
		var cfg map[string]any
		if err := json.Unmarshal([]byte(inv.Args[3]), &cfg); err != nil {
			t.Fatalf("cursor MCP JSON error = %v", err)
		}
		if cfg["name"] != "quant" || cfg["command"] != "/tmp/quant" {
			t.Fatalf("unexpected cursor config: %+v", cfg)
		}
	})

	t.Run("copilot", func(t *testing.T) {
		opts := opts
		opts.Client = "copilot"
		inv, err := buildLaunchInvocation(opts)
		if err != nil {
			t.Fatalf("buildLaunchInvocation(copilot) error = %v", err)
		}
		assertInvocation(t, inv, "copilot", opts.WorkspaceDir, nil)
		if inv.Args[0] != "--additional-mcp-config" || !strings.Contains(inv.Args[1], `"mcpServers"`) || !strings.Contains(inv.Args[1], `"type":"local"`) {
			t.Fatalf("unexpected copilot args: %v", inv.Args)
		}
		if !strings.Contains(inv.Args[1], `"tools":["*"]`) {
			t.Fatalf("unexpected copilot args: %v", inv.Args)
		}
		if inv.Args[2] != "--allow-tool=quant" {
			t.Fatalf("missing copilot quant MCP allow arg: %v", inv.Args)
		}
		if inv.Args[len(inv.Args)-1] != "hello" {
			t.Fatalf("missing copilot pass-through args: %v", inv.Args)
		}
	})

	t.Run("gemini", func(t *testing.T) {
		opts := opts
		opts.Client = "gemini"
		inv, err := buildLaunchInvocation(opts)
		if err != nil {
			t.Fatalf("buildLaunchInvocation(gemini) error = %v", err)
		}
		defer inv.Cleanup()
		assertInvocation(t, inv, "gemini", opts.WorkspaceDir, []string{"hello"})
		settingsPath := inv.Env["GEMINI_CLI_SYSTEM_SETTINGS_PATH"]
		data, err := os.ReadFile(settingsPath)
		if err != nil {
			t.Fatalf("reading gemini settings: %v", err)
		}
		if !strings.Contains(string(data), `"mcpServers"`) || !strings.Contains(string(data), `"/tmp/quant"`) || !strings.Contains(string(data), opts.IndexDir) {
			t.Fatalf("unexpected gemini settings:\n%s", data)
		}
	})
}

func TestRunLaunchCommandUsesRunnerExitCode(t *testing.T) {
	restoreDeps := stubLaunchDeps(t)
	defer restoreDeps()

	oldRunner := launchRunner
	defer func() { launchRunner = oldRunner }()

	dir := t.TempDir()
	withTestWD(t, dir)
	wd := mustGetwd(t)
	if err := os.Mkdir(filepath.Join(dir, "data"), 0755); err != nil {
		t.Fatalf("Mkdir(data) error = %v", err)
	}

	var got launchInvocation
	launchRunner = func(_ context.Context, inv launchInvocation) (int, error) {
		got = inv
		return 7, nil
	}

	code := runLaunchCommand(context.Background(), []string{"codex", "--", "start prompt"})
	if code != 7 {
		t.Fatalf("runLaunchCommand code = %d, want 7", code)
	}
	if got.Command != "codex" || got.Dir != wd || got.Args[len(got.Args)-1] != "start prompt" {
		t.Fatalf("unexpected invocation: %+v", got)
	}
}

func assertInvocation(t *testing.T, inv launchInvocation, command, dir string, args []string) {
	t.Helper()
	if inv.Command != command {
		t.Fatalf("command = %q, want %q", inv.Command, command)
	}
	if inv.Dir != dir {
		t.Fatalf("dir = %q, want %q", inv.Dir, dir)
	}
	if args != nil && !reflect.DeepEqual(inv.Args, args) {
		t.Fatalf("args = %v, want %v", inv.Args, args)
	}
}

func stubLaunchDeps(t *testing.T) func() {
	t.Helper()
	oldExecutable := launchExecutable
	oldLookPath := launchLookPath
	oldCursorUserDataDir := launchCursorUserDataDir
	launchExecutable = func() (string, error) { return "/tmp/quant", nil }
	launchLookPath = func(name string) (string, error) { return filepath.Join("/usr/bin", name), nil }
	launchCursorUserDataDir = func() (string, error) { return "/tmp/quant-cursor", nil }
	return func() {
		launchExecutable = oldExecutable
		launchLookPath = oldLookPath
		launchCursorUserDataDir = oldCursorUserDataDir
	}
}

func withTestWD(t *testing.T, dir string) {
	t.Helper()
	oldWD, err := os.Getwd()
	if err != nil {
		t.Fatalf("Getwd() error = %v", err)
	}
	if err := os.Chdir(dir); err != nil {
		t.Fatalf("Chdir(%s) error = %v", dir, err)
	}
	t.Cleanup(func() {
		if err := os.Chdir(oldWD); err != nil {
			t.Fatalf("restore Chdir(%s) error = %v", oldWD, err)
		}
	})
}

func mustGetwd(t *testing.T) string {
	t.Helper()
	wd, err := os.Getwd()
	if err != nil {
		t.Fatalf("Getwd() error = %v", err)
	}
	return wd
}
