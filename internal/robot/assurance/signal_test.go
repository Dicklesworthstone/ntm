package assurance

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"
)

func TestSignalJSONUsesStableNamesAndZeroDefaults(t *testing.T) {
	got := mustMarshalSignal(t, Signal{Type: SignalEvidenceFreshness})
	want := `{"type":"evidence_freshness","status":"unknown"}`
	if got != want {
		t.Fatalf("minimal Signal JSON = %s, want %s", got, want)
	}

	if strings.Contains(got, "observed_at") {
		t.Fatalf("minimal Signal JSON should omit observed_at: %s", got)
	}
}

func TestSignalJSONIncludesObservedAtWhenProvided(t *testing.T) {
	observedAt := time.Date(2026, 5, 8, 7, 0, 0, 0, time.UTC)
	got := mustMarshalSignal(t, Signal{
		Type:       SignalProviderDegraded,
		Status:     SignalStatusDegraded,
		Reasons:    []ReasonCode{ReasonProviderTimeout},
		Evidence:   "agent-mail fetch timed out",
		ObservedAt: &observedAt,
	})

	want := `{"type":"provider_degraded","status":"degraded","reasons":["provider.timeout"],"evidence":"agent-mail fetch timed out","observed_at":"2026-05-08T07:00:00Z"}`
	if got != want {
		t.Fatalf("Signal JSON = %s, want %s", got, want)
	}
}

func TestSignalEffectiveStatusAndConfidence(t *testing.T) {
	tests := []struct {
		name           string
		signal         Signal
		wantStatus     SignalStatus
		wantConfidence float64
	}{
		{
			name:           "zero value",
			signal:         Signal{},
			wantStatus:     SignalStatusUnknown,
			wantConfidence: 0,
		},
		{
			name: "healthy",
			signal: Signal{
				Status:     SignalStatusHealthy,
				Confidence: 0.75,
			},
			wantStatus:     SignalStatusHealthy,
			wantConfidence: 0.75,
		},
		{
			name: "negative confidence clamps to zero",
			signal: Signal{
				Status:     SignalStatusDegraded,
				Confidence: -0.1,
			},
			wantStatus:     SignalStatusDegraded,
			wantConfidence: 0,
		},
		{
			name: "overfull confidence clamps to one",
			signal: Signal{
				Status:     SignalStatusDegraded,
				Confidence: 1.25,
			},
			wantStatus:     SignalStatusDegraded,
			wantConfidence: 1,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := tt.signal.EffectiveStatus(); got != tt.wantStatus {
				t.Fatalf("EffectiveStatus() = %q, want %q", got, tt.wantStatus)
			}
			if got := tt.signal.EffectiveConfidence(); got != tt.wantConfidence {
				t.Fatalf("EffectiveConfidence() = %v, want %v", got, tt.wantConfidence)
			}
		})
	}
}

func TestSignalTypeRegistryIsStableAndCopied(t *testing.T) {
	want := []SignalType{
		SignalClassifierConfidence,
		SignalEvidenceFreshness,
		SignalCoordinationRisk,
		SignalQuiescenceCandidate,
		SignalProviderDegraded,
		SignalReservationContention,
		SignalCloseoutIntegrity,
	}

	got := AllSignalTypes()
	if len(got) != len(want) {
		t.Fatalf("AllSignalTypes length = %d, want %d", len(got), len(want))
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("AllSignalTypes()[%d] = %q, want %q", i, got[i], want[i])
		}
		if !KnownSignalType(got[i]) {
			t.Fatalf("KnownSignalType(%q) = false", got[i])
		}
	}
	if KnownSignalType("free_form_signal") {
		t.Fatalf("KnownSignalType accepted an unregistered signal type")
	}

	got[0] = "mutated"
	if AllSignalTypes()[0] != SignalClassifierConfidence {
		t.Fatalf("AllSignalTypes did not return a defensive copy")
	}
}

func TestReasonCodeRegistryIsStableUniqueAndCopied(t *testing.T) {
	codes := AllReasonCodes()
	if len(codes) != 30 {
		t.Fatalf("AllReasonCodes length = %d, want 30", len(codes))
	}
	if codes[0] != ReasonClassifierLowSampleSize {
		t.Fatalf("first reason code = %q, want %q", codes[0], ReasonClassifierLowSampleSize)
	}
	if codes[len(codes)-1] != ReasonCloseoutNoBeadReference {
		t.Fatalf("last reason code = %q, want %q", codes[len(codes)-1], ReasonCloseoutNoBeadReference)
	}

	seen := make(map[ReasonCode]struct{}, len(codes))
	for _, code := range codes {
		if !KnownReasonCode(code) {
			t.Fatalf("KnownReasonCode(%q) = false", code)
		}
		if _, ok := seen[code]; ok {
			t.Fatalf("duplicate reason code %q", code)
		}
		seen[code] = struct{}{}
		if !strings.Contains(string(code), ".") {
			t.Fatalf("reason code %q is not namespaced", code)
		}
	}
	if KnownReasonCode("free_form_reason") {
		t.Fatalf("KnownReasonCode accepted an unregistered reason code")
	}

	codes[0] = "mutated"
	if AllReasonCodes()[0] != ReasonClassifierLowSampleSize {
		t.Fatalf("AllReasonCodes did not return a defensive copy")
	}
}

func mustMarshalSignal(t *testing.T, signal Signal) string {
	t.Helper()

	b, err := json.Marshal(signal)
	if err != nil {
		t.Fatalf("json.Marshal(Signal) error = %v", err)
	}
	return string(b)
}

func ExampleSignal_healthy() {
	b, _ := json.Marshal(Signal{
		Type:       SignalCloseoutIntegrity,
		Status:     SignalStatusHealthy,
		Confidence: 1,
		Evidence:   "bd-123 verification complete",
	})
	fmt.Println(string(b))
	// Output:
	// {"type":"closeout_integrity","status":"healthy","confidence":1,"evidence":"bd-123 verification complete"}
}

func ExampleSignal_degraded() {
	b, _ := json.Marshal(Signal{
		Type:     SignalProviderDegraded,
		Status:   SignalStatusDegraded,
		Reasons:  []ReasonCode{ReasonProviderTimeout},
		Evidence: "agent-mail fetch timed out",
	})
	fmt.Println(string(b))
	// Output:
	// {"type":"provider_degraded","status":"degraded","reasons":["provider.timeout"],"evidence":"agent-mail fetch timed out"}
}

func ExampleSignal_unknown() {
	b, _ := json.Marshal(Signal{Type: SignalQuiescenceCandidate})
	fmt.Println(string(b))
	// Output:
	// {"type":"quiescence_candidate","status":"unknown"}
}
