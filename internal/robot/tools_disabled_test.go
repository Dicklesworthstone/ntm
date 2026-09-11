package robot

import (
	"testing"

	"github.com/Dicklesworthstone/ntm/internal/config"
	"github.com/Dicklesworthstone/ntm/internal/tools"
)

// TestDisabledToolsHonorsEveryIntegrationToggle is the ntm#313 mapping
// regression: each config toggle documented as a top-level switch for an
// ecosystem integration must actually reach the registry adapter it gates, so
// a disabled tool is reported rather than executed.
func TestDisabledToolsHonorsEveryIntegrationToggle(t *testing.T) {
	cases := []struct {
		name string
		tool tools.ToolName
		off  func(*config.Config)
	}{
		{"cass", tools.ToolCASS, func(c *config.Config) { c.CASS.Enabled = false }},
		{"cm", tools.ToolCM, func(c *config.Config) { c.Memory.Enabled = false }},
		{"am", tools.ToolAM, func(c *config.Config) { c.AgentMail.Enabled = false }},
		{"rch", tools.ToolRCH, func(c *config.Config) { c.Integrations.RCH.Enabled = false }},
		{"pt", tools.ToolPT, func(c *config.Config) { c.Integrations.ProcessTriage.Enabled = false }},
		{"rano", tools.ToolRano, func(c *config.Config) { c.Integrations.Rano.Enabled = false }},
		{"xf", tools.ToolXF, func(c *config.Config) { c.Integrations.XF.Enabled = false }},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			cfg := config.Default()
			tc.off(cfg)

			disabled := DisabledTools(cfg)

			if !disabled[tc.tool] {
				t.Errorf("%s disabled in config but %s is still probed", tc.name, tc.tool)
			}
			// Turning one integration off must not silence any other.
			for _, other := range cases {
				if other.tool == tc.tool {
					continue
				}
				if disabled[other.tool] {
					t.Errorf("disabling %s also disabled %s", tc.name, other.tool)
				}
			}
		})
	}
}

// TestDisabledToolsDefaultConfigDisablesNothing guards the blast radius:
// every gated integration defaults to enabled, so a default install must
// probe exactly what it probed before ntm#313.
func TestDisabledToolsDefaultConfigDisablesNothing(t *testing.T) {
	if disabled := DisabledTools(config.Default()); len(disabled) != 0 {
		t.Errorf("default config disables %v; every integration toggle defaults to enabled", disabled)
	}
}

// TestDisabledToolsNilConfigProbesEverything verifies that an unreadable or
// absent configuration never claims an integration is off — probing is the
// safe default when we do not know.
func TestDisabledToolsNilConfigProbesEverything(t *testing.T) {
	if disabled := DisabledTools(nil); len(disabled) != 0 {
		t.Errorf("nil config disabled %v, want nothing", disabled)
	}
}
