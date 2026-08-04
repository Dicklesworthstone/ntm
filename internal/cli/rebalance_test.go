package cli

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/Dicklesworthstone/ntm/internal/assignment"
	"github.com/Dicklesworthstone/ntm/internal/output"
	"github.com/Dicklesworthstone/ntm/internal/robot"
	statuspkg "github.com/Dicklesworthstone/ntm/internal/status"
	"github.com/Dicklesworthstone/ntm/internal/tmux"
)

func TestRunRebalancePreCanceledJSONEnvelope(t *testing.T) {
	canceled, cancel := context.WithCancel(context.Background())
	cancel()

	for _, test := range []struct {
		name     string
		ctx      context.Context
		wantCode string
	}{
		{name: "nil context", ctx: nil, wantCode: robot.ErrCodeInternalError},
		{name: "canceled context", ctx: canceled, wantCode: robot.ErrCodeTimeout},
	} {
		t.Run(test.name, func(t *testing.T) {
			oldStdout := os.Stdout
			reader, writer, err := os.Pipe()
			if err != nil {
				t.Fatalf("create stdout pipe: %v", err)
			}
			os.Stdout = writer
			runErr := runRebalance(test.ctx, "rebalance-json", false, false, "", 0, "json")
			_ = writer.Close()
			os.Stdout = oldStdout
			raw, readErr := io.ReadAll(reader)
			_ = reader.Close()
			if readErr != nil {
				t.Fatalf("read rebalance JSON: %v", readErr)
			}
			if !errors.Is(runErr, errJSONFailure) {
				t.Fatalf("runRebalance error=%v, want errJSONFailure", runErr)
			}
			var document map[string]any
			if err := json.Unmarshal(raw, &document); err != nil {
				t.Fatalf("decode rebalance JSON: %v raw=%s", err, raw)
			}
			if success, ok := document["success"].(bool); !ok || success {
				t.Fatalf("rebalance success=%v, want false", document["success"])
			}
			if got, _ := document["error_code"].(string); got != test.wantCode {
				t.Fatalf("rebalance error_code=%q, want %q", got, test.wantCode)
			}
			for _, field := range []string{"workloads", "transfers"} {
				items, ok := document[field].([]any)
				if !ok || len(items) != 0 {
					t.Fatalf("rebalance %s=%v, want required empty array", field, document[field])
				}
			}
		})
	}
}

func TestRunRebalancePolicyFailuresPrecedeStoreAndTmuxReads(t *testing.T) {
	previousConfigFile := cfgFile
	previousResolveProject := resolveRebalanceProjectDir
	previousLoadStore := loadRebalanceAssignmentStore
	previousGetPanes := getRebalancePanes
	t.Cleanup(func() {
		cfgFile = previousConfigFile
		resolveRebalanceProjectDir = previousResolveProject
		loadRebalanceAssignmentStore = previousLoadStore
		getRebalancePanes = previousGetPanes
	})
	t.Setenv("NTM_CONFIG", "")

	root := t.TempDir()
	validGlobal := filepath.Join(root, "valid-global.toml")
	if err := os.WriteFile(validGlobal, []byte("[assign]\noperator_gated_labels = [\"global-approval\"]\n"), 0o600); err != nil {
		t.Fatalf("write valid global config: %v", err)
	}
	invalidGlobal := filepath.Join(root, "invalid-global.toml")
	if err := os.WriteFile(invalidGlobal, []byte("[assign\n"), 0o600); err != nil {
		t.Fatalf("write invalid global config: %v", err)
	}
	missingGlobal := filepath.Join(root, "missing-global.toml")
	validProject := t.TempDir()
	invalidProject := t.TempDir()
	if err := os.MkdirAll(filepath.Join(invalidProject, ".ntm"), 0o755); err != nil {
		t.Fatalf("create invalid project config directory: %v", err)
	}
	invalidProjectConfig := filepath.Join(invalidProject, ".ntm", "config.toml")
	if err := os.WriteFile(invalidProjectConfig, []byte("[assign\n"), 0o600); err != nil {
		t.Fatalf("write invalid project config: %v", err)
	}

	for _, test := range []struct {
		name       string
		globalPath string
		projectDir string
		wantPath   string
	}{
		{name: "missing explicitly selected global", globalPath: missingGlobal, projectDir: validProject, wantPath: missingGlobal},
		{name: "invalid explicitly selected global", globalPath: invalidGlobal, projectDir: validProject, wantPath: invalidGlobal},
		{name: "invalid target project", globalPath: validGlobal, projectDir: invalidProject, wantPath: invalidProjectConfig},
	} {
		t.Run(test.name, func(t *testing.T) {
			cfgFile = test.globalPath
			resolveCalls := 0
			storeCalls := 0
			paneCalls := 0
			resolveRebalanceProjectDir = func(_ context.Context, session string) (string, error) {
				resolveCalls++
				if session != "rebalance-policy" {
					t.Errorf("resolve session = %q, want rebalance-policy", session)
				}
				return test.projectDir, nil
			}
			loadRebalanceAssignmentStore = func(string) (*assignment.AssignmentStore, error) {
				storeCalls++
				return nil, errors.New("assignment store must not be read")
			}
			getRebalancePanes = func(context.Context, string) ([]tmux.Pane, error) {
				paneCalls++
				return nil, errors.New("tmux panes must not be read")
			}

			oldStdout := os.Stdout
			reader, writer, err := os.Pipe()
			if err != nil {
				t.Fatalf("create stdout pipe: %v", err)
			}
			os.Stdout = writer
			runErr := runRebalance(t.Context(), "rebalance-policy", true, false, "", 0, "json")
			_ = writer.Close()
			os.Stdout = oldStdout
			raw, readErr := io.ReadAll(reader)
			_ = reader.Close()
			if readErr != nil {
				t.Fatalf("read rebalance JSON: %v", readErr)
			}
			if !errors.Is(runErr, errJSONFailure) {
				t.Fatalf("runRebalance() error = %v, want errJSONFailure", runErr)
			}
			if resolveCalls != 1 || storeCalls != 0 || paneCalls != 0 {
				t.Fatalf("rebalance calls: resolve=%d store=%d panes=%d, want 1/0/0", resolveCalls, storeCalls, paneCalls)
			}

			var document map[string]any
			if err := json.Unmarshal(raw, &document); err != nil {
				t.Fatalf("decode rebalance JSON: %v raw=%s", err, raw)
			}
			if success, ok := document["success"].(bool); !ok || success {
				t.Fatalf("rebalance success=%v, want false", document["success"])
			}
			if got, _ := document["error_code"].(string); got != robot.ErrCodeInvalidFlag {
				t.Fatalf("rebalance error_code=%q, want %q", got, robot.ErrCodeInvalidFlag)
			}
			if got, _ := document["error"].(string); !strings.Contains(got, test.wantPath) {
				t.Fatalf("rebalance error=%q, want path %q", got, test.wantPath)
			}
			for _, field := range []string{"workloads", "transfers"} {
				items, ok := document[field].([]any)
				if !ok || len(items) != 0 {
					t.Fatalf("rebalance %s=%v, want required empty array", field, document[field])
				}
			}
		})
	}
}

func TestCalculateImbalanceScore(t *testing.T) {
	tests := []struct {
		name      string
		workloads []RebalanceWorkload
		want      float64
		tolerance float64
	}{
		{
			name:      "empty workloads",
			workloads: []RebalanceWorkload{},
			want:      0,
			tolerance: 0.001,
		},
		{
			name: "single workload",
			workloads: []RebalanceWorkload{
				{Pane: 1, TaskCount: 5},
			},
			want:      0, // stddev/mean = 0/5 = 0
			tolerance: 0.001,
		},
		{
			name: "perfectly balanced",
			workloads: []RebalanceWorkload{
				{Pane: 1, TaskCount: 3},
				{Pane: 2, TaskCount: 3},
				{Pane: 3, TaskCount: 3},
			},
			want:      0,
			tolerance: 0.001,
		},
		{
			name: "moderate imbalance",
			workloads: []RebalanceWorkload{
				{Pane: 1, TaskCount: 4},
				{Pane: 2, TaskCount: 2},
				{Pane: 3, TaskCount: 3},
			},
			// mean = 3, variance = ((4-3)^2 + (2-3)^2 + (3-3)^2)/3 = 2/3
			// stddev = sqrt(2/3) ≈ 0.816
			// CV = 0.816/3 ≈ 0.272
			want:      0.272,
			tolerance: 0.01,
		},
		{
			name: "severe imbalance",
			workloads: []RebalanceWorkload{
				{Pane: 1, TaskCount: 10},
				{Pane: 2, TaskCount: 0},
				{Pane: 3, TaskCount: 0},
			},
			// mean = 10/3 ≈ 3.33
			// variance = ((10-3.33)^2 + (0-3.33)^2 + (0-3.33)^2)/3 ≈ 22.22
			// stddev ≈ 4.71
			// CV ≈ 4.71/3.33 ≈ 1.414
			want:      1.414,
			tolerance: 0.01,
		},
		{
			name: "all zero tasks",
			workloads: []RebalanceWorkload{
				{Pane: 1, TaskCount: 0},
				{Pane: 2, TaskCount: 0},
			},
			want:      0, // No tasks = balanced
			tolerance: 0.001,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := calculateImbalanceScore(tt.workloads)
			diff := got - tt.want
			if diff < 0 {
				diff = -diff
			}
			if diff > tt.tolerance {
				t.Errorf("calculateImbalanceScore() = %v, want %v (tolerance %v)", got, tt.want, tt.tolerance)
			}
		})
	}
}

func TestGetRecommendation(t *testing.T) {
	tests := []struct {
		name  string
		score float64
		want  string
	}{
		{name: "zero score", score: 0.0, want: "balanced"},
		{name: "low score", score: 0.2, want: "balanced"},
		{name: "at threshold 0.3", score: 0.3, want: "moderate_imbalance"},
		{name: "moderate score", score: 0.5, want: "moderate_imbalance"},
		{name: "at threshold 0.7", score: 0.7, want: "rebalance_recommended"},
		{name: "high score", score: 1.0, want: "rebalance_recommended"},
		{name: "very high score", score: 2.0, want: "rebalance_recommended"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := getRecommendation(tt.score)
			if got != tt.want {
				t.Errorf("getRecommendation(%v) = %v, want %v", tt.score, got, tt.want)
			}
		})
	}
}

func TestMatchesRebalanceFilter(t *testing.T) {
	tests := []struct {
		name      string
		agentType string
		filter    string
		want      bool
	}{
		{name: "no filter", agentType: "claude", filter: "", want: true},
		{name: "cc matches claude", agentType: "claude", filter: "cc", want: true},
		{name: "claude matches claude", agentType: "claude", filter: "claude", want: true},
		{name: "claude alias matches", agentType: "claude", filter: "claude_code", want: true},
		{name: "cc prefix matches", agentType: "cc_1", filter: "cc", want: true},
		{name: "cod matches codex", agentType: "codex", filter: "cod", want: true},
		{name: "codex matches codex", agentType: "codex", filter: "codex", want: true},
		{name: "codex alias matches", agentType: "cod_2", filter: "openai-codex", want: true},
		{name: "cod prefix matches", agentType: "cod_2", filter: "cod", want: true},
		{name: "gmi matches gemini", agentType: "gemini", filter: "gmi", want: true},
		{name: "gemini matches gemini", agentType: "gemini", filter: "gemini", want: true},
		{name: "gemini alias matches", agentType: "gmi_3", filter: "google_gemini", want: true},
		{name: "gmi prefix matches", agentType: "gmi_3", filter: "gmi", want: true},
		{name: "windsurf alias matches", agentType: "windsurf", filter: "ws", want: true},
		{name: "claude does not match cod", agentType: "claude", filter: "cod", want: false},
		{name: "codex does not match cc", agentType: "codex", filter: "cc", want: false},
		{name: "unknown type with filter returns false", agentType: "unknown", filter: "cc", want: false},
		{name: "invalid filter returns false", agentType: "claude", filter: "not-an-agent", want: false},
		{name: "case insensitive filter", agentType: "Claude", filter: "CC", want: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := matchesRebalanceFilter(tt.agentType, tt.filter)
			if got != tt.want {
				t.Errorf("matchesRebalanceFilter(%q, %q) = %v, want %v", tt.agentType, tt.filter, got, tt.want)
			}
		})
	}
}

func TestNormalizeAgentTypeFilter(t *testing.T) {
	tests := []struct {
		name    string
		filter  string
		want    string
		wantErr bool
	}{
		{name: "empty", filter: "", want: ""},
		{name: "claude alias", filter: " claude_code ", want: "cc"},
		{name: "codex alias", filter: "openai-codex", want: "cod"},
		{name: "gemini alias", filter: "google-gemini", want: "gmi"},
		{name: "windsurf alias", filter: "ws", want: "windsurf"},
		{name: "invalid", filter: "not-an-agent", wantErr: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := normalizeAgentTypeFilter(tt.filter)
			if (err != nil) != tt.wantErr {
				t.Fatalf("normalizeAgentTypeFilter(%q) error = %v, wantErr %v", tt.filter, err, tt.wantErr)
			}
			if got != tt.want {
				t.Errorf("normalizeAgentTypeFilter(%q) = %q, want %q", tt.filter, got, tt.want)
			}
		})
	}
}

func TestRebalanceWorkloadCounts(t *testing.T) {
	workloads := []RebalanceWorkload{
		{Pane: 1, PaneID: "%11", TaskCount: 5},
		{Pane: 2, PaneID: "%22", TaskCount: 3},
		{Pane: 3, PaneID: "%33", TaskCount: 0},
	}

	counts := rebalanceWorkloadCounts(workloads)

	if len(counts) != 3 {
		t.Errorf("expected 3 counts, got %d", len(counts))
	}
	if counts["%11"] != 5 {
		t.Errorf("expected pane %%11 count = 5, got %d", counts["%11"])
	}
	if counts["%22"] != 3 {
		t.Errorf("expected pane %%22 count = 3, got %d", counts["%22"])
	}
	if counts["%33"] != 0 {
		t.Errorf("expected pane %%33 count = 0, got %d", counts["%33"])
	}
}

func TestBuildRebalanceWorkloads_UsesParsedPaneType(t *testing.T) {
	store := assignment.NewStore("test-session")
	panes := []tmux.Pane{
		{ID: "%1", WindowIndex: 0, Index: 1, Type: tmux.AgentClaude, Title: "notes", Active: true},
		{ID: "%2", WindowIndex: 0, Index: 2, Type: tmux.AgentUser, Title: "__cc_2", Active: true},
		{ID: "%3", WindowIndex: 0, Index: 3, Type: tmux.AgentType("openai-codex"), Title: "custom", Active: true},
	}

	workloads, err := buildRebalanceWorkloads(store, panes, "cod")
	if err != nil {
		t.Fatalf("buildRebalanceWorkloads: %v", err)
	}
	if len(workloads) != 1 {
		t.Fatalf("expected 1 workload matching cod, got %d", len(workloads))
	}
	if workloads[0].Pane != 3 || workloads[0].AgentType != "codex" {
		t.Fatalf("workload = %+v, want pane 3 codex", workloads[0])
	}
}

func TestBuildRebalanceWorkloadsCanonicalizesDuplicateWindowLocalIndexes(t *testing.T) {
	first := makeAssignment("bd-window-0", "window zero", 0, assignment.StatusWorking)
	first.OccupancyKey, first.DispatchTarget = "%11", "%11"
	first.AgentType = "codex"
	second := makeAssignment("bd-window-1", "window one", 0, assignment.StatusWorking)
	second.OccupancyKey, second.DispatchTarget = "%22", "%22"
	second.AgentType = "codex"
	legacy := makeAssignment("bd-legacy-index", "legacy index", 0, assignment.StatusWorking)
	legacy.OccupancyKey, legacy.DispatchTarget = "", ""
	store := assignmentStoreWith(first, second)
	panes := []tmux.Pane{
		{ID: "%22", WindowIndex: 1, Index: 0, Type: tmux.AgentCodex, Title: "test__cod_2", Active: true},
		{ID: "%11", WindowIndex: 0, Index: 0, Type: tmux.AgentCodex, Title: "test__cod_1", Active: true},
	}

	workloads, err := buildRebalanceWorkloads(store, panes, "cod")
	if err != nil {
		t.Fatalf("buildRebalanceWorkloads: %v", err)
	}
	if len(workloads) != 2 || workloads[0].PaneID != "%11" || workloads[1].PaneID != "%22" {
		t.Fatalf("canonical workload order = %+v", workloads)
	}
	if workloads[0].Pane != 0 || workloads[1].Pane != 0 || workloads[0].TaskCount != 1 || workloads[1].TaskCount != 1 ||
		!reflect.DeepEqual(workloads[0].TaskIDs, []string{"bd-window-0"}) ||
		!reflect.DeepEqual(workloads[1].TaskIDs, []string{"bd-window-1"}) {
		t.Fatalf("duplicate-index workloads = %+v", workloads)
	}
	counts := rebalanceWorkloadCounts(workloads)
	if !reflect.DeepEqual(counts, map[string]int{"%11": 1, "%22": 1}) {
		t.Fatalf("canonical counts = %v", counts)
	}
	after := calculateAfterState(workloads, []RebalanceTransfer{{
		FromPane: 0, FromPaneID: "%11", ToPane: 0, ToPaneID: "%22",
	}})
	if !reflect.DeepEqual(after, map[string]int{"%11": 0, "%22": 2}) {
		t.Fatalf("canonical duplicate-index accounting = %v", after)
	}
	livePanes := map[string]tmux.Pane{"%11": panes[1], "%22": panes[0]}
	if _, err := rebalanceAssignmentPane(livePanes, legacy); !errors.Is(err, assignment.ErrPaneIdentityMigrationRequired) || rebalanceAssignmentMatchesWorkload(legacy, workloads[0]) {
		t.Fatalf("index-only legacy assignment error=%v, want typed migration failure", err)
	}
	if _, err := buildRebalanceWorkloads(assignmentStoreWith(legacy), panes, "cod"); !errors.Is(err, assignment.ErrPaneIdentityMigrationRequired) {
		t.Fatalf("legacy workload error=%v, want typed migration failure", err)
	}
}

func TestBuildRebalanceWorkloadsFailsClosedOnMissingActivePhysicalPane(t *testing.T) {
	missing := makeAssignment("ntm-missing-pane", "missing pane", 0, assignment.StatusWorking)
	missing.OccupancyKey, missing.DispatchTarget = "%99", "%99"
	missing.AgentType = "codex"
	panes := []tmux.Pane{{ID: "%11", WindowIndex: 0, Index: 0, Type: tmux.AgentCodex}}

	workloads, err := buildRebalanceWorkloads(assignmentStoreWith(missing), panes, "")
	if err == nil || !strings.Contains(err.Error(), "physical pane %99 is not present") || workloads != nil {
		t.Fatalf("workloads=%+v error=%v, want missing-pane failure", workloads, err)
	}
}

func TestRebalanceResponseRequiredArraysMarshalEmptyNotNull(t *testing.T) {
	data, err := json.Marshal(RebalanceResponse{
		TimestampedResponse: output.NewTimestamped(),
		Success:             true,
		Transfers:           []RebalanceTransfer{},
		Workloads:           []RebalanceWorkload{},
		Before:              map[string]int{},
		After:               map[string]int{},
	})
	if err != nil {
		t.Fatalf("marshal response: %v", err)
	}
	if !strings.Contains(string(data), `"success":true`) || strings.Contains(string(data), `"transfers":null`) || strings.Contains(string(data), `"workloads":null`) ||
		!strings.Contains(string(data), `"transfers":[]`) || !strings.Contains(string(data), `"workloads":[]`) {
		t.Fatalf("response JSON = %s, want required empty arrays", data)
	}
}

func TestClassifyRebalanceErrorUsesStableCodes(t *testing.T) {
	tests := []struct {
		err  error
		want string
	}{
		{err: fmt.Errorf("wrapped: %w", context.DeadlineExceeded), want: robot.ErrCodeTimeout},
		{err: newRebalanceCommandError(robot.ErrCodeInvalidFlag, errors.New("bad filter")), want: robot.ErrCodeInvalidFlag},
		{err: fmt.Errorf("active assignment: %w", assignment.ErrPaneIdentityMigrationRequired), want: "PANE_IDENTITY_MIGRATION_REQUIRED"},
		{err: fmt.Errorf("changed tracker owner: %w", assignment.ErrClaimIneligible), want: "BEAD_INELIGIBLE"},
		{err: errors.New("tmux failed"), want: robot.ErrCodeInternalError},
	}
	for _, test := range tests {
		if got := classifyRebalanceError(test.err); got != test.want {
			t.Fatalf("classifyRebalanceError(%v) = %q, want %q", test.err, got, test.want)
		}
	}
}

func TestCalculateAfterState(t *testing.T) {
	workloads := []RebalanceWorkload{
		{Pane: 1, PaneID: "%11", TaskCount: 5},
		{Pane: 2, PaneID: "%22", TaskCount: 0},
	}

	transfers := []RebalanceTransfer{
		{FromPane: 1, FromPaneID: "%11", ToPane: 2, ToPaneID: "%22"},
		{FromPane: 1, FromPaneID: "%11", ToPane: 2, ToPaneID: "%22"},
	}

	after := calculateAfterState(workloads, transfers)

	if after["%11"] != 3 {
		t.Errorf("expected pane %%11 after = 3, got %d", after["%11"])
	}
	if after["%22"] != 2 {
		t.Errorf("expected pane %%22 after = 2, got %d", after["%22"])
	}
}

func TestCalculateAfterStateEmpty(t *testing.T) {
	workloads := []RebalanceWorkload{
		{Pane: 1, PaneID: "%11", TaskCount: 3},
	}

	transfers := []RebalanceTransfer{}

	after := calculateAfterState(workloads, transfers)

	if after["%11"] != 3 {
		t.Errorf("expected pane %%11 after = 3 (unchanged), got %d", after["%11"])
	}
}

func TestSuggestTransfersDistributesAcrossTargets(t *testing.T) {
	workloads := []RebalanceWorkload{
		{Pane: 1, PaneTarget: "0.1", PaneID: "%1", AgentType: "claude", TaskCount: 5, TaskIDs: []string{"bd-1", "bd-2", "bd-3", "bd-4", "bd-5"}},
		{Pane: 2, PaneTarget: "0.2", PaneID: "%2", AgentType: "codex", TaskCount: 0, IsHealthy: true, IsIdle: true},
		{Pane: 3, PaneTarget: "0.3", PaneID: "%3", AgentType: "gemini", TaskCount: 0, IsHealthy: true, IsIdle: true},
	}

	store := assignmentStoreWith(
		makeAssignment("bd-1", "bead 1", 1, assignment.StatusWorking),
		makeAssignment("bd-2", "bead 2", 1, assignment.StatusWorking),
		makeAssignment("bd-3", "bead 3", 1, assignment.StatusWorking),
		makeAssignment("bd-4", "bead 4", 1, assignment.StatusWorking),
		makeAssignment("bd-5", "bead 5", 1, assignment.StatusWorking),
	)

	transfers := suggestTransfers(workloads, store)

	if len(transfers) != 2 {
		t.Fatalf("expected one transfer per available target, got %d", len(transfers))
	}

	seenTargets := make(map[string]bool)
	for _, t := range transfers {
		seenTargets[t.ToPaneID] = true
	}

	if len(seenTargets) < 2 {
		t.Fatalf("expected transfers to multiple targets, got %v", seenTargets)
	}
}

func TestSuggestTransfersRequiresWorkingAtomicGeneration(t *testing.T) {
	workloads := []RebalanceWorkload{
		{Pane: 1, PaneTarget: "0.1", PaneID: "%1", AgentType: "claude", TaskCount: 2, TaskIDs: []string{"bd-assigned", "bd-working"}},
		{Pane: 2, PaneTarget: "0.2", PaneID: "%2", AgentType: "codex", TaskCount: 0, IsHealthy: true, IsIdle: true},
	}

	store := assignmentStoreWith(
		makeAssignment("bd-assigned", "assigned bead", 1, assignment.StatusAssigned),
		makeAssignment("bd-working", "working bead", 1, assignment.StatusWorking),
	)

	transfers := suggestTransfers(workloads, store)

	if len(transfers) != 1 {
		t.Fatalf("expected 1 transfer, got %d", len(transfers))
	}
	if transfers[0].BeadID != "bd-working" {
		t.Fatalf("expected working atomic bead transfer, got %s", transfers[0].BeadID)
	}
}

func TestSuggestTransfersReasonSameType(t *testing.T) {
	workloads := []RebalanceWorkload{
		{Pane: 1, PaneTarget: "0.1", PaneID: "%1", AgentType: "claude", TaskCount: 3, TaskIDs: []string{"bd-1", "bd-2", "bd-3"}},
		{Pane: 2, PaneTarget: "0.2", PaneID: "%2", AgentType: "claude", TaskCount: 0, IsHealthy: true, IsIdle: true},
	}

	store := assignmentStoreWith(
		makeAssignment("bd-1", "bead 1", 1, assignment.StatusWorking),
		makeAssignment("bd-2", "bead 2", 1, assignment.StatusWorking),
		makeAssignment("bd-3", "bead 3", 1, assignment.StatusWorking),
	)

	transfers := suggestTransfers(workloads, store)
	if len(transfers) == 0 {
		t.Fatalf("expected transfers, got none")
	}
	for _, tr := range transfers {
		if tr.Reason != "same_type_balance" {
			t.Fatalf("expected reason same_type_balance, got %q", tr.Reason)
		}
	}
}

func TestSuggestTransfersReasonTargetIdle(t *testing.T) {
	workloads := []RebalanceWorkload{
		{Pane: 1, PaneTarget: "0.1", PaneID: "%1", AgentType: "claude", TaskCount: 2, TaskIDs: []string{"bd-1", "bd-2"}},
		{Pane: 2, PaneTarget: "0.2", PaneID: "%2", AgentType: "codex", TaskCount: 0, IsHealthy: true, IsIdle: true},
	}

	store := assignmentStoreWith(
		makeAssignment("bd-1", "bead 1", 1, assignment.StatusWorking),
		makeAssignment("bd-2", "bead 2", 1, assignment.StatusWorking),
	)

	transfers := suggestTransfers(workloads, store)
	if len(transfers) != 1 {
		t.Fatalf("expected 1 transfer, got %d", len(transfers))
	}
	if transfers[0].Reason != "target_idle" {
		t.Fatalf("expected reason target_idle, got %q", transfers[0].Reason)
	}
}

func assignmentStoreWith(assignments ...*assignment.Assignment) *assignment.AssignmentStore {
	store := &assignment.AssignmentStore{
		Assignments: make(map[string]*assignment.Assignment),
	}
	for _, a := range assignments {
		store.Assignments[a.BeadID] = a
	}
	return store
}

func makeAssignment(beadID, title string, pane int, status assignment.AssignmentStatus) *assignment.Assignment {
	return &assignment.Assignment{
		BeadID:         beadID,
		BeadTitle:      title,
		Pane:           pane,
		AgentType:      "claude",
		Status:         status,
		IdempotencyKey: "generation-" + beadID,
		ClaimActor:     "actor-" + beadID,
		OccupancyKey:   fmt.Sprintf("%%%d", pane),
		DispatchTarget: fmt.Sprintf("%%%d", pane),
		DispatchState:  assignment.DispatchSent,
		ClaimState:     assignment.ClaimClaimed,
		ClaimStatus:    "in_progress",
	}
}

type rebalanceTestClaimPort struct {
	owner               string
	status              string
	claimCalls          int
	eligibilityCalls    int
	eligibilityErr      error
	eligibilityRequests []assignment.AssignmentEligibilityAuthorizationRequest
}

func (p *rebalanceTestClaimPort) Claim(_ context.Context, beadID, actor string) (assignment.ClaimReceipt, error) {
	p.claimCalls++
	if p.owner != "" && p.owner != actor {
		return assignment.ClaimReceipt{}, assignment.ErrClaimConflict
	}
	p.owner = actor
	return assignment.ClaimReceipt{
		BeadID: beadID, Actor: actor, Status: "in_progress", ClaimedAt: time.Now().UTC(),
	}, nil
}

func (p *rebalanceTestClaimPort) AuthorizeWorkingReplacement(_ context.Context, _ string) (assignment.WorkingReplacementAuthorization, error) {
	return assignment.WorkingReplacementAuthorization{Status: p.status, Assignee: p.owner}, nil
}

func (p *rebalanceTestClaimPort) AuthorizeAssignment(_ context.Context, request assignment.AssignmentEligibilityAuthorizationRequest) error {
	p.eligibilityCalls++
	p.eligibilityRequests = append(p.eligibilityRequests, request)
	return p.eligibilityErr
}

type rebalanceTestReleasePort struct {
	calls int
	paths []string
	ids   []int
	err   error
}

func (p *rebalanceTestReleasePort) ReleaseWorkingAssignment(_ context.Context, current *assignment.Assignment) (assignment.WorkingReplacementReleaseReceipt, error) {
	p.calls++
	if current != nil {
		p.paths = append([]string(nil), current.ReservedPaths...)
		p.ids = append([]int(nil), current.ReservationIDs...)
	}
	return assignment.WorkingReplacementReleaseReceipt{
		ReleasedPaths: append([]string(nil), p.paths...), ReleasedReservationIDs: append([]int(nil), p.ids...),
	}, p.err
}

type rebalanceTestReservationPort struct {
	calls int
	ids   []int
	err   error
}

func (p *rebalanceTestReservationPort) Reserve(_ context.Context, req assignment.ReservationRequest) (assignment.LeaseReceipt, error) {
	p.calls++
	lease := assignment.LeaseReceipt{
		AgentName: req.AgentName, Target: req.Target,
		Requested: append([]string(nil), req.RequestedPaths...),
	}
	if p.err != nil {
		return lease, assignment.GuaranteeNoReservation(p.err)
	}
	lease.Granted = append([]string(nil), req.RequestedPaths...)
	lease.ReservationIDs = append([]int(nil), p.ids...)
	expiresAt := time.Now().UTC().Add(time.Hour)
	lease.ExpiresAt = &expiresAt
	return lease, nil
}

type rebalanceTestDispatchPort struct {
	calls    int
	fail     error
	requests []assignment.DispatchRequest
}

func (p *rebalanceTestDispatchPort) Dispatch(_ context.Context, req assignment.DispatchRequest) (assignment.DispatchReceipt, error) {
	p.calls++
	p.requests = append(p.requests, req)
	if p.fail != nil {
		return assignment.DispatchReceipt{}, assignment.GuaranteeNoActuation(p.fail)
	}
	return assignment.DispatchReceipt{
		DeliveryID: assignment.DispatchDeliveryID(req.Target, "tmux", req.IdempotencyKey),
	}, nil
}

type rebalanceTestObserver struct {
	observation statuspkg.SessionObservation
	err         error
	calls       int
}

func (o *rebalanceTestObserver) Observe(context.Context, string) (statuspkg.SessionObservation, error) {
	o.calls++
	return o.observation, o.err
}

func rebalanceTestPanes() []tmux.Pane {
	return []tmux.Pane{
		{ID: "%11", WindowIndex: 0, Index: 1, Type: tmux.AgentClaude, Title: "test__cc_1", Active: true},
		{ID: "%22", WindowIndex: 0, Index: 2, Type: tmux.AgentCodex, Title: "test__cod_2", Active: true},
	}
}

func rebalanceSafeObserver(session string, panes ...tmux.Pane) *rebalanceTestObserver {
	now := time.Now().UTC()
	observations := make([]statuspkg.PaneObservation, 0, len(panes))
	for _, pane := range panes {
		observations = append(observations, statuspkg.PaneObservation{
			Pane: pane.Ref(),
			Current: statuspkg.StateObservation{
				Status:     statuspkg.AgentStatus{State: statuspkg.StateIdle},
				ObservedAt: now,
				Freshness:  statuspkg.FreshnessFresh,
				Confidence: 0.99,
			},
		})
	}
	return &rebalanceTestObserver{observation: statuspkg.SessionObservation{
		Session: session, ObservedAt: now, Complete: true, Panes: observations,
	}}
}

func seedRebalanceWorkingAssignment(t *testing.T, session string, reservationPaths []string, reservationIDs []int) (*assignment.AssignmentStore, *assignment.Assignment) {
	t.Helper()
	t.Setenv("HOME", t.TempDir())
	store := assignment.NewStore(session)
	now := time.Now().UTC()
	request := assignment.AtomicRequest{
		BeadID: "ntm-rebalance", BeadTitle: "Atomic rebalance",
		Target: "%11", OccupancyKey: "%11", Pane: 1,
		AgentType: "claude", AgentName: "test_claude_1", Actor: "rebalance-owner",
		Prompt: "original prompt", IdempotencyKey: "source-generation",
		RequireReservation: len(reservationPaths) > 0,
		RequestedPaths:     append([]string(nil), reservationPaths...),
	}
	actor := assignment.StableClaimActor(request.Actor, request.IdempotencyKey)
	if _, err := store.RecordAtomicIntent(request, actor, now); err != nil {
		t.Fatalf("RecordAtomicIntent: %v", err)
	}
	if _, err := store.RecordAtomicClaim(request, assignment.ClaimReceipt{
		BeadID: request.BeadID, Actor: actor, Status: "in_progress", ClaimedAt: now,
	}); err != nil {
		t.Fatalf("RecordAtomicClaim: %v", err)
	}
	if len(reservationPaths) > 0 {
		expiresAt := now.Add(time.Hour)
		if err := store.RecordAtomicReservation(request.BeadID, request.IdempotencyKey, assignment.ReservationReserved, assignment.LeaseReceipt{
			AgentName: request.AgentName, Target: request.Target,
			Requested:      append([]string(nil), reservationPaths...),
			Granted:        append([]string(nil), reservationPaths...),
			ReservationIDs: append([]int(nil), reservationIDs...), ExpiresAt: &expiresAt,
		}, nil); err != nil {
			t.Fatalf("RecordAtomicReservation: %v", err)
		}
	}
	if err := store.RecordAtomicDispatchStarted(request.BeadID, request.IdempotencyKey, now); err != nil {
		t.Fatalf("RecordAtomicDispatchStarted: %v", err)
	}
	if err := store.RecordAtomicDispatchSent(request.BeadID, request.IdempotencyKey, request.Prompt, assignment.DispatchReceipt{
		DeliveryID: assignment.DispatchDeliveryID(request.Target, "tmux", request.IdempotencyKey),
	}, now); err != nil {
		t.Fatalf("RecordAtomicDispatchSent: %v", err)
	}
	if err := store.MarkWorking(request.BeadID); err != nil {
		t.Fatalf("MarkWorking: %v", err)
	}
	return store, store.Get(request.BeadID)
}

func rebalanceTestTransfer(source *assignment.Assignment) RebalanceTransfer {
	prompt := rebalanceTransferPrompt(source.BeadID, source.BeadTitle)
	operationKey := rebalanceOperationKey(source.BeadID, source.IdempotencyKey, "%22", prompt)
	return RebalanceTransfer{
		BeadID: source.BeadID, BeadTitle: source.BeadTitle,
		FromPane: 1, FromTarget: "0.1", FromPaneID: "%11", FromAgent: "claude",
		ToPane: 2, ToTarget: "0.2", ToPaneID: "%22", ToAgent: "codex",
		Reason: "target_idle", sourceKey: source.IdempotencyKey, operationKey: operationKey, prompt: prompt,
	}
}

func rebalanceTestCoordinator(
	store *assignment.AssignmentStore,
	claim *rebalanceTestClaimPort,
	reservation assignment.ReservationPort,
	dispatch assignment.DispatchPort,
	release assignment.WorkingReplacementReleasePort,
) *assignment.AtomicCoordinator {
	return assignment.NewAtomicCoordinator(store, claim, reservation, dispatch).
		WithAssignmentEligibilityAuthorizationPort(claim).
		WithWorkingReplacementAuthorizationPort(claim).
		WithWorkingReplacementReleasePort(release)
}

func TestApplyTransfersAtomicSuccessReplayAndExactReservationTransfer(t *testing.T) {
	paths := []string{"internal/cli/rebalance.go", "internal/cli/rebalance_test.go"}
	oldIDs := []int{41, 42}
	store, source := seedRebalanceWorkingAssignment(t, "rebalance-success", paths, oldIDs)
	claim := &rebalanceTestClaimPort{owner: source.ClaimActor, status: "in_progress"}
	release := &rebalanceTestReleasePort{}
	reservation := &rebalanceTestReservationPort{ids: []int{71, 72}}
	dispatch := &rebalanceTestDispatchPort{}
	coordinator := rebalanceTestCoordinator(store, claim, reservation, dispatch, release)
	panes := rebalanceTestPanes()
	observer := rebalanceSafeObserver(store.SessionName, panes[1])
	transfers := []RebalanceTransfer{rebalanceTestTransfer(source)}

	if err := applyTransfers(t.Context(), "", store.SessionName, store, panes, transfers, coordinator, observer); err != nil {
		t.Fatalf("applyTransfers: %v", err)
	}
	after := store.Get(source.BeadID)
	if after == nil || after.Status != assignment.StatusAssigned || after.IdempotencyKey != transfers[0].operationKey ||
		after.ClaimActor != source.ClaimActor || after.OccupancyKey != "%22" || after.DispatchTarget != "%22" ||
		after.DispatchState != assignment.DispatchSent || after.DispatchReceiptID == "" {
		t.Fatalf("atomic rebalance result: before=%+v after=%+v", source, after)
	}
	if release.calls != 1 || !reflect.DeepEqual(release.paths, paths) || !reflect.DeepEqual(release.ids, oldIDs) {
		t.Fatalf("source release calls=%d paths=%v ids=%v", release.calls, release.paths, release.ids)
	}
	if reservation.calls != 1 || !reflect.DeepEqual(after.ReservationRequested, paths) ||
		!reflect.DeepEqual(after.ReservedPaths, paths) || !reflect.DeepEqual(after.ReservationIDs, []int{71, 72}) {
		t.Fatalf("target reservation calls=%d record=%+v", reservation.calls, after)
	}
	if dispatch.calls != 1 || len(dispatch.requests) != 1 || dispatch.requests[0].Target != "%22" {
		t.Fatalf("dispatch calls=%d requests=%+v", dispatch.calls, dispatch.requests)
	}

	// Replaying the exact transfer uses the durable receipt and performs no
	// second claim, release, reservation, or dispatch.
	if err := applyTransfers(t.Context(), "", store.SessionName, store, panes, transfers, coordinator, observer); err != nil {
		t.Fatalf("replay applyTransfers: %v", err)
	}
	if claim.eligibilityCalls != 1 || claim.claimCalls != 1 || release.calls != 1 || reservation.calls != 1 || dispatch.calls != 1 {
		t.Fatalf("replay side effects authorization=%d claim=%d release=%d reserve=%d dispatch=%d", claim.eligibilityCalls, claim.claimCalls, release.calls, reservation.calls, dispatch.calls)
	}
	if replayed := store.Get(source.BeadID); !reflect.DeepEqual(replayed, after) {
		t.Fatalf("replay mutated receipt: before=%+v after=%+v", after, replayed)
	}
}

func TestApplyTransfersRejectsStaleOrUnauthorizedSourceWithoutMutation(t *testing.T) {
	t.Run("stale generation", func(t *testing.T) {
		store, source := seedRebalanceWorkingAssignment(t, "rebalance-stale", nil, nil)
		before := store.Get(source.BeadID)
		claim := &rebalanceTestClaimPort{owner: source.ClaimActor, status: "in_progress"}
		release := &rebalanceTestReleasePort{}
		dispatch := &rebalanceTestDispatchPort{}
		transfer := rebalanceTestTransfer(source)
		transfer.sourceKey = "superseded-generation"
		coordinator := rebalanceTestCoordinator(store, claim, nil, dispatch, release)
		panes := rebalanceTestPanes()
		err := applyTransfers(t.Context(), "", store.SessionName, store, panes, []RebalanceTransfer{transfer}, coordinator, rebalanceSafeObserver(store.SessionName, panes[1]))
		if err == nil || !strings.Contains(err.Error(), "generation changed") {
			t.Fatalf("stale generation error = %v", err)
		}
		if after := store.Get(source.BeadID); !reflect.DeepEqual(after, before) {
			t.Fatalf("stale generation mutated source: before=%+v after=%+v", before, after)
		}
		if claim.claimCalls != 0 || release.calls != 0 || dispatch.calls != 0 {
			t.Fatalf("stale generation side effects claim=%d release=%d dispatch=%d", claim.claimCalls, release.calls, dispatch.calls)
		}
	})

	t.Run("live owner changed", func(t *testing.T) {
		store, source := seedRebalanceWorkingAssignment(t, "rebalance-owner", nil, nil)
		before := store.Get(source.BeadID)
		claim := &rebalanceTestClaimPort{owner: "different-live-owner", status: "in_progress"}
		release := &rebalanceTestReleasePort{}
		dispatch := &rebalanceTestDispatchPort{}
		coordinator := rebalanceTestCoordinator(store, claim, nil, dispatch, release)
		panes := rebalanceTestPanes()
		err := applyTransfers(t.Context(), "", store.SessionName, store, panes, []RebalanceTransfer{rebalanceTestTransfer(source)}, coordinator, rebalanceSafeObserver(store.SessionName, panes[1]))
		if !errors.Is(err, assignment.ErrWorkingReplacementNotAllowed) || !strings.Contains(err.Error(), "does not match durable claim actor") {
			t.Fatalf("changed owner error = %v", err)
		}
		if after := store.Get(source.BeadID); !reflect.DeepEqual(after, before) {
			t.Fatalf("changed owner mutated source: before=%+v after=%+v", before, after)
		}
		if claim.claimCalls != 0 || release.calls != 0 || dispatch.calls != 0 {
			t.Fatalf("changed owner side effects claim=%d release=%d dispatch=%d", claim.claimCalls, release.calls, dispatch.calls)
		}
	})

	for _, policyChange := range []string{"operator gate appeared", "blocker appeared"} {
		policyChange := policyChange
		t.Run(policyChange, func(t *testing.T) {
			store, source := seedRebalanceWorkingAssignment(t, "rebalance-"+strings.ReplaceAll(policyChange, " ", "-"), nil, nil)
			before := store.Get(source.BeadID)
			claim := &rebalanceTestClaimPort{
				owner: source.ClaimActor, status: "in_progress",
				eligibilityErr: fmt.Errorf("%w: %s", assignment.ErrClaimIneligible, policyChange),
			}
			release := &rebalanceTestReleasePort{}
			reservation := &rebalanceTestReservationPort{}
			dispatch := &rebalanceTestDispatchPort{}
			coordinator := rebalanceTestCoordinator(store, claim, reservation, dispatch, release)
			panes := rebalanceTestPanes()
			err := applyTransfers(t.Context(), "", store.SessionName, store, panes, []RebalanceTransfer{rebalanceTestTransfer(source)}, coordinator, rebalanceSafeObserver(store.SessionName, panes[1]))
			if !errors.Is(err, assignment.ErrClaimIneligible) || !strings.Contains(err.Error(), policyChange) {
				t.Fatalf("policy change error = %v", err)
			}
			if after := store.Get(source.BeadID); !reflect.DeepEqual(after, before) {
				t.Fatalf("policy change mutated source: before=%+v after=%+v", before, after)
			}
			if claim.eligibilityCalls != 1 || claim.claimCalls != 0 || release.calls != 0 || reservation.calls != 0 || dispatch.calls != 0 {
				t.Fatalf("policy change side effects authorization=%d claim=%d release=%d reserve=%d dispatch=%d", claim.eligibilityCalls, claim.claimCalls, release.calls, reservation.calls, dispatch.calls)
			}
			if len(claim.eligibilityRequests) != 1 || !claim.eligibilityRequests[0].AllowOwnedInProgress || claim.eligibilityRequests[0].ClaimActor != source.ClaimActor {
				t.Fatalf("policy change authorization request=%+v", claim.eligibilityRequests)
			}
		})
	}

	t.Run("target is not freshly idle", func(t *testing.T) {
		store, source := seedRebalanceWorkingAssignment(t, "rebalance-busy-target", nil, nil)
		before := store.Get(source.BeadID)
		claim := &rebalanceTestClaimPort{owner: source.ClaimActor, status: "in_progress"}
		release := &rebalanceTestReleasePort{}
		dispatch := &rebalanceTestDispatchPort{}
		coordinator := rebalanceTestCoordinator(store, claim, nil, dispatch, release)
		panes := rebalanceTestPanes()
		observer := rebalanceSafeObserver(store.SessionName, panes[1])
		observer.observation.Panes[0].Current.Status.State = statuspkg.StateWorking
		err := applyTransfers(t.Context(), "", store.SessionName, store, panes, []RebalanceTransfer{rebalanceTestTransfer(source)}, coordinator, observer)
		if err == nil || !strings.Contains(err.Error(), "not freshly and confidently idle") {
			t.Fatalf("busy target error = %v", err)
		}
		if after := store.Get(source.BeadID); !reflect.DeepEqual(after, before) {
			t.Fatalf("busy target mutated source: before=%+v after=%+v", before, after)
		}
		if claim.claimCalls != 0 || release.calls != 0 || dispatch.calls != 0 {
			t.Fatalf("busy target side effects claim=%d release=%d dispatch=%d", claim.claimCalls, release.calls, dispatch.calls)
		}
	})
}

func TestApplyTransfersReservationFailureKeepsOneRecoverableGeneration(t *testing.T) {
	paths := []string{"internal/cli/rebalance.go"}
	store, source := seedRebalanceWorkingAssignment(t, "rebalance-reservation-failure", paths, []int{51})
	claim := &rebalanceTestClaimPort{owner: source.ClaimActor, status: "in_progress"}
	release := &rebalanceTestReleasePort{}
	reservation := &rebalanceTestReservationPort{err: errors.New("reservation unavailable")}
	dispatch := &rebalanceTestDispatchPort{}
	coordinator := rebalanceTestCoordinator(store, claim, reservation, dispatch, release)
	panes := rebalanceTestPanes()
	transfer := rebalanceTestTransfer(source)

	err := applyTransfers(t.Context(), "", store.SessionName, store, panes, []RebalanceTransfer{transfer}, coordinator, rebalanceSafeObserver(store.SessionName, panes[1]))
	if err == nil || !strings.Contains(err.Error(), "reservation unavailable") {
		t.Fatalf("reservation failure error = %v", err)
	}
	after := store.Get(source.BeadID)
	if after == nil || after.IdempotencyKey != transfer.operationKey || after.Status != assignment.StatusClaimed ||
		after.ClaimActor != source.ClaimActor || after.ReservationState != assignment.ReservationFailed ||
		after.DispatchState != assignment.DispatchPending || after.DispatchAttempts != 0 || after.DispatchReceiptID != "" {
		t.Fatalf("reservation failure durable state: before=%+v after=%+v", source, after)
	}
	if len(store.GetAll()) != 1 || release.calls != 1 || !reflect.DeepEqual(release.ids, []int{51}) ||
		reservation.calls != 1 || dispatch.calls != 0 || claim.owner != source.ClaimActor {
		t.Fatalf("reservation failure side effects assignments=%d release=%d ids=%v reserve=%d dispatch=%d owner=%q", len(store.GetAll()), release.calls, release.ids, reservation.calls, dispatch.calls, claim.owner)
	}
}

func TestApplyTransfersDispatchFailureIsRetryableWithoutDuplicateMutation(t *testing.T) {
	paths := []string{"internal/cli/rebalance.go"}
	store, source := seedRebalanceWorkingAssignment(t, "rebalance-dispatch-failure", paths, []int{81})
	claim := &rebalanceTestClaimPort{owner: source.ClaimActor, status: "in_progress"}
	release := &rebalanceTestReleasePort{}
	reservation := &rebalanceTestReservationPort{ids: []int{91}}
	dispatch := &rebalanceTestDispatchPort{fail: errors.New("known pre-send failure")}
	coordinator := rebalanceTestCoordinator(store, claim, reservation, dispatch, release)
	panes := rebalanceTestPanes()
	observer := rebalanceSafeObserver(store.SessionName, panes[1])
	transfers := []RebalanceTransfer{rebalanceTestTransfer(source)}

	err := applyTransfers(t.Context(), "", store.SessionName, store, panes, transfers, coordinator, observer)
	if err == nil || !strings.Contains(err.Error(), "known pre-send failure") {
		t.Fatalf("dispatch failure error = %v", err)
	}
	pending := store.Get(source.BeadID)
	if pending == nil || pending.IdempotencyKey != transfers[0].operationKey || pending.Status != assignment.StatusClaimed ||
		pending.DispatchState != assignment.DispatchPending || pending.DispatchAttempts != 1 || pending.DispatchReceiptID != "" ||
		!strings.Contains(pending.LastDispatchError, "known pre-send failure") || pending.ReservationState != assignment.ReservationReserved ||
		!reflect.DeepEqual(pending.ReservationIDs, []int{91}) || !reflect.DeepEqual(pending.ReservedPaths, paths) || len(store.GetAll()) != 1 {
		t.Fatalf("dispatch failure durable state: before=%+v pending=%+v", source, pending)
	}
	if release.calls != 1 || claim.claimCalls != 1 || reservation.calls != 1 || dispatch.calls != 1 {
		t.Fatalf("dispatch failure side effects release=%d claim=%d reserve=%d dispatch=%d", release.calls, claim.claimCalls, reservation.calls, dispatch.calls)
	}

	// Simulate the next CLI process: reload only durable state and rediscover the
	// known-unsent generation instead of retaining the in-memory transfer plan.
	freshStore, err := assignment.LoadStoreStrict(store.SessionName)
	if err != nil {
		t.Fatalf("reload pending rebalance: %v", err)
	}
	workloads, err := buildRebalanceWorkloads(freshStore, panes, "")
	if err != nil {
		t.Fatalf("build recovery workloads: %v", err)
	}
	if suggestions := suggestTransfers(workloads, freshStore); len(suggestions) != 0 {
		t.Fatalf("pending generation was incorrectly replanned: %+v", suggestions)
	}
	recoveries := discoverPendingRebalanceTransfers(workloads, freshStore)
	if len(recoveries) != 1 || recoveries[0].Reason != rebalanceRecoveryReason ||
		recoveries[0].FromPaneID != "%22" || recoveries[0].ToPaneID != "%22" ||
		recoveries[0].operationKey != pending.IdempotencyKey || recoveries[0].prompt != pending.PendingPrompt {
		t.Fatalf("fresh recovery plan = %+v pending=%+v", recoveries, pending)
	}
	if rebalanceRequiresReservationManager(freshStore, recoveries) {
		t.Fatal("valid same-key recovery unnecessarily requires a new reservation manager")
	}

	dispatch.fail = nil
	freshCoordinator := rebalanceTestCoordinator(freshStore, claim, nil, dispatch, release)
	if err := applyTransfers(t.Context(), "", freshStore.SessionName, freshStore, panes, recoveries, freshCoordinator, rebalanceSafeObserver(freshStore.SessionName, panes[1])); err != nil {
		t.Fatalf("dispatch recovery: %v", err)
	}
	recovered := freshStore.Get(source.BeadID)
	if recovered.Status != assignment.StatusAssigned || recovered.DispatchState != assignment.DispatchSent || recovered.DispatchReceiptID == "" ||
		recovered.IdempotencyKey != pending.IdempotencyKey || recovered.PromptSent != pending.PendingPrompt || recovered.DispatchAttempts != 2 {
		t.Fatalf("dispatch recovery state: pending=%+v recovered=%+v", pending, recovered)
	}
	if release.calls != 1 || claim.claimCalls != 1 || reservation.calls != 1 || dispatch.calls != 2 {
		t.Fatalf("dispatch retry duplicated non-dispatch side effects release=%d claim=%d reserve=%d dispatch=%d", release.calls, claim.claimCalls, reservation.calls, dispatch.calls)
	}
	if len(dispatch.requests) != 2 || dispatch.requests[1].IdempotencyKey != pending.IdempotencyKey ||
		dispatch.requests[1].Prompt != pending.PendingPrompt || dispatch.requests[1].Target != "%22" {
		t.Fatalf("fresh recovery dispatch requests = %+v", dispatch.requests)
	}
}
