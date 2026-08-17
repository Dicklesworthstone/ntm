// Package lib holds the three canary functions for the G1 deadcode gate
// self-test (bd-ws0-guards-klz98.2). See cmd/canary/main.go for the contract.
package lib

// Used is called from the RunE dispatcher; it must be live.
func Used() string {
	return "canary: alive"
}

// OrphanExported has zero callers anywhere. The gate MUST report it dead
// (canary case a); if it does not, the gate aborts as a placebo.
func OrphanExported() string {
	return "canary: orphan"
}

// TestedButUnwired is called only from lib_test.go. Because the gate runs
// deadcode WITHOUT -test (roots = the cmd binary only), it MUST be reported
// dead (canary case c): a passing unit test is not a caller.
func TestedButUnwired() string {
	return "canary: tested but orphaned"
}
