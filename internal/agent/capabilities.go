package agent

import "strings"

// GracefulExitStrategy describes the terminal key sequence used to ask an agent
// CLI to exit cleanly before a pane is respawned or recycled.
type GracefulExitStrategy string

const (
	GracefulExitDoubleInterrupt GracefulExitStrategy = "double_interrupt"
	GracefulExitSlashExit       GracefulExitStrategy = "slash_exit"
	GracefulExitEscapeInterrupt GracefulExitStrategy = "escape_interrupt"
	GracefulExitSingleInterrupt GracefulExitStrategy = "single_interrupt"
)

// HarnessCapability is the single declarative source of truth for every
// built-in AI agent harness supported by NTM (bd-rc2-track-2-harness-ori-3ydfr.1).
//
// Subsystems across internal/agent, internal/context, internal/ratelimit,
// internal/swarm, internal/session, internal/tmux, and internal/tui read from
// this table instead of maintaining divergent 2-, 3-, or 4-agent switch tables.
type HarnessCapability struct {
	Type                      AgentType
	Aliases                   []string
	DisplayName               string
	ProfileName               string
	ShortBadge                string
	Provider                  string // Canonical rate-limit / account provider ("anthropic", "openai", "google", "xai", ...)
	CAAMTool                  string // CAAM CLI tool name ("claude", "codex", "gemini", or "" if unsupported)
	NeedsDoubleEnter          bool
	GracefulExit              GracefulExitStrategy
	SupportsBuiltinCompact    bool
	BuiltinCompactCommand     string
	SupportsHistoryClear      bool
	HistoryClearCommand       string
	ComposerGated             bool
	DefaultContextWindow      int
	SupportsAutomatedDelivery bool
	SupportsAutomatedRelaunch bool
}

// canonicalHarnesses defines all 12 built-in agent harnesses in deterministic order.
var canonicalHarnesses = []HarnessCapability{
	{
		Type:                      AgentTypeClaudeCode,
		Aliases:                   []string{"cc", "claude", "claude-code", "claude_code", "claudecode"},
		DisplayName:               "Claude Code",
		ProfileName:               "Claude",
		ShortBadge:                "cc",
		Provider:                  "anthropic",
		CAAMTool:                  "claude",
		NeedsDoubleEnter:          false,
		GracefulExit:              GracefulExitDoubleInterrupt,
		SupportsBuiltinCompact:    true,
		BuiltinCompactCommand:     "/compact",
		SupportsHistoryClear:      true,
		HistoryClearCommand:       "/clear",
		ComposerGated:             true,
		DefaultContextWindow:      200000,
		SupportsAutomatedDelivery: true,
		SupportsAutomatedRelaunch: true,
	},
	{
		Type:                      AgentTypeCodex,
		Aliases:                   []string{"cod", "codex", "codex-cli", "codex_cli", "codexcli", "openai", "openai-codex", "openai_codex", "openaicodex"},
		DisplayName:               "Codex CLI",
		ProfileName:               "Codex",
		ShortBadge:                "cod",
		Provider:                  "openai",
		CAAMTool:                  "codex",
		NeedsDoubleEnter:          true,
		GracefulExit:              GracefulExitSlashExit,
		SupportsBuiltinCompact:    false,
		SupportsHistoryClear:      false,
		ComposerGated:             true,
		DefaultContextWindow:      128000,
		SupportsAutomatedDelivery: true,
		SupportsAutomatedRelaunch: true,
	},
	{
		Type:                      AgentTypeGemini,
		Aliases:                   []string{"gmi", "gemini", "gemini-cli", "gemini_cli", "geminicli", "google", "google-ai", "google_ai", "google-gemini", "google_gemini", "googlegemini"},
		DisplayName:               "Gemini CLI",
		ProfileName:               "Gemini",
		ShortBadge:                "gmi",
		Provider:                  "google",
		CAAMTool:                  "gemini",
		NeedsDoubleEnter:          true,
		GracefulExit:              GracefulExitEscapeInterrupt,
		SupportsBuiltinCompact:    false,
		SupportsHistoryClear:      true,
		HistoryClearCommand:       "/clear",
		ComposerGated:             false,
		DefaultContextWindow:      1000000,
		SupportsAutomatedDelivery: true,
		SupportsAutomatedRelaunch: true,
	},
	{
		Type:                      AgentTypeAntigravity,
		Aliases:                   []string{"agy", "antigravity", "antigravity-cli", "antigravity_cli", "antigravitycli", "google-antigravity"},
		DisplayName:               "Antigravity CLI",
		ProfileName:               "Antigravity",
		ShortBadge:                "agy",
		Provider:                  "google",
		CAAMTool:                  "gemini",
		NeedsDoubleEnter:          true,
		GracefulExit:              GracefulExitEscapeInterrupt,
		SupportsBuiltinCompact:    false,
		SupportsHistoryClear:      true,
		HistoryClearCommand:       "/clear",
		ComposerGated:             true,
		DefaultContextWindow:      1000000,
		SupportsAutomatedDelivery: true,
		SupportsAutomatedRelaunch: true,
	},
	{
		Type:                      AgentTypeGrok,
		Aliases:                   []string{"grok", "grok-build", "grok_build", "grokbuild", "xai-grok-build", "xai_grok_build", "xaigrokbuild"},
		DisplayName:               "Grok Build",
		ProfileName:               "Grok",
		ShortBadge:                "grok",
		Provider:                  "xai",
		CAAMTool:                  "",
		NeedsDoubleEnter:          false,
		GracefulExit:              GracefulExitDoubleInterrupt,
		SupportsBuiltinCompact:    false,
		SupportsHistoryClear:      false,
		ComposerGated:             true,
		DefaultContextWindow:      256000,
		SupportsAutomatedDelivery: true,
		SupportsAutomatedRelaunch: true,
	},
	{
		Type:                      AgentTypeOpencode,
		Aliases:                   []string{"oc", "opencode", "opencode-cli", "opencode_cli", "opencodecli", "open-code"},
		DisplayName:               "Opencode",
		ProfileName:               "Opencode",
		ShortBadge:                "oc",
		Provider:                  "opencode",
		CAAMTool:                  "",
		NeedsDoubleEnter:          false,
		GracefulExit:              GracefulExitDoubleInterrupt,
		SupportsBuiltinCompact:    false,
		SupportsHistoryClear:      false,
		ComposerGated:             true,
		DefaultContextWindow:      200000,
		SupportsAutomatedDelivery: true,
		SupportsAutomatedRelaunch: true,
	},
	{
		Type:                      AgentTypeOmp,
		Aliases:                   []string{"omp", "oh-my-pi", "oh_my_pi", "ohmypi"},
		DisplayName:               "Oh My Pi",
		ProfileName:               "OMP",
		ShortBadge:                "omp",
		Provider:                  "omp",
		CAAMTool:                  "",
		NeedsDoubleEnter:          false,
		GracefulExit:              GracefulExitDoubleInterrupt,
		SupportsBuiltinCompact:    true,
		BuiltinCompactCommand:     "/compact",
		SupportsHistoryClear:      true,
		HistoryClearCommand:       "/clear",
		ComposerGated:             true,
		DefaultContextWindow:      200000,
		SupportsAutomatedDelivery: true,
		SupportsAutomatedRelaunch: true,
	},
	{
		Type:                      AgentTypePi,
		Aliases:                   []string{"pi", "pi-agent", "pi_agent", "piagent"},
		DisplayName:               "Pi Agent",
		ProfileName:               "Pi",
		ShortBadge:                "pi",
		Provider:                  "pi",
		CAAMTool:                  "",
		NeedsDoubleEnter:          false,
		GracefulExit:              GracefulExitDoubleInterrupt,
		SupportsBuiltinCompact:    true,
		BuiltinCompactCommand:     "/compact",
		SupportsHistoryClear:      true,
		HistoryClearCommand:       "/clear",
		ComposerGated:             true,
		DefaultContextWindow:      200000,
		SupportsAutomatedDelivery: true,
		SupportsAutomatedRelaunch: true,
	},
	{
		Type:                      AgentTypeCursor,
		Aliases:                   []string{"cursor"},
		DisplayName:               "Cursor",
		ProfileName:               "Cursor",
		ShortBadge:                "cur",
		Provider:                  "cursor",
		CAAMTool:                  "",
		NeedsDoubleEnter:          false,
		GracefulExit:              GracefulExitSingleInterrupt,
		SupportsBuiltinCompact:    false,
		SupportsHistoryClear:      false,
		ComposerGated:             false,
		DefaultContextWindow:      200000,
		SupportsAutomatedDelivery: true,
		SupportsAutomatedRelaunch: true,
	},
	{
		Type:                      AgentTypeWindsurf,
		Aliases:                   []string{"windsurf", "ws"},
		DisplayName:               "Windsurf",
		ProfileName:               "Windsurf",
		ShortBadge:                "ws",
		Provider:                  "windsurf",
		CAAMTool:                  "",
		NeedsDoubleEnter:          false,
		GracefulExit:              GracefulExitSingleInterrupt,
		SupportsBuiltinCompact:    false,
		SupportsHistoryClear:      false,
		ComposerGated:             false,
		DefaultContextWindow:      200000,
		SupportsAutomatedDelivery: true,
		SupportsAutomatedRelaunch: true,
	},
	{
		Type:                      AgentTypeAider,
		Aliases:                   []string{"aider"},
		DisplayName:               "Aider",
		ProfileName:               "Aider",
		ShortBadge:                "aid",
		Provider:                  "aider",
		CAAMTool:                  "",
		NeedsDoubleEnter:          false,
		GracefulExit:              GracefulExitSingleInterrupt,
		SupportsBuiltinCompact:    false,
		SupportsHistoryClear:      false,
		ComposerGated:             false,
		DefaultContextWindow:      128000,
		SupportsAutomatedDelivery: true,
		SupportsAutomatedRelaunch: true,
	},
	{
		Type:                      AgentTypeOllama,
		Aliases:                   []string{"ollama"},
		DisplayName:               "Ollama",
		ProfileName:               "Ollama",
		ShortBadge:                "oll",
		Provider:                  "ollama",
		CAAMTool:                  "",
		NeedsDoubleEnter:          false,
		GracefulExit:              GracefulExitSingleInterrupt,
		SupportsBuiltinCompact:    false,
		SupportsHistoryClear:      false,
		ComposerGated:             false,
		DefaultContextWindow:      128000,
		SupportsAutomatedDelivery: true,
		SupportsAutomatedRelaunch: true,
	},
}

var (
	harnessByCanonical map[AgentType]HarnessCapability
	harnessAliasIndex  map[string]AgentType
)

func init() {
	harnessByCanonical = make(map[AgentType]HarnessCapability, len(canonicalHarnesses))
	harnessAliasIndex = make(map[string]AgentType, len(canonicalHarnesses)*4)
	for _, cap := range canonicalHarnesses {
		harnessByCanonical[cap.Type] = cap
		harnessAliasIndex[strings.ToLower(string(cap.Type))] = cap.Type
		for _, alias := range cap.Aliases {
			harnessAliasIndex[strings.ToLower(strings.TrimSpace(alias))] = cap.Type
		}
	}
}

// AllHarnesses returns a defensive copy of all 12 built-in agent harness
// capability descriptors in canonical order.
func AllHarnesses() []HarnessCapability {
	out := make([]HarnessCapability, len(canonicalHarnesses))
	for i, cap := range canonicalHarnesses {
		cp := cap
		cp.Aliases = append([]string(nil), cap.Aliases...)
		out[i] = cp
	}
	return out
}

// LookupHarness resolves any canonical AgentType or alias to its declarative
// HarnessCapability descriptor.
func LookupHarness(t AgentType) (HarnessCapability, bool) {
	key := strings.ToLower(strings.TrimSpace(string(t)))
	if canonical, ok := harnessAliasIndex[key]; ok {
		cap, exists := harnessByCanonical[canonical]
		if exists {
			cap.Aliases = append([]string(nil), cap.Aliases...)
			return cap, true
		}
	}
	return HarnessCapability{}, false
}

// CapabilitiesFor returns the HarnessCapability for t, or a safe conservative
// default for unknown/user panes.
func CapabilitiesFor(t AgentType) HarnessCapability {
	if cap, ok := LookupHarness(t); ok {
		return cap
	}
	return HarnessCapability{
		Type:         t.Canonical(),
		DisplayName:  t.DisplayName(),
		ProfileName:  t.ProfileName(),
		GracefulExit: GracefulExitDoubleInterrupt,
	}
}
