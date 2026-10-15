//go:build !windows

package robot

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func candidateCASSConfig(t *testing.T, global, scoped string) CASSConfig {
	t.Helper()
	quote := func(s string) string { return "'" + strings.ReplaceAll(s, "'", "'\"'\"'") + "'" }
	body := "case \"$*\" in\n*--workspace=*) printf '%s' " + quote(scoped) + ";;\n*) printf '%s' " + quote(global) + ";;\nesac"
	cfg := DefaultCASSConfig()
	cfg.BinaryPath = lifecycleCASSBinary(t, body)
	cfg.MaxResults = 1
	return cfg
}

func TestAssignmentRetrievesLocalHistoryOutsideGlobalLimit(t *testing.T) {
	project := t.TempDir()
	foreign := t.TempDir()
	global := fmt.Sprintf(`{"hits":[{"source_path":"foreign","workspace":%q,"content":"GLOBAL_ONLY_HISTORY","score":1}]}`, foreign)
	local := fmt.Sprintf(`{"hits":[{"source_path":"opaque","workspace":%q,"snippet":"LOCAL_INDEXED_HISTORY","created_at":%d,"score":0.9}]}`, project, time.Now().UnixMilli())
	cfg := candidateCASSConfig(t, global, local)
	filter := DefaultFilterConfig()
	filter.MaxItems = 1
	prompt, info, _ := EnrichAssignmentPrompt(context.Background(), AssignmentPrompt{
		Prompt: "Implement the assigned work.", Title: "Database transaction contention", ProjectDir: project,
	}, PromptContextOptions{WithCASS: true, CASSConfig: &cfg, FilterConfig: &filter})
	if !strings.Contains(prompt, "LOCAL_INDEXED_HISTORY") || strings.Contains(prompt, "GLOBAL_ONLY_HISTORY") || !strings.HasSuffix(prompt, "Implement the assigned work.") {
		t.Fatalf("project context was still hidden behind global limit: %s", prompt)
	}
	if info == nil || info.ItemsFound != 2 || info.ItemsInjected != 1 || !strings.Contains(prompt, cassContextFramingNote) {
		t.Fatalf("lost candidate evidence or historical-data framing: %q %+v", prompt, info)
	}
	if filter.CurrentWorkspace != "" {
		t.Fatal("retrieval mutated reusable caller filters")
	}
}

func TestPromptContextCandidatesKeepGlobalOnScopedFailure(t *testing.T) {
	project := t.TempDir()
	cfg := candidateCASSConfig(t, `{"hits":[{"source_path":"global","content":"GLOBAL_FALLBACK","score":1}]}`, `not-json PRIVATE_DIAGNOSTIC`)
	filter := DefaultFilterConfig()
	filter.CurrentWorkspace = project
	result, query, _ := injectCASSContextCandidates(context.Background(), "original task", "database transactions", cfg, filter, DefaultInjectConfig())
	if !result.Success || !query.Success || !strings.Contains(result.ModifiedPrompt, "GLOBAL_FALLBACK") || strings.Contains(result.ModifiedPrompt, "PRIVATE_DIAGNOSTIC") {
		t.Fatalf("optional scoped failure discarded global context: %+v %+v", result, query)
	}
}

func TestPromptContextCandidatesHonorDisabledProjectPreference(t *testing.T) {
	cfg := candidateCASSConfig(t, `{"hits":[{"source_path":"global","content":"GLOBAL_HISTORY","score":1}]}`, `{"hits":[{"source_path":"local","content":"UNREQUESTED_LOCAL_HISTORY","score":1}]}`)
	filter := DefaultFilterConfig()
	filter.CurrentWorkspace, filter.PreferSameProject = t.TempDir(), false
	result, _, _ := injectCASSContextCandidates(context.Background(), "task", "database transactions", cfg, filter, DefaultInjectConfig())
	if !result.Success || !strings.Contains(result.ModifiedPrompt, "GLOBAL_HISTORY") || strings.Contains(result.ModifiedPrompt, "UNREQUESTED_LOCAL_HISTORY") {
		t.Fatalf("disabled preference changed candidate selection: %+v", result)
	}
}

func TestPromptContextCandidatesSkipQueriesAtContextLimit(t *testing.T) {
	marker := filepath.Join(t.TempDir(), "started")
	cfg := lifecycleCASSConfig(t, marker)
	inject := DefaultInjectConfig()
	inject.CurrentContextPct = inject.SkipThreshold
	result, _, _ := injectCASSContextCandidates(context.Background(), "original task", "database transactions", cfg, DefaultFilterConfig(), inject)
	if !result.Success || result.ModifiedPrompt != "original task" || result.Metadata.SkippedReason == "" {
		t.Fatalf("full context did not produce the original task and skip receipt: %+v", result)
	}
	if _, err := os.Stat(marker); !os.IsNotExist(err) {
		t.Fatalf("full context window still launched retrieval: %v", err)
	}
}

func TestPromptContextCandidateFailureDoesNotEchoChildDiagnostics(t *testing.T) {
	cfg := DefaultCASSConfig()
	cfg.BinaryPath = lifecycleCASSBinary(t, "printf PRIVATE_CHILD_DIAGNOSTIC >&2\nexit 9")
	result, query, _ := injectCASSContextCandidates(context.Background(), "original task", "database transactions", cfg, DefaultFilterConfig(), DefaultInjectConfig())
	if result.Success || query.Success || result.ModifiedPrompt != "original task" || strings.Contains(fmt.Sprintf("%+v %+v", result, query), "PRIVATE_CHILD_DIAGNOSTIC") {
		t.Fatalf("failure fabricated enrichment or leaked child stderr: %+v %+v", result, query)
	}
	if !strings.Contains(result.Metadata.SkippedReason, "code 9") {
		t.Fatalf("lost actionable exit classification: %+v", result)
	}
}
