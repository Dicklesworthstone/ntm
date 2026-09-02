package robot

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/Dicklesworthstone/ntm/internal/agent"
	"github.com/Dicklesworthstone/ntm/internal/tmux"
)

// =============================================================================
// Health Classification Tests (bd-1alai)
// =============================================================================

func TestDetermineOverallHealth(t *testing.T) {
	tests := []struct {
		name    string
		summary DiagnoseSummary
		want    string
	}{
		// Healthy cases
		{
			name: "all healthy",
			summary: DiagnoseSummary{
				TotalPanes: 4,
				Healthy:    4,
			},
			want: "healthy",
		},
		{
			name: "empty session",
			summary: DiagnoseSummary{
				TotalPanes: 0,
			},
			want: "healthy",
		},
		{
			name: "single healthy pane",
			summary: DiagnoseSummary{
				TotalPanes: 1,
				Healthy:    1,
			},
			want: "healthy",
		},

		// Degraded cases
		{
			name: "one rate limited",
			summary: DiagnoseSummary{
				TotalPanes:  4,
				Healthy:     3,
				RateLimited: 1,
			},
			want: "degraded",
		},
		{
			name: "one degraded (non-rate limited)",
			summary: DiagnoseSummary{
				TotalPanes: 4,
				Healthy:    3,
				Degraded:   1, // We will need to add this field to DiagnoseSummary
			},
			want: "degraded",
		},
		{
			name: "one unresponsive minority",
			summary: DiagnoseSummary{
				TotalPanes:   4,
				Healthy:      3,
				Unresponsive: 1,
			},
			want: "degraded",
		},
		{
			name: "one unknown",
			summary: DiagnoseSummary{
				TotalPanes: 4,
				Healthy:    3,
				Unknown:    1,
			},
			want: "degraded",
		},
		{
			name: "multiple degraded states",
			summary: DiagnoseSummary{
				TotalPanes:   8,
				Healthy:      5,
				RateLimited:  2,
				Unresponsive: 1,
			},
			want: "degraded",
		},

		// Critical cases
		{
			name: "any crashed pane",
			summary: DiagnoseSummary{
				TotalPanes: 4,
				Healthy:    3,
				Crashed:    1,
			},
			want: "critical",
		},
		{
			name: "majority unresponsive",
			summary: DiagnoseSummary{
				TotalPanes:   4,
				Healthy:      1,
				Unresponsive: 3,
			},
			want: "critical",
		},
		{
			name: "equal unresponsive and healthy",
			summary: DiagnoseSummary{
				TotalPanes:   4,
				Healthy:      2,
				Unresponsive: 2,
			},
			want: "degraded", // Not majority, so degraded not critical
		},
		{
			name: "multiple crashed",
			summary: DiagnoseSummary{
				TotalPanes: 4,
				Healthy:    2,
				Crashed:    2,
			},
			want: "critical",
		},
		{
			name: "crashed plus rate limited",
			summary: DiagnoseSummary{
				TotalPanes:  4,
				Healthy:     2,
				Crashed:     1,
				RateLimited: 1,
			},
			want: "critical",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := determineOverallHealth(tt.summary)
			if got != tt.want {
				t.Errorf("determineOverallHealth(%+v) = %q, want %q", tt.summary, got, tt.want)
			}
		})
	}
}

// =============================================================================
// Recommendation Tests
// =============================================================================

func TestBuildRateLimitRecommendation(t *testing.T) {
	tests := []struct {
		name       string
		paneIndex  int
		session    string
		check      *HealthCheck
		wantAction string
		wantStatus string
	}{
		{
			name:      "with wait seconds",
			paneIndex: 2,
			session:   "test-session",
			check: &HealthCheck{
				ErrorCheck: &ErrorCheckResult{
					RateLimited: true,
					WaitSeconds: 300,
				},
			},
			wantAction: "wait",
			wantStatus: "rate_limited",
		},
		{
			name:      "no wait seconds",
			paneIndex: 0,
			session:   "my-session",
			check: &HealthCheck{
				ErrorCheck: &ErrorCheckResult{
					RateLimited: true,
					WaitSeconds: 0,
				},
			},
			wantAction: "wait_or_switch",
			wantStatus: "rate_limited",
		},
		{
			name:      "nil error check",
			paneIndex: 1,
			session:   "session",
			check: &HealthCheck{
				ErrorCheck: nil,
			},
			wantAction: "wait_or_switch",
			wantStatus: "rate_limited",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			rec := buildRateLimitRecommendation(tt.paneIndex, fmt.Sprintf("%d", tt.paneIndex), tt.session, tt.check)

			if rec.Status != tt.wantStatus {
				t.Errorf("buildRateLimitRecommendation() status = %q, want %q", rec.Status, tt.wantStatus)
			}
			if rec.Action != tt.wantAction {
				t.Errorf("buildRateLimitRecommendation() action = %q, want %q", rec.Action, tt.wantAction)
			}
			if rec.Pane != tt.paneIndex {
				t.Errorf("buildRateLimitRecommendation() pane = %d, want %d", rec.Pane, tt.paneIndex)
			}
			if rec.AutoFixable {
				t.Errorf("buildRateLimitRecommendation() auto_fixable should be false for rate limits")
			}
			if rec.Reason == "" {
				t.Errorf("buildRateLimitRecommendation() reason should not be empty")
			}
			if rec.FixCommand == "" {
				t.Errorf("buildRateLimitRecommendation() fix_command should not be empty")
			}
		})
	}
}

func TestBuildRateLimitRecommendation_FixCommandFormat(t *testing.T) {
	// Test that fix command contains correct session and pane info
	check := &HealthCheck{
		ErrorCheck: &ErrorCheckResult{
			RateLimited: true,
			WaitSeconds: 60,
		},
	}

	rec := buildRateLimitRecommendation(3, "3", "my-test-session", check)

	if rec.FixCommand == "" {
		t.Fatal("FixCommand should not be empty")
	}

	// Should contain sleep and re-diagnosis command
	if rec.Action == "wait" {
		// Expect: sleep 60 && ntm --robot-diagnose=my-test-session --pane=3
		if !contains(rec.FixCommand, "sleep 60") {
			t.Errorf("FixCommand should contain 'sleep 60', got: %s", rec.FixCommand)
		}
		if !contains(rec.FixCommand, "my-test-session") {
			t.Errorf("FixCommand should contain session name, got: %s", rec.FixCommand)
		}
	}
}

// =============================================================================
// DiagnoseRecommendation Structure Tests
// =============================================================================

func TestDiagnoseRecommendation_JSONStructure(t *testing.T) {
	rec := DiagnoseRecommendation{
		Pane:        2,
		Status:      "crashed",
		Action:      "restart",
		Reason:      "Process not running",
		AutoFixable: true,
		FixCommand:  "ntm --robot-restart-pane=session --panes=2",
	}

	data, err := json.Marshal(rec)
	if err != nil {
		t.Fatalf("Failed to marshal DiagnoseRecommendation: %v", err)
	}

	// Unmarshal to verify structure
	var decoded map[string]interface{}
	if err := json.Unmarshal(data, &decoded); err != nil {
		t.Fatalf("Failed to unmarshal: %v", err)
	}

	// Check all expected fields are present
	expectedFields := []string{"pane", "status", "action", "reason", "auto_fixable", "fix_command"}
	for _, field := range expectedFields {
		if _, ok := decoded[field]; !ok {
			t.Errorf("Missing field %q in JSON output", field)
		}
	}

	// Check specific values
	if decoded["pane"].(float64) != 2 {
		t.Errorf("pane = %v, want 2", decoded["pane"])
	}
	if decoded["status"].(string) != "crashed" {
		t.Errorf("status = %v, want 'crashed'", decoded["status"])
	}
	if decoded["auto_fixable"].(bool) != true {
		t.Errorf("auto_fixable = %v, want true", decoded["auto_fixable"])
	}
}

// GH#251 phase 2: grok supports automated relaunch, so a mixed batch with
// mutating actions on a grok pane passes the fix-target preflight.
func TestValidateDiagnoseFixTargetsAcceptsGrokInMixedBatch(t *testing.T) {
	diag := DiagnoseOutput{Recommendations: []DiagnoseRecommendation{
		{Pane: 1, Action: "restart", AutoFixable: true},
		{Pane: 2, Action: "interrupt", AutoFixable: true},
	}}
	panes := []tmux.Pane{
		{ID: "%1", Index: 1, Type: tmux.AgentClaude},
		{ID: "%2", Index: 2, Type: tmux.AgentGrok},
	}
	if err := validateDiagnoseFixTargets(diag, panes); err != nil {
		t.Fatalf("validateDiagnoseFixTargets() error = %v, want nil (grok relaunch is supported)", err)
	}
}

func TestValidateDiagnoseFixTargetsRejectsZAI(t *testing.T) {
	diag := DiagnoseOutput{Recommendations: []DiagnoseRecommendation{{
		Pane: 1, Action: "restart", AutoFixable: true,
	}}}
	err := validateDiagnoseFixTargets(diag, []tmux.Pane{{ID: "%zai", Index: 1, Type: tmux.AgentZAI}})
	if !errors.Is(err, agent.ErrZAIProfileRelaunchRequired) {
		t.Fatalf("validateDiagnoseFixTargets(zai) error = %v, want profile-required error", err)
	}
}

func TestValidateDiagnoseFixTargetsAllowsSupportedAndNonMutatingActions(t *testing.T) {
	diag := DiagnoseOutput{Recommendations: []DiagnoseRecommendation{
		{Pane: 1, Action: "restart", AutoFixable: true},
		{Pane: 2, Action: "investigate", AutoFixable: false},
	}}
	panes := []tmux.Pane{
		{ID: "%1", Index: 1, Type: tmux.AgentCodex},
		{ID: "%2", Index: 2, Type: tmux.AgentGrok},
	}
	if err := validateDiagnoseFixTargets(diag, panes); err != nil {
		t.Fatalf("validateDiagnoseFixTargets() unexpected error = %v", err)
	}
}

func TestExecuteDiagnoseRestartUsesCanonicalRelaunchPath(t *testing.T) {
	var got RestartPaneOptions
	success, message, err := executeDiagnoseRestart(t.Context(), "project", "%7", func(_ context.Context, opts RestartPaneOptions) (*RestartPaneOutput, error) {
		got = opts
		return &RestartPaneOutput{
			RobotResponse:   NewRobotResponse(true),
			Restarted:       []string{"2"},
			AgentRelaunched: map[string]bool{"2": true},
		}, nil
	})
	if err != nil || !success || !contains(message, "agent restarted") {
		t.Fatalf("diagnose restart success=%v message=%q err=%v", success, message, err)
	}
	if got.Session != "project" || len(got.Panes) != 1 || got.Panes[0] != "%7" {
		t.Fatalf("diagnose restart options = %+v", got)
	}
}

func TestExecuteDiagnoseRestartRejectsShellOnlySuccess(t *testing.T) {
	success, message, err := executeDiagnoseRestart(t.Context(), "project", "%7", func(context.Context, RestartPaneOptions) (*RestartPaneOutput, error) {
		return &RestartPaneOutput{
			RobotResponse:   NewRobotResponse(true),
			Restarted:       []string{"2"},
			AgentRelaunched: map[string]bool{"2": false},
		}, nil
	})
	if err != nil || success || !contains(message, "not relaunched") {
		t.Fatalf("shell-only diagnose restart success=%v message=%q err=%v", success, message, err)
	}
}

func TestExecuteDiagnoseRestartForwardsCanceledContext(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	calls := 0
	success, message, err := executeDiagnoseRestart(ctx, "project", "%7", func(gotCtx context.Context, opts RestartPaneOptions) (*RestartPaneOutput, error) {
		calls++
		if gotCtx != ctx {
			t.Fatal("diagnose restart replaced the caller context")
		}
		return GetRestartPaneContext(gotCtx, opts)
	})
	if success || calls != 1 || !errors.Is(err, context.Canceled) || !strings.Contains(strings.ToLower(message), "canceled") {
		t.Fatalf("canceled diagnose restart success=%v calls=%d message=%q err=%v", success, calls, message, err)
	}
}

type diagnoseFixTestOutput struct {
	RobotResponse
	FixAttempts []struct {
		Pane    int    `json:"pane"`
		Action  string `json:"action"`
		Success bool   `json:"success"`
		Message string `json:"message"`
	} `json:"fix_attempts"`
}

func captureDiagnoseFixOutput(t *testing.T, ctx context.Context, diag DiagnoseOutput, deps diagnoseDependencies) (diagnoseFixTestOutput, error) {
	t.Helper()
	raw, returnedErr := captureStdout(t, func() error {
		return executeDiagnoseFixWithDependencies(ctx, diag, DiagnoseOptions{Session: "project", Fix: true}, deps)
	})
	var output diagnoseFixTestOutput
	if err := json.Unmarshal([]byte(raw), &output); err != nil {
		t.Fatalf("decode diagnose fix output: %v raw=%s", err, raw)
	}
	return output, returnedErr
}

func TestExecuteDiagnoseFixStopsOnWrappedRestartCancellation(t *testing.T) {
	ctx := t.Context()
	restartCalls := 0
	interruptCalls := 0
	output, returnedErr := captureDiagnoseFixOutput(t, ctx, DiagnoseOutput{Recommendations: []DiagnoseRecommendation{
		{Pane: 1, Action: "restart", AutoFixable: true},
		{Pane: 2, Action: "interrupt", AutoFixable: true},
	}}, diagnoseDependencies{
		listPanes: func(gotCtx context.Context, session string) ([]tmux.Pane, error) {
			if gotCtx != ctx || session != "project" {
				t.Fatalf("list panes context/session = %p/%q, want %p/project", gotCtx, session, ctx)
			}
			return []tmux.Pane{
				{ID: "%1", Index: 1, Type: tmux.AgentCodex},
				{ID: "%2", Index: 2, Type: tmux.AgentClaude},
			}, nil
		},
		restartPane: func(gotCtx context.Context, _ RestartPaneOptions) (*RestartPaneOutput, error) {
			restartCalls++
			if gotCtx != ctx {
				t.Fatal("restart callback did not receive caller context")
			}
			return nil, fmt.Errorf("restart callback: %w", context.Canceled)
		},
		sendKeys: func(context.Context, string, string, bool) error {
			interruptCalls++
			return nil
		},
	})
	if returnedErr == nil || output.Success || output.ErrorCode != ErrCodeTimeout ||
		restartCalls != 1 || interruptCalls != 0 || len(output.FixAttempts) != 1 ||
		output.FixAttempts[0].Action != "restart" || output.FixAttempts[0].Success {
		t.Fatalf("restart cancellation output=%+v err=%v restart_calls=%d interrupt_calls=%d", output, returnedErr, restartCalls, interruptCalls)
	}
}

func TestExecuteDiagnoseFixStopsOnInterruptCancellation(t *testing.T) {
	ctx := t.Context()
	interruptCalls := 0
	restartCalls := 0
	output, returnedErr := captureDiagnoseFixOutput(t, ctx, DiagnoseOutput{Recommendations: []DiagnoseRecommendation{
		{Pane: 1, Action: "interrupt", AutoFixable: true},
		{Pane: 2, Action: "restart", AutoFixable: true},
	}}, diagnoseDependencies{
		listPanes: func(context.Context, string) ([]tmux.Pane, error) {
			return []tmux.Pane{
				{ID: "%1", Index: 1, Type: tmux.AgentClaude},
				{ID: "%2", Index: 2, Type: tmux.AgentCodex},
			}, nil
		},
		restartPane: func(context.Context, RestartPaneOptions) (*RestartPaneOutput, error) {
			restartCalls++
			return &RestartPaneOutput{RobotResponse: NewRobotResponse(true)}, nil
		},
		sendKeys: func(gotCtx context.Context, target, keys string, enter bool) error {
			interruptCalls++
			if gotCtx != ctx || target != "%1" || keys != "C-c" || enter {
				t.Fatalf("interrupt callback context/args = %p %q %q %v", gotCtx, target, keys, enter)
			}
			return fmt.Errorf("interrupt transport: %w", context.DeadlineExceeded)
		},
	})
	if returnedErr == nil || output.Success || output.ErrorCode != ErrCodeTimeout ||
		interruptCalls != 1 || restartCalls != 0 || len(output.FixAttempts) != 1 ||
		output.FixAttempts[0].Action != "interrupt" || output.FixAttempts[0].Success {
		t.Fatalf("interrupt cancellation output=%+v err=%v interrupt_calls=%d restart_calls=%d", output, returnedErr, interruptCalls, restartCalls)
	}
}

func TestDiagnoseDiscoveryErrorClassification(t *testing.T) {
	t.Run("missing session has no overall health", func(t *testing.T) {
		output, err := getDiagnoseWithDependencies(t.Context(), DiagnoseOptions{Session: "missing"}, diagnoseDependencies{
			sessionExists: func(context.Context, string) (bool, error) { return false, nil },
		})
		if err != nil || output.Success || output.ErrorCode != ErrCodeSessionNotFound || output.OverallHealth != "" {
			t.Fatalf("missing session output=%+v err=%v", output, err)
		}
	})

	t.Run("full session cancellation", func(t *testing.T) {
		output, err := getDiagnoseWithDependencies(t.Context(), DiagnoseOptions{Session: "project"}, diagnoseDependencies{
			sessionExists: func(context.Context, string) (bool, error) {
				return false, fmt.Errorf("session probe: %w", context.Canceled)
			},
		})
		if err != nil || output.Success || output.ErrorCode != ErrCodeTimeout || !strings.Contains(output.Error, "session probe") {
			t.Fatalf("session cancellation output=%+v err=%v", output, err)
		}
	})

	t.Run("full session transport failure", func(t *testing.T) {
		output, err := getDiagnoseWithDependencies(t.Context(), DiagnoseOptions{Session: "project"}, diagnoseDependencies{
			sessionExists: func(context.Context, string) (bool, error) {
				return false, errors.New("tmux socket unavailable")
			},
		})
		if err != nil || output.Success || output.ErrorCode != ErrCodeInternalError {
			t.Fatalf("session transport output=%+v err=%v", output, err)
		}
	})

	t.Run("full pane cancellation", func(t *testing.T) {
		output, err := getDiagnoseWithDependencies(t.Context(), DiagnoseOptions{Session: "project"}, diagnoseDependencies{
			sessionExists: func(context.Context, string) (bool, error) { return true, nil },
			listPanes: func(context.Context, string) ([]tmux.Pane, error) {
				return nil, fmt.Errorf("pane discovery: %w", context.DeadlineExceeded)
			},
		})
		if err != nil || output.Success || output.ErrorCode != ErrCodeTimeout || !strings.Contains(output.Error, "pane discovery") {
			t.Fatalf("pane cancellation output=%+v err=%v", output, err)
		}
	})

	t.Run("brief pane cancellation", func(t *testing.T) {
		output, err := getDiagnoseBriefWithDependencies(t.Context(), "project", diagnoseDependencies{
			sessionExists: func(context.Context, string) (bool, error) { return true, nil },
			listPanes: func(context.Context, string) ([]tmux.Pane, error) {
				return nil, fmt.Errorf("brief pane discovery: %w", context.Canceled)
			},
		})
		if err != nil || output.Success || output.ErrorCode != ErrCodeTimeout || !strings.Contains(output.Error, "brief pane discovery") {
			t.Fatalf("brief pane cancellation output=%+v err=%v", output, err)
		}
	})
}

func TestExecuteDiagnoseFixClassifiesDiscoveryCancellation(t *testing.T) {
	restartCalls := 0
	interruptCalls := 0
	output, returnedErr := captureDiagnoseFixOutput(t, t.Context(), DiagnoseOutput{Recommendations: []DiagnoseRecommendation{
		{Pane: 1, Action: "restart", AutoFixable: true},
	}}, diagnoseDependencies{
		listPanes: func(context.Context, string) ([]tmux.Pane, error) {
			return nil, fmt.Errorf("fix pane discovery: %w", context.Canceled)
		},
		restartPane: func(context.Context, RestartPaneOptions) (*RestartPaneOutput, error) {
			restartCalls++
			return nil, nil
		},
		sendKeys: func(context.Context, string, string, bool) error {
			interruptCalls++
			return nil
		},
	})
	if returnedErr == nil || output.Success || output.ErrorCode != ErrCodeTimeout || len(output.FixAttempts) != 0 ||
		restartCalls != 0 || interruptCalls != 0 {
		t.Fatalf("fix discovery cancellation output=%+v err=%v restart_calls=%d interrupt_calls=%d", output, returnedErr, restartCalls, interruptCalls)
	}
}

func TestGetDiagnoseRejectsMissingAndCanceledContexts(t *testing.T) {
	calls := 0
	deps := diagnoseDependencies{
		sessionExists: func(context.Context, string) (bool, error) {
			calls++
			return true, nil
		},
	}
	output, err := getDiagnoseWithDependencies(nil, DiagnoseOptions{Session: "project"}, deps)
	if err != nil || output.Success || output.ErrorCode != ErrCodeInternalError || calls != 0 {
		t.Fatalf("nil context output=%+v err=%v calls=%d", output, err, calls)
	}

	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	output, err = getDiagnoseWithDependencies(ctx, DiagnoseOptions{Session: "project"}, deps)
	if err != nil || output.Success || output.ErrorCode != ErrCodeTimeout || calls != 0 {
		t.Fatalf("pre-canceled output=%+v err=%v calls=%d", output, err, calls)
	}
}

// =============================================================================
// DiagnoseOutput Structure Tests
// =============================================================================

func TestDiagnoseOutput_JSONEnvelope(t *testing.T) {
	output := DiagnoseOutput{
		RobotResponse:   NewRobotResponse(true),
		Session:         "test-session",
		OverallHealth:   "healthy",
		Summary:         DiagnoseSummary{TotalPanes: 4, Healthy: 4},
		Panes:           DiagnosePanes{Healthy: []int{0, 1, 2, 3}},
		Recommendations: []DiagnoseRecommendation{},
		AutoFixAvail:    false,
	}

	data, err := json.Marshal(output)
	if err != nil {
		t.Fatalf("Failed to marshal DiagnoseOutput: %v", err)
	}

	var decoded map[string]interface{}
	if err := json.Unmarshal(data, &decoded); err != nil {
		t.Fatalf("Failed to unmarshal: %v", err)
	}

	// Check RobotResponse envelope fields
	if _, ok := decoded["success"]; !ok {
		t.Error("Missing 'success' field from RobotResponse envelope")
	}
	if _, ok := decoded["timestamp"]; !ok {
		t.Error("Missing 'timestamp' field from RobotResponse envelope")
	}

	// Check diagnose-specific fields
	requiredFields := []string{"session", "overall_health", "summary", "panes", "recommendations", "auto_fix_available"}
	for _, field := range requiredFields {
		if _, ok := decoded[field]; !ok {
			t.Errorf("Missing field %q in DiagnoseOutput", field)
		}
	}

	// Verify overall_health values
	if decoded["overall_health"].(string) != "healthy" {
		t.Errorf("overall_health = %v, want 'healthy'", decoded["overall_health"])
	}
}

func TestDiagnoseOutput_EmptySlicesNotNil(t *testing.T) {
	output := DiagnoseOutput{
		RobotResponse: NewRobotResponse(true),
		Session:       "test",
		OverallHealth: "healthy",
		Panes: DiagnosePanes{
			Healthy:      []int{},
			RateLimited:  []int{},
			Unresponsive: []int{},
			Crashed:      []int{},
			Unknown:      []int{},
		},
		Recommendations: []DiagnoseRecommendation{},
	}

	data, err := json.Marshal(output)
	if err != nil {
		t.Fatalf("Failed to marshal: %v", err)
	}

	var decoded map[string]interface{}
	if err := json.Unmarshal(data, &decoded); err != nil {
		t.Fatalf("Failed to unmarshal: %v", err)
	}

	// Verify panes object has empty arrays, not null
	panes := decoded["panes"].(map[string]interface{})
	for _, key := range []string{"healthy", "rate_limited", "unresponsive", "crashed", "unknown"} {
		arr := panes[key]
		if arr == nil {
			t.Errorf("panes.%s should be empty array [], not null", key)
		}
		if arrTyped, ok := arr.([]interface{}); !ok {
			t.Errorf("panes.%s should be array type", key)
		} else if len(arrTyped) != 0 {
			t.Errorf("panes.%s should be empty, got %v", key, arrTyped)
		}
	}

	// Verify recommendations is empty array, not null
	recs := decoded["recommendations"]
	if recs == nil {
		t.Error("recommendations should be empty array [], not null")
	}
}

// =============================================================================
// DiagnoseSummary Tests
// =============================================================================

func TestDiagnoseSummary_TotalPanes(t *testing.T) {
	tests := []struct {
		name    string
		summary DiagnoseSummary
	}{
		{
			name: "counts match total",
			summary: DiagnoseSummary{
				TotalPanes:   10,
				Healthy:      5,
				RateLimited:  2,
				Unresponsive: 1,
				Crashed:      1,
				Unknown:      1,
			},
		},
		{
			name: "all healthy",
			summary: DiagnoseSummary{
				TotalPanes: 8,
				Healthy:    8,
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			sum := tt.summary.Healthy + tt.summary.RateLimited +
				tt.summary.Unresponsive + tt.summary.Crashed + tt.summary.Unknown
			if sum != tt.summary.TotalPanes {
				t.Errorf("Sum of states (%d) != TotalPanes (%d)", sum, tt.summary.TotalPanes)
			}
		})
	}
}

// =============================================================================
// DiagnoseBriefOutput Tests
// =============================================================================

func TestDiagnoseBriefOutput_JSONStructure(t *testing.T) {
	output := DiagnoseBriefOutput{
		RobotResponse: NewRobotResponse(true),
		Session:       "test",
		OverallHealth: "degraded",
		Summary:       "3/4 healthy, 1 rate_limited",
		HasIssues:     true,
		FixAvailable:  false,
	}

	data, err := json.Marshal(output)
	if err != nil {
		t.Fatalf("Failed to marshal: %v", err)
	}

	var decoded map[string]interface{}
	if err := json.Unmarshal(data, &decoded); err != nil {
		t.Fatalf("Failed to unmarshal: %v", err)
	}

	requiredFields := []string{"success", "timestamp", "session", "overall_health", "summary", "has_issues", "fix_available"}
	for _, field := range requiredFields {
		if _, ok := decoded[field]; !ok {
			t.Errorf("Missing field %q in DiagnoseBriefOutput", field)
		}
	}
}

// =============================================================================
// DiagnoseOptions Tests
// =============================================================================

func TestDiagnoseOptions_Defaults(t *testing.T) {
	opts := DiagnoseOptions{
		Session: "my-session",
	}

	// Verify defaults
	if opts.Pane != 0 {
		t.Errorf("Default Pane should be 0 (all panes when -1 is set explicitly)")
	}
	if opts.Fix {
		t.Error("Default Fix should be false")
	}
	if opts.Brief {
		t.Error("Default Brief should be false")
	}
}

func TestDiagnoseOptions_PaneFiltering(t *testing.T) {
	tests := []struct {
		name     string
		pane     int
		wantAll  bool
		wantPane int
	}{
		{"all panes", -1, true, -1},
		{"pane 0", 0, false, 0},
		{"pane 5", 5, false, 5},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			opts := DiagnoseOptions{
				Session: "test",
				Pane:    tt.pane,
			}

			isAll := opts.Pane < 0
			if isAll != tt.wantAll {
				t.Errorf("Pane=%d: isAll=%v, wantAll=%v", tt.pane, isAll, tt.wantAll)
			}
		})
	}
}

// =============================================================================
// Action to FixCommand Mapping Tests
// =============================================================================

func TestRecommendationActions(t *testing.T) {
	tests := []struct {
		name              string
		status            string
		action            string
		autoFixable       bool
		fixCommandPattern string
	}{
		{
			name:              "crashed pane restart",
			status:            "crashed",
			action:            "restart",
			autoFixable:       true,
			fixCommandPattern: "--robot-restart-pane",
		},
		{
			name:              "unresponsive interrupt",
			status:            "unresponsive",
			action:            "interrupt",
			autoFixable:       true,
			fixCommandPattern: "--robot-interrupt",
		},
		{
			name:              "rate limited wait",
			status:            "rate_limited",
			action:            "wait",
			autoFixable:       false,
			fixCommandPattern: "sleep",
		},
		{
			name:              "unknown investigate",
			status:            "unknown",
			action:            "investigate",
			autoFixable:       false,
			fixCommandPattern: "inspect",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			rec := DiagnoseRecommendation{
				Pane:        0,
				Status:      tt.status,
				Action:      tt.action,
				AutoFixable: tt.autoFixable,
				FixCommand:  "ntm " + tt.fixCommandPattern + "=session",
				Reason:      "test reason",
			}

			if rec.AutoFixable != tt.autoFixable {
				t.Errorf("Status %q: AutoFixable = %v, want %v", tt.status, rec.AutoFixable, tt.autoFixable)
			}

			if !contains(rec.FixCommand, tt.fixCommandPattern) {
				t.Errorf("FixCommand %q should contain %q", rec.FixCommand, tt.fixCommandPattern)
			}
		})
	}
}

// =============================================================================
// Overall Health Threshold Tests
// =============================================================================

func TestOverallHealthThresholds(t *testing.T) {
	// Test the exact boundaries for health state transitions

	// Boundary: unresponsive exactly equals healthy (not majority) = degraded
	t.Run("unresponsive equals healthy is degraded", func(t *testing.T) {
		summary := DiagnoseSummary{
			TotalPanes:   4,
			Healthy:      2,
			Unresponsive: 2,
		}
		got := determineOverallHealth(summary)
		if got != "degraded" {
			t.Errorf("Equal unresponsive/healthy should be 'degraded', got %q", got)
		}
	})

	// Boundary: unresponsive > healthy = critical
	t.Run("unresponsive majority is critical", func(t *testing.T) {
		summary := DiagnoseSummary{
			TotalPanes:   4,
			Healthy:      1,
			Unresponsive: 3,
		}
		got := determineOverallHealth(summary)
		if got != "critical" {
			t.Errorf("Majority unresponsive should be 'critical', got %q", got)
		}
	})

	// Any crashed = critical (even 1)
	t.Run("single crashed is critical", func(t *testing.T) {
		summary := DiagnoseSummary{
			TotalPanes: 100,
			Healthy:    99,
			Crashed:    1,
		}
		got := determineOverallHealth(summary)
		if got != "critical" {
			t.Errorf("Any crashed pane should be 'critical', got %q", got)
		}
	})
}

// =============================================================================
// Helper functions
// =============================================================================

func contains(s, substr string) bool {
	return len(s) >= len(substr) && (s == substr || len(s) > 0 && containsHelper(s, substr))
}

func containsHelper(s, substr string) bool {
	for i := 0; i <= len(s)-len(substr); i++ {
		if s[i:i+len(substr)] == substr {
			return true
		}
	}
	return false
}

// Pane.Index is only unique within a window, so index-keyed lookups let one
// window's pane overwrite another's: --fix respawned a pane in the wrong window
// and validated the wrong pane's agent type.
func TestValidateDiagnoseFixTargetsDistinguishesPanesAcrossWindows(t *testing.T) {
	// Two windows each holding a pane 1. Only window 1's is Grok.
	panes := []tmux.Pane{
		{ID: "%1", Index: 1, WindowIndex: 0, Type: tmux.AgentClaude},
		{ID: "%2", Index: 1, WindowIndex: 1, Type: tmux.AgentGrok},
	}

	// Targeting window 0's claude pane must be allowed.
	claude := DiagnoseOutput{Recommendations: []DiagnoseRecommendation{
		{Pane: 1, PaneTarget: "0.1", Action: "restart", AutoFixable: true},
	}}
	if err := validateDiagnoseFixTargets(claude, panes); err != nil {
		t.Fatalf("restarting window 0's claude pane was rejected: %v", err)
	}

	// GH#251 phase 2: grok relaunch is supported, so window 1's grok pane is
	// also a valid target. The window-disambiguation coverage now lives in the
	// target-keyed lookup itself: each window's pane 1 must resolve to its own
	// agent type instead of one overwriting the other.
	grok := DiagnoseOutput{Recommendations: []DiagnoseRecommendation{
		{Pane: 1, PaneTarget: "1.1", Action: "restart", AutoFixable: true},
	}}
	if err := validateDiagnoseFixTargets(grok, panes); err != nil {
		t.Fatalf("restarting window 1's grok pane error = %v, want nil (grok relaunch is supported)", err)
	}
	multiWindow := tmux.PanesSpanMultipleWindows(panes)
	if !multiWindow {
		t.Fatal("panes should span multiple windows")
	}
	byTarget := map[string]string{}
	for _, p := range panes {
		byTarget[paneTargetKey(p, multiWindow)] = detectAgentTypeFromPane(p)
	}
	if byTarget["0.1"] != "claude" || byTarget["1.1"] != "grok" {
		t.Fatalf("target-keyed agent types = %v, want 0.1=claude and 1.1=grok", byTarget)
	}
}

// A recommendation without a PaneTarget still resolves on a single-window
// session, where the bare index is unambiguous.
func TestDiagnoseRecommendationKeyFallsBackToIndex(t *testing.T) {
	if got := diagnoseRecommendationKey(DiagnoseRecommendation{Pane: 3}); got != "3" {
		t.Fatalf("diagnoseRecommendationKey with no target = %q, want %q", got, "3")
	}
	if got := diagnoseRecommendationKey(DiagnoseRecommendation{Pane: 3, PaneTarget: "1.3"}); got != "1.3" {
		t.Fatalf("diagnoseRecommendationKey = %q, want the target %q", got, "1.3")
	}
}
