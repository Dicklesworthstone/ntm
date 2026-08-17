package lib

import "testing"

// TestTestedButUnwired passes, proving "has a green unit test" — the gate must
// still report TestedButUnwired as dead (canary case c: tested-but-orphaned).
func TestTestedButUnwired(t *testing.T) {
	if TestedButUnwired() == "" {
		t.Fatal("TestedButUnwired returned empty string")
	}
}
