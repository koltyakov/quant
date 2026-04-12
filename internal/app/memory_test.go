package app

import (
	"math"
	"testing"
)

func TestFormatMemoryLimit(t *testing.T) {
	tests := []struct {
		name  string
		value int64
		want  string
	}{
		{name: "bytes", value: 512, want: "512 B"},
		{name: "megabytes", value: 2 << 20, want: "2.0 MB"},
		{name: "gigabytes", value: 2 << 30, want: "2.0 GB"},
		{name: "unlimited", value: math.MaxInt64, want: "unlimited"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := formatMemoryLimit(tt.value); got != tt.want {
				t.Fatalf("formatMemoryLimit(%d) = %q, want %q", tt.value, got, tt.want)
			}
		})
	}
}
