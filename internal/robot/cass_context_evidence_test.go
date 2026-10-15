//go:build !windows

package robot

import (
	"context"
	"encoding/json"
	"fmt"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func evidenceCASSConfig(t *testing.T, response string) CASSConfig {
	t.Helper()
	cfg := DefaultCASSConfig()
	quoted := "'" + strings.ReplaceAll(response, "'", "'\"'\"'") + "'"
	cfg.BinaryPath = lifecycleCASSBinary(t, "printf '%s' "+quoted)
	return cfg
}

func TestCASSQueryRetainsIndexedSessionEvidence(t *testing.T) {
	created := time.Date(2026, 9, 15, 12, 30, 0, 0, time.UTC)
	for _, timestamp := range []string{
		fmt.Sprintf("%q", created.Format(time.RFC3339)),
		fmt.Sprint(created.Unix()),
		fmt.Sprint(created.UnixMilli()),
	} {
		t.Run(timestamp, func(t *testing.T) {
			response := fmt.Sprintf(`{"hits":[
				{"source_path":"opaque.jsonl","line_number":null,"workspace":"/work/team/project","created_at":%s,"snippet":"retrievable history","score":0.016},
				{"source_path":"second.jsonl","line_number":12,"workspace":"/work/team/other","created_at":null,"content":"full content","snippet":"summary only","score":0.01}
			],"total_matches":19}`, timestamp)
			out := QueryCASS("database transaction contention", evidenceCASSConfig(t, response), context.Background())
			if !out.Success || len(out.Hits) != 2 || out.TotalMatches != 19 {
				t.Fatalf("query lost results: %+v", out)
			}
			hit := out.Hits[0]
			if hit.Workspace != "/work/team/project" || hit.CreatedAt == nil || !hit.CreatedAt.Equal(created) || hit.Content != "retrievable history" || hit.LineNumber != 0 {
				t.Fatalf("indexed evidence was discarded: %+v", hit)
			}
			if out.Hits[1].Content != "full content" || out.Hits[1].LineNumber != 12 || out.Hits[1].CreatedAt != nil {
				t.Fatalf("full content or optional metadata changed: %+v", out.Hits[1])
			}
			raw, err := json.Marshal(out.Hits)
			if err != nil || !strings.Contains(string(raw), `"workspace":"/work/team/project"`) || !strings.Contains(string(raw), `"created_at":`) {
				t.Fatalf("metadata is absent from query JSON: %s %v", raw, err)
			}
		})
	}
}

func TestCASSWorkspaceEvidenceOverridesFilename(t *testing.T) {
	project := filepath.Join(t.TempDir(), "team", "project")
	for _, tc := range []struct {
		name, indexed, source string
		wantBonus             bool
	}{
		{"exact opaque archive", project, "opaque.jsonl", true},
		{"clean equivalent", project + string(filepath.Separator) + ".", "opaque.jsonl", true},
		{"different same basename", filepath.Join(filepath.Dir(filepath.Dir(project)), "other", "project"), "project-session.jsonl", false},
		{"prefix is not identity", project + "-backup", "project-session.jsonl", false},
		{"relative is not identity", "project", "project-session.jsonl", false},
		{"invalid metadata", project + "\n", "project-session.jsonl", false},
		{"legacy metadata absent", "", "project-session.jsonl", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			out := FilterResults([]CASSHit{{SourcePath: tc.source, Workspace: tc.indexed, Score: 1}}, FilterConfig{PreferSameProject: true, CurrentWorkspace: project})
			if len(out.Hits) != 1 || (out.Hits[0].ScoreDetail.ProjectBonus > 0) != tc.wantBonus {
				t.Fatalf("workspace evidence precedence incorrect: %+v", out)
			}
		})
	}
	if sameCASSWorkspace("/work/Project", "/work/project") {
		t.Fatal("distinct case-sensitive project paths were conflated")
	}
}

func TestCASSIndexedTimestampControlsFilteringAndRendering(t *testing.T) {
	fresh := time.Now().UTC().Add(-25 * time.Hour)
	old := time.Now().UTC().Add(-90 * 24 * time.Hour)
	hits := []CASSHit{
		{SourcePath: "/1999/01/01/archived.jsonl", CreatedAt: &fresh, Content: "fresh indexed history", Score: 1},
		{SourcePath: "/2099/01/01/renamed.jsonl", CreatedAt: &old, Content: "stale indexed history", Score: 0.8},
	}
	out := FilterResults(hits, FilterConfig{MaxAgeDays: 30, MinRelevance: 0.7})
	if len(out.Hits) != 1 || out.RemovedByAge != 1 || out.Hits[0].Content != "fresh indexed history" {
		t.Fatalf("archive path overrode indexed freshness: %+v", out)
	}
	for _, format := range []InjectionFormat{FormatMarkdown, FormatStructured} {
		text := FormatContext(out.Hits, InjectConfig{Format: format})
		if !strings.Contains(text, "1 day ago") || strings.Contains(text, "months ago") || strings.Contains(text, "stale indexed history") {
			t.Fatalf("rendered age disagrees with selected evidence: %s", text)
		}
	}
}

func TestCASSWorkspacePreferenceBreaksSaturatedScoreTies(t *testing.T) {
	out := FilterResults([]CASSHit{
		{SourcePath: "project-foreign.jsonl", Workspace: "/other/project", Score: 1},
		{SourcePath: "opaque-local.jsonl", Workspace: "/work/project", Score: 0.9},
	}, FilterConfig{PreferSameProject: true, CurrentWorkspace: "/work/project", MaxItems: 1})
	if len(out.Hits) != 1 || out.Hits[0].SourcePath != "opaque-local.jsonl" {
		t.Fatalf("normalizing to 1.0 erased the configured project preference: %+v", out)
	}
	withoutPreference := FilterResults([]CASSHit{
		{SourcePath: "project-foreign.jsonl", Workspace: "/other/project", Score: 1},
		{SourcePath: "opaque-local.jsonl", Workspace: "/work/project", Score: 0.9},
	}, FilterConfig{CurrentWorkspace: "/work/project", MaxItems: 1})
	if len(withoutPreference.Hits) != 1 || withoutPreference.Hits[0].SourcePath != "project-foreign.jsonl" {
		t.Fatal("disabled project preference still reordered results")
	}
}

func TestAssignmentPromptUsesIndexedHistoryFromAuthoritativeProject(t *testing.T) {
	project := filepath.Join(t.TempDir(), "project")
	foreign := filepath.Join(t.TempDir(), "project")
	fresh := time.Now().UTC().Add(-25 * time.Hour)
	old := fresh.Add(-90 * 24 * time.Hour)
	response := fmt.Sprintf(`{"hits":[
		{"source_path":"project-foreign.jsonl","workspace":%q,"created_at":%q,"content":"FOREIGN_HISTORY","score":1},
		{"source_path":"/1999/01/01/opaque-local.jsonl","workspace":%q,"created_at":%d,"snippet":"LOCAL_INDEXED_HISTORY","score":0.9},
		{"source_path":"/2099/01/01/project-stale.jsonl","workspace":%q,"created_at":%q,"content":"STALE_HISTORY","score":0.8}
	],"total_matches":3}`, foreign, fresh.Format(time.RFC3339), project, fresh.UnixMilli(), project, old.Format(time.RFC3339))
	query := evidenceCASSConfig(t, response)
	filter := FilterConfig{PreferSameProject: true, CurrentWorkspace: foreign, MaxAgeDays: 30, MaxItems: 1, MinRelevance: 0.7}
	prompt, evidence, memory := EnrichAssignmentPrompt(context.Background(), AssignmentPrompt{
		Prompt: "Implement the assigned database work.", Title: "Database transaction contention", AgentType: "claude", ProjectDir: project,
	}, PromptContextOptions{WithCASS: true, CASSConfig: &query, FilterConfig: &filter})
	if !strings.Contains(prompt, "LOCAL_INDEXED_HISTORY") || strings.Contains(prompt, "FOREIGN_HISTORY") || strings.Contains(prompt, "STALE_HISTORY") {
		t.Fatalf("assignment selected the wrong indexed context: %s", prompt)
	}
	if !strings.Contains(prompt, cassContextFramingNote) || !strings.HasSuffix(prompt, "Implement the assigned database work.") || evidence == nil || evidence.ItemsInjected != 1 || memory != nil {
		t.Fatalf("assignment lost framing, original work, or injection evidence: %q %+v %+v", prompt, evidence, memory)
	}
	if filter.CurrentWorkspace != foreign {
		t.Fatal("project binding mutated reusable caller configuration")
	}
}

func TestCASSMetadataMalformedTimeDegradesInsteadOfGuessing(t *testing.T) {
	query := evidenceCASSConfig(t, `{"hits":[{"source_path":"project-session.jsonl","workspace":"/work/project","created_at":"not-a-time","snippet":"unverified history"}]}`)
	prompt, evidence, _ := EnrichAssignmentPrompt(context.Background(), AssignmentPrompt{
		Prompt: "original task", Title: "Database transaction contention", ProjectDir: "/work/project",
	}, PromptContextOptions{WithCASS: true, CASSConfig: &query})
	if prompt != "original task" || evidence == nil || evidence.ItemsInjected != 0 {
		t.Fatalf("invalid indexed evidence was injected: %q %+v", prompt, evidence)
	}
}
