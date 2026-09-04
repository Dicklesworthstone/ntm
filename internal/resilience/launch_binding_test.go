package resilience

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestCaptureLaunchBindingPersistsOnlyOpaqueProfile(t *testing.T) {
	t.Setenv("SHALLOW_PROFILE", "profile-a")
	t.Setenv("HOME", "/tmp/secret-home")
	t.Setenv("ANTHROPIC_API_KEY", "secret-token")

	binding := CaptureLaunchBinding("claude")
	if binding == nil {
		t.Fatal("CaptureLaunchBinding returned nil")
	}
	data, err := json.Marshal(AgentConfig{
		PaneID:        "%1",
		PaneIndex:     1,
		Type:          "cc",
		Command:       "claude",
		LaunchBinding: binding,
	})
	if err != nil {
		t.Fatalf("marshal agent config: %v", err)
	}
	text := string(data)
	for _, want := range []string{"profile-a", `"provider":"cc"`, `"launcher":"caam"`} {
		if !strings.Contains(text, want) {
			t.Fatalf("manifest row %s does not contain %q", text, want)
		}
	}
	for _, forbidden := range []string{"HOME", "secret-home", "ANTHROPIC_API_KEY", "secret-token", `"environment"`} {
		if strings.Contains(text, forbidden) {
			t.Fatalf("manifest row persisted forbidden environment data %q: %s", forbidden, text)
		}
	}
}

func TestPrepareLaunchCommandPreservesCreationProfile(t *testing.T) {
	t.Setenv("SHALLOW_PROFILE", "profile-b")
	binding := &LaunchBinding{Provider: "cc", Launcher: "caam", Identifier: "profile-a"}
	var gotBinary, gotIdentifier string

	command, affinity, err := prepareLaunchCommand(
		context.Background(),
		"claude",
		"/opt/caam",
		binding,
		"claude --model opus",
		func(_ context.Context, binary string, got *LaunchBinding) error {
			gotBinary = binary
			gotIdentifier = got.Identifier
			return nil
		},
	)
	if err != nil {
		t.Fatalf("prepareLaunchCommand: %v", err)
	}
	if affinity != LaunchAffinityPreserved {
		t.Fatalf("affinity = %q, want %q", affinity, LaunchAffinityPreserved)
	}
	if gotBinary != "/opt/caam" || gotIdentifier != "profile-a" {
		t.Fatalf("preflight used binary=%q profile=%q, want /opt/caam profile-a", gotBinary, gotIdentifier)
	}
	if !strings.Contains(command, "shallow-spawn") || !strings.Contains(command, "profile-a") ||
		strings.Contains(command, "profile-b") || !strings.HasSuffix(command, " -- claude --model opus") {
		t.Fatalf("prepared command did not preserve profile-a: %q", command)
	}
}

func TestPrepareLaunchCommandLegacyAffinityIsExplicit(t *testing.T) {
	const original = "codex --model gpt-5"
	command, affinity, err := prepareLaunchCommand(
		context.Background(),
		"cod",
		"",
		nil,
		original,
		func(context.Context, string, *LaunchBinding) error {
			t.Fatal("legacy unknown affinity must not invoke a launcher")
			return nil
		},
	)
	if err != nil {
		t.Fatalf("prepareLaunchCommand: %v", err)
	}
	if command != original || affinity != LaunchAffinityUnknown {
		t.Fatalf("command=%q affinity=%q, want original/%q", command, affinity, LaunchAffinityUnknown)
	}
}

func TestPrepareLaunchCommandResolutionFailureNamesBinding(t *testing.T) {
	binding := &LaunchBinding{Provider: "cc", Launcher: "caam", Identifier: "missing-profile"}
	_, _, err := prepareLaunchCommand(
		context.Background(),
		"cc",
		"",
		binding,
		"claude",
		func(context.Context, string, *LaunchBinding) error {
			return errors.New("profile does not exist")
		},
	)
	if err == nil || !strings.Contains(err.Error(), "missing-profile") || !strings.Contains(err.Error(), "profile does not exist") {
		t.Fatalf("error = %v, want named resolver failure", err)
	}
}

func TestPrepareLaunchCommandRejectsProviderMismatchBeforePreflight(t *testing.T) {
	calls := 0
	binding := &LaunchBinding{Provider: "cod", Launcher: "caam", Identifier: "profile-a"}
	_, _, err := prepareLaunchCommand(
		context.Background(),
		"cc",
		"",
		binding,
		"claude",
		func(context.Context, string, *LaunchBinding) error {
			calls++
			return nil
		},
	)
	if err == nil || !strings.Contains(err.Error(), "profile-a") || calls != 0 {
		t.Fatalf("error=%v preflight calls=%d, want named mismatch before preflight", err, calls)
	}
}

func TestUpsertAgentConfigPreservesExistingManifestWithoutEnvironment(t *testing.T) {
	t.Setenv("XDG_DATA_HOME", t.TempDir())
	if err := SaveManifest(&SpawnManifest{
		Session:     "add-binding",
		ProjectDir:  "/tmp/project",
		AutoRestart: true,
		Agents: []AgentConfig{{
			PaneID: "%1", PaneIndex: 1, Type: "cod", Command: "codex",
		}},
	}); err != nil {
		t.Fatalf("SaveManifest: %v", err)
	}
	t.Setenv("HOME", "/tmp/secret-home")
	t.Setenv("ANTHROPIC_API_KEY", "secret-token")
	if err := UpsertAgentConfig("add-binding", "/tmp/project", AgentConfig{
		PaneID:    "%2",
		PaneIndex: 2,
		Type:      "cc",
		Command:   "claude",
		LaunchBinding: &LaunchBinding{
			Provider: "cc", Launcher: "caam", Identifier: "profile-a",
		},
	}); err != nil {
		t.Fatalf("UpsertAgentConfig: %v", err)
	}
	loaded, err := LoadManifest("add-binding")
	if err != nil {
		t.Fatalf("LoadManifest: %v", err)
	}
	if !loaded.AutoRestart || len(loaded.Agents) != 2 ||
		loaded.Agents[1].LaunchBinding == nil ||
		loaded.Agents[1].LaunchBinding.Identifier != "profile-a" {
		t.Fatalf("unexpected manifest after upsert: %+v", loaded)
	}
	raw, err := os.ReadFile(filepath.Join(ManifestDir(), "add-binding.json"))
	if err != nil {
		t.Fatalf("read manifest: %v", err)
	}
	for _, forbidden := range []string{"secret-home", "secret-token", `"environment"`} {
		if strings.Contains(string(raw), forbidden) {
			t.Fatalf("manifest contains forbidden data %q: %s", forbidden, raw)
		}
	}
}
