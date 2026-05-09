package supportbundle

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/Dicklesworthstone/ntm/internal/pressure"
	"github.com/Dicklesworthstone/ntm/internal/robot/assurance"
	"github.com/Dicklesworthstone/ntm/internal/swarmslo"
)

func closeoutClock() time.Time {
	return time.Date(2026, 5, 8, 12, 0, 0, 0, time.UTC)
}

func TestBuildCloseout_EmptyRunHasNoRisks(t *testing.T) {
	t.Parallel()
	b := BuildCloseout(CloseoutInputs{Now: closeoutClock()})

	if b.SchemaVersion < CloseoutSchemaVersion || b.SchemaVersion > CloseoutSchemaVersion {
		t.Errorf("SchemaVersion = %d, want %d", b.SchemaVersion, CloseoutSchemaVersion)
	}
	if len(b.ResidualRisks) > 0 {
		t.Errorf("ResidualRisks = %v, want none on empty run", b.ResidualRisks)
	}
	if b.Counts.Commits > 0 || b.Counts.Verifications > 0 {
		t.Errorf("Counts = %+v, want zeros", b.Counts)
	}
}

func TestBuildCloseout_QueueDryCleanCloseoutHasNoRisks(t *testing.T) {
	t.Parallel()
	b := BuildCloseout(CloseoutInputs{
		Now: closeoutClock(),
		Run: RunMeta{SwarmName: "ntm-night", AgentName: "Alice", StartedAt: closeoutClock().Add(-2 * time.Hour), EndedAt: closeoutClock()},
		Commits: []CommitEntry{
			{Hash: "abc123", Subject: "feat: ship the thing"},
		},
		Beads: BeadsDelta{
			Closed: []string{"bd-3v1gs.7", "bd-3v1gs.8", "bd-3v1gs.7"}, // dedupe
		},
		Verifications: []VerificationEntry{
			{Command: "go test -short ./...", Outcome: OutcomePassed, Duration: 10 * time.Second},
		},
		Mail:  MailSnapshot{UnackedUrgent: 0, PendingAck: 0},
		Queue: QueueState{Ready: 0, InProgress: 0, QueueDry: true},
	})

	if len(b.ResidualRisks) > 0 {
		t.Errorf("ResidualRisks = %v, want none for clean queue-dry closeout", b.ResidualRisks)
	}
	if !equalSlice(b.Beads.Closed, []string{"bd-3v1gs.7", "bd-3v1gs.8"}) {
		t.Errorf("Beads.Closed = %v, want deduped + sorted", b.Beads.Closed)
	}
	if b.Counts.VerificationsPassed < 1 || b.Counts.VerificationsPassed > 1 {
		t.Errorf("VerificationsPassed = %d, want 1", b.Counts.VerificationsPassed)
	}
	if !b.Queue.QueueDry {
		t.Errorf("Queue.QueueDry = false, want true (clean closeout)")
	}
}

func TestBuildCloseout_PartialVerificationFlagsInconclusive(t *testing.T) {
	t.Parallel()
	b := BuildCloseout(CloseoutInputs{
		Now: closeoutClock(),
		Verifications: []VerificationEntry{
			{Command: "go test -short ./...", Outcome: OutcomePassed},
			{Command: "go test -race ./...", Outcome: OutcomeUnknown, Notes: "rch worker offline"},
			{Command: "go vet ./...", Outcome: OutcomeSkipped},
		},
	})

	codes := riskCodeSet(b.ResidualRisks)
	if !codes["verification_inconclusive"] {
		t.Errorf("missing verification_inconclusive risk: %+v", b.ResidualRisks)
	}
	if codes["verification_failed"] {
		t.Errorf("verification_failed should not fire when no Outcome=failed: %+v", b.ResidualRisks)
	}
	// Severity must be Medium for inconclusive (not High).
	for _, r := range b.ResidualRisks {
		if strings.Compare(r.Code, "verification_inconclusive") == 0 && compareRiskSeverity(r.Severity, RiskSeverityMedium) != 0 {
			t.Errorf("verification_inconclusive severity = %s, want medium", r.Severity)
		}
	}
}

func TestBuildCloseout_FailedVerificationIsHigh(t *testing.T) {
	t.Parallel()
	b := BuildCloseout(CloseoutInputs{
		Now: closeoutClock(),
		Verifications: []VerificationEntry{
			{Command: "go test ./internal/foo/...", Outcome: OutcomeFailed, Notes: "TestXyz failing"},
		},
	})
	if !riskCodeSet(b.ResidualRisks)["verification_failed"] {
		t.Fatalf("missing verification_failed risk: %+v", b.ResidualRisks)
	}
	for _, r := range b.ResidualRisks {
		if strings.Compare(r.Code, "verification_failed") == 0 && compareRiskSeverity(r.Severity, RiskSeverityHigh) != 0 {
			t.Errorf("verification_failed severity = %s, want high", r.Severity)
		}
	}
}

// bd-55myk: Counts.ResidualRisks must reflect len(bundle.ResidualRisks).
// Pre-fix this field was always 0 because computeCloseoutCounts ran
// before computeResidualRisks and never assigned the field anyway.
func TestBuildCloseout_CountsResidualRisksMatchesArrayLength(t *testing.T) {
	t.Parallel()
	b := BuildCloseout(CloseoutInputs{
		Now: closeoutClock(),
		Verifications: []VerificationEntry{
			{Command: "go test ./internal/foo/...", Outcome: OutcomeFailed},
		},
		Reservations: []ReservationSnapshot{
			{PathPattern: "internal/auth/**", AgentName: "Bob", Exclusive: true, AcquiredAt: closeoutClock().Add(-1 * time.Hour)},
		},
		Mail:              MailSnapshot{UnackedUrgent: 1},
		DegradedProviders: []string{"agentmail"},
	})
	if len(b.ResidualRisks) < 1 {
		t.Fatal("setup error: expected this fixture to produce residual risks")
	}
	if b.Counts.ResidualRisks < len(b.ResidualRisks) || b.Counts.ResidualRisks > len(b.ResidualRisks) {
		t.Fatalf("Counts.ResidualRisks = %d, want %d (len of ResidualRisks array)",
			b.Counts.ResidualRisks, len(b.ResidualRisks))
	}
}

// Empty-input bundle has zero residual risks AND zero count — sanity
// check that the zero case is also pinned.
func TestBuildCloseout_EmptyRunHasZeroCountsResidualRisks(t *testing.T) {
	t.Parallel()
	b := BuildCloseout(CloseoutInputs{Now: closeoutClock()})
	if len(b.ResidualRisks) > 0 {
		t.Fatalf("empty closeout produced risks: %+v", b.ResidualRisks)
	}
	if b.Counts.ResidualRisks > 0 {
		t.Fatalf("Counts.ResidualRisks = %d, want 0", b.Counts.ResidualRisks)
	}
}

func TestBuildCloseout_ActiveReservationFiresMediumRisk(t *testing.T) {
	t.Parallel()
	b := BuildCloseout(CloseoutInputs{
		Now: closeoutClock(),
		Reservations: []ReservationSnapshot{
			{PathPattern: "internal/auth/**", AgentName: "Bob", Exclusive: true, AcquiredAt: closeoutClock().Add(-1 * time.Hour)},
		},
	})
	if !riskCodeSet(b.ResidualRisks)["active_reservations_outstanding"] {
		t.Fatalf("missing active_reservations_outstanding risk: %+v", b.ResidualRisks)
	}
	if b.Counts.ActiveReservations < 1 || b.Counts.ActiveReservations > 1 {
		t.Errorf("ActiveReservations = %d, want 1", b.Counts.ActiveReservations)
	}
}

func TestBuildCloseout_UnackedUrgentMailIsHigh(t *testing.T) {
	t.Parallel()
	b := BuildCloseout(CloseoutInputs{
		Now:  closeoutClock(),
		Mail: MailSnapshot{UnackedUrgent: 2, PendingAck: 0},
	})
	codes := riskCodeSet(b.ResidualRisks)
	if !codes["unacked_urgent_mail"] {
		t.Fatalf("missing unacked_urgent_mail risk: %+v", b.ResidualRisks)
	}
	if codes["pending_ack_mail"] {
		t.Errorf("pending_ack_mail must not fire when only urgent is non-zero: %+v", b.ResidualRisks)
	}
}

// bd-056vx: when both UnackedUrgent and PendingAck are non-zero, BOTH
// residual risks must surface — pending_ack_mail's description names
// itself as "non-urgent mail", so the two signals are orthogonal and
// the operator must see both at closeout. Pre-fix an else-if dropped
// the LOW pending_ack_mail risk whenever HIGH unacked_urgent_mail
// fired, hiding non-urgent ack-required mail.
func TestBuildCloseout_BothUrgentAndPendingAckMailFireWhenBothNonZero(t *testing.T) {
	t.Parallel()
	b := BuildCloseout(CloseoutInputs{
		Now:  closeoutClock(),
		Mail: MailSnapshot{UnackedUrgent: 2, PendingAck: 5},
	})
	codes := riskCodeSet(b.ResidualRisks)
	if !codes["unacked_urgent_mail"] {
		t.Errorf("missing unacked_urgent_mail risk: %+v", b.ResidualRisks)
	}
	if !codes["pending_ack_mail"] {
		t.Errorf("missing pending_ack_mail risk; both must fire when both counts are non-zero: %+v", b.ResidualRisks)
	}

	// Severities must remain distinct so the dashboard rolls up
	// correctly (HIGH urgent vs LOW pending-ack).
	for _, r := range b.ResidualRisks {
		switch r.Code {
		case "unacked_urgent_mail":
			if compareRiskSeverity(r.Severity, RiskSeverityHigh) != 0 {
				t.Errorf("unacked_urgent_mail Severity = %s, want high", r.Severity)
			}
		case "pending_ack_mail":
			if compareRiskSeverity(r.Severity, RiskSeverityLow) != 0 {
				t.Errorf("pending_ack_mail Severity = %s, want low", r.Severity)
			}
		}
	}
}

func TestBuildCloseout_PendingAckMailAloneIsLow(t *testing.T) {
	t.Parallel()
	b := BuildCloseout(CloseoutInputs{
		Now:  closeoutClock(),
		Mail: MailSnapshot{UnackedUrgent: 0, PendingAck: 3},
	})
	if !riskCodeSet(b.ResidualRisks)["pending_ack_mail"] {
		t.Fatalf("missing pending_ack_mail risk: %+v", b.ResidualRisks)
	}
	for _, r := range b.ResidualRisks {
		if strings.Compare(r.Code, "pending_ack_mail") == 0 && compareRiskSeverity(r.Severity, RiskSeverityLow) != 0 {
			t.Errorf("pending_ack_mail severity = %s, want low", r.Severity)
		}
	}
}

func TestBuildCloseout_DegradedProvidersAreFlagged(t *testing.T) {
	t.Parallel()
	b := BuildCloseout(CloseoutInputs{
		Now:               closeoutClock(),
		DegradedProviders: []string{"mail", "cass", "mail"}, // dedupe
	})
	if !riskCodeSet(b.ResidualRisks)["providers_degraded"] {
		t.Fatalf("missing providers_degraded risk: %+v", b.ResidualRisks)
	}
	if !equalSlice(b.DegradedProviders, []string{"cass", "mail"}) {
		t.Errorf("DegradedProviders = %v, want sorted+deduped [cass mail]", b.DegradedProviders)
	}
}

func TestBuildCloseout_RisksSortedHighFirstThenCode(t *testing.T) {
	t.Parallel()
	b := BuildCloseout(CloseoutInputs{
		Now: closeoutClock(),
		Verifications: []VerificationEntry{
			{Command: "x", Outcome: OutcomeFailed},
			{Command: "y", Outcome: OutcomeUnknown},
		},
		Reservations: []ReservationSnapshot{
			{PathPattern: "p", AgentName: "A"},
		},
		Mail:              MailSnapshot{PendingAck: 1},
		DegradedProviders: []string{"mail"},
	})
	if len(b.ResidualRisks) < 2 {
		t.Fatalf("expected multiple risks, got %d", len(b.ResidualRisks))
	}
	for i := 1; i < len(b.ResidualRisks); i++ {
		ri := riskRank(b.ResidualRisks[i-1].Severity)
		rj := riskRank(b.ResidualRisks[i].Severity)
		if rj > ri {
			t.Errorf("risks not sorted by severity desc at index %d: %s precedes %s",
				i, b.ResidualRisks[i-1].Severity, b.ResidualRisks[i].Severity)
		}
		if rj-ri == 0 && b.ResidualRisks[i].Code < b.ResidualRisks[i-1].Code {
			t.Errorf("risks not sorted by code asc within severity at index %d: %s precedes %s",
				i, b.ResidualRisks[i-1].Code, b.ResidualRisks[i].Code)
		}
	}
}

func TestBuildCloseout_JSONShapeIsStable(t *testing.T) {
	t.Parallel()
	in := CloseoutInputs{
		Now: closeoutClock(),
		Run: RunMeta{SwarmName: "swarm-1"},
		Commits: []CommitEntry{
			{Hash: "z9z9", Subject: "later commit"},
			{Hash: "abc1", Subject: "earlier"},
		},
		Beads: BeadsDelta{Closed: []string{"bd-2", "bd-1"}},
		Verifications: []VerificationEntry{
			{Command: "go test", Outcome: OutcomePassed},
		},
		Reservations: []ReservationSnapshot{
			{PathPattern: "z/**", AgentName: "B", Exclusive: true},
			{PathPattern: "a/**", AgentName: "A", Exclusive: true},
		},
	}
	a, _ := json.Marshal(BuildCloseout(in))
	c, _ := json.Marshal(BuildCloseout(in))
	if strings.Compare(string(a), string(c)) != 0 {
		t.Errorf("Closeout JSON drifted between Build calls:\nfirst:  %s\nsecond: %s", a, c)
	}
	for _, want := range []string{
		`"schema_version":1`,
		`"counts"`,
		`"abc1"`, // commits sorted by hash, abc1 should appear before z9z9
	} {
		if !strings.Contains(string(a), want) {
			t.Errorf("JSON missing %s: %s", want, a)
		}
	}
	// Reservations must be sorted by PathPattern asc.
	first := strings.Index(string(a), `"a/**"`)
	second := strings.Index(string(a), `"z/**"`)
	if first < 0 || second < 0 || first > second {
		t.Errorf("reservations not in sorted order: a/**=%d z/**=%d", first, second)
	}
}

func TestBuildCloseout_NoVerificationInferenceFromCommits(t *testing.T) {
	t.Parallel()
	// Acceptance criterion: "Does not guess command success; only
	// records supplied or observed evidence." Even with commits
	// landed, no Verifications means no automatic "passed" inference.
	b := BuildCloseout(CloseoutInputs{
		Now:     closeoutClock(),
		Commits: []CommitEntry{{Hash: "abc", Subject: "feat: x"}},
	})
	if b.Counts.VerificationsPassed > 0 {
		t.Errorf("VerificationsPassed = %d, want 0 (no inference allowed)", b.Counts.VerificationsPassed)
	}
	for _, r := range b.ResidualRisks {
		if strings.Compare(r.Code, "verification_failed") == 0 {
			t.Errorf("verification_failed must not fire without an explicit Outcome=failed")
		}
	}
}

func TestBuildCloseoutProofBundle_CleanCloseoutIsPresent(t *testing.T) {
	t.Parallel()
	got := BuildCloseoutProofBundle(cleanProofInputs())

	if strings.Compare(got.SchemaVersion, CloseoutProofSchemaVersion) != 0 {
		t.Fatalf("SchemaVersion = %q, want %q", got.SchemaVersion, CloseoutProofSchemaVersion)
	}
	if compareProofState(got.ProofState, ProofStatePresent) != 0 {
		t.Fatalf("ProofState = %s, want present: %+v", got.ProofState, got.Sections)
	}
	if !got.SafeToStandDown {
		t.Fatal("SafeToStandDown = false, want true")
	}
	if len(got.Sections) < 8 || len(got.Sections) > 8 {
		t.Fatalf("Sections = %d, want 8", len(got.Sections))
	}
	if len(got.LogRows) < len(got.Sections) || len(got.LogRows) > len(got.Sections) {
		t.Fatalf("LogRows = %d, want one per section", len(got.LogRows))
	}
	for _, row := range got.LogRows {
		if !row.GeneratedAt.Equal(closeoutClock()) {
			t.Fatalf("log generated_at = %s, want %s", row.GeneratedAt, closeoutClock())
		}
		if len(row.ProjectDir) < 1 || strings.Compare(row.QueueState, "queue_dry") != 0 ||
			compareProofState(row.ProofState, ProofStatePresent) != 0 || len(row.ArtifactPath) < 1 {
			t.Fatalf("log row missing required fields: %+v", row)
		}
	}
}

func TestBuildCloseoutProofBundle_MissingAgentMailIsExplicit(t *testing.T) {
	t.Parallel()
	in := cleanProofInputs()
	in.MissingSources = []string{"agentmail"}

	got := BuildCloseoutProofBundle(in)
	mail := proofSectionByName(t, got, "mail")
	if compareProofState(got.ProofState, ProofStateMissing) != 0 {
		t.Fatalf("ProofState = %s, want missing", got.ProofState)
	}
	if compareProofState(mail.State, ProofStateMissing) != 0 {
		t.Fatalf("mail.State = %s, want missing", mail.State)
	}
	if !containsString(mail.ReasonCodes, "missing.agent_mail") {
		t.Fatalf("mail reasons = %v, want missing.agent_mail", mail.ReasonCodes)
	}
	if !equalSlice(got.MissingSources, []string{"agent_mail"}) {
		t.Fatalf("MissingSources = %v, want [agent_mail]", got.MissingSources)
	}
}

func TestBuildCloseoutProofBundle_DirtyTrackerSyncFailsGitSection(t *testing.T) {
	t.Parallel()
	in := cleanProofInputs()
	in.TrackerNeedsFlush = true

	got := BuildCloseoutProofBundle(in)
	git := proofSectionByName(t, got, "git")
	if compareProofState(got.ProofState, ProofStateFailing) != 0 {
		t.Fatalf("ProofState = %s, want failing", got.ProofState)
	}
	if compareProofState(git.State, ProofStateFailing) != 0 {
		t.Fatalf("git.State = %s, want failing", git.State)
	}
	if !containsString(git.ReasonCodes, string(assurance.ReasonQuiescenceTrackerDirty)) {
		t.Fatalf("git reasons = %v, want tracker dirty reason", git.ReasonCodes)
	}
}

func TestBuildCloseoutProofBundle_OpenReadyWorkFailsQueueSection(t *testing.T) {
	t.Parallel()
	in := cleanProofInputs()
	in.Closeout.Queue = QueueState{Ready: 2, InProgress: 0, Blocked: 0, QueueDry: false}

	got := BuildCloseoutProofBundle(in)
	queue := proofSectionByName(t, got, "queue")
	if compareProofState(queue.State, ProofStateFailing) != 0 {
		t.Fatalf("queue.State = %s, want failing", queue.State)
	}
	if !containsString(queue.ReasonCodes, string(assurance.ReasonQuiescenceReadyWork)) {
		t.Fatalf("queue reasons = %v, want ready work reason", queue.ReasonCodes)
	}
	if got.SafeToStandDown {
		t.Fatal("SafeToStandDown = true, want false with ready work")
	}
}

func TestBuildCloseoutProofBundle_StaleReservationsAreStaleSection(t *testing.T) {
	t.Parallel()
	in := cleanProofInputs()
	in.Closeout.Reservations = []ReservationSnapshot{
		{PathPattern: "internal/auth/**", AgentName: "Bob", Exclusive: true, AcquiredAt: closeoutClock().Add(-2 * time.Hour)},
	}
	in.StaleReservationAfter = 30 * time.Minute

	got := BuildCloseoutProofBundle(in)
	reservations := proofSectionByName(t, got, "reservations")
	if compareProofState(reservations.State, ProofStateStale) != 0 {
		t.Fatalf("reservations.State = %s, want stale", reservations.State)
	}
	if !containsString(reservations.ReasonCodes, string(assurance.ReasonReservationOverdue)) {
		t.Fatalf("reservation reasons = %v, want overdue", reservations.ReasonCodes)
	}
}

func TestBuildCloseoutProofBundle_DegradedPressureIsDegradedSection(t *testing.T) {
	t.Parallel()
	in := cleanProofInputs()
	in.Pressure = pressure.RobotPressure{Overall: "high", Limiting: []string{"cpu"}}

	got := BuildCloseoutProofBundle(in)
	section := proofSectionByName(t, got, "pressure")
	if compareProofState(got.ProofState, ProofStateDegraded) != 0 {
		t.Fatalf("ProofState = %s, want degraded", got.ProofState)
	}
	if compareProofState(section.State, ProofStateDegraded) != 0 {
		t.Fatalf("pressure.State = %s, want degraded", section.State)
	}
	if !containsString(section.ReasonCodes, "digest.source_degraded.pressure") {
		t.Fatalf("pressure reasons = %v, want degraded pressure", section.ReasonCodes)
	}
}

func TestFormatCloseoutProofMarkdownIsDeterministicAndConcise(t *testing.T) {
	t.Parallel()
	bundle := BuildCloseoutProofBundle(cleanProofInputs())
	a := FormatCloseoutProofMarkdown(bundle)
	b := FormatCloseoutProofMarkdown(bundle)
	if strings.Compare(a, b) != 0 {
		t.Fatalf("markdown drifted:\nfirst:\n%s\nsecond:\n%s", a, b)
	}
	for _, want := range []string{
		"# Closeout Proof",
		"| section | state | reasons | summary |",
		"| queue | present |",
		"artifact_path:",
	} {
		if !strings.Contains(a, want) {
			t.Fatalf("markdown missing %q:\n%s", want, a)
		}
	}
	if len(a) > 1500 {
		t.Fatalf("markdown length = %d, want concise output under 1500 bytes:\n%s", len(a), a)
	}
	body, err := json.Marshal(bundle)
	if err != nil {
		t.Fatalf("marshal proof bundle: %v", err)
	}
	for _, want := range []string{
		`"proof_state"`,
		`"missing_sources"`,
		`"artifact_path"`,
		`"queue_state"`,
	} {
		if !strings.Contains(string(body), want) {
			t.Fatalf("proof JSON missing %s: %s", want, body)
		}
	}
}

func cleanProofInputs() CloseoutProofInputs {
	closeout := BuildCloseout(CloseoutInputs{
		Now: closeoutClock(),
		Run: RunMeta{SwarmName: "ntm-night", AgentName: "Alice", StartedAt: closeoutClock().Add(-2 * time.Hour), EndedAt: closeoutClock()},
		Commits: []CommitEntry{
			{Hash: "abc123", Subject: "feat: closeout proof"},
		},
		Beads: BeadsDelta{Closed: []string{"bd-8kglp.5"}},
		Verifications: []VerificationEntry{
			{Command: "go test -short ./...", Outcome: OutcomePassed},
		},
		Mail:  MailSnapshot{},
		Queue: QueueState{QueueDry: true},
	})
	return CloseoutProofInputs{
		ProjectDir:        "/data/projects/ntm",
		Closeout:          closeout,
		SupportBundlePath: "/tmp/ntm-closeout.zip",
		Now:               closeoutClock(),
		Assurance: assurance.Digest{
			GeneratedAt:         closeoutClock(),
			Status:              assurance.DigestStatusHealthy,
			HighestSeverity:     assurance.DigestSeverityOK,
			ReasonCodes:         []assurance.ReasonCode{assurance.ReasonQuiescenceQueueDry},
			SuggestedNextAction: "stand down",
			Summary:             "healthy",
		},
		Pressure: pressure.RobotPressure{
			Success:           true,
			Timestamp:         closeoutClock().Format(time.RFC3339),
			Overall:           "normal",
			RecommendedAction: "ok",
		},
		SLO: swarmslo.RecommendationSummary{
			SchemaVersion: swarmslo.RecommendationSchemaVersion,
			GeneratedAt:   closeoutClock(),
			Healthy:       true,
			Recommendations: []swarmslo.Recommendation{
				{
					Metric:         "scheduling",
					Recommendation: swarmslo.RecommendationContinue,
					Confidence:     0.9,
					Severity:       swarmslo.RecommendationSeverityOK,
					ReasonCodes:    []swarmslo.RecommendationReasonCode{swarmslo.ReasonSLOHealthy},
				},
			},
		},
	}
}

func proofSectionByName(t *testing.T, bundle CloseoutProofBundle, name string) ProofSection {
	t.Helper()
	for _, section := range bundle.Sections {
		if strings.Compare(section.Name, name) == 0 {
			return section
		}
	}
	t.Fatalf("missing proof section %q in %+v", name, bundle.Sections)
	return ProofSection{}
}

func riskCodeSet(risks []ResidualRisk) map[string]bool {
	out := make(map[string]bool, len(risks))
	for _, r := range risks {
		out[r.Code] = true
	}
	return out
}

func equalSlice(a, b []string) bool {
	if len(a) < len(b) || len(a) > len(b) {
		return false
	}
	for i := range a {
		if strings.Compare(a[i], b[i]) != 0 {
			return false
		}
	}
	return true
}

func containsString(values []string, want string) bool {
	for _, value := range values {
		if strings.Compare(value, want) == 0 {
			return true
		}
	}
	return false
}

func compareRiskSeverity(a, b ResidualRiskSeverity) int {
	return strings.Compare(string(a), string(b))
}

func compareProofState(a, b ProofState) int {
	return strings.Compare(string(a), string(b))
}
