//go:build e2e
// +build e2e

// Package e2e contains end-to-end tests for NTM robot mode commands.
//
// [E2E-SLB-APPROVAL] bd-cx733: SLB two-person approval workflow, end to end.
//
// This suite pins what the approval machinery ACTUALLY does today, verified
// by a code audit (file:line refs below) and asserted here against the real
// ntm binary with a hermetic HOME (policy at $HOME/.ntm/policy.yaml, see
// internal/policy/policy.go:109 ResolveEffectivePath) and a hermetic state DB
// (NTM_CONFIG dir/state.db, see internal/state/store.go:42 DefaultPath).
//
// The audited chain:
//
//   - Policy verdicts: internal/policy/policy.go:240 (Check) evaluated by
//     `ntm safety check` (internal/cli/safety.go:477 evaluateSafetyCheck),
//     exit 1 for block/approve. SLB flag surfaces via policy.slb.
//   - Durable approvals: internal/approval/engine.go (Request/Approve/Deny)
//     over state.db approvals table (internal/state/store.go:652).
//     The two-person rule lives at internal/approval/engine.go:245 and is
//     enforced ONLY when the approval record has RequiresSLB=true.
//   - Decision CLI: `ntm approve ...` (internal/cli/approve.go). Approver
//     identity = NTM_USER || USER env (internal/cli/approve.go:317) — i.e.
//     caller-asserted, not authenticated.
//
// GAPS pinned by this suite (current, real behavior — do not "fix" the test,
// fix the product and then update these assertions):
//
//	GAP-1 (P1, filed as bug bead bd-2y2on — see TestSLBApproval_ForceReleaseUngated):
//	  `ntm locks force-release` (internal/cli/locks.go:998 runForceRelease)
//	  never consults the policy engine or the approval engine. The default
//	  policy's automation.force_release="approval" and the SLB-flagged
//	  `force_release` approval_required rule (internal/policy/policy.go:150,
//	  173) are decorative for this command: no approval record is created,
//	  no second person is demanded, and --yes/--json skip the only (local,
//	  cosmetic) confirmation prompt.
//
//	GAP-2 (same bead, bd-2y2on): nothing in production creates durable approval
//	  records. approval.Engine.Request has zero non-test callers, so the
//	  `ntm approve list` queue that safety wrappers point users at
//	  ("Run 'ntm approve list' to see pending requests",
//	  internal/cli/safety.go:794) is always empty, and an *approved* record
//	  has no effect on enforcement — `ntm safety check` re-evaluates policy
//	  statelessly and keeps exiting 1.
//
// What DOES work end to end (proven here): once a durable SLB approval
// record exists, `ntm approve` enforces the two-person rule (requester
// cannot self-approve), a second identity can approve or deny, decisions
// are terminal, and the durable record captures both identities.
package e2e

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Dicklesworthstone/ntm/internal/approval"
	"github.com/Dicklesworthstone/ntm/internal/state"
	"github.com/Dicklesworthstone/ntm/internal/tmux"
)

// slbApprovalPolicyYAML requires approval for history rewrites and flags
// force_release as SLB (two-person). automation.force_release is "never" —
// the strictest setting — so that TestSLBApproval_ForceReleaseUngated pins
// the fact that even "never" does not gate `ntm locks force-release`.
const slbApprovalPolicyYAML = `version: 1
blocked:
  - pattern: 'git\s+reset\s+--hard'
    reason: "Hard reset loses uncommitted changes"
approval_required:
  - pattern: 'git\s+commit\s+--amend'
    reason: "Amending rewrites history"
  - pattern: 'force_release'
    reason: "Force release another agent's reservation"
    slb: true
automation:
  auto_push: false
  auto_commit: true
  force_release: never
`

// slbApprovalEnv is one hermetic world: its own HOME (policy file), its own
// NTM_CONFIG dir (own state.db), and a scratch project dir to run from.
type slbApprovalEnv struct {
	home       string
	cfgDir     string
	projectDir string
	stateDB    string
}

func newSLBApprovalEnv(t *testing.T, logger *TestLogger) *slbApprovalEnv {
	t.Helper()

	root := t.TempDir()
	env := &slbApprovalEnv{
		home:       filepath.Join(root, "home"),
		cfgDir:     filepath.Join(root, "config"),
		projectDir: filepath.Join(root, "project"),
	}
	env.stateDB = filepath.Join(env.cfgDir, "state.db")

	for _, dir := range []string{filepath.Join(env.home, ".ntm"), env.cfgDir, env.projectDir} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatalf("[E2E-SLB] mkdir %s: %v", dir, err)
		}
	}

	policyPath := filepath.Join(env.home, ".ntm", "policy.yaml")
	if err := os.WriteFile(policyPath, []byte(slbApprovalPolicyYAML), 0o644); err != nil {
		t.Fatalf("[E2E-SLB] write policy: %v", err)
	}

	logger.Log("[E2E-SLB] Hermetic env: HOME=%s NTM_CONFIG=%s/config.toml state.db=%s", env.home, env.cfgDir, env.stateDB)
	logger.Log("[E2E-SLB] Policy written: %s", policyPath)
	return env
}

// runNTM runs the freshly built ntm binary inside the hermetic env.
// approver becomes NTM_USER (the identity `ntm approve` records; see
// internal/cli/approve.go:317 getCurrentApprover). PATH is restricted so
// optional ecosystem tools (dcg, slb) cannot alter policy verdicts — this
// suite pins NTM's own machinery, not external escalation.
func (e *slbApprovalEnv) runNTM(t *testing.T, logger *TestLogger, approver string, args ...string) (string, int) {
	t.Helper()
	bin, err := ensureE2ENTMBin()
	if err != nil {
		t.Fatalf("[E2E-SLB] build ntm: %v", err)
	}

	cmd := exec.Command(bin, args...)
	cmd.Dir = e.projectDir
	cmd.Env = append(baseEnvWithout("HOME", "NTM_CONFIG", "NTM_USER", "PATH", "XDG_CONFIG_HOME"),
		"HOME="+e.home,
		"NTM_CONFIG="+filepath.Join(e.cfgDir, "config.toml"),
		"NTM_USER="+approver,
		"PATH=/usr/bin:/bin",
	)

	started := time.Now()
	out, err := cmd.CombinedOutput()
	exit := 0
	if err != nil {
		ee, ok := err.(*exec.ExitError)
		if !ok {
			t.Fatalf("[E2E-SLB] run ntm %v: %v (output=%s)", args, err, out)
		}
		exit = ee.ExitCode()
	}
	logger.Log("[E2E-SLB-NTM] approver=%q args=%v exit=%d elapsed=%s", approver, args, exit, time.Since(started).Round(time.Millisecond))
	logger.Log("[E2E-SLB-NTM] output=%s", strings.TrimSpace(string(out)))
	return string(out), exit
}

// baseEnvWithout returns os.Environ() minus the named keys, so hermetic
// overrides cannot be shadowed by duplicate entries.
func baseEnvWithout(keys ...string) []string {
	drop := make(map[string]bool, len(keys))
	for _, k := range keys {
		drop[k] = true
	}
	var env []string
	for _, kv := range os.Environ() {
		name, _, _ := strings.Cut(kv, "=")
		if !drop[name] {
			env = append(env, kv)
		}
	}
	return env
}

// seedApproval creates a durable approval record through the real engine
// against the SAME state.db the ntm subprocesses read (NTM_CONFIG dir).
//
// This in-process call is deliberate, not a shortcut: the audit found that
// NO production code path creates approval records (GAP-2), so the only way
// to exercise the decision surface end to end is to feed the queue exactly
// the way a future production caller must — approval.Engine.Request.
// EnableSLB is false here to keep the external `slb` CLI (if installed on
// the host) out of a hermetic test; it only affects notification fan-out,
// not the two-person enforcement under test (internal/approval/engine.go:245).
func seedApproval(t *testing.T, logger *TestLogger, env *slbApprovalEnv, params approval.RequestParams) *state.Approval {
	t.Helper()

	store, err := state.Open(env.stateDB)
	if err != nil {
		t.Fatalf("[E2E-SLB] open state store: %v", err)
	}
	defer store.Close()
	if err := store.Migrate(); err != nil {
		t.Fatalf("[E2E-SLB] migrate state store: %v", err)
	}

	engine := approval.New(store, nil, nil, approval.Config{
		DefaultExpiry: time.Hour,
		EnableSLB:     false,
	})
	record, err := engine.Request(context.Background(), params)
	if err != nil {
		t.Fatalf("[E2E-SLB] seed approval request: %v", err)
	}
	logger.LogJSON("seeded_approval_record", record)
	return record
}

// slbApproveListResponse mirrors `ntm approve list --json`
// (internal/cli/approve.go:171).
type slbApproveListResponse struct {
	Success bool             `json:"success"`
	Pending []state.Approval `json:"pending"`
	Count   int              `json:"count"`
}

// slbApproveShowResponse mirrors `ntm approve show --json`
// (internal/cli/approve.go:254).
type slbApproveShowResponse struct {
	Success  bool           `json:"success"`
	Approval state.Approval `json:"approval"`
}

// slbApproveActionResponse mirrors ApprovalResult (internal/cli/approve.go:92).
type slbApproveActionResponse struct {
	Success  bool   `json:"success"`
	ID       string `json:"id"`
	Action   string `json:"action"`
	Resource string `json:"resource"`
	Status   string `json:"status"`
	Error    string `json:"error,omitempty"`
}

func (e *slbApprovalEnv) approveList(t *testing.T, logger *TestLogger, approver string) slbApproveListResponse {
	t.Helper()
	out, exit := e.runNTM(t, logger, approver, "approve", "list", "--json")
	if exit != 0 {
		t.Fatalf("[E2E-SLB] approve list exited %d: %s", exit, out)
	}
	var resp slbApproveListResponse
	if err := json.Unmarshal([]byte(out), &resp); err != nil {
		t.Fatalf("[E2E-SLB] parse approve list: %v (output=%s)", err, out)
	}
	logger.LogJSON("approve_list", resp)
	return resp
}

func (e *slbApprovalEnv) approveShow(t *testing.T, logger *TestLogger, approver, id string) slbApproveShowResponse {
	t.Helper()
	out, exit := e.runNTM(t, logger, approver, "approve", "show", id, "--json")
	if exit != 0 {
		t.Fatalf("[E2E-SLB] approve show %s exited %d: %s", id, exit, out)
	}
	var resp slbApproveShowResponse
	if err := json.Unmarshal([]byte(out), &resp); err != nil {
		t.Fatalf("[E2E-SLB] parse approve show: %v (output=%s)", err, out)
	}
	logger.LogJSON("approve_show", resp)
	return resp
}

func (e *slbApprovalEnv) safetyCheck(t *testing.T, logger *TestLogger, command string) (SafetyCheckResponse, int) {
	t.Helper()
	out, exit := e.runNTM(t, logger, "agent-alice", "safety", "check", "--json", "--", command)
	var resp SafetyCheckResponse
	if err := json.Unmarshal([]byte(out), &resp); err != nil {
		t.Fatalf("[E2E-SLB] parse safety check: %v (output=%s)", err, out)
	}
	logger.LogJSON("safety_check", resp)
	return resp, exit
}

// TestSLBApproval_PolicyGate: Scenario 1. The policy engine really gates
// dangerous operations from its own vocabulary (approval_required patterns,
// SLB flag), and the attempt is refused (exit 1). But — GAP-2 — the refusal
// creates NO pending approval record: `ntm approve list` stays empty.
func TestSLBApproval_PolicyGate(t *testing.T) {
	SkipIfNoNTM(t)
	logger := NewTestLogger(t, "slb-approval-policy-gate")
	defer logger.Close()
	env := newSLBApprovalEnv(t, logger)

	// A command from the policy's approval_required vocabulary is refused.
	resp, exit := env.safetyCheck(t, logger, "git commit --amend")
	if exit != 1 {
		t.Fatalf("expected exit 1 for approval-required command, got %d", exit)
	}
	if resp.Action != "approve" {
		t.Fatalf("expected action=approve, got %q", resp.Action)
	}
	if resp.Policy == nil || resp.Policy.SLB {
		t.Fatalf("expected non-SLB policy verdict for amend, got %+v", resp.Policy)
	}

	// The SLB-flagged force_release action is refused with slb=true.
	resp, exit = env.safetyCheck(t, logger, "force_release res-42")
	if exit != 1 {
		t.Fatalf("expected exit 1 for force_release action, got %d", exit)
	}
	if resp.Action != "approve" || resp.Policy == nil || !resp.Policy.SLB {
		t.Fatalf("expected action=approve with policy.slb=true, got action=%q policy=%+v", resp.Action, resp.Policy)
	}

	// A blocked command is refused outright.
	resp, exit = env.safetyCheck(t, logger, "git reset --hard")
	if exit != 1 || resp.Action != "block" {
		t.Fatalf("expected block/exit1 for git reset --hard, got action=%q exit=%d", resp.Action, exit)
	}

	// An unmatched command passes.
	resp, exit = env.safetyCheck(t, logger, "git status")
	if exit != 0 || resp.Action != "allow" {
		t.Fatalf("expected allow/exit0 for git status, got action=%q exit=%d", resp.Action, exit)
	}

	// GAP-2 PINNED: the refusals above created NO pending approval record.
	// The safety wrapper text ("Run 'ntm approve list' to see pending
	// requests", internal/cli/safety.go:794) promises a queue entry that
	// nothing writes. When request-creation is wired up, this assertion
	// MUST flip to Count==2 (or per-attempt records) — that flip is the fix.
	list := env.approveList(t, logger, "agent-alice")
	if !list.Success || list.Count != 0 || len(list.Pending) != 0 {
		t.Fatalf("GAP-2 behavior changed: expected empty pending queue after policy refusals, got %+v", list)
	}
	logger.Log("[E2E-SLB] GAP-2 pinned: policy refusals produced zero pending approval records")
}

// TestSLBApproval_TwoPersonRule: Scenario 2. With a durable SLB approval
// record in the queue, the requester CANNOT self-approve (engine.go:245),
// a second identity CAN, and the durable record captures both identities.
// Also pins that the two-person rule is scoped to RequiresSLB records only:
// a non-SLB record can be self-approved by its requester.
func TestSLBApproval_TwoPersonRule(t *testing.T) {
	SkipIfNoNTM(t)
	logger := NewTestLogger(t, "slb-approval-two-person")
	defer logger.Close()
	env := newSLBApprovalEnv(t, logger)

	record := seedApproval(t, logger, env, approval.RequestParams{
		Action:      "force_release",
		Resource:    "reservation #42 (internal/auth/**)",
		Reason:      "Holder pane crashed while holding the lock",
		RequestedBy: "agent-alice",
		RequiresSLB: true,
	})

	// The pending record is visible on the real listing surface.
	list := env.approveList(t, logger, "agent-alice")
	if list.Count != 1 || len(list.Pending) != 1 {
		t.Fatalf("expected exactly one pending approval, got %+v", list)
	}
	pending := list.Pending[0]
	if pending.ID != record.ID || pending.RequestedBy != "agent-alice" || !pending.RequiresSLB || pending.Status != state.ApprovalPending {
		t.Fatalf("pending record mismatch: %+v", pending)
	}

	// Requester self-approval is rejected: the SLB two-person rule.
	out, exit := env.runNTM(t, logger, "agent-alice", "approve", record.ID, "--json")
	if exit == 0 {
		t.Fatalf("SLB VIOLATION NOT ENFORCED: requester agent-alice self-approved %s: %s", record.ID, out)
	}
	var failure map[string]interface{}
	if err := json.Unmarshal([]byte(out), &failure); err != nil {
		t.Fatalf("parse self-approve failure envelope: %v (output=%s)", err, out)
	}
	logger.LogJSON("self_approve_failure_envelope", failure)
	if ok, _ := failure["success"].(bool); ok {
		t.Fatalf("self-approve envelope claims success: %v", failure)
	}
	if msg, _ := failure["error"].(string); !strings.Contains(msg, "SLB violation") {
		t.Fatalf("expected 'SLB violation' error, got %q", msg)
	}

	// Still pending after the rejected self-approval.
	show := env.approveShow(t, logger, "agent-alice", record.ID)
	if show.Approval.Status != state.ApprovalPending || show.Approval.ApprovedBy != "" {
		t.Fatalf("record mutated by rejected self-approval: %+v", show.Approval)
	}

	// A second identity approves.
	out, exit = env.runNTM(t, logger, "agent-bob", "approve", record.ID, "--json")
	if exit != 0 {
		t.Fatalf("second-person approval failed (exit %d): %s", exit, out)
	}
	var action slbApproveActionResponse
	if err := json.Unmarshal([]byte(out), &action); err != nil {
		t.Fatalf("parse approve response: %v (output=%s)", err, out)
	}
	logger.LogJSON("second_person_approve", action)
	if !action.Success || action.Status != string(state.ApprovalApproved) {
		t.Fatalf("expected approved status, got %+v", action)
	}

	// AUDIT TRAIL: the durable record captures BOTH identities. (This is
	// the only trail that exists — `ntm approve` writes no internal/audit
	// event; part of the filed bug bead.)
	show = env.approveShow(t, logger, "agent-alice", record.ID)
	appr := show.Approval
	if appr.RequestedBy != "agent-alice" || appr.ApprovedBy != "agent-bob" {
		t.Fatalf("audit identities wrong: requested_by=%q approved_by=%q", appr.RequestedBy, appr.ApprovedBy)
	}
	if appr.Status != state.ApprovalApproved || appr.ApprovedAt == nil {
		t.Fatalf("approved record incomplete: %+v", appr)
	}
	logger.Log("[E2E-SLB] Two-person trail: requested_by=%s approved_by=%s approved_at=%s",
		appr.RequestedBy, appr.ApprovedBy, appr.ApprovedAt.Format(time.RFC3339))

	// Approved records leave the pending queue.
	if list = env.approveList(t, logger, "agent-alice"); list.Count != 0 {
		t.Fatalf("approved record still pending: %+v", list)
	}

	// GAP-2 PINNED (enforcement side): approval of the force_release record
	// does NOT unblock the policy verdict — `ntm safety check` is stateless
	// and never consults approval records, so a "re-attempt" of the gated
	// operation still exits 1. There is no machinery that lets an approved
	// record authorize anything.
	resp, exit := env.safetyCheck(t, logger, "force_release res-42")
	if exit != 1 || resp.Action != "approve" {
		t.Fatalf("GAP-2 behavior changed: safety check now honors approvals? action=%q exit=%d", resp.Action, exit)
	}
	logger.Log("[E2E-SLB] GAP-2 pinned: approved record does not unblock enforcement (safety check still exit 1)")

	// SCOPE PIN: two-person rule applies only to RequiresSLB records. A
	// non-SLB record is self-approvable by its own requester (engine.go:245
	// guards on approval.RequiresSLB). This is current, intended engine
	// behavior — recorded here so any tightening is a deliberate change.
	nonSLB := seedApproval(t, logger, env, approval.RequestParams{
		Action:      "git_amend",
		Resource:    "repo main",
		Reason:      "Fix commit message",
		RequestedBy: "agent-carol",
		RequiresSLB: false,
	})
	out, exit = env.runNTM(t, logger, "agent-carol", "approve", nonSLB.ID, "--json")
	if exit != 0 {
		t.Fatalf("non-SLB self-approval unexpectedly rejected (exit %d): %s", exit, out)
	}
	show = env.approveShow(t, logger, "agent-carol", nonSLB.ID)
	if show.Approval.Status != state.ApprovalApproved || show.Approval.ApprovedBy != "agent-carol" {
		t.Fatalf("non-SLB self-approval record wrong: %+v", show.Approval)
	}
	logger.Log("[E2E-SLB] Scope pinned: approver==requester allowed when requires_slb=false")
}

// TestSLBApproval_DenyKeepsBlocked: Scenario 3. Without any approval the
// gated operation stays refused; a denial (with reason) is terminal and
// keeps it refused; a later approval attempt on the denied record fails.
func TestSLBApproval_DenyKeepsBlocked(t *testing.T) {
	SkipIfNoNTM(t)
	logger := NewTestLogger(t, "slb-approval-deny")
	defer logger.Close()
	env := newSLBApprovalEnv(t, logger)

	record := seedApproval(t, logger, env, approval.RequestParams{
		Action:      "force_release",
		Resource:    "reservation #7 (cmd/ntm/**)",
		Reason:      "Suspected stale reservation",
		RequestedBy: "agent-alice",
		RequiresSLB: true,
	})

	// Pending approval alone does not unblock the operation.
	if _, exit := env.safetyCheck(t, logger, "force_release res-7"); exit != 1 {
		t.Fatalf("operation not blocked while approval pending (exit %d)", exit)
	}

	// Second identity denies with a reason.
	out, exit := env.runNTM(t, logger, "agent-bob", "approve", "deny", record.ID, "--reason", "Holder is still active", "--json")
	if exit != 0 {
		t.Fatalf("deny failed (exit %d): %s", exit, out)
	}
	var action slbApproveActionResponse
	if err := json.Unmarshal([]byte(out), &action); err != nil {
		t.Fatalf("parse deny response: %v (output=%s)", err, out)
	}
	logger.LogJSON("deny_response", action)
	if !action.Success || action.Status != string(state.ApprovalDenied) {
		t.Fatalf("expected denied status, got %+v", action)
	}

	// Denial is recorded with decider identity and reason; queue is empty.
	show := env.approveShow(t, logger, "agent-alice", record.ID)
	appr := show.Approval
	if appr.Status != state.ApprovalDenied || appr.ApprovedBy != "agent-bob" || appr.DeniedReason != "Holder is still active" {
		t.Fatalf("denied record incomplete: %+v", appr)
	}
	if list := env.approveList(t, logger, "agent-alice"); list.Count != 0 {
		t.Fatalf("denied record still pending: %+v", list)
	}

	// Denied is terminal: a later approval attempt fails.
	out, exit = env.runNTM(t, logger, "agent-bob", "approve", record.ID, "--json")
	if exit == 0 {
		t.Fatalf("approval of a denied record unexpectedly succeeded: %s", out)
	}
	var failure map[string]interface{}
	if err := json.Unmarshal([]byte(out), &failure); err != nil {
		t.Fatalf("parse post-deny approve failure: %v (output=%s)", err, out)
	}
	logger.LogJSON("post_deny_approve_failure", failure)
	if msg, _ := failure["error"].(string); !strings.Contains(msg, "not pending") {
		t.Fatalf("expected 'not pending' error, got %q", msg)
	}

	// And the operation remains refused throughout.
	if _, exit := env.safetyCheck(t, logger, "force_release res-7"); exit != 1 {
		t.Fatalf("operation unblocked after denial (exit %d)", exit)
	}
	logger.Log("[E2E-SLB] Denial keeps the operation blocked and the record terminal")
}

// TestSLBApproval_ForceReleaseUngated: Scenario 4 — GAP-1, the P1 finding.
//
// The bead asked to prove force-release is approval-gated end to end. The
// audit proves the opposite: internal/cli/locks.go:998 (runForceRelease)
// reaches straight into Agent Mail plumbing without ever loading the policy
// (policy.LoadOrDefault appears nowhere in locks.go) or touching the
// approval engine — even though the active policy here says BOTH
// automation.force_release: never AND force_release is an SLB-flagged
// approval_required action. This test pins that ungated behavior:
//
//   - the command fails on PLUMBING (no project root / no Agent Mail
//     identity for the session), never on policy refusal;
//   - no approval record is requested or consulted.
//
// P1 bug bead bd-2y2on documents this (with the evidence below), filed per
// bd-cx733 scope item 3. If force-release ever becomes approval-gated,
// this test MUST be rewritten to prove the gate instead.
//
// Contrast (also from the audit, judged against the policy engine's scope):
// the build-slot release in `ntm --robot-diagnose --fix`
// (internal/robot/diagnose_build_slots.go:200 executeBuildSlotRelease)
// likewise bypasses approvals, but legitimately: it releases leases whose
// holder identity no longer has a live pane, authenticates AS that holder
// via its persisted registration token, and audit-logs the release
// (diagnose_build_slots.go:218). That is self-release orphan cleanup, not
// the "force release another agent's reservation" action the policy's SLB
// rule names, so it is out of the policy engine's declared scope.
func TestSLBApproval_ForceReleaseUngated(t *testing.T) {
	SkipIfNoNTM(t)
	logger := NewTestLogger(t, "slb-approval-force-release-ungated")
	defer logger.Close()
	env := newSLBApprovalEnv(t, logger)

	assertUngatedFailure := func(label, out string, exit int) {
		if exit == 0 {
			t.Fatalf("[%s] force-release unexpectedly succeeded: %s", label, out)
		}
		var envelope map[string]interface{}
		if err := json.Unmarshal([]byte(out), &envelope); err != nil {
			t.Fatalf("[%s] parse force-release envelope: %v (output=%s)", label, err, out)
		}
		logger.LogJSON(label+"_envelope", envelope)
		if ok, _ := envelope["success"].(bool); ok {
			t.Fatalf("[%s] envelope claims success on failing force-release: %v", label, envelope)
		}
		msg, _ := envelope["error"].(string)
		if msg == "" {
			t.Fatalf("[%s] missing error in envelope: %v", label, envelope)
		}
		// THE PIN: the failure is plumbing, not policy. With
		// automation.force_release: never and an SLB approval_required rule
		// in force, a gated implementation would refuse with a policy or
		// approval error BEFORE touching session/Agent Mail plumbing.
		lower := strings.ToLower(msg)
		if strings.Contains(lower, "approv") || strings.Contains(lower, "policy") || strings.Contains(lower, "slb") {
			t.Fatalf("[%s] GAP-1 behavior changed: force-release now consults policy/approvals (error=%q). Rewrite this test to prove the gate end to end.", label, msg)
		}
		logger.Log("[E2E-SLB] [%s] ungated as audited: failed on plumbing (%q), not policy", label, msg)
	}

	// Leg 1: no such session anywhere. Fails resolving project scope —
	// i.e. it is already past any (nonexistent) policy/approval gate.
	out, exit := env.runNTM(t, logger, "agent-alice", "locks", "force-release", "slb-e2e-ghost", "42", "--yes", "--json")
	assertUngatedFailure("ghost_session", out, exit)

	// Leg 2 (when tmux is available): a real session on the isolated E2E
	// tmux server. Gets deeper into the plumbing (session resolves, no
	// Agent Mail identity) and still no policy/approval consult.
	if tmux.DefaultClient.IsInstalled() {
		session := fmt.Sprintf("slb-e2e-fr-%d", os.Getpid())
		if err := exec.Command(tmux.BinaryPath(), "new-session", "-d", "-s", session, "-x", "120", "-y", "30").Run(); err != nil {
			t.Fatalf("create tmux session %s: %v", session, err)
		}
		defer func() {
			_ = exec.Command(tmux.BinaryPath(), "kill-session", "-t", session).Run()
			logger.Log("[E2E-SLB] killed tmux session %s (no leaked sessions)", session)
		}()
		out, exit = env.runNTM(t, logger, "agent-alice", "locks", "force-release", session, "42", "--yes", "--json")
		assertUngatedFailure("live_session", out, exit)
	} else {
		logger.Log("[E2E-SLB] tmux unavailable; live-session leg skipped")
	}

	// No approval record was requested or consulted anywhere in the path.
	if list := env.approveList(t, logger, "agent-alice"); list.Count != 0 {
		t.Fatalf("force-release created approval records? %+v", list)
	}
	logger.Log("[E2E-SLB] GAP-1 pinned: force-release path never created nor consulted an approval record")
}
