package bv

// The readiness/blocked snapshot helpers that lived here now live in
// work_readiness_helpers_test.go.
//
// None of them were reachable from ./cmd/ntm, so the G1 dead-code gate flagged
// them. GetBlockedSnapshotContext in particular duplicated GetBlockedListContext
// in bv.go — the same `bd blocked --json` call, with the live callers
// (internal/cli/spawn.go, internal/robot/assign.go) all on the bv.go side — and
// ReadySnapshot/BlockedBeadPreview had no production consumer at all.
//
// They were moved rather than deleted so their tests keep running and nothing is
// lost while the duplication is resolved by whoever owns the readiness work.
