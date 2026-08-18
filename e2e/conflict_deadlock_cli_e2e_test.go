//go:build e2e
// +build e2e

// conflict_deadlock_cli_e2e_test.go — user-surface E2Es for C7 and C1
// (bd-izuqq.1), driven through the BUILT ntm binary (TestMain puts it first
// on PATH), never by calling runLocks()/RunCycle() in-process:
//
//   C7: `ntm --json locks list <session> --all-agents --check-deadlocks`
//       against a hermetic fake Agent Mail MCP server whose reservations
//       form a genuine creation-order wait cycle -> the JSON envelope
//       carries the cycle naming both agents; a cycle-free reservation set
//       -> an explicit all-clear (empty cycles, non-nil report).
//
//   C1: `ntm coordinator enable conflict-negotiate` persists the flag into
//       an NTM_CONFIG-selected TOML file, and `ntm coordinator run --once`
//       on a real tmux session then drives the negotiation engine: the
//       release request reaches the losing holder over the fake Agent Mail
//       wire (send_message to the LATEST holder, ack required, subject
//       naming the contested pattern).
//
// Every step logs command output so a CI failure is diagnosable from the
// log alone.

package e2e

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Dicklesworthstone/ntm/internal/tmux"
)

// conflictMailCall records one tools/call the binary made against the fake
// Agent Mail server.
type conflictMailCall struct {
	Tool string
	Args map[string]any
}

// conflictMailServer is a hermetic fake Agent Mail MCP JSON-RPC server: it
// serves a fixed reservation list on resources/read and records (and
// acknowledges) every tools/call.
type conflictMailServer struct {
	mu           sync.Mutex
	calls        []conflictMailCall
	reservations string
	server       *httptest.Server
}

func newConflictMailServer(t *testing.T, reservationsJSON string) *conflictMailServer {
	t.Helper()
	s := &conflictMailServer{reservations: reservationsJSON}
	s.server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var rpc struct {
			ID     any             `json:"id"`
			Method string          `json:"method"`
			Params json.RawMessage `json:"params"`
		}
		if err := json.NewDecoder(r.Body).Decode(&rpc); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		idJSON, _ := json.Marshal(rpc.ID)
		switch rpc.Method {
		case "resources/read":
			contents, _ := json.Marshal(map[string]any{
				"contents": []map[string]any{{"text": s.reservations}},
			})
			fmt.Fprintf(w, `{"jsonrpc":"2.0","id":%s,"result":%s}`, idJSON, contents)
		case "tools/call":
			var params struct {
				Name      string         `json:"name"`
				Arguments map[string]any `json:"arguments"`
			}
			_ = json.Unmarshal(rpc.Params, &params)
			s.mu.Lock()
			s.calls = append(s.calls, conflictMailCall{Tool: params.Name, Args: params.Arguments})
			s.mu.Unlock()
			fmt.Fprintf(w, `{"jsonrpc":"2.0","id":%s,"result":{"id":1}}`, idJSON)
		default:
			fmt.Fprintf(w, `{"jsonrpc":"2.0","id":%s,"error":{"code":-32601,"message":"unknown method"}}`, idJSON)
		}
	}))
	t.Cleanup(s.server.Close)
	return s
}

func (s *conflictMailServer) callsFor(tool string) []conflictMailCall {
	s.mu.Lock()
	defer s.mu.Unlock()
	var out []conflictMailCall
	for _, c := range s.calls {
		if c.Tool == tool {
			out = append(out, c)
		}
	}
	return out
}

// cyclicReservationsJSON builds four active exclusive reservations forming a
// genuine 2-cycle wait-for graph: AgentAlpha grabbed src/alpha/** first and
// now overlaps AgentBeta's later src/alpha/core.go, while AgentBeta grabbed
// docs/** first and now overlaps AgentAlpha's later docs/notes.md — the
// creation-order heuristic yields AgentBeta -> AgentAlpha and AgentAlpha ->
// AgentBeta.
func cyclicReservationsJSON() string {
	t0 := time.Now().UTC().Add(-time.Hour)
	t1 := t0.Add(10 * time.Minute)
	expiry := time.Now().UTC().Add(time.Hour)
	res := func(id int, agent, pattern string, created time.Time) map[string]any {
		return map[string]any{
			"id": id, "agent_name": agent, "path_pattern": pattern, "project_id": 1,
			"exclusive": true, "reason": "conflict e2e",
			"created_ts": created.Format(time.RFC3339Nano),
			"expires_ts": expiry.Format(time.RFC3339Nano),
		}
	}
	payload, _ := json.Marshal([]map[string]any{
		res(1, "AgentAlpha", "src/alpha/**", t0),
		res(2, "AgentBeta", "src/alpha/core.go", t1),
		res(3, "AgentBeta", "docs/**", t0),
		res(4, "AgentAlpha", "docs/notes.md", t1),
	})
	return string(payload)
}

// pairReservationsJSON builds a single two-agent exclusive conflict on one
// pattern (AgentAlpha reserved first): a conflict, but NOT a cycle.
func pairReservationsJSON(pattern string) string {
	now := time.Now().UTC()
	expiry := now.Add(time.Hour)
	payload, _ := json.Marshal([]map[string]any{
		{
			"id": 1, "agent_name": "AgentAlpha", "path_pattern": pattern, "project_id": 1,
			"exclusive": true, "reason": "conflict e2e",
			"created_ts": now.Add(-30 * time.Minute).Format(time.RFC3339Nano),
			"expires_ts": expiry.Format(time.RFC3339Nano),
		},
		{
			"id": 2, "agent_name": "AgentBeta", "path_pattern": pattern, "project_id": 1,
			"exclusive": true, "reason": "conflict e2e",
			"created_ts": now.Add(-5 * time.Minute).Format(time.RFC3339Nano),
			"expires_ts": expiry.Format(time.RFC3339Nano),
		},
	})
	return string(payload)
}

// conflictCLIFixture provides an isolated HOME/XDG, a temp project dir, a
// temp NTM_CONFIG path, and a real (isolated) tmux session.
type conflictCLIFixture struct {
	t          *testing.T
	projectDir string
	xdg        string
	configPath string
	session    string
}

func newConflictCLIFixture(t *testing.T, session string) *conflictCLIFixture {
	t.Helper()
	if !tmux.DefaultClient.IsInstalled() {
		t.Skip("tmux not available")
	}
	f := &conflictCLIFixture{
		t:          t,
		projectDir: t.TempDir(),
		xdg:        t.TempDir(),
		session:    session,
	}
	f.configPath = filepath.Join(f.xdg, "ntm-config.toml")

	out, err := exec.Command(tmux.BinaryPath(), "new-session", "-d", "-s", session,
		"-x", "80", "-y", "24", "-c", f.projectDir).CombinedOutput()
	if err != nil {
		t.Fatalf("tmux new-session: %v (%s)", err, out)
	}
	t.Cleanup(func() {
		_ = exec.Command(tmux.BinaryPath(), "kill-session", "-t", session).Run()
	})
	return f
}

func (f *conflictCLIFixture) env(extra map[string]string) []string {
	env := []string{
		"PATH=" + os.Getenv("PATH"),
		"HOME=" + f.xdg,
		"XDG_CONFIG_HOME=" + f.xdg,
		"NTM_CONFIG=" + f.configPath,
		"AGENT_MAIL_TOKEN=",
	}
	// The isolated tmux server socket from TestMain travels via TMUX_TMPDIR.
	if v := os.Getenv("TMUX_TMPDIR"); v != "" {
		env = append(env, "TMUX_TMPDIR="+v)
	}
	if term := os.Getenv("TERM"); term != "" {
		env = append(env, "TERM="+term)
	}
	for k, v := range extra {
		env = append(env, k+"="+v)
	}
	return env
}

func (f *conflictCLIFixture) runNTM(extra map[string]string, args ...string) (string, int) {
	f.t.Helper()
	cmd := exec.Command("ntm", args...)
	cmd.Dir = f.projectDir
	cmd.Env = f.env(extra)
	out, err := cmd.CombinedOutput()
	code := 0
	if err != nil {
		if exitErr, ok := err.(*exec.ExitError); ok {
			code = exitErr.ExitCode()
		} else {
			f.t.Fatalf("run ntm %v: %v", args, err)
		}
	}
	return string(out), code
}

// locksListEnvelope is the subset of the `ntm --json locks list` envelope
// these tests assert on.
type locksListEnvelope struct {
	Success   bool `json:"success"`
	Deadlocks *struct {
		Cycles []struct {
			Participants []string `json:"participants"`
			Resources    []string `json:"resources"`
		} `json:"cycles"`
		NodeCount int `json:"node_count"`
		EdgeCount int `json:"edge_count"`
	} `json:"deadlocks"`
}

// TestLocksCheckDeadlocksBinaryE2E drives `ntm locks list --check-deadlocks`
// through the built binary (C7's user surface, bd-izuqq.1).
func TestLocksCheckDeadlocksBinaryE2E(t *testing.T) {
	f := newConflictCLIFixture(t, "conflict-locks-e2e")

	t.Run("CycleReportedInJSONEnvelope", func(t *testing.T) {
		mail := newConflictMailServer(t, cyclicReservationsJSON())
		out, code := f.runNTM(map[string]string{"AGENT_MAIL_URL": mail.server.URL + "/"},
			"--json", "locks", "list", f.session, "--all-agents", "--check-deadlocks")
		t.Logf("STEP locks list (cycle): exit=%d output:\n%s", code, out)
		if code != 0 {
			t.Fatalf("locks list --check-deadlocks failed: exit=%d output=%s", code, out)
		}
		var envl locksListEnvelope
		if err := json.Unmarshal([]byte(out), &envl); err != nil {
			t.Fatalf("parse envelope: %v\noutput:\n%s", err, out)
		}
		if !envl.Success {
			t.Fatalf("envelope success=false:\n%s", out)
		}
		if envl.Deadlocks == nil {
			t.Fatalf("--check-deadlocks must attach a deadlocks report:\n%s", out)
		}
		if len(envl.Deadlocks.Cycles) != 1 {
			t.Fatalf("want exactly 1 cycle, got %d:\n%s", len(envl.Deadlocks.Cycles), out)
		}
		participants := strings.Join(envl.Deadlocks.Cycles[0].Participants, ",")
		for _, agent := range []string{"AgentAlpha", "AgentBeta"} {
			if !strings.Contains(participants, agent) {
				t.Errorf("cycle participants %q must name %s", participants, agent)
			}
		}
	})

	t.Run("NoCycleIsExplicitAllClear", func(t *testing.T) {
		mail := newConflictMailServer(t, pairReservationsJSON("src/shared/util.go"))
		out, code := f.runNTM(map[string]string{"AGENT_MAIL_URL": mail.server.URL + "/"},
			"--json", "locks", "list", f.session, "--all-agents", "--check-deadlocks")
		t.Logf("STEP locks list (no cycle): exit=%d output:\n%s", code, out)
		if code != 0 {
			t.Fatalf("locks list --check-deadlocks failed: exit=%d output=%s", code, out)
		}
		var envl locksListEnvelope
		if err := json.Unmarshal([]byte(out), &envl); err != nil {
			t.Fatalf("parse envelope: %v\noutput:\n%s", err, out)
		}
		if envl.Deadlocks == nil {
			t.Fatalf("all-clear must still attach the report (nil means the check never ran):\n%s", out)
		}
		if len(envl.Deadlocks.Cycles) != 0 {
			t.Fatalf("a plain two-holder conflict is not a cycle; got %d cycle(s):\n%s", len(envl.Deadlocks.Cycles), out)
		}
	})

	t.Run("HumanOutputHedgesDeadlockClaim", func(t *testing.T) {
		mail := newConflictMailServer(t, cyclicReservationsJSON())
		out, code := f.runNTM(map[string]string{"AGENT_MAIL_URL": mail.server.URL + "/"},
			"locks", "list", f.session, "--all-agents", "--check-deadlocks")
		t.Logf("STEP locks list (human): exit=%d output:\n%s", code, out)
		if code != 0 {
			t.Fatalf("locks list --check-deadlocks failed: exit=%d output=%s", code, out)
		}
		// bd-izuqq.4: the creation-order heuristic over advisory reservations
		// must not present its guess as a proven deadlock.
		if strings.Contains(out, "DEADLOCK DETECTED") {
			t.Errorf("human output over-claims certainty (DEADLOCK DETECTED):\n%s", out)
		}
		if !strings.Contains(out, "POSSIBLE DEADLOCK") {
			t.Errorf("human output must hedge as POSSIBLE DEADLOCK:\n%s", out)
		}
	})
}

// TestCoordinatorConflictNegotiateBinaryE2E drives the C1 user surface
// (bd-izuqq.1): persist the flag with `ntm coordinator enable
// conflict-negotiate`, then `ntm coordinator run --once` on a real tmux
// session, asserting the negotiation request reached the losing holder over
// the fake Agent Mail wire.
func TestCoordinatorConflictNegotiateBinaryE2E(t *testing.T) {
	f := newConflictCLIFixture(t, "conflict-coord-e2e")
	const pattern = "internal/coordinator/coordinator.go"
	mail := newConflictMailServer(t, pairReservationsJSON(pattern))
	mailEnv := map[string]string{"AGENT_MAIL_URL": mail.server.URL + "/"}

	// STEP 1: enable the persisted flag through the CLI.
	out, code := f.runNTM(mailEnv, "--json", "coordinator", "enable", "conflict-negotiate")
	t.Logf("STEP coordinator enable: exit=%d output:\n%s", code, out)
	if code != 0 {
		t.Fatalf("coordinator enable conflict-negotiate failed: exit=%d output=%s", code, out)
	}
	cfgData, err := os.ReadFile(f.configPath)
	if err != nil {
		t.Fatalf("read persisted config %s: %v", f.configPath, err)
	}
	t.Logf("STEP persisted config:\n%s", cfgData)
	if !strings.Contains(string(cfgData), "conflict_negotiate = true") {
		t.Fatalf("enable must persist conflict_negotiate = true into %s:\n%s", f.configPath, cfgData)
	}

	// STEP 2: one coordinator cycle through the binary.
	out, code = f.runNTM(mailEnv, "--json", "coordinator", "run", "--once", f.session)
	t.Logf("STEP coordinator run --once: exit=%d output:\n%s", code, out)
	if code != 0 {
		t.Fatalf("coordinator run --once failed: exit=%d output=%s", code, out)
	}

	// STEP 3: the engine's decision must have crossed the wire — an
	// ack-required release request to the LATEST holder (AgentBeta), whose
	// subject names the contested pattern.
	sends := mail.callsFor("send_message")
	if len(sends) != 1 {
		t.Fatalf("expected exactly 1 send_message over the wire, got %d: %+v", len(sends), sends)
	}
	args := sends[0].Args
	to, _ := args["to"].([]any)
	if len(to) != 1 || to[0] != "AgentBeta" {
		t.Fatalf("negotiation request must target AgentBeta (latest reservation yields), got to=%v", to)
	}
	if ack, _ := args["ack_required"].(bool); !ack {
		t.Errorf("negotiation request must be ack_required, args=%v", args)
	}
	subject, _ := args["subject"].(string)
	if !strings.Contains(subject, pattern) {
		t.Errorf("negotiation subject %q must name the conflicting pattern %q", subject, pattern)
	}
}
