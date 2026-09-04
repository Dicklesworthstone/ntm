package robot

import (
	"testing"

	"github.com/Dicklesworthstone/ntm/internal/tmux"
)

// #305: ACFS runs long-lived service panes (cm, cass, …) in tmux alongside
// agent panes. They are not agents. A healthy service whose terminal has been
// quiet for hours must never be counted as an agent, let alone as an agent in
// `error`, and it must not vanish silently either.
//
// The synthetic pane set below is the shape #305 reports: one tagged service
// pane, one real agent pane, and one untagged user pane.
func servicePaneFixture() []tmux.Pane {
	return []tmux.Pane{
		{
			ID: "%0", Index: 1, WindowIndex: 1, Title: "cm", Command: "cm",
			Type: tmux.AgentUser, Service: "cm", ServiceManager: "acfs",
		},
		{
			ID: "%1", Index: 2, WindowIndex: 1, Title: "swarm__cc_1",
			Command: "claude", Type: tmux.AgentClaude,
		},
		{
			ID: "%2", Index: 3, WindowIndex: 1, Title: "shell",
			Command: "zsh", Type: tmux.AgentUser,
		},
	}
}

func TestSnapshotPaneIndexSeparatesServicesFromAgents(t *testing.T) {
	t.Parallel()

	sessions := []tmux.Session{{Name: "acfs-svc", Panes: servicePaneFixture()}}

	labels, services := snapshotPaneIndex(sessions)

	// Every pane keeps its label: services are relocated, not erased.
	for _, key := range []string{"acfs-svc:%0", "acfs-svc:%1", "acfs-svc:%2"} {
		if _, ok := labels[key]; !ok {
			t.Fatalf("labels missing %q; every pane must keep an address", key)
		}
	}

	got := services["acfs-svc"]
	if len(got) != 1 {
		t.Fatalf("services = %+v, want exactly the one tagged pane", got)
	}
	if got[0].Name != "cm" || got[0].Manager != "acfs" {
		t.Fatalf("service = %+v, want name cm managed by acfs", got[0])
	}
	if got[0].Pane != "1.1" {
		t.Fatalf("service pane address = %q, want %q", got[0].Pane, "1.1")
	}
	if got[0].Command != "cm" {
		t.Fatalf("service command = %q, want %q", got[0].Command, "cm")
	}
}

func TestDashboardAgentTypeRejectsServicePanes(t *testing.T) {
	t.Parallel()

	panes := servicePaneFixture()

	if _, ok := dashboardAgentType(panes[0]); ok {
		t.Fatal("dashboardAgentType accepted a tagged service pane as an agent")
	}
	agentType, ok := dashboardAgentType(panes[1])
	if !ok || agentType != "claude" {
		t.Fatalf("dashboardAgentType(agent pane) = %q, %v; want claude, true", agentType, ok)
	}
	if _, ok := dashboardAgentType(panes[2]); ok {
		t.Fatal("dashboardAgentType accepted an untagged user pane as an agent")
	}
}

// A pane whose command looks like an agent CLI is still a service when it
// carries the tag: the tag is the durable identity, the command is a guess.
func TestDashboardAgentTypeRejectsAgentLookingServicePane(t *testing.T) {
	t.Parallel()

	pane := tmux.Pane{
		ID: "%7", Index: 0, Title: "watchdog", Command: "claude",
		Type: tmux.AgentClaude, Service: "watchdog", ServiceManager: "acfs",
	}
	if _, ok := dashboardAgentType(pane); ok {
		t.Fatal("dashboardAgentType accepted a tagged service pane whose command looks like an agent")
	}
}

// The whole point of #305: a session made only of service panes has no agents,
// so it must roll up as "no agents" rather than as a session full of failures.
func TestSessionHealthOfAServiceOnlySessionHasNoAgents(t *testing.T) {
	t.Parallel()

	adapter := NewTmuxAdapter(DefaultTmuxAdapterConfig())
	sess := &tmux.Session{
		Name: "acfs-svc",
		Panes: []tmux.Pane{
			{ID: "%0", Index: 1, Service: "cm", ServiceManager: "acfs"},
			{ID: "%1", Index: 2, Service: "cass", ServiceManager: "acfs"},
		},
	}

	runtime := adapter.NormalizeSession(sess, nil)

	if runtime.AgentCount != 0 {
		t.Fatalf("AgentCount = %d, want 0", runtime.AgentCount)
	}
	if runtime.ErrorAgents != 0 {
		t.Fatalf("ErrorAgents = %d, want 0", runtime.ErrorAgents)
	}
	if runtime.PaneCount != 2 {
		t.Fatalf("PaneCount = %d, want 2; service panes are still panes", runtime.PaneCount)
	}
	if runtime.HealthReason != "no agents" {
		t.Fatalf("HealthReason = %q, want %q", runtime.HealthReason, "no agents")
	}
}
