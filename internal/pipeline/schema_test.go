package pipeline

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"
	"time"

	"gopkg.in/yaml.v3"
)

// DefaultWorkflowSettings and DefaultStepTimeout are test-only helpers
// retained after the production functions were removed as dead code; the
// pipeline test suite builds most of its Workflows from these defaults.

// DefaultWorkflowSettings returns sensible defaults for workflow settings.
func DefaultWorkflowSettings() WorkflowSettings {
	logDispatch := true
	return WorkflowSettings{
		Timeout:          Duration{Duration: 30 * time.Minute},
		OnError:          ErrorActionFail,
		LogDispatch:      &logDispatch,
		NotifyOnComplete: false,
		NotifyOnError:    true,
	}
}

// DefaultStepTimeout returns the default timeout for a step.
func DefaultStepTimeout() Duration {
	return Duration{Duration: 5 * time.Minute}
}

func TestDuration_UnmarshalText(t *testing.T) {

	tests := []struct {
		name    string
		input   string
		want    time.Duration
		wantErr bool
	}{
		{
			name:  "seconds",
			input: "30s",
			want:  30 * time.Second,
		},
		{
			name:  "minutes",
			input: "5m",
			want:  5 * time.Minute,
		},
		{
			name:  "hours",
			input: "2h",
			want:  2 * time.Hour,
		},
		{
			name:  "combined",
			input: "1h30m45s",
			want:  1*time.Hour + 30*time.Minute + 45*time.Second,
		},
		{
			name:  "milliseconds",
			input: "500ms",
			want:  500 * time.Millisecond,
		},
		{
			name:  "zero",
			input: "0s",
			want:  0,
		},
		{
			name:    "invalid format",
			input:   "invalid",
			wantErr: true,
		},
		{
			// Bare-integer form is now accepted as seconds (deliberate
			// extension; matches author convenience in YAML where
			// `timeout: 300` is a frequent shorthand). Use "invalid"
			// above for the negative-format test.
			name:  "bare integer means seconds",
			input: "30",
			want:  30 * time.Second,
		},
		{
			name:  "empty string parses to zero",
			input: "",
			want:  0,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var d Duration
			err := d.UnmarshalText([]byte(tt.input))
			if tt.wantErr {
				if err == nil {
					t.Errorf("UnmarshalText(%q) expected error, got nil", tt.input)
				}
				return
			}
			if err != nil {
				t.Errorf("UnmarshalText(%q) unexpected error: %v", tt.input, err)
				return
			}
			if d.Duration != tt.want {
				t.Errorf("UnmarshalText(%q) = %v, want %v", tt.input, d.Duration, tt.want)
			}
		})
	}
}

func TestDuration_MarshalText(t *testing.T) {

	tests := []struct {
		name string
		d    Duration
		want string
	}{
		{
			name: "seconds",
			d:    Duration{Duration: 30 * time.Second},
			want: "30s",
		},
		{
			name: "minutes",
			d:    Duration{Duration: 5 * time.Minute},
			want: "5m0s",
		},
		{
			name: "hours",
			d:    Duration{Duration: 2 * time.Hour},
			want: "2h0m0s",
		},
		{
			name: "combined",
			d:    Duration{Duration: 1*time.Hour + 30*time.Minute + 45*time.Second},
			want: "1h30m45s",
		},
		{
			name: "zero",
			d:    Duration{Duration: 0},
			want: "0s",
		},
		{
			name: "milliseconds",
			d:    Duration{Duration: 500 * time.Millisecond},
			want: "500ms",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := tt.d.MarshalText()
			if err != nil {
				t.Errorf("MarshalText() unexpected error: %v", err)
				return
			}
			if string(got) != tt.want {
				t.Errorf("MarshalText() = %q, want %q", string(got), tt.want)
			}
		})
	}
}

func TestDuration_RoundTrip(t *testing.T) {

	durations := []time.Duration{
		0,
		time.Second,
		5 * time.Minute,
		2*time.Hour + 30*time.Minute,
		time.Hour,
	}

	for _, original := range durations {
		d := Duration{Duration: original}
		marshaled, err := d.MarshalText()
		if err != nil {
			t.Errorf("MarshalText(%v) unexpected error: %v", original, err)
			continue
		}

		var unmarshaled Duration
		if err := unmarshaled.UnmarshalText(marshaled); err != nil {
			t.Errorf("UnmarshalText(%q) unexpected error: %v", string(marshaled), err)
			continue
		}

		if unmarshaled.Duration != original {
			t.Errorf("RoundTrip(%v) = %v, want %v", original, unmarshaled.Duration, original)
		}
	}
}

func TestOutputParse_UnmarshalText(t *testing.T) {

	tests := []struct {
		name  string
		input string
		want  string
	}{
		{
			name:  "json",
			input: "json",
			want:  "json",
		},
		{
			name:  "yaml",
			input: "yaml",
			want:  "yaml",
		},
		{
			name:  "lines",
			input: "lines",
			want:  "lines",
		},
		{
			name:  "first_line",
			input: "first_line",
			want:  "first_line",
		},
		{
			name:  "regex",
			input: "regex",
			want:  "regex",
		},
		{
			name:  "none",
			input: "none",
			want:  "none",
		},
		{
			name:  "empty",
			input: "",
			want:  "",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var o OutputParse
			err := o.UnmarshalText([]byte(tt.input))
			if err != nil {
				t.Errorf("UnmarshalText(%q) unexpected error: %v", tt.input, err)
				return
			}
			if o.Type != tt.want {
				t.Errorf("UnmarshalText(%q).Type = %q, want %q", tt.input, o.Type, tt.want)
			}
		})
	}
}

func TestDefaultStepTimeout(t *testing.T) {

	d := DefaultStepTimeout()
	expected := 5 * time.Minute

	if d.Duration != expected {
		t.Errorf("DefaultStepTimeout() = %v, want %v", d.Duration, expected)
	}
}

func TestDefaultWorkflowSettings(t *testing.T) {

	s := DefaultWorkflowSettings()

	if s.Timeout.Duration != 30*time.Minute {
		t.Errorf("DefaultWorkflowSettings().Timeout = %v, want 30m", s.Timeout.Duration)
	}

	if s.OnError != ErrorActionFail {
		t.Errorf("DefaultWorkflowSettings().OnError = %q, want %q", s.OnError, ErrorActionFail)
	}

	if s.NotifyOnComplete {
		t.Error("DefaultWorkflowSettings().NotifyOnComplete = true, want false")
	}

	if !s.NotifyOnError {
		t.Error("DefaultWorkflowSettings().NotifyOnError = false, want true")
	}
}

func TestPaneSpec_UnmarshalYAML(t *testing.T) {
	type paneDoc struct {
		Pane PaneSpec `yaml:"pane,omitempty"`
	}

	tests := []struct {
		name     string
		input    string
		want     PaneSpec
		wantZero bool
	}{
		{
			name:  "literal int",
			input: "pane: 3\n",
			want:  PaneSpec{Index: 3},
		},
		{
			name:     "zero is unset",
			input:    "pane: 0\n",
			want:     PaneSpec{},
			wantZero: true,
		},
		{
			name:  "template expression",
			input: "pane: '${defaults.triage_pane}'\n",
			want:  PaneSpec{Expr: "${defaults.triage_pane}"},
		},
		{
			name:  "literal string expression",
			input: "pane: literal-string\n",
			want:  PaneSpec{Expr: "literal-string"},
		},
		{
			name:     "empty string is unset",
			input:    "pane: ''\n",
			want:     PaneSpec{},
			wantZero: true,
		},
		{
			name:     "null is unset",
			input:    "pane: null\n",
			want:     PaneSpec{},
			wantZero: true,
		},
		{
			name:     "missing is unset",
			input:    "{}\n",
			want:     PaneSpec{},
			wantZero: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var got paneDoc
			if err := yaml.Unmarshal([]byte(tt.input), &got); err != nil {
				t.Fatalf("yaml.Unmarshal() error = %v", err)
			}
			if got.Pane != tt.want {
				t.Fatalf("Pane = %+v, want %+v", got.Pane, tt.want)
			}
			if got.Pane.IsZero() != tt.wantZero {
				t.Fatalf("Pane.IsZero() = %v, want %v", got.Pane.IsZero(), tt.wantZero)
			}
		})
	}
}

func TestPaneSpec_MarshalYAML(t *testing.T) {
	type paneDoc struct {
		Pane PaneSpec `yaml:"pane,omitempty"`
	}

	tests := []struct {
		name      string
		pane      PaneSpec
		wantValue interface{}
		wantOmit  bool
	}{
		{
			name:      "literal int",
			pane:      PaneSpec{Index: 2},
			wantValue: 2,
		},
		{
			name:      "template expression",
			pane:      PaneSpec{Expr: "${foo}"},
			wantValue: "${foo}",
		},
		{
			name:     "zero is omitted",
			pane:     PaneSpec{},
			wantOmit: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			out, err := yaml.Marshal(paneDoc{Pane: tt.pane})
			if err != nil {
				t.Fatalf("yaml.Marshal() error = %v", err)
			}
			if tt.wantOmit {
				if strings.Contains(string(out), "pane:") {
					t.Fatalf("yaml.Marshal() = %q, want pane omitted", string(out))
				}
				return
			}

			var raw map[string]interface{}
			if err := yaml.Unmarshal(out, &raw); err != nil {
				t.Fatalf("yaml.Unmarshal(raw) error = %v", err)
			}
			if raw["pane"] != tt.wantValue {
				t.Fatalf("marshaled pane = %#v, want %#v; yaml = %q", raw["pane"], tt.wantValue, string(out))
			}

			var roundTrip paneDoc
			if err := yaml.Unmarshal(out, &roundTrip); err != nil {
				t.Fatalf("yaml.Unmarshal(roundTrip) error = %v", err)
			}
			if roundTrip.Pane != tt.pane {
				t.Fatalf("roundTrip.Pane = %+v, want %+v; yaml = %q", roundTrip.Pane, tt.pane, string(out))
			}
		})
	}
}

func TestParallelSpec_UnmarshalYAML(t *testing.T) {
	type doc struct {
		Parallel ParallelSpec `yaml:"parallel"`
	}

	tests := []struct {
		name      string
		yaml      string
		wantFlag  bool
		wantSteps int
		wantZero  bool
		wantErr   bool
	}{
		{
			name:     "bool true",
			yaml:     "parallel: true",
			wantFlag: true,
			wantZero: false,
		},
		{
			name:     "bool false",
			yaml:     "parallel: false",
			wantFlag: false,
			wantZero: true,
		},
		{
			name:      "list of steps",
			yaml:      "parallel:\n  - id: a\n    prompt: do A\n  - id: b\n    prompt: do B",
			wantSteps: 2,
			wantZero:  false,
		},
		{
			name:     "empty list",
			yaml:     "parallel: []",
			wantZero: true,
		},
		{
			name:     "missing",
			yaml:     "other: value",
			wantZero: true,
		},
		{
			name:    "invalid type",
			yaml:    "parallel: not-a-bool-or-list",
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var d doc
			err := yaml.Unmarshal([]byte(tt.yaml), &d)
			if tt.wantErr {
				if err == nil {
					t.Fatal("expected error, got nil")
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if d.Parallel.Flag != tt.wantFlag {
				t.Errorf("Flag = %v, want %v", d.Parallel.Flag, tt.wantFlag)
			}
			if len(d.Parallel.Steps) != tt.wantSteps {
				t.Errorf("len(Steps) = %d, want %d", len(d.Parallel.Steps), tt.wantSteps)
			}
			if d.Parallel.IsZero() != tt.wantZero {
				t.Errorf("IsZero() = %v, want %v", d.Parallel.IsZero(), tt.wantZero)
			}
		})
	}
}

func TestParallelSpec_MarshalYAML(t *testing.T) {
	type doc struct {
		Parallel ParallelSpec `yaml:"parallel,omitempty"`
	}

	tests := []struct {
		name       string
		spec       ParallelSpec
		wantInYAML string
	}{
		{
			name:       "flag true marshals as bool",
			spec:       ParallelSpec{Flag: true},
			wantInYAML: "parallel: true",
		},
		{
			name:       "steps marshal as list",
			spec:       ParallelSpec{Steps: []Step{{ID: "a"}, {ID: "b"}}},
			wantInYAML: "- id: a",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			d := doc{Parallel: tt.spec}
			out, err := yaml.Marshal(d)
			if err != nil {
				t.Fatalf("yaml.Marshal error: %v", err)
			}
			if !strings.Contains(string(out), tt.wantInYAML) {
				t.Errorf("output = %q, want to contain %q", string(out), tt.wantInYAML)
			}
		})
	}
}

func TestParallelSpec_IsZero(t *testing.T) {
	tests := []struct {
		name string
		spec ParallelSpec
		want bool
	}{
		{"default", ParallelSpec{}, true},
		{"flag false only", ParallelSpec{Flag: false}, true},
		{"flag true", ParallelSpec{Flag: true}, false},
		{"has steps", ParallelSpec{Steps: []Step{{ID: "x"}}}, false},
		{"flag true and steps", ParallelSpec{Flag: true, Steps: []Step{{ID: "x"}}}, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := tt.spec.IsZero(); got != tt.want {
				t.Errorf("IsZero() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestParallelSpec_Len(t *testing.T) {
	flagOnly := ParallelSpec{Flag: true}
	if flagOnly.Len() != 0 {
		t.Error("Flag-only Len should be 0")
	}
	withSteps := ParallelSpec{Steps: []Step{{ID: "a"}, {ID: "b"}}}
	if withSteps.Len() != 2 {
		t.Error("two-step Len should be 2")
	}
}

func TestParallelSpec_RoundTrip(t *testing.T) {
	type doc struct {
		Parallel ParallelSpec `yaml:"parallel"`
	}

	origFlag := doc{Parallel: ParallelSpec{Flag: true}}
	out, err := yaml.Marshal(origFlag)
	if err != nil {
		t.Fatal(err)
	}
	var rtFlag doc
	if err := yaml.Unmarshal(out, &rtFlag); err != nil {
		t.Fatal(err)
	}
	if !rtFlag.Parallel.Flag {
		t.Error("round-trip flag=true lost")
	}

	origSteps := doc{Parallel: ParallelSpec{Steps: []Step{{ID: "x", Prompt: "do X"}}}}
	out, err = yaml.Marshal(origSteps)
	if err != nil {
		t.Fatal(err)
	}
	var rtSteps doc
	if err := yaml.Unmarshal(out, &rtSteps); err != nil {
		t.Fatal(err)
	}
	if len(rtSteps.Parallel.Steps) != 1 || rtSteps.Parallel.Steps[0].ID != "x" {
		t.Errorf("round-trip steps lost: %+v", rtSteps.Parallel)
	}
}

func TestOnFailureSpec_UnmarshalYAML(t *testing.T) {
	type doc struct {
		OnFailure OnFailureSpec `yaml:"on_failure"`
	}

	tests := []struct {
		name       string
		yaml       string
		wantAction string
		wantRetry  int
		wantFB     bool
		wantZero   bool
		wantErr    bool
	}{
		{
			name:       "continue string",
			yaml:       "on_failure: continue",
			wantAction: "continue",
		},
		{
			name:       "retry:3",
			yaml:       "on_failure: 'retry:3'",
			wantAction: "retry",
			wantRetry:  3,
		},
		{
			name:       "retry:abc (unparseable count)",
			yaml:       "on_failure: 'retry:abc'",
			wantAction: "retry:abc",
			wantRetry:  0,
		},
		{
			name:   "structured fallback",
			yaml:   "on_failure:\n  pane: 1\n  template: foo.md",
			wantFB: true,
		},
		{
			name:       "fallback_to_ntm_inbox",
			yaml:       "on_failure: fallback_to_ntm_inbox",
			wantAction: "fallback_to_ntm_inbox",
		},
		{
			name:     "missing",
			yaml:     "other: value",
			wantZero: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var d doc
			err := yaml.Unmarshal([]byte(tt.yaml), &d)
			if tt.wantErr {
				if err == nil {
					t.Fatal("expected error")
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if d.OnFailure.Action != tt.wantAction {
				t.Errorf("Action = %q, want %q", d.OnFailure.Action, tt.wantAction)
			}
			if d.OnFailure.RetryCount != tt.wantRetry {
				t.Errorf("RetryCount = %d, want %d", d.OnFailure.RetryCount, tt.wantRetry)
			}
			if tt.wantFB && len(d.OnFailure.Fallback) == 0 {
				t.Error("expected Fallback to be non-empty")
			}
			if d.OnFailure.IsZero() != tt.wantZero {
				t.Errorf("IsZero() = %v, want %v", d.OnFailure.IsZero(), tt.wantZero)
			}
		})
	}
}

func TestOnFailureSpec_MarshalYAML(t *testing.T) {
	tests := []struct {
		name string
		spec OnFailureSpec
		want string
	}{
		{
			name: "continue",
			spec: OnFailureSpec{Action: "continue"},
			want: "continue",
		},
		{
			name: "retry with count",
			spec: OnFailureSpec{Action: "retry", RetryCount: 3},
			want: "retry:3",
		},
		{
			name: "fallback map",
			spec: OnFailureSpec{Fallback: map[string]interface{}{"pane": 1}},
			want: "pane:",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			out, err := yaml.Marshal(tt.spec)
			if err != nil {
				t.Fatalf("yaml.Marshal error: %v", err)
			}
			if !strings.Contains(string(out), tt.want) {
				t.Errorf("output = %q, want to contain %q", string(out), tt.want)
			}
		})
	}
}

func TestOnFailureSpec_IsZero(t *testing.T) {
	tests := []struct {
		name string
		spec OnFailureSpec
		want bool
	}{
		{"empty", OnFailureSpec{}, true},
		{"action only", OnFailureSpec{Action: "continue"}, false},
		{"retry count only", OnFailureSpec{RetryCount: 3}, false},
		{"fallback only", OnFailureSpec{Fallback: map[string]interface{}{"pane": 1}}, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := tt.spec.IsZero(); got != tt.want {
				t.Errorf("IsZero() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestOnFailureSpec_RoundTrip(t *testing.T) {
	type doc struct {
		OnFailure OnFailureSpec `yaml:"on_failure"`
	}

	orig := doc{OnFailure: OnFailureSpec{Action: "retry", RetryCount: 5}}
	out, err := yaml.Marshal(orig)
	if err != nil {
		t.Fatal(err)
	}
	var rt doc
	if err := yaml.Unmarshal(out, &rt); err != nil {
		t.Fatal(err)
	}
	if rt.OnFailure.Action != "retry" || rt.OnFailure.RetryCount != 5 {
		t.Errorf("round-trip failed: %+v", rt.OnFailure)
	}
}

func TestIntOrExpr_UnmarshalYAML(t *testing.T) {
	type doc struct {
		Val IntOrExpr `yaml:"val"`
	}

	tests := []struct {
		name     string
		yaml     string
		wantVal  int
		wantExpr string
		wantZero bool
		wantErr  bool
	}{
		{
			name:    "literal int 6",
			yaml:    "val: 6",
			wantVal: 6,
		},
		{
			name:     "template expression",
			yaml:     "val: '${defaults.foo}'",
			wantExpr: "${defaults.foo}",
		},
		{
			name:     "zero is unset",
			yaml:     "val: 0",
			wantZero: true,
		},
		{
			name:    "negative",
			yaml:    "val: -1",
			wantVal: -1,
		},
		{
			name:     "missing",
			yaml:     "other: 1",
			wantZero: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var d doc
			err := yaml.Unmarshal([]byte(tt.yaml), &d)
			if tt.wantErr {
				if err == nil {
					t.Fatal("expected error")
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if d.Val.Value != tt.wantVal {
				t.Errorf("Value = %d, want %d", d.Val.Value, tt.wantVal)
			}
			if d.Val.Expr != tt.wantExpr {
				t.Errorf("Expr = %q, want %q", d.Val.Expr, tt.wantExpr)
			}
			if d.Val.IsZero() != tt.wantZero {
				t.Errorf("IsZero() = %v, want %v", d.Val.IsZero(), tt.wantZero)
			}
		})
	}
}

func TestIntOrExpr_MarshalYAML(t *testing.T) {
	tests := []struct {
		name string
		spec IntOrExpr
		want string
	}{
		{
			name: "int 6",
			spec: IntOrExpr{Value: 6},
			want: "6",
		},
		{
			name: "expr",
			spec: IntOrExpr{Expr: "${defaults.foo}"},
			want: "${defaults.foo}",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			out, err := yaml.Marshal(tt.spec)
			if err != nil {
				t.Fatalf("yaml.Marshal error: %v", err)
			}
			if !strings.Contains(string(out), tt.want) {
				t.Errorf("output = %q, want to contain %q", string(out), tt.want)
			}
		})
	}
}

func TestIntOrExpr_IsZero(t *testing.T) {
	tests := []struct {
		name string
		spec IntOrExpr
		want bool
	}{
		{"default", IntOrExpr{}, true},
		{"zero value", IntOrExpr{Value: 0}, true},
		{"non-zero value", IntOrExpr{Value: 5}, false},
		{"has expr", IntOrExpr{Expr: "x"}, false},
		{"negative", IntOrExpr{Value: -1}, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := tt.spec.IsZero(); got != tt.want {
				t.Errorf("IsZero() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestAfterRef_UnmarshalYAML(t *testing.T) {
	type doc struct {
		After AfterRef `yaml:"after"`
	}

	tests := []struct {
		name string
		yaml string
		want []string
	}{
		{
			name: "single string",
			yaml: "after: spawn",
			want: []string{"spawn"},
		},
		{
			name: "list of strings",
			yaml: "after:\n  - spawn\n  - audit",
			want: []string{"spawn", "audit"},
		},
		{
			name: "empty string",
			yaml: "after: ''",
			want: nil,
		},
		{
			name: "missing",
			yaml: "other: value",
			want: nil,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var d doc
			if err := yaml.Unmarshal([]byte(tt.yaml), &d); err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if len(d.After) != len(tt.want) {
				t.Fatalf("len = %d, want %d; got %v", len(d.After), len(tt.want), []string(d.After))
			}
			for i := range d.After {
				if d.After[i] != tt.want[i] {
					t.Errorf("index %d: got %q, want %q", i, d.After[i], tt.want[i])
				}
			}
		})
	}
}

func TestStringOrList_UnmarshalYAML(t *testing.T) {
	type doc struct {
		Notes StringOrList `yaml:"notes"`
	}

	tests := []struct {
		name string
		yaml string
		want []string
	}{
		{
			name: "single string",
			yaml: "notes: one note",
			want: []string{"one note"},
		},
		{
			name: "list",
			yaml: "notes:\n  - a\n  - b",
			want: []string{"a", "b"},
		},
		{
			name: "empty string",
			yaml: "notes: ''",
			want: nil,
		},
		{
			name: "missing",
			yaml: "other: value",
			want: nil,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var d doc
			if err := yaml.Unmarshal([]byte(tt.yaml), &d); err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if len(d.Notes) != len(tt.want) {
				t.Fatalf("len = %d, want %d; got %v", len(d.Notes), len(tt.want), []string(d.Notes))
			}
			for i := range d.Notes {
				if d.Notes[i] != tt.want[i] {
					t.Errorf("index %d: got %q, want %q", i, d.Notes[i], tt.want[i])
				}
			}
		})
	}
}

func TestOutputDecl_UnmarshalYAML(t *testing.T) {
	type doc struct {
		Outputs []OutputDecl `yaml:"outputs"`
	}

	tests := []struct {
		name     string
		yaml     string
		wantLen  int
		wantName string
		wantPath string
		wantErr  bool
	}{
		{
			name:     "bare string path",
			yaml:     "outputs:\n  - deliverables/HANDBACK.md",
			wantLen:  1,
			wantPath: "deliverables/HANDBACK.md",
		},
		{
			name:     "full struct",
			yaml:     "outputs:\n  - name: report\n    description: final\n    path: foo",
			wantLen:  1,
			wantName: "report",
			wantPath: "foo",
		},
		{
			name:     "single-key shorthand",
			yaml:     "outputs:\n  - workspace: ${workspace_path}",
			wantLen:  1,
			wantName: "workspace",
			wantPath: "${workspace_path}",
		},
		{
			name:     "name-only structured form",
			yaml:     "outputs:\n  - name: report",
			wantLen:  1,
			wantName: "report",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var d doc
			err := yaml.Unmarshal([]byte(tt.yaml), &d)
			if tt.wantErr {
				if err == nil {
					t.Fatal("expected error")
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if len(d.Outputs) != tt.wantLen {
				t.Fatalf("len = %d, want %d", len(d.Outputs), tt.wantLen)
			}
			if tt.wantLen > 0 {
				o := d.Outputs[0]
				if tt.wantName != "" && o.Name != tt.wantName {
					t.Errorf("Name = %q, want %q", o.Name, tt.wantName)
				}
				if tt.wantPath != "" && o.Path != tt.wantPath {
					t.Errorf("Path = %q, want %q", o.Path, tt.wantPath)
				}
			}
		})
	}
}

func TestOutputDecl_UnmarshalYAML_Errors(t *testing.T) {
	type doc struct {
		Outputs []OutputDecl `yaml:"outputs"`
	}

	tests := []struct {
		name string
		yaml string
	}{
		{
			name: "empty map",
			yaml: "outputs:\n  - {}",
		},
		{
			name: "two-key shorthand",
			yaml: "outputs:\n  - a: 1\n    b: 2",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var d doc
			err := yaml.Unmarshal([]byte(tt.yaml), &d)
			if err == nil {
				t.Fatal("expected error for invalid OutputDecl form")
			}
		})
	}
}

func TestNormalize_Idempotency(t *testing.T) {
	w := &Workflow{
		Name:   "test",
		Inputs: []string{"x", "y"},
		Steps: []Step{
			{
				ID:    "s1",
				After: AfterRef{"s0"},
				OnFailure: OnFailureSpec{
					Action:     "retry",
					RetryCount: 3,
				},
				TemplateParams: map[string]interface{}{"foo": "bar"},
			},
		},
	}

	w.Normalize()

	snap1Steps := make([]Step, len(w.Steps))
	copy(snap1Steps, w.Steps)
	snap1Vars := make(map[string]VarDef, len(w.Vars))
	for k, v := range w.Vars {
		snap1Vars[k] = v
	}

	w.Normalize()

	if len(w.Steps) != len(snap1Steps) {
		t.Fatalf("step count changed on second normalize")
	}
	s := w.Steps[0]
	if len(s.DependsOn) != 1 || s.DependsOn[0] != "s0" {
		t.Errorf("DependsOn = %v after second normalize, want [s0]", s.DependsOn)
	}
	if s.OnError != ErrorActionRetry {
		t.Errorf("OnError = %q after second normalize, want retry", s.OnError)
	}
	if s.RetryCount != 3 {
		t.Errorf("RetryCount = %d after second normalize, want 3", s.RetryCount)
	}
	if len(s.Params) != 1 || s.Params["foo"] != "bar" {
		t.Errorf("Params = %v after second normalize, want {foo: bar}", s.Params)
	}
	if len(w.Vars) != len(snap1Vars) {
		t.Errorf("Vars count changed: %d vs %d", len(w.Vars), len(snap1Vars))
	}
}

func TestNormalize_AfterAlias(t *testing.T) {
	w := &Workflow{
		Steps: []Step{
			{
				ID:        "s1",
				After:     AfterRef{"a", "b"},
				DependsOn: []string{"a"},
			},
		},
	}
	w.Normalize()
	s := w.Steps[0]
	if len(s.DependsOn) != 2 {
		t.Fatalf("DependsOn = %v, want [a, b] (deduped)", s.DependsOn)
	}
	if s.DependsOn[0] != "a" || s.DependsOn[1] != "b" {
		t.Errorf("DependsOn = %v, want [a, b]", s.DependsOn)
	}
	if len(s.After) != 0 {
		t.Errorf("After should be cleared after normalize, got %v", []string(s.After))
	}
}

func TestNormalize_OnFailureRetry(t *testing.T) {
	w := &Workflow{
		Steps: []Step{
			{
				ID: "s1",
				OnFailure: OnFailureSpec{
					Action:     "retry",
					RetryCount: 5,
				},
			},
		},
	}
	w.Normalize()
	s := w.Steps[0]
	if s.OnError != ErrorActionRetry {
		t.Errorf("OnError = %q, want retry", s.OnError)
	}
	if s.RetryCount != 5 {
		t.Errorf("RetryCount = %d, want 5", s.RetryCount)
	}
	if !s.OnFailure.IsZero() {
		t.Errorf("OnFailure should be cleared, got %+v", s.OnFailure)
	}
}

func TestNormalize_OnFailurePreservesNonEnum(t *testing.T) {
	w := &Workflow{
		Steps: []Step{
			{
				ID: "s1",
				OnFailure: OnFailureSpec{
					Action: "fallback_to_ntm_inbox",
				},
			},
		},
	}
	w.Normalize()
	s := w.Steps[0]
	if s.OnError != "" {
		t.Errorf("OnError should be empty for non-enum, got %q", s.OnError)
	}
	if s.OnFailure.Action != "fallback_to_ntm_inbox" {
		t.Errorf("OnFailure.Action should be preserved, got %q", s.OnFailure.Action)
	}
}

func TestNormalize_TemplateParamsMerge(t *testing.T) {
	w := &Workflow{
		Steps: []Step{
			{
				ID:             "s1",
				Params:         map[string]interface{}{"a": "from-params"},
				TemplateParams: map[string]interface{}{"a": "from-tp", "b": "only-tp"},
			},
		},
	}
	w.Normalize()
	s := w.Steps[0]
	if s.Params["a"] != "from-params" {
		t.Errorf("Params[a] = %v, want from-params (Params wins)", s.Params["a"])
	}
	if s.Params["b"] != "only-tp" {
		t.Errorf("Params[b] = %v, want only-tp", s.Params["b"])
	}
	if s.TemplateParams != nil {
		t.Errorf("TemplateParams should be nil after normalize, got %v", s.TemplateParams)
	}
}

func TestNormalize_LoopBodyAlias(t *testing.T) {
	w := &Workflow{
		Steps: []Step{
			{
				ID: "s1",
				Loop: &LoopConfig{
					Times: 3,
					Body:  []Step{{ID: "inner"}},
				},
			},
		},
	}
	w.Normalize()
	lc := w.Steps[0].Loop
	if len(lc.Steps) != 1 || lc.Steps[0].ID != "inner" {
		t.Errorf("Loop.Steps = %v, want [{ID: inner}]", lc.Steps)
	}
	if len(lc.Body) != 0 {
		t.Errorf("Loop.Body should be cleared, got %v", lc.Body)
	}
}

func TestNormalize_ForeachBodyAlias(t *testing.T) {
	w := &Workflow{
		Steps: []Step{
			{
				ID: "s1",
				Foreach: &ForeachConfig{
					Items: "${vars.list}",
					Body:  []Step{{ID: "inner"}},
					TemplateParams: map[string]interface{}{
						"key": "val",
					},
				},
			},
		},
	}
	w.Normalize()
	fc := w.Steps[0].Foreach
	if len(fc.Steps) != 1 || fc.Steps[0].ID != "inner" {
		t.Errorf("Foreach.Steps = %v, want [{ID: inner}]", fc.Steps)
	}
	if len(fc.Body) != 0 {
		t.Errorf("Foreach.Body should be cleared, got %v", fc.Body)
	}
	if fc.Params["key"] != "val" {
		t.Errorf("Foreach.Params[key] = %v, want val", fc.Params["key"])
	}
	if fc.TemplateParams != nil {
		t.Errorf("Foreach.TemplateParams should be nil, got %v", fc.TemplateParams)
	}
}

func TestNormalize_InputsToVars(t *testing.T) {
	w := &Workflow{
		Inputs: []string{"x", "y"},
		Vars: map[string]VarDef{
			"x": {Default: "existing"},
		},
	}
	w.Normalize()
	if _, ok := w.Vars["y"]; !ok {
		t.Fatal("y not added to Vars")
	}
	if !w.Vars["y"].Required {
		t.Error("y should be Required")
	}
	if w.Vars["x"].Default != "existing" {
		t.Error("existing x var should not be overwritten")
	}
}

func TestNormalize_RecursiveParallel(t *testing.T) {
	w := &Workflow{
		Steps: []Step{
			{
				ID: "outer",
				Parallel: ParallelSpec{
					Steps: []Step{
						{
							ID:    "inner",
							After: AfterRef{"other"},
						},
					},
				},
			},
		},
	}
	w.Normalize()
	inner := w.Steps[0].Parallel.Steps[0]
	if len(inner.DependsOn) != 1 || inner.DependsOn[0] != "other" {
		t.Errorf("nested After not normalized: DependsOn = %v", inner.DependsOn)
	}
	if len(inner.After) != 0 {
		t.Errorf("nested After should be cleared, got %v", []string(inner.After))
	}
}

func TestSchemaAlternationTypes_UnmarshalJSONShorthands(t *testing.T) {
	data := []byte(`{
		"name": "json-shorthand",
		"notes": "single note",
		"outputs": [
			"deliverables/report.md",
			{"workspace": "${workspace_path}"},
			{"name": "summary", "path": "out/summary.md", "description": "final report"}
		],
		"steps": [
			{"id": "pane-index", "prompt": "hello", "pane": 3},
			{
				"id": "pane-expr",
				"prompt": "hello",
				"pane": "${defaults.triage_pane}",
				"after": "pane-index",
				"parallel": true,
				"loop": {"items": "${vars.items}", "max_iterations": "${defaults.max_iterations}"},
				"foreach": {"models": "codex", "max_rounds": 2}
			},
			{
				"id": "parallel-list",
				"parallel": [{"id": "a", "prompt": "A"}],
				"on_failure": "retry:2"
			},
			{
				"id": "fallback",
				"on_failure": {"pane": "${defaults.recovery_pane}", "template": "MO-recover.md"}
			}
		]
	}`)

	var workflow Workflow
	if err := json.Unmarshal(data, &workflow); err != nil {
		t.Fatalf("json.Unmarshal() error = %v", err)
	}

	if !reflect.DeepEqual(workflow.Notes, StringOrList{"single note"}) {
		t.Fatalf("Notes = %v, want single-note list", workflow.Notes)
	}
	if workflow.Outputs[0].Path != "deliverables/report.md" {
		t.Fatalf("bare output path = %q", workflow.Outputs[0].Path)
	}
	if workflow.Outputs[1].Name != "workspace" || workflow.Outputs[1].Path != "${workspace_path}" {
		t.Fatalf("single-key output = %+v", workflow.Outputs[1])
	}
	if workflow.Outputs[2].Name != "summary" || workflow.Outputs[2].Path != "out/summary.md" {
		t.Fatalf("structured output = %+v", workflow.Outputs[2])
	}

	if workflow.Steps[0].Pane != (PaneSpec{Index: 3}) {
		t.Fatalf("Pane index = %+v", workflow.Steps[0].Pane)
	}
	step := workflow.Steps[1]
	if step.Pane != (PaneSpec{Expr: "${defaults.triage_pane}"}) {
		t.Fatalf("Pane expr = %+v", step.Pane)
	}
	if !reflect.DeepEqual(step.After, AfterRef{"pane-index"}) {
		t.Fatalf("After = %v", []string(step.After))
	}
	if !step.Parallel.Flag || step.Parallel.Len() != 0 {
		t.Fatalf("Parallel flag = %+v", step.Parallel)
	}
	if step.Loop.MaxIterations != (IntOrExpr{Expr: "${defaults.max_iterations}"}) {
		t.Fatalf("Loop max_iterations = %+v", step.Loop.MaxIterations)
	}
	if !reflect.DeepEqual(step.Foreach.Models, StringOrList{"codex"}) {
		t.Fatalf("Foreach models = %v", step.Foreach.Models)
	}
	if step.Foreach.MaxRounds != (IntOrExpr{Value: 2}) {
		t.Fatalf("Foreach max_rounds = %+v", step.Foreach.MaxRounds)
	}

	parallelList := workflow.Steps[2]
	if parallelList.Parallel.Len() != 1 || parallelList.Parallel.Steps[0].ID != "a" {
		t.Fatalf("Parallel list = %+v", parallelList.Parallel)
	}
	if parallelList.OnFailure.Action != "retry" || parallelList.OnFailure.RetryCount != 2 {
		t.Fatalf("retry shorthand = %+v", parallelList.OnFailure)
	}

	fallback := workflow.Steps[3].OnFailure.Fallback
	if fallback["pane"] != "${defaults.recovery_pane}" || fallback["template"] != "MO-recover.md" {
		t.Fatalf("fallback map = %#v", fallback)
	}
}

func TestSchemaAlternationTypes_JSONRoundTripCanonical(t *testing.T) {
	workflow := Workflow{
		SchemaVersion: SchemaVersion,
		Name:          "json-roundtrip",
		Notes:         StringOrList{"operator note", "second note"},
		Outputs: []OutputDecl{
			{Path: "deliverables/report.md"},
			{Name: "summary", Path: "out/summary.md", Description: "final report"},
		},
		Steps: []Step{
			{
				ID:        "pane-index",
				Pane:      PaneSpec{Index: 2},
				After:     AfterRef{"bootstrap"},
				Prompt:    "literal pane",
				OnFailure: OnFailureSpec{Action: "retry", RetryCount: 3},
			},
			{
				ID:     "pane-expr",
				Pane:   PaneSpec{Expr: "${defaults.triage_pane}"},
				Prompt: "expr pane",
				Parallel: ParallelSpec{
					Steps: []Step{{ID: "sub", Prompt: "sub prompt"}},
				},
				Loop: &LoopConfig{
					Items:         "${vars.items}",
					MaxIterations: IntOrExpr{Expr: "${defaults.max_iterations}"},
				},
				Foreach: &ForeachConfig{
					Items:     "${vars.models}",
					Models:    StringOrList{"codex", "gemini"},
					MaxRounds: IntOrExpr{Value: 2},
				},
			},
			{
				ID:       "parallel-flag",
				Prompt:   "flag",
				Parallel: ParallelSpec{Flag: true},
				OnFailure: OnFailureSpec{
					Fallback: map[string]interface{}{
						"pane":     "${defaults.recovery_pane}",
						"template": "MO-recover.md",
					},
				},
			},
		},
	}

	data, err := json.Marshal(workflow)
	if err != nil {
		t.Fatalf("json.Marshal() error = %v", err)
	}
	var got Workflow
	if err := json.Unmarshal(data, &got); err != nil {
		t.Fatalf("json.Unmarshal() error = %v", err)
	}
	if !reflect.DeepEqual(got, workflow) {
		t.Fatalf("roundtrip mismatch\n got: %#v\nwant: %#v\njson: %s", got, workflow, data)
	}
}

func TestSchemaAlternationTypes_UnmarshalJSONErrors(t *testing.T) {
	tests := []struct {
		name string
		dst  interface{}
		json string
	}{
		{name: "pane object wrong shape", dst: &PaneSpec{}, json: `{"index": "nope"}`},
		{name: "after number", dst: &AfterRef{}, json: `42`},
		{name: "parallel string", dst: &ParallelSpec{}, json: `"yes"`},
		{name: "output empty object", dst: &OutputDecl{}, json: `{}`},
		{name: "notes object", dst: &StringOrList{}, json: `{"note": "x"}`},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if err := json.Unmarshal([]byte(tt.json), tt.dst); err == nil {
				t.Fatal("expected unmarshal error")
			}
		})
	}
}
