package e2e

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/Dicklesworthstone/ntm/tests/testutil"
)

// extractJSON extracts JSON object from output that may have log lines before it.
func extractJSON(data []byte) []byte {
	s := string(data)
	// Find the first '{' which starts the JSON
	idx := strings.Index(s, "{")
	if idx == -1 {
		return data
	}
	return []byte(s[idx:])
}

func setupSwarmTestEnv(t *testing.T) {
	t.Helper()
	t.Setenv("HOME", t.TempDir())
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("XDG_DATA_HOME", t.TempDir())
}

func uniqueSwarmSessionName(t *testing.T, prefix string) string {
	t.Helper()
	// `ntm swarm status` matches sessions like `cc_agents_123`.
	suffix := strconv.FormatInt(time.Now().UnixNano()%1_000_000_000, 10)
	return prefix + suffix
}

// swarmPlanResponse is the JSON output for ntm swarm plan.
type swarmPlanResponse struct {
	ScanDir         string             `json:"scan_dir"`
	TotalCC         int                `json:"total_cc"`
	TotalCod        int                `json:"total_cod"`
	TotalGmi        int                `json:"total_gmi"`
	TotalAgents     int                `json:"total_agents"`
	SessionsPerType int                `json:"sessions_per_type"`
	PanesPerSession int                `json:"panes_per_session"`
	Allocations     []allocationOutput `json:"allocations"`
	Sessions        []sessionOutput    `json:"sessions"`
	DryRun          bool               `json:"dry_run"`
	Error           string             `json:"error,omitempty"`
}

type allocationOutput struct {
	Project     string `json:"project"`
	Path        string `json:"path"`
	OpenBeads   int    `json:"open_beads"`
	Tier        int    `json:"tier"`
	CCAgents    int    `json:"cc_agents"`
	CodAgents   int    `json:"cod_agents"`
	GmiAgents   int    `json:"gmi_agents"`
	TotalAgents int    `json:"total_agents"`
}

type sessionOutput struct {
	Name      string       `json:"name"`
	AgentType string       `json:"agent_type"`
	PaneCount int          `json:"pane_count"`
	Panes     []paneOutput `json:"panes"`
}

type paneOutput struct {
	Index     int    `json:"index"`
	Project   string `json:"project"`
	AgentType string `json:"agent_type"`
}

// swarmStatusResponse is the JSON output for ntm swarm status.
type swarmStatusResponse struct {
	CheckedAt     string               `json:"checked_at"`
	Sessions      []swarmSessionStatus `json:"sessions"`
	Summary       healthSummary        `json:"summary"`
	OverallStatus string               `json:"overall_status"`
}

type swarmSessionStatus struct {
	Session string `json:"session"`
	Error   string `json:"error,omitempty"`
}

type healthSummary struct {
	Total   int `json:"total"`
	Healthy int `json:"healthy"`
	Warning int `json:"warning"`
	Error   int `json:"error"`
	Unknown int `json:"unknown"`
}

// swarmStopResponse is the JSON output for ntm swarm stop.
type swarmStopResponse struct {
	SessionsDestroyed int      `json:"sessions_destroyed"`
	PanesKilled       int      `json:"panes_killed"`
	GracefulExits     int      `json:"graceful_exits"`
	Duration          string   `json:"duration"`
	Errors            []string `json:"errors,omitempty"`
}

func runSwarmPlan(t *testing.T, dir string, args ...string) swarmPlanResponse {
	t.Helper()
	// Use `swarm --dry-run` instead of `swarm plan` (plan subcommand lacks --sessions-per-type).
	// NOTE: `swarm` has its own `--json` flag (distinct from root persistent flags).
	cmdArgs := []string{"swarm", "--dry-run", "--json"}
	cmdArgs = append(cmdArgs, args...)
	out := runCmd(t, dir, "ntm", cmdArgs...)
	jsonData := extractJSON(out)
	var resp swarmPlanResponse
	if err := json.Unmarshal(jsonData, &resp); err != nil {
		t.Fatalf("unmarshal swarm plan: %v\nout=%s", err, string(out))
	}
	return resp
}

func runSwarmStatus(t *testing.T, dir string) ([]byte, error) {
	t.Helper()
	return runCmdAllowFail(t, dir, "ntm", "swarm", "status")
}

func runSwarmStop(t *testing.T, dir string, args ...string) ([]byte, error) {
	t.Helper()
	cmdArgs := []string{"swarm", "stop"}
	cmdArgs = append(cmdArgs, args...)
	return runCmdAllowFail(t, dir, "ntm", cmdArgs...)
}

// TestE2ESwarmOrchestration_Plan tests swarm planning functionality.
func TestE2ESwarmOrchestration_Plan(t *testing.T) {
	testutil.RequireE2E(t)
	testutil.RequireNTMBinary(t)

	t.Run("plan_with_explicit_projects", func(t *testing.T) {
		setupSwarmTestEnv(t)

		// Create temp directory with mock project structure
		tmpDir := t.TempDir()

		// Create two mock projects with .beads directories
		project1 := filepath.Join(tmpDir, "project1")
		project2 := filepath.Join(tmpDir, "project2")

		for _, proj := range []string{project1, project2} {
			beadsDir := filepath.Join(proj, ".beads")
			if err := os.MkdirAll(beadsDir, 0755); err != nil {
				t.Fatalf("mkdir %s: %v", beadsDir, err)
			}
			// Create empty issues.jsonl
			if err := os.WriteFile(filepath.Join(beadsDir, "issues.jsonl"), []byte{}, 0644); err != nil {
				t.Fatalf("write issues.jsonl: %v", err)
			}
		}

		// Run swarm plan with explicit projects
		resp := runSwarmPlan(t, tmpDir, "--scan-dir="+tmpDir, "--projects="+project1+","+project2)

		// Verify dry run is true for plan command
		if !resp.DryRun {
			t.Errorf("expected dry_run=true for plan command, got false")
		}

		// Verify allocations exist for our projects
		if len(resp.Allocations) != 2 {
			t.Errorf("expected 2 allocations, got %d", len(resp.Allocations))
		}

		// Projects with no beads should be tier 3
		for _, alloc := range resp.Allocations {
			if alloc.OpenBeads != 0 {
				t.Errorf("expected 0 open beads for empty project, got %d", alloc.OpenBeads)
			}
			if alloc.Tier != 3 {
				t.Errorf("expected tier 3 for empty project, got %d", alloc.Tier)
			}
		}
	})

	t.Run("plan_no_projects_found", func(t *testing.T) {
		setupSwarmTestEnv(t)

		tmpDir := t.TempDir()

		// Run swarm --dry-run with non-existent scan directory
		out, err := runCmdAllowFail(t, tmpDir, "ntm", "swarm", "--dry-run", "--json", "--scan-dir="+filepath.Join(tmpDir, "nonexistent"))

		// Should fail or return JSON with error
		if err == nil {
			jsonData := extractJSON(out)
			var resp swarmPlanResponse
			if err := json.Unmarshal(jsonData, &resp); err == nil {
				if resp.Error == "" && len(resp.Allocations) > 0 {
					t.Errorf("expected error or no allocations for nonexistent scan dir")
				}
			}
		}
		// Command failing is also acceptable
	})

	t.Run("plan_with_beads_directory", func(t *testing.T) {
		setupSwarmTestEnv(t)

		tmpDir := t.TempDir()

		// Create project with .beads directory (scanner recognizes it as a beads project)
		project := filepath.Join(tmpDir, "project-with-beads")
		beadsDir := filepath.Join(project, ".beads")
		if err := os.MkdirAll(beadsDir, 0755); err != nil {
			t.Fatalf("mkdir %s: %v", beadsDir, err)
		}

		// Create empty issues.jsonl (br will report 0 beads, but swarm still allocates)
		if err := os.WriteFile(filepath.Join(beadsDir, "issues.jsonl"), []byte{}, 0644); err != nil {
			t.Fatalf("write issues.jsonl: %v", err)
		}

		resp := runSwarmPlan(t, tmpDir, "--scan-dir="+tmpDir, "--projects="+project)

		if len(resp.Allocations) != 1 {
			t.Fatalf("expected 1 allocation, got %d", len(resp.Allocations))
		}

		alloc := resp.Allocations[0]
		// Even with 0 beads, project should be recognized and get tier 3 allocation
		if alloc.Tier != 3 {
			t.Errorf("expected tier 3, got tier %d", alloc.Tier)
		}
		if alloc.TotalAgents == 0 {
			t.Errorf("expected some agents allocated for tier 3 project")
		}
	})

	t.Run("plan_json_structure", func(t *testing.T) {
		setupSwarmTestEnv(t)

		tmpDir := t.TempDir()

		// Create minimal project
		project := filepath.Join(tmpDir, "json-test")
		beadsDir := filepath.Join(project, ".beads")
		if err := os.MkdirAll(beadsDir, 0755); err != nil {
			t.Fatalf("mkdir %s: %v", beadsDir, err)
		}
		if err := os.WriteFile(filepath.Join(beadsDir, "issues.jsonl"), []byte{}, 0644); err != nil {
			t.Fatalf("write issues.jsonl: %v", err)
		}

		resp := runSwarmPlan(t, tmpDir, "--scan-dir="+tmpDir, "--projects="+project)

		// Verify essential JSON fields are populated
		if resp.SessionsPerType == 0 {
			t.Errorf("expected sessions_per_type > 0")
		}
		if resp.TotalAgents < 0 {
			t.Errorf("total_agents should be >= 0")
		}
	})
}

// TestE2ESwarmOrchestration_Status tests swarm status reporting.
func TestE2ESwarmOrchestration_Status(t *testing.T) {
	testutil.E2ETestPrecheck(t)

	t.Run("status_includes_test_session", func(t *testing.T) {
		setupSwarmTestEnv(t)

		tmpDir := t.TempDir()

		sessionName := uniqueSwarmSessionName(t, "cc_agents_")
		runCmd(t, tmpDir, "tmux", "new-session", "-d", "-s", sessionName, "sleep", "3600")
		t.Cleanup(func() {
			_, _ = runCmdAllowFail(t, tmpDir, "tmux", "kill-session", "-t", sessionName)
		})

		out, err := runSwarmStatus(t, tmpDir)
		if err != nil {
			t.Fatalf("status failed: %v\noutput:\n%s", err, string(out))
		}
		if !strings.Contains(string(out), sessionName) {
			t.Fatalf("expected status output to include test session %q\noutput:\n%s", sessionName, string(out))
		}
	})
}

// TestE2ESwarmOrchestration_Stop tests swarm stop functionality.
func TestE2ESwarmOrchestration_Stop(t *testing.T) {
	testutil.E2ETestPrecheck(t)

	t.Run("stop_no_sessions", func(t *testing.T) {
		setupSwarmTestEnv(t)

		tmpDir := t.TempDir()

		// Always pass an exact session name to avoid affecting real swarm sessions on developer machines.
		sessionName := uniqueSwarmSessionName(t, "cc_agents_")
		for {
			_, err := runCmdAllowFail(t, tmpDir, "tmux", "has-session", "-t", sessionName)
			if err != nil {
				break
			}
			sessionName = uniqueSwarmSessionName(t, "cc_agents_")
		}

		out, err := runSwarmStop(t, tmpDir, sessionName)
		if err != nil {
			t.Fatalf("stop failed: %v\noutput:\n%s", err, string(out))
		}
		if !strings.Contains(string(out), "No swarm sessions found") {
			t.Fatalf("expected no sessions found message\noutput:\n%s", string(out))
		}
	})

	t.Run("stop_force_flag", func(t *testing.T) {
		setupSwarmTestEnv(t)

		tmpDir := t.TempDir()

		sessionName := uniqueSwarmSessionName(t, "cc_agents_")
		for {
			_, err := runCmdAllowFail(t, tmpDir, "tmux", "has-session", "-t", sessionName)
			if err != nil {
				break
			}
			sessionName = uniqueSwarmSessionName(t, "cc_agents_")
		}

		out, err := runSwarmStop(t, tmpDir, "--force", sessionName)
		if err != nil {
			t.Fatalf("stop --force failed: %v\noutput:\n%s", err, string(out))
		}
		if !strings.Contains(string(out), "No swarm sessions found") {
			t.Fatalf("expected no sessions found message\noutput:\n%s", string(out))
		}
	})

	t.Run("stop_exact_session", func(t *testing.T) {
		setupSwarmTestEnv(t)

		tmpDir := t.TempDir()

		sessionName := uniqueSwarmSessionName(t, "cc_agents_")
		runCmd(t, tmpDir, "tmux", "new-session", "-d", "-s", sessionName, "sleep", "3600")

		out, err := runSwarmStop(t, tmpDir, "--force", sessionName)
		if err != nil {
			t.Fatalf("stop failed: %v\noutput:\n%s", err, string(out))
		}

		// Verify it is gone.
		_, hasErr := runCmdAllowFail(t, tmpDir, "tmux", "has-session", "-t", sessionName)
		if hasErr == nil {
			t.Fatalf("expected session %q to be destroyed", sessionName)
		}
	})
}

// TestE2ESwarmOrchestration_TierAllocation tests tier-based allocation logic.
func TestE2ESwarmOrchestration_TierAllocation(t *testing.T) {
	testutil.RequireE2E(t)
	testutil.RequireNTMBinary(t)

	t.Run("tier_calculation", func(t *testing.T) {
		setupSwarmTestEnv(t)

		tmpDir := t.TempDir()

		// Create projects with different bead counts to test tier assignment
		// Tier 1: >= 400 beads
		// Tier 2: >= 100 beads
		// Tier 3: < 100 beads

		tier3Project := filepath.Join(tmpDir, "tier3-small")
		beadsDir := filepath.Join(tier3Project, ".beads")
		if err := os.MkdirAll(beadsDir, 0755); err != nil {
			t.Fatalf("mkdir %s: %v", beadsDir, err)
		}

		// Create 10 open beads (tier 3)
		var beads []string
		for i := 0; i < 10; i++ {
			beads = append(beads, fmt.Sprintf(`{"id":"bd-%03d","title":"Task","status":"open"}`, i))
		}
		if err := os.WriteFile(filepath.Join(beadsDir, "issues.jsonl"), []byte(strings.Join(beads, "\n")), 0644); err != nil {
			t.Fatalf("write issues.jsonl: %v", err)
		}

		resp := runSwarmPlan(t, tmpDir, "--scan-dir="+tmpDir, "--projects="+tier3Project)

		if len(resp.Allocations) != 1 {
			t.Fatalf("expected 1 allocation, got %d", len(resp.Allocations))
		}

		alloc := resp.Allocations[0]
		if alloc.Tier != 3 {
			t.Errorf("expected tier 3 for %d beads, got tier %d", alloc.OpenBeads, alloc.Tier)
		}

		// Tier 3 should get minimal allocation
		if alloc.TotalAgents > 5 {
			t.Errorf("tier 3 should get minimal agents, got %d", alloc.TotalAgents)
		}
	})
}

// TestE2ESwarmOrchestration_ConfigOptions tests configuration options.
func TestE2ESwarmOrchestration_ConfigOptions(t *testing.T) {
	testutil.RequireE2E(t)
	testutil.RequireNTMBinary(t)

	t.Run("sessions_per_type", func(t *testing.T) {
		setupSwarmTestEnv(t)

		tmpDir := t.TempDir()

		// Create minimal project
		project := filepath.Join(tmpDir, "config-test")
		beadsDir := filepath.Join(project, ".beads")
		if err := os.MkdirAll(beadsDir, 0755); err != nil {
			t.Fatalf("mkdir %s: %v", beadsDir, err)
		}
		if err := os.WriteFile(filepath.Join(beadsDir, "issues.jsonl"), []byte{}, 0644); err != nil {
			t.Fatalf("write issues.jsonl: %v", err)
		}

		// Test via main swarm command with dry-run
		out := runCmd(t, tmpDir, "ntm", "swarm", "--dry-run", "--json", "--sessions-per-type=5", "--scan-dir="+tmpDir, "--projects="+project)

		jsonData := extractJSON(out)
		var resp swarmPlanResponse
		if err := json.Unmarshal(jsonData, &resp); err != nil {
			t.Fatalf("unmarshal swarm output: %v\nout=%s", err, string(out))
		}

		if resp.SessionsPerType != 5 {
			t.Errorf("expected sessions_per_type=5, got %d", resp.SessionsPerType)
		}
	})
}

// TestE2ESwarmOrchestration_ProjectDiscovery verifies scan-dir discovery excludes
// projects without beads while including valid beads projects.
func TestE2ESwarmOrchestration_ProjectDiscovery(t *testing.T) {
	testutil.RequireE2E(t)
	testutil.RequireNTMBinary(t)

	setupSwarmTestEnv(t)

	tmpDir := t.TempDir()
	validProject := filepath.Join(tmpDir, "project-with-beads")
	noBeadsProject := filepath.Join(tmpDir, "project-without-beads")

	if err := os.MkdirAll(filepath.Join(validProject, ".beads"), 0755); err != nil {
		t.Fatalf("mkdir beads dir: %v", err)
	}
	if err := os.MkdirAll(noBeadsProject, 0755); err != nil {
		t.Fatalf("mkdir no-beads dir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(validProject, ".beads", "issues.jsonl"), []byte{}, 0644); err != nil {
		t.Fatalf("write issues.jsonl: %v", err)
	}

	resp := runSwarmPlan(t, tmpDir, "--scan-dir="+tmpDir)

	if len(resp.Allocations) != 1 {
		t.Fatalf("expected 1 allocation, got %d", len(resp.Allocations))
	}
	if !strings.Contains(resp.Allocations[0].Path, validProject) {
		t.Fatalf("expected allocation for %s, got %s", validProject, resp.Allocations[0].Path)
	}
	for _, alloc := range resp.Allocations {
		if strings.Contains(alloc.Path, noBeadsProject) {
			t.Fatalf("unexpected allocation for no-beads project: %s", alloc.Path)
		}
	}
}

// TestE2ESwarmOrchestration_SessionNaming verifies session names follow
// the expected cc/cod/gmi naming convention in plan output.
func TestE2ESwarmOrchestration_SessionNaming(t *testing.T) {
	testutil.RequireE2E(t)
	testutil.RequireNTMBinary(t)

	setupSwarmTestEnv(t)

	tmpDir := t.TempDir()
	project := filepath.Join(tmpDir, "naming-test")
	beadsDir := filepath.Join(project, ".beads")
	if err := os.MkdirAll(beadsDir, 0755); err != nil {
		t.Fatalf("mkdir %s: %v", beadsDir, err)
	}
	if err := os.WriteFile(filepath.Join(beadsDir, "issues.jsonl"), []byte{}, 0644); err != nil {
		t.Fatalf("write issues.jsonl: %v", err)
	}

	resp := runSwarmPlan(t, tmpDir, "--scan-dir="+tmpDir, "--sessions-per-type=1")
	if len(resp.Sessions) == 0 {
		t.Fatalf("expected sessions in plan output")
	}

	wantPrefixes := map[string]string{
		"cc":  "cc_agents_",
		"cod": "cod_agents_",
		"gmi": "gmi_agents_",
	}

	seen := make(map[string]bool)
	for _, sess := range resp.Sessions {
		prefix, ok := wantPrefixes[sess.AgentType]
		if !ok {
			t.Fatalf("unexpected agent type: %s", sess.AgentType)
		}
		if !strings.HasPrefix(sess.Name, prefix) {
			t.Fatalf("expected session %s to start with %s", sess.Name, prefix)
		}
		seen[sess.AgentType] = true
	}

	for agentType := range wantPrefixes {
		if !seen[agentType] {
			t.Fatalf("missing session for agent type %s", agentType)
		}
	}
}
