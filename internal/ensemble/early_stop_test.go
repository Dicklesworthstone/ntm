package ensemble

import (
	"path/filepath"
	"testing"

	"github.com/Dicklesworthstone/ntm/internal/config"
	"github.com/Dicklesworthstone/ntm/internal/state"
)

// TestEarlyStopConfig_Defaults tests default configuration behavior.
func TestEarlyStopConfig_Defaults(t *testing.T) {
	cfg := EarlyStopConfig{}

	if cfg.Enabled {
		t.Error("Default Enabled should be false")
	}
	if cfg.MinAgentsBeforeStop != 0 {
		t.Errorf("Default MinAgentsBeforeStop = %d, want 0", cfg.MinAgentsBeforeStop)
	}
	if cfg.FindingsThreshold != 0 {
		t.Errorf("Default FindingsThreshold = %f, want 0", cfg.FindingsThreshold)
	}
	if cfg.SimilarityThreshold != 0 {
		t.Errorf("Default SimilarityThreshold = %f, want 0", cfg.SimilarityThreshold)
	}
	if cfg.WindowSize != 0 {
		t.Errorf("Default WindowSize = %d, want 0", cfg.WindowSize)
	}
}

func TestEarlyStopController_RedundantCloneStopsAndPersistsEvidenceLedger(t *testing.T) {
	store, err := state.Open(filepath.Join(t.TempDir(), "early_stop.db"))
	if err != nil {
		t.Fatalf("state.Open: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })
	if err := store.Migrate(); err != nil {
		t.Fatalf("store.Migrate: %v", err)
	}

	appCfg := config.EnsembleEarlyStopConfig{
		Enabled:             true,
		MinAgents:           2,
		FindingsThreshold:   0.25,
		SimilarityThreshold: 0.75,
		WindowSize:          100,
	}
	ctrl := NewEarlyStopController("ens-proj", EarlyStopConfigFromAppConfig(appCfg))

	baselineA := "Audit finding in internal/serve/server.go:412 missing origin validation allows cross site mutation requests"
	baselineB := "Security review confirms internal/serve/server.go:412 missing origin validation allows cross site mutation requests and session drift"

	// Before min_agents (only 1 completed mode), conservative fallback keeps running.
	ctrl.RecordCompletedMode("mode-a", baselineA)
	preMin := ctrl.EvaluateWindow("mode-clone", baselineA, 120)
	if preMin.ShouldStop || !preMin.FallbackActive || preMin.FallbackReason != "below_min_agents" {
		t.Fatalf("expected below_min_agents fallback, got %+v", preMin)
	}

	ctrl.RecordCompletedMode("mode-b", baselineB)
	if !ctrl.WindowReady("mode-clone", 240) {
		t.Fatal("expected WindowReady after advancing >= WindowSize tokens")
	}

	// Consecutive redundant windows accumulate e-process evidence until E_t >= 1/alpha (20.0).
	var finalDecision EarlyStopDecision
	stoppedAtWindow := 0
	for w := 1; w <= 5; w++ {
		dec := ctrl.EvaluateWindow("mode-clone", baselineA, 200+w*120)
		finalDecision = dec
		if dec.ShouldStop {
			stoppedAtWindow = w
			break
		}
	}
	if stoppedAtWindow < 2 {
		t.Fatalf("expected sequential stop after >=2 redundant windows, stoppedAtWindow=%d decision=%+v", stoppedAtWindow, finalDecision)
	}
	if finalDecision.EValue < 20.0 || finalDecision.Action != "stop" || finalDecision.FallbackActive {
		t.Fatalf("unexpected stop decision metrics: %+v", finalDecision)
	}

	rowID, err := PersistEarlyStopDecision(store, "ens-proj", finalDecision)
	if err != nil || rowID <= 0 {
		t.Fatalf("PersistEarlyStopDecision = (%d, %v)", rowID, err)
	}

	history, err := store.GetDecisionHistory("ensemble_mode", "ens-proj:mode-clone", 10)
	if err != nil || len(history) != 1 {
		t.Fatalf("GetDecisionHistory = (%+v, %v), want 1 record", history, err)
	}
	ev := history[0].Evidence
	if ev == nil || ev.SchemaVersion != "evidence_ledger.v1" || ev.Component != "ensemble.early_stop" || ev.Action != "stop" {
		t.Fatalf("persisted EvidenceLedger mismatch: %+v", ev)
	}
	if _, ok := ev.ExpectedLossByAction["stop"]; !ok {
		t.Fatalf("ExpectedLossByAction missing 'stop': %+v", ev.ExpectedLossByAction)
	}
	if ev.RegretByAction["stop"] != 0 {
		t.Fatalf("chosen optimal Bayes/Ville stop action should have 0 regret, got %+v", ev.RegretByAction)
	}
}

func TestEarlyStopController_ContrarianNovelCitationResetsEProcess(t *testing.T) {
	ctrl := NewEarlyStopController("ens-contrarian", EarlyStopConfig{
		Enabled:             true,
		MinAgentsBeforeStop: 2,
		FindingsThreshold:   0.25,
		SimilarityThreshold: 0.75,
		WindowSize:          80,
	})
	shared := "The retry loop in internal/robot/send.go:88 repeats identical selectors across panes without backoff"
	ctrl.RecordCompletedMode("mode-1", shared)
	ctrl.RecordCompletedMode("mode-2", shared)

	// Two redundant windows raise E_t above 7.0 (still below 20.0).
	d1 := ctrl.EvaluateWindow("mode-outlier", shared, 100)
	d2 := ctrl.EvaluateWindow("mode-outlier", shared, 200)
	if d1.ShouldStop || d2.ShouldStop || d2.EValue <= 5.0 {
		t.Fatalf("expected rising e-value < 20 after 2 windows, got d1=%+v d2=%+v", d1, d2)
	}

	// Window 3 shares prose but cites a brand-new file:line bug -> contrarian reset!
	contrarian := shared + " HOWEVER race condition found at internal/pipeline/executor.go:991!"
	d3 := ctrl.EvaluateWindow("mode-outlier", contrarian, 300)
	if d3.ShouldStop || d3.EValue != 1.0 || d3.NewCitations != 1 || d3.FallbackReason != "novel_citation_reset" {
		t.Fatalf("contrarian citation failed to reset e-process: %+v", d3)
	}

	// Even if Window 4 is redundant again, consecutive count restarted from 0 so it STILL must not stop!
	d4 := ctrl.EvaluateWindow("mode-outlier", shared, 400)
	if d4.ShouldStop {
		t.Fatalf("mode stopped prematurely on single post-reset redundant window: %+v", d4)
	}
}
