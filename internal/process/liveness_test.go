package process

import (
	"os"
	"testing"
)

func TestIsAlive_CurrentProcess(t *testing.T) {
	t.Parallel()
	pid := os.Getpid()
	if !IsAlive(pid) {
		t.Errorf("IsAlive(%d) = false, want true for current process", pid)
	}
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
	// Current test process should be R (running) or S (sleeping)
	if state != "R" && state != "S" {
		t.Errorf("GetProcessState(%d) state = %q, want R or S", pid, state)
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
