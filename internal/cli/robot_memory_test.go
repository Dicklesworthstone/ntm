package cli

import (
	"testing"
	"time"

	"github.com/Dicklesworthstone/ntm/internal/config"
)

func TestRobotSendMemoryOptions_FlagDrivenLikeCASS(t *testing.T) {
	// The per-call flag alone drives injection, mirroring SendOptions.WithCASS.
	enabled, inject := robotSendMemoryOptions(true, nil)
	if !enabled {
		t.Error("flag-enabled send should enable memory injection without config")
	}
	if inject != nil {
		t.Error("nil config should produce nil inject config (robot defaults apply)")
	}

	enabled, _ = robotSendMemoryOptions(false, nil)
	if enabled {
		t.Error("memory injection must stay off without the flag or config default")
	}
}

func TestRobotSendMemoryOptions_ConfigDefaultOn(t *testing.T) {
	cfg := config.Default()
	cfg.Memory.Enabled = true
	cfg.Memory.SendInjection = true

	enabled, inject := robotSendMemoryOptions(false, cfg)
	if !enabled {
		t.Error("[memory] send_injection=true should default robot sends to inject")
	}
	if inject == nil || !inject.Enabled {
		t.Error("inject config should carry memory.enabled=true")
	}
}

func TestRobotSendMemoryOptions_ConfigDefaultOffByDefault(t *testing.T) {
	cfg := config.Default()
	if cfg.Memory.SendInjection {
		t.Fatal("send_injection should default to false")
	}
	enabled, _ := robotSendMemoryOptions(false, cfg)
	if enabled {
		t.Error("default config must not enable send injection without the flag")
	}
}

func TestRobotSendMemoryOptions_MasterSwitchDegradesFlag(t *testing.T) {
	cfg := config.Default()
	cfg.Memory.Enabled = false
	cfg.Memory.SendInjection = true

	// send_injection=true cannot resurrect injection when memory is disabled...
	enabled, inject := robotSendMemoryOptions(false, cfg)
	if enabled {
		t.Error("memory.enabled=false must veto the config-default path")
	}

	// ...and an explicit flag still degrades to a recorded skip in the robot
	// layer (inject.Enabled=false) instead of failing the send.
	enabled, inject = robotSendMemoryOptions(true, cfg)
	if !enabled {
		t.Error("explicit flag keeps the injection attempt (skip is recorded downstream)")
	}
	if inject == nil || inject.Enabled {
		t.Error("inject config must carry memory.enabled=false for the graceful skip")
	}
}

func TestRobotSendMemoryOptions_ParameterMapping(t *testing.T) {
	cfg := config.Default()
	cfg.Memory.SendMaxRules = 7
	cfg.Memory.SendBudgetTokens = 2000
	cfg.Memory.QueryTimeoutSeconds = 9

	_, inject := robotSendMemoryOptions(true, cfg)
	if inject == nil {
		t.Fatal("expected inject config")
	}
	if inject.MaxRules != 7 {
		t.Errorf("MaxRules = %d, want 7", inject.MaxRules)
	}
	if inject.BudgetTokens != 2000 {
		t.Errorf("BudgetTokens = %d, want 2000", inject.BudgetTokens)
	}
	if inject.QueryTimeout != 9*time.Second {
		t.Errorf("QueryTimeout = %v, want 9s", inject.QueryTimeout)
	}
}
