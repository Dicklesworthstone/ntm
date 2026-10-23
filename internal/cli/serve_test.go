package cli

import (
	"context"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/Dicklesworthstone/ntm/internal/config"
	"github.com/Dicklesworthstone/ntm/internal/robot"
	"github.com/Dicklesworthstone/ntm/internal/state"
	"github.com/Dicklesworthstone/ntm/internal/tools"
	"github.com/Dicklesworthstone/ntm/tests/testutil"
)

// TestServeStateMaintenanceCollectsStaleRuntimeRows: the state store's GC had
// no caller, so its tables only grew (bd-a25g6). serve's maintenance loop must
// prune a row whose staleness window passed while keeping a fresh one.
func TestServeStateMaintenanceCollectsStaleRuntimeRows(t *testing.T) {
	store, err := state.Open(filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatalf("open state store: %v", err)
	}
	if err := store.Migrate(); err != nil {
		t.Fatalf("migrate state store: %v", err)
	}
	t.Cleanup(func() { store.Close() })

	now := time.Now().UTC()
	for _, sess := range []*state.RuntimeSession{
		{Name: "gone", CollectedAt: now.Add(-time.Hour), StaleAfter: now.Add(-30 * time.Minute)},
		{Name: "live", CollectedAt: now, StaleAfter: now.Add(time.Minute)},
	} {
		if err := store.UpsertRuntimeSession(sess); err != nil {
			t.Fatalf("seed runtime session %s: %v", sess.Name, err)
		}
	}

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		defer close(done)
		serveStateMaintenance(ctx, store, time.Hour, time.Hour)
	}()
	t.Cleanup(func() { cancel(); <-done })

	deadline := time.Now().Add(5 * time.Second)
	for {
		gone, err := store.GetRuntimeSession("gone")
		if err != nil {
			t.Fatalf("read runtime session: %v", err)
		}
		if gone == nil {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("serve maintenance never collected the stale runtime session")
		}
		time.Sleep(20 * time.Millisecond)
	}
	if live, err := store.GetRuntimeSession("live"); err != nil || live == nil {
		t.Fatalf("fresh runtime session was collected (session=%v, err=%v)", live, err)
	}
}

// TestCollectStateGarbageOccasionallyThrottlesCLIPasses: hosts that never run
// serve must still collect (bd-7dhqw), but robot commands run constantly, so
// a pass happens only when the stamp next to the DB is older than the interval.
func TestCollectStateGarbageOccasionallyThrottlesCLIPasses(t *testing.T) {
	dir := t.TempDir()
	store, err := state.Open(filepath.Join(dir, "state.db"))
	if err != nil {
		t.Fatalf("open state store: %v", err)
	}
	if err := store.Migrate(); err != nil {
		t.Fatalf("migrate state store: %v", err)
	}
	t.Cleanup(func() { store.Close() })
	seedStale := func(name string) {
		t.Helper()
		now := time.Now().UTC()
		if err := store.UpsertRuntimeSession(&state.RuntimeSession{Name: name, CollectedAt: now.Add(-time.Hour), StaleAfter: now.Add(-30 * time.Minute)}); err != nil {
			t.Fatalf("seed %s: %v", name, err)
		}
	}
	present := func(name string) bool {
		t.Helper()
		sess, err := store.GetRuntimeSession(name)
		if err != nil {
			t.Fatalf("read %s: %v", name, err)
		}
		return sess != nil
	}
	stamp := filepath.Join(dir, "state.db.gc")
	now := time.Now()

	seedStale("first")
	if !collectStateGarbageOccasionally(store, stamp, time.Hour, now) || present("first") {
		t.Fatal("first pass with no stamp must collect the stale session")
	}

	seedStale("second")
	if collectStateGarbageOccasionally(store, stamp, time.Hour, now.Add(30*time.Minute)) || !present("second") {
		t.Fatal("a pass inside the interval must be skipped")
	}
	if !collectStateGarbageOccasionally(store, stamp, time.Hour, now.Add(61*time.Minute)) || present("second") {
		t.Fatal("a pass after the interval must collect again")
	}
}

func TestServeCmdRejectsUnexpectedArguments(t *testing.T) {
	cmd := newServeCmd()
	cmd.SetArgs([]string{"unexpected"})

	err := cmd.Execute()
	if err == nil {
		t.Fatal("serve accepted an unexpected positional argument")
	}
	// serve declares cobra.NoArgs, whose message is `unknown command %q for %q`.
	// "accepts 0 arg(s)" is ExactArgs(0)'s wording and never applied here; the
	// contract under test is that the positional is rejected at all.
	if !strings.Contains(err.Error(), "unknown command") {
		t.Fatalf("error = %q, want Cobra no-arguments error", err)
	}
}

func TestServeAndWebRejectInvalidProjectBeforeOpeningState(t *testing.T) {
	for _, command := range []string{"serve", "web"} {
		for _, kind := range []string{"missing", "file"} {
			t.Run(command+"/"+kind, func(t *testing.T) {
				project := filepath.Join(t.TempDir(), kind)
				if kind == "file" {
					if err := os.WriteFile(project, []byte("not a directory"), 0600); err != nil {
						t.Fatal(err)
					}
				}
				homeDir := t.TempDir()
				t.Setenv("HOME", homeDir)
				t.Setenv("XDG_CONFIG_HOME", filepath.Join(homeDir, "config"))
				t.Setenv("XDG_DATA_HOME", filepath.Join(homeDir, "data"))
				t.Setenv("XDG_STATE_HOME", filepath.Join(homeDir, "state"))
				cmd := newServeCmd()
				if command == "web" {
					cmd = newWebCmd()
				}
				cmd.SilenceUsage, cmd.SilenceErrors = true, true
				cmd.SetArgs([]string{"--project-dir", project})
				if err := cmd.Execute(); err == nil || !strings.Contains(err.Error(), "invalid --project-dir") {
					t.Fatalf("%s accepted invalid project %q: %v", command, project, err)
				}
				entries, err := os.ReadDir(homeDir)
				if err != nil {
					t.Fatal(err)
				}
				if len(entries) != 0 {
					t.Fatalf("invalid launch created state before validating its project: %v", entries)
				}
			})
		}
	}
}

// fakePTScript speaks upstream pt's passive-watch contract
// (github.com/Dicklesworthstone/process_triage@8ac066c38b2e14223b6c9cf11a6b12a81c6c0e1c,
// pt wrapper + crates/pt-core/src/main.rs): `pt --version` prints the wrapper
// and pt-core versions, `agent watch --help` is clap help for AgentWatchArgs
// plus the global --format, and `agent watch --once --threshold low --format
// jsonl` prints one serde_json line per candidate (insertion-ordered keys,
// chrono to_rfc3339 timestamp, abandonment-probability confidence) and exits
// 0. There is no `classify` subcommand: anything else is a clap usage error.
const fakePTScript = `#!/bin/sh
if [ "$1" = "--version" ]; then
	printf 'pt version 2.2.1\npt-core 2.2.1 (%s)\n' "$0"
	exit 0
fi
if [ "$*" = "agent watch --help" ]; then
	cat <<'HELP'
Watch for new candidates and emit notifications

Usage: pt-core agent watch [OPTIONS]

Options:
      --notify-cmd <NOTIFY_CMD>  Execute command directly (no shell) when watch events are emitted
      --threshold <THRESHOLD>    Trigger sensitivity (low|medium|high|critical) [default: medium]
      --interval <INTERVAL>      Check interval in seconds [default: 60]
      --min-age <MIN_AGE>        Only consider processes older than threshold (seconds)
      --once                     Run a single iteration and exit
  -f, --format <FORMAT>          Output format [env: PT_OUTPUT_FORMAT=] [default: json]
HELP
	exit 0
fi
if [ "$*" = "agent watch --once --threshold low --format jsonl" ]; then
	printf '{"event":"candidate_detected","timestamp":"%s","pid":__PID__,"classification":"kill","confidence":0.9731842,"severity":"critical","command":"claude"}\n' "$(date -u +%Y-%m-%dT%H:%M:%S.000000000+00:00)"
	exit 0
fi
echo "error: unrecognized subcommand '$1'" >&2
exit 10
`

// ntm serve's process-triage wiring end to end: startServeProcessTriage starts
// the real monitor (real pt adapter, tmux topology and /proc attribution)
// against a fake pt that emits upstream's watch output for an agent pane. The
// classification must reach the durable attention feed serve publishes to and
// the in-process robot agent-health reader (pt_health / pt_summary).
func TestServeProcessTriageFeedsAgentHealthAndAttention(t *testing.T) {
	testutil.RequireTmuxThrottled(t)
	session, pane := startClaudePaneFixture(t, "ntmptserve")

	bin := t.TempDir()
	script := strings.ReplaceAll(fakePTScript, "__PID__", strconv.Itoa(pane.PID))
	if err := os.WriteFile(filepath.Join(bin, "pt"), []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	ptAdapter := tools.NewPTAdapter()
	ptAdapter.InvalidateStatusCache()
	t.Cleanup(ptAdapter.InvalidateStatusCache)

	feed := robot.NewAttentionFeed(robot.DefaultAttentionFeedConfig())
	t.Cleanup(feed.Stop)
	serveCfg := config.Default()
	serveCfg.Integrations.ProcessTriage.CheckInterval = 5
	serveCfg.Integrations.ProcessTriage.UseRanoData = false // keep any installed rano out of the sample
	stop := startServeProcessTriage(serveCfg, feed)
	if stop == nil {
		t.Fatal("serve did not start the monitor against pt's passive watch")
	}
	t.Cleanup(stop)

	var ptEvent *robot.AttentionEvent
	deadline := time.Now().Add(15 * time.Second)
	for ptEvent == nil {
		events, _, err := feed.Replay(0, 100)
		if err != nil {
			t.Fatalf("replay attention feed: %v", err)
		}
		for i := range events {
			if events[i].Details["monitor"] == "pt" && events[i].Details["pane_ref"] == pane.ID {
				ptEvent = &events[i]
			}
		}
		if ptEvent == nil {
			if time.Now().After(deadline) {
				t.Fatalf("pt watch sample never reached the attention feed (events=%+v)", events)
			}
			time.Sleep(100 * time.Millisecond)
		}
	}
	if ptEvent.Details["current_classification"] != "stuck" || ptEvent.Session != session {
		t.Fatalf("attention event = %+v", ptEvent)
	}

	opts := robot.DefaultAgentHealthOptions()
	opts.Session = session
	opts.IncludeCaut = false
	opts.PTTimeout = 5 * time.Second
	out, err := robot.GetAgentHealth(opts)
	if err != nil || !out.Success {
		t.Fatalf("agent health = %+v, %v", out, err)
	}
	if !out.PTAvailable || out.PTStatus != robot.PTAvailabilityAvailable || out.PTSummary == nil || out.PTSummary.Stuck != 1 {
		t.Fatalf("pt status=%q available=%v summary=%+v", out.PTStatus, out.PTAvailable, out.PTSummary)
	}
	var health *robot.PTHealthInfo
	for _, status := range out.Panes {
		if status.PTHealth != nil {
			health = status.PTHealth
		}
	}
	if health == nil || health.Classification != "stuck" || health.Source != "pt_agent_watch" || health.Recommendation != "kill" ||
		health.AbandonmentProbability == nil || *health.AbandonmentProbability != 0.9731842 {
		t.Fatalf("pt_health = %+v (panes=%+v)", health, out.Panes)
	}
}
