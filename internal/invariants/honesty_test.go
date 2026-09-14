package invariants

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/Dicklesworthstone/ntm/internal/policy"
)

// Every check in this package used to end with an unconditional
// `Passed = true; Status = "ok"`, so `ntm doctor` printed six green ticks no
// matter what it found — including a tick sitting directly above a detail line
// reporting the protection was missing. These tests exist to keep each verdict
// falsifiable: a check that cannot fail reports nothing.

func checkerFor(t *testing.T, projectDir string) *Checker {
	t.Helper()
	c := NewChecker(projectDir)
	// Never consult the developer's own ~/.ntm/policy.yaml.
	c.loadPolicy = func() (*policy.Policy, error) { return policy.DefaultPolicy(), nil }
	return c
}

func installGuard(t *testing.T, projectDir string) {
	t.Helper()
	hooks := filepath.Join(projectDir, ".git", "hooks")
	if err := os.MkdirAll(hooks, 0o755); err != nil {
		t.Fatalf("mkdir hooks: %v", err)
	}
	if err := os.WriteFile(filepath.Join(hooks, "pre-commit"),
		[]byte("#!/bin/sh\nntm-precommit-guard \"$@\"\n"), 0o755); err != nil {
		t.Fatalf("write hook: %v", err)
	}
}

func TestNoSilentDataLossTurnsOnTheGuard(t *testing.T) {
	withoutGuard := checkerFor(t, t.TempDir()).checkNoSilentDataLoss(context.Background())
	if withoutGuard.Status != StatusWarning || withoutGuard.Passed {
		t.Errorf("without a guard: status=%q passed=%v, want warning/false",
			withoutGuard.Status, withoutGuard.Passed)
	}

	dir := t.TempDir()
	installGuard(t, dir)
	withGuard := checkerFor(t, dir).checkNoSilentDataLoss(context.Background())
	if withGuard.Status != StatusOK || !withGuard.Passed {
		t.Errorf("with a guard installed: status=%q passed=%v, want ok/true",
			withGuard.Status, withGuard.Passed)
	}
}

// The Safe-by-Default check used to assert "automation.auto_commit defaults to
// false" without opening anything — which is also untrue, since DefaultPolicy
// sets AutoCommit. It must read the policy actually in effect.
func TestSafeByDefaultReadsTheEffectivePolicy(t *testing.T) {
	cases := map[string]struct {
		automation policy.AutomationConfig
		wantStatus string
	}{
		"all risky automation off": {
			automation: policy.AutomationConfig{ForceRelease: "approval"},
			wantStatus: StatusOK,
		},
		"auto push enabled": {
			automation: policy.AutomationConfig{AutoPush: true, ForceRelease: "approval"},
			wantStatus: StatusWarning,
		},
		"auto commit enabled": {
			automation: policy.AutomationConfig{AutoCommit: true, ForceRelease: "approval"},
			wantStatus: StatusWarning,
		},
		"unattended force release": {
			automation: policy.AutomationConfig{ForceRelease: "auto"},
			wantStatus: StatusWarning,
		},
	}

	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			c := NewChecker(t.TempDir())
			c.loadPolicy = func() (*policy.Policy, error) {
				return &policy.Policy{Version: 1, Automation: tc.automation}, nil
			}

			result := c.checkSafeByDefault(context.Background())
			if result.Status != tc.wantStatus {
				t.Errorf("status = %q, want %q (message: %s)", result.Status, tc.wantStatus, result.Message)
			}
			if (result.Status == StatusOK) != result.Passed {
				t.Errorf("Passed=%v disagrees with status %q", result.Passed, result.Status)
			}
		})
	}
}

// The stock policy enables auto-commit, so the stock answer is a warning — not
// the "safe defaults enforced by policy engine" tick this used to print.
func TestSafeByDefaultFlagsTheShippedDefault(t *testing.T) {
	c := NewChecker(t.TempDir())
	c.loadPolicy = func() (*policy.Policy, error) { return policy.DefaultPolicy(), nil }

	result := c.checkSafeByDefault(context.Background())
	if result.Status == StatusOK {
		t.Errorf("DefaultPolicy sets AutoCommit=%v; reporting ok hides it",
			policy.DefaultPolicy().Automation.AutoCommit)
	}
}

func TestSafeByDefaultReportsUnreadablePolicy(t *testing.T) {
	c := NewChecker(t.TempDir())
	c.loadPolicy = func() (*policy.Policy, error) { return nil, errors.New("boom") }

	result := c.checkSafeByDefault(context.Background())
	if result.Status != StatusWarning || result.Passed {
		t.Errorf("unreadable policy: status=%q passed=%v, want warning/false", result.Status, result.Passed)
	}
}

// Re-attaching after a crash is the entire Recoverable State invariant, so a
// missing tmux has to fail it rather than be narrated as present.
func TestRecoverableStateFailsWithoutTmux(t *testing.T) {
	t.Setenv("PATH", t.TempDir())

	result := checkerFor(t, t.TempDir()).checkRecoverableState(context.Background())
	if result.Status != StatusError || result.Passed {
		t.Errorf("with no tmux on PATH: status=%q passed=%v, want error/false", result.Status, result.Passed)
	}
}

// An audit log that cannot be written is exactly the failure this invariant
// exists to catch, so it is probed rather than assumed.
func TestAuditableActionsFailsWhenLogDirUnwritable(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("permission semantics differ on windows")
	}
	if os.Geteuid() == 0 {
		t.Skip("root bypasses directory permissions")
	}

	project := t.TempDir()
	ntmDir := filepath.Join(project, ".ntm")
	if err := os.MkdirAll(ntmDir, 0o755); err != nil {
		t.Fatalf("mkdir .ntm: %v", err)
	}
	// Read-only .ntm: the logs directory can be neither created nor written.
	if err := os.Chmod(ntmDir, 0o500); err != nil {
		t.Fatalf("chmod: %v", err)
	}
	t.Cleanup(func() { _ = os.Chmod(ntmDir, 0o755) })

	result := checkerFor(t, project).checkAuditableActions(context.Background())
	if result.Status != StatusError || result.Passed {
		t.Errorf("with an unwritable audit dir: status=%q passed=%v, want error/false",
			result.Status, result.Passed)
	}
}

func TestAuditableActionsPassesWhenWritable(t *testing.T) {
	result := checkerFor(t, t.TempDir()).checkAuditableActions(context.Background())
	if result.Status != StatusOK || !result.Passed {
		t.Errorf("with a writable audit dir: status=%q passed=%v, want ok/true", result.Status, result.Passed)
	}
}

// The guard against the whole bug class: no measurable invariant may be
// hardcoded to pass. Each is driven into a failing environment and must say so.
func TestEveryMeasurableInvariantCanFail(t *testing.T) {
	t.Setenv("PATH", t.TempDir()) // removes tmux for checkRecoverableState

	project := t.TempDir() // no pre-commit guard
	c := NewChecker(project)
	c.loadPolicy = func() (*policy.Policy, error) {
		return &policy.Policy{Version: 1, Automation: policy.AutomationConfig{AutoPush: true}}, nil
	}
	ctx := context.Background()

	for name, result := range map[string]CheckResult{
		"no_silent_data_loss": c.checkNoSilentDataLoss(ctx),
		"recoverable_state":   c.checkRecoverableState(ctx),
		"safe_by_default":     c.checkSafeByDefault(ctx),
	} {
		if result.Status == StatusOK || result.Passed {
			t.Errorf("%s reported ok in an environment that violates it (message: %q)",
				name, result.Message)
		}
	}
}

// Graceful Degradation is measured from the tool availability doctor already
// observed. The failure it names is degrading *silently*, so an unavailable
// tool that nothing reported must fail the check.
func TestGracefulDegradationFailsOnSilentDegradation(t *testing.T) {
	c := checkerFor(t, t.TempDir())
	c.WithToolReport(func() []ToolAvailability {
		return []ToolAvailability{
			{Name: "bd", Available: true, Reported: true},
			{Name: "cass", Available: false, Reported: false}, // missing, nobody said so
		}
	})

	result := c.checkGracefulDegradation(context.Background())
	if result.Status != StatusError || result.Passed {
		t.Errorf("silent degradation: status=%q passed=%v, want error/false", result.Status, result.Passed)
	}
	if !strings.Contains(result.Message, "cass") {
		t.Errorf("message should name the silently-degraded tool, got %q", result.Message)
	}
}

func TestGracefulDegradationPassesWhenUnavailableToolsAreReported(t *testing.T) {
	c := checkerFor(t, t.TempDir())
	c.WithToolReport(func() []ToolAvailability {
		return []ToolAvailability{
			{Name: "bd", Available: true, Reported: true},
			{Name: "cass", Available: false, Reported: true},
		}
	})

	result := c.checkGracefulDegradation(context.Background())
	if result.Status != StatusOK || !result.Passed {
		t.Errorf("reported degradation: status=%q passed=%v, want ok/true", result.Status, result.Passed)
	}
}

// With no evidence supplied the check must say so rather than guess either way.
func TestGracefulDegradationUnverifiedWithoutEvidence(t *testing.T) {
	result := NewChecker(t.TempDir()).checkGracefulDegradation(context.Background())
	if result.Status != StatusUnverified || result.Passed {
		t.Errorf("no evidence: status=%q passed=%v, want unverified/false", result.Status, result.Passed)
	}
}

// Retry safety for `ntm send` is enforced by a composite primary key (#245). If
// that constraint is gone from the live database, retries duplicate work — so
// the check reads the schema instead of describing the design.
func TestIdempotentOrchestrationChecksTheSendKey(t *testing.T) {
	cases := map[string]struct {
		schema []string
		want   string
	}{
		"constraint present": {
			schema: []string{"CREATE TABLE send_operations (\n operation_id TEXT NOT NULL,\n PRIMARY KEY (operation_id, session_name)\n)"},
			want:   StatusOK,
		},
		"table missing entirely": {
			schema: []string{"CREATE TABLE sessions (id TEXT)"},
			want:   StatusError,
		},
		"table present but unkeyed": {
			schema: []string{"CREATE TABLE send_operations (operation_id TEXT NOT NULL)"},
			want:   StatusError,
		},
		"keyed on operation_id alone (the pre-018 shape)": {
			schema: []string{"CREATE TABLE send_operations (operation_id TEXT PRIMARY KEY)"},
			want:   StatusError,
		},
	}

	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			c := checkerFor(t, t.TempDir())
			c.WithSchemaConstraints(func() ([]string, error) { return tc.schema, nil })

			result := c.checkIdempotentOrchestration(context.Background())
			if result.Status != tc.want {
				t.Errorf("status = %q, want %q (message: %s)", result.Status, tc.want, result.Message)
			}
			if (result.Status == StatusOK) != result.Passed {
				t.Errorf("Passed=%v disagrees with status %q", result.Passed, result.Status)
			}
		})
	}
}

func TestIdempotentOrchestrationReportsUnreadableSchema(t *testing.T) {
	c := checkerFor(t, t.TempDir())
	c.WithSchemaConstraints(func() ([]string, error) { return nil, errors.New("db locked") })

	result := c.checkIdempotentOrchestration(context.Background())
	if result.Status != StatusWarning || result.Passed {
		t.Errorf("unreadable schema: status=%q passed=%v, want warning/false", result.Status, result.Passed)
	}
}

// sqlite_master stores CREATE TABLE text exactly as the migration wrote it, so
// the key check must survive reformatting. Reporting the guarantee missing when
// it is intact is the same defect as a check that cannot fail, pointed the other
// way.
func TestIdempotencyKeyMatchSurvivesReformatting(t *testing.T) {
	equivalent := []string{
		"CREATE TABLE send_operations (\n  PRIMARY KEY (operation_id, session_name)\n)",
		"CREATE TABLE send_operations (PRIMARY KEY(operation_id,session_name))",
		"CREATE TABLE send_operations (\n  primary key (\n    operation_id,\n    session_name\n  )\n)",
		"CREATE TABLE send_operations (PRIMARY  KEY  ( operation_id , session_name ))",
	}

	for _, schema := range equivalent {
		c := checkerFor(t, t.TempDir())
		c.WithSchemaConstraints(func() ([]string, error) { return []string{schema}, nil })

		result := c.checkIdempotentOrchestration(context.Background())
		if result.Status != StatusOK {
			t.Errorf("status = %q for an equivalent schema spelling, want ok:\n%s",
				result.Status, schema)
		}
	}

	// A genuinely different key must still fail.
	for _, schema := range []string{
		"CREATE TABLE send_operations (operation_id TEXT PRIMARY KEY)",
		"CREATE TABLE send_operations (PRIMARY KEY (session_name, operation_id))",
	} {
		c := checkerFor(t, t.TempDir())
		c.WithSchemaConstraints(func() ([]string, error) { return []string{schema}, nil })
		if got := c.checkIdempotentOrchestration(context.Background()).Status; got != StatusError {
			t.Errorf("status = %q for a wrong key, want error:\n%s", got, schema)
		}
	}
}

// doctor is a diagnostic and does not migrate, so it can meet a database whose
// schema has never been created. That is not a missing guarantee — calling it
// one would fail every fresh install — while a table absent from an
// *initialized* schema still is.
func TestIdempotentOrchestrationSeparatesFreshFromMissing(t *testing.T) {
	fresh := checkerFor(t, t.TempDir())
	fresh.WithSchemaConstraints(func() ([]string, error) { return nil, nil })
	if got := fresh.checkIdempotentOrchestration(context.Background()); got.Status != StatusUnverified {
		t.Errorf("empty schema: status = %q, want %q", got.Status, StatusUnverified)
	}

	initialized := checkerFor(t, t.TempDir())
	initialized.WithSchemaConstraints(func() ([]string, error) {
		return []string{"CREATE TABLE runtime_sessions (id TEXT)"}, nil
	})
	if got := initialized.checkIdempotentOrchestration(context.Background()); got.Status != StatusError {
		t.Errorf("initialized schema missing send_operations: status = %q, want %q", got.Status, StatusError)
	}
}
