package assign

import (
	"reflect"
	"strings"
	"testing"

	"github.com/Dicklesworthstone/ntm/internal/tmux"
)

func TestMatcherAssignRankedTasksPreservesCallerOrderAndEligibility(t *testing.T) {
	t.Parallel()
	m := &Matcher{matrix: NewCapabilityMatrix(), config: DefaultMatcherConfig()}
	beads := []Bead{
		{ID: "ranked-first", Priority: 3, TaskType: TaskDocs},
		{ID: "later-higher-score", Priority: 0, TaskType: TaskRefactor},
	}
	agents := []Agent{
		{ID: "busy", AgentType: tmux.AgentGemini},
		{ID: "full", AgentType: tmux.AgentClaude, Idle: true, ContextUsage: .95},
		{ID: "available", AgentType: tmux.AgentClaude, Idle: true},
	}
	beforeBeads, beforeAgents := append([]Bead(nil), beads...), append([]Agent(nil), agents...)
	got := m.AssignRankedTasks(beads, agents)
	if len(got) != 1 || got[0].Bead.ID != "ranked-first" || got[0].Agent.ID != "available" || got[0].Score != .85 {
		t.Fatalf("ranked assignment reordered caller work or bypassed availability: %+v", got)
	}
	if !reflect.DeepEqual(beads, beforeBeads) || !reflect.DeepEqual(agents, beforeAgents) {
		t.Fatal("ranked assignment mutated caller inputs")
	}
	// Preserving external ranking does not relax the normal suitability gate.
	m.matrix.base[tmux.AgentClaude][TaskDocs] = .1
	got = m.AssignRankedTasks(beads, agents)
	if len(got) != 1 || got[0].Bead.ID != "later-higher-score" || got[0].Agent.ID != "available" {
		t.Fatalf("ranked assignment admitted an unsuitable pair: %+v", got)
	}
	if got := m.AssignRankedTasks(beads, nil); len(got) != 0 {
		t.Fatalf("ranked assignment invented worker capacity: %+v", got)
	}
}

func TestMatcherPriorityBatchKeepsSpecialistCapacity(t *testing.T) {
	for _, strategy := range []Strategy{StrategyQuality, StrategyDependency} {
		t.Run(string(strategy), func(t *testing.T) {
			m := &Matcher{matrix: NewCapabilityMatrix(), config: DefaultMatcherConfig()}
			m.matrix.base[tmux.AgentClaude][TaskFeature] = .95
			m.matrix.base[tmux.AgentClaude][TaskRefactor] = .9
			m.matrix.base[tmux.AgentCodex][TaskFeature] = .8
			m.matrix.base[tmux.AgentCodex][TaskRefactor] = .1
			beads := []Bead{
				{ID: "flexible", Priority: 2, TaskType: TaskFeature},
				{ID: "specialist-only", Priority: 3, TaskType: TaskRefactor},
			}
			agents := []Agent{
				{ID: "specialist", AgentType: tmux.AgentClaude, Idle: true},
				{ID: "generalist", AgentType: tmux.AgentCodex, Idle: true},
			}
			beforeBeads, beforeAgents := append([]Bead(nil), beads...), append([]Agent(nil), agents...)
			got := m.AssignTasks(beads, agents, strategy)
			if len(got) != 2 {
				t.Fatalf("got %d assignments, want 2: %+v", len(got), got)
			}
			if got[0].Bead.ID != "flexible" || got[0].Agent.ID != "generalist" || got[1].Agent.ID != "specialist" {
				t.Fatalf("did not repair the greedy choice: %+v", got)
			}
			if !reflect.DeepEqual(beads, beforeBeads) || !reflect.DeepEqual(agents, beforeAgents) {
				t.Fatal("mutated caller inputs")
			}
		})
	}
}

func TestMatcherPriorityBatchOptimizesAcrossAgents(t *testing.T) {
	m := &Matcher{matrix: NewCapabilityMatrix(), config: DefaultMatcherConfig()}
	m.matrix.base[tmux.AgentClaude][TaskFeature] = .9
	m.matrix.base[tmux.AgentClaude][TaskRefactor] = .85
	m.matrix.base[tmux.AgentCodex][TaskFeature] = .8
	m.matrix.base[tmux.AgentCodex][TaskRefactor] = .3
	got := m.AssignTasks([]Bead{
		{ID: "one", Priority: 2, TaskType: TaskFeature},
		{ID: "two", Priority: 2, TaskType: TaskRefactor},
	}, []Agent{
		{ID: "a", AgentType: tmux.AgentClaude, Idle: true},
		{ID: "b", AgentType: tmux.AgentCodex, Idle: true},
	}, StrategyQuality)
	if len(got) != 2 || got[0].Agent.ID != "b" || got[1].Agent.ID != "a" {
		t.Fatalf("want total score 1.65, not greedy 1.2: %+v", got)
	}
}

func TestMatcherPriorityBatchPreservesPriorityAndDependencyOrder(t *testing.T) {
	m := &Matcher{matrix: NewCapabilityMatrix(), config: DefaultMatcherConfig()}
	beads := []Bead{
		{ID: "low", Priority: 4, TaskType: TaskFeature},
		{ID: "ordinary", Priority: 2, TaskType: TaskFeature},
		{ID: "blocker", Priority: 2, TaskType: TaskFeature, UnblocksIDs: []string{"x", "y"}},
		{ID: "critical", Priority: 0, TaskType: TaskRefactor},
	}
	agents := []Agent{
		{ID: "a", AgentType: tmux.AgentClaude, Idle: true},
		{ID: "b", AgentType: tmux.AgentCodex, Idle: true},
	}
	quality := m.AssignTasks(beads, agents, StrategyQuality)
	dependency := m.AssignTasks(beads, agents, StrategyDependency)
	if len(quality) != 2 || quality[0].Bead.ID != "critical" || quality[1].Bead.ID != "ordinary" {
		t.Fatalf("quality discarded priority or input tie order: %+v", quality)
	}
	if len(dependency) != 2 || dependency[0].Bead.ID != "critical" || dependency[1].Bead.ID != "blocker" {
		t.Fatalf("dependency discarded priority or unblock order: %+v", dependency)
	}
	if !strings.Contains(dependency[1].Reason, "unblocks 2 items") {
		t.Fatalf("lost dependency explanation: %s", dependency[1].Reason)
	}
}

func TestMatcherPriorityBatchDeduplicatesIdentities(t *testing.T) {
	m := &Matcher{matrix: NewCapabilityMatrix(), config: DefaultMatcherConfig()}
	bead := Bead{ID: "one", TaskType: TaskFeature}
	agent := Agent{ID: "a", AgentType: tmux.AgentClaude, Idle: true}
	for _, strategy := range []Strategy{StrategyQuality, StrategyDependency} {
		got := m.AssignTasks([]Bead{bead, bead}, []Agent{agent, {ID: "b", AgentType: tmux.AgentCodex, Idle: true}}, strategy)
		if len(got) != 1 {
			t.Fatalf("%s assigned a duplicate bead: %+v", strategy, got)
		}
		got = m.AssignTasks([]Bead{bead, {ID: "two", TaskType: TaskFeature}}, []Agent{agent, agent}, strategy)
		if len(got) != 1 {
			t.Fatalf("%s reused a worker identity: %+v", strategy, got)
		}
	}
}

func TestMatcherPriorityBatchKeepsConfidenceAndAvailabilityGates(t *testing.T) {
	m := &Matcher{matrix: NewCapabilityMatrix(), config: DefaultMatcherConfig()}
	beads := []Bead{{ID: "one", Priority: 2, TaskType: TaskFeature}, {ID: "two", Priority: 2, TaskType: TaskFeature}}
	agents := []Agent{
		{ID: "busy", AgentType: tmux.AgentCodex},
		{ID: "full", AgentType: tmux.AgentCodex, Idle: true, ContextUsage: .95},
		{ID: "weak", AgentType: tmux.AgentCodex, Idle: true, ContextUsage: .8},
		{ID: "eligible", AgentType: tmux.AgentCodex, Idle: true, ContextUsage: .5},
	}
	for _, strategy := range []Strategy{StrategyQuality, StrategyDependency} {
		got := m.AssignTasks(beads, agents, strategy)
		if len(got) != 1 || got[0].Agent.ID != "eligible" || got[0].Score < m.config.MinConfidence {
			t.Fatalf("%s relaxed eligibility to fill the batch: %+v", strategy, got)
		}
	}
}
