package robot

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"strings"
	"time"

	processutil "github.com/shirou/gopsutil/v4/process"

	"github.com/Dicklesworthstone/ntm/internal/config"
	"github.com/Dicklesworthstone/ntm/internal/policy"
)

const (
	doctorSchemaID            = "ntm:robot:doctor:v1"
	doctorSchemaVersion       = "1.0.0"
	GuardDegradedDoctorWindow = 7 * 24 * time.Hour
	doctorGuardUnreachable    = "agent-mail-unreachable"
	doctorDepVersionTimeout   = 2 * time.Second
)

// DoctorOptions configures ORI ecosystem health and contention inspection.
type DoctorOptions struct {
	Verbose           bool
	IncludeContention bool
	SwarmCWD          string
	Config            *config.Config
	ConfigPath        string
}

// DoctorContentionHolder identifies a process using a resource that commonly
// blocks multi-agent work.
type DoctorContentionHolder struct {
	PID      int    `json:"pid"`
	CWD      string `json:"cwd,omitempty"`
	Command  string `json:"command"`
	Resource string `json:"resource"`
	Age      string `json:"age"`
	InSwarm  bool   `json:"in_swarm"`
}

// DoctorContentionProcess captures process attributes evaluated by ClassifyContentionProcess.
type DoctorContentionProcess struct {
	PID       int
	CWD       string
	Command   string
	Age       time.Duration
	Status    string
	OpenFiles []string
}

// DoctorSafetyDefaults captures security and privacy defaults derived from config.
type DoctorSafetyDefaults struct {
	RedactionMode              string   `json:"redaction_mode"`
	RedactionAllowlistEnabled  bool     `json:"redaction_allowlist_enabled"`
	RedactionAllowlistCount    int      `json:"redaction_allowlist_count"`
	PrivacyDefaultEnabled      bool     `json:"privacy_default_enabled"`
	EncryptionAtRestEnabled    bool     `json:"encryption_at_rest_enabled"`
	PreflightDefaultStrict     bool     `json:"preflight_default_strict"`
	ClaudeAgentHooks           []string `json:"claude_agent_hooks"`
	ClaudePolicyHookRegistered bool     `json:"claude_policy_hook_registered"`
	ClaudeAgentHooksWarning    string   `json:"claude_agent_hooks_warning,omitempty"`
}

// DoctorInvariantCheck represents a design invariant check result.
type DoctorInvariantCheck struct {
	ID      string   `json:"id"`
	Name    string   `json:"name"`
	Status  string   `json:"status"` // "ok", "warning", "error", "unverified"
	Message string   `json:"message,omitempty"`
	Details []string `json:"details,omitempty"`
}

// DoctorToolCheck represents a tool health check result.
type DoctorToolCheck struct {
	Name         string   `json:"name"`
	Installed    bool     `json:"installed"`
	Version      string   `json:"version,omitempty"`
	Path         string   `json:"path,omitempty"`
	Capabilities []string `json:"capabilities,omitempty"`
	Required     bool     `json:"required"`
	Status       string   `json:"status"` // "ok", "warning", "error"
	Message      string   `json:"message,omitempty"`
	Details      []string `json:"details,omitempty"`
}

// DoctorDepCheck represents a dependency check result.
type DoctorDepCheck struct {
	Name       string `json:"name"`
	Installed  bool   `json:"installed"`
	Version    string `json:"version,omitempty"`
	MinVersion string `json:"min_version,omitempty"`
	Path       string `json:"path,omitempty"`
	Required   bool   `json:"required,omitempty"`
	Status     string `json:"status"`
	Message    string `json:"message,omitempty"`
}

// DoctorDaemonCheck represents a daemon health check result.
type DoctorDaemonCheck struct {
	Name    string `json:"name"`
	Running bool   `json:"running"`
	Port    int    `json:"port,omitempty"`
	PID     int    `json:"pid,omitempty"`
	Status  string `json:"status"`
	Message string `json:"message,omitempty"`
}

// DoctorConfigCheck represents a configuration check result.
type DoctorConfigCheck struct {
	Name    string `json:"name"`
	Valid   bool   `json:"valid"`
	Status  string `json:"status"`
	Message string `json:"message,omitempty"`
}

// DoctorGuardHookCheck summarizes pre-commit guard degraded-mode history.
type DoctorGuardHookCheck struct {
	Name         string `json:"name"`
	Status       string `json:"status"`
	DegradedRuns int    `json:"degraded_runs"`
	Since        string `json:"since,omitempty"`
	Message      string `json:"message,omitempty"`
}

// DoctorOutput represents the structured JSON envelope for --robot-doctor.
type DoctorOutput struct {
	RobotResponse
	SchemaID       string                   `json:"schema_id"`
	SchemaVersion  string                   `json:"schema_version"`
	Overall        string                   `json:"overall"` // "healthy", "warning", "unhealthy"
	SafetyDefaults DoctorSafetyDefaults     `json:"safety_defaults"`
	Tools          []DoctorToolCheck        `json:"tools"`
	Dependencies   []DoctorDepCheck         `json:"dependencies"`
	Daemons        []DoctorDaemonCheck      `json:"daemons"`
	Configuration  []DoctorConfigCheck      `json:"configuration"`
	Invariants     []DoctorInvariantCheck   `json:"invariants"`
	GuardHook      *DoctorGuardHookCheck    `json:"guard_hook,omitempty"`
	Contention     []DoctorContentionHolder `json:"contention"`
	Warnings       int                      `json:"warnings"`
	Errors         int                      `json:"errors"`
	AgentHints     *AgentHints              `json:"_agent_hints,omitempty"`
}

func newDoctorErrorOutput(cfg *config.Config, err error, code, hint string) *DoctorOutput {
	return &DoctorOutput{
		RobotResponse:  NewErrorResponse(err, code, hint),
		SchemaID:       doctorSchemaID,
		SchemaVersion:  doctorSchemaVersion,
		Overall:        "unhealthy",
		SafetyDefaults: BuildSafetyDefaults(cfg),
		Tools:          []DoctorToolCheck{},
		Dependencies:   []DoctorDepCheck{},
		Daemons:        []DoctorDaemonCheck{},
		Configuration:  []DoctorConfigCheck{},
		Invariants:     []DoctorInvariantCheck{},
		Contention:     []DoctorContentionHolder{},
		Errors:         1,
	}
}

// GetDoctor executes the canonical ORI ecosystem health, configuration,
// invariant, guard-hook, and optional shared-resource contention checks.
func GetDoctor(ctx context.Context, opts DoctorOptions) (*DoctorOutput, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if err := ctx.Err(); err != nil {
		return newDoctorErrorOutput(opts.Config, err, ErrCodeTimeout, "Retry --robot-doctor with an active context"), nil
	}

	cfg := opts.Config
	if cfg == nil {
		if loaded, loadErr := config.LoadMerged("", opts.ConfigPath); loadErr == nil && loaded != nil {
			cfg = loaded
		} else {
			cfg = config.Default()
		}
	}

	safetyDefaults := BuildSafetyDefaults(cfg)
	toolChecks := checkDoctorTools(ctx, opts.Verbose)
	depChecks := CheckDoctorDependencies(ctx)
	daemonChecks := checkDoctorDaemons(ctx, cfg)
	configChecks, guardHook := checkDoctorConfiguration(opts.ConfigPath)
	invariantChecks := checkDoctorInvariants(ctx, toolChecks)

	contention := []DoctorContentionHolder{}
	var contentionWarn string
	if opts.IncludeContention {
		swarmCWD := strings.TrimSpace(opts.SwarmCWD)
		if swarmCWD == "" {
			if cwd, err := os.Getwd(); err == nil {
				swarmCWD = cwd
			}
		}
		if holders, err := FindContentionHolders(ctx, swarmCWD); err != nil {
			contentionWarn = fmt.Sprintf("contention scan failed: %v", err)
		} else if holders != nil {
			contention = holders
		}
	}

	var warningsCount, errorsCount int
	var hintWarnings []string

	for _, t := range toolChecks {
		switch t.Status {
		case "error":
			errorsCount++
		case "warning":
			warningsCount++
		}
	}
	for _, d := range depChecks {
		switch d.Status {
		case "error":
			errorsCount++
		case "warning":
			warningsCount++
		}
	}
	for _, d := range daemonChecks {
		switch d.Status {
		case "error":
			errorsCount++
		case "warning":
			warningsCount++
		}
	}
	for _, c := range configChecks {
		switch c.Status {
		case "error":
			errorsCount++
		case "warning":
			warningsCount++
		}
	}
	for _, i := range invariantChecks {
		switch i.Status {
		case "error":
			errorsCount++
		case "warning":
			warningsCount++
		}
	}
	if contentionWarn != "" {
		warningsCount++
		hintWarnings = append(hintWarnings, contentionWarn)
	}

	overall := "healthy"
	if errorsCount > 0 {
		overall = "unhealthy"
	} else if warningsCount > 0 {
		overall = "warning"
	}

	resp := NewRobotResponse(overall != "unhealthy")
	if overall == "unhealthy" {
		resp.ErrorCode = ErrCodeInternalError
		resp.Error = fmt.Sprintf("ecosystem is unhealthy (%d errors, %d warnings)", errorsCount, warningsCount)
		resp.Hint = "Inspect tools, dependencies, configuration, and invariants with status=\"error\" to remediate"
	}

	var actions []RobotAction
	if errorsCount > 0 {
		actions = append(actions, RobotAction{
			Action:   "remediate_errors",
			Target:   "ecosystem",
			Reason:   fmt.Sprintf("%d error check(s) failing", errorsCount),
			Priority: 1,
			Details:  "Install missing required tools/dependencies (tmux, bv, br) or remove dead config keys",
		})
	}
	if len(contention) > 0 {
		hintWarnings = append(hintWarnings, fmt.Sprintf("%d shared-resource contention holder(s) detected", len(contention)))
		actions = append(actions, RobotAction{
			Action:   "inspect_contention",
			Target:   "processes",
			Reason:   fmt.Sprintf("%d process(es) holding shared build or Beads resources", len(contention)),
			Priority: 2,
			Details:  "Review contention[] entries before terminating non-swarm holders via 'ntm doctor contention'",
		})
	}

	out := &DoctorOutput{
		RobotResponse:  resp,
		SchemaID:       doctorSchemaID,
		SchemaVersion:  doctorSchemaVersion,
		Overall:        overall,
		SafetyDefaults: safetyDefaults,
		Tools:          toolChecks,
		Dependencies:   depChecks,
		Daemons:        daemonChecks,
		Configuration:  configChecks,
		Invariants:     invariantChecks,
		GuardHook:      guardHook,
		Contention:     contention,
		Warnings:       warningsCount,
		Errors:         errorsCount,
		AgentHints: &AgentHints{
			Summary:          fmt.Sprintf("Ecosystem %s (%d errors, %d warnings, %d contention holders)", overall, errorsCount, warningsCount, len(contention)),
			Warnings:         hintWarnings,
			SuggestedActions: actions,
		},
	}

	return out, nil
}

// PrintDoctor executes GetDoctor and writes the terminal JSON/TOON response.
func PrintDoctor(ctx context.Context, opts DoctorOptions) error {
	out, err := GetDoctor(ctx, opts)
	if err != nil {
		return err
	}
	return encodeTerminalRobotOutput(out, out.RobotResponse, "ecosystem is unhealthy")
}

// FindContentionHolders scans running processes via gopsutil for processes
// holding Beads SQLite files, cargo registry, rsync, or uninterruptible I/O.
func FindContentionHolders(ctx context.Context, swarmCWD string) ([]DoctorContentionHolder, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	processes, err := processutil.ProcessesWithContext(ctx)
	if err != nil {
		return nil, fmt.Errorf("list processes: %w", err)
	}
	now := time.Now()
	holders := make([]DoctorContentionHolder, 0)
	for _, proc := range processes {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		command, err := proc.CmdlineWithContext(ctx)
		if err != nil || strings.TrimSpace(command) == "" {
			continue
		}
		cwd, _ := proc.CwdWithContext(ctx)
		createdMS, _ := proc.CreateTimeWithContext(ctx)
		age := time.Duration(0)
		if createdMS > 0 {
			age = now.Sub(time.UnixMilli(createdMS))
			if age < 0 {
				age = 0
			}
		}
		statuses, _ := proc.StatusWithContext(ctx)
		openFiles := contentionOpenFiles(ctx, proc)
		for _, resource := range ClassifyContentionProcess(DoctorContentionProcess{
			PID:       int(proc.Pid),
			CWD:       cwd,
			Command:   command,
			Age:       age,
			Status:    strings.Join(statuses, ","),
			OpenFiles: openFiles,
		}) {
			holders = append(holders, DoctorContentionHolder{
				PID:      int(proc.Pid),
				CWD:      cwd,
				Command:  command,
				Resource: resource,
				Age:      age.Round(time.Second).String(),
				InSwarm:  SameOrChildPath(cwd, swarmCWD),
			})
		}
	}
	sort.Slice(holders, func(i, j int) bool {
		if holders[i].PID != holders[j].PID {
			return holders[i].PID < holders[j].PID
		}
		return holders[i].Resource < holders[j].Resource
	})
	return holders, nil
}

func contentionOpenFiles(ctx context.Context, proc *processutil.Process) []string {
	files, openErr := proc.OpenFilesWithContext(ctx)
	if openErr != nil || len(files) == 0 {
		return []string{}
	}
	paths := make([]string, 0, len(files))
	for _, file := range files {
		paths = append(paths, file.Path)
	}
	return paths
}

// ClassifyContentionProcess identifies shared resources held by proc.
func ClassifyContentionProcess(proc DoctorContentionProcess) []string {
	command := strings.ToLower(proc.Command)
	resources := make([]string, 0, 3)
	hasBeadsDB := false
	for _, path := range proc.OpenFiles {
		path = filepath.ToSlash(strings.ToLower(path))
		if strings.HasSuffix(path, "/.beads/beads.db") || strings.HasSuffix(path, "/.beads/beads.db-wal") || strings.HasSuffix(path, "/.beads/beads.db-shm") {
			resources = append(resources, "beads_db")
			hasBeadsDB = true
			break
		}
	}
	if !hasBeadsDB && (CommandHasExecutable(command, "br") || CommandHasExecutable(command, "bd")) {
		resources = append(resources, "beads_db")
	}
	if CommandHasExecutable(command, "cargo") {
		resources = append(resources, "cargo_registry")
	}
	if strings.Contains(command, "rsync") {
		resources = append(resources, "rsync")
	}
	if strings.EqualFold(strings.TrimSpace(proc.Status), "d") {
		resources = append(resources, "uninterruptible_io")
	}
	return resources
}

// CommandHasExecutable reports whether the leading token of command resolves to executable.
func CommandHasExecutable(command, executable string) bool {
	fields := strings.Fields(command)
	return len(fields) > 0 && filepath.Base(fields[0]) == executable
}

// SameOrChildPath reports whether candidate is equal to or nested beneath root.
func SameOrChildPath(candidate, root string) bool {
	if candidate == "" || root == "" {
		return false
	}
	rel, err := filepath.Rel(root, candidate)
	return err == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator))
}

// TerminateContentionHolder sends SIGTERM to terminatePID when it appears in
// holders and passes --confirm and in-swarm --force safety checks.
func TerminateContentionHolder(ctx context.Context, holders []DoctorContentionHolder, terminatePID int, confirm, force bool) error {
	if terminatePID <= 0 {
		return nil
	}
	if !confirm {
		return errors.New("refusing termination without --confirm")
	}
	var target *DoctorContentionHolder
	for i := range holders {
		if holders[i].PID == terminatePID {
			target = &holders[i]
			break
		}
	}
	if target == nil {
		return fmt.Errorf("PID %d is not a detected contention holder", terminatePID)
	}
	if target.InSwarm && !force {
		return fmt.Errorf("refusing to terminate in-swarm PID %d without --force", terminatePID)
	}
	proc, err := processutil.NewProcess(int32(terminatePID))
	if err != nil {
		return fmt.Errorf("open PID %d: %w", terminatePID, err)
	}
	if err := proc.TerminateWithContext(ctx); err != nil {
		return fmt.Errorf("terminate PID %d: %w", terminatePID, err)
	}
	return nil
}

// BuildSafetyDefaults derives DoctorSafetyDefaults from cfg (falling back to config.Default()).
func BuildSafetyDefaults(cfg *config.Config) DoctorSafetyDefaults {
	if cfg == nil {
		cfg = config.Default()
	}

	redactionMode := strings.TrimSpace(cfg.Redaction.Mode)
	if redactionMode == "" {
		redactionMode = config.DefaultRedactionConfig().Mode
	}

	allowlistCount := len(cfg.Redaction.Allowlist)

	defaults := DoctorSafetyDefaults{
		RedactionMode:             redactionMode,
		RedactionAllowlistEnabled: allowlistCount > 0,
		RedactionAllowlistCount:   allowlistCount,
		PrivacyDefaultEnabled:     cfg.Privacy.Enabled,
		EncryptionAtRestEnabled:   cfg.Encryption.Enabled,
		PreflightDefaultStrict:    cfg.Preflight.Strict,
		ClaudeAgentHooks:          []string{},
	}
	defaults.ClaudePolicyHookRegistered, defaults.ClaudeAgentHooksWarning = claudeAgentHookCoverage(cfg, &defaults.ClaudeAgentHooks)
	return defaults
}

func claudeAgentHookCoverage(cfg *config.Config, agentHooks *[]string) (registered bool, warning string) {
	if home, err := os.UserHomeDir(); err == nil {
		script := policy.ClaudeHookScriptPath(home)
		ok, regErr := policy.ClaudeHookRegistered(policy.ClaudeUserSettingsPath(home), script)
		registered = regErr == nil && ok && doctorPathExists(script)
	}
	launch := policy.ClaudeAgentLaunchSettings(cfg)
	*agentHooks = append(*agentHooks, launch.Sources...)

	tmpl := cfg.Agents.Claude
	if strings.TrimSpace(tmpl) == "" {
		tmpl = config.DefaultAgentTemplates().Claude
	}
	rendered, err := config.GenerateAgentCommand(tmpl, config.AgentTemplateVars{AgentType: "cc", ClaudeSettings: launch.Settings})
	switch {
	case err != nil:
		return registered, fmt.Sprintf("the [agents] claude command does not render: %v", err)
	case config.ClaudeHooksNotAppliedWarning(rendered, launch.Settings) != "":
		*agentHooks = (*agentHooks)[:0]
		if registered {
			return registered, ""
		}
		return registered, "the [agents] claude command cannot carry --settings; add --settings {{shellQuote .ClaudeSettings}} to it"
	case !registered && !slices.Contains(launch.Sources, "ntm-policy"):
		return registered, "[safety] claude_policy_hook = false and no hook registered by `ntm safety install`"
	}
	return registered, ""
}

func doctorPathExists(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}
