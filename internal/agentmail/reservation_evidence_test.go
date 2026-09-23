package agentmail

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"testing"
	"time"
)

func TestReservationDecodeFailurePreservesUnverifiedHandles(t *testing.T) {
	for _, tc := range []struct {
		name, raw string
		wantRows  int
	}{
		{"null", `null`, 0},
		{"invalid document", `{"granted":[`, 0},
		{"bad grant", `{"granted":[{"id":7,"path_pattern":"a.go","expires_ts":"not a timestamp"}]}`, 1},
		{"bad conflict", `{"granted":[{"id":7,"path_pattern":"a.go"}],"conflicts":false}`, 1},
		{"partial rows", `{"granted":[{"id":7,"path_pattern":"a.go","exclusive":"wrong"},{"id":8,"path_pattern":"b.go"}]}`, 2},
	} {
		t.Run(tc.name, func(t *testing.T) {
			result, err := decodeReservationReply(json.RawMessage(tc.raw))
			if !errors.Is(err, ErrReservationUnverified) {
				t.Fatalf("bad receipt lacked unverified classification: result=%+v error=%v", result, err)
			}
			if tc.wantRows > 0 {
				if result == nil || len(result.Granted) != tc.wantRows || result.Granted[0].ID != 7 || result.Granted[0].PathPattern != "a.go" {
					t.Fatalf("decoder discarded recoverable handles: result=%+v error=%v", result, err)
				}
			}
			if tc.name == "bad grant" {
				var parseErr *time.ParseError
				if !errors.As(err, &parseErr) {
					t.Fatalf("timestamp cause lost under classification: %v", err)
				}
			}
			if tc.name == "bad conflict" || tc.name == "partial rows" {
				var typeErr *json.UnmarshalTypeError
				if !errors.As(err, &typeErr) {
					t.Fatalf("JSON cause lost under classification: %v", err)
				}
			}
		})
	}
}

func TestReservationDecodeSuccessIsNotUnverified(t *testing.T) {
	for _, raw := range []string{
		`{"granted":[],"conflicts":[]}`,
		`{"granted":[{"id":7,"project_id":4,"agent_name":"new","path_pattern":"a.go","expires_ts":"2099-01-01T00:00:00Z"}]}`,
	} {
		result, err := decodeReservationReply(json.RawMessage(raw))
		if err != nil || result == nil {
			t.Fatalf("well-formed receipt reclassified as failure: %+v, %v", result, err)
		}
	}
}

func TestReservationOwnershipFailureRetainsCancellationAndReceipt(t *testing.T) {
	raw := json.RawMessage(`{"granted":[{"id":7,"project_id":4,"agent_name":"new","path_pattern":"a.go"}]}`)
	result, err := decodeReservationReply(raw)
	if err != nil {
		t.Fatal(err)
	}
	before := append([]FileReservation(nil), result.Granted...)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	// With explicit ownership fields this takes no transport path. It still
	// must distinguish an uncompleted verification from a usable grant receipt.
	err = new(Client).completeReservationGrantOwnership(ctx, FileReservationOptions{}, raw, result)
	if !errors.Is(err, context.Canceled) || !errors.Is(err, ErrReservationUnverified) {
		t.Fatalf("ownership error lost cancellation or uncertainty: %v", err)
	}
	if !reflect.DeepEqual(before, result.Granted) {
		t.Fatalf("failed verification modified receipt: before=%+v after=%+v", before, result.Granted)
	}
}
