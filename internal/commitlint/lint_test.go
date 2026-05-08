package commitlint

import (
	"encoding/json"
	"strings"
	"testing"
	"time"
)

func fixedClock() time.Time {
	return time.Date(2026, 5, 8, 12, 0, 0, 0, time.UTC)
}

func ownReservation(p string) ReservationView {
	return ReservationView{
		ID:          1,
		PathPattern: p,
		AgentName:   "Alice",
		Exclusive:   true,
		CreatedAt:   fixedClock().Add(-1 * time.Hour),
		ExpiresAt:   fixedClock().Add(1 * time.Hour),
	}
}

func foreignReservation(p, holder string) ReservationView {
	return ReservationView{
		ID:          2,
		PathPattern: p,
		AgentName:   holder,
		Exclusive:   true,
		CreatedAt:   fixedClock().Add(-30 * time.Minute),
		ExpiresAt:   fixedClock().Add(1 * time.Hour),
	}
}

func cleanInputs() Inputs {
	return Inputs{
		AgentName:    "Alice",
		TouchedPaths: []string{"internal/auth/session.go"},
		Reservations: []ReservationView{ownReservation("internal/auth/**")},
		Inbox:        nil,
		Sync:         SyncView{HasLocalBeadsDB: true, NeedsFlush: false, Status: "in_sync"},
		Now:          fixedClock(),
	}
}

func TestEvaluate_CleanInputsAreSafeToCommit(t *testing.T) {
	t.Parallel()
	r := Evaluate(cleanInputs())
	if !r.SafeToCommit {
		t.Fatalf("SafeToCommit = false, findings = %+v", r.Findings)
	}
	if r.Summary.Critical != 0 || r.Summary.Warning != 0 {
		t.Errorf("Summary = %+v, want zeros for clean inputs", r.Summary)
	}
	if len(r.Findings) != 0 {
		t.Errorf("Findings = %+v, want empty", r.Findings)
	}
}

func TestEvaluate_MissingReservationFiresWarning(t *testing.T) {
	t.Parallel()
	in := cleanInputs()
	in.Reservations = nil // no reservations at all
	r := Evaluate(in)

	if !r.SafeToCommit {
		t.Errorf("SafeToCommit = false, want true (warning is non-blocking): %+v", r.Findings)
	}
	if !findHasCode(r.Findings, "missing_reservation") {
		t.Errorf("missing 'missing_reservation' finding: %+v", r.Findings)
	}
	for _, f := range r.Findings {
		if f.Code == "missing_reservation" && f.Severity != SeverityWarning {
			t.Errorf("missing_reservation severity = %s, want warning", f.Severity)
		}
	}
}

func TestEvaluate_ForeignReservationBlocks(t *testing.T) {
	t.Parallel()
	in := cleanInputs()
	in.Reservations = []ReservationView{
		foreignReservation("internal/auth/**", "Bob"),
	}
	r := Evaluate(in)

	if r.SafeToCommit {
		t.Errorf("SafeToCommit = true, want false (foreign reservation): %+v", r.Findings)
	}
	if !findHasCode(r.Findings, "foreign_reservation") {
		t.Errorf("missing 'foreign_reservation' finding: %+v", r.Findings)
	}
	for _, f := range r.Findings {
		if f.Code == "foreign_reservation" && f.Severity != SeverityCritical {
			t.Errorf("foreign_reservation severity = %s, want critical", f.Severity)
		}
	}
}

func TestEvaluate_StaleDBExportBlocks(t *testing.T) {
	t.Parallel()
	in := cleanInputs()
	in.Sync.NeedsFlush = true
	in.Sync.Status = "beads_db_newer_than_jsonl"
	r := Evaluate(in)

	if r.SafeToCommit {
		t.Errorf("SafeToCommit = true, want false (stale export): %+v", r.Findings)
	}
	if !findHasCode(r.Findings, "stale_beads_export") {
		t.Errorf("missing 'stale_beads_export' finding: %+v", r.Findings)
	}
}

func TestEvaluate_UrgentUnackedMailBlocks(t *testing.T) {
	t.Parallel()
	in := cleanInputs()
	in.Inbox = []InboxView{
		{ID: 7, Subject: "stop committing", From: "Bob", Importance: "URGENT", AckRequired: true, ReadAt: nil},
	}
	r := Evaluate(in)

	if r.SafeToCommit {
		t.Errorf("SafeToCommit = true, want false (urgent unacked mail): %+v", r.Findings)
	}
	if !findHasCode(r.Findings, "urgent_unacked_mail") {
		t.Errorf("missing 'urgent_unacked_mail' finding: %+v", r.Findings)
	}
}

func TestEvaluate_ReadAndAckedMailDoesNotBlock(t *testing.T) {
	t.Parallel()
	in := cleanInputs()
	read := fixedClock().Add(-5 * time.Minute)
	in.Inbox = []InboxView{
		{ID: 8, Subject: "checked", From: "Bob", Importance: "urgent", AckRequired: true, ReadAt: &read},
		{ID: 9, Subject: "info ping", From: "Bob", Importance: "normal", AckRequired: false},
	}
	r := Evaluate(in)
	if !r.SafeToCommit {
		t.Errorf("SafeToCommit = false, want true (mail is read or non-urgent): %+v", r.Findings)
	}
}

func TestEvaluate_StaleOwnReservationFiresWarning(t *testing.T) {
	t.Parallel()
	in := cleanInputs()
	stale := ownReservation("internal/auth/**")
	stale.CreatedAt = fixedClock().Add(-72 * time.Hour) // very old
	in.Reservations = []ReservationView{stale}
	in.StaleReservationThreshold = 24 * time.Hour
	r := Evaluate(in)

	if !r.SafeToCommit {
		t.Errorf("SafeToCommit = false, want true (stale own reservation is warning): %+v", r.Findings)
	}
	if !findHasCode(r.Findings, "stale_own_reservation") {
		t.Errorf("missing 'stale_own_reservation' finding: %+v", r.Findings)
	}
}

func TestEvaluate_ExpiredReservationDoesNotCount(t *testing.T) {
	t.Parallel()
	in := cleanInputs()
	expired := foreignReservation("internal/auth/**", "Bob")
	expired.ExpiresAt = fixedClock().Add(-1 * time.Hour) // already expired
	in.Reservations = []ReservationView{expired}
	r := Evaluate(in)

	// foreign_reservation should NOT fire because the lock is expired.
	for _, f := range r.Findings {
		if f.Code == "foreign_reservation" {
			t.Errorf("expired reservation triggered foreign_reservation: %+v", f)
		}
	}
	// ... but missing_reservation SHOULD fire because no live own
	// reservation covers the path.
	if !findHasCode(r.Findings, "missing_reservation") {
		t.Errorf("missing 'missing_reservation' finding for expired-only coverage: %+v", r.Findings)
	}
}

func TestEvaluate_FindingsSortedBySeverityThenCode(t *testing.T) {
	t.Parallel()
	in := cleanInputs()
	in.Reservations = []ReservationView{foreignReservation("internal/auth/**", "Bob")}
	in.Sync.NeedsFlush = true
	in.Sync.Status = "beads_db_newer_than_jsonl"
	in.Inbox = []InboxView{
		{ID: 1, Subject: "x", From: "Bob", Importance: "urgent", AckRequired: true},
	}
	r := Evaluate(in)

	if len(r.Findings) < 2 {
		t.Fatalf("expected multiple findings, got %d", len(r.Findings))
	}
	for i := 1; i < len(r.Findings); i++ {
		prev := severityRank(r.Findings[i-1].Severity)
		cur := severityRank(r.Findings[i].Severity)
		if cur > prev {
			t.Errorf("findings out of order: severity rank %d at index %d follows %d at %d",
				cur, i, prev, i-1)
		}
		if cur == prev && r.Findings[i].Code < r.Findings[i-1].Code {
			t.Errorf("findings out of order: code %q at index %d follows %q at %d",
				r.Findings[i].Code, i, r.Findings[i-1].Code, i-1)
		}
	}
}

func TestEvaluate_JSONShapeIsStable(t *testing.T) {
	t.Parallel()
	in := cleanInputs()
	in.Sync.NeedsFlush = true
	in.Sync.Status = "beads_db_newer_than_jsonl"
	r := Evaluate(in)

	a, err := json.Marshal(r)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	b, err := json.Marshal(Evaluate(in))
	if err != nil {
		t.Fatalf("marshal twice: %v", err)
	}
	if string(a) != string(b) {
		t.Errorf("JSON drifted between two Evaluates:\nfirst:  %s\nsecond: %s", a, b)
	}
	if !strings.Contains(string(a), `"safe_to_commit":false`) {
		t.Errorf("missing safe_to_commit:false in critical-finding case: %s", a)
	}
}

func TestPathMatchesReservation_Patterns(t *testing.T) {
	t.Parallel()
	cases := []struct {
		path, pattern string
		want          bool
	}{
		{"internal/auth/session.go", "internal/auth/**", true},
		{"internal/auth/session.go", "internal/auth/", false},
		{"internal/auth/session.go", "internal/auth/session.go", true},
		{"internal/auth", "internal/auth/**", true},
		{"internal/other/file.go", "internal/auth/**", false},
		{"internal/auth/session.go", "internal/auth/*", true},
		{"internal/auth/sub/file.go", "internal/auth/*", false},
		{"foo.go", "*.go", true},
		{"foo.go", "", false},
	}
	for _, c := range cases {
		got := pathMatchesReservation(c.path, c.pattern)
		if got != c.want {
			t.Errorf("pathMatchesReservation(%q, %q) = %v, want %v", c.path, c.pattern, got, c.want)
		}
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
