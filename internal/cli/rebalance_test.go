package cli

import (
	"testing"

	"github.com/Dicklesworthstone/ntm/internal/assignment"
	"github.com/Dicklesworthstone/ntm/internal/tmux"
)

func TestCalculateImbalanceScore(t *testing.T) {
	tests := []struct {
		name      string
		workloads []RebalanceWorkload
		want      float64
		tolerance float64
	}{
		{
			name:      "empty workloads",
			workloads: []RebalanceWorkload{},
			want:      0,
			tolerance: 0.001,
		},
		{
			name: "single workload",
			workloads: []RebalanceWorkload{
				{Pane: 1, TaskCount: 5},
			},
			want:      0, // stddev/mean = 0/5 = 0
			tolerance: 0.001,
		},
		{
			name: "perfectly balanced",
			workloads: []RebalanceWorkload{
				{Pane: 1, TaskCount: 3},
				{Pane: 2, TaskCount: 3},
				{Pane: 3, TaskCount: 3},
			},
			want:      0,
			tolerance: 0.001,
		},
		{
			name: "moderate imbalance",
			workloads: []RebalanceWorkload{
				{Pane: 1, TaskCount: 4},
				{Pane: 2, TaskCount: 2},
				{Pane: 3, TaskCount: 3},
			},
			// mean = 3, variance = ((4-3)^2 + (2-3)^2 + (3-3)^2)/3 = 2/3
			// stddev = sqrt(2/3) ≈ 0.816
			// CV = 0.816/3 ≈ 0.272
			want:      0.272,
			tolerance: 0.01,
		},
		{
			name: "severe imbalance",
			workloads: []RebalanceWorkload{
				{Pane: 1, TaskCount: 10},
				{Pane: 2, TaskCount: 0},
				{Pane: 3, TaskCount: 0},
			},
			// mean = 10/3 ≈ 3.33
			// variance = ((10-3.33)^2 + (0-3.33)^2 + (0-3.33)^2)/3 ≈ 22.22
			// stddev ≈ 4.71
			// CV ≈ 4.71/3.33 ≈ 1.414
			want:      1.414,
			tolerance: 0.01,
		},
		{
			name: "all zero tasks",
			workloads: []RebalanceWorkload{
				{Pane: 1, TaskCount: 0},
				{Pane: 2, TaskCount: 0},
			},
			want:      0, // No tasks = balanced
			tolerance: 0.001,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := calculateImbalanceScore(tt.workloads)
			diff := got - tt.want
			if diff < 0 {
				diff = -diff
			}
			if diff > tt.tolerance {
				t.Errorf("calculateImbalanceScore() = %v, want %v (tolerance %v)", got, tt.want, tt.tolerance)
			}
		})
	}
}

func TestGetRecommendation(t *testing.T) {
	tests := []struct {
		name  string
		score float64
		want  string
	}{
		{name: "zero score", score: 0.0, want: "balanced"},
		{name: "low score", score: 0.2, want: "balanced"},
		{name: "at threshold 0.3", score: 0.3, want: "moderate_imbalance"},
		{name: "moderate score", score: 0.5, want: "moderate_imbalance"},
		{name: "at threshold 0.7", score: 0.7, want: "rebalance_recommended"},
		{name: "high score", score: 1.0, want: "rebalance_recommended"},
		{name: "very high score", score: 2.0, want: "rebalance_recommended"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := getRecommendation(tt.score)
			if got != tt.want {
				t.Errorf("getRecommendation(%v) = %v, want %v", tt.score, got, tt.want)
			}
		})
	}
}

func TestMatchesRebalanceFilter(t *testing.T) {
	tests := []struct {
		name      string
		agentType string
		filter    string
		want      bool
	}{
		{name: "no filter", agentType: "claude", filter: "", want: true},
		{name: "cc matches claude", agentType: "claude", filter: "cc", want: true},
		{name: "claude matches claude", agentType: "claude", filter: "claude", want: true},
		{name: "claude alias matches", agentType: "claude", filter: "claude_code", want: true},
		{name: "cc prefix matches", agentType: "cc_1", filter: "cc", want: true},
		{name: "cod matches codex", agentType: "codex", filter: "cod", want: true},
		{name: "codex matches codex", agentType: "codex", filter: "codex", want: true},
		{name: "codex alias matches", agentType: "cod_2", filter: "openai-codex", want: true},
		{name: "cod prefix matches", agentType: "cod_2", filter: "cod", want: true},
		{name: "gmi matches gemini", agentType: "gemini", filter: "gmi", want: true},
		{name: "gemini matches gemini", agentType: "gemini", filter: "gemini", want: true},
		{name: "gemini alias matches", agentType: "gmi_3", filter: "google_gemini", want: true},
		{name: "gmi prefix matches", agentType: "gmi_3", filter: "gmi", want: true},
		{name: "windsurf alias matches", agentType: "windsurf", filter: "ws", want: true},
		{name: "claude does not match cod", agentType: "claude", filter: "cod", want: false},
		{name: "codex does not match cc", agentType: "codex", filter: "cc", want: false},
		{name: "unknown type with filter returns false", agentType: "unknown", filter: "cc", want: false},
		{name: "invalid filter returns false", agentType: "claude", filter: "not-an-agent", want: false},
		{name: "case insensitive filter", agentType: "Claude", filter: "CC", want: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := matchesRebalanceFilter(tt.agentType, tt.filter)
			if got != tt.want {
				t.Errorf("matchesRebalanceFilter(%q, %q) = %v, want %v", tt.agentType, tt.filter, got, tt.want)
			}
		})
	}
}

func TestNormalizeAgentTypeFilter(t *testing.T) {
	tests := []struct {
		name    string
		filter  string
		want    string
		wantErr bool
	}{
		{name: "empty", filter: "", want: ""},
		{name: "claude alias", filter: " claude_code ", want: "cc"},
		{name: "codex alias", filter: "openai-codex", want: "cod"},
		{name: "gemini alias", filter: "google-gemini", want: "gmi"},
		{name: "windsurf alias", filter: "ws", want: "windsurf"},
		{name: "invalid", filter: "not-an-agent", wantErr: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := normalizeAgentTypeFilter(tt.filter)
			if (err != nil) != tt.wantErr {
				t.Fatalf("normalizeAgentTypeFilter(%q) error = %v, wantErr %v", tt.filter, err, tt.wantErr)
			}
			if got != tt.want {
				t.Errorf("normalizeAgentTypeFilter(%q) = %q, want %q", tt.filter, got, tt.want)
			}
		})
	}
}

func TestRebalanceWorkloadCounts(t *testing.T) {
	workloads := []RebalanceWorkload{
		{Pane: 1, TaskCount: 5},
		{Pane: 2, TaskCount: 3},
		{Pane: 3, TaskCount: 0},
	}

	counts := rebalanceWorkloadCounts(workloads)

	if len(counts) != 3 {
		t.Errorf("expected 3 counts, got %d", len(counts))
	}
	if counts[1] != 5 {
		t.Errorf("expected pane 1 count = 5, got %d", counts[1])
	}
	if counts[2] != 3 {
		t.Errorf("expected pane 2 count = 3, got %d", counts[2])
	}
	if counts[3] != 0 {
		t.Errorf("expected pane 3 count = 0, got %d", counts[3])
	}
}

func TestBuildRebalanceWorkloads_UsesParsedPaneType(t *testing.T) {
	store := assignment.NewStore("test-session")
	panes := []tmux.Pane{
		{Index: 1, Type: tmux.AgentClaude, Title: "notes", Active: true},
		{Index: 2, Type: tmux.AgentUser, Title: "__cc_2", Active: true},
		{Index: 3, Type: tmux.AgentType("openai-codex"), Title: "custom", Active: true},
	}

	workloads := buildRebalanceWorkloads(store, panes, "cod")
	if len(workloads) != 1 {
		t.Fatalf("expected 1 workload matching cod, got %d", len(workloads))
	}
	if workloads[0].Pane != 3 || workloads[0].AgentType != "codex" {
		t.Fatalf("workload = %+v, want pane 3 codex", workloads[0])
	}
}

func TestCalculateAfterState(t *testing.T) {
	workloads := []RebalanceWorkload{
		{Pane: 1, TaskCount: 5},
		{Pane: 2, TaskCount: 0},
	}

	transfers := []RebalanceTransfer{
		{FromPane: 1, ToPane: 2},
		{FromPane: 1, ToPane: 2},
	}

	after := calculateAfterState(workloads, transfers)

	if after[1] != 3 {
		t.Errorf("expected pane 1 after = 3, got %d", after[1])
	}
	if after[2] != 2 {
		t.Errorf("expected pane 2 after = 2, got %d", after[2])
	}
}

func TestCalculateAfterStateEmpty(t *testing.T) {
	workloads := []RebalanceWorkload{
		{Pane: 1, TaskCount: 3},
	}

	transfers := []RebalanceTransfer{}

	after := calculateAfterState(workloads, transfers)

	if after[1] != 3 {
		t.Errorf("expected pane 1 after = 3 (unchanged), got %d", after[1])
	}
}

func TestSuggestTransfersDistributesAcrossTargets(t *testing.T) {
	workloads := []RebalanceWorkload{
		{Pane: 1, AgentType: "claude", TaskCount: 5},
		{Pane: 2, AgentType: "codex", TaskCount: 0, IsHealthy: true, IsIdle: true},
		{Pane: 3, AgentType: "gemini", TaskCount: 1, IsHealthy: true},
	}

	store := assignmentStoreWith(
		makeAssignment("bd-1", "bead 1", 1, assignment.StatusAssigned),
		makeAssignment("bd-2", "bead 2", 1, assignment.StatusAssigned),
		makeAssignment("bd-3", "bead 3", 1, assignment.StatusAssigned),
		makeAssignment("bd-4", "bead 4", 1, assignment.StatusAssigned),
		makeAssignment("bd-5", "bead 5", 1, assignment.StatusAssigned),
	)

	transfers := suggestTransfers(workloads, store)

	if len(transfers) != 3 {
		t.Fatalf("expected 3 transfers, got %d", len(transfers))
	}

	seenTargets := make(map[int]bool)
	for _, t := range transfers {
		seenTargets[t.ToPane] = true
	}

	if len(seenTargets) < 2 {
		t.Fatalf("expected transfers to multiple targets, got %v", seenTargets)
	}
}

func TestSuggestTransfersRespectsAssignedStatus(t *testing.T) {
	workloads := []RebalanceWorkload{
		{Pane: 1, AgentType: "claude", TaskCount: 2},
		{Pane: 2, AgentType: "codex", TaskCount: 0, IsHealthy: true, IsIdle: true},
	}

	store := assignmentStoreWith(
		makeAssignment("bd-assigned", "assigned bead", 1, assignment.StatusAssigned),
		makeAssignment("bd-working", "working bead", 1, assignment.StatusWorking),
	)

	transfers := suggestTransfers(workloads, store)

	if len(transfers) != 1 {
		t.Fatalf("expected 1 transfer, got %d", len(transfers))
	}
	if transfers[0].BeadID != "bd-assigned" {
		t.Fatalf("expected assigned bead transfer, got %s", transfers[0].BeadID)
	}
}

func TestSuggestTransfersReasonSameType(t *testing.T) {
	workloads := []RebalanceWorkload{
		{Pane: 1, AgentType: "claude", TaskCount: 3},
		{Pane: 2, AgentType: "claude", TaskCount: 0, IsHealthy: true, IsIdle: true},
	}

	store := assignmentStoreWith(
		makeAssignment("bd-1", "bead 1", 1, assignment.StatusAssigned),
		makeAssignment("bd-2", "bead 2", 1, assignment.StatusAssigned),
		makeAssignment("bd-3", "bead 3", 1, assignment.StatusAssigned),
	)

	transfers := suggestTransfers(workloads, store)
	if len(transfers) == 0 {
		t.Fatalf("expected transfers, got none")
	}
	for _, tr := range transfers {
		if tr.Reason != "same_type_balance" {
			t.Fatalf("expected reason same_type_balance, got %q", tr.Reason)
		}
	}
}

func TestSuggestTransfersReasonTargetIdle(t *testing.T) {
	workloads := []RebalanceWorkload{
		{Pane: 1, AgentType: "claude", TaskCount: 2},
		{Pane: 2, AgentType: "codex", TaskCount: 0, IsHealthy: true, IsIdle: true},
	}

	store := assignmentStoreWith(
		makeAssignment("bd-1", "bead 1", 1, assignment.StatusAssigned),
		makeAssignment("bd-2", "bead 2", 1, assignment.StatusAssigned),
	)

	transfers := suggestTransfers(workloads, store)
	if len(transfers) != 1 {
		t.Fatalf("expected 1 transfer, got %d", len(transfers))
	}
	if transfers[0].Reason != "target_idle" {
		t.Fatalf("expected reason target_idle, got %q", transfers[0].Reason)
	}
}

func assignmentStoreWith(assignments ...*assignment.Assignment) *assignment.AssignmentStore {
	store := &assignment.AssignmentStore{
		Assignments: make(map[string]*assignment.Assignment),
	}
	for _, a := range assignments {
		store.Assignments[a.BeadID] = a
	}
	return store
}

func makeAssignment(beadID, title string, pane int, status assignment.AssignmentStatus) *assignment.Assignment {
	return &assignment.Assignment{
		BeadID:    beadID,
		BeadTitle: title,
		Pane:      pane,
		AgentType: "claude",
		Status:    status,
	}
}
