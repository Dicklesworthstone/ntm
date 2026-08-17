package resilience

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
)

// TestStartSessionMonitor_DisabledUnderGoTest pins the fork-bomb guard: under
// `go test` the shared path must refuse with ErrInternalMonitorDisabled and
// write nothing.
func TestStartSessionMonitor_DisabledUnderGoTest(t *testing.T) {
	dataDir := t.TempDir()
	t.Setenv("XDG_DATA_HOME", dataDir)

	result, err := StartSessionMonitor(t.Context(), SpawnMonitorRequest{
		Session:    "guardproj",
		ProjectDir: "/tmp/guardproj",
		Agents:     []AgentConfig{{PaneID: "0.1", PaneIndex: 1, Type: "claude"}},
	})
	if !errors.Is(err, ErrInternalMonitorDisabled) {
		t.Fatalf("err = %v, want ErrInternalMonitorDisabled", err)
	}
	if result != nil {
		t.Fatalf("result = %+v, want nil when disabled", result)
	}
	if _, statErr := os.Stat(filepath.Join(ManifestDir(), "guardproj.json")); !os.IsNotExist(statErr) {
		t.Fatalf("manifest must not be written when the monitor is disabled (stat err = %v)", statErr)
	}
}

// TestBuildSpawnManifest pins single-definition manifest construction: field
// mapping, agent passthrough, and the restart-unsupported (grok) filter.
func TestBuildSpawnManifest(t *testing.T) {
	req := SpawnMonitorRequest{
		Session:     "proj--lane",
		ProjectDir:  "/srv/projects/proj",
		AutoRestart: true,
		Agents: []AgentConfig{
			{PaneID: "0.1", PaneIndex: 1, Type: "claude", Model: "opus", Command: "claude --model opus"},
			{PaneID: "0.2", PaneIndex: 2, Type: "grok", Model: "", Command: "grok"},
			{PaneID: "0.3", PaneIndex: 3, Type: "codex", Model: "", Command: "codex"},
		},
	}
	m := buildSpawnManifest(req)
	if m.Session != "proj--lane" || m.ProjectDir != "/srv/projects/proj" || !m.AutoRestart {
		t.Fatalf("manifest header mismatch: %+v", m)
	}
	if len(m.Agents) != 2 {
		t.Fatalf("agents = %d, want 2 (grok filtered): %+v", len(m.Agents), m.Agents)
	}
	if m.Agents[0].PaneID != "0.1" || m.Agents[0].Type != "claude" || m.Agents[0].Command != "claude --model opus" {
		t.Fatalf("agent 0 mismatch: %+v", m.Agents[0])
	}
	if m.Agents[1].PaneID != "0.3" || m.Agents[1].Type != "codex" {
		t.Fatalf("agent 1 mismatch: %+v", m.Agents[1])
	}

	// Round-trip through the persisted form the monitor loads.
	t.Setenv("XDG_DATA_HOME", t.TempDir())
	if err := SaveManifest(m); err != nil {
		t.Fatalf("SaveManifest: %v", err)
	}
	loaded, err := LoadManifest("proj--lane")
	if err != nil {
		t.Fatalf("LoadManifest: %v", err)
	}
	if loaded.Session != m.Session || len(loaded.Agents) != len(m.Agents) || loaded.Agents[1].PaneID != "0.3" {
		t.Fatalf("persisted manifest mismatch: %+v", loaded)
	}
}
