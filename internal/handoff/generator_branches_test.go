package handoff

import (
	"strings"
	"testing"
	"time"

	"github.com/Dicklesworthstone/ntm/internal/agentmail"
	"github.com/Dicklesworthstone/ntm/internal/cass"
)

// ---------------------------------------------------------------------------
// formatReservationSummary — 0% → 100%
// ---------------------------------------------------------------------------

func TestFormatReservationSummary_Empty(t *testing.T) {
	t.Parallel()

	result := formatReservationSummary(nil)
	if len(result) != 0 {
		t.Errorf("expected empty slice for nil input, got %d items", len(result))
	}
}

func TestFormatReservationSummary_Expired(t *testing.T) {
	t.Parallel()

	reservations := []agentmail.FileReservation{
		{
			PathPattern: "src/*.go",
			ExpiresTS:   agentmail.FlexTime{Time: time.Now().Add(-1 * time.Hour)},
			Exclusive:   false,
		},
	}

	result := formatReservationSummary(reservations)
	if len(result) != 1 {
		t.Fatalf("expected 1 entry, got %d", len(result))
	}
	if got := result[0]; got != "src/*.go (expires: expired)" {
		t.Errorf("unexpected format: %q", got)
	}
}

func TestFormatReservationSummary_ExpiresMinutes(t *testing.T) {
	t.Parallel()

	reservations := []agentmail.FileReservation{
		{
			PathPattern: "app/api/*.py",
			ExpiresTS:   agentmail.FlexTime{Time: time.Now().Add(30 * time.Minute)},
			Exclusive:   true,
		},
	}

	result := formatReservationSummary(reservations)
	if len(result) != 1 {
		t.Fatalf("expected 1 entry, got %d", len(result))
	}
	got := result[0]
	// Should contain "exclusive" and minutes format
	if !strings.Contains(got, "exclusive") {
		t.Errorf("expected 'exclusive' in result, got %q", got)
	}
	if !strings.Contains(got, "m") {
		t.Errorf("expected minutes format in result, got %q", got)
	}
}

func TestFormatReservationSummary_ExpiresHours(t *testing.T) {
	t.Parallel()

	reservations := []agentmail.FileReservation{
		{
			PathPattern: "internal/**",
			ExpiresTS:   agentmail.FlexTime{Time: time.Now().Add(3 * time.Hour)},
			Exclusive:   false,
		},
	}

	result := formatReservationSummary(reservations)
	if len(result) != 1 {
		t.Fatalf("expected 1 entry, got %d", len(result))
	}
	got := result[0]
	// Should contain hours format but NOT "exclusive"
	if strings.Contains(got, "exclusive") {
		t.Errorf("non-exclusive reservation should not contain 'exclusive', got %q", got)
	}
	if !strings.Contains(got, "h") {
		t.Errorf("expected hours format in result, got %q", got)
	}
}

func TestFormatReservationSummary_Multiple(t *testing.T) {
	t.Parallel()

	reservations := []agentmail.FileReservation{
		{PathPattern: "a.go", ExpiresTS: agentmail.FlexTime{Time: time.Now().Add(-1 * time.Minute)}, Exclusive: false},
		{PathPattern: "b.go", ExpiresTS: agentmail.FlexTime{Time: time.Now().Add(45 * time.Minute)}, Exclusive: true},
		{PathPattern: "c.go", ExpiresTS: agentmail.FlexTime{Time: time.Now().Add(2 * time.Hour)}, Exclusive: false},
	}

	result := formatReservationSummary(reservations)
	if len(result) != 3 {
		t.Fatalf("expected 3 entries, got %d", len(result))
	}
}

// ---------------------------------------------------------------------------
// buildReservationTransfer — 0% → 100%
// ---------------------------------------------------------------------------

func TestBuildReservationTransfer_EmptyReservations(t *testing.T) {
	t.Parallel()

	opts := GenerateHandoffOptions{AgentName: "TestAgent"}
	got := buildReservationTransfer(opts, "/proj", nil)
	if got != nil {
		t.Error("expected nil for empty reservations")
	}
}

func TestBuildReservationTransfer_EmptyAgentName(t *testing.T) {
	t.Parallel()

	reservations := []agentmail.FileReservation{
		{PathPattern: "a.go", Exclusive: true},
	}
	opts := GenerateHandoffOptions{AgentName: ""}
	got := buildReservationTransfer(opts, "/proj", reservations)
	if got != nil {
		t.Error("expected nil for empty agent name")
	}
}

func TestBuildReservationTransfer_Success(t *testing.T) {
	t.Parallel()

	expires := time.Now().Add(1 * time.Hour)
	reservations := []agentmail.FileReservation{
		{PathPattern: "src/*.go", Exclusive: true, Reason: "editing", ExpiresTS: agentmail.FlexTime{Time: expires}},
		{PathPattern: "tests/*.go", Exclusive: false, Reason: "reading", ExpiresTS: agentmail.FlexTime{Time: expires}},
	}
	opts := GenerateHandoffOptions{
		AgentName:            "BlueLake",
		TransferTTLSeconds:   3600,
		TransferGraceSeconds: 120,
	}

	got := buildReservationTransfer(opts, "/data/projects/ntm", reservations)
	if got == nil {
		t.Fatal("expected non-nil transfer")
	}
	if got.FromAgent != "BlueLake" {
		t.Errorf("FromAgent = %q, want %q", got.FromAgent, "BlueLake")
	}
	if got.ProjectKey != "/data/projects/ntm" {
		t.Errorf("ProjectKey = %q, want %q", got.ProjectKey, "/data/projects/ntm")
	}
	if got.TTLSeconds != 3600 {
		t.Errorf("TTLSeconds = %d, want 3600", got.TTLSeconds)
	}
	if got.GracePeriodSeconds != 120 {
		t.Errorf("GracePeriodSeconds = %d, want 120", got.GracePeriodSeconds)
	}
	if len(got.Reservations) != 2 {
		t.Fatalf("expected 2 reservations, got %d", len(got.Reservations))
	}
	if got.Reservations[0].PathPattern != "src/*.go" {
		t.Errorf("first reservation path = %q, want %q", got.Reservations[0].PathPattern, "src/*.go")
	}
	if !got.Reservations[0].Exclusive {
		t.Error("first reservation should be exclusive")
	}
	if got.Reservations[1].Exclusive {
		t.Error("second reservation should not be exclusive")
	}
	if got.CreatedAt.IsZero() {
		t.Error("CreatedAt should be set")
	}
}

func TestBuildCASSMemoryEntries_DedupAndOrder(t *testing.T) {
	t.Parallel()

	line := 12
	hits := []cass.SearchHit{
		{
			SourcePath: "sessions/b.jsonl",
			Agent:      "codex",
			SessionID:  "sess-b",
			Score:      0.70,
			Snippet:    "second priority snippet",
		},
		{
			SourcePath: "sessions/a.jsonl",
			LineNumber: &line,
			Agent:      "claude",
			SessionID:  "sess-a",
			Score:      0.90,
			Snippet:    "top snippet",
		},
		{
			SourcePath: "sessions/a.jsonl",
			LineNumber: &line,
			Agent:      "claude",
			SessionID:  "sess-a",
			Score:      0.80,
			Snippet:    "top snippet",
		},
	}

	entries := buildCASSMemoryEntries(hits, 5)
	if len(entries) != 2 {
		t.Fatalf("expected 2 deduped entries, got %d (%v)", len(entries), entries)
	}
	if !strings.HasPrefix(entries[0], "cass:sessions/a.jsonl#L12 [agent=claude, session=sess-a, score=0.90]") {
		t.Fatalf("unexpected first entry: %q", entries[0])
	}
	if !strings.HasPrefix(entries[1], "cass:sessions/b.jsonl [agent=codex, session=sess-b, score=0.70]") {
		t.Fatalf("unexpected second entry: %q", entries[1])
	}
}

func TestBuildCASSQuery_PrefersNowGoalNextBead(t *testing.T) {
	t.Parallel()

	h := &Handoff{
		Goal:        "Implement replay timeline",
		Now:         "Wire CASS handoff synthesis",
		Next:        []string{"Add deterministic tests"},
		ActiveBeads: []string{"bd-2mb03.6.5: CASS-backed handoff generator"},
	}

	query := buildCASSQuery(h)
	if !strings.Contains(query, "Wire CASS handoff synthesis") {
		t.Fatalf("query missing now text: %q", query)
	}
	if !strings.Contains(query, "Implement replay timeline") {
		t.Fatalf("query missing goal text: %q", query)
	}
	if !strings.Contains(query, "Add deterministic tests") {
		t.Fatalf("query missing next text: %q", query)
	}
	if !strings.Contains(query, "bd-2mb03.6.5") {
		t.Fatalf("query missing bead id: %q", query)
	}
}

func TestResolveCASSTimeout_Default(t *testing.T) {
	t.Parallel()

	if got := resolveCASSTimeout(0); got != DefaultCASSTimeout {
		t.Fatalf("resolveCASSTimeout(0) = %v, want %v", got, DefaultCASSTimeout)
	}
	if got := resolveCASSTimeout(-1 * time.Second); got != DefaultCASSTimeout {
		t.Fatalf("resolveCASSTimeout(-1s) = %v, want %v", got, DefaultCASSTimeout)
	}
}

func TestResolveCASSTimeout_Override(t *testing.T) {
	t.Parallel()

	want := 1500 * time.Millisecond
	if got := resolveCASSTimeout(want); got != want {
		t.Fatalf("resolveCASSTimeout(%v) = %v, want %v", want, got, want)
	}
}
