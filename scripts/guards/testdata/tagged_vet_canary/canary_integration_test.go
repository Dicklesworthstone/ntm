//go:build integration

// This file INTENTIONALLY fails to compile: it models a tagged test that
// rotted after its helper was deleted (the bd-xj28x failure mode). The
// tagged_vet.sh guard must detect this via `go vet -tags integration` or
// fail itself — a guard that cannot demonstrate a catch is a placebo.
package canary

import "testing"

func TestRottedTaggedHelper(t *testing.T) {
	if deletedHelperThatNoLongerExists() != 1 { // undefined: compile must fail
		t.Fatal("unreachable")
	}
}
