//go:build !darwin && !linux

package config

func totalMemory() uint64 {
	return 0
}
