package cli

import (
	"testing"

	"github.com/Dicklesworthstone/ntm/internal/health"
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
