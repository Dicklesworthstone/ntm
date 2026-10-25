package bv

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

type doneObservedContext struct {
	context.Context
	doneObserved chan<- struct{}
}

func (c doneObservedContext) Done() <-chan struct{} {
	select {
	case c.doneObserved <- struct{}{}:
	default:
	}
	return c.Context.Done()
}

type retryWaitObservedContext struct {
	context.Context
	retryWaitEntered chan<- time.Duration
	continueRetry    <-chan struct{}
}

func (c retryWaitObservedContext) observeBeadsRetryWaitForTest(delay time.Duration) {
	select {
	case c.retryWaitEntered <- delay:
	case <-c.Context.Done():
		return
	}
	select {
	case <-c.continueRetry:
	case <-c.Context.Done():
	}
}

func waitForTestFile(t *testing.T, path, description string) {
	t.Helper()
	deadline := time.NewTimer(10 * time.Second)
	defer deadline.Stop()
	ticker := time.NewTicker(5 * time.Millisecond)
	defer ticker.Stop()

	for {
		if _, err := os.Stat(path); err == nil {
			return
		} else if !os.IsNotExist(err) {
			t.Fatalf("inspect %s: %v", description, err)
		}
		select {
		case <-ticker.C:
		case <-deadline.C:
			t.Fatalf("timed out waiting for %s", description)
		}
	}
}

const beadsRecoveryTombstoneWarning = "the tombstone issue IDs could not be read before the rebuild (Database error: database disk image is malformed); preservation of unflushed local state could not be established"

const beadsRebuildPreservationConflict = `{"error":{"code":"SYNC_CONFLICT","message":"Sync conflict: Automatic database recovery refused because unflushed-state preservation could not be established:\n- the tombstone issue IDs could not be read before the rebuild (Database error: database disk image is malformed); preservation of unflushed local state could not be established\nThe database was not replaced. Run br doctor --repair to inspect retained evidence."}}`

func beadsRecoveryJSON(t *testing.T, value any) string {
	t.Helper()
	data, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	return string(data)
}

func beadsRecoveryDoctorReport() map[string]any {
	return map[string]any{
		"ok": false, "exit_code": 2, "code": "fix_partial", "repaired": true,
		"database_verified": true, "preservation_verified": false, "verified": false,
		"skipped": 0, "fk_violations_cleaned": 0,
		"history_preservation_warnings": []string{},
		"dirty_preservation_warnings":   []string{beadsRecoveryTombstoneWarning},
		"preserved_history":             []any{map[string]any{"table": "events", "restored": 1, "skipped": 0}},
		"verified_backups":              []any{map[string]any{"backup": "/retained/beads.db.bak"}},
		"recovery_failure_marker":       "/retained/beads.db.verification-failed.json",
		"recovery_failure_marker_error": nil,
	}
}

type beadsRecoveryTestCommand struct {
	stdout   string
	stderr   string
	exitCode int
}

type beadsRecoveryTestWorkspace struct {
	dir           string
	calls         string
	doctorStarted string
	jsonl         string
	jsonlContents string
}

// Run the actual subprocess wrapper so exit status, separate output streams,
// workspace locking and the no-database cache all participate in recovery.
func setupBeadsRecoveryCommands(t *testing.T, rebuild, doctor beadsRecoveryTestCommand) beadsRecoveryTestWorkspace {
	t.Helper()
	databasePath, db := assignmentMutexFixture(t)
	mutexSQL(t, db, `CREATE INDEX idx_issues_status ON issues(status)`)
	mutexSQL(t, db, `CREATE INDEX idx_issues_tombstone ON issues(status) WHERE status = 'tombstone'`)
	mutexIssue(t, db, "ntm-preserved", "tombstone", "")
	dir := filepath.Dir(databasePath)
	for _, name := range []string{".git", ".beads"} {
		if err := os.Mkdir(filepath.Join(dir, name), 0o755); err != nil {
			t.Fatalf("mkdir %s: %v", name, err)
		}
	}
	workspace := beadsRecoveryTestWorkspace{
		dir:           dir,
		calls:         filepath.Join(dir, "br-calls"),
		doctorStarted: filepath.Join(dir, "doctor-started"),
		jsonl:         filepath.Join(dir, ".beads", "issues.jsonl"),
		jsonlContents: "{\"id\":\"ntm-preserved\",\"status\":\"tombstone\"}\n",
	}
	if err := os.WriteFile(workspace.jsonl, []byte(workspace.jsonlContents), 0o600); err != nil {
		t.Fatalf("write authoritative JSONL: %v", err)
	}
	normalizedDir, err := normalizeTriageDir(dir)
	if err != nil {
		t.Fatalf("normalize recovery workspace: %v", err)
	}
	workspace.dir = normalizedDir
	setNoDBState(normalizedDir, true)
	t.Cleanup(func() { setNoDBState(normalizedDir, false) })
	for key, value := range map[string]string{
		"NTM_TEST_BR_CALLS":       workspace.calls,
		"NTM_TEST_BR_INFO":        beadsRecoveryJSON(t, map[string]string{"database_path": databasePath, "jsonl_path": workspace.jsonl}),
		"NTM_TEST_DOCTOR_STARTED": workspace.doctorStarted,
		"NTM_TEST_REBUILD_STDOUT": rebuild.stdout,
		"NTM_TEST_REBUILD_STDERR": rebuild.stderr,
		"NTM_TEST_REBUILD_EXIT":   fmt.Sprint(rebuild.exitCode),
		"NTM_TEST_DOCTOR_STDOUT":  doctor.stdout,
		"NTM_TEST_DOCTOR_STDERR":  doctor.stderr,
		"NTM_TEST_DOCTOR_EXIT":    fmt.Sprint(doctor.exitCode),
		"NTM_TEST_DOCTOR_BLOCK":   "",
	} {
		t.Setenv(key, value)
	}
	binDir := t.TempDir()
	script := `#!/bin/sh
printf '%s\t%s\n' "$PWD" "$*" >> "$NTM_TEST_BR_CALLS"
case "$*" in
  '--lock-timeout 5000 show task-1 --json')
    printf '%s' "$NTM_TEST_READ_STDOUT"
    printf '%s' "$NTM_TEST_READ_STDERR" >&2
    exit "${NTM_TEST_READ_EXIT:-0}"
    ;;
  '--lock-timeout 5000 info --json --no-auto-import --no-auto-flush')
    printf '%s' "$NTM_TEST_BR_INFO"
    ;;
  '--lock-timeout 5000 sync --import-only --rebuild --json')
    printf '%s' "$NTM_TEST_REBUILD_STDOUT"
    printf '%s' "$NTM_TEST_REBUILD_STDERR" >&2
    exit "$NTM_TEST_REBUILD_EXIT"
    ;;
  '--lock-timeout 5000 doctor --repair --json')
    printf '%s' "$NTM_TEST_DOCTOR_STDOUT"
    printf '%s' "$NTM_TEST_DOCTOR_STDERR" >&2
    printf 'started\n' > "$NTM_TEST_DOCTOR_STARTED"
    if [ "$NTM_TEST_DOCTOR_BLOCK" = true ]; then
      exec sleep 30
    fi
    exit "$NTM_TEST_DOCTOR_EXIT"
    ;;
  *)
    printf 'unexpected recovery command: %s\n' "$*" >&2
    exit 99
    ;;
esac
`
	if err := os.WriteFile(filepath.Join(binDir, "br"), []byte(script), 0o700); err != nil {
		t.Fatalf("write fake br: %v", err)
	}
	t.Setenv("PATH", binDir+string(os.PathListSeparator)+os.Getenv("PATH"))
	return workspace
}

func (w beadsRecoveryTestWorkspace) assertCommands(t *testing.T, commands ...string) {
	t.Helper()
	contents, err := os.ReadFile(w.calls)
	if err != nil && !(os.IsNotExist(err) && len(commands) == 0) {
		t.Fatalf("read recovery commands: %v", err)
	}
	want := ""
	for _, command := range commands {
		want += w.dir + "\t--lock-timeout 5000 " + command + "\n"
	}
	if string(contents) != want {
		t.Fatalf("recovery commands=%q, want %q", contents, want)
	}
	jsonl, err := os.ReadFile(w.jsonl)
	if err != nil || string(jsonl) != w.jsonlContents {
		t.Fatalf("authoritative JSONL changed: contents=%q error=%v", jsonl, err)
	}
}

// The read-recovery boundary requires both files to resolve inside .beads.
// Retain the shared claim fixture and copy its committed SQLite contents into
// a configured local filename so this also exercises non-default DB names.
func setupBeadsReadRecoveryCommands(t *testing.T, doctor beadsRecoveryTestCommand) beadsRecoveryTestWorkspace {
	t.Helper()
	w := setupBeadsRecoveryCommands(t,
		beadsRecoveryTestCommand{stderr: beadsRebuildPreservationConflict, exitCode: 6}, doctor)
	var info beadsWorkspaceInfo
	if err := json.Unmarshal([]byte(os.Getenv("NTM_TEST_BR_INFO")), &info); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(info.DatabasePath)
	if err != nil {
		t.Fatal(err)
	}
	info.DatabasePath = filepath.Join(w.dir, ".beads", "configured-index.db")
	if err := os.WriteFile(info.DatabasePath, data, 0600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("NTM_TEST_BR_INFO", beadsRecoveryJSON(t, info))
	t.Setenv("NTM_TEST_READ_STDOUT", `{"error":{"code":"DATABASE_ERROR","message":"database disk image is malformed"}}`)
	t.Setenv("NTM_TEST_READ_STDERR", "")
	t.Setenv("NTM_TEST_READ_EXIT", "2")
	setNoDBState(w.dir, false)
	return w
}

func TestReadForMutationContextRepairsOnlyFailedBeadsReads(t *testing.T) {
	for _, test := range []struct {
		name         string
		preservation bool
		unverified   bool
		persistent   bool
	}{
		{name: "verified partial repair with configured database filename"},
		{name: "preservation refusal from initial read", preservation: true},
		{name: "unverified repair stops before retry", unverified: true},
		{name: "persistent corruption gets only one repair", persistent: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			report := beadsRecoveryDoctorReport()
			if test.unverified {
				report["database_verified"] = false
			}
			w := setupBeadsReadRecoveryCommands(t, beadsRecoveryTestCommand{stdout: beadsRecoveryJSON(t, report), exitCode: 2})
			if test.preservation {
				t.Setenv("NTM_TEST_READ_STDOUT", beadsRebuildPreservationConflict)
				t.Setenv("NTM_TEST_READ_EXIT", "6")
			}
			reads := 0
			result, err := ReadForMutationContext(t.Context(), w.dir, func() (string, error) {
				reads++
				if reads == 1 || test.persistent {
					return RunBdContext(t.Context(), w.dir, "show", "task-1", "--json")
				}
				return "fresh evidence", nil
			})
			wantReads := 2
			if test.unverified {
				wantReads = 1
			}
			if reads != wantReads || (err != nil) != (test.unverified || test.persistent) {
				t.Fatalf("result=%q error=%v reads=%d, want reads=%d", result, err, reads, wantReads)
			}
			if err == nil && result != "fresh evidence" {
				t.Fatalf("result=%q, want newly collected evidence", result)
			}
			commands := []string{"show task-1 --json", "info --json --no-auto-import --no-auto-flush", "sync --import-only --rebuild --json", "info --json --no-auto-import --no-auto-flush", "doctor --repair --json"}
			if test.persistent {
				commands = append(commands, "show task-1 --json")
			}
			w.assertCommands(t, commands...)
		})
	}
}

func TestReadForMutationContextLeavesUnrelatedErrorsUntouched(t *testing.T) {
	for _, output := range []string{
		`{"error":{"code":"CONFIG_ERROR","message":"database disk image is malformed"}}`,
		`{"error":{"code":"DATABASE_ERROR","message":"permission denied"}}`,
		`{"error":{"code":"SYNC_CONFLICT","message":"JSONL changed concurrently"}}`,
		`database disk image is malformed`,
		`{"error":{"code":"DATABASE_ERROR","message":"database disk image is malformed"}} trailing task text`,
	} {
		t.Run(output, func(t *testing.T) {
			w := setupBeadsReadRecoveryCommands(t, beadsRecoveryTestCommand{})
			t.Setenv("NTM_TEST_READ_STDOUT", output)
			reads := 0
			var originalErr error
			_, err := ReadForMutationContext(t.Context(), w.dir, func() (string, error) {
				reads++
				result, readErr := RunBdContext(t.Context(), w.dir, "show", "task-1", "--json")
				originalErr = readErr
				return result, readErr
			})
			if err == nil || err != originalErr || reads != 1 {
				t.Fatalf("error=%v original=%v reads=%d, want unchanged read failure", err, originalErr, reads)
			}
			w.assertCommands(t, "show task-1 --json")
		})
	}
	for name, originalErr := range map[string]error{
		"healthy read":        nil,
		"parser diagnostic":   errors.New("task title contains database disk image is malformed"),
		"eligibility refusal": fmt.Errorf("database corruption: %w", ErrBeadAssignmentIneligible),
		"ownership refusal":   fmt.Errorf("database disk image is malformed: %w", ErrBeadAlreadyClaimed),
		"terminal refusal":    fmt.Errorf("file is not a database: %w", ErrBeadTerminal),
	} {
		t.Run(name, func(t *testing.T) {
			w := setupBeadsReadRecoveryCommands(t, beadsRecoveryTestCommand{})
			reads := 0
			result, err := ReadForMutationContext(t.Context(), w.dir, func() (string, error) {
				reads++
				return "original evidence", originalErr
			})
			if err != originalErr || result != "original evidence" || reads != 1 {
				t.Fatalf("result=%q error=%v reads=%d", result, err, reads)
			}
			w.assertCommands(t)
		})
	}
}

func TestReadForMutationContextDoesNotRepairInheritedOrRedirectedDatabase(t *testing.T) {
	for _, kind := range []string{"parent workspace", "symlinked beads directory", "external database", "external JSONL"} {
		t.Run(kind, func(t *testing.T) {
			w := setupBeadsReadRecoveryCommands(t, beadsRecoveryTestCommand{})
			readDir := w.dir
			commands := []string{"show task-1 --json"}
			switch kind {
			case "parent workspace":
				readDir = filepath.Join(w.dir, "child")
				if err := os.Mkdir(readDir, 0755); err != nil {
					t.Fatal(err)
				}
			case "symlinked beads directory":
				readDir = t.TempDir()
				if err := os.Mkdir(filepath.Join(readDir, ".git"), 0755); err != nil {
					t.Fatal(err)
				}
				if err := os.Symlink(filepath.Join(w.dir, ".beads"), filepath.Join(readDir, ".beads")); err != nil {
					t.Fatal(err)
				}
				w.dir = readDir
			default:
				var info beadsWorkspaceInfo
				if err := json.Unmarshal([]byte(os.Getenv("NTM_TEST_BR_INFO")), &info); err != nil {
					t.Fatal(err)
				}
				external := filepath.Join(t.TempDir(), "outside")
				if err := os.WriteFile(external, []byte("unrelated workspace evidence"), 0600); err != nil {
					t.Fatal(err)
				}
				redirect := filepath.Join(w.dir, ".beads", "redirect")
				if err := os.Symlink(external, redirect); err != nil {
					t.Fatal(err)
				}
				if kind == "external database" {
					info.DatabasePath = redirect
				} else {
					info.JSONLPath = redirect
				}
				t.Setenv("NTM_TEST_BR_INFO", beadsRecoveryJSON(t, info))
				commands = append(commands, "info --json --no-auto-import --no-auto-flush")
			}
			reads := 0
			_, err := ReadForMutationContext(t.Context(), readDir, func() (string, error) {
				reads++
				return RunBdContext(t.Context(), readDir, "show", "task-1", "--json")
			})
			if err == nil || !strings.Contains(err.Error(), "refusing database recovery") || reads != 1 {
				t.Fatalf("error=%v reads=%d, want unsafe recovery refused", err, reads)
			}
			w.assertCommands(t, commands...)
		})
	}
}

func TestReadForMutationContextCancellationPreventsReadAndRetry(t *testing.T) {
	t.Run("inactive context and missing callback", func(t *testing.T) {
		ctx, cancel := context.WithCancel(t.Context())
		cancel()
		for _, ctx := range []context.Context{nil, ctx} {
			_, err := ReadForMutationContext(ctx, t.TempDir(), func() (bool, error) {
				t.Fatal("read called for an inactive context")
				return false, nil
			})
			if err == nil {
				t.Fatal("inactive context accepted")
			}
		}
		if _, err := ReadForMutationContext[bool](t.Context(), t.TempDir(), nil); err == nil {
			t.Fatal("nil reader accepted")
		}
	})
	t.Run("canceled after initial read", func(t *testing.T) {
		w := setupBeadsReadRecoveryCommands(t, beadsRecoveryTestCommand{})
		ctx, cancel := context.WithCancel(t.Context())
		defer cancel()
		_, err := ReadForMutationContext(ctx, w.dir, func() (string, error) {
			result, err := RunBdContext(ctx, w.dir, "show", "task-1", "--json")
			cancel()
			return result, err
		})
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("error=%v, want canceled read", err)
		}
		w.assertCommands(t, "show task-1 --json")
	})
	t.Run("canceled during doctor", func(t *testing.T) {
		w := setupBeadsReadRecoveryCommands(t, beadsRecoveryTestCommand{stdout: beadsRecoveryJSON(t, beadsRecoveryDoctorReport()), exitCode: 2})
		t.Setenv("NTM_TEST_DOCTOR_BLOCK", "true")
		ctx, cancel := context.WithCancel(t.Context())
		defer cancel()
		reads := 0
		done := make(chan error, 1)
		go func() {
			_, err := ReadForMutationContext(ctx, w.dir, func() (string, error) {
				reads++
				return RunBdContext(ctx, w.dir, "show", "task-1", "--json")
			})
			done <- err
		}()
		waitForTestFile(t, w.doctorStarted, "read recovery doctor")
		cancel()
		select {
		case err := <-done:
			if !errors.Is(err, context.Canceled) || reads != 1 {
				t.Fatalf("error=%v reads=%d, want canceled recovery without reread", err, reads)
			}
		case <-time.After(5 * time.Second):
			t.Fatal("read recovery ignored cancellation")
		}
		w.assertCommands(t, "show task-1 --json", "info --json --no-auto-import --no-auto-flush", "sync --import-only --rebuild --json", "info --json --no-auto-import --no-auto-flush", "doctor --repair --json")
	})
}

func TestBeadsCorruptionRecoveryRequiresPreservationConflict(t *testing.T) {
	tests := []struct {
		name    string
		rebuild beadsRecoveryTestCommand
		wantErr bool
	}{
		{name: "successful rebuild", rebuild: beadsRecoveryTestCommand{stdout: `{"imported":1}`}},
		{name: "unrelated sync conflict", rebuild: beadsRecoveryTestCommand{stderr: `{"error":{"code":"SYNC_CONFLICT","message":"JSONL was modified concurrently"}}`, exitCode: 6}, wantErr: true},
		{name: "preservation wording with wrong code", rebuild: beadsRecoveryTestCommand{stderr: strings.ReplaceAll(beadsRebuildPreservationConflict, "SYNC_CONFLICT", "CONFIG_ERROR"), exitCode: 6}, wantErr: true},
		{name: "unstructured preservation error", rebuild: beadsRecoveryTestCommand{stderr: "SYNC_CONFLICT: Automatic database recovery refused because unflushed-state preservation could not be established: tombstones unreadable", exitCode: 6}, wantErr: true},
		{name: "ordinary command failure", rebuild: beadsRecoveryTestCommand{stderr: "permission denied", exitCode: 1}, wantErr: true},
		{name: "wrong process exit", rebuild: beadsRecoveryTestCommand{stdout: beadsRebuildPreservationConflict, exitCode: 1}, wantErr: true},
		{name: "code alone", rebuild: beadsRecoveryTestCommand{stdout: `{"error":{"code":"SYNC_CONFLICT"}}`, exitCode: 6}, wantErr: true},
		{name: "malformed JSON", rebuild: beadsRecoveryTestCommand{stdout: beadsRebuildPreservationConflict + "trailing diagnostic", exitCode: 6}, wantErr: true},
		{name: "noncorrupt tombstone failure", rebuild: beadsRecoveryTestCommand{stdout: strings.ReplaceAll(beadsRebuildPreservationConflict, "database disk image is malformed", "permission denied"), exitCode: 6}, wantErr: true},
		{name: "unreadable dirty row", rebuild: beadsRecoveryTestCommand{stdout: strings.ReplaceAll(beadsRebuildPreservationConflict, `\nThe database`, `\n- dirty issue task-1 could not be read\nThe database`), exitCode: 6}, wantErr: true},
		{name: "unreadable dirty bitmap", rebuild: beadsRecoveryTestCommand{stdout: strings.ReplaceAll(beadsRebuildPreservationConflict, `\n- the tombstone`, `\n- dirty issue IDs could not be read\n- the tombstone`), exitCode: 6}, wantErr: true},
		{name: "unreadable relationships", rebuild: beadsRecoveryTestCommand{stdout: strings.ReplaceAll(beadsRebuildPreservationConflict, `\nThe database`, `\n- dependencies could not be preserved\nThe database`), exitCode: 6}, wantErr: true},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			workspace := setupBeadsRecoveryCommands(t, test.rebuild, beadsRecoveryTestCommand{})
			attempts := 0
			result, err := withBeadsCorruptionRecovery(t.Context(), workspace.dir, func() (string, error) {
				attempts++
				if attempts == 1 {
					return "", errors.New("database disk image is malformed (11)")
				}
				return "claimed", nil
			})
			if test.wantErr {
				var processErr *exec.ExitError
				if err == nil || attempts != 1 || result != "" || !errors.As(err, &processErr) {
					t.Fatalf("result=%q error=%v attempts=%d, want failed recovery without retry", result, err, attempts)
				}
			} else if err != nil || attempts != 2 || result != "claimed" {
				t.Fatalf("result=%q error=%v attempts=%d, want one successful retry", result, err, attempts)
			}
			workspace.assertCommands(t, "sync --import-only --rebuild --json")
		})
	}
}

func TestBeadsCorruptionRecoveryValidatesDoctorRepair(t *testing.T) {
	partial := beadsRecoveryJSON(t, beadsRecoveryDoctorReport())
	type testCase struct {
		name        string
		doctor      beadsRecoveryTestCommand
		wantSuccess bool
	}
	tests := []testCase{
		{name: "verified local repair", doctor: beadsRecoveryTestCommand{stdout: `{"verified":true,"repaired":true,"post_repair":{"ok":true}}`}, wantSuccess: true},
		{name: "healthy report", doctor: beadsRecoveryTestCommand{stdout: `{"code":"healthy","exit_code":0,"verified":true}`}, wantSuccess: true},
		{name: "verified partial repair", doctor: beadsRecoveryTestCommand{stdout: partial, exitCode: 2}, wantSuccess: true},
		{name: "partial with stderr warnings", doctor: beadsRecoveryTestCommand{stdout: partial, stderr: "warning: original database is locked during best-effort inspection\n", exitCode: 2}, wantSuccess: true},
		{name: "partial unverified", doctor: beadsRecoveryTestCommand{stdout: `{"code":"fix_partial","exit_code":2,"repaired":true,"database_verified":false}`, exitCode: 2}},
		{name: "partial missing verification", doctor: beadsRecoveryTestCommand{stdout: `{"code":"fix_partial","exit_code":2,"repaired":true}`, exitCode: 2}},
		{name: "partial missing repair", doctor: beadsRecoveryTestCommand{stdout: `{"code":"fix_partial","exit_code":2,"database_verified":true}`, exitCode: 2}},
		{name: "partial not repaired", doctor: beadsRecoveryTestCommand{stdout: `{"code":"fix_partial","exit_code":2,"repaired":false,"database_verified":true}`, exitCode: 2}},
		{name: "partial missing code", doctor: beadsRecoveryTestCommand{stdout: `{"exit_code":2,"repaired":true,"database_verified":true}`, exitCode: 2}},
		{name: "partial mismatched report exit", doctor: beadsRecoveryTestCommand{stdout: `{"code":"fix_partial","exit_code":0,"repaired":true,"database_verified":true}`, exitCode: 2}},
		{name: "partial unexpected process exit", doctor: beadsRecoveryTestCommand{stdout: partial, exitCode: 1}},
		{name: "partial zero process exit", doctor: beadsRecoveryTestCommand{stdout: partial}},
		{name: "healthy nonzero process exit", doctor: beadsRecoveryTestCommand{stdout: `{"code":"healthy","exit_code":0,"verified":true}`, exitCode: 2}},
		{name: "healthy missing verification", doctor: beadsRecoveryTestCommand{stdout: `{"code":"healthy","exit_code":0}`}},
		{name: "healthy false verification", doctor: beadsRecoveryTestCommand{stdout: `{"code":"healthy","exit_code":0,"verified":false}`}},
		{name: "failed repair code", doctor: beadsRecoveryTestCommand{stdout: `{"code":"fix_failed","exit_code":0,"verified":true}`}},
		{name: "contradictory database verification", doctor: beadsRecoveryTestCommand{stdout: `{"code":"healthy","exit_code":0,"verified":true,"database_verified":false}`}},
		{name: "invalid verification type", doctor: beadsRecoveryTestCommand{stdout: `{"verified":"true"}`}},
		{name: "null verification", doctor: beadsRecoveryTestCommand{stdout: `{"verified":null}`}},
		{name: "empty report", doctor: beadsRecoveryTestCommand{}},
		{name: "malformed JSON", doctor: beadsRecoveryTestCommand{stdout: `{"verified":true`}},
		{name: "concatenated reports", doctor: beadsRecoveryTestCommand{stdout: `{"verified":true}{"verified":false}`}},
	}
	for _, test := range []struct {
		name   string
		mutate func(map[string]any)
	}{
		{"partial claims full verification", func(m map[string]any) { m["verified"] = true }},
		{"partial claims preservation verification", func(m map[string]any) { m["preservation_verified"] = true }},
		{"partial claims healthy result", func(m map[string]any) { m["ok"] = true }},
		{"skipped imports", func(m map[string]any) { m["skipped"] = 1 }},
		{"foreign key cleanup", func(m map[string]any) { m["fk_violations_cleaned"] = 1 }},
		{"skipped history", func(m map[string]any) {
			m["preserved_history"] = []any{map[string]any{"table": "events", "skipped": 1}}
		}},
		{"missing history counter", func(m map[string]any) { m["preserved_history"] = []any{map[string]any{"table": "events"}} }},
		{"dirty state loss", func(m map[string]any) {
			m["dirty_preservation_warnings"] = []string{beadsRecoveryTombstoneWarning, "dirty issue could not be preserved"}
		}},
		{"history state loss", func(m map[string]any) { m["history_preservation_warnings"] = []string{"events could not be preserved"} }},
		{"unrecognized partial warning", func(m map[string]any) { m["dirty_preservation_warnings"] = []string{"some local state was unreadable"} }},
		{"partial lacks warning", func(m map[string]any) { m["dirty_preservation_warnings"] = []string{} }},
		{"missing skipped counter", func(m map[string]any) { delete(m, "skipped") }},
		{"missing FK counter", func(m map[string]any) { delete(m, "fk_violations_cleaned") }},
		{"missing overall status", func(m map[string]any) { delete(m, "ok") }},
		{"missing preservation verification", func(m map[string]any) { delete(m, "preservation_verified") }},
		{"missing history evidence", func(m map[string]any) { delete(m, "preserved_history") }},
		{"null history warnings", func(m map[string]any) { m["history_preservation_warnings"] = nil }},
		{"missing dirty warnings", func(m map[string]any) { delete(m, "dirty_preservation_warnings") }},
		{"no retained backup", func(m map[string]any) { m["verified_backups"] = []any{} }},
		{"empty backup path", func(m map[string]any) { m["verified_backups"] = []any{map[string]any{"backup": ""}} }},
		{"no failure marker", func(m map[string]any) { delete(m, "recovery_failure_marker") }},
		{"failure marker persistence failed", func(m map[string]any) { m["recovery_failure_marker_error"] = "permission denied" }},
	} {
		report := beadsRecoveryDoctorReport()
		test.mutate(report)
		tests = append(tests, testCase{name: test.name, doctor: beadsRecoveryTestCommand{stdout: beadsRecoveryJSON(t, report), exitCode: 2}})
	}
	// Overall verification must not override explicit evidence of loss, even
	// in a zero-exit response. A complete healthy rebuild remains supported.
	healthy := beadsRecoveryDoctorReport()
	healthy["ok"], healthy["verified"], healthy["preservation_verified"] = true, true, true
	healthy["code"], healthy["exit_code"] = "healthy", 0
	healthy["dirty_preservation_warnings"], healthy["recovery_failure_marker"] = []string{}, nil
	tests = append(tests, testCase{name: "verified healthy JSONL rebuild", doctor: beadsRecoveryTestCommand{stdout: beadsRecoveryJSON(t, healthy)}, wantSuccess: true})
	healthy["recovery_failure_marker"] = "/retained/beads.db.verification-failed.json"
	tests = append(tests, testCase{name: "healthy rebuild retains verification failure", doctor: beadsRecoveryTestCommand{stdout: beadsRecoveryJSON(t, healthy)}})
	for _, report := range []string{
		`{"verified":true,"skipped":1}`,
		`{"verified":true,"fk_violations_cleaned":1}`,
		`{"verified":true,"preservation_verified":false}`,
		`{"verified":true,"ok":false}`,
		`{"verified":true,"dirty_preservation_warnings":["dirty state could not be preserved"]}`,
		`{"verified":true,"history_preservation_warnings":["history lost"]}`,
		`{"verified":true,"recovery_failure_marker_error":"permission denied"}`,
		`{"verified":true,"database_verified":true}`,
	} {
		tests = append(tests, testCase{name: "unsafe verified report " + report, doctor: beadsRecoveryTestCommand{stdout: report}})
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			// br versions can put the rebuild error on stdout while logging
			// separately to stderr; neither stream may hide the other.
			workspace := setupBeadsRecoveryCommands(t, beadsRecoveryTestCommand{
				stdout: beadsRebuildPreservationConflict, stderr: "warning: index needs repair\n", exitCode: 6,
			}, test.doctor)
			attempts := 0
			result, err := withBeadsCorruptionRecovery(t.Context(), workspace.dir, func() (string, error) {
				attempts++
				if attempts == 1 {
					return "", errors.New("database disk image is malformed (11)")
				}
				return "claimed", nil
			})
			if test.wantSuccess {
				if err != nil || attempts != 2 || result != "claimed" {
					t.Fatalf("result=%q error=%v attempts=%d, want one successful guarded retry", result, err, attempts)
				}
			} else {
				var processErr *exec.ExitError
				if err == nil || attempts != 1 || result != "" || !errors.As(err, &processErr) {
					t.Fatalf("result=%q error=%v attempts=%d, want unverified repair refused with original process error before retry", result, err, attempts)
				}
			}
			workspace.assertCommands(t, "sync --import-only --rebuild --json", "info --json --no-auto-import --no-auto-flush", "doctor --repair --json")
		})
	}
}

func TestBeadsCorruptionRecoveryDoesNotRepeatRepair(t *testing.T) {
	workspace := setupBeadsRecoveryCommands(t,
		beadsRecoveryTestCommand{stderr: beadsRebuildPreservationConflict, exitCode: 6},
		beadsRecoveryTestCommand{stdout: beadsRecoveryJSON(t, beadsRecoveryDoctorReport()), exitCode: 2},
	)
	retryErr := errors.New("database disk image is malformed on retry")
	attempts := 0
	result, err := withBeadsCorruptionRecovery(t.Context(), workspace.dir, func() (string, error) {
		attempts++
		if attempts == 1 {
			return "", errors.New("file is not a database")
		}
		return "retry result", retryErr
	})
	if err != retryErr || attempts != 2 || result != "retry result" {
		t.Fatalf("result=%q error=%v attempts=%d, want unchanged second failure", result, err, attempts)
	}
	workspace.assertCommands(t, "sync --import-only --rebuild --json", "info --json --no-auto-import --no-auto-flush", "doctor --repair --json")
}

func TestBeadsCorruptionRecoveryLeavesUnrelatedErrorsUntouched(t *testing.T) {
	for name, originalErr := range map[string]error{
		"normal assignment":   nil,
		"ordinary failure":    errors.New("permission denied"),
		"eligibility refusal": fmt.Errorf("database corruption: %w", ErrBeadAssignmentIneligible),
		"ownership refusal":   fmt.Errorf("database disk image is malformed: %w", ErrBeadAlreadyClaimed),
		"terminal refusal":    fmt.Errorf("file is not a database: %w", ErrBeadTerminal),
	} {
		t.Run(name, func(t *testing.T) {
			workspace := setupBeadsRecoveryCommands(t, beadsRecoveryTestCommand{}, beadsRecoveryTestCommand{})
			attempts := 0
			result, err := withBeadsCorruptionRecovery(t.Context(), workspace.dir, func() (string, error) {
				attempts++
				return "original result", originalErr
			})
			if err != originalErr || attempts != 1 || result != "original result" {
				t.Fatalf("result=%q error=%v attempts=%d, want unchanged original failure", result, err, attempts)
			}
			workspace.assertCommands(t)
		})
	}
}

func TestBeadsCorruptionRecoveryCancelsDuringDoctor(t *testing.T) {
	workspace := setupBeadsRecoveryCommands(t,
		beadsRecoveryTestCommand{stderr: beadsRebuildPreservationConflict, exitCode: 6},
		beadsRecoveryTestCommand{stdout: beadsRecoveryJSON(t, beadsRecoveryDoctorReport()), exitCode: 2},
	)
	t.Setenv("NTM_TEST_DOCTOR_BLOCK", "true")
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	attempts := 0
	result := make(chan error, 1)
	go func() {
		_, err := withBeadsCorruptionRecovery(ctx, workspace.dir, func() (bool, error) {
			attempts++
			return false, errors.New("database disk image is malformed (11)")
		})
		result <- err
	}()
	waitForTestFile(t, workspace.doctorStarted, "doctor repair to start")
	cancel()
	select {
	case err := <-result:
		if !errors.Is(err, context.Canceled) || attempts != 1 {
			t.Fatalf("error=%v attempts=%d, want cancellation without a guarded retry", err, attempts)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("doctor recovery ignored cancellation")
	}
	workspace.assertCommands(t, "sync --import-only --rebuild --json", "info --json --no-auto-import --no-auto-flush", "doctor --repair --json")
}

func TestBeadsCorruptionRecoveryCancelsAtWorkspaceGate(t *testing.T) {
	workspace := setupBeadsRecoveryCommands(t, beadsRecoveryTestCommand{}, beadsRecoveryTestCommand{})
	gate := workspaceBDMutex(workspace.dir)
	gate.Lock()
	defer gate.Unlock()
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	doneObserved := make(chan struct{}, 1)
	result := make(chan error, 1)
	go func() {
		result <- recoverCorruptBeadsDatabase(doneObservedContext{Context: ctx, doneObserved: doneObserved}, workspace.dir)
	}()
	select {
	case <-doneObserved:
	case err := <-result:
		t.Fatalf("recovery returned before waiting for the workspace gate: %v", err)
	case <-time.After(5 * time.Second):
		t.Fatal("recovery did not wait for the workspace gate")
	}
	cancel()
	select {
	case err := <-result:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("recovery error=%v, want context canceled", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("recovery ignored cancellation at the workspace gate")
	}
	workspace.assertCommands(t)
}

func TestBeadsMutatorsRejectInactiveContextWithoutInvokingSubprocesses(t *testing.T) {
	dir := t.TempDir()
	if err := os.Mkdir(filepath.Join(dir, ".git"), 0o755); err != nil {
		t.Fatalf("mkdir .git: %v", err)
	}
	binDir := t.TempDir()
	invoked := filepath.Join(t.TempDir(), "subprocess-invoked")
	script := "#!/bin/sh\nprintf 'invoked\\n' > " + invoked + "\nexit 1\n"
	for _, name := range []string{"br", "git"} {
		if err := os.WriteFile(filepath.Join(binDir, name), []byte(script), 0o700); err != nil {
			t.Fatalf("write fake %s: %v", name, err)
		}
	}
	t.Setenv("PATH", binDir)

	tests := []struct {
		name string
		run  func(context.Context) error
	}{
		{
			name: "generic claim",
			run: func(ctx context.Context) error {
				_, err := ClaimBead(ctx, dir, "ntm-test", "TestActor")
				return err
			},
		},
		{
			name: "assignment claim",
			run: func(ctx context.Context) error {
				_, err := ClaimBeadForAssignment(ctx, dir, "ntm-test", "TestActor")
				return err
			},
		},
		{
			name: "assignment claim with policy",
			run: func(ctx context.Context) error {
				_, err := ClaimBeadForAssignmentWithOperatorGatedLabels(ctx, dir, "ntm-test", "TestActor", []string{"human-gated"})
				return err
			},
		},
		{
			name: "stale assignment claim",
			run: func(ctx context.Context) error {
				_, err := ClaimStaleBeadForAssignment(ctx, dir, "ntm-test", "TestActor", time.Now().UTC())
				return err
			},
		},
		{
			name: "stale assignment claim with policy",
			run: func(ctx context.Context) error {
				_, err := ClaimStaleBeadForAssignmentWithOperatorGatedLabels(ctx, dir, "ntm-test", "TestActor", time.Now().UTC(), []string{"human-gated"})
				return err
			},
		},
		{
			name: "claim release",
			run: func(ctx context.Context) error {
				_, err := ReleaseBeadClaim(ctx, dir, "ntm-test", "TestActor")
				return err
			},
		},
	}

	contexts := []struct {
		name    string
		context func() context.Context
		want    error
	}{
		{name: "nil", context: func() context.Context { return nil }},
		{name: "pre-canceled", context: func() context.Context {
			ctx, cancel := context.WithCancel(context.Background())
			cancel()
			return ctx
		}, want: context.Canceled},
	}
	for _, test := range tests {
		for _, contextCase := range contexts {
			t.Run(test.name+"/"+contextCase.name, func(t *testing.T) {
				err := test.run(contextCase.context())
				if contextCase.want != nil {
					if !errors.Is(err, contextCase.want) {
						t.Fatalf("error=%v, want errors.Is(%v)", err, contextCase.want)
					}
				} else if err == nil || !strings.Contains(err.Error(), "beads mutation context is required") {
					t.Fatalf("error=%v, want required-context failure", err)
				}
				if _, statErr := os.Stat(invoked); !os.IsNotExist(statErr) {
					t.Fatalf("subprocess executed for inactive context: stat error=%v", statErr)
				}
			})
		}
	}
}

func TestRunBdContextCancelsWhileQueuedForWorkspaceGate(t *testing.T) {
	dir := t.TempDir()
	if err := os.Mkdir(filepath.Join(dir, ".git"), 0o755); err != nil {
		t.Fatalf("mkdir .git: %v", err)
	}
	normalized, err := normalizeTriageDir(dir)
	if err != nil {
		t.Fatalf("normalize triage dir: %v", err)
	}
	gate := workspaceBDMutex(normalized)
	gate.Lock()
	defer gate.Unlock()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	doneObserved := make(chan struct{}, 1)
	result := make(chan error, 1)
	go func() {
		_, runErr := RunBdContext(doneObservedContext{
			Context:      ctx,
			doneObserved: doneObserved,
		}, dir, "ready", "--json")
		result <- runErr
	}()

	select {
	case <-doneObserved:
	case err := <-result:
		t.Fatalf("RunBdContext returned before waiting for the workspace gate: %v", err)
	case <-time.After(5 * time.Second):
		t.Fatal("RunBdContext did not reach the workspace gate")
	}
	cancel()

	select {
	case err := <-result:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("RunBdContext error=%v, want context canceled", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("RunBdContext did not stop after cancellation while queued for the workspace gate")
	}
}

func TestGetBeadsSummaryContextCancelsWhileStatsWaitsForWorkspaceGate(t *testing.T) {
	dir := t.TempDir()
	for _, name := range []string{".git", ".beads"} {
		if err := os.Mkdir(filepath.Join(dir, name), 0o755); err != nil {
			t.Fatalf("mkdir %s: %v", name, err)
		}
	}
	binDir := t.TempDir()
	invoked := filepath.Join(t.TempDir(), "br-invoked")
	script := "#!/bin/sh\nprintf 'invoked\\n' > " + invoked + "\n"
	if err := os.WriteFile(filepath.Join(binDir, "br"), []byte(script), 0o700); err != nil {
		t.Fatalf("write fake br: %v", err)
	}
	t.Setenv("PATH", binDir+string(os.PathListSeparator)+os.Getenv("PATH"))
	normalized, err := normalizeTriageDir(dir)
	if err != nil {
		t.Fatalf("normalize triage dir: %v", err)
	}
	gate := workspaceBDMutex(normalized)
	gate.Lock()
	defer gate.Unlock()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	doneObserved := make(chan struct{}, 1)
	type summaryResult struct {
		summary *BeadsSummary
		err     error
	}
	result := make(chan summaryResult, 1)
	go func() {
		summary, summaryErr := GetBeadsSummaryContext(doneObservedContext{
			Context:      ctx,
			doneObserved: doneObserved,
		}, dir, 10)
		result <- summaryResult{summary: summary, err: summaryErr}
	}()

	select {
	case <-doneObserved:
	case got := <-result:
		t.Fatalf("GetBeadsSummaryContext returned before stats waited for the workspace gate: result=%+v error=%v", got.summary, got.err)
	case <-time.After(5 * time.Second):
		t.Fatal("GetBeadsSummaryContext stats did not reach the workspace gate")
	}
	cancel()

	select {
	case got := <-result:
		if !errors.Is(got.err, context.Canceled) {
			t.Fatalf("GetBeadsSummaryContext result=%+v error=%v, want context canceled", got.summary, got.err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("GetBeadsSummaryContext did not stop after cancellation while stats waited for the workspace gate")
	}
	if _, err := os.Stat(invoked); !os.IsNotExist(err) {
		t.Fatalf("gated br command executed before cancellation: stat error=%v", err)
	}
}

func TestRunBdContextCancelsDuringTransientRetryBackoff(t *testing.T) {
	dir := t.TempDir()
	if err := os.Mkdir(filepath.Join(dir, ".git"), 0o755); err != nil {
		t.Fatalf("mkdir .git: %v", err)
	}
	binDir := t.TempDir()
	marker := filepath.Join(t.TempDir(), "attempts")
	script := "#!/bin/sh\nprintf 'x\\n' >> " + marker + "\nprintf 'database is locked\\n' >&2\nexit 1\n"
	if err := os.WriteFile(filepath.Join(binDir, "br"), []byte(script), 0o700); err != nil {
		t.Fatalf("write fake br: %v", err)
	}
	t.Setenv("PATH", binDir)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	retryWaitEntered := make(chan time.Duration)
	continueRetry := make(chan struct{}, 1)
	result := make(chan error, 1)
	go func() {
		_, err := RunBdContext(retryWaitObservedContext{
			Context:          ctx,
			retryWaitEntered: retryWaitEntered,
			continueRetry:    continueRetry,
		}, dir, "ready", "--json")
		result <- err
	}()
	for attempt := 1; attempt <= 3; attempt++ {
		select {
		case delay := <-retryWaitEntered:
			if low, high := transientBeadsDBBackoffBounds(attempt); delay < low || delay > high {
				t.Fatalf("retry backoff %d delay=%s, want within [%s, %s]", attempt, delay, low, high)
			}
			if attempt < 3 {
				continueRetry <- struct{}{}
			}
		case err := <-result:
			t.Fatalf("RunBdContext returned before entering retry backoff %d: %v", attempt, err)
		case <-time.After(5 * time.Second):
			t.Fatalf("RunBdContext did not enter retry backoff %d", attempt)
		}
	}
	cancel()

	select {
	case err := <-result:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("RunBdContext error=%v, want context canceled", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("RunBdContext ignored cancellation during retry backoff")
	}

	data, err := os.ReadFile(marker)
	if err != nil {
		t.Fatalf("read fake br attempts: %v", err)
	}
	if attempts := strings.Count(string(data), "x"); attempts != 3 {
		t.Fatalf("fake br attempts=%d, want exactly 3 with no post-cancellation execution", attempts)
	}
}

// installFakeBRWithAttempts installs a fake br on PATH that appends one line
// to the returned attempts file per invocation and runs script after that.
// The script may reference $attempts (the file path) and $count (invocations
// so far, including the current one).
func installFakeBRWithAttempts(t *testing.T, script string) (dir, attempts string) {
	t.Helper()
	dir = t.TempDir()
	if err := os.Mkdir(filepath.Join(dir, ".git"), 0o755); err != nil {
		t.Fatalf("mkdir .git: %v", err)
	}
	binDir := t.TempDir()
	attempts = filepath.Join(t.TempDir(), "attempts")
	full := "#!/bin/sh\nattempts=" + attempts + "\nprintf 'x\\n' >> \"$attempts\"\ncount=$(wc -l < \"$attempts\" | tr -d ' ')\n" + script
	if err := os.WriteFile(filepath.Join(binDir, "br"), []byte(full), 0o700); err != nil {
		t.Fatalf("write fake br: %v", err)
	}
	// Keep the host PATH: the script shells out to wc/tr.
	t.Setenv("PATH", binDir+string(os.PathListSeparator)+os.Getenv("PATH"))
	return dir, attempts
}

func countFakeBRAttempts(t *testing.T, attempts string) int {
	t.Helper()
	data, err := os.ReadFile(attempts)
	if err != nil {
		t.Fatalf("read fake br attempts: %v", err)
	}
	return strings.Count(string(data), "x")
}

// brWriteLockTimeoutDiagnostic mirrors the exact CONFIG_ERROR br prints when
// its cross-process workspace write lock times out.
const brWriteLockTimeoutDiagnostic = "CONFIG_ERROR: Timed out after 5000ms waiting for write lock (database) at .beads/.write.lock. Another br process may be holding that authority; retry after it exits or investigate a stuck process."

func TestIsTransientBeadsDBError(t *testing.T) {
	cases := []struct {
		name   string
		stderr string
		want   bool
	}{
		{name: "sqlite locked", stderr: "Error: database is locked", want: true},
		{name: "sqlite busy", stderr: "database is busy", want: true},
		{name: "br write lock timeout", stderr: brWriteLockTimeoutDiagnostic, want: true},
		{name: "unwritable write lock is terminal", stderr: "CONFIG_ERROR: Failed to open write lock .beads/.write.lock: permission denied", want: false},
		{name: "other config error is terminal", stderr: "CONFIG_ERROR: invalid [sync] configuration", want: false},
		{name: "generic timeout is terminal", stderr: "Timed out after 30s waiting for remote", want: false},
		{name: "empty", stderr: "", want: false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := isTransientBeadsDBError(tc.stderr, ""); got != tc.want {
				t.Fatalf("isTransientBeadsDBError(%q) = %v, want %v", tc.stderr, got, tc.want)
			}
			if got := isTransientBeadsDBError("", tc.stderr); got != tc.want {
				t.Fatalf("isTransientBeadsDBError(stdout=%q) = %v, want %v", tc.stderr, got, tc.want)
			}
		})
	}
}

func TestTransientBeadsDBBackoffIsBoundedExponentialWithJitter(t *testing.T) {
	wantCeilings := []time.Duration{
		50 * time.Millisecond, 100 * time.Millisecond, 200 * time.Millisecond,
		400 * time.Millisecond, 800 * time.Millisecond, 800 * time.Millisecond,
		800 * time.Millisecond,
	}
	for i, ceiling := range wantCeilings {
		attempt := i + 1
		low, high := transientBeadsDBBackoffBounds(attempt)
		if high != ceiling || low != ceiling/2 {
			t.Fatalf("attempt %d bounds = [%s, %s], want [%s, %s]", attempt, low, high, ceiling/2, ceiling)
		}
		for sample := 0; sample < 64; sample++ {
			if got := transientBeadsDBBackoff(attempt); got < low || got > high {
				t.Fatalf("attempt %d backoff %s outside [%s, %s]", attempt, got, low, high)
			}
		}
	}
	if low, high := transientBeadsDBBackoffBounds(0); low != 25*time.Millisecond || high != 50*time.Millisecond {
		t.Fatalf("attempt 0 bounds = [%s, %s], want clamped to attempt 1", low, high)
	}
}

func TestTransientBeadsRetryDelayHonorsAttemptAndDeadlineBudgets(t *testing.T) {
	farDeadline := time.Now().Add(time.Hour)
	if _, retry := transientBeadsRetryDelay(6, 6, farDeadline); retry {
		t.Fatal("final attempt must not be retried")
	}
	if delay, retry := transientBeadsRetryDelay(1, 6, farDeadline); !retry || delay <= 0 {
		t.Fatalf("attempt 1 retry = (%s, %v), want positive delay", delay, retry)
	}
	if _, retry := transientBeadsRetryDelay(1, 6, time.Now()); retry {
		t.Fatal("an expired deadline must stop retries even with attempts remaining")
	}
}

func TestRunBdContextRetriesBeadsWriteLockTimeoutThenSucceeds(t *testing.T) {
	dir, attempts := installFakeBRWithAttempts(t, `
if [ "$count" -lt 3 ]; then
  printf '%s\n' "`+brWriteLockTimeoutDiagnostic+`" >&2
  exit 1
fi
printf '%s\n' '{"issues":[],"total":0}'
`)

	output, err := RunBdContext(context.Background(), dir, "ready", "--json")
	if err != nil {
		t.Fatalf("RunBdContext returned the transient write-lock timeout: %v", err)
	}
	if !strings.Contains(output, `"issues":[]`) {
		t.Fatalf("RunBdContext output=%q, want the successful third attempt", output)
	}
	if got := countFakeBRAttempts(t, attempts); got != 3 {
		t.Fatalf("fake br attempts=%d, want exactly 3", got)
	}
}

func TestRunBdContextDoesNotRetryTerminalConfigError(t *testing.T) {
	dir, attempts := installFakeBRWithAttempts(t, `
printf '%s\n' 'CONFIG_ERROR: Failed to open write lock .beads/.write.lock: permission denied' >&2
exit 1
`)

	_, err := RunBdContext(context.Background(), dir, "ready", "--json")
	if err == nil || !strings.Contains(err.Error(), "Failed to open write lock") {
		t.Fatalf("RunBdContext error=%v, want the terminal CONFIG_ERROR surfaced", err)
	}
	if got := countFakeBRAttempts(t, attempts); got != 1 {
		t.Fatalf("fake br attempts=%d, want exactly 1 (no retry on a terminal error)", got)
	}
}

func TestRunBdContextStopsRetryingContentionAtTotalDeadline(t *testing.T) {
	previous := transientBeadsRetryBudget
	transientBeadsRetryBudget = 120 * time.Millisecond
	t.Cleanup(func() { transientBeadsRetryBudget = previous })

	dir, attempts := installFakeBRWithAttempts(t, `
printf '%s\n' "`+brWriteLockTimeoutDiagnostic+`" >&2
exit 1
`)

	start := time.Now()
	_, err := RunBdContext(context.Background(), dir, "ready", "--json")
	elapsed := time.Since(start)
	if err == nil || !strings.Contains(err.Error(), "still contended") || !strings.Contains(err.Error(), "waiting for write lock") {
		t.Fatalf("RunBdContext error=%v, want contention surfaced after the retry budget", err)
	}
	if got := countFakeBRAttempts(t, attempts); got < 1 || got >= 6 {
		t.Fatalf("fake br attempts=%d, want the deadline to cut retries short of the %d-attempt cap", got, 6)
	}
	if elapsed > 5*time.Second {
		t.Fatalf("RunBdContext took %s, want the total retry deadline to bound it", elapsed)
	}
}

func TestGetRecentlyCompletedListContext(t *testing.T) {
	t.Run("returns_limited_completed_rows", func(t *testing.T) {
		dir := t.TempDir()
		for _, name := range []string{".git", ".beads"} {
			if err := os.Mkdir(filepath.Join(dir, name), 0o755); err != nil {
				t.Fatalf("mkdir %s: %v", name, err)
			}
		}
		binDir := t.TempDir()
		script := "#!/bin/sh\ncase \"$*\" in\n  *\"list --status=closed --json\"*) printf '[{\"id\":\"ntm-done-1\",\"title\":\"first\"},{\"id\":\"ntm-done-2\",\"title\":\"second\"}]\\n' ;;\n  *) printf 'unexpected br arguments: %s\\n' \"$*\" >&2; exit 64 ;;\nesac\n"
		if err := os.WriteFile(filepath.Join(binDir, "br"), []byte(script), 0o700); err != nil {
			t.Fatalf("write fake br: %v", err)
		}
		t.Setenv("PATH", binDir)

		items, err := GetRecentlyCompletedListContext(context.Background(), dir, 1)
		if err != nil {
			t.Fatalf("GetRecentlyCompletedListContext error: %v", err)
		}
		if len(items) != 1 || items[0].ID != "ntm-done-1" || items[0].Title != "first" {
			t.Fatalf("completed items=%+v, want first row only", items)
		}
	})

	t.Run("cancels_blocking_br", func(t *testing.T) {
		dir := t.TempDir()
		for _, name := range []string{".git", ".beads"} {
			if err := os.Mkdir(filepath.Join(dir, name), 0o755); err != nil {
				t.Fatalf("mkdir %s: %v", name, err)
			}
		}
		binDir := t.TempDir()
		started := filepath.Join(t.TempDir(), "br-started")
		script := "#!/bin/sh\n: > " + started + "\nexec /bin/sleep 30\n"
		if err := os.WriteFile(filepath.Join(binDir, "br"), []byte(script), 0o700); err != nil {
			t.Fatalf("write blocking fake br: %v", err)
		}
		t.Setenv("PATH", binDir)

		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()
		type completedResult struct {
			items []BeadPreview
			err   error
		}
		result := make(chan completedResult, 1)
		go func() {
			items, listErr := GetRecentlyCompletedListContext(ctx, dir, 10)
			result <- completedResult{items: items, err: listErr}
		}()
		waitForTestFile(t, started, "blocking fake br startup")
		cancel()

		select {
		case got := <-result:
			if !errors.Is(got.err, context.Canceled) {
				t.Fatalf("items=%+v error=%v, want context canceled", got.items, got.err)
			}
		case <-time.After(5 * time.Second):
			t.Fatal("blocking completed-list br process ignored cancellation")
		}
	})
}

func TestGetInProgressListContextZeroLimitReturnsFullList(t *testing.T) {
	dir := t.TempDir()
	for _, name := range []string{".git", ".beads"} {
		if err := os.Mkdir(filepath.Join(dir, name), 0o755); err != nil {
			t.Fatalf("mkdir %s: %v", name, err)
		}
	}
	binDir := t.TempDir()
	script := "#!/bin/sh\nprintf '[{\"id\":\"ntm-active-1\",\"title\":\"first\"},{\"id\":\"ntm-active-2\",\"title\":\"second\"}]\\n'\n"
	if err := os.WriteFile(filepath.Join(binDir, "br"), []byte(script), 0o700); err != nil {
		t.Fatalf("write fake br: %v", err)
	}
	t.Setenv("PATH", binDir)

	items, err := GetInProgressListContext(context.Background(), dir, 0)
	if err != nil {
		t.Fatalf("GetInProgressListContext error: %v", err)
	}
	if len(items) != 2 || items[0].ID != "ntm-active-1" || items[1].ID != "ntm-active-2" {
		t.Fatalf("in-progress items=%+v, want both rows", items)
	}
}

func TestUnmarshalBdList(t *testing.T) {
	t.Parallel()

	type bead struct {
		ID string `json:"id"`
	}

	tests := []struct {
		name    string
		input   string
		wantIDs []string
	}{
		{
			name:    "raw_array",
			input:   `[{"id":"bd-1"},{"id":"bd-2"}]`,
			wantIDs: []string{"bd-1", "bd-2"},
		},
		{
			name:    "issues_envelope",
			input:   `{"issues":[{"id":"bd-3"},{"id":"bd-4"}]}`,
			wantIDs: []string{"bd-3", "bd-4"},
		},
		{
			name:    "single_object",
			input:   `{"id":"bd-5"}`,
			wantIDs: []string{"bd-5"},
		},
		{
			name:    "empty_null",
			input:   `null`,
			wantIDs: []string{},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			got, err := UnmarshalBdList[bead](tt.input)
			if err != nil {
				t.Fatalf("UnmarshalBdList() error = %v", err)
			}

			if len(got) != len(tt.wantIDs) {
				t.Fatalf("len(got) = %d, want %d", len(got), len(tt.wantIDs))
			}
			for i, wantID := range tt.wantIDs {
				if got[i].ID != wantID {
					t.Fatalf("got[%d].ID = %q, want %q", i, got[i].ID, wantID)
				}
			}
		})
	}
}

func TestUnmarshalBdList_RawMessagesFromEnvelope(t *testing.T) {
	t.Parallel()

	raw, err := UnmarshalBdList[json.RawMessage](`{"issues":[{"id":"bd-7"}]}`)
	if err != nil {
		t.Fatalf("UnmarshalBdList() error = %v", err)
	}
	if len(raw) != 1 {
		t.Fatalf("len(raw) = %d, want 1", len(raw))
	}

	var decoded map[string]string
	if err := json.Unmarshal(raw[0], &decoded); err != nil {
		t.Fatalf("json.Unmarshal(raw[0]) error = %v", err)
	}
	if decoded["id"] != "bd-7" {
		t.Fatalf("decoded id = %q, want %q", decoded["id"], "bd-7")
	}
}

func TestCheckDrift_EarlyValidation(t *testing.T) {
	t.Parallel()

	t.Run("missing_project_dir", func(t *testing.T) {
		t.Parallel()

		res := CheckDrift("/path/that/does/not/exist")
		if res.Status != DriftNoBaseline {
			t.Fatalf("Status = %v, want %v", res.Status, DriftNoBaseline)
		}
		if IsInstalled() {
			if !strings.Contains(res.Message, "project directory does not exist") {
				t.Fatalf("Message = %q, want contains %q", res.Message, "project directory does not exist")
			}
		} else {
			if !strings.Contains(res.Message, "bv not installed") {
				t.Fatalf("Message = %q, want contains %q", res.Message, "bv not installed")
			}
		}
	})

	t.Run("missing_beads_dir", func(t *testing.T) {
		t.Parallel()

		dir := t.TempDir()
		res := CheckDrift(dir)
		if res.Status != DriftNoBaseline {
			t.Fatalf("Status = %v, want %v", res.Status, DriftNoBaseline)
		}
		if IsInstalled() {
			if !strings.Contains(res.Message, "no .beads directory") {
				t.Fatalf("Message = %q, want contains %q", res.Message, "no .beads directory")
			}
		} else {
			if !strings.Contains(res.Message, "bv not installed") {
				t.Fatalf("Message = %q, want contains %q", res.Message, "bv not installed")
			}
		}
	})
}

func TestGetBeadsSummary_EarlyValidation(t *testing.T) {
	t.Run("missing_project_dir", func(t *testing.T) {
		res := GetBeadsSummary("/path/that/does/not/exist", 3)
		if res.Available {
			t.Fatalf("Available = true, want false")
		}
		if !strings.Contains(res.Reason, "project directory does not exist") {
			t.Fatalf("Reason = %q, want contains %q", res.Reason, "project directory does not exist")
		}
	})

	t.Run("missing_beads_dir", func(t *testing.T) {
		dir := t.TempDir()
		res := GetBeadsSummary(dir, 3)
		if res.Available {
			t.Fatalf("Available = true, want false")
		}
		if !strings.Contains(res.Reason, "no .beads/ directory") {
			t.Fatalf("Reason = %q, want contains %q", res.Reason, "no .beads/ directory")
		}
	})

	t.Run("missing_br_binary_with_beads_dir", func(t *testing.T) {
		dir := t.TempDir()
		if err := os.Mkdir(filepath.Join(dir, ".beads"), 0755); err != nil {
			t.Fatalf("mkdir .beads: %v", err)
		}
		t.Setenv("PATH", "")

		res := GetBeadsSummary(dir, 3)
		if res.Available {
			t.Fatalf("Available = true, want false")
		}
		if res.Reason != "br not installed" {
			t.Fatalf("Reason = %q, want %q", res.Reason, "br not installed")
		}
	})
}

func TestGetBeadsSummaryParsesCurrentStatsSummaryEnvelope(t *testing.T) {
	dir := t.TempDir()
	if err := os.Mkdir(filepath.Join(dir, ".beads"), 0o755); err != nil {
		t.Fatalf("mkdir .beads: %v", err)
	}
	binDir := t.TempDir()
	script := `#!/bin/sh
for arg in "$@"; do
  case "$arg" in
    stats)
      echo '{"summary":{"total_issues":12,"open_issues":7,"in_progress_issues":2,"blocked_issues":3,"ready_issues":4,"closed_issues":5}}'
      exit 0
      ;;
    ready|list)
      echo '[]'
      exit 0
      ;;
  esac
done
exit 1
`
	if err := os.WriteFile(filepath.Join(binDir, "br"), []byte(script), 0o700); err != nil {
		t.Fatalf("write fake br: %v", err)
	}
	t.Setenv("PATH", binDir)

	summary, err := GetBeadsSummaryContext(context.Background(), dir, 5)
	if err != nil {
		t.Fatalf("GetBeadsSummaryContext: %v", err)
	}
	if !summary.Available {
		t.Fatalf("summary unavailable: %s", summary.Reason)
	}
	if summary.Total != 12 || summary.Open != 7 || summary.InProgress != 2 || summary.Blocked != 3 || summary.Ready != 4 || summary.Closed != 5 {
		t.Fatalf("summary counts = total=%d open=%d in_progress=%d blocked=%d ready=%d closed=%d, want 12/7/2/3/4/5", summary.Total, summary.Open, summary.InProgress, summary.Blocked, summary.Ready, summary.Closed)
	}
}

func TestGetPlanContextRequiresPlanTracksEnvelope(t *testing.T) {
	tests := []struct {
		name       string
		output     string
		wantErr    string
		wantTracks int
	}{
		{name: "missing plan", output: `{}`, wantErr: "missing plan object"},
		{name: "null plan", output: `{"plan":null}`, wantErr: "missing plan object"},
		{name: "missing tracks", output: `{"plan":{}}`, wantErr: "missing plan.tracks array"},
		{name: "null tracks", output: `{"plan":{"tracks":null}}`, wantErr: "missing plan.tracks array"},
		{name: "empty tracks is valid", output: `{"plan":{"tracks":[]}}`, wantTracks: 0},
		{name: "missing track items", output: `{"plan":{"tracks":[{"track_id":"one"}]}}`, wantErr: "missing plan.tracks[0].items array"},
		{name: "null track items", output: `{"plan":{"tracks":[{"track_id":"one","items":null}]}}`, wantErr: "missing plan.tracks[0].items array"},
		{name: "empty track items is valid", output: `{"plan":{"tracks":[{"track_id":"one","items":[]}]}}`, wantTracks: 1},
		{name: "missing item status", output: `{"plan":{"tracks":[{"track_id":"one","items":[{"id":"ntm-test","priority":1}]}]}}`, wantErr: "missing or blank plan.tracks[0].items[0].status"},
		{name: "null item status", output: `{"plan":{"tracks":[{"track_id":"one","items":[{"id":"ntm-test","status":null,"priority":1}]}]}}`, wantErr: "missing or blank plan.tracks[0].items[0].status"},
		{name: "blank item status", output: `{"plan":{"tracks":[{"track_id":"one","items":[{"id":"ntm-test","status":"   ","priority":1}]}]}}`, wantErr: "missing or blank plan.tracks[0].items[0].status"},
		{name: "missing item priority", output: `{"plan":{"tracks":[{"track_id":"one","items":[{"id":"ntm-test","status":"open"}]}]}}`, wantErr: "missing plan.tracks[0].items[0].priority"},
		{name: "null item priority", output: `{"plan":{"tracks":[{"track_id":"one","items":[{"id":"ntm-test","status":"open","priority":null}]}]}}`, wantErr: "missing plan.tracks[0].items[0].priority"},
		{name: "negative item priority", output: `{"plan":{"tracks":[{"track_id":"one","items":[{"id":"ntm-test","status":"open","priority":-1}]}]}}`, wantErr: "priority -1 is outside 0..4"},
		{name: "above-range item priority", output: `{"plan":{"tracks":[{"track_id":"one","items":[{"id":"ntm-test","status":"open","priority":5}]}]}}`, wantErr: "priority 5 is outside 0..4"},
		{name: "explicit priority zero is valid", output: `{"plan":{"tracks":[{"track_id":"one","items":[{"id":"ntm-test","status":"open","priority":0}]}]}}`, wantTracks: 1},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			dir := t.TempDir()
			binDir := t.TempDir()
			script := "#!/bin/sh\nprintf '%s\\n' '" + tt.output + "'\n"
			if err := os.WriteFile(filepath.Join(binDir, "bv"), []byte(script), 0o700); err != nil {
				t.Fatalf("write fake bv: %v", err)
			}
			t.Setenv("PATH", binDir)

			plan, err := GetPlanContext(context.Background(), dir)
			if tt.wantErr != "" {
				if err == nil || plan != nil {
					t.Fatalf("GetPlanContext() = plan:%+v err:%v, want error containing %q", plan, err, tt.wantErr)
				}
				if !strings.Contains(err.Error(), tt.wantErr) {
					t.Fatalf("GetPlanContext error = %q, want substring %q", err, tt.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatalf("GetPlanContext() error: %v", err)
			}
			if plan == nil {
				t.Fatal("GetPlanContext() returned nil plan")
			}
			if got := len(plan.Plan.Tracks); got != tt.wantTracks {
				t.Fatalf("len(plan.tracks) = %d, want %d", got, tt.wantTracks)
			}
			if plan.Plan.Tracks == nil {
				t.Fatal("valid empty plan.tracks decoded as nil, want present empty array")
			}
		})
	}
}

func TestGetHealthSummary_NonFatalBottlenecksError(t *testing.T) {
	t.Parallel()

	summary, err := GetHealthSummary("/path/that/does/not/exist")
	if err != nil {
		t.Fatalf("GetHealthSummary err = %v, want nil", err)
	}
	if summary == nil {
		t.Fatalf("GetHealthSummary summary = nil")
	}
	if summary.BottleneckCount != 0 {
		t.Fatalf("BottleneckCount = %d, want 0", summary.BottleneckCount)
	}
	if summary.DriftStatus != DriftNoBaseline {
		t.Fatalf("DriftStatus = %v, want %v", summary.DriftStatus, DriftNoBaseline)
	}
}

func TestGetDependencyContext_HandlesToolErrors(t *testing.T) {
	t.Parallel()

	ctx, err := GetDependencyContext("/path/that/does/not/exist", 3)
	if err != nil {
		t.Fatalf("GetDependencyContext err = %v, want nil", err)
	}
	if ctx == nil {
		t.Fatalf("GetDependencyContext ctx = nil")
	}
	if ctx.BlockedCount != 0 || ctx.ReadyCount != 0 {
		t.Fatalf("BlockedCount/ReadyCount = %d/%d, want 0/0", ctx.BlockedCount, ctx.ReadyCount)
	}
	if len(ctx.InProgressTasks) != 0 {
		t.Fatalf("len(InProgressTasks) = %d, want 0", len(ctx.InProgressTasks))
	}
	if len(ctx.TopBlockers) != 0 {
		t.Fatalf("len(TopBlockers) = %d, want 0", len(ctx.TopBlockers))
	}
}

// An envelope whose list key is present but empty means "no results". It used
// to fall through to the single-object branch, which unmarshalled the
// envelope itself into T (unknown fields are ignored, so it always succeeded)
// and returned one phantom zero-valued item. `br list --json` answers exactly
// this shape for an empty result, so callers with no matching beads got one
// blank bead — the alert generator then emitted a stale-bead alert for ID ""
// because a zero UpdatedAt is older than any threshold.
func TestUnmarshalBdListEmptyEnvelopeReturnsNoItems(t *testing.T) {
	type bead struct {
		ID    string `json:"id"`
		Title string `json:"title"`
	}
	cases := []struct {
		name    string
		payload string
	}{
		{"br list empty result", `{"issues":[],"total":0,"limit":0,"offset":0,"has_more":false}`},
		{"empty beads key", `{"beads":[],"total":0}`},
		{"empty items key", `{"items":[]}`},
		{"null issues", `{"issues":null,"total":0}`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := UnmarshalBdList[bead](tc.payload)
			if err != nil {
				t.Fatalf("UnmarshalBdList: %v", err)
			}
			if len(got) != 0 {
				t.Fatalf("got %d items (%+v), want 0 — an empty envelope must not yield a phantom bead", len(got), got)
			}
		})
	}
}

// A genuine single object must still parse, so the empty-envelope guard cannot
// simply disable the fallback.
func TestUnmarshalBdListStillAcceptsSingleObject(t *testing.T) {
	type bead struct {
		ID string `json:"id"`
	}
	got, err := UnmarshalBdList[bead](`{"id":"ntm-abc12"}`)
	if err != nil {
		t.Fatalf("UnmarshalBdList: %v", err)
	}
	if len(got) != 1 || got[0].ID != "ntm-abc12" {
		t.Fatalf("got %+v, want the single bead", got)
	}
}

// bd-u7v3p: isEmptyBrListEnvelope recognizes only issues/beads/items, so any
// OTHER JSON object still fell through to the single-object branch — which,
// because json.Unmarshal ignores unknown fields, always succeeded and returned
// one zero-valued phantom item. That is the same failure the empty-envelope
// guard was added for (a stale-bead alert for ID "", an envelope returned as a
// bead), just reached through a different shape.
func TestUnmarshalBdList_UnrecognizedObjectYieldsNoPhantom(t *testing.T) {
	type bead struct {
		ID    string `json:"id"`
		Title string `json:"title"`
	}
	cases := []struct {
		name    string
		payload string
	}{
		{"future envelope key", `{"records":[],"total":0}`},
		{"renamed envelope key", `{"work_items":[{"id":"ntm-1"}],"total":1}`},
		{"error object on stdout", `{"error":"no beads database found","code":"NO_DB"}`},
		{"unrelated metadata object", `{"total":0,"limit":50,"offset":0,"has_more":false}`},
	}
	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			got, err := UnmarshalBdList[bead](tc.payload)
			if err != nil {
				t.Fatalf("UnmarshalBdList: %v", err)
			}
			for _, item := range got {
				if item.ID == "" {
					t.Fatalf("got a phantom zero-valued bead from %s; alert generators treat ID \"\" with a zero UpdatedAt as a stale bead", tc.payload)
				}
			}
			if len(got) != 0 {
				t.Fatalf("got %d items (%+v), want 0 for a payload that maps onto nothing in T", len(got), got)
			}
		})
	}
}

// A partially-matching object is still a real item and must survive.
func TestUnmarshalBdList_PartiallyPopulatedObjectIsKept(t *testing.T) {
	type bead struct {
		ID    string `json:"id"`
		Title string `json:"title"`
	}
	got, err := UnmarshalBdList[bead](`{"title":"no id but real","extra":"ignored"}`)
	if err != nil {
		t.Fatalf("UnmarshalBdList: %v", err)
	}
	if len(got) != 1 || got[0].Title != "no id but real" {
		t.Fatalf("got %+v, want the single partially-populated bead kept", got)
	}
}
