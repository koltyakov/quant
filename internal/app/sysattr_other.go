//go:build !linux && !darwin

package app

import "os/exec"

// detachProcess is a no-op on platforms where process group detachment
// is not supported or not needed.
func detachProcess(_ *exec.Cmd) {}
