package main

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"io"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/koltyakov/quant/internal/selfupdate"
)

func TestRunAndRunMCPCommand(t *testing.T) {
	oldVersion := Version
	Version = "v1.2.3"
	defer func() { Version = oldVersion }()

	stdout, stderr := captureOutput(t, func() {
		if code := run(nil); code != 0 {
			t.Fatalf("run(help) code = %d", code)
		}
	})
	if !strings.Contains(stdout, "quant - filesystem-backed MCP index") {
		t.Fatalf("expected help output, got %q", stdout)
	}
	if stderr != "" {
		t.Fatalf("unexpected stderr for help: %q", stderr)
	}

	stdout, stderr = captureOutput(t, func() {
		if code := run([]string{"version"}); code != 0 {
			t.Fatalf("run(version) code = %d", code)
		}
	})
	if !strings.Contains(stdout, "quant v1.2.3") || stderr != "" {
		t.Fatalf("unexpected version output: stdout=%q stderr=%q", stdout, stderr)
	}

	stdout, stderr = captureOutput(t, func() {
		if code := run([]string{"wat"}); code != 1 {
			t.Fatalf("run(unknown) code = %d", code)
		}
	})
	if !strings.Contains(stderr, "error: unknown command") || !strings.Contains(stdout, "quant - filesystem-backed MCP index") {
		t.Fatalf("unexpected unknown command output: stdout=%q stderr=%q", stdout, stderr)
	}

	stdout, stderr = captureOutput(t, func() {
		if code := runMCPCommand([]string{"--help"}); code != 0 {
			t.Fatalf("runMCPCommand(--help) code = %d", code)
		}
	})
	if !strings.Contains(stdout, "quant mcp [flags]") || stderr != "" {
		t.Fatalf("unexpected mcp help output: stdout=%q stderr=%q", stdout, stderr)
	}

	_, stderr = captureOutput(t, func() {
		if code := runMCPCommand([]string{"--bad-flag"}); code != 1 {
			t.Fatalf("runMCPCommand(--bad-flag) code = %d", code)
		}
	})
	if !strings.Contains(stderr, "error:") {
		t.Fatalf("expected parse error, got %q", stderr)
	}

	stdout, stderr = captureOutput(t, func() {
		if code := run([]string{"launch", "--help"}); code != 0 {
			t.Fatalf("run(launch --help) code = %d", code)
		}
	})
	if !strings.Contains(stdout, "quant launch <client>") || stderr != "" {
		t.Fatalf("unexpected launch help output: stdout=%q stderr=%q", stdout, stderr)
	}
}

func TestTerminalPromptHelpers(t *testing.T) {
	temp, err := os.CreateTemp(t.TempDir(), "stdin")
	if err != nil {
		t.Fatalf("CreateTemp returned error: %v", err)
	}
	defer func() { _ = temp.Close() }()

	oldStdin := os.Stdin
	os.Stdin = temp
	defer func() { os.Stdin = oldStdin }()

	if isInteractiveInput() {
		t.Fatal("regular file stdin should not be treated as interactive")
	}

	stdout, _ := captureOutput(t, func() {
		answer, err := prompt(bufio.NewReader(strings.NewReader(" yes \n")), "Confirm? ")
		if err != nil {
			t.Fatalf("prompt returned error: %v", err)
		}
		if answer != "yes" {
			t.Fatalf("unexpected prompt answer: %q", answer)
		}
	})
	if stdout != "Confirm? " {
		t.Fatalf("unexpected prompt output: %q", stdout)
	}
}

func TestRunUpdateCommandAndUpdateHelpers(t *testing.T) {
	ctx := context.Background()
	oldVersion := Version
	Version = "v1.0.0"
	defer func() { Version = oldVersion }()

	restore := stubUpdateHooks()
	defer restore()

	updateCheck = func(context.Context, string) (*selfupdate.Release, error) {
		return nil, errors.New("boom")
	}
	_, stderr := captureOutput(t, func() {
		if code := runUpdateCommand(ctx, nil); code != 1 {
			t.Fatalf("runUpdateCommand(check error) code = %d", code)
		}
	})
	if !strings.Contains(stderr, "update check failed: boom") {
		t.Fatalf("unexpected stderr: %q", stderr)
	}

	updateCheck = func(context.Context, string) (*selfupdate.Release, error) {
		return nil, nil
	}
	stdout, stderr := captureOutput(t, func() {
		if code := runUpdateCommand(ctx, nil); code != 0 {
			t.Fatalf("runUpdateCommand(no update) code = %d", code)
		}
	})
	if !strings.Contains(stdout, "Already up to date.") || stderr != "" {
		t.Fatalf("unexpected up-to-date output: stdout=%q stderr=%q", stdout, stderr)
	}

	updateCheck = func(context.Context, string) (*selfupdate.Release, error) {
		return &selfupdate.Release{TagName: "v1.1.0"}, nil
	}
	updateApply = func(context.Context, *selfupdate.Release) (*selfupdate.Result, error) {
		return &selfupdate.Result{LatestVersion: "1.1.0", AssetName: "quant.tar.gz"}, nil
	}
	updateIsInteractive = func() bool { return false }
	stdout, stderr = captureOutput(t, func() {
		if code := runUpdateCommand(ctx, nil); code != 0 {
			t.Fatalf("runUpdateCommand(success) code = %d", code)
		}
	})
	if !strings.Contains(stdout, "Updated to v1.1.0 (quant.tar.gz)") || stderr != "" {
		t.Fatalf("unexpected successful update output: stdout=%q stderr=%q", stdout, stderr)
	}

	updateIsInteractive = func() bool { return true }
	updatePrompt = func(*bufio.Reader, string) (string, error) { return "n", nil }
	stdout, stderr = captureOutput(t, func() {
		if code := runUpdateCommand(ctx, nil); code != 0 {
			t.Fatalf("runUpdateCommand(cancel) code = %d", code)
		}
	})
	if !strings.Contains(stdout, "Update cancelled.") || stderr != "" {
		t.Fatalf("unexpected cancelled update output: stdout=%q stderr=%q", stdout, stderr)
	}

	updatePrompt = func(*bufio.Reader, string) (string, error) { return "", errors.New("read failed") }
	_, stderr = captureOutput(t, func() {
		if code := runUpdateCommand(ctx, nil); code != 1 {
			t.Fatalf("runUpdateCommand(prompt error) code = %d", code)
		}
	})
	if !strings.Contains(stderr, "error: read failed") {
		t.Fatalf("unexpected prompt stderr: %q", stderr)
	}

	updateCheck = func(context.Context, string) (*selfupdate.Release, error) {
		return &selfupdate.Release{TagName: "v1.1.0"}, nil
	}
	updateIsInteractive = func() bool { return false }
	updateApply = func(context.Context, *selfupdate.Release) (*selfupdate.Result, error) {
		return nil, errors.New("apply failed")
	}
	_, stderr = captureOutput(t, func() {
		if code := runUpdateCommand(ctx, nil); code != 1 {
			t.Fatalf("runUpdateCommand(apply error) code = %d", code)
		}
	})
	if !strings.Contains(stderr, "update failed: apply failed") {
		t.Fatalf("unexpected apply stderr: %q", stderr)
	}

	for _, tc := range []struct {
		value string
		want  bool
	}{
		{"true", true},
		{"1", true},
		{"yes", true},
		{"no", false},
	} {
		t.Setenv("QUANT_AUTOUPDATE", tc.value)
		if got := isAutoUpdateEnabled(); got != tc.want {
			t.Fatalf("isAutoUpdateEnabled(%q) = %v want %v", tc.value, got, tc.want)
		}
	}

	updateCheckAndApply = func(context.Context, string) (*selfupdate.Result, error) {
		return nil, errors.New("check failed")
	}
	if autoUpdateOnStart(ctx, "v1.0.0") {
		t.Fatal("expected failed auto update check to return false")
	}
	if autoUpdateOnStart(ctx, "dev") {
		t.Fatal("dev builds should skip auto update")
	}

	updateCheckAndApply = func(context.Context, string) (*selfupdate.Result, error) {
		return &selfupdate.Result{CurrentVersion: "v1.0.0", Updated: false}, nil
	}
	if autoUpdateOnStart(ctx, "v1.0.0") {
		t.Fatal("already-up-to-date should return false")
	}

	updateCheckAndApply = func(context.Context, string) (*selfupdate.Result, error) {
		return &selfupdate.Result{CurrentVersion: "v1.0.0", LatestVersion: "1.1.0", AssetName: "quant.tar.gz", Updated: true}, nil
	}
	if !autoUpdateOnStart(ctx, "v1.0.0") {
		t.Fatal("successful auto update should return true")
	}

	oldInterval := autoUpdateCheckInterval
	autoUpdateCheckInterval = 10 * time.Millisecond
	defer func() { autoUpdateCheckInterval = oldInterval }()

	loopCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	updates := make(chan struct{}, 1)
	updateCheckAndApply = func(context.Context, string) (*selfupdate.Result, error) {
		return &selfupdate.Result{CurrentVersion: "v1.0.0", LatestVersion: "1.1.0", Updated: true}, nil
	}
	go startAutoUpdateLoop(loopCtx, "v1.0.0", func() { updates <- struct{}{} })
	select {
	case <-updates:
	case <-time.After(500 * time.Millisecond):
		t.Fatal("timed out waiting for auto update loop callback")
	}

	updateRestart = func() error { return nil }
	if code := restartProcess(); code != 0 {
		t.Fatalf("restartProcess success code = %d", code)
	}
	updateRestart = func() error { return errors.New("restart failed") }
	_, stderr = captureOutput(t, func() {
		if code := restartProcess(); code != 1 {
			t.Fatalf("restartProcess failure code = %d", code)
		}
	})
	if !strings.Contains(stderr, "auto-update: restart failed: restart failed") {
		t.Fatalf("unexpected restart stderr: %q", stderr)
	}

	stdout, _ = captureOutput(t, printUpdateUsage)
	if !strings.Contains(stdout, "quant update") {
		t.Fatalf("unexpected update usage: %q", stdout)
	}
}

func stubUpdateHooks() func() {
	oldCheck := updateCheck
	oldApply := updateApply
	oldCheckAndApply := updateCheckAndApply
	oldRestart := updateRestart
	oldInteractive := updateIsInteractive
	oldPrompt := updatePrompt

	return func() {
		updateCheck = oldCheck
		updateApply = oldApply
		updateCheckAndApply = oldCheckAndApply
		updateRestart = oldRestart
		updateIsInteractive = oldInteractive
		updatePrompt = oldPrompt
	}
}

func captureOutput(t *testing.T, fn func()) (stdout string, stderr string) {
	t.Helper()

	oldStdout := os.Stdout
	oldStderr := os.Stderr

	stdoutR, stdoutW, err := os.Pipe()
	if err != nil {
		t.Fatalf("stdout pipe: %v", err)
	}
	stderrR, stderrW, err := os.Pipe()
	if err != nil {
		t.Fatalf("stderr pipe: %v", err)
	}

	os.Stdout = stdoutW
	os.Stderr = stderrW

	outCh := make(chan string, 1)
	errCh := make(chan string, 1)
	go func() {
		var buf bytes.Buffer
		_, _ = io.Copy(&buf, stdoutR)
		outCh <- buf.String()
	}()
	go func() {
		var buf bytes.Buffer
		_, _ = io.Copy(&buf, stderrR)
		errCh <- buf.String()
	}()

	fn()

	_ = stdoutW.Close()
	_ = stderrW.Close()
	os.Stdout = oldStdout
	os.Stderr = oldStderr

	return <-outCh, <-errCh
}
