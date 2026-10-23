package cli

// Tests for --with-cass / --no-cass send-time CASS injection wiring (C10,
// bd-ws2-wire-or-delete-ykmcz.11): flag/config precedence mirrors
// --with-memory, [cass.context] keys really parameterize the engines (the
// G2 liveness claims are honest), and the degraded path records a skip.

import (
	"context"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/Dicklesworthstone/ntm/internal/cass"
	"github.com/Dicklesworthstone/ntm/internal/config"
	"github.com/Dicklesworthstone/ntm/internal/robot"
)

func cassTestConfig(topEnabled, ctxEnabled bool) *config.Config {
	cfg := config.Default()
	cfg.CASS.Enabled = topEnabled
	cfg.CASS.Context.Enabled = ctxEnabled
	return cfg
}

func TestResolveSendCASSEnabled_Precedence(t *testing.T) {
	cases := []struct {
		name     string
		withFlag bool
		noFlag   bool
		cfg      *config.Config
		want     bool
	}{
		{"default off", false, false, cassTestConfig(true, false), false},
		{"flag turns on", true, false, cassTestConfig(true, false), true},
		{"config default on", false, false, cassTestConfig(true, true), true},
		{"context on but cass disabled", false, false, cassTestConfig(false, true), false},
		{"no-cass overrides config on", false, true, cassTestConfig(true, true), false},
		{"no-cass overrides explicit with-cass", true, true, cassTestConfig(true, true), false},
		{"nil config flag on", true, false, nil, true},
		{"nil config default off", false, false, nil, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := resolveSendCASSEnabled(tc.withFlag, tc.noFlag, tc.cfg); got != tc.want {
				t.Fatalf("resolveSendCASSEnabled(%v,%v) = %v, want %v", tc.withFlag, tc.noFlag, got, tc.want)
			}
		})
	}
}

// TestRobotSendCASSOptions_ConfigKeysParameterizeEngine flips every claimed
// [cass.context] key (plus [cass] binary_path/timeout) and observes it land
// in the robot engine configs — the liveness claims are readers in fact.
func TestRobotSendCASSOptions_ConfigKeysParameterizeEngine(t *testing.T) {
	cfg := cassTestConfig(true, true)
	cfg.CASS.BinaryPath = "/opt/bin/cass"
	cfg.CASS.Timeout = 7
	cfg.CASS.Context.MaxSessions = 9
	cfg.CASS.Context.LookbackDays = 14
	cfg.CASS.Context.MaxTokens = 1234
	cfg.CASS.Context.MinRelevance = 0.42
	cfg.CASS.Context.SkipIfContextAbove = 77
	cfg.CASS.Context.PreferSameProject = false

	enabled, query, filter, inject := robotSendCASSOptions(false, false, cfg)
	if !enabled {
		t.Fatal("enabled = false, want true via [cass.context] enabled")
	}
	if query.BinaryPath != "/opt/bin/cass" {
		t.Errorf("BinaryPath = %q", query.BinaryPath)
	}
	if query.Timeout != 7*time.Second {
		t.Errorf("Timeout = %v", query.Timeout)
	}
	if query.MaxResults != 9 || filter.MaxItems != 9 {
		t.Errorf("max_sessions not applied: query %d filter %d", query.MaxResults, filter.MaxItems)
	}
	if query.MaxAgeDays != 14 || filter.MaxAgeDays != 14 {
		t.Errorf("lookback_days not applied: query %d filter %d", query.MaxAgeDays, filter.MaxAgeDays)
	}
	if inject.MaxTokens != 1234 {
		t.Errorf("max_tokens not applied: %d", inject.MaxTokens)
	}
	if query.MinRelevance != 0.42 || filter.MinRelevance != 0.42 {
		t.Errorf("min_relevance not applied: query %v filter %v", query.MinRelevance, filter.MinRelevance)
	}
	if inject.SkipThreshold != 77 {
		t.Errorf("skip_if_context_above not applied: %d", inject.SkipThreshold)
	}
	if query.PreferSameProject || filter.PreferSameProject {
		t.Error("prefer_same_project=false not applied")
	}

	// [cass] enabled=false degrades to a recorded skip inside the engine
	// (query disabled) rather than vetoing an explicit --with-cass.
	cfg.CASS.Enabled = false
	enabled, query, _, _ = robotSendCASSOptions(true, false, cfg)
	if !enabled {
		t.Fatal("explicit --with-cass must stay enabled (degrades in engine)")
	}
	if query.Enabled {
		t.Fatal("query.Enabled should be false when [cass] enabled=false")
	}
}

func TestSendCASSInjectionConfigs_MirrorsRobotMapping(t *testing.T) {
	cfg := cassTestConfig(true, true)
	cfg.CASS.Timeout = 7
	cfg.CASS.Context.MaxSessions = 3
	cfg.CASS.Context.MaxTokens = 800
	cfg.CASS.Context.MinRelevance = 0.5

	enabled, query, filter, inject := sendCASSInjectionConfigs(false, false, cfg)
	if !enabled {
		t.Fatal("enabled = false, want true via config default")
	}
	if query.Timeout != 7*time.Second {
		t.Errorf("timeout not applied: %v", query.Timeout)
	}
	if query.MaxResults != 3 || filter.MaxItems != 3 {
		t.Errorf("max_sessions not applied: %d/%d", query.MaxResults, filter.MaxItems)
	}
	if inject.MaxTokens != 800 {
		t.Errorf("max_tokens not applied: %d", inject.MaxTokens)
	}
	if query.MinRelevance != 0.5 || filter.MinRelevance != 0.5 {
		t.Errorf("min_relevance not applied: %v/%v", query.MinRelevance, filter.MinRelevance)
	}
	if _, _, _, inj := sendCASSInjectionConfigs(true, false, nil); inj.MaxTokens == 0 {
		t.Error("nil config should fall back to engine defaults")
	}
}

func TestSendCASSInjectionInfo_DegradedRecordsSkip(t *testing.T) {
	info := sendCASSInjectionInfo(cass.InjectionResult{
		Success: false,
		Error:   "CASS query failed: cass command not found",
		Metadata: cass.InjectionMetadata{
			Enabled: true,
		},
	}, "rate limiting", nil)
	if info.SkippedReason == "" || !strings.Contains(info.SkippedReason, "not found") {
		t.Fatalf("degraded cobra envelope must record the skip: %+v", info)
	}
}

// Exercise the same config, query, injection, and response path used by ntm
// send. An optional search must relinquish dispatch on its configured deadline,
// even when a subprocess keeps the search command's output pipes open.
func TestSendCASSConfiguredDeadlineRecordsSkippedEnrichment(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("CASS process fixture requires a POSIX shell")
	}
	for _, fixture := range []struct {
		name string
		body string
	}{
		{name: "hung-search", body: "exec sleep 4"},
		{name: "inherited-output", body: "sleep 4 &\nwait"},
	} {
		t.Run(fixture.name, func(t *testing.T) {
			binary := filepath.Join(t.TempDir(), "cass")
			if err := os.WriteFile(binary, []byte("#!/bin/sh\n"+fixture.body+"\n"), 0o700); err != nil {
				t.Fatal(err)
			}
			cfg := cassTestConfig(true, true)
			cfg.CASS.BinaryPath = binary
			cfg.CASS.Timeout = 1
			enabled, query, filter, inject := sendCASSInjectionConfigs(true, false, cfg)
			if !enabled {
				t.Fatal("explicit CASS enrichment was not enabled")
			}
			const prompt = "Investigate database transaction contention"
			started := time.Now()
			result, search, filtered := cass.InjectContextFromQuery(t.Context(), prompt, query, filter, inject)
			elapsed := time.Since(started)
			if elapsed >= 3*time.Second {
				t.Fatalf("optional enrichment held dispatch for %v with a 1s timeout", elapsed)
			}
			if search.Success || !strings.Contains(search.Error, "timed out after 1s") {
				t.Fatalf("search did not report its configured deadline: %+v", search)
			}
			if result.Success || result.ModifiedPrompt != prompt || result.InjectedContext != "" || result.Metadata.ItemsInjected != 0 {
				t.Fatalf("timed-out enrichment returned an injected replacement prompt: %+v", result)
			}
			info := sendCASSInjectionInfo(result, search.Query, filtered.Hits)
			if !strings.Contains(info.SkippedReason, "timed out after 1s") {
				t.Fatalf("send response lost the enrichment timeout: %+v", info)
			}
		})
	}
}

func TestSendCommand_CASSFlagsRegistered(t *testing.T) {
	cmd := newSendCmd()
	for _, name := range []string{"with-cass", "no-cass"} {
		if cmd.Flags().Lookup(name) == nil {
			t.Errorf("ntm send missing --%s", name)
		}
	}
}

func TestSendCASSInjectionInfo_UsesIndexedAge(t *testing.T) {
	indexed := time.Now().UTC().Add(-72 * time.Hour)
	for _, format := range []cass.InjectionFormat{cass.FormatMarkdown, cass.FormatMinimal, cass.FormatStructured} {
		inject := cass.DefaultInjectConfig()
		inject.Format = format
		var hit cass.CASSHit
		raw, err := json.Marshal(map[string]interface{}{
			"source_path": "/archive/2099/01/01/renamed.jsonl",
			"created_at":  indexed, "content": "INDEXED_HISTORY",
		})
		if err != nil {
			t.Fatal(err)
		}
		if err := json.Unmarshal(raw, &hit); err != nil {
			t.Fatal(err)
		}
		filtered := cass.FilterResults([]cass.CASSHit{hit}, cass.FilterConfig{MaxAgeDays: 30})
		result := cass.InjectContext("original task", filtered.Hits, inject)
		info := sendCASSInjectionInfo(result, "query", filtered.Hits)
		if !result.Success || len(info.Sources) != 1 || info.Sources[0].AgeDays != 3 || !strings.Contains(result.ModifiedPrompt, "INDEXED_HISTORY") {
			t.Fatalf("format %s lost indexed receipt evidence: result=%+v info=%+v", format, result, info)
		}
	}
}

// Keep the real send command, strict session-project resolver, CASS process
// runner, result ranking, formatting and dispatch. Only the external tmux and
// CASS executables are deterministic recording fixtures.
func sendCASSCommandFixture(t *testing.T, action string) (projectRoot, tmuxLog, cassDir string) {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("recording external tools require a POSIX shell")
	}
	projectRoot, tmuxLog = workflowCommandTmuxFixture(t, "stable")
	cassDir = t.TempDir()
	t.Setenv("NTM_SEND_CASS_FIXTURE_DIR", cassDir)
	t.Setenv("NTM_SEND_CASS_FIXTURE_ACTION", action)
	const script = `#!/bin/sh
scope=global
for arg in "$@"; do
  case "$arg" in --workspace=*) scope=workspace ;; esac
done
printf '%s\n' "$@" > "$NTM_SEND_CASS_FIXTURE_DIR/$scope-args"
if [ "$NTM_SEND_CASS_FIXTURE_ACTION" = wait ]; then exec sleep 8; fi
cat "$NTM_SEND_CASS_FIXTURE_DIR/$scope.json"
`
	binary := filepath.Join(cassDir, "cass")
	if err := os.WriteFile(binary, []byte(script), 0700); err != nil {
		t.Fatal(err)
	}
	for _, scope := range []string{"global", "workspace"} {
		workspace, marker := "/unrelated/project", "GLOBAL_ONLY_HISTORY"
		if scope == "workspace" {
			workspace, marker = projectRoot, "TARGET_PROJECT_HISTORY"
		}
		data, err := json.Marshal(map[string]interface{}{"hits": []interface{}{map[string]interface{}{
			"source_path": "/archive/" + scope + ".jsonl", "line_number": 7,
			"workspace": workspace, "content": marker, "score": 1.0, "agent": "claude",
		}}})
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(cassDir, scope+".json"), data, 0600); err != nil {
			t.Fatal(err)
		}
	}
	cfg.CASS.Enabled = true
	cfg.CASS.BinaryPath = binary
	cfg.CASS.Timeout = 10
	cfg.CASS.Context.MaxSessions = 1
	cfg.CASS.Context.PreferSameProject = true
	return projectRoot, tmuxLog, cassDir
}

func runCASSFixtureSend(t *testing.T, ctx context.Context, extra ...string) (SendResult, error) {
	t.Helper()
	cmd := newSendCmd()
	cmd.SilenceUsage, cmd.SilenceErrors = true, true
	cmd.SetOut(io.Discard)
	cmd.SetErr(io.Discard)
	args := []string{"evidence-run", "Investigate database transaction contention", "--pane", "%91", "--with-cass", "--no-cass-check", "--no-hooks"}
	cmd.SetArgs(append(args, extra...))
	stdout, runErr := captureStdout(t, func() error { return cmd.ExecuteContext(ctx) })
	var result SendResult
	if err := json.Unmarshal([]byte(stdout), &result); err != nil {
		t.Fatalf("send did not emit one JSON result: %v, output=%q, command error=%v", err, stdout, runErr)
	}
	return result, runErr
}

// Two real transport identities deliberately share the same window-local
// index. Only the selected physical pane's live gauge can affect enrichment.
func sendCASSCapacityFixture(t *testing.T, mode string) (projectRoot, tmuxLog, cassDir string) {
	t.Helper()
	projectRoot, tmuxLog, cassDir = sendCASSCommandFixture(t, "")
	t.Setenv("NTM_SEND_CASS_BASE_TMUX", os.Getenv("NTM_TMUX_BINARY"))
	t.Setenv("NTM_SEND_CASS_CAPACITY_MODE", mode)
	const script = `#!/bin/sh
case "$1" in
  list-panes)
    printf '%s\n' "$*" >> "$NTM_WORKFLOW_TEST_ROOT/tmux-calls"
    echo '%91_NTM_SEP_1_NTM_SEP_evidence-run__gmi_1_NTM_SEP_gemini_NTM_SEP_120_NTM_SEP_40_NTM_SEP_1_NTM_SEP_0_NTM_SEP_0_NTM_SEP_gmi_NTM_SEP__NTM_SEP__NTM_SEP_0'
    echo '%92_NTM_SEP_1_NTM_SEP_evidence-run__gmi_2_NTM_SEP_gemini_NTM_SEP_120_NTM_SEP_40_NTM_SEP_0_NTM_SEP_0_NTM_SEP_1_NTM_SEP_gmi_NTM_SEP__NTM_SEP__NTM_SEP_0'
    ;;
  capture-pane)
    printf '%s\n' "$*" >> "$NTM_WORKFLOW_TEST_ROOT/tmux-calls"
    if [ "$NTM_SEND_CASS_CAPACITY_MODE" = wait ] && [ ! -f "$NTM_WORKFLOW_TEST_ROOT/capacity-started" ]; then
      : > "$NTM_WORKFLOW_TEST_ROOT/capacity-started"
      exec sleep 30
    fi
    case "$*" in *'%92'*) cat "$NTM_WORKFLOW_TEST_ROOT/hot-frame" ;; *) cat "$NTM_WORKFLOW_TEST_ROOT/cool-frame" ;; esac
    ;;
  *) exec "$NTM_SEND_CASS_BASE_TMUX" "$@" ;;
esac
`
	for name, content := range map[string]string{
		"capacity-tmux": script,
		"cool-frame":    "> Type your message or @path/to/file\n/model context\ngemini-pro 20% used\n",
		"hot-frame":     "> Type your message or @path/to/file\n/model context\ngemini-pro 90% used\n",
	} {
		if err := os.WriteFile(filepath.Join(projectRoot, name), []byte(content), 0700); err != nil {
			t.Fatal(err)
		}
	}
	t.Setenv("NTM_TMUX_BINARY", filepath.Join(projectRoot, "capacity-tmux"))
	return projectRoot, tmuxLog, cassDir
}

func TestSendCASSCommandRespectsSelectedPaneCapacity(t *testing.T) {
	for _, mode := range []string{"cool-selected", "hot-selected", "shared-broadcast", "quota-unknown"} {
		t.Run(mode, func(t *testing.T) {
			projectRoot, tmuxLog, cassDir := sendCASSCapacityFixture(t, "")
			var args []string
			wantDelivered, skip := 1, false
			switch mode {
			case "hot-selected":
				args, skip = []string{"--pane", "%92"}, true
			case "shared-broadcast":
				args, wantDelivered, skip = []string{"--pane", "", "--all"}, 2, true
			case "quota-unknown":
				frame := "> Type your message or @path/to/file\n/model quota\ngemini-pro 99% used\n"
				if err := os.WriteFile(filepath.Join(projectRoot, "cool-frame"), []byte(frame), 0600); err != nil {
					t.Fatal(err)
				}
			}
			result, err := runCASSFixtureSend(t, t.Context(), args...)
			if err != nil || !result.Success || result.Delivered != wantDelivered || result.CASSInjection == nil {
				t.Fatalf("capacity-aware send failed: result=%+v error=%v", result, err)
			}
			log, err := os.ReadFile(tmuxLog)
			if err != nil {
				t.Fatal(err)
			}
			if skip {
				if result.CASSInjection.ItemsInjected != 0 || !strings.Contains(result.CASSInjection.SkippedReason, "context at 90%") {
					t.Fatalf("hot target did not suppress history: %+v", result.CASSInjection)
				}
				for _, scope := range []string{"global", "workspace"} {
					if _, err := os.Stat(filepath.Join(cassDir, scope+"-args")); !os.IsNotExist(err) {
						t.Fatalf("hot target launched %s search: %v", scope, err)
					}
				}
				if strings.Count(string(log), "Investigate database transaction contention") != wantDelivered {
					t.Fatalf("capacity skip did not preserve delivery to all selected panes: %s", log)
				}
			} else {
				if result.CASSInjection.ItemsInjected != 1 || result.CASSInjection.SkippedReason != "" {
					t.Fatalf("unselected hot pane or unknown gauge suppressed enrichment: %+v", result.CASSInjection)
				}
				payload, err := os.ReadFile(filepath.Join(projectRoot, "delivered-prompt"))
				if err != nil || !strings.Contains(string(payload), "TARGET_PROJECT_HISTORY") {
					t.Fatalf("missing enriched delivery: %s (%v)", payload, err)
				}
				if strings.Contains(string(log), "capture-pane -t %92") {
					t.Fatalf("inspected unselected hot pane: %s", log)
				}
			}
		})
	}
}

func TestSendCASSCommandCapacityMeasurementLifecycle(t *testing.T) {
	for _, mode := range []string{"cancel", "deadline", "optional-probe-timeout"} {
		t.Run(mode, func(t *testing.T) {
			projectRoot, tmuxLog, cassDir := sendCASSCapacityFixture(t, "wait")
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			var watcherDone chan struct{}
			if mode == "deadline" {
				var stop context.CancelFunc
				ctx, stop = context.WithTimeout(ctx, time.Second)
				defer stop()
			} else if mode == "cancel" {
				watcherDone = make(chan struct{})
				go func() {
					defer close(watcherDone)
					ticker := time.NewTicker(5 * time.Millisecond)
					defer ticker.Stop()
					for {
						select {
						case <-ctx.Done():
							return
						case <-ticker.C:
							if _, err := os.Stat(filepath.Join(projectRoot, "capacity-started")); err == nil {
								cancel()
								return
							}
						}
					}
				}()
			}
			started := time.Now()
			result, runErr := runCASSFixtureSend(t, ctx)
			cancel()
			if watcherDone != nil {
				<-watcherDone
			}
			if elapsed := time.Since(started); elapsed > 5*time.Second {
				t.Fatalf("capacity probe held send for %v", elapsed)
			}
			if _, err := os.Stat(filepath.Join(projectRoot, "capacity-started")); err != nil {
				t.Fatalf("capacity probe never started: %v", err)
			}
			if mode == "optional-probe-timeout" {
				if runErr != nil || !result.Success || result.Delivered != 1 || result.CASSInjection == nil || result.CASSInjection.ItemsInjected != 1 {
					t.Fatalf("optional measurement deadline became a delivery gate: result=%+v error=%v", result, runErr)
				}
				return
			}
			if runErr == nil || result.Success || result.Delivered != 0 || result.ErrorCode != robot.ErrCodeTimeout || !strings.Contains(result.Error, "during CASS capacity measurement") {
				t.Fatalf("capacity probe lost caller cancellation: result=%+v error=%v", result, runErr)
			}
			for _, scope := range []string{"global", "workspace"} {
				if _, err := os.Stat(filepath.Join(cassDir, scope+"-args")); !os.IsNotExist(err) {
					t.Fatalf("canceled probe launched CASS: %v", err)
				}
			}
			log, err := os.ReadFile(tmuxLog)
			if err != nil {
				t.Fatal(err)
			}
			for _, operation := range []string{"load-buffer", "paste-buffer", "send-keys"} {
				if strings.Contains(string(log), operation) {
					t.Fatalf("canceled measurement reached delivery: %s", log)
				}
			}
		})
	}
}

func TestSendCASSCommandUsesTargetProjectFromDifferentWorkspace(t *testing.T) {
	for _, mode := range []string{"target-project", "preference-disabled", "unavailable-project"} {
		t.Run(mode, func(t *testing.T) {
			projectRoot, _, cassDir := sendCASSCommandFixture(t, "")
			// The caller has the same directory basename, but is a different
			// project. That cannot become CASS's authoritative workspace.
			caller := filepath.Join(t.TempDir(), filepath.Base(projectRoot))
			if err := os.MkdirAll(caller, 0700); err != nil {
				t.Fatal(err)
			}
			t.Chdir(caller)
			if mode == "preference-disabled" {
				cfg.CASS.Context.PreferSameProject = false
			}
			if mode == "unavailable-project" {
				// Let all normal tmux operations reach the recorder, but make
				// the session's current-path evidence unusable.
				t.Setenv("NTM_SEND_CASS_BASE_TMUX", os.Getenv("NTM_TMUX_BINARY"))
				wrapper := filepath.Join(cassDir, "tmux")
				const script = `#!/bin/sh
case "$*" in *'#{pane_current_path}'*) printf 'relative-project\n' ;;
*) exec "$NTM_SEND_CASS_BASE_TMUX" "$@" ;;
esac
`
				if err := os.WriteFile(wrapper, []byte(script), 0700); err != nil {
					t.Fatal(err)
				}
				t.Setenv("NTM_TMUX_BINARY", wrapper)
			}
			result, err := runCASSFixtureSend(t, t.Context())
			if err != nil || !result.Success || result.Delivered != 1 || result.CASSInjection == nil || result.CASSInjection.ItemsInjected != 1 {
				t.Fatalf("send did not deliver enriched prompt: result=%+v error=%v", result, err)
			}
			payload, err := os.ReadFile(filepath.Join(projectRoot, "delivered-prompt"))
			if err != nil {
				t.Fatal(err)
			}
			workspaceArgs, workspaceErr := os.ReadFile(filepath.Join(cassDir, "workspace-args"))
			if mode == "target-project" {
				if workspaceErr != nil || !strings.Contains(string(workspaceArgs), "--workspace="+projectRoot+"\n") || strings.Contains(string(workspaceArgs), caller) {
					t.Fatalf("lookup did not use target identity: args=%q error=%v", workspaceArgs, workspaceErr)
				}
				if !strings.Contains(string(payload), "TARGET_PROJECT_HISTORY") || strings.Contains(string(payload), "GLOBAL_ONLY_HISTORY") {
					t.Fatalf("target history outside the global limit was not selected: %s", payload)
				}
			} else {
				if !os.IsNotExist(workspaceErr) || !strings.Contains(string(payload), "GLOBAL_ONLY_HISTORY") || strings.Contains(string(payload), "TARGET_PROJECT_HISTORY") {
					t.Fatalf("global fallback/preference policy changed: args=%q error=%v payload=%s", workspaceArgs, workspaceErr, payload)
				}
			}
			if mode == "unavailable-project" && !strings.Contains(strings.Join(result.Warnings, " "), "CASS project preference unavailable") {
				t.Fatalf("lost degraded project preference warning: %+v", result)
			}
		})
	}
}

func TestSendCASSCommandStopsRetrievalWithoutDelivery(t *testing.T) {
	for _, mode := range []string{"cancel", "deadline"} {
		t.Run(mode, func(t *testing.T) {
			_, tmuxLog, cassDir := sendCASSCommandFixture(t, "wait")
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			var watcherDone chan struct{}
			if mode == "deadline" {
				var deadlineCancel context.CancelFunc
				ctx, deadlineCancel = context.WithTimeout(ctx, time.Second)
				defer deadlineCancel()
			} else {
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
							_, globalErr := os.Stat(filepath.Join(cassDir, "global-args"))
							_, localErr := os.Stat(filepath.Join(cassDir, "workspace-args"))
							if globalErr == nil && localErr == nil {
								cancel()
								return
							}
						}
					}
				}()
			}
			start := time.Now()
			result, runErr := runCASSFixtureSend(t, ctx)
			cancel()
			if watcherDone != nil {
				<-watcherDone
			}
			if elapsed := time.Since(start); elapsed >= 3*time.Second {
				t.Fatalf("send ignored caller stop and waited for CASS's 10s budget: %v", elapsed)
			}
			if runErr == nil || result.Success || result.Delivered != 0 || result.ErrorCode != robot.ErrCodeTimeout || !strings.Contains(result.Error, "during CASS context injection") {
				t.Fatalf("command lost cancellation or sent a prompt: result=%+v error=%v", result, runErr)
			}
			for _, scope := range []string{"global", "workspace"} {
				if _, err := os.Stat(filepath.Join(cassDir, scope+"-args")); err != nil {
					t.Fatalf("test did not cancel the in-flight %s search: %v", scope, err)
				}
			}
			log, err := os.ReadFile(tmuxLog)
			if err != nil {
				t.Fatal(err)
			}
			for _, operation := range []string{"load-buffer", "paste-buffer", "send-keys"} {
				if strings.Contains(string(log), operation) {
					t.Fatalf("canceled send reached %s: %s", operation, log)
				}
			}
		})
	}
}

func TestSendCASSCommandLookupTimeoutStillDeliversOriginalTask(t *testing.T) {
	_, tmuxLog, _ := sendCASSCommandFixture(t, "wait")
	cfg.CASS.Timeout = 1
	result, err := runCASSFixtureSend(t, t.Context())
	if err != nil || !result.Success || result.Delivered != 1 || result.CASSInjection == nil || result.CASSInjection.ItemsInjected != 0 || !strings.Contains(result.CASSInjection.SkippedReason, "timed out after 1s") {
		t.Fatalf("optional lookup timeout became a delivery gate: result=%+v error=%v", result, err)
	}
	// The original one-line task uses literal send-keys; enriched multiline
	// prompts use the fixture's load-buffer capture instead.
	log, err := os.ReadFile(tmuxLog)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(log), "Investigate database transaction contention") || strings.Contains(string(log), "_HISTORY") {
		t.Fatalf("optional failure changed the delivered task: %s", log)
	}
}
