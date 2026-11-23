package cli

import (
	"testing"
)

func TestValidateAgentCount(t *testing.T) {
	tests := []struct {
		input   string
		wantErr bool
	}{
		{"", false},
		{"0", false},
		{"1", false},
		{"10", false},
		{"20", false},
		{"21", true},  // exceeds max
		{"-1", true},  // negative
		{"abc", true}, // non-numeric
		{"3.5", true}, // float
	}
	for _, tt := range tests {
		err := validateAgentCount(tt.input)
		if (err != nil) != tt.wantErr {
			t.Errorf("validateAgentCount(%q) err=%v, wantErr=%v", tt.input, err, tt.wantErr)
		}
	}
}

func TestParseCount(t *testing.T) {
	tests := []struct {
		input string
		want  int
	}{
		{"", 0},
		{"0", 0},
		{"3", 3},
		{"10", 10},
		{"abc", 0}, // invalid returns 0
	}
	for _, tt := range tests {
		got := parseCount(tt.input)
		if got != tt.want {
			t.Errorf("parseCount(%q) = %d, want %d", tt.input, got, tt.want)
		}
	}
}

func TestSpawnWizardGrokCountProjection(t *testing.T) {
	result := spawnWizardResultFromCounts(map[string]int{"grok": 3, "pi": 2})
	if result.GrokCount != 3 || result.PiCount != 2 {
		t.Fatalf("GrokCount=%d PiCount=%d, want 3 and 2", result.GrokCount, result.PiCount)
	}

	specs := wizardAgentSpecs(result)
	if len(specs) != 2 || specs[0] != (AgentSpec{Type: AgentTypeGrok, Count: 3}) || specs[1] != (AgentSpec{Type: AgentTypePi, Count: 2}) {
		t.Fatalf("wizardAgentSpecs() = %+v, want Grok x3 and Pi x2", specs)
	}

	if got := formatWizardAgentCountSummary(map[string]int{"cc": 1, "grok": 3, "pi": 2}); got != "cc:1 grok:3 pi:2" {
		t.Fatalf("formatWizardAgentCountSummary() = %q, want %q", got, "cc:1 grok:3 pi:2")
	}
}
