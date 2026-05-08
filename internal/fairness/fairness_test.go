package fairness

import (
	"encoding/json"
	"strings"
	"testing"
	"time"
)

func at(offset time.Duration) time.Time {
	return time.Date(2026, 5, 8, 12, 0, 0, 0, time.UTC).Add(offset)
}

func clock() time.Time { return at(0) }

func TestDetect_FairlyMixedDispatchHasNoFindings(t *testing.T) {
	t.Parallel()
	in := Inputs{
		Now: clock(),
		Dispatches: []Dispatch{
			{Lane: "auth", AgentType: "cc", At: at(1 * time.Minute)},
			{Lane: "billing", AgentType: "cod", At: at(2 * time.Minute)},
			{Lane: "auth", AgentType: "gmi", At: at(3 * time.Minute)},
			{Lane: "billing", AgentType: "cc", At: at(4 * time.Minute)},
			{Lane: "auth", AgentType: "cod", At: at(5 * time.Minute)},
			{Lane: "billing", AgentType: "gmi", At: at(6 * time.Minute)},
		},
		Lanes: []LaneEligibility{
			{Lane: "auth", HadEligibleWork: true},
			{Lane: "billing", HadEligibleWork: true},
		},
	}
	r := Detect(in)
	if len(r.Findings) != 0 {
		t.Errorf("Findings = %+v, want none", r.Findings)
	}
	if r.Total != 6 {
		t.Errorf("Total = %d, want 6", r.Total)
	}
	if len(r.AgentTypes) != 3 {
		t.Errorf("AgentTypes = %d, want 3", len(r.AgentTypes))
	}
}

func TestDetect_StarvedLaneSurfaces(t *testing.T) {
	t.Parallel()
	in := Inputs{
		Now: clock(),
		Dispatches: []Dispatch{
			{Lane: "auth", AgentType: "cc", At: at(1 * time.Minute)},
			{Lane: "auth", AgentType: "cod", At: at(2 * time.Minute)},
		},
		Lanes: []LaneEligibility{
			{Lane: "auth", HadEligibleWork: true},
			{Lane: "billing", HadEligibleWork: true}, // starved!
		},
	}
	r := Detect(in)
	if !findHasCode(r.Findings, "lane_starvation") {
		t.Fatalf("missing lane_starvation finding: %+v", r.Findings)
	}
	for _, f := range r.Findings {
		if f.Code == "lane_starvation" {
			if f.Severity != SeverityCritical {
				t.Errorf("severity = %s, want critical", f.Severity)
			}
			if !contains(f.Evidence, "billing") {
				t.Errorf("evidence missing 'billing': %v", f.Evidence)
			}
		}
	}
}

func TestDetect_LaneWithNoEligibleWorkIsNotStarvation(t *testing.T) {
	t.Parallel()
	in := Inputs{
		Now: clock(),
		Dispatches: []Dispatch{
			{Lane: "auth", AgentType: "cc", At: at(1 * time.Minute)},
		},
		Lanes: []LaneEligibility{
			{Lane: "auth", HadEligibleWork: true},
			{Lane: "billing", HadEligibleWork: false}, // no work, not starved
		},
	}
	r := Detect(in)
	if findHasCode(r.Findings, "lane_starvation") {
		t.Errorf("starvation fired for lane with no eligible work: %+v", r.Findings)
	}
}

func TestDetect_AgentTypeMonopolyCriticalAt95Percent(t *testing.T) {
	t.Parallel()
	dispatches := make([]Dispatch, 0, 20)
	for i := 0; i < 19; i++ {
		dispatches = append(dispatches, Dispatch{Lane: "auth", AgentType: "cc", At: at(time.Duration(i) * time.Minute)})
	}
	dispatches = append(dispatches, Dispatch{Lane: "auth", AgentType: "cod", At: at(20 * time.Minute)})
	in := Inputs{Now: clock(), Dispatches: dispatches}
	r := Detect(in)
	if !findHasCode(r.Findings, "agent_type_monopoly_critical") {
		t.Fatalf("missing monopoly_critical finding (cc=19/20=0.95): %+v", r.Findings)
	}
}

func TestDetect_AgentTypeMonopolyIgnoresUntypedDispatchesInShare(t *testing.T) {
	t.Parallel()
	dispatches := make([]Dispatch, 0, 20)
	for i := 0; i < 9; i++ {
		dispatches = append(dispatches, Dispatch{Lane: "auth", AgentType: "cc", At: at(time.Duration(i) * time.Minute)})
	}
	dispatches = append(dispatches, Dispatch{Lane: "auth", AgentType: "cod", At: at(9 * time.Minute)})
	for i := 0; i < 10; i++ {
		dispatches = append(dispatches, Dispatch{Lane: "auth", At: at(time.Duration(10+i) * time.Minute)})
	}

	r := Detect(Inputs{Now: clock(), Dispatches: dispatches})
	if r.Total != 20 {
		t.Fatalf("Total = %d, want all observed dispatches", r.Total)
	}
	if len(r.AgentTypes) != 2 {
		t.Fatalf("AgentTypes = %d, want 2 typed agent buckets: %+v", len(r.AgentTypes), r.AgentTypes)
	}
	if got := r.AgentTypes[0]; got.AgentType != "cc" || got.Share != 0.9 {
		t.Fatalf("top agent type = %+v, want cc share 0.9 from typed dispatches only", got)
	}
	if !findHasCode(r.Findings, "agent_type_monopoly_critical") {
		t.Fatalf("missing monopoly_critical finding with blank AgentType padding: %+v", r.Findings)
	}
}

func TestDetect_AgentTypeMonopolyWarningBetween70And90(t *testing.T) {
	t.Parallel()
	// 8 cc, 2 cod = 80% cc share -> warning, not critical.
	var dispatches []Dispatch
	for i := 0; i < 8; i++ {
		dispatches = append(dispatches, Dispatch{Lane: "x", AgentType: "cc", At: at(time.Duration(i) * time.Minute)})
	}
	for i := 0; i < 2; i++ {
		dispatches = append(dispatches, Dispatch{Lane: "x", AgentType: "cod", At: at(time.Duration(8+i) * time.Minute)})
	}
	r := Detect(Inputs{Now: clock(), Dispatches: dispatches})
	if !findHasCode(r.Findings, "agent_type_monopoly_warning") {
		t.Fatalf("missing monopoly_warning finding (cc=8/10=0.80): %+v", r.Findings)
	}
	if findHasCode(r.Findings, "agent_type_monopoly_critical") {
		t.Errorf("critical fired below threshold: %+v", r.Findings)
	}
}

// bd-vfwnv: zero must select the documented default (0.70 / 0.90),
// negative must disable the corresponding check. Each gate is
// independent — disabling warn while leaving crit enabled keeps
// criticals firing on their own threshold, and vice versa.

func monopolyDispatches80Percent() []Dispatch {
	// 8 cc + 2 cod = 80 % cc share. Crosses warn (0.70) but not
	// crit (0.90).
	var dispatches []Dispatch
	for i := 0; i < 8; i++ {
		dispatches = append(dispatches, Dispatch{Lane: "x", AgentType: "cc", At: at(time.Duration(i) * time.Minute)})
	}
	for i := 0; i < 2; i++ {
		dispatches = append(dispatches, Dispatch{Lane: "x", AgentType: "cod", At: at(time.Duration(8+i) * time.Minute)})
	}
	return dispatches
}

func TestDetect_MonopolyWarnZeroSelectsDefault(t *testing.T) {
	t.Parallel()
	r := Detect(Inputs{
		Now:               clock(),
		Dispatches:        monopolyDispatches80Percent(),
		MonopolyRatioWarn: 0, // zero ≡ default 0.70 → warning fires at 0.80
	})
	if !findHasCode(r.Findings, "agent_type_monopoly_warning") {
		t.Fatalf("MonopolyRatioWarn=0 must select default (0.70); cc=0.80 should warn: %+v", r.Findings)
	}
}

func TestDetect_MonopolyWarnNegativeDisablesCheck(t *testing.T) {
	t.Parallel()
	r := Detect(Inputs{
		Now:               clock(),
		Dispatches:        monopolyDispatches80Percent(),
		MonopolyRatioWarn: -1, // negative ≡ disabled
	})
	if findHasCode(r.Findings, "agent_type_monopoly_warning") {
		t.Fatalf("MonopolyRatioWarn=-1 must disable the warn check; cc=0.80 should not warn: %+v", r.Findings)
	}
}

func TestDetect_MonopolyWarnDisabledStillAllowsCriticalToFire(t *testing.T) {
	t.Parallel()
	// 19 cc + 1 cod = 95 % cc share. With warn disabled, crit should
	// still fire because each gate is independent.
	var dispatches []Dispatch
	for i := 0; i < 19; i++ {
		dispatches = append(dispatches, Dispatch{Lane: "x", AgentType: "cc", At: at(time.Duration(i) * time.Minute)})
	}
	dispatches = append(dispatches, Dispatch{Lane: "x", AgentType: "cod", At: at(19 * time.Minute)})

	r := Detect(Inputs{
		Now:               clock(),
		Dispatches:        dispatches,
		MonopolyRatioWarn: -1, // warn disabled
	})
	if !findHasCode(r.Findings, "agent_type_monopoly_critical") {
		t.Fatalf("crit must still fire even when warn is disabled (cc=0.95 ≥ default 0.90): %+v", r.Findings)
	}
	if findHasCode(r.Findings, "agent_type_monopoly_warning") {
		t.Errorf("warn fired despite disable sentinel: %+v", r.Findings)
	}
}

func TestDetect_MonopolyCriticalNegativeDisablesCheck(t *testing.T) {
	t.Parallel()
	// 19 cc + 1 cod = 95 %. crit is disabled, warn (default 0.70)
	// still fires. The finding should be 'warning', not 'critical'.
	var dispatches []Dispatch
	for i := 0; i < 19; i++ {
		dispatches = append(dispatches, Dispatch{Lane: "x", AgentType: "cc", At: at(time.Duration(i) * time.Minute)})
	}
	dispatches = append(dispatches, Dispatch{Lane: "x", AgentType: "cod", At: at(19 * time.Minute)})

	r := Detect(Inputs{
		Now:                   clock(),
		Dispatches:            dispatches,
		MonopolyRatioCritical: -1, // crit disabled
	})
	if findHasCode(r.Findings, "agent_type_monopoly_critical") {
		t.Fatalf("MonopolyRatioCritical=-1 must disable the crit gate: %+v", r.Findings)
	}
	if !findHasCode(r.Findings, "agent_type_monopoly_warning") {
		t.Errorf("warn must still fire on its own threshold even with crit disabled: %+v", r.Findings)
	}
}

func TestDetect_MonopolyBothDisabledProducesNoMonopolyFindings(t *testing.T) {
	t.Parallel()
	r := Detect(Inputs{
		Now:                   clock(),
		Dispatches:            monopolyDispatches80Percent(),
		MonopolyRatioWarn:     -1,
		MonopolyRatioCritical: -1,
	})
	for _, f := range r.Findings {
		if strings.HasPrefix(f.Code, "agent_type_monopoly") {
			t.Errorf("disable sentinel did not suppress %+v", f)
		}
	}
}

func TestDetect_NoMonopolyWhenSingleAgentType(t *testing.T) {
	t.Parallel()
	// One agent type alone does not trigger monopoly (degenerate case).
	dispatches := make([]Dispatch, 5)
	for i := range dispatches {
		dispatches[i] = Dispatch{Lane: "x", AgentType: "cc", At: at(time.Duration(i) * time.Minute)}
	}
	r := Detect(Inputs{Now: clock(), Dispatches: dispatches})
	for _, f := range r.Findings {
		if strings.HasPrefix(f.Code, "agent_type_monopoly") {
			t.Errorf("monopoly fired with single agent type: %+v", f)
		}
	}
}

func TestDetect_WindowFilterDropsOutOfRange(t *testing.T) {
	t.Parallel()
	in := Inputs{
		Now:         clock(),
		WindowStart: at(2 * time.Minute),
		WindowEnd:   at(4 * time.Minute),
		Dispatches: []Dispatch{
			{Lane: "x", AgentType: "cc", At: at(0)},                 // before window
			{Lane: "x", AgentType: "cc", At: at(3 * time.Minute)},   // in window
			{Lane: "x", AgentType: "cod", At: at(10 * time.Minute)}, // after window
		},
	}
	r := Detect(in)
	if r.Total != 1 {
		t.Errorf("Total = %d, want 1 (window filter)", r.Total)
	}
}

func TestDetect_StarvationCriticalSortsBeforeMonopolyWarning(t *testing.T) {
	t.Parallel()
	// 8 cc + 2 cod (warning), plus a starved lane (critical).
	var dispatches []Dispatch
	for i := 0; i < 8; i++ {
		dispatches = append(dispatches, Dispatch{Lane: "auth", AgentType: "cc", At: at(time.Duration(i) * time.Minute)})
	}
	for i := 0; i < 2; i++ {
		dispatches = append(dispatches, Dispatch{Lane: "auth", AgentType: "cod", At: at(time.Duration(8+i) * time.Minute)})
	}
	in := Inputs{
		Now:        clock(),
		Dispatches: dispatches,
		Lanes: []LaneEligibility{
			{Lane: "auth", HadEligibleWork: true},
			{Lane: "billing", HadEligibleWork: true},
		},
	}
	r := Detect(in)
	if len(r.Findings) < 2 {
		t.Fatalf("findings = %d, want >=2", len(r.Findings))
	}
	if r.Findings[0].Severity != SeverityCritical {
		t.Errorf("first finding severity = %s, want critical (starvation)", r.Findings[0].Severity)
	}
}

func TestDetect_LaneStatsSortedStarvedFirstThenByCount(t *testing.T) {
	t.Parallel()
	in := Inputs{
		Now: clock(),
		Dispatches: []Dispatch{
			{Lane: "high", AgentType: "cc", At: at(1 * time.Minute)},
			{Lane: "high", AgentType: "cc", At: at(2 * time.Minute)},
			{Lane: "low", AgentType: "cc", At: at(3 * time.Minute)},
		},
		Lanes: []LaneEligibility{
			{Lane: "high", HadEligibleWork: true},
			{Lane: "low", HadEligibleWork: true},
			{Lane: "starved", HadEligibleWork: true},
		},
	}
	r := Detect(in)
	if len(r.Lanes) != 3 {
		t.Fatalf("Lanes = %d, want 3", len(r.Lanes))
	}
	if r.Lanes[0].Lane != "starved" {
		t.Errorf("first lane = %s, want starved", r.Lanes[0].Lane)
	}
	if r.Lanes[1].Lane != "low" {
		t.Errorf("second lane = %s, want low (1 dispatch)", r.Lanes[1].Lane)
	}
	if r.Lanes[2].Lane != "high" {
		t.Errorf("third lane = %s, want high (2 dispatches)", r.Lanes[2].Lane)
	}
}

func TestDetect_JSONShapeIsStable(t *testing.T) {
	t.Parallel()
	in := Inputs{
		Now: clock(),
		Dispatches: []Dispatch{
			{Lane: "auth", AgentType: "cc", At: at(1 * time.Minute)},
			{Lane: "auth", AgentType: "cc", At: at(2 * time.Minute)},
		},
		Lanes: []LaneEligibility{
			{Lane: "auth", HadEligibleWork: true},
			{Lane: "billing", HadEligibleWork: true},
		},
	}
	a, _ := json.Marshal(Detect(in))
	b, _ := json.Marshal(Detect(in))
	if string(a) != string(b) {
		t.Errorf("JSON drifted: %s vs %s", a, b)
	}
	for _, want := range []string{
		`"total_dispatches"`, `"lanes"`, `"agent_types"`,
		`"findings"`, `"starved_risk":true`,
	} {
		if !strings.Contains(string(a), want) {
			t.Errorf("JSON missing %s: %s", want, a)
		}
	}
}

func TestDetect_EmptyInputsHaveNoFindingsButValidEnvelope(t *testing.T) {
	t.Parallel()
	r := Detect(Inputs{Now: clock()})
	if r.Total != 0 {
		t.Errorf("Total = %d, want 0", r.Total)
	}
	if len(r.Findings) != 0 {
		t.Errorf("Findings = %+v, want none", r.Findings)
	}
	if r.GeneratedAt.IsZero() {
		t.Error("GeneratedAt unset")
	}
}

func findHasCode(findings []Finding, code string) bool {
	for _, f := range findings {
		if f.Code == code {
			return true
		}
	}
	return false
}

func contains(slice []string, s string) bool {
	for _, x := range slice {
		if x == s {
			return true
		}
	}
	return false
}
