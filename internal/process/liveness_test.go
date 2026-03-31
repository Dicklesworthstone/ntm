package process

import (
	"os"
	"os/exec"
	"runtime"
	"testing"
	"time"
)

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
