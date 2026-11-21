package serve

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Dicklesworthstone/ntm/internal/agentsession"
	"github.com/Dicklesworthstone/ntm/internal/config"
	"github.com/Dicklesworthstone/ntm/internal/session"
	"github.com/Dicklesworthstone/ntm/internal/tmux"
)

func savedRestoreHTTPFixture(t *testing.T) (string, *session.SessionState) {
	t.Helper()
	dir := t.TempDir()
	t.Setenv("HOME", dir)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(dir, "config"))
	t.Setenv("XDG_DATA_HOME", filepath.Join(dir, "data"))
	t.Setenv("NTM_RESTORE_FIXTURE", dir)
	t.Setenv("NTM_RESTORE_FAIL_SEND", "")
	t.Setenv("NTM_RESTORE_RACE_PANE", "")
	t.Setenv("NTM_RESTORE_RACE_PHASE", "")
	script := `#!/bin/sh
printf '%s\n' "$*" >> "$NTM_RESTORE_FIXTURE/calls"
case "$1" in
  -V) echo 'tmux 3.5a' ;;
  has-session)
    if [ ! -f "$NTM_RESTORE_FIXTURE/exists" ]; then
      echo "can't find session" >&2
      exit 1
    fi ;;
  new-session) : > "$NTM_RESTORE_FIXTURE/exists" ;;
  list-windows) echo 0 ;;
  list-panes)
    for i in 0 1; do
      type=''
      if [ -f "$NTM_RESTORE_FIXTURE/type%$i" ]; then type=$(cat "$NTM_RESTORE_FIXTURE/type%$i"); fi
      printf '%%%s_NTM_SEP_%s_NTM_SEP__NTM_SEP_bash_NTM_SEP_80_NTM_SEP_24_NTM_SEP_1_NTM_SEP_9999998_NTM_SEP_0_NTM_SEP_%s_NTM_SEP__NTM_SEP__NTM_SEP_0\n' "$i" "$i" "$type"
    done ;;
  display-message)
    if [ "$5" = '[#{@ntm_agent_type}]' ]; then
      type=''
      if [ -f "$NTM_RESTORE_FIXTURE/type$4" ]; then type=$(cat "$NTM_RESTORE_FIXTURE/type$4"); fi
      printf '[%s]\n' "$type"
    else
      printf '%s\n' "$NTM_RESTORE_FIXTURE"
    fi ;;
  if-shell)
    target="$4"
    cond="$5"
    true_cmd="$6"
    case "$cond" in
      *'#{==:#{pane_pid},'*'#{==:#{pane_dead},0}'*'#{==:#{pane_current_command},bash}'*) ;;
      *)
        echo "fixture missing atomic pane guard condition: $cond" >&2
        exit 99 ;;
    esac
    phase="other"
    case "$true_cmd" in
      *'@ntm_agent_launch'*) phase="metadata" ;;
      *"'send-keys'"*"'Enter'") phase="enter" ;;
      *'send-keys'*|*'paste-buffer'*) phase="input" ;;
    esac
    if [ -n "$NTM_RESTORE_RACE_PANE" ] && [ "$target" = "$NTM_RESTORE_RACE_PANE" ] && [ "$phase" = "$NTM_RESTORE_RACE_PHASE" ]; then
      printf 'ntm_pane_identity_changed\n'
      exit 0
    fi
    eval "exec \"\$0\" $true_cmd" ;;
  load-buffer)
    cat > "$NTM_RESTORE_FIXTURE/buf-$3" ;;
  paste-buffer)
    pane="$7"
    buf="$5"
    if [ "$NTM_RESTORE_FAIL_SEND" = 1 ]; then
      echo 'fixture delivery failed' >&2
      exit 2
    fi
    if [ -f "$NTM_RESTORE_FIXTURE/buf-$buf" ]; then
      payload=$(cat "$NTM_RESTORE_FIXTURE/buf-$buf")
      printf 'pasted %s %s\n' "$pane" "$payload" >> "$NTM_RESTORE_FIXTURE/calls"
    fi ;;
  delete-buffer) ;;
  set-option)
    if [ "$5" = '@ntm_agent_type' ]; then printf '%s\n' "$6" > "$NTM_RESTORE_FIXTURE/type$4"; fi
    if [ "$5" = '@ntm_agent_launch' ]; then printf '%s\n' "$6" > "$NTM_RESTORE_FIXTURE/launch$4"; fi ;;
  show-options)
    if [ -f "$NTM_RESTORE_FIXTURE/launch$5" ]; then
      cat "$NTM_RESTORE_FIXTURE/launch$5"
    else
      echo 'invalid option: @ntm_agent_launch' >&2
      exit 1
    fi ;;
  send-keys)
    if [ "$NTM_RESTORE_FAIL_SEND" = 1 ]; then
      echo 'fixture delivery failed' >&2
      exit 2
    fi ;;
esac
`
	bin := filepath.Join(dir, "tmux")
	if err := os.WriteFile(bin, []byte(script), 0700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("NTM_TMUX_BINARY", bin)
	oldClient := tmux.DefaultClient
	fastClient := tmux.NewClient("")
	fastClient.MaxSendDelay = 2 * time.Millisecond
	tmux.DefaultClient = fastClient
	t.Cleanup(func() { tmux.DefaultClient = oldClient })
	state := &session.SessionState{
		Name: "original", WorkDir: dir, Version: session.StateVersion, Layout: "tiled",
		Agents: session.AgentConfig{Codex: 1, User: 1},
		Panes: []session.PaneState{
			{Index: 0, AgentType: "cod", Command: "codex --model saved-model", SessionID: "saved-turn", SessionProvider: "codex", SessionFreshness: agentsession.BindingFresh, SessionConfidence: 1},
			{Index: 1, AgentType: "user"},
		},
	}
	return dir, state
}

func savedRestoreHTTPRequest(t *testing.T, srv *Server, body, key string, ctx context.Context) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, "/api/v1/saved-sessions/recovery/restore", strings.NewReader(body))
	if ctx != nil {
		req = req.WithContext(ctx)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Idempotency-Key", key)
	req.Header.Set("Authorization", "Bearer test-secret")
	rec := httptest.NewRecorder()
	srv.Router().ServeHTTP(rec, req)
	t.Logf("saved-session restore body=%s status=%d envelope=%s", body, rec.Code, rec.Body.String())
	return rec
}

func TestSavedSessionRestoreHTTPModesAndReplay(t *testing.T) {
	cases := []struct {
		name string
		body string
		mode string
	}{
		{"default topology", `{"target_name":"restored"}`, "topology"},
		{"explicit topology", `{"target_name":"restored","mode":"topology"}`, "topology"},
		{"fresh launch", `{"target_name":"restored","mode":"launch"}`, "launch"},
		{"native resume", `{"target_name":"restored","mode":"resume","native":true}`, "resume"},
		{"legacy launch resumes natively", `{"target_name":"restored","launch":true,"native":true}`, "resume"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			dir, snapshot := savedRestoreHTTPFixture(t)
			if _, err := session.Save(snapshot, session.SaveOptions{Name: "recovery"}); err != nil {
				t.Fatal(err)
			}
			srv := NewHermeticServer("restore-test")
			t.Cleanup(srv.Stop)
			rec := savedRestoreHTTPRequest(t, srv, tc.body, "restore-once", nil)
			var result SavedSessionRestoreEnvelopeResponse
			if err := json.Unmarshal(rec.Body.Bytes(), &result); err != nil {
				t.Fatal(err)
			}
			if rec.Code != http.StatusOK || !result.Success || result.Session != "restored" || result.Name != "recovery" || result.Mode != tc.mode {
				t.Fatalf("restore response: status=%d %+v", rec.Code, result)
			}
			if tc.mode == "topology" && result.Resume != nil {
				t.Fatalf("topology-only restore returned agent outcomes: %+v", result.Resume)
			}
			if tc.mode == "launch" && (result.Resume == nil || result.Resume.Launched != 1 || result.Resume.Resumed != 0 || result.Resume.Skipped != 1) {
				t.Fatalf("fresh launch outcomes missing: %+v", result.Resume)
			}
			if tc.mode == "resume" && (result.Resume == nil || result.Resume.Resumed != 1 || result.Resume.Launched != 0 || result.Resume.Skipped != 1) {
				t.Fatalf("native resume outcomes missing: %+v", result.Resume)
			}
			replay := savedRestoreHTTPRequest(t, srv, tc.body, "restore-once", nil)
			if replay.Code != http.StatusOK || replay.Header().Get("X-Idempotent-Replay") != "true" {
				t.Fatalf("retry was not replayed: status=%d body=%s", replay.Code, replay.Body.String())
			}
			calls, err := os.ReadFile(filepath.Join(dir, "calls"))
			if err != nil {
				t.Fatal(err)
			}
			t.Logf("tmux_calls=%s", calls)
			if strings.Count(string(calls), "new-session ") != 1 || !strings.Contains(string(calls), "-s restored") {
				t.Fatalf("restore did not create exactly one target session: %s", calls)
			}
			if tc.mode == "topology" && strings.Contains(string(calls), "send-keys") {
				t.Fatalf("topology-only restore launched an agent: %s", calls)
			}
			if tc.mode != "topology" && !strings.Contains(string(calls), "saved-model") {
				t.Fatalf("restore lost saved model: %s", calls)
			}
			if tc.mode == "resume" && !strings.Contains(string(calls), "saved-turn") {
				t.Fatalf("resume lost saved provider session: %s", calls)
			}
		})
	}
}

func TestSavedSessionRestoreHTTPLegacyTargetName(t *testing.T) {
	for _, model := range []string{"", "saved-model"} {
		t.Run("model="+model, func(t *testing.T) {
			dir, snapshot := savedRestoreHTTPFixture(t)
			snapshot.Panes[0].Command = ""
			snapshot.Panes[0].Model = model
			if _, err := session.Save(snapshot, session.SaveOptions{Name: "recovery"}); err != nil {
				t.Fatal(err)
			}
			srv := NewHermeticServer("restore-test")
			t.Cleanup(srv.Stop)
			effective := config.Default()
			effective.Agents.Codex = "codex --session {{.SessionName}}"
			srv.recoveryConfig = func(string) (*config.Config, error) { return effective, nil }
			rec := savedRestoreHTTPRequest(t, srv, `{"target_name":"renamed","mode":"launch"}`, "", nil)
			if rec.Code != http.StatusOK {
				t.Fatalf("legacy restore failed: %d %s", rec.Code, rec.Body.String())
			}
			calls, err := os.ReadFile(filepath.Join(dir, "calls"))
			if err != nil {
				t.Fatal(err)
			}
			if !strings.Contains(string(calls), "codex --session renamed") || strings.Contains(string(calls), "codex --session original") {
				t.Fatalf("legacy template used the wrong session name: %s", calls)
			}
			if model != "" && !strings.Contains(string(calls), model) {
				t.Fatalf("legacy restore lost the saved model: %s", calls)
			}
			persisted, err := session.Load("recovery")
			if err != nil || persisted.Name != "original" || persisted.Panes[0].Command != "" || persisted.Panes[0].Model != model {
				t.Fatalf("restoring mutated the saved snapshot: %+v (%v)", persisted, err)
			}
		})
	}
}

func TestSavedSessionRestoreHTTPRefusalsPreserveLiveSession(t *testing.T) {
	for _, scenario := range []string{"conflict", "invalid launch", "invalid body", "null body", "unknown field", "trailing JSON", "native without resume", "native fresh launch", "invalid mode", "unknown resume field", "invalid target", "missing policy", "viewer", "cancelled"} {
		t.Run(scenario, func(t *testing.T) {
			dir, snapshot := savedRestoreHTTPFixture(t)
			if err := os.WriteFile(filepath.Join(dir, "exists"), nil, 0600); err != nil {
				t.Fatal(err)
			}
			cfg := Config{Auth: AuthConfig{Mode: AuthModeAPIKey, APIKey: "test-secret", Role: RoleAdmin}}
			body, want := `{"target_name":"restored"}`, http.StatusBadRequest
			ctx := context.Background()
			switch scenario {
			case "conflict":
				want = http.StatusConflict
			case "invalid launch":
				snapshot.Panes[0].Command = "codex\nmalicious-command"
				body, want = `{"force":true,"mode":"launch"}`, http.StatusUnprocessableEntity
			case "invalid body":
				body = `{"force":`
			case "null body":
				body = `null`
			case "unknown field":
				body = `{"force":true,"launch_agents":true}`
			case "trailing JSON":
				body = `{"force":true} {}`
			case "native without resume":
				body = `{"force":true,"native":true}`
			case "native fresh launch":
				body = `{"force":true,"mode":"launch","native":true}`
			case "invalid mode":
				body = `{"force":true,"mode":"invalid"}`
			case "unknown resume field":
				body = `{"force":true,"resume":true}`
			case "invalid target":
				body = `{"force":true,"target_name":"bad:session"}`
			case "missing policy":
				body, want = `{"force":true,"mode":"launch"}`, http.StatusServiceUnavailable
			case "viewer":
				cfg.Auth.Role, want = RoleViewer, http.StatusForbidden
				body = `{"force":true}`
			case "cancelled":
				var cancel context.CancelFunc
				ctx, cancel = context.WithCancel(ctx)
				cancel()
				body, want = `{"force":true}`, http.StatusRequestTimeout
			}
			if _, err := session.Save(snapshot, session.SaveOptions{Name: "recovery"}); err != nil {
				t.Fatal(err)
			}
			srv := New(cfg)
			t.Cleanup(srv.Stop)
			if scenario == "missing policy" {
				if err := srv.ConfigureSpawnPolicy(filepath.Join(dir, "missing.toml"), true); err != nil {
					t.Fatal(err)
				}
			}
			rec := savedRestoreHTTPRequest(t, srv, body, "", ctx)
			if rec.Code != want {
				t.Fatalf("got status=%d want=%d: %s", rec.Code, want, rec.Body.String())
			}
			calls, _ := os.ReadFile(filepath.Join(dir, "calls"))
			if strings.Contains(string(calls), "kill-session") || strings.Contains(string(calls), "new-session") || strings.Contains(string(calls), "send-keys") {
				t.Fatalf("rejected request mutated live session: %s", calls)
			}
		})
	}
}

func TestSavedSessionRestoreHTTPPartialFailureIsRetained(t *testing.T) {
	dir, snapshot := savedRestoreHTTPFixture(t)
	t.Setenv("NTM_RESTORE_FAIL_SEND", "1")
	if _, err := session.Save(snapshot, session.SaveOptions{Name: "recovery"}); err != nil {
		t.Fatal(err)
	}
	srv := NewHermeticServer("restore-test")
	t.Cleanup(srv.Stop)
	body := `{"target_name":"partial","mode":"launch","force":true}`
	first := savedRestoreHTTPRequest(t, srv, body, "partial-once", nil)
	var failure struct {
		Details struct {
			Name    string                `json:"name"`
			Session string                `json:"session"`
			Mode    string                `json:"mode"`
			Resume  *session.ResumeResult `json:"resume"`
		} `json:"details"`
	}
	if err := json.Unmarshal(first.Body.Bytes(), &failure); err != nil {
		t.Fatal(err)
	}
	if first.Code != http.StatusInternalServerError || failure.Details.Name != "recovery" || failure.Details.Session != "partial" || failure.Details.Mode != "launch" || failure.Details.Resume == nil || failure.Details.Resume.Failed != 1 {
		t.Fatalf("partial result not reported: %d %s", first.Code, first.Body.String())
	}
	before, err := os.ReadFile(filepath.Join(dir, "calls"))
	if err != nil {
		t.Fatal(err)
	}
	second := savedRestoreHTTPRequest(t, srv, body, "partial-once", nil)
	after, err := os.ReadFile(filepath.Join(dir, "calls"))
	if err != nil {
		t.Fatal(err)
	}
	if second.Code != first.Code || second.Header().Get("X-Idempotent-Replay") != "true" || second.Body.String() != first.Body.String() || string(before) != string(after) {
		t.Fatalf("partial failure replay repeated work: %d %s\nbefore=%s\nafter=%s", second.Code, second.Body.String(), before, after)
	}
}

func TestSavedSessionRestoreHTTPFencesPaneReplacementRace(t *testing.T) {
	dir, snapshot := savedRestoreHTTPFixture(t)
	snapshot.Agents = session.AgentConfig{Codex: 2}
	snapshot.Panes = []session.PaneState{
		{Index: 0, AgentType: "cod", Command: "codex --model first"},
		{Index: 1, AgentType: "cod", Command: "codex --model second"},
	}
	t.Setenv("NTM_RESTORE_RACE_PANE", "%1")
	t.Setenv("NTM_RESTORE_RACE_PHASE", "enter")
	if _, err := session.Save(snapshot, session.SaveOptions{Name: "recovery"}); err != nil {
		t.Fatal(err)
	}
	srv := NewHermeticServer("restore-test")
	t.Cleanup(srv.Stop)
	body := `{"target_name":"raced","mode":"launch","force":true}`
	first := savedRestoreHTTPRequest(t, srv, body, "race-once", nil)
	var failure struct {
		Details struct {
			Resume *session.ResumeResult `json:"resume"`
		} `json:"details"`
	}
	if err := json.Unmarshal(first.Body.Bytes(), &failure); err != nil {
		t.Fatal(err)
	}
	if first.Code != http.StatusInternalServerError || failure.Details.Resume == nil || failure.Details.Resume.Launched != 1 || failure.Details.Resume.Failed != 1 {
		t.Fatalf("HTTP pane replacement race lost partial outcomes: %d %s", first.Code, first.Body.String())
	}
	if !strings.Contains(failure.Details.Resume.Panes[1].Error, tmux.ErrPaneIdentityChanged.Error()) {
		t.Fatalf("HTTP pane replacement race error = %q, want ErrPaneIdentityChanged", failure.Details.Resume.Panes[1].Error)
	}
	calls, err := os.ReadFile(filepath.Join(dir, "calls"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(calls), "if-shell -F -t %1") || strings.Contains(string(calls), "send-keys -t %1 Enter") {
		t.Fatalf("HTTP restore did not guard pane %%1 Enter: %s", calls)
	}
	second := savedRestoreHTTPRequest(t, srv, body, "race-once", nil)
	if second.Code != first.Code || second.Header().Get("X-Idempotent-Replay") != "true" {
		t.Fatalf("HTTP race partial failure was not retained for idempotent replay: %d %s", second.Code, second.Body.String())
	}
}
