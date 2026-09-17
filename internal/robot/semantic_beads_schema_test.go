package robot

import (
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"strings"
	"testing"
	"time"
)

func TestSemanticBeadListSchemas(t *testing.T) {
	now := time.Date(2026, 9, 17, 12, 0, 0, 123456789, time.UTC)
	cutoff := now.Add(-30 * time.Minute)
	rows := []brListIssue{
		{Status: "in_progress", UpdatedAt: cutoff.Format(time.RFC3339Nano)},
		{Status: "closed", ClosedAt: now.Add(-time.Minute).Format(time.RFC3339Nano)},
		{Status: "open", UpdatedAt: cutoff.Add(-time.Nanosecond).Format(time.RFC3339Nano)},
		// A bead updated and closed in the window still counts only once.
		{Status: "closed", UpdatedAt: now.Format(time.RFC3339Nano), ClosedAt: now.Format(time.RFC3339Nano)},
	}
	array, err := json.Marshal(rows)
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name string
		raw  string
	}{
		{"native array", string(array)},
		{"wrapper envelope", `{"issues":` + string(array) + `,"total":4}`},
		{"whitespace", "\n\t " + string(array) + "\r\n"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := countClaimsInWindow([]byte(tc.raw), 30*time.Minute, now)
			want := claimActivity{claimsInWindow: 3, anyLabeledBead: true, available: true}
			if got != want {
				t.Fatalf("activity = %+v, want %+v", got, want)
			}
		})
	}
}

func TestSemanticBeadListEmptyIsAvailable(t *testing.T) {
	for _, raw := range []string{`[]`, `{"issues":[]}`, ` { "issues" : [], "total": 0 } `} {
		got := countClaimsInWindow([]byte(raw), time.Minute, time.Now())
		if got != (claimActivity{available: true}) {
			t.Fatalf("%s: got %+v, want an available empty read", raw, got)
		}
	}
}

func TestSemanticBeadListRejectsUnavailableSchemas(t *testing.T) {
	for _, raw := range []string{
		``, `not json`, `null`, `{}`, `{"issues":null}`, `{"data":[]}`,
		`{"issues":{}}`, `{"issues":"[]"}`, `true`, `42`, `"[]"`,
		`[null]`, `[{}]`, `[1]`, `[{"status":42}]`, `[{"status":""}]`,
		`[{"status":"in_progress","updated_at":{}}]`,
		`[{"status":"closed"},null]`, `[] {}`, `{"issues":[]} garbage`,
	} {
		t.Run(raw, func(t *testing.T) {
			got := countClaimsInWindow([]byte(raw), time.Minute, time.Now())
			if got != (claimActivity{}) {
				t.Fatalf("invalid schema %q became evidence: %+v", raw, got)
			}
		})
	}
}

func TestSemanticBeadListTimestampCompatibility(t *testing.T) {
	now := time.Date(2026, 9, 17, 12, 0, 0, 0, time.UTC)
	raw := `[
		{"status":"in_progress","updated_at":"2026-09-17T07:30:00-04:00"},
		{"status":"closed","closed_at":"invalid","updated_at":"2026-09-17T11:59:00.123456789Z"},
		{"status":"closed","closed_at":"2026-09-17T11:50:00Z","updated_at":null},
		{"status":"open","updated_at":"2026-09-17T11:29:59.999999999Z"}
	]`
	got := countClaimsInWindow([]byte(raw), 30*time.Minute, now)
	if !got.available || !got.anyLabeledBead || got.claimsInWindow != 3 {
		t.Fatalf("timestamp handling = %+v, want available/attributed/3", got)
	}
}

// Keep these tests on the real exec path: an exit-zero process returning a
// broken schema must not undo the parser's unavailable result at the caller.
func TestSemanticBeadCollectorPreservesAvailability(t *testing.T) {
	dir := installSemanticSchemaCommands(t)
	now := time.Date(2026, 9, 17, 12, 0, 0, 0, time.UTC)
	for _, tc := range []struct {
		name      string
		raw       string
		available bool
		count     int
	}{
		{"array", `[{"status":"in_progress","updated_at":"2026-09-17T11:59:00Z"}]`, true, 1},
		{"envelope", `{"issues":[{"status":"closed","closed_at":"2026-09-17T11:59:00Z"}]}`, true, 1},
		{"empty", `[]`, true, 0},
		{"null", `null`, false, 0},
		{"wrong shape", `{"error":"workspace unavailable"}`, false, 0},
		{"malformed", `{`, false, 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv("NTM_TEST_BR_JSON", tc.raw)
			progress, available := paneSemanticProgressWithAvailability(
				PaneAddr{Session: "test", Window: 2, Pane: 3}, dir, time.Minute, false, now,
			)
			if available != tc.available || progress.ClaimsInWindow != tc.count {
				t.Fatalf("available=%v progress=%+v, want %v/%d claims", available, progress, tc.available, tc.count)
			}
			args, err := os.ReadFile(filepath.Join(dir, "br-args"))
			if err != nil {
				t.Fatal(err)
			}
			want := []string{"list", "--label", PaneBeadLabel("test", 2, 3), "--include-closed", "--json"}
			if got := strings.Split(strings.TrimSpace(string(args)), "\n"); !reflect.DeepEqual(got, want) {
				t.Fatalf("br args=%q, want %q", got, want)
			}
		})
	}
}

func installSemanticSchemaCommands(t *testing.T) string {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("test command fixtures require /bin/sh")
	}
	dir := t.TempDir()
	for name, script := range map[string]string{
		"git": "#!/bin/sh\nexit 0\n",
		"br":  "#!/bin/sh\nprintf '%s\\n' \"$@\" > \"$NTM_TEST_BR_ARGS\"\nprintf '%s\\n' \"$NTM_TEST_BR_JSON\"\n",
	} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(script), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
	t.Setenv("NTM_TEST_BR_ARGS", filepath.Join(dir, "br-args"))
	return dir
}
