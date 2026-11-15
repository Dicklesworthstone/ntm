package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/Dicklesworthstone/ntm/internal/ensemble"
	statepkg "github.com/Dicklesworthstone/ntm/internal/state"
	"github.com/Dicklesworthstone/ntm/internal/tmux"
)

func TestEnsembleForceRecoversOnlySavedOutputsAfterRefreshFailure(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("fake tmux requires a POSIX shell")
	}
	for _, tc := range []struct {
		name            string
		force           bool
		corruptSaved    bool
		failPersistence bool
		contextError    error
		wantError       string
	}{
		{name: "without force", wantError: "refresh ensemble outputs"},
		{name: "force saved recovery", force: true},
		{name: "force rejects invalid saved output", force: true, corruptSaved: true, wantError: "no valid outputs"},
		{name: "force after persistence failure", force: true, failPersistence: true},
		{name: "force canceled", force: true, contextError: context.Canceled},
		{name: "force deadline", force: true, contextError: context.DeadlineExceeded},
	} {
		t.Run(tc.name, func(t *testing.T) {
			isolateSessionAgentStorage(t)
			ensemble.CloseDefaultStateStore()
			t.Cleanup(ensemble.CloseDefaultStateStore)
			oldJSON, oldConfig := jsonOutput, cfg
			jsonOutput, cfg = false, nil
			t.Cleanup(func() { jsonOutput, cfg = oldJSON, oldConfig })

			const session = "cli-force-recovery"
			dir := t.TempDir()
			logPath := filepath.Join(dir, "tmux.log")
			t.Setenv("NTM_FORCE_RECOVERY_LOG", logPath)
			savedOutput := ensemble.ModeOutput{
				ModeID:      "deductive",
				Thesis:      "Only the durable saved analysis should be used.",
				Confidence:  0.8,
				TopFindings: []ensemble.Finding{{Finding: "Keep the saved finding.", Impact: ensemble.ImpactHigh, Confidence: 0.8}},
			}
			savedPath := filepath.Join(dir, "saved.json")
			savedJSON, err := json.Marshal(savedOutput)
			if err != nil {
				t.Fatal(err)
			}
			if tc.corruptSaved {
				savedJSON = []byte("not valid saved output")
			}
			if err := os.WriteFile(savedPath, savedJSON, 0o600); err != nil {
				t.Fatal(err)
			}
			liveOutput := savedOutput
			liveOutput.ModeID = "inductive"
			liveOutput.Thesis = "Discard this unpublished live analysis."
			liveOutput.TopFindings = []ensemble.Finding{{Finding: "Discard the unpublished live finding.", Impact: ensemble.ImpactHigh, Confidence: 0.8}}
			liveJSON, err := json.Marshal(liveOutput)
			if err != nil {
				t.Fatal(err)
			}
			livePath := filepath.Join(dir, "live.txt")
			if err := os.WriteFile(livePath, []byte("\x60\x60\x60json\n"+string(liveJSON)+"\n\x60\x60\x60\nclaude>\n"), 0o600); err != nil {
				t.Fatal(err)
			}
			t.Setenv("NTM_FORCE_RECOVERY_LIVE", livePath)
			script := strings.Join([]string{
				"#!/bin/sh",
				"printf '%s\\n' \"$1\" >> \"$NTM_FORCE_RECOVERY_LOG\"",
				"case \"$1\" in",
				"  -V) echo 'tmux 3.5a' ;;",
				"  has-session) exit 0 ;;",
				"  list-sessions) echo 'cli-force-recovery' ;;",
				"  list-panes) printf '%s\\n' '%1_NTM_SEP_0_NTM_SEP_cli-force-recovery__cc_1_NTM_SEP_claude_NTM_SEP_80_NTM_SEP_24_NTM_SEP_1_NTM_SEP_0_NTM_SEP_0_NTM_SEP_cc_NTM_SEP__NTM_SEP__NTM_SEP_0_NTM_SEP_[]' '%2_NTM_SEP_1_NTM_SEP_cli-force-recovery__cc_2_NTM_SEP_claude_NTM_SEP_80_NTM_SEP_24_NTM_SEP_0_NTM_SEP_0_NTM_SEP_0_NTM_SEP_cc_NTM_SEP__NTM_SEP__NTM_SEP_0_NTM_SEP_[]' ;;",
				"  capture-pane) cat \"$NTM_FORCE_RECOVERY_LIVE\" ;;",
				"  *) exit 1 ;;",
				"esac",
			}, "\n") + "\n"
			binaryPath := filepath.Join(dir, "tmux")
			if err := os.WriteFile(binaryPath, []byte(script), 0o755); err != nil {
				t.Fatal(err)
			}
			t.Setenv("NTM_TMUX_BINARY", binaryPath)
			oldClient := tmux.DefaultClient
			tmux.DefaultClient = tmux.NewClient("")
			t.Cleanup(func() { tmux.DefaultClient = oldClient })

			state := &ensemble.EnsembleSession{
				SessionName:       session,
				Question:          "Recover saved reasoning when live observation fails.",
				Status:            ensemble.EnsembleActive,
				SynthesisStrategy: ensemble.StrategyConsensus,
				Assignments: []ensemble.ModeAssignment{
					{ModeID: "deductive", PaneName: "%1", AgentType: "cc", Status: ensemble.AssignmentDone, OutputPath: savedPath},
					{ModeID: "inductive", PaneName: "%2", AgentType: "cc", Status: ensemble.AssignmentActive},
				},
			}
			if !tc.failPersistence {
				state.Assignments = append(state.Assignments, ensemble.ModeAssignment{
					ModeID: "abductive", PaneName: "%99", AgentType: "cc", Status: ensemble.AssignmentActive,
				})
			}
			if err := ensemble.SaveSession(session, state); err != nil {
				t.Fatal(err)
			}
			if tc.failPersistence {
				path := filepath.Join(filepath.Dir(statepkg.DefaultPath()), "ensemble-outputs")
				if err := os.WriteFile(path, []byte("block capture directory"), 0o600); err != nil {
					t.Fatal(err)
				}
			}

			ctx := t.Context()
			if tc.contextError == context.Canceled {
				var cancel context.CancelFunc
				ctx, cancel = context.WithCancel(ctx)
				cancel()
			} else if tc.contextError == context.DeadlineExceeded {
				var cancel context.CancelFunc
				ctx, cancel = context.WithDeadline(ctx, time.Now().Add(-time.Second))
				defer cancel()
			}
			var buf bytes.Buffer
			err = runEnsembleSynthesize(ctx, &buf, session, synthesizeOptions{
				Force: tc.force, Format: "json", UseCache: true,
			})
			switch {
			case tc.contextError != nil:
				if !errors.Is(err, tc.contextError) {
					t.Fatalf("context error = %v, want %v", err, tc.contextError)
				}
			case tc.wantError != "":
				if err == nil || !strings.Contains(err.Error(), tc.wantError) {
					t.Fatalf("error = %v, want %q", err, tc.wantError)
				}
			default:
				if err != nil {
					t.Fatal(err)
				}
				var response struct {
					Synthesis *ensemble.SynthesisResult `json:"synthesis"`
				}
				if err := json.Unmarshal(buf.Bytes(), &response); err != nil || response.Synthesis == nil {
					t.Fatalf("invalid result: %v\n%s", err, buf.String())
				}
				result := response.Synthesis
				if result.SynthesizedBy != ensemble.SynthesisPathMechanical ||
					!strings.Contains(result.FallbackReason, "validated saved outputs") ||
					!strings.Contains(result.FallbackReason, "failed") ||
					result.Summary != savedOutput.Thesis || len(result.Findings) != 1 ||
					result.Findings[0].Finding != savedOutput.TopFindings[0].Finding {
					t.Fatalf("forced recovery accepted live work or hid failure: %+v", result)
				}
			}
			log, err := os.ReadFile(logPath)
			if err != nil && !errors.Is(err, os.ErrNotExist) {
				t.Fatal(err)
			}
			for _, command := range []string{"load-buffer", "paste-buffer", "send-keys", "display-message"} {
				if strings.Contains(string(log), command) {
					t.Fatalf("recovery reached live cache or lead: %s", log)
				}
			}
			reloaded, err := ensemble.LoadSession(session)
			if err != nil {
				t.Fatal(err)
			}
			for _, assignment := range reloaded.Assignments[1:] {
				if assignment.Status != ensemble.AssignmentActive || assignment.OutputPath != "" {
					t.Fatalf("failed refresh published live work: %+v", assignment)
				}
			}
		})
	}
}
