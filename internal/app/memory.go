package app

import (
	"fmt"
	"math"
	"os"
	"runtime/debug"
	"strings"

	"github.com/koltyakov/quant/internal/logx"
)

const defaultGoMemoryLimitBytes int64 = 2 << 30

func configureProcessMemory() {
	if limit := strings.TrimSpace(os.Getenv("GOMEMLIMIT")); limit != "" {
		logx.Info("using Go memory limit from environment", "gomemlimit", limit)
		return
	}

	previous := debug.SetMemoryLimit(defaultGoMemoryLimitBytes)
	logx.Info(
		"configured default Go memory limit",
		"limit", formatMemoryLimit(defaultGoMemoryLimitBytes),
		"previous", formatMemoryLimit(previous),
	)
}

func reclaimProcessMemory() {
	debug.FreeOSMemory()
}

func formatMemoryLimit(n int64) string {
	if n >= math.MaxInt64/2 {
		return "unlimited"
	}
	const unit = 1024
	if n < unit {
		return fmt.Sprintf("%d B", n)
	}
	div, exp := int64(unit), 0
	for v := n / unit; v >= unit; v /= unit {
		div *= unit
		exp++
	}
	return fmt.Sprintf("%.1f %cB", float64(n)/float64(div), "KMGTPE"[exp])
}
