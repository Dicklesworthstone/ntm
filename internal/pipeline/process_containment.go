package pipeline

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"syscall"
	"time"
)

const (
	ContainmentTierCgroupV2       = "cgroup_v2"
	ContainmentTierSubreaperPIDFD = "subreaper_pidfd"
	ContainmentTierProcPGID       = "proc_pgid_fallback"

	ContainmentTierEnv     = "NTM_CONTAINMENT_TIER"
	CgroupRootEnv          = "NTM_CGROUP_ROOT"
	StepReaperBinEnv       = "NTM_STEP_REAPER_BIN"
	StepReaperTestAllowEnv = "NTM_STEP_REAPER_TEST_ENABLE"

	StepReaperSubcommand = "internal-step-reaper"

	stepExitReceiptVersion = 1
)

// commandGroupProcStatOpens counts /proc/<pid>/stat reads performed by the
// Tier-3 procfs PGID census so tests and benchmarks can assert zero opens on
// the cgroup_v2 and subreaper_pidfd containment tiers without strace.
var commandGroupProcStatOpens atomic.Int64

type commandStartOptions struct {
	RecordLaunchMetadata bool
	ExitCodeFile         string
	StepID               string
	StepHash             string
}

type commandContainmentMetadata struct {
	Tier                 string
	LeaderPID            int
	LeaderStartTimeTicks uint64
	CgroupPath           string
	ExitCodeFile         string
}

// StepExitReceipt records the terminal outcome written atomically by the child
// wrapper or subreaper when a command step finishes. If the parent ntm
// orchestrator crashes (e.g. SIGKILL) mid-step, resume reconciles this receipt
// once (leader_pid, leader_start_time_ticks) and any cgroup leaf are confirmed
// quiescent.
type StepExitReceipt struct {
	Version              int       `json:"version"`
	StepID               string    `json:"step_id"`
	StepHash             string    `json:"step_hash"`
	LeaderPID            int       `json:"leader_pid,omitempty"`
	LeaderStartTimeTicks uint64    `json:"leader_start_time_ticks,omitempty"`
	ExitCode             int       `json:"exit_code"`
	Output               string    `json:"output,omitempty"`
	OutputCaptured       bool      `json:"output_captured,omitempty"`
	FinishedAt           time.Time `json:"finished_at,omitempty"`
}

func validContainmentTier(tier string) bool {
	switch tier {
	case ContainmentTierCgroupV2, ContainmentTierSubreaperPIDFD, ContainmentTierProcPGID:
		return true
	default:
		return false
	}
}

func stepExitReceiptPath(projectDir, runID, stepID string, startedAt time.Time) string {
	sum := sha256.Sum256([]byte(stepID))
	idDigest := hex.EncodeToString(sum[:8])
	fileName := fmt.Sprintf("%s-%d.json", idDigest, startedAt.UTC().UnixNano())
	return filepath.Join(projectDir, ".ntm", "pipelines", "step-receipts", runID, fileName)
}

func writeStepExitReceiptAtomic(path string, receipt StepExitReceipt) error {
	if strings.TrimSpace(path) == "" {
		return nil
	}
	if receipt.Version == 0 {
		receipt.Version = stepExitReceiptVersion
	}
	if receipt.FinishedAt.IsZero() {
		receipt.FinishedAt = time.Now().UTC()
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	encoded, err := json.Marshal(receipt)
	if err != nil {
		return err
	}
	tmpPath := fmt.Sprintf("%s.%d.tmp", path, os.Getpid())
	if err := os.WriteFile(tmpPath, encoded, 0o600); err != nil {
		return err
	}
	return os.Rename(tmpPath, path)
}

func readStepExitReceipt(path string) (StepExitReceipt, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return StepExitReceipt{}, err
	}
	var receipt StepExitReceipt
	if err := json.Unmarshal(data, &receipt); err != nil {
		return StepExitReceipt{}, fmt.Errorf("decode step exit receipt %q: %w", path, err)
	}
	return receipt, nil
}

func shellSingleQuote(value string) string {
	return "'" + strings.ReplaceAll(value, "'", "'\\''") + "'"
}

// wrapShellCommandForExitReceipt installs a POSIX EXIT trap that atomically
// writes the step's exit code to exitFile when the command finishes naturally
// or via exit/trap, while leaving default signal dispositions (e.g. SIGTERM,
// SIGPIPE), positional parameters, heredocs, and set -eu untouched.
func wrapShellCommandForExitReceipt(expandedCmd, exitFile, stepID, stepHash string) string {
	if strings.TrimSpace(exitFile) == "" {
		return expandedCmd
	}
	stepIDJSON, err := json.Marshal(stepID)
	if err != nil {
		return expandedCmd
	}
	stepHashJSON, err := json.Marshal(stepHash)
	if err != nil {
		return expandedCmd
	}
	tmpFile := exitFile + ".tmp"
	quotedID := shellSingleQuote(string(stepIDJSON))
	quotedHash := shellSingleQuote(string(stepHashJSON))
	quotedTmp := shellSingleQuote(tmpFile)
	quotedFinal := shellSingleQuote(exitFile)
	trapBody := fmt.Sprintf(
		`__ntm_ec=$?; { printf '{"version":1,"step_id":%%s,"step_hash":%%s,"leader_pid":%%d,"exit_code":%%d}\n' %s %s "$$" "$__ntm_ec" > %s && mv -f %s %s; } 2>/dev/null || :; exit "$__ntm_ec"`,
		quotedID, quotedHash, quotedTmp, quotedTmp, quotedFinal,
	)
	return "trap " + shellSingleQuote(trapBody) + " EXIT\n" + expandedCmd
}

// parseCgroupPopulated parses the `populated` key from a cgroup v2
// `cgroup.events` payload (`populated 0` or `populated 1`).
func parseCgroupPopulated(data []byte) (bool, error) {
	for _, line := range strings.Split(string(data), "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		fields := strings.Fields(line)
		if len(fields) == 2 && fields[0] == "populated" {
			switch fields[1] {
			case "0":
				return false, nil
			case "1":
				return true, nil
			default:
				return false, fmt.Errorf("invalid cgroup.events populated value %q", fields[1])
			}
		}
	}
	return false, errors.New("cgroup.events missing populated field")
}

func stepRequiresCapturedStdout(workflow *Workflow, stepID string, exitCode int) bool {
	if workflow == nil {
		return false
	}
	graph := NewDependencyGraph(workflow)
	var target *Step
	if resolved, ok := graph.GetStep(stepID); ok && resolved != nil {
		target = resolved
	} else if scoped, _, ok := graph.ResolveScopedRuntimeStep(stepID); ok && scoped != nil {
		target = scoped
	}
	if target != nil {
		if strings.TrimSpace(target.OutputVar) != "" {
			return true
		}
		if target.OutputParse.Type != "" && target.OutputParse.Type != "none" {
			return true
		}
		if exitCode != 0 && len(target.OnFailure.Fallback) > 0 {
			return true
		}
	}
	rawJSON, err := json.Marshal(workflow)
	if err == nil {
		needleOut := "steps." + stepID + ".output"
		needleData := "steps." + stepID + ".data"
		if strings.Contains(string(rawJSON), needleOut) || strings.Contains(string(rawJSON), needleData) {
			return true
		}
	}
	return false
}

// reconcileInterruptedCommandExecution inspects a running foreground
// CommandExecutionState during resume. Note that `unresolved` records and steps
// already fenced as `execution_unknown` by a prior supervisor pass are never
// auto-reconciled here because a live supervisor already ran and explicitly
// refused settlement (deferring recovery to explicit operator acceptance).
// For interrupted `running` records, it deterministically distinguishes:
//   - still-running step leader (matching leader_pid + leader_start_time_ticks)
//   - still-populated step cgroup (cgroup.events populated 1)
//   - recycled numeric PID (leader_start_time_ticks mismatch — never signaled)
//   - quiescent step with a verified atomic exit_code_file receipt -> settled!
func reconcileInterruptedCommandExecution(workflow *Workflow, state *ExecutionState, stepID string, record CommandExecutionState) (CommandExecutionState, bool, error) {
	if record.Status != commandExecutionRunning || record.Background {
		return record, false, nil
	}
	if state != nil {
		if priorStep, exists := state.Steps[stepID]; exists && priorStep.Error != nil && priorStep.Error.Type == "execution_unknown" {
			return record, false, nil
		}
	}
	hasCrashMetadata := record.LeaderPID > 0 || record.CgroupPath != "" || record.ExitCodeFile != ""
	if !hasCrashMetadata {
		return record, false, nil
	}

	if record.LeaderPID > 0 && record.LeaderStartTimeTicks > 0 {
		alive, pidReused, inspectErr := inspectLiveLeaderIdentity(record.LeaderPID, record.LeaderStartTimeTicks)
		if inspectErr != nil {
			slog.Warn("command receipt reconciliation could not verify leader liveness",
				"step_id", stepID,
				"leader_pid", record.LeaderPID,
				"error", inspectErr,
			)
			return record, false, nil
		}
		if alive {
			return record, false, fmt.Errorf(
				"command step %q leader (PID %d, start_time_ticks %d) is still running; refusing to kill or replay healthy step: %w",
				stepID, record.LeaderPID, record.LeaderStartTimeTicks,
				uncertainCommandExecutionError(stepID, record.ProcessID),
			)
		}
		if pidReused {
			slog.Warn("command receipt reconciliation detected leader PID reuse",
				"step_id", stepID,
				"leader_pid", record.LeaderPID,
				"recorded_start_time_ticks", record.LeaderStartTimeTicks,
			)
		}
	}

	cgroupEmptyReadyToRemove := false
	if record.CgroupPath != "" {
		populated, exists, cgErr := inspectCgroupPathPopulated(record.CgroupPath)
		if cgErr != nil {
			slog.Warn("command receipt reconciliation could not inspect step cgroup",
				"step_id", stepID,
				"cgroup_path", record.CgroupPath,
				"error", cgErr,
			)
			return record, false, nil
		}
		if exists && populated {
			return record, false, fmt.Errorf(
				"command step %q cgroup %q still has live descendants (populated 1); refusing to kill or replay running step: %w",
				stepID, record.CgroupPath,
				uncertainCommandExecutionError(stepID, record.ProcessID),
			)
		}
		cgroupEmptyReadyToRemove = exists && !populated
	}

	if strings.TrimSpace(record.ExitCodeFile) == "" {
		return record, false, nil
	}
	receipt, err := readStepExitReceipt(record.ExitCodeFile)
	if err != nil {
		if !errors.Is(err, os.ErrNotExist) && !errors.Is(err, syscall.ENOENT) {
			slog.Warn("command receipt reconciliation rejected unreadable exit receipt",
				"step_id", stepID,
				"exit_code_file", record.ExitCodeFile,
				"error", err,
			)
		}
		return record, false, nil
	}
	if receipt.Version != stepExitReceiptVersion ||
		receipt.StepID != stepID ||
		receipt.StepHash != record.StepHash ||
		receipt.ExitCode < 0 || receipt.ExitCode > 255 {
		slog.Warn("command receipt reconciliation rejected mismatched exit receipt",
			"step_id", stepID,
			"exit_code_file", record.ExitCodeFile,
			"receipt_version", receipt.Version,
			"receipt_step_id", receipt.StepID,
			"receipt_exit_code", receipt.ExitCode,
		)
		return record, false, nil
	}
	if receipt.LeaderPID > 0 && record.LeaderPID > 0 && receipt.LeaderPID != record.LeaderPID {
		slog.Warn("command receipt reconciliation rejected exit receipt with mismatched leader_pid",
			"step_id", stepID,
			"recorded_leader_pid", record.LeaderPID,
			"receipt_leader_pid", receipt.LeaderPID,
		)
		return record, false, nil
	}
	if receipt.LeaderStartTimeTicks > 0 && record.LeaderStartTimeTicks > 0 && receipt.LeaderStartTimeTicks != record.LeaderStartTimeTicks {
		slog.Warn("command receipt reconciliation rejected exit receipt with mismatched leader_start_time_ticks",
			"step_id", stepID,
			"recorded_start_time_ticks", record.LeaderStartTimeTicks,
			"receipt_start_time_ticks", receipt.LeaderStartTimeTicks,
		)
		return record, false, nil
	}
	if !receipt.OutputCaptured && stepRequiresCapturedStdout(workflow, stepID, receipt.ExitCode) {
		return record, false, nil
	}
	if cgroupEmptyReadyToRemove {
		_ = os.Remove(record.CgroupPath)
	}

	now := time.Now()
	finishedAt := receipt.FinishedAt
	if finishedAt.IsZero() || finishedAt.Before(record.StartedAt) {
		finishedAt = now
	}
	settledAt := now
	if settledAt.Before(finishedAt) {
		settledAt = finishedAt
	}

	stepResult := StepResult{
		StepID:     stepID,
		AgentType:  "command",
		StartedAt:  record.StartedAt,
		FinishedAt: finishedAt,
		Output:     strings.TrimSpace(receipt.Output),
	}
	if receipt.ExitCode == 0 {
		stepResult.Status = StatusCompleted
	} else {
		stepResult.Status = StatusFailed
		stepForErr := &Step{ID: stepID}
		if workflow != nil {
			graph := NewDependencyGraph(workflow)
			if resolved, ok := graph.GetStep(stepID); ok && resolved != nil {
				stepForErr = resolved
			} else if scoped, _, ok := graph.ResolveScopedRuntimeStep(stepID); ok && scoped != nil {
				stepForErr = scoped
			}
		}
		stepResult.Error = commandExitStepError(stepForErr, receipt.ExitCode)
	}

	record.Status = commandExecutionSettled
	record.SettledAt = settledAt
	record.Result = stepResult
	record.Reconciled = true

	slog.Info("command step receipt auto-reconciled after orchestrator interruption",
		"step_id", stepID,
		"leader_pid", record.LeaderPID,
		"leader_start_time_ticks", record.LeaderStartTimeTicks,
		"containment_tier", record.ContainmentTier,
		"exit_code", receipt.ExitCode,
	)
	return record, true, nil
}
