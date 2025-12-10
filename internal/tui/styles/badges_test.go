package styles

import (
	"fmt"
	"strings"
	"testing"
)

func TestAgentBadge(t *testing.T) {
	tests := []struct {
		agentType string
		wantLabel string
	}{
		{"claude", "claude"},
		{"cc", "claude"},
		{"codex", "codex"},
		{"cod", "codex"},
		{"gemini", "gemini"},
		{"gmi", "gemini"},
		{"user", "user"},
		{"unknown", "unknown"},
	}

	for _, tt := range tests {
		t.Run(tt.agentType, func(t *testing.T) {
			result := AgentBadge(tt.agentType)
			if result == "" {
				t.Error("AgentBadge returned empty string")
			}
			// Badge should contain the label
			if !strings.Contains(result, tt.wantLabel) {
				t.Errorf("AgentBadge(%q) should contain %q", tt.agentType, tt.wantLabel)
			}
		})
	}
}

func TestAgentBadgeWithCount(t *testing.T) {
	tests := []struct {
		agentType string
		count     int
	}{
		{"claude", 3},
		{"codex", 1},
		{"gemini", 5},
		{"user", 0},
	}

	for _, tt := range tests {
		t.Run(tt.agentType, func(t *testing.T) {
			result := AgentBadgeWithCount(tt.agentType, tt.count)
			if result == "" {
				t.Error("AgentBadgeWithCount returned empty string")
			}
		})
	}
}

func TestStatusBadge(t *testing.T) {
	statuses := []string{
		"success", "ok", "done",
		"running", "active",
		"idle", "waiting",
		"warning", "warn",
		"error", "failed",
		"pending",
		"disabled",
		"blocked",
		"unknown",
	}

	for _, status := range statuses {
		t.Run(status, func(t *testing.T) {
			result := StatusBadge(status)
			if result == "" {
				t.Errorf("StatusBadge(%q) returned empty string", status)
			}
		})
	}
}

func TestStatusBadgeIcon(t *testing.T) {
	statuses := []string{
		"success", "running", "idle", "warning", "error", "pending", "blocked", "unknown",
	}

	for _, status := range statuses {
		t.Run(status, func(t *testing.T) {
			result := StatusBadgeIcon(status)
			if result == "" {
				t.Errorf("StatusBadgeIcon(%q) returned empty string", status)
			}
		})
	}
}

func TestPriorityBadge(t *testing.T) {
	for priority := 0; priority <= 5; priority++ {
		name := fmt.Sprintf("P%d", priority)
		p := priority // capture for closure
		t.Run(name, func(t *testing.T) {
			result := PriorityBadge(p)
			if result == "" {
				t.Errorf("PriorityBadge(%d) returned empty string", p)
			}
			// Should contain P followed by number
			expected := fmt.Sprintf("P%d", p)
			if !strings.Contains(result, expected) {
				t.Errorf("PriorityBadge(%d) should contain %q", p, expected)
			}
		})
	}
}

func TestCountBadge(t *testing.T) {
	tests := []struct {
		count int
	}{
		{0},
		{1},
		{99},
		{999},
	}

	for _, tt := range tests {
		result := CountBadge(tt.count, "#89b4fa", "#1e1e2e")
		if result == "" {
			t.Errorf("CountBadge(%d) returned empty string", tt.count)
		}
	}
}

func TestHealthBadge(t *testing.T) {
	statuses := []string{
		"ok", "healthy", "warning", "drift", "critical", "no_baseline", "unavailable", "unknown",
	}

	for _, status := range statuses {
		t.Run(status, func(t *testing.T) {
			result := HealthBadge(status)
			if result == "" {
				t.Errorf("HealthBadge(%q) returned empty string", status)
			}
		})
	}
}

func TestIssueTypeBadge(t *testing.T) {
	types := []string{
		"epic", "feature", "task", "bug", "chore", "unknown",
	}

	for _, issueType := range types {
		t.Run(issueType, func(t *testing.T) {
			result := IssueTypeBadge(issueType)
			if result == "" {
				t.Errorf("IssueTypeBadge(%q) returned empty string", issueType)
			}
			// Should contain the type name
			if !strings.Contains(result, issueType) {
				t.Errorf("IssueTypeBadge(%q) should contain the type name", issueType)
			}
		})
	}
}

func TestBadgeOptions(t *testing.T) {
	// Test with different badge styles
	opts := []BadgeOptions{
		{Style: BadgeStyleDefault, Bold: true, ShowIcon: true},
		{Style: BadgeStyleCompact, Bold: false, ShowIcon: false},
		{Style: BadgeStylePill, Bold: true, ShowIcon: true},
	}

	for i, opt := range opts {
		result := AgentBadge("claude", opt)
		if result == "" {
			t.Errorf("AgentBadge with opts[%d] returned empty string", i)
		}
	}
}

func TestBadgeGroup(t *testing.T) {
	b1 := AgentBadge("claude")
	b2 := StatusBadge("running")
	b3 := PriorityBadge(1)

	result := BadgeGroup(b1, b2, b3)
	if result == "" {
		t.Error("BadgeGroup returned empty string")
	}
	// Should contain all three badges separated by space
	if !strings.Contains(result, " ") {
		t.Error("BadgeGroup should separate badges with spaces")
	}
}

func TestBadgeBar(t *testing.T) {
	b1 := AgentBadge("claude")
	b2 := StatusBadge("running")

	result := BadgeBar(b1, b2)
	if result == "" {
		t.Error("BadgeBar returned empty string")
	}
	// Should contain double space separator
	if !strings.Contains(result, "  ") {
		t.Error("BadgeBar should separate badges with double spaces")
	}
}

func TestTextBadge(t *testing.T) {
	result := TextBadge("custom", "#89b4fa", "#1e1e2e")
	if result == "" {
		t.Error("TextBadge returned empty string")
	}
	if !strings.Contains(result, "custom") {
		t.Error("TextBadge should contain the text")
	}
}
