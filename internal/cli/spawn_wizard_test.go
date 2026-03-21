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
