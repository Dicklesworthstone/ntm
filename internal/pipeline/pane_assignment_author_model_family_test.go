package pipeline

import "testing"

func TestForeachAuthorModelFamilyPrefersCanonicalKeys(t *testing.T) {
	item := map[string]interface{}{
		"author_model": "claude-sonnet-4",
		"model_family": "cc",
	}
	if got := foreachAuthorModelFamily(item); got != "cc" {
		t.Fatalf("foreachAuthorModelFamily() = %q, want cc", got)
	}
}

func TestForeachAuthorModelFamilyFallsBackToAuthorModel(t *testing.T) {
	item := map[string]interface{}{"author_model": "cod"}
	if got := foreachAuthorModelFamily(item); got != "cod" {
		t.Fatalf("foreachAuthorModelFamily() = %q, want cod", got)
	}
}

func TestForeachAuthorModelFamilyNormalizesVerboseAuthorModel(t *testing.T) {
	item := map[string]interface{}{"author_model": "claude-sonnet-4"}
	if got := foreachAuthorModelFamily(item); got != "cc" {
		t.Fatalf("foreachAuthorModelFamily() = %q, want cc", got)
	}
}

func TestForeachAuthorModelFamilySkipsBlankAliases(t *testing.T) {
	item := map[string]interface{}{
		"model_family": "   ",
		"family":       "",
		"type":         "gmi",
	}
	if got := foreachAuthorModelFamily(item); got != "gmi" {
		t.Fatalf("foreachAuthorModelFamily() = %q, want gmi", got)
	}
}

func TestSelectForeachPaneModelFamilyDifferencePrefersCanonicalOverVerboseAuthor(t *testing.T) {
	strategyPanes := []paneStrategyPane{
		{ID: "p1", ModelFamily: "cc"},
		{ID: "p2", ModelFamily: "cod"},
	}
	item := map[string]interface{}{
		"author_model": "claude-sonnet-4",
		"model_family": "cc",
	}

	got, _, _, err := selectForeachPane("by_model_family_difference", strategyPanes, nil, item, 0)
	if err != nil {
		t.Fatalf("selectForeachPane() error = %v", err)
	}
	if got != "p2" {
		t.Fatalf("selectForeachPane() = %q, want p2", got)
	}
}

func TestForeachAuthorModelFamilyForPanesPrefersPaneVocabulary(t *testing.T) {
	strategyPanes := []paneStrategyPane{
		{ID: "p1", ModelFamily: "codex"},
		{ID: "p2", ModelFamily: "claude"},
	}
	item := map[string]interface{}{"author_model": "openai-codex"}

	if got := foreachAuthorModelFamilyForPanes(item, strategyPanes); got != "codex" {
		t.Fatalf("foreachAuthorModelFamilyForPanes() = %q, want codex", got)
	}
}

func TestForeachAuthorModelFamilyForPanesIgnoresExcludedPanes(t *testing.T) {
	// bd-i310h: an excluded pane (Excluded:true) must not be the source of
	// the pane-vocabulary token used to map the author model family.
	// Otherwise byModelFamilyDifference exact-compares the available panes
	// against a token that came from a stale/excluded pane and silently
	// routes same-family work to the wrong family.
	strategyPanes := []paneStrategyPane{
		{ID: "old", ModelFamily: "cc", Excluded: true},
		{ID: "same", ModelFamily: "claude"},
		{ID: "other", ModelFamily: "cod"},
	}
	item := map[string]interface{}{"author_model": "claude-sonnet-4"}

	if got := foreachAuthorModelFamilyForPanes(item, strategyPanes); got != "claude" {
		t.Fatalf("foreachAuthorModelFamilyForPanes() = %q, want claude (ignore excluded 'cc' pane)", got)
	}

	// Composing with byModelFamilyDifference: the comparison should see
	// "claude" matching the available "same" pane, returning errNoModelFamilyPane
	// because there is no AVAILABLE different-family pane in the claude
	// canonical group; "other" is cod, and cc-pane is excluded.
	got, _, err := byModelFamilyDifference(strategyPanes, foreachAuthorModelFamilyForPanes(item, strategyPanes))
	if err != nil {
		t.Fatalf("byModelFamilyDifference() error = %v", err)
	}
	// The expected pane is "other" (cod) since the author family resolves
	// to "claude" and we must pick a DIFFERENT family pane that is available.
	if got != "other" {
		t.Fatalf("byModelFamilyDifference() = %q, want %q (different-family available pane)", got, "other")
	}
}

func TestSelectForeachPaneByModelFamilyRoutesCanonicalToVariantPanes(t *testing.T) {
	// Phase-6-style foreach over model families typically uses canonical tokens
	// (cc, cod, gmi) or verbose names (claude-sonnet-4) in items, while panes
	// often store ModelFamily as the bare variant (opus, sonnet, pro). Without
	// normalizing through the pane vocabulary, byModelFamily exact-compares
	// strings and returns errNoModelFamilyPane.
	cases := []struct {
		name     string
		paneFam  string
		itemFam  string
		wantPane string
	}{
		{name: "cc routes to opus pane", paneFam: "opus", itemFam: "cc", wantPane: "p1"},
		{name: "claude routes to sonnet pane", paneFam: "sonnet", itemFam: "claude", wantPane: "p1"},
		{name: "verbose claude-sonnet-4 routes to haiku pane", paneFam: "haiku", itemFam: "claude-sonnet-4", wantPane: "p1"},
		{name: "gmi routes to pro pane", paneFam: "pro", itemFam: "gmi", wantPane: "p1"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			strategyPanes := []paneStrategyPane{
				{ID: "p1", ModelFamily: tc.paneFam},
				{ID: "p2", ModelFamily: "cod"},
			}
			item := map[string]interface{}{"model_family": tc.itemFam}

			got, _, _, err := selectForeachPane("by_model_family", strategyPanes, nil, item, 0)
			if err != nil {
				t.Fatalf("selectForeachPane() error = %v", err)
			}
			if got != tc.wantPane {
				t.Fatalf("selectForeachPane() = %q, want %q", got, tc.wantPane)
			}
		})
	}
}

func TestSelectForeachPaneModelFamilyDifferenceTreatsGeminiVariantsAsSameFamily(t *testing.T) {
	// Gemini panes are commonly stored with bare variant ModelFamily values
	// like "pro", "flash", or "ultra" (paneMetadataFromTmuxPane variant
	// fallback). Without grouping those under Gemini, by_model_family_difference
	// would route a Gemini-authored item back to the same-family pane,
	// defeating the cross-family debate contract.
	cases := []struct {
		name        string
		variant     string
		authorModel string
	}{
		{name: "pro variant", variant: "pro", authorModel: "gemini-pro"},
		{name: "flash variant", variant: "flash", authorModel: "gemini-1.5-flash"},
		{name: "ultra variant", variant: "ultra", authorModel: "google-gemini-ultra"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			strategyPanes := []paneStrategyPane{
				{ID: "p1", ModelFamily: tc.variant},
				{ID: "p2", ModelFamily: "cod"},
			}
			item := map[string]interface{}{"author_model": tc.authorModel}

			got, _, _, err := selectForeachPane("by_model_family_difference", strategyPanes, nil, item, 0)
			if err != nil {
				t.Fatalf("selectForeachPane() error = %v", err)
			}
			if got != "p2" {
				t.Fatalf("selectForeachPane() = %q, want p2 (Gemini-authored work must avoid the Gemini-variant pane)", got)
			}
		})
	}
}

func TestSelectForeachPaneModelFamilyDifferenceTreatsClaudeVariantsAsSameFamily(t *testing.T) {
	// Pane spawn paths set ModelFamily to bare variant names like "opus",
	// "sonnet", or "haiku" via paneMetadataFromTmuxPane. Without grouping
	// those under Claude, by_model_family_difference would compare
	// "opus" != "cc" exactly and route the Claude-authored work back to a
	// Claude pane — defeating the cross-family debate contract.
	cases := []struct {
		name        string
		opusVariant string
		authorModel string
	}{
		{name: "opus variant", opusVariant: "opus", authorModel: "claude-sonnet-4"},
		{name: "sonnet variant", opusVariant: "sonnet", authorModel: "claude-opus-4"},
		{name: "haiku variant", opusVariant: "haiku", authorModel: "anthropic-claude-3"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			strategyPanes := []paneStrategyPane{
				{ID: "p1", ModelFamily: tc.opusVariant},
				{ID: "p2", ModelFamily: "cod"},
			}
			item := map[string]interface{}{"author_model": tc.authorModel}

			got, _, _, err := selectForeachPane("by_model_family_difference", strategyPanes, nil, item, 0)
			if err != nil {
				t.Fatalf("selectForeachPane() error = %v", err)
			}
			if got != "p2" {
				t.Fatalf("selectForeachPane() = %q, want p2 (Claude-authored work must avoid the Claude-variant pane)", got)
			}
		})
	}
}
