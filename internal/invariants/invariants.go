// Package invariants defines and enforces the 6 non-negotiable design invariants
// that must ALWAYS hold across all NTM features.
//
// These invariants represent core safety and reliability guarantees that NTM
// provides to users. Violating any invariant should cause tests to fail and
// be flagged by ntm doctor.
package invariants

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/Dicklesworthstone/ntm/internal/policy"
)

// InvariantID uniquely identifies each design invariant.
type InvariantID string

const (
	// InvariantNoSilentDataLoss ensures NTM never causes untracked destructive
	// actions without explicit, recorded approval.
	InvariantNoSilentDataLoss InvariantID = "no_silent_data_loss"

	// InvariantGracefulDegradation ensures that if any external tool is missing
	// or unhealthy, NTM continues with reduced capability and clear warnings.
	InvariantGracefulDegradation InvariantID = "graceful_degradation"

	// InvariantIdempotentOrchestration ensures that spawning, reserving, assigning,
	// and messaging are safe to retry without duplicating work.
	InvariantIdempotentOrchestration InvariantID = "idempotent_orchestration"

	// InvariantRecoverableState ensures NTM can re-attach to an existing session
	// after crash/restart.
	InvariantRecoverableState InvariantID = "recoverable_state"

	// InvariantAuditableActions ensures critical actions are logged with
	// correlation IDs.
	InvariantAuditableActions InvariantID = "auditable_actions"

	// InvariantSafeByDefault ensures risky automation is opt-in and policy-gated.
	InvariantSafeByDefault InvariantID = "safe_by_default"
)

// AllInvariants returns all defined invariants.
func AllInvariants() []InvariantID {
	return []InvariantID{
		InvariantNoSilentDataLoss,
		InvariantGracefulDegradation,
		InvariantIdempotentOrchestration,
		InvariantRecoverableState,
		InvariantAuditableActions,
		InvariantSafeByDefault,
	}
}

// Invariant describes a design invariant with its enforcement details.
type Invariant struct {
	ID          InvariantID `json:"id"`
	Name        string      `json:"name"`
	Description string      `json:"description"`
	Enforcement string      `json:"enforcement"`
	Examples    []string    `json:"examples,omitempty"`
}

// Definitions returns the full definitions of all invariants.
func Definitions() map[InvariantID]Invariant {
	return map[InvariantID]Invariant{
		InvariantNoSilentDataLoss: {
			ID:   InvariantNoSilentDataLoss,
			Name: "No Silent Data Loss",
			Description: "NTM must never cause untracked destructive actions without " +
				"explicit, recorded approval.",
			Enforcement: "All destructive commands blocked or require approval. " +
				"All force-release operations logged with correlation IDs. " +
				"All file operations auditable.",
			Examples: []string{
				"git reset --hard blocked by safety wrappers",
				"rm -rf / blocked by policy",
				"force-release requires SLB approval",
			},
		},
		InvariantGracefulDegradation: {
			ID:   InvariantGracefulDegradation,
			Name: "Graceful Degradation",
			Description: "If any external tool is missing/unhealthy, NTM continues " +
				"with reduced capability and clear warnings.",
			Enforcement: "Tool Adapter detects missing/broken tools. " +
				"Features fallback gracefully (e.g., macros -> granular calls). " +
				"Clear messaging about degraded functionality.",
			Examples: []string{
				"bv missing: ntm work uses manual priorities",
				"agent-mail unavailable: skip coordination, warn user",
				"br missing: TodoWrite only, no beads sync",
			},
		},
		InvariantIdempotentOrchestration: {
			ID:   InvariantIdempotentOrchestration,
			Name: "Idempotent Orchestration",
			Description: "Spawning, reserving, assigning, and messaging should be " +
				"safe to retry without duplicating work.",
			Enforcement: "Reservation operations are idempotent. " +
				"Agent registration is idempotent (same name = update, not duplicate). " +
				"Message deduplication across channels.",
			Examples: []string{
				"register_agent twice with same name updates profile",
				"file_reservation_paths re-request extends TTL",
				"spawn with existing session attaches instead",
			},
		},
		InvariantRecoverableState: {
			ID:   InvariantRecoverableState,
			Name: "Recoverable State",
			Description: "NTM must be able to re-attach to an existing session " +
				"after crash/restart.",
			Enforcement: "State Store persists sessions, agents, tasks. " +
				"Event Log enables replay for crash recovery. " +
				"tmux sessions survive NTM process death.",
			Examples: []string{
				"ntm attach works after NTM crash",
				"session state survives process restart",
				"agents continue working without NTM orchestrator",
			},
		},
		InvariantAuditableActions: {
			ID:          InvariantAuditableActions,
			Name:        "Auditable Actions",
			Description: "Critical actions are logged with correlation IDs.",
			Enforcement: "Reservations, releases, force-releases logged. " +
				"Blocked commands logged. Approvals and denials logged. " +
				"Task assignments and completions logged.",
			Examples: []string{
				".ntm/logs/blocked.jsonl contains blocked commands",
				"events.jsonl contains all session events",
				"each action has a correlation_id for tracing",
			},
		},
		InvariantSafeByDefault: {
			ID:          InvariantSafeByDefault,
			Name:        "Safe-by-Default",
			Description: "Risky automation is opt-in and policy-gated.",
			Enforcement: "auto_push: disabled by default, requires policy + approval. " +
				"force_release: requires approval by default. " +
				"destructive commands: blocked by default.",
			Examples: []string{
				"git push requires explicit policy.automation.auto_push=true",
				"force-release requires approval workflow",
				"git reset --hard blocked without explicit allow rule",
			},
		},
	}
}

// Check statuses.
//
// Every check in this file used to end with an unconditional
// `Passed = true; Status = "ok"`, so `ntm doctor` printed six green ticks
// whatever it found — including ticks sitting directly above details reporting
// the protection was absent. A check that cannot fail carries no information;
// it only teaches operators to trust a signal that never says anything.
const (
	// StatusOK means the check ran and the invariant holds.
	StatusOK = "ok"
	// StatusWarning means the check ran and found the invariant weakened.
	StatusWarning = "warning"
	// StatusError means the check ran and found the invariant violated.
	StatusError = "error"
	// StatusUnverified means the invariant is structural and this process did
	// not measure it. doctor renders an unknown status as a muted "?", so this
	// reads as an honest "not checked" rather than an invented pass.
	StatusUnverified = "unverified"
)

// CheckResult represents the result of checking an invariant.
type CheckResult struct {
	InvariantID InvariantID `json:"invariant_id"`
	Passed      bool        `json:"passed"`
	Status      string      `json:"status"` // "ok", "warning", "error"
	Message     string      `json:"message,omitempty"`
	Details     []string    `json:"details,omitempty"`
	CheckedAt   time.Time   `json:"checked_at"`
}

// Report contains results for all invariant checks.
type Report struct {
	Timestamp time.Time                   `json:"timestamp"`
	Results   map[InvariantID]CheckResult `json:"results"`
	AllPassed bool                        `json:"all_passed"`
	Errors    int                         `json:"errors"`
	Warnings  int                         `json:"warnings"`
}

// Checker provides methods to verify invariant enforcement.
type Checker struct {
	ntmDir     string // Path to .ntm directory
	projectDir string // Path to project directory

	// loadPolicy resolves the policy in effect. Injectable so tests can drive
	// the Safe-by-Default verdict without depending on the developer's own
	// ~/.ntm/policy.yaml.
	loadPolicy func() (*policy.Policy, error)
}

// NewChecker creates a new invariant checker.
func NewChecker(projectDir string) *Checker {
	ntmDir := filepath.Join(projectDir, ".ntm")
	if home, err := os.UserHomeDir(); err == nil {
		if _, err := os.Stat(ntmDir); os.IsNotExist(err) {
			// Try home directory
			ntmDir = filepath.Join(home, ".ntm")
		}
	}
	return &Checker{
		ntmDir:     ntmDir,
		projectDir: projectDir,
		loadPolicy: policy.LoadOrDefault,
	}
}

// CheckAll verifies all invariants and returns a complete report.
func (c *Checker) CheckAll(ctx context.Context) *Report {
	report := &Report{
		Timestamp: time.Now(),
		Results:   make(map[InvariantID]CheckResult),
		AllPassed: true,
	}

	checks := map[InvariantID]func(context.Context) CheckResult{
		InvariantNoSilentDataLoss:        c.checkNoSilentDataLoss,
		InvariantGracefulDegradation:     c.checkGracefulDegradation,
		InvariantIdempotentOrchestration: c.checkIdempotentOrchestration,
		InvariantRecoverableState:        c.checkRecoverableState,
		InvariantAuditableActions:        c.checkAuditableActions,
		InvariantSafeByDefault:           c.checkSafeByDefault,
	}

	for id, checkFn := range checks {
		result := checkFn(ctx)
		report.Results[id] = result

		switch result.Status {
		case "error":
			report.Errors++
			report.AllPassed = false
		case "warning":
			report.Warnings++
		}
	}

	return report
}

// checkNoSilentDataLoss verifies the No Silent Data Loss invariant.
func (c *Checker) checkNoSilentDataLoss(ctx context.Context) CheckResult {
	result := CheckResult{
		InvariantID: InvariantNoSilentDataLoss,
		CheckedAt:   time.Now(),
	}

	var details []string

	// Check 1: Safety wrappers should be installed or policy exists
	policyPath := filepath.Join(c.ntmDir, "policy.yaml")
	if _, err := os.Stat(policyPath); os.IsNotExist(err) {
		details = append(details, "policy.yaml not found (default policy will be used)")
	} else {
		details = append(details, "policy.yaml exists")
	}

	// Check 2: Blocked command log directory should be writable
	logsDir := filepath.Join(c.ntmDir, "logs")
	if info, err := os.Stat(logsDir); err == nil && info.IsDir() {
		details = append(details, "logs directory exists for audit trail")
	} else {
		details = append(details, "logs directory missing (will be created on first blocked command)")
	}

	// Check 3: Git hooks for pre-commit guards. This is the one piece of
	// evidence that actually decides the verdict — the others describe
	// defaults that hold either way.
	guardInstalled := false
	gitHooksDir := filepath.Join(c.projectDir, ".git", "hooks")
	preCommit := filepath.Join(gitHooksDir, "pre-commit")
	if _, err := os.Stat(preCommit); err == nil {
		content, readErr := os.ReadFile(preCommit)
		switch {
		case readErr != nil:
			details = append(details, "pre-commit hook exists but unreadable")
		case strings.Contains(string(content), "ntm-precommit-guard"),
			strings.Contains(string(content), "ntm guard"),
			strings.Contains(string(content), "ntm safety"):
			details = append(details, "pre-commit guard installed")
			guardInstalled = true
		default:
			details = append(details, "pre-commit hook exists but no ntm guard")
		}
	} else {
		details = append(details, "no pre-commit hook (run ntm guards install)")
	}

	result.Details = details
	if guardInstalled {
		result.Passed = true
		result.Status = StatusOK
		result.Message = "pre-commit guard installed"
		return result
	}
	result.Passed = false
	result.Status = StatusWarning
	result.Message = "no ntm pre-commit guard in this repository (ntm guards install)"
	return result
}

// checkGracefulDegradation verifies the Graceful Degradation invariant.
func (c *Checker) checkGracefulDegradation(ctx context.Context) CheckResult {
	result := CheckResult{
		InvariantID: InvariantGracefulDegradation,
		CheckedAt:   time.Now(),
	}

	var details []string

	// Structural invariant: it is a property of the code, enforced by the tool
	// adapter tests, and nothing here measures it at runtime. The lines below
	// describe the design; they are not evidence, so this reports unverified
	// rather than claiming a pass it never established.
	details = append(details, "design: tool adapter framework provides detection and fallback")
	details = append(details, "design: NTM continues if external tools are unavailable")
	details = append(details, "not measured by doctor; enforced by the internal/tools adapter tests")

	result.Details = details
	result.Passed = false
	result.Status = StatusUnverified
	result.Message = "structural invariant, not checked at runtime"

	return result
}

// checkIdempotentOrchestration verifies the Idempotent Orchestration invariant.
func (c *Checker) checkIdempotentOrchestration(ctx context.Context) CheckResult {
	result := CheckResult{
		InvariantID: InvariantIdempotentOrchestration,
		CheckedAt:   time.Now(),
	}

	var details []string

	// Structural invariant, verified by the spawn/reservation/dispatch tests.
	// Nothing here exercises those paths, so it is reported as unmeasured.
	details = append(details, "design: agent registration uses upsert semantics")
	details = append(details, "design: file reservations extend TTL on re-request")
	details = append(details, "design: session spawn checks for an existing tmux session")
	details = append(details, "not measured by doctor; enforced by the spawn and reservation tests")

	result.Details = details
	result.Passed = false
	result.Status = StatusUnverified
	result.Message = "structural invariant, not checked at runtime"

	return result
}

// checkRecoverableState verifies the Recoverable State invariant.
func (c *Checker) checkRecoverableState(ctx context.Context) CheckResult {
	result := CheckResult{
		InvariantID: InvariantRecoverableState,
		CheckedAt:   time.Now(),
	}

	var details []string

	// Check 1: State store database exists or can be created
	stateDBPath := filepath.Join(c.ntmDir, "state.db")
	if _, err := os.Stat(stateDBPath); err == nil {
		details = append(details, "state.db exists for session persistence")
	} else {
		details = append(details, "state.db will be created on first session")
	}

	// Check 2: Event log for replay
	eventsPath := filepath.Join(c.ntmDir, "logs", "events.jsonl")
	if _, err := os.Stat(eventsPath); err == nil {
		details = append(details, "events.jsonl exists for crash recovery")
	} else {
		details = append(details, "events.jsonl will be created when logging enabled")
	}

	// Check 3: tmux must actually be present. Re-attaching after a crash is the
	// whole invariant, and without tmux on PATH there is nothing to re-attach
	// to — so this decides the verdict instead of being asserted.
	tmuxPath, tmuxErr := exec.LookPath("tmux")
	if tmuxErr != nil {
		details = append(details, "tmux not found on PATH: sessions cannot survive NTM process death")
		result.Details = details
		result.Passed = false
		result.Status = StatusError
		result.Message = "tmux not available, sessions cannot be recovered"
		return result
	}
	details = append(details, fmt.Sprintf("tmux present at %s; sessions survive NTM process death", tmuxPath))

	result.Details = details
	result.Passed = true
	result.Status = StatusOK
	result.Message = "state recovery mechanisms available"

	return result
}

// checkAuditableActions verifies the Auditable Actions invariant.
func (c *Checker) checkAuditableActions(ctx context.Context) CheckResult {
	result := CheckResult{
		InvariantID: InvariantAuditableActions,
		CheckedAt:   time.Now(),
	}

	var details []string

	// Check 1: Logs directory
	logsDir := filepath.Join(c.ntmDir, "logs")
	if _, err := os.Stat(logsDir); err == nil {
		details = append(details, "logs directory exists")

		// Check for specific log files
		files := []struct {
			name string
			desc string
		}{
			{"blocked.jsonl", "blocked command audit log"},
			{"events.jsonl", "session event log"},
		}
		for _, f := range files {
			path := filepath.Join(logsDir, f.name)
			if _, err := os.Stat(path); err == nil {
				details = append(details, fmt.Sprintf("%s present", f.desc))
			} else {
				details = append(details, fmt.Sprintf("%s will be created on first event", f.desc))
			}
		}
	} else {
		details = append(details, "logs directory will be created when needed")
	}

	// Check 2: the audit trail must be writable. An audit log that cannot be
	// written is the failure this invariant exists to catch, so probe it rather
	// than assert it.
	if err := os.MkdirAll(logsDir, 0o755); err != nil {
		details = append(details, fmt.Sprintf("cannot create audit log directory %s: %v", logsDir, err))
		result.Details = details
		result.Passed = false
		result.Status = StatusError
		result.Message = "audit log directory is not writable"
		return result
	}
	probe := filepath.Join(logsDir, ".ntm-audit-write-probe")
	if err := os.WriteFile(probe, []byte("probe\n"), 0o600); err != nil {
		details = append(details, fmt.Sprintf("audit log directory %s is not writable: %v", logsDir, err))
		result.Details = details
		result.Passed = false
		result.Status = StatusError
		result.Message = "audit log directory is not writable"
		return result
	}
	if err := os.Remove(probe); err != nil {
		details = append(details, fmt.Sprintf("audit write probe left behind at %s: %v", probe, err))
	}
	details = append(details, fmt.Sprintf("audit log directory %s is writable", logsDir))

	result.Details = details
	result.Passed = true
	result.Status = StatusOK
	result.Message = "audit log directory writable"

	return result
}

// checkSafeByDefault verifies the Safe-by-Default invariant.
func (c *Checker) checkSafeByDefault(ctx context.Context) CheckResult {
	result := CheckResult{
		InvariantID: InvariantSafeByDefault,
		CheckedAt:   time.Now(),
	}

	var details []string

	// Read the policy actually in effect. This used to state that
	// "automation.auto_commit defaults to false" without opening anything —
	// which is not merely unverified but wrong, since DefaultPolicy sets
	// AutoCommit true. Asserting a safe default while the operator has enabled
	// risky automation is the exact failure this invariant exists to catch.
	load := c.loadPolicy
	if load == nil {
		load = policy.LoadOrDefault
	}
	effective, err := load()
	if err != nil || effective == nil {
		details = append(details, fmt.Sprintf("could not load the effective policy: %v", err))
		result.Details = details
		result.Passed = false
		result.Status = StatusWarning
		result.Message = "effective policy could not be read"
		return result
	}

	var risky []string
	if effective.Automation.AutoPush {
		risky = append(risky, "automation.auto_push=true")
	}
	if effective.Automation.AutoCommit {
		risky = append(risky, "automation.auto_commit=true")
	}
	forceRelease := effective.ForceReleasePolicy()
	if forceRelease == "auto" {
		risky = append(risky, "automation.force_release=auto")
	}

	details = append(details,
		fmt.Sprintf("automation.auto_push=%t", effective.Automation.AutoPush),
		fmt.Sprintf("automation.auto_commit=%t", effective.Automation.AutoCommit),
		fmt.Sprintf("automation.force_release=%s", forceRelease),
		fmt.Sprintf("%d blocked rule(s), %d approval-required rule(s)",
			len(effective.Blocked), len(effective.ApprovalRequired)),
	)

	result.Details = details
	if len(risky) > 0 {
		result.Passed = false
		result.Status = StatusWarning
		result.Message = "risky automation enabled: " + strings.Join(risky, ", ")
		return result
	}
	result.Passed = true
	result.Status = StatusOK
	result.Message = "risky automation is opt-in and currently disabled"
	return result
}
