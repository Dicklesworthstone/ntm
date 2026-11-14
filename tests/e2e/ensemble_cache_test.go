package e2e

import (
	"fmt"
	"os/exec"
	"testing"
	"time"

	"github.com/Dicklesworthstone/ntm/internal/ensemble"
	"github.com/Dicklesworthstone/ntm/internal/tmux"
	"github.com/Dicklesworthstone/ntm/tests/testutil"
)

func TestEnsembleSynthesize_UsesModeOutputCache(t *testing.T) {
	testutil.E2ETestPrecheck(t)
	logger := testutil.NewTestLoggerStdout(t)

	workDir := t.TempDir()
	homeDir := t.TempDir()
	t.Setenv("HOME", homeDir)
	t.Setenv("XDG_CONFIG_HOME", homeDir)

	session := fmt.Sprintf("ntm_cache_%d", time.Now().UnixNano())
	logger.LogSection("create tmux session")
	if err := exec.Command(tmux.BinaryPath(), "new-session", "-d", "-s", session).Run(); err != nil {
		t.Fatalf("create tmux session: %v", err)
	}
	t.Cleanup(func() {
		exec.Command(tmux.BinaryPath(), "kill-session", "-t", session).Run()
	})

	paneID := firstPaneID(t, session)

	catalog, err := ensemble.LoadModeCatalog()
	if err != nil {
		t.Fatalf("load mode catalog: %v", err)
	}
	modes := catalog.ListModes()
	if len(modes) == 0 {
		t.Fatalf("no modes available")
	}
	mode := modes[0]

	assignment := ensemble.ModeAssignment{
		ModeID:     mode.ID,
		PaneName:   paneID,
		AgentType:  string(tmux.AgentClaude),
		Status:     ensemble.AssignmentDone,
		AssignedAt: time.Now().UTC(),
	}
	state := &ensemble.EnsembleSession{
		SessionName:       session,
		Question:          "Cache test question",
		Assignments:       []ensemble.ModeAssignment{assignment},
		Status:            ensemble.EnsembleActive,
		SynthesisStrategy: ensemble.StrategyConsensus,
		CreatedAt:         time.Now().UTC(),
	}
	if err := ensemble.SaveSession(session, state); err != nil {
		t.Fatalf("save ensemble session: %v", err)
	}

	cacheCfg := ensemble.DefaultModeOutputCacheConfig()
	cache, err := ensemble.NewModeOutputCache(workDir, cacheCfg, nil)
	if err != nil {
		t.Fatalf("cache init: %v", err)
	}
	generator := ensemble.NewContextPackGenerator(workDir, nil, nil)
	pack, err := generator.Generate(t.Context(), state.Question, "", ensemble.CacheConfig{Enabled: false})
	if err != nil {
		t.Fatalf("context pack generate: %v", err)
	}

	cfg := ensemble.ModeOutputConfig{
		Question:          state.Question,
		AgentType:         assignment.AgentType,
		SynthesisStrategy: state.SynthesisStrategy.String(),
		SchemaVersion:     ensemble.SchemaVersion,
	}
	fingerprint, err := ensemble.BuildModeOutputFingerprint(pack.Hash, &mode, cfg)
	if err != nil {
		t.Fatalf("fingerprint: %v", err)
	}
	output := &ensemble.ModeOutput{
		ModeID: mode.ID,
		Thesis: "Cached synthesis output",
		TopFindings: []ensemble.Finding{{
			Finding:    "Cache used",
			Impact:     ensemble.ImpactLow,
			Confidence: 0.6,
		}},
		Confidence:  0.7,
		GeneratedAt: time.Now().UTC(),
	}
	if err := cache.Put(fingerprint, output); err != nil {
		t.Fatalf("cache put: %v", err)
	}

	logger.LogSection("synthesize with cache")
	_ = runCmd(t, workDir, "ntm", "ensemble", "synthesize", session, "--format", "json", "--use-cache")

	logger.LogSection("synthesize without cache (should fail)")
	if _, err := runCmdAllowFail(t, workDir, "ntm", "ensemble", "synthesize", session, "--format", "json", "--no-cache"); err == nil {
		t.Fatalf("expected synthesize to fail without cache")
	}
}
