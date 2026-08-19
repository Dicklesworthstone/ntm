package rano

import (
	"os"
	"strconv"
	"testing"
	"time"

	"github.com/Dicklesworthstone/ntm/internal/tmux"
)

func TestPaneIdentityString(t *testing.T) {
	tests := []struct {
		name     string
		identity PaneIdentity
		want     string
	}{
		{
			name: "with pane title",
			identity: PaneIdentity{
				Session:   "myproject",
				PaneIndex: 1,
				PaneTitle: "myproject__cc_1",
				AgentType: tmux.AgentClaude,
			},
			want: "myproject__cc_1",
		},
		{
			name: "without pane title",
			identity: PaneIdentity{
				Session:   "myproject",
				PaneIndex: 2,
				PaneTitle: "",
			},
			want: "myproject:2",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := tt.identity.String()
			if got != tt.want {
				t.Errorf("PaneIdentity.String() = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestNewPIDMap(t *testing.T) {
	t.Run("with session", func(t *testing.T) {
		m := NewPIDMap("myproject")
		if m == nil {
			t.Fatal("NewPIDMap returned nil")
		}
		if m.session != "myproject" {
			t.Errorf("session = %q, want %q", m.session, "myproject")
		}
	})

	t.Run("without session", func(t *testing.T) {
		m := NewPIDMap("")
		if m == nil {
			t.Fatal("NewPIDMap returned nil")
		}
		if m.session != "" {
			t.Errorf("session = %q, want empty", m.session)
		}
	})
}

func TestPIDMapOperations(t *testing.T) {
	m := NewPIDMap("test")

	// Manually populate the map for testing
	m.mu.Lock()
	identity := &PaneIdentity{
		Session:   "test",
		PaneIndex: 0,
		PaneTitle: "test__cc_1",
		AgentType: tmux.AgentClaude,
		NTMIndex:  1,
	}
	m.paneToShellPID["test__cc_1"] = 1234
	m.pidToPane[1234] = identity
	m.pidToPane[1235] = identity // child process
	m.pidToPane[1236] = identity // another child
	m.shellToChildren[1234] = []int{1235, 1236}
	m.lastRefresh = time.Now()
	m.mu.Unlock()

	t.Run("GetPaneForPID shell", func(t *testing.T) {
		got := m.GetPaneForPID(1234)
		if got == nil {
			t.Fatal("GetPaneForPID returned nil for shell PID")
		}
		if got.PaneTitle != "test__cc_1" {
			t.Errorf("got pane title %q, want %q", got.PaneTitle, "test__cc_1")
		}
	})

	t.Run("GetPaneForPID child", func(t *testing.T) {
		got := m.GetPaneForPID(1235)
		if got == nil {
			t.Fatal("GetPaneForPID returned nil for child PID")
		}
		if got.PaneTitle != "test__cc_1" {
			t.Errorf("got pane title %q, want %q", got.PaneTitle, "test__cc_1")
		}
	})

	t.Run("GetPaneForPID unknown", func(t *testing.T) {
		got := m.GetPaneForPID(9999)
		if got != nil {
			t.Error("GetPaneForPID should return nil for unknown PID")
		}
	})

	t.Run("GetShellPID", func(t *testing.T) {
		got := m.GetShellPID("test__cc_1")
		if got != 1234 {
			t.Errorf("GetShellPID = %d, want %d", got, 1234)
		}
	})

	t.Run("GetShellPID unknown", func(t *testing.T) {
		got := m.GetShellPID("unknown__pane")
		if got != 0 {
			t.Errorf("GetShellPID should return 0 for unknown pane, got %d", got)
		}
	})

	t.Run("GetAllPIDsForPane", func(t *testing.T) {
		pids := m.GetAllPIDsForPane("test__cc_1")
		if len(pids) != 3 {
			t.Errorf("GetAllPIDsForPane returned %d PIDs, want 3", len(pids))
		}
		// Should include shell + children
		pidSet := make(map[int]bool)
		for _, pid := range pids {
			pidSet[pid] = true
		}
		for _, expected := range []int{1234, 1235, 1236} {
			if !pidSet[expected] {
				t.Errorf("missing expected PID %d", expected)
			}
		}
	})

	t.Run("GetPIDLabels", func(t *testing.T) {
		labels := m.GetPIDLabels()
		if len(labels) != 3 {
			t.Errorf("GetPIDLabels returned %d entries, want 3", len(labels))
		}
		if labels[1234] != "test__cc_1" {
			t.Errorf("label for PID 1234 = %q, want %q", labels[1234], "test__cc_1")
		}
	})

	t.Run("GetStats", func(t *testing.T) {
		stats := m.GetStats()
		if stats.PaneCount != 1 {
			t.Errorf("PaneCount = %d, want 1", stats.PaneCount)
		}
		if stats.TotalPIDCount != 3 {
			t.Errorf("TotalPIDCount = %d, want 3", stats.TotalPIDCount)
		}
		if stats.ShellPIDCount != 1 {
			t.Errorf("ShellPIDCount = %d, want 1", stats.ShellPIDCount)
		}
		if stats.ChildPIDCount != 2 {
			t.Errorf("ChildPIDCount = %d, want 2", stats.ChildPIDCount)
		}
		if stats.ByAgentType["cc"] != 3 {
			t.Errorf("ByAgentType[cc] = %d, want 3", stats.ByAgentType["cc"])
		}
	})

	t.Run("LastRefresh", func(t *testing.T) {
		lastRefresh := m.LastRefresh()
		if time.Since(lastRefresh) > time.Minute {
			t.Error("LastRefresh seems too old")
		}
	})
}

func TestGetParentPID(t *testing.T) {
	// Test with current process
	currentPID := os.Getpid()
	ppid, err := getParentPID(currentPID)
	if err != nil {
		t.Skipf("Skipping getParentPID test: %v (may not have /proc)", err)
	}

	expectedPPID := os.Getppid()
	if ppid != expectedPPID {
		t.Errorf("getParentPID(%d) = %d, want %d", currentPID, ppid, expectedPPID)
	}
}

func TestGetChildPIDs(t *testing.T) {
	// This test verifies the function doesn't crash and returns a valid result.
	// On systems without /proc, it should gracefully return an empty slice or error.
	currentPID := os.Getpid()
	children, err := getChildPIDs(currentPID)
	if err != nil {
		// If /proc isn't available, skip the test
		if _, statErr := os.Stat("/proc"); os.IsNotExist(statErr) {
			t.Skip("Skipping: /proc filesystem not available")
		}
		// Otherwise, log but don't fail (process may legitimately have no children)
		t.Logf("getChildPIDs returned error: %v (may be expected if no children)", err)
	}

	// Just verify it returns a slice (may be empty)
	if children == nil {
		t.Log("getChildPIDs returned nil (no children or error)")
	} else {
		t.Logf("getChildPIDs found %d children for PID %d", len(children), currentPID)
	}
}

func TestProcStatParsing(t *testing.T) {
	// Test edge cases in stat parsing
	tests := []struct {
		name     string
		statLine string
		wantPPID int
		wantErr  bool
	}{
		{
			name:     "normal process",
			statLine: "1234 (bash) S 1230 1234 1234 34817 1234 4194304 1234 0 0 0 0 0 0 0 20 0 1 0",
			wantPPID: 1230,
			wantErr:  false,
		},
		{
			name:     "process with spaces in name",
			statLine: "1234 (my process) S 1230 1234 1234 34817 1234 4194304",
			wantPPID: 1230,
			wantErr:  false,
		},
		{
			name:     "process with parens in name",
			statLine: "1234 (my (weird) proc) S 1230 1234 1234 34817",
			wantPPID: 1230,
			wantErr:  false,
		},
		{
			name:     "malformed - no parens",
			statLine: "1234 bash S 1230 1234",
			wantPPID: 0,
			wantErr:  true,
		},
		{
			name:     "malformed - too short",
			statLine: "1234 (bash) S",
			wantPPID: 0,
			wantErr:  true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Parse the same way getParentPID does
			line := tt.statLine
			lastParen := len(line) - 1
			for i := len(line) - 1; i >= 0; i-- {
				if line[i] == ')' {
					lastParen = i
					break
				}
			}

			if lastParen == len(line)-1 && line[lastParen] != ')' {
				if !tt.wantErr {
					t.Errorf("expected to find ')' in stat line")
				}
				return
			}

			fields := make([]string, 0)
			if lastParen < len(line)-1 {
				for _, f := range []byte(line[lastParen+1:]) {
					// Skip leading space
					if f == ' ' && len(fields) == 0 {
						continue
					}
					// Build up fields
					if f == ' ' {
						fields = append(fields, "")
					} else if len(fields) > 0 {
						fields[len(fields)-1] += string(f)
					} else {
						fields = append(fields, string(f))
					}
				}
			}

			// Clean fields
			cleanFields := make([]string, 0)
			for _, f := range fields {
				f = trimSpace(f)
				if f != "" {
					cleanFields = append(cleanFields, f)
				}
			}

			if len(cleanFields) < 2 {
				if !tt.wantErr {
					t.Errorf("expected at least 2 fields after comm, got %d", len(cleanFields))
				}
				return
			}

			ppid, err := strconv.Atoi(cleanFields[1])
			if err != nil {
				if !tt.wantErr {
					t.Errorf("failed to parse PPID: %v", err)
				}
				return
			}

			if tt.wantErr {
				t.Errorf("expected error but parsing succeeded")
				return
			}

			if ppid != tt.wantPPID {
				t.Errorf("PPID = %d, want %d", ppid, tt.wantPPID)
			}
		})
	}
}

func trimSpace(s string) string {
	result := ""
	for _, c := range s {
		if c != ' ' && c != '\t' {
			result += string(c)
		}
	}
	return result
}

func TestGetAllPIDsForPaneUnknown(t *testing.T) {
	t.Parallel()

	m := NewPIDMap("test")

	// Test with unknown pane
	pids := m.GetAllPIDsForPane("unknown__pane")
	if pids != nil {
		t.Errorf("GetAllPIDsForPane for unknown pane should return nil, got %v", pids)
	}
}

func TestGetStatsEmptyMap(t *testing.T) {
	t.Parallel()

	m := NewPIDMap("test")
	stats := m.GetStats()

	if stats.PaneCount != 0 {
		t.Errorf("PaneCount = %d, want 0", stats.PaneCount)
	}
	if stats.TotalPIDCount != 0 {
		t.Errorf("TotalPIDCount = %d, want 0", stats.TotalPIDCount)
	}
	if stats.ShellPIDCount != 0 {
		t.Errorf("ShellPIDCount = %d, want 0", stats.ShellPIDCount)
	}
	if stats.ChildPIDCount != 0 {
		t.Errorf("ChildPIDCount = %d, want 0", stats.ChildPIDCount)
	}
	if stats.Session != "test" {
		t.Errorf("Session = %q, want %q", stats.Session, "test")
	}
	if len(stats.ByAgentType) != 0 {
		t.Errorf("ByAgentType = %v, want empty map", stats.ByAgentType)
	}
	if !stats.LastRefresh.IsZero() {
		t.Errorf("LastRefresh should be zero for new map, got %v", stats.LastRefresh)
	}
}

func TestGetPIDLabelsEmpty(t *testing.T) {
	t.Parallel()

	m := NewPIDMap("test")
	labels := m.GetPIDLabels()

	if len(labels) != 0 {
		t.Errorf("GetPIDLabels for empty map should return empty map, got %v", labels)
	}
}

func TestLastRefreshEmpty(t *testing.T) {
	t.Parallel()

	m := NewPIDMap("test")
	lastRefresh := m.LastRefresh()

	if !lastRefresh.IsZero() {
		t.Errorf("LastRefresh for new map should be zero, got %v", lastRefresh)
	}
}

func TestGetStatsWithMixedAgentTypes(t *testing.T) {
	t.Parallel()

	m := NewPIDMap("test")

	// Populate with multiple agent types
	m.mu.Lock()
	m.pidToPane[1000] = &PaneIdentity{AgentType: tmux.AgentClaude}
	m.pidToPane[1001] = &PaneIdentity{AgentType: tmux.AgentClaude}
	m.pidToPane[1002] = &PaneIdentity{AgentType: tmux.AgentCodex}
	m.pidToPane[1003] = &PaneIdentity{AgentType: tmux.AgentGemini}
	m.pidToPane[1004] = &PaneIdentity{AgentType: ""} // no agent type
	m.mu.Unlock()

	stats := m.GetStats()

	if stats.TotalPIDCount != 5 {
		t.Errorf("TotalPIDCount = %d, want 5", stats.TotalPIDCount)
	}
	if stats.ByAgentType["cc"] != 2 {
		t.Errorf("ByAgentType[cc] = %d, want 2", stats.ByAgentType["cc"])
	}
	if stats.ByAgentType["cod"] != 1 {
		t.Errorf("ByAgentType[cod] = %d, want 1", stats.ByAgentType["cod"])
	}
	if stats.ByAgentType["gmi"] != 1 {
		t.Errorf("ByAgentType[gmi] = %d, want 1", stats.ByAgentType["gmi"])
	}
	// Empty agent type should not be counted
	if _, exists := stats.ByAgentType[""]; exists {
		t.Error("empty agent type should not be in ByAgentType")
	}
}

func TestGetChildPIDsWithKnownParent(t *testing.T) {
	// Test with PID 1 which always exists on Linux
	if _, err := os.Stat("/proc/1"); os.IsNotExist(err) {
		t.Skip("Skipping: /proc/1 not available")
	}

	children, err := getChildPIDs(1)
	if err != nil {
		t.Logf("getChildPIDs(1) returned error: %v", err)
		// This is fine, PID 1 may have restricted access
		return
	}

	// PID 1 (init/systemd) should have many children
	t.Logf("PID 1 has %d child processes", len(children))
	// We don't assert a specific count since it varies by system
}

func TestGetChildPIDsNonexistent(t *testing.T) {
	if _, err := os.Stat("/proc"); os.IsNotExist(err) {
		t.Skip("Skipping: /proc filesystem not available")
	}

	// Use a very high PID that's unlikely to exist
	children, err := getChildPIDs(999999999)
	if err != nil {
		// Expected - PID doesn't exist
		t.Logf("getChildPIDs(999999999) returned expected error: %v", err)
		return
	}

	// Should return empty list
	if len(children) != 0 {
		t.Errorf("expected empty children list for nonexistent PID, got %d", len(children))
	}
}

func TestGetParentPIDNonexistent(t *testing.T) {
	if _, err := os.Stat("/proc"); os.IsNotExist(err) {
		t.Skip("Skipping: /proc filesystem not available")
	}

	// Use a very high PID that's unlikely to exist
	_, err := getParentPID(999999999)
	if err == nil {
		t.Error("getParentPID for nonexistent PID should return error")
	}
}

func TestPaneIdentityStringVariants(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		identity PaneIdentity
		want     string
	}{
		{
			name: "full identity with title",
			identity: PaneIdentity{
				Session:   "project",
				PaneIndex: 3,
				PaneTitle: "project__cc_2_opus",
				AgentType: tmux.AgentClaude,
				NTMIndex:  2,
			},
			want: "project__cc_2_opus",
		},
		{
			name: "no title falls back to session:index",
			identity: PaneIdentity{
				Session:   "myproject",
				PaneIndex: 5,
				PaneTitle: "",
			},
			want: "myproject:5",
		},
		{
			name: "zero index with title",
			identity: PaneIdentity{
				Session:   "project",
				PaneIndex: 0,
				PaneTitle: "project__cod_1",
			},
			want: "project__cod_1",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := tt.identity.String()
			if got != tt.want {
				t.Errorf("PaneIdentity.String() = %q, want %q", got, tt.want)
			}
		})
	}
}
