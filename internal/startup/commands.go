package startup

// CommandRequirement specifies what startup phases a command needs
type CommandRequirement int

const (
	// RequirePhase1Only - command can run after minimal Phase 1 startup
	// Examples: version, help, init, completion, robot-help, robot-version
	RequirePhase1Only CommandRequirement = iota

	// RequireConfig - command needs config but not tmux state
	// Examples: config show, bind, recipes list
	RequireConfig

	// RequireFullStartup - command needs full Phase 2 initialization
	// Examples: spawn, status, dashboard, palette
	RequireFullStartup
)

// CommandClassification maps command names to their requirements
var CommandClassification = map[string]CommandRequirement{
	// Phase 1 only commands - instant response
	"version":    RequirePhase1Only,
	"help":       RequirePhase1Only,
	"init":       RequirePhase1Only,
	"completion": RequirePhase1Only,
	"upgrade":    RequirePhase1Only,

	// Silent shell surfaces (bd-config-migrate-warning-wall-151x2): these are
	// eval'd/parsed by the user's shell on every pane and tab-completion, so
	// they must never trigger config loading — a broken config would print
	// warnings into the eval'd stream or completion output. `ntm shell` falls
	// back to built-in defaults silently; cobra's hidden completion commands
	// (`__complete`/`__completeNoDesc`, cobra.ShellCompRequestCmd /
	// cobra.ShellCompNoDescRequestCmd) need no config at all.
	"shell":            RequirePhase1Only,
	"__complete":       RequirePhase1Only,
	"__completeNoDesc": RequirePhase1Only,

	// `ntm config migrate` reads the config file leniently itself — loading
	// the (by definition possibly broken) config first would defeat it.
	"migrate": RequirePhase1Only,

	// Config-only commands
	"config":   RequireConfig,
	"bind":     RequireConfig,
	"recipes":  RequireConfig,
	"personas": RequireConfig,
	"template": RequireConfig,
	"scrub":    RequireConfig,

	// Full startup commands
	"spawn":           RequireFullStartup,
	"create":          RequireFullStartup,
	"quick":           RequireFullStartup,
	"add":             RequireFullStartup,
	"send":            RequireFullStartup,
	"replay":          RequireFullStartup,
	"interrupt":       RequireFullStartup,
	"attach":          RequireFullStartup,
	"list":            RequireFullStartup,
	"status":          RequireFullStartup,
	"view":            RequireFullStartup,
	"zoom":            RequireFullStartup,
	"dashboard":       RequireFullStartup,
	"watch":           RequireFullStartup,
	"copy":            RequireFullStartup,
	"save":            RequireFullStartup,
	"grep":            RequireFullStartup,
	"extract":         RequireFullStartup,
	"checkpoint":      RequireFullStartup,
	"rollback":        RequireFullStartup,
	"session-persist": RequireFullStartup,
	"palette":         RequireFullStartup,
	"kill":            RequireFullStartup,
	"scan":            RequireFullStartup,
	"bugs":            RequireFullStartup,
	"hooks":           RequireFullStartup,
	"health":          RequireFullStartup,
	"history":         RequireFullStartup,
	"analytics":       RequireFullStartup,
	"mail":            RequireFullStartup,
	"lock":            RequireFullStartup,
	"unlock":          RequireFullStartup,
	"locks":           RequireFullStartup,
	"git":             RequireFullStartup,
	"deps":            RequireFullStartup,
	"tutorial":        RequireFullStartup,
	"coordinator":     RequireFullStartup,
	"rotate":          RequireFullStartup,
	"plugins":         RequireFullStartup,
}

// RobotFlagClassification maps robot flags to their requirements
var RobotFlagClassification = map[string]CommandRequirement{
	// Phase 1 only robot flags - instant response
	"robot-help":    RequirePhase1Only,
	"robot-version": RequirePhase1Only,

	// Config-only robot flags
	"robot-recipes":               RequireConfig,
	"robot-grok-acp-run":          RequireConfig,
	"robot-grok-acp-receipt":      RequireConfig,
	"robot-provider-capabilities": RequireConfig,
	"robot-provider-conformance":  RequireConfig,

	// Full startup robot flags
	"robot-status":       RequireFullStartup,
	"robot-plan":         RequireFullStartup,
	"robot-snapshot":     RequireFullStartup,
	"robot-tail":         RequireFullStartup,
	"robot-send":         RequireFullStartup,
	"robot-ack":          RequireFullStartup,
	"robot-spawn":        RequireFullStartup,
	"robot-interrupt":    RequireFullStartup,
	"robot-restart-pane": RequireFullStartup,
	"robot-graph":        RequireFullStartup,
	"robot-mail":         RequireFullStartup,
	"robot-health":       RequireFullStartup,
	"robot-assign":       RequireFullStartup,
	"robot-terse":        RequireFullStartup,
	"robot-save":         RequireFullStartup,
	"robot-restore":      RequireFullStartup,
}

// GetCommandRequirement returns the startup requirement for a command
func GetCommandRequirement(cmd string) CommandRequirement {
	if req, ok := CommandClassification[cmd]; ok {
		return req
	}
	// Default to full startup for unknown commands
	return RequireFullStartup
}

// NeedsConfig returns true if the command requires config loading
func NeedsConfig(cmd string) bool {
	req := GetCommandRequirement(cmd)
	return req >= RequireConfig
}

// CanSkipConfig returns true if the command can run without loading config
func CanSkipConfig(cmd string) bool {
	return GetCommandRequirement(cmd) == RequirePhase1Only
}
