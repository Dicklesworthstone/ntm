//go:build e2e
// +build e2e

// Package e2e contains end-to-end tests for NTM robot mode commands.
package e2e

import (
	"os"
	"os/exec"
	"testing"

	"github.com/Dicklesworthstone/ntm/internal/tmux"
)

// SkipIfShort skips the test in short mode.
func SkipIfShort(t *testing.T) {
	if testing.Short() {
		t.Skip("Skipping E2E test in short mode")
	}
}

// SkipIfNoTmux skips the test if tmux is not available.
func SkipIfNoTmux(t *testing.T) {
	if !tmux.DefaultClient.IsInstalled() {
		t.Skip("tmux not found, skipping E2E test")
	}
}

// SkipIfNoNTM skips the test if ntm is not available.
func SkipIfNoNTM(t *testing.T) {
	if _, err := exec.LookPath("ntm"); err != nil {
		t.Skip("ntm not found, skipping E2E test")
	}
}

// SkipIfNoAgent skips the test if the specified agent CLI is not available.
func SkipIfNoAgent(t *testing.T, agentType string) {
	executable, ok := agentExecutable(agentType)
	if !ok {
		t.Fatalf("Unknown agent type: %s", agentType)
	}

	if _, err := exec.LookPath(executable); err != nil {
		t.Skipf("%s not found, skipping E2E test", executable)
	}
}

// agentExecutable maps NTM's agent-type aliases to the actual CLI command
// launched by the default configuration. In particular, "cc" is a common C
// compiler command and must not be mistaken for Claude Code in real-agent E2E
// tests.
func agentExecutable(agentType string) (string, bool) {
	switch agentType {
	case "cc", "claude":
		return "claude", true
	case "cod", "codex":
		return "codex", true
	case "gmi", "gemini":
		return "gemini", true
	default:
		return "", false
	}
}

// SkipIfNoAgents skips if none of the common agents are available.
func SkipIfNoAgents(t *testing.T) {
	if GetAvailableAgent() == "" {
		t.Skip("No agent CLIs (claude, codex, gemini) found, skipping E2E test")
	}
}

// GetAvailableAgent returns the first available agent type.
func GetAvailableAgent() string {
	for _, agentType := range []string{"cod", "cc", "gmi"} {
		executable, _ := agentExecutable(agentType)
		if _, err := exec.LookPath(executable); err == nil {
			return agentType
		}
	}
	return ""
}

func TestAgentExecutable(t *testing.T) {
	tests := []struct {
		agentType string
		want      string
		ok        bool
	}{
		{agentType: "cc", want: "claude", ok: true},
		{agentType: "claude", want: "claude", ok: true},
		{agentType: "cod", want: "codex", ok: true},
		{agentType: "codex", want: "codex", ok: true},
		{agentType: "gmi", want: "gemini", ok: true},
		{agentType: "gemini", want: "gemini", ok: true},
		{agentType: "unknown", want: "", ok: false},
	}

	for _, test := range tests {
		t.Run(test.agentType, func(t *testing.T) {
			got, ok := agentExecutable(test.agentType)
			if got != test.want || ok != test.ok {
				t.Fatalf("agentExecutable(%q) = (%q, %t), want (%q, %t)", test.agentType, got, ok, test.want, test.ok)
			}
		})
	}
}

// IsMockMode returns true if running in mock mode (for CI).
func IsMockMode() bool {
	return os.Getenv("E2E_MOCK_MODE") == "1"
}

// GetMockFile returns the path to the mock caut response file.
func GetMockFile() string {
	return os.Getenv("CAUT_MOCK_FILE")
}

// HasCautInstalled returns true if caut is available.
func HasCautInstalled() bool {
	_, err := exec.LookPath("caut")
	return err == nil
}

// CommonE2EPrerequisites checks all common prerequisites for E2E tests.
func CommonE2EPrerequisites(t *testing.T) {
	SkipIfShort(t)
	SkipIfNoTmux(t)
	SkipIfNoNTM(t)
}
