package process

import (
	"bytes"
	"context"
	"errors"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

func TestParseProcessSnapshot(t *testing.T) {
	const shell = "101 1 101 202 Ss Thu Nov 12 16:10:14 2026 bash\n"
	const agent = "202 101 202 202 T+ Thu Nov 12 16:10:15 2026 codex\n"
	const zombie = "203 101 203 -1 Z Thu Nov 12 16:10:16 2026 codex\n"
	got, err := parseProcessSnapshot([]byte(shell + agent + zombie))
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 3 || got[101].Command != "bash" || got[202].PPID != 101 || got[202].State != "T+" || got[203].State != "Z" || got[203].TPGID != -1 {
		t.Fatalf("snapshot lost process evidence: %+v", got)
	}
	if got[101].Started != "Thu Nov 12 16:10:14 2026" {
		t.Fatalf("unexpected process start identity: %q", got[101].Started)
	}
	for _, tt := range []struct {
		name string
		text string
	}{
		{"empty", ""},
		{"blank", "\n"},
		{"missing_final_newline", strings.TrimSuffix(shell, "\n")},
		{"missing_command", "101 1 101 202 Ss Thu Nov 12 16:10:14 2026\n"},
		{"partial_last_row", shell + "202 101 202\n"},
		{"duplicate_pid", shell + shell},
		{"invalid_pid", strings.Replace(shell, "101", "invalid", 1)},
		{"invalid_parent", strings.Replace(shell, "101 1", "101 -1", 1)},
		{"invalid_foreground", strings.Replace(shell, "101 202 Ss", "101 -2 Ss", 1)},
		{"invalid_state", strings.Replace(shell, "Ss", "???", 1)},
		{"invalid_start", strings.Replace(shell, "Nov", "Foo", 1)},
		{"nul", shell + "\x00\n"},
		{"blank_row", shell + "\n"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			if table, err := parseProcessSnapshot([]byte(tt.text)); err == nil || table != nil {
				t.Fatalf("invalid snapshot returned table=%v, err=%v", table, err)
			}
		})
	}
}

func TestSnapshotContextCurrentProcess(t *testing.T) {
	if runtime.GOOS != "linux" && runtime.GOOS != "darwin" {
		t.Skip("snapshot requires Linux or macOS ps")
	}
	got, err := SnapshotContext(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	self, exists := got[os.Getpid()]
	if !exists || self.PID != os.Getpid() || self.Started == "" || self.Command == "" || self.State == "" {
		t.Fatalf("snapshot did not observe the calling process: %+v", self)
	}
}

func TestSnapshotContextRejectsFailedObservations(t *testing.T) {
	if runtime.GOOS != "linux" && runtime.GOOS != "darwin" {
		t.Skip("snapshot requires Linux or macOS ps")
	}
	for _, tt := range []struct {
		name string
		body string
	}{
		{"failed_command", "printf '%s\\n' '101 1 101 202 Ss Thu Nov 12 16:10:14 2026 bash'\nprintf '%s\\n' 'private-arguments' >&2\nexit 7\n"},
		{"malformed_output", "printf '%s\\n' 'partial-private-arguments'\n"},
		{"empty_output", "exit 0\n"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			dir := t.TempDir()
			if err := os.WriteFile(filepath.Join(dir, "ps"), []byte("#!/bin/sh\n"+tt.body), 0700); err != nil {
				t.Fatal(err)
			}
			t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
			got, err := SnapshotContext(t.Context())
			if err == nil || got != nil {
				t.Fatalf("failed observation returned table=%v, err=%v", got, err)
			}
			if strings.Contains(err.Error(), "private-arguments") {
				t.Fatalf("snapshot error exposed process output: %v", err)
			}
		})
	}
}

func TestSnapshotContextPinsFormatAndLocale(t *testing.T) {
	if runtime.GOOS != "linux" && runtime.GOOS != "darwin" {
		t.Skip("snapshot requires Linux or macOS ps")
	}
	dir := t.TempDir()
	script := "#!/bin/sh\n" +
		"[ \"$LC_ALL\" = C ] || exit 2\n" +
		"[ \"$*\" = '-ww -axo pid=,ppid=,pgid=,tpgid=,stat=,lstart=,ucomm=' ] || exit 3\n" +
		"printf '%s\\n' '101 1 101 -1 Ss Thu Nov 12 16:10:14 2026 bash'\n"
	if err := os.WriteFile(filepath.Join(dir, "ps"), []byte(script), 0700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
	t.Setenv("LC_ALL", "invalid-locale")
	got, err := SnapshotContext(t.Context())
	if err != nil || len(got) != 1 || got[101].Command != "bash" {
		t.Fatalf("snapshot command format/locale changed: table=%v, err=%v", got, err)
	}
}

func TestSnapshotContextCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if got, err := SnapshotContext(ctx); !errors.Is(err, context.Canceled) || got != nil {
		t.Fatalf("canceled observation returned table=%v, err=%v", got, err)
	}
	if got, err := SnapshotContext(nil); err == nil || got != nil {
		t.Fatalf("nil context returned table=%v, err=%v", got, err)
	}
	if runtime.GOOS != "linux" && runtime.GOOS != "darwin" {
		return
	}
	dir := t.TempDir()
	marker := filepath.Join(dir, "started")
	t.Setenv("NTM_SNAPSHOT_STARTED", marker)
	script := "#!/bin/sh\n: > \"$NTM_SNAPSHOT_STARTED\"\nexec sleep 30\n"
	if err := os.WriteFile(filepath.Join(dir, "ps"), []byte(script), 0700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
	ctx, cancel = context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	done := make(chan error, 1)
	go func() {
		_, err := SnapshotContext(ctx)
		done <- err
	}()
	for {
		if _, err := os.Stat(marker); err == nil {
			break
		}
		select {
		case <-ctx.Done():
			t.Fatal("snapshot command did not start")
		case <-time.After(5 * time.Millisecond):
		}
	}
	cancel()
	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("running snapshot ignored cancellation: %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("running snapshot did not stop after cancellation")
	}
}

func TestSnapshotOutputIsBounded(t *testing.T) {
	var output processSnapshotBuffer
	data := bytes.Repeat([]byte{'x'}, processSnapshotOutputLimit+1)
	// exec.Cmd copies pipe output through io.Copy; a promoted ReaderFrom must
	// not bypass the bounded Write implementation.
	if n, err := io.Copy(&output, io.LimitReader(bytes.NewReader(data), int64(len(data)))); err != nil || n != int64(len(data)) {
		t.Fatalf("snapshot output copy = %d, %v", n, err)
	}
	if !output.exceeded || output.Len() != processSnapshotOutputLimit {
		t.Fatalf("snapshot output was not capped: size=%d, exceeded=%v", output.Len(), output.exceeded)
	}
	if _, err := output.Write(data[:1]); err != nil || output.Len() != processSnapshotOutputLimit {
		t.Fatalf("snapshot output grew beyond the bound: size=%d, err=%v", output.Len(), err)
	}
}

func TestContextProcessQueriesCancelPortableCommands(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("portable command fixtures require a POSIX shell")
	}
	for _, name := range []string{"ps", "pgrep"} {
		if name == "pgrep" && runtime.GOOS == "darwin" {
			continue // macOS has a native process snapshot instead of pgrep
		}
		for _, body := range []string{"exec sleep 30", "sleep 4 &\nwait"} {
			t.Run(name+"/"+body, func(t *testing.T) {
				dir := t.TempDir()
				marker := filepath.Join(dir, "started")
				t.Setenv("NTM_PROCESS_CONTEXT_STARTED", marker)
				script := "#!/bin/sh\n: > \"$NTM_PROCESS_CONTEXT_STARTED\"\n" + body + "\n"
				if err := os.WriteFile(filepath.Join(dir, name), []byte(script), 0700); err != nil {
					t.Fatal(err)
				}
				t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
				ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
				defer cancel()
				done := make(chan int, 1)
				go func() {
					if name == "ps" {
						done <- len(GetCmdlineContext(ctx, 999999999))
					} else {
						done <- len(GetChildPIDsContext(ctx, 999999999, 8))
					}
				}()
				for {
					if _, err := os.Stat(marker); err == nil {
						break
					}
					select {
					case <-ctx.Done():
						t.Fatal("process query did not reach the portable command")
					case <-time.After(5 * time.Millisecond):
					}
				}
				cancel()
				select {
				case count := <-done:
					if count != 0 {
						t.Fatalf("canceled query returned %d results", count)
					}
				case <-time.After(time.Second):
					t.Fatal("process query ignored cancellation or waited on inherited output pipes")
				}
			})
		}
	}
}

func TestContextProcessQueriesRejectCanceledCaller(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if got := GetCmdlineContext(ctx, os.Getpid()); got != nil {
		t.Fatalf("canceled cmdline query = %v", got)
	}
	if got := GetChildPIDsContext(ctx, os.Getpid(), 8); got != nil {
		t.Fatalf("canceled children query = %v", got)
	}
}

func TestLivenessHelperProcess(t *testing.T) {
	if os.Getenv("NTM_PROCESS_HELPER") != "exit-immediately" {
		return
	}
	os.Exit(0)
}

func TestIsAlive_CurrentProcess(t *testing.T) {
	t.Parallel()
	pid := os.Getpid()
	if !IsAlive(pid) {
		t.Errorf("IsAlive(%d) = false, want true for current process", pid)
	}
}

func TestIsAlive_ZombieProcessReturnsFalse(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("zombie-state check relies on /proc state semantics")
	}

	cmd := exec.Command(os.Args[0], "-test.run=TestLivenessHelperProcess")
	cmd.Env = append(os.Environ(), "NTM_PROCESS_HELPER=exit-immediately")
	if err := cmd.Start(); err != nil {
		t.Fatalf("start helper: %v", err)
	}
	defer func() {
		_ = cmd.Wait()
	}()

	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		state, _, err := GetProcessState(cmd.Process.Pid)
		if err == nil && state == "Z" {
			if IsAlive(cmd.Process.Pid) {
				t.Fatalf("IsAlive(%d) = true, want false for zombie process", cmd.Process.Pid)
			}
			return
		}
		time.Sleep(10 * time.Millisecond)
	}

	t.Fatalf("helper process %d never reached zombie state", cmd.Process.Pid)
}

func TestIsAlive_InvalidPID(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		pid  int
	}{
		{"zero", 0},
		{"negative", -1},
		{"very large", 999999999},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			if IsAlive(tt.pid) {
				t.Errorf("IsAlive(%d) = true, want false", tt.pid)
			}
		})
	}
}

func TestGetChildPID_InvalidParent(t *testing.T) {
	t.Parallel()
	if pid := GetChildPID(0); pid != 0 {
		t.Errorf("GetChildPID(0) = %d, want 0", pid)
	}
	if pid := GetChildPID(-1); pid != 0 {
		t.Errorf("GetChildPID(-1) = %d, want 0", pid)
	}
}

func TestHasChildAlive_InvalidPID(t *testing.T) {
	t.Parallel()
	if HasChildAlive(0) {
		t.Error("HasChildAlive(0) = true, want false")
	}
	if HasChildAlive(-1) {
		t.Error("HasChildAlive(-1) = true, want false")
	}
}

func TestIsChildAlive_Alias(t *testing.T) {
	t.Parallel()
	// IsChildAlive should behave identically to HasChildAlive
	if IsChildAlive(0) {
		t.Error("IsChildAlive(0) = true, want false")
	}
	if IsChildAlive(-1) {
		t.Error("IsChildAlive(-1) = true, want false")
	}
}

func TestGetProcessState_CurrentProcess(t *testing.T) {
	t.Parallel()
	pid := os.Getpid()
	state, name, err := GetProcessState(pid)
	if err != nil {
		t.Fatalf("GetProcessState(%d) error = %v", pid, err)
	}
	// The current test process should report a live, non-terminal state.
	// Under load Linux can legitimately report D (disk sleep) or other
	// non-terminal codes, so don't overfit this to only R/S.
	if state == "" || state == "Z" || state == "X" || state == "x" {
		t.Errorf("GetProcessState(%d) state = %q, want a live non-terminal state", pid, state)
	}
	if name == "" {
		t.Error("GetProcessState() name should not be empty")
	}
}

func TestGetProcessState_InvalidPID(t *testing.T) {
	t.Parallel()
	_, _, err := GetProcessState(0)
	if err == nil {
		t.Error("GetProcessState(0) should return error")
	}
	_, _, err = GetProcessState(-1)
	if err == nil {
		t.Error("GetProcessState(-1) should return error")
	}
}

func TestGetProcessState_NonExistentProcess(t *testing.T) {
	t.Parallel()
	_, _, err := GetProcessState(999999999)
	if err == nil {
		t.Error("GetProcessState(999999999) should return error for non-existent process")
	}
}

func TestGetChildPID_CurrentProcess(t *testing.T) {
	t.Parallel()
	// The test process likely has no child processes
	pid := os.Getpid()
	child := GetChildPID(pid)
	// Just verify it doesn't panic; child may be 0 or a valid PID
	if child < 0 {
		t.Errorf("GetChildPID(%d) = %d, should not be negative", pid, child)
	}
}

func TestHasChildAlive_NonExistentProcess(t *testing.T) {
	t.Parallel()
	if HasChildAlive(999999999) {
		t.Error("HasChildAlive(999999999) = true, want false")
	}
}

func TestProcessStateNames(t *testing.T) {
	t.Parallel()
	// Verify the map covers common states
	expected := map[string]string{
		"R": "running",
		"S": "sleeping",
		"D": "disk sleep",
		"Z": "zombie",
		"T": "stopped",
		"I": "idle",
	}
	for code, name := range expected {
		if got := processStateNames[code]; got != name {
			t.Errorf("processStateNames[%q] = %q, want %q", code, got, name)
		}
	}
}

// StartTime is the pane-age source (ntm-qvpm): it must return a sane, past
// timestamp for a live PID and error for invalid ones.
func TestStartTime(t *testing.T) {
	started, err := StartTime(os.Getpid())
	if err != nil {
		t.Fatalf("StartTime(self): %v", err)
	}
	if started.IsZero() || started.After(time.Now().Add(time.Minute)) {
		t.Fatalf("StartTime(self) = %v, want past timestamp", started)
	}
	if time.Since(started) > 24*time.Hour {
		t.Fatalf("StartTime(self) = %v, implausibly old for a test process", started)
	}
	if _, err := StartTime(0); err == nil {
		t.Fatal("StartTime(0) should error")
	}
	if _, err := StartTime(-5); err == nil {
		t.Fatal("StartTime(-5) should error")
	}
}
