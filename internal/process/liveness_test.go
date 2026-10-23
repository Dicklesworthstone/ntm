package process

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"testing"
	"time"
)

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
