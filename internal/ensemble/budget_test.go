package ensemble

import (
	"testing"
)

func TestNewBudgetTracker(t *testing.T) {
	t.Run("with custom config", func(t *testing.T) {
		cfg := BudgetConfig{
			MaxTokensPerMode: 5000,
			MaxTotalTokens:   25000,
		}
		bt := NewBudgetTracker(cfg, nil)

		if bt.config.MaxTokensPerMode != 5000 {
			t.Errorf("MaxTokensPerMode = %d, want 5000", bt.config.MaxTokensPerMode)
		}
		if bt.config.MaxTotalTokens != 25000 {
			t.Errorf("MaxTotalTokens = %d, want 25000", bt.config.MaxTotalTokens)
		}
	})

	t.Run("with zero config uses defaults", func(t *testing.T) {
		bt := NewBudgetTracker(BudgetConfig{}, nil)

		defaults := DefaultBudgetConfig()
		if bt.config.MaxTokensPerMode != defaults.MaxTokensPerMode {
			t.Errorf("MaxTokensPerMode = %d, want %d", bt.config.MaxTokensPerMode, defaults.MaxTokensPerMode)
		}
		if bt.config.MaxTotalTokens != defaults.MaxTotalTokens {
			t.Errorf("MaxTotalTokens = %d, want %d", bt.config.MaxTotalTokens, defaults.MaxTotalTokens)
		}
	})
}

func TestBudgetTracker_RecordSpend(t *testing.T) {
	t.Run("records spend correctly", func(t *testing.T) {
		bt := NewBudgetTracker(BudgetConfig{
			MaxTokensPerMode: 1000,
			MaxTotalTokens:   5000,
		}, nil)

		result := bt.RecordSpend("agent1", 500)

		if !result.Allowed {
			t.Error("expected spend to be allowed")
		}
		if result.Remaining != 500 {
			t.Errorf("Remaining = %d, want 500", result.Remaining)
		}
		if result.TotalRemaining != 4500 {
			t.Errorf("TotalRemaining = %d, want 4500", result.TotalRemaining)
		}
	})

	t.Run("tracks multiple agents", func(t *testing.T) {
		bt := NewBudgetTracker(BudgetConfig{
			MaxTokensPerMode: 1000,
			MaxTotalTokens:   5000,
		}, nil)

		bt.RecordSpend("agent1", 300)
		bt.RecordSpend("agent2", 400)
		bt.RecordSpend("agent1", 200)

		state := bt.GetState()
		if state.PerAgentSpent["agent1"] != 500 {
			t.Errorf("agent1 spent = %d, want 500", state.PerAgentSpent["agent1"])
		}
		if state.PerAgentSpent["agent2"] != 400 {
			t.Errorf("agent2 spent = %d, want 400", state.PerAgentSpent["agent2"])
		}
		if state.TotalSpent != 900 {
			t.Errorf("TotalSpent = %d, want 900", state.TotalSpent)
		}
	})

	t.Run("detects agent over budget", func(t *testing.T) {
		bt := NewBudgetTracker(BudgetConfig{
			MaxTokensPerMode: 1000,
			MaxTotalTokens:   50000,
		}, nil)

		bt.RecordSpend("agent1", 800)
		result := bt.RecordSpend("agent1", 300)

		if result.Allowed {
			t.Error("expected spend to be disallowed when agent over budget")
		}
		if result.Message != "agent budget exceeded" {
			t.Errorf("Message = %q, want 'agent budget exceeded'", result.Message)
		}
	})

	t.Run("detects total over budget", func(t *testing.T) {
		bt := NewBudgetTracker(BudgetConfig{
			MaxTokensPerMode: 10000,
			MaxTotalTokens:   2000,
		}, nil)

		bt.RecordSpend("agent1", 1500)
		result := bt.RecordSpend("agent2", 600)

		if result.Allowed {
			t.Error("expected spend to be disallowed when total over budget")
		}
		if result.Message != "total budget exceeded" {
			t.Errorf("Message = %q, want 'total budget exceeded'", result.Message)
		}
	})
}

func TestBudgetTracker_RemainingForAgent(t *testing.T) {
	bt := NewBudgetTracker(BudgetConfig{
		MaxTokensPerMode: 1000,
		MaxTotalTokens:   5000,
	}, nil)

	bt.RecordSpend("agent1", 300)

	remaining := bt.RemainingForAgent("agent1")
	if remaining != 700 {
		t.Errorf("RemainingForAgent = %d, want 700", remaining)
	}

	// Unknown agent should have full budget
	unknown := bt.RemainingForAgent("unknown")
	if unknown != 1000 {
		t.Errorf("RemainingForAgent(unknown) = %d, want 1000", unknown)
	}
}

func TestBudgetTracker_TotalRemaining(t *testing.T) {
	bt := NewBudgetTracker(BudgetConfig{
		MaxTokensPerMode: 1000,
		MaxTotalTokens:   5000,
	}, nil)

	bt.RecordSpend("agent1", 1000)
	bt.RecordSpend("agent2", 1500)

	remaining := bt.TotalRemaining()
	if remaining != 2500 {
		t.Errorf("TotalRemaining = %d, want 2500", remaining)
	}
}

func TestBudgetTracker_IsOverBudget(t *testing.T) {
	t.Run("not over budget", func(t *testing.T) {
		bt := NewBudgetTracker(BudgetConfig{
			MaxTokensPerMode: 1000,
			MaxTotalTokens:   5000,
		}, nil)

		bt.RecordSpend("agent1", 2000)

		if bt.IsOverBudget() {
			t.Error("expected not to be over budget")
		}
	})

	t.Run("over budget", func(t *testing.T) {
		bt := NewBudgetTracker(BudgetConfig{
			MaxTokensPerMode: 10000,
			MaxTotalTokens:   1000,
		}, nil)

		bt.RecordSpend("agent1", 1100)

		if !bt.IsOverBudget() {
			t.Error("expected to be over budget")
		}
	})
}

func TestBudgetTracker_IsAgentOverBudget(t *testing.T) {
	bt := NewBudgetTracker(BudgetConfig{
		MaxTokensPerMode: 1000,
		MaxTotalTokens:   50000,
	}, nil)

	bt.RecordSpend("agent1", 500)
	bt.RecordSpend("agent2", 1100)

	if bt.IsAgentOverBudget("agent1") {
		t.Error("agent1 should not be over budget")
	}
	if !bt.IsAgentOverBudget("agent2") {
		t.Error("agent2 should be over budget")
	}
}

func TestBudgetTracker_GetState(t *testing.T) {
	bt := NewBudgetTracker(BudgetConfig{
		MaxTokensPerMode: 1000,
		MaxTotalTokens:   5000,
	}, nil)

	bt.RecordSpend("agent1", 500)
	bt.RecordSpend("agent2", 1100) // Over per-agent limit

	state := bt.GetState()

	if state.TotalSpent != 1600 {
		t.Errorf("TotalSpent = %d, want 1600", state.TotalSpent)
	}
	if state.TotalRemaining != 3400 {
		t.Errorf("TotalRemaining = %d, want 3400", state.TotalRemaining)
	}
	if state.TotalLimit != 5000 {
		t.Errorf("TotalLimit = %d, want 5000", state.TotalLimit)
	}
	if state.PerAgentLimit != 1000 {
		t.Errorf("PerAgentLimit = %d, want 1000", state.PerAgentLimit)
	}
	if state.PerAgentSpent["agent1"] != 500 {
		t.Errorf("PerAgentSpent[agent1] = %d, want 500", state.PerAgentSpent["agent1"])
	}
	if state.PerAgentRemaining["agent1"] != 500 {
		t.Errorf("PerAgentRemaining[agent1] = %d, want 500", state.PerAgentRemaining["agent1"])
	}
	if len(state.OverBudgetAgents) != 1 || state.OverBudgetAgents[0] != "agent2" {
		t.Errorf("OverBudgetAgents = %v, want [agent2]", state.OverBudgetAgents)
	}
}

func TestBudgetTracker_Reset(t *testing.T) {
	bt := NewBudgetTracker(BudgetConfig{
		MaxTokensPerMode: 1000,
		MaxTotalTokens:   5000,
	}, nil)

	bt.RecordSpend("agent1", 500)
	bt.RecordSpend("agent2", 600)

	bt.Reset()

	state := bt.GetState()
	if state.TotalSpent != 0 {
		t.Errorf("TotalSpent after reset = %d, want 0", state.TotalSpent)
	}
	if len(state.PerAgentSpent) != 0 {
		t.Errorf("PerAgentSpent after reset = %v, want empty", state.PerAgentSpent)
	}
}

func TestBudgetTracker_Config(t *testing.T) {
	cfg := BudgetConfig{
		MaxTokensPerMode: 5000,
		MaxTotalTokens:   25000,
	}
	bt := NewBudgetTracker(cfg, nil)

	returned := bt.Config()
	if returned.MaxTokensPerMode != 5000 {
		t.Errorf("Config().MaxTokensPerMode = %d, want 5000", returned.MaxTokensPerMode)
	}
	if returned.MaxTotalTokens != 25000 {
		t.Errorf("Config().MaxTotalTokens = %d, want 25000", returned.MaxTotalTokens)
	}
}
