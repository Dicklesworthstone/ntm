package cli

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Dicklesworthstone/ntm/internal/agentmail"
	"github.com/Dicklesworthstone/ntm/internal/assignment"
	"github.com/Dicklesworthstone/ntm/internal/config"
	"github.com/Dicklesworthstone/ntm/internal/coordinator"
	"github.com/Dicklesworthstone/ntm/internal/robot"
	"github.com/Dicklesworthstone/ntm/internal/tmux"
)

func TestCoordinatorFeatureKeys(t *testing.T) {
	tests := []struct {
		name     string
		feature  string
		enable   bool
		interval string
		want     [][2]string
		wantErr  string
	}{
		{name: "enable auto assign", feature: "auto-assign", enable: true, want: [][2]string{{"auto_assign", "true"}}},
		{name: "disable auto assign", feature: "auto-assign", want: [][2]string{{"auto_assign", "false"}}},
		{name: "enable digest", feature: "digest", enable: true, want: [][2]string{{"send_digests", "true"}}},
		{name: "enable digest interval", feature: "digest", enable: true, interval: "30m", want: [][2]string{{"send_digests", "true"}, {"digest_interval", `"30m"`}}},
		{name: "disable digest ignores no interval", feature: "digest", want: [][2]string{{"send_digests", "false"}}},
		{name: "enable conflict notify", feature: "conflict-notify", enable: true, want: [][2]string{{"conflict_notify", "true"}}},
		{name: "disable conflict negotiate", feature: "conflict-negotiate", want: [][2]string{{"conflict_negotiate", "false"}}},
		{name: "enable mail nudge", feature: "mail-nudge", enable: true, want: [][2]string{{"mail_nudge", "true"}}},
		{name: "invalid duration", feature: "digest", enable: true, interval: "later", wantErr: "invalid --interval"},
		{name: "zero duration", feature: "digest", enable: true, interval: "0s", wantErr: "must be at least"},
		{name: "negative duration", feature: "digest", enable: true, interval: "-1s", wantErr: "must be at least"},
		{name: "below runtime minimum", feature: "digest", enable: true, interval: (coordinator.MinDigestInterval - time.Second).String(), wantErr: "must be at least"},
		{name: "interval on wrong feature", feature: "auto-assign", enable: true, interval: "30m", wantErr: "only valid with the digest"},
		{name: "unknown feature", feature: "missing", enable: true, wantErr: "unknown feature"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := coordinatorFeatureKeys(tt.feature, tt.enable, tt.interval)
			if tt.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), tt.wantErr) {
					t.Fatalf("coordinatorFeatureKeys() error = %v, want %q", err, tt.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatalf("coordinatorFeatureKeys() error = %v", err)
			}
			if !reflect.DeepEqual(got, tt.want) {
				t.Fatalf("coordinatorFeatureKeys() = %#v, want %#v", got, tt.want)
			}
		})
	}
}

func TestRunCoordinatorToggleValidationClassifiesInvalidFlag(t *testing.T) {
	tests := []struct {
		name     string
		feature  string
		interval string
	}{
		{name: "unknown feature", feature: "missing"},
		{name: "invalid digest interval", feature: "digest", interval: "later"},
		{name: "interval below runtime minimum", feature: "digest", interval: "1s"},
		{name: "interval on wrong feature", feature: "auto-assign", interval: "30m"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := runCoordinatorToggle(nil, []string{tt.feature}, true, tt.interval)
			if err == nil {
				t.Fatal("runCoordinatorToggle() unexpectedly succeeded")
			}
			if !errors.Is(err, errCLIInvalidInput) {
				t.Fatalf("runCoordinatorToggle() error = %v, want errCLIInvalidInput", err)
			}
			code, _ := classifyRobotExecuteError(err)
			if code != robot.ErrCodeInvalidFlag {
				t.Fatalf("classifyRobotExecuteError() code = %q, want %q", code, robot.ErrCodeInvalidFlag)
			}
		})
	}
}

func TestRunCoordinatorTogglePersistsSelectedConfig(t *testing.T) {
	previousConfigFile := cfgFile
	previousJSON := jsonOutput
	t.Cleanup(func() {
		cfgFile = previousConfigFile
		jsonOutput = previousJSON
	})

	path := filepath.Join(t.TempDir(), "selected.toml")
	original := "# retained\n[coordinator]\nsend_digests = false # retained comment\nauto_assign = false\n"
	if err := os.WriteFile(path, []byte(original), 0o600); err != nil {
		t.Fatalf("write selected config: %v", err)
	}
	cfgFile = path
	jsonOutput = true
	output, err := captureStdout(t, func() error {
		return runCoordinatorToggle(nil, []string{"digest"}, true, "30m")
	})
	if err != nil {
		t.Fatalf("runCoordinatorToggle: %v", err)
	}
	var envelope struct {
		Feature    string            `json:"feature"`
		Enabled    bool              `json:"enabled"`
		Persisted  bool              `json:"persisted"`
		ConfigPath string            `json:"config_path"`
		Written    map[string]string `json:"written"`
	}
	if err := json.Unmarshal([]byte(output), &envelope); err != nil {
		t.Fatalf("decode JSON: %v\n%s", err, output)
	}
	wantWritten := map[string]string{"send_digests": "true", "digest_interval": `"30m"`}
	if envelope.Feature != "digest" || !envelope.Enabled || !envelope.Persisted || envelope.ConfigPath != path || !reflect.DeepEqual(envelope.Written, wantWritten) {
		t.Fatalf("toggle envelope = %+v, want path=%s written=%v", envelope, path, wantWritten)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read selected config: %v", err)
	}
	if !strings.Contains(string(data), "# retained") || !strings.Contains(string(data), "send_digests = true # retained comment") {
		t.Fatalf("selected config was not surgically updated:\n%s", data)
	}
	loaded, err := config.Load(path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if !loaded.Coordinator.SendDigests || loaded.Coordinator.DigestInterval != 30*time.Minute || loaded.Coordinator.AutoAssign {
		t.Fatalf("persisted coordinator config = %+v", loaded.Coordinator)
	}
}

func TestCoordinatorRunCommandExposesDeterministicOnceMode(t *testing.T) {
	cmd := newCoordinatorRunCmd()
	if cmd.Use != "run [session]" {
		t.Fatalf("Use=%q", cmd.Use)
	}
	if flag := cmd.Flags().Lookup("once"); flag == nil || flag.DefValue != "false" {
		t.Fatalf("--once flag = %+v", flag)
	}
}

type coordinatorMaintenanceCLIFixture struct {
	session string
	project string
	store   *assignment.AssignmentStore
	renewed chan struct{}

	mu           sync.Mutex
	reservations []agentmail.FileReservation
	renewals     []agentmail.RenewReservationsOptions
	releaseIDs   [][]int
	toolCalls    []string
	renewOnce    sync.Once
}

// newCoordinatorMaintenanceCLIFixture exercises the real coordinator through
// recording tmux/br processes and the actual Agent Mail client. Watch mode
// uses the same fixture to prove its maintenance worker reaches this engine.
func newCoordinatorMaintenanceCLIFixture(t *testing.T, unobservableSibling bool) *coordinatorMaintenanceCLIFixture {
	t.Helper()
	isolateIdentityDirs(t)
	f := &coordinatorMaintenanceCLIFixture{
		session: "coordinator-maintenance", project: t.TempDir(), renewed: make(chan struct{}),
	}
	if err := os.MkdirAll(filepath.Join(f.project, ".git"), 0700); err != nil {
		t.Fatal(err)
	}
	f.store = assignment.NewStore(f.session)
	expires := time.Now().UTC().Add(2 * time.Minute)
	registry := agentmail.NewSessionAgentRegistry(f.session, f.project)
	var activityLines, paneLines []string
	for _, seed := range []struct {
		bead, pane, owner, path string
		index, id               int
	}{
		{"ntm-maintenance", "%94", "BlueLake", "active.go", 1, 941},
		{"ntm-unobservable", "%95", "RedLake", "unknown.go", 2, 951},
		{"ntm-finished", "%96", "GreenLake", "finished.go", 3, 961},
	} {
		if seed.bead == "ntm-unobservable" && !unobservableSibling {
			continue
		}
		actor := seed.owner + ":" + seed.bead
		if seed.bead == "ntm-finished" {
			// Legacy assignments can have no claim actor; exact-ID reservation
			// cleanup still applies. Actor-guarded cleanup has package coverage.
			actor = ""
		}
		row := &assignment.Assignment{
			BeadID: seed.bead, BeadTitle: "Maintain " + seed.path, Pane: seed.index, AgentType: "cc", AgentName: seed.owner,
			Status: assignment.StatusWorking, AssignedAt: time.Now().UTC().Add(-58 * time.Minute), IdempotencyKey: seed.bead,
			ClaimActor: actor, DispatchTarget: seed.pane, OccupancyKey: seed.pane, DispatchState: assignment.DispatchSent,
			ReservationRequired: true, ReservationState: assignment.ReservationReserved, ReservationCompleted: true,
			ReservationAgent: seed.owner, ReservationTarget: seed.pane, ReservationRequested: []string{seed.path},
			ReservedPaths: []string{seed.path}, ReservationIDs: []int{seed.id}, ReservationExpiresAt: &expires,
		}
		f.reservations = append(f.reservations, agentmail.FileReservation{
			ID: seed.id, ProjectID: 9, AgentName: seed.owner, PathPattern: seed.path,
			Reason: "bead assignment: " + seed.bead, Exclusive: true, ExpiresTS: agentmail.FlexTime{Time: expires},
		})
		if seed.bead == "ntm-maintenance" {
			row.ReservationRequested = append(row.ReservationRequested, "active_test.go")
			row.ReservedPaths = append(row.ReservedPaths, "active_test.go")
			row.ReservationIDs = append(row.ReservationIDs, 942)
			f.reservations = append(f.reservations, agentmail.FileReservation{
				ID: 942, ProjectID: 9, AgentName: seed.owner, PathPattern: "active_test.go",
				Reason: "bead assignment: " + seed.bead, Exclusive: true, ExpiresTS: agentmail.FlexTime{Time: expires},
			})
		}
		f.store.Assignments[seed.bead] = row
		if seed.bead != "ntm-finished" {
			title := fmt.Sprintf("%s__cc_%d", f.session, seed.index)
			registry.AddAgent(title, seed.pane, seed.owner)
			primary := []string{seed.pane, fmt.Sprint(seed.index), title, "claude", "100", "30", "0"}
			activity := append(append([]string(nil), primary...), fmt.Sprint(time.Now().Unix()), "4242", "0", "cc")
			plain := append(append([]string(nil), primary...), "4242", "0", "cc")
			activityLines = append(activityLines, strings.Join(activity, tmux.FieldSeparator))
			paneLines = append(paneLines, strings.Join(plain, tmux.FieldSeparator))
		}
	}
	f.reservations = append(f.reservations, agentmail.FileReservation{
		ID: 999, ProjectID: 9, AgentName: "BlueLake", PathPattern: "unrelated.go", Reason: "another task",
		Exclusive: true, ExpiresTS: agentmail.FlexTime{Time: expires},
	})
	if err := f.store.Save(); err != nil {
		t.Fatal(err)
	}
	if err := agentmail.SaveSessionAgentRegistry(registry); err != nil {
		t.Fatal(err)
	}
	binDir := t.TempDir()
	for name, data := range map[string]string{"activity": strings.Join(activityLines, "\n") + "\n", "panes": strings.Join(paneLines, "\n") + "\n"} {
		if err := os.WriteFile(filepath.Join(binDir, name), []byte(data), 0600); err != nil {
			t.Fatal(err)
		}
	}
	t.Setenv("NTM_MAINTENANCE_FIXTURE", binDir)
	const tmuxScript = `#!/bin/sh
printf '%s\n' "$*" >> "$NTM_MAINTENANCE_FIXTURE/tmux-calls"
case "$1" in
  -V) echo 'tmux 3.4' ;;
  has-session) ;;
  list-sessions) echo 'coordinator-maintenance_NTM_SEP_1_NTM_SEP_0_NTM_SEP_today' ;;
  list-panes)
    case "$*" in
      *window_activity*) cat "$NTM_MAINTENANCE_FIXTURE/activity" ;;
      *) cat "$NTM_MAINTENANCE_FIXTURE/panes" ;;
    esac ;;
  capture-pane)
    case "$*" in
      *%95*) echo 'sibling capture unavailable' >&2; exit 1 ;;
      *) printf '● Done. All tests pass.\n\n❯ \n' ;;
    esac ;;
  *) echo "unexpected tmux action: $*" >&2; exit 1 ;;
esac
`
	const brScript = `#!/bin/sh
printf '%s\n' "$*" >> "$NTM_MAINTENANCE_FIXTURE/br-calls"
while [ "$#" -gt 0 ] && [ "$1" != show ]; do shift; done
if [ "$#" -lt 2 ]; then echo 'unexpected br mutation' >&2; exit 1; fi
case "$2" in
  ntm-maintenance) echo '[{"id":"ntm-maintenance","title":"Maintain active.go","issue_type":"task","status":"in_progress","assignee":"BlueLake:ntm-maintenance","labels":[],"dependencies":[]}]' ;;
  ntm-unobservable) echo '[{"id":"ntm-unobservable","title":"Maintain unknown.go","issue_type":"task","status":"in_progress","assignee":"RedLake:ntm-unobservable","labels":[],"dependencies":[]}]' ;;
  ntm-finished) echo '[{"id":"ntm-finished","title":"Finished work","issue_type":"task","status":"closed","assignee":"","labels":[],"dependencies":[]}]' ;;
  *) echo 'unexpected bead' >&2; exit 1 ;;
esac
`
	for name, data := range map[string]string{"tmux": tmuxScript, "br": brScript} {
		if err := os.WriteFile(filepath.Join(binDir, name), []byte(data), 0700); err != nil {
			t.Fatal(err)
		}
	}
	t.Setenv("NTM_TMUX_BINARY", filepath.Join(binDir, "tmux"))
	t.Setenv("PATH", binDir+string(os.PathListSeparator)+os.Getenv("PATH"))
	previousClient, previousConfigFile, previousJSON := tmux.DefaultClient, cfgFile, jsonOutput
	t.Cleanup(func() { tmux.DefaultClient, cfgFile, jsonOutput = previousClient, previousConfigFile, previousJSON })
	tmux.DefaultClient = tmux.NewClient("")
	cfgFile = filepath.Join(binDir, "config.toml")
	if err := os.WriteFile(cfgFile, []byte("[coordinator]\nauto_assign=true\nconflict_notify=false\n"), 0600); err != nil {
		t.Fatal(err)
	}
	jsonOutput = true
	stubCoordinatorLiveTopology(t, []tmux.Pane{{ID: "%94", Index: 1}}, map[string]string{"%94": f.project})
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var request struct {
			ID     any    `json:"id"`
			Method string `json:"method"`
			Params struct {
				Name      string `json:"name"`
				URI       string `json:"uri"`
				Arguments struct {
					HumanKey       string   `json:"human_key"`
					ProjectKey     string   `json:"project_key"`
					AgentName      string   `json:"agent_name"`
					ExtendSeconds  int      `json:"extend_seconds"`
					ReservationIDs []int    `json:"file_reservation_ids"`
					Paths          []string `json:"paths"`
				} `json:"arguments"`
			} `json:"params"`
		}
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		writeResult := func(result any) {
			_ = json.NewEncoder(w).Encode(map[string]any{"jsonrpc": "2.0", "id": request.ID, "result": result})
		}
		f.mu.Lock()
		defer f.mu.Unlock()
		if request.Method == "resources/read" {
			rows, _ := json.Marshal(f.reservations)
			writeResult(map[string]any{"contents": []map[string]any{{"uri": request.Params.URI, "mimeType": "application/json", "text": string(rows)}}})
			return
		}
		f.toolCalls = append(f.toolCalls, request.Params.Name)
		args := request.Params.Arguments
		switch request.Params.Name {
		case "ensure_project":
			writeResult(map[string]any{"id": 9, "slug": "maintenance", "human_key": args.HumanKey})
		case "register_agent":
			writeResult(map[string]any{"id": 1, "name": "AmberLake", "project_id": 9, "program": "ntm", "model": "coordinator"})
		case "renew_file_reservations":
			f.renewals = append(f.renewals, agentmail.RenewReservationsOptions{
				ProjectKey: args.ProjectKey, AgentName: args.AgentName, ExtendSeconds: args.ExtendSeconds,
				ReservationIDs: append([]int(nil), args.ReservationIDs...), Paths: append([]string(nil), args.Paths...),
			})
			for i := range f.reservations {
				for _, id := range args.ReservationIDs {
					if f.reservations[i].ID == id {
						f.reservations[i].ExpiresTS.Time = f.reservations[i].ExpiresTS.Add(time.Duration(args.ExtendSeconds) * time.Second)
					}
				}
			}
			writeResult(map[string]any{"renewed": len(args.ReservationIDs)})
			f.renewOnce.Do(func() { close(f.renewed) })
		case "release_file_reservations":
			f.releaseIDs = append(f.releaseIDs, append([]int(nil), args.ReservationIDs...))
			for i := range f.reservations {
				for _, id := range args.ReservationIDs {
					if f.reservations[i].ID == id {
						f.reservations[i].ReleasedTS = &agentmail.FlexTime{Time: time.Now().UTC()}
					}
				}
			}
			writeResult(map[string]any{"released": len(args.ReservationIDs)})
		default:
			_ = json.NewEncoder(w).Encode(map[string]any{"jsonrpc": "2.0", "id": request.ID, "error": map[string]any{"code": -32601, "message": "unexpected tool " + request.Params.Name}})
		}
	}))
	t.Cleanup(server.Close)
	enableFakeAgentMail(t, server.URL)
	return f
}

func TestCoordinatorRunOnceMaintainsHealthyAssignmentsDespiteSiblingCaptureFailure(t *testing.T) {
	f := newCoordinatorMaintenanceCLIFixture(t, true)
	beforeUnknown := f.store.Get("ntm-unobservable")
	var output bytes.Buffer
	cmd := newCoordinatorRunCmd()
	cmd.SilenceUsage, cmd.SilenceErrors = true, true // Inherited from the production root command.
	cmd.SetOut(&output)
	cmd.SetErr(io.Discard)
	cmd.SetArgs([]string{f.session, "--once"})
	if err := cmd.Execute(); err == nil {
		t.Fatalf("degraded observation reported success: %s", output.String())
	}
	var response coordinatorRunOutput
	if err := json.Unmarshal(output.Bytes(), &response); err != nil {
		t.Fatalf("decode coordinator output: %v\n%s", err, output.String())
	}
	if response.Success || response.ErrorCode != "COORDINATOR_CYCLE_FAILED" || !strings.Contains(response.Error, "sibling capture unavailable") ||
		!response.Once || !response.AutoAssign || len(response.Assignments) != 0 {
		t.Fatalf("coordinator hid observation failure or admitted new work: %+v", response)
	}
	if err := f.store.LoadStrict(); err != nil {
		t.Fatal(err)
	}
	if active := f.store.Get("ntm-maintenance"); active.ReservationRenewalError != "" || active.ReservationExpiresAt.Before(time.Now().Add(time.Hour)) {
		t.Fatalf("healthy assignment lost protection: %+v", active)
	}
	if unknown := f.store.Get("ntm-unobservable"); unknown.Status != assignment.StatusWorking || unknown.ReservationRenewalError == "" ||
		!reflect.DeepEqual(unknown.ReservationIDs, beforeUnknown.ReservationIDs) || !unknown.ReservationExpiresAt.Equal(*beforeUnknown.ReservationExpiresAt) {
		t.Fatalf("unobservable assignment lost its ownership barrier: %+v", unknown)
	}
	if terminal := f.store.Get("ntm-finished"); terminal.Status != assignment.StatusCompleted || len(terminal.ReservationIDs) != 0 {
		t.Fatalf("closed assignment retained stale leases: %+v", terminal)
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if len(f.renewals) != 1 || !reflect.DeepEqual(f.renewals[0].ReservationIDs, []int{941, 942}) || len(f.renewals[0].Paths) != 0 ||
		f.renewals[0].AgentName != "BlueLake" || f.renewals[0].ProjectKey != f.project || f.renewals[0].ExtendSeconds != 3600 {
		t.Fatalf("renewal changed lease scope: %+v", f.renewals)
	}
	if len(f.releaseIDs) != 1 || !reflect.DeepEqual(f.releaseIDs[0], []int{961}) {
		t.Fatalf("terminal cleanup changed lease scope: %+v", f.releaseIDs)
	}
	for _, call := range f.toolCalls {
		if call == "send_message" || call == "file_reservation_paths" {
			t.Fatalf("degraded coordinator started new work: %v", f.toolCalls)
		}
	}
}

func TestCoordinatorInspectionCommandsDoNotStartMaintenanceOrRegisterIdentity(t *testing.T) {
	for _, surface := range []string{"status", "digest"} {
		t.Run(surface, func(t *testing.T) {
			isolateIdentityDirs(t)
			const session = "coordinator-inspection"
			project := t.TempDir()
			if err := os.MkdirAll(filepath.Join(project, ".git"), 0700); err != nil {
				t.Fatal(err)
			}
			stubCoordinatorLiveTopology(t, []tmux.Pane{{ID: "%94", Index: 1}}, map[string]string{"%94": project})
			priorConfigFile, priorJSON, priorTmux := cfgFile, jsonOutput, tmux.DefaultClient
			t.Cleanup(func() { cfgFile, jsonOutput, tmux.DefaultClient = priorConfigFile, priorJSON, priorTmux })
			cfgFile = filepath.Join(t.TempDir(), "config.toml")
			if err := os.WriteFile(cfgFile, []byte("[coordinator]\nauto_assign=true\npoll_interval='100ms'\nsend_digests=true\nconflict_notify=true\nconflict_negotiate=true\nmail_nudge=true\n"), 0600); err != nil {
				t.Fatal(err)
			}
			jsonOutput = true
			tmux.DefaultClient = tmux.NewClient("")
			binDir := t.TempDir()
			logPath := filepath.Join(binDir, "tmux-calls")
			t.Setenv("NTM_COORDINATOR_INSPECTION_LOG", logPath)
			const tmuxScript = `#!/bin/sh
printf '%s\n' "$*" >> "$NTM_COORDINATOR_INSPECTION_LOG"
case "$1" in
  list-sessions) echo 'coordinator-inspection_NTM_SEP_1_NTM_SEP_0_NTM_SEP_today' ;;
  list-panes|has-session) ;;
  *) echo "unexpected tmux action: $*" >&2; exit 1 ;;
esac
`
			bin := filepath.Join(binDir, "tmux")
			if err := os.WriteFile(bin, []byte(tmuxScript), 0700); err != nil {
				t.Fatal(err)
			}
			t.Setenv("NTM_TMUX_BINARY", bin)
			if err := os.WriteFile(filepath.Join(binDir, "bv"), []byte("#!/bin/sh\nexit 1\n"), 0700); err != nil {
				t.Fatal(err)
			}
			t.Setenv("PATH", binDir+string(os.PathListSeparator)+os.Getenv("PATH"))
			var callsMu sync.Mutex
			var toolCalls []string
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				var request struct {
					ID     any    `json:"id"`
					Method string `json:"method"`
					Params struct {
						Name string `json:"name"`
						URI  string `json:"uri"`
					} `json:"params"`
				}
				if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
					http.Error(w, err.Error(), http.StatusBadRequest)
					return
				}
				w.Header().Set("Content-Type", "application/json")
				if request.Method == "resources/read" {
					// A slow digest source must not give an accidentally started
					// monitor time to renew leases or negotiate conflicts.
					time.Sleep(3 * coordinator.MinPollInterval)
					_ = json.NewEncoder(w).Encode(map[string]any{"jsonrpc": "2.0", "id": request.ID, "result": map[string]any{
						"contents": []map[string]any{{"uri": request.Params.URI, "mimeType": "application/json", "text": "[]"}},
					}})
					return
				}
				if request.Method == "tools/call" {
					callsMu.Lock()
					toolCalls = append(toolCalls, request.Params.Name)
					callsMu.Unlock()
				}
				_ = json.NewEncoder(w).Encode(map[string]any{"jsonrpc": "2.0", "id": request.ID, "error": map[string]any{"code": -32601, "message": "inspection must not call mutation tools"}})
			}))
			t.Cleanup(server.Close)
			enableFakeAgentMail(t, server.URL)
			store := assignment.NewStore(session)
			store.Assignments["ntm-inspection"] = &assignment.Assignment{
				BeadID: "ntm-inspection", Status: assignment.StatusWorking, AssignedAt: time.Now().UTC(),
				DispatchTarget: "%94", OccupancyKey: "%94", DispatchState: assignment.DispatchSent,
			}
			if err := store.Save(); err != nil {
				t.Fatal(err)
			}
			before := store.Get("ntm-inspection")
			cmd := newCoordinatorStatusCmd()
			if surface == "digest" {
				cmd = newCoordinatorDigestCmd()
			}
			cmd.SetOut(io.Discard)
			cmd.SetErr(io.Discard)
			cmd.SetArgs([]string{session})
			out, err := captureStdout(t, cmd.Execute)
			if err != nil {
				t.Fatalf("coordinator %s: %v; output=%s", surface, err, out)
			}
			var response struct {
				Session string `json:"session"`
			}
			if err := json.Unmarshal([]byte(out), &response); err != nil || response.Session != session {
				t.Fatalf("inspection output=%s decode=%v", out, err)
			}
			callsMu.Lock()
			calls := append([]string(nil), toolCalls...)
			callsMu.Unlock()
			if len(calls) != 0 {
				t.Fatalf("inspection registered an identity or called mutation tools: %v", calls)
			}
			log, err := os.ReadFile(logPath)
			if err != nil || strings.Count(string(log), "list-panes") != 1 {
				t.Fatalf("inspection started background observations: %s error=%v", log, err)
			}
			if err := store.LoadStrict(); err != nil {
				t.Fatal(err)
			}
			if after := store.Get(before.BeadID); !reflect.DeepEqual(before, after) {
				t.Fatalf("inspection changed assignment state: before=%+v after=%+v", before, after)
			}
		})
	}
}

func stubCoordinatorLiveTopology(t *testing.T, panes []tmux.Pane, paths map[string]string) {
	t.Helper()
	previousExists := coordinatorSessionExists
	previousPanes := coordinatorGetPanes
	previousPath := coordinatorPaneCurrentDir
	coordinatorSessionExists = func(string) bool { return true }
	coordinatorGetPanes = func(string) ([]tmux.Pane, error) { return append([]tmux.Pane(nil), panes...), nil }
	coordinatorPaneCurrentDir = func(paneID string) (string, error) {
		path, ok := paths[paneID]
		if !ok {
			return "", errors.New("missing pane path")
		}
		return path, nil
	}
	t.Cleanup(func() {
		coordinatorSessionExists = previousExists
		coordinatorGetPanes = previousPanes
		coordinatorPaneCurrentDir = previousPath
	})
}

func TestCoordinatorRunFailureIncludesAssignmentFailures(t *testing.T) {
	tests := []struct {
		name        string
		assignments []coordinator.AssignmentResult
		cycleErr    error
		wantError   bool
	}{
		{name: "empty success"},
		{name: "assignment success", assignments: []coordinator.AssignmentResult{{Success: true}}},
		{name: "assignment failure", assignments: []coordinator.AssignmentResult{{Success: false}}, wantError: true},
		{name: "cycle failure", cycleErr: errors.New("observe failed"), wantError: true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := coordinatorRunFailure(tc.assignments, tc.cycleErr); (got != nil) != tc.wantError {
				t.Fatalf("coordinatorRunFailure()=%v, wantError=%t", got, tc.wantError)
			}
		})
	}
}

func TestResolveCoordinatorProjectKeyPreservesExactResolvedSession(t *testing.T) {
	isolateSessionAgentStorage(t)
	originalConfig := cfg
	t.Cleanup(func() { cfg = originalConfig })

	projectsBase := t.TempDir()
	cfg = &config.Config{ProjectsBase: projectsBase}
	session := "coordinator-exact-session"
	competing := filepath.Join(projectsBase, session+"-prefixed")
	if err := os.MkdirAll(filepath.Join(competing, ".git"), 0o755); err != nil {
		t.Fatalf("create competing project: %v", err)
	}
	authoritative := t.TempDir()
	if err := os.MkdirAll(filepath.Join(authoritative, ".git"), 0o755); err != nil {
		t.Fatalf("create authoritative project marker: %v", err)
	}
	saveSessionAgentForTest(t, session, authoritative, "GreenCastle")

	got, err := resolveCoordinatorProjectKey(t.Context(), session, false)
	if err != nil {
		t.Fatalf("resolveCoordinatorProjectKey: %v", err)
	}
	if got != authoritative {
		t.Fatalf("resolved project = %q, want exact session project %q (not %q)", got, authoritative, competing)
	}
}

func TestResolveCoordinatorProjectKeyPrefersCommonLivePaneRoot(t *testing.T) {
	isolateSessionAgentStorage(t)
	const session = "coordinator-live-authority"
	staleProject := t.TempDir()
	if err := os.MkdirAll(filepath.Join(staleProject, ".git"), 0o755); err != nil {
		t.Fatalf("create stale project marker: %v", err)
	}
	liveProject := t.TempDir()
	for _, dir := range []string{filepath.Join(liveProject, ".git"), filepath.Join(liveProject, ".beads"), filepath.Join(liveProject, "internal", "cli"), filepath.Join(liveProject, "internal", "coordinator")} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatalf("create live project path: %v", err)
		}
	}
	saveSessionAgentForTest(t, session, staleProject, "GreenCastle")
	stubCoordinatorLiveTopology(t, []tmux.Pane{{ID: "%70"}, {ID: "%71"}}, map[string]string{
		"%70": filepath.Join(liveProject, "internal", "cli"),
		"%71": filepath.Join(liveProject, "internal", "coordinator"),
	})

	got, err := resolveCoordinatorProjectKey(t.Context(), session, false)
	if err != nil {
		t.Fatalf("resolveCoordinatorProjectKey: %v", err)
	}
	if got != liveProject {
		t.Fatalf("resolved project = %q, want live root %q instead of stale registry %q", got, liveProject, staleProject)
	}
}

func TestResolveCoordinatorProjectKeyRejectsMixedLiveRoots(t *testing.T) {
	const session = "coordinator-mixed-roots"
	first := t.TempDir()
	second := t.TempDir()
	for _, project := range []string{first, second} {
		if err := os.MkdirAll(filepath.Join(project, ".git"), 0o755); err != nil {
			t.Fatalf("create project marker: %v", err)
		}
	}
	stubCoordinatorLiveTopology(t, []tmux.Pane{{ID: "%80"}, {ID: "%81"}}, map[string]string{
		"%80": first,
		"%81": second,
	})

	_, err := resolveCoordinatorProjectKey(t.Context(), session, false)
	if err == nil || !strings.Contains(err.Error(), "multiple project roots") {
		t.Fatalf("mixed live roots error = %v", err)
	}
}

// TestResolveCoordinatorProjectKeyAcceptsLinkedWorktrees — issue #252: an
// `ntm spawn --worktrees` session puts the controller in the base checkout and
// workers in linked worktrees of the same repository. That is one physical
// repo (one git common directory), so coordinator status must resolve it to
// the base checkout instead of rejecting it as multiple project roots.
func TestResolveCoordinatorProjectKeyAcceptsLinkedWorktrees(t *testing.T) {
	isolateSessionAgentStorage(t)
	const session = "coordinator-worktrees"
	base := t.TempDir()
	if resolved, err := filepath.EvalSymlinks(base); err == nil {
		base = resolved
	}
	gitCommand := func(args ...string) {
		t.Helper()
		cmd := exec.Command("git", args...)
		cmd.Dir = base
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Skipf("git %v failed: %v\n%s", args, err, out)
		}
	}
	gitCommand("init", "-b", "main")
	gitCommand("config", "user.email", "test@test.com")
	gitCommand("config", "user.name", "Test")
	gitCommand("commit", "--allow-empty", "-m", "init")
	worktree := filepath.Join(base, ".ntm", "worktrees", session, "cod_1")
	gitCommand("worktree", "add", "-b", "cod_1", worktree)

	stubCoordinatorLiveTopology(t, []tmux.Pane{{ID: "%90"}, {ID: "%91"}}, map[string]string{
		"%90": base,
		"%91": worktree,
	})

	got, err := resolveCoordinatorProjectKey(t.Context(), session, false)
	if err != nil {
		t.Fatalf("resolveCoordinatorProjectKey rejected linked worktrees: %v", err)
	}
	if got != base {
		t.Fatalf("resolved project = %q, want base checkout %q", got, base)
	}
}

// TestCoordinatorConfigFromTOMLPropagatesValues — translator passes through
// every field when the TOML carries explicit values. Anchors the contract
// `coordinator status --json` relies on.
func TestCoordinatorConfigFromTOMLPropagatesValues(t *testing.T) {
	toml := config.CoordinatorConfig{
		PollInterval:      30 * time.Second,
		DigestInterval:    30 * time.Minute,
		AutoAssign:        true,
		IdleThreshold:     300,
		AssignOnlyIdle:    false,
		ConflictNotify:    false,
		ConflictNegotiate: true,
		SendDigests:       true,
		HumanAgent:        "Operator",
		MailNudge:         true,
	}
	got := coordinatorConfigFromTOML(toml, coordinator.DefaultCoordinatorConfig())
	if got.PollInterval != 30*time.Second {
		t.Errorf("PollInterval = %s, want 30s", got.PollInterval)
	}
	if got.DigestInterval != 30*time.Minute {
		t.Errorf("DigestInterval = %s, want 30m", got.DigestInterval)
	}
	if !got.AutoAssign {
		t.Errorf("AutoAssign = false, want true")
	}
	if got.IdleThreshold != 300 {
		t.Errorf("IdleThreshold = %v, want 300", got.IdleThreshold)
	}
	if got.AssignOnlyIdle {
		t.Errorf("AssignOnlyIdle = true, want false")
	}
	if got.ConflictNotify {
		t.Errorf("ConflictNotify = true, want false")
	}
	if !got.ConflictNegotiate {
		t.Errorf("ConflictNegotiate = false, want true")
	}
	if !got.SendDigests {
		t.Errorf("SendDigests = false, want true")
	}
	if got.HumanAgent != "Operator" {
		t.Errorf("HumanAgent = %q, want %q", got.HumanAgent, "Operator")
	}
	if !got.MailNudge {
		t.Error("MailNudge = false, want true")
	}
}

// TestCoordinatorConfigFromTOMLClampsBelowMinimumDurations — anything below
// the runtime minimums (which would otherwise panic time.NewTicker) is clamped
// up. This matches the validation inside SessionCoordinator.Start.
func TestCoordinatorConfigFromTOMLClampsBelowMinimumDurations(t *testing.T) {
	toml := config.CoordinatorConfig{
		PollInterval:   1 * time.Millisecond, // below MinPollInterval
		DigestInterval: 1 * time.Second,      // below MinDigestInterval
		HumanAgent:     "X",
	}
	got := coordinatorConfigFromTOML(toml, coordinator.DefaultCoordinatorConfig())
	if got.PollInterval != coordinator.MinPollInterval {
		t.Errorf("PollInterval = %s, want clamped to %s", got.PollInterval, coordinator.MinPollInterval)
	}
	if got.DigestInterval != coordinator.MinDigestInterval {
		t.Errorf("DigestInterval = %s, want clamped to %s", got.DigestInterval, coordinator.MinDigestInterval)
	}
}

// TestCoordinatorConfigFromTOMLEmptyHumanAgentFallsBack — explicitly empty
// `human_agent = ""` in TOML must fall back to the runtime default. Otherwise
// digest delivery would silently target an empty agent name.
func TestCoordinatorConfigFromTOMLEmptyHumanAgentFallsBack(t *testing.T) {
	toml := config.CoordinatorConfig{
		PollInterval:   coordinator.MinPollInterval,
		DigestInterval: coordinator.MinDigestInterval,
		HumanAgent:     "  ", // whitespace-only also counts as empty
	}
	defaults := coordinator.DefaultCoordinatorConfig()
	got := coordinatorConfigFromTOML(toml, defaults)
	if got.HumanAgent != defaults.HumanAgent {
		t.Errorf("HumanAgent = %q, want fallback %q", got.HumanAgent, defaults.HumanAgent)
	}
}

// TestCoordinatorMirrorMatchesRuntime — config.DefaultCoordinatorConfig() must
// stay in lock-step with coordinator.DefaultCoordinatorConfig(). Drift here
// means a user with no [coordinator] TOML section sees one set of "defaults"
// reflected in `am config validate`, and a different set actually enforced at
// runtime — exactly the symptom #111 was filed for. This test lives in the cli
// package because it can import both internal/config and internal/coordinator
// without forming a cycle (config → coordinator → robot → config).
func TestCoordinatorMirrorMatchesRuntime(t *testing.T) {
	mirror := config.DefaultCoordinatorConfig()
	runtime := coordinator.DefaultCoordinatorConfig()

	if mirror.PollInterval != runtime.PollInterval {
		t.Errorf("PollInterval drift: mirror=%s runtime=%s", mirror.PollInterval, runtime.PollInterval)
	}
	if mirror.DigestInterval != runtime.DigestInterval {
		t.Errorf("DigestInterval drift: mirror=%s runtime=%s", mirror.DigestInterval, runtime.DigestInterval)
	}
	if mirror.AutoAssign != runtime.AutoAssign {
		t.Errorf("AutoAssign drift: mirror=%v runtime=%v", mirror.AutoAssign, runtime.AutoAssign)
	}
	if mirror.IdleThreshold != runtime.IdleThreshold {
		t.Errorf("IdleThreshold drift: mirror=%v runtime=%v", mirror.IdleThreshold, runtime.IdleThreshold)
	}
	if mirror.AssignOnlyIdle != runtime.AssignOnlyIdle {
		t.Errorf("AssignOnlyIdle drift: mirror=%v runtime=%v", mirror.AssignOnlyIdle, runtime.AssignOnlyIdle)
	}
	if mirror.ConflictNotify != runtime.ConflictNotify {
		t.Errorf("ConflictNotify drift: mirror=%v runtime=%v", mirror.ConflictNotify, runtime.ConflictNotify)
	}
	if mirror.ConflictNegotiate != runtime.ConflictNegotiate {
		t.Errorf("ConflictNegotiate drift: mirror=%v runtime=%v", mirror.ConflictNegotiate, runtime.ConflictNegotiate)
	}
	if mirror.SendDigests != runtime.SendDigests {
		t.Errorf("SendDigests drift: mirror=%v runtime=%v", mirror.SendDigests, runtime.SendDigests)
	}
	if mirror.HumanAgent != runtime.HumanAgent {
		t.Errorf("HumanAgent drift: mirror=%q runtime=%q", mirror.HumanAgent, runtime.HumanAgent)
	}
}

func TestFormatIdleDuration(t *testing.T) {

	tests := []struct {
		name     string
		duration time.Duration
		expected string
	}{
		// Less than 1 minute - seconds
		{name: "0 seconds", duration: 0, expected: "0s"},
		{name: "1 second", duration: 1 * time.Second, expected: "1s"},
		{name: "30 seconds", duration: 30 * time.Second, expected: "30s"},
		{name: "59 seconds", duration: 59 * time.Second, expected: "59s"},

		// 1 minute to less than 1 hour - minutes
		{name: "1 minute", duration: 1 * time.Minute, expected: "1m"},
		{name: "5 minutes", duration: 5 * time.Minute, expected: "5m"},
		{name: "30 minutes", duration: 30 * time.Minute, expected: "30m"},
		{name: "59 minutes", duration: 59 * time.Minute, expected: "59m"},
		{name: "59 min 59 sec", duration: 59*time.Minute + 59*time.Second, expected: "59m"},

		// 1+ hours - hours and minutes
		{name: "1 hour", duration: 1 * time.Hour, expected: "1h0m"},
		{name: "1 hour 30 min", duration: 1*time.Hour + 30*time.Minute, expected: "1h30m"},
		{name: "2 hours", duration: 2 * time.Hour, expected: "2h0m"},
		{name: "2 hours 15 min", duration: 2*time.Hour + 15*time.Minute, expected: "2h15m"},
		{name: "24 hours", duration: 24 * time.Hour, expected: "24h0m"},
		{name: "48 hours", duration: 48 * time.Hour, expected: "48h0m"},
		{name: "100 hours 45 min", duration: 100*time.Hour + 45*time.Minute, expected: "100h45m"},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			result := formatIdleDuration(tc.duration)
			if result != tc.expected {
				t.Errorf("formatIdleDuration(%v) = %q; want %q", tc.duration, result, tc.expected)
			}
		})
	}
}

// The coordinator's Agent Mail sender must be a real registered identity, not
// the unregistered "NTM-Coordinator" literal the server rejects. With no
// reachable server, the resolver must fall back to a previously persisted
// session identity, and only to the legacy literal when nothing is persisted.
func TestResolveCoordinatorIdentity_FallbackChain(t *testing.T) {
	tmpDir := t.TempDir()
	t.Setenv("HOME", tmpDir)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(tmpDir, ".config"))

	// Nothing persisted, no client: legacy literal.
	if got := resolveCoordinatorIdentity(t.Context(), nil, "coord-id-sess", "/proj/coord-id"); got != coordinatorFallbackName {
		t.Fatalf("empty state identity = %q, want %q", got, coordinatorFallbackName)
	}

	// Persisted session identity, no client: reuse the registered name.
	if err := agentmail.SaveSessionAgent("coord-id-sess", "/proj/coord-id", &agentmail.SessionAgentInfo{
		AgentName:    "PurpleFinch",
		ProjectKey:   "/proj/coord-id",
		RegisteredAt: time.Now(),
		LastActiveAt: time.Now(),
	}); err != nil {
		t.Fatalf("seed session agent: %v", err)
	}
	if got := resolveCoordinatorIdentity(t.Context(), nil, "coord-id-sess", "/proj/coord-id"); got != "PurpleFinch" {
		t.Fatalf("persisted identity = %q, want PurpleFinch", got)
	}
}
