// Package main is the G1 deadcode-gate canary fixture root
// (bd-ws0-guards-klz98.2). It is a standalone module under testdata/ so the
// main build never sees it. The gate's --self-test runs deadcode over this
// module and asserts three behaviors before trusting the tool on the real
// tree:
//
//	(a) lib.OrphanExported     — exported, zero callers      => MUST be reported dead
//	(b) runEDispatch           — reached only via cobra RunE  => MUST NOT be reported dead
//	(c) lib.TestedButUnwired   — called only from a _test.go  => MUST be reported dead
//	                             (roots are ./cmd/canary WITHOUT -test; this is
//	                             the tested-but-orphaned pathology the gate exists for)
package main

import (
	"fmt"
	"os"

	"github.com/spf13/cobra"

	"deadcodecanary/lib"
)

// runEDispatch is only ever address-taken as a cobra RunE value; RTA must
// still see it as live (canary case b).
func runEDispatch(cmd *cobra.Command, args []string) error {
	fmt.Println(lib.Used())
	return nil
}

func main() {
	root := &cobra.Command{
		Use:  "canary",
		RunE: runEDispatch,
	}
	if err := root.Execute(); err != nil {
		os.Exit(1)
	}
}
