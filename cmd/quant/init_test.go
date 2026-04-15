package main

import (
	"bufio"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestRunInitCommandHelpAndErrors(t *testing.T) {
	stdout, stderr := captureOutput(t, func() {
		if code := runInitCommand([]string{"--help"}); code != 0 {
			t.Fatalf("runInitCommand(--help) code = %d", code)
		}
	})
	if !strings.Contains(stdout, "quant init [client] [flags]") || stderr != "" {
		t.Fatalf("unexpected help output: stdout=%q stderr=%q", stdout, stderr)
	}

	_, stderr = captureOutput(t, func() {
		if code := runInitCommand([]string{"wat"}); code != 1 {
			t.Fatalf("runInitCommand(unsupported) code = %d", code)
		}
	})
	if !strings.Contains(stderr, "unsupported client") || !strings.Contains(stderr, "opencode, codex, claude") {
		t.Fatalf("unexpected unsupported-client stderr: %q", stderr)
	}

	restore := stubInitPrompt(false, "")
	defer restore()
	stdout, stderr = captureOutput(t, func() {
		if code := runInitCommand(nil); code != 1 {
			t.Fatalf("runInitCommand(no client) code = %d", code)
		}
	})
	if !strings.Contains(stderr, "client is required in non-interactive mode") {
		t.Fatalf("unexpected no-client stderr: %q", stderr)
	}
	if !strings.Contains(stdout, "quant init [client] [flags]") {
		t.Fatalf("expected usage for no-client error, got %q", stdout)
	}
}

func TestParseInitArgsInteractiveClient(t *testing.T) {
	restore := stubInitPrompt(true, " codex ")
	defer restore()

	opts, err := parseInitArgs([]string{"--dir", t.TempDir()})
	if err != nil {
		t.Fatalf("parseInitArgs() error = %v", err)
	}
	if opts.Client != "codex" {
		t.Fatalf("client = %q, want codex", opts.Client)
	}
}

func TestParseInitArgsClientBeforeFlags(t *testing.T) {
	dir := t.TempDir()
	opts, err := parseInitArgs([]string{"opencode", "--dir", dir, "--data-dir", "docs"})
	if err != nil {
		t.Fatalf("parseInitArgs() error = %v", err)
	}
	if opts.Client != "opencode" {
		t.Fatalf("client = %q, want opencode", opts.Client)
	}
	if opts.ProjectDir != dir {
		t.Fatalf("project dir = %q, want %q", opts.ProjectDir, dir)
	}
	if opts.DataDir != "docs" {
		t.Fatalf("data dir = %q, want docs", opts.DataDir)
	}
}

func TestInitProjectCreatesClientFiles(t *testing.T) {
	tests := []struct {
		client     string
		configPath string
		extraPath  string
		configWant []string
	}{
		{client: "opencode", configPath: "opencode.json", configWant: []string{`"command": [`, `"quant"`, `"mcp"`, `"--dir"`, `"./data"`, `"instructions"`}},
		{client: "codex", configPath: filepath.Join(".codex", "config.toml"), configWant: []string{`command = "quant"`, `args = ["mcp", "--dir", "./data"]`, `[mcp_servers.quant.env]`}},
		{client: "claude", configPath: ".mcp.json", extraPath: "CLAUDE.md", configWant: []string{`"mcpServers"`, `"command": "quant"`, `"mcp"`, `"--dir"`, `"./data"`, `"type": "stdio"`}},
		{client: "cursor", configPath: filepath.Join(".cursor", "mcp.json"), configWant: []string{`"mcpServers"`, `"command": "quant"`, `"mcp"`, `"--dir"`, `"./data"`}},
		{client: "copilot", configPath: filepath.Join(".vscode", "mcp.json"), configWant: []string{`"servers"`, `"command": "quant"`, `"mcp"`, `"--dir"`, `"./data"`, `"type": "stdio"`}},
		{client: "gemini", configPath: filepath.Join(".gemini", "settings.json"), configWant: []string{`"contextFileName": "AGENTS.md"`, `"mcpServers"`, `"command": "quant"`, `"mcp"`, `"--dir"`, `"./data"`}},
	}

	for _, tt := range tests {
		t.Run(tt.client, func(t *testing.T) {
			dir := t.TempDir()
			_, err := initProject(initOptions{
				Client:       tt.client,
				ProjectDir:   dir,
				DataDir:      "data",
				QuantCommand: "quant",
				Autoupdate:   true,
			})
			if err != nil {
				t.Fatalf("initProject() error = %v", err)
			}

			assertDirExists(t, filepath.Join(dir, "data"))
			config := readFile(t, filepath.Join(dir, tt.configPath))
			for _, want := range tt.configWant {
				if !strings.Contains(config, want) {
					t.Fatalf("%s missing %q:\n%s", tt.configPath, want, config)
				}
			}

			agents := readFile(t, filepath.Join(dir, "AGENTS.md"))
			if !strings.Contains(agents, "Use the `quant` MCP tool") || !strings.Contains(agents, "Do not read files in the `data/` folder directly") {
				t.Fatalf("unexpected AGENTS.md:\n%s", agents)
			}
			if tt.extraPath != "" {
				extra := readFile(t, filepath.Join(dir, tt.extraPath))
				if !strings.Contains(extra, "@AGENTS.md") {
					t.Fatalf("%s missing AGENTS import: %q", tt.extraPath, extra)
				}
			}
		})
	}
}

func TestInitProjectSkipAndForce(t *testing.T) {
	dir := t.TempDir()
	configPath := filepath.Join(dir, ".codex", "config.toml")
	if err := os.MkdirAll(filepath.Dir(configPath), 0755); err != nil {
		t.Fatalf("MkdirAll() error = %v", err)
	}
	if err := os.WriteFile(configPath, []byte("custom config"), 0644); err != nil {
		t.Fatalf("WriteFile(config) error = %v", err)
	}
	agentsPath := filepath.Join(dir, "AGENTS.md")
	if err := os.WriteFile(agentsPath, []byte("custom agents"), 0644); err != nil {
		t.Fatalf("WriteFile(agents) error = %v", err)
	}

	res, err := initProject(initOptions{
		Client:       "codex",
		ProjectDir:   dir,
		DataDir:      "data",
		QuantCommand: "quant",
		Autoupdate:   true,
	})
	if err != nil {
		t.Fatalf("initProject(skip) error = %v", err)
	}
	if len(res.Skipped) != 2 {
		t.Fatalf("skipped = %v, want two skipped files", res.Skipped)
	}
	if got := readFile(t, configPath); got != "custom config" {
		t.Fatalf("config changed without force: %q", got)
	}
	if got := readFile(t, agentsPath); got != "custom agents" {
		t.Fatalf("AGENTS.md changed without force: %q", got)
	}

	_, err = initProject(initOptions{
		Client:       "codex",
		ProjectDir:   dir,
		DataDir:      "data",
		QuantCommand: "quant",
		Force:        true,
		Autoupdate:   true,
	})
	if err != nil {
		t.Fatalf("initProject(force) error = %v", err)
	}
	if got := readFile(t, configPath); !strings.Contains(got, `[mcp_servers.quant]`) {
		t.Fatalf("config not overwritten with force: %q", got)
	}
	if got := readFile(t, agentsPath); !strings.Contains(got, "# Research Assistant") {
		t.Fatalf("AGENTS.md not overwritten with force: %q", got)
	}
}

func TestInitProjectNoAgents(t *testing.T) {
	dir := t.TempDir()
	_, err := initProject(initOptions{
		Client:       "gemini",
		ProjectDir:   dir,
		DataDir:      "data",
		QuantCommand: "quant",
		NoAgents:     true,
		Autoupdate:   true,
	})
	if err != nil {
		t.Fatalf("initProject() error = %v", err)
	}
	if _, err := os.Stat(filepath.Join(dir, "AGENTS.md")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("AGENTS.md stat error = %v, want not exist", err)
	}
	config := readFile(t, filepath.Join(dir, ".gemini", "settings.json"))
	if strings.Contains(config, "contextFileName") {
		t.Fatalf("gemini config should not include contextFileName with --no-agents:\n%s", config)
	}
}

func TestInitProjectSkills(t *testing.T) {
	tests := []struct {
		client string
		path   string
	}{
		{client: "codex", path: filepath.Join(".agents", "skills", "quant-research", "SKILL.md")},
		{client: "claude", path: filepath.Join(".claude", "skills", "quant-research", "SKILL.md")},
	}
	for _, tt := range tests {
		t.Run(tt.client, func(t *testing.T) {
			dir := t.TempDir()
			_, err := initProject(initOptions{
				Client:       tt.client,
				ProjectDir:   dir,
				DataDir:      "data",
				QuantCommand: "quant",
				Skill:        true,
				Autoupdate:   true,
			})
			if err != nil {
				t.Fatalf("initProject() error = %v", err)
			}
			skill := readFile(t, filepath.Join(dir, tt.path))
			if !strings.Contains(skill, "name: quant-research") {
				t.Fatalf("unexpected skill file:\n%s", skill)
			}
		})
	}

	_, err := initProject(initOptions{
		Client:       "opencode",
		ProjectDir:   t.TempDir(),
		DataDir:      "data",
		QuantCommand: "quant",
		Skill:        true,
		Autoupdate:   true,
	})
	if !errors.Is(err, errInitSkillSupport) {
		t.Fatalf("initProject(opencode skill) error = %v, want errInitSkillSupport", err)
	}
}

func TestInitProjectQuantCommandParts(t *testing.T) {
	dir := t.TempDir()
	_, err := initProject(initOptions{
		Client:       "codex",
		ProjectDir:   dir,
		DataDir:      "data",
		QuantCommand: "go run ./cmd/quant",
		Autoupdate:   true,
	})
	if err != nil {
		t.Fatalf("initProject() error = %v", err)
	}
	config := readFile(t, filepath.Join(dir, ".codex", "config.toml"))
	if !strings.Contains(config, `command = "go"`) || !strings.Contains(config, `args = ["run", "./cmd/quant", "mcp", "--dir", "./data"]`) {
		t.Fatalf("unexpected codex config:\n%s", config)
	}
}

func stubInitPrompt(interactive bool, answer string) func() {
	oldInteractive := initIsInteractive
	oldPrompt := initPrompt
	initIsInteractive = func() bool { return interactive }
	initPrompt = func(*bufio.Reader, string) (string, error) { return answer, nil }
	return func() {
		initIsInteractive = oldInteractive
		initPrompt = oldPrompt
	}
}

func readFile(t *testing.T, path string) string {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("ReadFile(%s) error = %v", path, err)
	}
	return string(data)
}

func assertDirExists(t *testing.T, path string) {
	t.Helper()
	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("Stat(%s) error = %v", path, err)
	}
	if !info.IsDir() {
		t.Fatalf("%s is not a directory", path)
	}
}
