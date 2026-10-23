package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/Dicklesworthstone/ntm/internal/state"
	"github.com/Dicklesworthstone/ntm/internal/tmux"
)

func TestContextInjectCobraPackModes(t *testing.T) {
	oldJSON := jsonOutput
	jsonOutput = true
	t.Cleanup(func() { jsonOutput = oldJSON })
	for _, scenario := range []string{"build", "stored", "dry_run", "partial_failure", "invalid_flags"} {
		t.Run(scenario, func(t *testing.T) {
			deps := packInjectionTestDeps(t)
			calls := 0
			deps.send = func(_ context.Context, pane tmux.Pane, _ string) error {
				calls++
				if scenario == "partial_failure" && pane.ID == "%2" {
					return errors.New("transport unavailable")
				}
				return nil
			}
			deps.load = func(id string) (*state.ContextPack, error) {
				return &state.ContextPack{ID: id, AgentType: "cc", RenderedPrompt: "stored artifact"}, nil
			}
			args := []string{"demo", "--build", "--files", "main.go"}
			switch scenario {
			case "stored":
				args = []string{"demo", "--pack", "pack-stored", "--pane", "0"}
			case "dry_run":
				args = append(args, "--dry-run")
			case "invalid_flags":
				args = append(args, "--pack", "pack-stored")
			}
			cmd := newContextInjectCmdWithDeps(&deps)
			var stdout bytes.Buffer
			cmd.SetOut(&stdout)
			cmd.SetErr(io.Discard)
			cmd.SilenceErrors, cmd.SilenceUsage = true, true
			cmd.SetArgs(args)
			err := cmd.Execute()
			var result ContextInjectResult
			dec := json.NewDecoder(&stdout)
			if decodeErr := dec.Decode(&result); decodeErr != nil {
				t.Fatalf("Cobra did not emit the result to its writer: %v", decodeErr)
			}
			var extra any
			if decodeErr := dec.Decode(&extra); decodeErr != io.EOF {
				t.Fatalf("expected one JSON document: %v", decodeErr)
			}
			switch scenario {
			case "build":
				if err != nil || !result.Success || calls != 3 || result.Mode != "build" {
					t.Fatalf("build surface: %+v err=%v calls=%d", result, err, calls)
				}
			case "stored":
				if err != nil || !result.Success || calls != 1 || result.Deliveries[0].PackID != "pack-stored" {
					t.Fatalf("stored surface: %+v err=%v calls=%d", result, err, calls)
				}
			case "dry_run":
				if err != nil || !result.DryRun || calls != 0 || len(result.PanesInjected) != 0 || len(result.PanesPlanned) != 3 {
					t.Fatalf("dry-run surface: %+v err=%v calls=%d", result, err, calls)
				}
			case "partial_failure":
				if err == nil || result.Success || len(result.PanesInjected) != 2 || result.Deliveries[2].Status != "uncertain" {
					t.Fatalf("failure envelope/exit: %+v err=%v", result, err)
				}
			case "invalid_flags":
				if err == nil || result.Success || calls != 0 {
					t.Fatalf("invalid flags admitted: %+v err=%v calls=%d", result, err, calls)
				}
			}
		})
	}
}

func TestContextPackInjectionFlagsReachableFromContextCommand(t *testing.T) {
	cmd, _, err := newContextCmd().Find([]string{"inject"})
	if err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"build", "pack", "files", "task", "bead", "pane", "dry-run"} {
		if cmd.Flags().Lookup(name) == nil {
			t.Fatalf("context inject is missing --%s", name)
		}
	}
}

func TestContextInjectCobraUsesCanonicalTargetsAcrossModes(t *testing.T) {
	oldJSON := jsonOutput
	jsonOutput = true
	t.Cleanup(func() { jsonOutput = oldJSON })
	for _, mode := range []string{"files", "build", "pack"} {
		t.Run(mode, func(t *testing.T) {
			for _, scenario := range []string{"default", "qualified", "pane-id", "ambiguous", "all", "no-agents"} {
				t.Run(scenario, func(t *testing.T) {
					deps := packInjectionTestDeps(t)
					projectDir, err := deps.project(t.Context(), "demo")
					if err != nil {
						t.Fatal(err)
					}
					if err := os.WriteFile(filepath.Join(projectDir, "README.md"), []byte("Context for the selected project"), 0600); err != nil {
						t.Fatal(err)
					}
					panes := []tmux.Pane{
						{ID: "%10", WindowIndex: 0, Index: 0, Type: tmux.AgentClaude},
						{ID: "%11", WindowIndex: 0, Index: 1, Type: tmux.AgentClaude},
						{ID: "%20", WindowIndex: 1, Index: 0, Type: "claude-code"},
						{ID: "%21", WindowIndex: 1, Index: 1, Type: tmux.AgentClaude},
						{ID: "%30", WindowIndex: 2, Index: 0, Type: tmux.AgentUser},
						{ID: "%31", WindowIndex: 2, Index: 1, Type: "unknown"},
					}
					if scenario == "no-agents" {
						panes = panes[4:]
					}
					deps.panes = func(string) ([]tmux.Pane, error) { return panes, nil }
					deps.load = func(id string) (*state.ContextPack, error) {
						return &state.ContextPack{ID: id, AgentType: "cc", RenderedPrompt: "stored context"}, nil
					}
					var sent []string
					deps.send = func(_ context.Context, pane tmux.Pane, content string) error {
						if strings.TrimSpace(content) == "" {
							t.Fatal("selected pane received empty context")
						}
						sent = append(sent, pane.ID)
						return nil
					}
					args := []string{"demo"}
					switch mode {
					case "build":
						args = append(args, "--build", "--files", "main.go")
					case "pack":
						args = append(args, "--pack", "saved-context")
					}
					want := []string{"%10", "%11", "%20", "%21"}
					wantError := ""
					switch scenario {
					case "qualified":
						args = append(args, "--pane", "1.1")
						want = []string{"%21"}
					case "pane-id":
						args = append(args, "--pane", "%20")
						want = []string{"%20"}
					case "ambiguous":
						args = append(args, "--pane", "1")
						wantError = "matched 2 panes"
					case "all":
						args = append(args, "--all")
						if mode == "files" {
							want = append(want, "%30", "%31")
						} else {
							wantError = "not a known agent"
						}
					case "no-agents":
						wantError = "no target panes"
					}
					cmd := newContextInjectCmdWithDeps(&deps)
					var stdout bytes.Buffer
					cmd.SetOut(&stdout)
					cmd.SetErr(io.Discard)
					cmd.SilenceErrors, cmd.SilenceUsage = true, true
					cmd.SetArgs(args)
					err = cmd.ExecuteContext(t.Context())
					var result ContextInjectResult
					if decodeErr := json.Unmarshal(stdout.Bytes(), &result); decodeErr != nil {
						t.Fatalf("command did not emit one result: %v; output=%q; error=%v", decodeErr, stdout.String(), err)
					}
					if wantError != "" {
						if err == nil || result.Success || len(sent) != 0 || !strings.Contains(result.Error, wantError) {
							t.Fatalf("invalid target reached delivery: result=%+v sent=%v error=%v", result, sent, err)
						}
						if scenario == "ambiguous" && (!strings.Contains(result.Error, "1.0") || !strings.Contains(result.Error, "1.1")) {
							t.Fatalf("ambiguity error omitted exact target alternatives: %s", result.Error)
						}
						return
					}
					if err != nil || !result.Success || !reflect.DeepEqual(sent, want) || len(result.Deliveries) != len(want) {
						t.Fatalf("command selected wrong targets: result=%+v sent=%v want=%v error=%v", result, sent, want, err)
					}
					for i, receipt := range result.Deliveries {
						if receipt.Target != want[i] || receipt.Status != "delivered" {
							t.Fatalf("receipt lost selected physical target: %+v", receipt)
						}
					}
				})
			}
		})
	}
}
