package cli

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/Dicklesworthstone/ntm/internal/config"
	"github.com/Dicklesworthstone/ntm/internal/health"
	"github.com/Dicklesworthstone/ntm/internal/robot"
)

func TestStatusSeverity(t *testing.T) {

	tests := []struct {
		name   string
		status health.Status
		want   int
	}{
		{"ok is lowest severity", health.StatusOK, 0},
		{"warning is medium severity", health.StatusWarning, 1},
		{"error is highest severity", health.StatusError, 2},
		{"unknown defaults to ok severity", health.Status("unknown"), 0},
		{"empty defaults to ok severity", health.Status(""), 0},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := statusSeverity(tc.status)
			if got != tc.want {
				t.Errorf("statusSeverity(%q) = %d, want %d", tc.status, got, tc.want)
			}
		})
	}
}

func TestStatusSeverityOrdering(t *testing.T) {

	// Verify severity ordering: OK < Warning < Error
	okSev := statusSeverity(health.StatusOK)
	warnSev := statusSeverity(health.StatusWarning)
	errSev := statusSeverity(health.StatusError)

	if okSev >= warnSev {
		t.Errorf("OK severity (%d) should be less than Warning (%d)", okSev, warnSev)
	}
	if warnSev >= errSev {
		t.Errorf("Warning severity (%d) should be less than Error (%d)", warnSev, errSev)
	}
}

func TestAutoRestartStuckOptionsPreserveEffectiveConfig(t *testing.T) {
	effectiveConfig := config.Default()
	opts := autoRestartStuckOptions("project", 11*time.Minute, true, effectiveConfig)

	if opts.Session != "project" || opts.Threshold != 11*time.Minute || !opts.DryRun {
		t.Fatalf("auto-restart options = %+v", opts)
	}
	if opts.Config != effectiveConfig {
		t.Fatal("CLI auto-restart options discarded the effective config")
	}
}

func TestHealthDryRunFlagWiresPreviewMode(t *testing.T) {
	old := healthDryRun
	healthDryRun = false
	t.Cleanup(func() { healthDryRun = old })

	cmd := newHealthCmd()
	if err := cmd.ParseFlags([]string{"--dry-run"}); err != nil {
		t.Fatalf("parse health --dry-run: %v", err)
	}
	if !healthDryRun {
		t.Fatal("health --dry-run did not enable preview mode")
	}
	flag := cmd.Flags().Lookup("dry-run")
	if flag == nil || flag.Usage == "" {
		t.Fatal("health command does not expose a documented --dry-run flag")
	}
}

func TestAutoRestartStuckResultErrorRejectsTerminalFailure(t *testing.T) {
	if err := autoRestartStuckResultError(nil); err == nil {
		t.Fatal("nil auto-restart result returned nil error")
	}

	result := &robot.AutoRestartStuckOutput{
		RobotResponse: robot.NewErrorResponse(
			errors.New("Grok Build restart is not implemented"),
			robot.ErrCodeNotImplemented,
			"Use an interactive pane",
		),
	}
	err := autoRestartStuckResultError(result)
	if err == nil {
		t.Fatal("terminal auto-restart failure returned nil error")
	}
	if !strings.Contains(err.Error(), "Grok Build") || !strings.Contains(err.Error(), string(robot.ErrCodeNotImplemented)) {
		t.Fatalf("terminal auto-restart error = %q", err)
	}

	success := &robot.AutoRestartStuckOutput{RobotResponse: robot.NewRobotResponse(true)}
	if err := autoRestartStuckResultError(success); err != nil {
		t.Fatalf("successful auto-restart result error = %v", err)
	}
}

func TestTruncateString(t *testing.T) {

	tests := []struct {
		name   string
		s      string
		maxLen int
		want   string
	}{
		{"short string unchanged", "hello", 10, "hello"},
		{"exact length unchanged", "hello", 5, "hello"},
		{"truncates with ellipsis", "hello world", 8, "hello w…"},
		{"truncates to min with ellipsis", "hello", 3, "he…"},
		{"maxLen 1 returns first char", "hello", 1, "h"},
		{"maxLen 0 returns empty", "hello", 0, ""},
		{"empty string unchanged", "", 10, ""},
		{"unicode preserved", "héllo wörld", 6, "héllo…"},
		{"unicode exact", "日本語テスト", 6, "日本語テスト"},
		{"unicode truncated", "日本語テストです", 6, "日本語テス…"},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := truncateString(tc.s, tc.maxLen)
			if got != tc.want {
				t.Errorf("truncateString(%q, %d) = %q, want %q", tc.s, tc.maxLen, got, tc.want)
			}
		})
	}
}

func TestCoerceHealthOutput(t *testing.T) {

	t.Run("HealthOutput value passes through", func(t *testing.T) {
		input := HealthOutput{Error: "test"}
		result, err := coerceHealthOutput(input)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if result.Error != "test" {
			t.Errorf("Error = %q, want %q", result.Error, "test")
		}
	})

	t.Run("HealthOutput pointer dereferences", func(t *testing.T) {
		input := &HealthOutput{Error: "pointer test"}
		result, err := coerceHealthOutput(input)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if result.Error != "pointer test" {
			t.Errorf("Error = %q, want %q", result.Error, "pointer test")
		}
	})

	t.Run("nil pointer returns error", func(t *testing.T) {
		var input *HealthOutput
		_, err := coerceHealthOutput(input)
		if err == nil {
			t.Error("expected error for nil pointer, got nil")
		}
	})

	t.Run("wrong type returns error", func(t *testing.T) {
		_, err := coerceHealthOutput("string value")
		if err == nil {
			t.Error("expected error for wrong type, got nil")
		}
	})

	t.Run("int type returns error", func(t *testing.T) {
		_, err := coerceHealthOutput(42)
		if err == nil {
			t.Error("expected error for int type, got nil")
		}
	})
}

func TestRunHealthOnceJSONHardFailuresEmitOneDocument(t *testing.T) {
	oldJSONOutput := jsonOutput
	oldHealthWatch := healthWatch
	oldKernelRun := healthKernelRun
	jsonOutput = true
	healthWatch = false
	t.Cleanup(func() {
		jsonOutput = oldJSONOutput
		healthWatch = oldHealthWatch
		healthKernelRun = oldKernelRun
	})

	t.Run("kernel error", func(t *testing.T) {
		cause := errors.New("kernel sentinel")
		healthKernelRun = func(context.Context, string, any) (any, error) {
			return nil, cause
		}
		stdout, runErr := captureStdout(t, func() error { return runHealthOnce("test-session") })
		if !errors.Is(runErr, errJSONFailure) || !errors.Is(runErr, cause) {
			t.Fatalf("error = %v, want errJSONFailure joined with kernel cause", runErr)
		}
		assertHealthFailureJSON(t, stdout, "kernel sentinel")
	})

	t.Run("coercion error", func(t *testing.T) {
		healthKernelRun = func(context.Context, string, any) (any, error) {
			return struct{}{}, nil
		}
		stdout, runErr := captureStdout(t, func() error { return runHealthOnce("test-session") })
		if !errors.Is(runErr, errJSONFailure) {
			t.Fatalf("error = %v, want errJSONFailure", runErr)
		}
		assertHealthFailureJSON(t, stdout, "unexpected type")
	})
}

func TestRunHealthOnceJSONSeverityExitLadder(t *testing.T) {
	oldJSONOutput := jsonOutput
	oldHealthWatch := healthWatch
	oldKernelRun := healthKernelRun
	jsonOutput = true
	healthWatch = false
	t.Cleanup(func() {
		jsonOutput = oldJSONOutput
		healthWatch = oldHealthWatch
		healthKernelRun = oldKernelRun
	})

	tests := []struct {
		name     string
		status   health.Status
		wantCode int
	}{
		{name: "ok", status: health.StatusOK, wantCode: 0},
		{name: "warning", status: health.StatusWarning, wantCode: 1},
		{name: "error", status: health.StatusError, wantCode: 2},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			healthKernelRun = func(context.Context, string, any) (any, error) {
				return HealthOutput{SessionHealth: &health.SessionHealth{
					Session:       "test-session",
					OverallStatus: tt.status,
				}}, nil
			}
			stdout, runErr := captureStdout(t, func() error { return runHealthOnce("test-session") })

			var response map[string]interface{}
			if err := json.Unmarshal([]byte(stdout), &response); err != nil {
				t.Fatalf("single JSON response decode failed: %v\nstdout=%s", err, stdout)
			}
			if tt.wantCode == 0 {
				if runErr != nil {
					t.Fatalf("error = %v, want nil", runErr)
				}
				if response["success"] != true {
					t.Fatalf("success = %v, want true", response["success"])
				}
				return
			}

			var exitErr *robot.ProcessExitError
			if !errors.As(runErr, &exitErr) {
				t.Fatalf("error = %v, want ProcessExitError", runErr)
			}
			if exitErr.ExitCode() != tt.wantCode || !exitErr.JSONWritten() {
				t.Fatalf("exit result = code %d, json_written %v; want %d, true", exitErr.ExitCode(), exitErr.JSONWritten(), tt.wantCode)
			}
			if response["success"] != false {
				t.Fatalf("success = %v, want false", response["success"])
			}
			if response["error"] == "" || response["error"] == nil {
				t.Fatalf("error = %v, want severity cause", response["error"])
			}
		})
	}
}

func assertHealthFailureJSON(t *testing.T, stdout, wantError string) {
	t.Helper()
	var response map[string]interface{}
	if err := json.Unmarshal([]byte(stdout), &response); err != nil {
		t.Fatalf("single JSON response decode failed: %v\nstdout=%s", err, stdout)
	}
	if response["success"] != false {
		t.Fatalf("success = %v, want false", response["success"])
	}
	errorText, _ := response["error"].(string)
	if !strings.Contains(errorText, wantError) {
		t.Fatalf("error = %q, want substring %q", errorText, wantError)
	}
}

func TestFilterHealthResultWithPaneIDsDisambiguatesRepeatedPaneIndex(t *testing.T) {
	result := &health.SessionHealth{
		Session: "multi-window",
		Agents: []health.AgentHealth{
			{Pane: 0, PaneID: "%10", Status: health.StatusOK},
			{Pane: 0, PaneID: "%20", Status: health.StatusWarning},
		},
	}

	filtered := filterHealthResultWithPaneIDs(result, map[string]struct{}{"%20": {}}, "")
	if len(filtered.Agents) != 1 || filtered.Agents[0].PaneID != "%20" {
		t.Fatalf("filtered agents = %+v, want only %%20", filtered.Agents)
	}
	if filtered.Summary.Total != 1 || filtered.Summary.Warning != 1 {
		t.Fatalf("filtered summary = %+v, want one warning", filtered.Summary)
	}
}
