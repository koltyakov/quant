//go:build darwin

package config

import "golang.org/x/sys/unix"

func totalMemory() uint64 {
	val, err := unix.SysctlUint64("hw.memsize")
	if err != nil {
		return 0
	}
	return val
}
