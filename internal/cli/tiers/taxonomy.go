// Package tiers provides command tier taxonomy for progressive CLI discovery.
// Commands are categorized into three tiers:
//   - Tier 1 (Apprentice): Essential workflow commands for new users
//   - Tier 2 (Journeyman): Full standard commands for regular users
//   - Tier 3 (Master): Advanced/experimental features for power users
package tiers

// Tier represents the proficiency level required to see a command.
type Tier int

const (
	// TierApprentice contains essential workflow commands only.
	// New users should start here to avoid cognitive overload.
	// Commands: spawn, send, status, kill, help, version
	TierApprentice Tier = 1

	// TierJourneyman contains all standard commands.
	// Regular users who understand the basics.
	TierJourneyman Tier = 2

	// TierMaster contains advanced/experimental features.
	// Power users, automation, debugging, robot mode.
	TierMaster Tier = 3
)

// String returns the human-readable tier name.
func (t Tier) String() string {
	switch t {
	case TierApprentice:
		return "Apprentice"
	case TierJourneyman:
		return "Journeyman"
	case TierMaster:
		return "Master"
	default:
		return "Unknown"
	}
}

// Description returns a brief explanation of the tier.
func (t Tier) Description() string {
	switch t {
	case TierApprentice:
		return "Essential workflow commands (spawn, send, status, kill)"
	case TierJourneyman:
		return "Full standard commands for regular users"
	case TierMaster:
		return "Advanced features, automation, and debugging"
	default:
		return "Unknown tier"
	}
}

// CommandInfo describes a command with its tier assignment.
type CommandInfo struct {
	// Name is the command name (e.g., "spawn", "send").
	Name string

	// Tier is the minimum proficiency level to see this command.
	Tier Tier

	// Alias is the short alias (e.g., "sat" for spawn).
	Alias string

	// Category groups related commands (e.g., "session", "agent").
	Category string

	// Description is a brief summary of what the command does.
	Description string

	// Examples shows usage patterns.
	Examples []string
}

// Category constants for grouping commands.
const (
	CategorySessionCreation = "Session Creation"
	CategoryAgentManagement = "Agent Management"
	CategorySessionNav      = "Session Navigation"
	CategoryOutput          = "Output Management"
	CategoryPersistence     = "Session Persistence"
	CategoryUtilities       = "Utilities"
	CategoryAdvanced        = "Advanced"
	CategoryCoordination    = "Agent Coordination"
	CategoryConfiguration   = "Configuration"
	CategoryInternal        = "Internal"
)

// Registry maps command names to their tier information.
// This is the authoritative source for command categorization.
var Registry = map[string]CommandInfo{
	// ═══════════════════════════════════════════════════════════════
	// TIER 1 (APPRENTICE) - Essential workflow commands
	// ═══════════════════════════════════════════════════════════════

	"spawn": {
		Name:        "spawn",
		Tier:        TierApprentice,
		Alias:       "sat",
		Category:    CategorySessionCreation,
		Description: "Create session and launch AI agents",
		Examples: []string{
			"ntm spawn myproject --cc=2 --cod=2",
			"ntm spawn myproject --cc=4 --gmi=2",
		},
	},
	"send": {
		Name:        "send",
		Tier:        TierApprentice,
		Alias:       "bp",
		Category:    CategoryAgentManagement,
		Description: "Send prompt to agents",
		Examples: []string{
			"ntm send myproject --cc \"fix the tests\"",
			"ntm send myproject --all \"summarize your progress\"",
		},
	},
	"status": {
		Name:        "status",
		Tier:        TierApprentice,
		Alias:       "snt",
		Category:    CategorySessionNav,
		Description: "Show detailed session status",
		Examples: []string{
			"ntm status myproject",
		},
	},
	"kill": {
		Name:        "kill",
		Tier:        TierApprentice,
		Alias:       "knt",
		Category:    CategoryUtilities,
		Description: "Kill a session",
		Examples: []string{
			"ntm kill myproject",
			"ntm kill -f myproject",
		},
	},
	"version": {
		Name:        "version",
		Tier:        TierApprentice,
		Category:    CategoryUtilities,
		Description: "Print version information",
		Examples: []string{
			"ntm version",
			"ntm version --short",
		},
	},

	// ═══════════════════════════════════════════════════════════════
	// TIER 2 (JOURNEYMAN) - Full standard commands
	// ═══════════════════════════════════════════════════════════════

	// Session Creation
	"create": {
		Name:        "create",
		Tier:        TierJourneyman,
		Alias:       "cnt",
		Category:    CategorySessionCreation,
		Description: "Create empty session with N panes",
		Examples: []string{
			"ntm create myproject --panes=6",
		},
	},
	"quick": {
		Name:        "quick",
		Tier:        TierJourneyman,
		Alias:       "qps",
		Category:    CategorySessionCreation,
		Description: "Quick project setup with git and VSCode",
		Examples: []string{
			"ntm quick myproject --template=go",
		},
	},

	// Agent Management
	"add": {
		Name:        "add",
		Tier:        TierJourneyman,
		Alias:       "ant",
		Category:    CategoryAgentManagement,
		Description: "Add agents to existing session",
		Examples: []string{
			"ntm add myproject --cc=2",
		},
	},
	"interrupt": {
		Name:        "interrupt",
		Tier:        TierJourneyman,
		Alias:       "int",
		Category:    CategoryAgentManagement,
		Description: "Send Ctrl+C to all agents",
		Examples: []string{
			"ntm interrupt myproject",
		},
	},

	// Session Navigation
	"attach": {
		Name:        "attach",
		Tier:        TierJourneyman,
		Alias:       "rnt",
		Category:    CategorySessionNav,
		Description: "Attach/switch to session",
		Examples: []string{
			"ntm attach myproject",
		},
	},
	"list": {
		Name:        "list",
		Tier:        TierJourneyman,
		Alias:       "lnt",
		Category:    CategorySessionNav,
		Description: "List all tmux sessions",
		Examples: []string{
			"ntm list",
		},
	},
	"view": {
		Name:        "view",
		Tier:        TierJourneyman,
		Alias:       "vnt",
		Category:    CategorySessionNav,
		Description: "Tile all panes and attach",
		Examples: []string{
			"ntm view myproject",
		},
	},
	"zoom": {
		Name:        "zoom",
		Tier:        TierJourneyman,
		Alias:       "znt",
		Category:    CategorySessionNav,
		Description: "Zoom to specific pane",
		Examples: []string{
			"ntm zoom myproject 1",
			"ntm zoom myproject cc",
		},
	},
	"dashboard": {
		Name:        "dashboard",
		Tier:        TierJourneyman,
		Alias:       "d",
		Category:    CategorySessionNav,
		Description: "Interactive session dashboard",
		Examples: []string{
			"ntm dashboard myproject",
		},
	},
	"watch": {
		Name:        "watch",
		Tier:        TierJourneyman,
		Alias:       "w",
		Category:    CategorySessionNav,
		Description: "Stream agent output in real-time",
		Examples: []string{
			"ntm watch myproject --cc",
		},
	},

	// Output Management
	"copy": {
		Name:        "copy",
		Tier:        TierJourneyman,
		Alias:       "cpnt",
		Category:    CategoryOutput,
		Description: "Copy pane output to clipboard",
		Examples: []string{
			"ntm copy myproject:1",
			"ntm copy myproject --all",
		},
	},
	"save": {
		Name:        "save",
		Tier:        TierJourneyman,
		Alias:       "svnt",
		Category:    CategoryOutput,
		Description: "Save all outputs to files",
		Examples: []string{
			"ntm save myproject -o ~/logs",
		},
	},
	"grep": {
		Name:        "grep",
		Tier:        TierJourneyman,
		Category:    CategoryOutput,
		Description: "Search pane output with regex",
		Examples: []string{
			"ntm grep 'error' myproject -C 3",
		},
	},
	"extract": {
		Name:        "extract",
		Tier:        TierJourneyman,
		Category:    CategoryOutput,
		Description: "Extract code blocks from output",
		Examples: []string{
			"ntm extract myproject --lang=go",
		},
	},
	"diff": {
		Name:        "diff",
		Tier:        TierJourneyman,
		Category:    CategoryOutput,
		Description: "Compare outputs from two panes",
		Examples: []string{
			"ntm diff myproject cc_1 cod_1",
		},
	},
	"summary": {
		Name:        "summary",
		Tier:        TierJourneyman,
		Category:    CategoryOutput,
		Description: "Generate session summary",
		Examples: []string{
			"ntm summary myproject",
		},
	},

	// Session Persistence
	"checkpoint": {
		Name:        "checkpoint",
		Tier:        TierJourneyman,
		Category:    CategoryPersistence,
		Description: "Create session checkpoint",
		Examples: []string{
			"ntm checkpoint save myproject -m \"Before refactor\"",
		},
	},

	// Utilities
	"palette": {
		Name:        "palette",
		Tier:        TierJourneyman,
		Alias:       "ncp",
		Category:    CategoryUtilities,
		Description: "Open interactive command palette",
		Examples: []string{
			"ntm palette myproject",
		},
	},
	"bind": {
		Name:        "bind",
		Tier:        TierJourneyman,
		Category:    CategoryUtilities,
		Description: "Set up tmux F6 popup binding",
		Examples: []string{
			"ntm bind",
			"ntm bind --key=F5",
		},
	},
	"deps": {
		Name:        "deps",
		Tier:        TierJourneyman,
		Alias:       "cad",
		Category:    CategoryUtilities,
		Description: "Check agent CLI dependencies",
		Examples: []string{
			"ntm deps -v",
		},
	},
	"tutorial": {
		Name:        "tutorial",
		Tier:        TierJourneyman,
		Category:    CategoryUtilities,
		Description: "Interactive tutorial",
		Examples: []string{
			"ntm tutorial",
		},
	},
	"shell": {
		Name:        "shell",
		Tier:        TierJourneyman,
		Category:    CategoryUtilities,
		Description: "Generate shell integration",
		Examples: []string{
			"eval \"$(ntm shell zsh)\"",
		},
	},
	"completion": {
		Name:        "completion",
		Tier:        TierJourneyman,
		Category:    CategoryUtilities,
		Description: "Generate shell completions",
		Examples: []string{
			"ntm completion zsh",
		},
	},
	"config": {
		Name:        "config",
		Tier:        TierJourneyman,
		Category:    CategoryConfiguration,
		Description: "Manage configuration",
		Examples: []string{
			"ntm config init",
			"ntm config show",
		},
	},
	"init": {
		Name:        "init",
		Tier:        TierJourneyman,
		Category:    CategoryUtilities,
		Description: "Initialize NTM for a project",
		Examples: []string{
			"ntm init",
			"ntm init /path/to/project",
		},
	},
	"upgrade": {
		Name:        "upgrade",
		Tier:        TierJourneyman,
		Category:    CategoryUtilities,
		Description: "Self-update to latest version",
		Examples: []string{
			"ntm upgrade",
			"ntm upgrade --check",
		},
	},

	// ═══════════════════════════════════════════════════════════════
	// TIER 3 (MASTER) - Advanced/experimental features
	// ═══════════════════════════════════════════════════════════════

	// Advanced Agent Management
	"replay": {
		Name:        "replay",
		Tier:        TierMaster,
		Category:    CategoryAgentManagement,
		Description: "Replay command from history",
		Examples: []string{
			"ntm replay myproject --id=42",
		},
	},
	"rotate": {
		Name:        "rotate",
		Tier:        TierMaster,
		Category:    CategoryAgentManagement,
		Description: "Rotate agent sessions",
		Examples: []string{
			"ntm rotate myproject --cc",
		},
	},
	"quota": {
		Name:        "quota",
		Tier:        TierMaster,
		Category:    CategoryAgentManagement,
		Description: "Manage agent quotas",
		Examples: []string{
			"ntm quota show",
		},
	},
	"pipeline": {
		Name:        "pipeline",
		Tier:        TierMaster,
		Category:    CategoryAgentManagement,
		Description: "Run multi-step pipelines",
		Examples: []string{
			"ntm pipeline run build-and-test",
		},
	},
	"wait": {
		Name:        "wait",
		Tier:        TierMaster,
		Category:    CategoryAgentManagement,
		Description: "Wait for agent completion",
		Examples: []string{
			"ntm wait myproject --timeout=5m",
		},
	},
	"plugins": {
		Name:        "plugins",
		Tier:        TierMaster,
		Category:    CategoryConfiguration,
		Description: "Manage command plugins",
		Examples: []string{
			"ntm plugins list",
		},
	},
	"agents": {
		Name:        "agents",
		Tier:        TierMaster,
		Category:    CategoryAgentManagement,
		Description: "Agent configuration management",
		Examples: []string{
			"ntm agents list",
		},
	},
	"assign": {
		Name:        "assign",
		Tier:        TierMaster,
		Category:    CategoryAgentManagement,
		Description: "Assign work to agents",
		Examples: []string{
			"ntm assign myproject --auto",
			"ntm assign myproject --pane=1 --beads=bd-123",
		},
	},

	// Session Persistence (advanced)
	"rollback": {
		Name:        "rollback",
		Tier:        TierMaster,
		Category:    CategoryPersistence,
		Description: "Rollback to checkpoint",
		Examples: []string{
			"ntm rollback myproject 20251210-143052",
		},
	},
	"session-persist": {
		Name:        "session-persist",
		Tier:        TierMaster,
		Category:    CategoryPersistence,
		Description: "Persist session state",
		Examples: []string{
			"ntm session-persist myproject",
		},
	},
	"handoff": {
		Name:        "handoff",
		Tier:        TierMaster,
		Category:    CategoryPersistence,
		Description: "Hand off session to next agent",
		Examples: []string{
			"ntm handoff myproject",
		},
	},

	// Advanced Output
	"changes": {
		Name:        "changes",
		Tier:        TierMaster,
		Category:    CategoryOutput,
		Description: "Show file changes by agents",
		Examples: []string{
			"ntm changes myproject",
		},
	},
	"conflicts": {
		Name:        "conflicts",
		Tier:        TierMaster,
		Category:    CategoryOutput,
		Description: "Detect file edit conflicts",
		Examples: []string{
			"ntm conflicts myproject",
		},
	},
	"get-all-session-text": {
		Name:        "get-all-session-text",
		Tier:        TierMaster,
		Category:    CategoryOutput,
		Description: "Get all session text output",
		Examples: []string{
			"ntm get-all-session-text myproject",
		},
	},

	// Debugging & Safety
	"scan": {
		Name:        "scan",
		Tier:        TierMaster,
		Category:    CategoryAdvanced,
		Description: "Scan for issues",
		Examples: []string{
			"ntm scan myproject",
		},
	},
	"bugs": {
		Name:        "bugs",
		Tier:        TierMaster,
		Category:    CategoryAdvanced,
		Description: "Run bug scanner",
		Examples: []string{
			"ntm bugs",
		},
	},
	"cass": {
		Name:        "cass",
		Tier:        TierMaster,
		Category:    CategoryAdvanced,
		Description: "Cross-agent session search",
		Examples: []string{
			"ntm cass search \"auth error\"",
		},
	},
	"hooks": {
		Name:        "hooks",
		Tier:        TierMaster,
		Category:    CategoryAdvanced,
		Description: "Manage session hooks",
		Examples: []string{
			"ntm hooks list",
		},
	},
	"health": {
		Name:        "health",
		Tier:        TierMaster,
		Category:    CategoryAdvanced,
		Description: "Check agent health",
		Examples: []string{
			"ntm health myproject",
		},
	},
	"doctor": {
		Name:        "doctor",
		Tier:        TierMaster,
		Category:    CategoryAdvanced,
		Description: "Diagnose issues",
		Examples: []string{
			"ntm doctor",
		},
	},
	"cleanup": {
		Name:        "cleanup",
		Tier:        TierMaster,
		Category:    CategoryAdvanced,
		Description: "Clean up temp files",
		Examples: []string{
			"ntm cleanup",
		},
	},
	"safety": {
		Name:        "safety",
		Tier:        TierMaster,
		Category:    CategoryAdvanced,
		Description: "Configure safety settings",
		Examples: []string{
			"ntm safety show",
		},
	},
	"policy": {
		Name:        "policy",
		Tier:        TierMaster,
		Category:    CategoryAdvanced,
		Description: "Manage command policies",
		Examples: []string{
			"ntm policy show",
			"ntm policy validate",
		},
	},
	"guards": {
		Name:        "guards",
		Tier:        TierMaster,
		Category:    CategoryAdvanced,
		Description: "Manage safety guards",
		Examples: []string{
			"ntm guards list",
		},
	},
	"approve": {
		Name:        "approve",
		Tier:        TierMaster,
		Category:    CategoryAdvanced,
		Description: "Approve pending actions",
		Examples: []string{
			"ntm approve pending",
		},
	},
	"serve": {
		Name:        "serve",
		Tier:        TierMaster,
		Category:    CategoryAdvanced,
		Description: "Start NTM server",
		Examples: []string{
			"ntm serve",
		},
	},
	"setup": {
		Name:        "setup",
		Tier:        TierMaster,
		Category:    CategoryAdvanced,
		Description: "Run setup wizard",
		Examples: []string{
			"ntm setup",
		},
	},

	// Analytics & Monitoring
	"activity": {
		Name:        "activity",
		Tier:        TierMaster,
		Category:    CategoryAdvanced,
		Description: "Show agent activity",
		Examples: []string{
			"ntm activity myproject --watch",
		},
	},
	"history": {
		Name:        "history",
		Tier:        TierMaster,
		Category:    CategoryAdvanced,
		Description: "View command history",
		Examples: []string{
			"ntm history myproject",
		},
	},
	"analytics": {
		Name:        "analytics",
		Tier:        TierMaster,
		Category:    CategoryAdvanced,
		Description: "View session analytics",
		Examples: []string{
			"ntm analytics --days=7",
		},
	},
	"metrics": {
		Name:        "metrics",
		Tier:        TierMaster,
		Category:    CategoryAdvanced,
		Description: "Export session metrics",
		Examples: []string{
			"ntm metrics myproject",
		},
	},
	"work": {
		Name:        "work",
		Tier:        TierMaster,
		Category:    CategoryAdvanced,
		Description: "Work queue management",
		Examples: []string{
			"ntm work list",
		},
	},
	"memory": {
		Name:        "memory",
		Tier:        TierMaster,
		Category:    CategoryAdvanced,
		Description: "Agent memory integration",
		Examples: []string{
			"ntm memory query \"auth patterns\"",
		},
	},
	"context": {
		Name:        "context",
		Tier:        TierMaster,
		Category:    CategoryAdvanced,
		Description: "Context window management",
		Examples: []string{
			"ntm context myproject",
		},
	},
	"monitor": {
		Name:        "monitor",
		Tier:        TierMaster,
		Category:    CategoryInternal,
		Description: "Internal monitoring",
		Examples: []string{
			"ntm monitor",
		},
	},
	"beads": {
		Name:        "beads",
		Tier:        TierMaster,
		Category:    CategoryAdvanced,
		Description: "Beads daemon management",
		Examples: []string{
			"ntm beads status",
		},
	},

	// Agent Mail & Coordination
	"mail": {
		Name:        "mail",
		Tier:        TierMaster,
		Category:    CategoryCoordination,
		Description: "Agent mail management",
		Examples: []string{
			"ntm mail inbox",
		},
	},
	"lock": {
		Name:        "lock",
		Tier:        TierMaster,
		Category:    CategoryCoordination,
		Description: "Acquire file reservation",
		Examples: []string{
			"ntm lock myproject internal/cli/*.go",
		},
	},
	"unlock": {
		Name:        "unlock",
		Tier:        TierMaster,
		Category:    CategoryCoordination,
		Description: "Release file reservation",
		Examples: []string{
			"ntm unlock myproject",
		},
	},
	"locks": {
		Name:        "locks",
		Tier:        TierMaster,
		Category:    CategoryCoordination,
		Description: "Show active file reservations",
		Examples: []string{
			"ntm locks myproject",
		},
	},
	"message": {
		Name:        "message",
		Tier:        TierMaster,
		Category:    CategoryCoordination,
		Description: "Send agent messages",
		Examples: []string{
			"ntm message send --to=agent1 \"Hello\"",
		},
	},
	"coordinator": {
		Name:        "coordinator",
		Tier:        TierMaster,
		Category:    CategoryCoordination,
		Description: "Multi-agent coordination",
		Examples: []string{
			"ntm coordinator status",
		},
	},

	// Git & Worktrees
	"git": {
		Name:        "git",
		Tier:        TierMaster,
		Category:    CategoryAdvanced,
		Description: "Git coordination",
		Examples: []string{
			"ntm git status",
		},
	},
	"worktrees": {
		Name:        "worktrees",
		Tier:        TierMaster,
		Category:    CategoryAdvanced,
		Description: "Git worktree management",
		Examples: []string{
			"ntm worktrees list",
		},
	},

	// Configuration Management
	"recipes": {
		Name:        "recipes",
		Tier:        TierMaster,
		Category:    CategoryConfiguration,
		Description: "Spawn recipe management",
		Examples: []string{
			"ntm recipes list",
		},
	},
	"workflows": {
		Name:        "workflows",
		Tier:        TierMaster,
		Category:    CategoryConfiguration,
		Description: "Workflow template management",
		Examples: []string{
			"ntm workflows list",
		},
	},
	"personas": {
		Name:        "personas",
		Tier:        TierMaster,
		Category:    CategoryConfiguration,
		Description: "Agent persona/profile management",
		Examples: []string{
			"ntm personas list",
		},
	},
	"template": {
		Name:        "template",
		Tier:        TierMaster,
		Category:    CategoryConfiguration,
		Description: "Project template management",
		Examples: []string{
			"ntm template list",
		},
	},
}

// GetByTier returns all commands at or below the specified tier.
func GetByTier(maxTier Tier) []CommandInfo {
	var result []CommandInfo
	for _, cmd := range Registry {
		if cmd.Tier <= maxTier {
			result = append(result, cmd)
		}
	}
	return result
}

// GetByCategory returns all commands in the specified category.
func GetByCategory(category string) []CommandInfo {
	var result []CommandInfo
	for _, cmd := range Registry {
		if cmd.Category == category {
			result = append(result, cmd)
		}
	}
	return result
}

// GetApprenticeCommands returns essential commands for new users.
func GetApprenticeCommands() []CommandInfo {
	return GetByTier(TierApprentice)
}

// GetJourneymanCommands returns standard commands for regular users.
func GetJourneymanCommands() []CommandInfo {
	return GetByTier(TierJourneyman)
}

// GetMasterCommands returns all commands including advanced features.
func GetMasterCommands() []CommandInfo {
	return GetByTier(TierMaster)
}

// GetTier returns the tier for a given command name.
// Returns TierMaster for unknown commands (conservative default).
func GetTier(command string) Tier {
	if info, ok := Registry[command]; ok {
		return info.Tier
	}
	return TierMaster // Unknown commands default to Master tier
}

// IsEssential returns true if the command is in the Apprentice tier.
func IsEssential(command string) bool {
	return GetTier(command) == TierApprentice
}

// AllCategories returns all unique command categories.
func AllCategories() []string {
	return []string{
		CategorySessionCreation,
		CategoryAgentManagement,
		CategorySessionNav,
		CategoryOutput,
		CategoryPersistence,
		CategoryUtilities,
		CategoryAdvanced,
		CategoryCoordination,
		CategoryConfiguration,
		CategoryInternal,
	}
}

// CountByTier returns the number of commands at each tier.
func CountByTier() map[Tier]int {
	counts := make(map[Tier]int)
	for _, cmd := range Registry {
		counts[cmd.Tier]++
	}
	return counts
}
