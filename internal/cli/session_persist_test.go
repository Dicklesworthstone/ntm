package cli

import (
	"errors"
	"io"
	"os"
	"testing"
)

// TestRunSessionsShow_LoadFailureRoutesThroughJSONEnvelope covers bd-1yws7:
// when --json is set, runSessionsShow's session.Load failure path must emit
// a parseable JSON envelope and propagate errJSONFailure so automation
// gating on `$?` no longer treats a missing/corrupt saved-session as
// success. Pre-fix the function returned the raw err, which under --json
// surfaced as a stderr "Error:" line and empty stdin to jq.
func TestRunSessionsShow_LoadFailureRoutesThroughJSONEnvelope(t *testing.T) {
	prevJSON := jsonOutput
	jsonOutput = true
	t.Cleanup(func() { jsonOutput = prevJSON })

	origStdout := os.Stdout
	r, w, pipeErr := os.Pipe()
	if pipeErr != nil {
		t.Fatalf("os.Pipe error = %v", pipeErr)
	}
	os.Stdout = w
	t.Cleanup(func() { os.Stdout = origStdout })

	done := make(chan struct{})
	go func() {
		_, _ = io.Copy(io.Discard, r)
		close(done)
	}()

	// Empty name trips normalizeSavedSessionName inside session.Load,
	// which is the deterministic failure surface for runSessionsShow.
	err := runSessionsShow("")
	_ = w.Close()
	<-done

	if !errors.Is(err, errJSONFailure) {
		t.Fatalf("runSessionsShow returned %v, want errJSONFailure (load failure must route through emitJSONFailureEnvelope under --json)", err)
	}
}

// TestRunSessionsDelete_NotFoundRoutesThroughJSONEnvelope covers bd-1yws7:
// runSessionsDelete previously returned a raw fmt.Errorf for the missing-
// session path, which bypassed --json and forced automation to parse
// stderr text. The fix routes the error through emitJSONFailureEnvelope so
// `ntm sessions delete --json | jq` sees a parseable failure on stdout and
// the process exits non-zero via errJSONFailure.
func TestRunSessionsDelete_NotFoundRoutesThroughJSONEnvelope(t *testing.T) {
	prevJSON := jsonOutput
	jsonOutput = true
	t.Cleanup(func() { jsonOutput = prevJSON })

	origStdout := os.Stdout
	r, w, pipeErr := os.Pipe()
	if pipeErr != nil {
		t.Fatalf("os.Pipe error = %v", pipeErr)
	}
	os.Stdout = w
	t.Cleanup(func() { os.Stdout = origStdout })

	done := make(chan struct{})
	go func() {
		_, _ = io.Copy(io.Discard, r)
		close(done)
	}()

	err := runSessionsDelete("ntm-bd-1yws7-nonexistent-12345-do-not-exist", false)
	_ = w.Close()
	<-done

	if !errors.Is(err, errJSONFailure) {
		t.Fatalf("runSessionsDelete returned %v, want errJSONFailure (not-found path must emit JSON envelope under --json)", err)
	}
}
