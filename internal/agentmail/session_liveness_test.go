package agentmail

import (
	"encoding/json"
	"reflect"
	"testing"
)

// liveSet builds a PaneLiveness from pane id -> pid, mirroring what the CLI
// derives from a tmux pane listing: a pane is live when it exists and, if a
// pid was recorded, still carries that pid.
func liveSet(pids map[string]int) PaneLiveness {
	return func(paneID string, recordedPID int) bool {
		pid, ok := pids[paneID]
		if !ok {
			return false
		}
		if recordedPID > 0 && pid > 0 && pid != recordedPID {
			return false
		}
		return true
	}
}

func TestResolveForPane_RefusesLiveHolder(t *testing.T) {
	// The ntm#256 reproduction: pane %5 registered as sess__cc_1, then had
	// its title overwritten. `ntm add` composes sess__cc_1 again for the new
	// pane %9. The title matches, but %5 is still alive, so the slot is
	// occupied and no name may be reused.
	r := NewSessionAgentRegistry("sess", "/proj")
	r.AddAgent("sess__cc_1", "%5", "GreenCastle")
	r.SetPanePID("%5", 4242)

	name, from, ok := r.ResolveForPane("sess__cc_1", "%9", liveSet(map[string]int{"%5": 4242, "%9": 9999}))
	if ok || name != "" || from != "" {
		t.Fatalf("ResolveForPane with a live holder = (%q, %q, %v), want refusal", name, from, ok)
	}
	// The original binding is untouched.
	if got, _ := r.GetAgentByID("%5"); got != "GreenCastle" {
		t.Fatalf("live holder lost its binding: %q", got)
	}
}

func TestResolveForPane_ReusesDeadHolder(t *testing.T) {
	// Same-session respawn after a kill: %5 is gone, so the name it held is
	// free for the new pane and the provenance names the dead pane.
	r := NewSessionAgentRegistry("sess", "/proj")
	r.AddAgent("sess__cc_1", "%5", "GreenCastle")
	r.SetPanePID("%5", 4242)

	name, from, ok := r.ResolveForPane("sess__cc_1", "%9", liveSet(map[string]int{"%9": 9999}))
	if !ok || name != "GreenCastle" || from != "%5" {
		t.Fatalf("ResolveForPane with a dead holder = (%q, %q, %v), want (GreenCastle, %%5, true)", name, from, ok)
	}
}

func TestResolveForPane_PidMismatchMeansDead(t *testing.T) {
	// tmux reuses %N across server restarts. The pane id exists again but
	// belongs to a different process, so the recorded binding is dead and
	// the name is reusable.
	r := NewSessionAgentRegistry("sess", "/proj")
	r.AddAgent("sess__cc_1", "%5", "GreenCastle")
	r.SetPanePID("%5", 4242)

	name, from, ok := r.ResolveForPane("sess__cc_1", "%9", liveSet(map[string]int{"%5": 1, "%9": 2}))
	if !ok || name != "GreenCastle" || from != "%5" {
		t.Fatalf("pid mismatch should read as dead, got (%q, %q, %v)", name, from, ok)
	}
}

func TestResolveForPane_UnknownPidFallsBackToExistence(t *testing.T) {
	// Registries written before pane_pids carry no pid: existence alone
	// decides, so a present pane is still treated as occupying its slot.
	r := NewSessionAgentRegistry("sess", "/proj")
	r.AddAgent("sess__cc_1", "%5", "GreenCastle")

	if _, _, ok := r.ResolveForPane("sess__cc_1", "%9", liveSet(map[string]int{"%5": 77})); ok {
		t.Fatal("present pane without a recorded pid must count as live")
	}
	name, from, ok := r.ResolveForPane("sess__cc_1", "%9", liveSet(map[string]int{}))
	if !ok || name != "GreenCastle" || from != "%5" {
		t.Fatalf("absent pane must read as dead, got (%q, %q, %v)", name, from, ok)
	}
}

func TestResolveForPane_PaneIDIsPrimary(t *testing.T) {
	// The physical pane id wins over any title, including a title that is
	// bound to a different, live agent.
	r := NewSessionAgentRegistry("sess", "/proj")
	r.AddAgent("sess__cc_1", "%5", "GreenCastle")
	r.AddAgent("sess__cc_2", "%6", "BlueLake")

	name, from, ok := r.ResolveForPane("sess__cc_1", "%6", liveSet(map[string]int{"%5": 1, "%6": 2}))
	if !ok || name != "BlueLake" || from != "" {
		t.Fatalf("pane id lookup must be primary, got (%q, %q, %v)", name, from, ok)
	}
}

func TestResolveForPane_TitleWithoutPaneRecord(t *testing.T) {
	// A title bound to a name that has no pane record cannot be live.
	r := NewSessionAgentRegistry("sess", "/proj")
	r.AddAgent("sess__cc_1", "", "GreenCastle")

	name, from, ok := r.ResolveForPane("sess__cc_1", "%9", liveSet(map[string]int{}))
	if !ok || name != "GreenCastle" || from != "" {
		t.Fatalf("title-only binding should reuse, got (%q, %q, %v)", name, from, ok)
	}
}

func TestResolveForPane_NilLivenessIsConservative(t *testing.T) {
	// Unobservable liveness must never hand a recorded holder's name to a
	// second pane: the title path is refused, the pane-id path still works.
	r := NewSessionAgentRegistry("sess", "/proj")
	r.AddAgent("sess__cc_1", "%5", "GreenCastle")

	if _, _, ok := r.ResolveForPane("sess__cc_1", "%9", nil); ok {
		t.Fatal("nil liveness must refuse title-based reuse")
	}
	if name, _, ok := r.ResolveForPane("wrong_title", "%5", nil); !ok || name != "GreenCastle" {
		t.Fatalf("nil liveness must still resolve by pane id, got (%q, %v)", name, ok)
	}
}

func TestResolveForPane_NotFoundAndNilRegistry(t *testing.T) {
	var nilReg *SessionAgentRegistry
	if _, _, ok := nilReg.ResolveForPane("t", "%1", nil); ok {
		t.Fatal("nil registry must not resolve")
	}
	r := NewSessionAgentRegistry("sess", "/proj")
	if _, _, ok := r.ResolveForPane("sess__cc_1", "%1", liveSet(nil)); ok {
		t.Fatal("empty registry must not resolve")
	}
	if _, _, ok := r.ResolveForPane("", "", liveSet(nil)); ok {
		t.Fatal("empty title and pane id must not resolve")
	}
}

func TestOccupiedTitles(t *testing.T) {
	r := NewSessionAgentRegistry("sess", "/proj")
	r.AddAgent("sess__cc_1", "%5", "GreenCastle") // live
	r.AddAgent("sess__cc_2", "%6", "BlueLake")    // dead (pane gone)
	r.AddAgent("sess__cod_1", "%7", "RedRock")    // live but pid changed -> dead
	r.SetPanePID("%7", 100)
	r.AddAgent("sess__gmi_1", "", "NoPane") // no pane record -> never live

	got := r.OccupiedTitles(liveSet(map[string]int{"%5": 1, "%7": 200}))
	want := []string{"sess__cc_1"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("OccupiedTitles = %v, want %v", got, want)
	}

	// Nil liveness: every recorded holder counts as occupied.
	got = r.OccupiedTitles(nil)
	want = []string{"sess__cc_1", "sess__cc_2", "sess__cod_1"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("OccupiedTitles(nil) = %v, want %v", got, want)
	}

	var nilReg *SessionAgentRegistry
	if got := nilReg.OccupiedTitles(nil); got != nil {
		t.Fatalf("nil registry OccupiedTitles = %v, want nil", got)
	}
}

func TestPanePIDRecords(t *testing.T) {
	r := NewSessionAgentRegistry("sess", "/proj")
	if r.PanePID("%1") != 0 {
		t.Fatal("unrecorded pid must be 0")
	}
	r.SetPanePID("%1", 321)
	if r.PanePID("%1") != 321 {
		t.Fatalf("PanePID = %d, want 321", r.PanePID("%1"))
	}
	r.SetPanePID("%1", 0)
	if r.PanePID("%1") != 0 {
		t.Fatal("SetPanePID(0) must clear the record")
	}
	r.SetPanePID("", 5)
	if len(r.PanePIDs) != 0 {
		t.Fatalf("empty pane id must not be recorded: %v", r.PanePIDs)
	}
	var nilReg *SessionAgentRegistry
	nilReg.SetPanePID("%1", 1) // must not panic
	if nilReg.PanePID("%1") != 0 {
		t.Fatal("nil registry PanePID must be 0")
	}
}

func TestPaneIDForAgent(t *testing.T) {
	r := NewSessionAgentRegistry("sess", "/proj")
	r.AddAgent("sess__cc_1", "%5", "GreenCastle")
	if id, ok := r.PaneIDForAgent("GreenCastle"); !ok || id != "%5" {
		t.Fatalf("PaneIDForAgent = (%q, %v), want (%%5, true)", id, ok)
	}
	if _, ok := r.PaneIDForAgent("Nobody"); ok {
		t.Fatal("unknown agent must not resolve a pane")
	}
	if _, ok := r.PaneIDForAgent(""); ok {
		t.Fatal("empty agent name must not resolve a pane")
	}
}

func TestAddAgent_MovingAnAgentRetiresItsOldPid(t *testing.T) {
	// Dead-slot reuse moves a name from the dead pane to the new one; the
	// dead pane's pid must go with it so it cannot later masquerade as live.
	r := NewSessionAgentRegistry("sess", "/proj")
	r.AddAgent("sess__cc_1", "%5", "GreenCastle")
	r.SetPanePID("%5", 4242)

	r.AddAgent("sess__cc_1", "%9", "GreenCastle")
	r.SetPanePID("%9", 9999)

	if _, stale := r.PanePIDs["%5"]; stale {
		t.Fatalf("retired pane kept its pid: %v", r.PanePIDs)
	}
	if r.PanePID("%9") != 9999 {
		t.Fatalf("new pane pid = %d, want 9999", r.PanePID("%9"))
	}
	if id, _ := r.PaneIDForAgent("GreenCastle"); id != "%9" {
		t.Fatalf("agent bound to %q, want %%9", id)
	}
}

func TestRegistryPanePIDsRoundTripJSON(t *testing.T) {
	r := NewSessionAgentRegistry("sess", "/proj")
	r.AddAgent("sess__cc_1", "%5", "GreenCastle")
	r.SetPanePID("%5", 4242)

	data, err := json.Marshal(r)
	if err != nil {
		t.Fatal(err)
	}
	var decoded SessionAgentRegistry
	if err := json.Unmarshal(data, &decoded); err != nil {
		t.Fatal(err)
	}
	if decoded.PanePID("%5") != 4242 {
		t.Fatalf("pane_pids did not round-trip: %s", data)
	}

	// Registries that predate the field decode with no pids.
	legacy := []byte(`{"session_name":"sess","project_key":"/proj","agents":{"sess__cc_1":"GreenCastle"},"pane_id_map":{"%5":"GreenCastle"}}`)
	var old SessionAgentRegistry
	if err := json.Unmarshal(legacy, &old); err != nil {
		t.Fatal(err)
	}
	if old.PanePID("%5") != 0 {
		t.Fatal("legacy registry must report unknown pid")
	}
}
