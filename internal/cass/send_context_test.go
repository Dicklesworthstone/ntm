package cass

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"strings"
	"testing"
	"time"
)

// Run the test executable as a deterministic CASS-shaped process. This reaches
// the production executor, limits, argument builder, decoder and scope merger;
// it needs neither a shell wrapper nor an installed CASS index.
func init() {
	fixture := os.Getenv("NTM_CASS_SEND_FIXTURE")
	if fixture == "" || len(os.Args) < 2 || os.Args[1] != "search" {
		return
	}
	raw, err := os.ReadFile(fixture)
	if err != nil {
		os.Exit(98)
	}
	var spec sendContextFixture
	if json.Unmarshal(raw, &spec) != nil {
		os.Exit(98)
	}
	scope, action, body := "global", spec.GlobalAction, spec.Global
	for _, arg := range os.Args[2:] {
		if strings.HasPrefix(arg, "--workspace=") {
			scope, action, body = "workspace", spec.WorkspaceAction, spec.Workspace
		}
	}
	args, _ := json.Marshal(os.Args[1:])
	if os.WriteFile(filepath.Join(spec.ArgsDir, scope+".json"), args, 0o600) != nil {
		os.Exit(98)
	}
	switch action {
	case "timeout":
		time.Sleep(5 * time.Second)
	case "fail":
		fmt.Fprintln(os.Stderr, "PRIVATE_FIXTURE_DIAGNOSTIC")
		os.Exit(9)
	case "uninitialized":
		os.Exit(3)
	case "empty":
		os.Exit(1)
	case "stderr-overflow":
		fmt.Fprint(os.Stderr, strings.Repeat("x", (64<<10)+1))
	case "stdout-overflow":
		fmt.Fprint(os.Stdout, `{"hits":[{"content":"`)
		chunk := strings.Repeat("x", 1<<20)
		for i := 0; i < 33; i++ {
			fmt.Fprint(os.Stdout, chunk)
		}
		fmt.Fprint(os.Stdout, `"}]}`)
		os.Exit(0)
	}
	fmt.Fprint(os.Stdout, body)
	os.Exit(0)
}

type sendContextFixture struct {
	Global, Workspace             string
	GlobalAction, WorkspaceAction string
	ArgsDir                       string
}

func sendContextProcess(t *testing.T, spec sendContextFixture) (CASSConfig, string) {
	t.Helper()
	file := filepath.Join(t.TempDir(), "fixture.json")
	spec.ArgsDir = t.TempDir()
	raw, err := json.Marshal(spec)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(file, raw, 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("NTM_CASS_SEND_FIXTURE", file)
	// Do not charge the child's race-runtime exit sleep to the query budget.
	t.Setenv("GORACE", "atexit_sleep_ms=0")
	binary, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	cfg := DefaultCASSConfig()
	cfg.BinaryPath, cfg.Timeout = binary, 2*time.Second
	return cfg, spec.ArgsDir
}

func sendContextBody(workspace, text string, indexed interface{}) string {
	raw, err := json.Marshal(map[string]interface{}{
		"total_matches": 42,
		"hits": []interface{}{map[string]interface{}{
			"source_path": "/archive/project/2099/01/01/session.jsonl", "line_number": 7,
			"workspace": workspace, "agent": "claude", "score": 1.0,
			"snippet": text, "created_at": indexed,
		}},
	})
	if err != nil {
		panic(err)
	}
	return string(raw)
}

func sendContextFilter(workspace string) FilterConfig {
	return FilterConfig{CurrentWorkspace: workspace, PreferSameProject: true, MaxItems: 1, MaxAgeDays: 30, RecencyBoost: 0.3}
}

func TestSendContextRetrievesProjectHistoryBeyondGlobalLimit(t *testing.T) {
	workspace := filepath.Join(t.TempDir(), "project")
	global := sendContextBody(filepath.Join(t.TempDir(), "project"), "GLOBAL_HISTORY", nil)
	local := strings.ReplaceAll(sendContextBody(workspace, "LOCAL_HISTORY", nil), "session.jsonl", "local.jsonl")
	cfg, _ := sendContextProcess(t, sendContextFixture{Global: global, Workspace: local})
	cfg.MaxResults = 1
	prompt := "implement authentication middleware"
	result, search, filtered := InjectContextFromQuery(t.Context(), prompt, cfg, sendContextFilter(workspace), DefaultInjectConfig())
	if !result.Success || !search.Success || len(search.Hits) != 2 || len(filtered.Hits) != 1 {
		t.Fatalf("candidate retrieval: injection=%+v search=%+v filtered=%+v", result, search, filtered)
	}
	if !strings.Contains(result.InjectedContext, "LOCAL_HISTORY") || strings.Contains(result.InjectedContext, "GLOBAL_HISTORY") || !strings.HasSuffix(result.ModifiedPrompt, prompt) {
		t.Fatalf("wrong history or changed task: %q", result.ModifiedPrompt)
	}
	if search.TotalMatches != 42 || result.Metadata.ItemsFound != 2 || result.Metadata.ItemsInjected != 1 {
		t.Fatalf("incorrect retrieval receipt: %+v %+v", search, result.Metadata)
	}
}

func TestSendContextDecodesIndexedEvidence(t *testing.T) {
	indexed := time.Now().UTC().Add(-48 * time.Hour).Truncate(time.Second)
	for _, timestamp := range []interface{}{indexed.Format(time.RFC3339), indexed.Unix(), indexed.UnixMilli()} {
		t.Run(fmt.Sprint(timestamp), func(t *testing.T) {
			workspace := t.TempDir()
			cfg, _ := sendContextProcess(t, sendContextFixture{Global: sendContextBody(workspace, "SNIPPET_HISTORY", timestamp)})
			search := QueryCASS(t.Context(), "authentication middleware", cfg)
			if !search.Success || len(search.Hits) != 1 || search.Hits[0].Content != "SNIPPET_HISTORY" {
				t.Fatalf("snippet or response lost: %+v", search)
			}
			raw, _ := json.Marshal(search.Hits[0])
			var evidence struct {
				Workspace string    `json:"workspace"`
				CreatedAt time.Time `json:"created_at"`
			}
			if err := json.Unmarshal(raw, &evidence); err != nil || evidence.Workspace != workspace || !evidence.CreatedAt.Equal(indexed) {
				t.Fatalf("indexed evidence lost: %s (%v)", raw, err)
			}
			result := InjectContext("task", FilterResults(search.Hits, sendContextFilter(workspace)).Hits, DefaultInjectConfig())
			if !strings.Contains(result.InjectedContext, "2 days ago") {
				t.Fatalf("archive filename overrode indexed time: %q", result.InjectedContext)
			}
		})
	}
}

func TestSendContextScopePolicyAndArguments(t *testing.T) {
	for _, mode := range []string{"scoped", "query-disabled", "filter-disabled", "relative-workspace", "invalid-workspace"} {
		t.Run(mode, func(t *testing.T) {
			workspace := filepath.Join(t.TempDir(), "project with spaces")
			cfg, logs := sendContextProcess(t, sendContextFixture{Global: `{"hits":[]}`, Workspace: `{"hits":[]}`})
			cfg.MaxResults, cfg.MaxAgeDays = 3, 7
			cfg.AgentFilter = []string{"claude", "codex --not-a-flag"}
			filter := sendContextFilter(workspace)
			switch mode {
			case "query-disabled":
				cfg.PreferSameProject = false
			case "filter-disabled":
				filter.PreferSameProject = false
			case "relative-workspace":
				filter.CurrentWorkspace = "relative/project"
			case "invalid-workspace":
				filter.CurrentWorkspace += "\n"
			}
			result, _ := QueryAndFilterCASS(t.Context(), "authentication --limit=99", cfg, filter)
			if !result.Success {
				t.Fatal(result.Error)
			}
			for _, scope := range []string{"global", "workspace"} {
				raw, err := os.ReadFile(filepath.Join(logs, scope+".json"))
				if scope == "workspace" && mode != "scoped" {
					if !os.IsNotExist(err) {
						t.Fatalf("unexpected workspace search: %s %v", raw, err)
					}
					continue
				}
				var args []string
				if err != nil || json.Unmarshal(raw, &args) != nil {
					t.Fatalf("missing %s arguments: %s %v", scope, raw, err)
				}
				want := []string{"search", "--json", "--limit=3", "--agent=claude", "--agent=codex --not-a-flag"}
				if scope == "workspace" {
					want = append(want, "--workspace="+workspace)
				}
				want = append(want, "--since=7d", "--", result.Query)
				if !reflect.DeepEqual(args, want) {
					t.Fatalf("argument boundaries: got %q want %q", args, want)
				}
			}
		})
	}
}

func TestSendContextRetainsHealthyScopeOnPartialFailure(t *testing.T) {
	for _, scope := range []string{"global", "workspace"} {
		for _, action := range []string{"fail", "timeout", "malformed", "uninitialized"} {
			t.Run(scope+"/"+action, func(t *testing.T) {
				workspace := t.TempDir()
				spec := sendContextFixture{Global: sendContextBody("", "GLOBAL_HISTORY", nil), Workspace: strings.ReplaceAll(sendContextBody(workspace, "LOCAL_HISTORY", nil), "session.jsonl", "local.jsonl")}
				want := "LOCAL_HISTORY"
				if scope == "global" {
					spec.GlobalAction = action
					if action == "malformed" {
						spec.Global = "{invalid"
					}
				} else {
					want, spec.WorkspaceAction = "GLOBAL_HISTORY", action
					if action == "malformed" {
						spec.Workspace = "{invalid"
					}
				}
				cfg, _ := sendContextProcess(t, spec)
				cfg.Timeout = 200 * time.Millisecond
				result, _, _ := InjectContextFromQuery(t.Context(), "authentication task", cfg, sendContextFilter(workspace), DefaultInjectConfig())
				if !result.Success || !strings.Contains(result.InjectedContext, want) {
					t.Fatalf("lost healthy %s scope after %s: %+v", scope, action, result)
				}
			})
		}
	}
}

func TestSendContextOperationCancellationDiscardsBothScopes(t *testing.T) {
	for _, mode := range []string{"cancel", "deadline", "pre-canceled", "nil-context"} {
		t.Run(mode, func(t *testing.T) {
			workspace := t.TempDir()
			cfg, logs := sendContextProcess(t, sendContextFixture{
				Global:          sendContextBody("", "PARTIAL_HISTORY_MUST_NOT_BE_INJECTED", nil),
				WorkspaceAction: "timeout",
			})
			cfg.Timeout = 5 * time.Second
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			wantError := "context canceled"
			var watcherDone chan struct{}
			switch mode {
			case "cancel":
				watcherDone = make(chan struct{})
				go func() {
					defer close(watcherDone)
					ticker := time.NewTicker(10 * time.Millisecond)
					defer ticker.Stop()
					for {
						select {
						case <-ctx.Done():
							return
						case <-ticker.C:
							_, globalErr := os.Stat(filepath.Join(logs, "global.json"))
							_, localErr := os.Stat(filepath.Join(logs, "workspace.json"))
							if globalErr == nil && localErr == nil {
								cancel()
								return
							}
						}
					}
				}()
			case "deadline":
				var deadlineCancel context.CancelFunc
				ctx, deadlineCancel = context.WithTimeout(ctx, 750*time.Millisecond)
				defer deadlineCancel()
				wantError = "context deadline exceeded"
			case "pre-canceled":
				cancel()
			case "nil-context":
				ctx = nil
				wantError = "requires an operation context"
			}
			start := time.Now()
			const prompt = "Implement authentication middleware"
			result, search, filtered := InjectContextFromQuery(ctx, prompt, cfg, sendContextFilter(workspace), DefaultInjectConfig())
			cancel()
			if watcherDone != nil {
				<-watcherDone
			}
			if elapsed := time.Since(start); elapsed >= 3*time.Second {
				t.Fatalf("caller cancellation waited for the 5s optional lookup budget: %v", elapsed)
			}
			if result.Success || search.Success || len(search.Hits) != 0 || len(filtered.Hits) != 0 || result.ModifiedPrompt != prompt || result.InjectedContext != "" || result.Metadata.ItemsInjected != 0 || !strings.Contains(search.Error, wantError) {
				t.Fatalf("stopped operation retained history or lost cancellation: injection=%+v search=%+v filtered=%+v", result, search, filtered)
			}
			entries, err := os.ReadDir(logs)
			if err != nil {
				t.Fatal(err)
			}
			if (mode == "pre-canceled" || mode == "nil-context") && len(entries) != 0 {
				t.Fatalf("stopped operation started CASS: %v", entries)
			}
			if (mode == "cancel" || mode == "deadline") && len(entries) != 2 {
				t.Fatalf("fixture did not exercise both in-flight scopes: %v", entries)
			}
		})
	}
}

func TestSendContextFailuresNeverInjectPartialOutput(t *testing.T) {
	for _, action := range []string{"fail", "malformed", "stderr-overflow", "stdout-overflow", "mixed-index-overflow"} {
		t.Run(action, func(t *testing.T) {
			body := sendContextBody("", "MUST_NOT_BE_INJECTED", nil)
			spec := sendContextFixture{Global: body, Workspace: body, GlobalAction: action, WorkspaceAction: action}
			if action == "malformed" {
				spec.Global, spec.Workspace = "{invalid", "{invalid"
			}
			if action == "mixed-index-overflow" {
				spec.GlobalAction, spec.WorkspaceAction = "uninitialized", "stderr-overflow"
			}
			cfg, _ := sendContextProcess(t, spec)
			filter := sendContextFilter(t.TempDir())
			if action != "mixed-index-overflow" {
				filter.PreferSameProject = false
			}
			prompt := "authentication task"
			result, search, _ := InjectContextFromQuery(t.Context(), prompt, cfg, filter, DefaultInjectConfig())
			if result.Success || search.Success || result.ModifiedPrompt != prompt || result.InjectedContext != "" || len(search.Hits) != 0 {
				t.Fatalf("failed search changed task or published history: %+v %+v", result, search)
			}
			if strings.Contains(result.Error, "PRIVATE_FIXTURE_DIAGNOSTIC") {
				t.Fatalf("subprocess diagnostic escaped into send receipt: %s", result.Error)
			}
			if strings.Contains(action, "overflow") && !strings.Contains(result.Error, "capture budget") {
				t.Fatalf("lost bounded-output failure: %s", result.Error)
			}
		})
	}
}

func TestSendContextSkipsQueriesThatCannotBeInjected(t *testing.T) {
	for _, mode := range []string{"disabled", "full-window", "no-keywords", "empty-index", "no-matches"} {
		t.Run(mode, func(t *testing.T) {
			spec := sendContextFixture{Global: `{"hits":[]}`}
			if mode == "empty-index" {
				spec.GlobalAction = "uninitialized"
			}
			if mode == "no-matches" {
				spec.GlobalAction = "empty"
			}
			cfg, logs := sendContextProcess(t, spec)
			inject, prompt := DefaultInjectConfig(), "authentication task"
			if mode == "disabled" {
				cfg.Enabled = false
			}
			if mode == "full-window" {
				inject.CurrentContextPct = inject.SkipThreshold
			}
			if mode == "no-keywords" {
				prompt = "the and a"
			}
			result, search, _ := InjectContextFromQuery(t.Context(), prompt, cfg, FilterConfig{}, inject)
			if !result.Success || !search.Success || result.ModifiedPrompt != prompt || result.Metadata.ItemsInjected != 0 || result.Metadata.SkippedReason == "" {
				t.Fatalf("skip lost task or reason: %+v %+v", result, search)
			}
			if mode != "empty-index" && mode != "no-matches" {
				entries, err := os.ReadDir(logs)
				if err != nil || len(entries) != 0 {
					t.Fatalf("skipped query launched a subprocess: %v %v", entries, err)
				}
			}
		})
	}
}

func TestSendContextRedactsQueryAndHistoricalSnippets(t *testing.T) {
	secret := "fixture-only-not-a-real-credential"
	body := sendContextBody("", "password="+secret, nil)
	cfg, logs := sendContextProcess(t, sendContextFixture{Global: body})
	prompt := "authentication password=" + secret
	for _, format := range []InjectionFormat{FormatMarkdown, FormatMinimal, FormatStructured} {
		inject := DefaultInjectConfig()
		inject.Format = format
		result, query, _ := InjectContextFromQuery(t.Context(), prompt, cfg, FilterConfig{}, inject)
		if !result.Success || !strings.HasSuffix(result.ModifiedPrompt, prompt) || !strings.Contains(result.InjectedContext, "REDACTED") || !strings.Contains(result.InjectedContext, "historical data") || strings.Contains(result.InjectedContext, secret) || strings.Contains(query.Query, secret) {
			t.Fatalf("redaction/framing/query contract: %+v %+v", result, query)
		}
		raw, err := os.ReadFile(filepath.Join(logs, "global.json"))
		if err != nil || strings.Contains(string(raw), secret) {
			t.Fatalf("secret reached search arguments: %s %v", raw, err)
		}
	}
}

func TestSendContextIndexedFilteringOverridesArchiveNames(t *testing.T) {
	workspace := filepath.Join(t.TempDir(), "project")
	now := time.Now().UTC()
	var hits []CASSHit
	raw := fmt.Sprintf(`[{"source_path":"/project/2099/01/01/stale.jsonl","workspace":%q,"score":1,"content":"STALE","created_at":%q},
{"source_path":"/project/1990/01/01/foreign.jsonl","workspace":%q,"score":1,"content":"FOREIGN","created_at":%q},
{"source_path":"/archive/1990/01/01/local.jsonl","workspace":%q,"score":1,"content":"LOCAL","created_at":%q}]`,
		workspace, now.AddDate(0, 0, -90).Format(time.RFC3339), filepath.Join(t.TempDir(), "project"), now.Format(time.RFC3339), workspace, now.Format(time.RFC3339))
	if err := json.Unmarshal([]byte(raw), &hits); err != nil {
		t.Fatal(err)
	}
	filter := sendContextFilter(workspace)
	result := FilterResults(hits, filter)
	if result.RemovedByAge != 1 || len(result.Hits) != 1 || result.Hits[0].Content != "LOCAL" {
		t.Fatalf("wrong indexed selection: %+v", result)
	}
	filter.PreferSameProject = false
	result = FilterResults(hits, filter)
	if len(result.Hits) != 1 || result.Hits[0].Content != "FOREIGN" {
		t.Fatalf("disabled preference reordered equal hits: %+v", result)
	}
}

func TestSendContextRetainsExistingPipeDrainDeadline(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("inherited-pipe fixture requires a POSIX shell")
	}
	for _, body := range []string{"exec sleep 4", "sleep 4 &\nwait"} {
		binary := filepath.Join(t.TempDir(), "cass")
		if err := os.WriteFile(binary, []byte("#!/bin/sh\n"+body+"\n"), 0o700); err != nil {
			t.Fatal(err)
		}
		cfg := DefaultCASSConfig()
		cfg.BinaryPath, cfg.Timeout = binary, time.Second
		start := time.Now()
		result, search, _ := InjectContextFromQuery(t.Context(), "authentication task", cfg, FilterConfig{}, DefaultInjectConfig())
		if elapsed := time.Since(start); elapsed >= 3*time.Second {
			t.Fatalf("one-second lookup exceeded the existing send drain bound: %v", elapsed)
		}
		if result.Success || search.Success || result.ModifiedPrompt != "authentication task" || result.InjectedContext != "" || !strings.Contains(search.Error, "timed out after 1s") {
			t.Fatalf("deadline changed the task or lost its failure: %+v %+v", result, search)
		}
	}
}

func TestSendContextContentAndIdentityContracts(t *testing.T) {
	for _, mode := range []string{"full-content", "blank-content", "invalid-time", "wrong-project", "dry-run"} {
		t.Run(mode, func(t *testing.T) {
			workspace := t.TempDir()
			var body map[string]interface{}
			if err := json.Unmarshal([]byte(sendContextBody(workspace, "SNIPPET_HISTORY", nil)), &body); err != nil {
				t.Fatal(err)
			}
			hit := body["hits"].([]interface{})[0].(map[string]interface{})
			want := "SNIPPET_HISTORY"
			switch mode {
			case "full-content":
				hit["content"], want = "FULL_HISTORY", "FULL_HISTORY"
			case "blank-content":
				hit["content"] = " \n\t"
			case "invalid-time":
				hit["created_at"] = "not-an-indexed-date"
			case "wrong-project":
				hit["workspace"] = workspace + "-foreign"
			}
			raw, err := json.Marshal(body)
			if err != nil {
				t.Fatal(err)
			}
			spec := sendContextFixture{Global: string(raw), Workspace: string(raw)}
			filter := FilterConfig{}
			if mode == "wrong-project" {
				spec.Global = `{"hits":[]}`
				filter = sendContextFilter(workspace)
			}
			cfg, _ := sendContextProcess(t, spec)
			inject := DefaultInjectConfig()
			inject.DryRun = mode == "dry-run"
			result, search, _ := InjectContextFromQuery(t.Context(), "original task", cfg, filter, inject)
			switch mode {
			case "invalid-time":
				if result.Success || search.Success || result.ModifiedPrompt != "original task" || result.InjectedContext != "" {
					t.Fatalf("malformed indexed evidence was injected: %+v %+v", result, search)
				}
			case "wrong-project":
				if !result.Success || len(search.Hits) != 0 || result.InjectedContext != "" || result.ModifiedPrompt != "original task" {
					t.Fatalf("scoped search silently relabeled foreign history: %+v %+v", result, search)
				}
			default:
				if !result.Success || !strings.Contains(result.InjectedContext, want) {
					t.Fatalf("lost content precedence: %+v", result)
				}
				if mode == "full-content" && strings.Contains(result.InjectedContext, "SNIPPET_HISTORY") {
					t.Fatalf("snippet overrode full content: %s", result.InjectedContext)
				}
				if mode == "dry-run" && result.ModifiedPrompt != "original task" {
					t.Fatal("dry-run modified the task")
				}
			}
		})
	}
}
