package parity

import (
	"encoding/json"
	"strings"
	"testing"
	"time"
)

func clock() time.Time { return time.Date(2026, 5, 8, 12, 0, 0, 0, time.UTC) }

// twin returns one canonical state visible identically on both
// surfaces — a session with two panes and an agent. Compare must
// return zero drifts.
func twin() Inputs {
	return Inputs{
		Now: clock(),
		Robot: []SnapshotView{
			{Kind: "session", ID: "proj1", Name: "proj1", Status: "active", Count: 2},
			{Kind: "pane", ID: "%17", Name: "pane 1", Status: "working", AgentType: "cc", PaneIndex: 1},
			{Kind: "pane", ID: "%18", Name: "pane 2", Status: "idle", AgentType: "cod", PaneIndex: 2},
		},
		TUI: []SnapshotView{
			{Kind: "session", ID: "proj1", Name: "proj1", Status: "active", Count: 2},
			{Kind: "pane", ID: "%17", Name: "pane 1", Status: "working", AgentType: "cc", PaneIndex: 1},
			{Kind: "pane", ID: "%18", Name: "pane 2", Status: "idle", AgentType: "cod", PaneIndex: 2},
		},
	}
}

func TestCompare_TwinSurfacesHaveNoDrift(t *testing.T) {
	t.Parallel()
	r := Compare(twin())
	if len(r.Drifts) != 0 {
		t.Errorf("Drifts = %+v, want none", r.Drifts)
	}
	if r.Counts.Critical != 0 || r.Counts.Warning != 0 {
		t.Errorf("Counts = %+v, want zeros", r.Counts)
	}
}

func TestCompare_PresentOnRobotMissingOnTUIIsCritical(t *testing.T) {
	t.Parallel()
	in := twin()
	// Drop pane %18 from TUI.
	in.TUI = in.TUI[:2]
	r := Compare(in)
	found := false
	for _, d := range r.Drifts {
		if d.ID == "%18" && d.Field == "presence" && d.Severity == SeverityCritical {
			if d.Robot != "present" || d.TUI != "missing" {
				t.Errorf("presence drift = robot=%s tui=%s, want present/missing", d.Robot, d.TUI)
			}
			found = true
		}
	}
	if !found {
		t.Errorf("missing critical presence drift for %%18: %+v", r.Drifts)
	}
}

func TestCompare_PresentOnTUIMissingOnRobotIsCritical(t *testing.T) {
	t.Parallel()
	in := twin()
	// Drop pane %18 from Robot.
	in.Robot = in.Robot[:2]
	r := Compare(in)
	found := false
	for _, d := range r.Drifts {
		if d.ID == "%18" && d.Field == "presence" && d.Severity == SeverityCritical {
			if d.Robot != "missing" || d.TUI != "present" {
				t.Errorf("presence drift = robot=%s tui=%s, want missing/present", d.Robot, d.TUI)
			}
			found = true
		}
	}
	if !found {
		t.Errorf("missing critical presence drift for %%18: %+v", r.Drifts)
	}
}

func TestCompare_StatusDisagreementIsWarning(t *testing.T) {
	t.Parallel()
	in := twin()
	in.TUI[1].Status = "stuck" // robot says working
	r := Compare(in)
	found := false
	for _, d := range r.Drifts {
		if d.Field == "status" && d.ID == "%17" {
			if d.Severity != SeverityWarning {
				t.Errorf("severity = %s, want warning", d.Severity)
			}
			if d.Robot != "working" || d.TUI != "stuck" {
				t.Errorf("drift = robot=%s tui=%s, want working/stuck", d.Robot, d.TUI)
			}
			found = true
		}
	}
	if !found {
		t.Errorf("status drift missing: %+v", r.Drifts)
	}
}

func TestCompare_AgentTypeDriftIsWarning(t *testing.T) {
	t.Parallel()
	in := twin()
	in.TUI[1].AgentType = "gmi"
	r := Compare(in)
	for _, d := range r.Drifts {
		if d.Field == "agent_type" && d.ID == "%17" && d.Severity != SeverityWarning {
			t.Errorf("severity = %s, want warning", d.Severity)
		}
	}
}

func TestCompare_PaneIndexDriftIsWarning(t *testing.T) {
	t.Parallel()
	in := twin()
	in.TUI[1].PaneIndex = 99
	r := Compare(in)
	hasIndex := false
	for _, d := range r.Drifts {
		if d.Field == "pane_index" && d.ID == "%17" {
			hasIndex = true
		}
	}
	if !hasIndex {
		t.Errorf("missing pane_index drift: %+v", r.Drifts)
	}
}

func TestCompare_CountDriftIsWarning(t *testing.T) {
	t.Parallel()
	in := twin()
	in.TUI[0].Count = 99 // session pane count drift
	r := Compare(in)
	hasCount := false
	for _, d := range r.Drifts {
		if d.Field == "count" && d.ID == "proj1" {
			hasCount = true
		}
	}
	if !hasCount {
		t.Errorf("missing count drift: %+v", r.Drifts)
	}
}

func TestCompare_NameDifferenceIsInfoOnly(t *testing.T) {
	t.Parallel()
	in := twin()
	in.TUI[1].Name = "alternate label"
	r := Compare(in)
	for _, d := range r.Drifts {
		if d.Field == "name" && d.Severity != SeverityInfo {
			t.Errorf("name drift severity = %s, want info", d.Severity)
		}
	}
}

func TestCompare_ExtraKeyMissingOnOneSideIsInfo(t *testing.T) {
	t.Parallel()
	in := twin()
	in.Robot[1].Extra = map[string]string{"hint": "rate-limited"}
	// TUI has no Extra key for %17 -> drift.
	r := Compare(in)
	found := false
	for _, d := range r.Drifts {
		if d.Field == "extra.hint" && d.ID == "%17" && d.Severity == SeverityInfo {
			found = true
		}
	}
	if !found {
		t.Errorf("missing extra.hint drift: %+v", r.Drifts)
	}
}

func TestCompare_ExtraValueDifferenceIsWarning(t *testing.T) {
	t.Parallel()
	in := twin()
	in.Robot[1].Extra = map[string]string{"hint": "rate-limited"}
	in.TUI[1].Extra = map[string]string{"hint": "OK"}
	r := Compare(in)
	hasValueDiff := false
	for _, d := range r.Drifts {
		if d.Field == "extra.hint" && d.Severity == SeverityWarning {
			hasValueDiff = true
		}
	}
	if !hasValueDiff {
		t.Errorf("missing extra.hint value-difference drift: %+v", r.Drifts)
	}
}

func TestCompare_IgnoredFieldsSuppressDrift(t *testing.T) {
	t.Parallel()
	in := twin()
	in.TUI[1].Status = "stuck"
	in.IgnoredFields = []string{"status"}
	r := Compare(in)
	for _, d := range r.Drifts {
		if d.Field == "status" {
			t.Errorf("status drift fired despite ignore list: %+v", d)
		}
	}
}

func TestCompare_DriftsSortedCriticalFirstThenStable(t *testing.T) {
	t.Parallel()
	in := twin()
	in.TUI = in.TUI[:2]              // drop %18 -> critical drift
	in.TUI[1].Status = "stuck"       // status drift on %17 -> warning
	in.TUI[1].Name = "new label"     // name drift on %17 -> info
	r := Compare(in)
	if len(r.Drifts) < 2 {
		t.Fatalf("expected multiple drifts, got %d", len(r.Drifts))
	}
	for i := 1; i < len(r.Drifts); i++ {
		ri := severityRank(r.Drifts[i-1].Severity)
		rj := severityRank(r.Drifts[i].Severity)
		if rj > ri {
			t.Errorf("drifts out of order at %d: %s after %s",
				i, r.Drifts[i].Severity, r.Drifts[i-1].Severity)
		}
	}
}

func TestCompare_JSONShapeIsStable(t *testing.T) {
	t.Parallel()
	in := twin()
	in.TUI = in.TUI[:2]
	a, err := json.Marshal(Compare(in))
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}
	b, err := json.Marshal(Compare(in))
	if err != nil {
		t.Fatalf("Marshal twice: %v", err)
	}
	if string(a) != string(b) {
		t.Errorf("JSON drifted across two Compare calls:\nfirst:  %s\nsecond: %s", a, b)
	}
	for _, want := range []string{
		`"robot_count":3`, `"tui_count":2`,
		`"counts"`, `"summary":`,
	} {
		if !strings.Contains(string(a), want) {
			t.Errorf("JSON missing %s: %s", want, a)
		}
	}
}

func TestCompare_EmptyInputsHaveNoDrift(t *testing.T) {
	t.Parallel()
	r := Compare(Inputs{Now: clock()})
	if len(r.Drifts) != 0 {
		t.Errorf("Drifts = %+v, want none on empty inputs", r.Drifts)
	}
}
