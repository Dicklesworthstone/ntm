package robot

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Dicklesworthstone/ntm/internal/config"
	"github.com/Dicklesworthstone/ntm/internal/state"
)

func TestDoctor_GetDoctorContractAndNonDestructiveState(t *testing.T) {
	homeDir := t.TempDir()
	xdgDir := t.TempDir()
	t.Setenv("HOME", homeDir)
	t.Setenv("XDG_CONFIG_HOME", xdgDir)
	t.Setenv("NTM_CONFIG", "")

	cfg := config.Default()
	cfg.Redaction.Mode = "redact"
	cfg.Redaction.Allowlist = []string{"allow-1"}
	cfg.Preflight.Strict = true

	out, err := GetDoctor(context.Background(), DoctorOptions{
		Config: cfg,
	})
	if err != nil {
		t.Fatalf("GetDoctor: %v", err)
	}
	if out.SchemaID != doctorSchemaID || out.SchemaVersion != doctorSchemaVersion {
		t.Fatalf("schema = (%q, %q), want (%q, %q)", out.SchemaID, out.SchemaVersion, doctorSchemaID, doctorSchemaVersion)
	}
	switch out.Overall {
	case "healthy", "warning", "unhealthy":
	default:
		t.Fatalf("unexpected Overall = %q", out.Overall)
	}
	if out.Success != (out.Overall != "unhealthy") {
		t.Fatalf("Success = %v disagrees with Overall = %q", out.Success, out.Overall)
	}
	if out.Tools == nil || out.Dependencies == nil || out.Daemons == nil || out.Configuration == nil || out.Invariants == nil || out.Contention == nil {
		t.Fatalf("all DoctorOutput slices must be non-nil: %+v", out)
	}
	if out.GuardHook == nil || out.GuardHook.Name != "guard-hook" || out.GuardHook.Status != "ok" {
		t.Fatalf("unexpected GuardHook = %+v", out.GuardHook)
	}
	if out.SafetyDefaults.RedactionMode != "redact" || !out.SafetyDefaults.RedactionAllowlistEnabled || out.SafetyDefaults.RedactionAllowlistCount != 1 || !out.SafetyDefaults.PreflightDefaultStrict {
		t.Fatalf("unexpected SafetyDefaults = %+v", out.SafetyDefaults)
	}
	if len(out.Invariants) == 0 {
		t.Fatal("expected design invariants to be populated")
	}
	if out.AgentHints == nil || out.AgentHints.Summary == "" {
		t.Fatalf("expected populated AgentHints, got %+v", out.AgentHints)
	}

	// Verify GetDoctor did NOT create state.db as a side effect.
	expectedDBPath := state.DefaultPath()
	if _, statErr := os.Stat(expectedDBPath); statErr == nil {
		t.Fatalf("GetDoctor created state DB at %s on a read-only diagnostic check", expectedDBPath)
	}
}

func TestDoctor_GuardHookDegradedLedgerAndUnhealthyConfig(t *testing.T) {
	homeDir := t.TempDir()
	xdgDir := t.TempDir()
	t.Setenv("HOME", homeDir)
	t.Setenv("XDG_CONFIG_HOME", xdgDir)
	t.Setenv("NTM_CONFIG", "")

	dbPath := state.DefaultPath()
	store, err := state.Open(dbPath)
	if err != nil {
		t.Fatalf("state.Open: %v", err)
	}
	if err := store.Migrate(); err != nil {
		_ = store.Close()
		t.Fatalf("store.Migrate: %v", err)
	}
	for _, reason := range []string{"agent-mail-unreachable", "agent-mail-error"} {
		if err := store.RecordGuardDegradedEvent(&state.GuardDegradedEvent{
			RepoPath:   "/repo",
			ProjectKey: "/repo",
			Reason:     reason,
			Detail:     "simulated failure",
		}); err != nil {
			_ = store.Close()
			t.Fatalf("RecordGuardDegradedEvent: %v", err)
		}
	}
	_ = store.Close()

	// Write a config file containing a removed key so Configuration reports an error -> Overall="unhealthy".
	badCfgPath := filepath.Join(t.TempDir(), "config.toml")
	if err := os.WriteFile(badCfgPath, []byte("[tmux]\npalette_key = \"F12\"\n"), 0o644); err != nil {
		t.Fatalf("write bad config: %v", err)
	}

	out, err := GetDoctor(context.Background(), DoctorOptions{
		Config:     config.Default(),
		ConfigPath: badCfgPath,
	})
	if err != nil {
		t.Fatalf("GetDoctor: %v", err)
	}
	if out.GuardHook == nil || out.GuardHook.Status != "warning" || out.GuardHook.DegradedRuns != 2 || out.GuardHook.Since == "" {
		t.Fatalf("expected warning GuardHook with 2 degraded runs, got %+v", out.GuardHook)
	}
	if out.Overall != "unhealthy" || out.Success || out.ErrorCode != ErrCodeInternalError || out.Errors == 0 {
		t.Fatalf("expected unhealthy envelope for removed config key, got Overall=%q Success=%v ErrorCode=%q Errors=%d",
			out.Overall, out.Success, out.ErrorCode, out.Errors)
	}
}

func TestDoctor_ClassifyAndFindContentionHolders(t *testing.T) {
	t.Parallel()
	got := ClassifyContentionProcess(DoctorContentionProcess{
		Command:   "/usr/local/bin/cargo build --release && rsync -a target/ out/",
		Status:    "D",
		OpenFiles: []string{"/data/projects/ntm/.beads/beads.db-wal"},
	})
	want := []string{"beads_db", "cargo_registry", "rsync", "uninterruptible_io"}
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Fatalf("ClassifyContentionProcess = %v, want %v", got, want)
	}

	brOnly := ClassifyContentionProcess(DoctorContentionProcess{
		Command: "/usr/local/bin/br sync --flush-only",
	})
	if len(brOnly) != 1 || brOnly[0] != "beads_db" {
		t.Fatalf("ClassifyContentionProcess(br) = %v, want [beads_db]", brOnly)
	}

	if none := ClassifyContentionProcess(DoctorContentionProcess{Command: "go test ./internal/robot"}); len(none) != 0 {
		t.Fatalf("expected empty classification for go test, got %v", none)
	}

	holders, err := FindContentionHolders(context.Background(), t.TempDir())
	if err != nil {
		t.Fatalf("FindContentionHolders: %v", err)
	}
	if holders == nil {
		t.Fatal("FindContentionHolders must return non-nil slice")
	}

	docWithContention, err := GetDoctor(context.Background(), DoctorOptions{
		Config:            config.Default(),
		IncludeContention: true,
		SwarmCWD:          t.TempDir(),
	})
	if err != nil {
		t.Fatalf("GetDoctor(IncludeContention): %v", err)
	}
	if docWithContention.Contention == nil {
		t.Fatal("Contention slice must be non-nil")
	}
}

func TestDoctor_TerminateContentionHolderGuardsAndExecution(t *testing.T) {
	t.Parallel()
	holders := []DoctorContentionHolder{
		{PID: 424242, InSwarm: true, Resource: "beads_db", Command: "br ready"},
	}

	// Zero/negative PID is a safe no-op.
	if err := TerminateContentionHolder(context.Background(), holders, 0, false, false); err != nil {
		t.Fatalf("TerminateContentionHolder(0): %v", err)
	}
	// Refuses without --confirm.
	if err := TerminateContentionHolder(context.Background(), holders, 424242, false, false); err == nil || !strings.Contains(err.Error(), "--confirm") {
		t.Fatalf("expected --confirm error, got %v", err)
	}
	// Refuses in-swarm holder without --force.
	if err := TerminateContentionHolder(context.Background(), holders, 424242, true, false); err == nil || !strings.Contains(err.Error(), "--force") {
		t.Fatalf("expected --force error for in-swarm PID, got %v", err)
	}
	// Refuses unlisted PID.
	if err := TerminateContentionHolder(context.Background(), holders, 999999, true, true); err == nil || !strings.Contains(err.Error(), "not a detected contention holder") {
		t.Fatalf("expected unlisted PID error, got %v", err)
	}

	// Real child process termination when confirmed + non-swarm.
	sleepPath, err := exec.LookPath("sleep")
	if err == nil {
		cmd := exec.Command(sleepPath, "30")
		if err := cmd.Start(); err == nil {
			pid := cmd.Process.Pid
			t.Cleanup(func() {
				_ = cmd.Process.Kill()
				_ = cmd.Wait()
			})
			liveHolders := []DoctorContentionHolder{
				{PID: pid, InSwarm: false, Resource: "rsync", Command: "sleep 30"},
			}
			if termErr := TerminateContentionHolder(context.Background(), liveHolders, pid, true, false); termErr != nil {
				t.Fatalf("TerminateContentionHolder(live sleep pid=%d): %v", pid, termErr)
			}
			_ = cmd.Wait()
		}
	}
}

func TestDoctor_CancelledContextAndRegistrySchema(t *testing.T) {
	t.Parallel()
	cancelledCtx, cancel := context.WithCancel(context.Background())
	cancel()
	out, err := GetDoctor(cancelledCtx, DoctorOptions{})
	if err != nil {
		t.Fatalf("GetDoctor(cancelled): %v", err)
	}
	if out.Success || out.ErrorCode != ErrCodeTimeout || out.Tools == nil || out.Contention == nil {
		t.Fatalf("cancelled GetDoctor = %+v, want TIMEOUT with non-nil slices", out)
	}

	surface, ok := GetRobotRegistry().Surface("doctor")
	if !ok {
		t.Fatal("expected 'doctor' surface in robot registry")
	}
	if surface.SchemaID != doctorSchemaID || surface.SchemaType != "doctor" || surface.Flag != "--robot-doctor" {
		t.Fatalf("unexpected doctor surface metadata: %+v", surface)
	}
	schemaOut, err := GetSchema("doctor")
	if err != nil || schemaOut == nil || !schemaOut.Success {
		t.Fatalf("GetSchema(doctor) = (%+v, %v), want valid schema", schemaOut, err)
	}
}

func TestDoctor_ScopesBeadsAndGuardChecksToSwarmCWD(t *testing.T) {
	homeDir := t.TempDir()
	xdgDir := t.TempDir()
	t.Setenv("HOME", homeDir)
	t.Setenv("XDG_CONFIG_HOME", xdgDir)
	t.Setenv("NTM_CONFIG", "")

	noBeadsDir := t.TempDir()
	out, err := GetDoctor(context.Background(), DoctorOptions{
		Config:   config.Default(),
		SwarmCWD: noBeadsDir,
	})
	if err != nil {
		t.Fatalf("GetDoctor(noBeadsDir): %v", err)
	}
	foundBeadsWarning := false
	for _, c := range out.Configuration {
		if c.Name == ".beads directory" && c.Status == "warning" {
			foundBeadsWarning = true
		}
	}
	if !foundBeadsWarning {
		t.Fatalf("expected .beads directory warning for empty SwarmCWD %s, got %+v", noBeadsDir, out.Configuration)
	}

	withBeadsDir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(withBeadsDir, ".beads"), 0o755); err != nil {
		t.Fatal(err)
	}
	out2, err := GetDoctor(context.Background(), DoctorOptions{
		Config:   config.Default(),
		SwarmCWD: withBeadsDir,
	})
	if err != nil {
		t.Fatalf("GetDoctor(withBeadsDir): %v", err)
	}
	foundBeadsOK := false
	for _, c := range out2.Configuration {
		if c.Name == ".beads directory" && c.Status == "ok" && c.Valid {
			foundBeadsOK = true
		}
	}
	if !foundBeadsOK {
		t.Fatalf("expected .beads directory ok for SwarmCWD %s, got %+v", withBeadsDir, out2.Configuration)
	}
}
