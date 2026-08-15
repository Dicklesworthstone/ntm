package config

import "testing"

// bd-3j6hm: [memory] send-injection keys for per-task rule injection on sends.

func TestDefaultMemoryConfig_SendInjectionKeys(t *testing.T) {
	cfg := DefaultMemoryConfig()
	if cfg.SendInjection {
		t.Error("send_injection should default to false (opt-in via --with-memory)")
	}
	if cfg.SendMaxRules != 5 {
		t.Errorf("send_max_rules default = %d, want 5", cfg.SendMaxRules)
	}
	if cfg.SendBudgetTokens != 1500 {
		t.Errorf("send_budget_tokens default = %d, want 1500", cfg.SendBudgetTokens)
	}
}

func TestValidateMemoryConfig_SendKeys(t *testing.T) {
	cfg := DefaultMemoryConfig()
	if err := ValidateMemoryConfig(&cfg); err != nil {
		t.Fatalf("default memory config should validate: %v", err)
	}

	bad := DefaultMemoryConfig()
	bad.SendMaxRules = -1
	if err := ValidateMemoryConfig(&bad); err == nil {
		t.Error("negative send_max_rules should fail validation")
	}

	bad = DefaultMemoryConfig()
	bad.SendBudgetTokens = -1
	if err := ValidateMemoryConfig(&bad); err == nil {
		t.Error("negative send_budget_tokens should fail validation")
	}
}

func TestGetValue_MemorySendKeys(t *testing.T) {
	cfg := Default()
	cfg.Memory.SendInjection = true
	cfg.Memory.SendMaxRules = 3
	cfg.Memory.SendBudgetTokens = 800

	cases := []struct {
		key  string
		want any
	}{
		{"memory.send_injection", true},
		{"memory.send_max_rules", 3},
		{"memory.send_budget_tokens", 800},
	}
	for _, tc := range cases {
		got, err := GetValue(cfg, tc.key)
		if err != nil {
			t.Errorf("GetValue(%q) error: %v", tc.key, err)
			continue
		}
		if got != tc.want {
			t.Errorf("GetValue(%q) = %v, want %v", tc.key, got, tc.want)
		}
	}
}
