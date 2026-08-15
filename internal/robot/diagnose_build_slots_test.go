package robot

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/Dicklesworthstone/ntm/internal/agentmail"
	"github.com/Dicklesworthstone/ntm/internal/tmux"
)

// buildSlotTestRegistry builds a session registry with three identities:
// LiveAgent (pane %1 alive), GoneAgent (pane %9 gone, token persisted),
// TokenlessAgent (pane %8 gone, no token).
func buildSlotTestRegistry(projectKey string) *agentmail.SessionAgentRegistry {
	registry := agentmail.NewSessionAgentRegistry("project", projectKey)
	registry.AddAgent("cc_1", "%1", "LiveAgent")
	registry.AddAgent("cc_2", "%9", "GoneAgent")
	registry.AddAgent("cod_1", "%8", "TokenlessAgent")
	registry.SetRegistrationToken("LiveAgent", "tok-live")
	registry.SetRegistrationToken("GoneAgent", "tok-gone")
	return registry
}

func buildSlotTestDeps(registry *agentmail.SessionAgentRegistry, leases []agentmail.BuildSlotLease, listErr error) diagnoseDependencies {
	return diagnoseDependencies{
		sessionExists: func(context.Context, string) (bool, error) { return true, nil },
		listPanes: func(context.Context, string) ([]tmux.Pane, error) {
			// User-type pane so pane health checks are skipped and the test
			// exercises only the build-slot path. %1 keeps LiveAgent live.
			return []tmux.Pane{{ID: "%1", Index: 1, Title: "cc_1", Type: tmux.AgentUser}}, nil
		},
		projectKey: func() (string, error) { return "/test/project", nil },
		loadAgentRegistry: func(session string, projectKeys ...string) (*agentmail.SessionAgentRegistry, error) {
			return registry, nil
		},
		listBuildSlotLeases: func(projectKey string, now time.Time) ([]agentmail.BuildSlotLease, error) {
			return leases, listErr
		},
		releaseBuildSlot: func(context.Context, string, *agentmail.SessionAgentRegistry, agentmail.BuildSlotLease) error {
			return nil
		},
	}
}

func buildSlotLeaseFor(agent, slot string) agentmail.BuildSlotLease {
	return agentmail.BuildSlotLease{
		Slot:      slot,
		Agent:     agent,
		Branch:    "main",
		Exclusive: true,
		ExpiresTS: agentmail.FlexTime{Time: time.Now().UTC().Add(45 * time.Minute)},
	}
}

func TestDiagnoseDetectsStaleBuildSlotLeases(t *testing.T) {
	registry := buildSlotTestRegistry("/test/project")
	leases := []agentmail.BuildSlotLease{
		buildSlotLeaseFor("LiveAgent", "frontend"),     // live holder: not stale
		buildSlotLeaseFor("GoneAgent", "frontend"),     // orphan with token: auto-fixable
		buildSlotLeaseFor("TokenlessAgent", "backend"), // orphan without token: manual
		buildSlotLeaseFor("OtherSessionAgent", "misc"), // unknown holder: not ours to touch
	}

	output, err := getDiagnoseWithDependencies(t.Context(), DiagnoseOptions{Session: "project", Pane: -1}, buildSlotTestDeps(registry, leases, nil))
	if err != nil {
		t.Fatalf("getDiagnoseWithDependencies: %v", err)
	}
	if !output.Success {
		t.Fatalf("diagnose failed: %+v", output.RobotResponse)
	}
	if output.BuildSlots == nil || !output.BuildSlots.Checked {
		t.Fatalf("build-slot check did not run: %+v", output.BuildSlots)
	}
	if output.BuildSlots.ActiveLeases != 4 {
		t.Errorf("active leases = %d, want 4", output.BuildSlots.ActiveLeases)
	}
	if len(output.BuildSlots.StaleLeases) != 2 {
		t.Fatalf("stale leases = %+v, want GoneAgent + TokenlessAgent", output.BuildSlots.StaleLeases)
	}
	t.Logf("decision: orphan = holder known to THIS session's registry with no live pane; unknown holders (other sessions) and live holders are never flagged")

	byAgent := map[string]StaleBuildSlotLease{}
	for _, stale := range output.BuildSlots.StaleLeases {
		byAgent[stale.Agent] = stale
	}
	if stale := byAgent["GoneAgent"]; !stale.AutoFixable {
		t.Errorf("GoneAgent lease should be auto-fixable (token persisted): %+v", stale)
	}
	if stale := byAgent["TokenlessAgent"]; stale.AutoFixable {
		t.Errorf("TokenlessAgent lease must not be auto-fixable without a token: %+v", stale)
	}

	releaseRecs := 0
	fixableRecs := 0
	for _, rec := range output.Recommendations {
		if rec.Action != "release_build_slot" {
			continue
		}
		releaseRecs++
		if rec.Status != "stale_build_slot" || rec.BuildSlot == nil {
			t.Errorf("malformed build-slot recommendation: %+v", rec)
		}
		if rec.AutoFixable {
			fixableRecs++
		}
	}
	if releaseRecs != 2 || fixableRecs != 1 {
		t.Errorf("release recommendations = %d (fixable %d), want 2 (1)", releaseRecs, fixableRecs)
	}
	if !output.AutoFixAvail {
		t.Error("auto-fix should be available for the tokened orphan lease")
	}
	if output.OverallHealth != "degraded" {
		t.Errorf("overall health = %q, want degraded (stale leases block other agents' builds)", output.OverallHealth)
	}
	t.Logf("decision: stale leases degrade an otherwise healthy session; the fix command is the standard --robot-diagnose --fix loop")
}

func TestDiagnoseBuildSlotListingUnavailableDegradesGracefully(t *testing.T) {
	registry := buildSlotTestRegistry("/test/project")
	deps := buildSlotTestDeps(registry, nil, agentmail.ErrBuildSlotListingUnavailable)

	output, err := getDiagnoseWithDependencies(t.Context(), DiagnoseOptions{Session: "project", Pane: -1}, deps)
	if err != nil {
		t.Fatalf("getDiagnoseWithDependencies: %v", err)
	}
	if !output.Success {
		t.Fatalf("diagnose must succeed even when lease listing is unavailable: %+v", output.RobotResponse)
	}
	if output.BuildSlots == nil || !output.BuildSlots.Degraded {
		t.Fatalf("expected degraded build-slot source: %+v", output.BuildSlots)
	}
	if output.BuildSlots.DegradedReason == "" {
		t.Error("degraded source must carry a reason")
	}
	if len(output.BuildSlots.StaleLeases) != 0 || output.OverallHealth != "healthy" {
		t.Errorf("degraded source must not invent findings: %+v health=%s", output.BuildSlots, output.OverallHealth)
	}
	for _, rec := range output.Recommendations {
		if rec.Action == "release_build_slot" {
			t.Errorf("no release recommendation expected when listing failed: %+v", rec)
		}
	}
	t.Logf("decision: Agent Mail archive missing → degraded-source note (%q), never a diagnose failure", output.BuildSlots.DegradedReason)
}

func TestDiagnoseBuildSlotSkipsWithoutRegistry(t *testing.T) {
	deps := buildSlotTestDeps(nil, []agentmail.BuildSlotLease{buildSlotLeaseFor("Someone", "s")}, nil)
	deps.loadAgentRegistry = func(string, ...string) (*agentmail.SessionAgentRegistry, error) {
		return nil, nil
	}
	listCalled := false
	deps.listBuildSlotLeases = func(string, time.Time) ([]agentmail.BuildSlotLease, error) {
		listCalled = true
		return nil, nil
	}

	output, err := getDiagnoseWithDependencies(t.Context(), DiagnoseOptions{Session: "project", Pane: -1}, deps)
	if err != nil {
		t.Fatalf("getDiagnoseWithDependencies: %v", err)
	}
	if output.BuildSlots == nil || !output.BuildSlots.Checked || output.BuildSlots.Degraded {
		t.Fatalf("no-registry case should be a benign check: %+v", output.BuildSlots)
	}
	if listCalled {
		t.Error("lease listing should be skipped without a registry (no tokens, no correlation basis)")
	}
	if len(output.BuildSlots.StaleLeases) != 0 {
		t.Errorf("no stale leases without a registry: %+v", output.BuildSlots.StaleLeases)
	}
	t.Logf("decision: sessions never registered with Agent Mail have nothing to correlate; report checked-but-empty, not degraded")
}

func TestDiagnoseFixReleasesStaleBuildSlots(t *testing.T) {
	t.Setenv("HOME", t.TempDir()) // keep audit-log writes out of the real home

	registry := buildSlotTestRegistry("/test/project")
	released := []agentmail.BuildSlotLease{}
	deps := buildSlotTestDeps(registry, nil, nil)
	deps.releaseBuildSlot = func(_ context.Context, projectKey string, reg *agentmail.SessionAgentRegistry, lease agentmail.BuildSlotLease) error {
		if projectKey != "/test/project" || reg == nil {
			t.Fatalf("release got projectKey=%q registry=%v", projectKey, reg)
		}
		if lease.Agent == "FailAgent" {
			return errors.New("agent mail unreachable")
		}
		released = append(released, lease)
		return nil
	}

	goneStale := StaleBuildSlotLease{Slot: "frontend", Agent: "GoneAgent", Branch: "main", AutoFixable: true}
	failStale := StaleBuildSlotLease{Slot: "backend", Agent: "FailAgent", Branch: "dev", AutoFixable: true}
	manualStale := StaleBuildSlotLease{Slot: "docs", Agent: "TokenlessAgent", AutoFixable: false}
	diag := DiagnoseOutput{Recommendations: []DiagnoseRecommendation{
		{Pane: -1, PaneTarget: "build_slot:frontend/GoneAgent", Status: "stale_build_slot", Action: "release_build_slot", AutoFixable: true, BuildSlot: &goneStale},
		{Pane: -1, PaneTarget: "build_slot:backend/FailAgent", Status: "stale_build_slot", Action: "release_build_slot", AutoFixable: true, BuildSlot: &failStale},
		{Pane: -1, PaneTarget: "build_slot:docs/TokenlessAgent", Status: "stale_build_slot", Action: "release_build_slot", AutoFixable: false, BuildSlot: &manualStale},
	}}

	output, returnedErr := captureDiagnoseFixOutput(t, t.Context(), diag, deps)
	if returnedErr != nil {
		t.Fatalf("fix returned error: %v", returnedErr)
	}
	if len(released) != 1 || released[0].Agent != "GoneAgent" || released[0].Slot != "frontend" || released[0].Branch != "main" {
		t.Fatalf("released = %+v, want exactly GoneAgent frontend@main", released)
	}
	if len(output.FixAttempts) != 2 {
		t.Fatalf("fix attempts = %+v, want 2 (tokenless lease skipped as non-fixable)", output.FixAttempts)
	}
	for _, attempt := range output.FixAttempts {
		if attempt.Action != "release_build_slot" {
			t.Errorf("unexpected fix action: %+v", attempt)
		}
	}
	if !output.FixAttempts[0].Success || output.FixAttempts[1].Success {
		t.Errorf("attempt success flags = %+v, want [true false]", output.FixAttempts)
	}
	t.Logf("decision: --fix releases only tokened orphans via release_build_slot; failures are reported per-attempt and audit-logged, never fatal")
}

func TestDiagnoseFixBuildSlotWithoutDetails(t *testing.T) {
	t.Setenv("HOME", t.TempDir())

	deps := buildSlotTestDeps(buildSlotTestRegistry("/test/project"), nil, nil)
	releaseCalls := 0
	deps.releaseBuildSlot = func(context.Context, string, *agentmail.SessionAgentRegistry, agentmail.BuildSlotLease) error {
		releaseCalls++
		return nil
	}
	diag := DiagnoseOutput{Recommendations: []DiagnoseRecommendation{
		{Pane: -1, PaneTarget: "build_slot:x/y", Status: "stale_build_slot", Action: "release_build_slot", AutoFixable: true /* BuildSlot nil */},
	}}

	// All attempts failing makes the whole fix report unsuccessful, which
	// the encoder surfaces as an error — that is the established contract.
	output, returnedErr := captureDiagnoseFixOutput(t, t.Context(), diag, deps)
	if releaseCalls != 0 {
		t.Errorf("release must not be called without lease details")
	}
	if len(output.FixAttempts) != 1 || output.FixAttempts[0].Success {
		t.Fatalf("expected one failed attempt: %+v", output.FixAttempts)
	}
	if returnedErr == nil {
		t.Error("an all-failed fix pass reports an unsuccessful envelope (non-nil error), matching existing restart/interrupt behavior")
	}
}

// Guards the sort interaction: build-slot recommendations use Pane -1 and
// must sort ahead of pane recommendations without panicking or being lost.
func TestDiagnoseBuildSlotRecommendationOrdering(t *testing.T) {
	registry := buildSlotTestRegistry("/test/project")
	leases := []agentmail.BuildSlotLease{buildSlotLeaseFor("GoneAgent", "frontend")}
	output, err := getDiagnoseWithDependencies(t.Context(), DiagnoseOptions{Session: "project", Pane: -1}, buildSlotTestDeps(registry, leases, nil))
	if err != nil {
		t.Fatalf("getDiagnoseWithDependencies: %v", err)
	}
	if len(output.Recommendations) == 0 {
		t.Fatal("expected the stale-lease recommendation")
	}
	first := output.Recommendations[0]
	if first.Action != "release_build_slot" || first.PaneTarget != fmt.Sprintf("build_slot:%s/%s", "frontend", "GoneAgent") {
		t.Errorf("first recommendation = %+v", first)
	}
}
