package status

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// loadOmpFixture reads a verbatim omp v18.2.3 tmux capture from the agent
// package's testdata, the single home of the omp chrome fixtures.
func loadOmpFixture(t *testing.T, name string) string {
	t.Helper()
	data, err := os.ReadFile(filepath.Join("..", "agent", "testdata", name))
	if err != nil {
		t.Fatalf("load omp fixture %s: %v", name, err)
	}
	return string(data)
}

func TestDetectIdleFromOutput_Omp(t *testing.T) {
	cases := map[string]bool{
		"omp_nerd_idle_fresh.txt":      true,
		"omp_nerd_idle_done.txt":       true,
		"omp_nerd_interrupted.txt":     true,
		"omp_nerd_draft.txt":           true,
		"omp_ascii_idle_done.txt":      true,
		"omp_unicode_idle_done.txt":    true,
		"omp_nerd_working.txt":         false,
		"omp_nerd_tool_working.txt":    false,
		"omp_unicode_working.txt":      false,
		"omp_unicode_steering.txt":     false,
		"omp_ascii_working.txt":        false,
		"omp_nerd_autocomplete.txt":    false,
		"omp_nerd_exited_to_shell.txt": false,
		"omp_nerd_model_selector.txt":  false,
	}
	for file, want := range cases {
		t.Run(file, func(t *testing.T) {
			out := loadOmpFixture(t, file)
			if got := DetectIdleFromOutput(out, "omp"); got != want {
				t.Fatalf("DetectIdleFromOutput(omp) = %v, want %v", got, want)
			}
			if got := DetectIdleFromOutput(out, "oh-my-pi"); got != want {
				t.Fatalf("alias must canonicalize to omp: got %v, want %v", got, want)
			}
		})
	}
}

func TestDetermineState_Omp(t *testing.T) {
	d := NewDetector()
	recent := time.Now()
	stale := recent.Add(-time.Hour)
	for _, tc := range []struct {
		file         string
		lastActivity time.Time
		want         AgentState
	}{
		// A quiet composer is idle even while a busy sibling keeps the
		// window-scoped activity timestamp fresh.
		{"omp_nerd_idle_done.txt", recent, StateIdle},
		{"omp_ascii_idle_done.txt", recent, StateIdle},
		// In-flight chrome is working even at low velocity (a long
		// thinking step produces no new lines).
		{"omp_nerd_working.txt", stale, StateWorking},
		{"omp_nerd_tool_working.txt", stale, StateWorking},
		{"omp_unicode_steering.txt", stale, StateWorking},
	} {
		t.Run(tc.file, func(t *testing.T) {
			if got, _ := d.determineState(loadOmpFixture(t, tc.file), "omp", tc.lastActivity); got != tc.want {
				t.Fatalf("determineState = %v, want %v", got, tc.want)
			}
		})
	}
	// A failed turn (dismissable provider-error block above the quiet
	// composer) is an error, not idle-after-completion, even while a busy
	// sibling keeps the activity clock fresh.
	for file, wantType := range map[string]ErrorType{
		"omp_nerd_provider_error.txt":    ErrorGeneric,
		"omp_nerd_provider_error_f5.txt": ErrorGeneric,
		"omp_nerd_api_error.txt":         ErrorAuth,
	} {
		if got, errType := d.determineState(loadOmpFixture(t, file), "omp", recent); got != StateError || errType != wantType {
			t.Fatalf("determineState(%s) = (%v, %v), want (error, %v)", file, got, errType, wantType)
		}
	}
	if c := observationConfidence(AgentStatus{State: StateIdle, AgentType: "omp"}, loadOmpFixture(t, "omp_nerd_idle_done.txt")); c < 0.9 {
		t.Fatalf("idle omp confidence = %v, want actionable", c)
	}
}

func TestAnalyze_OmpContextUsageAndPreview(t *testing.T) {
	d := NewDetector()
	st := d.Analyze("%1", "proj__omp_1", "omp", loadOmpFixture(t, "omp_nerd_tool_working.txt"), time.Now().Add(-time.Hour))
	if st.State != StateWorking {
		t.Fatalf("State = %v, want working", st.State)
	}
	if st.ContextUsage != 7 {
		t.Fatalf("ContextUsage = %v, want 7 (from the embedded context gauge)", st.ContextUsage)
	}
	// The preview is the transcript, never the pinned chrome.
	if strings.Contains(st.LastOutput, "Union Alpha") || strings.Contains(st.LastOutput, "Sleeping 25 seconds") {
		t.Fatalf("LastOutput leaked omp chrome: %q", st.LastOutput)
	}
	if !strings.Contains(st.LastOutput, "sleep 25") {
		t.Fatalf("LastOutput lost the transcript: %q", st.LastOutput)
	}
}
