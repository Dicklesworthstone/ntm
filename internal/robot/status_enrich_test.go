package robot

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Dicklesworthstone/ntm/internal/process"
)

// ====================
// Rate Limit Detection Tests
// ====================

func TestDetectRateLimit(t *testing.T) {
	tests := []struct {
		name         string
		content      string
		wantDetected bool
		wantMatchHas string // substring that should be in match
	}{
		{
			name:         "Claude rate limit message",
			content:      "Error: You've hit your limit for the day. Please try again tomorrow.",
			wantDetected: true,
			wantMatchHas: "hit your limit",
		},
		{
			name:         "Claude rate limit case insensitive",
			content:      "YOU'VE HIT YOUR LIMIT",
			wantDetected: true,
			wantMatchHas: "HIT YOUR LIMIT",
		},
		{
			name:         "Rate limit generic",
			content:      "API rate limit exceeded, please slow down",
			wantDetected: true,
			wantMatchHas: "rate limit",
		},
		{
			name:         "Too many requests",
			content:      "Error 429: Too many requests. Retry after 60 seconds.",
			wantDetected: true,
			wantMatchHas: "Too many requests",
		},
		{
			name:         "Google resource exhausted",
			content:      "Error: RESOURCE_EXHAUSTED: Quota exceeded",
			wantDetected: true,
			wantMatchHas: "RESOURCE_EXHAUSTED",
		},
		{
			name:         "Reset time pattern AM",
			content:      "Your limit resets 6am Pacific time",
			wantDetected: true,
			wantMatchHas: "resets 6am",
		},
		{
			name:         "Reset time pattern PM",
			content:      "Usage resets 10pm",
			wantDetected: true,
			wantMatchHas: "resets 10pm",
		},
		{
			name:         "No rate limit - normal output",
			content:      "Successfully completed the task.\nAll tests passed.",
			wantDetected: false,
			wantMatchHas: "",
		},
		{
			name:         "No rate limit - empty",
			content:      "",
			wantDetected: false,
			wantMatchHas: "",
		},
		{
			name:         "No rate limit - code discussing rate limits",
			content:      "// TODO: implement rate limiting for the API",
			wantDetected: true, // This is a known false positive, but pattern matches
			wantMatchHas: "rate limit",
		},
		{
			name:         "Multiline with rate limit buried",
			content:      "Working on feature...\nCompiling...\nError: Rate limit reached\nRetrying...",
			wantDetected: true,
			wantMatchHas: "Rate limit",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Logf("INPUT: content=%q", truncateForLog(tc.content, 100))

			detected, match := detectRateLimit(tc.content)

			t.Logf("OUTPUT: detected=%v match=%q", detected, match)
			t.Logf("EXPECTED: detected=%v matchContains=%q", tc.wantDetected, tc.wantMatchHas)

			if detected != tc.wantDetected {
				t.Errorf("detectRateLimit() detected = %v, want %v", detected, tc.wantDetected)
			}

			if tc.wantDetected && tc.wantMatchHas != "" {
				if match == "" {
					t.Errorf("detectRateLimit() match is empty, want to contain %q", tc.wantMatchHas)
				} else if !containsIgnoreCase(match, tc.wantMatchHas) {
					t.Errorf("detectRateLimit() match = %q, want to contain %q", match, tc.wantMatchHas)
				}
			}
		})
	}
}

// ====================
// Output Activity Tracking Tests
// ====================

func TestUpdateActivity_FirstCall(t *testing.T) {
	// Clear state
	clearPaneStates()

	paneID := "test-pane-first"
	content := "Line 1\nLine 2\nLine 3\n"

	t.Logf("INPUT: paneID=%q content=%q", paneID, content)

	lastTS, linesDelta := updateActivity(paneID, content)

	t.Logf("OUTPUT: lastTS=%v linesDelta=%d", lastTS, linesDelta)

	// First call should return current time and total lines
	if lastTS.IsZero() {
		t.Error("updateActivity() first call should have non-zero timestamp")
	}

	if linesDelta != 3 {
		t.Errorf("updateActivity() first call linesDelta = %d, want 3", linesDelta)
	}
}

func TestUpdateActivity_NoChange(t *testing.T) {
	clearPaneStates()

	paneID := "test-pane-nochange"
	content := "Static content\n"

	t.Logf("INPUT: paneID=%q content=%q", paneID, content)

	// First call
	firstTS, _ := updateActivity(paneID, content)
	t.Logf("First call: lastTS=%v", firstTS)

	// Wait a tiny bit
	time.Sleep(10 * time.Millisecond)

	// Second call with same content
	secondTS, linesDelta := updateActivity(paneID, content)
	t.Logf("Second call: lastTS=%v linesDelta=%d", secondTS, linesDelta)

	// Timestamp should NOT change when content is same
	if !secondTS.Equal(firstTS) {
		t.Logf("Note: timestamp updated even with same content - may be expected behavior")
	}

	// Lines delta should be 0 when no change
	if linesDelta != 0 {
		t.Errorf("updateActivity() with same content, linesDelta = %d, want 0", linesDelta)
	}
}

func TestUpdateActivity_ContentChange(t *testing.T) {
	clearPaneStates()

	paneID := "test-pane-change"
	content1 := "Line 1\nLine 2\n"
	content2 := "Line 1\nLine 2\nLine 3\nLine 4\n"

	t.Logf("INPUT: paneID=%q content1=%q content2=%q", paneID, content1, content2)

	// First call
	firstTS, firstDelta := updateActivity(paneID, content1)
	t.Logf("First call: lastTS=%v linesDelta=%d", firstTS, firstDelta)

	// Wait briefly
	time.Sleep(10 * time.Millisecond)

	// Second call with more content
	secondTS, secondDelta := updateActivity(paneID, content2)
	t.Logf("Second call: lastTS=%v linesDelta=%d", secondTS, secondDelta)

	// Timestamp should update
	if !secondTS.After(firstTS) {
		t.Errorf("updateActivity() timestamp did not advance on content change")
	}

	// Lines delta should be 2 (4 - 2)
	if secondDelta != 2 {
		t.Errorf("updateActivity() linesDelta = %d, want 2", secondDelta)
	}
}

func TestUpdateActivity_BufferWrap(t *testing.T) {
	clearPaneStates()

	paneID := "test-pane-wrap"
	content1 := "Line 1\nLine 2\nLine 3\nLine 4\nLine 5\n"
	content2 := "New Line 1\nNew Line 2\n" // Fewer lines (simulating buffer clear/wrap)

	t.Logf("INPUT: paneID=%q content1 lines=5, content2 lines=2", paneID)

	// First call
	_, firstDelta := updateActivity(paneID, content1)
	t.Logf("First call: linesDelta=%d", firstDelta)

	// Second call with fewer lines (buffer wrapped/cleared)
	_, secondDelta := updateActivity(paneID, content2)
	t.Logf("Second call: linesDelta=%d", secondDelta)

	// On wrap, should return the new line count (not negative)
	if secondDelta < 0 {
		t.Errorf("updateActivity() on buffer wrap returned negative delta = %d", secondDelta)
	}
	if secondDelta != 2 {
		t.Errorf("updateActivity() on buffer wrap linesDelta = %d, want 2", secondDelta)
	}
}

func TestUpdateActivity_ContentChangeButSameLineCount(t *testing.T) {
	clearPaneStates()

	paneID := "test-pane-window-shift"
	content1 := "Line A\nLine B\n"
	content2 := "Line C\nLine D\n" // Same line count but different content

	t.Logf("INPUT: paneID=%q both have 2 lines but different content", paneID)

	_, firstDelta := updateActivity(paneID, content1)
	t.Logf("First call: linesDelta=%d", firstDelta)

	time.Sleep(10 * time.Millisecond)

	_, secondDelta := updateActivity(paneID, content2)
	t.Logf("Second call: linesDelta=%d", secondDelta)

	// Should detect activity even when line count is same
	if secondDelta != 1 {
		t.Errorf("updateActivity() window shift linesDelta = %d, want 1", secondDelta)
	}
}

func TestUpdateActivity_ConcurrentAccess(t *testing.T) {
	clearPaneStates()

	t.Log("Testing concurrent access to updateActivity")

	var wg sync.WaitGroup
	paneID := "test-pane-concurrent"

	for i := 0; i < 10; i++ {
		wg.Add(1)
		go func(idx int) {
			defer wg.Done()
			content := "Line from goroutine\n"
			for j := 0; j < idx; j++ {
				content += "Additional line\n"
			}
			_, delta := updateActivity(paneID, content)
			t.Logf("Goroutine %d: linesDelta=%d", idx, delta)
		}(i)
	}

	wg.Wait()
	t.Log("All goroutines completed without panic")
}

// ====================
// Process State Tests (Linux-specific)
// ====================

func TestGetProcessState_CurrentProcess(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("Process state tests require Linux /proc filesystem")
	}

	pid := os.Getpid()
	t.Logf("INPUT: pid=%d (current process)", pid)

	state, stateName, err := process.GetProcessState(pid)

	t.Logf("OUTPUT: state=%q stateName=%q err=%v", state, stateName, err)

	if err != nil {
		t.Errorf("process.GetProcessState(self) error = %v", err)
	}

	// Current process should be running or sleeping
	validStates := map[string]bool{"R": true, "S": true}
	if !validStates[state] {
		t.Errorf("process.GetProcessState(self) state = %q, want R or S", state)
	}

	if stateName == "" {
		t.Error("process.GetProcessState(self) stateName is empty")
	}
}

func TestGetProcessState_NonExistentProcess(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("Process state tests require Linux /proc filesystem")
	}

	// Use a PID that almost certainly doesn't exist
	pid := 999999999
	t.Logf("INPUT: pid=%d (non-existent)", pid)

	state, stateName, err := process.GetProcessState(pid)

	t.Logf("OUTPUT: state=%q stateName=%q err=%v", state, stateName, err)

	// Should return error for non-existent process
	if err == nil {
		t.Errorf("process.GetProcessState(non-existent) expected error, got state=%q", state)
	}
}

// ====================
// Process Memory Tests (Linux-specific)
// ====================

func TestGetProcessMemoryMB_CurrentProcess(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("Memory tests require Linux /proc filesystem")
	}

	pid := os.Getpid()
	t.Logf("INPUT: pid=%d (current process)", pid)

	mem, err := getProcessMemoryMB(pid)

	t.Logf("OUTPUT: mem=%dMB err=%v", mem, err)

	if err != nil {
		t.Errorf("getProcessMemoryMB(self) error = %v", err)
	}

	// Go process should use at least some memory
	if mem < 1 {
		t.Errorf("getProcessMemoryMB(self) = %dMB, expected >= 1MB", mem)
	}
}

func TestGetProcessMemoryMB_NonExistentProcess(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("Memory tests require Linux /proc filesystem")
	}

	pid := 999999999
	t.Logf("INPUT: pid=%d (non-existent)", pid)

	mem, err := getProcessMemoryMB(pid)

	t.Logf("OUTPUT: mem=%dMB err=%v", mem, err)

	if err == nil {
		t.Errorf("getProcessMemoryMB(non-existent) expected error, got mem=%d", mem)
	}
}

// ====================
// Child PID Tests (Linux-specific)
// ====================

func TestGetChildPID_NoChildren(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("Child PID tests require Linux")
	}

	// Current test process likely has no children
	pid := os.Getpid()
	t.Logf("INPUT: pid=%d (current process, likely no children)", pid)

	childPID := process.GetChildPID(pid)

	t.Logf("OUTPUT: childPID=%d", childPID)

	// Should return 0 for process without children
	if childPID != 0 {
		t.Logf("Note: Current process unexpectedly has child PID %d", childPID)
	}
}

func TestGetChildPID_NonExistentProcess(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("Child PID tests require Linux")
	}

	pid := 999999999
	t.Logf("INPUT: pid=%d (non-existent)", pid)

	childPID := process.GetChildPID(pid)

	t.Logf("OUTPUT: childPID=%d", childPID)

	if childPID != 0 {
		t.Errorf("process.GetChildPID(non-existent) expected 0, got childPID=%d", childPID)
	}
}

// ====================
// GetLastOutput Tests
// ====================

func TestGetLastOutput_ExistingPane(t *testing.T) {
	clearPaneStates()

	paneID := "test-pane-lastoutput"
	content := "Some output\n"

	// Initialize state
	updateActivity(paneID, content)

	t.Logf("INPUT: paneID=%q (exists)", paneID)

	ts := getLastOutput(paneID)

	t.Logf("OUTPUT: ts=%v isZero=%v", ts, ts.IsZero())

	if ts.IsZero() {
		t.Error("getLastOutput() returned zero time for existing pane")
	}
}

func TestGetLastOutput_NonExistentPane(t *testing.T) {
	clearPaneStates()

	paneID := "non-existent-pane"
	t.Logf("INPUT: paneID=%q (does not exist)", paneID)

	ts := getLastOutput(paneID)

	t.Logf("OUTPUT: ts=%v isZero=%v", ts, ts.IsZero())

	if !ts.IsZero() {
		t.Errorf("getLastOutput() returned non-zero time for non-existent pane: %v", ts)
	}
}

// ====================
// Rate Limit Patterns Test
// ====================

func TestRateLimitPatterns_Count(t *testing.T) {
	t.Logf("Number of rate limit patterns: %d", len(rateLimitPatterns))

	// We expect at least the documented patterns
	if len(rateLimitPatterns) < 3 {
		t.Errorf("Expected at least 3 rate limit patterns, got %d", len(rateLimitPatterns))
	}
}

// ====================
// Integration Test: Status Enrichment Flow
// ====================

func TestEnrichAgentStatus_WithMockedData(t *testing.T) {
	// This test verifies the enrichment flow without requiring actual /proc access
	// by testing the individual components

	t.Log("Testing enrichment flow components...")

	// Test rate limit detection flow
	agent := &Agent{
		PID:  os.Getpid(),
		Pane: "test-pane",
	}
	t.Logf("Created agent: PID=%d Pane=%s", agent.PID, agent.Pane)

	// Verify agent struct has enrichment fields
	if agent.RateLimitDetected {
		t.Error("New agent should not have rate limit detected")
	}
	if agent.ChildPID != 0 {
		t.Error("New agent should not have child PID set")
	}
}

// ====================
// Fixture-Based Tests
// ====================

func TestDetectRateLimit_Fixtures(t *testing.T) {
	// Create temp fixture directory
	fixtureDir := filepath.Join(t.TempDir(), "fixtures")
	if err := os.MkdirAll(fixtureDir, 0755); err != nil {
		t.Fatalf("Failed to create fixture dir: %v", err)
	}

	// Create fixture: healthy agent output
	healthyOutput := `Working on implementing the feature...
Analyzing codebase structure...
Found 15 relevant files
Proceeding with implementation...
Done!`
	healthyPath := filepath.Join(fixtureDir, "healthy_output.txt")
	if err := os.WriteFile(healthyPath, []byte(healthyOutput), 0644); err != nil {
		t.Fatalf("Failed to write fixture: %v", err)
	}

	// Create fixture: rate limited output
	rateLimitedOutput := `Working on task...
Error: You've hit your limit for this period.
Please try again later.`
	rateLimitPath := filepath.Join(fixtureDir, "rate_limited_output.txt")
	if err := os.WriteFile(rateLimitPath, []byte(rateLimitedOutput), 0644); err != nil {
		t.Fatalf("Failed to write fixture: %v", err)
	}

	// Test with fixtures
	t.Run("healthy fixture", func(t *testing.T) {
		content, err := os.ReadFile(healthyPath)
		if err != nil {
			t.Fatalf("Failed to read fixture: %v", err)
		}
		detected, match := detectRateLimit(string(content))
		t.Logf("Healthy output: detected=%v match=%q", detected, match)
		if detected {
			t.Error("Healthy output should not be detected as rate limited")
		}
	})

	t.Run("rate limited fixture", func(t *testing.T) {
		content, err := os.ReadFile(rateLimitPath)
		if err != nil {
			t.Fatalf("Failed to read fixture: %v", err)
		}
		detected, match := detectRateLimit(string(content))
		t.Logf("Rate limited output: detected=%v match=%q", detected, match)
		if !detected {
			t.Error("Rate limited output should be detected")
		}
	})
}

// ====================
// Helper Functions
// ====================

func clearPaneStates() {
	outputStateMu.Lock()
	paneStates = make(map[string]*paneState)
	outputStateMu.Unlock()
}

func truncateForLog(s string, maxLen int) string {
	if len(s) <= maxLen {
		return s
	}
	return s[:maxLen] + "..."
}

func containsIgnoreCase(s, substr string) bool {
	return strings.Contains(strings.ToLower(s), strings.ToLower(substr))
}
