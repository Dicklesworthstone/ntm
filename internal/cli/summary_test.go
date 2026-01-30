package cli

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Dicklesworthstone/ntm/internal/summary"
)

func TestParseSummaryFilename(t *testing.T) {
	session, ts, ok := parseSummaryFilename("my-session-20260128-101112.json")
	if !ok {
		t.Fatalf("expected filename to parse")
	}
	if session != "my-session" {
		t.Fatalf("expected session my-session, got %q", session)
	}
	if ts.IsZero() {
		t.Fatalf("expected timestamp to parse")
	}

	if _, _, ok := parseSummaryFilename("badname.json"); ok {
		t.Fatalf("expected bad filename to fail")
	}
}

func TestParseArchiveFilename(t *testing.T) {
	session, ts, ok := parseArchiveFilename("my_session_2026-01-28.jsonl")
	if !ok {
		t.Fatalf("expected archive filename to parse")
	}
	if session != "my_session" {
		t.Fatalf("expected session my_session, got %q", session)
	}
	if ts.IsZero() {
		t.Fatalf("expected timestamp to parse")
	}

	if _, _, ok := parseArchiveFilename("bad.jsonl"); ok {
		t.Fatalf("expected invalid archive filename to fail")
	}
}

func TestListSummaryFilesSortsByTime(t *testing.T) {
	dir := t.TempDir()
	summaryDir := filepath.Join(dir, ".ntm", "summaries")
	if err := os.MkdirAll(summaryDir, 0755); err != nil {
		t.Fatalf("failed to create summary dir: %v", err)
	}

	files := []string{
		filepath.Join(summaryDir, "alpha-20260128-101112.json"),
		filepath.Join(summaryDir, "alpha-20260129-091011.json"),
		filepath.Join(summaryDir, "beta-20260127-090000.json"),
	}
	for _, path := range files {
		if err := os.WriteFile(path, []byte(`{}`), 0644); err != nil {
			t.Fatalf("failed to write summary file: %v", err)
		}
	}

	list, err := listSummaryFiles(dir)
	if err != nil {
		t.Fatalf("listSummaryFiles: %v", err)
	}
	if len(list) != 3 {
		t.Fatalf("expected 3 summaries, got %d", len(list))
	}

	if list[0].Timestamp.Before(list[1].Timestamp) {
		t.Fatalf("expected summaries sorted descending by timestamp")
	}

	if list[0].Session != "alpha" {
		t.Fatalf("expected latest session alpha, got %q", list[0].Session)
	}
}

func TestResolveSummarySessionName(t *testing.T) {
	now := time.Now()
	files := []summaryFileInfo{
		{Session: "alpha", Timestamp: now},
		{Session: "beta", Timestamp: now},
		{Session: "alphonse", Timestamp: now},
	}

	resolved, ok, err := resolveSummarySessionName("beta", files)
	if err != nil || !ok || resolved != "beta" {
		t.Fatalf("expected exact match beta, got %q (ok=%v, err=%v)", resolved, ok, err)
	}

	resolved, ok, err = resolveSummarySessionName("alph", files)
	if err == nil || ok {
		t.Fatalf("expected ambiguous prefix error, got %q (ok=%v, err=%v)", resolved, ok, err)
	}

	resolved, ok, err = resolveSummarySessionName("alp", []summaryFileInfo{{Session: "alpha", Timestamp: now}})
	if err != nil || !ok || resolved != "alpha" {
		t.Fatalf("expected prefix match alpha, got %q (ok=%v, err=%v)", resolved, ok, err)
	}
}

func TestParseSummaryFormat(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name       string
		input      string
		wantFormat summary.SummaryFormat
		wantJSON   bool
		wantErr    bool
	}{
		{"default empty", "", summary.FormatBrief, false, false},
		{"text", "text", summary.FormatBrief, false, false},
		{"brief", "brief", summary.FormatBrief, false, false},
		{"json", "json", summary.FormatBrief, true, false},
		{"markdown", "markdown", summary.FormatDetailed, false, false},
		{"md", "md", summary.FormatDetailed, false, false},
		{"detailed", "detailed", summary.FormatDetailed, false, false},
		{"handoff", "handoff", summary.FormatHandoff, false, false},
		{"invalid", "xml", "", false, true},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			gotFormat, gotJSON, err := parseSummaryFormat(tc.input)
			if tc.wantErr {
				if err == nil {
					t.Fatalf("expected error for %q, got nil", tc.input)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error for %q: %v", tc.input, err)
			}
			if gotFormat != tc.wantFormat {
				t.Fatalf("parseSummaryFormat(%q) format=%q, want %q", tc.input, gotFormat, tc.wantFormat)
			}
			if gotJSON != tc.wantJSON {
				t.Fatalf("parseSummaryFormat(%q) json=%v, want %v", tc.input, gotJSON, tc.wantJSON)
			}
		})
	}
}

func TestResolveProjectDir_EmptySession(t *testing.T) {
	t.Parallel()
	wd := t.TempDir()
	if got := resolveProjectDir("", wd); got != wd {
		t.Fatalf("resolveProjectDir empty session = %q, want %q", got, wd)
	}
}

func TestUniqueSessions(t *testing.T) {
	t.Parallel()
	now := time.Now()
	files := []summaryFileInfo{
		{Session: "beta", Timestamp: now},
		{Session: "alpha", Timestamp: now},
		{Session: "beta", Timestamp: now},
	}
	got := uniqueSessions(files)
	want := []string{"alpha", "beta"}
	if len(got) != len(want) {
		t.Fatalf("uniqueSessions len=%d, want %d", len(got), len(want))
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("uniqueSessions[%d]=%q, want %q", i, got[i], want[i])
		}
	}
}

func TestLatestSummary(t *testing.T) {
	t.Parallel()
	now := time.Now()
	files := []summaryFileInfo{
		{Session: "alpha", Timestamp: now.Add(2 * time.Hour)},
		{Session: "beta", Timestamp: now.Add(time.Hour)},
	}

	latest, ok := latestSummary(files, "")
	if !ok || latest.Session != "alpha" {
		t.Fatalf("latestSummary empty session = %q (ok=%v), want alpha", latest.Session, ok)
	}

	latest, ok = latestSummary(files, "beta")
	if !ok || latest.Session != "beta" {
		t.Fatalf("latestSummary beta = %q (ok=%v), want beta", latest.Session, ok)
	}
}

func TestLatestSummaryForSession(t *testing.T) {
	t.Parallel()
	now := time.Now()
	files := []summaryFileInfo{
		{Session: "alpha", Timestamp: now.Add(2 * time.Hour)},
		{Session: "beta", Timestamp: now.Add(time.Hour)},
	}

	latest, ok := latestSummaryForSession(files, "beta")
	if !ok || latest.Session != "beta" {
		t.Fatalf("latestSummaryForSession beta = %q (ok=%v), want beta", latest.Session, ok)
	}

	if _, ok := latestSummaryForSession(files, "gamma"); ok {
		t.Fatalf("expected no latest summary for missing session")
	}
}

func TestOutputSummaryFromFile_Text(t *testing.T) {
	sum := summary.SessionSummary{
		Session:         "demo",
		Format:          summary.FormatBrief,
		Accomplishments: []string{"did the thing"},
	}
	data, err := json.Marshal(sum)
	if err != nil {
		t.Fatalf("json marshal: %v", err)
	}

	path := filepath.Join(t.TempDir(), "summary.json")
	if err := os.WriteFile(path, data, 0644); err != nil {
		t.Fatalf("write summary file: %v", err)
	}

	out, runErr := captureStdout(t, func() error {
		return outputSummaryFromFile(path, summary.FormatBrief, false)
	})
	if runErr != nil {
		t.Fatalf("outputSummaryFromFile: %v", runErr)
	}
	if !strings.Contains(out, "Session demo summary") {
		t.Fatalf("expected brief summary output, got %q", out)
	}
}

func TestOutputSummaryFromFile_JSON(t *testing.T) {
	sum := summary.SessionSummary{
		Session:         "demo",
		Format:          summary.FormatBrief,
		Accomplishments: []string{"did the thing"},
	}
	data, err := json.Marshal(sum)
	if err != nil {
		t.Fatalf("json marshal: %v", err)
	}

	path := filepath.Join(t.TempDir(), "summary.json")
	if err := os.WriteFile(path, data, 0644); err != nil {
		t.Fatalf("write summary file: %v", err)
	}

	out, runErr := captureStdout(t, func() error {
		return outputSummaryFromFile(path, summary.FormatBrief, true)
	})
	if runErr != nil {
		t.Fatalf("outputSummaryFromFile: %v", runErr)
	}

	var decoded summary.SessionSummary
	if err := json.Unmarshal([]byte(out), &decoded); err != nil {
		t.Fatalf("parse JSON output: %v", err)
	}
	if decoded.Session != "demo" {
		t.Fatalf("expected session demo, got %q", decoded.Session)
	}
}

func TestOutputSummaryFromFile_InvalidJSON(t *testing.T) {
	path := filepath.Join(t.TempDir(), "summary.json")
	if err := os.WriteFile(path, []byte("{not-json"), 0644); err != nil {
		t.Fatalf("write summary file: %v", err)
	}

	if err := outputSummaryFromFile(path, summary.FormatBrief, false); err == nil {
		t.Fatal("expected error for invalid JSON, got nil")
	}
}
