package reservationsim

import (
	"encoding/json"
	"strings"
	"testing"
	"time"
)

func TestAdviseReservations_StaleBroadAndInactiveHolder(t *testing.T) {
	t.Parallel()
	now := anchor().Add(4 * time.Hour)
	report := AdviseReservations([]ReservationRiskInput{
		{
			ID:          7,
			PathPattern: "**",
			AgentName:   "BlueLake",
			Exclusive:   true,
			Reason:      "bd-stale",
			CreatedAt:   anchor(),
			ExpiresAt:   now.Add(10 * time.Minute),
		},
	}, ReservationAdvisorOptions{
		Now: now,
		HolderLastActive: map[string]time.Time{
			"BlueLake": anchor().Add(30 * time.Minute),
		},
		StaleInProgressByReason: map[string]bool{"bd-stale": true},
	})

	if !report.AgentMailAvailable {
		t.Fatal("agent mail should be available by default")
	}
	if len(report.Recommendations) != 1 {
		t.Fatalf("recommendations = %d, want 1", len(report.Recommendations))
	}
	rec := report.Recommendations[0]
	if !reservationActionOK(rec.Action, ReservationActionMessageHolder, ReservationActionNarrow) {
		t.Fatalf("Action = %q, want holder message or narrow recommendation", rec.Action)
	}
	requireReservationText(t, rec.Risk, "critical")
	for _, want := range []string{"broad_path_pattern", "inactive_holder", "stale_in_progress_context", "stale_reservation", "short_ttl"} {
		if !containsReservationString(rec.ReasonCodes, want) {
			t.Fatalf("ReasonCodes missing %q: %#v", want, rec.ReasonCodes)
		}
	}
	if len(report.LogRows) != 1 {
		t.Fatalf("LogRows = %d, want 1", len(report.LogRows))
	}
	log := report.LogRows[0]
	requireReservationText(t, log.PathPattern, "**")
	requireReservationText(t, log.Holder, "BlueLake")
	requireReservationText(t, log.WorktreePath, "")
	if log.ReservationID != 7 {
		t.Fatalf("ReservationID = %d, want 7", log.ReservationID)
	}
}

func TestAdviseReservations_OverlapsSortByRisk(t *testing.T) {
	t.Parallel()
	now := anchor().Add(30 * time.Minute)
	report := AdviseReservations([]ReservationRiskInput{
		{
			ID:          1,
			PathPattern: "internal/auth/**",
			AgentName:   "BlueLake",
			Exclusive:   true,
			CreatedAt:   anchor(),
			ExpiresAt:   now.Add(time.Hour),
		},
		{
			ID:          2,
			PathPattern: "internal/auth/session.go",
			AgentName:   "GreenHill",
			Exclusive:   true,
			CreatedAt:   now.Add(-5 * time.Minute),
			ExpiresAt:   now.Add(time.Hour),
		},
	}, ReservationAdvisorOptions{Now: now})

	if len(report.Recommendations) != 2 {
		t.Fatalf("recommendations = %d, want 2", len(report.Recommendations))
	}
	for _, rec := range report.Recommendations {
		if !containsReservationString(rec.ReasonCodes, "overlapping_reservation") {
			t.Fatalf("expected overlap reason in %+v", rec)
		}
	}
	if report.Recommendations[0].RiskScore < report.Recommendations[1].RiskScore {
		t.Fatalf("recommendations not sorted by risk: %+v", report.Recommendations)
	}
}

func TestAdviseReservations_AgentMailUnavailableIsProofModeWarning(t *testing.T) {
	t.Parallel()
	report := AdviseReservations([]ReservationRiskInput{
		{ID: 1, PathPattern: "**", AgentName: "BlueLake", Exclusive: true},
	}, ReservationAdvisorOptions{
		Now:                  anchor(),
		AgentMailUnavailable: true,
		AgentMailError:       "connection refused",
	})

	if report.AgentMailAvailable {
		t.Fatal("AgentMailAvailable = true, want false")
	}
	if len(report.Recommendations) != 0 {
		t.Fatalf("recommendations = %d, want 0 when source unavailable", len(report.Recommendations))
	}
	if len(report.Warnings) != 1 || !strings.Contains(report.Warnings[0], "connection refused") {
		t.Fatalf("unexpected warnings: %#v", report.Warnings)
	}
}

func reservationActionOK(got string, wants ...string) bool {
	for _, want := range wants {
		if strings.Compare(got, want) == 0 {
			return true
		}
	}
	return false
}

func containsReservationString(values []string, want string) bool {
	for _, value := range values {
		if strings.Compare(value, want) == 0 {
			return true
		}
	}
	return false
}

func requireReservationText(t *testing.T, got, want string) {
	t.Helper()
	if strings.Compare(got, want) != 0 {
		t.Fatalf("got %q, want %q", got, want)
	}
}

func TestAdviseReservations_IntersectionEvidence(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name, first, second             string
		firstExclusive, secondExclusive bool
		sameHolder, wantOverlap         bool
	}{
		{"crossing globs", "src/*/main.go", "src/service/*.go", true, true, false, true},
		{"shared versus exclusive", "src/*/main.go", "src/service/*.go", false, true, false, true},
		{"both shared", "src/*/main.go", "src/service/*.go", false, false, false, false},
		{"same holder", "src/*/main.go", "src/service/*.go", true, true, true, false},
		{"basename glob", "*.go", "src/nested/main.go", true, true, false, true},
		{"literal subtree", "src", "src/nested/main.go", true, true, false, true},
		{"invalid is unverified", "[broken", "src/main.go", true, true, false, true},
		{"disjoint", "src/*.go", "docs/*.md", true, true, false, false},
		{"whitespace is data", " src/*.go", "src/main.go", true, true, false, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			now := anchor()
			rows := []ReservationRiskInput{
				{ID: 1, PathPattern: tc.first, AgentName: "BlueLake", Exclusive: tc.firstExclusive, CreatedAt: now, ExpiresAt: now.Add(time.Hour)},
				{ID: 2, PathPattern: tc.second, AgentName: "GreenHill", Exclusive: tc.secondExclusive, CreatedAt: now, ExpiresAt: now.Add(time.Hour)},
			}
			if tc.sameHolder {
				rows[1].AgentName = rows[0].AgentName
			}
			report := AdviseReservations(rows, ReservationAdvisorOptions{Now: now})
			// Exercise the report shape consumed by ntm locks advise --json,
			// not just the internal matcher. No file needs to exist on disk.
			data, err := json.Marshal(report)
			if err != nil {
				t.Fatal(err)
			}
			var decoded ReservationAdvisorReport
			if err := json.Unmarshal(data, &decoded); err != nil {
				t.Fatal(err)
			}
			if decoded.SchemaVersion != ReservationAdvisorSchemaVersion || decoded.Mode != "proof" || len(decoded.Recommendations) != 2 || len(decoded.LogRows) != 2 {
				t.Fatalf("unexpected report: %s", data)
			}
			for _, rec := range decoded.Recommendations {
				gotOverlap := containsReservationString(rec.ReasonCodes, "overlapping_reservation")
				if gotOverlap != tc.wantOverlap {
					t.Fatalf("overlap = %v, want %v: %s", gotOverlap, tc.wantOverlap, data)
				}
				if tc.wantOverlap && (!containsReservationString(rec.Evidence, "overlapping_reservations=1") || rec.Action != ReservationActionMessageHolder) {
					t.Fatalf("missing holder coordination evidence: %+v", rec)
				}
				row := rows[rec.ReservationID-1]
				if rec.PathPattern != row.PathPattern {
					t.Fatalf("pattern mutated: %q became %q", row.PathPattern, rec.PathPattern)
				}
				alone := AdviseReservations([]ReservationRiskInput{row}, ReservationAdvisorOptions{Now: now}).Recommendations[0]
				wantScore := alone.RiskScore
				if tc.wantOverlap {
					wantScore += 25
				}
				if rec.RiskScore != wantScore {
					t.Fatalf("risk score = %d, want %d", rec.RiskScore, wantScore)
				}
			}
		})
	}
}
