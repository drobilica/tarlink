package freshness

import (
	"fmt"
	"strings"
	"time"
)

// IsNewerRelease compares dotted numeric versions numerically. Other identifiers
// are opaque and require publication times for both releases; lexical order is
// never evidence of a newer version.
func IsNewerRelease(version, current string, published, currentPublished time.Time) (bool, error) {
	version, current = canonicalTagVersion(version), canonicalTagVersion(current)
	if version == current {
		return false, nil
	}
	left, right := strings.Split(version, "."), strings.Split(current, ".")
	numeric := func(parts []string) bool {
		for _, part := range parts {
			if part == "" {
				return false
			}
			for _, char := range part {
				if char < '0' || char > '9' {
					return false
				}
			}
		}
		return true
	}
	if numeric(left) && numeric(right) {
		count := max(len(left), len(right))
		for i := 0; i < count; i++ {
			a, b := "0", "0"
			if i < len(left) {
				a = strings.TrimLeft(left[i], "0")
			}
			if a == "" {
				a = "0"
			}
			if i < len(right) {
				b = strings.TrimLeft(right[i], "0")
			}
			if b == "" {
				b = "0"
			}
			if len(a) != len(b) {
				return len(a) > len(b), nil
			}
			if a != b {
				return a > b, nil
			}
		}
		return false, nil
	}
	if !published.IsZero() && !currentPublished.IsZero() {
		return published.After(currentPublished), nil
	}
	return false, fmt.Errorf("cannot establish a release newer than %q without its upstream publication time; manual review required", current)
}
