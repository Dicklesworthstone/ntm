package agentmail

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestCoordinatorSessionStateRoundTripPersistsStrictIdentityAndBindings(t *testing.T) {
	configHome := t.TempDir()
	t.Setenv("HOME", configHome)
	t.Setenv("XDG_CONFIG_HOME", configHome)
	projectDir := filepath.Join(t.TempDir(), "project")
	if err := os.MkdirAll(projectDir, 0o755); err != nil {
		t.Fatal(err)
	}
	state := &CoordinatorSessionState{
		Version: 1, SessionName: "bbi-infrastructure", ProjectDir: projectDir,
		MailProjectKey: "/repos/github.com/biji-biji-initiative/bbi-infrastructure",
		Coordinator: CoordinatorIdentity{
			AgentName: "SilverHarbor", RegistrationToken: "secret-token", Program: "ntm", Model: "coordinator",
		},
		PaneBindings: map[string]CoordinatorPaneBinding{
			"%44": {AgentName: "AzureCreek", PanePID: 4400, ProjectDir: projectDir, ImportedAt: time.Now().UTC()},
		},
	}
	if err := SaveCoordinatorSessionState(state); err != nil {
		t.Fatalf("SaveCoordinatorSessionState: %v", err)
	}
	loaded, err := LoadCoordinatorSessionState(state.SessionName, projectDir)
	if err != nil {
		t.Fatalf("LoadCoordinatorSessionState: %v", err)
	}
	if loaded == nil || loaded.MailProjectKey == loaded.ProjectDir {
		t.Fatalf("project/mail namespaces were conflated: %+v", loaded)
	}
	if loaded.Coordinator.AgentName != "SilverHarbor" || loaded.Coordinator.RegistrationToken != "secret-token" {
		t.Fatalf("coordinator identity/token not persisted: %+v", loaded.Coordinator)
	}
	if got := loaded.PaneBindings["%44"]; got.AgentName != "AzureCreek" || got.PanePID != 4400 {
		t.Fatalf("exact pane binding not persisted: %+v", got)
	}
	info, err := os.Stat(coordinatorStatePath(state.SessionName, projectDir))
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Fatalf("coordinator state mode = %o, want 600", info.Mode().Perm())
	}
}

func TestCoordinatorSessionStateRejectsMissingSenderToken(t *testing.T) {
	state := &CoordinatorSessionState{
		Version: 1, SessionName: "session", ProjectDir: "/work/project", MailProjectKey: "/repos/example/project",
		Coordinator: CoordinatorIdentity{AgentName: "SilverHarbor", Program: "ntm", Model: "coordinator"},
	}
	if err := ValidateCoordinatorSessionState(state, "session", "/work/project"); err == nil {
		t.Fatal("state without a coordinator token was accepted")
	}
}
