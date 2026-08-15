//go:build e2e
// +build e2e

// Package e2e contains end-to-end tests for NTM robot mode commands.
//
// [E2E-SLB-APPROVAL] bd-cx733: SLB two-person approval workflow, end to end.
//
// This suite proves the approval machinery against the real ntm binary with
// a hermetic HOME (policy at $HOME/.ntm/policy.yaml, see
// internal/policy/policy.go ResolveEffectivePath) and a hermetic state DB
// (NTM_CONFIG dir/state.db, see internal/state/store.go DefaultPath).
//
// The chain under test:
//
//   - Policy verdicts: internal/policy/policy.go (Check) evaluated by
//     `ntm safety check` (internal/cli/safety.go evaluateSafetyCheck),
//     exit 1 for block/approve. SLB flag surfaces via policy.slb.
//   - Durable approvals: internal/approval/engine.go (Request/Approve/Deny/
//     Consume) over the state.db approvals table. The two-person rule is
//     enforced ONLY when the approval record has RequiresSLB=true.
//   - Decision CLI: `ntm approve ...` (internal/cli/approve.go). Approver
//     identity = NTM_USER || USER env (getCurrentApprover) — i.e.
//     caller-asserted, not authenticated (known limitation, out of scope
//     for bd-2y2on).
//   - Enforcement: `ntm locks force-release` is gated
//     (internal/cli/force_release_gate.go, wired into runForceRelease in
//     internal/cli/locks.go). This was the P1 gap bd-2y2on: previously the
//     command reached straight into Agent Mail plumbing and the policy's
//     automation.force_release knob was decorative.
//
// GAPS formerly pinned here (bd-2y2on) and their resolution:
//
//	GAP-1 (FIXED): force-release now loads the policy and honors
//	  automation.force_release. "never" refuses outright, naming the policy
//	  file. "approval" (the default) runs a durable two-person workflow: the
//	  attempt creates an SLB approval record keyed by a stable operation key
//	  (correlation_id), stays blocked until a SECOND identity approves, and
//	  an approved record is consumed at gate-pass time so one approval
//	  authorizes exactly one execution. --yes only skips the cosmetic local
//	  prompt, never the gate. TestSLBApproval_ForceReleaseGated proves the
//	  full loop end to end.
//
//	GAP-2 (RESOLVED, split): force-release is now the production caller of
//	  approval.Engine.Request, so the `ntm approve list` queue is real for
//	  gated commands. `ntm safety check` itself remains a stateless advisory
//	  evaluator BY DESIGN (its wrapper scripts only refuse and exit — there
//	  is no execution flow to gate), and the wrapper hints in
//	  internal/cli/safety.go were reworded to stop advertising a queue that
//	  advisory checks do not feed.
//
// Also proven here: once a durable SLB approval record exists, `ntm approve`
// enforces the two-person rule (requester cannot self-approve), a second
// identity can approve or deny, decisions are terminal, and the durable
// record captures both identities.
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
// the strictest setting — which TestSLBApproval_ForceReleaseGated proves is
// honored (a refusal naming the policy file, no approval record created)
// before switching the policy to "approval" for the durable workflow legs.
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

// slbApprovalPolicyApprovalYAML is the same policy with the default
// automation.force_release="approval": force-release must run the durable
// two-person approval workflow.
const slbApprovalPolicyApprovalYAML = `version: 1
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
  force_release: approval
`

// writePolicy overwrites the hermetic env's policy file.
func (e *slbApprovalEnv) writePolicy(t *testing.T, logger *TestLogger, yaml string) {
	t.Helper()
	policyPath := filepath.Join(e.home, ".ntm", "policy.yaml")
	if err := os.WriteFile(policyPath, []byte(yaml), 0o644); err != nil {
		t.Fatalf("[E2E-SLB] rewrite policy: %v", err)
	}
	logger.Log("[E2E-SLB] Policy rewritten: %s", policyPath)
}

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
	cmd.Env = append(baseEnvWithout("HOME", "NTM_CONFIG", "NTM_USER", "PATH", "XDG_CONFIG_HOME", "NTM_TMUX_BINARY"),
		"HOME="+e.home,
		"NTM_CONFIG="+filepath.Join(e.cfgDir, "config.toml"),
		"NTM_USER="+approver,
		"PATH=/usr/bin:/bin",
	)
	// The PATH above is restricted so optional ecosystem tools (dcg, slb)
	// cannot alter verdicts, but the force-release legs need the subprocess
	// to reach the same tmux server this test drives. NTM_TMUX_BINARY is
	// NTM's own explicit override (internal/tmux/client.go BinaryPath) —
	// it admits exactly one binary, keeping the hermetic PATH meaningful.
	if tmux.DefaultClient.IsInstalled() {
		if tmuxPath, err := exec.LookPath(tmux.BinaryPath()); err == nil {
			cmd.Env = append(cmd.Env, "NTM_TMUX_BINARY="+tmuxPath)
		}
	}

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
// This in-process call is deliberate, not a shortcut: it feeds the queue
// exactly the way the production caller does (`ntm locks force-release`'s
// gate calls approval.Engine.Request since bd-2y2on), letting the decision
// scenarios exercise arbitrary record shapes without tmux plumbing.
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
// SLB flag), and the attempt is refused (exit 1). `ntm safety check` is a
// stateless ADVISORY evaluator by design: its refusals enqueue nothing.
// Durable approval records are created by gated commands themselves —
// `ntm locks force-release` (see TestSLBApproval_ForceReleaseGated) — so
// after pure safety checks the queue is still empty.
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

	// DESIGN PIN (bd-2y2on resolution): advisory safety-check refusals
	// create NO approval records — that is now deliberate, not a gap. The
	// wrapper hints in internal/cli/safety.go were reworded accordingly
	// (they no longer point at `ntm approve list` for advisory refusals).
	// Only gated commands enqueue records; a force-release attempt in this
	// same env WOULD make this count non-zero (proven in
	// TestSLBApproval_ForceReleaseGated).
	list := env.approveList(t, logger, "agent-alice")
	if !list.Success || list.Count != 0 || len(list.Pending) != 0 {
		t.Fatalf("advisory safety checks must not enqueue approval records, got %+v", list)
	}
	logger.Log("[E2E-SLB] design pinned: advisory policy refusals produced zero pending approval records")
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

	// DESIGN PIN: `ntm safety check` stays a stateless advisory evaluator —
	// it never consults approval records, so it still exits 1 for the
	// pattern even though an approval exists. Approved records authorize
	// only their own gated command's execution (force-release consumes its
	// record at gate-pass; see TestSLBApproval_ForceReleaseGated).
	resp, exit := env.safetyCheck(t, logger, "force_release res-42")
	if exit != 1 || resp.Action != "approve" {
		t.Fatalf("safety check should remain stateless/advisory: action=%q exit=%d", resp.Action, exit)
	}
	logger.Log("[E2E-SLB] design pinned: approvals do not alter the stateless safety-check verdict")

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

// slbForceReleaseResponse mirrors ForceReleaseResult
// (internal/cli/locks.go), including the approval-gate fields added for
// bd-2y2on.
type slbForceReleaseResponse struct {
	Success        bool   `json:"success"`
	Session        string `json:"session"`
	ReservationID  int    `json:"reservation_id"`
	ApprovalID     string `json:"approval_id"`
	ApprovalStatus string `json:"approval_status"`
	Error          string `json:"error"`
}

// TestSLBApproval_ForceReleaseGated: Scenario 4 — the bd-2y2on fix, proven
// end to end. `ntm locks force-release` is now approval-gated
// (internal/cli/force_release_gate.go wired into runForceRelease):
//
//  0. automation.force_release=never refuses outright, naming the policy
//     file, creating no records;
//  1. under the default "approval" policy an attempt (WITH --yes — which
//     must not bypass the gate) is blocked and creates a durable SLB
//     approval record visible in `ntm approve list`;
//  2. the requester cannot self-approve (two-person rule);
//  3. a second identity approves;
//  4. the re-attempt passes the approval gate — in this hermetic env it
//     then fails on Agent Mail PLUMBING, not on policy/approval — and the
//     approved record is consumed at gate-pass (one approval, one
//     execution);
//  5. a third attempt is blocked again with a FRESH approval record.
//
// Contrast (from the bd-cx733 audit, still true): the build-slot release in
// `ntm --robot-diagnose --fix` (internal/robot/diagnose_build_slots.go
// executeBuildSlotRelease) bypasses approvals legitimately: it releases
// leases whose holder identity no longer has a live pane, authenticates AS
// that holder via its persisted registration token, and audit-logs the
// release. That is self-release orphan cleanup, outside the policy engine's
// "force release another agent's reservation" scope.
func TestSLBApproval_ForceReleaseGated(t *testing.T) {
	SkipIfNoNTM(t)
	if !tmux.DefaultClient.IsInstalled() {
		t.Skip("tmux required: the gate sits between session-scope resolution and Agent Mail plumbing, so a resolvable live session is needed")
	}
	logger := NewTestLogger(t, "slb-approval-force-release-gated")
	defer logger.Close()
	env := newSLBApprovalEnv(t, logger)

	session := fmt.Sprintf("slb-e2e-frg-%d", os.Getpid())
	if err := exec.Command(tmux.BinaryPath(), "new-session", "-d", "-s", session, "-x", "120", "-y", "30").Run(); err != nil {
		t.Fatalf("create tmux session %s: %v", session, err)
	}
	defer func() {
		_ = exec.Command(tmux.BinaryPath(), "kill-session", "-t", session).Run()
		logger.Log("[E2E-SLB] killed tmux session %s (no leaked sessions)", session)
	}()

	attempt := func(label, approver string) (slbForceReleaseResponse, int) {
		// --yes on every attempt: it skips only the cosmetic local prompt
		// and must never bypass the approval gate.
		out, exit := env.runNTM(t, logger, approver, "locks", "force-release", session, "42", "--yes", "--json", "--note", "holder crashed")
		var resp slbForceReleaseResponse
		if err := json.Unmarshal([]byte(out), &resp); err != nil {
			t.Fatalf("[%s] parse force-release envelope: %v (output=%s)", label, err, out)
		}
		logger.LogJSON(label+"_envelope", resp)
		return resp, exit
	}

	// Phase 0: automation.force_release=never (the env's initial policy)
	// refuses with a clear policy error naming the policy file, before any
	// plumbing, and creates no approval record.
	resp, exit := attempt("never_policy", "agent-alice")
	if exit == 0 || resp.Success {
		t.Fatalf("never policy did not refuse force-release: exit=%d resp=%+v", exit, resp)
	}
	if !strings.Contains(resp.Error, "automation.force_release=never") {
		t.Fatalf("never refusal should name the policy setting, got %q", resp.Error)
	}
	if !strings.Contains(resp.Error, filepath.Join(env.home, ".ntm", "policy.yaml")) {
		t.Fatalf("never refusal should name the policy file, got %q", resp.Error)
	}
	if list := env.approveList(t, logger, "agent-alice"); list.Count != 0 {
		t.Fatalf("never policy created approval records: %+v", list)
	}
	logger.Log("[E2E-SLB] Phase 0: never policy refused cleanly, zero records")

	// Phase 1: switch to the default approval policy. The attempt is
	// blocked (exit non-zero, clean envelope) and creates a durable SLB
	// approval record that the real listing surface shows.
	env.writePolicy(t, logger, slbApprovalPolicyApprovalYAML)
	resp, exit = attempt("first_attempt", "agent-alice")
	if exit == 0 || resp.Success {
		t.Fatalf("approval policy did not block ungated attempt: exit=%d resp=%+v", exit, resp)
	}
	if resp.ApprovalID == "" || resp.ApprovalStatus != "pending" {
		t.Fatalf("blocked attempt should carry approval_id + pending status, got %+v", resp)
	}
	if !strings.Contains(resp.Error, "approval required") || !strings.Contains(resp.Error, resp.ApprovalID) {
		t.Fatalf("blocked attempt should explain the approval workflow, got %q", resp.Error)
	}
	approvalID := resp.ApprovalID

	list := env.approveList(t, logger, "agent-alice")
	if list.Count != 1 || len(list.Pending) != 1 {
		t.Fatalf("expected exactly one pending approval after blocked attempt, got %+v", list)
	}
	pending := list.Pending[0]
	if pending.ID != approvalID || pending.RequestedBy != "agent-alice" || !pending.RequiresSLB || pending.Status != state.ApprovalPending {
		t.Fatalf("pending record mismatch: %+v", pending)
	}
	if pending.Action != "force_release" {
		t.Fatalf("record action = %q, want force_release", pending.Action)
	}

	// Re-running the identical command finds ITS OWN record (stable
	// operation key) instead of enqueueing a duplicate.
	resp, exit = attempt("rerun_pending", "agent-alice")
	if exit == 0 || resp.ApprovalID != approvalID || resp.ApprovalStatus != "pending" {
		t.Fatalf("re-run should stay blocked on the SAME pending record %s, got exit=%d resp=%+v", approvalID, exit, resp)
	}
	if list = env.approveList(t, logger, "agent-alice"); list.Count != 1 {
		t.Fatalf("re-run enqueued a duplicate approval: %+v", list)
	}
	logger.Log("[E2E-SLB] Phase 1: blocked + durable SLB record %s created (no duplicates on re-run)", approvalID)

	// Phase 2: the requester cannot self-approve (SLB two-person rule).
	out, selfExit := env.runNTM(t, logger, "agent-alice", "approve", approvalID, "--json")
	if selfExit == 0 {
		t.Fatalf("SLB VIOLATION: requester self-approved %s: %s", approvalID, out)
	}
	var failure map[string]interface{}
	if err := json.Unmarshal([]byte(out), &failure); err != nil {
		t.Fatalf("parse self-approve failure: %v (output=%s)", err, out)
	}
	logger.LogJSON("self_approve_failure", failure)
	if msg, _ := failure["error"].(string); !strings.Contains(msg, "SLB violation") {
		t.Fatalf("expected 'SLB violation' error, got %q", msg)
	}

	// Phase 3: a second identity approves.
	out, exitCode := env.runNTM(t, logger, "agent-bob", "approve", approvalID, "--json")
	if exitCode != 0 {
		t.Fatalf("second-person approval failed (exit %d): %s", exitCode, out)
	}
	var action slbApproveActionResponse
	if err := json.Unmarshal([]byte(out), &action); err != nil {
		t.Fatalf("parse approve response: %v (output=%s)", err, out)
	}
	if !action.Success || action.Status != string(state.ApprovalApproved) {
		t.Fatalf("expected approved status, got %+v", action)
	}
	logger.Log("[E2E-SLB] Phase 2+3: self-approval rejected, agent-bob approved %s", approvalID)

	// Phase 4: the re-attempt proceeds PAST the approval gate. In this
	// hermetic env it must then fail on Agent Mail plumbing — the point is
	// that the failure is no longer the approval gate.
	resp, exit = attempt("approved_attempt", "agent-alice")
	if exit == 0 || resp.Success {
		t.Fatalf("approved attempt unexpectedly succeeded in hermetic env: %+v", resp)
	}
	lower := strings.ToLower(resp.Error)
	if strings.Contains(lower, "approval required") || strings.Contains(lower, "denied") || strings.Contains(lower, "policy") {
		t.Fatalf("approved attempt still blocked by the gate (error=%q); expected a plumbing failure", resp.Error)
	}
	if resp.ApprovalID != approvalID || resp.ApprovalStatus != "consumed" {
		t.Fatalf("approved attempt should report its consumed approval (%s), got %+v", approvalID, resp)
	}
	logger.Log("[E2E-SLB] Phase 4: gate passed; failure is plumbing (%q), approval consumed", resp.Error)

	// The durable record is consumed: one approval, one execution.
	show := env.approveShow(t, logger, "agent-alice", approvalID)
	if string(show.Approval.Status) != "consumed" {
		t.Fatalf("approval %s not consumed after gate pass: %+v", approvalID, show.Approval)
	}
	if show.Approval.ApprovedBy != "agent-bob" || show.Approval.RequestedBy != "agent-alice" {
		t.Fatalf("consumed record lost its two-person trail: %+v", show.Approval)
	}

	// Phase 5: a third attempt requires a FRESH approval.
	resp, exit = attempt("third_attempt", "agent-alice")
	if exit == 0 || resp.Success {
		t.Fatalf("third attempt should be blocked pending fresh approval: %+v", resp)
	}
	if resp.ApprovalStatus != "pending" || resp.ApprovalID == "" || resp.ApprovalID == approvalID {
		t.Fatalf("third attempt should create a NEW pending approval (old=%s), got %+v", approvalID, resp)
	}
	if list = env.approveList(t, logger, "agent-alice"); list.Count != 1 || list.Pending[0].ID != resp.ApprovalID {
		t.Fatalf("queue should hold exactly the fresh record %s, got %+v", resp.ApprovalID, list)
	}
	logger.Log("[E2E-SLB] Phase 5: consumed approval not reusable; fresh record %s required", resp.ApprovalID)
}
