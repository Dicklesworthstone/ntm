package cli

import (
	"context"
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Dicklesworthstone/ntm/internal/workflow"
)

func TestWorkflowCheckpointStatusAndRecoverySurface(t *testing.T) {
	root := t.TempDir()
	store := &workflow.StateStore{Dir: filepath.Join(root, ".ntm", "workflows", "state")}
	state := &workflow.WorkflowState{
		SessionName: "s", WorkflowName: "flow", CurrentStage: "review", ResumeVersion: 1, Turn: 4,
		Agents: map[string]string{"%1": "review"}, Variables: map[string]string{"secret": "PRIVATE_SETUP_CONTEXT"},
		Dispatches: []workflow.StageDispatch{{Pane: "%1", Role: "review", Turn: 4, Status: "sending"}},
	}
	if err := store.Save(state); err != nil {
		t.Fatal(err)
	}
	status, err := getWorkflowCheckpoint(context.Background(), root, "s", "", "", "", false)
	if err != nil || !status.Success || len(status.Dispatches) != 1 || status.Dispatches[0].Status != "sending" {
		t.Fatalf("status = %+v %v", status, err)
	}
	data, err := json.Marshal(status)
	if err != nil || strings.Contains(string(data), "PRIVATE_SETUP_CONTEXT") || strings.Contains(string(data), "variables") {
		t.Fatalf("status leaked setup variables: %s %v", data, err)
	}
	result, err := getWorkflowCheckpoint(context.Background(), root, "s", "%1", status.CheckpointToken, "delivered", true)
	if err != nil || result.Dispatches[0].Status != "delivered" || result.Dispatches[0].Resolution != "delivered" {
		t.Fatalf("recover = %+v %v", result, err)
	}
	if _, err := getWorkflowCheckpoint(context.Background(), root, "missing", "", "", "", false); err == nil {
		t.Fatal("missing checkpoint returned false success")
	}
}

func TestWorkflowCheckpointCommandsRegistered(t *testing.T) {
	parent := newWorkflowsCmd()
	for _, action := range []string{"status", "recover"} {
		command, _, err := parent.Find([]string{action})
		if err != nil || command == parent || command.Name() != action || command.RunE == nil {
			t.Fatalf("workflow %s is not reachable: %v", action, err)
		}
		for _, name := range []string{"session", "project-root"} {
			if command.Flags().Lookup(name) == nil {
				t.Fatalf("workflow %s lacks --%s", action, name)
			}
		}
	}
	recovery, _, _ := parent.Find([]string{"recover"})
	for _, name := range []string{"pane", "token", "outcome"} {
		flag := recovery.Flags().Lookup(name)
		if flag == nil || len(flag.Annotations["cobra_annotation_bash_completion_one_required_flag"]) == 0 {
			t.Errorf("recovery identity/outcome flag --%s is not required", name)
		}
	}
}
