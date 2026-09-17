package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
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
