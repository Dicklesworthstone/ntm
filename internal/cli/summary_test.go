package cli

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Dicklesworthstone/ntm/internal/agentmail"
	"github.com/Dicklesworthstone/ntm/internal/archive"
	"github.com/Dicklesworthstone/ntm/internal/config"
	"github.com/Dicklesworthstone/ntm/internal/summary"
	"github.com/Dicklesworthstone/ntm/tests/testutil"
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
	for _, tc := range []struct {
		filename string
		session  string
	}{
		{"my_session_2026-01-28.jsonl", "my_session"},
		{"my_session_2026-01-28_101112.123456789-1234.jsonl", "my_session"},
		{"my_session_2025-12-31_2026-01-28.jsonl", "my_session_2025-12-31"},
		{"my_session_2025-12-31_2026-01-28_101112.123456789-1234.jsonl", "my_session_2025-12-31"},
	} {
		t.Run(tc.filename, func(t *testing.T) {
			session, ts, ok := archive.ParseFilename(tc.filename)
			if !ok || session != tc.session || ts.Format("2006-01-02") != "2026-01-28" {
				t.Fatalf("ParseFilename = %q, %v, %v; want %q on 2026-01-28", session, ts, ok, tc.session)
			}
			if strings.Contains(tc.filename, "101112.123456789") && ts.Format("150405.000000000") != "101112.123456789" {
				t.Fatalf("chunk timestamp lost its time component: %v", ts)
			}
		})
	}
	for _, invalid := range []string{"bad.jsonl", "session_2026-13-40.jsonl", "session_2026-01-28_251112.123456789-1234.jsonl", "session_2026-01-28_101112.123456789.jsonl"} {
		if _, _, ok := archive.ParseFilename(invalid); ok {
			t.Fatalf("accepted invalid archive filename %q", invalid)
		}
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
	wd := t.TempDir()
	got, err := resolveProjectDir(t.Context(), "", wd, false)
	if err != nil {
		t.Fatalf("resolveProjectDir empty session error: %v", err)
	}
	if got != wd {
		t.Fatalf("resolveProjectDir empty session = %q, want %q", got, wd)
	}
}

func TestResolveProjectDir_InvalidSession(t *testing.T) {
	wd := t.TempDir()
	_, err := resolveProjectDir(t.Context(), "../escape", wd, true)
	if err == nil {
		t.Fatal("expected invalid session error")
	}
	if !strings.Contains(err.Error(), "invalid session name") {
		t.Fatalf("expected invalid session error, got %v", err)
	}
}

func TestResolveProjectDir_UsesConfiguredProjectPrefix(t *testing.T) {
	origCfg := cfg
	t.Cleanup(func() { cfg = origCfg })

	projectsBase := t.TempDir()
	projectDir := filepath.Join(projectsBase, "myproject")
	if err := os.MkdirAll(projectDir, 0o755); err != nil {
		t.Fatalf("mkdir project: %v", err)
	}
	if err := os.MkdirAll(filepath.Join(projectDir, ".ntm"), 0o755); err != nil {
		t.Fatalf("mkdir ntm dir: %v", err)
	}
	cfg = &config.Config{ProjectsBase: projectsBase}

	oldWd, _ := os.Getwd()
	wd := t.TempDir()
	if err := os.Chdir(wd); err != nil {
		t.Fatalf("chdir: %v", err)
	}
	defer os.Chdir(oldWd)

	got, err := resolveProjectDir(t.Context(), "mypro", wd, true)
	if err != nil {
		t.Fatalf("resolveProjectDir() error = %v", err)
	}
	if got != projectDir {
		t.Fatalf("resolveProjectDir() = %q, want %q", got, projectDir)
	}
}

func TestResolveProjectDir_ExplicitRejectsWorkspaceFallback(t *testing.T) {
	isolateSessionAgentStorage(t)
	session := fmt.Sprintf("missing-summary-project-%d", time.Now().UnixNano())

	origCfg := cfg
	origWd, _ := os.Getwd()
	t.Cleanup(func() {
		cfg = origCfg
		_ = os.Chdir(origWd)
	})

	cfg = &config.Config{ProjectsBase: t.TempDir()}

	wd := t.TempDir()
	if err := os.MkdirAll(filepath.Join(wd, ".git"), 0o755); err != nil {
		t.Fatalf("mkdir wd git: %v", err)
	}
	if err := os.Chdir(wd); err != nil {
		t.Fatalf("chdir: %v", err)
	}

	_, err := resolveProjectDir(t.Context(), session, wd, true)
	if err == nil {
		t.Fatal("expected missing session project error")
	}
	if !strings.Contains(err.Error(), "getting project root failed") {
		t.Fatalf("expected project root error, got %v", err)
	}
}

func TestResolveProjectDir_ExplicitUsesSavedSessionAgentProject(t *testing.T) {
	isolateSessionAgentStorage(t)

	origCfg := cfg
	origWd, _ := os.Getwd()
	t.Cleanup(func() {
		cfg = origCfg
		_ = os.Chdir(origWd)
	})

	projectsBase := t.TempDir()
	cfg = &config.Config{ProjectsBase: projectsBase}
	session := fmt.Sprintf("saved-project-%d", time.Now().UnixNano())
	if err := os.MkdirAll(filepath.Join(projectsBase, session+"-prefix"), 0o755); err != nil {
		t.Fatalf("mkdir competing configured prefix: %v", err)
	}

	wd := t.TempDir()
	if err := os.MkdirAll(filepath.Join(wd, ".git"), 0o755); err != nil {
		t.Fatalf("mkdir wd git: %v", err)
	}
	if err := os.Chdir(wd); err != nil {
		t.Fatalf("chdir: %v", err)
	}

	actualProject := t.TempDir()
	if err := os.MkdirAll(filepath.Join(actualProject, ".git"), 0o755); err != nil {
		t.Fatalf("mkdir actual git: %v", err)
	}
	saveSessionAgentForTest(t, session, actualProject, "GreenCastle")
	saved, err := agentmail.LoadBestSessionAgent(session)
	if err != nil || saved == nil || saved.ProjectKey != actualProject {
		t.Fatalf("saved session agent lookup = %+v, err=%v", saved, err)
	}

	got, err := resolveProjectDir(t.Context(), session, wd, true)
	if err != nil {
		t.Fatalf("resolveProjectDir() error = %v", err)
	}
	if got != actualProject {
		t.Fatalf("resolveProjectDir() = %q, want %q", got, actualProject)
	}
}

func TestUniqueSessions(t *testing.T) {
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

func TestRegenerateSummaryFromArchive_NormalizesProjectScopedPrefix(t *testing.T) {
	testutil.RequireTmuxThrottled(t)

	origCfg := cfg
	origWD, _ := os.Getwd()
	t.Cleanup(func() {
		cfg = origCfg
		_ = os.Chdir(origWD)
	})

	projectsBase := t.TempDir()
	projectDir := filepath.Join(projectsBase, "myproject")
	if err := os.MkdirAll(projectDir, 0o755); err != nil {
		t.Fatalf("mkdir project: %v", err)
	}
	if err := os.MkdirAll(filepath.Join(projectDir, ".ntm"), 0o755); err != nil {
		t.Fatalf("mkdir project .ntm: %v", err)
	}
	cfg = &config.Config{ProjectsBase: projectsBase}

	homeDir := t.TempDir()
	t.Setenv("HOME", homeDir)
	archiveDir := filepath.Join(homeDir, ".ntm", "archive")
	if err := os.MkdirAll(archiveDir, 0o755); err != nil {
		t.Fatalf("mkdir archive dir: %v", err)
	}

	archivePath := filepath.Join(archiveDir, "myproject_2026-01-28.jsonl")
	record := archive.ArchiveRecord{
		Session:   "myproject",
		Pane:      "%1",
		PaneIndex: 1,
		Agent:     "claude",
		Timestamp: time.Now(),
		Content:   "Implemented the auth fix and updated tests.",
		Lines:     1,
		Sequence:  1,
	}
	file, err := os.Create(archivePath)
	if err != nil {
		t.Fatalf("create archive file: %v", err)
	}
	if err := json.NewEncoder(file).Encode(record); err != nil {
		file.Close()
		t.Fatalf("encode archive record: %v", err)
	}
	if err := file.Close(); err != nil {
		t.Fatalf("close archive file: %v", err)
	}

	otherWD := t.TempDir()
	if err := os.Chdir(otherWD); err != nil {
		t.Fatalf("chdir: %v", err)
	}

	if _, err := captureStdout(t, func() error {
		return regenerateSummaryFromArchive(t.Context(), "mypro", summary.FormatBrief, false, projectDir, otherWD, time.Time{})
	}); err != nil {
		t.Fatalf("regenerateSummaryFromArchive() error = %v", err)
	}

	summaryFiles, err := listSummaryFiles(projectDir)
	if err != nil {
		t.Fatalf("listSummaryFiles() error = %v", err)
	}
	if len(summaryFiles) != 1 {
		t.Fatalf("expected 1 summary file, got %d", len(summaryFiles))
	}
	if summaryFiles[0].Session != "myproject" {
		t.Fatalf("summary session = %q, want %q", summaryFiles[0].Session, "myproject")
	}
}

func TestSummaryCommandRegeneratesAcrossArchiveRotation(t *testing.T) {
	origCfg := cfg
	t.Cleanup(func() { cfg = origCfg })
	cfg = &config.Config{ProjectsBase: t.TempDir()}
	projectDir := t.TempDir()
	t.Chdir(projectDir)
	homeDir := t.TempDir()
	t.Setenv("HOME", homeDir)
	archiveDir := filepath.Join(homeDir, ".ntm", "archive")
	if err := os.MkdirAll(archiveDir, 0700); err != nil {
		t.Fatal(err)
	}
	session := "release_2026-10-01"
	writeChunk := func(name string, records ...archive.ArchiveRecord) {
		t.Helper()
		file, err := os.Create(filepath.Join(archiveDir, name))
		if err != nil {
			t.Fatal(err)
		}
		for _, record := range records {
			if err := json.NewEncoder(file).Encode(record); err != nil {
				_ = file.Close()
				t.Fatal(err)
			}
		}
		if err := file.Close(); err != nil {
			t.Fatal(err)
		}
	}
	before := time.Date(2026, 10, 24, 23, 0, 0, 0, time.UTC)
	first := "Implemented legacy history recovery"
	second := "Fixed dispatch receipt persistence"
	third := "Added bounded background archives"
	otherPane := "Created cross-pane integration tests"
	writeChunk(session+"_2026-10-24.jsonl", archive.ArchiveRecord{Session: session, Pane: "cc_2", Agent: "cc", Timestamp: before, Content: first, Sequence: 1})
	writeChunk(session+"_2026-10-25_090000.000000000-111.jsonl",
		archive.ArchiveRecord{Session: session, Pane: "cc_2", Agent: "cc", Timestamp: before.Add(time.Hour), Content: second, Sequence: 2},
		archive.ArchiveRecord{Session: session, Pane: "cod_3", Agent: "cod", Timestamp: before.Add(time.Hour), Content: otherPane, Sequence: 1})
	writeChunk(session+"_2026-10-25_100000.000000000-222.jsonl", archive.ArchiveRecord{Session: session, Pane: "cc_2", Agent: "cc", Timestamp: before.Add(2 * time.Hour), Content: third, Sequence: 3})
	// A prepared but still-empty active chunk must not hide earlier output.
	writeChunk(session + "_2026-10-25_110000.000000000-333.jsonl")
	writeChunk("unrelated_2026-10-25_083000.000000000-444.jsonl", archive.ArchiveRecord{Session: "unrelated", Pane: "cc_2", Agent: "cc", Content: "Fixed unrelated project"})
	paths, selected, err := findArchiveFiles(session)
	if err != nil || selected != session || len(paths) != 4 {
		t.Fatalf("session discovery = %q, %v, %v; want all four session chunks", selected, paths, err)
	}
	all, err := loadArchiveOutputs(paths, time.Time{})
	if err != nil || len(all) != 2 || all[0].AgentID != "cc_2" || all[0].Output != first+"\n"+second+"\n"+third || all[1].Output != otherPane {
		t.Fatalf("chunk aggregation lost content, order, or line boundaries: %+v, %v", all, err)
	}
	filtered, err := loadArchiveOutputs(paths, before.Add(30*time.Minute))
	if err != nil || len(filtered) != 2 || filtered[0].Output != second+"\n"+third {
		t.Fatalf("since filtering did not span archive chunks: %+v, %v", filtered, err)
	}
	cmd := newSummaryCmd()
	cmd.SetArgs([]string{"--regenerate", "--format=json"})
	text, err := captureStdout(t, func() error { return cmd.ExecuteContext(t.Context()) })
	if err != nil {
		t.Fatalf("summary --regenerate: %v", err)
	}
	var result summary.SessionSummary
	if err := json.Unmarshal([]byte(text), &result); err != nil {
		t.Fatalf("summary JSON: %v\n%s", err, text)
	}
	if result.Session != session {
		t.Fatalf("date-like session name was changed: %q", result.Session)
	}
	accomplishments := strings.Join(result.Accomplishments, "\n")
	for _, want := range []string{first, second, third, otherPane} {
		if !strings.Contains(accomplishments, want) {
			t.Errorf("summary omitted an archived accomplishment %q: %s", want, accomplishments)
		}
	}
	if strings.Contains(accomplishments, "unrelated project") {
		t.Fatalf("summary combined different sessions: %s", accomplishments)
	}
	files, err := listSummaryFiles(projectDir)
	if err != nil || len(files) != 1 || files[0].Session != session {
		t.Fatalf("regenerated summary was not persisted under the selected session: %+v, %v", files, err)
	}
}

func TestTrimOutputsToSinceWindowFindsBoundaryInEarlierChunk(t *testing.T) {
	now := time.Now()
	oldChunk := writeArchiveJSONL(t, []archive.ArchiveRecord{
		{Session: "s", Pane: "cc_2", Agent: "cc", Timestamp: now.Add(-2 * time.Hour), Content: "stale work\nOLD BOUNDARY", Sequence: 1},
	})
	activeChunk := writeArchiveJSONL(t, []archive.ArchiveRecord{
		{Session: "s", Pane: "cc_2", Agent: "cc", Timestamp: now.Add(-time.Minute), Content: "fresh work", Sequence: 2},
	})
	outputs := []summary.AgentOutput{{AgentID: "%2", AgentType: "claude", Output: "stale work\nOLD BOUNDARY\nfresh work\nnewest work"}}
	trimmed := trimOutputsToSinceWindow(outputs, []string{oldChunk, activeChunk}, now.Add(-30*time.Minute), map[string]string{"%2": "cc_2"})
	if len(trimmed) != 1 || trimmed[0].Output != "fresh work\nnewest work" {
		t.Fatalf("rotation hid earlier since-window evidence: %+v", trimmed)
	}
	if outputs[0].Output != "stale work\nOLD BOUNDARY\nfresh work\nnewest work" {
		t.Fatal("trimming modified the original live capture")
	}
}

// writeArchiveJSONL writes records to a temp JSONL archive file and returns its path.
func writeArchiveJSONL(t *testing.T, records []archive.ArchiveRecord) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "sess_2026-08-18.jsonl")
	file, err := os.Create(path)
	if err != nil {
		t.Fatalf("create archive: %v", err)
	}
	enc := json.NewEncoder(file)
	for _, r := range records {
		if err := enc.Encode(r); err != nil {
			file.Close()
			t.Fatalf("encode record: %v", err)
		}
	}
	if err := file.Close(); err != nil {
		t.Fatalf("close archive: %v", err)
	}
	return path
}

// TestLoadArchiveOutputsSinceFilter proves --since actually filters the
// regenerated capture: records older than the window are excluded, newer
// ones included, and a zero cutoff keeps everything (bd-gjo4k).
func TestLoadArchiveOutputsSinceFilter(t *testing.T) {
	now := time.Now()
	records := []archive.ArchiveRecord{
		{Session: "s", Pane: "%1", Agent: "claude", Timestamp: now.Add(-2 * time.Hour), Content: "ANCIENT WORK\n", Sequence: 1},
		{Session: "s", Pane: "%1", Agent: "claude", Timestamp: now.Add(-5 * time.Minute), Content: "FRESH WORK\n", Sequence: 2},
		{Session: "s", Pane: "%2", Agent: "codex", Timestamp: now.Add(-3 * time.Hour), Content: "OLD ONLY\n", Sequence: 1},
	}
	path := writeArchiveJSONL(t, records)

	// Zero cutoff: everything survives.
	all, err := loadArchiveOutputs([]string{path}, time.Time{})
	if err != nil {
		t.Fatalf("loadArchiveOutputs(zero): %v", err)
	}
	if len(all) != 2 {
		t.Fatalf("zero cutoff: expected 2 panes, got %d", len(all))
	}
	joined := ""
	for _, o := range all {
		joined += o.Output
	}
	if !strings.Contains(joined, "ANCIENT WORK") || !strings.Contains(joined, "FRESH WORK") || !strings.Contains(joined, "OLD ONLY") {
		t.Fatalf("zero cutoff lost content: %q", joined)
	}

	// 30m cutoff: only the fresh record survives.
	filtered, err := loadArchiveOutputs([]string{path}, now.Add(-30*time.Minute))
	if err != nil {
		t.Fatalf("loadArchiveOutputs(cutoff): %v", err)
	}
	if len(filtered) != 1 {
		t.Fatalf("cutoff: expected 1 pane, got %d (%+v)", len(filtered), filtered)
	}
	if filtered[0].AgentID != "%1" {
		t.Fatalf("cutoff: expected pane %%1, got %s", filtered[0].AgentID)
	}
	if strings.Contains(filtered[0].Output, "ANCIENT WORK") {
		t.Fatalf("content older than window was not excluded: %q", filtered[0].Output)
	}
	if !strings.Contains(filtered[0].Output, "FRESH WORK") {
		t.Fatalf("content newer than window was excluded: %q", filtered[0].Output)
	}
}

// TestTrimOutputsToSinceWindow proves the live-capture path uses archive
// timestamps to drop pre-window scrollback while keeping newer content.
func TestTrimOutputsToSinceWindow(t *testing.T) {
	now := time.Now()
	records := []archive.ArchiveRecord{
		{Session: "s", Pane: "%1", Agent: "claude", Timestamp: now.Add(-2 * time.Hour), Content: "setup output\nOLD BOUNDARY LINE\n", Sequence: 1},
		{Session: "s", Pane: "%1", Agent: "claude", Timestamp: now.Add(-time.Minute), Content: "fresh line\n", Sequence: 2},
	}
	path := writeArchiveJSONL(t, records)

	outputs := []summary.AgentOutput{
		{AgentID: "%1", AgentType: "claude", Output: "setup output\nOLD BOUNDARY LINE\nfresh line\nnewest line\n"},
		{AgentID: "%9", AgentType: "codex", Output: "untouched pane\n"},
	}

	trimmed := trimOutputsToSinceWindow(outputs, []string{path}, now.Add(-30*time.Minute), nil)
	if len(trimmed) != 2 {
		t.Fatalf("expected 2 outputs, got %d", len(trimmed))
	}
	if strings.Contains(trimmed[0].Output, "OLD BOUNDARY LINE") || strings.Contains(trimmed[0].Output, "setup output") {
		t.Fatalf("pre-window content survived the trim: %q", trimmed[0].Output)
	}
	if !strings.Contains(trimmed[0].Output, "fresh line") || !strings.Contains(trimmed[0].Output, "newest line") {
		t.Fatalf("in-window content was dropped: %q", trimmed[0].Output)
	}
	// Pane without dated pre-cutoff archive evidence stays untouched.
	if trimmed[1].Output != "untouched pane\n" {
		t.Fatalf("pane without archive evidence was modified: %q", trimmed[1].Output)
	}
	// Zero cutoff is a no-op.
	same := trimOutputsToSinceWindow(outputs, []string{path}, time.Time{}, nil)
	if same[0].Output != outputs[0].Output {
		t.Fatalf("zero cutoff modified output")
	}
	// Inputs must not be mutated.
	if !strings.Contains(outputs[0].Output, "OLD BOUNDARY LINE") {
		t.Fatal("trimOutputsToSinceWindow mutated its input slice")
	}
}

// TestTrimOutputsToSinceWindowArchivePaneAlias proves trimming works against
// REAL archiver-keyed rows: archive.Archiver names panes "<type>_<index>"
// (e.g. "cc_1"), not by tmux pane ID, so the alias map must bridge the two.
// Regression for the fresh-eyes review finding that --since trimming was a
// silent no-op on production archives.
func TestTrimOutputsToSinceWindowArchivePaneAlias(t *testing.T) {
	now := time.Now()
	records := []archive.ArchiveRecord{
		{Session: "s", Pane: "cc_1", Agent: "cc", Timestamp: now.Add(-2 * time.Hour), Content: "stale prelude\nCC BOUNDARY\n", Sequence: 1},
	}
	path := writeArchiveJSONL(t, records)

	outputs := []summary.AgentOutput{
		{AgentID: "%4", AgentType: "claude", Output: "stale prelude\nCC BOUNDARY\nrecent work\n"},
	}
	aliases := map[string]string{"%4": "cc_1"}
	trimmed := trimOutputsToSinceWindow(outputs, []string{path}, now.Add(-30*time.Minute), aliases)
	if strings.Contains(trimmed[0].Output, "CC BOUNDARY") || strings.Contains(trimmed[0].Output, "stale prelude") {
		t.Fatalf("archiver-keyed pre-window content survived the trim: %q", trimmed[0].Output)
	}
	if !strings.Contains(trimmed[0].Output, "recent work") {
		t.Fatalf("in-window content was dropped: %q", trimmed[0].Output)
	}

	// Without the alias the pane has no provable stale content: untouched.
	untrimmed := trimOutputsToSinceWindow(outputs, []string{path}, now.Add(-30*time.Minute), nil)
	if untrimmed[0].Output != outputs[0].Output {
		t.Fatalf("pane without alias evidence was modified: %q", untrimmed[0].Output)
	}
}

// TestTrimOutputsToSinceWindowFallbackLine covers the wrapped/re-rendered
// case where only the marker's last non-blank line is findable.
func TestTrimOutputsToSinceWindowFallbackLine(t *testing.T) {
	now := time.Now()
	records := []archive.ArchiveRecord{
		{Session: "s", Pane: "%1", Agent: "claude", Timestamp: now.Add(-2 * time.Hour), Content: "alpha rendered differently\nBOUNDARY-XYZ\n", Sequence: 1},
	}
	path := writeArchiveJSONL(t, records)

	outputs := []summary.AgentOutput{
		{AgentID: "%1", AgentType: "claude", Output: "alpha rendered\ndifferently\nBOUNDARY-XYZ\nnew content after\n"},
	}
	trimmed := trimOutputsToSinceWindow(outputs, []string{path}, now.Add(-30*time.Minute), nil)
	if strings.Contains(trimmed[0].Output, "BOUNDARY-XYZ") {
		t.Fatalf("boundary line survived: %q", trimmed[0].Output)
	}
	if !strings.Contains(trimmed[0].Output, "new content after") {
		t.Fatalf("post-boundary content dropped: %q", trimmed[0].Output)
	}
}
