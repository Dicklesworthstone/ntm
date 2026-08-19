package assurance

import (
	"time"
)

func fixedDigestClock() time.Time {
	return time.Date(2026, 5, 8, 12, 0, 0, 0, time.UTC)
}

func contains(codes []ReasonCode, want ReasonCode) bool {
	for _, c := range codes {
		if c == want {
			return true
		}
	}
	return false
}

func equalStringSlice(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
