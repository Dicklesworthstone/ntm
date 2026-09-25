package serve

import (
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

// installCloseTestBR exercises the HTTP -> production RunBdContext seam using
// an executable protocol fixture, without a real tracker or agent processes.
func installCloseTestBR(t *testing.T, mode string) (string, string) {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("br protocol fixture requires a POSIX shell")
	}
	root := t.TempDir()
	project, bin := filepath.Join(root, "project"), filepath.Join(root, "bin")
	for _, dir := range []string{project, bin, filepath.Join(project, ".beads")} {
		if err := os.MkdirAll(dir, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(project, ".beads", "issues.jsonl"), []byte("{\"id\":\"bd-task\",\"title\":\"test\",\"status\":\"open\",\"priority\":2,\"issue_type\":\"task\"}\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	logPath := filepath.Join(root, "calls")
	t.Setenv("NTM_CLOSE_TEST_LOG", logPath)
	t.Setenv("NTM_CLOSE_TEST_MODE", mode)
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	script := `#!/bin/sh
command=""
for arg in "$@"; do
  case "$arg" in show|close) command="$arg";; esac
done
[ -n "$command" ] || { printf '{}\n'; exit 0; }
printf '%s:%s\n' "$PWD" "$command" >> "$NTM_CLOSE_TEST_LOG"
if [ "$command" = show ]; then
  if [ "$NTM_CLOSE_TEST_MODE" = block ]; then
    while [ ! -f "$NTM_CLOSE_TEST_LOG.release" ]; do sleep 0.02; done
  fi
  status=open
  [ ! -f .close-state ] || status=closed
  printf '{"id":"bd-task","status":"%s"}\n' "$status"
  exit 0
fi
case "$NTM_CLOSE_TEST_MODE" in
  empty) printf '[]\n';;
  error-after-write) printf 'closed\n' > .close-state; printf 'write completed but response failed\n' >&2; exit 1;;
  *) printf 'closed\n' > .close-state; printf '{"id":"bd-task","status":"closed"}\n';;
esac
`
	if err := os.WriteFile(filepath.Join(bin, "br"), []byte(script), 0o700); err != nil {
		t.Fatal(err)
	}
	return project, logPath
}

func TestCloseBeadHTTPRequiresConfirmedOutcome(t *testing.T) {
	for _, tc := range []struct {
		mode   string
		status int
	}{
		{"empty", http.StatusInternalServerError},
		{"error-after-write", http.StatusOK},
		{"normal", http.StatusOK},
	} {
		t.Run(tc.mode, func(t *testing.T) {
			project, _ := installCloseTestBR(t, tc.mode)
			srv := NewHermeticServer("close-http-test")
			defer srv.Stop()
			srv.projectDir = project
			rec := admissionRequest(srv, http.MethodPost, "/api/v1/beads/bd-task/close", "")
			if rec.Code != tc.status {
				t.Fatalf("close = %d %s, want %d", rec.Code, rec.Body.String(), tc.status)
			}
			var body map[string]interface{}
			if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
				t.Fatal(err)
			}
			if tc.mode == "empty" {
				details, _ := body["details"].(map[string]interface{})
				if body["success"] != false || details["outcome_unknown"] != true || details["closed"] == true {
					t.Fatalf("false success or missing recovery evidence: %s", rec.Body.String())
				}
			} else if body["closed"] != true || (tc.mode == "error-after-write" && body["reconciled"] != true) {
				t.Fatalf("missing verified outcome: %s", rec.Body.String())
			}
		})
	}
}

func TestCloseBeadAsyncHTTPCancellationStopsTrackerWork(t *testing.T) {
	project, logPath := installCloseTestBR(t, "block")
	srv := NewHermeticServer("close-cancel-test")
	defer srv.Stop()
	// Release before Stop so a failing assertion cannot strand cleanup.
	defer func() { _ = os.WriteFile(logPath+".release", nil, 0o600) }()
	srv.projectDir = project
	rec := admissionRequest(srv, http.MethodPost, "/api/v1/beads/bd-task/close?async=true", "")
	var accepted struct {
		Job *Job `json:"job"`
	}
	if rec.Code != http.StatusAccepted || json.Unmarshal(rec.Body.Bytes(), &accepted) != nil || accepted.Job == nil {
		t.Fatalf("close acceptance = %d %s", rec.Code, rec.Body.String())
	}
	if rec.Header().Get("Location") != "/api/v1/jobs/"+accepted.Job.ID {
		t.Fatalf("missing polling location: %v", rec.Header())
	}
	deadline := time.Now().Add(5 * time.Second)
	for {
		data, _ := os.ReadFile(logPath)
		if strings.Contains(string(data), ":show") {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("tracker read never started")
		}
		time.Sleep(10 * time.Millisecond)
	}
	rec = admissionRequest(srv, http.MethodDelete, "/api/v1/jobs/"+accepted.Job.ID, "")
	if rec.Code != http.StatusOK {
		t.Fatalf("cancel = %d %s", rec.Code, rec.Body.String())
	}
	deadline = time.Now().Add(5 * time.Second)
	for {
		srv.jobStore.mu.RLock()
		_, owned := srv.jobStore.cancels[accepted.Job.ID]
		srv.jobStore.mu.RUnlock()
		if !owned {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("cancelled tracker worker did not exit")
		}
		time.Sleep(10 * time.Millisecond)
	}
	data, _ := os.ReadFile(logPath)
	if strings.Contains(string(data), ":close") {
		t.Fatalf("cancellation was followed by a mutation: %s", data)
	}
	job := srv.jobStore.Get(accepted.Job.ID)
	if job == nil || job.Status != JobStatusCancelled || job.Error != "cancelled by user" {
		t.Fatalf("worker overwrote cancellation: %+v", job)
	}
}
