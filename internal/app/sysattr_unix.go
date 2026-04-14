//go:build linux || darwin

package app

import (
	"os/exec"
	"syscall"
)

// detachProcess puts cmd in its own process group so that terminal signals
// (e.g. Ctrl+C / SIGINT sent to the foreground group) do not reach it.
func detachProcess(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
}
