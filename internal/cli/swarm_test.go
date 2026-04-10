package cli

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/Dicklesworthstone/ntm/internal/config"
	"github.com/Dicklesworthstone/ntm/internal/swarm"
)

func TestWritePlanToFile(t *testing.T) {
	tmpDir := t.TempDir()
	path := filepath.Join(tmpDir, "plans", "swarm_plan.json")

	createdAt := time.Now().UTC().Truncate(time.Second)
	plan := &swarm.SwarmPlan{
		CreatedAt:       createdAt,
		ScanDir:         "/tmp/projects",
		TotalCC:         1,
		TotalCod:        2,
		TotalGmi:        0,
		TotalAgents:     3,
		SessionsPerType: 2,
		PanesPerSession: 2,
	}

	if err := writePlanToFile(plan, path); err != nil {
		t.Fatalf("writePlanToFile error: %v", err)
	}

	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read plan file: %v", err)
	}

	var got swarm.SwarmPlan
	if err := json.Unmarshal(data, &got); err != nil {
		t.Fatalf("unmarshal plan: %v", err)
	}

	if got.ScanDir != plan.ScanDir {
		t.Errorf("ScanDir = %q, want %q", got.ScanDir, plan.ScanDir)
	}
	if got.TotalAgents != plan.TotalAgents {
		t.Errorf("TotalAgents = %d, want %d", got.TotalAgents, plan.TotalAgents)
	}
	if got.SessionsPerType != plan.SessionsPerType {
		t.Errorf("SessionsPerType = %d, want %d", got.SessionsPerType, plan.SessionsPerType)
	}
	if !got.CreatedAt.Equal(createdAt) {
		t.Errorf("CreatedAt = %v, want %v", got.CreatedAt, createdAt)
	}
}

func TestWritePlanToFileNilPlan(t *testing.T) {
	tmpDir := t.TempDir()
	path := filepath.Join(tmpDir, "plan.json")

	if err := writePlanToFile(nil, path); err == nil {
		t.Fatal("expected error for nil plan, got nil")
	}
}

func TestSwarmCmd_AutoRotateAccountsFlag_DefaultFromConfig(t *testing.T) {
	prevCfg := cfg
	t.Cleanup(func() { cfg = prevCfg })

	cfg = &config.Config{
		Swarm: config.DefaultSwarmConfig(),
	}
	cfg.Swarm.AutoRotateAccounts = true

	cmd := newSwarmCmd()

	if cmd.PersistentFlags().Lookup("auto-rotate-accounts") == nil {
		t.Fatal("expected --auto-rotate-accounts flag to exist")
	}

	got, err := cmd.PersistentFlags().GetBool("auto-rotate-accounts")
	if err != nil {
		t.Fatalf("GetBool(auto-rotate-accounts) error: %v", err)
	}
	if got != true {
		t.Errorf("auto-rotate-accounts default = %v, want true", got)
	}
}

func TestSwarmCmd_PromptFlagsExist(t *testing.T) {
	cmd := newSwarmCmd()
	if cmd.Flags().Lookup("prompt") == nil {
		t.Fatal("expected --prompt flag to exist")
	}
	if cmd.Flags().Lookup("prompt-file") == nil {
		t.Fatal("expected --prompt-file flag to exist")
	}
}

func TestResolveSwarmInitialPrompt_MutuallyExclusive(t *testing.T) {
	_, _, _, err := resolveSwarmInitialPrompt("hi", "/tmp/prompt.txt")
	if err == nil {
		t.Fatal("expected error for mutually exclusive flags, got nil")
	}
}

func TestResolveSwarmInitialPrompt_PromptFlag(t *testing.T) {
	got, source, path, err := resolveSwarmInitialPrompt("hello", "")
	if err != nil {
		t.Fatalf("resolveSwarmInitialPrompt error: %v", err)
	}
	if got != "hello" {
		t.Errorf("prompt=%q, want %q", got, "hello")
	}
	if source != "flag" {
		t.Errorf("source=%q, want %q", source, "flag")
	}
	if path != "" {
		t.Errorf("path=%q, want empty", path)
	}
}

func TestResolveSwarmInitialPrompt_PromptFile(t *testing.T) {
	tmpDir := t.TempDir()
	path := filepath.Join(tmpDir, "prompt.txt")
	if err := os.WriteFile(path, []byte("from-file"), 0644); err != nil {
		t.Fatalf("WriteFile error: %v", err)
	}

	got, source, gotPath, err := resolveSwarmInitialPrompt("", path)
	if err != nil {
		t.Fatalf("resolveSwarmInitialPrompt error: %v", err)
	}
	if got != "from-file" {
		t.Errorf("prompt=%q, want %q", got, "from-file")
	}
	if source != "file" {
		t.Errorf("source=%q, want %q", source, "file")
	}
	if gotPath != path {
		t.Errorf("path=%q, want %q", gotPath, path)
	}
}

func TestResolveSwarmInitialPrompt_PromptFileReadError(t *testing.T) {
	_, _, _, err := resolveSwarmInitialPrompt("", "/definitely/does/not/exist.txt")
	if err == nil {
		t.Fatal("expected error for missing prompt file, got nil")
	}
}

func TestGlobToRegex(t *testing.T) {

	tests := []struct {
		name   string
		glob   string
		want   string
		match  string // Test string that should match
		reject string // Test string that should not match
	}{
		{"simple wildcard", "*.go", ".*\\.go", "main.go", "main.gox"},
		{"question mark", "a?c", "a.c", "abc", "abbc"},
		{"double star", "**/*.ts", ".*.*/.*\\.ts", "src/app.ts", ""},
		{"literal dots", "file.txt", "file\\.txt", "", ""},
		{"special chars escaped", "test[1].go", "test\\[1\\]\\.go", "", ""},
		{"complex pattern", "src/**/*.{js,ts}", "src/.*.*/.*\\.\\{js,ts\\}", "", ""},
		{"plain string", "hello", "hello", "hello", "world"},
		{"empty string", "", "", "", ""},
		{"parens escaped", "func()", "func\\(\\)", "", ""},
		{"caret escaped", "^start", "\\^start", "", ""},
		{"dollar escaped", "end$", "end\\$", "", ""},
		{"pipe escaped", "a|b", "a\\|b", "", ""},
		{"backslash escaped", "a\\b", "a\\\\b", "", ""},
		{"plus escaped", "a+b", "a\\+b", "", ""},
		{"braces escaped", "{a,b}", "\\{a,b\\}", "", ""},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := globToRegex(tc.glob)
			if got != tc.want {
				t.Errorf("globToRegex(%q) = %q, want %q", tc.glob, got, tc.want)
			}
		})
	}
}

func TestBuildSwarmPlanOutput(t *testing.T) {

	plan := &swarm.SwarmPlan{
		ScanDir:         "/projects",
		TotalCC:         2,
		TotalCod:        3,
		TotalGmi:        1,
		TotalAgents:     6,
		SessionsPerType: 2,
		PanesPerSession: 4,
		Allocations: []swarm.ProjectAllocation{
			{
				Project: swarm.ProjectBeadCount{
					Name:      "proj1",
					Path:      "/projects/proj1",
					OpenBeads: 5,
					Tier:      1,
				},
				CCAgents:    1,
				CodAgents:   2,
				GmiAgents:   0,
				TotalAgents: 3,
			},
		},
		Sessions: []swarm.SessionSpec{
			{
				Name:      "cc_0",
				AgentType: "cc",
				PaneCount: 2,
				Panes: []swarm.PaneSpec{
					{Index: 0, Project: "proj1", AgentType: "cc"},
					{Index: 1, Project: "proj1", AgentType: "cc"},
				},
			},
		},
	}

	out := buildSwarmPlanOutput(plan, true)

	if out.ScanDir != plan.ScanDir {
		t.Errorf("ScanDir = %q, want %q", out.ScanDir, plan.ScanDir)
	}
	if out.TotalCC != plan.TotalCC {
		t.Errorf("TotalCC = %d, want %d", out.TotalCC, plan.TotalCC)
	}
	if out.TotalCod != plan.TotalCod {
		t.Errorf("TotalCod = %d, want %d", out.TotalCod, plan.TotalCod)
	}
	if out.TotalGmi != plan.TotalGmi {
		t.Errorf("TotalGmi = %d, want %d", out.TotalGmi, plan.TotalGmi)
	}
	if out.TotalAgents != plan.TotalAgents {
		t.Errorf("TotalAgents = %d, want %d", out.TotalAgents, plan.TotalAgents)
	}
	if !out.DryRun {
		t.Error("DryRun should be true")
	}
	if len(out.Allocations) != 1 {
		t.Fatalf("Allocations length = %d, want 1", len(out.Allocations))
	}
	if out.Allocations[0].Project != "proj1" {
		t.Errorf("Allocations[0].Project = %q, want %q", out.Allocations[0].Project, "proj1")
	}
	if len(out.Sessions) != 1 {
		t.Fatalf("Sessions length = %d, want 1", len(out.Sessions))
	}
	if len(out.Sessions[0].Panes) != 2 {
		t.Errorf("Sessions[0].Panes length = %d, want 2", len(out.Sessions[0].Panes))
	}
}

func TestBuildSwarmPlanOutput_EmptyPlan(t *testing.T) {

	plan := &swarm.SwarmPlan{
		ScanDir:     "/empty",
		Allocations: []swarm.ProjectAllocation{},
		Sessions:    []swarm.SessionSpec{},
	}

	out := buildSwarmPlanOutput(plan, false)

	if out.ScanDir != "/empty" {
		t.Errorf("ScanDir = %q, want %q", out.ScanDir, "/empty")
	}
	if out.DryRun {
		t.Error("DryRun should be false")
	}
	if len(out.Allocations) != 0 {
		t.Errorf("Allocations length = %d, want 0", len(out.Allocations))
	}
	if len(out.Sessions) != 0 {
		t.Errorf("Sessions length = %d, want 0", len(out.Sessions))
	}
}
