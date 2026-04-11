package selfupdate

import "testing"

func TestComparePrerelease_Equal(t *testing.T) {
	if got := comparePrerelease("alpha", "alpha"); got != 0 {
		t.Errorf("comparePrerelease(alpha, alpha) = %d, want 0", got)
	}
}

func TestComparePrerelease_Numeric(t *testing.T) {
	if got := comparePrerelease("alpha.1", "alpha.2"); got != -1 {
		t.Errorf("comparePrerelease(alpha.1, alpha.2) = %d, want -1", got)
	}
	if got := comparePrerelease("alpha.2", "alpha.1"); got != 1 {
		t.Errorf("comparePrerelease(alpha.2, alpha.1) = %d, want 1", got)
	}
}

func TestComparePrerelease_Alphabetic(t *testing.T) {
	if got := comparePrerelease("alpha", "beta"); got != -1 {
		t.Errorf("comparePrerelease(alpha, beta) = %d, want -1", got)
	}
	if got := comparePrerelease("beta", "alpha"); got != 1 {
		t.Errorf("comparePrerelease(beta, alpha) = %d, want 1", got)
	}
}

func TestComparePrerelease_MixedNumericAlpha(t *testing.T) {
	if got := comparePrerelease("rc.1", "rc.alpha"); got != -1 {
		t.Errorf("comparePrerelease(rc.1, rc.alpha) = %d, want -1 (numeric < alpha)", got)
	}
	if got := comparePrerelease("rc.alpha", "rc.1"); got != 1 {
		t.Errorf("comparePrerelease(rc.alpha, rc.1) = %d, want 1", got)
	}
}

func TestComparePrerelease_DifferentLengths(t *testing.T) {
	if got := comparePrerelease("alpha", "alpha.1"); got != -1 {
		t.Errorf("comparePrerelease(alpha, alpha.1) = %d, want -1", got)
	}
	if got := comparePrerelease("alpha.1", "alpha"); got != 1 {
		t.Errorf("comparePrerelease(alpha.1, alpha) = %d, want 1", got)
	}
}

func TestIsNewer_Prerelease(t *testing.T) {
	tests := []struct {
		current, latest string
		want            bool
	}{
		{"1.0.0-alpha", "1.0.0-beta", true},
		{"1.0.0-beta", "1.0.0-alpha", false},
		{"1.0.0-alpha", "1.0.0", true},
		{"1.0.0", "1.0.0-alpha", false},
		{"1.0.0-alpha.1", "1.0.0-alpha.2", true},
		{"1.0.0-alpha.2", "1.0.0-alpha.1", false},
	}
	for _, tt := range tests {
		got := isNewer(tt.current, tt.latest)
		if got != tt.want {
			t.Errorf("isNewer(%q, %q) = %v, want %v", tt.current, tt.latest, got, tt.want)
		}
	}
}

func TestGoosToAssetOS(t *testing.T) {
	tests := []struct {
		goos string
		want string
		ok   bool
	}{
		{"darwin", "Darwin", true},
		{"linux", "Linux", true},
		{"windows", "Windows", true},
		{"freebsd", "", false},
	}
	for _, tt := range tests {
		got, err := goosToAssetOS(tt.goos)
		if (err == nil) != tt.ok {
			t.Errorf("goosToAssetOS(%q) err=%v, want ok=%v", tt.goos, err, tt.ok)
		}
		if got != tt.want {
			t.Errorf("goosToAssetOS(%q) = %q, want %q", tt.goos, got, tt.want)
		}
	}
}

func TestGoarchToAssetArch(t *testing.T) {
	tests := []struct {
		goarch string
		want   string
		ok     bool
	}{
		{"amd64", "x86_64", true},
		{"arm64", "arm64", true},
		{"386", "", false},
	}
	for _, tt := range tests {
		got, err := goarchToAssetArch(tt.goarch)
		if (err == nil) != tt.ok {
			t.Errorf("goarchToAssetArch(%q) err=%v, want ok=%v", tt.goarch, err, tt.ok)
		}
		if got != tt.want {
			t.Errorf("goarchToAssetArch(%q) = %q, want %q", tt.goarch, got, tt.want)
		}
	}
}
