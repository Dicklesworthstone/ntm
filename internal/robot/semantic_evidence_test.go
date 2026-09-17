package robot

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestSemanticWedgeRequiresCompleteEvidence(t *testing.T) {
	now := time.Date(2026, 9, 17, 12, 0, 0, 0, time.UTC)
	stale := now.Add(-time.Hour)
	for _, gitOK := range []bool{false, true} {
		for _, beadsOK := range []bool{false, true} {
			t.Run(fmt.Sprintf("git=%v/beads=%v", gitOK, beadsOK), func(t *testing.T) {
				sp := buildSemanticProgress("NTM-Pane: s/0.1", time.Minute, true,
					gitTokenActivity{anyTokenCommit: true, lastCommitAt: &stale, available: gitOK},
					claimActivity{available: beadsOK}, now)
				complete := gitOK && beadsOK
				if sp.EvidenceComplete != complete || (sp.SuspectedWedge != "") != complete {
					t.Fatalf("unavailable evidence became a wedge: %+v", sp)
				}
				if sp.Source != "token" || sp.LastCommitAt == nil {
					t.Fatalf("positive attribution was discarded: %+v", sp)
				}
				raw, err := json.Marshal(sp)
				if err != nil || !strings.Contains(string(raw), fmt.Sprintf(`"evidence_complete":%v`, complete)) {
					t.Fatalf("availability missing from JSON: %s, %v", raw, err)
				}
			})
		}
	}
}

func TestSemanticGitEvidenceBoundsAndOrdering(t *testing.T) {
	now := time.Date(2026, 9, 17, 12, 0, 0, 123456789, time.UTC)
	token := "NTM-Pane: s/0.1"
	record := func(at time.Time, token string) string {
		return at.Format(time.RFC3339Nano) + "\x1fwork\n\n" + token + "\n\x00"
	}
	recent := now.Add(-time.Minute)
	old := now.Add(-time.Hour)
	got := parseGitTokenActivity([]byte(record(old, token)+record(recent, token)), token, 30*time.Minute, now)
	if !got.available || got.commitsInWindow != 1 || got.lastCommitAt == nil || !got.lastCommitAt.Equal(recent) {
		t.Fatalf("nonchronological history: %+v", got)
	}
	got = parseGitTokenActivity([]byte(record(now.Add(-30*time.Minute), token)), token, 30*time.Minute, now)
	if !got.available || got.commitsInWindow != 1 {
		t.Fatalf("inclusive nanosecond boundary: %+v", got)
	}
	for _, raw := range []string{
		"broken record\x00",
		"invalid-date\x1f" + token + "\x00",
		record(now.Add(time.Nanosecond), token),
	} {
		got = parseGitTokenActivity([]byte(raw+record(recent, token)), token, 30*time.Minute, now)
		if got.available || got.commitsInWindow != 1 || !got.anyTokenCommit {
			t.Fatalf("corruption must retain positive work but mark incomplete: %+v", got)
		}
	}
	got = parseGitTokenActivity(nil, token, time.Minute, now)
	if !got.available || got.anyTokenCommit || got.commitsInWindow != 0 {
		t.Fatalf("empty successful log: %+v", got)
	}
	// No exact-token matches on a capped prefix page does NOT prove no work.
	got = parseGitTokenActivity([]byte(strings.Repeat(record(old, token+"0"), semanticGitLogCap)), token, time.Minute, now)
	if got.available || got.anyTokenCommit || got.commitsInWindow != 0 {
		t.Fatalf("prefix-saturated log must be incomplete, not attributed: %+v", got)
	}
}

func TestSemanticClaimTimestampEvidenceAvailability(t *testing.T) {
	now := time.Date(2026, 9, 17, 12, 0, 0, 0, time.UTC)
	cutoff := now.Add(-time.Minute)
	old := cutoff.Add(-time.Second).Format(time.RFC3339Nano)
	recent := cutoff.Format(time.RFC3339Nano)
	future := now.Add(time.Nanosecond).Format(time.RFC3339Nano)
	for _, tc := range []struct {
		name, updated, closed string
		recent, available     bool
	}{
		{"missing", "", "", false, false},
		{"malformed", "invalid", "", false, false},
		{"future", future, "", false, false},
		{"old", old, "", false, true},
		{"recent", recent, "", true, true},
		{"invalid with old", old, "invalid", false, false},
		{"invalid with recent", recent, "invalid", true, true},
		{"future with recent", future, recent, true, true},
		{"whitespace", "  " + recent + " ", "", true, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			raw, err := json.Marshal([]brListIssue{{Status: "closed", UpdatedAt: tc.updated, ClosedAt: tc.closed}})
			if err != nil {
				t.Fatal(err)
			}
			got := countClaimsInWindow(raw, time.Minute, now)
			if got.available != tc.available || (got.claimsInWindow == 1) != tc.recent || !got.anyLabeledBead {
				t.Fatalf("activity=%+v, want recent=%v available=%v", got, tc.recent, tc.available)
			}
		})
	}
}

func TestSemanticLiveCollectorSuppressesWedgeOnUnknownEvidence(t *testing.T) {
	dir := installSemanticSchemaCommands(t)
	now := time.Date(2026, 9, 17, 12, 0, 0, 0, time.UTC)
	// Exit-zero git fixture with a stale, correctly attributed commit.
	script := "#!/bin/sh\nprintf '2026-09-17T10:00:00Z\\037work\\n\\nNTM-Pane: test/2.3\\n\\000'\n"
	if err := os.WriteFile(filepath.Join(dir, "git"), []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	for _, raw := range []string{`null`, `{}`, `[{"status":"closed","updated_at":"invalid"}]`, `[]`} {
		t.Run(raw, func(t *testing.T) {
			t.Setenv("NTM_TEST_BR_JSON", raw)
			sp, available := paneSemanticProgressWithAvailability(PaneAddr{Session: "test", Window: 2, Pane: 3}, dir, time.Minute, true, now)
			wantComplete := raw == `[]`
			if available != wantComplete || sp.EvidenceComplete != wantComplete || (sp.SuspectedWedge != "") != wantComplete {
				t.Fatalf("collector misclassified %s: available=%v %+v", raw, available, sp)
			}
		})
	}
}

// Shadow only br, retaining real git for the integration guardrail test.
func installUnavailableSemanticBR(t *testing.T) {
	t.Helper()
	if _, err := os.Stat("/bin/sh"); err != nil {
		t.Skip("command fixture requires /bin/sh")
	}
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "br"), []byte("#!/bin/sh\nexit 1\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
}
