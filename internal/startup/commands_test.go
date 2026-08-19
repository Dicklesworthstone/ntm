package startup

import "testing"

func TestCommandRequirements(t *testing.T) {
	tests := []struct {
		cmd      string
		expected CommandRequirement
	}{
		// Phase 1 only commands
		{"version", RequirePhase1Only},
		{"help", RequirePhase1Only},
		{"init", RequirePhase1Only},
		{"completion", RequirePhase1Only},
		{"upgrade", RequirePhase1Only},

		// Config-only commands
		{"config", RequireConfig},
		{"bind", RequireConfig},
		{"recipes", RequireConfig},
		{"personas", RequireConfig},

		// Full startup commands
		{"spawn", RequireFullStartup},
		{"status", RequireFullStartup},
		{"dashboard", RequireFullStartup},
		{"palette", RequireFullStartup},

		// Unknown command defaults to full startup
		{"unknown_command", RequireFullStartup},
	}

	for _, tt := range tests {
		t.Run(tt.cmd, func(t *testing.T) {
			got := GetCommandRequirement(tt.cmd)
			if got != tt.expected {
				t.Errorf("GetCommandRequirement(%q) = %v, want %v", tt.cmd, got, tt.expected)
			}
		})
	}
}

func TestNeedsConfig(t *testing.T) {
	tests := []struct {
		cmd      string
		expected bool
	}{
		{"version", false},
		{"help", false},
		{"config", true},
		{"spawn", true},
		{"dashboard", true},
	}

	for _, tt := range tests {
		t.Run(tt.cmd, func(t *testing.T) {
			got := NeedsConfig(tt.cmd)
			if got != tt.expected {
				t.Errorf("NeedsConfig(%q) = %v, want %v", tt.cmd, got, tt.expected)
			}
		})
	}
}

func TestCanSkipConfig(t *testing.T) {
	tests := []struct {
		cmd      string
		expected bool
	}{
		{"version", true},
		{"help", true},
		{"init", true},
		{"config", false},
		{"spawn", false},
	}

	for _, tt := range tests {
		t.Run(tt.cmd, func(t *testing.T) {
			got := CanSkipConfig(tt.cmd)
			if got != tt.expected {
				t.Errorf("CanSkipConfig(%q) = %v, want %v", tt.cmd, got, tt.expected)
			}
		})
	}
}
