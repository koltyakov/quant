package selfupdate

import (
	"strconv"
	"strings"
)

// IsNewer reports whether latest is a newer semver than current.
func IsNewer(current, latest string) bool {
	return isNewer(current, latest)
}

func isNewer(current, latest string) bool {
	cp, cpre, cok := parseSemverDetails(current)
	lp, lpre, lok := parseSemverDetails(latest)
	if !cok || !lok {
		return latest > current
	}
	if lp[0] != cp[0] {
		return lp[0] > cp[0]
	}
	if lp[1] != cp[1] {
		return lp[1] > cp[1]
	}
	if lp[2] != cp[2] {
		return lp[2] > cp[2]
	}
	if cpre == "" && lpre == "" {
		return false
	}
	if cpre == "" {
		return false
	}
	if lpre == "" {
		return !isGitDescribePrerelease(cpre)
	}
	return comparePrerelease(lpre, cpre) > 0
}

func parseSemver(v string) []int {
	nums, _, ok := parseSemverDetails(v)
	if !ok {
		return nil
	}
	return nums
}

func parseSemverDetails(v string) ([]int, string, bool) {
	parts := strings.SplitN(v, ".", 3)
	if len(parts) != 3 {
		return nil, "", false
	}
	nums := make([]int, 3)
	prerelease := ""
	for i, p := range parts {
		if i == 2 {
			parts := strings.SplitN(p, "+", 2)
			p = parts[0]
			preParts := strings.SplitN(p, "-", 2)
			p = preParts[0]
			if len(preParts) == 2 {
				prerelease = preParts[1]
			}
		}
		p = strings.SplitN(p, "+", 2)[0]
		if p == "" {
			return nil, "", false
		}
		n := 0
		for _, ch := range p {
			if ch < '0' || ch > '9' {
				return nil, "", false
			}
			n = n*10 + int(ch-'0')
		}
		nums[i] = n
	}
	return nums, prerelease, true
}

func isGitDescribePrerelease(pre string) bool {
	i := strings.Index(pre, "-g")
	if i < 0 {
		return false
	}
	commitCount := pre[:i]
	for _, ch := range commitCount {
		if ch < '0' || ch > '9' {
			return false
		}
	}
	if len(commitCount) == 0 {
		return false
	}
	hash := pre[i+2:]
	return len(hash) > 0
}

func comparePrerelease(a, b string) int {
	aParts := strings.Split(a, ".")
	bParts := strings.Split(b, ".")
	for i := 0; i < len(aParts) && i < len(bParts); i++ {
		if aParts[i] == bParts[i] {
			continue
		}
		aNum, aErr := strconv.Atoi(aParts[i])
		bNum, bErr := strconv.Atoi(bParts[i])
		switch {
		case aErr == nil && bErr == nil:
			if aNum < bNum {
				return -1
			}
			return 1
		case aErr == nil:
			return -1
		case bErr == nil:
			return 1
		case aParts[i] < bParts[i]:
			return -1
		default:
			return 1
		}
	}
	switch {
	case len(aParts) < len(bParts):
		return -1
	case len(aParts) > len(bParts):
		return 1
	default:
		return 0
	}
}
