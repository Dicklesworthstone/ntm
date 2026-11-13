package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/charmbracelet/lipgloss"
	"github.com/spf13/cobra"

	"github.com/Dicklesworthstone/ntm/internal/config"
	"github.com/Dicklesworthstone/ntm/internal/invariants"
	"github.com/Dicklesworthstone/ntm/internal/policy"
	"github.com/Dicklesworthstone/ntm/internal/state"
	"github.com/Dicklesworthstone/ntm/internal/tmux"
	"github.com/Dicklesworthstone/ntm/internal/tools"
	processutil "github.com/shirou/gopsutil/v4/process"
)

var (
	doctorVerbose bool
)

func newDoctorCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "doctor",
		Short: "Validate the NTM ecosystem health",
		Long: `Validates that all ecosystem tools and dependencies are properly installed
and configured. Checks:

  - Tool detection (bv, bd, am, cm, cass, s2p)
  - Version compatibility (including optional Grok Build and Oh My Pi discovery)
  - Daemon health and port availability
  - Configuration files

This command helps diagnose issues before spawning sessions.`,
		RunE: runDoctor,
	}

	cmd.Flags().BoolVarP(&doctorVerbose, "verbose", "v", false, "Show detailed output")
	cmd.AddCommand(newDoctorContentionCmd())

	return cmd
}

type contentionCommandOptions struct {
	terminatePID int
	confirm      bool
	force        bool
}

// ContentionHolder identifies a process using a resource that commonly blocks
// multi-agent work. Metadata is gathered through gopsutil so cwd and age have
// the same meaning on macOS and Linux.
type ContentionHolder struct {
	PID      int    `json:"pid"`
	CWD      string `json:"cwd,omitempty"`
	Command  string `json:"command"`
	Resource string `json:"resource"`
	Age      string `json:"age"`
	InSwarm  bool   `json:"in_swarm"`
}

func newDoctorContentionCmd() *cobra.Command {
	opts := contentionCommandOptions{}
	cmd := &cobra.Command{
		Use:   "contention",
		Short: "Find processes holding shared build and Beads resources",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx, cancel := context.WithTimeout(cmd.Context(), 10*time.Second)
			defer cancel()
			cwd, err := os.Getwd()
			if err != nil {
				return fmt.Errorf("get working directory: %w", err)
			}
			holders, err := findContentionHolders(ctx, cwd)
			if err != nil {
				return err
			}
			if opts.terminatePID > 0 {
				if err := terminateContentionHolder(ctx, holders, opts); err != nil {
					return err
				}
			}
			if IsJSONOutput() {
				return json.NewEncoder(os.Stdout).Encode(struct {
					Holders []ContentionHolder `json:"holders"`
				}{Holders: holders})
			}
			if len(holders) == 0 {
				fmt.Fprintln(cmd.OutOrStdout(), "No shared-resource contention detected.")
				return nil
			}
			fmt.Fprintln(cmd.OutOrStdout(), "PID\tAGE\tRESOURCE\tIN_SWARM\tCWD\tCOMMAND")
			for _, holder := range holders {
				fmt.Fprintf(cmd.OutOrStdout(), "%d\t%s\t%s\t%t\t%s\t%s\n", holder.PID, holder.Age, holder.Resource, holder.InSwarm, holder.CWD, holder.Command)
			}
			return nil
		},
	}
	cmd.Flags().IntVar(&opts.terminatePID, "terminate-pid", 0, "Send SIGTERM to a listed PID (requires --confirm)")
	cmd.Flags().BoolVar(&opts.confirm, "confirm", false, "Confirm termination of --terminate-pid")
	cmd.Flags().BoolVar(&opts.force, "force", false, "Allow termination of an in-swarm PID (requires --confirm)")
	return cmd
}

type contentionProcess struct {
	pid       int
	cwd       string
	command   string
	age       time.Duration
	status    string
	openFiles []string
}

func findContentionHolders(ctx context.Context, swarmCWD string) ([]ContentionHolder, error) {
	processes, err := processutil.ProcessesWithContext(ctx)
	if err != nil {
		return nil, fmt.Errorf("list processes: %w", err)
	}
	now := time.Now()
	holders := make([]ContentionHolder, 0)
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
		for _, resource := range classifyContentionProcess(contentionProcess{
			pid: int(proc.Pid), cwd: cwd, command: command, age: age, status: strings.Join(statuses, ","), openFiles: openFiles,
		}) {
			holders = append(holders, ContentionHolder{
				PID: int(proc.Pid), CWD: cwd, Command: command, Resource: resource,
				Age: age.Round(time.Second).String(), InSwarm: sameOrChildPath(cwd, swarmCWD),
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
	files, err := proc.OpenFilesWithContext(ctx)
	if err != nil {
		return nil
	}
	paths := make([]string, 0, len(files))
	for _, file := range files {
		paths = append(paths, file.Path)
	}
	return paths
}

func classifyContentionProcess(proc contentionProcess) []string {
	command := strings.ToLower(proc.command)
	resources := make([]string, 0, 3)
	hasBeadsDB := false
	for _, path := range proc.openFiles {
		path = filepath.ToSlash(strings.ToLower(path))
		if strings.HasSuffix(path, "/.beads/beads.db") || strings.HasSuffix(path, "/.beads/beads.db-wal") || strings.HasSuffix(path, "/.beads/beads.db-shm") {
			resources = append(resources, "beads_db")
			hasBeadsDB = true
			break
		}
	}
	if !hasBeadsDB && (commandHasExecutable(command, "br") || commandHasExecutable(command, "bd")) {
		resources = append(resources, "beads_db")
	}
	if commandHasExecutable(command, "cargo") {
		resources = append(resources, "cargo_registry")
	}
	if strings.Contains(command, "rsync") {
		resources = append(resources, "rsync")
	}
	if strings.EqualFold(strings.TrimSpace(proc.status), "d") {
		resources = append(resources, "uninterruptible_io")
	}
	return resources
}

func commandHasExecutable(command, executable string) bool {
	fields := strings.Fields(command)
	return len(fields) > 0 && filepath.Base(fields[0]) == executable
}

func sameOrChildPath(candidate, root string) bool {
	if candidate == "" || root == "" {
		return false
	}
	rel, err := filepath.Rel(root, candidate)
	return err == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator))
}

func terminateContentionHolder(ctx context.Context, holders []ContentionHolder, opts contentionCommandOptions) error {
	if opts.terminatePID <= 0 {
		return nil
	}
	if !opts.confirm {
		return errors.New("refusing termination without --confirm")
	}
	var target *ContentionHolder
	for i := range holders {
		if holders[i].PID == opts.terminatePID {
			target = &holders[i]
			break
		}
	}
	if target == nil {
		return fmt.Errorf("PID %d is not a detected contention holder", opts.terminatePID)
	}
	if target.InSwarm && !opts.force {
		return fmt.Errorf("refusing to terminate in-swarm PID %d without --force", opts.terminatePID)
	}
	proc, err := processutil.NewProcess(int32(opts.terminatePID))
	if err != nil {
		return fmt.Errorf("open PID %d: %w", opts.terminatePID, err)
	}
	if err := proc.TerminateWithContext(ctx); err != nil {
		return fmt.Errorf("terminate PID %d: %w", opts.terminatePID, err)
	}
	return nil
}

// DoctorReport contains the full health check report
type DoctorReport struct {
	Timestamp      time.Time        `json:"timestamp"`
	Overall        string           `json:"overall"` // "healthy", "warning", "unhealthy"
	SafetyDefaults SafetyDefaults   `json:"safety_defaults"`
	Tools          []ToolCheck      `json:"tools"`
	Dependencies   []DepCheck       `json:"dependencies"`
	Daemons        []DaemonCheck    `json:"daemons"`
	Configuration  []ConfigCheck    `json:"configuration"`
	Invariants     []InvariantCheck `json:"invariants"`
	Warnings       int              `json:"warnings"`
	Errors         int              `json:"errors"`
}

// SafetyDefaults captures security/privacy defaults derived from config.
type SafetyDefaults struct {
	RedactionMode             string `json:"redaction_mode"`
	RedactionAllowlistEnabled bool   `json:"redaction_allowlist_enabled"`
	RedactionAllowlistCount   int    `json:"redaction_allowlist_count"`
	PrivacyDefaultEnabled     bool   `json:"privacy_default_enabled"`
	EncryptionAtRestEnabled   bool   `json:"encryption_at_rest_enabled"`
	PreflightDefaultStrict    bool   `json:"preflight_default_strict"`
	// ClaudeAgentHooks names the PreToolUse hooks ntm-launched Claude agents
	// carry through --settings (ntm-policy, dcg, rch); empty when none.
	ClaudeAgentHooks []string `json:"claude_agent_hooks"`
	// ClaudePolicyHookRegistered reports that `ntm safety install` registered
	// the policy hook in ~/.claude/settings.json, which covers every Claude
	// session (launches then skip their own copy).
	ClaudePolicyHookRegistered bool `json:"claude_policy_hook_registered"`
	// ClaudeAgentHooksWarning explains why Claude agents would run without
	// the safety policy hook; empty when they are covered.
	ClaudeAgentHooksWarning string `json:"claude_agent_hooks_warning,omitempty"`
}

// InvariantCheck represents a design invariant check result
type InvariantCheck struct {
	ID      string   `json:"id"`
	Name    string   `json:"name"`
	Status  string   `json:"status"` // "ok", "warning", "error"
	Message string   `json:"message,omitempty"`
	Details []string `json:"details,omitempty"`
}

// ToolCheck represents a tool health check result
type ToolCheck struct {
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

// DepCheck represents a dependency check result
type DepCheck struct {
	Name       string `json:"name"`
	Installed  bool   `json:"installed"`
	Version    string `json:"version,omitempty"`
	MinVersion string `json:"min_version,omitempty"`
	Status     string `json:"status"`
	Message    string `json:"message,omitempty"`
}

// DaemonCheck represents a daemon health check result
type DaemonCheck struct {
	Name    string `json:"name"`
	Running bool   `json:"running"`
	Port    int    `json:"port,omitempty"`
	PID     int    `json:"pid,omitempty"`
	Status  string `json:"status"`
	Message string `json:"message,omitempty"`
}

// ConfigCheck represents a configuration check result
type ConfigCheck struct {
	Name    string `json:"name"`
	Valid   bool   `json:"valid"`
	Status  string `json:"status"`
	Message string `json:"message,omitempty"`
}

// doctorExitFailure reports whether an overall verdict must produce a non-zero
// process exit.
//
// Both output paths consult this one predicate so they cannot disagree about
// what counts as failure. They previously did: renderDoctorTUITo mapped
// "unhealthy" onto an error, while runDoctor's JSON branch returned
// outputDoctorJSON's encode error and nothing else — so `ntm doctor --json`
// exited 0 on an unhealthy ecosystem. JSON mode is precisely the mode a
// preflight script gates on, so the surface that mattered was the silent one.
//
// Warnings deliberately do not fail: an optional tool being absent is the
// normal state on most machines, and failing on it would make the command
// useless as a gate. Callers who want to gate on warnings read `.warnings`.
func doctorExitFailure(overall string) bool {
	return overall == "unhealthy"
}

// doctorCheck is the seam tests use to drive runDoctor with a chosen verdict;
// production always runs the real probe. Mirrors healthKernelRun in health.go.
var doctorCheck = performDoctorCheck

func runDoctor(cmd *cobra.Command, args []string) error {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	report := doctorCheck(ctx)

	if IsJSONOutput() {
		if err := outputDoctorJSON(report); err != nil {
			return err
		}
		if doctorExitFailure(report.Overall) {
			// The report itself is the machine-readable failure surface: its
			// `overall` field already says "unhealthy". errJSONFailure makes
			// Execute exit non-zero without encoding a second JSON envelope
			// after the report, which would break a single-document `jq`.
			return errJSONFailure
		}
		return nil
	}

	return renderDoctorTUI(report)
}

func performDoctorCheck(ctx context.Context) *DoctorReport {
	report := &DoctorReport{
		Timestamp: time.Now(),
		Overall:   "healthy",
	}

	report.SafetyDefaults = buildSafetyDefaults(cfg)

	// Check tools using the adapter framework
	report.Tools = checkTools(ctx)

	// Check dependencies (tmux, Go)
	report.Dependencies = checkDependencies(ctx)

	// Check daemons
	report.Daemons = checkDaemons(ctx)

	// Check configuration
	report.Configuration = checkConfiguration()

	// Check design invariants. The tool results computed above are handed over
	// as evidence so Graceful Degradation is measured rather than asserted,
	// without probing every adapter a second time.
	report.Invariants = checkInvariants(ctx, report.Tools)

	// Calculate overall status
	for _, t := range report.Tools {
		switch t.Status {
		case "error":
			report.Errors++
		case "warning":
			report.Warnings++
		}
	}
	for _, d := range report.Dependencies {
		switch d.Status {
		case "error":
			report.Errors++
		case "warning":
			report.Warnings++
		}
	}
	for _, d := range report.Daemons {
		switch d.Status {
		case "error":
			report.Errors++
		case "warning":
			report.Warnings++
		}
	}
	for _, c := range report.Configuration {
		switch c.Status {
		case "error":
			report.Errors++
		case "warning":
			report.Warnings++
		}
	}
	for _, i := range report.Invariants {
		switch i.Status {
		case "error":
			report.Errors++
		case "warning":
			report.Warnings++
		}
	}

	if report.Errors > 0 {
		report.Overall = "unhealthy"
	} else if report.Warnings > 0 {
		report.Overall = "warning"
	}

	return report
}

func checkTools(ctx context.Context) []ToolCheck {
	var checks []ToolCheck

	// Required tools
	requiredTools := map[tools.ToolName]bool{
		tools.ToolBV: true, // Required for priority/insights
		tools.ToolBD: true, // Required for beads tracking
	}

	// Optional tools
	optionalTools := []tools.ToolName{
		tools.ToolAM,
		tools.ToolCM,
		tools.ToolCASS,
		tools.ToolS2P,
	}

	// Check each registered tool
	for _, toolName := range tools.AllTools() {
		adapter, ok := tools.Get(toolName)
		if !ok {
			continue
		}

		displayName := string(toolName)
		if toolName == tools.ToolBD {
			displayName = "br"
		}

		check := ToolCheck{
			Name:     displayName,
			Required: requiredTools[toolName],
		}

		path, installed := adapter.Detect()
		check.Installed = installed
		check.Path = path

		if installed {
			// Get version
			if version, err := adapter.Version(ctx); err == nil {
				check.Version = version.String()
			}

			// Get capabilities
			if caps, err := adapter.Capabilities(ctx); err == nil {
				for _, c := range caps {
					check.Capabilities = append(check.Capabilities, string(c))
				}
			}

			// Health check
			if health, err := adapter.Health(ctx); err == nil && health.Healthy {
				check.Status = "ok"
				check.Message = check.Version
			} else {
				check.Status = "warning"
				check.Message = "health check failed"
			}

			if doctorVerbose {
				check.Details = collectVerboseToolDetails(ctx, adapter)
			}
		} else {
			if check.Required {
				check.Status = "error"
				check.Message = "required tool not found"
			} else {
				check.Status = "warning"
				check.Message = "optional tool not found"
			}
		}

		checks = append(checks, check)
	}

	// Add any optional tools not in registry
	for _, name := range optionalTools {
		found := false
		for _, c := range checks {
			if c.Name == string(name) {
				found = true
				break
			}
		}
		if !found {
			checks = append(checks, ToolCheck{
				Name:     string(name),
				Required: false,
				Status:   "warning",
				Message:  "adapter not registered",
			})
		}
	}

	return checks
}

func summarizeDiagnosticText(raw string) string {
	for _, line := range strings.Split(strings.TrimSpace(raw), "\n") {
		cleaned := strings.Map(func(r rune) rune {
			if r < 0x20 || r == 0x7f {
				return -1
			}
			return r
		}, strings.TrimSpace(line))
		if cleaned == "" {
			continue
		}
		runes := []rune(cleaned)
		if len(runes) > 120 {
			return string(runes[:117]) + "..."
		}
		return cleaned
	}
	return ""
}

func collectVerboseToolDetails(ctx context.Context, adapter tools.Adapter) []string {
	detailCtx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()

	var details []string
	switch a := adapter.(type) {
	case *tools.ACFSAdapter:
		if info, err := a.GetInfo(detailCtx); err == nil && len(bytes.TrimSpace(info)) > 0 {
			if s := summarizeDiagnosticText(string(info)); s != "" {
				details = append(details, "info: "+s)
			}
		}
		if st, err := a.GetStatus(detailCtx); err == nil && len(bytes.TrimSpace(st)) > 0 {
			if s := summarizeDiagnosticText(string(st)); s != "" {
				details = append(details, "status: "+s)
			}
		}
		if doc, err := a.Doctor(detailCtx); err == nil && len(bytes.TrimSpace(doc)) > 0 {
			if s := summarizeDiagnosticText(string(doc)); s != "" {
				details = append(details, "doctor: "+s)
			}
		}
	case *tools.AMAdapter:
		if srvURL := strings.TrimSpace(a.ServerURL()); srvURL != "" {
			details = append(details, "endpoint: "+srvURL)
		}
		if hc, err := a.HealthCheck(detailCtx); err == nil && len(bytes.TrimSpace(hc)) > 0 {
			if s := summarizeDiagnosticText(string(hc)); s != "" {
				details = append(details, "health_check: "+s)
			}
		}
	case *tools.CAAMAdapter:
		avail := a.IsAvailable(detailCtx)
		multi := a.HasMultipleAccounts(detailCtx)
		details = append(details, fmt.Sprintf("accounts_configured=%t multiple_accounts=%t", avail, multi))
		if active, err := a.GetActiveAccount(detailCtx); err == nil && active != nil {
			acct := active.ID
			if active.Name != "" {
				acct = active.Name
			}
			details = append(details, fmt.Sprintf("active_account: %s (%s)", acct, active.Provider))
		}
	case *tools.CASSAdapter:
		if rawCaps, err := a.GetCapabilities(detailCtx); err == nil && len(bytes.TrimSpace(rawCaps)) > 0 {
			if s := summarizeDiagnosticText(string(rawCaps)); s != "" {
				details = append(details, "capabilities_json: "+s)
			}
		}
	case *tools.CMAdapter:
		if onboard, err := a.OnboardStatus(detailCtx); err == nil && len(bytes.TrimSpace(onboard)) > 0 {
			if s := summarizeDiagnosticText(string(onboard)); s != "" {
				details = append(details, "onboard: "+s)
			}
		}
	case *tools.DCGAdapter:
		if st, err := a.GetStatus(detailCtx); err == nil && st != nil {
			details = append(details, fmt.Sprintf("guard_enabled=%t blocked_patterns=%d", st.Enabled, len(st.BlockedPatterns)))
		}
	case *tools.RCHAdapter:
		avail := a.IsAvailable(detailCtx)
		healthy := a.HasHealthyWorkers(detailCtx)
		details = append(details, fmt.Sprintf("compatible=%t healthy_workers=%t", avail, healthy))
		if healthy {
			if best, err := a.SelectWorker(detailCtx, ""); err == nil && best != nil {
				details = append(details, fmt.Sprintf("selected_worker: %s (load=%d%%)", best.Name, best.Load))
			}
		}
	case *tools.RUAdapter:
		if doc, err := a.Doctor(detailCtx); err == nil && strings.TrimSpace(doc) != "" {
			if s := summarizeDiagnosticText(doc); s != "" {
				details = append(details, "doctor: "+s)
			}
		}
	case *tools.UBSAdapter:
		if doc, err := a.Doctor(detailCtx); err == nil && strings.TrimSpace(doc) != "" {
			if s := summarizeDiagnosticText(doc); s != "" {
				details = append(details, "doctor: "+s)
			}
		}
	case *tools.XFAdapter:
		if doc, err := a.Doctor(detailCtx); err == nil && strings.TrimSpace(doc) != "" {
			if s := summarizeDiagnosticText(doc); s != "" {
				details = append(details, "doctor: "+s)
			}
		}
	}

	return details
}

func checkDependencies(ctx context.Context) []DepCheck {
	var checks []DepCheck

	// Check tmux
	tmuxCheck := DepCheck{
		Name:       "tmux",
		MinVersion: "3.0",
	}
	if tmux.DefaultClient.IsInstalled() {
		path := tmux.BinaryPath()
		tmuxCheck.Installed = true
		cmd := exec.CommandContext(ctx, path, "-V")
		cmd.WaitDelay = 2 * time.Second
		if out, err := cmd.Output(); err == nil {
			tmuxCheck.Version = sanitizeDependencyVersion(string(out))
		}
		tmuxCheck.Status = "ok"
	} else {
		tmuxCheck.Status = "error"
		tmuxCheck.Message = "tmux is required for NTM"
	}
	checks = append(checks, tmuxCheck)

	// Check Go version (for building)
	goCheck := DepCheck{
		Name:       "go",
		MinVersion: "1.25",
	}
	if path, err := exec.LookPath("go"); err == nil {
		goCheck.Installed = true
		cmd := exec.CommandContext(ctx, path, "version")
		cmd.WaitDelay = 2 * time.Second
		if out, err := cmd.Output(); err == nil {
			parts := strings.Fields(string(out))
			if len(parts) >= 3 && parts[0] == "go" && parts[1] == "version" {
				goCheck.Version = parts[2]
			} else {
				goCheck.Version = "unknown"
			}
			goCheck.Status = "ok"
		} else {
			goCheck.Status = "warning"
			goCheck.Message = "version check failed"
		}
	} else {
		goCheck.Installed = false
		goCheck.Status = "warning"
		goCheck.Message = "not found (needed for plugins)"
	}
	checks = append(checks, goCheck)

	// Grok Build is optional. Absence is healthy; presence is reported with a
	// sanitized version so doctor output is safe to render or embed in JSON.
	grokStatus, grokVersion, _ := checkDepWithPath(depCheck{
		Name:        "Grok Build (xAI)",
		Command:     "grok",
		VersionArgs: []string{"--version"},
	})
	grokCheck := DepCheck{
		Name:      "grok",
		Installed: grokStatus == "found",
		Version:   grokVersion,
		Status:    "ok",
	}
	if !grokCheck.Installed {
		grokCheck.Message = "optional agent not found"
	}
	checks = append(checks, grokCheck)

	// Oh My Pi is optional too: absence is healthy, presence reports the
	// sanitized `omp --version` ("omp/18.2.3").
	ompStatus, ompVersion, _ := checkDepWithPath(depCheck{
		Name:        "Oh My Pi (omp)",
		Command:     "omp",
		VersionArgs: []string{"--version"},
	})
	ompCheck := DepCheck{
		Name:      "omp",
		Installed: ompStatus == "found",
		Version:   ompVersion,
		Status:    "ok",
	}
	if !ompCheck.Installed {
		ompCheck.Message = "optional agent not found"
	}
	checks = append(checks, ompCheck)

	return checks
}

// daemonVerdict renders the status and message for one daemon probe.
//
// Both arms of this decision used to report "ok", so a daemon that was not
// running drew a green tick reading "port 8765 available" — including one the
// operator had switched on in config. A down daemon is now a warning when it is
// expected, and an unjudged "?" when it is not.
func daemonVerdict(running, expected bool, port int) (status, message string) {
	switch {
	case running:
		return "ok", fmt.Sprintf("listening on port %d", port)
	case expected:
		return "warning", fmt.Sprintf("enabled in config but not listening on port %d", port)
	default:
		return "unknown", fmt.Sprintf("not running (port %d free)", port)
	}
}

func agentMailDaemonAddr() (string, int) {
	const defaultHost = "127.0.0.1"
	const defaultPort = 8765

	raw := strings.TrimSpace(tools.NewAMAdapter().ServerURL())
	if raw == "" {
		return defaultHost, defaultPort
	}
	parsed, err := url.Parse(raw)
	if err != nil {
		return defaultHost, defaultPort
	}
	host := parsed.Hostname()
	if host == "" {
		host = defaultHost
	}
	if portStr := parsed.Port(); portStr != "" {
		if port, err := strconv.Atoi(portStr); err == nil && port > 0 && port <= 65535 {
			return host, port
		}
	}
	switch strings.ToLower(parsed.Scheme) {
	case "https":
		return host, 443
	case "http":
		return host, 80
	default:
		return host, defaultPort
	}
}

func checkDaemons(ctx context.Context) []DaemonCheck {
	var checks []DaemonCheck

	amHost, amPort := agentMailDaemonAddr()

	// Check common daemon ports in deterministic order
	type daemonPort struct {
		name     string
		host     string
		port     int
		expected bool
	}
	// expected records whether this daemon is supposed to be up, so a daemon
	// that is down can be reported as a problem rather than as a tick.
	daemons := []daemonPort{
		{"agent-mail", amHost, amPort, cfg != nil && cfg.AgentMail.Enabled},
		{"cm-server", "127.0.0.1", 8766, false},
	}

	dialer := &net.Dialer{Timeout: time.Second}

	for _, dp := range daemons {
		check := DaemonCheck{
			Name: dp.name,
			Port: dp.port,
		}

		// Check if port is in use (use context for cancellation)
		conn, err := dialer.DialContext(ctx, "tcp", net.JoinHostPort(dp.host, strconv.Itoa(dp.port)))
		if err == nil {
			conn.Close()
			check.Running = true
		}
		check.Status, check.Message = daemonVerdict(check.Running, dp.expected, dp.port)

		checks = append(checks, check)
	}

	return checks
}

func checkConfiguration() []ConfigCheck {
	var checks []ConfigCheck

	// Check .ntm directory
	homeDir, err := os.UserHomeDir()
	if err != nil {
		checks = append(checks, ConfigCheck{
			Name:    ".ntm directory",
			Status:  "error",
			Message: "cannot determine home directory",
		})
	} else {
		ntmDir := filepath.Join(homeDir, ".ntm")
		if info, err := os.Stat(ntmDir); err == nil && info.IsDir() {
			// Check if writable
			testFile := filepath.Join(ntmDir, ".doctor_test")
			if f, err := os.Create(testFile); err == nil {
				f.Close()
				os.Remove(testFile)
				checks = append(checks, ConfigCheck{
					Name:    ".ntm directory",
					Valid:   true,
					Status:  "ok",
					Message: "exists and writable",
				})
			} else {
				checks = append(checks, ConfigCheck{
					Name:    ".ntm directory",
					Valid:   false,
					Status:  "error",
					Message: "not writable",
				})
			}
		} else {
			checks = append(checks, ConfigCheck{
				Name:    ".ntm directory",
				Valid:   false,
				Status:  "warning",
				Message: "does not exist (will be created)",
			})
		}
	}

	// Check for .beads directory in current directory
	if _, err := os.Stat(".beads"); err == nil {
		checks = append(checks, ConfigCheck{
			Name:    ".beads directory",
			Valid:   true,
			Status:  "ok",
			Message: "project has beads initialized",
		})
	} else {
		checks = append(checks, ConfigCheck{
			Name:    ".beads directory",
			Valid:   false,
			Status:  "warning",
			Message: "no beads in current directory",
		})
	}

	// Pre-commit guard degraded runs (bd-ws1-truth-safety-l5ddi.1): fail-open
	// hook runs are recorded in the state DB precisely because a WARN in
	// commit scrollback is unobserved; doctor is where they become visible.
	checks = append(checks, guardDegradationCheck())

	// The one fail-open guard path that can never record a ledger row
	// (bd-2c0yh.4): hook installed but `ntm` missing from PATH.
	checks = append(checks, guardHookPathCheck())

	// Removed config knobs (WS6-remove-finalize, bd-ws6-config-truth-ienmd.3):
	// keys removed in v1.26.0 are hard strict-loader errors since v1.27.0.
	// Doctor scans the config file leniently (ScanRemovedKnobs), so it can
	// still name each removed key + disposition even though the strict loader
	// refuses to load such a config.
	checks = append(checks, removedKnobChecks()...)

	// Deprecated config knobs (bd-6otuk, v1.28.0 batch): hard strict-loader
	// errors since v1.29.0. Doctor scans the config file leniently
	// (ScanDeprecatedKnobs), so it can still name each key + disposition even
	// though the strict loader refuses to load such a config.
	checks = append(checks, deprecatedKnobChecks()...)

	return checks
}

// deprecatedKnobChecks reports every deprecated-in-v1.28.0 config key present
// in the active config file, one error per key, with the exact disposition
// text the strict loader uses in its load error.
func deprecatedKnobChecks() []ConfigCheck {
	knobs, err := config.ScanDeprecatedKnobs(selectedConfigPath())
	if err != nil {
		return []ConfigCheck{{
			Name:    "deprecated config keys",
			Valid:   false,
			Status:  "warning",
			Message: fmt.Sprintf("could not scan config for deprecated keys: %v", err),
		}}
	}
	if len(knobs) == 0 {
		return []ConfigCheck{{
			Name:    "deprecated config keys",
			Valid:   true,
			Status:  "ok",
			Message: "no deprecated config keys present",
		}}
	}
	checks := make([]ConfigCheck, 0, len(knobs))
	for _, knob := range knobs {
		checks = append(checks, ConfigCheck{
			Name:    "deprecated config key: " + knob.Key,
			Valid:   false,
			Status:  "error",
			Message: deadKnobDoctorMessage(knob),
		})
	}
	return checks
}

// removedKnobChecks reports every removed config key present in the active
// config file, one error per key, with the exact disposition text the strict
// loader uses in its load error and that key's own removal-batch provenance.
func removedKnobChecks() []ConfigCheck {
	knobs, err := config.ScanRemovedKnobs(selectedConfigPath())
	if err != nil {
		return []ConfigCheck{{
			Name:    "removed config keys",
			Valid:   false,
			Status:  "warning",
			Message: fmt.Sprintf("could not scan config for removed keys: %v", err),
		}}
	}
	if len(knobs) == 0 {
		return []ConfigCheck{{
			Name:    "removed config keys",
			Valid:   true,
			Status:  "ok",
			Message: "no removed config keys present",
		}}
	}
	checks := make([]ConfigCheck, 0, len(knobs))
	for _, knob := range knobs {
		checks = append(checks, ConfigCheck{
			Name:    "removed config key: " + knob.Key,
			Valid:   false,
			Status:  "error",
			Message: deadKnobDoctorMessage(knob),
		})
	}
	return checks
}

// deadKnobDoctorMessage renders doctor's per-key remediation line.
//
// The release text comes from the key's OWN removal batch. This function used
// to be two copies that hard-coded "since v1.27.0" and "since v1.29.0", which
// was fine while each scan returned exactly one batch — and became wrong the
// moment ScanRemovedKnobs started carrying a later batch too, telling users a
// key had failed loading since a release in which it still worked (ntm#323).
func deadKnobDoctorMessage(knob config.RemovedKnob) string {
	if provenance := config.DeadKeyTierProvenance(knob.Tier); provenance != "" {
		return fmt.Sprintf("%s (%s) — delete it from your config file", knob.Disposition, provenance)
	}
	return fmt.Sprintf("%s — delete it from your config file", knob.Disposition)
}

func buildSafetyDefaults(cfg *config.Config) SafetyDefaults {
	if cfg == nil {
		cfg = config.Default()
	}

	redactionMode := strings.TrimSpace(cfg.Redaction.Mode)
	if redactionMode == "" {
		redactionMode = config.DefaultRedactionConfig().Mode
	}

	allowlistCount := len(cfg.Redaction.Allowlist)

	defaults := SafetyDefaults{
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

// claudeAgentHookCoverage reports whether the Claude agents ntm launches are
// covered by the safety policy hook: through --settings on the launch command,
// or through the hook `ntm safety install` registered for every Claude session.
func claudeAgentHookCoverage(cfg *config.Config, hooks *[]string) (registered bool, warning string) {
	if home, err := os.UserHomeDir(); err == nil {
		script := policy.ClaudeHookScriptPath(home)
		ok, regErr := policy.ClaudeHookRegistered(policy.ClaudeUserSettingsPath(home), script)
		registered = regErr == nil && ok && fileExists(script)
	}
	launch := policy.ClaudeAgentLaunchSettings(cfg)
	*hooks = append(*hooks, launch.Sources...)

	template := cfg.Agents.Claude
	if strings.TrimSpace(template) == "" {
		template = config.DefaultAgentTemplates().Claude
	}
	rendered, err := config.GenerateAgentCommand(template, config.AgentTemplateVars{AgentType: "cc", ClaudeSettings: launch.Settings})
	switch {
	case err != nil:
		return registered, fmt.Sprintf("the [agents] claude command does not render: %v", err)
	case config.ClaudeHooksNotAppliedWarning(rendered, launch.Settings) != "":
		*hooks = (*hooks)[:0]
		if registered {
			return registered, ""
		}
		return registered, "the [agents] claude command cannot carry --settings; add --settings {{shellQuote .ClaudeSettings}} to it"
	case !registered && !slices.Contains(launch.Sources, "ntm-policy"):
		return registered, "[safety] claude_policy_hook = false and no hook registered by `ntm safety install`"
	}
	return registered, ""
}

// toolAvailabilityFromChecks converts doctor's tool results into the evidence
// the Graceful Degradation invariant is measured from.
//
// "Reported" means the unavailable tool was actually surfaced to the operator —
// a non-ok status with a message. A tool that is missing while doctor shows it
// as fine is the silent degradation the invariant exists to catch.
func toolAvailabilityFromChecks(tools []ToolCheck) []invariants.ToolAvailability {
	out := make([]invariants.ToolAvailability, 0, len(tools))
	for _, t := range tools {
		out = append(out, invariants.ToolAvailability{
			Name:      t.Name,
			Available: t.Installed,
			Reported:  t.Installed || (t.Status != "ok" && t.Message != ""),
		})
	}
	return out
}

// liveSchemaConstraints returns the table definitions from the state database,
// so the Idempotent Orchestration invariant can verify that retry safety is
// enforced by the schema rather than merely intended.
func liveSchemaConstraints() ([]string, error) {
	store, err := state.Open("")
	if err != nil {
		return nil, fmt.Errorf("open state store: %w", err)
	}
	defer func() { _ = store.Close() }()

	// Deliberately does not migrate. doctor is a diagnostic, and migrating here
	// would have it silently rewrite the operator's schema as a side effect of
	// being asked a question — on a database still at the previous release that
	// means running 023's table rebuild. An unmigrated database reports as
	// unverified instead, which is the truth.
	db := store.DB()
	if db == nil {
		return nil, fmt.Errorf("state store exposes no database handle")
	}
	rows, err := db.Query(`SELECT sql FROM sqlite_master WHERE type = 'table' AND sql IS NOT NULL`)
	if err != nil {
		return nil, fmt.Errorf("read schema: %w", err)
	}
	defer func() { _ = rows.Close() }()

	var stmts []string
	for rows.Next() {
		var stmt string
		if err := rows.Scan(&stmt); err != nil {
			return nil, fmt.Errorf("scan schema row: %w", err)
		}
		stmts = append(stmts, stmt)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate schema: %w", err)
	}
	return stmts, nil
}

func checkInvariants(ctx context.Context, tools []ToolCheck) []InvariantCheck {
	var checks []InvariantCheck

	// Get current working directory for the checker
	cwd, err := os.Getwd()
	if err != nil {
		cwd = "."
	}

	checker := invariants.NewChecker(cwd).
		WithToolReport(func() []invariants.ToolAvailability {
			return toolAvailabilityFromChecks(tools)
		}).
		WithSchemaConstraints(liveSchemaConstraints)
	report := checker.CheckAll(ctx)

	// Convert invariant results to InvariantCheck format
	defs := invariants.Definitions()
	for _, id := range invariants.AllInvariants() {
		result, ok := report.Results[id]
		if !ok {
			continue
		}

		def := defs[id]
		checks = append(checks, InvariantCheck{
			ID:      string(id),
			Name:    def.Name,
			Status:  result.Status,
			Message: result.Message,
			Details: result.Details,
		})
	}

	return checks
}

func outputDoctorJSON(report *DoctorReport) error {
	return encodeDoctorJSON(os.Stdout, report)
}

func encodeDoctorJSON(w io.Writer, report *DoctorReport) error {
	encoder := json.NewEncoder(w)
	encoder.SetIndent("", "  ")
	return encoder.Encode(report)
}

func renderDoctorTUI(report *DoctorReport) error {
	return renderDoctorTUITo(os.Stdout, report)
}

func renderDoctorTUITo(w io.Writer, report *DoctorReport) error {
	// Define styles
	titleStyle := lipgloss.NewStyle().
		Bold(true).
		Foreground(lipgloss.Color("99"))

	sectionStyle := lipgloss.NewStyle().
		Bold(true).
		Foreground(lipgloss.Color("75")).
		MarginTop(1)

	okStyle := lipgloss.NewStyle().Foreground(lipgloss.Color("42"))     // Green
	warnStyle := lipgloss.NewStyle().Foreground(lipgloss.Color("214"))  // Orange
	errorStyle := lipgloss.NewStyle().Foreground(lipgloss.Color("196")) // Red
	mutedStyle := lipgloss.NewStyle().Foreground(lipgloss.Color("240")) // Gray

	boxStyle := lipgloss.NewStyle().
		Border(lipgloss.RoundedBorder()).
		BorderForeground(lipgloss.Color("240")).
		Padding(0, 1).
		MarginTop(1)

	// Status icon helper
	statusIcon := func(s string) string {
		switch s {
		case "ok":
			return okStyle.Render("✓")
		case "warning":
			return warnStyle.Render("⚠")
		case "error":
			return errorStyle.Render("✗")
		default:
			return mutedStyle.Render("?")
		}
	}

	// Title
	fmt.Fprintln(w)
	fmt.Fprintln(w, titleStyle.Render("NTM Doctor"))
	fmt.Fprintln(w)

	// Tools section
	fmt.Fprintln(w, sectionStyle.Render("Tools:"))
	for _, t := range report.Tools {
		icon := statusIcon(t.Status)
		if t.Installed {
			caps := ""
			if len(t.Capabilities) > 0 && doctorVerbose {
				caps = fmt.Sprintf(" (%v)", t.Capabilities)
			}
			fmt.Fprintf(w, "  %s %s %s%s\n", icon, t.Name, mutedStyle.Render(t.Message), caps)
			if doctorVerbose {
				for _, detail := range t.Details {
					fmt.Fprintf(w, "      %s\n", mutedStyle.Render(detail))
				}
			}
		} else {
			msg := t.Message
			if t.Required {
				msg = errorStyle.Render(msg)
			} else {
				msg = mutedStyle.Render(msg)
			}
			fmt.Fprintf(w, "  %s %s %s\n", icon, t.Name, msg)
		}
	}

	// Dependencies section
	fmt.Fprintln(w, sectionStyle.Render("Dependencies:"))
	for _, d := range report.Dependencies {
		icon := statusIcon(d.Status)
		version := ""
		if d.Version != "" {
			version = mutedStyle.Render(fmt.Sprintf(" (%s)", d.Version))
		}
		fmt.Fprintf(w, "  %s %s%s\n", icon, d.Name, version)
	}

	// Daemons section
	fmt.Fprintln(w, sectionStyle.Render("Daemons:"))
	for _, d := range report.Daemons {
		icon := statusIcon(d.Status)
		fmt.Fprintf(w, "  %s %s %s\n", icon, d.Name, mutedStyle.Render(d.Message))
	}

	// Configuration section
	fmt.Fprintln(w, sectionStyle.Render("Configuration:"))
	for _, c := range report.Configuration {
		icon := statusIcon(c.Status)
		fmt.Fprintf(w, "  %s %s %s\n", icon, c.Name, mutedStyle.Render(c.Message))
	}

	// Safety defaults section
	fmt.Fprintln(w, sectionStyle.Render("Safety Defaults:"))
	redactionStatus := statusIcon("ok")
	if report.SafetyDefaults.RedactionMode == "off" {
		redactionStatus = statusIcon("warning")
	}
	allowlistSummary := "none"
	if report.SafetyDefaults.RedactionAllowlistEnabled {
		allowlistSummary = fmt.Sprintf("%d entries", report.SafetyDefaults.RedactionAllowlistCount)
	}
	fmt.Fprintf(w, "  %s Redaction mode: %s (allowlist: %s)\n",
		redactionStatus,
		mutedStyle.Render(report.SafetyDefaults.RedactionMode),
		mutedStyle.Render(allowlistSummary),
	)

	privacyStatus := statusIcon("ok")
	privacyLabel := "disabled"
	if report.SafetyDefaults.PrivacyDefaultEnabled {
		privacyLabel = "enabled"
	}
	fmt.Fprintf(w, "  %s Privacy default: %s\n", privacyStatus, mutedStyle.Render(privacyLabel))

	encryptionStatus := statusIcon("ok")
	encryptionLabel := "disabled"
	if report.SafetyDefaults.EncryptionAtRestEnabled {
		encryptionLabel = "enabled"
	}
	fmt.Fprintf(w, "  %s Encryption at rest: %s\n", encryptionStatus, mutedStyle.Render(encryptionLabel))

	preflightStatus := statusIcon("ok")
	strictLabel := "strict=off"
	if report.SafetyDefaults.PreflightDefaultStrict {
		strictLabel = "strict=on"
	}
	fmt.Fprintf(w, "  %s Prompt preflight: %s\n", preflightStatus, mutedStyle.Render(strictLabel))

	hooksStatus, hooksLabel := statusIcon("ok"), ""
	switch {
	case report.SafetyDefaults.ClaudeAgentHooksWarning != "":
		hooksStatus, hooksLabel = statusIcon("warning"), "unprotected: "+report.SafetyDefaults.ClaudeAgentHooksWarning
	case len(report.SafetyDefaults.ClaudeAgentHooks) > 0:
		hooksLabel = strings.Join(report.SafetyDefaults.ClaudeAgentHooks, ", ") + " via --settings"
		if report.SafetyDefaults.ClaudePolicyHookRegistered {
			hooksLabel += "; policy hook registered in ~/.claude/settings.json"
		}
	default:
		hooksLabel = "policy hook registered in ~/.claude/settings.json"
	}
	fmt.Fprintf(w, "  %s Claude agent hooks: %s\n", hooksStatus, mutedStyle.Render(hooksLabel))

	// Invariants section
	fmt.Fprintln(w, sectionStyle.Render("Design Invariants:"))
	for _, i := range report.Invariants {
		icon := statusIcon(i.Status)
		fmt.Fprintf(w, "  %s %s %s\n", icon, i.Name, mutedStyle.Render(i.Message))
		if doctorVerbose && len(i.Details) > 0 {
			for _, detail := range i.Details {
				fmt.Fprintf(w, "      %s\n", mutedStyle.Render(detail))
			}
		}
	}

	// Overall status box
	var statusMsg string
	var statusStyle lipgloss.Style
	switch report.Overall {
	case "healthy":
		statusMsg = "Overall: HEALTHY"
		statusStyle = okStyle
	case "warning":
		statusMsg = fmt.Sprintf("Overall: HEALTHY (%d warnings)", report.Warnings)
		statusStyle = warnStyle
	case "unhealthy":
		statusMsg = fmt.Sprintf("Overall: UNHEALTHY (%d errors, %d warnings)", report.Errors, report.Warnings)
		statusStyle = errorStyle
	}
	fmt.Fprintln(w, boxStyle.Render(statusStyle.Render(statusMsg)))
	fmt.Fprintln(w)

	// Return error to indicate non-healthy status (Cobra will set appropriate
	// exit code). Shares doctorExitFailure with the JSON path so both surfaces
	// agree on the exit contract.
	if doctorExitFailure(report.Overall) {
		return errors.New("ecosystem is unhealthy")
	}
	return nil
}
