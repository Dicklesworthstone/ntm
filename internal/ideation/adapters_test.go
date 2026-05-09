package ideation

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
)

type fakeOptionalRunner struct {
	outputs map[string][]byte
	errs    map[string]error
	missing map[string]bool
}

func (r fakeOptionalRunner) Run(ctx context.Context, workdir, name string, args []string) ([]byte, error) {
	key := name + " " + strings.Join(args, " ")
	if r.errs != nil {
		if err := r.errs[key]; err != nil {
			return nil, err
		}
	}
	if out, ok := r.outputs[key]; ok {
		return out, nil
	}
	return []byte{}, nil
}

func (r fakeOptionalRunner) LookPath(name string) bool {
	if r.missing != nil && r.missing[name] {
		return false
	}
	return true
}

func TestCollectOptionalCASSWhenMissingRecordsDegradedSource(t *testing.T) {
	snapshot := NewIdeaEvidenceSnapshot(t.TempDir())
	collector := Collector{Runner: fakeOptionalRunner{missing: map[string]bool{"cass": true, "cm": true}}}
	collector.CollectCASSSignals(context.Background(), &snapshot, OptionalAdapterOptions{ProjectDir: snapshot.Project, CASSQueries: []string{"queue dry"}})
	if !hasDegradedSource(snapshot, "cass:search") {
		t.Fatalf("missing cass should produce cass:search degraded source: %+v", snapshot.DegradedSources)
	}
	if len(snapshot.OptionalSignals) != 0 {
		t.Fatalf("missing cass should not emit signals, got %+v", snapshot.OptionalSignals)
	}
	for _, note := range snapshot.DegradedSources {
		if note.Severity == ValidationError {
			t.Fatalf("missing optional tool must not be a fatal error: %+v", note)
		}
	}
}

func TestCollectOptionalCASSParsesResults(t *testing.T) {
	snapshot := NewIdeaEvidenceSnapshot(t.TempDir())
	runner := fakeOptionalRunner{
		outputs: map[string][]byte{
			"cass search auth error --robot --limit 5 --fields minimal --days 30": []byte(`[{"id":"sess-a","title":"Auth review","summary":"OAuth flow with bearer eyJabcdef1234567890123456","agent":"claude","score":0.9,"modified_at":"2026-05-01T00:00:00Z"},{"id":"sess-b","title":"Auth fix","summary":"Email user@example.com asked","agent":"codex"}]`),
		},
	}
	collector := Collector{Runner: runner}
	collector.CollectCASSSignals(context.Background(), &snapshot, OptionalAdapterOptions{ProjectDir: snapshot.Project, CASSQueries: []string{"auth error"}})
	if len(snapshot.OptionalSignals) != 2 {
		t.Fatalf("expected 2 cass signals, got %d: %+v", len(snapshot.OptionalSignals), snapshot.OptionalSignals)
	}
	if !strings.Contains(snapshot.OptionalSignals[0].Summary, "[REDACTED_TOKEN]") &&
		!strings.Contains(snapshot.OptionalSignals[0].Summary, "[REDACTED_KEY]") {
		t.Fatalf("expected token redaction in summary, got %q", snapshot.OptionalSignals[0].Summary)
	}
	hasEmailRedacted := false
	for _, sig := range snapshot.OptionalSignals {
		if strings.Contains(sig.Summary, "[REDACTED_EMAIL]") {
			hasEmailRedacted = true
		}
	}
	if !hasEmailRedacted {
		t.Fatalf("expected email redaction in at least one signal: %+v", snapshot.OptionalSignals)
	}
}

func TestCollectOptionalCASSContextErrorBecomesDegradedSource(t *testing.T) {
	snapshot := NewIdeaEvidenceSnapshot(t.TempDir())
	runner := fakeOptionalRunner{
		errs: map[string]error{
			"cass search lookups --robot --limit 5 --fields minimal --days 30": errors.New("cass exit 1"),
		},
	}
	collector := Collector{Runner: runner}
	collector.CollectCASSSignals(context.Background(), &snapshot, OptionalAdapterOptions{ProjectDir: snapshot.Project, CASSQueries: []string{"lookups"}})
	if !hasDegradedSource(snapshot, "cass:search") {
		t.Fatalf("expected degraded cass:search source: %+v", snapshot.DegradedSources)
	}
}

func TestCollectOptionalCMParsesContext(t *testing.T) {
	snapshot := NewIdeaEvidenceSnapshot(t.TempDir())
	runner := fakeOptionalRunner{
		outputs: map[string][]byte{
			"cm context queue-dry ideation --json --no-history": []byte(`{"relevantBullets":[{"id":"b-1","category":"debugging","summary":"Check tests at /home/jeff/repo/x","tags":["go"]}],"antiPatterns":[{"id":"b-2","category":"refactor","summary":"Don't reset --hard"}],"suggestedCassQueries":["auth error","supabase 5xx"]}`),
		},
	}
	collector := Collector{Runner: runner}
	collector.CollectCMSignals(context.Background(), &snapshot, OptionalAdapterOptions{ProjectDir: snapshot.Project})
	if len(snapshot.OptionalSignals) != 4 {
		t.Fatalf("expected 4 cm signals, got %d: %+v", len(snapshot.OptionalSignals), snapshot.OptionalSignals)
	}
	foundPathRedaction := false
	for _, sig := range snapshot.OptionalSignals {
		if strings.Contains(sig.Summary, "[REDACTED_PATH]") {
			foundPathRedaction = true
		}
	}
	if !foundPathRedaction {
		t.Fatalf("expected /home path redaction in cm signal: %+v", snapshot.OptionalSignals)
	}
}

func TestCollectOptionalCMWhenMissingRecordsDegradedSource(t *testing.T) {
	snapshot := NewIdeaEvidenceSnapshot(t.TempDir())
	collector := Collector{Runner: fakeOptionalRunner{missing: map[string]bool{"cm": true}}}
	collector.CollectCMSignals(context.Background(), &snapshot, OptionalAdapterOptions{ProjectDir: snapshot.Project})
	if !hasDegradedSource(snapshot, "cm:context") {
		t.Fatalf("expected degraded cm:context source: %+v", snapshot.DegradedSources)
	}
	for _, note := range snapshot.DegradedSources {
		if note.Severity == ValidationError {
			t.Fatalf("missing optional tool must not be fatal: %+v", note)
		}
	}
}

func TestCollectOptionalHandoffSummariesScansFilesystem(t *testing.T) {
	dir := t.TempDir()
	handoffDir := filepath.Join(dir, ".ntm", "handoffs")
	mustWriteAdaptersFile(t, filepath.Join(handoffDir, "session-1.md"), []byte("# Handoff one\n\nWorked on db connection at 10.1.2.3\n\nMore detail."))
	mustWriteAdaptersFile(t, filepath.Join(handoffDir, "session-2.md"), []byte("Handoff two: token=abcdef0123456789abcdef0123456789abcd"))

	snapshot := NewIdeaEvidenceSnapshot(dir)
	Collector{}.CollectHandoffSummaries(&snapshot, OptionalAdapterOptions{ProjectDir: dir})
	if len(snapshot.OptionalSignals) != 2 {
		t.Fatalf("expected 2 handoff signals, got %d: %+v", len(snapshot.OptionalSignals), snapshot.OptionalSignals)
	}
	foundIPRedacted := false
	foundKeyRedacted := false
	for _, sig := range snapshot.OptionalSignals {
		if strings.Contains(sig.Snippet, "[REDACTED_IP]") {
			foundIPRedacted = true
		}
		if strings.Contains(sig.Snippet, "[REDACTED_TOKEN]") || strings.Contains(sig.Snippet, "[REDACTED_KEY]") || strings.Contains(sig.Snippet, "[REDACTED_HEX]") {
			foundKeyRedacted = true
		}
	}
	if !foundIPRedacted {
		t.Fatalf("expected IP redaction across handoff signals: %+v", snapshot.OptionalSignals)
	}
	if !foundKeyRedacted {
		t.Fatalf("expected token-shaped redaction across handoff signals: %+v", snapshot.OptionalSignals)
	}
}

func TestCollectOptionalHandoffMissingRecordsAvailableEmpty(t *testing.T) {
	dir := t.TempDir()
	snapshot := NewIdeaEvidenceSnapshot(dir)
	Collector{}.CollectHandoffSummaries(&snapshot, OptionalAdapterOptions{ProjectDir: dir})
	source := findCandidateSource(snapshot, "handoff:local")
	if source == nil {
		t.Fatalf("expected handoff:local source to be recorded")
	}
	if !source.Available {
		t.Fatalf("missing handoff dir should still record an available source with an evidence note, got %+v", source)
	}
	if len(snapshot.OptionalSignals) != 0 {
		t.Fatalf("expected no signals when handoff dir missing, got %+v", snapshot.OptionalSignals)
	}
}

func TestCollectOptionalAssuranceDigestsScansFilesystem(t *testing.T) {
	dir := t.TempDir()
	assuranceDir := filepath.Join(dir, "tests", "artifacts", "assurance")
	mustWriteAdaptersFile(t, filepath.Join(assuranceDir, "digest-2026.json"), []byte(`{"summary":"closeout proof verified","sha":"deadbeefcafebabedeadbeefcafebabedeadbeef"}`))
	mustWriteAdaptersFile(t, filepath.Join(assuranceDir, "report.md"), []byte("# Assurance report\n\nVerified at 2026-05-09.\n"))

	snapshot := NewIdeaEvidenceSnapshot(dir)
	Collector{}.CollectAssuranceDigests(&snapshot, OptionalAdapterOptions{ProjectDir: dir})
	if len(snapshot.OptionalSignals) != 2 {
		t.Fatalf("expected 2 assurance signals, got %d: %+v", len(snapshot.OptionalSignals), snapshot.OptionalSignals)
	}
}

func TestCollectClosedWorkHistorySignalsCompactsExistingWork(t *testing.T) {
	snapshot := NewIdeaEvidenceSnapshot(t.TempDir())
	snapshot.ExistingWork = []ExistingWorkFingerprint{
		{ID: "bd-2mb03.1", FamilyID: "bd-2mb03", Title: "Adaptive ops", Summary: "closed work item", Status: WorkStatusClosed, UpdatedAt: "2026-05-01"},
		{ID: "bd-2mb03.2", FamilyID: "bd-2mb03", Title: "Recent close", Summary: "another closed item", Status: WorkStatusClosed, UpdatedAt: "2026-05-05"},
		{ID: "bd-open", FamilyID: "bd-open", Title: "Open item", Status: WorkStatusOpen},
	}
	Collector{}.CollectClosedWorkHistorySignals(&snapshot, OptionalAdapterOptions{ProjectDir: snapshot.Project, MaxSignalsPerSource: 8})
	if len(snapshot.OptionalSignals) != 2 {
		t.Fatalf("expected 2 closed-history signals, got %d: %+v", len(snapshot.OptionalSignals), snapshot.OptionalSignals)
	}
	if snapshot.OptionalSignals[0].Title != "Recent close" {
		t.Fatalf("expected most-recent closed first, got %+v", snapshot.OptionalSignals)
	}
}

func TestCollectOptionalEnforcesPerSourceBudgetTruncation(t *testing.T) {
	snapshot := NewIdeaEvidenceSnapshot(t.TempDir())
	bigSummary := strings.Repeat("x", 5_000)
	results := "["
	for i := 0; i < 12; i++ {
		if i > 0 {
			results += ","
		}
		results += `{"id":"sess-` + string(rune('a'+i)) + `","title":"T","summary":"` + bigSummary + `"}`
	}
	results += "]"
	runner := fakeOptionalRunner{
		outputs: map[string][]byte{
			"cass search q --robot --limit 5 --fields minimal --days 30": []byte(results),
		},
	}
	collector := Collector{Runner: runner}
	collector.CollectCASSSignals(context.Background(), &snapshot, OptionalAdapterOptions{ProjectDir: snapshot.Project, CASSQueries: []string{"q"}, MaxSignalsPerSource: 3, PerSignalSummaryBytes: 80})
	if len(snapshot.OptionalSignals) != 3 {
		t.Fatalf("expected 3 cass signals after per-source truncation, got %d", len(snapshot.OptionalSignals))
	}
	for _, sig := range snapshot.OptionalSignals {
		if len(sig.Summary) > 80 {
			t.Fatalf("expected per-signal summary <= 80 bytes, got %d (%q)", len(sig.Summary), sig.Summary)
		}
	}
}

func TestCollectOptionalEnforcesTotalBudgetTruncation(t *testing.T) {
	snapshot := NewIdeaEvidenceSnapshot(t.TempDir())
	for i := 0; i < 10; i++ {
		snapshot.OptionalSignals = append(snapshot.OptionalSignals, OptionalSignal{
			ID:       "x" + string(rune('a'+i)),
			SourceID: "test",
			Kind:     "filler",
		})
	}
	enforceOptionalSignalBudget(&snapshot, 4)
	if len(snapshot.OptionalSignals) != 4 {
		t.Fatalf("expected total truncation to 4, got %d", len(snapshot.OptionalSignals))
	}
	if !hasValidationCode(snapshot, "optional_signals_truncated") {
		t.Fatalf("expected optional_signals_truncated note: %+v", snapshot.ValidationNotes)
	}
}

func TestRedactSensitiveCovers(t *testing.T) {
	cases := []struct {
		input string
		want  []string
	}{
		{"contact me at jeff@example.com", []string{"[REDACTED_EMAIL]"}},
		{"bearer eyJabcdefghijklmnopqrstuvwxyz", []string{"[REDACTED_TOKEN]"}},
		{"ssh /home/jeff/code/project/src/x.go", []string{"[REDACTED_PATH]"}},
		{"AKIAIOSFODNN7EXAMPLE in config", []string{"[REDACTED_AWS_KEY]"}},
		{"127.0.0.1 latency", []string{"[REDACTED_IP]"}},
		{"deadbeefcafebabedeadbeefcafebabe", []string{"[REDACTED_HEX]", "[REDACTED_KEY]"}},
	}
	for _, tc := range cases {
		got := redactSensitive(tc.input)
		if got == tc.input {
			t.Fatalf("redact %q produced no change", tc.input)
		}
		matched := false
		for _, marker := range tc.want {
			if strings.Contains(got, marker) {
				matched = true
				break
			}
		}
		if !matched {
			t.Fatalf("redact %q produced %q, expected one of %v", tc.input, got, tc.want)
		}
	}
}

func TestSortOptionalSignalsIsDeterministic(t *testing.T) {
	signals := []OptionalSignal{
		{ID: "z", SourceID: "cm:context", Kind: "cm_rule"},
		{ID: "a", SourceID: "cass:search", Kind: "cass_search"},
		{ID: "m", SourceID: "cass:search", Kind: "cass_search"},
	}
	sortOptionalSignals(signals)
	if signals[0].SourceID != "cass:search" || signals[0].ID != "a" {
		t.Fatalf("unexpected ordering: %+v", signals)
	}
	if !sort.SliceIsSorted(signals, func(i, j int) bool {
		if signals[i].SourceID != signals[j].SourceID {
			return signals[i].SourceID < signals[j].SourceID
		}
		if signals[i].Kind != signals[j].Kind {
			return signals[i].Kind < signals[j].Kind
		}
		return signals[i].ID < signals[j].ID
	}) {
		t.Fatalf("expected deterministic sort: %+v", signals)
	}
}

func TestCollectOptionalDoesNotRecurseIntoNodeModules(t *testing.T) {
	dir := t.TempDir()
	junk := filepath.Join(dir, ".ntm", "handoffs", "node_modules")
	mustWriteAdaptersFile(t, filepath.Join(junk, "x.md"), []byte("should not be picked up"))
	mustWriteAdaptersFile(t, filepath.Join(dir, ".ntm", "handoffs", "real.md"), []byte("real summary content"))

	snapshot := NewIdeaEvidenceSnapshot(dir)
	Collector{}.CollectHandoffSummaries(&snapshot, OptionalAdapterOptions{ProjectDir: dir})
	for _, sig := range snapshot.OptionalSignals {
		if strings.Contains(sig.Title, "x.md") {
			t.Fatalf("collector recursed into node_modules: %+v", sig)
		}
	}
}

func mustWriteAdaptersFile(t *testing.T, path string, data []byte) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("MkdirAll: %v", err)
	}
	if err := os.WriteFile(path, data, 0o644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
}

func findCandidateSource(snapshot IdeaEvidenceSnapshot, id string) *CandidateSource {
	for i := range snapshot.Sources {
		if snapshot.Sources[i].ID == id {
			return &snapshot.Sources[i]
		}
	}
	return nil
}

func hasValidationCode(snapshot IdeaEvidenceSnapshot, code string) bool {
	for _, note := range snapshot.ValidationNotes {
		if note.Code == code {
			return true
		}
	}
	return false
}
