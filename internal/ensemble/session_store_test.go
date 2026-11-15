package ensemble

import (
	"math"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Dicklesworthstone/ntm/internal/state"
)

func resetDefaultStateStoreForTest() {
	CloseDefaultStateStore()
	defaultStateStore = struct {
		mu    sync.Mutex
		store *StateStore
	}{}
}

func TestSessionStore_SaveLoad_DefaultPath(t *testing.T) {
	tmpHome := t.TempDir()
	t.Setenv("HOME", tmpHome)
	// bd-vqs4c: clear ambient XDG_CONFIG_HOME / NTM_CONFIG so DefaultPath
	// resolves through the test HOME and not whatever the CI/agent shell
	// happens to have set (an invalid NTM_CONFIG=/nonexistent/config.toml
	// caused mkdir /nonexistent: permission denied here).
	t.Setenv("XDG_CONFIG_HOME", "")
	t.Setenv("NTM_CONFIG", "")

	resetDefaultStateStoreForTest()

	session := &EnsembleSession{
		SessionName:       "default-session",
		Question:          "Question",
		Status:            EnsembleActive,
		SynthesisStrategy: StrategyConsensus,
		CreatedAt:         time.Now().UTC(),
	}

	if err := SaveSession("", session); err != nil {
		t.Fatalf("SaveSession error: %v", err)
	}
	loaded, err := LoadSession("default-session")
	if err != nil {
		t.Fatalf("LoadSession error: %v", err)
	}
	if loaded == nil {
		t.Fatal("expected loaded session")
	}
	if loaded.Question != session.Question {
		t.Fatalf("Question = %q, want %q", loaded.Question, session.Question)
	}

	resetDefaultStateStoreForTest()

	// ensure no accidental writes to real home
	if _, err := os.Stat(tmpHome); err != nil {
		t.Fatalf("temp home missing: %v", err)
	}
}

func TestSessionStore_ReopensAfterCloseDefaultStateStore(t *testing.T) {
	tmpHome := t.TempDir()
	t.Setenv("HOME", tmpHome)
	// bd-vqs4c: clear ambient XDG_CONFIG_HOME / NTM_CONFIG (see
	// TestSessionStore_SaveLoad_DefaultPath comment).
	t.Setenv("XDG_CONFIG_HOME", "")
	t.Setenv("NTM_CONFIG", "")

	resetDefaultStateStoreForTest()

	first := &EnsembleSession{
		SessionName:       "first-session",
		Question:          "First question",
		Status:            EnsembleReady,
		SynthesisStrategy: StrategyConsensus,
		CreatedAt:         time.Now().UTC(),
	}
	if err := SaveSession("", first); err != nil {
		t.Fatalf("first SaveSession error: %v", err)
	}

	CloseDefaultStateStore()

	second := &EnsembleSession{
		SessionName:       "second-session",
		Question:          "Second question",
		Status:            EnsembleReady,
		SynthesisStrategy: StrategyConsensus,
		CreatedAt:         time.Now().UTC(),
	}
	if err := SaveSession("", second); err != nil {
		t.Fatalf("second SaveSession after close error: %v", err)
	}

	loaded, err := LoadSession("second-session")
	if err != nil {
		t.Fatalf("LoadSession after reopen error: %v", err)
	}
	if loaded == nil || loaded.Question != second.Question {
		t.Fatalf("loaded second session = %#v, want question %q", loaded, second.Question)
	}
}

func TestSessionStore_RetriesAfterOpenFailure(t *testing.T) {
	tempRoot := t.TempDir()
	badHome := tempRoot + "/home-file"
	if err := os.WriteFile(badHome, []byte("not-a-directory"), 0o644); err != nil {
		t.Fatalf("write bad home file: %v", err)
	}

	resetDefaultStateStoreForTest()
	t.Setenv("HOME", badHome)
	// Ensure DefaultPath() falls through to HOME instead of using ambient
	// XDG_CONFIG_HOME / NTM_CONFIG that CI runners may pre-populate with a
	// valid directory (which would defeat the "invalid HOME path" scenario).
	t.Setenv("XDG_CONFIG_HOME", "")
	t.Setenv("NTM_CONFIG", "")

	bad := &EnsembleSession{
		SessionName:       "bad-open",
		Question:          "Should fail first",
		Status:            EnsembleReady,
		SynthesisStrategy: StrategyConsensus,
		CreatedAt:         time.Now().UTC(),
	}
	if err := SaveSession("", bad); err == nil {
		t.Fatal("expected SaveSession to fail with invalid HOME path")
	}

	goodHome := t.TempDir()
	t.Setenv("HOME", goodHome)

	good := &EnsembleSession{
		SessionName:       "good-open",
		Question:          "Should succeed second",
		Status:            EnsembleReady,
		SynthesisStrategy: StrategyConsensus,
		CreatedAt:         time.Now().UTC(),
	}
	if err := SaveSession("", good); err != nil {
		t.Fatalf("SaveSession after transient open failure error: %v", err)
	}

	loaded, err := LoadSession("good-open")
	if err != nil {
		t.Fatalf("LoadSession after transient open failure error: %v", err)
	}
	if loaded == nil || loaded.Question != good.Question {
		t.Fatalf("loaded session = %#v, want question %q", loaded, good.Question)
	}
}

func TestSessionStoreExecutionConfigSurvivesReopen(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "ensemble.db")
	store, err := NewStateStore(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })

	cases := []struct {
		name      string
		budget    BudgetConfig
		synthesis SynthesisConfig
	}{
		{
			name: "custom",
			budget: BudgetConfig{
				MaxTokensPerMode:       2750,
				MaxTotalTokens:         16000,
				SynthesisReserveTokens: 2100,
				ContextReserveTokens:   1200,
				TimeoutPerMode:         37 * time.Second,
				TotalTimeout:           11 * time.Minute,
				MaxRetries:             1,
			},
			synthesis: SynthesisConfig{
				Strategy:           StrategyAdversarial,
				MinConfidence:      0.73,
				MaxFindings:        6,
				IncludeRawOutputs:  true,
				ConflictResolution: "majority",
				IncludeExplanation: true,
				MaxSynthesisTokens: 2100,
			},
		},
		{
			name:      "explicit-zero",
			budget:    BudgetConfig{},
			synthesis: SynthesisConfig{Strategy: StrategyManual},
		},
	}
	for _, tc := range cases {
		if err := store.Save(&EnsembleSession{
			SessionName:       tc.name,
			Question:          "Preserve the effective run settings",
			Status:            EnsembleActive,
			SynthesisStrategy: tc.synthesis.Strategy,
			Budget:            &tc.budget,
			Synthesis:         &tc.synthesis,
		}); err != nil {
			t.Fatalf("save %s: %v", tc.name, err)
		}
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	store, err = NewStateStore(dbPath)
	if err != nil {
		t.Fatal(err)
	}

	fallbackBudget := DefaultBudgetConfig()
	fallbackSynthesis := DefaultSynthesisConfig()
	fallbackSynthesis.IncludeRawOutputs = true
	fallbackSynthesis.IncludeExplanation = true
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			loaded, err := store.Load(tc.name)
			if err != nil {
				t.Fatal(err)
			}
			if loaded.Budget == nil || loaded.Synthesis == nil {
				t.Fatal("configuration snapshots missing after reopen")
			}
			if got := loaded.EffectiveBudget(fallbackBudget); got != tc.budget {
				t.Fatalf("budget = %+v, want %+v", got, tc.budget)
			}
			if got := loaded.EffectiveSynthesis(fallbackSynthesis); got != tc.synthesis {
				t.Fatalf("synthesis = %+v, want %+v", got, tc.synthesis)
			}

			loaded.Status = EnsembleComplete
			loaded.SynthesisStrategy = StrategyCreative
			if err := store.Save(loaded); err != nil {
				t.Fatal(err)
			}
			reloaded, err := store.Load(tc.name)
			if err != nil {
				t.Fatal(err)
			}
			if got := reloaded.EffectiveBudget(fallbackBudget); got != tc.budget {
				t.Fatalf("status update changed budget: %+v", got)
			}
			wantSynthesis := tc.synthesis
			wantSynthesis.Strategy = StrategyCreative
			if got := reloaded.EffectiveSynthesis(fallbackSynthesis); got != wantSynthesis {
				t.Fatalf("strategy update lost settings: %+v, want %+v", got, wantSynthesis)
			}
		})
	}
}

func TestSessionStoreExecutionConfigLegacyAndCorruption(t *testing.T) {
	store, err := NewStateStore(filepath.Join(t.TempDir(), "ensemble.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	raw := state.NewEnsembleStore(store.store)
	if err := raw.SaveEnsemble(&state.EnsembleSession{
		SessionName:       "legacy",
		Question:          "Created before configuration snapshots",
		Status:            "active",
		SynthesisStrategy: string(StrategyAdversarial),
	}); err != nil {
		t.Fatal(err)
	}
	legacy, err := store.Load("legacy")
	if err != nil {
		t.Fatal(err)
	}
	if legacy.Budget != nil || legacy.Synthesis != nil {
		t.Fatal("legacy session unexpectedly has configuration snapshots")
	}
	fallbackBudget := DefaultBudgetConfig()
	fallbackSynthesis := DefaultSynthesisConfig()
	if got := legacy.EffectiveBudget(fallbackBudget); got != fallbackBudget {
		t.Fatalf("legacy budget = %+v, want %+v", got, fallbackBudget)
	}
	fallbackSynthesis.Strategy = StrategyAdversarial
	if got := legacy.EffectiveSynthesis(DefaultSynthesisConfig()); got != fallbackSynthesis {
		t.Fatalf("legacy synthesis = %+v, want %+v", got, fallbackSynthesis)
	}

	cases := []struct {
		name      string
		budget    string
		synthesis string
		wantError string
	}{
		{name: "truncated-budget", budget: "{", wantError: "decode ensemble budget"},
		{name: "null-budget", budget: "null", wantError: "decode ensemble budget"},
		{name: "invalid-budget-type", budget: `{"max_total_tokens":"many"}`, wantError: "decode ensemble budget"},
		{name: "truncated-synthesis", synthesis: "{", wantError: "decode ensemble synthesis config"},
		{name: "null-synthesis", synthesis: "null", wantError: "decode ensemble synthesis config"},
		{name: "invalid-synthesis-type", synthesis: "[]", wantError: "decode ensemble synthesis config"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if err := raw.SaveEnsemble(&state.EnsembleSession{
				SessionName:         tc.name,
				Question:            "Corrupt settings must not inherit defaults",
				Status:              "active",
				BudgetJSON:          tc.budget,
				SynthesisConfigJSON: tc.synthesis,
			}); err != nil {
				t.Fatal(err)
			}
			loaded, err := store.Load(tc.name)
			if err == nil || loaded != nil || !strings.Contains(err.Error(), tc.wantError) {
				t.Fatalf("Load = (%+v, %v), want rejected configuration", loaded, err)
			}
		})
	}

	err = store.Save(&EnsembleSession{
		SessionName: "invalid-encoding",
		Question:    "Do not write a partial session",
		Status:      EnsembleActive,
		Synthesis: &SynthesisConfig{
			Strategy:      StrategyManual,
			MinConfidence: Confidence(math.NaN()),
		},
	})
	if err == nil || !strings.Contains(err.Error(), "encode ensemble synthesis config") {
		t.Fatalf("Save invalid synthesis error = %v", err)
	}
	if _, err := store.Load("invalid-encoding"); !os.IsNotExist(err) {
		t.Fatalf("invalid configuration wrote a session: %v", err)
	}
}
