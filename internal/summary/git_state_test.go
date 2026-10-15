package summary

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

func evidenceGit(t *testing.T, dir string, args ...string) []byte {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, "git", args...)
	cmd.Dir = dir
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, out)
	}
	return out
}

func evidenceRepo(t *testing.T) string {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("Git is not installed")
	}
	t.Setenv("GIT_CONFIG_GLOBAL", os.DevNull)
	t.Setenv("GIT_CONFIG_SYSTEM", os.DevNull)
	dir := t.TempDir()
	evidenceGit(t, dir, "init", "-b", "main")
	evidenceGit(t, dir, "config", "user.name", "Summary Test")
	evidenceGit(t, dir, "config", "user.email", "summary@example.invalid")
	return dir
}

func evidenceWrite(t *testing.T, dir, name, text string) {
	t.Helper()
	path := filepath.Join(dir, name)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(text), 0o600); err != nil {
		t.Fatal(err)
	}
}

func evidenceSummary(t *testing.T, dir string, format SummaryFormat) *SessionSummary {
	t.Helper()
	s, err := SummarizeSession(context.Background(), Options{
		Session: "evidence", ProjectDir: dir, IncludeGitDiff: true, Format: format,
		Outputs: []AgentOutput{{Output: `{"accomplishments":["Implemented the feature"]}`}},
	})
	if err != nil {
		t.Fatal(err)
	}
	return s
}

// Decode the public JSON receipt rather than relying on the new internal type.
// These surface assertions also run against the previous generator unchanged.
func evidenceReceipt(t *testing.T, s *SessionSummary) map[string]interface{} {
	t.Helper()
	raw, err := json.Marshal(s)
	if err != nil {
		t.Fatal(err)
	}
	var doc map[string]interface{}
	if err := json.Unmarshal(raw, &doc); err != nil {
		t.Fatal(err)
	}
	state, ok := doc["git"].(map[string]interface{})
	if !ok {
		t.Fatalf("summary omitted the requested Git observation: %s", raw)
	}
	return state
}

func TestSummaryGitClassifiesFilesFromOneObservation(t *testing.T) {
	dir := evidenceRepo(t)
	for _, name := range []string{"modified.txt", "deleted.txt", "old name.txt"} {
		evidenceWrite(t, dir, name, name+"\n")
	}
	evidenceGit(t, dir, "add", ".")
	evidenceGit(t, dir, "-c", "commit.gpgsign=false", "commit", "-m", "base")
	evidenceWrite(t, dir, "modified.txt", "changed\n")
	evidenceGit(t, dir, "rm", "deleted.txt")
	evidenceGit(t, dir, "mv", "old name.txt", "renamed.txt")
	evidenceWrite(t, dir, "added.txt", "staged addition\n")
	evidenceGit(t, dir, "add", "added.txt")
	name := "new dir/ space and Ω.txt "
	if runtime.GOOS != "windows" {
		name = "new dir/ space\nquote\" and Ω.txt "
	}
	evidenceWrite(t, dir, name, "untracked\n")
	index, err := os.ReadFile(filepath.Join(dir, ".git", "index"))
	if err != nil {
		t.Fatal(err)
	}
	s := evidenceSummary(t, dir, FormatDetailed)
	got := map[string][]string{}
	for _, f := range s.Files {
		got[f.Path] = append(got[f.Path], f.Action)
	}
	for path, action := range map[string]string{
		"modified.txt": "modified", "deleted.txt": "deleted", "old name.txt": "deleted",
		"renamed.txt": "created", "added.txt": "created", name: "created",
	} {
		if fmt.Sprint(got[path]) != "["+action+"]" {
			t.Errorf("file %q classified as %v, want only %s", path, got[path], action)
		}
	}
	if len(got) != 6 {
		t.Errorf("unexpected or mangled paths: %#v", got)
	}
	if evidenceReceipt(t, s)["status"] != "dirty" {
		t.Fatal("dirty checkout not recorded")
	}
	after, err := os.ReadFile(filepath.Join(dir, ".git", "index"))
	if err != nil || !bytes.Equal(index, after) {
		t.Fatalf("summary rewrote the index: %v", err)
	}
	if !strings.Contains(s.Text, fmt.Sprintf("%q", name)) {
		t.Fatal("detailed Git evidence lost the literal pathname")
	}
}

func TestSummaryGitUnbornStagedAdditionIsNotLost(t *testing.T) {
	dir := evidenceRepo(t)
	evidenceWrite(t, dir, "first.txt", "first\n")
	evidenceGit(t, dir, "add", "first.txt")
	s := evidenceSummary(t, dir, FormatHandoff)
	if len(s.Files) != 1 || s.Files[0].Path != "first.txt" || s.Files[0].Action != "created" {
		t.Fatalf("staged addition without HEAD was lost: %+v", s.Files)
	}
	if s.Handoff == nil || s.Handoff.Status != "partial" {
		t.Fatalf("uncommitted work reported complete: %+v", s.Handoff)
	}
}

func TestSummaryGitUnavailableCannotClaimCompletion(t *testing.T) {
	for _, dir := range []string{t.TempDir(), ""} {
		s := evidenceSummary(t, dir, FormatHandoff)
		if s.Handoff == nil || s.Handoff.Status != "partial" || s.Handoff.Outcome == "SUCCEEDED" {
			t.Fatalf("unavailable Git became task success: %+v", s.Handoff)
		}
		if len(s.Errors) != 0 {
			t.Fatalf("optional inspection failure attributed to the agent: %+v", s.Errors)
		}
		if len(s.Handoff.Next) == 0 {
			t.Fatal("missing next-step inspection guidance")
		}
		if evidenceReceipt(t, s)["status"] != "unavailable" {
			t.Fatal("missing unavailable receipt")
		}
		for _, format := range []SummaryFormat{FormatBrief, FormatDetailed} {
			if !strings.Contains(RenderSummary(s, format), "Git observation: unavailable") {
				t.Fatalf("%s rendering hid inspection failure", format)
			}
		}
	}
}

func TestSummaryGitConflictsOverrideAgentSuccess(t *testing.T) {
	dir := evidenceRepo(t)
	evidenceWrite(t, dir, "conflict.txt", "base\n")
	evidenceGit(t, dir, "add", ".")
	evidenceGit(t, dir, "-c", "commit.gpgsign=false", "commit", "-m", "base")
	evidenceGit(t, dir, "checkout", "-b", "competing-work")
	evidenceWrite(t, dir, "conflict.txt", "other\n")
	evidenceGit(t, dir, "-c", "commit.gpgsign=false", "commit", "-am", "other")
	evidenceGit(t, dir, "checkout", "main")
	evidenceWrite(t, dir, "conflict.txt", "current\n")
	evidenceGit(t, dir, "-c", "commit.gpgsign=false", "commit", "-am", "current")
	cmd := exec.Command("git", "merge", "competing-work")
	cmd.Dir = dir
	if out, err := cmd.CombinedOutput(); err == nil {
		t.Fatalf("fixture did not conflict: %s", out)
	}
	before := evidenceGit(t, dir, "ls-files", "-u", "-z")
	s := evidenceSummary(t, dir, FormatHandoff)
	if s.Handoff == nil || s.Handoff.Status != "blocked" {
		t.Fatalf("unresolved merge reported successful handoff: %+v", s.Handoff)
	}
	if !strings.Contains(strings.Join(s.Handoff.Blockers, "\n"), "conflict.txt") {
		t.Fatal("handoff lost the conflicted path")
	}
	if evidenceReceipt(t, s)["status"] != "conflicted" {
		t.Fatal("conflict state not exposed")
	}
	if after := evidenceGit(t, dir, "ls-files", "-u", "-z"); !bytes.Equal(before, after) {
		t.Fatal("summary changed unresolved index entries")
	}
	if _, err := os.Stat(filepath.Join(dir, ".git", "MERGE_HEAD")); err != nil {
		t.Fatal("summary removed merge recovery state")
	}
}

func TestSummaryGitCleanDoesNotErasePendingWork(t *testing.T) {
	dir := evidenceRepo(t)
	evidenceGit(t, dir, "-c", "commit.gpgsign=false", "commit", "--allow-empty", "-m", "initial")
	s := evidenceSummary(t, dir, FormatHandoff)
	if s.Handoff.Status != "complete" || evidenceReceipt(t, s)["status"] != "clean" {
		t.Fatalf("clean completed task changed: %+v", s)
	}
	s.Pending = []string{"Implement the remaining endpoint"}
	h := buildHandoffSummary(s)
	if h.Status != "partial" {
		t.Fatalf("clean checkout erased pending tasks: %+v", h)
	}
}

func evidenceWrapper(t *testing.T, body string) {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("fault-injection wrapper needs a POSIX shell")
	}
	bin := t.TempDir()
	if err := os.WriteFile(filepath.Join(bin, "git"), []byte("#!/bin/sh\n"+body+"\n"), 0o700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
}

func TestSummaryGitFailedAndMalformedOutputNeverBecomesClean(t *testing.T) {
	for i, body := range []string{
		"printf '?? partial.txt\\000'; printf 'PRIVATE_DIAGNOSTIC' >&2; exit 17",
		"printf '?? partial.txt\\000broken'",
	} {
		t.Run(fmt.Sprintf("fault_%d", i), func(t *testing.T) {
			evidenceWrapper(t, body)
			s := evidenceSummary(t, t.TempDir(), FormatBrief)
			if evidenceReceipt(t, s)["status"] != "unavailable" || len(s.Files) != 0 {
				t.Fatalf("partial data survived failure: %+v", s)
			}
			raw, _ := json.Marshal(s)
			if strings.Contains(string(raw), "PRIVATE_DIAGNOSTIC") {
				t.Fatal("subprocess diagnostics leaked into summary")
			}
		})
	}
}

func TestSummaryGitCancellationJoinsProcess(t *testing.T) {
	marker := filepath.Join(t.TempDir(), "started")
	t.Setenv("NTM_GIT_EVIDENCE_STARTED", marker)
	evidenceWrapper(t, "printf started > \"$NTM_GIT_EVIDENCE_STARTED\"\nexec sleep 30")
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() {
		s, err := SummarizeSession(ctx, Options{Outputs: []AgentOutput{{Output: "completed work"}}, ProjectDir: t.TempDir(), IncludeGitDiff: true})
		if s != nil {
			done <- fmt.Errorf("cancelled operation produced a summary")
			return
		}
		done <- err
	}()
	deadline := time.Now().Add(3 * time.Second)
	for {
		if _, err := os.Stat(marker); err == nil {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("subprocess did not start")
		}
		time.Sleep(5 * time.Millisecond)
	}
	cancel()
	select {
	case err := <-done:
		if err != context.Canceled {
			t.Fatalf("cancellation = %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("cancelled process retained its lookup timeout")
	}
}

func TestSummaryGitStatusParserPreservesTwoPathRecords(t *testing.T) {
	raw := []byte("RM destination\nname\x00 origin name \x00?? -leading\x00UU conflict\x00")
	entries, err := parseGitStatus(context.Background(), raw)
	if err != nil || len(entries) != 3 {
		t.Fatalf("parse = %+v %v", entries, err)
	}
	if entries[0].Path != "destination\nname" || entries[0].OriginalPath != " origin name " || entries[0].Status != "RM" || entries[1].Path != "-leading" {
		t.Fatalf("paths altered: %+v", entries)
	}
	for _, status := range []string{"DD", "AU", "UD", "UA", "DU", "AA", "UU"} {
		if !gitStatusConflicted(status) {
			t.Errorf("missed %s", status)
		}
	}
	for _, raw := range []string{" M missing-terminator", "R  dst\x00", "?? \x00", "ZZ invalid\x00", "  unchanged\x00", "?? good\x00\x00"} {
		if entries, err := parseGitStatus(context.Background(), []byte(raw)); err == nil || entries != nil {
			t.Errorf("accepted malformed record %q: %+v %v", raw, entries, err)
		}
	}
}

func TestSummaryGitOutputCaptureCannotBypassLimit(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	capture := &gitStatusCapture{cancel: cancel}
	if _, err := capture.Write(make([]byte, maxGitStatusBytes)); err != nil {
		t.Fatal(err)
	}
	if _, err := capture.Write([]byte{1}); err == nil || !capture.overflow || ctx.Err() == nil {
		t.Fatal("capture did not refuse excess output")
	}
	if capture.buffer.Len() != maxGitStatusBytes {
		t.Fatal("buffer exceeded its budget")
	}
}

type evidenceSummarizer struct{ prompt *string }

func (s evidenceSummarizer) Summarize(ctx context.Context, prompt string, maxTokens int) (string, error) {
	*s.prompt = prompt
	return "All done.", nil
}

func TestSummaryGitEvidenceSurvivesGeneratedProse(t *testing.T) {
	var prompt string
	s, err := SummarizeSession(context.Background(), Options{
		Outputs: []AgentOutput{{Output: `{"accomplishments":["Implemented feature"]}`}}, ProjectDir: t.TempDir(), IncludeGitDiff: true,
		Summarizer: evidenceSummarizer{prompt: &prompt},
	})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(s.Text, "Git observation: unavailable") || !strings.Contains(prompt, "Git observation: unavailable") {
		t.Fatalf("generated prose erased observation: prompt=%q output=%q", prompt, s.Text)
	}
}
