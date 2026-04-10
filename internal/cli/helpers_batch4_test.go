package cli

import (
	"strings"
	"testing"
	"time"
)

// =============================================================================
// history.go: formatTargets
// =============================================================================

func TestFormatTargets(t *testing.T) {

	tests := []struct {
		name    string
		targets []string
		want    string
	}{
		{"empty", nil, "(none)"},
		{"single", []string{"my-session"}, "my-session"},
		{"two", []string{"s1", "s2"}, "s1,s2"},
		{"three", []string{"s1", "s2", "s3"}, "s1,s2,s3"},
		{"four becomes all", []string{"a", "b", "c", "d"}, "all (4)"},
		{"five", []string{"a", "b", "c", "d", "e"}, "all (5)"},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := formatTargets(tc.targets)
			if got != tc.want {
				t.Errorf("formatTargets(%v) = %q, want %q", tc.targets, got, tc.want)
			}
		})
	}
}

// =============================================================================
// history.go: truncateHistoryStr
// =============================================================================

func TestTruncateHistoryStr(t *testing.T) {

	tests := []struct {
		name   string
		input  string
		maxLen int
		check  func(string) bool
		desc   string
	}{
		{
			"newlines removed",
			"line1\nline2\nline3",
			100,
			func(s string) bool { return !strings.Contains(s, "\n") },
			"should not contain newlines",
		},
		{
			"carriage returns removed",
			"hello\r\nworld",
			100,
			func(s string) bool { return !strings.Contains(s, "\r") },
			"should not contain carriage returns",
		},
		{
			"truncated",
			"this is a very long string that should be truncated",
			10,
			func(s string) bool { return len(s) <= 13 }, // truncate may add "..."
			"should be short",
		},
		{
			"short string unchanged",
			"hello",
			100,
			func(s string) bool { return s == "hello" },
			"should be unchanged",
		},
		{
			"empty string",
			"",
			10,
			func(s string) bool { return s == "" },
			"should be empty",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := truncateHistoryStr(tc.input, tc.maxLen)
			if !tc.check(got) {
				t.Errorf("truncateHistoryStr(%q, %d) = %q; %s", tc.input, tc.maxLen, got, tc.desc)
			}
		})
	}
}

// =============================================================================
// history.go: parseHistoryTimeFilter
// =============================================================================

func TestParseHistoryTimeFilter(t *testing.T) {

	t.Run("empty string returns zero time", func(t *testing.T) {
		got, err := parseHistoryTimeFilter("")
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if !got.IsZero() {
			t.Errorf("expected zero time, got %v", got)
		}
	})

	t.Run("whitespace returns zero time", func(t *testing.T) {
		got, err := parseHistoryTimeFilter("   ")
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if !got.IsZero() {
			t.Errorf("expected zero time, got %v", got)
		}
	})

	t.Run("duration string", func(t *testing.T) {
		before := time.Now()
		got, err := parseHistoryTimeFilter("1h")
		after := time.Now()
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		expectedEarliest := before.Add(-1 * time.Hour)
		expectedLatest := after.Add(-1 * time.Hour)
		if got.Before(expectedEarliest.Add(-time.Second)) || got.After(expectedLatest.Add(time.Second)) {
			t.Errorf("parseHistoryTimeFilter(1h) = %v, expected around %v", got, expectedEarliest)
		}
	})

	t.Run("RFC3339 timestamp", func(t *testing.T) {
		ts := "2025-01-15T10:30:00Z"
		got, err := parseHistoryTimeFilter(ts)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		want, _ := time.Parse(time.RFC3339, ts)
		if !got.Equal(want) {
			t.Errorf("parseHistoryTimeFilter(%q) = %v, want %v", ts, got, want)
		}
	})

	t.Run("invalid input returns error", func(t *testing.T) {
		_, err := parseHistoryTimeFilter("not-a-time-or-duration")
		if err == nil {
			t.Error("expected error for invalid input")
		}
	})
}

// =============================================================================
// agent_spec.go: AgentSpecs.Set and Type
// =============================================================================

func TestAgentSpecsSetAndType(t *testing.T) {

	t.Run("Set appends specs", func(t *testing.T) {
		var specs AgentSpecs
		if err := specs.Set("3"); err != nil {
			t.Fatalf("Set(3) error: %v", err)
		}
		if len(specs) != 1 || specs[0].Count != 3 {
			t.Errorf("after Set(3): specs = %+v", specs)
		}

		if err := specs.Set("2:opus"); err != nil {
			t.Fatalf("Set(2:opus) error: %v", err)
		}
		if len(specs) != 2 || specs[1].Count != 2 || specs[1].Model != "opus" {
			t.Errorf("after Set(2:opus): specs = %+v", specs)
		}
	})

	t.Run("Set invalid returns error", func(t *testing.T) {
		var specs AgentSpecs
		if err := specs.Set(""); err == nil {
			t.Error("expected error for empty spec")
		}
		if err := specs.Set("abc"); err == nil {
			t.Error("expected error for non-numeric count")
		}
	})

	t.Run("Type returns N[:model]", func(t *testing.T) {
		var specs AgentSpecs
		if specs.Type() != "N[:model]" {
			t.Errorf("Type() = %q, want N[:model]", specs.Type())
		}
	})
}

// =============================================================================
// send.go: SendTargets.Set and Type
// =============================================================================

func TestSendTargetsSetAndType(t *testing.T) {

	t.Run("Set without variant", func(t *testing.T) {
		var targets SendTargets
		if err := targets.Set("cc"); err != nil {
			t.Fatalf("Set(cc) error: %v", err)
		}
		if len(targets) != 1 {
			t.Fatalf("expected 1 target, got %d", len(targets))
		}
		// Note: SendTargets.Set doesn't set Type (that's set by flag registration)
		if targets[0].Variant != "" {
			t.Errorf("expected empty variant, got %q", targets[0].Variant)
		}
	})

	t.Run("Set with variant", func(t *testing.T) {
		var targets SendTargets
		if err := targets.Set("cc:opus"); err != nil {
			t.Fatalf("Set(cc:opus) error: %v", err)
		}
		if len(targets) != 1 || targets[0].Variant != "opus" {
			t.Errorf("expected variant opus, got %+v", targets)
		}
	})

	t.Run("Set accumulates", func(t *testing.T) {
		var targets SendTargets
		_ = targets.Set("cc:opus")
		_ = targets.Set("cod:gpt4")
		if len(targets) != 2 {
			t.Errorf("expected 2 targets, got %d", len(targets))
		}
	})

	t.Run("Type returns [variant]", func(t *testing.T) {
		var targets SendTargets
		if targets.Type() != "[variant]" {
			t.Errorf("Type() = %q, want [variant]", targets.Type())
		}
	})
}
