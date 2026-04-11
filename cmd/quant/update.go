package main

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/koltyakov/quant/internal/logx"
	"github.com/koltyakov/quant/internal/selfupdate"
)

const autoUpdateCheckInterval = 30 * time.Minute

var errRestartRequired = errors.New("restart required")

func runUpdateCommand(ctx context.Context, args []string) int {
	if len(args) > 0 {
		if isHelpRequest(args) {
			printUpdateUsage()
			return 0
		}
		fmt.Fprintf(os.Stderr, "error: unexpected arguments for update: %s\n", strings.Join(args, " "))
		printUpdateUsage()
		return 1
	}

	fmt.Printf("Current version: %s\n", Version)
	fmt.Println("Checking for updates...")

	rel, err := selfupdate.Check(ctx, Version)
	if err != nil {
		fmt.Fprintln(os.Stderr, "update check failed:", err)
		return 1
	}
	if rel == nil {
		fmt.Println("Already up to date.")
		return 0
	}

	fmt.Printf("New version available: %s\n", ensureVPrefix(rel.TagName))
	if isInteractiveInput() {
		reader := bufio.NewReader(os.Stdin)
		answer, err := prompt(reader, "Do you want to update? [y/N] ")
		if err != nil {
			fmt.Fprintln(os.Stderr, "error:", err)
			return 1
		}
		answer = strings.ToLower(strings.TrimSpace(answer))
		if answer != "y" && answer != "yes" {
			fmt.Println("Update cancelled.")
			return 0
		}
	}

	fmt.Println("Downloading...")
	res, err := selfupdate.Apply(ctx, rel)
	if err != nil {
		fmt.Fprintln(os.Stderr, "update failed:", err)
		return 1
	}

	fmt.Printf("Updated to %s (%s)\n", ensureVPrefix(res.LatestVersion), res.AssetName)
	return 0
}

func isAutoUpdateEnabled() bool {
	v := strings.ToLower(strings.TrimSpace(os.Getenv("QUANT_AUTOUPDATE")))
	return v == "true" || v == "1" || v == "yes"
}

func autoUpdateOnStart(ctx context.Context, currentVersion string) bool {
	if currentVersion == "" || currentVersion == "dev" || strings.HasSuffix(currentVersion, "-dev") {
		return false
	}

	logx.Info("auto-update check starting", "current_version", currentVersion)
	result, err := selfupdate.CheckAndApply(ctx, currentVersion)
	if err != nil {
		logx.Warn("auto-update check failed", "err", err)
		return false
	}
	if !result.Updated {
		logx.Info("auto-update already up to date", "current_version", currentVersion)
		return false
	}

	logx.Info("auto-update applied", "from", result.CurrentVersion, "to", ensureVPrefix(result.LatestVersion), "asset", result.AssetName)
	return true
}

func startAutoUpdateLoop(ctx context.Context, currentVersion string, onUpdate func()) {
	if currentVersion == "" || currentVersion == "dev" || strings.HasSuffix(currentVersion, "-dev") {
		return
	}

	logx.Info("auto-update periodic checks enabled", "interval", autoUpdateCheckInterval)
	ticker := time.NewTicker(autoUpdateCheckInterval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			result, err := selfupdate.CheckAndApply(ctx, currentVersion)
			if err != nil {
				logx.Warn("auto-update periodic check failed", "err", err)
				continue
			}
			if result.Updated {
				logx.Info("auto-update periodic check applied update", "from", result.CurrentVersion, "to", ensureVPrefix(result.LatestVersion))
				onUpdate()
				return
			}
			logx.Info("auto-update periodic check passed", "status", "up to date")
		}
	}
}

func restartProcess() int {
	logx.Info("auto-update restarting process")
	if err := selfupdate.Restart(); err != nil {
		fmt.Fprintln(os.Stderr, "auto-update: restart failed:", err)
		return 1
	}
	return 0
}

func printUpdateUsage() {
	fmt.Println("Usage:")
	fmt.Println("  quant update")
	fmt.Println()
	fmt.Println("Checks GitHub Releases for a newer quant binary, downloads it, and replaces the current executable.")
}
