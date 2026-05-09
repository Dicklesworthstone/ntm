package swarmslo

import (
	"encoding/json"
	"strings"
	"testing"
	"time"
)

func clock() time.Time {
	return time.Date(2026, 5, 8, 12, 0, 0, 0, time.UTC)
}

func at(offset time.Duration) time.Time { return clock().Add(offset) }
func ptrAt(offset time.Duration) *time.Time {
	t := at(offset)
	return &t
}

func TestCompute_AllZerosWhenNoInputs(t *testing.T) {
	t.Parallel()
	got := Compute(Inputs{Now: clock()})
	if got.TimeToFirstAck.Count != 0 {
		t.Errorf("TimeToFirstAck.Count = %d, want 0", got.TimeToFirstAck.Count)
	}
	if got.ReadyToClaim.Count != 0 {
		t.Errorf("ReadyToClaim.Count = %d, want 0", got.ReadyToClaim.Count)
	}
	if got.ReservationContention.Count != 0 {
		t.Errorf("ReservationContention.Count = %d, want 0", got.ReservationContention.Count)
	}
	if !got.GeneratedAt.Equal(clock()) {
		t.Errorf("GeneratedAt = %s, want %s", got.GeneratedAt, clock())
	}
}

func TestCompute_TimeToFirstAck_OnlyAckRequiredWithAck(t *testing.T) {
	t.Parallel()
	created := at(0)
	in := Inputs{
		Now: at(2 * time.Hour),
		Mail: []MailEvent{
			// Acked after 30s.
			{ID: 1, CreatedAt: created, AckedAt: ptrAt(30 * time.Second), AckRequired: true},
			// Acked after 90s.
			{ID: 2, CreatedAt: created, AckedAt: ptrAt(90 * time.Second), AckRequired: true},
			// Ack required but never acked — must NOT count.
			{ID: 3, CreatedAt: created, AckedAt: nil, AckRequired: true},
			// Not ack-required — must NOT count.
			{ID: 4, CreatedAt: created, AckedAt: ptrAt(5 * time.Second), AckRequired: false},
			// Pre-acked (acked before create) — must be filtered.
			{ID: 5, CreatedAt: at(60 * time.Second), AckedAt: ptrAt(10 * time.Second), AckRequired: true},
		},
	}
	d := Compute(in).TimeToFirstAck
	if d.Count != 2 {
		t.Fatalf("Count = %d, want 2", d.Count)
	}
	if d.MaxSeconds != 90 {
		t.Errorf("MaxSeconds = %v, want 90", d.MaxSeconds)
	}
	if d.MeanSeconds != 60 {
		t.Errorf("MeanSeconds = %v, want 60", d.MeanSeconds)
	}
}

// bd-h1i8z: Distribution.Pending must surface the count of
// ack-required messages with AckedAt=nil. Pre-fix this number was
// computed and discarded via '_ = pending', so a swarm with 0 acks
// + 50 pending was indistinguishable in JSON from 0 messages at all.
func TestCompute_TimeToFirstAck_PendingCountIsSurfaced(t *testing.T) {
	t.Parallel()
	created := at(0)
	in := Inputs{
		Now: at(2 * time.Hour),
		Mail: []MailEvent{
			// 1 acked.
			{ID: 1, CreatedAt: created, AckedAt: ptrAt(30 * time.Second), AckRequired: true},
			// 3 pending (ack-required, AckedAt=nil).
			{ID: 2, CreatedAt: created, AckedAt: nil, AckRequired: true},
			{ID: 3, CreatedAt: created, AckedAt: nil, AckRequired: true},
			{ID: 4, CreatedAt: created, AckedAt: nil, AckRequired: true},
			// Non-ack-required pending — not counted as pending.
			{ID: 5, CreatedAt: created, AckedAt: nil, AckRequired: false},
		},
	}
	d := Compute(in).TimeToFirstAck
	if d.Count != 1 {
		t.Errorf("Count = %d, want 1 (one acked sample)", d.Count)
	}
	if d.Pending != 3 {
		t.Fatalf("Pending = %d, want 3 (three ack-required, AckedAt=nil)", d.Pending)
	}
}

// Pending=0 must NOT serialize the field, so existing consumers see
// the same envelope when no acks are pending.
func TestCompute_TimeToFirstAck_PendingZeroOmitsFromJSON(t *testing.T) {
	t.Parallel()
	created := at(0)
	in := Inputs{
		Now: at(2 * time.Hour),
		Mail: []MailEvent{
			{ID: 1, CreatedAt: created, AckedAt: ptrAt(30 * time.Second), AckRequired: true},
		},
	}
	body, err := json.Marshal(Compute(in).TimeToFirstAck)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if strings.Contains(string(body), `"pending"`) {
		t.Errorf("zero Pending leaked into JSON: %s", body)
	}
}

// All-pending fixture (no acks at all): Count=0, Pending=N. The
// previous implementation reported the SAME envelope for "no
// messages" and "N pending messages."
func TestCompute_TimeToFirstAck_AllPendingDistinguishesFromEmpty(t *testing.T) {
	t.Parallel()
	created := at(0)
	allPending := Inputs{
		Now: at(2 * time.Hour),
		Mail: []MailEvent{
			{ID: 1, CreatedAt: created, AckedAt: nil, AckRequired: true},
			{ID: 2, CreatedAt: created, AckedAt: nil, AckRequired: true},
		},
	}
	empty := Inputs{Now: at(2 * time.Hour)}

	allPendingDist := Compute(allPending).TimeToFirstAck
	emptyDist := Compute(empty).TimeToFirstAck

	if allPendingDist.Count != 0 || emptyDist.Count != 0 {
		t.Fatalf("Counts must both be 0; got allPending=%d empty=%d", allPendingDist.Count, emptyDist.Count)
	}
	if allPendingDist.Pending != 2 {
		t.Errorf("allPending.Pending = %d, want 2", allPendingDist.Pending)
	}
	if emptyDist.Pending != 0 {
		t.Errorf("empty.Pending = %d, want 0", emptyDist.Pending)
	}
}

func TestCompute_ReadyToClaim_PairsTransitions(t *testing.T) {
	t.Parallel()
	in := Inputs{
		Now: clock(),
		Beads: []BeadTransition{
			// bd-1: ready at +0, claimed at +60s -> 60s.
			{BeadID: "bd-1", Status: "ready", EnteredAt: at(0)},
			{BeadID: "bd-1", Status: "in_progress", EnteredAt: at(60 * time.Second)},
			// bd-2: ready at +0, claimed at +5min -> 300s.
			{BeadID: "bd-2", Status: "ready", EnteredAt: at(0)},
			{BeadID: "bd-2", Status: "in_progress", EnteredAt: at(5 * time.Minute)},
			// bd-3: claimed before any ready transition -> ignored.
			{BeadID: "bd-3", Status: "in_progress", EnteredAt: at(10 * time.Second)},
		},
	}
	d := Compute(in).ReadyToClaim
	if d.Count != 2 {
		t.Fatalf("Count = %d, want 2", d.Count)
	}
	if d.MaxSeconds != 300 {
		t.Errorf("MaxSeconds = %v, want 300", d.MaxSeconds)
	}
	if d.MeanSeconds != 180 {
		t.Errorf("MeanSeconds = %v, want 180", d.MeanSeconds)
	}
}

func TestCompute_ClaimToCloseout(t *testing.T) {
	t.Parallel()
	in := Inputs{
		Now: clock(),
		Beads: []BeadTransition{
			{BeadID: "bd-a", Status: "in_progress", EnteredAt: at(0)},
			{BeadID: "bd-a", Status: "closed", EnteredAt: at(2 * time.Hour)},
			{BeadID: "bd-b", Status: "in_progress", EnteredAt: at(30 * time.Minute)},
			{BeadID: "bd-b", Status: "closed", EnteredAt: at(40 * time.Minute)}, // 600s
		},
	}
	d := Compute(in).ClaimToCloseout
	if d.Count != 2 {
		t.Fatalf("Count = %d, want 2", d.Count)
	}
	if d.MaxSeconds != 7200 {
		t.Errorf("MaxSeconds = %v, want 7200", d.MaxSeconds)
	}
}

func TestCompute_StaleInProgress_ExcludesClosed(t *testing.T) {
	t.Parallel()
	in := Inputs{
		Now: at(2 * time.Hour),
		Beads: []BeadTransition{
			// bd-a: still in_progress, claimed at t=0 -> 7200s stale.
			{BeadID: "bd-a", Status: "ready", EnteredAt: at(-1 * time.Hour)},
			{BeadID: "bd-a", Status: "in_progress", EnteredAt: at(0)},
			// bd-b: closed -> excluded.
			{BeadID: "bd-b", Status: "in_progress", EnteredAt: at(0)},
			{BeadID: "bd-b", Status: "closed", EnteredAt: at(30 * time.Minute)},
		},
	}
	d := Compute(in).StaleInProgress
	if d.Count != 1 {
		t.Fatalf("Count = %d, want 1", d.Count)
	}
	if d.MaxSeconds != 7200 {
		t.Errorf("MaxSeconds = %v, want 7200", d.MaxSeconds)
	}
}

func TestCompute_ReservationContention_DifferentAgentsOnly(t *testing.T) {
	t.Parallel()
	in := Inputs{
		Now: clock(),
		Reservations: []ReservationWindow{
			// Alice acquires at +0, releases at +60s.
			// Bob acquires at +90s -> 30s contention.
			{PathPattern: "internal/auth/**", AgentName: "Alice", AcquiredAt: at(0), ReleasedAt: ptrAt(60 * time.Second)},
			{PathPattern: "internal/auth/**", AgentName: "Bob", AcquiredAt: at(90 * time.Second), ReleasedAt: ptrAt(150 * time.Second)},
			// Carol acquires at +200s -> Bob released at +150s -> 50s contention.
			{PathPattern: "internal/auth/**", AgentName: "Carol", AcquiredAt: at(200 * time.Second), ReleasedAt: nil},
			// Same agent renewal -> NOT counted.
			{PathPattern: "internal/foo/**", AgentName: "Alice", AcquiredAt: at(0), ReleasedAt: ptrAt(10 * time.Second)},
			{PathPattern: "internal/foo/**", AgentName: "Alice", AcquiredAt: at(20 * time.Second), ReleasedAt: nil},
		},
	}
	d := Compute(in).ReservationContention
	if d.Count != 2 {
		t.Fatalf("Count = %d, want 2 (renewal must not count)", d.Count)
	}
	if d.MaxSeconds != 50 {
		t.Errorf("MaxSeconds = %v, want 50", d.MaxSeconds)
	}
	if d.MeanSeconds != 40 {
		t.Errorf("MeanSeconds = %v, want 40", d.MeanSeconds)
	}
}

func TestCompute_MissingSourcesPropagateAndWarnAndZero(t *testing.T) {
	t.Parallel()
	in := Inputs{
		Now:            clock(),
		MissingSources: []string{"agentmail", "beads"},
	}
	got := Compute(in)
	if !got.TimeToFirstAck.Missing {
		t.Errorf("TimeToFirstAck.Missing = false, want true (agentmail unavailable)")
	}
	if !got.ReadyToClaim.Missing || !got.ClaimToCloseout.Missing || !got.StaleInProgress.Missing {
		t.Errorf("beads-derived metrics did not all set Missing: ready=%v claim=%v stale=%v",
			got.ReadyToClaim.Missing, got.ClaimToCloseout.Missing, got.StaleInProgress.Missing)
	}
	if got.ReservationContention.Missing {
		t.Errorf("ReservationContention.Missing = true, want false (reservations not in MissingSources)")
	}
	if len(got.Warnings) != 2 {
		t.Errorf("Warnings = %v, want 2 entries", got.Warnings)
	}
}

func TestCompute_PercentileShape(t *testing.T) {
	t.Parallel()
	// 20 evenly-spaced ack latencies: 1s..20s.
	mail := make([]MailEvent, 20)
	for i := 0; i < 20; i++ {
		mail[i] = MailEvent{
			ID:          i + 1,
			CreatedAt:   at(0),
			AckedAt:     ptrAt(time.Duration(i+1) * time.Second),
			AckRequired: true,
		}
	}
	d := Compute(Inputs{Now: clock(), Mail: mail}).TimeToFirstAck
	if d.Count != 20 {
		t.Fatalf("Count = %d", d.Count)
	}
	if d.MaxSeconds != 20 {
		t.Errorf("MaxSeconds = %v, want 20", d.MaxSeconds)
	}
	// p50 nearest-rank: ceil(50/100*20)=10 -> sorted[9]=10
	if d.P50Seconds != 10 {
		t.Errorf("P50Seconds = %v, want 10", d.P50Seconds)
	}
	// p95: ceil(95/100*20)=19 -> sorted[18]=19
	if d.P95Seconds != 19 {
		t.Errorf("P95Seconds = %v, want 19", d.P95Seconds)
	}
}

func TestCompute_JSONShapeIsStableAndDeterministic(t *testing.T) {
	t.Parallel()
	in := Inputs{
		Now: clock(),
		Mail: []MailEvent{
			{ID: 1, CreatedAt: at(0), AckedAt: ptrAt(30 * time.Second), AckRequired: true},
		},
		Beads: []BeadTransition{
			{BeadID: "bd-1", Status: "ready", EnteredAt: at(0)},
			{BeadID: "bd-1", Status: "in_progress", EnteredAt: at(60 * time.Second)},
		},
	}
	a, _ := json.Marshal(Compute(in))
	b, _ := json.Marshal(Compute(in))
	if string(a) != string(b) {
		t.Errorf("JSON drifted between two Compute calls:\nfirst:  %s\nsecond: %s", a, b)
	}
	for _, want := range []string{
		`"time_to_first_ack"`, `"ready_to_claim"`, `"claim_to_closeout"`,
		`"reservation_contention"`, `"stale_in_progress"`, `"generated_at"`,
	} {
		if !strings.Contains(string(a), want) {
			t.Errorf("JSON missing field %s: %s", want, a)
		}
	}
}

func TestCompute_NegativeAckLatencyIsDropped(t *testing.T) {
	t.Parallel()
	in := Inputs{
		Now: clock(),
		Mail: []MailEvent{
			// Acked 30s BEFORE created — must be silently dropped.
			{ID: 1, CreatedAt: at(60 * time.Second), AckedAt: ptrAt(30 * time.Second), AckRequired: true},
			// Valid sample.
			{ID: 2, CreatedAt: at(0), AckedAt: ptrAt(10 * time.Second), AckRequired: true},
		},
	}
	d := Compute(in).TimeToFirstAck
	if d.Count != 1 {
		t.Fatalf("Count = %d, want 1 (negative latency dropped)", d.Count)
	}
}

func TestRecommend_HealthySLOsContinueCurrentSchedule(t *testing.T) {
	t.Parallel()
	summary := Summary{
		GeneratedAt: clock(),
		TimeToFirstAck: Distribution{
			Count:      3,
			P95Seconds: 20,
		},
		ReadyToClaim: Distribution{
			Count:      3,
			P95Seconds: 30,
		},
		ClaimToCloseout: Distribution{
			Count:      3,
			P95Seconds: 40,
		},
		ReservationContention: Distribution{
			Count:      1,
			P95Seconds: 5,
		},
		StaleInProgress: Distribution{
			Count:      1,
			P95Seconds: 50,
		},
	}

	got := Recommend(RecommendationInput{Summary: summary, Thresholds: testRecommendationThresholds()})
	if got.SchemaVersion != RecommendationSchemaVersion {
		t.Fatalf("SchemaVersion = %q, want %q", got.SchemaVersion, RecommendationSchemaVersion)
	}
	if !got.Healthy {
		t.Fatal("Healthy = false, want true for all metrics inside thresholds")
	}
	if len(got.Recommendations) != 1 {
		t.Fatalf("Recommendations = %d, want exactly one continue recommendation: %+v", len(got.Recommendations), got.Recommendations)
	}
	rec := got.Recommendations[0]
	if rec.Recommendation != RecommendationContinue {
		t.Fatalf("Recommendation = %q, want %q", rec.Recommendation, RecommendationContinue)
	}
	if !containsRecommendationReason(rec.ReasonCodes, ReasonSLOHealthy) {
		t.Fatalf("ReasonCodes = %v, want %s", rec.ReasonCodes, ReasonSLOHealthy)
	}
	if len(got.LogRows) != 1 {
		t.Fatalf("LogRows = %d, want 1", len(got.LogRows))
	}
	row := got.LogRows[0]
	if row.Metric != "scheduling" ||
		row.Recommendation != RecommendationContinue ||
		row.Confidence == 0 ||
		!containsRecommendationReason(row.ReasonCodes, ReasonSLOHealthy) {
		t.Fatalf("log row missing required scheduling fields: %+v", row)
	}
}

func TestRecommend_HighMetricDistributionsMapToSchedulingActions(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name       string
		summary    Summary
		wantMetric string
		wantAction RecommendationAction
		wantReason RecommendationReasonCode
	}{
		{
			name: "high ready-to-claim adds reviewer",
			summary: Summary{
				GeneratedAt:  clock(),
				ReadyToClaim: Distribution{Count: 2, P95Seconds: 90},
			},
			wantMetric: "ready_to_claim",
			wantAction: RecommendationAddReviewer,
			wantReason: ReasonSLOReadyToClaimHigh,
		},
		{
			name: "high claim-to-close splits bead",
			summary: Summary{
				GeneratedAt:     clock(),
				ClaimToCloseout: Distribution{Count: 2, P95Seconds: 120},
			},
			wantMetric: "claim_to_closeout",
			wantAction: RecommendationSplitBead,
			wantReason: ReasonSLOClaimToCloseoutHigh,
		},
		{
			name: "reservation contention renews reservation",
			summary: Summary{
				GeneratedAt:           clock(),
				ReservationContention: Distribution{Count: 2, P95Seconds: 80},
			},
			wantMetric: "reservation_contention",
			wantAction: RecommendationRenewReservation,
			wantReason: ReasonSLOReservationContentionHigh,
		},
		{
			name: "stale in-progress stops idle pane",
			summary: Summary{
				GeneratedAt:     clock(),
				StaleInProgress: Distribution{Count: 2, P95Seconds: 120},
			},
			wantMetric: "stale_in_progress",
			wantAction: RecommendationStopIdlePane,
			wantReason: ReasonSLOStaleInProgressHigh,
		},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got := Recommend(RecommendationInput{Summary: tt.summary, Thresholds: testRecommendationThresholds()})
			if got.Healthy {
				t.Fatal("Healthy = true, want false for threshold breach")
			}
			if len(got.Recommendations) != 1 {
				t.Fatalf("Recommendations = %d, want 1: %+v", len(got.Recommendations), got.Recommendations)
			}
			rec := got.Recommendations[0]
			if rec.Metric != tt.wantMetric {
				t.Fatalf("Metric = %q, want %q", rec.Metric, tt.wantMetric)
			}
			if rec.Recommendation != tt.wantAction {
				t.Fatalf("Recommendation = %q, want %q", rec.Recommendation, tt.wantAction)
			}
			if !containsRecommendationReason(rec.ReasonCodes, tt.wantReason) {
				t.Fatalf("ReasonCodes = %v, want %s", rec.ReasonCodes, tt.wantReason)
			}
			if len(got.LogRows) != 1 {
				t.Fatalf("LogRows = %d, want 1", len(got.LogRows))
			}
			assertLogRowCoversRecommendation(t, got.LogRows[0], rec)
		})
	}
}

func TestRecommend_PendingAcksReduceFanOut(t *testing.T) {
	t.Parallel()
	summary := Summary{
		GeneratedAt:    clock(),
		TimeToFirstAck: Distribution{Pending: 2},
	}

	got := Recommend(RecommendationInput{Summary: summary, Thresholds: testRecommendationThresholds()})
	if len(got.Recommendations) != 1 {
		t.Fatalf("Recommendations = %d, want 1: %+v", len(got.Recommendations), got.Recommendations)
	}
	rec := got.Recommendations[0]
	if rec.Metric != "time_to_first_ack" {
		t.Fatalf("Metric = %q, want time_to_first_ack", rec.Metric)
	}
	if rec.Recommendation != RecommendationReduceFanOut {
		t.Fatalf("Recommendation = %q, want %q", rec.Recommendation, RecommendationReduceFanOut)
	}
	if rec.Pending != 2 {
		t.Fatalf("Pending = %d, want 2", rec.Pending)
	}
	if !containsRecommendationReason(rec.ReasonCodes, ReasonSLOAckPending) {
		t.Fatalf("ReasonCodes = %v, want %s", rec.ReasonCodes, ReasonSLOAckPending)
	}
	if len(got.LogRows) != 1 {
		t.Fatalf("LogRows = %d, want 1", len(got.LogRows))
	}
	assertLogRowCoversRecommendation(t, got.LogRows[0], rec)
}

func TestRecommend_StableOrderingAndReasonCodes(t *testing.T) {
	t.Parallel()
	summary := Summary{
		GeneratedAt: clock(),
		TimeToFirstAck: Distribution{
			Count:      2,
			P95Seconds: 90,
			Pending:    3,
		},
		ReadyToClaim: Distribution{
			Count:      2,
			P95Seconds: 80,
		},
		ClaimToCloseout: Distribution{
			Count:      2,
			P95Seconds: 100,
		},
		ReservationContention: Distribution{
			Count:      2,
			P95Seconds: 70,
		},
		StaleInProgress: Distribution{
			Count:      2,
			P95Seconds: 110,
		},
	}

	got := Recommend(RecommendationInput{Summary: summary, Thresholds: testRecommendationThresholds()})
	wantOrder := []string{
		"time_to_first_ack",
		"ready_to_claim",
		"claim_to_closeout",
		"reservation_contention",
		"stale_in_progress",
	}
	if len(got.Recommendations) != len(wantOrder) {
		t.Fatalf("Recommendations = %d, want %d: %+v", len(got.Recommendations), len(wantOrder), got.Recommendations)
	}
	for i, want := range wantOrder {
		rec := got.Recommendations[i]
		if rec.Metric != want {
			t.Fatalf("Recommendations[%d].Metric = %q, want %q", i, rec.Metric, want)
		}
		if len(rec.ReasonCodes) == 0 {
			t.Fatalf("Recommendations[%d] has no reason codes: %+v", i, rec)
		}
		assertLogRowCoversRecommendation(t, got.LogRows[i], rec)
	}
	first := got.Recommendations[0]
	if !containsRecommendationReason(first.ReasonCodes, ReasonSLOAckLatencyHigh) ||
		!containsRecommendationReason(first.ReasonCodes, ReasonSLOAckPending) {
		t.Fatalf("time_to_first_ack reasons = %v, want latency and pending reasons", first.ReasonCodes)
	}
}

func TestRecommend_MissingSourcesWarnAndDoNotReportHealthyZeroData(t *testing.T) {
	t.Parallel()
	summary := Summary{
		GeneratedAt:    clock(),
		TimeToFirstAck: Distribution{Missing: true},
		ReadyToClaim:   Distribution{Missing: true},
		Warnings:       []string{"agentmail unavailable: source not loaded"},
	}

	got := Recommend(RecommendationInput{Summary: summary, Thresholds: testRecommendationThresholds()})
	if got.Healthy {
		t.Fatal("Healthy = true, want false when sources are missing")
	}
	if len(got.Recommendations) != 2 {
		t.Fatalf("Recommendations = %d, want one per missing metric: %+v", len(got.Recommendations), got.Recommendations)
	}
	for _, rec := range got.Recommendations {
		if rec.Recommendation != RecommendationRefreshSource {
			t.Fatalf("Recommendation = %q, want %q for missing source", rec.Recommendation, RecommendationRefreshSource)
		}
		if rec.Severity != RecommendationSeverityWatch {
			t.Fatalf("Severity = %q, want %q", rec.Severity, RecommendationSeverityWatch)
		}
		if !containsRecommendationReason(rec.ReasonCodes, ReasonSLOMissingSource) {
			t.Fatalf("ReasonCodes = %v, want %s", rec.ReasonCodes, ReasonSLOMissingSource)
		}
	}
	if len(got.Warnings) == 0 {
		t.Fatal("Warnings empty; want missing-source warnings")
	}
	if strings.Contains(strings.Join(got.Warnings, "\n"), "all_slo_metrics_within_thresholds") {
		t.Fatalf("Warnings look like healthy evidence: %v", got.Warnings)
	}
}

func TestBuildSchedulingRecommendationBundleIsRobotAndProofFriendlyJSON(t *testing.T) {
	t.Parallel()
	summary := Summary{
		GeneratedAt:  clock(),
		ReadyToClaim: Distribution{Count: 1, P95Seconds: 90},
	}

	bundle := BuildSchedulingRecommendationBundle(summary, testRecommendationThresholds())
	if bundle.SchemaVersion != RecommendationSchemaVersion {
		t.Fatalf("SchemaVersion = %q, want %q", bundle.SchemaVersion, RecommendationSchemaVersion)
	}
	if !bundle.GeneratedAt.Equal(clock()) {
		t.Fatalf("GeneratedAt = %s, want %s", bundle.GeneratedAt, clock())
	}
	if len(bundle.Recommendations.LogRows) != 1 {
		t.Fatalf("LogRows = %d, want 1", len(bundle.Recommendations.LogRows))
	}
	body, err := json.Marshal(bundle)
	if err != nil {
		t.Fatalf("marshal bundle: %v", err)
	}
	for _, want := range []string{
		`"schema_version"`,
		`"slo"`,
		`"recommendations"`,
		`"log_rows"`,
		`"metric"`,
		`"p95_seconds"`,
		`"pending"`,
		`"threshold"`,
		`"recommendation"`,
		`"confidence"`,
		`"reason_codes"`,
	} {
		if !strings.Contains(string(body), want) {
			t.Fatalf("bundle JSON missing %s: %s", want, body)
		}
	}
}

func testRecommendationThresholds() RecommendationThresholds {
	return RecommendationThresholds{
		TimeToFirstAckP95Seconds:        60,
		PendingAckCount:                 0,
		ReadyToClaimP95Seconds:          60,
		ClaimToCloseoutP95Seconds:       60,
		ReservationContentionP95Seconds: 60,
		StaleInProgressP95Seconds:       60,
	}
}

func containsRecommendationReason(codes []RecommendationReasonCode, want RecommendationReasonCode) bool {
	for _, code := range codes {
		if code == want {
			return true
		}
	}
	return false
}

func assertLogRowCoversRecommendation(t *testing.T, row RecommendationLogRow, rec Recommendation) {
	t.Helper()
	if row.Metric != rec.Metric {
		t.Fatalf("log metric = %q, want %q", row.Metric, rec.Metric)
	}
	if row.P95Seconds != rec.P95Seconds {
		t.Fatalf("log p95_seconds = %v, want %v", row.P95Seconds, rec.P95Seconds)
	}
	if row.Pending != rec.Pending {
		t.Fatalf("log pending = %d, want %d", row.Pending, rec.Pending)
	}
	if row.Threshold != rec.Threshold {
		t.Fatalf("log threshold = %v, want %v", row.Threshold, rec.Threshold)
	}
	if row.Recommendation != rec.Recommendation {
		t.Fatalf("log recommendation = %q, want %q", row.Recommendation, rec.Recommendation)
	}
	if row.Confidence != rec.Confidence {
		t.Fatalf("log confidence = %v, want %v", row.Confidence, rec.Confidence)
	}
	if len(row.ReasonCodes) != len(rec.ReasonCodes) {
		t.Fatalf("log reason codes = %v, want %v", row.ReasonCodes, rec.ReasonCodes)
	}
	for i := range row.ReasonCodes {
		if row.ReasonCodes[i] != rec.ReasonCodes[i] {
			t.Fatalf("log reason codes = %v, want %v", row.ReasonCodes, rec.ReasonCodes)
		}
	}
}
