package coordinator

import (
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Dicklesworthstone/ntm/internal/agent"
	ntmctx "github.com/Dicklesworthstone/ntm/internal/context"
	"github.com/Dicklesworthstone/ntm/internal/robot"
	"github.com/Dicklesworthstone/ntm/internal/tmux"
)

// Exercise the existing coordinator tick with native on-disk accounting, not
// a mocked token lookup. Only pane observation and actuation use test seams.
func TestGeminiNativeAccountingCoordinatorTick(t *testing.T) {
	for _, tc := range []struct {
		remote                                      bool
		name, capture, kind                         string
		age                                         time.Duration
		ambiguous, corrupt, cancelled, unknownLimit bool
		wantAction, wantReason                      string
	}{
		{name: "idle above threshold", capture: ">\n? for shortcuts", wantAction: "enqueued"},
		{name: "active response", capture: "⠋ Working\nEsc to cancel", wantAction: "skipped", wantReason: "working"},
		{name: "rate limited", capture: "Quota exceeded\n>\n? for shortcuts", wantAction: "skipped", wantReason: "rate_limited"},
		{name: "unknown frame", capture: "Connecting to Gemini", wantAction: "skipped", wantReason: "unknown_agent_state"},
		{name: "unsubmitted draft", capture: "> do not discard this draft", wantAction: "skipped", wantReason: "unknown_agent_state"},
		{name: "modal", capture: "Choose an account\n1. First\n2. Second", wantAction: "skipped", wantReason: "unknown_agent_state"},
		{name: "stale accounting with new mtime", capture: ">", age: ntmctx.TranscriptFreshness + time.Second, wantAction: "skipped", wantReason: "stale_transcript"},
		{name: "same workspace ambiguity", capture: ">", ambiguous: true},
		{name: "subagent", capture: ">", kind: "subagent"},
		{name: "partial rewind", capture: ">", corrupt: true},
		{name: "cancelled tick", capture: ">", cancelled: true},
		{name: "unknown context window", capture: ">", unknownLimit: true},
		{name: "remote pane cannot use local transcript", capture: ">", remote: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			originalRemote := tmux.DefaultClient.Remote
			t.Cleanup(func() { tmux.DefaultClient.Remote = originalRemote })
			tmux.DefaultClient.Remote = ""
			if tc.remote {
				tmux.DefaultClient.Remote = "remote.example"
			}
			home, cwd := t.TempDir(), t.TempDir()
			t.Setenv("HOME", home)
			t.Setenv("USERPROFILE", home)
			now := time.Now().UTC().Truncate(time.Second)
			hash := fmt.Sprintf("%x", sha256.Sum256([]byte(filepath.Clean(cwd))))
			chats := filepath.Join(home, ".gemini", "tmp", hash, "chats")
			if err := os.MkdirAll(chats, 0700); err != nil {
				t.Fatal(err)
			}
			path := filepath.Join(chats, "session-test.jsonl")
			kind := tc.kind
			if kind == "" {
				kind = "main"
			}
			data := fmt.Sprintf("{\"sessionId\":\"coordinator-gemini\",\"projectHash\":%q,\"kind\":%q}\n{\"id\":\"answer\",\"type\":\"gemini\",\"timestamp\":%q,\"model\":\"gemini-test-model\",\"tokens\":{\"input\":950,\"output\":0,\"cached\":800,\"total\":950}}\n", hash, kind, now.Add(-tc.age).Format(time.RFC3339Nano))
			if tc.corrupt {
				data += `{"$rewindTo":`
			}
			if err := os.WriteFile(path, []byte(data), 0600); err != nil {
				t.Fatal(err)
			}
			panes := []tmux.Pane{{ID: "%1", Title: "test__gmi_1", Type: tmux.AgentGemini, Width: 120}}
			if tc.ambiguous {
				panes = append(panes, tmux.Pane{ID: "%2", Title: "test__gmi_2", Type: tmux.AgentGemini, Width: 120})
			}
			var queued int
			var records []robot.ActuationRecord
			rc := &rotationChecker{
				session: "test", workDir: cwd, threshold: 80,
				getPanes: func(string) ([]tmux.Pane, error) { return panes, nil },
				paneCwd:  func(string) (string, bool) { return cwd, true },
				transcriptUsage: func(agentType, cwd string) (*ntmctx.TranscriptUsage, bool) {
					return ntmctx.LatestAgentTranscriptUsage(agentType, cwd, time.Time{})
				},
				capturePane: func(string, int) (string, error) { return tc.capture, nil },
				contextLimit: func(model string) int {
					if tc.unknownLimit {
						return 0
					}
					if model != "gemini-test-model" {
						t.Errorf("wrong model: %q", model)
					}
					return 1000
				},
				enqueue: func(agentID, paneID string, usage float64) *ntmctx.PendingRotation {
					queued++
					if agentID != "test__gmi_1" || paneID != "%1" || usage != 95 {
						t.Errorf("enqueue = %s %s %.1f", agentID, paneID, usage)
					}
					return nil
				},
				publish: func(record robot.ActuationRecord) { records = append(records, record) },
				now:     func() time.Time { return now },
			}
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			if tc.cancelled {
				cancel()
			}
			decisions := rc.runOnce(ctx)
			if tc.wantAction == "" {
				if len(decisions) != 0 || queued != 0 {
					t.Fatalf("unobserved/ambiguous pane acted upon: %+v, queued=%d", decisions, queued)
				}
				return
			}
			if len(decisions) != 1 || decisions[0].Action != tc.wantAction || decisions[0].SkipReason != tc.wantReason {
				t.Fatalf("decisions = %+v", decisions)
			}
			if (tc.wantAction == "enqueued" && queued != 1) || (tc.wantAction != "enqueued" && queued != 0) {
				t.Fatalf("queued=%d, decisions=%+v", queued, decisions)
			}
			if len(records) != 1 || !strings.Contains(records[0].MessagePreview, "tokens=950") || !strings.Contains(records[0].MessagePreview, path) {
				t.Fatalf("missing accounting evidence: %+v", records)
			}
		})
	}
}

// The production constructor must use the server-scoped fallback. These ticks
// use real native accounting; only tmux observations and actuation are injected.
func TestRotationServerScopedNativeAccounting(t *testing.T) {
	for _, tc := range []struct {
		name string
		want bool
	}{
		{"unique workspace", true},
		{"same workspace in another session", false},
		{"linked physical pane", true},
		{"conflicting linked observation", false},
		{"target absent", false},
		{"target process changed", false},
		{"target directory changed", false},
		{"unknown peer directory", false},
		{"unknown other provider directory", true},
		{"dead peer", true},
		{"service peer", true},
		{"dead target", false},
		{"service target", false},
		{"symlink alias", false},
		{"inventory failure", false},
		{"partial inventory", false},
		{"inventory bound", false},
		{"exact process needs no inventory", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			originalRemote := tmux.DefaultClient.Remote
			t.Cleanup(func() { tmux.DefaultClient.Remote = originalRemote })
			tmux.DefaultClient.Remote = ""
			home, cwd, other := t.TempDir(), t.TempDir(), t.TempDir()
			t.Setenv("HOME", home)
			t.Setenv("USERPROFILE", home)
			now := time.Now().UTC().Truncate(time.Second)
			hash := fmt.Sprintf("%x", sha256.Sum256([]byte(filepath.Clean(cwd))))
			dir := filepath.Join(home, ".gemini", "tmp", hash, "chats")
			if err := os.MkdirAll(dir, 0700); err != nil {
				t.Fatal(err)
			}
			path := filepath.Join(dir, "session-scope.jsonl")
			data := fmt.Sprintf("{\"sessionId\":\"scope-session\",\"projectHash\":%q}\n{\"id\":\"answer\",\"type\":\"gemini\",\"timestamp\":%q,\"model\":\"scope-model\",\"tokens\":{\"input\":950,\"total\":950}}\n", hash, now.Format(time.RFC3339Nano))
			if err := os.WriteFile(path, []byte(data), 0600); err != nil {
				t.Fatal(err)
			}
			target := tmux.Pane{ID: "%1", PID: 101, Type: tmux.AgentGemini, Title: "scope__gmi_1", Width: 120}
			peer := tmux.Pane{ID: "%2", PID: 202, Type: tmux.AgentGemini, Title: "other__gmi_1"}
			paths := map[string]string{"%1": cwd, "%2": other}
			inventory := map[string][]tmux.Pane{"scope": {target}, "other": {peer}}
			var inventoryErr error
			switch tc.name {
			case "same workspace in another session":
				paths["%2"] = cwd
			case "linked physical pane":
				inventory["linked"] = []tmux.Pane{target}
			case "conflicting linked observation":
				changed := target
				changed.PID++
				inventory["linked"] = []tmux.Pane{changed}
			case "target absent":
				delete(inventory, "scope")
				paths["%2"] = cwd
			case "target process changed":
				inventory["scope"][0].PID++
			case "target directory changed":
				paths["%1"] = other
			case "unknown peer directory", "unknown other provider directory":
				delete(paths, "%2")
				if tc.name == "unknown other provider directory" {
					inventory["other"][0].Type = agent.AgentTypeCodex
				}
			case "dead peer":
				inventory["other"][0].Dead = true
				delete(paths, "%2")
			case "service peer":
				inventory["other"][0].Service = "cass"
				delete(paths, "%2")
			case "dead target":
				target.Dead = true
			case "service target":
				target.Service = "cass"
			case "symlink alias":
				alias := filepath.Join(t.TempDir(), "alias")
				if err := os.Symlink(cwd, alias); err != nil {
					t.Skipf("symlinks unavailable: %v", err)
				}
				paths["%2"] = alias
			case "inventory failure":
				inventory = nil
				inventoryErr = errors.New("inventory unavailable")
			case "partial inventory":
				inventoryErr = errors.New("incomplete inventory")
			case "inventory bound":
				for i := 3; i < 4100; i++ {
					inventory["other"] = append(inventory["other"], tmux.Pane{ID: fmt.Sprintf("%%%d", i)})
				}
			}
			rc := newRotationChecker("scope", cwd, CoordinatorConfig{RotationUsageThreshold: 80}, nil)
			if rc == nil || rc.transcriptUsage != nil || rc.paneCwdContext == nil {
				t.Fatal("production constructor bypasses scoped accounting")
			}
			rc.rotator, rc.ctxMonitor, rc.storedPending = nil, nil, nil
			rc.getPanes = func(string) ([]tmux.Pane, error) { return []tmux.Pane{target}, nil }
			rc.paneCwd = func(string) (string, bool) { return cwd, true }
			rc.paneCwdContext = func(ctx context.Context, id string) (string, bool) {
				if _, ok := ctx.Deadline(); !ok {
					t.Error("fleet directory query has no shared deadline")
				}
				path, ok := paths[id]
				return path, ok
			}
			inventoryCalls := 0
			rc.allPanes = func(context.Context) (map[string][]tmux.Pane, error) {
				inventoryCalls++
				return inventory, inventoryErr
			}
			rc.processUsage = func(string, int) (*ntmctx.TranscriptUsage, bool) {
				if tc.name == "exact process needs no inventory" {
					usage, err := ntmctx.ReadLatestTranscriptUsage(path)
					return usage, err == nil && usage != nil
				}
				return nil, false
			}
			rc.capturePane = func(id string, _ int) (string, error) {
				if id != "%1" {
					t.Error("captured a pane outside the target session")
				}
				return ">\n? for shortcuts", nil
			}
			rc.contextLimit = func(string) int { return 1000 }
			rc.now = func() time.Time { return now }
			rc.publish = nil
			queued := 0
			rc.enqueue = func(id, pane string, pct float64) *ntmctx.PendingRotation {
				queued++
				if id != target.Title || pane != target.ID || pct != 95 {
					t.Errorf("wrong target/accounting: %s %s %v", id, pane, pct)
				}
				return nil
			}
			decisions := rc.runOnce(context.Background())
			if (queued == 1) != tc.want || len(decisions) != queued {
				t.Fatalf("queued=%d decisions=%+v want action=%v", queued, decisions, tc.want)
			}
			if tc.name == "exact process needs no inventory" && inventoryCalls != 0 {
				t.Fatal("exact accounting unnecessarily required fleet discovery")
			}
		})
	}
}

func TestRotationRemotePanesNeverReadControllerTranscripts(t *testing.T) {
	original := tmux.DefaultClient.Remote
	t.Cleanup(func() { tmux.DefaultClient.Remote = original })
	tmux.DefaultClient.Remote = "remote.example"
	for _, typ := range []agent.AgentType{agent.AgentTypeClaudeCode, agent.AgentTypeCodex, agent.AgentTypeGemini} {
		rc := &rotationChecker{
			paneCwd: func(string) (string, bool) { t.Fatal("queried local transcript cwd for remote pane"); return "", false },
			processUsage: func(string, int) (*ntmctx.TranscriptUsage, bool) {
				t.Fatal("queried controller process for remote pane")
				return nil, false
			},
			transcriptUsage: func(string, string) (*ntmctx.TranscriptUsage, bool) {
				t.Fatal("queried controller transcript for remote pane")
				return nil, false
			},
		}
		if got := rc.resolvePaneTranscripts([]tmux.Pane{{ID: "%1", PID: 101, Type: typ}}); len(got) != 0 {
			t.Fatalf("remote %s acquired local accounting: %+v", typ, got)
		}
	}
}

func TestRotationGaugeDoesNotMakeSiblingWorkspaceUnique(t *testing.T) {
	original := tmux.DefaultClient.Remote
	t.Cleanup(func() { tmux.DefaultClient.Remote = original })
	tmux.DefaultClient.Remote = ""
	// Same captured composer as the native OmpStatusBarUsage regression.
	capture := " ok\n\n" +
		"╭── ⬢ Union Alpha > 🗑 omp-probe ▶─────7%─────────────────────────────────────────────────────────┃──────────262K───╮\n" +
		"╰─                                                                                                                    ─╯\n"
	now := time.Now()
	if usage, ok := ntmctx.OmpStatusBarUsage(capture, now); !ok || usage.Tokens != 18340 {
		t.Fatalf("invalid native gauge fixture: %+v, %v", usage, ok)
	}
	cwd := t.TempDir()
	calls := 0
	rc := &rotationChecker{
		paneCwd: func(string) (string, bool) { return cwd, true },
		capturePane: func(id string, _ int) (string, error) {
			if id == "%1" {
				return capture, nil
			}
			return "no gauge visible", nil
		},
		transcriptUsage: func(string, string) (*ntmctx.TranscriptUsage, bool) {
			calls++
			return &ntmctx.TranscriptUsage{Tokens: 99999}, true
		},
		now: func() time.Time { return now },
	}
	got := rc.resolvePaneTranscripts([]tmux.Pane{
		{ID: "%1", Type: agent.AgentTypeOmp}, {ID: "%2", Type: agent.AgentTypeOmp},
	})
	if calls != 0 || len(got) != 1 || got["%1"] == nil || got["%1"].Tokens != 18340 || got["%2"] != nil {
		t.Fatalf("gauge hid a competing conversation: calls=%d readings=%+v", calls, got)
	}
}

func TestRotationScopeRejectsCollidingClaudeStoreNames(t *testing.T) {
	home, root := t.TempDir(), t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	left, right := filepath.Join(root, "project-a"), filepath.Join(root, "project_a")
	for _, dir := range []string{left, right} {
		if err := os.Mkdir(dir, 0700); err != nil {
			t.Fatal(err)
		}
	}
	store := filepath.Join(home, ".claude", "projects", ntmctx.MungeProjectPath(left))
	if err := os.MkdirAll(store, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(store, "session.jsonl"), []byte("{\"type\":\"assistant\",\"message\":{\"model\":\"claude-test\",\"usage\":{\"input_tokens\":900}}}\n"), 0600); err != nil {
		t.Fatal(err)
	}
	target := tmux.Pane{ID: "%1", PID: 101, Type: agent.AgentTypeClaudeCode}
	peer := tmux.Pane{ID: "%2", PID: 202, Type: agent.AgentTypeClaudeCode}
	withPeer := true
	rc := &rotationChecker{
		allPanes: func(context.Context) (map[string][]tmux.Pane, error) {
			all := map[string][]tmux.Pane{"target": {target}}
			if withPeer {
				all["peer"] = []tmux.Pane{peer}
			}
			return all, nil
		},
		paneCwd: func(id string) (string, bool) {
			if id == "%1" {
				return left, true
			}
			return right, true
		},
	}
	refs := []ntmctx.PaneTranscriptRef{{PaneID: "%1", AgentType: "claude", PID: 101, Cwd: left}}
	if got := rc.resolveServerScopedTranscripts(refs, nil); len(got) != 0 {
		t.Fatalf("different cwd names share one Claude store: %+v", got)
	}
	withPeer = false
	if got := rc.resolveServerScopedTranscripts(refs, nil); len(got) != 1 || got["%1"].Tokens != 900 {
		t.Fatalf("non-colliding native reading lost: %+v", got)
	}
}

func TestRotationScopeFailureRetainsExactReadings(t *testing.T) {
	known := map[string]*ntmctx.TranscriptUsage{"%1": {Tokens: 321}}
	refs := []ntmctx.PaneTranscriptRef{
		{PaneID: "%1", AgentType: "gemini", PID: 101, Cwd: t.TempDir()},
		{PaneID: "%2", AgentType: "gemini", PID: 202, Cwd: t.TempDir()},
	}
	rc := &rotationChecker{allPanes: func(context.Context) (map[string][]tmux.Pane, error) {
		return nil, errors.New("scope unavailable")
	}}
	got := rc.resolveServerScopedTranscripts(refs, known)
	if len(got) != 1 || got["%1"] != known["%1"] || got["%2"] != nil {
		t.Fatalf("scope failure erased exact evidence or guessed for a peer: %+v", got)
	}
}
