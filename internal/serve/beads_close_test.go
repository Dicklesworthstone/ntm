package serve

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"strings"
	"testing"
)

func TestConfirmedClosedBeadRequiresMatchingAuthoritativeRecord(t *testing.T) {
	for _, tc := range []struct {
		name, output string
		closed       bool
	}{
		{"object", `{"id":"bd-task.1","status":"closed"}`, true},
		{"singleton", `[{"id":"bd-task.1","status":"closed"}]`, true},
		{"normalized status", `{"id":"bd-task.1","status":" CLOSED "}`, true},
		{"wrong id", `{"id":"bd-other","status":"closed"}`, false},
		{"no id", `{"status":"closed"}`, false},
		{"no status", `{"id":"bd-task.1"}`, false},
		{"open", `{"id":"bd-task.1","status":"open"}`, false},
		{"null", `null`, false},
		{"empty list", `[]`, false},
		{"empty object", `{}`, false},
		{"multiple", `[{"id":"bd-task.1","status":"closed"},{"id":"bd-other","status":"closed"}]`, false},
		{"envelope", `{"success":true}`, false},
		{"text", `closed`, false},
		{"trailing value", `{"id":"bd-task.1","status":"closed"} {}`, false},
		{"trailing garbage", `{"id":"bd-task.1","status":"closed"} !`, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, closed := confirmedClosedBead(tc.output, "bd-task.1")
			if closed != tc.closed {
				t.Fatalf("closed = %t, want %t for %s", closed, tc.closed, tc.output)
			}
		})
	}
	bead, ok := confirmedClosedBead(`{"id":"bd-task.1","status":"closed","sequence":9007199254740993}`, "bd-task.1")
	if !ok || bead["sequence"] != json.Number("9007199254740993") {
		t.Fatalf("metadata rounded: %#v", bead)
	}
}

func TestRunBeadCloseReconcilesOnlyConfirmedOutcomes(t *testing.T) {
	const open = `{"id":"bd-task","status":"open"}`
	const closed = `{"id":"bd-task","status":"closed"}`
	commandErr := errors.New("command failed after write")
	readErr := errors.New("tracker unavailable")
	type response struct {
		output string
		err    error
	}
	for _, tc := range []struct {
		name           string
		responses      []response
		commands       []string
		wantErr        error
		alreadyClosed  bool
		reconciled     bool
		outcomeUnknown bool
	}{
		{"already closed", []response{{closed, nil}}, []string{"show"}, nil, true, false, false},
		{"direct close", []response{{open, nil}, {closed, nil}}, []string{"show", "close"}, nil, false, false, false},
		{"read unavailable then close", []response{{"", readErr}, {closed, nil}}, []string{"show", "close"}, nil, false, false, false},
		{"empty close then confirmed", []response{{open, nil}, {"[]", nil}, {closed, nil}}, []string{"show", "close", "show"}, nil, false, true, false},
		{"error after commit", []response{{open, nil}, {"", commandErr}, {closed, nil}}, []string{"show", "close", "show"}, nil, false, true, false},
		{"empty close still open", []response{{open, nil}, {"[]", nil}, {open, nil}}, []string{"show", "close", "show"}, errBeadCloseUnconfirmed, false, false, true},
		{"wrong bead in close", []response{{open, nil}, {`{"id":"bd-other","status":"closed"}`, nil}, {open, nil}}, []string{"show", "close", "show"}, errBeadCloseUnconfirmed, false, false, true},
		{"unavailable reconciliation", []response{{open, nil}, {"ok", nil}, {"", readErr}}, []string{"show", "close", "show"}, errBeadCloseUnconfirmed, false, false, true},
		{"failed close still open", []response{{open, nil}, {"", commandErr}, {open, nil}}, []string{"show", "close", "show"}, commandErr, false, false, true},
		{"all unavailable", []response{{"", readErr}, {"", commandErr}, {"", readErr}}, []string{"show", "close", "show"}, commandErr, false, false, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var commands []string
			ctx := context.WithValue(context.Background(), struct{}{}, tc.name)
			result, err := runBeadClose(ctx, "/workspace/original", "bd-task", func(got context.Context, dir string, args ...string) (string, error) {
				if got != ctx || dir != "/workspace/original" || len(args) != 3 || args[1] != "bd-task" || args[2] != "--json" {
					t.Fatalf("execution identity changed: %v %q %v", got, dir, args)
				}
				index := len(commands)
				commands = append(commands, args[0])
				if index >= len(tc.responses) {
					t.Fatalf("unexpected command: %v", commands)
				}
				return tc.responses[index].output, tc.responses[index].err
			})
			if !errors.Is(err, tc.wantErr) || !reflect.DeepEqual(commands, tc.commands) {
				t.Fatalf("commands=%v err=%v, want commands=%v err=%v", commands, err, tc.commands, tc.wantErr)
			}
			if result["bead_id"] != "bd-task" || result["project_dir"] != "/workspace/original" {
				t.Fatalf("missing recovery identity: %#v", result)
			}
			if (result["closed"] == true) != (tc.wantErr == nil) ||
				(result["already_closed"] == true) != tc.alreadyClosed ||
				(result["reconciled"] == true) != tc.reconciled ||
				(result["outcome_unknown"] == true) != tc.outcomeUnknown {
				t.Fatalf("dishonest outcome: %#v", result)
			}
			if err != nil && strings.Contains(err.Error(), "%!") {
				t.Fatalf("malformed error: %v", err)
			}
		})
	}
}

func TestRunBeadCloseCancellationNeverStartsAnotherCommand(t *testing.T) {
	for _, stage := range []string{"before", "show", "close", "confirmed-close", "reconcile"} {
		t.Run(stage, func(t *testing.T) {
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			if stage == "before" {
				cancel()
			}
			calls := 0
			result, err := runBeadClose(ctx, "/project", "bd-task", func(got context.Context, _ string, args ...string) (string, error) {
				if got.Err() != nil {
					t.Fatal("new command started after cancellation")
				}
				calls++
				if (calls == 1 && stage == "show") ||
					(calls == 2 && (stage == "close" || stage == "confirmed-close")) ||
					(calls == 3 && stage == "reconcile") {
					cancel()
					if stage == "confirmed-close" {
						return `{"id":"bd-task","status":"closed"}`, nil
					}
					return "", context.Canceled
				}
				return `{"id":"bd-task","status":"open"}`, nil
			})
			wantCalls := map[string]int{"before": 0, "show": 1, "close": 2, "confirmed-close": 2, "reconcile": 3}[stage]
			if !errors.Is(err, context.Canceled) || calls != wantCalls {
				t.Fatalf("calls=%d, err=%v, want %d and cancellation", calls, err, wantCalls)
			}
			if (result["closed"] == true) != (stage == "confirmed-close") {
				t.Fatalf("lost or invented cancellation evidence: %#v", result)
			}
		})
	}
}
