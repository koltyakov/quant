package selfupdate

import "strings"

// IsNewer reports whether latest is a newer semver than current.
func IsNewer(current, latest string) bool {
	return isNewer(current, latest)
}

func isNewer(current, latest string) bool {
	cp := parseSemver(current)
	lp := parseSemver(latest)
	if cp == nil || lp == nil {
		return latest > current
	}
	if lp[0] != cp[0] {
		return lp[0] > cp[0]
	}
	if lp[1] != cp[1] {
		return lp[1] > cp[1]
	}
	return lp[2] > cp[2]
}

func parseSemver(v string) []int {
	parts := strings.SplitN(v, ".", 3)
	if len(parts) != 3 {
		return nil
	}
	nums := make([]int, 3)
	for i, p := range parts {
		p = strings.SplitN(p, "-", 2)[0]
		p = strings.SplitN(p, "+", 2)[0]
		if p == "" {
			return nil
		}
		n := 0
		for _, ch := range p {
			if ch < '0' || ch > '9' {
				return nil
			}
			n = n*10 + int(ch-'0')
		}
		nums[i] = n
	}
	return nums
}
