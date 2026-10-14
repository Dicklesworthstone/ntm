package coordinator

import (
	"context"
	"crypto/sha256"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

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
