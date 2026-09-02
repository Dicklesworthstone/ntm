package tmux

import (
	"strings"
	"testing"
)

// The @ntm_agent_type pane option recorded by adopt must outrank the pane
// title: OpenCode rewrites its own title continuously, so a title-only
// registration is undone within seconds (ntm#266).

func paneLine(id, index, title, command, pid, window, recordedType string) string {
	sep := FieldSeparator
	return strings.Join([]string{id, index, title, command, "80", "24", "1", pid, window, recordedType}, sep)
}

func TestParsePaneLine_RecordedAgentTypeOutranksRewrittenTitle(t *testing.T) {
	t.Parallel()

	// OpenCode has already replaced the "sess__oc_1" title adopt set. The
	// current command is a shell wrapper so the command heuristic cannot
	// rescue it either; only the recorded option can.
	pane, err := parsePaneLine(paneLine("%7", "2", "opencode | dev", "bash", "0", "1", "oc"), FieldSeparator)
	if err != nil {
		t.Fatalf("parsePaneLine: %v", err)
	}
	if pane.Type != AgentOpencode {
		t.Fatalf("Type = %q, want %q (recorded pane option)", pane.Type, AgentOpencode)
	}
}

func TestParsePaneLine_RecordedAgentTypeWinsOverNTMTitle(t *testing.T) {
	t.Parallel()

	// The option is what ntm itself recorded most recently; a stale NTM
	// title (for example from an earlier adopt as a different type) must not
	// override it, but index/variant/tags still come from the title.
	pane, err := parsePaneLine(paneLine("%3", "0", "sess__cc_4_opus[api]", "zsh", "0", "0", "codex"), FieldSeparator)
	if err != nil {
		t.Fatalf("parsePaneLine: %v", err)
	}
	if pane.Type != AgentCodex {
		t.Fatalf("Type = %q, want %q", pane.Type, AgentCodex)
	}
	if pane.NTMIndex != 4 || pane.Variant != "opus" || len(pane.Tags) != 1 || pane.Tags[0] != "api" {
		t.Fatalf("title metadata lost: index=%d variant=%q tags=%v", pane.NTMIndex, pane.Variant, pane.Tags)
	}
}

func TestParsePaneLine_UnknownRecordedTypeFallsBackToHeuristics(t *testing.T) {
	t.Parallel()

	// A hand-edited or stale option naming a type ntm does not know must
	// not be trusted; the normal title/command chain applies instead.
	pane, err := parsePaneLine(paneLine("%3", "0", "sess__cc_1", "claude", "0", "0", "not-an-agent"), FieldSeparator)
	if err != nil {
		t.Fatalf("parsePaneLine: %v", err)
	}
	if pane.Type != AgentClaude {
		t.Fatalf("Type = %q, want %q", pane.Type, AgentClaude)
	}
}

func TestParsePaneLine_AbsentOptionKeepsLegacyBehaviour(t *testing.T) {
	t.Parallel()

	// tmux servers without pane options expand #{@ntm_agent_type} to "".
	pane, err := parsePaneLine(paneLine("%1", "0", "sess__gmi_2", "gemini", "0", "0", ""), FieldSeparator)
	if err != nil {
		t.Fatalf("parsePaneLine: %v", err)
	}
	if pane.Type != AgentGemini || pane.NTMIndex != 2 {
		t.Fatalf("pane = %+v, want gemini index 2", pane)
	}

	// Older 9-field callers of parsePaneFromParts remain valid.
	legacy, err := parsePaneFromParts(
		[]string{"%1", "0", "sess__cod_1", "codex", "80", "24", "1"},
		[]string{"0", "0"},
	)
	if err != nil {
		t.Fatalf("parsePaneFromParts legacy shape: %v", err)
	}
	if legacy.Type != AgentCodex {
		t.Fatalf("legacy Type = %q, want %q", legacy.Type, AgentCodex)
	}
}

func TestParsePaneLine_OpenCodeSelfTitleAndCommand(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name    string
		title   string
		command string
		want    AgentType
	}{
		{"opencode binary as current command", "some shell prompt", "opencode", AgentOpencode},
		{"opencode binary by path", "whatever", "/usr/local/bin/opencode", AgentOpencode},
		{"opencode self-set title under a wrapper", "opencode | dev", "bash", AgentOpencode},
		{"opencode self-set title, case-insensitive", "OpenCode | /work/repo", "sh", AgentOpencode},
		{"opencode self-set title without directory", "opencode", "zsh", AgentOpencode},
		{"project merely named opencode is not an agent", "my-opencode-notes | dev", "zsh", AgentUser},
		{"plain shell pane", "user | sess | dev", "zsh", AgentUser},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			pane, err := parsePaneLine(paneLine("%9", "1", tc.title, tc.command, "0", "0", ""), FieldSeparator)
			if err != nil {
				t.Fatalf("parsePaneLine: %v", err)
			}
			if pane.Type != tc.want {
				t.Fatalf("Type = %q, want %q", pane.Type, tc.want)
			}
		})
	}
}

func TestParsePaneAgentTypeOption(t *testing.T) {
	t.Parallel()

	cases := map[string]AgentType{
		"":          AgentUnknown,
		"   ":       AgentUnknown,
		"oc":        AgentOpencode,
		"opencode":  AgentOpencode,
		" cc ":      AgentClaude,
		"codex":     AgentCodex,
		"user":      AgentUser,
		"unknown":   AgentUnknown,
		"garbage-x": AgentUnknown,
	}
	for raw, want := range cases {
		if got := ParsePaneAgentTypeOption(raw); got != want {
			t.Errorf("ParsePaneAgentTypeOption(%q) = %q, want %q", raw, got, want)
		}
	}
}

func TestParsePaneLine_ProviderIdentityMetadataIsStrictAndSafe(t *testing.T) {
	t.Parallel()
	hash := strings.Repeat("a", 64)
	receipt := strings.Repeat("b", 64)
	line := strings.Join([]string{"%7", "2", "sess__zai_1", "zsh", "80", "24", "1", "0", "1", "zai", "zai-kevin-glm53", hash, "qualified", receipt}, FieldSeparator)
	pane, err := parsePaneLine(line, FieldSeparator)
	if err != nil {
		t.Fatalf("parsePaneLine: %v", err)
	}
	if pane.ProviderProfile != "zai-kevin-glm53" || pane.ProviderIdentitySHA256 != hash || pane.ModelProbeState != "qualified" || pane.ModelProbeReceiptSHA256 != receipt {
		t.Fatalf("provider pane metadata=%+v", pane)
	}
	if pane.ProviderIdentityState != ProviderIdentityStateProfileAttested || !pane.ProviderIdentityBound() {
		t.Fatalf("provider identity state=%q bound=%v, want profile-attested bound pane", pane.ProviderIdentityState, pane.ProviderIdentityBound())
	}

	unsafe := strings.Join([]string{"%7", "2", "sess__zai_1", "zsh", "80", "24", "1", "0", "1", "zai", "zai-kevin-glm53", hash, "qualified", "https://token@example.invalid"}, FieldSeparator)
	pane, err = parsePaneLine(unsafe, FieldSeparator)
	if err != nil {
		t.Fatalf("parsePaneLine unsafe metadata: %v", err)
	}
	if pane.ProviderProfile != "" || pane.ProviderIdentitySHA256 != "" {
		t.Fatalf("unsafe metadata must be discarded, got %+v", pane)
	}
	if pane.ProviderIdentityState != ProviderIdentityStateUnboundInvalid || pane.ProviderIdentityBound() {
		t.Fatalf("unsafe provider identity state=%q bound=%v", pane.ProviderIdentityState, pane.ProviderIdentityBound())
	}

	missing := strings.Join([]string{"%7", "2", "sess__zai_1", "zsh", "80", "24", "1", "0", "1", "zai", "", "", "", ""}, FieldSeparator)
	pane, err = parsePaneLine(missing, FieldSeparator)
	if err != nil {
		t.Fatalf("parsePaneLine missing metadata: %v", err)
	}
	if pane.ProviderIdentityState != ProviderIdentityStateUnboundMissing || pane.ProviderIdentityBound() {
		t.Fatalf("missing provider identity state=%q bound=%v", pane.ProviderIdentityState, pane.ProviderIdentityBound())
	}
}

func TestSetProviderPaneIdentityRejectsUnsafeValues(t *testing.T) {
	t.Parallel()
	c := &Client{}
	valid := ProviderPaneIdentity{Profile: "zai-kevin-glm53", IdentitySHA256: strings.Repeat("a", 64), ModelProbeState: "qualified", ModelProbeReceiptSHA256: strings.Repeat("b", 64)}
	if err := validateProviderPaneIdentity(valid); err != nil {
		t.Fatalf("validate valid metadata: %v", err)
	}
	for _, bad := range []ProviderPaneIdentity{
		{Profile: "ZAI", IdentitySHA256: valid.IdentitySHA256, ModelProbeState: "qualified", ModelProbeReceiptSHA256: valid.ModelProbeReceiptSHA256},
		{Profile: valid.Profile, IdentitySHA256: "token", ModelProbeState: "qualified", ModelProbeReceiptSHA256: valid.ModelProbeReceiptSHA256},
		{Profile: valid.Profile, IdentitySHA256: valid.IdentitySHA256, ModelProbeState: "unprobed", ModelProbeReceiptSHA256: valid.ModelProbeReceiptSHA256},
	} {
		if err := c.SetProviderPaneIdentityContext(t.Context(), "%1", bad); err == nil {
			t.Fatalf("SetProviderPaneIdentityContext accepted unsafe metadata=%+v", bad)
		}
	}
}

func TestValidateProviderPaneIdentityAcceptsLiveVerifiedReceipt(t *testing.T) {
	metadata := ProviderPaneIdentity{Profile: "zai-kevin-glm53", IdentitySHA256: strings.Repeat("a", 64), ModelProbeState: "live_verified", ModelProbeReceiptSHA256: strings.Repeat("b", 64)}
	if err := validateProviderPaneIdentity(metadata); err != nil {
		t.Fatalf("live_verified metadata rejected: %v", err)
	}
}

func TestSetPaneAgentTypeRejectsUnknownType(t *testing.T) {
	t.Parallel()

	c := &Client{}
	if err := c.SetPaneAgentType("%1", AgentType("nope")); err == nil {
		t.Fatal("SetPaneAgentType accepted an unknown agent type")
	}
	if err := c.SetPaneAgentType("", AgentOpencode); err == nil {
		t.Fatal("SetPaneAgentType accepted an empty pane ID")
	}
	if err := c.SetPaneAgentIdentityContext(t.Context(), "%1", "proj__bad_1", AgentType("nope")); err == nil {
		t.Fatal("SetPaneAgentIdentityContext accepted an unknown agent type")
	}
}

// GH#268: lifecycle code knows the provider before launch. Persisting that
// knowledge must survive both a wrapper becoming pane_current_command and a
// TUI replacing the visible title.
func TestRealSetPaneAgentIdentitySurvivesWrapperAndTitleRewrite(t *testing.T) {
	skipIfNoTmux(t)

	session := createTestSession(t)
	panes, err := GetPanes(session)
	if err != nil || len(panes) == 0 {
		t.Fatalf("GetPanes: panes=%d err=%v", len(panes), err)
	}
	paneID := panes[0].ID

	for _, want := range []AgentType{AgentClaude, AgentCodex, AgentGrok} {
		title := FormatPaneName(session, string(want), 1, "")
		if err := SetPaneAgentIdentityContext(t.Context(), paneID, title, want); err != nil {
			t.Fatalf("SetPaneAgentIdentityContext(%s): %v", want, err)
		}
		if err := DefaultClient.RunSilent("select-pane", "-t", ExactTarget(paneID), "-T", "wrapper | changed by TUI"); err != nil {
			t.Fatalf("rewrite title for %s: %v", want, err)
		}

		got, err := GetPanes(session)
		if err != nil {
			t.Fatalf("GetPanes after %s title rewrite: %v", want, err)
		}
		if len(got) != 1 || got[0].Type != want {
			t.Fatalf("panes after %s title rewrite = %+v, want durable type %s", want, got, want)
		}
	}
}

// TestRealSetPaneAgentTypeSurvivesTitleRewrite drives a real tmux server: the
// recorded type must be reported by GetPanes even after the pane title is
// rewritten to OpenCode's own shape, which is exactly the ntm#266 scenario.
func TestRealSetPaneAgentTypeSurvivesTitleRewrite(t *testing.T) {
	skipIfNoTmux(t)

	session := createTestSession(t)
	panes, err := GetPanes(session)
	if err != nil || len(panes) == 0 {
		t.Fatalf("GetPanes: panes=%d err=%v", len(panes), err)
	}
	paneID := panes[0].ID

	if err := SetPaneAgentType(paneID, AgentOpencode); err != nil {
		t.Skipf("pane options unsupported by this tmux: %v", err)
	}
	// Simulate OpenCode overwriting the title after adopt renamed it.
	if err := DefaultClient.RunSilent("select-pane", "-t", ExactTarget(paneID), "-T", "opencode | dev"); err != nil {
		t.Fatalf("rewrite title: %v", err)
	}

	for _, lookup := range []struct {
		name string
		get  func() ([]Pane, error)
	}{
		{"GetPanes", func() ([]Pane, error) { return GetPanes(session) }},
		{"GetAllPanes", func() ([]Pane, error) {
			all, err := GetAllPanes()
			if err != nil {
				return nil, err
			}
			return all[session], nil
		}},
		{"GetPanesWithActivity", func() ([]Pane, error) {
			acts, err := GetPanesWithActivity(session)
			if err != nil {
				return nil, err
			}
			out := make([]Pane, 0, len(acts))
			for _, a := range acts {
				out = append(out, a.Pane)
			}
			return out, nil
		}},
	} {
		got, err := lookup.get()
		if err != nil {
			t.Fatalf("%s: %v", lookup.name, err)
		}
		found := false
		for _, p := range got {
			if p.ID != paneID {
				continue
			}
			found = true
			if p.Type != AgentOpencode {
				t.Fatalf("%s: Type = %q after title rewrite, want %q", lookup.name, p.Type, AgentOpencode)
			}
		}
		if !found {
			t.Fatalf("%s: pane %s missing from listing", lookup.name, paneID)
		}
	}
}
