package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/Dicklesworthstone/ntm/internal/history"
	"github.com/Dicklesworthstone/ntm/internal/robot"
	"github.com/Dicklesworthstone/ntm/internal/tmux"
)

func TestNewReplayResponseInitializesStableJSONFields(t *testing.T) {
	response := newReplayResponse(true)
	if response.Targets == nil || response.Warnings == nil {
		t.Fatalf("response slices must be present: %+v", response)
	}
	if !response.DryRun || response.Success || response.Delivered != 0 || response.Failed != 0 {
		t.Fatalf("unexpected initial response: %+v", response)
	}
	if _, err := time.Parse(time.RFC3339Nano, response.Timestamp); err != nil {
		t.Fatalf("timestamp %q is not RFC3339: %v", response.Timestamp, err)
	}
}

func TestSelectReplayEntry(t *testing.T) {
	entries := []history.HistoryEntry{
		{ID: "older-entry", Prompt: "older"},
		{ID: "newer-entry", Prompt: "newer"},
	}
	tests := []struct {
		name       string
		arg        string
		last       bool
		wantPrompt string
		wantError  string
	}{
		{name: "implicit latest", wantPrompt: "newer"},
		{name: "explicit latest", arg: "older", last: true, wantPrompt: "newer"},
		{name: "one based newest index", arg: "1", wantPrompt: "newer"},
		{name: "one based older index", arg: "2", wantPrompt: "older"},
		{name: "newest matching prefix", arg: "new", wantPrompt: "newer"},
		{name: "out of range", arg: "3", wantError: "out of range"},
		{name: "missing prefix", arg: "missing", wantError: "no entry found"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			entry, err := selectReplayEntry(entries, test.arg, test.last)
			if test.wantError != "" {
				if err == nil || !strings.Contains(err.Error(), test.wantError) {
					t.Fatalf("error=%v, want substring %q", err, test.wantError)
				}
				return
			}
			if err != nil {
				t.Fatalf("selectReplayEntry: %v", err)
			}
			if entry.Prompt != test.wantPrompt {
				t.Fatalf("prompt=%q, want %q", entry.Prompt, test.wantPrompt)
			}
		})
	}
	if _, err := selectReplayEntry(nil, "", false); err == nil || !strings.Contains(err.Error(), "no history") {
		t.Fatalf("empty history error=%v", err)
	}
}

func TestSelectReplayTargetsAndCanonicalKeys(t *testing.T) {
	panes := []tmux.Pane{
		{ID: "%1", WindowIndex: 0, Index: 0, Type: tmux.AgentUser},
		{ID: "%2", WindowIndex: 0, Index: 1, Type: tmux.AgentClaude},
		{ID: "%3", WindowIndex: 1, Index: 0, Type: tmux.AgentCodex},
		{ID: "%4", WindowIndex: 1, Index: 1, Type: tmux.AgentGemini},
		{ID: "%5", WindowIndex: 1, Index: 2, Type: tmux.AgentAntigravity},
		{ID: "%6", WindowIndex: 1, Index: 3, Type: tmux.AgentClaude, Service: "cm"},
	}
	tests := []struct {
		name                   string
		cc, cod, gmi, agy, all bool
		want                   []string
	}{
		{name: "default excludes user and service", want: []string{"0.1", "1.0", "1.1", "1.2"}},
		{name: "claude excludes service", cc: true, want: []string{"0.1"}},
		{name: "codex and gemini", cod: true, gmi: true, want: []string{"1.0", "1.1"}},
		{name: "antigravity", agy: true, want: []string{"1.2"}},
		{name: "all includes user but excludes service", all: true, want: []string{"0.0", "0.1", "1.0", "1.1", "1.2"}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			targets := selectReplayTargets(panes, test.cc, test.cod, test.gmi, test.agy, test.all)
			if got := replayTargetKeys(panes, targets); !reflect.DeepEqual(got, test.want) {
				t.Fatalf("keys=%v, want %v", got, test.want)
			}
		})
	}

	singleWindow := panes[:2]
	if got := replayTargetKeys(singleWindow, singleWindow[1:]); !reflect.DeepEqual(got, []string{"1"}) {
		t.Fatalf("single-window keys=%v, want [1]", got)
	}
}

func TestReplayFailureClassificationAndCounting(t *testing.T) {
	if got := replayFailureCode(errors.Join(errors.New("dispatch"), context.Canceled), robot.ErrCodePromptSendFailed); got != robot.ErrCodeTimeout {
		t.Fatalf("canceled code=%q, want %q", got, robot.ErrCodeTimeout)
	}
	if got := replayFailureCode(context.DeadlineExceeded, robot.ErrCodeInternalError); got != robot.ErrCodeTimeout {
		t.Fatalf("deadline code=%q, want %q", got, robot.ErrCodeTimeout)
	}
	if got := replayFailureCode(errors.New("send failed"), robot.ErrCodePromptSendFailed); got != robot.ErrCodePromptSendFailed {
		t.Fatalf("ordinary code=%q, want %q", got, robot.ErrCodePromptSendFailed)
	}
	for _, test := range []struct {
		targets, delivered, want int
	}{
		{targets: 3, delivered: 0, want: 3},
		{targets: 3, delivered: 1, want: 2},
		{targets: 3, delivered: 3, want: 0},
		{targets: 1, delivered: 2, want: 0},
	} {
		if got := replayFailedCount(test.targets, test.delivered); !reflect.DeepEqual(got, test.want) {
			t.Fatalf("replayFailedCount(%d, %d)=%d, want %d", test.targets, test.delivered, got, test.want)
		}
	}
}

func TestReplayHistoryWarnings(t *testing.T) {
	appendFailure := errors.New("history is read-only")
	var captured *history.HistoryEntry
	warnings := replayHistoryWarnings(false, "proj", []string{"0.1"}, []string{"claude"}, "work", func(entry *history.HistoryEntry) error {
		captured = entry
		return appendFailure
	})
	if len(warnings) != 1 || !strings.Contains(warnings[0], appendFailure.Error()) {
		t.Fatalf("warnings=%v, want append failure", warnings)
	}
	if captured == nil || !captured.Success {
		t.Fatalf("captured history entry=%+v", captured)
	}
	if got, want := []any{captured.Source, captured.Session, captured.Prompt}, []any{history.SourceReplay, "proj", "work"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("captured history identity=%v, want %v", got, want)
	}
	if !reflect.DeepEqual(captured.Targets, []string{"0.1"}) || !reflect.DeepEqual(captured.AgentTypes, []string{"claude"}) {
		t.Fatalf("captured history topology=%+v", captured)
	}

	called := false
	warnings = replayHistoryWarnings(true, "proj", nil, nil, "work", func(*history.HistoryEntry) error {
		called = true
		return nil
	})
	if called || warnings == nil || len(warnings) != 0 {
		t.Fatalf("no-history called=%v warnings=%v, want no append and []", called, warnings)
	}
}

func TestReplayJSONProcessFailuresOwnExactlyOneDocument(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping process-level replay JSON contract test in short mode")
	}
	tmpDir := t.TempDir()
	for _, dir := range []string{
		filepath.Join(tmpDir, "home"),
		filepath.Join(tmpDir, "config"),
		filepath.Join(tmpDir, "data", "ntm"),
	} {
		if err := os.MkdirAll(dir, 0o700); err != nil {
			t.Fatalf("create %s: %v", dir, err)
		}
	}
	entry := history.NewEntry("replay-process-session", nil, "process prompt", history.SourceCLI)
	entry.SetSuccess()
	encoded, err := json.Marshal(entry)
	if err != nil {
		t.Fatalf("marshal history entry: %v", err)
	}
	if err := os.WriteFile(filepath.Join(tmpDir, "data", "ntm", "history.jsonl"), append(encoded, '\n'), 0o600); err != nil {
		t.Fatalf("write history: %v", err)
	}

	tests := []struct {
		name        string
		args        []string
		extraArgs   []string
		wantCode    string
		wantSession string
	}{
		{name: "execution requires yes", wantCode: replayErrCodeConfirmationRequired, wantSession: "replay-process-session"},
		{name: "editor is rejected", extraArgs: []string{"--edit"}, wantCode: robot.ErrCodeInvalidFlag, wantSession: "replay-process-session"},
		{name: "missing history selector", args: []string{"--json", "replay", "missing-prefix"}, wantCode: robot.ErrCodeInvalidFlag},
		{name: "missing session", extraArgs: []string{"--yes"}, wantCode: robot.ErrCodeSessionNotFound, wantSession: "replay-process-session"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			args := append([]string(nil), test.args...)
			if len(args) == 0 {
				args = []string{"--json", "replay", "--last"}
			}
			args = append(args, test.extraArgs...)
			rawArgs, err := json.Marshal(args)
			if err != nil {
				t.Fatalf("marshal helper args: %v", err)
			}
			ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
			defer cancel()
			cmd := exec.CommandContext(ctx, os.Args[0], "-test.run=^TestRobotProcessContractHelper$")
			cmd.Dir = tmpDir
			cmd.Env = envWithOverrides(os.Environ(),
				"HOME="+filepath.Join(tmpDir, "home"),
				"XDG_CONFIG_HOME="+filepath.Join(tmpDir, "config"),
				"XDG_DATA_HOME="+filepath.Join(tmpDir, "data"),
				"NTM_CONFIG=",
				"NTM_NO_COLOR=1",
				"NTM_ROBOT_CONTRACT_ARGS="+string(rawArgs),
			)
			var stdout, stderr bytes.Buffer
			cmd.Stdout = &stdout
			cmd.Stderr = &stderr
			err = cmd.Run()
			var exitErr *exec.ExitError
			if !errors.As(err, &exitErr) || exitErr.ExitCode() != 1 {
				t.Fatalf("process error=%v, want exit 1; stdout=%q stderr=%q", err, stdout.String(), stderr.String())
			}
			if got := strings.TrimSpace(stderr.String()); got != "" {
				t.Fatalf("stderr=%q, want empty", got)
			}
			var response ReplayResponse
			if err := json.Unmarshal(stdout.Bytes(), &response); err != nil {
				t.Fatalf("stdout is not exactly one JSON document: %v; stdout=%q", err, stdout.String())
			}
			if response.Success || response.ErrorCode != test.wantCode || response.Error == "" {
				t.Fatalf("response=%+v, want failure code %q", response, test.wantCode)
			}
			if !reflect.DeepEqual(response.Session, test.wantSession) || response.Targets == nil || response.Warnings == nil {
				t.Fatalf("response lost stable fields: %+v", response)
			}
		})
	}
}
