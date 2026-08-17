package robot

import (
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"testing"

	"github.com/Dicklesworthstone/ntm/internal/tools"
)

func TestReadAuditLogStats_EmptyLog(t *testing.T) {
	tmpDir := t.TempDir()
	logPath := filepath.Join(tmpDir, "empty.jsonl")

	// Create empty file
	if err := os.WriteFile(logPath, []byte{}, 0644); err != nil {
		t.Fatalf("Failed to create empty log: %v", err)
	}

	checked, blocked, lastBlocked := readAuditLogStats(logPath)

	if checked != 0 {
		t.Errorf("Expected checked 0, got %d", checked)
	}
	if blocked != 0 {
		t.Errorf("Expected blocked 0, got %d", blocked)
	}
	if lastBlocked != nil {
		t.Errorf("Expected nil lastBlocked, got %+v", lastBlocked)
	}
}

func TestReadAuditLogStats_WithEntries(t *testing.T) {
	tmpDir := t.TempDir()
	logPath := filepath.Join(tmpDir, "test.jsonl")

	// Mixed log: three checked cycles, two of which were blocked.
	content := `{"timestamp":"2024-01-15T10:29:00Z","event":"command_checked","command":"ls -la","pane":"robot-dcg-check","session":"","rule":"","dcg_output":"allowed"}
{"timestamp":"2024-01-15T10:30:00Z","event":"command_blocked","command":"rm -rf /","pane":"agent-1","session":"test","rule":"rm_rf_root","dcg_output":"Blocked"}
{"timestamp":"2024-01-15T10:31:00Z","event":"command_blocked","command":"git reset --hard","pane":"agent-2","session":"test","rule":"git_reset","dcg_output":"Blocked"}
`
	if err := os.WriteFile(logPath, []byte(content), 0644); err != nil {
		t.Fatalf("Failed to create log: %v", err)
	}

	checked, blocked, lastBlocked := readAuditLogStats(logPath)

	if checked != 3 {
		t.Errorf("Expected checked 3 (1 checked + 2 blocked entries), got %d", checked)
	}
	if blocked != 2 {
		t.Errorf("Expected blocked 2, got %d", blocked)
	}
	if lastBlocked == nil {
		t.Fatal("Expected non-nil lastBlocked")
	}
	if lastBlocked.Command != "git reset --hard" {
		t.Errorf("Expected command 'git reset --hard', got '%s'", lastBlocked.Command)
	}
	if lastBlocked.Pane != "agent-2" {
		t.Errorf("Expected pane 'agent-2', got '%s'", lastBlocked.Pane)
	}
}

func TestReadAuditLogStats_NonExistent(t *testing.T) {
	checked, blocked, lastBlocked := readAuditLogStats("/nonexistent/path/log.jsonl")

	if checked != 0 {
		t.Errorf("Expected checked 0 for non-existent file, got %d", checked)
	}
	if blocked != 0 {
		t.Errorf("Expected blocked 0 for non-existent file, got %d", blocked)
	}
	if lastBlocked != nil {
		t.Errorf("Expected nil lastBlocked for non-existent file")
	}
}

func TestGetDefaultAuditLogPath(t *testing.T) {
	path := getDefaultAuditLogPath()

	if path == "" {
		t.Error("Expected non-empty default audit log path")
	}

	// Should contain "ntm" and "dcg-audit.jsonl"
	if filepath.Base(path) != "dcg-audit.jsonl" {
		t.Errorf("Expected path to end with dcg-audit.jsonl, got %s", filepath.Base(path))
	}
}

// writeDCGConfigFixture writes an NTM config file whose DCG values all DIFFER
// from the values that dcg_status.go used to hardcode (AllowOverride=false,
// blocklist/whitelist counts 0, audit log at the built-in default path). A
// fixture equal to those hardcoded values would pass against the bug.
func writeDCGConfigFixture(t *testing.T, auditLogPath string) string {
	t.Helper()

	dir := t.TempDir()
	cfgPath := filepath.Join(dir, "config.toml")
	content := fmt.Sprintf(`[integrations.dcg]
enabled = true
allow_override = true
custom_blocklist = ["dd if=", "mkfs", "shred"]
custom_whitelist = ["rm -rf ./build", "git clean -fd"]
audit_log = %q
`, auditLogPath)
	if err := os.WriteFile(cfgPath, []byte(content), 0644); err != nil {
		t.Fatalf("write config fixture: %v", err)
	}
	return cfgPath
}

// TestGetDCGStatus_ReportsRealConfig proves the status surface reports the
// operator's real DCG configuration, not the previously hardcoded
// AllowOverride=false / 0 / 0 placeholder values.
func TestGetDCGStatus_ReportsRealConfig(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("PATH shim test uses a unix shell script")
	}

	tools.NewDCGAdapter().InvalidateAvailabilityCache()
	t.Cleanup(func() { tools.NewDCGAdapter().InvalidateAvailabilityCache() })

	binDir := t.TempDir()
	writeFakeDCG(t, binDir)
	t.Setenv("PATH", binDir)

	auditLog := filepath.Join(t.TempDir(), "dcg-audit.jsonl")
	// Fixture audit log: 5 checked cycles, 2 of which were blocked. Both
	// counters must differ from the hardcoded zeros.
	content := ""
	for i := 0; i < 3; i++ {
		content += fmt.Sprintf(`{"timestamp":"2024-01-15T10:0%d:00Z","event":"command_checked","command":"echo %d","pane":"robot-dcg-check","session":"","rule":"","dcg_output":"allowed"}`+"\n", i, i)
	}
	content += `{"timestamp":"2024-01-15T10:10:00Z","event":"command_blocked","command":"rm -rf /","pane":"agent-1","session":"s","rule":"rm_rf_root","dcg_output":"Blocked"}` + "\n"
	content += `{"timestamp":"2024-01-15T10:11:00Z","event":"command_blocked","command":"dd if=/dev/zero","pane":"agent-2","session":"s","rule":"dd","dcg_output":"Blocked"}` + "\n"
	if err := os.WriteFile(auditLog, []byte(content), 0644); err != nil {
		t.Fatalf("write audit log fixture: %v", err)
	}

	t.Setenv("NTM_CONFIG", writeDCGConfigFixture(t, auditLog))
	// Keep the repo's project overlay (.ntm/config.toml) out of the merge.
	t.Chdir(t.TempDir())

	out, err := GetDCGStatus()
	if err != nil {
		t.Fatalf("GetDCGStatus: %v", err)
	}
	if !out.Success {
		t.Fatalf("expected success=true, got error %q", out.Error)
	}

	cfg := out.DCG.Config
	if !cfg.AllowOverride {
		t.Errorf("AllowOverride=false: hardcoded default reported instead of fixture value true")
	}
	if cfg.CustomBlocklistCount != 3 {
		t.Errorf("CustomBlocklistCount=%d, want 3 (fixture blocklist entries)", cfg.CustomBlocklistCount)
	}
	if cfg.CustomWhitelistCount != 2 {
		t.Errorf("CustomWhitelistCount=%d, want 2 (fixture whitelist entries)", cfg.CustomWhitelistCount)
	}
	if cfg.AuditLog != auditLog {
		t.Errorf("AuditLog=%q, want fixture path %q", cfg.AuditLog, auditLog)
	}

	stats := out.DCG.Stats
	if stats.CommandsChecked != 5 {
		t.Errorf("CommandsChecked=%d, want 5 (3 checked + 2 blocked entries)", stats.CommandsChecked)
	}
	if stats.CommandsBlocked != 2 {
		t.Errorf("CommandsBlocked=%d, want 2", stats.CommandsBlocked)
	}
	if stats.LastBlocked == nil || stats.LastBlocked.Command != "dd if=/dev/zero" {
		t.Errorf("LastBlocked=%+v, want last blocked command 'dd if=/dev/zero'", stats.LastBlocked)
	}
}

// TestGetDCGStatus_CommandsCheckedCountsExactCycles drives N commands through
// the robot check cycle and asserts CommandsChecked equals exactly N.
func TestGetDCGStatus_CommandsCheckedCountsExactCycles(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("PATH shim test uses a unix shell script")
	}

	tools.NewDCGAdapter().InvalidateAvailabilityCache()
	t.Cleanup(func() { tools.NewDCGAdapter().InvalidateAvailabilityCache() })

	binDir := t.TempDir()
	writeFakeDCG(t, binDir)
	t.Setenv("PATH", binDir)

	auditLog := filepath.Join(t.TempDir(), "dcg-audit.jsonl")
	t.Setenv("NTM_CONFIG", writeDCGConfigFixture(t, auditLog))
	t.Chdir(t.TempDir())

	// N=4 check cycles: three allowed, one blocked by the fake dcg policy.
	commands := []string{"echo one", "ls -la", "rm -rf /tmp/scratch", "echo two"}
	for _, cmd := range commands {
		out, err := GetDCGCheckWithOptions(DCGCheckOptions{Command: cmd})
		if err != nil {
			t.Fatalf("GetDCGCheckWithOptions(%q): %v", cmd, err)
		}
		if !out.Success {
			t.Fatalf("GetDCGCheckWithOptions(%q) success=false: %s", cmd, out.Error)
		}
	}

	out, err := GetDCGStatus()
	if err != nil {
		t.Fatalf("GetDCGStatus: %v", err)
	}
	if got, want := out.DCG.Stats.CommandsChecked, len(commands); got != want {
		t.Errorf("CommandsChecked=%d, want exactly %d (one per check cycle)", got, want)
	}
	// Robot check cycles record command_checked events only; blocked-command
	// audit entries come from the send path.
	if out.DCG.Stats.CommandsBlocked != 0 {
		t.Errorf("CommandsBlocked=%d, want 0 (no send-path blocks recorded)", out.DCG.Stats.CommandsBlocked)
	}
}
