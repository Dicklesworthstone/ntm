//go:build ensemble_experimental
// +build ensemble_experimental

package ensemble

import (
	"testing"

	"github.com/Dicklesworthstone/ntm/internal/tmux"
)

func TestResolveEnsembleConfig_PresetOverrides(t *testing.T) {
	catalog := testModeCatalog(t)
	preset := EnsemblePreset{
		Name:  "diagnosis",
		Modes: []ModeRef{ModeRefFromID("deductive"), ModeRefFromID("abductive")},
		Synthesis: SynthesisConfig{
			Strategy: StrategyConsensus,
		},
		Budget: BudgetConfig{
			MaxTokensPerMode: 1111,
			MaxTotalTokens:   50000,
		},
	}
	registry := NewEnsembleRegistry([]EnsemblePreset{preset}, catalog)

	cfg := &EnsembleConfig{
		SessionName: "demo",
		Question:    "What is broken?",
		Ensemble:    "diagnosis",
		Synthesis:   SynthesisConfig{Strategy: StrategyCreative},
		Budget:      BudgetConfig{MaxTokensPerMode: 3333},
	}

	modeIDs, resolved, explicitSpecs, err := resolveEnsembleConfig(cfg, catalog, registry)
	if err != nil {
		t.Fatalf("resolveEnsembleConfig error: %v", err)
	}
	if len(modeIDs) != 2 {
		t.Fatalf("modeIDs len = %d, want 2", len(modeIDs))
	}
	if explicitSpecs != nil {
		t.Fatalf("explicit specs should be nil for preset path, got %v", explicitSpecs)
	}
	if resolved.presetName != "diagnosis" {
		t.Fatalf("presetName = %q, want diagnosis", resolved.presetName)
	}
	if resolved.synthesis.Strategy != StrategyCreative {
		t.Fatalf("synthesis strategy = %q, want %q", resolved.synthesis.Strategy, StrategyCreative)
	}
	if resolved.budget.MaxTokensPerMode != 3333 {
		t.Fatalf("budget MaxTokensPerMode = %d, want 3333", resolved.budget.MaxTokensPerMode)
	}
	if resolved.budget.MaxTotalTokens != 50000 {
		t.Fatalf("budget MaxTotalTokens = %d, want 50000", resolved.budget.MaxTotalTokens)
	}
}

func TestResolveEnsembleConfig_ExplicitSpecs(t *testing.T) {
	catalog := testModeCatalog(t)
	registry := NewEnsembleRegistry(nil, catalog)

	cfg := &EnsembleConfig{
		SessionName: "demo",
		Question:    "Test explicit",
		Assignment:  assignmentExplicit,
		Modes:       []string{"deductive:cc,abductive:cod"},
	}

	modeIDs, resolved, explicitSpecs, err := resolveEnsembleConfig(cfg, catalog, registry)
	if err != nil {
		t.Fatalf("resolveEnsembleConfig error: %v", err)
	}
	if len(modeIDs) != 2 {
		t.Fatalf("modeIDs len = %d, want 2", len(modeIDs))
	}
	if len(explicitSpecs) != 2 {
		t.Fatalf("explicit specs len = %d, want 2", len(explicitSpecs))
	}
	if resolved.presetName != "" {
		t.Fatalf("presetName = %q, want empty", resolved.presetName)
	}
}

func TestResolveEnsembleConfig_AdvancedModeRejected(t *testing.T) {
	catalog := testModeCatalog(t)
	preset := EnsemblePreset{
		Name:  "advanced-demo",
		Modes: []ModeRef{ModeRefFromID("advanced-mode"), ModeRefFromID("deductive")},
	}
	registry := NewEnsembleRegistry([]EnsemblePreset{preset}, catalog)

	cfg := &EnsembleConfig{
		SessionName:   "demo",
		Question:      "Test advanced",
		Ensemble:      "advanced-demo",
		AllowAdvanced: false,
	}

	_, _, _, err := resolveEnsembleConfig(cfg, catalog, registry)
	if err == nil {
		t.Fatal("expected error when advanced modes not allowed")
	}
}

func TestBuildPaneSpecs_DefaultsAndValidation(t *testing.T) {
	cfg := &EnsembleConfig{ProjectDir: "/tmp"}

	panes, err := buildPaneSpecs(cfg, 2)
	if err != nil {
		t.Fatalf("buildPaneSpecs error: %v", err)
	}
	if len(panes) != 2 {
		t.Fatalf("panes len = %d, want 2", len(panes))
	}
	for i, pane := range panes {
		if pane.AgentType != string(tmux.AgentClaude) {
			t.Errorf("pane[%d] AgentType = %q, want %q", i, pane.AgentType, tmux.AgentClaude)
		}
		if pane.Project != "/tmp" {
			t.Errorf("pane[%d] Project = %q, want /tmp", i, pane.Project)
		}
	}

	cfg.AgentMix = map[string]int{"cc": 1}
	_, err = buildPaneSpecs(cfg, 2)
	if err == nil {
		t.Fatal("expected error when agent mix insufficient")
	}

	cfg.AgentMix = map[string]int{"": 1}
	_, err = buildPaneSpecs(cfg, 1)
	if err == nil {
		t.Fatal("expected error for empty agent type")
	}
}

func TestNormalizeExplicitSpecs_RejectsDuplicates(t *testing.T) {
	catalog := testModeCatalog(t)
	_, err := normalizeExplicitSpecs([]string{"deductive:cc", "deductive:cod"}, catalog)
	if err == nil {
		t.Fatal("expected error for duplicate mode in explicit specs")
	}
}

func TestValidateResolvedConfig_BudgetDefaults(t *testing.T) {
	catalog := testModeCatalog(t)
	resolved := resolvedEnsembleConfig{
		synthesis: DefaultSynthesisConfig(),
		budget:    BudgetConfig{MaxTokensPerMode: 4000, MaxTotalTokens: 50000},
	}
	modeIDs := []string{"deductive", "abductive"}

	if err := validateResolvedConfig(&resolved, modeIDs, catalog, true); err != nil {
		t.Fatalf("validateResolvedConfig error: %v", err)
	}
}

func TestNormalizeAssignment_Defaults(t *testing.T) {
	if got := normalizeAssignment(""); got != assignmentAffinity {
		t.Fatalf("normalizeAssignment(\"\") = %q, want %q", got, assignmentAffinity)
	}
	if got := normalizeAssignment("round-robin"); got != assignmentRoundRobin {
		t.Fatalf("normalizeAssignment(\"round-robin\") = %q, want %q", got, assignmentRoundRobin)
	}
	if got := normalizeAssignment("explicit"); got != assignmentExplicit {
		t.Fatalf("normalizeAssignment(\"explicit\") = %q, want %q", got, assignmentExplicit)
	}
}

func TestBuildPaneTargetMap_UsesTitleAndID(t *testing.T) {
	panes := []tmux.Pane{
		{ID: "%1", Title: "pane-1", Index: 1},
		{ID: "%2", Title: "pane-2", Index: 2},
	}

	targets := buildPaneTargetMap("session", panes)
	if targets["pane-1"] != "%1" {
		t.Fatalf("target for pane-1 = %q, want %q", targets["pane-1"], "%1")
	}
	if targets["%2"] != "%2" {
		t.Fatalf("target for %%2 = %q, want %q", targets["%2"], "%2")
	}
}

func TestNewEnsembleManager(t *testing.T) {
	m := NewEnsembleManager()
	if m == nil {
		t.Fatal("NewEnsembleManager returned nil")
	}
	if m.TmuxClient != nil {
		t.Error("TmuxClient should be nil by default")
	}
	if m.SessionOrchestrator == nil {
		t.Error("SessionOrchestrator should not be nil")
	}
	if m.PaneLauncher == nil {
		t.Error("PaneLauncher should not be nil")
	}
	if m.PromptInjector == nil {
		t.Error("PromptInjector should not be nil")
	}
	if m.Logger == nil {
		t.Error("Logger should not be nil")
	}
}

func TestEnsembleManager_Helpers(t *testing.T) {
	m := &EnsembleManager{}

	// Test tmuxClient returns default
	client := m.tmuxClient()
	if client == nil {
		t.Error("tmuxClient() should return default client")
	}

	// Test sessionOrchestrator returns new instance
	orch := m.sessionOrchestrator()
	if orch == nil {
		t.Error("sessionOrchestrator() should return new instance")
	}

	// Test paneLauncher returns new instance
	launcher := m.paneLauncher()
	if launcher == nil {
		t.Error("paneLauncher() should return new instance")
	}

	// Test promptInjector returns new instance
	injector := m.promptInjector()
	if injector == nil {
		t.Error("promptInjector() should return new instance")
	}

	// Test logger returns default
	logger := m.logger()
	if logger == nil {
		t.Error("logger() should return default logger")
	}
}

func TestEnsembleManager_HelpersWithValues(t *testing.T) {
	m := NewEnsembleManager()
	m.TmuxClient = tmux.DefaultClient

	// When set, should return the set value
	client := m.tmuxClient()
	if client != tmux.DefaultClient {
		t.Error("tmuxClient() should return set client")
	}

	// Test logger with set value
	if m.logger() == nil {
		t.Error("logger() should return set logger")
	}
}

func TestSpawnEnsemble_NilConfig(t *testing.T) {
	m := NewEnsembleManager()
	_, err := m.SpawnEnsemble(nil, nil)
	if err == nil {
		t.Fatal("expected error for nil config")
	}
	if err.Error() != "ensemble config is nil" {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestSpawnEnsemble_EmptySessionName(t *testing.T) {
	m := NewEnsembleManager()
	cfg := &EnsembleConfig{
		Question: "test question",
		Modes:    []string{"deductive"},
	}
	_, err := m.SpawnEnsemble(nil, cfg)
	if err == nil {
		t.Fatal("expected error for empty session name")
	}
	if err.Error() != "session name is required" {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestSpawnEnsemble_InvalidSessionName(t *testing.T) {
	m := NewEnsembleManager()
	cfg := &EnsembleConfig{
		SessionName: "invalid:name",
		Question:    "test question",
		Modes:       []string{"deductive"},
	}
	_, err := m.SpawnEnsemble(nil, cfg)
	if err == nil {
		t.Fatal("expected error for invalid session name")
	}
}

func TestSpawnEnsemble_EmptyQuestion(t *testing.T) {
	m := NewEnsembleManager()
	cfg := &EnsembleConfig{
		SessionName: "valid-session",
		Question:    "   ",
		Modes:       []string{"deductive"},
	}
	_, err := m.SpawnEnsemble(nil, cfg)
	if err == nil {
		t.Fatal("expected error for empty question")
	}
	if err.Error() != "question is required" {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestSpawnEnsemble_NoModesOrEnsemble(t *testing.T) {
	m := NewEnsembleManager()
	cfg := &EnsembleConfig{
		SessionName: "valid-session",
		Question:    "test question",
	}
	_, err := m.SpawnEnsemble(nil, cfg)
	if err == nil {
		t.Fatal("expected error when neither ensemble nor modes provided")
	}
	if err.Error() != "either ensemble name or explicit modes are required" {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestSpawnEnsemble_BothModesAndEnsemble(t *testing.T) {
	m := NewEnsembleManager()
	cfg := &EnsembleConfig{
		SessionName: "valid-session",
		Question:    "test question",
		Ensemble:    "diagnosis",
		Modes:       []string{"deductive"},
	}
	_, err := m.SpawnEnsemble(nil, cfg)
	if err == nil {
		t.Fatal("expected error when both ensemble and modes provided")
	}
	if err.Error() != "ensemble name and explicit modes are mutually exclusive" {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestAssignModes_RoundRobin(t *testing.T) {
	catalog := testModeCatalog(t)
	panes := []tmux.Pane{
		{Title: "pane-1", Type: tmux.AgentClaude, Index: 1, NTMIndex: 1},
		{Title: "pane-2", Type: tmux.AgentCodex, Index: 2, NTMIndex: 2},
	}
	modeIDs := []string{"deductive", "abductive"}

	assignments, err := assignModes("round-robin", modeIDs, nil, panes, catalog)
	if err != nil {
		t.Fatalf("assignModes error: %v", err)
	}
	if len(assignments) != 2 {
		t.Fatalf("expected 2 assignments, got %d", len(assignments))
	}
}

func TestAssignModes_Category(t *testing.T) {
	catalog := testModeCatalog(t)
	panes := []tmux.Pane{
		{Title: "pane-1", Type: tmux.AgentClaude, Index: 1, NTMIndex: 1},
		{Title: "pane-2", Type: tmux.AgentCodex, Index: 2, NTMIndex: 2},
	}
	modeIDs := []string{"deductive", "practical"}

	assignments, err := assignModes("category", modeIDs, nil, panes, catalog)
	if err != nil {
		t.Fatalf("assignModes error: %v", err)
	}
	if len(assignments) != 2 {
		t.Fatalf("expected 2 assignments, got %d", len(assignments))
	}
}

func TestAssignModes_Affinity(t *testing.T) {
	catalog := testModeCatalog(t)
	panes := []tmux.Pane{
		{Title: "pane-1", Type: tmux.AgentClaude, Index: 1, NTMIndex: 1},
		{Title: "pane-2", Type: tmux.AgentCodex, Index: 2, NTMIndex: 2},
	}
	modeIDs := []string{"deductive", "practical"}

	assignments, err := assignModes("affinity", modeIDs, nil, panes, catalog)
	if err != nil {
		t.Fatalf("assignModes error: %v", err)
	}
	if len(assignments) != 2 {
		t.Fatalf("expected 2 assignments, got %d", len(assignments))
	}
}

func TestAssignModes_Explicit(t *testing.T) {
	catalog := testModeCatalog(t)
	panes := []tmux.Pane{
		{Title: "pane-1", Type: tmux.AgentClaude, Index: 1, NTMIndex: 1},
		{Title: "pane-2", Type: tmux.AgentCodex, Index: 2, NTMIndex: 2},
	}
	explicitSpecs := []string{"deductive:cc", "abductive:cod"}

	assignments, err := assignModes("explicit", []string{"deductive", "abductive"}, explicitSpecs, panes, catalog)
	if err != nil {
		t.Fatalf("assignModes error: %v", err)
	}
	if len(assignments) != 2 {
		t.Fatalf("expected 2 assignments, got %d", len(assignments))
	}
}

func TestAssignModes_ExplicitMissingSpecs(t *testing.T) {
	catalog := testModeCatalog(t)
	panes := []tmux.Pane{
		{Title: "pane-1", Type: tmux.AgentClaude, Index: 1, NTMIndex: 1},
	}

	_, err := assignModes("explicit", []string{"deductive"}, nil, panes, catalog)
	if err == nil {
		t.Fatal("expected error when explicit specs missing")
	}
}

func TestAssignModes_NoModes(t *testing.T) {
	catalog := testModeCatalog(t)
	panes := []tmux.Pane{
		{Title: "pane-1", Type: tmux.AgentClaude, Index: 1, NTMIndex: 1},
	}

	_, err := assignModes("round-robin", nil, nil, panes, catalog)
	if err == nil {
		t.Fatal("expected error when no modes provided")
	}
}

func TestAssignModes_NoPanes(t *testing.T) {
	catalog := testModeCatalog(t)

	_, err := assignModes("round-robin", []string{"deductive"}, nil, nil, catalog)
	if err == nil {
		t.Fatal("expected error when no panes available")
	}
}

func TestResolveEnsembleConfig_NotFoundEnsemble(t *testing.T) {
	catalog := testModeCatalog(t)
	registry := NewEnsembleRegistry(nil, catalog)

	cfg := &EnsembleConfig{
		SessionName: "demo",
		Question:    "test",
		Ensemble:    "nonexistent",
	}

	_, _, _, err := resolveEnsembleConfig(cfg, catalog, registry)
	if err == nil {
		t.Fatal("expected error for nonexistent ensemble")
	}
}

func TestResolveEnsembleConfig_ModeRefs(t *testing.T) {
	catalog := testModeCatalog(t)
	registry := NewEnsembleRegistry(nil, catalog)

	cfg := &EnsembleConfig{
		SessionName: "demo",
		Question:    "test",
		Modes:       []string{"deductive", "abductive"},
	}

	modeIDs, resolved, explicitSpecs, err := resolveEnsembleConfig(cfg, catalog, registry)
	if err != nil {
		t.Fatalf("resolveEnsembleConfig error: %v", err)
	}
	if len(modeIDs) != 2 {
		t.Fatalf("modeIDs len = %d, want 2", len(modeIDs))
	}
	if explicitSpecs != nil {
		t.Fatal("explicit specs should be nil for mode refs path")
	}
	if resolved.presetName != "" {
		t.Fatal("presetName should be empty for mode refs path")
	}
}

func TestResolveEnsembleConfig_EmptyModeRef(t *testing.T) {
	catalog := testModeCatalog(t)
	registry := NewEnsembleRegistry(nil, catalog)

	cfg := &EnsembleConfig{
		SessionName: "demo",
		Question:    "test",
		Modes:       []string{"deductive", ""},
	}

	_, _, _, err := resolveEnsembleConfig(cfg, catalog, registry)
	if err == nil {
		t.Fatal("expected error for empty mode reference")
	}
}

func TestApplyConfigOverrides_AllFields(t *testing.T) {
	resolved := &resolvedEnsembleConfig{
		synthesis: DefaultSynthesisConfig(),
		budget:    DefaultBudgetConfig(),
		cache:     DefaultCacheConfig(),
	}

	cfg := &EnsembleConfig{
		Synthesis: SynthesisConfig{Strategy: StrategyCreative},
		Budget: BudgetConfig{
			MaxTokensPerMode:       5000,
			MaxTotalTokens:         100000,
			SynthesisReserveTokens: 8000,
			ContextReserveTokens:   4000,
			TimeoutPerMode:         120,
			TotalTimeout:           600,
			MaxRetries:             5,
		},
		Cache: CacheConfig{
			Enabled:    true,
			MaxEntries: 100,
			TTL:        3600,
			CacheDir:   "/tmp/cache",
		},
		CacheOverride: true,
	}

	applyConfigOverrides(cfg, resolved)

	if resolved.synthesis.Strategy != StrategyCreative {
		t.Errorf("synthesis strategy = %q, want %q", resolved.synthesis.Strategy, StrategyCreative)
	}
	if resolved.budget.MaxTokensPerMode != 5000 {
		t.Errorf("budget MaxTokensPerMode = %d, want 5000", resolved.budget.MaxTokensPerMode)
	}
	if resolved.budget.MaxTotalTokens != 100000 {
		t.Errorf("budget MaxTotalTokens = %d, want 100000", resolved.budget.MaxTotalTokens)
	}
	if resolved.budget.SynthesisReserveTokens != 8000 {
		t.Errorf("budget SynthesisReserveTokens = %d, want 8000", resolved.budget.SynthesisReserveTokens)
	}
	if resolved.budget.ContextReserveTokens != 4000 {
		t.Errorf("budget ContextReserveTokens = %d, want 4000", resolved.budget.ContextReserveTokens)
	}
	if resolved.budget.TimeoutPerMode != 120 {
		t.Errorf("budget TimeoutPerMode = %d, want 120", resolved.budget.TimeoutPerMode)
	}
	if resolved.budget.TotalTimeout != 600 {
		t.Errorf("budget TotalTimeout = %d, want 600", resolved.budget.TotalTimeout)
	}
	if resolved.budget.MaxRetries != 5 {
		t.Errorf("budget MaxRetries = %d, want 5", resolved.budget.MaxRetries)
	}
	if !resolved.cache.Enabled {
		t.Error("cache should be enabled")
	}
}

func TestValidateResolvedConfig_NilResolved(t *testing.T) {
	catalog := testModeCatalog(t)
	err := validateResolvedConfig(nil, []string{"deductive"}, catalog, true)
	if err == nil {
		t.Fatal("expected error for nil resolved config")
	}
}

func TestValidateResolvedConfig_NilCatalog(t *testing.T) {
	resolved := &resolvedEnsembleConfig{
		synthesis: DefaultSynthesisConfig(),
		budget:    DefaultBudgetConfig(),
	}
	err := validateResolvedConfig(resolved, []string{"deductive"}, nil, true)
	if err == nil {
		t.Fatal("expected error for nil catalog")
	}
}

func TestParseModeRefs_Valid(t *testing.T) {
	refs, err := parseModeRefs([]string{"deductive", "A1", "abductive"})
	if err != nil {
		t.Fatalf("parseModeRefs error: %v", err)
	}
	if len(refs) != 3 {
		t.Fatalf("refs len = %d, want 3", len(refs))
	}
}

func TestParseModeRefs_Empty(t *testing.T) {
	_, err := parseModeRefs([]string{"deductive", ""})
	if err == nil {
		t.Fatal("expected error for empty mode reference")
	}
}

func TestExplicitModeIDs_DuplicateHandling(t *testing.T) {
	specs := []string{"deductive:cc", "deductive:cod", "abductive:cc"}
	modeIDs := explicitModeIDs(specs)
	if len(modeIDs) != 2 {
		t.Fatalf("expected 2 unique mode IDs, got %d", len(modeIDs))
	}
}

func TestExplicitModeIDs_EmptySpecs(t *testing.T) {
	modeIDs := explicitModeIDs(nil)
	if len(modeIDs) != 0 {
		t.Fatalf("expected 0 mode IDs for empty specs, got %d", len(modeIDs))
	}
}

func TestBuildPaneSpecs_ZeroModes(t *testing.T) {
	cfg := &EnsembleConfig{}
	_, err := buildPaneSpecs(cfg, 0)
	if err == nil {
		t.Fatal("expected error for zero modes")
	}
}

func TestBuildPaneTargetMap_EmptyID(t *testing.T) {
	panes := []tmux.Pane{
		{ID: "", Title: "pane-1", Index: 1},
	}
	targets := buildPaneTargetMap("session", panes)
	// With empty ID, it should use fallback target
	if targets["pane-1"] == "" {
		t.Fatal("expected target for pane-1 to be set")
	}
}

func TestIsModeCode_Various(t *testing.T) {
	tests := []struct {
		input    string
		expected bool
	}{
		{"A1", true},
		{"B2", true},
		{"C10", true},
		{"deductive", false},
		{"abc", false},
		{"1A", false},
		{"", false},
		{"A", false},
		{"a1", true}, // lowercase should still match
	}

	for _, tt := range tests {
		result := isModeCode(tt.input)
		if result != tt.expected {
			t.Errorf("isModeCode(%q) = %v, want %v", tt.input, result, tt.expected)
		}
	}
}

func TestNormalizeExplicitSpecs_EmptyAgent(t *testing.T) {
	catalog := testModeCatalog(t)
	_, err := normalizeExplicitSpecs([]string{"deductive:"}, catalog)
	if err == nil {
		t.Fatal("expected error for empty agent type")
	}
}

func TestNormalizeExplicitSpecs_InvalidFormat(t *testing.T) {
	catalog := testModeCatalog(t)
	_, err := normalizeExplicitSpecs([]string{"deductive"}, catalog)
	if err == nil {
		t.Fatal("expected error for invalid format")
	}
}

func TestNormalizeExplicitSpecs_EmptyResult(t *testing.T) {
	catalog := testModeCatalog(t)
	_, err := normalizeExplicitSpecs([]string{"", "   "}, catalog)
	if err == nil {
		t.Fatal("expected error for empty result")
	}
}

func TestEnsembleManager_CatalogHelper(t *testing.T) {
	catalog := testModeCatalog(t)
	m := &EnsembleManager{
		Catalog: catalog,
	}

	got, err := m.catalog()
	if err != nil {
		t.Fatalf("catalog() error: %v", err)
	}
	if got != catalog {
		t.Error("catalog() should return set catalog")
	}
}

func TestEnsembleManager_RegistryHelper(t *testing.T) {
	catalog := testModeCatalog(t)
	registry := NewEnsembleRegistry(nil, catalog)
	m := &EnsembleManager{
		Registry: registry,
	}

	got, err := m.registry(catalog)
	if err != nil {
		t.Fatalf("registry() error: %v", err)
	}
	if got != registry {
		t.Error("registry() should return set registry")
	}
}

func TestEnsembleManager_RegistryHelper_CreateNew(t *testing.T) {
	// With test catalog, the built-in ensembles may fail to load
	// because they reference modes not in the test catalog.
	// This test just verifies the code path is exercised.
	catalog := testModeCatalog(t)
	m := &EnsembleManager{
		Catalog: catalog,
	}

	// This may error due to missing modes in test catalog
	// We're just testing the code path
	_, _ = m.registry(catalog)
}

func TestEnsembleManager_SessionOrchestrator_CreateNew(t *testing.T) {
	m := &EnsembleManager{}
	orch := m.sessionOrchestrator()
	if orch == nil {
		t.Error("sessionOrchestrator() should create new instance")
	}
}

func TestEnsembleManager_PaneLauncher_CreateNew(t *testing.T) {
	m := &EnsembleManager{}
	launcher := m.paneLauncher()
	if launcher == nil {
		t.Error("paneLauncher() should create new instance")
	}
}

func TestEnsembleManager_PromptInjector_CreateNew(t *testing.T) {
	m := &EnsembleManager{}
	injector := m.promptInjector()
	if injector == nil {
		t.Error("promptInjector() should create new instance")
	}
}

func TestEnsembleManager_Catalog_CreateNew(t *testing.T) {
	m := &EnsembleManager{}
	// This will try to load from the default location
	// If it fails, that's okay for the test - we just want to verify the path
	_, _ = m.catalog()
}

func TestResolveEnsembleConfig_WithAllowAdvanced(t *testing.T) {
	catalog := testModeCatalog(t)
	preset := EnsemblePreset{
		Name:          "advanced-test",
		Modes:         []ModeRef{ModeRefFromID("advanced-mode"), ModeRefFromID("deductive")},
		AllowAdvanced: false, // Preset disallows advanced
	}
	registry := NewEnsembleRegistry([]EnsemblePreset{preset}, catalog)

	// AllowAdvanced in config should override preset
	cfg := &EnsembleConfig{
		SessionName:   "demo",
		Question:      "Test advanced",
		Ensemble:      "advanced-test",
		AllowAdvanced: true, // Config allows advanced
	}

	modeIDs, _, _, err := resolveEnsembleConfig(cfg, catalog, registry)
	if err != nil {
		t.Fatalf("resolveEnsembleConfig error: %v", err)
	}
	if len(modeIDs) != 2 {
		t.Fatalf("expected 2 modes with AllowAdvanced=true, got %d", len(modeIDs))
	}
}

func TestNormalizeExplicitSpecs_ValidCommaSpecs(t *testing.T) {
	catalog := testModeCatalog(t)
	specs, err := normalizeExplicitSpecs([]string{"deductive:cc,abductive:cod"}, catalog)
	if err != nil {
		t.Fatalf("normalizeExplicitSpecs error: %v", err)
	}
	if len(specs) != 2 {
		t.Fatalf("expected 2 specs, got %d", len(specs))
	}
}

func TestNormalizeExplicitSpecs_ModeCode(t *testing.T) {
	catalog := testModeCatalog(t)
	specs, err := normalizeExplicitSpecs([]string{"A1:cc"}, catalog)
	if err != nil {
		t.Fatalf("normalizeExplicitSpecs error: %v", err)
	}
	if len(specs) != 1 {
		t.Fatalf("expected 1 spec, got %d", len(specs))
	}
	// A1 should resolve to deductive
	if specs[0] != "deductive:cc" {
		t.Fatalf("expected deductive:cc, got %s", specs[0])
	}
}

func TestExplicitModeIDs_EmptySpec(t *testing.T) {
	specs := []string{":cc"} // Empty mode ID
	modeIDs := explicitModeIDs(specs)
	if len(modeIDs) != 0 {
		t.Fatalf("expected 0 mode IDs for empty mode, got %d", len(modeIDs))
	}
}

func TestAssignModes_EmptyStrategy(t *testing.T) {
	catalog := testModeCatalog(t)
	panes := []tmux.Pane{
		{Title: "pane-1", Type: tmux.AgentClaude, Index: 1, NTMIndex: 1},
		{Title: "pane-2", Type: tmux.AgentCodex, Index: 2, NTMIndex: 2},
	}
	modeIDs := []string{"deductive", "practical"}

	// Empty strategy should default to affinity
	assignments, err := assignModes("", modeIDs, nil, panes, catalog)
	if err != nil {
		t.Fatalf("assignModes error: %v", err)
	}
	if len(assignments) != 2 {
		t.Fatalf("expected 2 assignments, got %d", len(assignments))
	}
}

func TestOrderAssignmentsForTimebox_PrioritizesCoreAndValue(t *testing.T) {
	catalog := testModeCatalog(t)
	assignments := []ModeAssignment{
		{ModeID: "advanced-mode", Status: AssignmentPending},
		{ModeID: "deductive", Status: AssignmentPending},
		{ModeID: "abductive", Status: AssignmentPending},
	}

	order := orderAssignmentsForTimebox(assignments, catalog)
	if len(order) != 3 {
		t.Fatalf("order len = %d, want 3", len(order))
	}

	got := []string{
		assignments[order[0]].ModeID,
		assignments[order[1]].ModeID,
		assignments[order[2]].ModeID,
	}
	want := []string{"abductive", "deductive", "advanced-mode"}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("order[%d] = %q, want %q (full order=%v)", i, got[i], want[i], got)
		}
	}
}

func TestMarkAssignmentsSkipped(t *testing.T) {
	assignments := []ModeAssignment{
		{ModeID: "mode-a", Status: AssignmentPending},
		{ModeID: "mode-b", Status: AssignmentPending},
	}

	skipped := markAssignmentsSkipped(assignments, []int{1}, "timeout reached")
	if len(skipped) != 1 || skipped[0] != "mode-b" {
		t.Fatalf("skipped = %v, want [mode-b]", skipped)
	}
	if assignments[1].Status != AssignmentError {
		t.Fatalf("assignment[1] status = %q, want %q", assignments[1].Status, AssignmentError)
	}
	if assignments[1].Error != "timeout reached" {
		t.Fatalf("assignment[1] error = %q, want %q", assignments[1].Error, "timeout reached")
	}
	if assignments[1].CompletedAt == nil {
		t.Fatal("assignment[1] CompletedAt should be set")
	}
	if assignments[0].Status != AssignmentPending {
		t.Fatalf("assignment[0] status = %q, want %q", assignments[0].Status, AssignmentPending)
	}
}

func TestSpawnCompletionStatus(t *testing.T) {
	tests := []struct {
		name         string
		skipInject   bool
		successes    int
		injectErrors int
		skippedModes int
		want         EnsembleStatus
	}{
		{
			name:       "skip inject leaves session ready",
			skipInject: true,
			want:       EnsembleReady,
		},
		{
			name:         "all injections failed",
			injectErrors: 2,
			want:         EnsembleError,
		},
		{
			name:         "all injections skipped",
			skippedModes: 3,
			want:         EnsembleError,
		},
		{
			name:      "successful injection activates session",
			successes: 1,
			want:      EnsembleActive,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := spawnCompletionStatus(tt.skipInject, tt.successes, tt.injectErrors, tt.skippedModes); got != tt.want {
				t.Fatalf("spawnCompletionStatus(%v, %d, %d, %d) = %q, want %q", tt.skipInject, tt.successes, tt.injectErrors, tt.skippedModes, got, tt.want)
			}
		})
	}
}
