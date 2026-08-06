// Package robot provides machine-readable output for AI agents and automation.
// productivity.go reports bounded, attributable evidence for swarm progress.
package robot

import (
	"context"
	"encoding/json"
	"fmt"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/Dicklesworthstone/ntm/internal/tmux"
	processutil "github.com/shirou/gopsutil/v4/process"
)

const (
	defaultProductivityWindow = 30 * time.Minute
	productivityReadTimeout   = 5 * time.Second
)

// ProductivityDecision answers whether the available evidence supports
// continuing work, a converged stop state, or neither conclusion.
type ProductivityDecision string

const (
	ProductivityContinue  ProductivityDecision = "continue"
	ProductivityConverged ProductivityDecision = "converged"
	ProductivityUnknown   ProductivityDecision = "unknown"
)

// ProductivityOptions configures one point-in-time productivity observation.
type ProductivityOptions struct {
	Session string
	Window  time.Duration
}

// ProductivityOutput is the response for --robot-productivity. It deliberately
// exposes the raw evidence that produced Decision so callers do not need to
// reconstruct a stop/continue verdict from shell commands.
type ProductivityOutput struct {
	RobotResponse
	Session          string               `json:"session"`
	WindowSeconds    int                  `json:"window_seconds"`
	Decision         ProductivityDecision `json:"decision"`
	DecisionReason   string               `json:"decision_reason"`
	ReadyBeadCount   int                  `json:"ready_bead_count"`
	ReadyBeadDelta   int                  `json:"ready_bead_delta,omitempty"`
	Panes            []ProductivityPane   `json:"panes"`
	BuildProcesses   []BuildProcess       `json:"build_processes"`
	EvidenceComplete bool                 `json:"evidence_complete"`
}

// ProductivityPane contains the attributable forward-progress evidence for one
// agent pane. The current working directory remains private; only its derived
// evidence is returned.
type ProductivityPane struct {
	Pane      string            `json:"pane"`
	AgentType string            `json:"agent_type"`
	Progress  *SemanticProgress `json:"progress"`
	Builds    []BuildProcess    `json:"builds"`
}

// BuildProcess describes a live build or test process whose current working
// directory exactly matches an agent pane's directory.
type BuildProcess struct {
	PID     int    `json:"pid"`
	Command string `json:"command"`
}

// ConvergenceState is the durable-in-memory state a wait loop needs between
// productivity observations. It deliberately contains no filesystem path or
// process-global state: callers own its lifecycle instead of recreating the
// former /tmp streak files.
type ConvergenceState struct {
	PreviousReadyBeadCount int
	HasPreviousReadyCount  bool
	ConvergedStreak        int
}

type productivityDependencies struct {
	sessionExists func(string) bool
	getPanes      func(string) ([]tmux.Pane, error)
	panePath      func(context.Context, string) string
	processes     func(context.Context) ([]productivityProcess, error)
	readyBeads    func(context.Context, string) (int, error)
	now           func() time.Time
}

type productivityProcess struct {
	pid     int
	cwd     string
	command string
}

// GetProductivity collects native, bounded evidence for whether a session is
// still producing work. It never infers convergence from unavailable git,
// Beads, tmux, or process data; those cases return "unknown".
func GetProductivity(opts ProductivityOptions) (*ProductivityOutput, error) {
	return getProductivity(opts, defaultProductivityDependencies())
}

// PrintProductivity executes the productivity observation and emits the
// canonical robot envelope. It mirrors the other robot print helpers so CLI
// dispatchers never have to reimplement JSON/exit-code behavior.
func PrintProductivity(opts ProductivityOptions) int {
	output, err := GetProductivity(opts)
	if err != nil {
		output = &ProductivityOutput{
			RobotResponse:  NewErrorResponse(err, ErrCodeInternalError, "Retry after checking tmux, git, and Beads availability"),
			Session:        opts.Session,
			Panes:          []ProductivityPane{},
			BuildProcesses: []BuildProcess{},
		}
	}
	return printLegacyRobotOutput(output, output.RobotResponse, ExitCodeForResponse(output.RobotResponse), "robot productivity failed")
}

func defaultProductivityDependencies() productivityDependencies {
	return productivityDependencies{
		sessionExists: tmux.SessionExists,
		getPanes:      tmux.GetPanes,
		panePath:      paneCurrentPathForTarget,
		processes:     liveBuildProcesses,
		readyBeads:    readyBeadCount,
		now:           time.Now,
	}
}

func getProductivity(opts ProductivityOptions, deps productivityDependencies) (*ProductivityOutput, error) {
	window := opts.Window
	if window <= 0 {
		window = defaultProductivityWindow
	}
	output := &ProductivityOutput{
		RobotResponse:  NewRobotResponse(true),
		Session:        opts.Session,
		WindowSeconds:  int(window / time.Second),
		Decision:       ProductivityUnknown,
		ReadyBeadCount: 0,
		Panes:          []ProductivityPane{},
		BuildProcesses: []BuildProcess{},
	}
	if strings.TrimSpace(opts.Session) == "" {
		output.RobotResponse = NewErrorResponse(fmt.Errorf("session is required"), ErrCodeInvalidFlag, "Pass --robot-productivity=SESSION")
		return output, nil
	}
	if !deps.sessionExists(opts.Session) {
		output.RobotResponse = NewErrorResponse(fmt.Errorf("session %q not found", opts.Session), ErrCodeSessionNotFound, "Use --robot-status to list sessions")
		return output, nil
	}
	now := deps.now().UTC()

	panes, err := deps.getPanes(opts.Session)
	if err != nil {
		output.RobotResponse = NewErrorResponse(fmt.Errorf("list session panes: %w", err), ErrCodeInternalError, "Retry after checking tmux")
		return output, nil
	}

	ctx, cancel := context.WithTimeout(context.Background(), productivityReadTimeout)
	defer cancel()
	processes, processErr := deps.processes(ctx)
	readyCount, beadsErr := deps.readyBeads(ctx, paneProjectDir(opts.Session, panes, deps.panePath, ctx))
	if beadsErr == nil {
		output.ReadyBeadCount = readyCount
	}

	associatedBuilds := make(map[int]productivityProcess)
	agentPaneSeen := false
	for _, pane := range panes {
		if pane.Type == tmux.AgentUser || pane.Type == tmux.AgentUnknown {
			continue
		}
		agentPaneSeen = true
		path := deps.panePath(ctx, pane.ID)
		progress := PaneSemanticProgress(PaneAddr{Session: opts.Session, Window: pane.WindowIndex, Pane: pane.Index}, path, window, false, now)
		paneBuilds := matchingBuildProcesses(processes, path)
		for _, build := range paneBuilds {
			associatedBuilds[build.pid] = build
		}
		output.Panes = append(output.Panes, ProductivityPane{
			Pane:      pane.Ref().Physical(),
			AgentType: string(pane.Type),
			Progress:  progress,
			Builds:    publicBuildProcesses(paneBuilds),
		})
	}
	sort.Slice(output.Panes, func(i, j int) bool { return output.Panes[i].Pane < output.Panes[j].Pane })
	for _, build := range associatedBuilds {
		output.BuildProcesses = append(output.BuildProcesses, BuildProcess{PID: build.pid, Command: build.command})
	}
	sort.Slice(output.BuildProcesses, func(i, j int) bool { return output.BuildProcesses[i].PID < output.BuildProcesses[j].PID })

	output.EvidenceComplete = agentPaneSeen && processErr == nil && beadsErr == nil
	output.Decision, output.DecisionReason = evaluateProductivity(output)
	return output, nil
}

func paneProjectDir(session string, panes []tmux.Pane, panePath func(context.Context, string) string, ctx context.Context) string {
	for _, pane := range panes {
		if pane.Type == tmux.AgentUser || pane.Type == tmux.AgentUnknown {
			continue
		}
		if path := strings.TrimSpace(panePath(ctx, pane.ID)); path != "" {
			return path
		}
	}
	return ""
}

func evaluateProductivity(output *ProductivityOutput) (ProductivityDecision, string) {
	if !output.EvidenceComplete {
		return ProductivityUnknown, "productivity evidence is incomplete; do not infer convergence"
	}
	for _, pane := range output.Panes {
		if len(pane.Builds) > 0 {
			return ProductivityContinue, "live build or test process is associated with an agent pane"
		}
		if pane.Progress != nil && (pane.Progress.CommitsInWindow > 0 || pane.Progress.ClaimsInWindow > 0) {
			return ProductivityContinue, "token-attributed commits or Beads updates occurred within the observation window"
		}
	}
	if output.ReadyBeadCount > 0 {
		return ProductivityUnknown, "ready work remains but no attributable activity was observed"
	}
	return ProductivityConverged, "no ready work, live builds, or token-attributed progress observed"
}

// AdvanceConvergenceState records one productivity observation and returns
// whether the configured number of consecutive converged observations has been
// reached. A ready-bead count change always resets the streak, even if the
// current point-in-time report otherwise looks converged.
func AdvanceConvergenceState(state ConvergenceState, output *ProductivityOutput, requiredStreak int) (ConvergenceState, bool) {
	if requiredStreak <= 0 {
		requiredStreak = 1
	}
	if output == nil {
		state.ConvergedStreak = 0
		return state, false
	}
	if state.HasPreviousReadyCount {
		output.ReadyBeadDelta = output.ReadyBeadCount - state.PreviousReadyBeadCount
	}
	readyChanged := state.HasPreviousReadyCount && output.ReadyBeadDelta != 0
	state.PreviousReadyBeadCount = output.ReadyBeadCount
	state.HasPreviousReadyCount = true

	if output.Decision != ProductivityConverged || readyChanged {
		state.ConvergedStreak = 0
		return state, false
	}
	state.ConvergedStreak++
	return state, state.ConvergedStreak >= requiredStreak
}

func matchingBuildProcesses(processes []productivityProcess, paneDir string) []productivityProcess {
	paneDir = cleanPath(paneDir)
	if paneDir == "" {
		return nil
	}
	matched := make([]productivityProcess, 0)
	for _, process := range processes {
		if cleanPath(process.cwd) == paneDir {
			matched = append(matched, process)
		}
	}
	return matched
}

func publicBuildProcesses(processes []productivityProcess) []BuildProcess {
	output := make([]BuildProcess, 0, len(processes))
	for _, process := range processes {
		output = append(output, BuildProcess{PID: process.pid, Command: process.command})
	}
	sort.Slice(output, func(i, j int) bool { return output[i].PID < output[j].PID })
	return output
}

func cleanPath(path string) string {
	path = strings.TrimSpace(path)
	if path == "" {
		return ""
	}
	return filepath.Clean(path)
}

func liveBuildProcesses(ctx context.Context) ([]productivityProcess, error) {
	processes, err := processutil.ProcessesWithContext(ctx)
	if err != nil {
		return nil, err
	}
	output := make([]productivityProcess, 0)
	for _, process := range processes {
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		command, err := process.CmdlineWithContext(ctx)
		if err != nil || !isBuildOrTestCommand(command) {
			continue
		}
		cwd, err := process.CwdWithContext(ctx)
		if err != nil || strings.TrimSpace(cwd) == "" {
			continue
		}
		output = append(output, productivityProcess{pid: int(process.Pid), cwd: cwd, command: command})
	}
	return output, nil
}

func isBuildOrTestCommand(command string) bool {
	fields := strings.Fields(command)
	if len(fields) == 0 {
		return false
	}
	binary := strings.ToLower(filepath.Base(fields[0]))
	return binary == "go" || binary == "cargo" || binary == "rustc" || binary == "bun"
}

func readyBeadCount(ctx context.Context, dir string) (int, error) {
	if strings.TrimSpace(dir) == "" {
		return 0, fmt.Errorf("no pane working directory")
	}
	command := exec.CommandContext(ctx, "br", "ready", "--json")
	command.Dir = dir
	raw, err := command.Output()
	if err != nil {
		return 0, err
	}
	var issues []json.RawMessage
	if err := json.Unmarshal(raw, &issues); err != nil {
		return 0, err
	}
	return len(issues), nil
}
