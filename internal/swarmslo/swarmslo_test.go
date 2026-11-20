package swarmslo

import (
	"encoding/json"
	"strings"
	"testing"
	"time"
)

func TestSummaryAndBundleJSONContract(t *testing.T) {
	t.Parallel()

	now := time.Date(2026, 11, 20, 15, 0, 0, 0, time.UTC)
	summary := Summary{
		GeneratedAt: now,
		TimeToFirstAck: Distribution{
			Count:       4,
			P50Seconds:  12.5,
			P95Seconds:  45.0,
			MaxSeconds:  60.0,
			MeanSeconds: 20.0,
			Pending:     2,
		},
		ReadyToClaim: Distribution{
			Count:   0,
			Missing: true,
		},
		Warnings: []string{"beads history unavailable"},
	}

	raw, err := json.Marshal(summary)
	if err != nil {
		t.Fatalf("marshal Summary: %v", err)
	}
	jsonStr := string(raw)
	if !strings.Contains(jsonStr, `"pending":2`) {
		t.Errorf("expected pending count in JSON: %s", jsonStr)
	}
	if !strings.Contains(jsonStr, `"missing_source":true`) {
		t.Errorf("expected missing_source flag in JSON: %s", jsonStr)
	}
	// ClaimToCloseout has Pending==0 and Missing==false -> both must be omitted.
	if strings.Contains(jsonStr, `"pending":0`) || strings.Contains(jsonStr, `"missing_source":false`) {
		t.Errorf("zero omitempty fields leaked into JSON: %s", jsonStr)
	}

	var decoded Summary
	if err := json.Unmarshal(raw, &decoded); err != nil {
		t.Fatalf("unmarshal Summary: %v", err)
	}
	if decoded.TimeToFirstAck.Pending != 2 || !decoded.ReadyToClaim.Missing || len(decoded.Warnings) != 1 {
		t.Fatalf("round-trip mismatch: %+v", decoded)
	}

	bundle := SchedulingRecommendationBundle{
		SchemaVersion: RecommendationSchemaVersion,
		GeneratedAt:   now,
		SLO:           summary,
		Recommendations: RecommendationSummary{
			SchemaVersion: RecommendationSchemaVersion,
			GeneratedAt:   now,
			Healthy:       false,
			Recommendations: []Recommendation{
				{
					Metric:         "time_to_first_ack",
					P95Seconds:     45.0,
					Pending:        2,
					Threshold:      30.0,
					Recommendation: RecommendationAddReviewer,
					Confidence:     0.9,
					Severity:       RecommendationSeverityAction,
					ReasonCodes:    []RecommendationReasonCode{ReasonSLOAckLatencyHigh, ReasonSLOAckPending},
				},
			},
			LogRows: []RecommendationLogRow{
				{
					Metric:         "time_to_first_ack",
					P95Seconds:     45.0,
					Pending:        2,
					Threshold:      30.0,
					Recommendation: RecommendationAddReviewer,
					Confidence:     0.9,
					ReasonCodes:    []RecommendationReasonCode{ReasonSLOAckLatencyHigh},
				},
			},
		},
	}

	bundleBytes, err := json.Marshal(bundle)
	if err != nil {
		t.Fatalf("marshal bundle: %v", err)
	}
	var roundTrip SchedulingRecommendationBundle
	if err := json.Unmarshal(bundleBytes, &roundTrip); err != nil {
		t.Fatalf("unmarshal bundle: %v", err)
	}
	if roundTrip.SchemaVersion != "ntm.swarm.slo_recommendations.v1" {
		t.Errorf("SchemaVersion = %q, want ntm.swarm.slo_recommendations.v1", roundTrip.SchemaVersion)
	}
	if len(roundTrip.Recommendations.Recommendations) != 1 || roundTrip.Recommendations.Recommendations[0].Recommendation != RecommendationAddReviewer {
		t.Errorf("unexpected recommendations after round-trip: %+v", roundTrip.Recommendations)
	}
}
