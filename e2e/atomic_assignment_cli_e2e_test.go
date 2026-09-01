//go:build e2e
// +build e2e

package e2e

import (
	"bufio"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
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
	"sort"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"testing"
	"time"

	"github.com/Dicklesworthstone/ntm/internal/agentmail"
	"github.com/Dicklesworthstone/ntm/internal/assignment"
	"github.com/Dicklesworthstone/ntm/internal/config"
	"github.com/Dicklesworthstone/ntm/internal/tmux"
	"github.com/Dicklesworthstone/ntm/internal/util"
	"github.com/Dicklesworthstone/ntm/tests/testutil"
)

// This test crosses every production process boundary used by assignment:
// the built ntm binary, a private real tmux server, a real br SQLite database,
// and the durable assignment ledger under an isolated HOME. The agent panes
// run cat with terminal echo disabled so one transport submission produces
// exactly one observable marker.

// atomicAgentPaneCommand is the command every fixture agent pane runs. The
// panes emulate agent TUIs with `cat` (echo disabled, one transport
// submission -> exactly one observable marker). Panes start with an EMPTY
// screen on purpose: dispatch's composer gate (bd-dp9oy) fails open on
// empty captures, and distribution eligibility stays with the panes a test
// explicitly primes via primeEndpointForSafeDispatch, which renders the
// pane's own composer glyph ("❯" for Claude, "›" for codex).
const atomicAgentPaneCommand = `bash --noprofile --norc -c 'stty -echo; exec cat'`

type atomicAssignmentCLIFixture struct {
	ntmPath    string
	tmuxPath   string
	brPath     string
	session    string
	root       string
	projectDir string
	homeDir    string
	tmuxConfig string
	env        []string
	panes      map[int]atomicAssignmentPane
}

type atomicAssignmentPane struct {
	Window int
	Index  int
	Target string
	ID     string
	Title  string
}

type atomicAssignmentProcessResult struct {
	stdout   []byte
	stderr   []byte
	exitCode int
}

type atomicCompletionWatchProcess struct {
	cmd    *exec.Cmd
	group  *testutil.ProcessGroupForTest
	ctx    context.Context
	cancel context.CancelFunc
	stdout *os.File
	stderr *os.File
	waited bool
}

type atomicAssignmentDirectEnvelope struct {
	Command string `json:"command"`
	Session string `json:"session"`
	Success bool   `json:"success"`
	Data    *struct {
		Assignment struct {
			BeadID     string `json:"bead_id"`
			BeadTitle  string `json:"bead_title"`
			Pane       int    `json:"pane"`
			PaneTarget string `json:"pane_target"`
			PaneID     string `json:"pane_id"`
			AgentType  string `json:"agent_type"`
			Prompt     string `json:"prompt"`
			PromptSent bool   `json:"prompt_sent"`
		} `json:"assignment"`
		Receipt *struct {
			WorkItemID string `json:"work_item_id"`
			Pane       struct {
				Session     string `json:"session"`
				Target      string `json:"target"`
				WindowIndex int    `json:"window_index"`
				Index       int    `json:"index"`
				ID          string `json:"id"`
			} `json:"pane"`
			Prompt struct {
				Length     int    `json:"length"`
				HashSHA256 string `json:"hash_sha256"`
			} `json:"prompt"`
			Transport struct {
				Sent       bool   `json:"sent"`
				DeliveryID string `json:"delivery_id"`
				Error      string `json:"error"`
			} `json:"transport"`
			Timestamp string `json:"timestamp"`
		} `json:"receipt"`
	} `json:"data"`
	Error *struct {
		Code    string `json:"code"`
		Message string `json:"message"`
	} `json:"error"`
}

type atomicAssignmentAutoEnvelope struct {
	Command string `json:"command"`
	Session string `json:"session"`
	Success bool   `json:"success"`
	Data    *struct {
		Assignments []struct {
			BeadID     string `json:"bead_id"`
			PaneID     string `json:"pane_id"`
			Status     string `json:"status"`
			PromptSent bool   `json:"prompt_sent"`
		} `json:"assignments"`
		Summary struct {
			AssignedCount int `json:"assigned_count"`
		} `json:"summary"`
	} `json:"data"`
	Error *struct {
		Code    string `json:"code"`
		Message string `json:"message"`
	} `json:"error"`
}

type atomicAssignmentBulkEnvelope struct {
	Success     bool `json:"success"`
	Assignments []struct {
		Pane              string `json:"pane"`
		PaneID            string `json:"pane_id"`
		Bead              string `json:"bead"`
		AgentType         string `json:"agent_type"`
		Status            string `json:"status"`
		PromptSent        bool   `json:"prompt_sent"`
		Claimed           bool   `json:"claimed"`
		ClaimActor        string `json:"claim_actor"`
		IdempotencyKey    string `json:"idempotency_key"`
		DispatchReceiptID string `json:"dispatch_receipt_id"`
		Error             string `json:"error"`
	} `json:"assignments"`
	Summary struct {
		Assigned int `json:"assigned"`
		Failed   int `json:"failed"`
	} `json:"summary"`
	Error     string `json:"error"`
	ErrorCode string `json:"error_code"`
}

type atomicAssignmentLedger struct {
	SessionName           string                             `json:"session_name"`
	Assignments           map[string]*atomicAssignmentRecord `json:"assignments"`
	PersistenceGeneration uint64                             `json:"persistence_generation"`
	Version               int                                `json:"version"`
}

type atomicAssignmentRecord struct {
	BeadID                   string     `json:"bead_id"`
	BeadTitle                string     `json:"bead_title"`
	Pane                     int        `json:"pane"`
	AgentType                string     `json:"agent_type"`
	AgentName                string     `json:"agent_name"`
	Status                   string     `json:"status"`
	AssignedAt               time.Time  `json:"assigned_at"`
	CompletedAt              *time.Time `json:"completed_at"`
	RetryCount               int        `json:"retry_count"`
	PromptSent               string     `json:"prompt_sent"`
	IdempotencyKey           string     `json:"idempotency_key"`
	ClaimActor               string     `json:"claim_actor"`
	ClaimState               string     `json:"claim_state"`
	ClaimStatus              string     `json:"claim_status"`
	ClaimAttempts            int        `json:"claim_attempts"`
	ClaimedAt                *time.Time `json:"claimed_at"`
	ReservationRequired      bool       `json:"reservation_required"`
	ReservationDiscovery     bool       `json:"reservation_discovery"`
	ReservationInputPaths    []string   `json:"reservation_input_paths"`
	ReservationRequested     []string   `json:"reservation_requested"`
	ReservationState         string     `json:"reservation_state"`
	ReservationAttempts      int        `json:"reservation_attempts"`
	ReservationCompleted     bool       `json:"reservation_completed"`
	ReservedPaths            []string   `json:"reserved_paths"`
	ReservationIDs           []int      `json:"reservation_ids"`
	ReservationExpiresAt     *time.Time `json:"reservation_expires_at"`
	ReservationError         string     `json:"reservation_error"`
	DispatchState            string     `json:"dispatch_state"`
	DispatchTarget           string     `json:"dispatch_target"`
	OccupancyKey             string     `json:"occupancy_key"`
	PromptSHA256             string     `json:"prompt_sha256"`
	IntentSHA256             string     `json:"intent_sha256"`
	PendingPrompt            string     `json:"pending_prompt"`
	DispatchAttempts         int        `json:"dispatch_attempts"`
	DispatchStartedAt        *time.Time `json:"dispatch_started_at"`
	DispatchedAt             *time.Time `json:"dispatched_at"`
	DispatchReceiptID        string     `json:"dispatch_receipt_id"`
	LastDispatchError        string     `json:"last_dispatch_error"`
	ClearState               string     `json:"clear_state"`
	ClearError               string     `json:"clear_error"`
	PendingTerminalStatus    string     `json:"pending_terminal_status"`
	PendingTerminalReason    string     `json:"pending_terminal_reason"`
	TerminalClaimReleased    bool       `json:"terminal_claim_released"`
	PendingCompletionEventID string     `json:"pending_completion_event_id"`
	CompletionDetectedAt     *time.Time `json:"completion_detected_at"`
	CompletionConsumerToken  string     `json:"completion_consumer_token"`
	CompletionLeaseExpiresAt *time.Time `json:"completion_lease_expires_at"`
}

type atomicAssignmentRetryEnvelope struct {
	Command    string `json:"command"`
	Subcommand string `json:"subcommand"`
	Session    string `json:"session"`
	Success    bool   `json:"success"`
	Data       *struct {
		Retried []struct {
			BeadID     string `json:"bead_id"`
			Pane       int    `json:"pane"`
			Status     string `json:"status"`
			PromptSent bool   `json:"prompt_sent"`
			RetryCount int    `json:"retry_count"`
		} `json:"retried"`
		Skipped []struct {
			BeadID string `json:"bead_id"`
			Reason string `json:"reason"`
		} `json:"skipped"`
		Summary struct {
			TotalFailed  int `json:"total_failed"`
			RetriedCount int `json:"retried_count"`
			SkippedCount int `json:"skipped_count"`
		} `json:"summary"`
	} `json:"data"`
	Error *struct {
		Code    string `json:"code"`
		Message string `json:"message"`
	} `json:"error"`
}

type atomicAssignmentReassignEnvelope struct {
	Command    string `json:"command"`
	Subcommand string `json:"subcommand"`
	Session    string `json:"session"`
	Success    bool   `json:"success"`
	Data       *struct {
		BeadID                       string `json:"bead_id"`
		BeadTitle                    string `json:"bead_title"`
		Pane                         int    `json:"pane"`
		AgentType                    string `json:"agent_type"`
		AgentName                    string `json:"agent_name"`
		Status                       string `json:"status"`
		PromptSent                   bool   `json:"prompt_sent"`
		PreviousPane                 int    `json:"previous_pane"`
		PreviousAgent                string `json:"previous_agent"`
		PreviousAgentType            string `json:"previous_agent_type"`
		PreviousStatus               string `json:"previous_status"`
		FileReservationsTransferred  bool   `json:"file_reservations_transferred"`
		FileReservationsReleasedFrom int    `json:"file_reservations_released_from"`
		FileReservationsCreatedFor   int    `json:"file_reservations_created_for"`
	} `json:"data"`
	Error *struct {
		Code    string         `json:"code"`
		Message string         `json:"message"`
		Details map[string]any `json:"details"`
	} `json:"error"`
}

type atomicAssignmentBead struct {
	ID       string `json:"id"`
	Status   string `json:"status"`
	Assignee string `json:"assignee"`
}

type atomicAssignmentMailSnapshot struct {
	EnsureCalls   int
	ReserveCalls  int
	ListCalls     int
	ReleaseCalls  int
	SendCalls     int
	ReleasedIDs   []int
	GrantedAgents []string
	GrantedPaths  [][]string
	Active        []map[string]any
	RawRequests   []byte
}

type atomicAssignmentFileSnapshot struct {
	Exists bool
	Mode   os.FileMode
	Data   []byte
}

type atomicAssignmentMailStub struct {
	projectDir string
	server     *httptest.Server

	mu                  sync.Mutex
	nextReservationID   int
	active              []map[string]any
	ensureCalls         int
	reserveCalls        int
	listCalls           int
	releaseCalls        int
	sendCalls           int
	releasedIDs         []int
	grantedAgents       []string
	grantedPaths        [][]string
	rawRequests         []byte
	grantReasonOverride string
	retainAfterRelease  bool
	releaseGate         chan struct{}
	releaseStarted      chan struct{}
	releaseAborted      chan struct{}
	releaseGateUsed     bool
}

func newAtomicAssignmentMailStub(projectDir string) *atomicAssignmentMailStub {
	stub := &atomicAssignmentMailStub{projectDir: projectDir, nextReservationID: 9800}
	stub.server = httptest.NewServer(http.HandlerFunc(stub.handle))
	return stub
}

func (s *atomicAssignmentMailStub) close() {
	if s != nil && s.server != nil {
		s.server.Close()
	}
}

func (s *atomicAssignmentMailStub) env() map[string]string {
	return map[string]string{"AGENT_MAIL_URL": s.server.URL + "/"}
}

func (s *atomicAssignmentMailStub) setGrantReasonOverride(reason string) {
	s.mu.Lock()
	s.grantReasonOverride = reason
	s.mu.Unlock()
}

func (s *atomicAssignmentMailStub) setRetainAfterRelease(retain bool) {
	s.mu.Lock()
	s.retainAfterRelease = retain
	s.mu.Unlock()
}

func (s *atomicAssignmentMailStub) blockNextRelease(t *testing.T) (<-chan struct{}, <-chan struct{}) {
	t.Helper()
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.releaseGate != nil || s.releaseStarted != nil {
		t.Fatal("Agent Mail release gate is already armed")
	}
	s.releaseGate = make(chan struct{})
	s.releaseStarted = make(chan struct{})
	s.releaseAborted = make(chan struct{})
	s.releaseGateUsed = false
	return s.releaseStarted, s.releaseAborted
}

func (s *atomicAssignmentMailStub) allowBlockedRelease(t *testing.T) {
	t.Helper()
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.releaseGate == nil {
		t.Fatal("Agent Mail release gate is not armed")
	}
	close(s.releaseGate)
	s.releaseGate = nil
	s.releaseStarted = nil
	s.releaseAborted = nil
	s.releaseGateUsed = false
}

func (s *atomicAssignmentMailStub) snapshot() atomicAssignmentMailSnapshot {
	s.mu.Lock()
	defer s.mu.Unlock()
	paths := make([][]string, len(s.grantedPaths))
	for index := range s.grantedPaths {
		paths[index] = append([]string(nil), s.grantedPaths[index]...)
	}
	return atomicAssignmentMailSnapshot{
		EnsureCalls:   s.ensureCalls,
		ReserveCalls:  s.reserveCalls,
		ListCalls:     s.listCalls,
		ReleaseCalls:  s.releaseCalls,
		SendCalls:     s.sendCalls,
		ReleasedIDs:   append([]int(nil), s.releasedIDs...),
		GrantedAgents: append([]string(nil), s.grantedAgents...),
		GrantedPaths:  paths,
		Active:        append([]map[string]any(nil), s.active...),
		RawRequests:   append([]byte(nil), s.rawRequests...),
	}
}

func (s *atomicAssignmentMailStub) handle(w http.ResponseWriter, r *http.Request) {
	if r.Method == http.MethodGet && r.URL.Path == "/health/liveness" {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"status":"ok"}`))
		return
	}
	body, err := io.ReadAll(r.Body)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	s.mu.Lock()
	s.rawRequests = append(s.rawRequests, body...)
	s.rawRequests = append(s.rawRequests, '\n')
	s.mu.Unlock()
	var request struct {
		JSONRPC string `json:"jsonrpc"`
		ID      any    `json:"id"`
		Method  string `json:"method"`
		Params  struct {
			Name      string         `json:"name"`
			Arguments map[string]any `json:"arguments"`
		} `json:"params"`
	}
	if err := json.Unmarshal(body, &request); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	writeResult := func(result any) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{"jsonrpc": "2.0", "id": request.ID, "result": result})
	}
	if request.JSONRPC == "2.0" && request.Method == "resources/read" {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"jsonrpc": "2.0", "id": request.ID,
			"error": map[string]any{"code": -32601, "message": "resource view not supported"},
		})
		return
	}
	if request.JSONRPC != "2.0" || request.Method != "tools/call" {
		http.Error(w, "expected JSON-RPC tools/call", http.StatusBadRequest)
		return
	}
	now := time.Now().UTC()
	switch request.Params.Name {
	case "health_check":
		writeResult(map[string]any{"status": "ok", "timestamp": now.Format(time.RFC3339Nano)})
	case "ensure_project":
		humanKey, _ := request.Params.Arguments["human_key"].(string)
		if humanKey != s.projectDir {
			http.Error(w, fmt.Sprintf("unexpected project key: %#v", request.Params.Arguments), http.StatusBadRequest)
			return
		}
		s.mu.Lock()
		s.ensureCalls++
		s.mu.Unlock()
		writeResult(map[string]any{"id": 1, "slug": "atomic-e2e", "human_key": humanKey, "created_at": now.Format(time.RFC3339Nano)})
	case "file_reservation_paths":
		paths, pathsOK := anyStringSlice(request.Params.Arguments["paths"])
		agentName, _ := request.Params.Arguments["agent_name"].(string)
		reason, _ := request.Params.Arguments["reason"].(string)
		if request.Params.Arguments["project_key"] != s.projectDir || !pathsOK || len(paths) == 0 || agentName == "" || reason == "" {
			http.Error(w, fmt.Sprintf("unexpected reservation args: %#v", request.Params.Arguments), http.StatusBadRequest)
			return
		}
		s.mu.Lock()
		s.reserveCalls++
		s.grantedAgents = append(s.grantedAgents, agentName)
		s.grantedPaths = append(s.grantedPaths, append([]string(nil), paths...))
		grantReason := reason
		if s.grantReasonOverride != "" {
			grantReason = s.grantReasonOverride
		}
		granted := make([]map[string]any, 0, len(paths))
		for _, path := range paths {
			s.nextReservationID++
			grant := map[string]any{
				"id": s.nextReservationID, "path_pattern": path, "agent_name": agentName,
				"project_id": 1, "exclusive": true, "reason": grantReason,
				"created_ts": now.Format(time.RFC3339Nano), "expires_ts": now.Add(time.Hour).Format(time.RFC3339Nano),
			}
			granted = append(granted, grant)
			s.active = append(s.active, grant)
		}
		s.mu.Unlock()
		writeResult(map[string]any{"granted": granted, "conflicts": []any{}})
	case "list_file_reservations":
		s.mu.Lock()
		s.listCalls++
		active := append([]map[string]any(nil), s.active...)
		s.mu.Unlock()
		writeResult(active)
	case "release_file_reservations":
		ids, idsOK := atomicAssignmentAnyIntSlice(request.Params.Arguments["file_reservation_ids"])
		if request.Params.Arguments["project_key"] != s.projectDir || !idsOK || len(ids) == 0 {
			http.Error(w, fmt.Sprintf("unexpected release args: %#v", request.Params.Arguments), http.StatusBadRequest)
			return
		}
		s.mu.Lock()
		releaseGate := s.releaseGate
		releaseStarted := s.releaseStarted
		releaseAborted := s.releaseAborted
		if releaseGate != nil && !s.releaseGateUsed {
			// The gate is one-shot. A fresh process must be able to retry the
			// same durable release barrier after this request is canceled.
			s.releaseGateUsed = true
		} else {
			releaseGate = nil
			releaseStarted = nil
			releaseAborted = nil
		}
		s.mu.Unlock()
		if releaseGate != nil {
			close(releaseStarted)
			select {
			case <-releaseGate:
			case <-r.Context().Done():
				close(releaseAborted)
				return
			}
		}
		s.mu.Lock()
		s.releaseCalls++
		s.releasedIDs = append(s.releasedIDs, ids...)
		if !s.retainAfterRelease {
			wanted := make(map[int]struct{}, len(ids))
			for _, id := range ids {
				wanted[id] = struct{}{}
			}
			remaining := s.active[:0]
			for _, reservation := range s.active {
				id, _ := reservation["id"].(int)
				if _, released := wanted[id]; !released {
					remaining = append(remaining, reservation)
				}
			}
			s.active = remaining
		}
		s.mu.Unlock()
		writeResult(map[string]any{"released": len(ids)})
	case "send_message":
		recipients, recipientsOK := anyStringSlice(request.Params.Arguments["to"])
		if !recipientsOK || len(recipients) != 1 {
			http.Error(w, fmt.Sprintf("unexpected send args: %#v", request.Params.Arguments), http.StatusBadRequest)
			return
		}
		s.mu.Lock()
		s.sendCalls++
		messageID := 9900 + s.sendCalls
		s.mu.Unlock()
		writeResult(map[string]any{
			"count": 1,
			"deliveries": []map[string]any{{
				"project": s.projectDir,
				"payload": map[string]any{"id": messageID, "to": recipients},
			}},
		})
	default:
		http.Error(w, "unexpected Agent Mail tool "+request.Params.Name, http.StatusNotFound)
	}
}

func TestE2EAtomicAssignmentIsolatedEnvScrubsHostOverrides(t *testing.T) {
	for _, key := range []string{"BR_DB", "BD_DB", "BEADS_DB", "GIT_DIR", "GIT_WORK_TREE", "AGENT_NAME", "PWD", "OLDPWD", "NTM_TEST_ASSIGNMENT_SAVE_FAIL_AFTER_BACKUP", "NTM_TEST_COMPLETION_ACK_FAIL_ONCE", "NTM_TEST_COMPLETION_LEASE_DURATION", "NTM_TEST_COMPLETION_HANDLER_DELAY"} {
		t.Setenv(key, "/should/not/escape")
	}
	env := atomicAssignmentIsolatedEnv(map[string]string{"HOME": t.TempDir()})
	for _, entry := range env {
		key, _, _ := strings.Cut(entry, "=")
		switch key {
		case "BR_DB", "BD_DB", "BEADS_DB", "GIT_DIR", "GIT_WORK_TREE", "AGENT_NAME", "PWD", "OLDPWD", "NTM_TEST_ASSIGNMENT_SAVE_FAIL_AFTER_BACKUP", "NTM_TEST_COMPLETION_ACK_FAIL_ONCE", "NTM_TEST_COMPLETION_LEASE_DURATION", "NTM_TEST_COMPLETION_HANDLER_DELAY":
			t.Fatalf("isolated process environment retained %s", key)
		}
	}
}

func TestE2EAtomicAssignmentProductionCLI(t *testing.T) {
	CommonE2EPrerequisites(t)
	testutil.RequireTmuxThrottled(t)

	fixture := newAtomicAssignmentCLIFixture(t)
	directBead := fixture.createBead(t, "Atomic direct assignment")
	bulkBead := fixture.createBead(t, "Atomic bulk assignment")
	directPrompt := fmt.Sprintf("NTM_ATOMIC_DIRECT_%d", time.Now().UnixNano())

	// Reservation is enabled by default. An unavailable Agent Mail endpoint
	// must stop before the br claim and before tmux transport.
	failed := fixture.runNTM(t, nil, atomicDirectArgs(fixture, directBead, directPrompt, true)...)
	if failed.exitCode == 0 {
		t.Fatalf("reservation-required assignment exited 0: stdout=%s stderr=%s", failed.stdout, failed.stderr)
	}
	if len(bytes.TrimSpace(failed.stderr)) != 0 {
		t.Fatalf("reservation failure stderr = %q", failed.stderr)
	}
	var failedEnvelope atomicAssignmentDirectEnvelope
	decodeAtomicAssignmentJSON(t, failed.stdout, &failedEnvelope)
	if failedEnvelope.Success || failedEnvelope.Error == nil || failedEnvelope.Error.Code != "RESERVATION_REQUIRED" ||
		!strings.Contains(failedEnvelope.Error.Message, "file reservation is required but unavailable") {
		t.Fatalf("reservation failure envelope = %+v", failedEnvelope)
	}
	fixture.assertBead(t, directBead, "open", "")
	fixture.assertLedgerHasNoAssignment(t, directBead)
	fixture.assertMarkerCounts(t, directPrompt, map[int]int{0: 0, 1: 0})

	// Explicitly disabling reservations permits the same intent to proceed.
	// The command is a new OS process and uses the production direct adapter.
	succeeded := fixture.runNTM(t, nil, atomicDirectArgs(fixture, directBead, directPrompt, false)...)
	if succeeded.exitCode != 0 {
		t.Fatalf("direct assignment exit=%d stdout=%s stderr=%s", succeeded.exitCode, succeeded.stdout, succeeded.stderr)
	}
	if len(bytes.TrimSpace(succeeded.stderr)) != 0 {
		t.Fatalf("direct assignment stderr = %q", succeeded.stderr)
	}
	var directEnvelope atomicAssignmentDirectEnvelope
	decodeAtomicAssignmentJSON(t, succeeded.stdout, &directEnvelope)
	assertDirectAssignmentEnvelope(t, directEnvelope, fixture.session, directBead, directPrompt, fixture.panes[0])
	fixture.waitForMarkerCount(t, 0, directPrompt, 1)
	fixture.assertMarkerCounts(t, directPrompt, map[int]int{0: 1, 1: 0})

	directRecord := fixture.readLedgerAssignment(t, directBead)
	assertAtomicAssignmentRecord(t, directRecord, directBead, directPrompt, fixture.panes[0], "codex")
	fixture.assertBead(t, directBead, "in_progress", directRecord.ClaimActor)
	directKey := directRecord.IdempotencyKey
	directDispatchedAt := *directRecord.DispatchedAt
	directReceiptID := directRecord.DispatchReceiptID

	// A second built CLI process with the same raw intent must replay the
	// durable receipt without another claim, observation, or tmux delivery.
	// Move the same physical pane to a different local index first: `%N` is the
	// durable identity, while Pane is display metadata that may drift.
	fixture.mustTMUX(t, "swap-pane", "-s", fixture.panes[0].ID, "-t", fixture.panes[1].ID)
	liveTopology, err := fixture.tmuxOutput(context.Background(), "list-panes", "-s", "-t", fixture.session, "-F", "#{pane_id}:#{pane_index}")
	if err != nil || !strings.Contains(string(liveTopology), fixture.panes[0].ID+":1") {
		t.Fatalf("physical pane did not move to local index 1: topology=%s error=%v", liveTopology, err)
	}
	replay := fixture.runNTM(t, nil, atomicDirectArgsForSelector(fixture, fixture.panes[0].ID, directBead, directPrompt, false)...)
	if replay.exitCode != 0 || len(bytes.TrimSpace(replay.stderr)) != 0 {
		t.Fatalf("direct replay exit=%d stdout=%s stderr=%s", replay.exitCode, replay.stdout, replay.stderr)
	}
	var replayEnvelope atomicAssignmentDirectEnvelope
	decodeAtomicAssignmentJSON(t, replay.stdout, &replayEnvelope)
	movedPane := fixture.panes[0]
	movedPane.Index = 1
	movedPane.Target = "1"
	assertDirectAssignmentEnvelopeWithReceiptPane(
		t, replayEnvelope, fixture.session, directBead, directPrompt, fixture.panes[0].Index, movedPane,
	)
	if directEnvelope.Data == nil || directEnvelope.Data.Receipt == nil || replayEnvelope.Data == nil || replayEnvelope.Data.Receipt == nil ||
		directEnvelope.Data.Receipt.Transport.DeliveryID != replayEnvelope.Data.Receipt.Transport.DeliveryID ||
		directEnvelope.Data.Receipt.Timestamp != replayEnvelope.Data.Receipt.Timestamp {
		t.Fatalf("direct replay receipt changed: first=%+v replay=%+v", directEnvelope.Data, replayEnvelope.Data)
	}
	fixture.assertMarkerCounts(t, directPrompt, map[int]int{0: 1, 1: 0})
	reloadedDirect := fixture.readLedgerAssignment(t, directBead)
	if reloadedDirect.IdempotencyKey != directKey || reloadedDirect.DispatchAttempts != 1 ||
		reloadedDirect.DispatchedAt == nil || !reloadedDirect.DispatchedAt.Equal(directDispatchedAt) ||
		reloadedDirect.DispatchReceiptID != directReceiptID {
		t.Fatalf("replay process mutated durable direct receipt: before=%+v after=%+v", directRecord, reloadedDirect)
	}
	fixture.assertBead(t, directBead, "in_progress", directRecord.ClaimActor)
	fixture.mustTMUX(t, "swap-pane", "-s", fixture.panes[0].ID, "-t", fixture.panes[1].ID)
	liveTopology, err = fixture.tmuxOutput(context.Background(), "list-panes", "-s", "-t", fixture.session, "-F", "#{pane_id}:#{pane_index}")
	if err != nil || !strings.Contains(string(liveTopology), fixture.panes[0].ID+":0") {
		t.Fatalf("physical pane did not return to local index 0: topology=%s error=%v", liveTopology, err)
	}

	changedPrompt := directPrompt + "_CHANGED"
	changed := fixture.runNTM(t, nil, atomicDirectArgs(fixture, directBead, changedPrompt, false)...)
	if changed.exitCode == 0 || len(bytes.TrimSpace(changed.stderr)) != 0 {
		t.Fatalf("changed direct intent exit=%d stdout=%s stderr=%s", changed.exitCode, changed.stdout, changed.stderr)
	}
	var changedEnvelope atomicAssignmentDirectEnvelope
	decodeAtomicAssignmentJSON(t, changed.stdout, &changedEnvelope)
	if changedEnvelope.Success || changedEnvelope.Error == nil || changedEnvelope.Error.Code != "CLAIM_CONFLICT" {
		t.Fatalf("changed direct intent envelope = %+v", changedEnvelope)
	}
	fixture.assertMarkerCounts(t, changedPrompt, map[int]int{0: 0, 1: 0})

	// Robot bulk assignment has its own production adapter and intentionally
	// reuses a matching persisted idempotency key. Run it twice from separate
	// CLI processes and prove the second response is a side-effect-free replay.
	bulkTemplate := filepath.Join(fixture.projectDir, "atomic-bulk-template.txt")
	bulkTemplateBody := fmt.Sprintf("NTM_ATOMIC_BULK_%d_{bead_id}", time.Now().UnixNano())
	if err := os.WriteFile(bulkTemplate, []byte(bulkTemplateBody), 0o600); err != nil {
		t.Fatalf("write bulk prompt template: %v", err)
	}
	bulkPrompt := strings.ReplaceAll(bulkTemplateBody, "{bead_id}", bulkBead)
	bulkArgs := atomicBulkArgs(fixture, bulkBead, bulkTemplate)

	fixture.primePaneForSafeDispatch(t, 1)
	bulkFirstResult := fixture.runNTM(t, nil, bulkArgs...)
	if bulkFirstResult.exitCode != 0 {
		t.Fatalf("bulk assignment exit=%d stdout=%s stderr=%s", bulkFirstResult.exitCode, bulkFirstResult.stdout, bulkFirstResult.stderr)
	}
	if len(bytes.TrimSpace(bulkFirstResult.stderr)) != 0 {
		t.Fatalf("bulk assignment stderr = %q", bulkFirstResult.stderr)
	}
	var bulkFirst atomicAssignmentBulkEnvelope
	decodeAtomicAssignmentJSON(t, bulkFirstResult.stdout, &bulkFirst)
	bulkFirstAssignment := assertBulkAssignmentEnvelope(t, bulkFirst, bulkBead, "1")
	fixture.waitForMarkerCount(t, 1, bulkPrompt, 1)
	fixture.assertMarkerCounts(t, bulkPrompt, map[int]int{0: 0, 1: 1})

	bulkRecord := fixture.readLedgerAssignment(t, bulkBead)
	assertAtomicAssignmentRecord(t, bulkRecord, bulkBead, bulkPrompt, fixture.panes[1], "claude")
	fixture.assertBead(t, bulkBead, "in_progress", bulkRecord.ClaimActor)
	if bulkFirstAssignment.IdempotencyKey != bulkRecord.IdempotencyKey ||
		bulkFirstAssignment.ClaimActor != bulkRecord.ClaimActor ||
		bulkFirstAssignment.DispatchReceiptID != bulkRecord.DispatchReceiptID {
		t.Fatalf("bulk response does not match ledger: response=%+v ledger=%+v", bulkFirstAssignment, bulkRecord)
	}

	// The first assignment leaves its marker as the pane's trailing line. Put
	// the fake agent back at a recognizable idle prompt so replay crosses the
	// same production observation gate before reaching the durable receipt.
	fixture.primePaneForSafeDispatch(t, 1)
	bulkSecondResult := fixture.runNTM(t, nil, bulkArgs...)
	if bulkSecondResult.exitCode != 0 {
		t.Fatalf("bulk replay exit=%d stdout=%s stderr=%s", bulkSecondResult.exitCode, bulkSecondResult.stdout, bulkSecondResult.stderr)
	}
	var bulkSecond atomicAssignmentBulkEnvelope
	decodeAtomicAssignmentJSON(t, bulkSecondResult.stdout, &bulkSecond)
	bulkSecondAssignment := assertBulkAssignmentEnvelope(t, bulkSecond, bulkBead, "1")
	if bulkSecondAssignment.IdempotencyKey != bulkFirstAssignment.IdempotencyKey ||
		bulkSecondAssignment.DispatchReceiptID != bulkFirstAssignment.DispatchReceiptID ||
		bulkSecondAssignment.ClaimActor != bulkFirstAssignment.ClaimActor {
		t.Fatalf("bulk replay identity changed: first=%+v second=%+v", bulkFirstAssignment, bulkSecondAssignment)
	}
	fixture.assertMarkerCounts(t, bulkPrompt, map[int]int{0: 0, 1: 1})
	reloadedBulk := fixture.readLedgerAssignment(t, bulkBead)
	if reloadedBulk.IdempotencyKey != bulkRecord.IdempotencyKey || reloadedBulk.DispatchAttempts != 1 ||
		reloadedBulk.DispatchedAt == nil || bulkRecord.DispatchedAt == nil ||
		!reloadedBulk.DispatchedAt.Equal(*bulkRecord.DispatchedAt) ||
		reloadedBulk.DispatchReceiptID != bulkRecord.DispatchReceiptID {
		t.Fatalf("bulk replay mutated durable delivery: before=%+v after=%+v", bulkRecord, reloadedBulk)
	}
	fixture.assertBead(t, bulkBead, "in_progress", bulkRecord.ClaimActor)
}

func TestE2EAtomicAssignmentVerboseJSONSuppressesDependencyStderrBuiltProcess(t *testing.T) {
	CommonE2EPrerequisites(t)
	testutil.RequireTmuxThrottled(t)

	type verboseAssignEnvelope struct {
		Command  string   `json:"command"`
		Session  string   `json:"session"`
		Success  bool     `json:"success"`
		Warnings []string `json:"warnings"`
		Data     *struct {
			Assignments []struct {
				BeadID string `json:"bead_id"`
			} `json:"assignments"`
			Summary struct {
				ActionableCount int `json:"actionable_count"`
			} `json:"summary"`
		} `json:"data"`
		Error *struct {
			Code    string `json:"code"`
			Message string `json:"message"`
		} `json:"error"`
	}
	decodeExactlyOne := func(t *testing.T, result atomicAssignmentProcessResult) verboseAssignEnvelope {
		t.Helper()
		decoder := json.NewDecoder(bytes.NewReader(bytes.TrimSpace(result.stdout)))
		var envelope verboseAssignEnvelope
		if err := decoder.Decode(&envelope); err != nil {
			t.Fatalf("decode verbose assign JSON: %v raw=%s", err, result.stdout)
		}
		var trailing any
		if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
			t.Fatalf("verbose assign emitted trailing output: err=%v trailing=%v raw=%s", err, trailing, result.stdout)
		}
		return envelope
	}
	installVerboseBV := func(t *testing.T, fixture *atomicAssignmentCLIFixture, name, insightsMode string) (map[string]string, string) {
		t.Helper()
		binDir := filepath.Join(fixture.root, "verbose-bv-"+name)
		if err := os.MkdirAll(binDir, 0o700); err != nil {
			t.Fatalf("create verbose BV wrapper directory: %v", err)
		}
		tracePath := filepath.Join(binDir, "calls.log")
		script := `#!/bin/sh
printf '%s\n' "${1:-}" >> "$NTM_E2E_VERBOSE_BV_TRACE"
case "${1:-}" in
    --robot-triage)
        printf 'injected verbose triage failure\n' >&2
        exit 71
        ;;
    --robot-insights)
        if [ "$NTM_E2E_VERBOSE_BV_INSIGHTS" = "success" ]; then
            printf '%s\n' '{"Cycles":[]}'
            exit 0
        fi
        printf 'injected verbose insights failure\n' >&2
        exit 72
        ;;
    *)
        printf 'unexpected verbose BV arguments: %s\n' "$*" >&2
        exit 73
        ;;
esac
`
		if err := os.WriteFile(filepath.Join(binDir, "bv"), []byte(script), 0o700); err != nil {
			t.Fatalf("write verbose BV wrapper: %v", err)
		}
		return map[string]string{
			"PATH":                        binDir + string(os.PathListSeparator) + atomicAssignmentEnvValue(fixture.env, "PATH"),
			"NTM_E2E_VERBOSE_BV_TRACE":    tracePath,
			"NTM_E2E_VERBOSE_BV_INSIGHTS": insightsMode,
		}, tracePath
	}
	assertTriageIsTerminal := func(t *testing.T, tracePath string) {
		t.Helper()
		trace, err := os.ReadFile(tracePath)
		if err != nil {
			t.Fatalf("read verbose BV trace: %v", err)
		}
		if string(trace) != "--robot-triage\n" {
			t.Fatalf("verbose dependency calls=%q, want terminal triage call only", trace)
		}
	}
	args := func(fixture *atomicAssignmentCLIFixture, beadID string) []string {
		return []string{
			"assign", fixture.session,
			"--repo=" + fixture.projectDir,
			"--beads=" + beadID,
			"--json",
			"--verbose",
			"--reserve-files=false",
			"--timeout=15s",
		}
	}

	for _, test := range []struct {
		name         string
		insightsMode string
	}{
		{name: "insights available", insightsMode: "success"},
		{name: "insights unavailable", insightsMode: "failure"},
	} {
		t.Run(test.name, func(t *testing.T) {
			fixture := newAtomicAssignmentCLIFixture(t)
			title := "Verbose JSON triage failure is terminal " + test.name
			beadID := fixture.createBead(t, title)
			env, tracePath := installVerboseBV(t, fixture, strings.ReplaceAll(test.name, " ", "-"), test.insightsMode)
			beforeFiles := snapshotAtomicAssignmentFiles(t, fixture.ledgerPath(), fixture.ledgerPath()+".bak")
			beforePanes := snapshotAtomicAssignmentPanes(t, fixture)

			result := fixture.runNTM(t, env, args(fixture, beadID)...)
			if result.exitCode != 1 || len(result.stderr) != 0 {
				t.Fatalf("verbose JSON terminal triage failure exit=%d stdout=%s stderr=%s", result.exitCode, result.stdout, result.stderr)
			}
			envelope := decodeExactlyOne(t, result)
			if envelope.Success || envelope.Command != "assign" || envelope.Session != fixture.session || envelope.Data != nil ||
				envelope.Error == nil || envelope.Error.Code != "ASSIGN_ERROR" ||
				!strings.Contains(envelope.Error.Message, "actionable work could not be verified") ||
				!strings.Contains(envelope.Error.Message, "injected verbose triage failure") || len(envelope.Warnings) != 0 {
				t.Fatalf("verbose JSON terminal triage envelope=%+v", envelope)
			}
			assertTriageIsTerminal(t, tracePath)
			fixture.assertBead(t, beadID, "open", "")
			assertAtomicAssignmentFilesUnchanged(t, beforeFiles)
			assertAtomicAssignmentPanesUnchanged(t, fixture, beforePanes)
			fixture.assertMarkerCounts(t, title, map[int]int{0: 0, 1: 0})
		})
	}
}

func TestE2EAtomicAssignmentReceiptBackupRecoveryBuiltProcess(t *testing.T) {
	CommonE2EPrerequisites(t)
	testutil.RequireTmuxThrottled(t)

	fixture := newAtomicAssignmentCLIFixture(t)
	beadID := fixture.createBead(t, "Atomic receipt backup recovery")
	marker := "NTM_ATOMIC_BACKUP_RECEIPT_" + strings.ToUpper(strings.ReplaceAll(beadID, "-", "_"))
	args := atomicDirectArgs(fixture, beadID, marker, false)

	failed := fixture.runNTM(t, map[string]string{"NTM_TEST_ASSIGNMENT_SAVE_FAIL_AFTER_BACKUP": beadID}, args...)
	if failed.exitCode == 0 || len(bytes.TrimSpace(failed.stderr)) != 0 {
		t.Fatalf("injected receipt persistence exit=%d stdout=%s stderr=%s", failed.exitCode, failed.stdout, failed.stderr)
	}
	var failedEnvelope atomicAssignmentDirectEnvelope
	decodeAtomicAssignmentJSON(t, failed.stdout, &failedEnvelope)
	if failedEnvelope.Success || failedEnvelope.Error == nil || failedEnvelope.Error.Code != "DISPATCH_UNKNOWN" {
		t.Fatalf("injected receipt persistence envelope=%+v", failedEnvelope)
	}
	fixture.waitForMarkerCount(t, 0, marker, 1)
	fixture.assertMarkerCounts(t, marker, map[int]int{0: 1, 1: 0})

	primary, primaryData := readAtomicAssignmentLedgerAt(t, fixture.ledgerPath())
	backup, backupData := readAtomicAssignmentLedgerAt(t, fixture.ledgerPath()+".bak")
	primaryRecord := primary.Assignments[beadID]
	backupRecord := backup.Assignments[beadID]
	if primaryRecord == nil || primaryRecord.DispatchState != "sending" || primaryRecord.DispatchReceiptID != "" {
		t.Fatalf("pre-restart primary record=%+v", primaryRecord)
	}
	if backupRecord == nil || backupRecord.DispatchState != "sent" || backupRecord.DispatchReceiptID == "" || backupRecord.DispatchedAt == nil {
		t.Fatalf("pre-restart backup record=%+v", backupRecord)
	}
	if backup.PersistenceGeneration != primary.PersistenceGeneration+1 {
		t.Fatalf("publication generations primary=%d backup=%d", primary.PersistenceGeneration, backup.PersistenceGeneration)
	}
	if bytes.Equal(primaryData, backupData) {
		t.Fatal("injected command did not stop between backup and primary publication")
	}
	if failedEnvelope.Data == nil || failedEnvelope.Data.Receipt == nil ||
		failedEnvelope.Data.Receipt.Transport.DeliveryID != backupRecord.DispatchReceiptID {
		t.Fatalf("failed-process delivery receipt=%+v backup=%+v", failedEnvelope.Data, backupRecord)
	}

	replayed := fixture.runNTM(t, nil, args...)
	if replayed.exitCode != 0 || len(bytes.TrimSpace(replayed.stderr)) != 0 {
		t.Fatalf("restart replay exit=%d stdout=%s stderr=%s", replayed.exitCode, replayed.stdout, replayed.stderr)
	}
	var replayEnvelope atomicAssignmentDirectEnvelope
	decodeAtomicAssignmentJSON(t, replayed.stdout, &replayEnvelope)
	assertDirectAssignmentEnvelope(t, replayEnvelope, fixture.session, beadID, marker, fixture.panes[0])
	if replayEnvelope.Data == nil || replayEnvelope.Data.Receipt == nil ||
		replayEnvelope.Data.Receipt.Transport.DeliveryID != backupRecord.DispatchReceiptID {
		t.Fatalf("restart replay receipt=%+v want delivery %q", replayEnvelope.Data, backupRecord.DispatchReceiptID)
	}
	fixture.assertMarkerCounts(t, marker, map[int]int{0: 1, 1: 0})

	finalLedger, finalData := readAtomicAssignmentLedgerAt(t, fixture.ledgerPath())
	finalRecord := finalLedger.Assignments[beadID]
	if finalRecord == nil || finalRecord.DispatchState != "sent" || finalRecord.DispatchReceiptID != backupRecord.DispatchReceiptID ||
		finalRecord.IdempotencyKey != backupRecord.IdempotencyKey || finalRecord.DispatchAttempts != backupRecord.DispatchAttempts ||
		finalRecord.DispatchedAt == nil || !finalRecord.DispatchedAt.Equal(*backupRecord.DispatchedAt) {
		t.Fatalf("restart replay mutated recovered receipt: backup=%+v final=%+v", backupRecord, finalRecord)
	}
	if finalLedger.PersistenceGeneration != backup.PersistenceGeneration || !bytes.Equal(finalData, backupData) {
		t.Fatalf("promoted primary generation=%d want=%d exact_bytes=%v", finalLedger.PersistenceGeneration, backup.PersistenceGeneration, bytes.Equal(finalData, backupData))
	}
	fixture.assertBead(t, beadID, "in_progress", backupRecord.ClaimActor)
}

func TestE2EAtomicAssignmentSplitBrainLedgerFailsClosedBuiltProcess(t *testing.T) {
	CommonE2EPrerequisites(t)
	testutil.RequireTmuxThrottled(t)

	fixture := newAtomicAssignmentCLIFixture(t)
	beadID := fixture.createBead(t, "Split brain ledger internal/cli/split_brain.go")
	marker := "NTM_ATOMIC_SPLIT_BRAIN_" + strings.ToUpper(strings.ReplaceAll(beadID, "-", "_"))
	mail := newAtomicAssignmentMailStub(fixture.projectDir)
	defer mail.close()

	ledgerPath := fixture.ledgerPath()
	if err := os.MkdirAll(filepath.Dir(ledgerPath), 0o700); err != nil {
		t.Fatalf("create split-brain ledger directory: %v", err)
	}
	updatedAt := time.Date(2026, time.July, 13, 12, 0, 0, 0, time.UTC)
	primaryDocument := map[string]any{
		"session_name":           fixture.session,
		"assignments":            map[string]any{},
		"cleared_generations":    map[string]uint64{},
		"persistence_generation": uint64(1),
		"updated_at":             updatedAt,
		"version":                9,
	}
	backupDocument := map[string]any{
		"session_name":           fixture.session,
		"assignments":            map[string]any{},
		"cleared_generations":    map[string]uint64{},
		"persistence_generation": uint64(1),
		"updated_at":             updatedAt,
		"version":                9,
		"split_brain_marker":     "backup-divergence",
	}
	primaryData, err := json.MarshalIndent(primaryDocument, "", "  ")
	if err != nil {
		t.Fatalf("marshal split-brain primary: %v", err)
	}
	backupData, err := json.MarshalIndent(backupDocument, "", "  ")
	if err != nil {
		t.Fatalf("marshal split-brain backup: %v", err)
	}
	primaryData = append(primaryData, '\n')
	backupData = append(backupData, '\n')
	if err := os.WriteFile(ledgerPath, primaryData, 0o600); err != nil {
		t.Fatalf("write split-brain primary: %v", err)
	}
	if err := os.WriteFile(ledgerPath+".bak", backupData, 0o600); err != nil {
		t.Fatalf("write split-brain backup: %v", err)
	}

	result := fixture.runNTM(t, mail.env(), atomicDirectArgs(fixture, beadID, marker, true)...)
	if result.exitCode != 1 || len(bytes.TrimSpace(result.stderr)) != 0 {
		t.Fatalf("split-brain assignment exit=%d stdout=%s stderr=%s", result.exitCode, result.stdout, result.stderr)
	}
	var envelope atomicAssignmentDirectEnvelope
	decodeAtomicAssignmentJSON(t, result.stdout, &envelope)
	if envelope.Success || envelope.Data != nil || envelope.Error == nil || envelope.Error.Code != "STORE_ERROR" ||
		!strings.Contains(envelope.Error.Message, "diverge at persistence generation 1") {
		t.Fatalf("split-brain assignment envelope=%+v", envelope)
	}

	primaryAfter, err := os.ReadFile(ledgerPath)
	if err != nil {
		t.Fatalf("read split-brain primary after rejection: %v", err)
	}
	backupAfter, err := os.ReadFile(ledgerPath + ".bak")
	if err != nil {
		t.Fatalf("read split-brain backup after rejection: %v", err)
	}
	if !bytes.Equal(primaryAfter, primaryData) || !bytes.Equal(backupAfter, backupData) {
		t.Fatalf("split-brain artifacts changed: primary=%v backup=%v", bytes.Equal(primaryAfter, primaryData), bytes.Equal(backupAfter, backupData))
	}
	fixture.assertMarkerCounts(t, marker, map[int]int{0: 0, 1: 0})
	fixture.assertBead(t, beadID, "open", "")
	mailState := mail.snapshot()
	if mailState.EnsureCalls != 0 || mailState.ReserveCalls != 0 || mailState.ListCalls != 0 ||
		mailState.ReleaseCalls != 0 || mailState.SendCalls != 0 || len(mailState.Active) != 0 {
		t.Fatalf("split-brain rejection crossed Agent Mail boundary: %+v", mailState)
	}
}

func TestE2EAtomicAssignmentReservationRecoveryBuiltProcess(t *testing.T) {
	CommonE2EPrerequisites(t)
	testutil.RequireTmuxThrottled(t)

	fixture := newAtomicAssignmentCLIFixture(t)
	const zeroTitle = "Zero grant internal/cli/zero_grant.go"
	const partialTitle = "Partial grant internal/cli/partial_grant.go internal/robot/partial_grant.go"
	const unknownTitle = "Unknown response internal/cli/unknown_response.go"
	zeroBeadID := fixture.createBead(t, zeroTitle)
	partialBeadID := fixture.createBead(t, partialTitle)
	unknownBeadID := fixture.createBead(t, unknownTitle)
	templatePath := filepath.Join(fixture.projectDir, "atomic-reservation-template.txt")
	templateBody := fmt.Sprintf("NTM_ATOMIC_RESERVATION_%d::{BEAD_ID}::{TITLE}", time.Now().UnixNano())
	if err := os.WriteFile(templatePath, []byte(templateBody), 0o600); err != nil {
		t.Fatalf("write reservation prompt template: %v", err)
	}
	reservationPrompt := func(beadID, title string) string {
		return strings.ReplaceAll(strings.ReplaceAll(templateBody, "{BEAD_ID}", beadID), "{TITLE}", title)
	}
	reservationArgs := func(pane int, beadID string) []string {
		return []string{
			"assign", fixture.session, "--repo=" + fixture.projectDir,
			fmt.Sprintf("--pane=%d", pane), "--beads=" + beadID,
			"--template=custom", "--template-file=" + templatePath,
			"--force", "--ignore-deps", "--timeout=60s", "--json",
		}
	}
	zeroPrompt := reservationPrompt(zeroBeadID, zeroTitle)
	partialPrompt := reservationPrompt(partialBeadID, partialTitle)
	unknownPrompt := reservationPrompt(unknownBeadID, unknownTitle)

	var stubMu sync.Mutex
	modes := map[string]string{zeroBeadID: "zero", partialBeadID: "partial", unknownBeadID: "drop_after_grant"}
	reserveCalls := make(map[string]int)
	activeReservations := make(map[string][]map[string]any)
	var releasedIDs []int
	dropNextReleaseResponse := false
	mailServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodGet && r.URL.Path == "/health/liveness" {
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"status":"ok"}`))
			return
		}
		var request struct {
			JSONRPC string `json:"jsonrpc"`
			ID      any    `json:"id"`
			Method  string `json:"method"`
			Params  struct {
				Name      string         `json:"name"`
				Arguments map[string]any `json:"arguments"`
			} `json:"params"`
		}
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		if request.JSONRPC == "2.0" && request.Method == "resources/read" {
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(map[string]any{
				"jsonrpc": "2.0", "id": request.ID,
				"error": map[string]any{"code": -32601, "message": "resource view not supported"},
			})
			return
		}
		if request.JSONRPC != "2.0" || request.Method != "tools/call" {
			http.Error(w, "expected JSON-RPC tools/call", http.StatusBadRequest)
			return
		}
		writeResult := func(result any) {
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(map[string]any{"jsonrpc": "2.0", "id": request.ID, "result": result})
		}
		switch request.Params.Name {
		case "health_check":
			writeResult(map[string]any{"status": "ok", "timestamp": time.Now().UTC().Format(time.RFC3339Nano)})
		case "ensure_project":
			if request.Params.Arguments["human_key"] != fixture.projectDir {
				http.Error(w, fmt.Sprintf("unexpected ensure project args: %#v", request.Params.Arguments), http.StatusBadRequest)
				return
			}
			writeResult(map[string]any{"id": 1, "slug": "atomic-e2e", "human_key": fixture.projectDir, "created_at": time.Now().UTC().Format(time.RFC3339Nano)})
		case "file_reservation_paths":
			reason, _ := request.Params.Arguments["reason"].(string)
			beadID := strings.TrimPrefix(reason, "bead assignment: ")
			paths, pathsOK := anyStringSlice(request.Params.Arguments["paths"])
			if request.Params.Arguments["project_key"] != fixture.projectDir || !pathsOK || len(paths) == 0 || beadID == reason {
				http.Error(w, fmt.Sprintf("unexpected reservation args: %#v", request.Params.Arguments), http.StatusBadRequest)
				return
			}
			stubMu.Lock()
			mode := modes[beadID]
			reserveCalls[beadID]++
			stubMu.Unlock()
			makeGrant := func(id int, path string) map[string]any {
				now := time.Now().UTC()
				return map[string]any{
					"id": id, "path_pattern": path, "agent_name": request.Params.Arguments["agent_name"],
					"project_id": 1, "exclusive": true, "reason": reason,
					"created_ts": now.Format(time.RFC3339Nano), "expires_ts": now.Add(time.Hour).Format(time.RFC3339Nano),
				}
			}
			switch mode {
			case "zero":
				writeResult(map[string]any{
					"granted":   []any{},
					"conflicts": []map[string]any{{"path": paths[0], "holders": []string{"OtherAgent"}}},
				})
			case "partial":
				if len(paths) < 2 {
					http.Error(w, fmt.Sprintf("partial fixture needs two paths: %v", paths), http.StatusBadRequest)
					return
				}
				grant := makeGrant(9201, paths[0])
				stubMu.Lock()
				activeReservations[beadID] = []map[string]any{grant}
				stubMu.Unlock()
				writeResult(map[string]any{
					"granted":   []map[string]any{grant},
					"conflicts": []map[string]any{{"path": paths[1], "holders": []string{"OtherAgent"}}},
				})
			case "success":
				granted := make([]map[string]any, 0, len(paths))
				for index, path := range paths {
					granted = append(granted, makeGrant(9101+index, path))
				}
				stubMu.Lock()
				activeReservations[beadID] = append([]map[string]any(nil), granted...)
				stubMu.Unlock()
				writeResult(map[string]any{"granted": granted, "conflicts": []any{}})
			case "drop_after_grant":
				granted := make([]map[string]any, 0, len(paths))
				for index, path := range paths {
					granted = append(granted, makeGrant(9301+index, path))
				}
				stubMu.Lock()
				activeReservations[beadID] = append([]map[string]any(nil), granted...)
				stubMu.Unlock()
				panic(http.ErrAbortHandler)
			default:
				http.Error(w, "unknown reservation mode for "+beadID, http.StatusBadRequest)
			}
		case "list_file_reservations":
			stubMu.Lock()
			var active []map[string]any
			for _, reservations := range activeReservations {
				active = append(active, reservations...)
			}
			stubMu.Unlock()
			writeResult(active)
		case "release_file_reservations":
			ids, idsOK := atomicAssignmentAnyIntSlice(request.Params.Arguments["file_reservation_ids"])
			if request.Params.Arguments["project_key"] != fixture.projectDir || !idsOK || len(ids) == 0 {
				http.Error(w, fmt.Sprintf("unexpected release args: %#v", request.Params.Arguments), http.StatusBadRequest)
				return
			}
			stubMu.Lock()
			releasedIDs = append(releasedIDs, ids...)
			wanted := make(map[int]struct{}, len(ids))
			for _, id := range ids {
				wanted[id] = struct{}{}
			}
			for beadID, reservations := range activeReservations {
				remaining := reservations[:0]
				for _, reservation := range reservations {
					id, _ := reservation["id"].(int)
					if _, released := wanted[id]; !released {
						remaining = append(remaining, reservation)
					}
				}
				activeReservations[beadID] = remaining
			}
			dropResponse := dropNextReleaseResponse
			dropNextReleaseResponse = false
			stubMu.Unlock()
			if dropResponse {
				panic(http.ErrAbortHandler)
			}
			writeResult(map[string]any{"released": len(ids)})
		default:
			http.Error(w, "unexpected tool: "+request.Params.Name, http.StatusNotFound)
		}
	}))
	defer mailServer.Close()
	env := map[string]string{"AGENT_MAIL_URL": mailServer.URL + "/"}

	zeroFailure := fixture.runNTM(t, env, reservationArgs(0, zeroBeadID)...)
	if zeroFailure.exitCode != 1 || len(bytes.TrimSpace(zeroFailure.stderr)) != 0 {
		t.Fatalf("zero-grant assignment exit=%d stdout=%s stderr=%s", zeroFailure.exitCode, zeroFailure.stdout, zeroFailure.stderr)
	}
	var zeroFailureEnvelope atomicAssignmentDirectEnvelope
	decodeAtomicAssignmentJSON(t, zeroFailure.stdout, &zeroFailureEnvelope)
	if zeroFailureEnvelope.Success || zeroFailureEnvelope.Error == nil || zeroFailureEnvelope.Error.Code != "ASSIGN_ERROR" {
		t.Fatalf("zero-grant envelope = %+v", zeroFailureEnvelope)
	}
	zeroPending := fixture.readLedgerAssignment(t, zeroBeadID)
	if zeroPending.ReservationState != "failed" || zeroPending.ReservationAttempts != 1 || zeroPending.ReservationError == "" ||
		len(zeroPending.ReservationIDs) != 0 || len(zeroPending.ReservedPaths) != 0 || zeroPending.DispatchAttempts != 0 {
		t.Fatalf("zero-grant durable state = %+v", zeroPending)
	}
	fixture.assertMarkerCounts(t, zeroPrompt, map[int]int{0: 0, 1: 0})
	stubMu.Lock()
	modes[zeroBeadID] = "success"
	stubMu.Unlock()
	fixture.primePaneForSafeDispatch(t, 0)
	// The production observer requires the explicit prompt to remain quiet for
	// its five-second activity threshold before it authorizes actuation.
	time.Sleep(5500 * time.Millisecond)
	zeroRetry := fixture.runNTM(t, env,
		"assign", fixture.session, "--repo="+fixture.projectDir,
		"--retry="+zeroBeadID, "--timeout=60s", "--json",
	)
	if zeroRetry.exitCode != 0 || len(bytes.TrimSpace(zeroRetry.stderr)) != 0 {
		t.Fatalf("zero-grant recovery exit=%d stdout=%s stderr=%s", zeroRetry.exitCode, zeroRetry.stdout, zeroRetry.stderr)
	}
	fixture.waitForMarkerCount(t, 0, zeroPrompt, 1)
	zeroRecovered := fixture.readLedgerAssignment(t, zeroBeadID)
	if zeroRecovered.ReservationState != "reserved" || !zeroRecovered.ReservationCompleted || len(zeroRecovered.ReservationIDs) == 0 ||
		zeroRecovered.DispatchState != "sent" || zeroRecovered.DispatchAttempts != 1 {
		t.Fatalf("zero-grant recovery durable state = %+v", zeroRecovered)
	}

	partialFailure := fixture.runNTM(t, env, reservationArgs(1, partialBeadID)...)
	if partialFailure.exitCode != 1 || len(bytes.TrimSpace(partialFailure.stderr)) != 0 {
		t.Fatalf("partial-grant assignment exit=%d stdout=%s stderr=%s", partialFailure.exitCode, partialFailure.stdout, partialFailure.stderr)
	}
	partialPending := fixture.readLedgerAssignment(t, partialBeadID)
	if partialPending.ReservationState != "reserved" || partialPending.ReservationCompleted ||
		!reflect.DeepEqual(partialPending.ReservationIDs, []int{9201}) || len(partialPending.ReservedPaths) != 1 ||
		!strings.Contains(partialPending.ReservationError, "file reservation must be reconciled or released") || partialPending.DispatchAttempts != 0 {
		t.Fatalf("partial-grant durable state = %+v", partialPending)
	}
	stubMu.Lock()
	partialReserveCalls := reserveCalls[partialBeadID]
	stubMu.Unlock()
	partialRetry := fixture.runNTM(t, env,
		"assign", fixture.session, "--repo="+fixture.projectDir,
		"--retry="+partialBeadID, "--timeout=60s", "--json",
	)
	if partialRetry.exitCode != 1 || len(bytes.TrimSpace(partialRetry.stderr)) != 0 {
		t.Fatalf("partial-grant retry exit=%d stdout=%s stderr=%s", partialRetry.exitCode, partialRetry.stdout, partialRetry.stderr)
	}
	var partialRetryEnvelope atomicAssignmentRetryEnvelope
	decodeAtomicAssignmentJSON(t, partialRetry.stdout, &partialRetryEnvelope)
	if partialRetryEnvelope.Success || partialRetryEnvelope.Error == nil || partialRetryEnvelope.Error.Code != "RETRY_SKIPPED" ||
		partialRetryEnvelope.Data == nil || partialRetryEnvelope.Data.Summary.RetriedCount != 0 || partialRetryEnvelope.Data.Summary.SkippedCount != 1 {
		t.Fatalf("partial-grant retry envelope = %+v", partialRetryEnvelope)
	}
	stubMu.Lock()
	if reserveCalls[partialBeadID] != partialReserveCalls {
		t.Fatalf("partial-grant retry repeated reservation: before=%d after=%d", partialReserveCalls, reserveCalls[partialBeadID])
	}
	stubMu.Unlock()
	fixture.assertMarkerCounts(t, partialPrompt, map[int]int{0: 0, 1: 0})
	stubMu.Lock()
	durablePartial := activeReservations[partialBeadID]
	if len(durablePartial) != 1 {
		stubMu.Unlock()
		t.Fatalf("partial-grant active reservations before drift=%v", durablePartial)
	}
	drifted := make(map[string]any, len(durablePartial[0]))
	for key, value := range durablePartial[0] {
		drifted[key] = value
	}
	drifted["id"] = 9202
	activeReservations[partialBeadID] = []map[string]any{drifted}
	dropNextReleaseResponse = true
	stubMu.Unlock()

	partialClear := fixture.runNTM(t, env,
		"--json", "assign", fixture.session, "--repo="+fixture.projectDir,
		"--clear="+partialBeadID, "--timeout=60s",
	)
	if partialClear.exitCode != 0 || len(bytes.TrimSpace(partialClear.stderr)) != 0 {
		t.Fatalf("partial-grant clear exit=%d stdout=%s stderr=%s", partialClear.exitCode, partialClear.stdout, partialClear.stderr)
	}
	fixture.assertLedgerHasNoAssignment(t, partialBeadID)
	stubMu.Lock()
	if !reflect.DeepEqual(releasedIDs, []int{9202}) || len(activeReservations[partialBeadID]) != 0 || dropNextReleaseResponse {
		t.Fatalf("release reconciliation IDs=%v active=%v response_drop_pending=%v", releasedIDs, activeReservations[partialBeadID], dropNextReleaseResponse)
	}
	stubMu.Unlock()

	unknownFailure := fixture.runNTM(t, env, reservationArgs(1, unknownBeadID)...)
	if unknownFailure.exitCode != 1 || len(bytes.TrimSpace(unknownFailure.stderr)) != 0 {
		t.Fatalf("unknown-response assignment exit=%d stdout=%s stderr=%s", unknownFailure.exitCode, unknownFailure.stdout, unknownFailure.stderr)
	}
	stubMu.Lock()
	unknownReserveCalls := reserveCalls[unknownBeadID]
	unknownActiveReservations := len(activeReservations[unknownBeadID])
	stubMu.Unlock()
	if unknownReserveCalls != 1 || unknownActiveReservations == 0 {
		t.Fatalf(
			"unknown-response fixture did not reach its dropped-response boundary: reserve_calls=%d active_reservations=%d",
			unknownReserveCalls,
			unknownActiveReservations,
		)
	}
	unknownPending := fixture.readLedgerAssignment(t, unknownBeadID)
	if unknownPending.ReservationState != "unknown" || unknownPending.ReservationAttempts != 1 || unknownPending.ReservationError == "" ||
		len(unknownPending.ReservationIDs) != 0 || unknownPending.DispatchAttempts != 0 {
		t.Fatalf("unknown-response durable state = %+v", unknownPending)
	}
	fixture.assertMarkerCounts(t, unknownPrompt, map[int]int{0: 0, 1: 0})
	fixture.primePaneForSafeDispatch(t, 1)
	time.Sleep(5500 * time.Millisecond)
	unknownRetry := fixture.runNTM(t, env,
		"assign", fixture.session, "--repo="+fixture.projectDir,
		"--retry="+unknownBeadID, "--timeout=60s", "--json",
	)
	if unknownRetry.exitCode != 0 || len(bytes.TrimSpace(unknownRetry.stderr)) != 0 {
		t.Fatalf("unknown-response recovery exit=%d stdout=%s stderr=%s", unknownRetry.exitCode, unknownRetry.stdout, unknownRetry.stderr)
	}
	fixture.waitForMarkerCount(t, 1, unknownPrompt, 1)
	unknownRecovered := fixture.readLedgerAssignment(t, unknownBeadID)
	if unknownRecovered.ReservationState != "reserved" || !unknownRecovered.ReservationCompleted ||
		!reflect.DeepEqual(unknownRecovered.ReservationIDs, []int{9301, 9302}) || unknownRecovered.DispatchState != "sent" || unknownRecovered.DispatchAttempts != 1 {
		t.Fatalf("unknown-response recovered state = %+v", unknownRecovered)
	}
	stubMu.Lock()
	if reserveCalls[unknownBeadID] != 1 {
		t.Fatalf("unknown-response recovery repeated reservation %d times", reserveCalls[unknownBeadID])
	}
	stubMu.Unlock()
}

func TestE2EAtomicAssignmentCustomTemplateProcessIdentity(t *testing.T) {
	CommonE2EPrerequisites(t)
	testutil.RequireTmuxThrottled(t)

	fixture := newAtomicAssignmentCLIFixture(t)
	const title = "Custom template process identity"
	beadID := fixture.createBead(t, title)
	templatePath := filepath.Join(fixture.projectDir, "atomic-direct-template.txt")
	marker := fmt.Sprintf("NTM_ATOMIC_TEMPLATE_%d", time.Now().UnixNano())
	originalTemplate := marker + "::{BEAD_ID}::{TITLE}\n"
	changedTemplate := marker + "::changed::{BEAD_ID}::{TITLE}\n"
	originalPrompt := strings.ReplaceAll(strings.ReplaceAll(originalTemplate, "{BEAD_ID}", beadID), "{TITLE}", title)
	changedPrompt := strings.ReplaceAll(strings.ReplaceAll(changedTemplate, "{BEAD_ID}", beadID), "{TITLE}", title)
	if err := os.WriteFile(templatePath, []byte(originalTemplate), 0o600); err != nil {
		t.Fatalf("write direct template: %v", err)
	}
	args := atomicDirectTemplateArgs(fixture, 0, beadID, templatePath)

	firstResult := fixture.runNTM(t, nil, args...)
	if firstResult.exitCode != 0 || len(bytes.TrimSpace(firstResult.stderr)) != 0 {
		t.Fatalf("custom-template assignment exit=%d stdout=%s stderr=%s", firstResult.exitCode, firstResult.stdout, firstResult.stderr)
	}
	var first atomicAssignmentDirectEnvelope
	decodeAtomicAssignmentJSON(t, firstResult.stdout, &first)
	assertDirectAssignmentEnvelope(t, first, fixture.session, beadID, originalPrompt, fixture.panes[0])
	fixture.waitForMarkerCount(t, 0, marker, 1)
	fixture.assertMarkerCounts(t, marker, map[int]int{0: 1, 1: 0})
	firstRecord := fixture.readLedgerAssignment(t, beadID)
	assertAtomicAssignmentRecord(t, firstRecord, beadID, originalPrompt, fixture.panes[0], "codex")

	replayResult := fixture.runNTM(t, nil, args...)
	if replayResult.exitCode != 0 || len(bytes.TrimSpace(replayResult.stderr)) != 0 {
		t.Fatalf("custom-template replay exit=%d stdout=%s stderr=%s", replayResult.exitCode, replayResult.stdout, replayResult.stderr)
	}
	var replay atomicAssignmentDirectEnvelope
	decodeAtomicAssignmentJSON(t, replayResult.stdout, &replay)
	assertDirectAssignmentEnvelope(t, replay, fixture.session, beadID, originalPrompt, fixture.panes[0])
	assertSameDirectReceipt(t, first, replay)
	fixture.assertMarkerCounts(t, marker, map[int]int{0: 1, 1: 0})

	if err := os.WriteFile(templatePath, []byte(changedTemplate), 0o600); err != nil {
		t.Fatalf("change direct template: %v", err)
	}
	changedResult := fixture.runNTM(t, nil, args...)
	if changedResult.exitCode == 0 || len(bytes.TrimSpace(changedResult.stderr)) != 0 {
		t.Fatalf("changed-template intent exit=%d stdout=%s stderr=%s", changedResult.exitCode, changedResult.stdout, changedResult.stderr)
	}
	var changed atomicAssignmentDirectEnvelope
	decodeAtomicAssignmentJSON(t, changedResult.stdout, &changed)
	if changed.Success || changed.Error == nil || changed.Error.Code != "CLAIM_CONFLICT" {
		t.Fatalf("changed-template intent envelope = %+v", changed)
	}
	fixture.assertMarkerCounts(t, changedPrompt, map[int]int{0: 0, 1: 0})
	fixture.assertMarkerCounts(t, marker, map[int]int{0: 1, 1: 0})
	afterConflict := fixture.readLedgerAssignment(t, beadID)
	assertAtomicAssignmentReceiptUnchanged(t, firstRecord, afterConflict)

	if err := os.WriteFile(templatePath, []byte(originalTemplate), 0o600); err != nil {
		t.Fatalf("restore direct template: %v", err)
	}
	restoredResult := fixture.runNTM(t, nil, args...)
	if restoredResult.exitCode != 0 || len(bytes.TrimSpace(restoredResult.stderr)) != 0 {
		t.Fatalf("restored-template replay exit=%d stdout=%s stderr=%s", restoredResult.exitCode, restoredResult.stdout, restoredResult.stderr)
	}
	var restored atomicAssignmentDirectEnvelope
	decodeAtomicAssignmentJSON(t, restoredResult.stdout, &restored)
	assertDirectAssignmentEnvelope(t, restored, fixture.session, beadID, originalPrompt, fixture.panes[0])
	assertSameDirectReceipt(t, first, restored)
	fixture.assertMarkerCounts(t, marker, map[int]int{0: 1, 1: 0})
	assertAtomicAssignmentReceiptUnchanged(t, firstRecord, fixture.readLedgerAssignment(t, beadID))
}

func TestE2EAtomicAssignmentConcurrentBuiltProcesses(t *testing.T) {
	CommonE2EPrerequisites(t)
	testutil.RequireTmuxThrottled(t)

	fixture := newAtomicAssignmentCLIFixture(t)
	const title = "Concurrent built process assignment"
	beadID := fixture.createBead(t, title)
	templatePath := filepath.Join(fixture.projectDir, "atomic-contended-template.txt")
	marker := fmt.Sprintf("NTM_ATOMIC_CONTENDED_%d", time.Now().UnixNano())
	templateBody := marker + "::{BEAD_ID}::{TITLE}"
	prompt := strings.ReplaceAll(strings.ReplaceAll(templateBody, "{BEAD_ID}", beadID), "{TITLE}", title)
	if err := os.WriteFile(templatePath, []byte(templateBody), 0o600); err != nil {
		t.Fatalf("write contended template: %v", err)
	}
	args := atomicDirectTemplateArgs(fixture, 0, beadID, templatePath)
	gateDir := filepath.Join(fixture.root, "contended-claim-bin")
	if err := os.MkdirAll(gateDir, 0o700); err != nil {
		t.Fatalf("create contended claim wrapper directory: %v", err)
	}
	enteredPath := filepath.Join(gateDir, "claim-finalization-entered")
	completedPath := filepath.Join(gateDir, "claim-finalization-completed")
	releasePath := filepath.Join(gateDir, "release-claim-finalization")
	claimWrapper := strings.Join([]string{
		"#!/bin/sh",
		"real_br=" + tmux.ShellQuote(fixture.brPath),
		"entered=" + tmux.ShellQuote(enteredPath),
		"completed=" + tmux.ShellQuote(completedPath),
		"release=" + tmux.ShellQuote(releasePath),
		"sync_command=0",
		"flush_only=0",
		`for arg in "$@"; do`,
		`  if [ "$arg" = "sync" ]; then sync_command=1; fi`,
		`  if [ "$arg" = "--flush-only" ]; then flush_only=1; fi`,
		"done",
		`guarded_sync=$((sync_command * flush_only))`,
		`if [ "$guarded_sync" -eq 1 ]; then`,
		`  printf 'guarded-sync\n' >> "$entered"`,
		`  while [ ! -e "$release" ]; do sleep 0.01; done`,
		"fi",
		`"$real_br" "$@"`,
		"status=$?",
		`if [ "$status" -eq 0 ] && [ "$guarded_sync" -eq 1 ]; then`,
		`  printf 'guarded-sync\n' >> "$completed"`,
		"fi",
		`exit "$status"`,
		"",
	}, "\n")
	if err := os.WriteFile(filepath.Join(gateDir, "br"), []byte(claimWrapper), 0o700); err != nil {
		t.Fatalf("write contended claim wrapper: %v", err)
	}
	path := gateDir + string(os.PathListSeparator) + atomicAssignmentEnvValue(fixture.env, "PATH")
	afterStart := func() {
		deadline := time.Now().Add(60 * time.Second)
		for {
			data, err := os.ReadFile(enteredPath)
			if err == nil && len(data) > 0 {
				break
			}
			if time.Now().After(deadline) {
				t.Fatalf("no contender reached guarded claim finalization: %v", err)
			}
			time.Sleep(10 * time.Millisecond)
		}
		time.Sleep(250 * time.Millisecond)
		data, err := os.ReadFile(enteredPath)
		if err != nil {
			t.Fatalf("read contended claim counter: %v", err)
		}
		if got := strings.Count(string(data), "guarded-sync\n"); got != 1 {
			t.Fatalf("%d contenders crossed guarded claim finalization while the first held the atomic lock; want 1", got)
		}
		if err := os.WriteFile(releasePath, []byte("release\n"), 0o600); err != nil {
			t.Fatalf("release contended claim gate: %v", err)
		}
	}
	results := fixture.runNTMConcurrentAfterStart(t, 2, map[string]string{"PATH": path}, afterStart, args...)
	envelopes := make([]atomicAssignmentDirectEnvelope, len(results))
	for i, result := range results {
		if result.exitCode != 0 || len(bytes.TrimSpace(result.stderr)) != 0 {
			t.Fatalf("contender %d exit=%d stdout=%s stderr=%s", i, result.exitCode, result.stdout, result.stderr)
		}
		decodeAtomicAssignmentJSON(t, result.stdout, &envelopes[i])
		assertDirectAssignmentEnvelope(t, envelopes[i], fixture.session, beadID, prompt, fixture.panes[0])
	}
	assertSameDirectReceipt(t, envelopes[0], envelopes[1])
	fixture.waitForMarkerCount(t, 0, marker, 1)
	fixture.assertMarkerCounts(t, marker, map[int]int{0: 1, 1: 0})
	record := fixture.readLedgerAssignment(t, beadID)
	assertAtomicAssignmentRecord(t, record, beadID, prompt, fixture.panes[0], "codex")
	if record.ClaimAttempts != 1 {
		t.Fatalf("contenders performed %d durable claim attempts, want 1: %+v", record.ClaimAttempts, record)
	}
	claimCalls, err := os.ReadFile(enteredPath)
	if err != nil {
		t.Fatalf("read final contended claim counter: %v", err)
	}
	if got := strings.Count(string(claimCalls), "guarded-sync\n"); got != 1 {
		t.Fatalf("contenders performed %d guarded claim finalizations, want exactly 1", got)
	}
	completedCalls, err := os.ReadFile(completedPath)
	if err != nil {
		t.Fatalf("read completed guarded claim counter: %v", err)
	}
	if got := strings.Count(string(completedCalls), "guarded-sync\n"); got != 1 {
		t.Fatalf("contenders completed %d guarded claim finalizations, want exactly 1", got)
	}

	changedTemplate := templateBody + "::different"
	if err := os.WriteFile(templatePath, []byte(changedTemplate), 0o600); err != nil {
		t.Fatalf("change contended template: %v", err)
	}
	conflictResult := fixture.runNTM(t, nil, args...)
	if conflictResult.exitCode == 0 || len(bytes.TrimSpace(conflictResult.stderr)) != 0 {
		t.Fatalf("post-contention conflict exit=%d stdout=%s stderr=%s", conflictResult.exitCode, conflictResult.stdout, conflictResult.stderr)
	}
	var conflict atomicAssignmentDirectEnvelope
	decodeAtomicAssignmentJSON(t, conflictResult.stdout, &conflict)
	if conflict.Success || conflict.Error == nil || conflict.Error.Code != "CLAIM_CONFLICT" {
		t.Fatalf("post-contention conflict envelope = %+v", conflict)
	}
	fixture.assertMarkerCounts(t, marker, map[int]int{0: 1, 1: 0})
	assertAtomicAssignmentReceiptUnchanged(t, record, fixture.readLedgerAssignment(t, beadID))
}

func TestE2EAtomicDistributeConcurrentBuiltProcessesDeliverExactlyOnce(t *testing.T) {
	CommonE2EPrerequisites(t)
	testutil.RequireTmuxThrottled(t)

	fixture := newAtomicAssignmentCLIFixture(t)
	fixture.primePaneForSafeDispatch(t, 0)
	time.Sleep(5500 * time.Millisecond)

	marker := fmt.Sprintf("NTM_ATOMIC_DISTRIBUTE_CONTENDED_%d", time.Now().UnixNano())
	title := marker + " built process assignment"
	beadID := fixture.createBead(t, title)
	triagePath := filepath.Join(fixture.root, "contended-distribute-triage.json")
	planPath := filepath.Join(fixture.root, "contended-distribute-plan.json")
	if err := os.WriteFile(triagePath, []byte(`{"triage":{"recommendations":[]}}`), 0o600); err != nil {
		t.Fatalf("write contended distribute triage payload: %v", err)
	}
	planPayload, err := json.Marshal(map[string]any{
		"plan": map[string]any{
			"tracks": []map[string]any{{
				"track_id": "contended-distribute-track",
				"items": []map[string]any{{
					"id": beadID, "title": title, "status": "open", "priority": 1,
				}},
			}},
		},
	})
	if err != nil {
		t.Fatalf("marshal contended distribute plan: %v", err)
	}
	if err := os.WriteFile(planPath, planPayload, 0o600); err != nil {
		t.Fatalf("write contended distribute plan payload: %v", err)
	}

	binDir := filepath.Join(fixture.root, "contended-distribute-bin")
	if err := os.MkdirAll(binDir, 0o700); err != nil {
		t.Fatalf("create contended distribute bin: %v", err)
	}
	bvScript := strings.Join([]string{
		"#!/bin/sh",
		`case "$1" in`,
		`  --robot-triage) cat "$NTM_E2E_TRIAGE_PAYLOAD" ;;`,
		`  --robot-plan) cat "$NTM_E2E_PLAN_PAYLOAD" ;;`,
		`  *) printf 'unexpected bv args: %s\n' "$*" >&2; exit 64 ;;`,
		"esac",
		"",
	}, "\n")
	if err := os.WriteFile(filepath.Join(binDir, "bv"), []byte(bvScript), 0o700); err != nil {
		t.Fatalf("write contended distribute bv wrapper: %v", err)
	}
	showBarrier := filepath.Join(fixture.root, "contended-distribute-live-show.trace")
	brScript := strings.Join([]string{
		"#!/bin/sh",
		"real_br=" + tmux.ShellQuote(fixture.brPath),
		"barrier=" + tmux.ShellQuote(showBarrier),
		"bead=" + tmux.ShellQuote(beadID),
		"show_command=0",
		"previous=",
		`for arg in "$@"; do`,
		`  if [ "$previous" = "show" ] && [ "$arg" = "$bead" ]; then show_command=1; fi`,
		`  previous="$arg"`,
		"done",
		`if [ "$show_command" -eq 1 ]; then`,
		`  printf '%s\n' "$$" >> "$barrier"`,
		`  deadline=0`,
		`  while [ "$(wc -l < "$barrier")" -lt 2 ]; do`,
		`    sleep 0.01`,
		`    deadline=$((deadline + 1))`,
		`    if [ "$deadline" -gt 6000 ]; then exit 98; fi`,
		`  done`,
		"fi",
		`exec "$real_br" "$@"`,
		"",
	}, "\n")
	if err := os.WriteFile(filepath.Join(binDir, "br"), []byte(brScript), 0o700); err != nil {
		t.Fatalf("write contended distribute br wrapper: %v", err)
	}
	env := map[string]string{
		"PATH":                   binDir + string(os.PathListSeparator) + atomicAssignmentEnvValue(fixture.env, "PATH"),
		"NTM_E2E_TRIAGE_PAYLOAD": triagePath,
		"NTM_E2E_PLAN_PAYLOAD":   planPath,
	}
	args := []string{"--json", "send", fixture.session, "--distribute", "--dist-limit=1", "--dist-auto"}
	results := fixture.runNTMConcurrent(t, 2, env, args...)
	type distributeEnvelope struct {
		Success   bool `json:"success"`
		Delivered int  `json:"delivered"`
		Failed    int  `json:"failed"`
		Receipts  []struct {
			BeadID         string `json:"bead_id"`
			PaneID         string `json:"pane_id"`
			Status         string `json:"status"`
			Protocol       string `json:"protocol"`
			DeliveryID     string `json:"delivery_id"`
			IdempotencyKey string `json:"idempotency_key"`
			ClaimActor     string `json:"claim_actor"`
			Replayed       bool   `json:"replayed"`
		} `json:"receipts"`
	}
	envelopes := make([]distributeEnvelope, len(results))
	for index, result := range results {
		if result.exitCode != 0 || len(bytes.TrimSpace(result.stderr)) != 0 {
			t.Fatalf("distribute contender %d exit=%d stdout=%s stderr=%s", index, result.exitCode, result.stdout, result.stderr)
		}
		decodeAtomicAssignmentJSON(t, result.stdout, &envelopes[index])
		if !envelopes[index].Success || envelopes[index].Delivered != 1 || envelopes[index].Failed != 0 || len(envelopes[index].Receipts) != 1 {
			t.Fatalf("distribute contender %d envelope = %+v", index, envelopes[index])
		}
		receipt := envelopes[index].Receipts[0]
		if receipt.BeadID != beadID || receipt.PaneID != fixture.panes[0].ID || receipt.Status != "delivered" ||
			receipt.Protocol != "double_enter" || receipt.DeliveryID == "" || receipt.IdempotencyKey == "" || receipt.ClaimActor == "" {
			t.Fatalf("distribute contender %d receipt = %+v", index, receipt)
		}
	}
	first := envelopes[0].Receipts[0]
	second := envelopes[1].Receipts[0]
	if first.DeliveryID != second.DeliveryID || first.IdempotencyKey != second.IdempotencyKey || first.ClaimActor != second.ClaimActor {
		t.Fatalf("contended distribute receipts diverged: first=%+v second=%+v", first, second)
	}
	if first.Replayed == second.Replayed {
		t.Fatalf("contended distribute replay flags = %v and %v, want exactly one replay", first.Replayed, second.Replayed)
	}
	fixture.waitForMarkerCount(t, 0, marker, 1)
	fixture.assertMarkerCounts(t, marker, map[int]int{0: 1, 1: 0})
	record := fixture.readLedgerAssignment(t, beadID)
	if record.Status != "assigned" || record.ClaimState != "claimed" || record.ClaimStatus != "in_progress" ||
		record.ClaimAttempts != 1 || record.DispatchState != "sent" || record.DispatchAttempts != 1 ||
		record.DispatchReceiptID != first.DeliveryID || record.IdempotencyKey != first.IdempotencyKey || record.ClaimActor != first.ClaimActor {
		t.Fatalf("contended distribute durable record = %+v", record)
	}
	fixture.assertBead(t, beadID, "in_progress", record.ClaimActor)
	showCalls, err := os.ReadFile(showBarrier)
	if err != nil {
		t.Fatalf("read contended distribute live-show barrier: %v", err)
	}
	if len(strings.Fields(string(showCalls))) < 2 {
		t.Fatalf("only one contender crossed live-open revalidation: %s", showCalls)
	}
}

func TestE2EAtomicDistributeCanonicalMultiWindowDelivery(t *testing.T) {
	CommonE2EPrerequisites(t)
	testutil.RequireTmuxThrottled(t)

	fixture := newAtomicAssignmentCLIFixture(t)
	topology := fixture.addSecondAgentWindow(t)
	targets := []atomicAssignmentPane{topology["0.1"], topology["1.1"]}
	for _, target := range targets {
		fixture.primeEndpointForSafeDispatch(t, target)
	}
	time.Sleep(5500 * time.Millisecond)

	beadIDs := []string{
		fixture.createBead(t, "Atomic distribute window zero"),
		fixture.createBead(t, "Atomic distribute window one"),
	}
	triagePath := filepath.Join(fixture.root, "multiwindow-distribute-triage.json")
	planPath := filepath.Join(fixture.root, "multiwindow-distribute-plan.json")
	if err := os.WriteFile(triagePath, []byte(`{"triage":{"recommendations":[]}}`), 0o600); err != nil {
		t.Fatalf("write multiwindow distribute triage payload: %v", err)
	}
	planPayload, err := json.Marshal(map[string]any{
		"plan": map[string]any{
			"tracks": []map[string]any{{
				"track_id": "multiwindow-distribute-track",
				"items": []map[string]any{
					{"id": beadIDs[0], "title": "Atomic distribute window zero", "status": "open", "priority": 1},
					{"id": beadIDs[1], "title": "Atomic distribute window one", "status": "open", "priority": 1},
				},
			}},
		},
	})
	if err != nil {
		t.Fatalf("marshal multiwindow distribute plan: %v", err)
	}
	if err := os.WriteFile(planPath, planPayload, 0o600); err != nil {
		t.Fatalf("write multiwindow distribute plan: %v", err)
	}
	binDir := filepath.Join(fixture.root, "multiwindow-distribute-bin")
	if err := os.MkdirAll(binDir, 0o700); err != nil {
		t.Fatalf("create multiwindow distribute bin: %v", err)
	}
	bvScript := strings.Join([]string{
		"#!/bin/sh",
		`case "$1" in`,
		`  --robot-triage) cat "$NTM_E2E_TRIAGE_PAYLOAD" ;;`,
		`  --robot-plan) cat "$NTM_E2E_PLAN_PAYLOAD" ;;`,
		`  *) printf 'unexpected bv args: %s\n' "$*" >&2; exit 64 ;;`,
		"esac",
		"",
	}, "\n")
	if err := os.WriteFile(filepath.Join(binDir, "bv"), []byte(bvScript), 0o700); err != nil {
		t.Fatalf("write multiwindow distribute bv wrapper: %v", err)
	}
	env := map[string]string{
		"PATH":                   binDir + string(os.PathListSeparator) + atomicAssignmentEnvValue(fixture.env, "PATH"),
		"NTM_E2E_TRIAGE_PAYLOAD": triagePath,
		"NTM_E2E_PLAN_PAYLOAD":   planPath,
	}
	result := fixture.runNTM(t, env, "--json", "send", fixture.session, "--distribute", "--dist-limit=2", "--dist-auto")
	if result.exitCode != 0 || len(bytes.TrimSpace(result.stderr)) != 0 {
		t.Fatalf("multiwindow distribute exit=%d stdout=%s stderr=%s", result.exitCode, result.stdout, result.stderr)
	}
	var envelope struct {
		Success   bool `json:"success"`
		Delivered int  `json:"delivered"`
		Failed    int  `json:"failed"`
		Receipts  []struct {
			BeadID         string `json:"bead_id"`
			PaneID         string `json:"pane_id"`
			PaneTarget     string `json:"pane_target"`
			Status         string `json:"status"`
			Protocol       string `json:"protocol"`
			DeliveryID     string `json:"delivery_id"`
			IdempotencyKey string `json:"idempotency_key"`
			ClaimActor     string `json:"claim_actor"`
		} `json:"receipts"`
	}
	decodeAtomicAssignmentJSON(t, result.stdout, &envelope)
	if !envelope.Success || envelope.Delivered != 2 || envelope.Failed != 0 || len(envelope.Receipts) != 2 {
		t.Fatalf("multiwindow distribute envelope = %+v", envelope)
	}
	byBead := make(map[string]struct {
		paneID     string
		paneTarget string
		deliveryID string
		key        string
		actor      string
	}, len(envelope.Receipts))
	for _, receipt := range envelope.Receipts {
		if receipt.Status != "delivered" || receipt.Protocol != "double_enter" || receipt.DeliveryID == "" ||
			receipt.IdempotencyKey == "" || receipt.ClaimActor == "" {
			t.Fatalf("multiwindow distribute receipt = %+v", receipt)
		}
		if _, duplicate := byBead[receipt.BeadID]; duplicate {
			t.Fatalf("duplicate multiwindow receipt for %s", receipt.BeadID)
		}
		byBead[receipt.BeadID] = struct {
			paneID     string
			paneTarget string
			deliveryID string
			key        string
			actor      string
		}{receipt.PaneID, receipt.PaneTarget, receipt.DeliveryID, receipt.IdempotencyKey, receipt.ClaimActor}
	}
	for index, beadID := range beadIDs {
		receipt, ok := byBead[beadID]
		if !ok {
			t.Fatalf("multiwindow distribute omitted bead %s: %+v", beadID, byBead)
		}
		target := targets[index]
		if receipt.paneID != target.ID || receipt.paneTarget != target.Target {
			t.Fatalf("bead %s receipt target = %s/%s, want %s/%s", beadID, receipt.paneTarget, receipt.paneID, target.Target, target.ID)
		}
		fixture.waitForEndpointMarkerCount(t, target, beadID, 1)
		record := fixture.readLedgerAssignment(t, beadID)
		if record.OccupancyKey != target.ID || record.DispatchTarget != target.ID || record.DispatchReceiptID != receipt.deliveryID ||
			record.IdempotencyKey != receipt.key || record.ClaimActor != receipt.actor || record.DispatchAttempts != 1 || record.ClaimAttempts != 1 {
			t.Fatalf("multiwindow durable record for %s = %+v", beadID, record)
		}
		fixture.assertBead(t, beadID, "in_progress", record.ClaimActor)
	}
	if targets[0].ID == targets[1].ID || targets[0].Index != targets[1].Index {
		t.Fatalf("multiwindow fixture did not exercise duplicate local indexes: %+v", targets)
	}
}

func TestE2EAtomicAssignmentEligibilityMutationAtClaimBoundaryBuiltProcess(t *testing.T) {
	CommonE2EPrerequisites(t)
	testutil.RequireTmuxThrottled(t)

	fixture := newAtomicAssignmentCLIFixture(t)
	mail := newAtomicAssignmentMailStub(fixture.projectDir)
	defer mail.close()

	tests := []struct {
		name        string
		title       string
		mutation    func(string) []string
		wantMessage string
	}{
		{
			name:  "dependency added after planning",
			title: "Dependency race internal/assignment/eligibility_dependency.go",
			mutation: func(beadID string) []string {
				blockerID := fixture.createBead(t, "Late assignment dependency")
				return []string{"dep", "add", beadID, blockerID, "--type", "blocks", "--json"}
			},
			wantMessage: "unresolved blockers",
		},
		{
			name:  "operator label added after planning",
			title: "Operator race internal/assignment/eligibility_operator.go",
			mutation: func(beadID string) []string {
				return []string{"update", beadID, "--add-label=operator-gated", "--json"}
			},
			wantMessage: "operator-gated labels",
		},
		{
			name:  "defer date added after planning",
			title: "Deferred race internal/assignment/eligibility_deferred.go",
			mutation: func(beadID string) []string {
				return []string{"update", beadID, "--defer=2099-01-01T00:00:00Z", "--json"}
			},
			wantMessage: "work is deferred",
		},
	}

	for index, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			beadID := fixture.createBead(t, test.title)
			marker := fmt.Sprintf("NTM_ATOMIC_ELIGIBILITY_%d_%d", index, time.Now().UnixNano())
			path, fired, failed := fixture.armAssignmentEligibilityMutation(t, test.name, test.mutation(beadID))
			env := mail.env()
			env["PATH"] = path
			before := mail.snapshot()

			result := fixture.runNTM(t, env, atomicDirectArgsForPane(fixture, index%2, beadID, marker, true)...)
			if result.exitCode == 0 || len(bytes.TrimSpace(result.stderr)) != 0 {
				t.Fatalf("eligibility race exit=%d stdout=%s stderr=%s", result.exitCode, result.stdout, result.stderr)
			}
			var envelope atomicAssignmentDirectEnvelope
			decodeAtomicAssignmentJSON(t, result.stdout, &envelope)
			if envelope.Success || envelope.Error == nil || envelope.Error.Code != "BEAD_INELIGIBLE" ||
				!strings.Contains(envelope.Error.Message, test.wantMessage) {
				t.Fatalf("eligibility race envelope=%+v", envelope)
			}
			if _, err := os.Stat(fired); err != nil {
				t.Fatalf("claim-boundary mutation did not fire: %v", err)
			}
			if _, err := os.Stat(failed); !errors.Is(err, os.ErrNotExist) {
				t.Fatalf("claim-boundary mutation failed, marker error=%v", err)
			}
			fixture.assertBead(t, beadID, "open", "")
			fixture.assertMarkerCounts(t, marker, map[int]int{0: 0, 1: 0})

			record := fixture.readLedgerAssignment(t, beadID)
			if record.Status != "failed" || record.ClaimState != "ineligible" || record.ClaimAttempts != 1 ||
				record.ReservationAttempts != 0 || record.DispatchAttempts != 0 || len(record.ReservationIDs) != 0 ||
				record.DispatchReceiptID != "" {
				t.Fatalf("eligibility race durable row=%+v", record)
			}
			after := mail.snapshot()
			if after.ReserveCalls != before.ReserveCalls || after.SendCalls != before.SendCalls {
				t.Fatalf("eligibility race crossed Agent Mail side-effect boundary: before=%+v after=%+v", before, after)
			}
		})
	}
}

func TestE2EAtomicAssignmentTerminalGenerationBuiltProcess(t *testing.T) {
	CommonE2EPrerequisites(t)
	testutil.RequireTmuxThrottled(t)

	fixture := newAtomicAssignmentCLIFixture(t)
	beadID := fixture.createBead(t, "Terminal assignment generation")
	prompt := fmt.Sprintf("NTM_ATOMIC_TERMINAL_%d", time.Now().UnixNano())
	args := atomicDirectArgs(fixture, beadID, prompt, false)

	firstResult := fixture.runNTM(t, nil, args...)
	if firstResult.exitCode != 0 || len(bytes.TrimSpace(firstResult.stderr)) != 0 {
		t.Fatalf("initial generation exit=%d stdout=%s stderr=%s", firstResult.exitCode, firstResult.stdout, firstResult.stderr)
	}
	var first atomicAssignmentDirectEnvelope
	decodeAtomicAssignmentJSON(t, firstResult.stdout, &first)
	assertDirectAssignmentEnvelope(t, first, fixture.session, beadID, prompt, fixture.panes[0])
	fixture.waitForMarkerCount(t, 0, prompt, 1)
	firstRecord := fixture.readLedgerAssignment(t, beadID)
	assertAtomicAssignmentRecord(t, firstRecord, beadID, prompt, fixture.panes[0], "codex")

	fixture.mustBR(t, "close", beadID, "--reason=terminal-generation-e2e", "--json")
	fixture.assertBead(t, beadID, "closed", firstRecord.ClaimActor)
	fixture.setLedgerAssignmentStatus(t, beadID, "completed")

	closedResult := fixture.runNTM(t, nil, args...)
	if closedResult.exitCode == 0 || len(bytes.TrimSpace(closedResult.stderr)) != 0 {
		t.Fatalf("closed generation retry exit=%d stdout=%s stderr=%s", closedResult.exitCode, closedResult.stdout, closedResult.stderr)
	}
	var closed atomicAssignmentDirectEnvelope
	decodeAtomicAssignmentJSON(t, closedResult.stdout, &closed)
	if closed.Success || closed.Error == nil || closed.Error.Code != "BEAD_NOT_REOPENED" || !strings.Contains(closed.Error.Message, "tracker status is \"closed\"") {
		t.Fatalf("closed generation retry envelope = %+v", closed)
	}
	fixture.assertMarkerCounts(t, prompt, map[int]int{0: 1, 1: 0})
	assertAtomicAssignmentReceiptUnchanged(t, firstRecord, fixture.readLedgerAssignment(t, beadID))
	fixture.assertBead(t, beadID, "closed", firstRecord.ClaimActor)

	fixture.mustBR(t, "reopen", beadID, "--reason=terminal-generation-e2e", "--json")
	fixture.assertBead(t, beadID, "open", firstRecord.ClaimActor)

	// The first delivery left prompt text on the pane; re-render the
	// composer glyph so the bd-dp9oy delivery gate accepts a second typed
	// send (a real agent shows its composer again once it finishes).
	fixture.primePaneForSafeDispatch(t, 0)
	secondResult := fixture.runNTM(t, nil, args...)
	if secondResult.exitCode != 0 || len(bytes.TrimSpace(secondResult.stderr)) != 0 {
		t.Fatalf("second generation exit=%d stdout=%s stderr=%s", secondResult.exitCode, secondResult.stdout, secondResult.stderr)
	}
	var second atomicAssignmentDirectEnvelope
	decodeAtomicAssignmentJSON(t, secondResult.stdout, &second)
	assertDirectAssignmentEnvelope(t, second, fixture.session, beadID, prompt, fixture.panes[0])
	fixture.waitForMarkerCount(t, 0, prompt, 2)
	fixture.assertMarkerCounts(t, prompt, map[int]int{0: 2, 1: 0})
	secondRecord := fixture.readLedgerAssignment(t, beadID)
	assertAtomicAssignmentRecordWithClaimIdentity(t, secondRecord, beadID, prompt, fixture.panes[0], "codex", false)
	if secondRecord.IdempotencyKey == firstRecord.IdempotencyKey {
		t.Fatalf("terminal generation reused idempotency key %q", secondRecord.IdempotencyKey)
	}
	if secondRecord.ClaimActor != firstRecord.ClaimActor {
		t.Fatalf("terminal generation changed retained br actor: first=%q second=%q", firstRecord.ClaimActor, secondRecord.ClaimActor)
	}
	if secondRecord.DispatchReceiptID == firstRecord.DispatchReceiptID ||
		(secondRecord.DispatchedAt != nil && firstRecord.DispatchedAt != nil && secondRecord.DispatchedAt.Equal(*firstRecord.DispatchedAt)) {
		t.Fatalf("terminal generation reused delivery receipt: first=%+v second=%+v", firstRecord, secondRecord)
	}
	fixture.assertBead(t, beadID, "in_progress", firstRecord.ClaimActor)
}

func TestE2EAtomicAssignmentFreshTerminalBeadsAreGuarded(t *testing.T) {
	CommonE2EPrerequisites(t)
	testutil.RequireTmuxThrottled(t)

	fixture := newAtomicAssignmentCLIFixture(t)
	closedBeadID := fixture.createBead(t, "Fresh closed guarded assignment")
	tombstonedBeadID := fixture.createBead(t, "Fresh tombstoned guarded assignment")
	fixture.mustBR(t, "close", closedBeadID, "--reason=fresh-terminal-e2e", "--json")
	fixture.mustBR(t, "delete", tombstonedBeadID, "--reason=fresh-terminal-e2e", "--json")

	for index, test := range []struct {
		name   string
		beadID string
		status string
	}{
		{name: "closed", beadID: closedBeadID, status: "closed"},
		{name: "tombstoned", beadID: tombstonedBeadID, status: "tombstone"},
	} {
		t.Run(test.name, func(t *testing.T) {
			prompt := fmt.Sprintf("NTM_ATOMIC_FRESH_TERMINAL_%s_%d", strings.ToUpper(test.name), time.Now().UnixNano())
			result := fixture.runNTM(t, nil, atomicDirectArgsForPane(fixture, index, test.beadID, prompt, false)...)
			if result.exitCode != 1 || len(bytes.TrimSpace(result.stderr)) != 0 {
				t.Fatalf("fresh %s assignment exit=%d stdout=%s stderr=%s", test.name, result.exitCode, result.stdout, result.stderr)
			}
			var envelope atomicAssignmentDirectEnvelope
			decodeAtomicAssignmentJSON(t, result.stdout, &envelope)
			if envelope.Success || envelope.Error == nil || envelope.Error.Code != "BEAD_INELIGIBLE" ||
				!strings.Contains(strings.ToLower(envelope.Error.Message), test.status) {
				t.Fatalf("fresh %s guarded envelope = %+v", test.name, envelope)
			}
			fixture.assertBead(t, test.beadID, test.status, "")
			fixture.assertMarkerCounts(t, prompt, map[int]int{0: 0, 1: 0})
			fixture.assertLedgerReplicasHaveNoAssignment(t, test.beadID)
		})
	}

	bulkClosedBeadID := fixture.createBead(t, "Fresh closed guarded bulk assignment")
	bulkTombstonedBeadID := fixture.createBead(t, "Fresh tombstoned guarded bulk assignment")
	fixture.mustBR(t, "close", bulkClosedBeadID, "--reason=fresh-terminal-bulk-e2e", "--json")
	fixture.mustBR(t, "delete", bulkTombstonedBeadID, "--reason=fresh-terminal-bulk-e2e", "--json")
	bulkTemplate := filepath.Join(fixture.projectDir, "atomic-fresh-terminal-bulk-template.txt")
	bulkTemplateBody := fmt.Sprintf("NTM_ATOMIC_FRESH_TERMINAL_BULK_%d_{bead_id}", time.Now().UnixNano())
	if err := os.WriteFile(bulkTemplate, []byte(bulkTemplateBody), 0o600); err != nil {
		t.Fatalf("write fresh terminal bulk template: %v", err)
	}
	fixture.primePaneForSafeDispatch(t, 1)
	time.Sleep(5500 * time.Millisecond)
	for _, test := range []struct {
		name   string
		beadID string
		status string
	}{
		{name: "closed_bulk", beadID: bulkClosedBeadID, status: "closed"},
		{name: "tombstoned_bulk", beadID: bulkTombstonedBeadID, status: "tombstone"},
	} {
		t.Run(test.name, func(t *testing.T) {
			prompt := strings.ReplaceAll(bulkTemplateBody, "{bead_id}", test.beadID)
			result := fixture.runNTM(t, nil, atomicBulkArgs(fixture, test.beadID, bulkTemplate)...)
			if result.exitCode != 1 || len(bytes.TrimSpace(result.stderr)) != 0 {
				t.Fatalf("fresh %s assignment exit=%d stdout=%s stderr=%s", test.name, result.exitCode, result.stdout, result.stderr)
			}
			var envelope atomicAssignmentBulkEnvelope
			decodeAtomicAssignmentJSON(t, result.stdout, &envelope)
			if envelope.Success || envelope.Summary.Assigned != 0 || envelope.Summary.Failed != 1 || len(envelope.Assignments) != 1 ||
				envelope.Assignments[0].Bead != test.beadID || envelope.Assignments[0].Claimed || envelope.Assignments[0].PromptSent ||
				!strings.Contains(strings.ToLower(envelope.Assignments[0].Error), test.status) {
				t.Fatalf("fresh %s guarded bulk envelope = %+v", test.name, envelope)
			}
			fixture.assertBead(t, test.beadID, test.status, "")
			fixture.assertMarkerCounts(t, prompt, map[int]int{0: 0, 1: 0})
			fixture.assertLedgerReplicasHaveNoAssignment(t, test.beadID)
		})
	}
}

func TestE2EAtomicAssignmentCloseForceClearReopenGeneration(t *testing.T) {
	CommonE2EPrerequisites(t)
	testutil.RequireTmuxThrottled(t)

	fixture := newAtomicAssignmentCLIFixture(t)
	beadID := fixture.createBead(t, "Close clear reopen assignment generation")
	prompt := fmt.Sprintf("NTM_ATOMIC_CLOSE_CLEAR_REOPEN_%d", time.Now().UnixNano())
	args := atomicDirectArgs(fixture, beadID, prompt, false)

	firstResult := fixture.runNTM(t, nil, args...)
	if firstResult.exitCode != 0 || len(bytes.TrimSpace(firstResult.stderr)) != 0 {
		t.Fatalf("initial lifecycle assignment exit=%d stdout=%s stderr=%s", firstResult.exitCode, firstResult.stdout, firstResult.stderr)
	}
	fixture.waitForMarkerCount(t, 0, prompt, 1)
	first := fixture.readLedgerAssignment(t, beadID)
	fixture.mustBR(t, "close", beadID, "--reason=close-clear-reopen-e2e", "--json")
	fixture.assertBead(t, beadID, "closed", first.ClaimActor)
	fixture.setLedgerAssignmentStatus(t, beadID, "completed")

	clearResult := fixture.runNTM(t, nil,
		"--json", "assign", fixture.session,
		"--repo="+fixture.projectDir,
		"--clear="+beadID,
		"--force",
		"--timeout=15s",
	)
	if clearResult.exitCode != 0 || len(bytes.TrimSpace(clearResult.stderr)) != 0 {
		t.Fatalf("force clear terminal assignment exit=%d stdout=%s stderr=%s", clearResult.exitCode, clearResult.stdout, clearResult.stderr)
	}
	var clearEnvelope struct {
		Success bool `json:"success"`
		Data    struct {
			Cleared []struct {
				BeadID  string `json:"bead_id"`
				Success bool   `json:"success"`
			} `json:"cleared"`
			Summary struct {
				ClearedCount int `json:"cleared_count"`
				FailedCount  int `json:"failed_count"`
			} `json:"summary"`
		} `json:"data"`
	}
	decodeAtomicAssignmentJSON(t, clearResult.stdout, &clearEnvelope)
	if !clearEnvelope.Success || clearEnvelope.Data.Summary.ClearedCount != 1 || clearEnvelope.Data.Summary.FailedCount != 0 ||
		len(clearEnvelope.Data.Cleared) != 1 || clearEnvelope.Data.Cleared[0].BeadID != beadID || !clearEnvelope.Data.Cleared[0].Success {
		t.Fatalf("force clear terminal envelope = %+v", clearEnvelope)
	}
	fixture.assertLedgerHasNoAssignment(t, beadID)
	fixture.assertBead(t, beadID, "closed", "")

	fixture.mustBR(t, "reopen", beadID, "--reason=close-clear-reopen-e2e", "--json")
	fixture.assertBead(t, beadID, "open", "")
	// Earlier delivery left prompt text on the pane; re-render the composer
	// glyph so the bd-dp9oy delivery gate accepts the reopened send.
	fixture.primePaneForSafeDispatch(t, 0)
	secondResult := fixture.runNTM(t, nil, args...)
	if secondResult.exitCode != 0 || len(bytes.TrimSpace(secondResult.stderr)) != 0 {
		t.Fatalf("reopened lifecycle assignment exit=%d stdout=%s stderr=%s", secondResult.exitCode, secondResult.stdout, secondResult.stderr)
	}
	fixture.waitForMarkerCount(t, 0, prompt, 2)
	second := fixture.readLedgerAssignment(t, beadID)
	if second.IdempotencyKey == first.IdempotencyKey || second.ClaimActor == first.ClaimActor ||
		second.DispatchReceiptID == first.DispatchReceiptID || second.DispatchAttempts != 1 {
		t.Fatalf("reopened generation reused terminal identity: first=%+v second=%+v", first, second)
	}
	fixture.assertBead(t, beadID, "in_progress", second.ClaimActor)
}

func TestE2EAtomicAssignmentClearPaneLeaseFailureIsDurable(t *testing.T) {
	CommonE2EPrerequisites(t)
	testutil.RequireTmuxThrottled(t)

	fixture := newAtomicAssignmentCLIFixture(t)
	beadID := fixture.createBead(t, "Clear pane lease barrier")
	prompt := fmt.Sprintf("NTM_ATOMIC_CLEAR_%d", time.Now().UnixNano())
	assigned := fixture.runNTM(t, nil, atomicDirectArgs(fixture, beadID, prompt, false)...)
	if assigned.exitCode != 0 || len(bytes.TrimSpace(assigned.stderr)) != 0 {
		t.Fatalf("seed assignment exit=%d stdout=%s stderr=%s", assigned.exitCode, assigned.stdout, assigned.stderr)
	}
	fixture.waitForMarkerCount(t, 0, prompt, 1)
	before := fixture.readLedgerAssignment(t, beadID)
	fixture.setLedgerAssignmentReservations(t, beadID, []string{"internal/cli/**"}, []int{987654})

	cleared := fixture.runNTM(t, nil,
		"--json", "assign", fixture.session,
		"--clear-pane=0", "--timeout=2s",
	)
	if cleared.exitCode != 1 || len(bytes.TrimSpace(cleared.stderr)) != 0 {
		t.Fatalf("clear-pane exit=%d stdout=%s stderr=%s", cleared.exitCode, cleared.stdout, cleared.stderr)
	}
	var envelope struct {
		Success bool `json:"success"`
		Error   *struct {
			Code    string `json:"code"`
			Message string `json:"message"`
		} `json:"error"`
		Data struct {
			Cleared []struct {
				BeadID  string `json:"bead_id"`
				Success bool   `json:"success"`
				Error   string `json:"error"`
			} `json:"cleared"`
			Summary struct {
				ClearedCount int `json:"cleared_count"`
				FailedCount  int `json:"failed_count"`
			} `json:"summary"`
		} `json:"data"`
	}
	decodeAtomicAssignmentJSON(t, cleared.stdout, &envelope)
	if envelope.Success || envelope.Error == nil || envelope.Error.Code != "CLEAR_FAILED" ||
		envelope.Data.Summary.ClearedCount != 0 || envelope.Data.Summary.FailedCount != 1 || len(envelope.Data.Cleared) != 1 ||
		envelope.Data.Cleared[0].BeadID != beadID || envelope.Data.Cleared[0].Success ||
		!strings.Contains(envelope.Data.Cleared[0].Error, "Agent Mail is unavailable") {
		t.Fatalf("clear-pane failure envelope = %+v", envelope)
	}
	after := fixture.readLedgerAssignment(t, beadID)
	if after.ClearState != "reservation_releasing" || !strings.Contains(after.ClearError, "Agent Mail is unavailable") ||
		!reflect.DeepEqual(after.ReservationIDs, []int{987654}) || !reflect.DeepEqual(after.ReservedPaths, []string{"internal/cli/**"}) {
		t.Fatalf("clear-pane failure did not retain release barrier: %+v", after)
	}
	assertAtomicAssignmentReceiptUnchanged(t, before, after)
	fixture.assertMarkerCounts(t, prompt, map[int]int{0: 1, 1: 0})

	var releaseCalls atomic.Int32
	var activeLease atomic.Bool
	activeLease.Store(true)
	mailServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodGet && r.URL.Path == "/health/liveness" {
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"status":"ok"}`))
			return
		}
		var request struct {
			JSONRPC string `json:"jsonrpc"`
			ID      any    `json:"id"`
			Method  string `json:"method"`
			Params  struct {
				Name      string         `json:"name"`
				Arguments map[string]any `json:"arguments"`
			} `json:"params"`
		}
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		if request.JSONRPC == "2.0" && request.Method == "tools/call" && request.Params.Name == "health_check" {
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(map[string]any{
				"jsonrpc": "2.0", "id": request.ID,
				"result": map[string]any{"status": "ok", "timestamp": time.Now().UTC().Format(time.RFC3339Nano)},
			})
			return
		}
		if request.JSONRPC != "2.0" || request.Method != "tools/call" {
			http.Error(w, fmt.Sprintf("unexpected Agent Mail request: method=%q name=%q", request.Method, request.Params.Name), http.StatusBadRequest)
			return
		}
		writeResult := func(result any) {
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(map[string]any{"jsonrpc": "2.0", "id": request.ID, "result": result})
		}
		switch request.Params.Name {
		case "ensure_project":
			if request.Params.Arguments["human_key"] != fixture.projectDir {
				http.Error(w, fmt.Sprintf("unexpected ensure project args: %#v", request.Params.Arguments), http.StatusBadRequest)
				return
			}
			writeResult(map[string]any{"id": 1, "slug": "atomic-e2e", "human_key": fixture.projectDir, "created_at": time.Now().UTC().Format(time.RFC3339Nano)})
		case "list_file_reservations":
			if activeLease.Load() {
				now := time.Now().UTC()
				writeResult([]map[string]any{{
					"id": 987654, "path_pattern": "internal/cli/**", "agent_name": after.AgentName,
					"project_id": 1, "exclusive": true, "reason": "bead assignment: " + beadID,
					"created_ts": now.Format(time.RFC3339Nano), "expires_ts": now.Add(time.Hour).Format(time.RFC3339Nano),
				}})
				return
			}
			writeResult([]any{})
		case "release_file_reservations":
			ids, idsOK := atomicAssignmentAnyIntSlice(request.Params.Arguments["file_reservation_ids"])
			if request.Params.Arguments["project_key"] != fixture.projectDir || request.Params.Arguments["agent_name"] != after.AgentName ||
				!idsOK || !reflect.DeepEqual(ids, []int{987654}) {
				http.Error(w, fmt.Sprintf("unexpected reservation release: args=%#v", request.Params.Arguments), http.StatusBadRequest)
				return
			}
			releaseCalls.Add(1)
			activeLease.Store(false)
			writeResult(map[string]any{"released": 1})
		default:
			http.Error(w, "unexpected Agent Mail tool "+request.Params.Name, http.StatusNotFound)
		}
	}))
	defer mailServer.Close()

	recovered := fixture.runNTM(t, map[string]string{"AGENT_MAIL_URL": mailServer.URL + "/"},
		"--json", "assign", fixture.session,
		"--clear-pane=0", "--timeout=2s",
	)
	if recovered.exitCode != 0 || len(bytes.TrimSpace(recovered.stderr)) != 0 {
		t.Fatalf("recovered clear-pane exit=%d stdout=%s stderr=%s", recovered.exitCode, recovered.stdout, recovered.stderr)
	}
	var recoveredEnvelope struct {
		Success bool `json:"success"`
		Data    struct {
			Cleared []struct {
				BeadID  string `json:"bead_id"`
				Success bool   `json:"success"`
			} `json:"cleared"`
			Summary struct {
				ClearedCount int `json:"cleared_count"`
				FailedCount  int `json:"failed_count"`
			} `json:"summary"`
		} `json:"data"`
	}
	decodeAtomicAssignmentJSON(t, recovered.stdout, &recoveredEnvelope)
	if !recoveredEnvelope.Success || recoveredEnvelope.Data.Summary.ClearedCount != 1 || recoveredEnvelope.Data.Summary.FailedCount != 0 ||
		len(recoveredEnvelope.Data.Cleared) != 1 || recoveredEnvelope.Data.Cleared[0].BeadID != beadID || !recoveredEnvelope.Data.Cleared[0].Success {
		t.Fatalf("recovered clear-pane envelope = %+v", recoveredEnvelope)
	}
	if releaseCalls.Load() != 1 {
		t.Fatalf("reservation release calls=%d, want exactly 1", releaseCalls.Load())
	}
	ledger, err := fixture.readLedger()
	if err != nil {
		t.Fatalf("read ledger after recovered clear: %v", err)
	}
	if _, exists := ledger.Assignments[beadID]; exists {
		t.Fatalf("recovered clear retained assignment %s: %+v", beadID, ledger.Assignments[beadID])
	}

	// Earlier delivery left prompt text on the pane; re-render the composer
	// glyph so the bd-dp9oy delivery gate accepts the post-clear send.
	fixture.primePaneForSafeDispatch(t, 0)
	reassigned := fixture.runNTM(t, nil, atomicDirectArgs(fixture, beadID, prompt, false)...)
	if reassigned.exitCode != 0 || len(bytes.TrimSpace(reassigned.stderr)) != 0 {
		t.Fatalf("post-clear reassignment exit=%d stdout=%s stderr=%s", reassigned.exitCode, reassigned.stdout, reassigned.stderr)
	}
	fixture.waitForMarkerCount(t, 0, prompt, 2)
	fixture.assertMarkerCounts(t, prompt, map[int]int{0: 2, 1: 0})
	reassignedRecord := fixture.readLedgerAssignment(t, beadID)
	if reassignedRecord.IdempotencyKey == before.IdempotencyKey || reassignedRecord.DispatchReceiptID == before.DispatchReceiptID {
		t.Fatalf("post-clear assignment reused prior receipt: before=%+v after=%+v", before, reassignedRecord)
	}
}

func TestE2EAtomicAssignmentClearFailedBuiltProcess(t *testing.T) {
	CommonE2EPrerequisites(t)
	testutil.RequireTmuxThrottled(t)

	fixture := newAtomicAssignmentCLIFixture(t)
	mail := newAtomicAssignmentMailStub(fixture.projectDir)
	defer mail.close()
	failedBeadID := fixture.createBead(t, "Completion detector failure internal/completion/e2e_failure.go")
	retainedBeadID := fixture.createBead(t, "Healthy assignment retained")
	failedPrompt := fmt.Sprintf("NTM_ATOMIC_FAILED_%d", time.Now().UnixNano())
	retainedPrompt := fmt.Sprintf("NTM_ATOMIC_RETAINED_%d", time.Now().UnixNano())
	for _, seed := range []struct {
		pane   int
		beadID string
		prompt string
	}{
		{pane: 0, beadID: failedBeadID, prompt: failedPrompt},
		{pane: 1, beadID: retainedBeadID, prompt: retainedPrompt},
	} {
		requireReservation := seed.beadID == failedBeadID
		extraEnv := map[string]string(nil)
		if requireReservation {
			extraEnv = mail.env()
		}
		result := fixture.runNTM(t, extraEnv, atomicDirectArgsForPane(fixture, seed.pane, seed.beadID, seed.prompt, requireReservation)...)
		if result.exitCode != 0 || len(bytes.TrimSpace(result.stderr)) != 0 {
			t.Fatalf("seed pane %d exit=%d stdout=%s stderr=%s", seed.pane, result.exitCode, result.stdout, result.stderr)
		}
		fixture.waitForMarkerCount(t, seed.pane, seed.prompt, 1)
	}
	retainedBefore := fixture.readLedgerAssignment(t, retainedBeadID)
	failedBeforeDetection := fixture.readLedgerAssignment(t, failedBeadID)
	watch := fixture.startCompletionWatch(t, mail.env())
	readinessDeadline := time.Now().Add(60 * time.Second)
	var watchStdout, watchStderr string
	for {
		watchStdout, watchStderr = watch.output(t)
		if strings.Contains(watchStdout, "Starting watch mode with strategy=") {
			break
		}
		if time.Now().After(readinessDeadline) {
			t.Fatalf("assignment watch did not become ready: stdout=%s stderr=%s", watchStdout, watchStderr)
		}
		time.Sleep(50 * time.Millisecond)
	}

	const failureMarker = "ERROR: fatal assignment failure"
	fixture.mustTMUX(t, "send-keys", "-t", fixture.panes[0].ID, "-l", failureMarker)
	fixture.mustTMUX(t, "send-keys", "-t", fixture.panes[0].ID, "Enter")
	fixture.waitForMarkerCount(t, 0, failureMarker, 1)

	deadline := time.Now().Add(20 * time.Second)
	for {
		ledger, err := fixture.readLedger()
		if err == nil {
			if record := ledger.Assignments[failedBeadID]; record != nil && record.Status == "failed" &&
				record.PendingCompletionEventID == "" && record.CompletionConsumerToken == "" && record.CompletionLeaseExpiresAt == nil {
				break
			}
		}
		if time.Now().After(deadline) {
			watchStdout, watchStderr = watch.output(t)
			t.Fatalf("completion detector did not mark %s failed: read_err=%v stdout=%s stderr=%s", failedBeadID, err, watchStdout, watchStderr)
		}
		time.Sleep(50 * time.Millisecond)
	}
	failedAfterDetection := fixture.readLedgerAssignment(t, failedBeadID)
	if failedAfterDetection.Status != "failed" || failedAfterDetection.ReservationState != "released" || failedAfterDetection.ReservationCompleted ||
		len(failedAfterDetection.ReservationIDs) != 0 || len(failedAfterDetection.ReservedPaths) != 0 || failedAfterDetection.ClearState != "" ||
		failedAfterDetection.PendingTerminalStatus != "" || failedAfterDetection.TerminalClaimReleased ||
		failedAfterDetection.PendingCompletionEventID != "" || failedAfterDetection.CompletionConsumerToken != "" || failedAfterDetection.CompletionLeaseExpiresAt != nil {
		t.Fatalf("completion detector terminal reconciliation = %+v", failedAfterDetection)
	}
	mailAfterDetection := mail.snapshot()
	if mailAfterDetection.ReleaseCalls != 1 || !reflect.DeepEqual(mailAfterDetection.ReleasedIDs, failedBeforeDetection.ReservationIDs) || len(mailAfterDetection.Active) != 0 {
		t.Fatalf("completion detector reservation release = %+v", mailAfterDetection)
	}
	watchStdout, watchStderr = watch.stopClean(t)
	if len(bytes.TrimSpace([]byte(watchStderr))) != 0 {
		t.Fatalf("assignment watch stderr=%s", watchStderr)
	}
	retainedAfterDetection := fixture.readLedgerAssignment(t, retainedBeadID)
	if retainedAfterDetection.Status != "assigned" && retainedAfterDetection.Status != "working" {
		t.Fatalf("healthy assignment entered unexpected state during failure detection: %+v", retainedAfterDetection)
	}
	assertAtomicAssignmentReceiptUnchanged(t, retainedBefore, retainedAfterDetection)

	cleared := fixture.runNTM(t, mail.env(),
		"--json", "assign", fixture.session,
		"--repo="+fixture.projectDir,
		"--clear-failed",
		"--timeout=15s",
	)
	if cleared.exitCode != 0 || len(bytes.TrimSpace(cleared.stderr)) != 0 {
		t.Fatalf("clear-failed exit=%d stdout=%s stderr=%s", cleared.exitCode, cleared.stdout, cleared.stderr)
	}
	var envelope struct {
		Success    bool   `json:"success"`
		Subcommand string `json:"subcommand"`
		Data       struct {
			Cleared []struct {
				BeadID  string `json:"bead_id"`
				Success bool   `json:"success"`
			} `json:"cleared"`
			Summary struct {
				ClearedCount int `json:"cleared_count"`
				FailedCount  int `json:"failed_count"`
			} `json:"summary"`
		} `json:"data"`
	}
	decodeAtomicAssignmentJSON(t, cleared.stdout, &envelope)
	if !envelope.Success || envelope.Subcommand != "clear-failed" || envelope.Data.Summary.ClearedCount != 1 ||
		envelope.Data.Summary.FailedCount != 0 || len(envelope.Data.Cleared) != 1 ||
		envelope.Data.Cleared[0].BeadID != failedBeadID || !envelope.Data.Cleared[0].Success {
		t.Fatalf("clear-failed envelope = %+v", envelope)
	}
	fixture.assertLedgerHasNoAssignment(t, failedBeadID)
	if afterClear := mail.snapshot(); afterClear.ReleaseCalls != 1 {
		t.Fatalf("clear-failed repeated terminal reservation release: before=%+v after=%+v", mailAfterDetection, afterClear)
	}
	assertAtomicAssignmentReceiptUnchanged(t, retainedBefore, fixture.readLedgerAssignment(t, retainedBeadID))
	fixture.assertMarkerCounts(t, retainedPrompt, map[int]int{0: 0, 1: 1})
}

func TestE2EAtomicCompletionOutboxRestartReplayAckRetryAndFinalAckBuiltProcess(t *testing.T) {
	CommonE2EPrerequisites(t)
	testutil.RequireTmuxThrottled(t)

	fixture := newAtomicAssignmentCLIFixture(t)
	t.Setenv("HOME", fixture.homeDir)
	seedCompletionOutbox := func(title string) (string, string, string) {
		t.Helper()
		beadID := fixture.createBead(t, title)
		prompt := fmt.Sprintf("NTM_ATOMIC_COMPLETION_OUTBOX_%d", time.Now().UnixNano())
		// Prior seeds left prompt text on the pane; re-render the composer
		// glyph so the bd-dp9oy delivery gate accepts each new typed send.
		fixture.primePaneForSafeDispatch(t, 0)
		seed := fixture.runNTM(t, nil, atomicDirectArgs(fixture, beadID, prompt, false)...)
		if seed.exitCode != 0 || len(bytes.TrimSpace(seed.stderr)) != 0 {
			t.Fatalf("seed completion outbox assignment exit=%d stdout=%s stderr=%s", seed.exitCode, seed.stdout, seed.stderr)
		}
		fixture.waitForMarkerCount(t, 0, prompt, 1)
		fixture.mustBR(t, "close", beadID, "--reason=completion-outbox-e2e", "--json")

		store, err := assignment.LoadStoreStrict(fixture.session)
		if err != nil {
			t.Fatalf("load assignment store for completion outbox seed: %v", err)
		}
		observed := store.Get(beadID)
		barrier, applied, err := store.BeginTerminalReconciliationWithCompletionEventIfCurrent(t.Context(), observed, assignment.StatusCompleted, "")
		if err != nil || !applied || barrier == nil {
			t.Fatalf("begin durable completion outbox barrier=%+v applied=%v error=%v", barrier, applied, err)
		}
		if _, err := store.RecordClearLeasesReleased(t.Context(), beadID); err != nil {
			t.Fatalf("record completion outbox lease release: %v", err)
		}
		if _, err := store.RecordTerminalClaimReleased(t.Context(), beadID); err != nil {
			t.Fatalf("record completion outbox claim release: %v", err)
		}
		if err := store.CompleteTerminalReconciliation(t.Context(), beadID, assignment.StatusCompleted, ""); err != nil {
			t.Fatalf("complete durable completion outbox seed: %v", err)
		}
		terminal := fixture.readLedgerAssignment(t, beadID)
		if terminal.Status != "completed" || terminal.PendingCompletionEventID == "" || terminal.CompletionDetectedAt == nil ||
			terminal.CompletionConsumerToken != "" || terminal.CompletionLeaseExpiresAt != nil {
			t.Fatalf("durable completion outbox seed=%+v", terminal)
		}
		return beadID, terminal.PendingCompletionEventID, "Completion: " + beadID + " by pane"
	}
	outboxCleared := func(current *atomicAssignmentRecord) bool {
		return current.PendingCompletionEventID == "" && current.CompletionDetectedAt == nil &&
			current.CompletionConsumerToken == "" && current.CompletionLeaseExpiresAt == nil
	}

	// The first built watcher starts from a fully reconciled durable row, handles
	// it once, survives a persistence failure while acknowledging, and retries
	// only the exact acknowledgement after the detector's deduplication window.
	beadID, eventID, completionMarker := seedCompletionOutbox("Completion outbox restart replay")
	watch := fixture.startCompletionWatch(t, map[string]string{"NTM_TEST_COMPLETION_ACK_FAIL_ONCE": beadID})
	ackFailureMarker := "injected one-shot completion acknowledgement failure for " + beadID
	deadline := time.Now().Add(35 * time.Second)
	for {
		current := fixture.readLedgerAssignment(t, beadID)
		stdout, stderr := watch.output(t)
		if strings.Contains(stdout, ackFailureMarker) && outboxCleared(current) {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("completion outbox was not retried and acknowledged: record=%+v stdout=%s stderr=%s", current, stdout, stderr)
		}
		time.Sleep(50 * time.Millisecond)
	}
	watchStdoutText, watchStderrText := watch.stopClean(t)
	if len(bytes.TrimSpace([]byte(watchStderrText))) != 0 {
		t.Fatalf("completion outbox watcher stderr=%s", watchStderrText)
	}
	if count := strings.Count(watchStdoutText, completionMarker); count != 1 {
		t.Fatalf("completion handler count=%d, want 1 after acknowledgement retry: stdout=%s", count, watchStdoutText)
	}
	if count := strings.Count(watchStdoutText, ackFailureMarker); count != 1 {
		t.Fatalf("completion acknowledgement failure count=%d, want 1: stdout=%s", count, watchStdoutText)
	}

	// One watcher may claim more events than its serial handler can consume before
	// their leases expire. Hold the first handler long enough for its already-
	// queued second event to expire, then let another built process take over the
	// second event. The stale queued consumer must validate ownership before any
	// handler or acknowledgement side effect.
	queuedFirstBeadID, queuedFirstEventID, queuedFirstMarker := seedCompletionOutbox("Completion outbox queued lease first")
	queuedSecondBeadID, queuedSecondEventID, queuedSecondMarker := seedCompletionOutbox("Completion outbox queued lease second")
	queuedOriginal := fixture.startCompletionWatchWithInterval(t, map[string]string{
		"NTM_TEST_COMPLETION_LEASE_DURATION": "15s",
		"NTM_TEST_COMPLETION_HANDLER_DELAY":  "45s",
	}, 2*time.Second)
	var queuedOriginalToken string
	var queuedFirstExpiry time.Time
	var queuedSecondExpiry time.Time
	deadline = time.Now().Add(45 * time.Second)
	for {
		first := fixture.readLedgerAssignment(t, queuedFirstBeadID)
		second := fixture.readLedgerAssignment(t, queuedSecondBeadID)
		if first.PendingCompletionEventID == queuedFirstEventID && second.PendingCompletionEventID == queuedSecondEventID &&
			first.CompletionConsumerToken != "" && first.CompletionConsumerToken == second.CompletionConsumerToken &&
			first.CompletionLeaseExpiresAt != nil && second.CompletionLeaseExpiresAt != nil {
			queuedOriginalToken = first.CompletionConsumerToken
			queuedFirstExpiry = *first.CompletionLeaseExpiresAt
			queuedSecondExpiry = *second.CompletionLeaseExpiresAt
			break
		}
		if time.Now().After(deadline) {
			stdout, stderr := queuedOriginal.output(t)
			t.Fatalf("one watcher did not claim both queued completion events: first=%+v second=%+v stdout=%s stderr=%s", first, second, stdout, stderr)
		}
		time.Sleep(25 * time.Millisecond)
	}
	type queuedCompletion struct {
		beadID string
		event  string
		marker string
		record *atomicAssignmentRecord
	}
	firstCompletion := queuedCompletion{beadID: queuedFirstBeadID, event: queuedFirstEventID, marker: queuedFirstMarker}
	secondCompletion := queuedCompletion{beadID: queuedSecondBeadID, event: queuedSecondEventID, marker: queuedSecondMarker}
	isOriginalRenewed := func(item queuedCompletion, initialExpiry time.Time) bool {
		return item.record.PendingCompletionEventID == item.event && item.record.CompletionConsumerToken == queuedOriginalToken &&
			item.record.CompletionLeaseExpiresAt != nil && item.record.CompletionLeaseExpiresAt.After(initialExpiry)
	}
	var activeCompletion, expiredCompletion queuedCompletion
	deadline = time.Now().Add(45 * time.Second)
	for activeCompletion.beadID == "" {
		firstCompletion.record = fixture.readLedgerAssignment(t, queuedFirstBeadID)
		secondCompletion.record = fixture.readLedgerAssignment(t, queuedSecondBeadID)
		firstRenewed := isOriginalRenewed(firstCompletion, queuedFirstExpiry)
		secondRenewed := isOriginalRenewed(secondCompletion, queuedSecondExpiry)
		switch {
		case firstRenewed && !secondRenewed:
			activeCompletion, expiredCompletion = firstCompletion, secondCompletion
		case secondRenewed && !firstRenewed:
			activeCompletion, expiredCompletion = secondCompletion, firstCompletion
		}
		if activeCompletion.beadID != "" {
			break
		}
		if time.Now().After(deadline) {
			stdout, stderr := queuedOriginal.output(t)
			t.Fatalf("one queued completion handler did not establish a unique heartbeat: first=%+v second=%+v stdout=%s stderr=%s", firstCompletion.record, secondCompletion.record, stdout, stderr)
		}
		time.Sleep(25 * time.Millisecond)
	}

	queuedTakeover := fixture.startCompletionWatchWithInterval(t, map[string]string{
		"NTM_TEST_COMPLETION_LEASE_DURATION": "15s",
		"NTM_TEST_COMPLETION_HANDLER_DELAY":  "10s",
	}, 500*time.Millisecond)
	var queuedTakeoverToken string
	deadline = time.Now().Add(60 * time.Second)
	for {
		queued := fixture.readLedgerAssignment(t, expiredCompletion.beadID)
		active := fixture.readLedgerAssignment(t, activeCompletion.beadID)
		if queued.PendingCompletionEventID == expiredCompletion.event && queued.CompletionConsumerToken != "" &&
			queued.CompletionConsumerToken != queuedOriginalToken && queued.CompletionLeaseExpiresAt != nil &&
			active.PendingCompletionEventID == activeCompletion.event && active.CompletionConsumerToken == queuedOriginalToken &&
			active.CompletionLeaseExpiresAt != nil && active.CompletionLeaseExpiresAt.After(time.Now().UTC()) {
			queuedTakeoverToken = queued.CompletionConsumerToken
			break
		}
		if time.Now().After(deadline) {
			originalStdout, originalStderr := queuedOriginal.output(t)
			takeoverStdout, takeoverStderr := queuedTakeover.output(t)
			t.Fatalf("expired queued event was not taken over: record=%+v original=%s/%s takeover=%s/%s", queued, originalStdout, originalStderr, takeoverStdout, takeoverStderr)
		}
		time.Sleep(25 * time.Millisecond)
	}
	staleQueuedStore, err := assignment.LoadStoreStrict(fixture.session)
	if err != nil {
		t.Fatalf("load completion store for stale queued acknowledgement: %v", err)
	}
	if acknowledged, err := staleQueuedStore.AcknowledgeCompletionEvent(
		t.Context(), expiredCompletion.beadID, expiredCompletion.event, queuedOriginalToken,
	); err != nil || acknowledged {
		t.Fatalf("stale queued acknowledgement applied=%v error=%v", acknowledged, err)
	}
	afterStaleQueuedAck := fixture.readLedgerAssignment(t, expiredCompletion.beadID)
	if afterStaleQueuedAck.PendingCompletionEventID != expiredCompletion.event ||
		afterStaleQueuedAck.CompletionConsumerToken != queuedTakeoverToken {
		t.Fatalf("stale queued acknowledgement changed takeover lease: %+v", afterStaleQueuedAck)
	}
	activeDuringTakeover := fixture.readLedgerAssignment(t, activeCompletion.beadID)
	if activeDuringTakeover.PendingCompletionEventID != activeCompletion.event ||
		activeDuringTakeover.CompletionConsumerToken != queuedOriginalToken ||
		activeDuringTakeover.CompletionLeaseExpiresAt == nil ||
		!activeDuringTakeover.CompletionLeaseExpiresAt.After(time.Now().UTC()) {
		t.Fatalf("active queued event lost its live heartbeat during queued-event takeover: %+v", activeDuringTakeover)
	}

	deadline = time.Now().Add(2 * time.Minute)
	for {
		active := fixture.readLedgerAssignment(t, activeCompletion.beadID)
		queued := fixture.readLedgerAssignment(t, expiredCompletion.beadID)
		originalStdout, originalStderr := queuedOriginal.output(t)
		if outboxCleared(active) && outboxCleared(queued) && strings.Contains(originalStdout, "completion event "+expiredCompletion.event+" consumer lease was lost before handling") {
			break
		}
		if time.Now().After(deadline) {
			takeoverStdout, takeoverStderr := queuedTakeover.output(t)
			t.Fatalf("queued completion takeover did not converge: active=%+v queued=%+v original=%s/%s takeover=%s/%s", active, queued, originalStdout, originalStderr, takeoverStdout, takeoverStderr)
		}
		time.Sleep(50 * time.Millisecond)
	}
	queuedOriginalStdout, queuedOriginalStderr := queuedOriginal.stopClean(t)
	queuedTakeoverStdout, queuedTakeoverStderr := queuedTakeover.stopClean(t)
	if len(bytes.TrimSpace([]byte(queuedOriginalStderr))) != 0 || len(bytes.TrimSpace([]byte(queuedTakeoverStderr))) != 0 {
		t.Fatalf("queued completion watcher stderr: original=%s takeover=%s", queuedOriginalStderr, queuedTakeoverStderr)
	}
	if count := strings.Count(queuedOriginalStdout, activeCompletion.marker); count != 1 {
		t.Fatalf("original queued watcher active-event handler count=%d, want 1: stdout=%s", count, queuedOriginalStdout)
	}
	if count := strings.Count(queuedOriginalStdout, expiredCompletion.marker); count != 0 {
		t.Fatalf("stale queued watcher crossed expired-event handler boundary %d times: stdout=%s", count, queuedOriginalStdout)
	}
	if count := strings.Count(queuedTakeoverStdout, expiredCompletion.marker); count != 1 {
		t.Fatalf("takeover queued watcher expired-event handler count=%d, want 1: stdout=%s", count, queuedTakeoverStdout)
	}
	if count := strings.Count(queuedTakeoverStdout, activeCompletion.marker); count != 0 {
		t.Fatalf("takeover queued watcher crossed active-event handler boundary %d times: stdout=%s", count, queuedTakeoverStdout)
	}
	if queuedTakeoverToken == "" || queuedTakeoverToken == queuedOriginalToken {
		t.Fatalf("queued takeover token=%q original=%q", queuedTakeoverToken, queuedOriginalToken)
	}

	// Two independent built watchers contend for one event. The owner runs for
	// more than three times the lease duration; its heartbeat must keep the same token live,
	// and the competing process must never enter the handler.
	contendedBeadID, contendedEventID, contendedMarker := seedCompletionOutbox("Completion outbox multi-process lease")
	leaseEnv := map[string]string{
		"NTM_TEST_COMPLETION_LEASE_DURATION": "30s",
		"NTM_TEST_COMPLETION_HANDLER_DELAY":  "100s",
	}
	contenderA := fixture.startCompletionWatchWithInterval(t, leaseEnv, 2*time.Second)
	contenderB := fixture.startCompletionWatchWithInterval(t, leaseEnv, 2*time.Second)
	var ownerToken string
	var firstLeaseExpiry time.Time
	deadline = time.Now().Add(45 * time.Second)
	for {
		current := fixture.readLedgerAssignment(t, contendedBeadID)
		if current.PendingCompletionEventID == contendedEventID && current.CompletionConsumerToken != "" && current.CompletionLeaseExpiresAt != nil {
			ownerToken = current.CompletionConsumerToken
			firstLeaseExpiry = *current.CompletionLeaseExpiresAt
			break
		}
		if time.Now().After(deadline) {
			stdoutA, stderrA := contenderA.output(t)
			stdoutB, stderrB := contenderB.output(t)
			t.Fatalf("no watcher claimed contended event: record=%+v a=%s/%s b=%s/%s", current, stdoutA, stderrA, stdoutB, stderrB)
		}
		time.Sleep(25 * time.Millisecond)
	}
	deadline = time.Now().Add(60 * time.Second)
	var current *atomicAssignmentRecord
	var stdoutA, stderrA, stdoutB, stderrB string
	for {
		current = fixture.readLedgerAssignment(t, contendedBeadID)
		stdoutA, stderrA = contenderA.output(t)
		stdoutB, stderrB = contenderB.output(t)
		if current.PendingCompletionEventID != contendedEventID || current.CompletionConsumerToken != ownerToken {
			t.Fatalf("slow handler lost completion ownership before heartbeat proof: first_expiry=%v record=%+v", firstLeaseExpiry, current)
		}
		if count := strings.Count(stdoutA, contendedMarker) + strings.Count(stdoutB, contendedMarker); count != 0 {
			t.Fatalf("slow handler completed before heartbeat proof: count=%d a=%s b=%s", count, stdoutA, stdoutB)
		}
		if len(bytes.TrimSpace([]byte(stderrA))) != 0 || len(bytes.TrimSpace([]byte(stderrB))) != 0 {
			t.Fatalf("contending watcher stderr before completion: a=%s b=%s", stderrA, stderrB)
		}
		now := time.Now()
		if !now.Before(firstLeaseExpiry) && current.CompletionLeaseExpiresAt != nil &&
			current.CompletionLeaseExpiresAt.After(firstLeaseExpiry) && current.CompletionLeaseExpiresAt.After(now) {
			break
		}
		if now.After(deadline) {
			t.Fatalf("slow handler lease was not renewed past original expiry: first_expiry=%v record=%+v", firstLeaseExpiry, current)
		}
		time.Sleep(50 * time.Millisecond)
	}
	deadline = time.Now().Add(90 * time.Second)
	for {
		current = fixture.readLedgerAssignment(t, contendedBeadID)
		if outboxCleared(current) {
			break
		}
		if time.Now().After(deadline) {
			stdoutA, stderrA = contenderA.output(t)
			stdoutB, stderrB = contenderB.output(t)
			t.Fatalf("contended completion was not acknowledged: record=%+v a=%s/%s b=%s/%s", current, stdoutA, stderrA, stdoutB, stderrB)
		}
		time.Sleep(50 * time.Millisecond)
	}
	stdoutA, stderrA = contenderA.stopClean(t)
	stdoutB, stderrB = contenderB.stopClean(t)
	if len(bytes.TrimSpace([]byte(stderrA))) != 0 || len(bytes.TrimSpace([]byte(stderrB))) != 0 {
		t.Fatalf("contending watcher stderr: a=%s b=%s", stderrA, stderrB)
	}
	if count := strings.Count(stdoutA, contendedMarker) + strings.Count(stdoutB, contendedMarker); count != 1 {
		t.Fatalf("contended completion handler count=%d, want 1: a=%s b=%s", count, stdoutA, stdoutB)
	}

	// Terminate a lease holder before the delayed handler performs its observable
	// effect. After durable expiry, a new process must replace the token. The old
	// token cannot acknowledge the generation, while the recovery handler runs
	// and acknowledges exactly once.
	crashBeadID, crashEventID, crashMarker := seedCompletionOutbox("Completion outbox expired holder recovery")
	holder := fixture.startCompletionWatchWithInterval(t, map[string]string{
		"NTM_TEST_COMPLETION_LEASE_DURATION": "3s",
		"NTM_TEST_COMPLETION_HANDLER_DELAY":  "10s",
	}, 2*time.Second)
	var holderToken string
	deadline = time.Now().Add(20 * time.Second)
	for {
		current = fixture.readLedgerAssignment(t, crashBeadID)
		if current.PendingCompletionEventID == crashEventID && current.CompletionConsumerToken != "" && current.CompletionLeaseExpiresAt != nil {
			holderToken = current.CompletionConsumerToken
			break
		}
		if time.Now().After(deadline) {
			stdout, stderr := holder.output(t)
			t.Fatalf("crash holder did not claim event: record=%+v stdout=%s stderr=%s", current, stdout, stderr)
		}
		time.Sleep(25 * time.Millisecond)
	}
	holderStdout, holderStderr := holder.kill(t)
	if len(bytes.TrimSpace([]byte(holderStderr))) != 0 || strings.Count(holderStdout, crashMarker) != 0 {
		t.Fatalf("crashed holder crossed completion side effect: stdout=%s stderr=%s", holderStdout, holderStderr)
	}
	afterCrash := fixture.readLedgerAssignment(t, crashBeadID)
	if afterCrash.PendingCompletionEventID != crashEventID || afterCrash.CompletionConsumerToken != holderToken || afterCrash.CompletionLeaseExpiresAt == nil {
		t.Fatalf("crashed holder did not leave its durable lease for expiry recovery: %+v", afterCrash)
	}
	if wait := time.Until(afterCrash.CompletionLeaseExpiresAt.Add(750 * time.Millisecond)); wait > 0 {
		time.Sleep(wait)
	}
	recovery := fixture.startCompletionWatchWithInterval(t, map[string]string{
		"NTM_TEST_COMPLETION_LEASE_DURATION": "3s",
		"NTM_TEST_COMPLETION_HANDLER_DELAY":  "5s",
	}, 2*time.Second)
	var recoveryToken string
	deadline = time.Now().Add(20 * time.Second)
	for {
		current = fixture.readLedgerAssignment(t, crashBeadID)
		if current.PendingCompletionEventID == crashEventID && current.CompletionConsumerToken != "" && current.CompletionConsumerToken != holderToken {
			recoveryToken = current.CompletionConsumerToken
			break
		}
		if time.Now().After(deadline) {
			stdout, stderr := recovery.output(t)
			t.Fatalf("expired completion lease was not taken over: record=%+v stdout=%s stderr=%s", current, stdout, stderr)
		}
		time.Sleep(25 * time.Millisecond)
	}
	staleStore, err := assignment.LoadStoreStrict(fixture.session)
	if err != nil {
		t.Fatalf("load completion store for stale acknowledgement: %v", err)
	}
	if acknowledged, err := staleStore.AcknowledgeCompletionEvent(t.Context(), crashBeadID, crashEventID, holderToken); err != nil || acknowledged {
		t.Fatalf("stale holder acknowledgement applied=%v error=%v", acknowledged, err)
	}
	afterStaleAck := fixture.readLedgerAssignment(t, crashBeadID)
	if afterStaleAck.PendingCompletionEventID != crashEventID || afterStaleAck.CompletionConsumerToken != recoveryToken {
		t.Fatalf("stale holder acknowledgement changed recovery lease: %+v", afterStaleAck)
	}
	// The handler intentionally holds the lease for five seconds so this test can
	// exercise a stale acknowledgement while the replacement owns it. Under the
	// full E2E shard, scheduler contention can delay that bounded handler without
	// invalidating the heartbeat-protected lease, so wait for the durable outcome
	// rather than coupling correctness to a 20-second wall-clock slot.
	deadline = time.Now().Add(60 * time.Second)
	for {
		current = fixture.readLedgerAssignment(t, crashBeadID)
		if outboxCleared(current) {
			break
		}
		if time.Now().After(deadline) {
			stdout, stderr := recovery.output(t)
			t.Fatalf("recovered completion was not acknowledged: record=%+v stdout=%s stderr=%s", current, stdout, stderr)
		}
		time.Sleep(50 * time.Millisecond)
	}
	recoveryStdout, recoveryStderr := recovery.stopClean(t)
	if len(bytes.TrimSpace([]byte(recoveryStderr))) != 0 || strings.Count(recoveryStdout, crashMarker) != 1 {
		t.Fatalf("recovery completion side effect was not exactly once: stdout=%s stderr=%s", recoveryStdout, recoveryStderr)
	}

	// A final fresh process must not replay any acknowledged event generation.
	// Give it a new sentinel generation and wait for that durable acknowledgement
	// so the assertion proves the detector completed a post-restart scan without
	// relying on a fixed grace sleep.
	restartBeadID, restartEventID, restartMarker := seedCompletionOutbox("Completion outbox post-ack restart sentinel")
	restart := fixture.startCompletionWatch(t, nil)
	deadline = time.Now().Add(60 * time.Second)
	for {
		current = fixture.readLedgerAssignment(t, restartBeadID)
		if outboxCleared(current) {
			break
		}
		if time.Now().After(deadline) {
			stdout, stderr := restart.output(t)
			t.Fatalf("post-ack watcher did not scan sentinel: record=%+v stdout=%s stderr=%s", current, stdout, stderr)
		}
		time.Sleep(50 * time.Millisecond)
	}
	restartStdout, restartStderr := restart.stopClean(t)
	if len(bytes.TrimSpace([]byte(restartStderr))) != 0 {
		t.Fatalf("post-ack completion watcher stderr=%s", restartStderr)
	}
	if count := strings.Count(restartStdout, restartMarker); count != 1 {
		t.Fatalf("post-ack sentinel handler count=%d, want 1: bead=%s event=%s stdout=%s", count, restartBeadID, restartEventID, restartStdout)
	}
	for _, completed := range []struct {
		beadID string
		event  string
		marker string
	}{
		{beadID: beadID, event: eventID, marker: completionMarker},
		{beadID: queuedFirstBeadID, event: queuedFirstEventID, marker: queuedFirstMarker},
		{beadID: queuedSecondBeadID, event: queuedSecondEventID, marker: queuedSecondMarker},
		{beadID: contendedBeadID, event: contendedEventID, marker: contendedMarker},
		{beadID: crashBeadID, event: crashEventID, marker: crashMarker},
	} {
		if strings.Contains(restartStdout, completed.marker) || strings.Contains(restartStdout, completed.event) {
			t.Fatalf("acknowledged completion event replayed after restart: bead=%s event=%s stdout=%s", completed.beadID, completed.event, restartStdout)
		}
		if final := fixture.readLedgerAssignment(t, completed.beadID); !outboxCleared(final) {
			t.Fatalf("post-restart completion outbox for %s=%+v", completed.beadID, final)
		}
	}
}

func TestE2EAtomicExplicitClearPreservesPendingCompletionOutboxBuiltProcess(t *testing.T) {
	CommonE2EPrerequisites(t)
	testutil.RequireTmuxThrottled(t)

	for _, leased := range []bool{false, true} {
		name := "unleased"
		if leased {
			name = "actively_leased"
		}
		t.Run(name, func(t *testing.T) {
			fixture := newAtomicAssignmentCLIFixture(t)
			t.Setenv("HOME", fixture.homeDir)
			beadID := fixture.createBead(t, "Pending completion clear guard "+name)
			prompt := fmt.Sprintf("NTM_ATOMIC_PENDING_COMPLETION_CLEAR_%s_%d", name, time.Now().UnixNano())
			seed := fixture.runNTM(t, nil, atomicDirectArgs(fixture, beadID, prompt, false)...)
			if seed.exitCode != 0 || len(bytes.TrimSpace(seed.stderr)) != 0 {
				t.Fatalf("seed pending completion clear exit=%d stdout=%s stderr=%s", seed.exitCode, seed.stdout, seed.stderr)
			}
			fixture.waitForMarkerCount(t, 0, prompt, 1)

			store, err := assignment.LoadStoreStrict(fixture.session)
			if err != nil {
				t.Fatalf("load pending completion clear store: %v", err)
			}
			observed := store.Get(beadID)
			barrier, applied, err := store.BeginTerminalReconciliationWithCompletionEventIfCurrent(
				t.Context(), observed, assignment.StatusFailed, "completion-clear-guard-e2e",
			)
			if err != nil || !applied || barrier == nil {
				t.Fatalf("begin pending completion clear barrier=%+v applied=%v error=%v", barrier, applied, err)
			}
			if _, err := store.RecordClearLeasesReleased(t.Context(), beadID); err != nil {
				t.Fatalf("record pending completion reservation release: %v", err)
			}
			if _, err := store.RecordTerminalClaimReleased(t.Context(), beadID); err != nil {
				t.Fatalf("record pending completion claim release: %v", err)
			}
			if err := store.CompleteTerminalReconciliation(t.Context(), beadID, assignment.StatusFailed, "completion-clear-guard-e2e"); err != nil {
				t.Fatalf("complete pending completion terminal row: %v", err)
			}
			terminal := fixture.readLedgerAssignment(t, beadID)
			if terminal.Status != "failed" || terminal.PendingCompletionEventID == "" || terminal.CompletionDetectedAt == nil {
				t.Fatalf("pending completion clear fixture = %+v", terminal)
			}
			if leased {
				claimed, acquired, claimErr := store.ClaimPendingCompletionEvent(
					t.Context(), beadID, terminal.PendingCompletionEventID, "clear-guard-consumer", time.Minute,
				)
				if claimErr != nil || !acquired || claimed == nil || claimed.CompletionLeaseExpiresAt == nil {
					t.Fatalf("claim pending completion clear event acquired=%v row=%+v error=%v", acquired, claimed, claimErr)
				}
				terminal = fixture.readLedgerAssignment(t, beadID)
			}

			result := fixture.runNTM(t, nil,
				"--json", "assign", fixture.session,
				"--repo="+fixture.projectDir,
				"--clear-failed",
				"--timeout=15s",
			)
			if result.exitCode != 1 || len(bytes.TrimSpace(result.stderr)) != 0 {
				t.Fatalf("pending completion clear exit=%d stdout=%s stderr=%s", result.exitCode, result.stdout, result.stderr)
			}
			var envelope struct {
				Success bool `json:"success"`
				Data    struct {
					Cleared []struct {
						BeadID    string `json:"bead_id"`
						Success   bool   `json:"success"`
						ErrorCode string `json:"error_code"`
					} `json:"cleared"`
				} `json:"data"`
				Error *struct {
					Code string `json:"code"`
				} `json:"error"`
			}
			decodeAtomicAssignmentJSON(t, result.stdout, &envelope)
			if envelope.Success || envelope.Error == nil || envelope.Error.Code != "COMPLETION_EVENT_PENDING" ||
				len(envelope.Data.Cleared) != 1 || envelope.Data.Cleared[0].BeadID != beadID ||
				envelope.Data.Cleared[0].Success || envelope.Data.Cleared[0].ErrorCode != "COMPLETION_EVENT_PENDING" {
				t.Fatalf("pending completion clear envelope = %+v", envelope)
			}
			after := fixture.readLedgerAssignment(t, beadID)
			if !reflect.DeepEqual(after, terminal) {
				t.Fatalf("pending completion clear mutated durable outbox:\nbefore=%+v\nafter=%+v", terminal, after)
			}
			if _, err := assignment.LoadStoreStrict(fixture.session); err != nil {
				t.Fatalf("strict reload after refused pending completion clear: %v", err)
			}
			fixture.assertMarkerCounts(t, prompt, map[int]int{0: 1, 1: 0})
		})
	}
}

func TestE2EAtomicAssignmentCompletionRejectsSupersededGeneration(t *testing.T) {
	CommonE2EPrerequisites(t)
	testutil.RequireTmuxThrottled(t)

	fixture := newAtomicAssignmentCLIFixture(t)
	mail := newAtomicAssignmentMailStub(fixture.projectDir)
	defer mail.close()
	beadID := fixture.createBead(t, "Completion generation compare and set internal/completion/winner_lease.go")
	prompt := fmt.Sprintf("NTM_ATOMIC_COMPLETION_CAS_%d", time.Now().UnixNano())
	seed := fixture.runNTM(t, mail.env(), atomicDirectArgs(fixture, beadID, prompt, true)...)
	if seed.exitCode != 0 || len(bytes.TrimSpace(seed.stderr)) != 0 {
		t.Fatalf("seed completion generation exit=%d stdout=%s stderr=%s", seed.exitCode, seed.stdout, seed.stderr)
	}
	fixture.waitForMarkerCount(t, 0, prompt, 1)
	oldGeneration := fixture.readLedgerAssignment(t, beadID)
	if len(oldGeneration.ReservationIDs) == 0 || len(oldGeneration.ReservationIDs) != len(oldGeneration.ReservedPaths) {
		t.Fatalf("stale-generation fixture has no winner lease: %+v", oldGeneration)
	}
	fixture.mustBR(t, "close", beadID, "--reason=completion-generation-cas-e2e", "--json")

	lockPath := atomicAssignmentBeadOperationLockPath(fixture.ledgerPath(), beadID)
	lockFile, err := os.OpenFile(lockPath, os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		t.Fatalf("open completion generation lock: %v", err)
	}
	defer lockFile.Close()
	if err := syscall.Flock(int(lockFile.Fd()), syscall.LOCK_EX); err != nil {
		t.Fatalf("hold completion generation lock: %v", err)
	}
	lockHeld := true
	defer func() {
		if lockHeld {
			_ = syscall.Flock(int(lockFile.Fd()), syscall.LOCK_UN)
		}
	}()

	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	watch := exec.CommandContext(ctx, fixture.ntmPath,
		"assign", fixture.session,
		"--repo="+fixture.projectDir,
		"--watch",
		"--watch-interval=2s",
		"--dry-run",
		"--reserve-files=false",
		"--quiet",
	)
	watch.Dir = fixture.projectDir
	watch.Env = atomicAssignmentMergeEnv(fixture.env, fixture.completionWatchEnv(t, mail.env()))
	var watchStdout bytes.Buffer
	var watchStderr bytes.Buffer
	watch.Stdout = &watchStdout
	watch.Stderr = &watchStderr
	if err := watch.Start(); err != nil {
		t.Fatalf("start generation-CAS assignment watch: %v", err)
	}
	watchStopped := false
	defer func() {
		if !watchStopped {
			_ = watch.Process.Signal(syscall.SIGTERM)
			_ = watch.Wait()
		}
	}()
	atomicAssignmentWaitForProcessOpenPath(t, watch.Process.Pid, lockPath, 10*time.Second)

	newKey := oldGeneration.IdempotencyKey + "-superseding-generation"
	newReceipt := oldGeneration.DispatchReceiptID + "-superseding-generation"
	newAssignedAt := time.Now().UTC().Add(time.Second)
	fixture.setLedgerAssignmentFields(t, beadID, map[string]any{
		"status":              "assigned",
		"assigned_at":         newAssignedAt,
		"started_at":          nil,
		"completed_at":        nil,
		"failed_at":           nil,
		"fail_reason":         "",
		"failure_reason":      "",
		"idempotency_key":     newKey,
		"claim_actor":         oldGeneration.AgentName + "/" + newKey,
		"dispatch_receipt_id": newReceipt,
		"last_dispatch_error": "",
		"dispatch_started_at": newAssignedAt,
		"dispatched_at":       newAssignedAt,
	})
	if err := syscall.Flock(int(lockFile.Fd()), syscall.LOCK_UN); err != nil {
		t.Fatalf("release completion generation lock: %v", err)
	}
	lockHeld = false
	// The watcher was already blocked in the stale generation's guarded
	// transition. Give that transition time to reload and reject generation B,
	// while remaining comfortably inside the two-second polling interval.
	time.Sleep(300 * time.Millisecond)

	if err := watch.Process.Signal(syscall.SIGTERM); err != nil {
		t.Fatalf("signal generation-CAS assignment watch: %v", err)
	}
	if err := watch.Wait(); err != nil {
		t.Fatalf("generation-CAS assignment watch did not exit cleanly: %v stdout=%s stderr=%s", err, watchStdout.String(), watchStderr.String())
	}
	watchStopped = true
	if ctx.Err() != nil {
		t.Fatalf("generation-CAS assignment watch exceeded deadline: %v", ctx.Err())
	}
	if len(bytes.TrimSpace(watchStderr.Bytes())) != 0 {
		t.Fatalf("generation-CAS assignment watch stderr=%s", watchStderr.String())
	}

	winner := fixture.readLedgerAssignment(t, beadID)
	if winner.IdempotencyKey != newKey || winner.DispatchReceiptID != newReceipt || winner.Status != "assigned" ||
		winner.CompletedAt != nil || !winner.AssignedAt.Equal(newAssignedAt) || winner.ClearState != "" ||
		winner.PendingTerminalStatus != "" || !reflect.DeepEqual(winner.ReservationIDs, oldGeneration.ReservationIDs) ||
		!reflect.DeepEqual(winner.ReservedPaths, oldGeneration.ReservedPaths) {
		t.Fatalf("stale completion transition corrupted superseding generation: old=%+v winner=%+v", oldGeneration, winner)
	}
	mailSnapshot := mail.snapshot()
	activeReservationIDs := make([]int, 0, len(mailSnapshot.Active))
	for _, reservation := range mailSnapshot.Active {
		if id, ok := reservation["id"].(int); ok {
			activeReservationIDs = append(activeReservationIDs, id)
		}
	}
	if mailSnapshot.ReleaseCalls != 0 || !reflect.DeepEqual(activeReservationIDs, oldGeneration.ReservationIDs) {
		t.Fatalf("stale completion released winner lease: record=%+v mail=%+v", winner, mailSnapshot)
	}
}

func TestE2EAtomicAssignmentObservationDeadlineContracts(t *testing.T) {
	CommonE2EPrerequisites(t)
	testutil.RequireTmuxThrottled(t)
	assertNoBRInvocations := func(t *testing.T, tracePath string) {
		t.Helper()
		// ubs:ignore -- tracePath is created under the fixture's t.TempDir.
		trace, readErr := os.ReadFile(tracePath)
		if os.IsNotExist(readErr) {
			return
		}
		if readErr != nil {
			t.Fatalf("read br invocation trace: %v", readErr)
		}
		if len(bytes.TrimSpace(trace)) != 0 {
			t.Fatalf("observation timeout reached br: %s", trace)
		}
	}
	assertFixturePathExists := func(t *testing.T, path, description string) {
		t.Helper()
		// ubs:ignore -- path is returned by a helper rooted in the fixture's t.TempDir.
		_, statErr := os.Stat(path)
		if statErr != nil {
			t.Fatalf("%s: %v", description, statErr)
		}
	}

	t.Run("capture_outlives_status_poll_budget_and_dispatches_once", func(t *testing.T) {
		fixture := newAtomicAssignmentCLIFixture(t)
		beadID := fixture.createBead(t, "Delayed assignment observation")
		prompt := fmt.Sprintf("NTM_ATOMIC_DELAYED_OBSERVATION_%d", time.Now().UnixNano())
		tmuxBinary, reached := fixture.armTMUXCaptureDelay(t, 2500*time.Millisecond)
		result := fixture.runNTM(t, map[string]string{"NTM_TMUX_BINARY": tmuxBinary},
			"assign", fixture.session,
			"--repo="+fixture.projectDir,
			"--pane="+fixture.panes[0].ID,
			"--beads="+beadID,
			"--prompt="+prompt,
			"--force",
			"--ignore-deps",
			"--reserve-files=false",
			"--timeout=8s",
			"--json",
		)
		if result.exitCode != 0 || len(bytes.TrimSpace(result.stderr)) != 0 {
			t.Fatalf("delayed observation assignment exit=%d stdout=%s stderr=%s", result.exitCode, result.stdout, result.stderr)
		}
		var envelope atomicAssignmentDirectEnvelope
		decodeAtomicAssignmentJSON(t, result.stdout, &envelope)
		if !envelope.Success || envelope.Error != nil || envelope.Data == nil || envelope.Data.Receipt == nil ||
			!envelope.Data.Assignment.PromptSent || !envelope.Data.Receipt.Transport.Sent || len(envelope.Data.Receipt.Transport.DeliveryID) == 0 {
			t.Fatalf("delayed observation assignment envelope = %+v", envelope)
		}
		assertFixturePathExists(t, reached, "delayed observation wrapper was not reached")
		fixture.waitForEndpointMarkerCount(t, fixture.panes[0], prompt, 1)
		fixture.assertMarkerCounts(t, prompt, map[int]int{0: 1, 1: 0})
		record := fixture.readLedgerAssignment(t, beadID)
		if strings.Compare(record.DispatchState, "sent") != 0 || len(record.DispatchReceiptID) == 0 ||
			strings.Compare(record.PromptSent, prompt) != 0 {
			t.Fatalf("delayed observation durable assignment = %+v", record)
		}
		fixture.assertBead(t, beadID, "in_progress", record.ClaimActor)
	})

	t.Run("direct_observation_cancellation_has_zero_side_effects", func(t *testing.T) {
		fixture := newAtomicAssignmentCLIFixture(t)
		mail := newAtomicAssignmentMailStub(fixture.projectDir)
		defer mail.close()
		beadID := fixture.createBead(t, "Timed assignment observation internal/cli/observation_timeout.go")
		prompt := fmt.Sprintf("NTM_ATOMIC_OBSERVATION_TIMEOUT_%d", time.Now().UnixNano())
		tmuxBinary, reached := fixture.armTMUXCaptureDelay(t, 30*time.Second)
		brPath, brTrace := fixture.armBRInvocationTrace(t)
		ledgerReplicas := []string{fixture.ledgerPath(), fixture.ledgerPath() + ".bak"}
		assertLedgerReplicasAbsent := func(stage string) {
			t.Helper()
			for _, path := range ledgerReplicas {
				// ubs:ignore -- path is a fixed replica beneath the fixture's isolated HOME.
				_, statErr := os.Stat(path)
				if statErr == nil {
					t.Fatalf("%s assignment ledger replica exists: %s", stage, path)
				}
				if !os.IsNotExist(statErr) {
					t.Fatalf("inspect %s assignment ledger replica %s: %v", stage, path, statErr)
				}
			}
		}
		assertLedgerReplicasAbsent("before timed observation")
		env := mail.env()
		env["NTM_TMUX_BINARY"] = tmuxBinary
		env["PATH"] = brPath
		result := fixture.runNTMInterrupted(t, env, func(pid int) {
			waitForAtomicAssignmentPath(t, reached, pid)
		},
			"assign", fixture.session,
			"--repo="+fixture.projectDir,
			"--pane="+fixture.panes[0].ID,
			"--beads="+beadID,
			"--prompt="+prompt,
			"--force",
			"--ignore-deps",
			"--reserve-files=true",
			"--timeout=30s",
			"--json",
		)
		assertAtomicAssignmentSingleTimeoutJSON(t, result)
		assertFixturePathExists(t, reached, "timed observation wrapper was not reached")
		assertNoBRInvocations(t, brTrace)
		assertLedgerReplicasAbsent("after timed observation")
		fixture.assertLedgerHasNoAssignment(t, beadID)
		fixture.assertBead(t, beadID, "open", "")
		fixture.assertMarkerCounts(t, prompt, map[int]int{0: 0, 1: 0})
		if snapshot := mail.snapshot(); snapshot.EnsureCalls != 0 || snapshot.ReserveCalls != 0 || snapshot.ListCalls != 0 ||
			snapshot.ReleaseCalls != 0 || snapshot.SendCalls != 0 || len(snapshot.Active) != 0 {
			t.Fatalf("timed observation touched Agent Mail: %+v", snapshot)
		}
	})

	t.Run("reassign_short_timeout_preserves_source_ownership", func(t *testing.T) {
		fixture := newAtomicAssignmentCLIFixture(t)
		mail := newAtomicAssignmentMailStub(fixture.projectDir)
		defer mail.close()
		beadID, sourcePrompt, before := seedReservedWorkingAssignment(
			t, fixture, mail, fixture.panes[0], "Timed reassign observation internal/cli/reassign_observation_timeout.go",
		)
		_, beforeLedger := readAtomicAssignmentLedgerAt(t, fixture.ledgerPath())
		_, beforeBackup := readAtomicAssignmentLedgerAt(t, fixture.ledgerPath()+".bak")
		beforeMail := mail.snapshot()
		prompt := fmt.Sprintf("NTM_ATOMIC_REASSIGN_OBSERVATION_TIMEOUT_%d", time.Now().UnixNano())
		tmuxBinary, reached := fixture.armTMUXCaptureDelay(t, 2500*time.Millisecond)
		brPath, brTrace := fixture.armBRInvocationTrace(t)
		env := mail.env()
		env["NTM_TMUX_BINARY"] = tmuxBinary
		env["PATH"] = brPath
		result := fixture.runNTM(t, env,
			"--json", "assign", fixture.session,
			"--repo="+fixture.projectDir,
			"--reassign="+beadID,
			"--to-pane="+fixture.panes[1].ID,
			"--prompt="+prompt,
			"--force",
			"--reserve-files=true",
			"--timeout=250ms",
		)
		assertAtomicAssignmentSingleTimeoutJSON(t, result)
		assertFixturePathExists(t, reached, "timed reassign observation wrapper was not reached")
		assertNoBRInvocations(t, brTrace)
		_, afterLedger := readAtomicAssignmentLedgerAt(t, fixture.ledgerPath())
		_, afterBackup := readAtomicAssignmentLedgerAt(t, fixture.ledgerPath()+".bak")
		if !bytes.Equal(afterLedger, beforeLedger) || !bytes.Equal(afterBackup, beforeBackup) {
			t.Fatalf("timed reassign observation mutated durable source: before=%+v after=%+v", before, fixture.readLedgerAssignment(t, beadID))
		}
		afterMail := mail.snapshot()
		if afterMail.EnsureCalls != beforeMail.EnsureCalls || afterMail.ReserveCalls != beforeMail.ReserveCalls ||
			afterMail.ListCalls != beforeMail.ListCalls || afterMail.ReleaseCalls != beforeMail.ReleaseCalls ||
			afterMail.SendCalls != beforeMail.SendCalls || len(afterMail.Active) != len(beforeMail.Active) ||
			len(afterMail.ReleasedIDs) != len(beforeMail.ReleasedIDs) || !bytes.Equal(afterMail.RawRequests, beforeMail.RawRequests) {
			t.Fatalf("timed reassign observation touched Agent Mail: before=%+v after=%+v", beforeMail, afterMail)
		}
		fixture.assertBead(t, beadID, "in_progress", before.ClaimActor)
		fixture.assertMarkerCounts(t, sourcePrompt, map[int]int{0: 1, 1: 0})
		fixture.assertMarkerCounts(t, prompt, map[int]int{0: 0, 1: 0})
	})
}

func TestE2EAtomicCoordinatorTogglePersistenceBuiltProcess(t *testing.T) {
	CommonE2EPrerequisites(t)
	ntmPath, err := ensureE2ENTMBin()
	if err != nil {
		t.Fatalf("resolve E2E ntm binary: %v", err)
	}
	root := t.TempDir()
	projectDir := filepath.Join(root, "project")
	homeDir := filepath.Join(root, "home")
	for _, dir := range []string{projectDir, homeDir, filepath.Join(root, "config"), filepath.Join(root, "data")} {
		if err := os.MkdirAll(dir, 0o700); err != nil {
			t.Fatalf("create toggle fixture directory %s: %v", dir, err)
		}
	}
	fixture := &atomicAssignmentCLIFixture{
		ntmPath:    ntmPath,
		root:       root,
		projectDir: projectDir,
		homeDir:    homeDir,
		env: atomicAssignmentIsolatedEnv(map[string]string{
			"HOME":              homeDir,
			"XDG_CONFIG_HOME":   filepath.Join(root, "config"),
			"XDG_DATA_HOME":     filepath.Join(root, "data"),
			"AGENT_MAIL_URL":    "http://127.0.0.1:1/mcp/",
			"AGENT_MAIL_TOKEN":  "",
			"HTTP_PROXY":        "",
			"HTTPS_PROXY":       "",
			"ALL_PROXY":         "",
			"NO_PROXY":          "127.0.0.1,localhost",
			"NO_COLOR":          "1",
			"TERM":              "xterm-256color",
			"NTM_OUTPUT_FORMAT": "",
			"NTM_ROBOT_FORMAT":  "",
		}),
	}
	configPath := filepath.Join(root, "selected-config.toml")
	original := strings.Join([]string{
		"# retained top-level comment",
		`[coordinator] # retained section comment`,
		`auto_assign = false # retained auto comment`,
		`send_digests = false # retained digest comment`,
		`conflict_notify = false`,
		`conflict_negotiate = true`,
		`poll_interval = "15s"`,
		"",
		"[assign]",
		`operator_gated_labels = ["security-review"]`,
		"",
	}, "\n")
	if err := os.WriteFile(configPath, []byte(original), 0o600); err != nil {
		t.Fatalf("write selected config: %v", err)
	}

	assertJSONToggle := func(result atomicAssignmentProcessResult, feature string, enabled bool, written map[string]string) {
		t.Helper()
		if result.exitCode != 0 || len(bytes.TrimSpace(result.stderr)) != 0 {
			t.Fatalf("toggle %s exit=%d stdout=%s stderr=%s", feature, result.exitCode, result.stdout, result.stderr)
		}
		var envelope struct {
			Feature    string            `json:"feature"`
			Enabled    bool              `json:"enabled"`
			Persisted  bool              `json:"persisted"`
			ConfigPath string            `json:"config_path"`
			Written    map[string]string `json:"written"`
		}
		decodeAtomicAssignmentJSON(t, result.stdout, &envelope)
		if envelope.Feature != feature || envelope.Enabled != enabled || !envelope.Persisted ||
			envelope.ConfigPath != configPath || !reflect.DeepEqual(envelope.Written, written) {
			t.Fatalf("toggle envelope=%+v, want feature=%s enabled=%t path=%s written=%v", envelope, feature, enabled, configPath, written)
		}
	}

	digest := fixture.runNTM(t, nil,
		"--config="+configPath, "--json", "coordinator", "enable", "digest", "--interval=30m",
	)
	assertJSONToggle(digest, "digest", true, map[string]string{
		"send_digests": "true", "digest_interval": `"30m"`,
	})
	disableConflict := fixture.runNTM(t, nil,
		"--config="+configPath, "--json", "coordinator", "disable", "conflict-negotiate",
	)
	assertJSONToggle(disableConflict, "conflict-negotiate", false, map[string]string{"conflict_negotiate": "false"})
	enableAuto := fixture.runNTM(t, nil,
		"--config="+configPath, "--json", "coordinator", "enable", "auto-assign",
	)
	assertJSONToggle(enableAuto, "auto-assign", true, map[string]string{"auto_assign": "true"})

	data, err := os.ReadFile(configPath)
	if err != nil {
		t.Fatalf("read toggled config: %v", err)
	}
	updated := string(data)
	for _, retained := range []string{
		"# retained top-level comment",
		`[coordinator] # retained section comment`,
		`auto_assign = true # retained auto comment`,
		`send_digests = true # retained digest comment`,
		`operator_gated_labels = ["security-review"]`,
	} {
		if !strings.Contains(updated, retained) {
			t.Fatalf("toggle lost %q:\n%s", retained, updated)
		}
	}
	loaded, err := config.Load(configPath)
	if err != nil {
		t.Fatalf("reload toggled config: %v", err)
	}
	if !loaded.Coordinator.AutoAssign || !loaded.Coordinator.SendDigests || loaded.Coordinator.DigestInterval != 30*time.Minute ||
		loaded.Coordinator.ConflictNegotiate || !reflect.DeepEqual(loaded.Assign.OperatorGatedLabels, []string{"security-review"}) {
		t.Fatalf("reloaded toggled config = coordinator:%+v assign:%+v", loaded.Coordinator, loaded.Assign)
	}

	beforeInvalid := append([]byte(nil), data...)
	for _, tc := range []struct {
		name           string
		args           []string
		wantDiagnostic string
	}{
		{name: "short digest interval", args: []string{"--config=" + configPath, "--json", "coordinator", "enable", "digest", "--interval=5s"}, wantDiagnostic: "must be at least"},
		{name: "unknown feature", args: []string{"--config=" + configPath, "--json", "coordinator", "enable", "not-a-feature"}, wantDiagnostic: "unknown feature"},
		{name: "interval on non-digest feature", args: []string{"--config=" + configPath, "--json", "coordinator", "enable", "auto-assign", "--interval=30m"}, wantDiagnostic: "only valid"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			result := fixture.runNTM(t, nil, tc.args...)
			code, message := assertAtomicAssignmentSingleFailureJSON(t, result)
			if code != "INVALID_FLAG" || !strings.Contains(message, tc.wantDiagnostic) {
				t.Fatalf("invalid toggle code=%q message=%q, want INVALID_FLAG containing %q", code, message, tc.wantDiagnostic)
			}
			after, readErr := os.ReadFile(configPath)
			if readErr != nil {
				t.Fatalf("read config after invalid toggle: %v", readErr)
			}
			if !bytes.Equal(after, beforeInvalid) {
				t.Fatalf("invalid toggle mutated config:\nbefore=%s\nafter=%s", beforeInvalid, after)
			}
		})
	}

	staleHealth := []byte("[health]\nenabled = true\n")
	if err := os.WriteFile(configPath, staleHealth, 0o600); err != nil {
		t.Fatalf("write stale health config: %v", err)
	}
	rejected := fixture.runNTM(t, nil,
		"--config="+configPath, "--json", "coordinator", "enable", "auto-assign",
	)
	if rejected.exitCode == 0 || !bytes.Contains(append(append([]byte(nil), rejected.stdout...), rejected.stderr...), []byte("health")) {
		t.Fatalf("stale-health toggle exit=%d stdout=%s stderr=%s", rejected.exitCode, rejected.stdout, rejected.stderr)
	}
	afterRejected, err := os.ReadFile(configPath)
	if err != nil {
		t.Fatalf("read rejected config: %v", err)
	}
	if !bytes.Equal(afterRejected, staleHealth) {
		t.Fatalf("stale-health rejection mutated config:\nbefore=%s\nafter=%s", staleHealth, afterRejected)
	}
}

func TestE2EAtomicAssignmentInvalidConfigHasZeroSideEffectsBuiltProcess(t *testing.T) {
	CommonE2EPrerequisites(t)
	testutil.RequireTmuxThrottled(t)

	fixture := newAtomicAssignmentCLIFixture(t)
	binDir := filepath.Join(fixture.root, "invalid-config-bin")
	if err := os.MkdirAll(binDir, 0o700); err != nil {
		t.Fatalf("create invalid-config bin directory: %v", err)
	}
	bvScript := strings.Join([]string{
		"#!/bin/sh",
		`printf '%s\n' "$*" >> "$NTM_E2E_BV_TRACE"`,
		`case "$1" in`,
		`  --robot-triage) cat "$NTM_E2E_TRIAGE_PAYLOAD" ;;`,
		`  --robot-plan) cat "$NTM_E2E_PLAN_PAYLOAD" ;;`,
		`  *) printf 'unexpected bv args: %s\n' "$*" >&2; exit 64 ;;`,
		"esac",
		"",
	}, "\n")
	if err := os.WriteFile(filepath.Join(binDir, "bv"), []byte(bvScript), 0o700); err != nil {
		t.Fatalf("write invalid-config bv fixture: %v", err)
	}
	brScript := strings.Join([]string{
		"#!/bin/sh",
		`printf '%s\n' "$*" >> "$NTM_E2E_BR_TRACE"`,
		"real_br=" + tmux.ShellQuote(fixture.brPath),
		`exec "$real_br" "$@"`,
		"",
	}, "\n")
	if err := os.WriteFile(filepath.Join(binDir, "br"), []byte(brScript), 0o700); err != nil {
		t.Fatalf("write invalid-config br fixture: %v", err)
	}
	triagePath := filepath.Join(fixture.root, "invalid-config-triage.json")
	if err := os.WriteFile(triagePath, []byte(`{"triage":{"recommendations":[]}}`), 0o600); err != nil {
		t.Fatalf("write invalid-config triage payload: %v", err)
	}
	assertTraceEmpty := func(t *testing.T, path, surface string) {
		t.Helper()
		data, err := os.ReadFile(path)
		if os.IsNotExist(err) {
			return
		}
		if err != nil {
			t.Fatalf("read %s trace: %v", surface, err)
		}
		if len(bytes.TrimSpace(data)) != 0 {
			t.Fatalf("invalid config reached %s: %s", surface, data)
		}
	}

	fixture.primePaneForSafeDispatch(t, 0)
	time.Sleep(5500 * time.Millisecond)
	for index, tc := range []struct {
		name          string
		projectConfig bool
		missingConfig bool
		envConfig     bool
		watchDryRun   bool
	}{
		{name: "invalid selected global config"},
		{name: "missing selected global config", missingConfig: true},
		{name: "missing NTM_CONFIG global config", missingConfig: true, envConfig: true},
		{name: "invalid target project config", projectConfig: true},
		{name: "invalid target project config in watch dry-run", projectConfig: true, watchDryRun: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			targetDir := filepath.Join(fixture.root, fmt.Sprintf("invalid-config-project-%d", index))
			if err := os.MkdirAll(targetDir, 0o700); err != nil {
				t.Fatalf("create invalid-config target project: %v", err)
			}
			fixture.mustBRAt(t, targetDir, "init", "--prefix="+fmt.Sprintf("icfg%d", index), "--json")
			beadID := strings.TrimSpace(string(fixture.mustBRAt(t, targetDir,
				"create", "Invalid config must stop automatic assignment", "--type=task", "--priority=1", "--silent")))
			if beadID == "" || strings.ContainsAny(beadID, " \t\r\n") {
				t.Fatalf("unexpected invalid-config bead ID %q", beadID)
			}
			fixture.assertBeadAt(t, targetDir, beadID, "open", "")
			beforeBead := fixture.mustBRAt(t, targetDir, "show", beadID, "--json")

			planPath := filepath.Join(fixture.root, fmt.Sprintf("invalid-config-plan-%d.json", index))
			planPayload, err := json.Marshal(map[string]any{
				"plan": map[string]any{
					"tracks": []map[string]any{{
						"track_id": "invalid-config-track",
						"items": []map[string]any{{
							"id": beadID, "title": "Invalid config must stop automatic assignment", "status": "open", "priority": 1,
						}},
					}},
				},
			})
			if err != nil {
				t.Fatalf("marshal invalid-config plan payload: %v", err)
			}
			if err := os.WriteFile(planPath, planPayload, 0o600); err != nil {
				t.Fatalf("write invalid-config plan payload: %v", err)
			}

			configPath := filepath.Join(fixture.root, fmt.Sprintf("invalid-selected-%d.toml", index))
			if tc.projectConfig {
				configDir := filepath.Join(targetDir, ".ntm")
				if err := os.MkdirAll(configDir, 0o700); err != nil {
					t.Fatalf("create invalid project config directory: %v", err)
				}
				configPath = filepath.Join(configDir, "config.toml")
			}
			invalidConfig := []byte("[assign\noperator_gated_labels = [\"release-approval\"]\n")
			if !tc.missingConfig {
				if err := os.WriteFile(configPath, invalidConfig, 0o600); err != nil {
					t.Fatalf("write invalid config: %v", err)
				}
			}
			outsideDir := filepath.Join(fixture.root, fmt.Sprintf("invalid-config-outside-%d", index))
			if err := os.MkdirAll(outsideDir, 0o700); err != nil {
				t.Fatalf("create invalid-config outside directory: %v", err)
			}
			marker := fmt.Sprintf("NTM_INVALID_CONFIG_MUST_NOT_DISPATCH_%d_%d", index, time.Now().UnixNano())
			templatePath := filepath.Join(targetDir, "invalid-config-template.txt")
			if err := os.WriteFile(templatePath, []byte(marker+"::{BEAD_ID}::{TITLE}"), 0o600); err != nil {
				t.Fatalf("write invalid-config assignment template: %v", err)
			}
			bvTrace := filepath.Join(fixture.root, fmt.Sprintf("invalid-config-bv-%d.trace", index))
			brTrace := filepath.Join(fixture.root, fmt.Sprintf("invalid-config-br-%d.trace", index))
			mail := newAtomicAssignmentMailStub(targetDir)
			defer mail.close()
			env := mail.env()
			env["PATH"] = binDir + string(os.PathListSeparator) + atomicAssignmentEnvValue(fixture.env, "PATH")
			env["NTM_E2E_TRIAGE_PAYLOAD"] = triagePath
			env["NTM_E2E_PLAN_PAYLOAD"] = planPath
			env["NTM_E2E_BV_TRACE"] = bvTrace
			env["NTM_E2E_BR_TRACE"] = brTrace
			if tc.envConfig {
				env["NTM_CONFIG"] = configPath
			}
			args := make([]string, 0, 13)
			if !tc.projectConfig && !tc.envConfig {
				args = append(args, "--config="+configPath)
			}
			args = append(args,
				"--json", "assign", fixture.session,
				"--repo="+targetDir,
			)
			if tc.watchDryRun {
				args = append(args, "--watch", "--dry-run", "--stop-when-done")
			} else {
				args = append(args, "--auto")
			}
			args = append(args,
				"--limit=1",
				"--cod-only",
				"--template=custom",
				"--template-file="+templatePath,
				"--reserve-files=true",
				"--timeout=15s",
				"--quiet",
			)
			result := fixture.runNTMInDir(t, outsideDir, env, args...)
			code, message := assertAtomicAssignmentSingleFailureJSON(t, result)
			if code != "INVALID_FLAG" || !strings.Contains(strings.ToLower(message), "config") {
				t.Fatalf("invalid config failure code=%q message=%q, want INVALID_FLAG with a config diagnostic", code, message)
			}
			assertTraceEmpty(t, bvTrace, "bv planning")
			assertTraceEmpty(t, brTrace, "Beads")
			if tc.missingConfig {
				if _, err := os.Stat(configPath); !os.IsNotExist(err) {
					t.Fatalf("missing explicit config was created during rejection: %v", err)
				}
			} else {
				afterConfig, err := os.ReadFile(configPath)
				if err != nil {
					t.Fatalf("read invalid config after rejection: %v", err)
				}
				if !bytes.Equal(afterConfig, invalidConfig) {
					t.Fatalf("invalid config changed during rejection:\nbefore=%s\nafter=%s", invalidConfig, afterConfig)
				}
			}
			afterBead := fixture.mustBRAt(t, targetDir, "show", beadID, "--json")
			if !bytes.Equal(bytes.TrimSpace(afterBead), bytes.TrimSpace(beforeBead)) {
				t.Fatalf("invalid config mutated Beads state:\nbefore=%s\nafter=%s", beforeBead, afterBead)
			}
			fixture.assertBeadAt(t, targetDir, beadID, "open", "")
			fixture.assertLedgerReplicasHaveNoAssignment(t, beadID)
			fixture.assertMarkerCounts(t, marker, map[int]int{0: 0, 1: 0})
			assertAtomicAssignmentMailUntouched(t, mail.snapshot())
		})
	}
}

func TestE2EAtomicCoordinatorRunOnceBuiltProcess(t *testing.T) {
	CommonE2EPrerequisites(t)
	testutil.RequireTmuxThrottled(t)

	fixture := newAtomicAssignmentCLIFixture(t)
	beadID := fixture.createBead(t, "Coordinator once delivery")
	for pane := range fixture.panes {
		fixture.primePaneForSafeDispatch(t, pane)
	}
	time.Sleep(5500 * time.Millisecond)

	for key, value := range map[string]string{
		"HOME":            fixture.homeDir,
		"XDG_CONFIG_HOME": filepath.Join(fixture.root, "config"),
		"XDG_DATA_HOME":   filepath.Join(fixture.root, "data"),
	} {
		t.Setenv(key, value)
	}
	registry := agentmail.NewSessionAgentRegistry(fixture.session, fixture.projectDir)
	for pane, endpoint := range fixture.panes {
		registry.AddAgent(endpoint.Title, endpoint.ID, fmt.Sprintf("CoordinatorAgent%d", pane))
	}
	if err := agentmail.SaveSessionAgentRegistry(registry); err != nil {
		t.Fatalf("save coordinator Agent Mail registry: %v", err)
	}
	configPath := filepath.Join(fixture.root, "config", "ntm", "config.toml")
	if err := os.MkdirAll(filepath.Dir(configPath), 0o700); err != nil {
		t.Fatalf("create coordinator config directory: %v", err)
	}
	coordinatorConfig := strings.Join([]string{
		"[coordinator]",
		"auto_assign = true",
		"assign_only_idle = true",
		"send_digests = false",
		"poll_interval = \"100ms\"",
		"digest_interval = \"1h\"",
		"idle_threshold = 0",
		"",
		"[assign]",
		`operator_gated_labels = ["release-approval-required"]`,
		"",
	}, "\n")
	if err := os.WriteFile(configPath, []byte(coordinatorConfig), 0o600); err != nil {
		t.Fatalf("write coordinator config: %v", err)
	}

	binDir := filepath.Join(fixture.root, "coordinator-bin")
	if err := os.MkdirAll(binDir, 0o700); err != nil {
		t.Fatalf("create coordinator bin directory: %v", err)
	}
	bvPayloadDir := filepath.Join(fixture.root, "coordinator-bv-payloads")
	if err := os.MkdirAll(bvPayloadDir, 0o700); err != nil {
		t.Fatalf("create coordinator bv payload directory: %v", err)
	}
	bvCurrentBeadPath := filepath.Join(bvPayloadDir, "current-bead")
	bvEmptyPlanPath := filepath.Join(bvPayloadDir, "empty-plan.json")
	emptyPlan := []byte(`{"generated_at":"2026-07-12T00:00:00Z","plan":{"tracks":[]}}`)
	if err := util.AtomicWriteFile(bvEmptyPlanPath, emptyPlan, 0o600); err != nil {
		t.Fatalf("write empty bv plan payload: %v", err)
	}
	writeBVPayload := func(id, title string) {
		t.Helper()
		triagePayload := fmt.Sprintf(`{"generated_at":"2026-07-12T00:00:00Z","data_hash":"e2e","triage":{"meta":{"version":"1","generated_at":"2026-07-12T00:00:00Z","phase2_ready":true,"issue_count":1},"quick_ref":{"open_count":1,"actionable_count":1,"blocked_count":0,"in_progress_count":0,"top_picks":[]},"recommendations":[{"id":%q,"title":%q,"type":"task","status":"open","priority":1,"score":1,"action":"assign","reasons":[]}]}}`, id, title)
		planPayload := fmt.Sprintf(`{"generated_at":"2026-07-12T00:00:00Z","plan":{"tracks":[{"track_id":"coordinator-e2e","items":[{"id":%q,"title":%q,"status":"open","priority":1}]}]}}`, id, title)
		if err := util.AtomicWriteFile(filepath.Join(bvPayloadDir, id+".triage.json"), []byte(triagePayload), 0o600); err != nil {
			t.Fatalf("write bv triage payload: %v", err)
		}
		if err := util.AtomicWriteFile(filepath.Join(bvPayloadDir, id+".plan.json"), []byte(planPayload), 0o600); err != nil {
			t.Fatalf("write bv plan payload: %v", err)
		}
		// Publish the new ID last so a running coordinator never observes an ID
		// before both immutable payloads for that generation are complete.
		if err := util.AtomicWriteFile(bvCurrentBeadPath, []byte(id+"\n"), 0o600); err != nil {
			t.Fatalf("write current bv bead: %v", err)
		}
	}
	writeBVPayload(beadID, "Coordinator once delivery")
	bvScript := `#!/bin/sh
set -eu

if [ "$#" -ne 1 ]; then
  printf 'unexpected bv args: %s\n' "$*" >&2
  exit 64
fi

bead_id=$(cat "$NTM_E2E_BV_CURRENT_BEAD")
case "$1" in
  --robot-triage)
    cat "$NTM_E2E_BV_PAYLOAD_DIR/$bead_id.triage.json"
    ;;
  --robot-plan)
    if "$NTM_E2E_REAL_BR" show "$bead_id" --json 2>/dev/null |
      grep -Eq '"status"[[:space:]]*:[[:space:]]*"open"'; then
      cat "$NTM_E2E_BV_PAYLOAD_DIR/$bead_id.plan.json"
    else
      cat "$NTM_E2E_BV_EMPTY_PLAN"
    fi
    ;;
  *)
    printf 'unexpected bv args: %s\n' "$*" >&2
    exit 64
    ;;
esac
`
	if err := os.WriteFile(filepath.Join(binDir, "bv"), []byte(bvScript), 0o700); err != nil {
		t.Fatalf("write bv fixture: %v", err)
	}

	var sendCount atomic.Int32
	var reserveCount atomic.Int32
	var releaseCount atomic.Int32
	var reservationMu sync.Mutex
	var releasedReservationIDs []int
	var activeCoordinatorReservations []map[string]any
	var rawMailRequests bytes.Buffer
	var dropReservationResponse atomic.Bool
	var invalidReceipt atomic.Bool
	mailServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodGet && r.URL.Path == "/health/liveness" {
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"status":"ok"}`))
			return
		}
		var request struct {
			ID     any    `json:"id"`
			Method string `json:"method"`
			Params struct {
				Name      string         `json:"name"`
				Arguments map[string]any `json:"arguments"`
			} `json:"params"`
		}
		body, err := io.ReadAll(r.Body)
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		reservationMu.Lock()
		rawMailRequests.Write(body)
		rawMailRequests.WriteByte('\n')
		reservationMu.Unlock()
		if err := json.Unmarshal(body, &request); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		if request.Method == "tools/call" && request.Params.Name == "health_check" {
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(map[string]any{
				"jsonrpc": "2.0", "id": request.ID,
				"result": map[string]any{"status": "ok", "timestamp": time.Now().UTC().Format(time.RFC3339Nano)},
			})
			return
		}
		if request.Method != "tools/call" {
			http.Error(w, "unexpected Agent Mail method", http.StatusNotFound)
			return
		}
		writeResult := func(result any) {
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(map[string]any{"jsonrpc": "2.0", "id": request.ID, "result": result})
		}
		switch request.Params.Name {
		case "ensure_project":
			if request.Params.Arguments["human_key"] != fixture.projectDir {
				http.Error(w, fmt.Sprintf("unexpected coordinator ensure project args: %#v", request.Params.Arguments), http.StatusBadRequest)
				return
			}
			writeResult(map[string]any{"id": 1, "slug": "atomic-e2e", "human_key": fixture.projectDir, "created_at": time.Now().UTC().Format(time.RFC3339Nano)})
		case "send_message":
			recipients, _ := request.Params.Arguments["to"].([]any)
			if len(recipients) != 1 {
				http.Error(w, "expected one recipient", http.StatusBadRequest)
				return
			}
			if request.Method == "resources/read" {
				w.Header().Set("Content-Type", "application/json")
				_ = json.NewEncoder(w).Encode(map[string]any{
					"jsonrpc": "2.0", "id": request.ID,
					"error": map[string]any{"code": -32601, "message": "resource view not supported"},
				})
				return
			}
			recipient, _ := recipients[0].(string)
			sender, _ := request.Params.Arguments["sender_name"].(string)
			subject, _ := request.Params.Arguments["subject"].(string)
			body, _ := request.Params.Arguments["body_md"].(string)
			messageID := int(sendCount.Add(1)) + 7000
			if invalidReceipt.Load() {
				writeResult(map[string]any{"count": 0, "deliveries": []any{}})
				return
			}
			writeResult(map[string]any{
				"count": 1,
				"deliveries": []map[string]any{{
					"project": fixture.projectDir,
					"payload": map[string]any{
						"id": messageID, "project_id": 1, "sender_id": 1,
						"from": sender, "to": []string{recipient},
						"subject": subject, "body_md": body,
						"importance": "normal", "ack_required": true,
					},
				}},
			})
		case "file_reservation_paths":
			paths, ok := anyStringSlice(request.Params.Arguments["paths"])
			agentName, _ := request.Params.Arguments["agent_name"].(string)
			reason, _ := request.Params.Arguments["reason"].(string)
			if !ok || len(paths) == 0 || agentName == "" || request.Params.Arguments["project_key"] != fixture.projectDir {
				http.Error(w, fmt.Sprintf("unexpected coordinator reservation args: %#v", request.Params.Arguments), http.StatusBadRequest)
				return
			}
			baseID := int(reserveCount.Add(1))*100 + 8000
			now := time.Now().UTC()
			granted := make([]map[string]any, 0, len(paths))
			for index, path := range paths {
				granted = append(granted, map[string]any{
					"id": baseID + index, "path_pattern": path, "agent_name": agentName,
					"project_id": 1, "exclusive": true, "reason": reason,
					"created_ts": now.Format(time.RFC3339Nano), "expires_ts": now.Add(time.Hour).Format(time.RFC3339Nano),
				})
			}
			reservationMu.Lock()
			activeCoordinatorReservations = append(activeCoordinatorReservations, granted...)
			reservationMu.Unlock()
			if dropReservationResponse.Swap(false) {
				panic(http.ErrAbortHandler)
			}
			writeResult(map[string]any{"granted": granted, "conflicts": []any{}})
		case "list_file_reservations":
			reservationMu.Lock()
			active := append([]map[string]any(nil), activeCoordinatorReservations...)
			reservationMu.Unlock()
			writeResult(active)
		case "release_file_reservations":
			ids, ok := atomicAssignmentAnyIntSlice(request.Params.Arguments["file_reservation_ids"])
			if !ok || len(ids) == 0 || request.Params.Arguments["project_key"] != fixture.projectDir {
				http.Error(w, fmt.Sprintf("unexpected coordinator release args: %#v", request.Params.Arguments), http.StatusBadRequest)
				return
			}
			reservationMu.Lock()
			releasedReservationIDs = append(releasedReservationIDs, ids...)
			wanted := make(map[int]struct{}, len(ids))
			for _, id := range ids {
				wanted[id] = struct{}{}
			}
			remaining := activeCoordinatorReservations[:0]
			for _, reservation := range activeCoordinatorReservations {
				id, _ := reservation["id"].(int)
				if _, released := wanted[id]; !released {
					remaining = append(remaining, reservation)
				}
			}
			activeCoordinatorReservations = remaining
			reservationMu.Unlock()
			releaseCount.Add(1)
			writeResult(map[string]any{"released": len(ids)})
		default:
			http.Error(w, "unexpected Agent Mail tool "+request.Params.Name, http.StatusNotFound)
		}
	}))
	defer mailServer.Close()

	env := map[string]string{
		"PATH":                    binDir + string(os.PathListSeparator) + atomicAssignmentEnvValue(fixture.env, "PATH"),
		"NTM_E2E_BV_PAYLOAD_DIR":  bvPayloadDir,
		"NTM_E2E_BV_CURRENT_BEAD": bvCurrentBeadPath,
		"NTM_E2E_BV_EMPTY_PLAN":   bvEmptyPlanPath,
		"NTM_E2E_REAL_BR":         fixture.brPath,
		"AGENT_MAIL_URL":          mailServer.URL + "/",
	}
	runOnce := func() (atomicAssignmentProcessResult, struct {
		Success     bool   `json:"success"`
		Once        bool   `json:"once"`
		AutoAssign  bool   `json:"auto_assign"`
		ErrorCode   string `json:"error_code"`
		Error       string `json:"error"`
		Assignments []struct {
			Success        bool   `json:"success"`
			MessageSent    bool   `json:"message_sent"`
			IdempotencyKey string `json:"idempotency_key"`
		} `json:"assignments"`
	}) {
		t.Helper()
		result := fixture.runNTM(t, env, "--json", "coordinator", "run", fixture.session, "--once")
		var envelope struct {
			Success     bool   `json:"success"`
			Once        bool   `json:"once"`
			AutoAssign  bool   `json:"auto_assign"`
			ErrorCode   string `json:"error_code"`
			Error       string `json:"error"`
			Assignments []struct {
				Success        bool   `json:"success"`
				MessageSent    bool   `json:"message_sent"`
				IdempotencyKey string `json:"idempotency_key"`
			} `json:"assignments"`
		}
		decodeAtomicAssignmentJSON(t, result.stdout, &envelope)
		return result, envelope
	}

	firstResult, first := runOnce()
	if firstResult.exitCode != 0 || len(bytes.TrimSpace(firstResult.stderr)) != 0 || !first.Success || !first.Once || !first.AutoAssign ||
		len(first.Assignments) != 1 || !first.Assignments[0].Success || !first.Assignments[0].MessageSent || first.Assignments[0].IdempotencyKey == "" {
		t.Fatalf("first coordinator cycle result=%+v envelope=%+v", firstResult, first)
	}
	if sendCount.Load() != 1 {
		t.Fatalf("first coordinator cycle deliveries=%d, want 1", sendCount.Load())
	}
	firstRecord := fixture.readLedgerAssignment(t, beadID)
	if firstRecord.DispatchState != "sent" || firstRecord.DispatchReceiptID != "7001" || firstRecord.IdempotencyKey != first.Assignments[0].IdempotencyKey {
		t.Fatalf("coordinator durable receipt = %+v", firstRecord)
	}
	fixture.assertBead(t, beadID, "in_progress", firstRecord.ClaimActor)

	secondResult, second := runOnce()
	if secondResult.exitCode != 0 || len(bytes.TrimSpace(secondResult.stderr)) != 0 || !second.Success || !second.Once || !second.AutoAssign || len(second.Assignments) != 0 {
		t.Fatalf("second coordinator cycle result=%+v envelope=%+v", secondResult, second)
	}
	if sendCount.Load() != 1 {
		t.Fatalf("coordinator restart deliveries=%d, want exactly 1", sendCount.Load())
	}
	assertAtomicAssignmentReceiptUnchanged(t, firstRecord, fixture.readLedgerAssignment(t, beadID))

	// BV is a planning snapshot, not assignment authority. Prove that the
	// coordinator refreshes the recommendation through live `br show` and
	// honors an operator gate added after the snapshot was produced.
	gatedBeadID := fixture.createBead(t, "Coordinator configured global operator gate")
	fixture.mustBR(t, "update", gatedBeadID, "--add-label=release-approval-required", "--json")
	writeBVPayload(gatedBeadID, "Coordinator configured global operator gate")
	sendsBeforeGate := sendCount.Load()
	reservesBeforeGate := reserveCount.Load()
	gatedResult, gated := runOnce()
	if gatedResult.exitCode != 0 || len(bytes.TrimSpace(gatedResult.stderr)) != 0 || !gated.Success ||
		!gated.Once || !gated.AutoAssign || len(gated.Assignments) != 0 {
		t.Fatalf("live semantic gate result=%+v envelope=%+v", gatedResult, gated)
	}
	if sendCount.Load() != sendsBeforeGate || reserveCount.Load() != reservesBeforeGate {
		t.Fatalf("live semantic gate produced side effects: sends=%d/%d reserves=%d/%d", sendCount.Load(), sendsBeforeGate, reserveCount.Load(), reservesBeforeGate)
	}
	fixture.assertBead(t, gatedBeadID, "open", "")
	fixture.assertLedgerHasNoAssignment(t, gatedBeadID)

	projectConfigDir := filepath.Join(fixture.projectDir, ".ntm")
	if err := os.MkdirAll(projectConfigDir, 0o700); err != nil {
		t.Fatalf("create project config directory: %v", err)
	}
	projectConfig := "[assign]\noperator_gated_labels = [\"project-change-approval\"]\n"
	if err := os.WriteFile(filepath.Join(projectConfigDir, "config.toml"), []byte(projectConfig), 0o600); err != nil {
		t.Fatalf("write project operator-gate config: %v", err)
	}
	projectGatedID := fixture.createBead(t, "Coordinator configured project operator gate")
	fixture.mustBR(t, "update", projectGatedID, "--add-label=project-change-approval", "--json")
	writeBVPayload(projectGatedID, "Coordinator configured project operator gate")
	sendsBeforeProjectGate := sendCount.Load()
	reservesBeforeProjectGate := reserveCount.Load()
	projectGatedResult, projectGated := runOnce()
	if projectGatedResult.exitCode != 0 || len(bytes.TrimSpace(projectGatedResult.stderr)) != 0 || !projectGated.Success ||
		!projectGated.Once || !projectGated.AutoAssign || len(projectGated.Assignments) != 0 {
		t.Fatalf("project semantic gate result=%+v envelope=%+v", projectGatedResult, projectGated)
	}
	if sendCount.Load() != sendsBeforeProjectGate || reserveCount.Load() != reservesBeforeProjectGate {
		t.Fatalf("project semantic gate produced side effects: sends=%d/%d reserves=%d/%d", sendCount.Load(), sendsBeforeProjectGate, reserveCount.Load(), reservesBeforeProjectGate)
	}
	fixture.assertBead(t, projectGatedID, "open", "")
	fixture.assertLedgerHasNoAssignment(t, projectGatedID)

	recoveryTitle := "Coordinator reserve recovery internal/coordinator/recovery.go"
	recoveryBeadID := fixture.createBead(t, recoveryTitle)
	writeBVPayload(recoveryBeadID, recoveryTitle)
	dropReservationResponse.Store(true)
	recoveryFailureResult, recoveryFailure := runOnce()
	if recoveryFailureResult.exitCode != 1 || len(bytes.TrimSpace(recoveryFailureResult.stderr)) != 0 || recoveryFailure.Success ||
		recoveryFailure.ErrorCode != "ASSIGNMENT_FAILED" || len(recoveryFailure.Assignments) != 1 || recoveryFailure.Assignments[0].Success {
		t.Fatalf("coordinator reservation-loss cycle result=%+v envelope=%+v", recoveryFailureResult, recoveryFailure)
	}
	recoveryPending := fixture.readLedgerAssignment(t, recoveryBeadID)
	if recoveryPending.ReservationState != "unknown" || recoveryPending.ReservationAttempts != 1 ||
		recoveryPending.DispatchAttempts != 0 || recoveryPending.IdempotencyKey == "" {
		t.Fatalf("coordinator reservation-loss ledger = %+v", recoveryPending)
	}
	reserveCallsBeforeRecovery := reserveCount.Load()
	recoveryResult, recoveredEnvelope := runOnce()
	if recoveryResult.exitCode != 0 || len(bytes.TrimSpace(recoveryResult.stderr)) != 0 || !recoveredEnvelope.Success ||
		len(recoveredEnvelope.Assignments) != 1 || !recoveredEnvelope.Assignments[0].Success || !recoveredEnvelope.Assignments[0].MessageSent ||
		recoveredEnvelope.Assignments[0].IdempotencyKey != recoveryPending.IdempotencyKey {
		t.Fatalf("coordinator reservation recovery result=%+v envelope=%+v", recoveryResult, recoveredEnvelope)
	}
	if reserveCount.Load() != reserveCallsBeforeRecovery || sendCount.Load() != 2 {
		t.Fatalf("coordinator reservation recovery reserve=%d/%d send=%d, want no repeat and 2 sends", reserveCallsBeforeRecovery, reserveCount.Load(), sendCount.Load())
	}
	recoveryRecord := fixture.readLedgerAssignment(t, recoveryBeadID)
	if recoveryRecord.ReservationState != "reserved" || !recoveryRecord.ReservationCompleted || len(recoveryRecord.ReservationIDs) != 1 ||
		recoveryRecord.DispatchState != "sent" || recoveryRecord.DispatchReceiptID == "" {
		t.Fatalf("coordinator recovered reservation ledger = %+v", recoveryRecord)
	}
	fixture.mustBR(t, "close", recoveryBeadID, "--reason=coordinator-recovery-e2e", "--json")
	writeBVPayload(beadID, "Coordinator once delivery")
	closedRecoveryResult, closedRecovery := runOnce()
	if closedRecoveryResult.exitCode != 0 || len(bytes.TrimSpace(closedRecoveryResult.stderr)) != 0 || !closedRecovery.Success || len(closedRecovery.Assignments) != 0 {
		t.Fatalf("coordinator recovered lease cleanup result=%+v envelope=%+v", closedRecoveryResult, closedRecovery)
	}
	closedRecoveryRecord := fixture.readLedgerAssignment(t, recoveryBeadID)
	if closedRecoveryRecord.Status != "completed" || closedRecoveryRecord.ReservationState != "released" ||
		len(closedRecoveryRecord.ReservationIDs) != 0 || releaseCount.Load() != 1 {
		t.Fatalf("coordinator recovered lease cleanup ledger=%+v releases=%d", closedRecoveryRecord, releaseCount.Load())
	}

	pollingTitle := "Coordinator polling internal/coordinator/e2e_reserved.go"
	pollingBeadID := fixture.createBead(t, pollingTitle)
	writeBVPayload(pollingBeadID, pollingTitle)
	const coordinatorTransitionTimeout = 30 * time.Second
	ctx, cancel := context.WithTimeout(context.Background(), 6*coordinatorTransitionTimeout)
	defer cancel()
	longRun := exec.CommandContext(ctx, fixture.ntmPath, "--json", "coordinator", "run", fixture.session)
	longRun.Dir = fixture.projectDir
	longRun.Env = atomicAssignmentMergeEnv(fixture.env, env)
	stdout, err := longRun.StdoutPipe()
	if err != nil {
		t.Fatalf("coordinator stdout pipe: %v", err)
	}
	var longRunStderr bytes.Buffer
	longRun.Stderr = &longRunStderr
	if err := longRun.Start(); err != nil {
		t.Fatalf("start long-running coordinator: %v", err)
	}
	startupLine := make(chan struct {
		line []byte
		err  error
	}, 1)
	go func() {
		line, readErr := bufio.NewReader(stdout).ReadBytes('\n')
		startupLine <- struct {
			line []byte
			err  error
		}{line: line, err: readErr}
	}()
	var startup struct {
		Success    bool `json:"success"`
		Once       bool `json:"once"`
		AutoAssign bool `json:"auto_assign"`
	}
	select {
	case received := <-startupLine:
		if received.err != nil {
			t.Fatalf("read coordinator startup envelope: %v stderr=%s", received.err, longRunStderr.String())
		}
		decodeAtomicAssignmentJSON(t, received.line, &startup)
	case <-time.After(coordinatorTransitionTimeout):
		t.Fatal("timed out waiting for coordinator startup envelope")
	}
	if !startup.Success || startup.Once || !startup.AutoAssign {
		t.Fatalf("long-running coordinator startup = %+v", startup)
	}
	waitForRecord := func(id string, predicate func(*atomicAssignmentRecord) bool, description string) *atomicAssignmentRecord {
		t.Helper()
		deadline := time.Now().Add(coordinatorTransitionTimeout)
		for {
			ledger, readErr := fixture.readLedger()
			if readErr == nil {
				if record := ledger.Assignments[id]; record != nil && predicate(record) {
					return record
				}
			}
			if time.Now().After(deadline) {
				t.Fatalf("timed out waiting for %s for %s: read_err=%v stderr=%s", description, id, readErr, longRunStderr.String())
			}
			time.Sleep(50 * time.Millisecond)
		}
	}
	pollingRecord := waitForRecord(pollingBeadID, func(record *atomicAssignmentRecord) bool {
		return record.DispatchState == "sent" && record.DispatchReceiptID != ""
	}, "ticker-driven durable dispatch")
	if sendCount.Load() != 3 {
		t.Fatalf("ticker-driven coordinator deliveries=%d, want 3 total", sendCount.Load())
	}
	fixture.assertBead(t, pollingBeadID, "in_progress", pollingRecord.ClaimActor)

	fixture.mustBR(t, "close", pollingBeadID, "--reason=coordinator-completion-e2e", "--json")
	successorBeadID := fixture.createBead(t, "Coordinator completion successor")
	writeBVPayload(successorBeadID, "Coordinator completion successor")
	completedPolling := waitForRecord(pollingBeadID, func(record *atomicAssignmentRecord) bool {
		return record.Status == "completed" && record.CompletedAt != nil
	}, "production completion transition")
	if completedPolling.ReservationState != "released" || completedPolling.ReservationCompleted ||
		len(completedPolling.ReservationIDs) != 0 || len(completedPolling.ReservedPaths) != 0 {
		t.Fatalf("completion retained coordinator reservation handles: %+v", completedPolling)
	}
	if reserveCount.Load() != 2 || releaseCount.Load() != 2 {
		t.Fatalf("coordinator reservation lifecycle reserve=%d release=%d, want 2/2", reserveCount.Load(), releaseCount.Load())
	}
	reservationMu.Lock()
	if len(releasedReservationIDs) != 2 || releasedReservationIDs[0] <= 0 || releasedReservationIDs[1] <= 0 || releasedReservationIDs[0] == releasedReservationIDs[1] {
		t.Fatalf("coordinator released reservation IDs = %v", releasedReservationIDs)
	}
	reservationMu.Unlock()
	successorRecord := waitForRecord(successorBeadID, func(record *atomicAssignmentRecord) bool {
		return record.DispatchState == "sent" && record.DispatchReceiptID != ""
	}, "post-completion successor dispatch")
	if successorRecord.OccupancyKey != pollingRecord.OccupancyKey {
		t.Fatalf("successor did not reuse the completion-freed pane: completed=%q successor=%q", pollingRecord.OccupancyKey, successorRecord.OccupancyKey)
	}
	if sendCount.Load() != 4 {
		t.Fatalf("post-completion coordinator deliveries=%d, want 4 total", sendCount.Load())
	}
	fixture.assertBead(t, successorBeadID, "in_progress", successorRecord.ClaimActor)

	// Keep triage pointed at the still-active first assignment while the
	// successor reaches terminal state, so the freed pane cannot be filled by
	// a repeated recommendation for a just-closed work item.
	writeBVPayload(beadID, "Coordinator once delivery")
	fixture.mustBR(t, "close", successorBeadID, "--reason=coordinator-completion-e2e", "--json")
	waitForRecord(successorBeadID, func(record *atomicAssignmentRecord) bool {
		return record.Status == "completed" && record.CompletedAt != nil
	}, "successor completion transition")
	if err := longRun.Process.Signal(syscall.SIGTERM); err != nil {
		t.Fatalf("signal coordinator: %v", err)
	}
	if err := longRun.Wait(); err != nil {
		t.Fatalf("long-running coordinator did not exit cleanly: %v stderr=%s", err, longRunStderr.String())
	}
	if ctx.Err() != nil {
		t.Fatalf("long-running coordinator exceeded deadline: %v", ctx.Err())
	}
	if len(bytes.TrimSpace(longRunStderr.Bytes())) != 0 {
		t.Fatalf("long-running coordinator stderr=%s", longRunStderr.String())
	}

	secret := "sk-proj-NTM_COORDINATOR_TITLE_1234567890123456789012345678901234567890"
	secretTitle := "Coordinator secret title internal/coordinator/secret_mail.go " + secret
	secretBeadID := fixture.createBead(t, secretTitle)
	writeBVPayload(secretBeadID, secretTitle)
	secretResult, secretEnvelope := runOnce()
	if secretResult.exitCode != 0 || len(bytes.TrimSpace(secretResult.stderr)) != 0 || !secretEnvelope.Success ||
		len(secretEnvelope.Assignments) != 1 || !secretEnvelope.Assignments[0].Success || !secretEnvelope.Assignments[0].MessageSent {
		t.Fatalf("coordinator secret-title cycle result=%+v envelope=%+v", secretResult, secretEnvelope)
	}
	fixture.assertSecretAbsent(t, secret, secretResult.stdout, secretResult.stderr)
	secretRecord := fixture.readLedgerAssignment(t, secretBeadID)
	secretRecordJSON, err := json.Marshal(secretRecord)
	if err != nil {
		t.Fatalf("encode coordinator secret-title record: %v", err)
	}
	fixture.assertSecretAbsent(t, secret, secretRecordJSON)
	if !strings.Contains(secretRecord.BeadTitle, "[REDACTED:") || !strings.Contains(secretRecord.PromptSent, "[REDACTED:") {
		t.Fatalf("coordinator secret-title durable record = %+v", secretRecord)
	}
	reservationMu.Lock()
	mailProtocol := append([]byte(nil), rawMailRequests.Bytes()...)
	reservationMu.Unlock()
	fixture.assertSecretAbsent(t, secret, mailProtocol)
	fixture.assertPrivateStateExcludes(t, secret)
	if sendCount.Load() != 5 || reserveCount.Load() != 3 {
		t.Fatalf("coordinator secret-title side effects send=%d reserve=%d, want 5/3", sendCount.Load(), reserveCount.Load())
	}
	fixture.mustBR(t, "close", secretBeadID, "--reason=coordinator-secret-e2e", "--json")
	writeBVPayload(beadID, "Coordinator once delivery")
	secretCleanupResult, secretCleanup := runOnce()
	if secretCleanupResult.exitCode != 0 || len(bytes.TrimSpace(secretCleanupResult.stderr)) != 0 || !secretCleanup.Success || len(secretCleanup.Assignments) != 0 {
		t.Fatalf("coordinator secret-title cleanup result=%+v envelope=%+v", secretCleanupResult, secretCleanup)
	}
	secretCompleted := fixture.readLedgerAssignment(t, secretBeadID)
	if secretCompleted.Status != "completed" || secretCompleted.ReservationState != "released" || releaseCount.Load() != 3 {
		t.Fatalf("coordinator secret-title cleanup ledger=%+v releases=%d", secretCompleted, releaseCount.Load())
	}

	failureBeadID := fixture.createBead(t, "Coordinator invalid receipt")
	writeBVPayload(failureBeadID, "Coordinator invalid receipt")
	invalidReceipt.Store(true)
	failureResult, failure := runOnce()
	if failureResult.exitCode != 1 || len(bytes.TrimSpace(failureResult.stderr)) != 0 || failure.Success ||
		failure.ErrorCode != "ASSIGNMENT_FAILED" || failure.Error == "" || len(failure.Assignments) != 1 ||
		failure.Assignments[0].Success || failure.Assignments[0].MessageSent {
		t.Fatalf("failed coordinator cycle result=%+v envelope=%+v", failureResult, failure)
	}
	if sendCount.Load() != 6 {
		t.Fatalf("failed coordinator cycle deliveries=%d, want one attempted delivery after five durable sends", sendCount.Load())
	}
	failureRecord := fixture.readLedgerAssignment(t, failureBeadID)
	if failureRecord.DispatchState != "sending" || failureRecord.DispatchReceiptID != "" || failureRecord.DispatchAttempts != 1 {
		t.Fatalf("failed coordinator durable outcome = %+v", failureRecord)
	}

	retryResult, retryFailure := runOnce()
	if retryResult.exitCode != 1 || len(bytes.TrimSpace(retryResult.stderr)) != 0 || retryFailure.Success ||
		retryFailure.ErrorCode != "ASSIGNMENT_FAILED" || len(retryFailure.Assignments) != 1 || retryFailure.Assignments[0].Success ||
		retryFailure.Assignments[0].MessageSent {
		t.Fatalf("invalid-receipt retry result=%+v envelope=%+v", retryResult, retryFailure)
	}
	if sendCount.Load() != 6 {
		t.Fatalf("invalid-receipt retry repeated transport: deliveries=%d, want 6", sendCount.Load())
	}
	if afterRetry := fixture.readLedgerAssignment(t, failureBeadID); !reflect.DeepEqual(afterRetry, failureRecord) {
		t.Fatalf("invalid-receipt retry mutated ambiguous generation: before=%+v after=%+v", failureRecord, afterRetry)
	}
}

func TestE2EAtomicAssignmentAutoBuiltProcessRestart(t *testing.T) {
	CommonE2EPrerequisites(t)
	testutil.RequireTmuxThrottled(t)

	fixture := newAtomicAssignmentCLIFixture(t)
	const title = "Automatic built process assignment"
	beadID := fixture.createBead(t, title)
	templatePath := filepath.Join(fixture.projectDir, "atomic-auto-template.txt")
	marker := fmt.Sprintf("NTM_ATOMIC_AUTO_%d", time.Now().UnixNano())
	templateBody := marker + "::{BEAD_ID}::{TITLE}"
	prompt := strings.ReplaceAll(strings.ReplaceAll(templateBody, "{BEAD_ID}", beadID), "{TITLE}", title)
	if err := os.WriteFile(templatePath, []byte(templateBody), 0o600); err != nil {
		t.Fatalf("write automatic assignment template: %v", err)
	}
	fixture.primePaneForSafeDispatch(t, 0)
	// Production observation requires both an explicit agent prompt and five
	// seconds without pane activity before authorizing an automatic dispatch.
	time.Sleep(5500 * time.Millisecond)
	args := []string{
		"--json", "assign", fixture.session,
		"--repo=" + fixture.projectDir,
		"--auto",
		"--beads=" + beadID,
		"--limit=1",
		"--cod-only",
		"--template=custom",
		"--template-file=" + templatePath,
		"--reserve-files=false",
		"--ignore-deps",
		"--timeout=15s",
		"--quiet",
	}

	first := fixture.runNTM(t, nil, args...)
	if first.exitCode != 0 || len(bytes.TrimSpace(first.stderr)) != 0 {
		t.Fatalf("automatic assignment exit=%d stdout=%s stderr=%s", first.exitCode, first.stdout, first.stderr)
	}
	var firstEnvelope atomicAssignmentAutoEnvelope
	decodeAtomicAssignmentJSON(t, first.stdout, &firstEnvelope)
	if !firstEnvelope.Success || firstEnvelope.Error != nil || firstEnvelope.Command != "assign" ||
		firstEnvelope.Session != fixture.session || firstEnvelope.Data == nil ||
		firstEnvelope.Data.Summary.AssignedCount != 1 || len(firstEnvelope.Data.Assignments) != 1 ||
		firstEnvelope.Data.Assignments[0].BeadID != beadID ||
		firstEnvelope.Data.Assignments[0].PaneID != fixture.panes[0].ID ||
		firstEnvelope.Data.Assignments[0].Status != "assigned" ||
		!firstEnvelope.Data.Assignments[0].PromptSent {
		t.Fatalf("automatic assignment JSON envelope = %+v", firstEnvelope)
	}
	fixture.waitForMarkerCount(t, 0, marker, 1)
	fixture.assertMarkerCounts(t, marker, map[int]int{0: 1, 1: 0})
	firstRecord := fixture.readLedgerAssignment(t, beadID)
	assertAtomicAssignmentRecord(t, firstRecord, beadID, prompt, fixture.panes[0], "cod")

	second := fixture.runNTM(t, nil, args...)
	if second.exitCode != 0 || len(bytes.TrimSpace(second.stderr)) != 0 {
		t.Fatalf("automatic restart exit=%d stdout=%s stderr=%s", second.exitCode, second.stdout, second.stderr)
	}
	var secondEnvelope atomicAssignmentAutoEnvelope
	decodeAtomicAssignmentJSON(t, second.stdout, &secondEnvelope)
	if !secondEnvelope.Success || secondEnvelope.Error != nil || secondEnvelope.Data == nil ||
		secondEnvelope.Data.Summary.AssignedCount != 0 || len(secondEnvelope.Data.Assignments) != 0 {
		t.Fatalf("automatic replay JSON envelope = %+v", secondEnvelope)
	}
	fixture.assertMarkerCounts(t, marker, map[int]int{0: 1, 1: 0})
	assertAtomicAssignmentReceiptUnchanged(t, firstRecord, fixture.readLedgerAssignment(t, beadID))
	fixture.assertBead(t, beadID, "in_progress", firstRecord.ClaimActor)
}

func TestE2EAtomicAssignmentPlanOnlyEpicHonorsProjectOperatorGate(t *testing.T) {
	CommonE2EPrerequisites(t)
	testutil.RequireTmuxThrottled(t)

	fixture := newAtomicAssignmentCLIFixture(t)
	mail := newAtomicAssignmentMailStub(fixture.projectDir)
	defer mail.close()
	const title = "Plan-only epic requires project approval"
	epicOutput := fixture.mustBR(t, "create", title, "--type=epic", "--priority=1", "--silent")
	epicID := strings.TrimSpace(string(epicOutput))
	if epicID == "" || strings.ContainsAny(epicID, " \t\r\n") {
		t.Fatalf("unexpected epic ID %q", epicOutput)
	}
	fixture.mustBR(t, "update", epicID, "--add-label=architecture-approval", "--json")
	fixture.assertBead(t, epicID, "open", "")
	projectConfigDir := filepath.Join(fixture.projectDir, ".ntm")
	if err := os.MkdirAll(projectConfigDir, 0o700); err != nil {
		t.Fatalf("create project config directory: %v", err)
	}
	projectConfig := "[assign]\noperator_gated_labels = [\"architecture-approval\"]\n"
	if err := os.WriteFile(filepath.Join(projectConfigDir, "config.toml"), []byte(projectConfig), 0o600); err != nil {
		t.Fatalf("write project gate config: %v", err)
	}

	binDir := filepath.Join(fixture.root, "plan-only-bin")
	if err := os.MkdirAll(binDir, 0o700); err != nil {
		t.Fatalf("create plan-only bin: %v", err)
	}
	triagePath := filepath.Join(fixture.root, "plan-only-triage.json")
	planPath := filepath.Join(fixture.root, "plan-only-plan.json")
	if err := os.WriteFile(triagePath, []byte(`{"triage":{"recommendations":[]}}`), 0o600); err != nil {
		t.Fatalf("write triage payload: %v", err)
	}
	planPayload, err := json.Marshal(map[string]any{
		"plan": map[string]any{
			"tracks": []map[string]any{{
				"track_id": "epic-track",
				"items": []map[string]any{{
					"id": epicID, "title": title, "status": "open", "priority": 1,
				}},
			}},
		},
	})
	if err != nil {
		t.Fatalf("marshal plan payload: %v", err)
	}
	if err := os.WriteFile(planPath, planPayload, 0o600); err != nil {
		t.Fatalf("write plan payload: %v", err)
	}
	bvScript := strings.Join([]string{
		"#!/bin/sh",
		`case "$1" in`,
		`  --robot-triage) cat "$NTM_E2E_TRIAGE_PAYLOAD" ;;`,
		`  --robot-plan) cat "$NTM_E2E_PLAN_PAYLOAD" ;;`,
		`  --robot-insights) printf '%s\n' '{"Cycles":[]}' ;;`,
		`  *) printf 'unexpected bv args: %s\n' "$*" >&2; exit 64 ;;`,
		"esac",
		"",
	}, "\n")
	if err := os.WriteFile(filepath.Join(binDir, "bv"), []byte(bvScript), 0o700); err != nil {
		t.Fatalf("write bv fixture: %v", err)
	}
	brTrace := filepath.Join(fixture.root, "plan-only-br.trace")
	brScript := strings.Join([]string{
		"#!/bin/sh",
		"trace=" + tmux.ShellQuote(brTrace),
		"real_br=" + tmux.ShellQuote(fixture.brPath),
		`printf '%s\n' "$*" >> "$trace"`,
		`exec "$real_br" "$@"`,
		"",
	}, "\n")
	if err := os.WriteFile(filepath.Join(binDir, "br"), []byte(brScript), 0o700); err != nil {
		t.Fatalf("write br tracing fixture: %v", err)
	}

	fixture.primePaneForSafeDispatch(t, 0)
	time.Sleep(5500 * time.Millisecond)
	marker := fmt.Sprintf("NTM_PLAN_ONLY_EPIC_MUST_NOT_DISPATCH_%d", time.Now().UnixNano())
	templatePath := filepath.Join(fixture.projectDir, "plan-only-template.txt")
	if err := os.WriteFile(templatePath, []byte(marker+"::{BEAD_ID}::{TITLE}"), 0o600); err != nil {
		t.Fatalf("write plan-only template: %v", err)
	}
	outsideDir := filepath.Join(fixture.root, "outside-plan-project")
	if err := os.MkdirAll(outsideDir, 0o700); err != nil {
		t.Fatalf("create outside working directory: %v", err)
	}
	env := mail.env()
	env["PATH"] = binDir + string(os.PathListSeparator) + atomicAssignmentEnvValue(fixture.env, "PATH")
	env["NTM_E2E_TRIAGE_PAYLOAD"] = triagePath
	env["NTM_E2E_PLAN_PAYLOAD"] = planPath
	result := fixture.runNTMInDir(t, outsideDir, env,
		"--json", "assign", fixture.session,
		"--repo="+fixture.projectDir,
		"--auto",
		"--limit=1",
		"--cod-only",
		"--template=custom",
		"--template-file="+templatePath,
		"--reserve-files=true",
		"--timeout=15s",
		"--quiet",
	)
	if result.exitCode != 0 || len(bytes.TrimSpace(result.stderr)) != 0 {
		t.Fatalf("plan-only epic gate exit=%d stdout=%s stderr=%s", result.exitCode, result.stdout, result.stderr)
	}
	var envelope atomicAssignmentAutoEnvelope
	decodeAtomicAssignmentJSON(t, result.stdout, &envelope)
	if !envelope.Success || envelope.Error != nil || envelope.Data == nil ||
		envelope.Data.Summary.AssignedCount != 0 || len(envelope.Data.Assignments) != 0 {
		t.Fatalf("plan-only epic gate envelope = %+v", envelope)
	}
	trace, err := os.ReadFile(brTrace)
	if err != nil {
		t.Fatalf("read br trace: %v", err)
	}
	for _, want := range []string{
		"ready --json --limit 100000",
		"list --json --status open --limit 100000",
	} {
		if !bytes.Contains(trace, []byte(want)) {
			t.Fatalf("br trace omitted %q:\n%s", want, trace)
		}
	}
	fixture.assertBead(t, epicID, "open", "")
	fixture.assertLedgerReplicasHaveNoAssignment(t, epicID)
	fixture.assertMarkerCounts(t, marker, map[int]int{0: 0, 1: 0})
	assertAtomicAssignmentMailUntouched(t, mail.snapshot())
}

func TestE2EAtomicAssignmentEligibleBelowFormerPlanCapBuiltProcess(t *testing.T) {
	CommonE2EPrerequisites(t)
	testutil.RequireTmuxThrottled(t)

	fixture := newAtomicAssignmentCLIFixture(t)
	const eligibleTitle = "Eligible assignment below former plan cap"
	eligibleID := fixture.createBead(t, eligibleTitle)

	planItems := make([]map[string]any, 0, 102)
	labelRows := make([]map[string]any, 0, 102)
	gatedIDs := make(map[string]struct{}, 101)
	for index := 0; index < 101; index++ {
		beadID := fmt.Sprintf("ntm-e2e-gated-%03d", index)
		gatedIDs[beadID] = struct{}{}
		planItems = append(planItems, map[string]any{
			"id":       beadID,
			"title":    fmt.Sprintf("Operator-gated plan row %03d", index),
			"status":   "open",
			"priority": 1,
		})
		// issue_type is required since ntm-e165/GH#242: without verified
		// type evidence the classifier must assume a plan item could be an
		// epic and refuse automated dispatch.
		labelRows = append(labelRows, map[string]any{
			"id":         beadID,
			"issue_type": "task",
			"labels":     []string{"operator-gated"},
		})
	}
	planItems = append(planItems, map[string]any{
		"id":       eligibleID,
		"title":    eligibleTitle,
		"status":   "open",
		"priority": 2,
	})
	labelRows = append(labelRows, map[string]any{
		"id":         eligibleID,
		"issue_type": "task",
		"labels":     []string{},
	})

	planPayload, err := json.Marshal(map[string]any{
		"plan": map[string]any{
			"tracks": []map[string]any{{
				"track_id": "former-plan-cap",
				"items":    planItems,
			}},
		},
	})
	if err != nil {
		t.Fatalf("marshal former-plan-cap payload: %v", err)
	}
	labelsPayload, err := json.Marshal(labelRows)
	if err != nil {
		t.Fatalf("marshal former-plan-cap label payload: %v", err)
	}

	binDir := filepath.Join(fixture.root, "former-plan-cap-bin")
	if err := os.MkdirAll(binDir, 0o700); err != nil {
		t.Fatalf("create former-plan-cap bin directory: %v", err)
	}
	triagePath := filepath.Join(fixture.root, "former-plan-cap-triage.json")
	planPath := filepath.Join(fixture.root, "former-plan-cap-plan.json")
	labelsPath := filepath.Join(fixture.root, "former-plan-cap-labels.json")
	for path, payload := range map[string][]byte{
		triagePath: []byte(`{"triage":{"recommendations":[]}}`),
		planPath:   planPayload,
		labelsPath: labelsPayload,
	} {
		if err := os.WriteFile(path, payload, 0o600); err != nil {
			t.Fatalf("write former-plan-cap fixture %s: %v", path, err)
		}
	}

	bvTrace := filepath.Join(fixture.root, "former-plan-cap-bv.trace")
	brTrace := filepath.Join(fixture.root, "former-plan-cap-br.trace")
	bvScript := strings.Join([]string{
		"#!/bin/sh",
		`printf '%s\n' "$*" >> "$NTM_E2E_BV_TRACE"`,
		`case "$1" in`,
		`  --robot-triage) cat "$NTM_E2E_TRIAGE_PAYLOAD" ;;`,
		`  --robot-plan) cat "$NTM_E2E_PLAN_PAYLOAD" ;;`,
		`  --robot-insights) printf '%s\n' '{"Cycles":[]}' ;;`,
		`  *) printf 'unexpected bv args: %s\n' "$*" >&2; exit 64 ;;`,
		"esac",
		"",
	}, "\n")
	if err := os.WriteFile(filepath.Join(binDir, "bv"), []byte(bvScript), 0o700); err != nil {
		t.Fatalf("write former-plan-cap bv fixture: %v", err)
	}
	brScript := strings.Join([]string{
		"#!/bin/sh",
		`printf '%s\n' "$*" >> "$NTM_E2E_BR_TRACE"`,
		`subcommand=`,
		`for arg in "$@"; do`,
		`  case "$arg" in`,
		`    ready|list) subcommand="$arg"; break ;;`,
		`  esac`,
		`done`,
		`case "$subcommand" in`,
		`  ready) cat "$NTM_E2E_LABEL_PAYLOAD"; exit 0 ;;`,
		`  list)`,
		`    case " $* " in`,
		`      *" --status open "*) cat "$NTM_E2E_LABEL_PAYLOAD"; exit 0 ;;`,
		`    esac ;;`,
		`esac`,
		"real_br=" + tmux.ShellQuote(fixture.brPath),
		`exec "$real_br" "$@"`,
		"",
	}, "\n")
	if err := os.WriteFile(filepath.Join(binDir, "br"), []byte(brScript), 0o700); err != nil {
		t.Fatalf("write former-plan-cap br fixture: %v", err)
	}
	configPath := filepath.Join(fixture.root, "former-plan-cap-config.toml")
	if err := os.WriteFile(configPath, []byte("[assign]\n"), 0o600); err != nil {
		t.Fatalf("write former-plan-cap config: %v", err)
	}

	fixture.primePaneForSafeDispatch(t, 0)
	fixture.waitForEndpointIdle(t, fixture.panes[0], 6*time.Second, 60*time.Second)
	marker := fmt.Sprintf("NTM_ELIGIBLE_BELOW_FORMER_PLAN_CAP_%d", time.Now().UnixNano())
	templatePath := filepath.Join(fixture.projectDir, "former-plan-cap-template.txt")
	if err := os.WriteFile(templatePath, []byte(marker+"::{BEAD_ID}::{TITLE}"), 0o600); err != nil {
		t.Fatalf("write former-plan-cap template: %v", err)
	}
	env := map[string]string{
		"PATH":                   binDir + string(os.PathListSeparator) + atomicAssignmentEnvValue(fixture.env, "PATH"),
		"NTM_E2E_TRIAGE_PAYLOAD": triagePath,
		"NTM_E2E_PLAN_PAYLOAD":   planPath,
		"NTM_E2E_LABEL_PAYLOAD":  labelsPath,
		"NTM_E2E_BV_TRACE":       bvTrace,
		"NTM_E2E_BR_TRACE":       brTrace,
	}
	result := fixture.runNTM(t, env,
		"--config="+configPath,
		"--json", "assign", fixture.session,
		"--repo="+fixture.projectDir,
		"--auto",
		"--limit=1",
		"--cod-only",
		"--strategy=round-robin",
		"--template=custom",
		"--template-file="+templatePath,
		"--reserve-files=false",
		"--ignore-deps",
		"--timeout=60s",
		"--quiet",
	)
	if result.exitCode != 0 || len(bytes.TrimSpace(result.stderr)) != 0 {
		t.Fatalf("former-plan-cap assignment exit=%d stdout=%s stderr=%s", result.exitCode, result.stdout, result.stderr)
	}
	var envelope struct {
		Success bool `json:"success"`
		Data    *struct {
			Assignments []struct {
				BeadID     string `json:"bead_id"`
				PaneID     string `json:"pane_id"`
				Status     string `json:"status"`
				PromptSent bool   `json:"prompt_sent"`
			} `json:"assignments"`
			Skipped []struct {
				BeadID string `json:"bead_id"`
				Reason string `json:"reason"`
			} `json:"skipped"`
			Summary struct {
				TotalBeadCount  int `json:"total_bead_count"`
				ActionableCount int `json:"actionable_count"`
				BlockedCount    int `json:"blocked_count"`
				AssignedCount   int `json:"assigned_count"`
				SkippedCount    int `json:"skipped_count"`
			} `json:"summary"`
		} `json:"data"`
		Error *struct {
			Code    string `json:"code"`
			Message string `json:"message"`
		} `json:"error"`
	}
	decodeAtomicAssignmentJSON(t, result.stdout, &envelope)
	if !envelope.Success || envelope.Error != nil || envelope.Data == nil ||
		envelope.Data.Summary.TotalBeadCount != 102 || envelope.Data.Summary.ActionableCount != 1 ||
		envelope.Data.Summary.BlockedCount != 0 || envelope.Data.Summary.AssignedCount != 1 ||
		envelope.Data.Summary.SkippedCount != 101 || len(envelope.Data.Assignments) != 1 ||
		envelope.Data.Assignments[0].BeadID != eligibleID ||
		envelope.Data.Assignments[0].PaneID != fixture.panes[0].ID ||
		envelope.Data.Assignments[0].Status != "assigned" || !envelope.Data.Assignments[0].PromptSent ||
		len(envelope.Data.Skipped) != 101 {
		t.Fatalf("former-plan-cap assignment envelope = %+v", envelope)
	}
	for _, skipped := range envelope.Data.Skipped {
		if skipped.Reason != "operator_gated" {
			t.Fatalf("former-plan-cap skip for %s has reason %q, want operator_gated", skipped.BeadID, skipped.Reason)
		}
		if _, ok := gatedIDs[skipped.BeadID]; !ok {
			t.Fatalf("former-plan-cap returned unexpected skipped bead %q", skipped.BeadID)
		}
		delete(gatedIDs, skipped.BeadID)
	}
	if len(gatedIDs) != 0 {
		t.Fatalf("former-plan-cap omitted %d gated rows from skipped output", len(gatedIDs))
	}

	prompt := marker + "::" + eligibleID + "::" + eligibleTitle
	fixture.waitForMarkerCount(t, 0, prompt, 1)
	fixture.assertMarkerCounts(t, marker, map[int]int{0: 1, 1: 0})
	record := fixture.readLedgerAssignment(t, eligibleID)
	assertAtomicAssignmentRecord(t, record, eligibleID, prompt, fixture.panes[0], "codex")
	fixture.assertBead(t, eligibleID, "in_progress", record.ClaimActor)
	ledger, err := fixture.readLedger()
	if err != nil {
		t.Fatalf("read former-plan-cap ledger: %v", err)
	}
	if len(ledger.Assignments) != 1 {
		t.Fatalf("former-plan-cap ledger contains %d assignments, want only %s", len(ledger.Assignments), eligibleID)
	}

	bvTraceData, err := os.ReadFile(bvTrace)
	if err != nil {
		t.Fatalf("read former-plan-cap bv trace: %v", err)
	}
	for _, want := range []string{"--robot-triage", "--robot-plan", "--robot-insights"} {
		if !bytes.Contains(bvTraceData, []byte(want)) {
			t.Fatalf("former-plan-cap bv trace omitted %q:\n%s", want, bvTraceData)
		}
	}
	brTraceData, err := os.ReadFile(brTrace)
	if err != nil {
		t.Fatalf("read former-plan-cap br trace: %v", err)
	}
	for _, want := range []string{
		"ready --json --limit 100000",
		"list --json --status open --limit 100000",
	} {
		if !bytes.Contains(brTraceData, []byte(want)) {
			t.Fatalf("former-plan-cap br trace omitted %q:\n%s", want, brTraceData)
		}
	}
}

func TestE2EAtomicRobotAssignAndDistributeAuthoritativePolicyBuiltProcess(t *testing.T) {
	CommonE2EPrerequisites(t)
	testutil.RequireTmuxThrottled(t)

	fixture := newAtomicAssignmentCLIFixture(t)
	mail := newAtomicAssignmentMailStub(fixture.projectDir)
	defer mail.close()

	const gateLabel = "release-control-room"
	const title = "Target project gate blocks robot and distribute assignment"
	beadID := fixture.createBead(t, title)
	fixture.mustBR(t, "update", beadID, "--add-label="+gateLabel, "--json")
	beforeBead := fixture.mustBR(t, "show", beadID, "--json")

	projectConfigDir := filepath.Join(fixture.projectDir, ".ntm")
	if err := os.MkdirAll(projectConfigDir, 0o700); err != nil {
		t.Fatalf("create authoritative-policy config directory: %v", err)
	}
	projectConfigPath := filepath.Join(projectConfigDir, "config.toml")
	validProjectConfig := []byte("[assign]\noperator_gated_labels = [\"" + gateLabel + "\"]\n")
	if err := os.WriteFile(projectConfigPath, validProjectConfig, 0o600); err != nil {
		t.Fatalf("write authoritative target-project policy: %v", err)
	}

	binDir := filepath.Join(fixture.root, "authoritative-policy-bin")
	if err := os.MkdirAll(binDir, 0o700); err != nil {
		t.Fatalf("create authoritative-policy bin directory: %v", err)
	}
	triagePath := filepath.Join(fixture.root, "authoritative-policy-triage.json")
	planPath := filepath.Join(fixture.root, "authoritative-policy-plan.json")
	if err := os.WriteFile(triagePath, []byte(`{"triage":{"recommendations":[]}}`), 0o600); err != nil {
		t.Fatalf("write authoritative-policy triage payload: %v", err)
	}
	planPayload, err := json.Marshal(map[string]any{
		"plan": map[string]any{
			"tracks": []map[string]any{{
				"track_id": "authoritative-policy-track",
				"items": []map[string]any{{
					"id": beadID, "title": title, "status": "open", "priority": 1,
				}},
			}},
		},
	})
	if err != nil {
		t.Fatalf("marshal authoritative-policy plan payload: %v", err)
	}
	if err := os.WriteFile(planPath, planPayload, 0o600); err != nil {
		t.Fatalf("write authoritative-policy plan payload: %v", err)
	}
	bvTrace := filepath.Join(fixture.root, "authoritative-policy-bv.trace")
	brTrace := filepath.Join(fixture.root, "authoritative-policy-br.trace")
	bvScript := strings.Join([]string{
		"#!/bin/sh",
		`printf '%s\n' "$*" >> "$NTM_E2E_BV_TRACE"`,
		`case "$1" in`,
		`  --robot-triage) cat "$NTM_E2E_TRIAGE_PAYLOAD" ;;`,
		`  --robot-plan) cat "$NTM_E2E_PLAN_PAYLOAD" ;;`,
		`  *) printf 'unexpected bv args: %s\n' "$*" >&2; exit 64 ;;`,
		"esac",
		"",
	}, "\n")
	if err := os.WriteFile(filepath.Join(binDir, "bv"), []byte(bvScript), 0o700); err != nil {
		t.Fatalf("write authoritative-policy bv fixture: %v", err)
	}
	brScript := strings.Join([]string{
		"#!/bin/sh",
		`printf '%s\n' "$*" >> "$NTM_E2E_BR_TRACE"`,
		"real_br=" + tmux.ShellQuote(fixture.brPath),
		`exec "$real_br" "$@"`,
		"",
	}, "\n")
	if err := os.WriteFile(filepath.Join(binDir, "br"), []byte(brScript), 0o700); err != nil {
		t.Fatalf("write authoritative-policy br fixture: %v", err)
	}

	outsideDir := filepath.Join(fixture.root, "authoritative-policy-outside")
	if err := os.MkdirAll(outsideDir, 0o700); err != nil {
		t.Fatalf("create unrelated caller working directory: %v", err)
	}
	historyPath := filepath.Join(fixture.root, "data", "ntm", "history.jsonl")
	if err := os.MkdirAll(filepath.Dir(historyPath), 0o700); err != nil {
		t.Fatalf("create send history directory: %v", err)
	}
	if err := os.WriteFile(historyPath, []byte("{\"sentinel\":\"preserve authoritative-policy history\"}\n"), 0o600); err != nil {
		t.Fatalf("write send history sentinel: %v", err)
	}
	promptHistoryPath := filepath.Join(fixture.homeDir, ".ntm", "sessions", fixture.session, "prompts.json")
	protectedPaths := []string{
		fixture.ledgerPath(),
		fixture.ledgerPath() + ".bak",
		historyPath,
		promptHistoryPath,
	}
	protectedFiles := snapshotAtomicAssignmentFiles(t, protectedPaths...)

	for pane := range fixture.panes {
		fixture.primePaneForSafeDispatch(t, pane)
	}
	time.Sleep(5500 * time.Millisecond)
	protectedPanes := snapshotAtomicAssignmentPanes(t, fixture)
	env := mail.env()
	env["PATH"] = binDir + string(os.PathListSeparator) + atomicAssignmentEnvValue(fixture.env, "PATH")
	env["NTM_E2E_TRIAGE_PAYLOAD"] = triagePath
	env["NTM_E2E_PLAN_PAYLOAD"] = planPath
	env["NTM_E2E_BV_TRACE"] = bvTrace
	env["NTM_E2E_BR_TRACE"] = brTrace

	assertReadOnly := func(t *testing.T) {
		t.Helper()
		afterBead := fixture.mustBR(t, "show", beadID, "--json")
		if !bytes.Equal(bytes.TrimSpace(afterBead), bytes.TrimSpace(beforeBead)) {
			t.Fatalf("policy-gated command mutated Beads state:\nbefore=%s\nafter=%s", beforeBead, afterBead)
		}
		assertAtomicAssignmentFilesUnchanged(t, protectedFiles)
		assertAtomicAssignmentPanesUnchanged(t, fixture, protectedPanes)
		assertAtomicAssignmentMailUntouched(t, mail.snapshot())
		afterConfig, err := os.ReadFile(projectConfigPath)
		if err != nil {
			t.Fatalf("read target-project policy after command: %v", err)
		}
		if !bytes.Equal(afterConfig, validProjectConfig) {
			t.Fatalf("policy-gated command mutated target config:\nbefore=%s\nafter=%s", validProjectConfig, afterConfig)
		}
	}

	robotResult := fixture.runNTMInDir(t, outsideDir, env,
		"--robot-format=json",
		"--robot-assign="+fixture.session,
		"--strategy=balanced",
	)
	if robotResult.exitCode != 0 || len(bytes.TrimSpace(robotResult.stderr)) != 0 {
		t.Fatalf("target-policy robot assign exit=%d stdout=%s stderr=%s", robotResult.exitCode, robotResult.stdout, robotResult.stderr)
	}
	var robotEnvelope struct {
		Success         bool              `json:"success"`
		Recommendations []json.RawMessage `json:"recommendations"`
		IdleAgents      []string          `json:"idle_agents"`
		Summary         struct {
			ReadyBeads      int `json:"ready_beads"`
			Recommendations int `json:"recommendations"`
		} `json:"summary"`
	}
	decodeAtomicAssignmentJSON(t, robotResult.stdout, &robotEnvelope)
	if !robotEnvelope.Success || len(robotEnvelope.IdleAgents) == 0 || len(robotEnvelope.Recommendations) != 0 ||
		robotEnvelope.Summary.ReadyBeads != 0 || robotEnvelope.Summary.Recommendations != 0 {
		t.Fatalf("target-policy robot assign envelope = %+v", robotEnvelope)
	}
	assertReadOnly(t)

	for _, tc := range []struct {
		name string
		auto bool
	}{
		{name: "distribute preview"},
		{name: "distribute auto", auto: true},
	} {
		t.Run(tc.name+" honors target custom gate", func(t *testing.T) {
			args := []string{"--json", "send", fixture.session, "--distribute", "--dist-limit=1"}
			if tc.auto {
				args = append(args, "--dist-auto")
			}
			result := fixture.runNTMInDir(t, outsideDir, env, args...)
			if result.exitCode != 0 || len(bytes.TrimSpace(result.stderr)) != 0 {
				t.Fatalf("target-policy %s exit=%d stdout=%s stderr=%s", tc.name, result.exitCode, result.stdout, result.stderr)
			}
			var envelope struct {
				Success         bool              `json:"success"`
				Distributed     int               `json:"distributed"`
				Recommendations []json.RawMessage `json:"recommendations"`
				Delivered       int               `json:"delivered"`
			}
			decodeAtomicAssignmentJSON(t, result.stdout, &envelope)
			if !envelope.Success || envelope.Distributed != 0 || envelope.Delivered != 0 || len(envelope.Recommendations) != 0 {
				t.Fatalf("target-policy %s envelope = %+v", tc.name, envelope)
			}
			assertReadOnly(t)
		})
	}

	bvTraceData, err := os.ReadFile(bvTrace)
	if err != nil {
		t.Fatalf("read authoritative-policy bv trace: %v", err)
	}
	for _, want := range []string{"--robot-triage", "--robot-plan"} {
		if !bytes.Contains(bvTraceData, []byte(want)) {
			t.Fatalf("authoritative-policy bv trace omitted %q:\n%s", want, bvTraceData)
		}
	}
	brTraceData, err := os.ReadFile(brTrace)
	if err != nil {
		t.Fatalf("read authoritative-policy br trace: %v", err)
	}
	for _, want := range []string{
		"ready --json --limit 100000",
		"list --json --status open --limit 100000",
	} {
		if !bytes.Contains(brTraceData, []byte(want)) {
			t.Fatalf("authoritative-policy br trace omitted %q:\n%s", want, brTraceData)
		}
	}

	invalidProjectConfig := []byte("[assign\noperator_gated_labels = [\"" + gateLabel + "\"]\n")
	for index, tc := range []struct {
		name          string
		command       string
		invalidTarget bool
		missingGlobal bool
	}{
		{name: "robot assign invalid target policy", command: "robot", invalidTarget: true},
		{name: "robot assign missing explicit global policy", command: "robot", missingGlobal: true},
		{name: "distribute preview invalid target policy", command: "preview", invalidTarget: true},
		{name: "distribute preview missing explicit global policy", command: "preview", missingGlobal: true},
		{name: "distribute auto invalid target policy", command: "auto", invalidTarget: true},
		{name: "distribute auto missing explicit global policy", command: "auto", missingGlobal: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			expectedProjectConfig := validProjectConfig
			if tc.invalidTarget {
				expectedProjectConfig = invalidProjectConfig
			}
			if err := os.WriteFile(projectConfigPath, expectedProjectConfig, 0o600); err != nil {
				t.Fatalf("write %s project policy: %v", tc.name, err)
			}
			missingConfigPath := filepath.Join(fixture.root, fmt.Sprintf("missing-authoritative-policy-%d.toml", index))
			args := make([]string, 0, 8)
			if tc.missingGlobal {
				args = append(args, "--config="+missingConfigPath)
			}
			switch tc.command {
			case "robot":
				args = append(args, "--robot-format=json", "--robot-assign="+fixture.session, "--strategy=balanced")
			case "preview", "auto":
				args = append(args, "--json", "send", fixture.session, "--distribute", "--dist-limit=1")
				if tc.command == "auto" {
					args = append(args, "--dist-auto")
				}
			default:
				t.Fatalf("unknown authoritative-policy command %q", tc.command)
			}

			beforeFiles := snapshotAtomicAssignmentFiles(t,
				fixture.ledgerPath(),
				fixture.ledgerPath()+".bak",
				historyPath,
				promptHistoryPath,
				projectConfigPath,
				missingConfigPath,
				bvTrace,
				brTrace,
			)
			sessionStateRoot := filepath.Dir(fixture.ledgerPath())
			beforeSessionState := snapshotAtomicAssignmentTree(t, sessionStateRoot)
			beadsRoot := filepath.Join(fixture.projectDir, ".beads")
			beforeBeadsTree := snapshotAtomicAssignmentTree(t, beadsRoot)
			beforePanes := snapshotAtomicAssignmentPanes(t, fixture)
			result := fixture.runNTMInDir(t, outsideDir, env, args...)
			code, message := assertAtomicAssignmentSingleFailureJSON(t, result)
			if code != "INVALID_FLAG" || !strings.Contains(strings.ToLower(message), "config") {
				t.Fatalf("%s code=%q message=%q, want INVALID_FLAG with config diagnostic", tc.name, code, message)
			}
			if tc.command != "robot" {
				var failure struct {
					Targets []string `json:"targets"`
				}
				decodeAtomicAssignmentJSON(t, result.stdout, &failure)
				if failure.Targets == nil || len(failure.Targets) != 0 {
					t.Fatalf("%s targets=%v, want present empty array", tc.name, failure.Targets)
				}
			}
			assertAtomicAssignmentFilesUnchanged(t, beforeFiles)
			assertAtomicAssignmentTreeUnchanged(t, sessionStateRoot, beforeSessionState)
			assertAtomicAssignmentTreeUnchanged(t, beadsRoot, beforeBeadsTree)
			assertAtomicAssignmentPanesUnchanged(t, fixture, beforePanes)
			afterBead := fixture.mustBR(t, "show", beadID, "--json")
			if !bytes.Equal(bytes.TrimSpace(afterBead), bytes.TrimSpace(beforeBead)) {
				t.Fatalf("%s mutated Beads state:\nbefore=%s\nafter=%s", tc.name, beforeBead, afterBead)
			}
			fixture.assertBead(t, beadID, "open", "")
			fixture.assertLedgerReplicasHaveNoAssignment(t, beadID)
			assertAtomicAssignmentMailUntouched(t, mail.snapshot())
		})
	}
}

func TestE2EAtomicRetryReassignAndRebalanceInvalidPolicyIsMutationFreeBuiltProcess(t *testing.T) {
	CommonE2EPrerequisites(t)
	testutil.RequireTmuxThrottled(t)

	fixture := newAtomicAssignmentCLIFixture(t)
	for pane := range fixture.panes {
		fixture.primePaneForSafeDispatch(t, pane)
	}
	time.Sleep(5500 * time.Millisecond)

	retryBeadID := fixture.createBead(t, "Invalid policy preserves failed retry generation")
	retrySourcePrompt := fmt.Sprintf("NTM_INVALID_POLICY_RETRY_SOURCE_%d", time.Now().UnixNano())
	retrySeed := fixture.runNTM(t, nil, atomicDirectArgsForPane(fixture, 0, retryBeadID, retrySourcePrompt, false)...)
	if retrySeed.exitCode != 0 || len(bytes.TrimSpace(retrySeed.stderr)) != 0 {
		t.Fatalf("seed invalid-policy retry generation exit=%d stdout=%s stderr=%s", retrySeed.exitCode, retrySeed.stdout, retrySeed.stderr)
	}
	fixture.waitForMarkerCount(t, 0, retrySourcePrompt, 1)
	fixture.driveAssignmentStatus(t, fixture.panes[0], retryBeadID, "ERROR: fatal invalid-policy retry fixture", "failed")
	retryBefore := fixture.readLedgerAssignment(t, retryBeadID)
	if retryBefore.Status != "failed" {
		t.Fatalf("invalid-policy retry seed status=%q, want failed: %+v", retryBefore.Status, retryBefore)
	}

	reassignBeadID := fixture.createBead(t, "Invalid policy preserves live reassign generation")
	reassignSourcePrompt := fmt.Sprintf("NTM_INVALID_POLICY_REASSIGN_SOURCE_%d", time.Now().UnixNano())
	reassignSeed := fixture.runNTM(t, nil, atomicDirectArgsForPane(fixture, 1, reassignBeadID, reassignSourcePrompt, false)...)
	if reassignSeed.exitCode != 0 || len(bytes.TrimSpace(reassignSeed.stderr)) != 0 {
		t.Fatalf("seed invalid-policy reassign generation exit=%d stdout=%s stderr=%s", reassignSeed.exitCode, reassignSeed.stdout, reassignSeed.stderr)
	}
	fixture.waitForMarkerCount(t, 1, reassignSourcePrompt, 1)
	reassignBefore := fixture.readLedgerAssignment(t, reassignBeadID)
	if reassignBefore.Status != "assigned" || reassignBefore.DispatchState != "sent" {
		t.Fatalf("invalid-policy reassign seed is not active: %+v", reassignBefore)
	}

	mail := newAtomicAssignmentMailStub(fixture.projectDir)
	defer mail.close()
	outsideDir := filepath.Join(fixture.root, "invalid-policy-recovery-outside")
	if err := os.MkdirAll(outsideDir, 0o700); err != nil {
		t.Fatalf("create invalid-policy recovery caller directory: %v", err)
	}
	projectConfigDir := filepath.Join(fixture.projectDir, ".ntm")
	if err := os.MkdirAll(projectConfigDir, 0o700); err != nil {
		t.Fatalf("create invalid-policy recovery config directory: %v", err)
	}
	projectConfigPath := filepath.Join(projectConfigDir, "config.toml")
	validProjectConfig := []byte("[assign]\noperator_gated_labels = [\"change-window\"]\n")
	invalidProjectConfig := []byte("[assign\noperator_gated_labels = [\"change-window\"]\n")
	invalidGlobalConfig := []byte("[assign\noperator_gated_labels = [\"global-change-window\"]\n")

	for index, tc := range []struct {
		name    string
		command string
		policy  string
	}{
		{name: "retry invalid target project policy", command: "retry", policy: "target-invalid"},
		{name: "retry invalid selected global policy", command: "retry", policy: "global-invalid"},
		{name: "retry missing selected global policy", command: "retry", policy: "global-missing"},
		{name: "reassign invalid target project policy", command: "reassign", policy: "target-invalid"},
		{name: "reassign invalid selected global policy", command: "reassign", policy: "global-invalid"},
		{name: "reassign missing selected global policy", command: "reassign", policy: "global-missing"},
		{name: "rebalance preview invalid target project policy", command: "rebalance-preview", policy: "target-invalid"},
		{name: "rebalance apply invalid target project policy", command: "rebalance-apply", policy: "target-invalid"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			expectedProjectConfig := validProjectConfig
			if tc.policy == "target-invalid" {
				expectedProjectConfig = invalidProjectConfig
			}
			if err := os.WriteFile(projectConfigPath, expectedProjectConfig, 0o600); err != nil {
				t.Fatalf("write %s target-project policy: %v", tc.name, err)
			}

			selectedConfigPath := filepath.Join(fixture.root, fmt.Sprintf("selected-recovery-policy-%d.toml", index))
			args := make([]string, 0, 16)
			switch tc.policy {
			case "global-invalid":
				if err := os.WriteFile(selectedConfigPath, invalidGlobalConfig, 0o600); err != nil {
					t.Fatalf("write %s selected global policy: %v", tc.name, err)
				}
				args = append(args, "--config="+selectedConfigPath)
			case "global-missing":
				args = append(args, "--config="+selectedConfigPath)
			case "target-invalid":
			default:
				t.Fatalf("unknown invalid-policy fixture %q", tc.policy)
			}

			marker := fmt.Sprintf("NTM_INVALID_POLICY_%s_MUST_NOT_DISPATCH_%d", strings.ToUpper(strings.ReplaceAll(tc.command, "-", "_")), time.Now().UnixNano())
			switch tc.command {
			case "retry":
				args = append(args,
					"--json", "assign", fixture.session,
					"--repo="+fixture.projectDir,
					"--retry="+retryBeadID,
					"--to-pane="+fixture.panes[0].ID,
					"--prompt="+marker,
					"--force",
					"--reserve-files=false",
					"--timeout=15s",
				)
			case "reassign":
				args = append(args,
					"--json", "assign", fixture.session,
					"--repo="+fixture.projectDir,
					"--reassign="+reassignBeadID,
					"--to-pane="+fixture.panes[0].ID,
					"--prompt="+marker,
					"--force",
					"--reserve-files=false",
					"--timeout=15s",
				)
			case "rebalance-preview", "rebalance-apply":
				args = append(args, "--json", "rebalance", fixture.session, "--format=json")
				if tc.command == "rebalance-apply" {
					args = append(args, "--apply")
				}
			default:
				t.Fatalf("unknown invalid-policy command %q", tc.command)
			}

			protectedPaths := []string{
				fixture.ledgerPath(),
				fixture.ledgerPath() + ".bak",
				projectConfigPath,
			}
			if tc.policy == "global-invalid" || tc.policy == "global-missing" {
				protectedPaths = append(protectedPaths, selectedConfigPath)
			}
			beforeFiles := snapshotAtomicAssignmentFiles(t, protectedPaths...)
			sessionStateRoot := filepath.Dir(fixture.ledgerPath())
			beforeSessionState := snapshotAtomicAssignmentTree(t, sessionStateRoot)
			beadsRoot := filepath.Join(fixture.projectDir, ".beads")
			beforeBeadsTree := snapshotAtomicAssignmentTree(t, beadsRoot)
			beforePanes := snapshotAtomicAssignmentPanes(t, fixture)
			beforeRetryBead := fixture.mustBR(t, "show", retryBeadID, "--json")
			beforeReassignBead := fixture.mustBR(t, "show", reassignBeadID, "--json")
			beforeMail := mail.snapshot()
			beforeBeadsTree = snapshotAtomicAssignmentTree(t, beadsRoot)

			result := fixture.runNTMInDir(t, outsideDir, mail.env(), args...)
			code, message := assertAtomicAssignmentSingleFailureJSON(t, result)
			if code != "INVALID_FLAG" || !strings.Contains(strings.ToLower(message), "config") {
				t.Fatalf("%s code=%q message=%q, want INVALID_FLAG with config diagnostic", tc.name, code, message)
			}

			assertAtomicAssignmentFilesUnchanged(t, beforeFiles)
			assertAtomicAssignmentTreeUnchanged(t, sessionStateRoot, beforeSessionState)
			assertAtomicAssignmentTreeUnchanged(t, beadsRoot, beforeBeadsTree)
			assertAtomicAssignmentPanesUnchanged(t, fixture, beforePanes)
			afterRetryBead := fixture.mustBR(t, "show", retryBeadID, "--json")
			if !bytes.Equal(bytes.TrimSpace(afterRetryBead), bytes.TrimSpace(beforeRetryBead)) {
				t.Fatalf("%s mutated retry Beads state:\nbefore=%s\nafter=%s", tc.name, beforeRetryBead, afterRetryBead)
			}
			afterReassignBead := fixture.mustBR(t, "show", reassignBeadID, "--json")
			if !bytes.Equal(bytes.TrimSpace(afterReassignBead), bytes.TrimSpace(beforeReassignBead)) {
				t.Fatalf("%s mutated reassign Beads state:\nbefore=%s\nafter=%s", tc.name, beforeReassignBead, afterReassignBead)
			}
			if after := fixture.readLedgerAssignment(t, retryBeadID); !reflect.DeepEqual(after, retryBefore) {
				t.Fatalf("%s mutated failed retry generation:\nbefore=%+v\nafter=%+v", tc.name, retryBefore, after)
			}
			if after := fixture.readLedgerAssignment(t, reassignBeadID); !reflect.DeepEqual(after, reassignBefore) {
				t.Fatalf("%s mutated active reassign generation:\nbefore=%+v\nafter=%+v", tc.name, reassignBefore, after)
			}
			fixture.assertMarkerCounts(t, marker, map[int]int{0: 0, 1: 0})
			afterMail := mail.snapshot()
			if !reflect.DeepEqual(afterMail, beforeMail) {
				t.Fatalf("%s touched Agent Mail:\nbefore=%+v\nafter=%+v", tc.name, beforeMail, afterMail)
			}
			assertAtomicAssignmentMailUntouched(t, afterMail)
		})
	}
}

func TestE2EAtomicAssignmentLabelEnrichmentFailuresFailClosedBuiltProcess(t *testing.T) {
	CommonE2EPrerequisites(t)
	testutil.RequireTmuxThrottled(t)

	fixture := newAtomicAssignmentCLIFixture(t)
	binDir := filepath.Join(fixture.root, "label-enrichment-bin")
	if err := os.MkdirAll(binDir, 0o700); err != nil {
		t.Fatalf("create label-enrichment bin directory: %v", err)
	}
	bvScript := strings.Join([]string{
		"#!/bin/sh",
		`case "$1" in`,
		`  --robot-triage) cat "$NTM_E2E_TRIAGE_PAYLOAD" ;;`,
		`  --robot-plan) cat "$NTM_E2E_PLAN_PAYLOAD" ;;`,
		`  *) printf 'unexpected bv args: %s\n' "$*" >&2; exit 64 ;;`,
		"esac",
		"",
	}, "\n")
	if err := os.WriteFile(filepath.Join(binDir, "bv"), []byte(bvScript), 0o700); err != nil {
		t.Fatalf("write label-enrichment bv fixture: %v", err)
	}
	brScript := strings.Join([]string{
		"#!/bin/sh",
		`printf '%s\n' "$*" >> "$NTM_E2E_BR_TRACE"`,
		`command=`,
		`for arg in "$@"; do`,
		`  case "$arg" in ready|list) command="$arg"; break ;; esac`,
		`done`,
		`for arg in "$@"; do`,
		`  [ "$arg" = "--all" ] && [ "$command" = "list" ] && command=list_all`,
		`done`,
		`case "$NTM_E2E_BR_MODE:$command" in`,
		`  ready_failure:ready) printf 'injected br ready failure\n' >&2; exit 70 ;;`,
		`  list_failure:list) printf 'injected br list failure\n' >&2; exit 71 ;;`,
		`  malformed_ready:ready) printf '{\n'; exit 0 ;;`,
		`  malformed_list:list) printf '{\n'; exit 0 ;;`,
		`  absent:ready|absent:list|absent:list_all) printf '[]\n'; exit 0 ;;`,
		`  stale_closed:ready|stale_closed:list) printf '[]\n'; exit 0 ;;`,
		`  stale_closed:list_all) printf '[{"id":"%s","status":"closed"}]\n' "$NTM_E2E_BEAD_ID"; exit 0 ;;`,
		"esac",
		"real_br=" + tmux.ShellQuote(fixture.brPath),
		`exec "$real_br" "$@"`,
		"",
	}, "\n")
	if err := os.WriteFile(filepath.Join(binDir, "br"), []byte(brScript), 0o700); err != nil {
		t.Fatalf("write label-enrichment br fixture: %v", err)
	}
	triagePath := filepath.Join(fixture.root, "label-enrichment-triage.json")
	if err := os.WriteFile(triagePath, []byte(`{"triage":{"recommendations":[]}}`), 0o600); err != nil {
		t.Fatalf("write label-enrichment triage payload: %v", err)
	}

	fixture.primePaneForSafeDispatch(t, 0)
	time.Sleep(5500 * time.Millisecond)
	for index, tc := range []struct {
		name              string
		mode              string
		wantDiagnostic    string
		wantBRInvocations []string
		wantStaleSkip     bool
	}{
		{
			name: "br ready failure", mode: "ready_failure",
			wantDiagnostic:    "read bead labels (br ready",
			wantBRInvocations: []string{"--lock-timeout 5000 ready --json --limit 100000"},
		},
		{
			name: "br list failure", mode: "list_failure",
			wantDiagnostic:    "read bead labels (br list",
			wantBRInvocations: []string{"--lock-timeout 5000 ready --json --limit 100000", "--lock-timeout 5000 list --json --status open --limit 100000"},
		},
		{
			name: "malformed br ready JSON", mode: "malformed_ready",
			wantDiagnostic:    "parse bead labels (br ready",
			wantBRInvocations: []string{"--lock-timeout 5000 ready --json --limit 100000"},
		},
		{
			name: "malformed br list JSON", mode: "malformed_list",
			wantDiagnostic:    "parse bead labels (br list",
			wantBRInvocations: []string{"--lock-timeout 5000 ready --json --limit 100000", "--lock-timeout 5000 list --json --status open --limit 100000"},
		},
		{
			name: "plan item absent from every tracker surface", mode: "absent",
			wantDiagnostic: "absent from br ready, br list --status open, and br list --all",
			wantBRInvocations: []string{
				"--lock-timeout 5000 ready --json --limit 100000",
				"--lock-timeout 5000 list --json --status open --limit 100000",
				"--lock-timeout 5000 list --json --all --limit 100000",
			},
		},
		{
			name: "plan item closed since the bv snapshot is skipped", mode: "stale_closed",
			wantStaleSkip: true,
			wantBRInvocations: []string{
				"--lock-timeout 5000 ready --json --limit 100000",
				"--lock-timeout 5000 list --json --status open --limit 100000",
				"--lock-timeout 5000 list --json --all --limit 100000",
			},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			title := fmt.Sprintf("Label enrichment fail-closed case %d", index)
			beadID := fixture.createBead(t, title)
			beforeBead := fixture.mustBR(t, "show", beadID, "--json")
			planPath := filepath.Join(fixture.root, fmt.Sprintf("label-enrichment-plan-%d.json", index))
			planPayload, err := json.Marshal(map[string]any{
				"plan": map[string]any{
					"tracks": []map[string]any{{
						"track_id": "label-enrichment-track",
						"items": []map[string]any{{
							"id": beadID, "title": title, "status": "open", "priority": 1,
						}},
					}},
				},
			})
			if err != nil {
				t.Fatalf("marshal label-enrichment plan payload: %v", err)
			}
			if err := os.WriteFile(planPath, planPayload, 0o600); err != nil {
				t.Fatalf("write label-enrichment plan payload: %v", err)
			}
			marker := fmt.Sprintf("NTM_LABEL_ENRICHMENT_MUST_NOT_DISPATCH_%d_%d", index, time.Now().UnixNano())
			templatePath := filepath.Join(fixture.projectDir, fmt.Sprintf("label-enrichment-template-%d.txt", index))
			if err := os.WriteFile(templatePath, []byte(marker+"::{BEAD_ID}::{TITLE}"), 0o600); err != nil {
				t.Fatalf("write label-enrichment template: %v", err)
			}
			brTrace := filepath.Join(fixture.root, fmt.Sprintf("label-enrichment-br-%d.trace", index))
			mail := newAtomicAssignmentMailStub(fixture.projectDir)
			defer mail.close()
			env := mail.env()
			env["PATH"] = binDir + string(os.PathListSeparator) + atomicAssignmentEnvValue(fixture.env, "PATH")
			env["NTM_E2E_TRIAGE_PAYLOAD"] = triagePath
			env["NTM_E2E_PLAN_PAYLOAD"] = planPath
			env["NTM_E2E_BR_TRACE"] = brTrace
			env["NTM_E2E_BR_MODE"] = tc.mode
			env["NTM_E2E_BEAD_ID"] = beadID
			result := fixture.runNTM(t, env,
				"--json", "assign", fixture.session,
				"--repo="+fixture.projectDir,
				"--auto",
				"--limit=1",
				"--cod-only",
				"--template=custom",
				"--template-file="+templatePath,
				"--reserve-files=true",
				"--timeout=15s",
				"--quiet",
			)
			if tc.wantStaleSkip {
				// A plan row that Beads reports closed is satisfied, not an
				// error: the run must succeed cleanly with zero assignments.
				if result.exitCode != 0 || len(bytes.TrimSpace(result.stderr)) != 0 {
					t.Fatalf("stale-skip exit=%d stdout=%s stderr=%s, want clean success", result.exitCode, result.stdout, result.stderr)
				}
				var envelope struct {
					Success bool `json:"success"`
					Summary struct {
						Assigned int `json:"assigned"`
						Failed   int `json:"failed"`
					} `json:"summary"`
				}
				decodeAtomicAssignmentJSON(t, result.stdout, &envelope)
				if !envelope.Success || envelope.Summary.Assigned != 0 || envelope.Summary.Failed != 0 {
					t.Fatalf("stale-skip envelope=%+v raw=%s", envelope, result.stdout)
				}
			} else {
				code, message := assertAtomicAssignmentSingleFailureJSON(t, result)
				if code != "ASSIGN_ERROR" || !strings.Contains(message, "actionable bead labels could not be verified") ||
					!strings.Contains(message, tc.wantDiagnostic) {
					t.Fatalf("label-enrichment failure code=%q message=%q, want ASSIGN_ERROR containing %q", code, message, tc.wantDiagnostic)
				}
			}

			trace, err := os.ReadFile(brTrace)
			if err != nil {
				t.Fatalf("read label-enrichment br trace: %v", err)
			}
			invocations := strings.Split(strings.TrimSpace(string(trace)), "\n")
			if len(invocations) != len(tc.wantBRInvocations) {
				t.Fatalf("br invocations=%q, want exactly %q", invocations, tc.wantBRInvocations)
			}
			for invocationIndex, want := range tc.wantBRInvocations {
				if !strings.HasPrefix(invocations[invocationIndex], want) {
					t.Fatalf("br invocation %d=%q, want prefix %q", invocationIndex, invocations[invocationIndex], want)
				}
			}
			afterBead := fixture.mustBR(t, "show", beadID, "--json")
			if !bytes.Equal(bytes.TrimSpace(afterBead), bytes.TrimSpace(beforeBead)) {
				t.Fatalf("label-enrichment failure mutated Beads state:\nbefore=%s\nafter=%s", beforeBead, afterBead)
			}
			fixture.assertBead(t, beadID, "open", "")
			fixture.assertLedgerReplicasHaveNoAssignment(t, beadID)
			fixture.assertMarkerCounts(t, marker, map[int]int{0: 0, 1: 0})
			assertAtomicAssignmentMailUntouched(t, mail.snapshot())
		})
	}
}

func TestE2EAtomicAssignmentOverlappingTriagePlanUsesLiveOperatorGateBuiltProcess(t *testing.T) {
	CommonE2EPrerequisites(t)
	testutil.RequireTmuxThrottled(t)

	fixture := newAtomicAssignmentCLIFixture(t)
	mail := newAtomicAssignmentMailStub(fixture.projectDir)
	defer mail.close()
	const (
		title     = "Overlapping triage and plan item requires live approval"
		gateLabel = "production-approval"
	)
	beadID := fixture.createBead(t, title)
	fixture.mustBR(t, "update", beadID, "--add-label="+gateLabel, "--json")
	beforeBead := fixture.mustBR(t, "show", beadID, "--json")

	projectConfigDir := filepath.Join(fixture.projectDir, ".ntm")
	if err := os.MkdirAll(projectConfigDir, 0o700); err != nil {
		t.Fatalf("create overlap project config directory: %v", err)
	}
	projectConfig := []byte("[assign]\noperator_gated_labels = [\"" + gateLabel + "\"]\n")
	projectConfigPath := filepath.Join(projectConfigDir, "config.toml")
	if err := os.WriteFile(projectConfigPath, projectConfig, 0o600); err != nil {
		t.Fatalf("write overlap project gate config: %v", err)
	}

	binDir := filepath.Join(fixture.root, "overlap-live-gate-bin")
	if err := os.MkdirAll(binDir, 0o700); err != nil {
		t.Fatalf("create overlap fixture bin directory: %v", err)
	}
	triagePath := filepath.Join(fixture.root, "overlap-live-gate-triage.json")
	triagePayload, err := json.Marshal(map[string]any{
		"triage": map[string]any{
			"recommendations": []map[string]any{{
				"id": beadID, "title": title, "type": "task", "status": "open",
				"priority": 1, "score": 100, "action": "assign", "reasons": []string{}, "labels": []string{},
			}},
		},
	})
	if err != nil {
		t.Fatalf("marshal overlapping stale triage payload: %v", err)
	}
	if err := os.WriteFile(triagePath, triagePayload, 0o600); err != nil {
		t.Fatalf("write overlapping stale triage payload: %v", err)
	}
	planPath := filepath.Join(fixture.root, "overlap-live-gate-plan.json")
	planPayload, err := json.Marshal(map[string]any{
		"plan": map[string]any{
			"tracks": []map[string]any{{
				"track_id": "overlap-live-gate-track",
				"items": []map[string]any{{
					"id": beadID, "title": title, "status": "open", "priority": 1,
				}},
			}},
		},
	})
	if err != nil {
		t.Fatalf("marshal overlapping plan payload: %v", err)
	}
	if err := os.WriteFile(planPath, planPayload, 0o600); err != nil {
		t.Fatalf("write overlapping plan payload: %v", err)
	}
	bvScript := strings.Join([]string{
		"#!/bin/sh",
		`case "$1" in`,
		`  --robot-triage) cat "$NTM_E2E_TRIAGE_PAYLOAD" ;;`,
		`  --robot-plan) cat "$NTM_E2E_PLAN_PAYLOAD" ;;`,
		`  --robot-insights) printf '%s\n' '{"Cycles":[]}' ;;`,
		`  *) printf 'unexpected bv args: %s\n' "$*" >&2; exit 64 ;;`,
		"esac",
		"",
	}, "\n")
	if err := os.WriteFile(filepath.Join(binDir, "bv"), []byte(bvScript), 0o700); err != nil {
		t.Fatalf("write overlapping bv fixture: %v", err)
	}
	brTrace := filepath.Join(fixture.root, "overlap-live-gate-br.trace")
	brScript := strings.Join([]string{
		"#!/bin/sh",
		"trace=" + tmux.ShellQuote(brTrace),
		"real_br=" + tmux.ShellQuote(fixture.brPath),
		`printf '%s\n' "$*" >> "$trace"`,
		`exec "$real_br" "$@"`,
		"",
	}, "\n")
	if err := os.WriteFile(filepath.Join(binDir, "br"), []byte(brScript), 0o700); err != nil {
		t.Fatalf("write overlapping br fixture: %v", err)
	}

	fixture.primePaneForSafeDispatch(t, 0)
	time.Sleep(5500 * time.Millisecond)
	marker := fmt.Sprintf("NTM_OVERLAPPING_LIVE_GATE_MUST_NOT_DISPATCH_%d", time.Now().UnixNano())
	templatePath := filepath.Join(fixture.projectDir, "overlap-live-gate-template.txt")
	if err := os.WriteFile(templatePath, []byte(marker+"::{BEAD_ID}::{TITLE}"), 0o600); err != nil {
		t.Fatalf("write overlapping gate template: %v", err)
	}
	env := mail.env()
	env["PATH"] = binDir + string(os.PathListSeparator) + atomicAssignmentEnvValue(fixture.env, "PATH")
	env["NTM_E2E_TRIAGE_PAYLOAD"] = triagePath
	env["NTM_E2E_PLAN_PAYLOAD"] = planPath
	result := fixture.runNTM(t, env,
		"--json", "assign", fixture.session,
		"--repo="+fixture.projectDir,
		"--auto",
		"--limit=1",
		"--cod-only",
		"--template=custom",
		"--template-file="+templatePath,
		"--reserve-files=true",
		"--timeout=15s",
		"--quiet",
	)
	if result.exitCode != 0 || len(bytes.TrimSpace(result.stderr)) != 0 {
		t.Fatalf("overlapping live gate exit=%d stdout=%s stderr=%s", result.exitCode, result.stdout, result.stderr)
	}
	var envelope struct {
		Success bool `json:"success"`
		Data    *struct {
			Assignments []struct {
				BeadID string `json:"bead_id"`
			} `json:"assignments"`
			Skipped []struct {
				BeadID string `json:"bead_id"`
				Reason string `json:"reason"`
			} `json:"skipped"`
			Summary struct {
				AssignedCount int `json:"assigned_count"`
				SkippedCount  int `json:"skipped_count"`
			} `json:"summary"`
		} `json:"data"`
		Error *struct {
			Code    string `json:"code"`
			Message string `json:"message"`
		} `json:"error"`
	}
	decodeAtomicAssignmentJSON(t, result.stdout, &envelope)
	if !envelope.Success || envelope.Error != nil || envelope.Data == nil ||
		envelope.Data.Summary.AssignedCount != 0 ||
		len(envelope.Data.Assignments) != 0 || len(envelope.Data.Skipped) != 1 ||
		envelope.Data.Skipped[0].BeadID != beadID || envelope.Data.Skipped[0].Reason != "operator_gated" {
		t.Fatalf("overlapping live gate envelope = %+v", envelope)
	}
	trace, err := os.ReadFile(brTrace)
	if err != nil {
		t.Fatalf("read overlapping br trace: %v", err)
	}
	for _, want := range []string{
		"ready --json --limit 100000",
		"list --json --status open --limit 100000",
	} {
		if !bytes.Contains(trace, []byte(want)) {
			t.Fatalf("overlapping live gate omitted authoritative label lookup %q:\n%s", want, trace)
		}
	}
	afterBead := fixture.mustBR(t, "show", beadID, "--json")
	if !bytes.Equal(bytes.TrimSpace(afterBead), bytes.TrimSpace(beforeBead)) {
		t.Fatalf("overlapping live gate mutated Beads state:\nbefore=%s\nafter=%s", beforeBead, afterBead)
	}
	afterConfig, err := os.ReadFile(projectConfigPath)
	if err != nil {
		t.Fatalf("read overlapping project config after assignment: %v", err)
	}
	if !bytes.Equal(afterConfig, projectConfig) {
		t.Fatalf("overlapping live gate mutated project config:\nbefore=%s\nafter=%s", projectConfig, afterConfig)
	}
	fixture.assertBead(t, beadID, "open", "")
	fixture.assertLedgerReplicasHaveNoAssignment(t, beadID)
	fixture.assertMarkerCounts(t, marker, map[int]int{0: 0, 1: 0})
	assertAtomicAssignmentMailUntouched(t, mail.snapshot())
}

func TestE2EAtomicAssignmentPlanIntegrityFailuresFailClosedBuiltProcess(t *testing.T) {
	CommonE2EPrerequisites(t)
	testutil.RequireTmuxThrottled(t)

	fixture := newAtomicAssignmentCLIFixture(t)
	binDir := filepath.Join(fixture.root, "plan-integrity-bin")
	if err := os.MkdirAll(binDir, 0o700); err != nil {
		t.Fatalf("create plan-integrity bin directory: %v", err)
	}
	bvScript := strings.Join([]string{
		"#!/bin/sh",
		`printf '%s\n' "$*" >> "$NTM_E2E_BV_TRACE"`,
		`case "$1" in`,
		`  --robot-triage) cat "$NTM_E2E_TRIAGE_PAYLOAD" ;;`,
		`  --robot-plan)`,
		`    case "$NTM_E2E_PLAN_MODE" in`,
		`      command_failure) printf 'injected bv plan failure\n' >&2; exit 70 ;;`,
		`      malformed) printf '{\n' ;;`,
		`      missing_plan) printf '{}\n' ;;`,
		`      missing_tracks) printf '{"plan":{}}\n' ;;`,
		`      blank_id) cat "$NTM_E2E_PLAN_PAYLOAD" ;;`,
		`      *) printf 'unexpected plan mode: %s\n' "$NTM_E2E_PLAN_MODE" >&2; exit 64 ;;`,
		`    esac ;;`,
		`  *) printf 'unexpected bv args: %s\n' "$*" >&2; exit 64 ;;`,
		"esac",
		"",
	}, "\n")
	if err := os.WriteFile(filepath.Join(binDir, "bv"), []byte(bvScript), 0o700); err != nil {
		t.Fatalf("write plan-integrity bv fixture: %v", err)
	}
	brScript := strings.Join([]string{
		"#!/bin/sh",
		`printf '%s\n' "$*" >> "$NTM_E2E_BR_TRACE"`,
		"real_br=" + tmux.ShellQuote(fixture.brPath),
		`exec "$real_br" "$@"`,
		"",
	}, "\n")
	if err := os.WriteFile(filepath.Join(binDir, "br"), []byte(brScript), 0o700); err != nil {
		t.Fatalf("write plan-integrity br fixture: %v", err)
	}

	fixture.primePaneForSafeDispatch(t, 0)
	time.Sleep(5500 * time.Millisecond)
	for index, tc := range []struct {
		name           string
		mode           string
		wantDiagnostic string
	}{
		{name: "bv plan command failure", mode: "command_failure", wantDiagnostic: "load bv --robot-plan"},
		{name: "malformed bv plan JSON", mode: "malformed", wantDiagnostic: "parsing plan"},
		{name: "missing plan object", mode: "missing_plan", wantDiagnostic: "missing plan object"},
		{name: "missing plan tracks array", mode: "missing_tracks", wantDiagnostic: "missing plan.tracks array"},
		{name: "blank plan item ID", mode: "blank_id", wantDiagnostic: "track 0 item 0 has an empty id"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			title := fmt.Sprintf("Plan integrity failure case %d", index)
			beadID := fixture.createBead(t, title)
			beforeBead := fixture.mustBR(t, "show", beadID, "--json")
			triagePath := filepath.Join(fixture.root, fmt.Sprintf("plan-integrity-triage-%d.json", index))
			triagePayload, err := json.Marshal(map[string]any{
				"triage": map[string]any{
					"recommendations": []map[string]any{{
						"id": beadID, "title": title, "type": "task", "status": "open",
						"priority": 1, "score": 100, "action": "assign", "reasons": []string{},
					}},
				},
			})
			if err != nil {
				t.Fatalf("marshal plan-integrity triage payload: %v", err)
			}
			if err := os.WriteFile(triagePath, triagePayload, 0o600); err != nil {
				t.Fatalf("write plan-integrity triage payload: %v", err)
			}
			planPath := filepath.Join(fixture.root, fmt.Sprintf("plan-integrity-plan-%d.json", index))
			planPayload, err := json.Marshal(map[string]any{
				"plan": map[string]any{
					"tracks": []map[string]any{{
						"track_id": "plan-integrity-track",
						"items": []map[string]any{{
							"id": " ", "title": title, "status": "open", "priority": 1,
						}},
					}},
				},
			})
			if err != nil {
				t.Fatalf("marshal blank-ID plan payload: %v", err)
			}
			if err := os.WriteFile(planPath, planPayload, 0o600); err != nil {
				t.Fatalf("write plan-integrity plan payload: %v", err)
			}
			marker := fmt.Sprintf("NTM_PLAN_INTEGRITY_MUST_NOT_DISPATCH_%d_%d", index, time.Now().UnixNano())
			templatePath := filepath.Join(fixture.projectDir, fmt.Sprintf("plan-integrity-template-%d.txt", index))
			if err := os.WriteFile(templatePath, []byte(marker+"::{BEAD_ID}::{TITLE}"), 0o600); err != nil {
				t.Fatalf("write plan-integrity template: %v", err)
			}
			bvTrace := filepath.Join(fixture.root, fmt.Sprintf("plan-integrity-bv-%d.trace", index))
			brTrace := filepath.Join(fixture.root, fmt.Sprintf("plan-integrity-br-%d.trace", index))
			mail := newAtomicAssignmentMailStub(fixture.projectDir)
			defer mail.close()
			env := mail.env()
			env["PATH"] = binDir + string(os.PathListSeparator) + atomicAssignmentEnvValue(fixture.env, "PATH")
			env["NTM_E2E_TRIAGE_PAYLOAD"] = triagePath
			env["NTM_E2E_PLAN_PAYLOAD"] = planPath
			env["NTM_E2E_PLAN_MODE"] = tc.mode
			env["NTM_E2E_BV_TRACE"] = bvTrace
			env["NTM_E2E_BR_TRACE"] = brTrace
			result := fixture.runNTM(t, env,
				"--json", "assign", fixture.session,
				"--repo="+fixture.projectDir,
				"--auto",
				"--limit=1",
				"--cod-only",
				"--template=custom",
				"--template-file="+templatePath,
				"--reserve-files=true",
				"--timeout=15s",
				"--quiet",
			)
			code, message := assertAtomicAssignmentSingleFailureJSON(t, result)
			if code != "ASSIGN_ERROR" ||
				!strings.Contains(message, "automated assignment stopped because actionable work could not be verified") ||
				!strings.Contains(message, "actionable bv plan could not be verified") ||
				!strings.Contains(message, tc.wantDiagnostic) {
				t.Fatalf("plan-integrity failure code=%q message=%q, want ASSIGN_ERROR containing %q", code, message, tc.wantDiagnostic)
			}
			trace, err := os.ReadFile(bvTrace)
			if err != nil {
				t.Fatalf("read plan-integrity bv trace: %v", err)
			}
			invocations := strings.Split(strings.TrimSpace(string(trace)), "\n")
			if !reflect.DeepEqual(invocations, []string{"--robot-triage", "--robot-plan"}) {
				t.Fatalf("plan-integrity bv invocations=%q, want triage followed by plan exactly once", invocations)
			}
			if brCalls, err := os.ReadFile(brTrace); err == nil && bytes.Contains(brCalls, []byte("update")) {
				t.Fatalf("plan-integrity failure reached mutating br command: %s", brCalls)
			} else if err != nil && !os.IsNotExist(err) {
				t.Fatalf("read plan-integrity br trace: %v", err)
			}
			afterBead := fixture.mustBR(t, "show", beadID, "--json")
			if !bytes.Equal(bytes.TrimSpace(afterBead), bytes.TrimSpace(beforeBead)) {
				t.Fatalf("plan-integrity failure mutated Beads state:\nbefore=%s\nafter=%s", beforeBead, afterBead)
			}
			fixture.assertBead(t, beadID, "open", "")
			fixture.assertLedgerReplicasHaveNoAssignment(t, beadID)
			fixture.assertMarkerCounts(t, marker, map[int]int{0: 0, 1: 0})
			assertAtomicAssignmentMailUntouched(t, mail.snapshot())
		})
	}
}

func TestE2EAtomicAssignmentSameSessionInvalidPolicyIsMutationFreeBuiltProcess(t *testing.T) {
	CommonE2EPrerequisites(t)
	testutil.RequireTmuxThrottled(t)

	type treeEntry struct {
		Mode os.FileMode
		Data []byte
	}
	snapshotTree := func(t *testing.T, root string) map[string]treeEntry {
		t.Helper()
		snapshot := make(map[string]treeEntry)
		if err := filepath.WalkDir(root, func(path string, entry os.DirEntry, walkErr error) error {
			if walkErr != nil {
				return walkErr
			}
			relative, err := filepath.Rel(root, path)
			if err != nil {
				return err
			}
			info, err := entry.Info()
			if err != nil {
				return err
			}
			var data []byte
			if info.Mode().IsRegular() {
				data, err = os.ReadFile(path)
				if err != nil {
					return err
				}
			}
			snapshot[relative] = treeEntry{Mode: info.Mode(), Data: append([]byte(nil), data...)}
			return nil
		}); err != nil {
			t.Fatalf("snapshot session storage %s: %v", root, err)
		}
		return snapshot
	}

	for index, tc := range []struct {
		name          string
		projectConfig bool
	}{
		{name: "invalid selected global policy"},
		{name: "invalid target project policy", projectConfig: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			fixture := newAtomicAssignmentCLIFixture(t)
			beadID := fixture.createBead(t, fmt.Sprintf("Same-session invalid policy case %d", index))
			beforeBead := fixture.mustBR(t, "show", beadID, "--json")
			mail := newAtomicAssignmentMailStub(fixture.projectDir)
			defer mail.close()

			sessionStorage := filepath.Join(fixture.homeDir, ".ntm", "sessions", fixture.session)
			if err := os.MkdirAll(filepath.Join(sessionStorage, "operator-state"), 0o700); err != nil {
				t.Fatalf("create same-session storage fixture: %v", err)
			}
			storageSentinel := []byte("preserve-session-storage-byte-for-byte\r\n")
			if err := os.WriteFile(filepath.Join(sessionStorage, "operator-state", "sentinel.bin"), storageSentinel, 0o600); err != nil {
				t.Fatalf("write same-session storage sentinel: %v", err)
			}
			beforeStorage := snapshotTree(t, sessionStorage)

			tmuxConfPath := filepath.Join(fixture.homeDir, ".tmux.conf")
			tmuxConf := []byte("# preserve this exact tmux config\r\nbind-key -n F11 display-message keep-existing-binding\r\n")
			if err := os.WriteFile(tmuxConfPath, tmuxConf, 0o600); err != nil {
				t.Fatalf("write same-session tmux config: %v", err)
			}
			fixture.mustTMUX(t, "bind-key", "-n", "F12", "display-message", "NTM_E2E_KEEP_EXISTING_F12")
			ctx, cancel := context.WithTimeout(context.Background(), defaultTmuxSetupTimeout)
			beforeBinding, err := fixture.tmuxOutput(ctx, "list-keys", "-T", "root", "F12")
			cancel()
			if err != nil {
				t.Fatalf("capture same-session F12 binding before invalid policy: %v", err)
			}
			ctx, cancel = context.WithTimeout(context.Background(), defaultTmuxSetupTimeout)
			tmuxEnvValue, err := fixture.tmuxOutput(ctx, "display-message", "-p", "-t", fixture.panes[0].ID, "#{socket_path},#{pid},0")
			cancel()
			if err != nil {
				t.Fatalf("resolve private same-session TMUX value: %v", err)
			}

			configPath := filepath.Join(fixture.root, fmt.Sprintf("same-session-invalid-%d.toml", index))
			if tc.projectConfig {
				projectConfigDir := filepath.Join(fixture.projectDir, ".ntm")
				if err := os.MkdirAll(projectConfigDir, 0o700); err != nil {
					t.Fatalf("create same-session project config directory: %v", err)
				}
				configPath = filepath.Join(projectConfigDir, "config.toml")
			}
			invalidConfig := []byte("[assign\noperator_gated_labels = [\"production-approval\"]\n")
			if err := os.WriteFile(configPath, invalidConfig, 0o600); err != nil {
				t.Fatalf("write same-session invalid policy: %v", err)
			}
			marker := fmt.Sprintf("NTM_SAME_SESSION_INVALID_POLICY_MUST_NOT_DISPATCH_%d_%d", index, time.Now().UnixNano())
			templatePath := filepath.Join(fixture.projectDir, fmt.Sprintf("same-session-invalid-template-%d.txt", index))
			if err := os.WriteFile(templatePath, []byte(marker+"::{BEAD_ID}::{TITLE}"), 0o600); err != nil {
				t.Fatalf("write same-session invalid-policy template: %v", err)
			}
			env := mail.env()
			env["TMUX"] = strings.TrimSpace(string(tmuxEnvValue))
			env["TMUX_PANE"] = fixture.panes[0].ID
			args := make([]string, 0, 16)
			if !tc.projectConfig {
				args = append(args, "--config="+configPath)
			}
			args = append(args,
				"--json", "assign", fixture.session,
				"--repo="+fixture.projectDir,
				"--watch",
				"--dry-run",
				"--stop-when-done",
				"--limit=1",
				"--cod-only",
				"--template=custom",
				"--template-file="+templatePath,
				"--reserve-files=true",
				"--timeout=15s",
				"--quiet",
			)
			result := fixture.runNTM(t, env, args...)
			code, message := assertAtomicAssignmentSingleFailureJSON(t, result)
			if code != "INVALID_FLAG" || !strings.Contains(strings.ToLower(message), "config") {
				t.Fatalf("same-session invalid policy code=%q message=%q, want INVALID_FLAG config diagnostic", code, message)
			}

			afterStorage := snapshotTree(t, sessionStorage)
			if !reflect.DeepEqual(afterStorage, beforeStorage) {
				t.Fatalf("invalid policy mutated same-session storage:\nbefore=%#v\nafter=%#v", beforeStorage, afterStorage)
			}
			afterTmuxConf, err := os.ReadFile(tmuxConfPath)
			if err != nil {
				t.Fatalf("read same-session tmux config after invalid policy: %v", err)
			}
			if !bytes.Equal(afterTmuxConf, tmuxConf) {
				t.Fatalf("invalid policy mutated ~/.tmux.conf:\nbefore=%q\nafter=%q", tmuxConf, afterTmuxConf)
			}
			ctx, cancel = context.WithTimeout(context.Background(), defaultTmuxSetupTimeout)
			afterBinding, err := fixture.tmuxOutput(ctx, "list-keys", "-T", "root", "F12")
			cancel()
			if err != nil {
				t.Fatalf("capture same-session F12 binding after invalid policy: %v", err)
			}
			if !bytes.Equal(afterBinding, beforeBinding) {
				t.Fatalf("invalid policy mutated private tmux F12 binding:\nbefore=%s\nafter=%s", beforeBinding, afterBinding)
			}
			afterConfig, err := os.ReadFile(configPath)
			if err != nil {
				t.Fatalf("read same-session invalid config after rejection: %v", err)
			}
			if !bytes.Equal(afterConfig, invalidConfig) {
				t.Fatalf("invalid policy mutated its config:\nbefore=%s\nafter=%s", invalidConfig, afterConfig)
			}
			afterBead := fixture.mustBR(t, "show", beadID, "--json")
			if !bytes.Equal(bytes.TrimSpace(afterBead), bytes.TrimSpace(beforeBead)) {
				t.Fatalf("same-session invalid policy mutated Beads state:\nbefore=%s\nafter=%s", beforeBead, afterBead)
			}
			fixture.assertBead(t, beadID, "open", "")
			fixture.assertMarkerCounts(t, marker, map[int]int{0: 0, 1: 0})
			assertAtomicAssignmentMailUntouched(t, mail.snapshot())
		})
	}
}

func TestE2EAtomicAssignmentWatchStopWhenDoneTreatsGatedQueuesAsDrainedBuiltProcess(t *testing.T) {
	CommonE2EPrerequisites(t)
	testutil.RequireTmuxThrottled(t)

	fixture := newAtomicAssignmentCLIFixture(t)
	const gateLabel = "deployment-approval"
	projectConfigDir := filepath.Join(fixture.projectDir, ".ntm")
	if err := os.MkdirAll(projectConfigDir, 0o700); err != nil {
		t.Fatalf("create gated watch project config directory: %v", err)
	}
	projectConfig := []byte("[assign]\noperator_gated_labels = [\"" + gateLabel + "\"]\n")
	if err := os.WriteFile(filepath.Join(projectConfigDir, "config.toml"), projectConfig, 0o600); err != nil {
		t.Fatalf("write gated watch project config: %v", err)
	}

	const taskTitle = "Gated task is drained for watch completion"
	taskID := fixture.createBead(t, taskTitle)
	fixture.mustBR(t, "update", taskID, "--add-label="+gateLabel, "--json")
	const epicTitle = "Plan-only gated epic is drained for watch completion"
	epicOutput := fixture.mustBR(t, "create", epicTitle, "--type=epic", "--priority=1", "--silent")
	epicID := strings.TrimSpace(string(epicOutput))
	if epicID == "" || strings.ContainsAny(epicID, " \t\r\n") {
		t.Fatalf("unexpected gated watch epic ID %q", epicOutput)
	}
	fixture.mustBR(t, "update", epicID, "--add-label="+gateLabel, "--json")

	binDir := filepath.Join(fixture.root, "gated-watch-stop-bin")
	if err := os.MkdirAll(binDir, 0o700); err != nil {
		t.Fatalf("create gated watch fixture bin directory: %v", err)
	}
	bvScript := strings.Join([]string{
		"#!/bin/sh",
		`printf '%s\n' "$*" >> "$NTM_E2E_BV_TRACE"`,
		`case "$1" in`,
		`  --robot-triage) cat "$NTM_E2E_TRIAGE_PAYLOAD" ;;`,
		`  --robot-plan) cat "$NTM_E2E_PLAN_PAYLOAD" ;;`,
		`  --robot-insights) printf '%s\n' '{"Cycles":[]}' ;;`,
		`  *) printf 'unexpected bv args: %s\n' "$*" >&2; exit 64 ;;`,
		"esac",
		"",
	}, "\n")
	if err := os.WriteFile(filepath.Join(binDir, "bv"), []byte(bvScript), 0o700); err != nil {
		t.Fatalf("write gated watch bv fixture: %v", err)
	}
	brScript := strings.Join([]string{
		"#!/bin/sh",
		`printf '%s\n' "$*" >> "$NTM_E2E_BR_TRACE"`,
		"real_br=" + tmux.ShellQuote(fixture.brPath),
		`exec "$real_br" "$@"`,
		"",
	}, "\n")
	if err := os.WriteFile(filepath.Join(binDir, "br"), []byte(brScript), 0o700); err != nil {
		t.Fatalf("write gated watch br fixture: %v", err)
	}

	fixture.primePaneForSafeDispatch(t, 0)
	time.Sleep(5500 * time.Millisecond)
	for index, tc := range []struct {
		name        string
		beadID      string
		title       string
		includeLive bool
	}{
		{name: "triage and plan gated task", beadID: taskID, title: taskTitle, includeLive: true},
		{name: "plan only gated epic", beadID: epicID, title: epicTitle},
	} {
		t.Run(tc.name, func(t *testing.T) {
			beforeBead := fixture.mustBR(t, "show", tc.beadID, "--json")
			recommendations := []map[string]any{}
			if tc.includeLive {
				recommendations = append(recommendations, map[string]any{
					"id": tc.beadID, "title": tc.title, "type": "task", "status": "open",
					"priority": 1, "score": 100, "action": "assign", "reasons": []string{}, "labels": []string{},
				})
			}
			triagePath := filepath.Join(fixture.root, fmt.Sprintf("gated-watch-triage-%d.json", index))
			triagePayload, err := json.Marshal(map[string]any{
				"triage": map[string]any{"recommendations": recommendations},
			})
			if err != nil {
				t.Fatalf("marshal gated watch triage payload: %v", err)
			}
			if err := os.WriteFile(triagePath, triagePayload, 0o600); err != nil {
				t.Fatalf("write gated watch triage payload: %v", err)
			}
			planPath := filepath.Join(fixture.root, fmt.Sprintf("gated-watch-plan-%d.json", index))
			planPayload, err := json.Marshal(map[string]any{
				"plan": map[string]any{
					"tracks": []map[string]any{{
						"track_id": "gated-watch-track",
						"items": []map[string]any{{
							"id": tc.beadID, "title": tc.title, "status": "open", "priority": 1,
						}},
					}},
				},
			})
			if err != nil {
				t.Fatalf("marshal gated watch plan payload: %v", err)
			}
			if err := os.WriteFile(planPath, planPayload, 0o600); err != nil {
				t.Fatalf("write gated watch plan payload: %v", err)
			}
			marker := fmt.Sprintf("NTM_GATED_WATCH_MUST_NOT_DISPATCH_%d_%d", index, time.Now().UnixNano())
			templatePath := filepath.Join(fixture.projectDir, fmt.Sprintf("gated-watch-template-%d.txt", index))
			if err := os.WriteFile(templatePath, []byte(marker+"::{BEAD_ID}::{TITLE}"), 0o600); err != nil {
				t.Fatalf("write gated watch template: %v", err)
			}
			bvTrace := filepath.Join(fixture.root, fmt.Sprintf("gated-watch-bv-%d.trace", index))
			brTrace := filepath.Join(fixture.root, fmt.Sprintf("gated-watch-br-%d.trace", index))
			mail := newAtomicAssignmentMailStub(fixture.projectDir)
			defer mail.close()
			env := mail.env()
			env["PATH"] = binDir + string(os.PathListSeparator) + atomicAssignmentEnvValue(fixture.env, "PATH")
			env["NTM_E2E_TRIAGE_PAYLOAD"] = triagePath
			env["NTM_E2E_PLAN_PAYLOAD"] = planPath
			env["NTM_E2E_BV_TRACE"] = bvTrace
			env["NTM_E2E_BR_TRACE"] = brTrace
			result := fixture.runNTM(t, env,
				"assign", fixture.session,
				"--repo="+fixture.projectDir,
				"--watch",
				"--stop-when-done",
				"--watch-interval=50ms",
				"--limit=1",
				"--cod-only",
				"--template=custom",
				"--template-file="+templatePath,
				"--reserve-files=true",
				"--timeout=15s",
				"--quiet",
			)
			if result.exitCode != 0 || len(bytes.TrimSpace(result.stderr)) != 0 {
				t.Fatalf("gated watch stop exit=%d stdout=%s stderr=%s", result.exitCode, result.stdout, result.stderr)
			}
			bvCalls, err := os.ReadFile(bvTrace)
			if err != nil {
				t.Fatalf("read gated watch bv trace: %v", err)
			}
			for _, want := range []string{"--robot-triage", "--robot-plan", "--robot-insights"} {
				if !bytes.Contains(bvCalls, []byte(want)) {
					t.Fatalf("gated watch dependency calls=%q, want %s", bvCalls, want)
				}
			}
			brCalls, err := os.ReadFile(brTrace)
			if err != nil {
				t.Fatalf("read gated watch br trace: %v", err)
			}
			for _, want := range []string{
				"ready --json --limit 100000",
				"list --json --status open --limit 100000",
			} {
				if !bytes.Contains(brCalls, []byte(want)) {
					t.Fatalf("gated watch authoritative label calls=%q, want %s", brCalls, want)
				}
			}
			afterBead := fixture.mustBR(t, "show", tc.beadID, "--json")
			if !bytes.Equal(bytes.TrimSpace(afterBead), bytes.TrimSpace(beforeBead)) {
				t.Fatalf("gated watch stop mutated Beads state:\nbefore=%s\nafter=%s", beforeBead, afterBead)
			}
			fixture.assertBead(t, tc.beadID, "open", "")
			fixture.assertLedgerReplicasHaveNoAssignment(t, tc.beadID)
			fixture.assertMarkerCounts(t, marker, map[int]int{0: 0, 1: 0})
			assertAtomicAssignmentMailUntouched(t, mail.snapshot())
		})
	}
}

func TestE2EAtomicAssignmentRepositoryScopeBuiltProcess(t *testing.T) {
	CommonE2EPrerequisites(t)
	testutil.RequireTmuxThrottled(t)

	fixture := newAtomicAssignmentCLIFixture(t)
	overrideRepo := filepath.Join(fixture.root, "override-project")
	if err := os.MkdirAll(overrideRepo, 0o700); err != nil {
		t.Fatalf("create override repository: %v", err)
	}
	fixture.mustBRAt(t, overrideRepo, "init", "--prefix=scopeb", "--json")
	overrideBeadID := strings.TrimSpace(string(fixture.mustBRAt(t, overrideRepo,
		"create", "Authoritative CLI repository override", "--type=task", "--priority=1", "--silent")))
	if overrideBeadID == "" {
		t.Fatal("override repository bead ID is empty")
	}
	overridePrompt := fmt.Sprintf("NTM_ATOMIC_REPO_OVERRIDE_%d", time.Now().UnixNano())
	cliResult := fixture.runNTM(t, nil,
		"assign", fixture.session,
		"--repo="+overrideRepo,
		"--pane=0",
		"--beads="+overrideBeadID,
		"--prompt="+overridePrompt,
		"--reserve-files=false",
		"--force",
		"--ignore-deps",
		"--timeout=15s",
		"--json",
	)
	if cliResult.exitCode != 0 || len(bytes.TrimSpace(cliResult.stderr)) != 0 {
		t.Fatalf("authoritative --repo assignment exit=%d stdout=%s stderr=%s", cliResult.exitCode, cliResult.stdout, cliResult.stderr)
	}
	fixture.waitForMarkerCount(t, 0, overridePrompt, 1)
	overrideRecord := fixture.readLedgerAssignment(t, overrideBeadID)
	fixture.assertBeadAt(t, overrideRepo, overrideBeadID, "in_progress", overrideRecord.ClaimActor)
	fixture.assertBeadAbsentAt(t, fixture.projectDir, overrideBeadID)

	remoteBeadID := fixture.createBead(t, "Robot explicit-session repository scope")
	remoteTemplate := filepath.Join(fixture.projectDir, "atomic-remote-scope-template.txt")
	remoteTemplateBody := fmt.Sprintf("NTM_ATOMIC_REMOTE_SCOPE_%d_{bead_id}", time.Now().UnixNano())
	if err := os.WriteFile(remoteTemplate, []byte(remoteTemplateBody), 0o600); err != nil {
		t.Fatalf("write remote scope template: %v", err)
	}
	fixture.primePaneForSafeDispatch(t, 1)
	robotResult := fixture.runNTMInDir(t, overrideRepo, nil, atomicBulkArgs(fixture, remoteBeadID, remoteTemplate)...)
	if robotResult.exitCode != 0 || len(bytes.TrimSpace(robotResult.stderr)) != 0 {
		t.Fatalf("remote robot assignment exit=%d stdout=%s stderr=%s", robotResult.exitCode, robotResult.stdout, robotResult.stderr)
	}
	var robotEnvelope atomicAssignmentBulkEnvelope
	decodeAtomicAssignmentJSON(t, robotResult.stdout, &robotEnvelope)
	assertBulkAssignmentEnvelope(t, robotEnvelope, remoteBeadID, "1")
	remotePrompt := strings.ReplaceAll(remoteTemplateBody, "{bead_id}", remoteBeadID)
	fixture.waitForMarkerCount(t, 1, remotePrompt, 1)
	remoteRecord := fixture.readLedgerAssignment(t, remoteBeadID)
	fixture.assertBead(t, remoteBeadID, "in_progress", remoteRecord.ClaimActor)
	fixture.assertBeadAbsentAt(t, overrideRepo, remoteBeadID)
}

func TestE2EAtomicExplicitSessionOutsideRepositoryUsesLiveProjectBuiltProcess(t *testing.T) {
	CommonE2EPrerequisites(t)
	testutil.RequireTmuxThrottled(t)

	fixture := newAtomicAssignmentCLIFixture(t)
	mail := newAtomicAssignmentMailStub(fixture.projectDir)
	defer mail.close()
	outsideDir := filepath.Join(fixture.root, "outside-any-project")
	if err := os.MkdirAll(outsideDir, 0o700); err != nil {
		t.Fatalf("create explicit-session outside directory: %v", err)
	}

	beadID := fixture.createBead(t, "Explicit live session project resolution")
	prompt := fmt.Sprintf("NTM_ATOMIC_EXPLICIT_SESSION_PROJECT_%d", time.Now().UnixNano())
	result := fixture.runNTMInDir(t, outsideDir, mail.env(),
		"--json", "assign", fixture.session,
		"--pane="+fixture.panes[0].ID,
		"--beads="+beadID,
		"--prompt="+prompt,
		"--reserve-files=false",
		"--force",
		"--ignore-deps",
		"--timeout=60s",
	)
	if result.exitCode != 0 || len(bytes.TrimSpace(result.stderr)) != 0 {
		t.Fatalf("explicit-session cross-CWD assignment exit=%d stdout=%s stderr=%s", result.exitCode, result.stdout, result.stderr)
	}
	var envelope atomicAssignmentDirectEnvelope
	decodeAtomicAssignmentJSON(t, result.stdout, &envelope)
	assertDirectAssignmentEnvelope(t, envelope, fixture.session, beadID, prompt, fixture.panes[0])
	fixture.waitForMarkerCount(t, 0, prompt, 1)
	fixture.assertMarkerCounts(t, prompt, map[int]int{0: 1, 1: 0})

	record := fixture.readLedgerAssignment(t, beadID)
	assertAtomicAssignmentRecord(t, record, beadID, prompt, fixture.panes[0], "codex")
	fixture.assertBead(t, beadID, "in_progress", record.ClaimActor)
	fixture.assertBeadAbsentAt(t, outsideDir, beadID)
	if snapshot := mail.snapshot(); snapshot.ReserveCalls != 0 || snapshot.ReleaseCalls != 0 || snapshot.SendCalls != 0 {
		t.Fatalf("reservation-disabled cross-CWD assignment touched Agent Mail: %+v", snapshot)
	}
}

func TestE2EAtomicExplicitSessionMixedLiveRootsFailsBeforeAssignmentBuiltProcess(t *testing.T) {
	CommonE2EPrerequisites(t)
	testutil.RequireTmuxThrottled(t)

	fixture := newAtomicAssignmentCLIFixture(t)
	mail := newAtomicAssignmentMailStub(fixture.projectDir)
	defer mail.close()
	outsideDir := filepath.Join(fixture.root, "mixed-root-outside")
	otherProject := filepath.Join(fixture.root, "other-live-project")
	for _, dir := range []string{outsideDir, otherProject, filepath.Join(otherProject, ".git")} {
		if err := os.MkdirAll(dir, 0o700); err != nil {
			t.Fatalf("create mixed-root fixture directory %s: %v", dir, err)
		}
	}
	agentCommand := atomicAgentPaneCommand
	fixture.mustTMUX(t, "new-window", "-d", "-t", fixture.session+":1", "-c", otherProject, agentCommand)
	fixture.mustTMUX(t, "select-pane", "-t", fixture.session+":1.0", "-T", fixture.session+"__cod_mixed_root")

	beadID := fixture.createBead(t, "Mixed live roots must reject assignment")
	prompt := fmt.Sprintf("NTM_ATOMIC_MIXED_LIVE_ROOT_MUST_NOT_DISPATCH_%d", time.Now().UnixNano())
	beforeBead := fixture.mustBR(t, "show", beadID, "--json")
	sessionStateRoot := filepath.Dir(fixture.ledgerPath())
	beforeSessionState := snapshotAtomicAssignmentTree(t, sessionStateRoot)
	beadsRoot := filepath.Join(fixture.projectDir, ".beads")
	beforeBeads := snapshotAtomicAssignmentTree(t, beadsRoot)
	beforePanes := snapshotAtomicAssignmentPanes(t, fixture)
	beforeMail := mail.snapshot()

	result := fixture.runNTMInDir(t, outsideDir, mail.env(),
		"--json", "assign", fixture.session,
		"--pane="+fixture.panes[0].ID,
		"--beads="+beadID,
		"--prompt="+prompt,
		"--reserve-files=false",
		"--force",
		"--ignore-deps",
		"--timeout=60s",
	)
	_, message := assertAtomicAssignmentSingleFailureJSON(t, result)
	if !strings.Contains(message, "panes span multiple project roots") ||
		!strings.Contains(message, filepath.Clean(fixture.projectDir)) ||
		!strings.Contains(message, filepath.Clean(otherProject)) {
		t.Fatalf("mixed-live-root diagnostic = %q", message)
	}

	assertAtomicAssignmentTreeUnchanged(t, sessionStateRoot, beforeSessionState)
	assertAtomicAssignmentTreeUnchanged(t, beadsRoot, beforeBeads)
	assertAtomicAssignmentPanesUnchanged(t, fixture, beforePanes)
	afterBead := fixture.mustBR(t, "show", beadID, "--json")
	if !bytes.Equal(bytes.TrimSpace(afterBead), bytes.TrimSpace(beforeBead)) {
		t.Fatalf("mixed-live-root rejection mutated Beads state:\nbefore=%s\nafter=%s", beforeBead, afterBead)
	}
	fixture.assertBead(t, beadID, "open", "")
	fixture.assertLedgerReplicasHaveNoAssignment(t, beadID)
	fixture.assertMarkerCounts(t, prompt, map[int]int{0: 0, 1: 0})
	afterMail := mail.snapshot()
	if !reflect.DeepEqual(afterMail, beforeMail) {
		t.Fatalf("mixed-live-root rejection touched Agent Mail:\nbefore=%+v\nafter=%+v", beforeMail, afterMail)
	}
	assertAtomicAssignmentMailUntouched(t, afterMail)
}

func TestE2EAtomicExplicitSessionMissingLiveRootFailsBeforeAssignmentBuiltProcess(t *testing.T) {
	CommonE2EPrerequisites(t)
	testutil.RequireTmuxThrottled(t)

	fixture := newAtomicAssignmentCLIFixture(t)
	mail := newAtomicAssignmentMailStub(fixture.projectDir)
	defer mail.close()
	outsideDir := filepath.Join(fixture.root, "missing-root-outside")
	if err := os.MkdirAll(outsideDir, 0o700); err != nil {
		t.Fatalf("create missing-root outside directory: %v", err)
	}
	missingRoot := filepath.Join(fixture.root, "missing-live-project")
	if _, err := os.Stat(missingRoot); !os.IsNotExist(err) {
		t.Fatalf("missing live project path must not exist: path=%s err=%v", missingRoot, err)
	}

	wrapperDir := filepath.Join(fixture.root, "missing-root-tmux-bin")
	if err := os.MkdirAll(wrapperDir, 0o700); err != nil {
		t.Fatalf("create missing-root tmux wrapper directory: %v", err)
	}
	wrapperPath := filepath.Join(wrapperDir, "tmux")
	firedPath := filepath.Join(wrapperDir, "current-path-override-fired")
	wrapper := strings.Join([]string{
		"#!/bin/sh",
		"real_tmux=" + tmux.ShellQuote(fixture.tmuxPath),
		"missing_pane=" + tmux.ShellQuote(fixture.panes[0].ID),
		"missing_root=" + tmux.ShellQuote(missingRoot),
		"fired=" + tmux.ShellQuote(firedPath),
		"is_display=0",
		"is_missing_pane=0",
		"is_current_path=0",
		`for arg in "$@"; do`,
		`  if [ "$arg" = "display-message" ]; then is_display=1; fi`,
		`  if [ "$arg" = "$missing_pane" ]; then is_missing_pane=1; fi`,
		`  if [ "$arg" = '#{pane_current_path}' ]; then is_current_path=1; fi`,
		"done",
		`if [ "$is_display" -eq 1 ] && [ "$is_missing_pane" -eq 1 ] && [ "$is_current_path" -eq 1 ]; then`,
		`  printf 'fired\n' > "$fired"`,
		`  printf '%s\n' "$missing_root"`,
		"  exit 0",
		"fi",
		`exec "$real_tmux" "$@"`,
		"",
	}, "\n")
	if err := os.WriteFile(wrapperPath, []byte(wrapper), 0o700); err != nil {
		t.Fatalf("write missing-root tmux wrapper: %v", err)
	}

	beadID := fixture.createBead(t, "Missing live root must reject assignment")
	prompt := fmt.Sprintf("NTM_ATOMIC_MISSING_LIVE_ROOT_MUST_NOT_DISPATCH_%d", time.Now().UnixNano())
	beforeBead := fixture.mustBR(t, "show", beadID, "--json")
	sessionStateRoot := filepath.Dir(fixture.ledgerPath())
	beforeSessionState := snapshotAtomicAssignmentTree(t, sessionStateRoot)
	beadsRoot := filepath.Join(fixture.projectDir, ".beads")
	beforeBeads := snapshotAtomicAssignmentTree(t, beadsRoot)
	beforePanes := snapshotAtomicAssignmentPanes(t, fixture)
	beforeMail := mail.snapshot()
	env := mail.env()
	env["NTM_TMUX_BINARY"] = wrapperPath

	result := fixture.runNTMInDir(t, outsideDir, env,
		"--json", "assign", fixture.session,
		"--pane="+fixture.panes[0].ID,
		"--beads="+beadID,
		"--prompt="+prompt,
		"--reserve-files=false",
		"--force",
		"--ignore-deps",
		"--timeout=60s",
	)
	_, message := assertAtomicAssignmentSingleFailureJSON(t, result)
	if !strings.Contains(message, "current path") || !strings.Contains(message, missingRoot) || !strings.Contains(message, "not usable") {
		t.Fatalf("missing-live-root diagnostic = %q", message)
	}
	if _, err := os.Stat(firedPath); err != nil {
		t.Fatalf("missing-live-root override did not reach pane project lookup: %v", err)
	}

	assertAtomicAssignmentTreeUnchanged(t, sessionStateRoot, beforeSessionState)
	assertAtomicAssignmentTreeUnchanged(t, beadsRoot, beforeBeads)
	assertAtomicAssignmentPanesUnchanged(t, fixture, beforePanes)
	afterBead := fixture.mustBR(t, "show", beadID, "--json")
	if !bytes.Equal(bytes.TrimSpace(afterBead), bytes.TrimSpace(beforeBead)) {
		t.Fatalf("missing-live-root rejection mutated Beads state:\nbefore=%s\nafter=%s", beforeBead, afterBead)
	}
	fixture.assertBead(t, beadID, "open", "")
	fixture.assertLedgerReplicasHaveNoAssignment(t, beadID)
	fixture.assertMarkerCounts(t, prompt, map[int]int{0: 0, 1: 0})
	afterMail := mail.snapshot()
	if !reflect.DeepEqual(afterMail, beforeMail) {
		t.Fatalf("missing-live-root rejection touched Agent Mail:\nbefore=%+v\nafter=%+v", beforeMail, afterMail)
	}
	assertAtomicAssignmentMailUntouched(t, afterMail)
}

func TestE2EAtomicAssignmentRemoteRepositoryDependenciesAndDefaultPrompt(t *testing.T) {
	CommonE2EPrerequisites(t)
	testutil.RequireTmuxThrottled(t)

	fixture := newAtomicAssignmentCLIFixture(t)
	callerRepo := filepath.Join(fixture.root, "caller-project")
	if err := os.MkdirAll(callerRepo, 0o700); err != nil {
		t.Fatalf("create caller repository: %v", err)
	}
	fixture.mustBRAt(t, callerRepo, "init", "--prefix=caller", "--json")

	const title = "Remote repository default prompt title"
	blockerID := fixture.createBead(t, "Remote repository blocker")
	beadID := fixture.createBead(t, title)
	fixture.mustBR(t, "dep", "add", beadID, blockerID, "--type=blocks", "--json")
	args := []string{
		"assign", fixture.session,
		"--repo=" + fixture.projectDir,
		"--pane=0",
		"--beads=" + beadID,
		"--reserve-files=false",
		"--force",
		"--timeout=15s",
		"--json",
	}
	expectedPrompt := fmt.Sprintf("Work on bead %s: %s. Check dependencies first with `br dep tree %s`.", beadID, title, beadID)

	blocked := fixture.runNTMInDir(t, callerRepo, nil, args...)
	if blocked.exitCode != 1 || len(bytes.TrimSpace(blocked.stderr)) != 0 {
		t.Fatalf("remote blocked assignment exit=%d stdout=%s stderr=%s", blocked.exitCode, blocked.stdout, blocked.stderr)
	}
	var blockedEnvelope atomicAssignmentDirectEnvelope
	decodeAtomicAssignmentJSON(t, blocked.stdout, &blockedEnvelope)
	if blockedEnvelope.Success || blockedEnvelope.Error == nil || blockedEnvelope.Error.Code != "BLOCKED" ||
		!strings.Contains(blockedEnvelope.Error.Message, blockerID) {
		t.Fatalf("remote blocked envelope = %+v", blockedEnvelope)
	}
	fixture.assertBead(t, beadID, "open", "")
	fixture.assertLedgerHasNoAssignment(t, beadID)
	fixture.assertMarkerCounts(t, expectedPrompt, map[int]int{0: 0, 1: 0})
	fixture.assertBeadAbsentAt(t, callerRepo, beadID)

	fixture.mustBR(t, "close", blockerID, "--reason=remote-repository-e2e", "--json")
	assigned := fixture.runNTMInDir(t, callerRepo, nil, args...)
	if assigned.exitCode != 0 || len(bytes.TrimSpace(assigned.stderr)) != 0 {
		t.Fatalf("remote default-template assignment exit=%d stdout=%s stderr=%s", assigned.exitCode, assigned.stdout, assigned.stderr)
	}
	var assignedEnvelope atomicAssignmentDirectEnvelope
	decodeAtomicAssignmentJSON(t, assigned.stdout, &assignedEnvelope)
	if !assignedEnvelope.Success || assignedEnvelope.Data == nil || assignedEnvelope.Data.Assignment.BeadID != beadID ||
		assignedEnvelope.Data.Assignment.BeadTitle != title || assignedEnvelope.Data.Assignment.Prompt != expectedPrompt ||
		!assignedEnvelope.Data.Assignment.PromptSent {
		t.Fatalf("remote default-template envelope = %+v", assignedEnvelope)
	}
	fixture.waitForMarkerCount(t, 0, expectedPrompt, 1)
	record := fixture.readLedgerAssignment(t, beadID)
	if record.BeadTitle != title || record.PromptSent != expectedPrompt || record.DispatchState != "sent" {
		t.Fatalf("remote default-template durable assignment = %+v", record)
	}
	fixture.assertBead(t, beadID, "in_progress", record.ClaimActor)
	fixture.assertBeadAbsentAt(t, callerRepo, beadID)
}

func TestE2EAtomicAssignmentClaimedPendingRetryBuiltProcess(t *testing.T) {
	CommonE2EPrerequisites(t)
	testutil.RequireTmuxThrottled(t)

	fixture := newAtomicAssignmentCLIFixture(t)
	prompt := fmt.Sprintf("NTM_ATOMIC_PENDING_RETRY_%d", time.Now().UnixNano())
	originalPaneID := fixture.panes[0].ID
	beadID, pending := fixture.createClaimedPendingViaTmuxOutage(t, "same-pane-id", "Claimed pending built process retry", prompt)

	fixture.startAgentPanes(t)
	if fixture.panes[0].ID != originalPaneID {
		t.Fatalf("private tmux restart changed recovery pane ID: before=%s after=%s", originalPaneID, fixture.panes[0].ID)
	}
	fixture.primePaneForSafeDispatch(t, 0)
	time.Sleep(5500 * time.Millisecond)
	retry := fixture.runNTM(t, nil,
		"assign", fixture.session,
		"--repo="+fixture.projectDir,
		"--retry="+beadID,
		"--reserve-files=false",
		"--timeout=15s",
		"--json",
	)
	if retry.exitCode != 0 || len(bytes.TrimSpace(retry.stderr)) != 0 {
		t.Fatalf("pending retry exit=%d stdout=%s stderr=%s", retry.exitCode, retry.stdout, retry.stderr)
	}
	var retryEnvelope atomicAssignmentRetryEnvelope
	decodeAtomicAssignmentJSON(t, retry.stdout, &retryEnvelope)
	if !retryEnvelope.Success || retryEnvelope.Command != "assign" || retryEnvelope.Subcommand != "retry" ||
		retryEnvelope.Session != fixture.session || retryEnvelope.Error != nil || retryEnvelope.Data == nil ||
		retryEnvelope.Data.Summary.TotalFailed != 1 || retryEnvelope.Data.Summary.RetriedCount != 1 ||
		retryEnvelope.Data.Summary.SkippedCount != 0 || len(retryEnvelope.Data.Retried) != 1 ||
		len(retryEnvelope.Data.Skipped) != 0 {
		t.Fatalf("pending retry envelope = %+v", retryEnvelope)
	}
	retried := retryEnvelope.Data.Retried[0]
	if retried.BeadID != beadID || retried.Pane != 0 || retried.Status != "assigned" || !retried.PromptSent || retried.RetryCount != 1 {
		t.Fatalf("pending retry item = %+v", retried)
	}
	fixture.waitForMarkerCount(t, 0, prompt, 1)
	fixture.assertMarkerCounts(t, prompt, map[int]int{0: 1, 1: 0})
	recovered := fixture.readLedgerAssignment(t, beadID)
	if recovered.IdempotencyKey != pending.IdempotencyKey || recovered.ClaimActor != pending.ClaimActor ||
		recovered.ClaimAttempts != 1 || recovered.DispatchState != "sent" || recovered.DispatchAttempts != 2 ||
		recovered.PendingPrompt != "" || recovered.PromptSent != prompt || recovered.DispatchReceiptID == "" ||
		recovered.DispatchedAt == nil || recovered.RetryCount != 1 {
		t.Fatalf("pending retry did not preserve atomic identity: pending=%+v recovered=%+v", pending, recovered)
	}
	fixture.assertBead(t, beadID, "in_progress", pending.ClaimActor)
}

func TestE2EAtomicAssignmentPendingRetryRefusesChangedPhysicalPane(t *testing.T) {
	CommonE2EPrerequisites(t)
	testutil.RequireTmuxThrottled(t)

	fixture := newAtomicAssignmentCLIFixture(t)
	prompt := fmt.Sprintf("NTM_ATOMIC_PENDING_TOPOLOGY_%d", time.Now().UnixNano())
	originalPaneID := fixture.panes[0].ID
	beadID, pending := fixture.createClaimedPendingViaTmuxOutage(t, "changed-pane-id", "Pending retry topology change", prompt)

	// Start a throwaway pane first in the restarted private tmux server. This
	// consumes the former physical pane ID while leaving the production session
	// with the same window-local pane indexes.
	dummySession := fixture.session + "-pane-id-shift"
	agentCommand := atomicAgentPaneCommand
	fixture.mustTMUX(t, "-f", fixture.tmuxConfig, "new-session", "-d", "-s", dummySession, "-x", "80", "-y", "24", "-c", fixture.projectDir, agentCommand)
	fixture.startAgentPanes(t)
	if fixture.panes[0].ID == originalPaneID {
		t.Fatalf("topology-change fixture reused original pane ID %s", originalPaneID)
	}
	if fixture.panes[0].Index != pending.Pane {
		t.Fatalf("topology-change fixture did not preserve local pane index: pending=%d current=%d", pending.Pane, fixture.panes[0].Index)
	}
	fixture.primePaneForSafeDispatch(t, 0)

	retry := fixture.runNTM(t, nil,
		"assign", fixture.session,
		"--repo="+fixture.projectDir,
		"--retry="+beadID,
		"--reserve-files=false",
		"--timeout=15s",
		"--json",
	)
	if retry.exitCode != 1 || len(bytes.TrimSpace(retry.stderr)) != 0 {
		t.Fatalf("changed-pane retry exit=%d stdout=%s stderr=%s", retry.exitCode, retry.stdout, retry.stderr)
	}
	var envelope atomicAssignmentRetryEnvelope
	decodeAtomicAssignmentJSON(t, retry.stdout, &envelope)
	if envelope.Success || envelope.Command != "assign" || envelope.Subcommand != "retry" || envelope.Session != fixture.session ||
		envelope.Error == nil || envelope.Error.Code != "RETRY_SKIPPED" || envelope.Data == nil || envelope.Data.Summary.TotalFailed != 1 ||
		envelope.Data.Summary.RetriedCount != 0 || envelope.Data.Summary.SkippedCount != 1 || len(envelope.Data.Retried) != 0 ||
		len(envelope.Data.Skipped) != 1 || envelope.Data.Skipped[0].BeadID != beadID ||
		!strings.Contains(envelope.Data.Skipped[0].Reason, "original physical pane "+originalPaneID+" is unavailable") {
		t.Fatalf("changed-pane retry envelope = %+v", envelope)
	}
	fixture.assertMarkerCounts(t, prompt, map[int]int{0: 0, 1: 0})
	after := fixture.readLedgerAssignment(t, beadID)
	if after.IdempotencyKey != pending.IdempotencyKey || after.ClaimActor != pending.ClaimActor ||
		after.ClaimAttempts != pending.ClaimAttempts || after.DispatchState != "pending" ||
		after.DispatchAttempts != pending.DispatchAttempts || after.PendingPrompt != pending.PendingPrompt ||
		after.DispatchReceiptID != "" || after.DispatchedAt != nil || after.RetryCount != pending.RetryCount {
		t.Fatalf("changed-pane retry mutated pending atomic identity: before=%+v after=%+v", pending, after)
	}
	fixture.assertBead(t, beadID, "in_progress", pending.ClaimActor)
}

func TestE2EAtomicAssignmentRetryFailedMixedRecoveryBuiltProcess(t *testing.T) {
	CommonE2EPrerequisites(t)
	testutil.RequireTmuxThrottled(t)

	fixture := newAtomicAssignmentCLIFixture(t)
	t.Setenv("HOME", fixture.homeDir)
	recoverableBeadID := fixture.createBead(t, "Mixed retry recoverable assignment")
	skippedBeadID := fixture.createBead(t, "Mixed retry missing physical pane")
	recoverableSeedPrompt := fmt.Sprintf("NTM_ATOMIC_MIXED_RETRY_RECOVERABLE_SEED_%d", time.Now().UnixNano())
	skippedSeedPrompt := fmt.Sprintf("NTM_ATOMIC_MIXED_RETRY_SKIPPED_SEED_%d", time.Now().UnixNano())

	for _, seed := range []struct {
		pane   atomicAssignmentPane
		beadID string
		prompt string
	}{
		{pane: fixture.panes[0], beadID: recoverableBeadID, prompt: recoverableSeedPrompt},
		{pane: fixture.panes[1], beadID: skippedBeadID, prompt: skippedSeedPrompt},
	} {
		result := fixture.runNTM(t, nil, atomicDirectArgsForSelector(fixture, seed.pane.ID, seed.beadID, seed.prompt, false)...)
		if result.exitCode != 0 || len(bytes.TrimSpace(result.stderr)) != 0 {
			t.Fatalf("seed mixed retry %s exit=%d stdout=%s stderr=%s", seed.beadID, result.exitCode, result.stdout, result.stderr)
		}
		fixture.waitForEndpointMarkerCount(t, seed.pane, seed.prompt, 1)
	}

	fixture.driveAssignmentStatusWithCompletionEvent(t, fixture.panes[0], recoverableBeadID, "ERROR: fatal mixed retry recoverable failure", "failed")
	failedBeforeRetry := fixture.readLedgerAssignment(t, recoverableBeadID)
	if failedBeforeRetry.PendingCompletionEventID == "" {
		t.Fatalf("mixed retry recoverable failure has no durable completion event: %+v", failedBeforeRetry)
	}
	retryStore, err := assignment.LoadStoreStrict(fixture.session)
	if err != nil {
		t.Fatalf("load mixed retry store for completion acknowledgement: %v", err)
	}
	consumerToken := failedBeforeRetry.CompletionConsumerToken
	if consumerToken == "" {
		consumerToken = "mixed-retry-e2e-consumer"
	}
	if _, acquired, claimErr := retryStore.ClaimPendingCompletionEvent(
		t.Context(), recoverableBeadID, failedBeforeRetry.PendingCompletionEventID, consumerToken, time.Minute,
	); claimErr != nil || !acquired {
		t.Fatalf("claim mixed retry completion event acquired=%v error=%v", acquired, claimErr)
	}
	if acknowledged, ackErr := retryStore.AcknowledgeCompletionEvent(
		t.Context(), recoverableBeadID, failedBeforeRetry.PendingCompletionEventID, consumerToken,
	); ackErr != nil || !acknowledged {
		t.Fatalf("acknowledge mixed retry completion event applied=%v error=%v", acknowledged, ackErr)
	}

	missingPaneID := "%999999"
	skippedPendingPrompt := fmt.Sprintf("NTM_ATOMIC_MIXED_RETRY_MISSING_PENDING_%d", time.Now().UnixNano())
	fixture.setLedgerAssignmentFields(t, skippedBeadID, map[string]any{
		"status":                  "claimed",
		"dispatch_state":          "pending",
		"dispatch_target":         missingPaneID,
		"occupancy_key":           missingPaneID,
		"pending_prompt":          skippedPendingPrompt,
		"prompt_sent":             "",
		"dispatched_at":           nil,
		"dispatch_receipt_id":     "",
		"last_dispatch_error":     "injected missing physical pane",
		"dispatch_attempts":       1,
		"pending_terminal_status": "",
	})
	_, skippedLedger := readAtomicAssignmentLedgerAt(t, fixture.ledgerPath())
	if err := os.WriteFile(fixture.ledgerPath()+".bak", skippedLedger, 0o600); err != nil {
		t.Fatalf("mirror mixed retry fixture to assignment backup: %v", err)
	}
	skippedBefore := fixture.readLedgerAssignment(t, skippedBeadID)
	if skippedBefore.Status != "claimed" || skippedBefore.DispatchState != "pending" ||
		skippedBefore.OccupancyKey != missingPaneID || skippedBefore.DispatchTarget != missingPaneID ||
		skippedBefore.PendingPrompt != skippedPendingPrompt || skippedBefore.IdempotencyKey == "" || skippedBefore.ClaimActor == "" {
		t.Fatalf("invalid mixed retry skipped fixture = %+v", skippedBefore)
	}

	fixture.primeEndpointForSafeDispatch(t, fixture.panes[0])
	time.Sleep(5500 * time.Millisecond)
	retryMarker := fmt.Sprintf("NTM_ATOMIC_MIXED_RETRY_DELIVERY_%d", time.Now().UnixNano())
	templatePath := filepath.Join(fixture.projectDir, "atomic-mixed-retry-template.txt")
	if err := os.WriteFile(templatePath, []byte(retryMarker+"::{BEAD_ID}"), 0o600); err != nil {
		t.Fatalf("write mixed retry template: %v", err)
	}

	result := fixture.runNTM(t, nil,
		"assign", fixture.session,
		"--repo="+fixture.projectDir,
		"--retry-failed",
		"--to-pane="+fixture.panes[0].ID,
		"--template=custom",
		"--template-file="+templatePath,
		"--reserve-files=false",
		"--timeout=15s",
		"--json",
	)
	if result.exitCode != 1 || len(bytes.TrimSpace(result.stderr)) != 0 {
		t.Fatalf("mixed retry exit=%d stdout=%s stderr=%s", result.exitCode, result.stdout, result.stderr)
	}
	var envelope atomicAssignmentRetryEnvelope
	decodeAtomicAssignmentJSON(t, result.stdout, &envelope)
	if envelope.Success || envelope.Command != "assign" || envelope.Subcommand != "retry" || envelope.Session != fixture.session ||
		envelope.Error == nil || envelope.Error.Code != "RETRY_PARTIAL" || envelope.Data == nil ||
		envelope.Data.Summary.TotalFailed != 2 || envelope.Data.Summary.RetriedCount != 1 || envelope.Data.Summary.SkippedCount != 1 ||
		len(envelope.Data.Retried) != 1 || len(envelope.Data.Skipped) != 1 {
		t.Fatalf("mixed retry envelope = %+v", envelope)
	}
	if retried := envelope.Data.Retried[0]; retried.BeadID != recoverableBeadID || retried.Pane != fixture.panes[0].Index ||
		retried.Status != "assigned" || !retried.PromptSent || retried.RetryCount != 1 {
		t.Fatalf("mixed retry recovered item = %+v", retried)
	}
	if skipped := envelope.Data.Skipped[0]; skipped.BeadID != skippedBeadID ||
		!strings.Contains(skipped.Reason, "original physical pane "+missingPaneID+" is unavailable") {
		t.Fatalf("mixed retry skipped item = %+v", skipped)
	}

	deliveredPrompt := retryMarker + "::" + recoverableBeadID
	fixture.waitForEndpointMarkerCount(t, fixture.panes[0], deliveredPrompt, 1)
	fixture.assertMarkerCounts(t, deliveredPrompt, map[int]int{0: 1, 1: 0})
	fixture.assertMarkerCounts(t, skippedPendingPrompt, map[int]int{0: 0, 1: 0})
	recovered := fixture.readLedgerAssignment(t, recoverableBeadID)
	if recovered.Status != "assigned" || recovered.OccupancyKey != fixture.panes[0].ID || recovered.DispatchTarget != fixture.panes[0].ID ||
		recovered.DispatchState != "sent" || recovered.DispatchReceiptID == "" || recovered.PromptSent != deliveredPrompt || recovered.RetryCount != 1 {
		t.Fatalf("mixed retry durable recovery = %+v", recovered)
	}
	if skippedAfter := fixture.readLedgerAssignment(t, skippedBeadID); !reflect.DeepEqual(skippedAfter, skippedBefore) {
		t.Fatalf("mixed retry mutated skipped durable identity: before=%+v after=%+v", skippedBefore, skippedAfter)
	}
	skippedBackup, _ := readAtomicAssignmentLedgerAt(t, fixture.ledgerPath()+".bak")
	if skippedBackupRecord := skippedBackup.Assignments[skippedBeadID]; !reflect.DeepEqual(skippedBackupRecord, skippedBefore) {
		t.Fatalf("mixed retry mutated skipped backup identity: before=%+v backup=%+v", skippedBefore, skippedBackupRecord)
	}
	fixture.assertBead(t, recoverableBeadID, "in_progress", recovered.ClaimActor)
	fixture.assertBead(t, skippedBeadID, "in_progress", skippedBefore.ClaimActor)
}

func TestE2EAtomicAssignmentRetryTargetValidationBuiltProcess(t *testing.T) {
	CommonE2EPrerequisites(t)
	testutil.RequireTmuxThrottled(t)

	t.Run("pending_recovery_rejects_conflicting_explicit_target", func(t *testing.T) {
		fixture := newAtomicAssignmentCLIFixture(t)
		prompt := fmt.Sprintf("NTM_ATOMIC_PENDING_EXPLICIT_%d", time.Now().UnixNano())
		originalPaneID := fixture.panes[0].ID
		beadID, pending := fixture.createClaimedPendingViaTmuxOutage(t, "explicit-target", "Pending explicit target validation", prompt)
		fixture.startAgentPanes(t)
		if fixture.panes[0].ID != originalPaneID {
			t.Fatalf("private tmux restart changed recovery pane ID: before=%s after=%s", originalPaneID, fixture.panes[0].ID)
		}

		conflict := fixture.runNTM(t, nil,
			"assign", fixture.session,
			"--repo="+fixture.projectDir,
			"--retry="+beadID,
			"--to-pane="+fixture.panes[1].ID,
			"--reserve-files=false",
			"--timeout=15s",
			"--json",
		)
		if conflict.exitCode != 1 || len(bytes.TrimSpace(conflict.stderr)) != 0 {
			t.Fatalf("conflicting pending target exit=%d stdout=%s stderr=%s", conflict.exitCode, conflict.stdout, conflict.stderr)
		}
		var conflictEnvelope atomicAssignmentRetryEnvelope
		decodeAtomicAssignmentJSON(t, conflict.stdout, &conflictEnvelope)
		if conflictEnvelope.Success || conflictEnvelope.Error == nil || conflictEnvelope.Error.Code != "RETRY_SKIPPED" ||
			conflictEnvelope.Data == nil || len(conflictEnvelope.Data.Skipped) != 1 ||
			!strings.Contains(conflictEnvelope.Data.Skipped[0].Reason, "cannot retarget") {
			t.Fatalf("conflicting pending target envelope = %+v", conflictEnvelope)
		}
		if after := fixture.readLedgerAssignment(t, beadID); !reflect.DeepEqual(after, pending) {
			t.Fatalf("conflicting pending target mutated durable identity: before=%+v after=%+v", pending, after)
		}
		fixture.assertMarkerCounts(t, prompt, map[int]int{0: 0, 1: 0})

		fixture.primePaneForSafeDispatch(t, 0)
		time.Sleep(5500 * time.Millisecond)
		accepted := fixture.runNTM(t, nil,
			"assign", fixture.session,
			"--repo="+fixture.projectDir,
			"--retry="+beadID,
			"--to-pane="+fixture.panes[0].Target,
			"--reserve-files=false",
			"--timeout=15s",
			"--json",
		)
		if accepted.exitCode != 0 || len(bytes.TrimSpace(accepted.stderr)) != 0 {
			t.Fatalf("matching pending target exit=%d stdout=%s stderr=%s", accepted.exitCode, accepted.stdout, accepted.stderr)
		}
		var acceptedEnvelope atomicAssignmentRetryEnvelope
		decodeAtomicAssignmentJSON(t, accepted.stdout, &acceptedEnvelope)
		if !acceptedEnvelope.Success || acceptedEnvelope.Data == nil || acceptedEnvelope.Data.Summary.RetriedCount != 1 ||
			acceptedEnvelope.Data.Summary.SkippedCount != 0 {
			t.Fatalf("matching pending target envelope = %+v", acceptedEnvelope)
		}
		fixture.waitForMarkerCount(t, 0, prompt, 1)
	})

	t.Run("user_pane_rejected_by_id_and_window_pane", func(t *testing.T) {
		fixture := newAtomicAssignmentCLIFixture(t)
		beadID := fixture.createBead(t, "Retry user-pane validation")
		seedPrompt := fmt.Sprintf("NTM_ATOMIC_RETRY_USER_SEED_%d", time.Now().UnixNano())
		seed := fixture.runNTM(t, nil, atomicDirectArgs(fixture, beadID, seedPrompt, false)...)
		if seed.exitCode != 0 || len(bytes.TrimSpace(seed.stderr)) != 0 {
			t.Fatalf("seed user-pane retry exit=%d stdout=%s stderr=%s", seed.exitCode, seed.stdout, seed.stderr)
		}
		fixture.waitForMarkerCount(t, 0, seedPrompt, 1)
		fixture.driveAssignmentStatus(t, fixture.panes[0], beadID, "ERROR: fatal retry target fixture", "failed")
		before := fixture.readLedgerAssignment(t, beadID)
		userPane := fixture.panes[1]
		fixture.mustTMUX(t, "select-pane", "-t", userPane.ID, "-T", fixture.session+"__user_2")
		retryMarker := fmt.Sprintf("NTM_ATOMIC_RETRY_USER_TARGET_%d", time.Now().UnixNano())
		templatePath := filepath.Join(fixture.projectDir, "atomic-retry-user-template.txt")
		if err := os.WriteFile(templatePath, []byte(retryMarker+"::{BEAD_ID}"), 0o600); err != nil {
			t.Fatalf("write user-pane retry template: %v", err)
		}

		for _, selector := range []string{userPane.ID, userPane.Target} {
			result := fixture.runNTM(t, nil,
				"assign", fixture.session,
				"--repo="+fixture.projectDir,
				"--retry="+beadID,
				"--to-pane="+selector,
				"--template=custom",
				"--template-file="+templatePath,
				"--reserve-files=false",
				"--timeout=15s",
				"--json",
			)
			if result.exitCode != 1 || len(bytes.TrimSpace(result.stderr)) != 0 {
				t.Fatalf("user-pane selector %s exit=%d stdout=%s stderr=%s", selector, result.exitCode, result.stdout, result.stderr)
			}
			var envelope atomicAssignmentRetryEnvelope
			decodeAtomicAssignmentJSON(t, result.stdout, &envelope)
			if envelope.Success || envelope.Error == nil || envelope.Error.Code != "RETRY_SKIPPED" || envelope.Data == nil ||
				len(envelope.Data.Skipped) != 1 || !strings.Contains(envelope.Data.Skipped[0].Reason, "not an agent pane (type: user)") {
				t.Fatalf("user-pane selector %s envelope = %+v", selector, envelope)
			}
			if after := fixture.readLedgerAssignment(t, beadID); !reflect.DeepEqual(after, before) {
				t.Fatalf("user-pane selector %s mutated failed assignment: before=%+v after=%+v", selector, before, after)
			}
			fixture.assertMarkerCounts(t, retryMarker, map[int]int{0: 0, 1: 0})
		}
	})
}

func TestE2EAtomicAssignmentReassignFailureContracts(t *testing.T) {
	CommonE2EPrerequisites(t)
	testutil.RequireTmuxThrottled(t)

	t.Run("send_failure_is_nonzero_and_pending_replacement_recovers", func(t *testing.T) {
		fixture := newAtomicAssignmentCLIFixture(t)
		beadID := fixture.createBead(t, "Reassign durable send failure")
		sourcePrompt := fmt.Sprintf("NTM_ATOMIC_REASSIGN_FAILURE_SOURCE_%d", time.Now().UnixNano())
		targetPrompt := fmt.Sprintf("NTM_ATOMIC_REASSIGN_FAILURE_TARGET_%d", time.Now().UnixNano())
		seed := fixture.runNTM(t, nil, atomicDirectArgs(fixture, beadID, sourcePrompt, false)...)
		if seed.exitCode != 0 || len(bytes.TrimSpace(seed.stderr)) != 0 {
			t.Fatalf("seed send-failure reassign exit=%d stdout=%s stderr=%s", seed.exitCode, seed.stdout, seed.stderr)
		}
		fixture.waitForMarkerCount(t, 0, sourcePrompt, 1)
		fixture.driveAssignmentStatus(t, fixture.panes[0], beadID, "• Working (4s · esc to interrupt)", "working")
		before := fixture.readLedgerAssignment(t, beadID)
		originalPaneIDs := map[int]string{0: fixture.panes[0].ID, 1: fixture.panes[1].ID}
		path, fired := fixture.armGuardedClaimInfoThenKillTmux(t, "reassign-send-failure")

		failed := fixture.runNTM(t, map[string]string{"PATH": path},
			"--json", "assign", fixture.session,
			"--repo="+fixture.projectDir,
			"--reassign="+beadID,
			"--to-pane="+fixture.panes[1].ID,
			"--prompt="+targetPrompt,
			"--force",
			"--reserve-files=false",
			"--timeout=15s",
		)
		if failed.exitCode != 1 || len(bytes.TrimSpace(failed.stderr)) != 0 {
			t.Fatalf("send-failure reassign exit=%d stdout=%s stderr=%s", failed.exitCode, failed.stdout, failed.stderr)
		}
		var failedEnvelope atomicAssignmentReassignEnvelope
		decodeAtomicAssignmentJSON(t, failed.stdout, &failedEnvelope)
		if failedEnvelope.Success || failedEnvelope.Error == nil || failedEnvelope.Error.Code != "SEND_ERROR" ||
			failedEnvelope.Error.Details["dispatch_state"] != "pending" {
			t.Fatalf("send-failure reassign envelope = %+v", failedEnvelope)
		}
		if _, err := os.Stat(fired); err != nil {
			t.Fatalf("reassign guarded-info outage did not fire: %v", err)
		}
		pending := fixture.readLedgerAssignment(t, beadID)
		if pending.Status != "claimed" || pending.ClaimState != "claimed" || pending.ClaimActor != before.ClaimActor ||
			pending.IdempotencyKey == before.IdempotencyKey || pending.OccupancyKey != originalPaneIDs[1] ||
			pending.DispatchTarget != originalPaneIDs[1] || pending.DispatchState != "pending" || pending.DispatchAttempts != 1 ||
			pending.PendingPrompt != targetPrompt || pending.PromptSent != "" || pending.LastDispatchError == "" ||
			pending.DispatchedAt != nil || pending.DispatchReceiptID != "" || pending.ClearState != "" {
			t.Fatalf("send-failure replacement is not durably retryable: before=%+v pending=%+v", before, pending)
		}
		fixture.assertBead(t, beadID, "in_progress", before.ClaimActor)

		fixture.startAgentPanes(t)
		for pane, id := range originalPaneIDs {
			if fixture.panes[pane].ID != id {
				t.Fatalf("private tmux restart changed pane %d ID: before=%s after=%s", pane, id, fixture.panes[pane].ID)
			}
		}
		fixture.assertMarkerCounts(t, targetPrompt, map[int]int{0: 0, 1: 0})
		fixture.primePaneForSafeDispatch(t, 1)
		time.Sleep(5500 * time.Millisecond)
		recovered := fixture.runNTM(t, nil,
			"assign", fixture.session,
			"--repo="+fixture.projectDir,
			"--retry="+beadID,
			"--to-pane="+fixture.panes[1].Target,
			"--reserve-files=false",
			"--timeout=15s",
			"--json",
		)
		if recovered.exitCode != 0 || len(bytes.TrimSpace(recovered.stderr)) != 0 {
			t.Fatalf("recover send-failure replacement exit=%d stdout=%s stderr=%s", recovered.exitCode, recovered.stdout, recovered.stderr)
		}
		fixture.waitForMarkerCount(t, 1, targetPrompt, 1)
		after := fixture.readLedgerAssignment(t, beadID)
		if after.IdempotencyKey != pending.IdempotencyKey || after.ClaimActor != pending.ClaimActor ||
			after.DispatchState != "sent" || after.DispatchAttempts != 2 || after.DispatchReceiptID == "" || after.RetryCount != 1 {
			t.Fatalf("recovered replacement changed atomic identity: pending=%+v after=%+v", pending, after)
		}
	})

	t.Run("redaction_block_does_not_send_or_persist_secret", func(t *testing.T) {
		fixture := newAtomicAssignmentCLIFixture(t)
		beadID := fixture.createBead(t, "Reassign redaction block")
		sourcePrompt := fmt.Sprintf("NTM_ATOMIC_REASSIGN_REDACT_SOURCE_%d", time.Now().UnixNano())
		seed := fixture.runNTM(t, nil, atomicDirectArgs(fixture, beadID, sourcePrompt, false)...)
		if seed.exitCode != 0 || len(bytes.TrimSpace(seed.stderr)) != 0 {
			t.Fatalf("seed redaction reassign exit=%d stdout=%s stderr=%s", seed.exitCode, seed.stdout, seed.stderr)
		}
		fixture.waitForMarkerCount(t, 0, sourcePrompt, 1)
		fixture.driveAssignmentStatus(t, fixture.panes[0], beadID, "• Working (4s · esc to interrupt)", "working")
		before := fixture.readLedgerAssignment(t, beadID)
		secret := "NTM_E2E_REASSIGN_SECRET_987654321"
		blockedPrompt := "Use password=" + secret + " while reassigning"

		blocked := fixture.runNTM(t, nil,
			"--json", "assign", fixture.session,
			"--repo="+fixture.projectDir,
			"--reassign="+beadID,
			"--to-pane="+fixture.panes[1].ID,
			"--prompt="+blockedPrompt,
			"--force",
			"--reserve-files=false",
			"--redact=block",
			"--timeout=15s",
		)
		if blocked.exitCode != 1 || len(bytes.TrimSpace(blocked.stderr)) != 0 {
			t.Fatalf("redaction-blocked reassign exit=%d stdout=%s stderr=%s", blocked.exitCode, blocked.stdout, blocked.stderr)
		}
		fixture.assertSecretAbsent(t, secret, blocked.stdout, blocked.stderr)
		var blockedEnvelope atomicAssignmentReassignEnvelope
		decodeAtomicAssignmentJSON(t, blocked.stdout, &blockedEnvelope)
		if blockedEnvelope.Success || blockedEnvelope.Error == nil || blockedEnvelope.Error.Code != "REDACTION_BLOCKED" {
			t.Fatalf("redaction-blocked reassign envelope = %+v", blockedEnvelope)
		}
		fixture.assertMarkerCounts(t, secret, map[int]int{0: 0, 1: 0})
		fixture.assertAssignmentArtifactsExclude(t, secret)
		if after := fixture.readLedgerAssignment(t, beadID); !reflect.DeepEqual(after, before) {
			t.Fatalf("redaction preflight mutated the source assignment before release: before=%+v after=%+v", before, after)
		}

		cleanPrompt := fmt.Sprintf("NTM_ATOMIC_REASSIGN_REDACT_CLEAN_%d", time.Now().UnixNano())
		clean := fixture.runNTM(t, nil,
			"--json", "assign", fixture.session,
			"--repo="+fixture.projectDir,
			"--reassign="+beadID,
			"--to-pane="+fixture.panes[1].ID,
			"--prompt="+cleanPrompt,
			"--force",
			"--reserve-files=false",
			"--timeout=15s",
		)
		if clean.exitCode != 0 || len(bytes.TrimSpace(clean.stderr)) != 0 {
			t.Fatalf("clean reassign after redaction block exit=%d stdout=%s stderr=%s", clean.exitCode, clean.stdout, clean.stderr)
		}
		fixture.waitForMarkerCount(t, 1, cleanPrompt, 1)
		after := fixture.readLedgerAssignment(t, beadID)
		if after.ClaimActor != before.ClaimActor || after.IdempotencyKey == before.IdempotencyKey || after.DispatchState != "sent" {
			t.Fatalf("clean reassign after redaction block lost atomic identity: before=%+v after=%+v", before, after)
		}
	})

	t.Run("force_refuses_durably_occupied_target", func(t *testing.T) {
		fixture := newAtomicAssignmentCLIFixture(t)
		sourceBeadID := fixture.createBead(t, "Reassign occupied source")
		targetBeadID := fixture.createBead(t, "Reassign occupied target")
		sourcePrompt := fmt.Sprintf("NTM_ATOMIC_REASSIGN_OCCUPIED_SOURCE_%d", time.Now().UnixNano())
		targetPrompt := fmt.Sprintf("NTM_ATOMIC_REASSIGN_OCCUPIED_TARGET_%d", time.Now().UnixNano())
		for _, seed := range []struct {
			pane   int
			beadID string
			prompt string
		}{
			{pane: 0, beadID: sourceBeadID, prompt: sourcePrompt},
			{pane: 1, beadID: targetBeadID, prompt: targetPrompt},
		} {
			result := fixture.runNTM(t, nil, atomicDirectArgsForPane(fixture, seed.pane, seed.beadID, seed.prompt, false)...)
			if result.exitCode != 0 || len(bytes.TrimSpace(result.stderr)) != 0 {
				t.Fatalf("seed occupied pane %d exit=%d stdout=%s stderr=%s", seed.pane, result.exitCode, result.stdout, result.stderr)
			}
			fixture.waitForMarkerCount(t, seed.pane, seed.prompt, 1)
		}
		fixture.driveAssignmentStatus(t, fixture.panes[0], sourceBeadID, "• Working (4s · esc to interrupt)", "working")
		sourceBefore := fixture.readLedgerAssignment(t, sourceBeadID)
		targetBefore := fixture.readLedgerAssignment(t, targetBeadID)
		candidatePrompt := fmt.Sprintf("NTM_ATOMIC_REASSIGN_OCCUPIED_CANDIDATE_%d", time.Now().UnixNano())

		result := fixture.runNTM(t, nil,
			"--json", "assign", fixture.session,
			"--repo="+fixture.projectDir,
			"--reassign="+sourceBeadID,
			"--to-pane="+fixture.panes[1].ID,
			"--prompt="+candidatePrompt,
			"--force",
			"--reserve-files=false",
			"--timeout=15s",
		)
		if result.exitCode != 1 || len(bytes.TrimSpace(result.stderr)) != 0 {
			t.Fatalf("force occupied reassign exit=%d stdout=%s stderr=%s", result.exitCode, result.stdout, result.stderr)
		}
		var envelope atomicAssignmentReassignEnvelope
		decodeAtomicAssignmentJSON(t, result.stdout, &envelope)
		if envelope.Success || envelope.Error == nil || envelope.Error.Code != "TARGET_BUSY" ||
			!strings.Contains(envelope.Error.Message, targetBeadID) {
			t.Fatalf("force occupied reassign envelope = %+v", envelope)
		}
		fixture.assertMarkerCounts(t, candidatePrompt, map[int]int{0: 0, 1: 0})
		if sourceAfter := fixture.readLedgerAssignment(t, sourceBeadID); !reflect.DeepEqual(sourceAfter, sourceBefore) {
			t.Fatalf("force occupied reassign mutated source: before=%+v after=%+v", sourceBefore, sourceAfter)
		}
		if targetAfter := fixture.readLedgerAssignment(t, targetBeadID); !reflect.DeepEqual(targetAfter, targetBefore) {
			t.Fatalf("force occupied reassign mutated target: before=%+v after=%+v", targetBefore, targetAfter)
		}
		fixture.assertBead(t, sourceBeadID, "in_progress", sourceBefore.ClaimActor)
		fixture.assertBead(t, targetBeadID, "in_progress", targetBefore.ClaimActor)
	})
}

func TestE2EAtomicAssignmentAmbiguousTMUXDeliveryDoesNotRetry(t *testing.T) {
	CommonE2EPrerequisites(t)
	testutil.RequireTmuxThrottled(t)

	fixture := newAtomicAssignmentCLIFixture(t)
	beadID := fixture.createBead(t, "Ambiguous tmux delivery")
	prompt := fmt.Sprintf("NTM_ATOMIC_AMBIGUOUS_TMUX_%d", time.Now().UnixNano())
	tmuxBinary, fired := fixture.armTMUXPostActuationFailure(t)

	first := fixture.runNTM(t, map[string]string{"NTM_TMUX_BINARY": tmuxBinary}, atomicDirectArgs(fixture, beadID, prompt, false)...)
	if first.exitCode != 1 || len(bytes.TrimSpace(first.stderr)) != 0 {
		t.Fatalf("ambiguous tmux assignment exit=%d stdout=%s stderr=%s", first.exitCode, first.stdout, first.stderr)
	}
	var firstEnvelope atomicAssignmentDirectEnvelope
	decodeAtomicAssignmentJSON(t, first.stdout, &firstEnvelope)
	if firstEnvelope.Success || firstEnvelope.Error == nil || firstEnvelope.Error.Code != "DISPATCH_UNKNOWN" {
		t.Fatalf("ambiguous tmux envelope = %+v", firstEnvelope)
	}
	pending := fixture.readLedgerAssignment(t, beadID)
	if pending.DispatchState != "sending" || pending.DispatchAttempts != 1 || pending.DispatchReceiptID != "" ||
		pending.DispatchedAt != nil || pending.DispatchStartedAt == nil || pending.IdempotencyKey == "" {
		t.Fatalf("ambiguous tmux durable state = %+v", pending)
	}
	if _, statErr := os.Stat(fired); statErr != nil {
		t.Fatalf("ambiguous tmux wrapper did not fail after actuation: %v", statErr)
	}
	fixture.waitForMarkerCount(t, 0, prompt, 1)
	fixture.assertBead(t, beadID, "in_progress", pending.ClaimActor)

	second := fixture.runNTM(t, map[string]string{"NTM_TMUX_BINARY": fixture.tmuxPath}, atomicDirectArgs(fixture, beadID, prompt, false)...)
	if second.exitCode != 1 || len(bytes.TrimSpace(second.stderr)) != 0 {
		t.Fatalf("ambiguous tmux replay exit=%d stdout=%s stderr=%s", second.exitCode, second.stdout, second.stderr)
	}
	var secondEnvelope atomicAssignmentDirectEnvelope
	decodeAtomicAssignmentJSON(t, second.stdout, &secondEnvelope)
	if secondEnvelope.Success || secondEnvelope.Error == nil || secondEnvelope.Error.Code != "DISPATCH_UNKNOWN" {
		t.Fatalf("ambiguous tmux replay envelope = %+v", secondEnvelope)
	}
	if after := fixture.readLedgerAssignment(t, beadID); !reflect.DeepEqual(after, pending) {
		t.Fatalf("ambiguous tmux replay mutated or retried generation: before=%+v after=%+v", pending, after)
	}
	fixture.assertMarkerCounts(t, prompt, map[int]int{0: 1, 1: 0})
}

func TestE2EAtomicAssignmentReassignTransfersReservation(t *testing.T) {
	CommonE2EPrerequisites(t)
	testutil.RequireTmuxThrottled(t)

	fixture := newAtomicAssignmentCLIFixture(t)
	const title = "Transfer reservation internal/cli/reassign_transfer.go"
	reservedPaths := []string{"internal/cli/reassign_transfer.go", "internal/cli/reassign_transfer/**/*"}
	beadID := fixture.createBead(t, title)
	source := fixture.panes[0]
	target := fixture.panes[1]

	var reservationMu sync.Mutex
	var grantCalls int
	var releaseCalls int
	var releasedIDs []int
	var grantedAgents []string
	var expectedReleaseAgent string
	var activeReservations []map[string]any
	mailServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodGet && r.URL.Path == "/health/liveness" {
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"status":"ok"}`))
			return
		}
		var request struct {
			JSONRPC string `json:"jsonrpc"`
			ID      any    `json:"id"`
			Method  string `json:"method"`
			Params  struct {
				Name      string         `json:"name"`
				Arguments map[string]any `json:"arguments"`
			} `json:"params"`
		}
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		writeResult := func(result any) {
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(map[string]any{"jsonrpc": "2.0", "id": request.ID, "result": result})
		}
		if request.Method == "resources/read" {
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(map[string]any{
				"jsonrpc": "2.0", "id": request.ID,
				"error": map[string]any{"code": -32601, "message": "resource view not supported"},
			})
			return
		}
		if request.Method != "tools/call" {
			http.Error(w, "expected JSON-RPC tools/call", http.StatusBadRequest)
			return
		}
		switch request.Params.Name {
		case "health_check":
			writeResult(map[string]any{"status": "ok", "timestamp": time.Now().UTC().Format(time.RFC3339Nano)})
		case "ensure_project":
			if request.Params.Arguments["human_key"] != fixture.projectDir {
				http.Error(w, fmt.Sprintf("unexpected reassign ensure project args: %#v", request.Params.Arguments), http.StatusBadRequest)
				return
			}
			writeResult(map[string]any{"id": 1, "slug": "atomic-e2e", "human_key": fixture.projectDir, "created_at": time.Now().UTC().Format(time.RFC3339Nano)})
		case "file_reservation_paths":
			paths, pathsOK := anyStringSlice(request.Params.Arguments["paths"])
			agentName, _ := request.Params.Arguments["agent_name"].(string)
			reason, _ := request.Params.Arguments["reason"].(string)
			if request.Params.Arguments["project_key"] != fixture.projectDir || !pathsOK ||
				!reflect.DeepEqual(paths, reservedPaths) || agentName == "" || !strings.Contains(reason, beadID) {
				http.Error(w, fmt.Sprintf("unexpected reassign reservation args: %#v", request.Params.Arguments), http.StatusBadRequest)
				return
			}
			reservationMu.Lock()
			grantCalls++
			grantedAgents = append(grantedAgents, agentName)
			baseID := 9600 + grantCalls*10
			now := time.Now().UTC()
			granted := make([]map[string]any, 0, len(paths))
			for index, path := range paths {
				granted = append(granted, map[string]any{
					"id": baseID + index, "path_pattern": path, "agent_name": agentName,
					"project_id": 1, "exclusive": true, "reason": reason,
					"created_ts": now.Format(time.RFC3339Nano), "expires_ts": now.Add(time.Hour).Format(time.RFC3339Nano),
				})
			}
			activeReservations = append([]map[string]any(nil), granted...)
			reservationMu.Unlock()
			writeResult(map[string]any{"granted": granted, "conflicts": []any{}})
		case "list_file_reservations":
			reservationMu.Lock()
			active := append([]map[string]any(nil), activeReservations...)
			reservationMu.Unlock()
			writeResult(active)
		case "release_file_reservations":
			ids, idsOK := atomicAssignmentAnyIntSlice(request.Params.Arguments["file_reservation_ids"])
			agentName, _ := request.Params.Arguments["agent_name"].(string)
			reservationMu.Lock()
			wantAgent := expectedReleaseAgent
			reservationMu.Unlock()
			if request.Params.Arguments["project_key"] != fixture.projectDir || !idsOK || len(ids) != len(reservedPaths) ||
				wantAgent == "" || agentName != wantAgent {
				http.Error(w, fmt.Sprintf("unexpected reassign release args: %#v", request.Params.Arguments), http.StatusBadRequest)
				return
			}
			reservationMu.Lock()
			releaseCalls++
			releasedIDs = append(releasedIDs, ids...)
			activeReservations = nil
			reservationMu.Unlock()
			writeResult(map[string]any{"released": len(ids)})
		default:
			http.Error(w, "unexpected Agent Mail tool: "+request.Params.Name, http.StatusNotFound)
		}
	}))
	defer mailServer.Close()
	env := map[string]string{"AGENT_MAIL_URL": mailServer.URL + "/"}

	initialPrompt := fmt.Sprintf("Work on bead %s: %s. Check dependencies first with `br dep tree %s`.", beadID, title, beadID)
	seed := fixture.runNTM(t, env,
		"assign", fixture.session,
		"--repo="+fixture.projectDir,
		"--pane="+source.ID,
		"--beads="+beadID,
		"--template=impl",
		"--force",
		"--ignore-deps",
		"--timeout=15s",
		"--json",
	)
	if seed.exitCode != 0 || len(bytes.TrimSpace(seed.stderr)) != 0 {
		t.Fatalf("seed reserved reassign exit=%d stdout=%s stderr=%s", seed.exitCode, seed.stdout, seed.stderr)
	}
	fixture.waitForMarkerCount(t, 0, initialPrompt, 1)
	fixture.driveAssignmentStatus(t, source, beadID, "• Working (4s · esc to interrupt)", "working")
	before := fixture.readLedgerAssignment(t, beadID)
	if !before.ReservationRequired || !before.ReservationDiscovery || !before.ReservationCompleted || before.ReservationState != "reserved" ||
		!reflect.DeepEqual(before.ReservedPaths, reservedPaths) || len(before.ReservationIDs) != len(reservedPaths) || before.ReservationExpiresAt == nil {
		t.Fatalf("seed reservation metadata = %+v", before)
	}
	reservationMu.Lock()
	expectedReleaseAgent = before.AgentName
	reservationMu.Unlock()
	reassignedPrompt := fmt.Sprintf("NTM_ATOMIC_REASSIGN_RESERVED_TARGET_%d", time.Now().UnixNano())
	result := fixture.runNTM(t, env,
		"--json", "assign", fixture.session,
		"--repo="+fixture.projectDir,
		"--reassign="+beadID,
		"--to-pane="+target.ID,
		"--prompt="+reassignedPrompt,
		"--force",
		"--reserve-files=true",
		"--timeout=15s",
	)
	if result.exitCode != 0 || len(bytes.TrimSpace(result.stderr)) != 0 {
		t.Fatalf("reserved reassign exit=%d stdout=%s stderr=%s", result.exitCode, result.stdout, result.stderr)
	}
	var envelope atomicAssignmentReassignEnvelope
	decodeAtomicAssignmentJSON(t, result.stdout, &envelope)
	if !envelope.Success || envelope.Error != nil || envelope.Data == nil || envelope.Data.BeadID != beadID ||
		envelope.Data.AgentName == "" || envelope.Data.AgentName == before.AgentName || !envelope.Data.PromptSent ||
		!envelope.Data.FileReservationsTransferred || envelope.Data.FileReservationsReleasedFrom != len(before.ReservationIDs) ||
		envelope.Data.FileReservationsCreatedFor != len(reservedPaths) {
		if envelope.Data == nil {
			t.Fatalf("reserved reassign envelope = %+v", envelope)
		}
		t.Fatalf("reserved reassign envelope data = %+v error=%+v", *envelope.Data, envelope.Error)
	}
	fixture.waitForMarkerCount(t, 1, reassignedPrompt, 1)
	after := fixture.readLedgerAssignment(t, beadID)
	if after.ClaimActor != before.ClaimActor || after.IdempotencyKey == before.IdempotencyKey || after.AgentName != envelope.Data.AgentName ||
		after.OccupancyKey != target.ID || after.DispatchTarget != target.ID || after.DispatchState != "sent" ||
		!after.ReservationRequired || after.ReservationDiscovery || !after.ReservationCompleted || after.ReservationState != "reserved" ||
		!reflect.DeepEqual(after.ReservedPaths, reservedPaths) || len(after.ReservationIDs) != len(reservedPaths) ||
		reflect.DeepEqual(after.ReservationIDs, before.ReservationIDs) || after.ReservationExpiresAt == nil {
		t.Fatalf("reserved reassign durable transfer: before=%+v after=%+v", before, after)
	}
	reservationMu.Lock()
	gotGrantCalls := grantCalls
	gotReleaseCalls := releaseCalls
	gotReleasedIDs := append([]int(nil), releasedIDs...)
	gotGrantedAgents := append([]string(nil), grantedAgents...)
	active := append([]map[string]any(nil), activeReservations...)
	reservationMu.Unlock()
	if gotGrantCalls != 2 || gotReleaseCalls != 1 || !reflect.DeepEqual(gotReleasedIDs, before.ReservationIDs) ||
		!reflect.DeepEqual(gotGrantedAgents, []string{before.AgentName, after.AgentName}) || len(active) != len(reservedPaths) {
		t.Fatalf("reservation transfer calls grants=%d releases=%d released=%v agents=%v active=%v", gotGrantCalls, gotReleaseCalls, gotReleasedIDs, gotGrantedAgents, active)
	}
	fixture.assertBead(t, beadID, "in_progress", before.ClaimActor)
}

func TestE2EAtomicRebalanceUsesAtomicReplacement(t *testing.T) {
	CommonE2EPrerequisites(t)
	testutil.RequireTmuxThrottled(t)

	seedOverloadedSource := func(t *testing.T, fixture *atomicAssignmentCLIFixture, source atomicAssignmentPane, target atomicAssignmentPane, sourceBeadID string, sourceRecord *atomicAssignmentRecord) (string, *atomicAssignmentRecord) {
		t.Helper()
		shadowBeadID := fixture.createBead(t, "Rebalance shadow assignment")
		shadowPrompt := fmt.Sprintf("NTM_ATOMIC_REBALANCE_SHADOW_%d", time.Now().UnixNano())
		seedShadow := fixture.runNTM(t, nil, atomicDirectArgsForSelector(fixture, target.ID, shadowBeadID, shadowPrompt, false)...)
		if seedShadow.exitCode != 0 || len(bytes.TrimSpace(seedShadow.stderr)) != 0 {
			t.Fatalf("seed rebalance shadow exit=%d stdout=%s stderr=%s", seedShadow.exitCode, seedShadow.stdout, seedShadow.stderr)
		}
		fixture.waitForEndpointMarkerCount(t, target, shadowPrompt, 1)
		fixture.setLedgerAssignmentFields(t, shadowBeadID, map[string]any{
			"pane":            source.Index,
			"occupancy_key":   source.ID,
			"dispatch_target": source.ID,
			"agent_type":      sourceRecord.AgentType,
			"agent_name":      sourceRecord.AgentName,
			"status":          "assigned",
		})
		shadow := fixture.readLedgerAssignment(t, shadowBeadID)
		if shadow.BeadID == sourceBeadID || shadow.OccupancyKey != source.ID || shadow.Status != "assigned" {
			t.Fatalf("invalid rebalance overload fixture: source=%+v shadow=%+v", sourceRecord, shadow)
		}
		return shadowPrompt, shadow
	}

	t.Run("successful transfer preserves claim and moves exact reservations", func(t *testing.T) {
		fixture := newAtomicAssignmentCLIFixture(t)
		mail := newAtomicAssignmentMailStub(fixture.projectDir)
		defer mail.close()
		source := fixture.panes[0]
		target := fixture.panes[1]
		beadID, _, before := seedReservedWorkingAssignment(
			t, fixture, mail, source, "Atomic rebalance internal/cli/rebalance.go",
		)
		_, shadowBefore := seedOverloadedSource(t, fixture, source, target, beadID, before)
		fixture.primeEndpointForSafeDispatch(t, target)

		result := fixture.runNTM(t, mail.env(),
			"rebalance", fixture.session,
			"--format=json",
			"--apply",
		)
		if result.exitCode != 0 || len(bytes.TrimSpace(result.stderr)) != 0 {
			t.Fatalf("atomic rebalance exit=%d stdout=%s stderr=%s", result.exitCode, result.stdout, result.stderr)
		}
		var envelope struct {
			Session   string         `json:"session"`
			Applied   bool           `json:"applied"`
			Before    map[string]int `json:"before"`
			After     map[string]int `json:"after"`
			Transfers []struct {
				BeadID     string `json:"bead_id"`
				FromPaneID string `json:"from_pane_id"`
				ToPaneID   string `json:"to_pane_id"`
			} `json:"transfers"`
		}
		decodeAtomicAssignmentJSON(t, result.stdout, &envelope)
		if envelope.Session != fixture.session || !envelope.Applied || len(envelope.Transfers) != 1 ||
			envelope.Transfers[0].BeadID != beadID || envelope.Transfers[0].FromPaneID != source.ID ||
			envelope.Transfers[0].ToPaneID != target.ID ||
			!reflect.DeepEqual(envelope.Before, map[string]int{source.ID: 2, target.ID: 0}) ||
			!reflect.DeepEqual(envelope.After, map[string]int{source.ID: 1, target.ID: 1}) {
			t.Fatalf("atomic rebalance envelope = %+v", envelope)
		}

		prompt := fmt.Sprintf(
			"Work on bead %s: %s. This work was transferred by ntm rebalance. Check dependencies first with `br dep tree %s`.",
			beadID, before.BeadTitle, beadID,
		)
		terminalMarker := "This work was transferred by ntm rebalance."
		fixture.waitForEndpointMarkerCount(t, target, terminalMarker, 1)
		fixture.assertEndpointMarkerCounts(t, terminalMarker, map[string]atomicAssignmentPane{
			source.Target: source, target.Target: target,
		}, map[string]int{target.Target: 1})

		after := fixture.readLedgerAssignment(t, beadID)
		if after.Status != "assigned" || after.ClaimActor != before.ClaimActor || after.IdempotencyKey == before.IdempotencyKey ||
			after.OccupancyKey != target.ID || after.DispatchTarget != target.ID || after.DispatchState != "sent" ||
			after.DispatchReceiptID == "" || after.DispatchReceiptID == before.DispatchReceiptID || after.PromptSent != prompt ||
			!after.ReservationRequired || !after.ReservationCompleted || after.ReservationState != "reserved" ||
			!reflect.DeepEqual(after.ReservationRequested, before.ReservationRequested) ||
			!reflect.DeepEqual(after.ReservedPaths, before.ReservedPaths) ||
			reflect.DeepEqual(after.ReservationIDs, before.ReservationIDs) {
			t.Fatalf("atomic rebalance durable transfer: before=%+v after=%+v", before, after)
		}
		if shadowAfter := fixture.readLedgerAssignment(t, shadowBefore.BeadID); !reflect.DeepEqual(shadowAfter, shadowBefore) {
			t.Fatalf("atomic rebalance mutated non-transferred assignment: before=%+v after=%+v", shadowBefore, shadowAfter)
		}
		snapshot := mail.snapshot()
		if snapshot.ReserveCalls != 2 || snapshot.ReleaseCalls != 1 ||
			!reflect.DeepEqual(snapshot.ReleasedIDs, before.ReservationIDs) || len(snapshot.GrantedAgents) != 2 ||
			snapshot.GrantedAgents[0] != before.AgentName || snapshot.GrantedAgents[1] != after.AgentName ||
			len(snapshot.Active) != len(after.ReservationIDs) {
			t.Fatalf("atomic rebalance reservation transfer = %+v", snapshot)
		}
		fixture.assertBead(t, beadID, "in_progress", before.ClaimActor)
	})

	t.Run("fresh process retries known-unsent durable generation exactly once", func(t *testing.T) {
		fixture := newAtomicAssignmentCLIFixture(t)
		mail := newAtomicAssignmentMailStub(fixture.projectDir)
		defer mail.close()
		source := fixture.panes[0]
		target := fixture.panes[1]
		beadID, _, before := seedReservedWorkingAssignment(
			t, fixture, mail, source, "Atomic rebalance recovery internal/cli/rebalance.go",
		)
		_, shadowBefore := seedOverloadedSource(t, fixture, source, target, beadID, before)
		fixture.primeEndpointForSafeDispatch(t, target)
		originalPaneIDs := map[int]string{0: source.ID, 1: target.ID}
		path, fired := fixture.armGuardedClaimInfoThenKillTmux(t, "rebalance-send-failure")
		failureEnv := mail.env()
		failureEnv["PATH"] = path

		failed := fixture.runNTM(t, failureEnv,
			"rebalance", fixture.session,
			"--format=json",
			"--apply",
		)
		if failed.exitCode != 1 || len(bytes.TrimSpace(failed.stderr)) != 0 ||
			!bytes.Contains(failed.stdout, []byte("failed to apply transfers")) {
			t.Fatalf("failed rebalance exit=%d stdout=%s stderr=%s", failed.exitCode, failed.stdout, failed.stderr)
		}
		if _, err := os.Stat(fired); err != nil {
			t.Fatalf("rebalance guarded-info outage did not fire: %v", err)
		}
		pending := fixture.readLedgerAssignment(t, beadID)
		if pending.Status != "claimed" || pending.ClaimState != "claimed" || pending.ClaimActor != before.ClaimActor ||
			pending.IdempotencyKey == before.IdempotencyKey || !strings.HasSuffix(pending.IdempotencyKey, ":ntm-rebalance-v1") ||
			pending.OccupancyKey != target.ID || pending.DispatchTarget != target.ID || pending.DispatchState != "pending" ||
			pending.DispatchAttempts != 1 || pending.PendingPrompt == "" || pending.PromptSent != "" || pending.LastDispatchError == "" ||
			pending.DispatchedAt != nil || pending.DispatchReceiptID != "" || pending.ReservationState != "reserved" ||
			!reflect.DeepEqual(pending.ReservationRequested, before.ReservationRequested) ||
			!reflect.DeepEqual(pending.ReservedPaths, before.ReservedPaths) || reflect.DeepEqual(pending.ReservationIDs, before.ReservationIDs) {
			t.Fatalf("known-unsent rebalance is not durably recoverable: before=%+v pending=%+v", before, pending)
		}
		callsAfterFailure := mail.snapshot()
		if callsAfterFailure.ReserveCalls != 2 || callsAfterFailure.ReleaseCalls != 1 ||
			!reflect.DeepEqual(callsAfterFailure.ReleasedIDs, before.ReservationIDs) ||
			!reflect.DeepEqual(callsAfterFailure.GrantedAgents, []string{before.AgentName, pending.AgentName}) ||
			len(callsAfterFailure.Active) != len(pending.ReservationIDs) {
			t.Fatalf("known-unsent rebalance reservation boundary = %+v", callsAfterFailure)
		}
		fixture.assertBead(t, beadID, "in_progress", before.ClaimActor)

		fixture.startAgentPanes(t)
		for pane, paneID := range originalPaneIDs {
			if fixture.panes[pane].ID != paneID {
				t.Fatalf("private tmux restart changed pane %d ID: before=%s after=%s", pane, paneID, fixture.panes[pane].ID)
			}
		}
		source, target = fixture.panes[0], fixture.panes[1]
		terminalMarker := "This work was transferred by ntm rebalance."
		fixture.assertEndpointMarkerCounts(t, terminalMarker, map[string]atomicAssignmentPane{
			source.Target: source, target.Target: target,
		}, nil)
		fixture.primeEndpointForSafeDispatch(t, target)
		time.Sleep(5500 * time.Millisecond)

		// The target reservation is already durably complete, so same-key
		// recovery must not require Agent Mail to be reachable again.
		recoveredResult := fixture.runNTM(t, nil,
			"rebalance", fixture.session,
			"--format=json",
			"--apply",
		)
		if recoveredResult.exitCode != 0 || len(bytes.TrimSpace(recoveredResult.stderr)) != 0 {
			t.Fatalf("recover rebalance exit=%d stdout=%s stderr=%s", recoveredResult.exitCode, recoveredResult.stdout, recoveredResult.stderr)
		}
		var recoveredEnvelope struct {
			Applied   bool           `json:"applied"`
			Before    map[string]int `json:"before"`
			After     map[string]int `json:"after"`
			Transfers []struct {
				BeadID     string `json:"bead_id"`
				FromPaneID string `json:"from_pane_id"`
				ToPaneID   string `json:"to_pane_id"`
				Reason     string `json:"reason"`
			} `json:"transfers"`
		}
		decodeAtomicAssignmentJSON(t, recoveredResult.stdout, &recoveredEnvelope)
		if !recoveredEnvelope.Applied || len(recoveredEnvelope.Transfers) != 1 ||
			recoveredEnvelope.Transfers[0].BeadID != beadID || recoveredEnvelope.Transfers[0].Reason != "recover_pending_dispatch" ||
			recoveredEnvelope.Transfers[0].FromPaneID != target.ID || recoveredEnvelope.Transfers[0].ToPaneID != target.ID ||
			!reflect.DeepEqual(recoveredEnvelope.Before, map[string]int{source.ID: 1, target.ID: 1}) ||
			!reflect.DeepEqual(recoveredEnvelope.After, recoveredEnvelope.Before) {
			t.Fatalf("fresh-process rebalance recovery envelope = %+v", recoveredEnvelope)
		}
		fixture.waitForEndpointMarkerCount(t, target, terminalMarker, 1)
		fixture.assertEndpointMarkerCounts(t, terminalMarker, map[string]atomicAssignmentPane{
			source.Target: source, target.Target: target,
		}, map[string]int{target.Target: 1})

		after := fixture.readLedgerAssignment(t, beadID)
		if after.Status != "assigned" || after.IdempotencyKey != pending.IdempotencyKey || after.ClaimActor != pending.ClaimActor ||
			after.ClaimAttempts != pending.ClaimAttempts || after.OccupancyKey != target.ID || after.DispatchTarget != target.ID ||
			after.DispatchState != "sent" || after.DispatchAttempts != 2 || after.DispatchReceiptID == "" ||
			after.PromptSent != pending.PendingPrompt || after.PendingPrompt != "" ||
			!reflect.DeepEqual(after.ReservationIDs, pending.ReservationIDs) || !reflect.DeepEqual(after.ReservedPaths, pending.ReservedPaths) {
			t.Fatalf("fresh-process rebalance recovery state: pending=%+v after=%+v", pending, after)
		}
		callsAfterRecovery := mail.snapshot()
		if callsAfterRecovery.ReserveCalls != callsAfterFailure.ReserveCalls ||
			callsAfterRecovery.ReleaseCalls != callsAfterFailure.ReleaseCalls ||
			!reflect.DeepEqual(callsAfterRecovery.ReleasedIDs, callsAfterFailure.ReleasedIDs) ||
			!reflect.DeepEqual(callsAfterRecovery.Active, callsAfterFailure.Active) {
			t.Fatalf("fresh recovery duplicated reservation side effects: before=%+v after=%+v", callsAfterFailure, callsAfterRecovery)
		}
		if shadowAfter := fixture.readLedgerAssignment(t, shadowBefore.BeadID); !reflect.DeepEqual(shadowAfter, shadowBefore) {
			t.Fatalf("fresh recovery mutated shadow assignment: before=%+v after=%+v", shadowBefore, shadowAfter)
		}
		fixture.assertBead(t, beadID, "in_progress", before.ClaimActor)
	})

	t.Run("multi-window duplicate local indexes remain physically distinct", func(t *testing.T) {
		fixture := newAtomicAssignmentCLIFixture(t)
		panes := fixture.addSecondAgentWindow(t)
		source := panes["0.1"]
		target := panes["1.1"]
		spare := panes["0.0"]
		beadID := fixture.createBead(t, "Atomic rebalance duplicate local pane indexes")
		sourcePrompt := fmt.Sprintf("NTM_ATOMIC_REBALANCE_MULTI_SOURCE_%d", time.Now().UnixNano())
		seedSource := fixture.runNTM(t, nil, atomicDirectArgsForSelector(fixture, source.ID, beadID, sourcePrompt, false)...)
		if seedSource.exitCode != 0 || len(bytes.TrimSpace(seedSource.stderr)) != 0 {
			t.Fatalf("seed multi-window rebalance exit=%d stdout=%s stderr=%s", seedSource.exitCode, seedSource.stdout, seedSource.stderr)
		}
		fixture.waitForEndpointMarkerCount(t, source, sourcePrompt, 1)
		fixture.driveAssignmentStatus(t, source, beadID, "• Working (4s · esc to interrupt)", "working")
		before := fixture.readLedgerAssignment(t, beadID)
		_, shadowBefore := seedOverloadedSource(t, fixture, source, spare, beadID, before)
		fixture.primeEndpointForSafeDispatch(t, target)

		result := fixture.runNTM(t, nil,
			"rebalance", fixture.session,
			"--filter=cc",
			"--format=json",
			"--apply",
		)
		if result.exitCode != 0 || len(bytes.TrimSpace(result.stderr)) != 0 {
			t.Fatalf("multi-window rebalance exit=%d stdout=%s stderr=%s", result.exitCode, result.stdout, result.stderr)
		}
		var envelope struct {
			Applied   bool           `json:"applied"`
			Before    map[string]int `json:"before"`
			After     map[string]int `json:"after"`
			Workloads []struct {
				Pane       int    `json:"pane"`
				PaneTarget string `json:"pane_target"`
				PaneID     string `json:"pane_id"`
			} `json:"workloads"`
			Transfers []struct {
				BeadID     string `json:"bead_id"`
				FromPane   int    `json:"from_pane"`
				FromPaneID string `json:"from_pane_id"`
				ToPane     int    `json:"to_pane"`
				ToPaneID   string `json:"to_pane_id"`
			} `json:"transfers"`
		}
		decodeAtomicAssignmentJSON(t, result.stdout, &envelope)
		if !envelope.Applied || len(envelope.Workloads) != 2 || len(envelope.Transfers) != 1 ||
			envelope.Transfers[0].BeadID != beadID || envelope.Transfers[0].FromPane != 1 || envelope.Transfers[0].ToPane != 1 ||
			envelope.Transfers[0].FromPaneID != source.ID || envelope.Transfers[0].ToPaneID != target.ID ||
			!reflect.DeepEqual(envelope.Before, map[string]int{source.ID: 2, target.ID: 0}) ||
			!reflect.DeepEqual(envelope.After, map[string]int{source.ID: 1, target.ID: 1}) {
			t.Fatalf("multi-window canonical rebalance envelope = %+v", envelope)
		}
		seenWorkloads := map[string]string{}
		for _, workload := range envelope.Workloads {
			if workload.Pane != 1 {
				t.Fatalf("multi-window Claude workload local pane=%d, want duplicate index 1: %+v", workload.Pane, workload)
			}
			seenWorkloads[workload.PaneID] = workload.PaneTarget
		}
		if !reflect.DeepEqual(seenWorkloads, map[string]string{source.ID: source.Target, target.ID: target.Target}) {
			t.Fatalf("multi-window workload identities = %v", seenWorkloads)
		}

		terminalMarker := "This work was transferred by ntm rebalance."
		fixture.waitForEndpointMarkerCount(t, target, terminalMarker, 1)
		fixture.assertEndpointMarkerCounts(t, terminalMarker, panes, map[string]int{target.Target: 1})
		after := fixture.readLedgerAssignment(t, beadID)
		if after.Status != "assigned" || after.ClaimActor != before.ClaimActor || after.IdempotencyKey == before.IdempotencyKey ||
			after.OccupancyKey != target.ID || after.DispatchTarget != target.ID || after.DispatchState != "sent" ||
			after.DispatchReceiptID == "" || after.PromptSent == "" {
			t.Fatalf("multi-window canonical rebalance state: before=%+v after=%+v", before, after)
		}
		if shadowAfter := fixture.readLedgerAssignment(t, shadowBefore.BeadID); !reflect.DeepEqual(shadowAfter, shadowBefore) {
			t.Fatalf("multi-window rebalance mutated shadow: before=%+v after=%+v", shadowBefore, shadowAfter)
		}
		fixture.assertBead(t, beadID, "in_progress", before.ClaimActor)
	})

	t.Run("changed live owner has no partial state", func(t *testing.T) {
		fixture := newAtomicAssignmentCLIFixture(t)
		source := fixture.panes[0]
		target := fixture.panes[1]
		beadID := fixture.createBead(t, "Atomic rebalance changed owner")
		sourcePrompt := fmt.Sprintf("NTM_ATOMIC_REBALANCE_OWNER_SOURCE_%d", time.Now().UnixNano())
		seedSource := fixture.runNTM(t, nil, atomicDirectArgsForSelector(fixture, source.ID, beadID, sourcePrompt, false)...)
		if seedSource.exitCode != 0 || len(bytes.TrimSpace(seedSource.stderr)) != 0 {
			t.Fatalf("seed owner rebalance exit=%d stdout=%s stderr=%s", seedSource.exitCode, seedSource.stdout, seedSource.stderr)
		}
		fixture.waitForEndpointMarkerCount(t, source, sourcePrompt, 1)
		fixture.setLedgerAssignmentFields(t, beadID, map[string]any{
			"status":     "working",
			"started_at": time.Now().UTC(),
		})
		before := fixture.readLedgerAssignment(t, beadID)
		_, shadowBefore := seedOverloadedSource(t, fixture, source, target, beadID, before)
		fixture.mustBR(t, "update", beadID, "--assignee=DifferentRebalanceOwner", "--json")
		fixture.primeEndpointForSafeDispatch(t, target)
		candidateMarker := "This work was transferred by ntm rebalance."

		result := fixture.runNTM(t, nil,
			"rebalance", fixture.session,
			"--format=json",
			"--apply",
		)
		if result.exitCode != 1 || len(bytes.TrimSpace(result.stderr)) != 0 ||
			!bytes.Contains(result.stdout, []byte("authorized generation state")) {
			t.Fatalf("changed-owner rebalance exit=%d stdout=%s stderr=%s", result.exitCode, result.stdout, result.stderr)
		}
		var envelope struct {
			Success   bool   `json:"success"`
			Session   string `json:"session"`
			ErrorCode string `json:"error_code"`
			Error     string `json:"error"`
		}
		decodeAtomicAssignmentJSON(t, result.stdout, &envelope)
		if envelope.Success || envelope.Session != fixture.session || envelope.ErrorCode != "BEAD_INELIGIBLE" ||
			!strings.Contains(envelope.Error, "DifferentRebalanceOwner") || !strings.Contains(envelope.Error, "authorized generation state") {
			t.Fatalf("changed-owner rebalance envelope = %+v", envelope)
		}
		if after := fixture.readLedgerAssignment(t, beadID); !reflect.DeepEqual(after, before) {
			t.Fatalf("changed-owner rebalance mutated source: before=%+v after=%+v", before, after)
		}
		if shadowAfter := fixture.readLedgerAssignment(t, shadowBefore.BeadID); !reflect.DeepEqual(shadowAfter, shadowBefore) {
			t.Fatalf("changed-owner rebalance mutated shadow: before=%+v after=%+v", shadowBefore, shadowAfter)
		}
		fixture.assertEndpointMarkerCounts(t, candidateMarker, map[string]atomicAssignmentPane{
			source.Target: source, target.Target: target,
		}, nil)
		fixture.assertBead(t, beadID, "in_progress", "DifferentRebalanceOwner")
	})
}

func TestE2EAtomicRebalanceFailsClosedOnUnsafeDurablePaneIdentity(t *testing.T) {
	CommonE2EPrerequisites(t)
	testutil.RequireTmuxThrottled(t)

	type failureEnvelope struct {
		Success   bool              `json:"success"`
		Session   string            `json:"session"`
		ErrorCode string            `json:"error_code"`
		Error     string            `json:"error"`
		Workloads []json.RawMessage `json:"workloads"`
		Transfers []json.RawMessage `json:"transfers"`
	}
	assertFailureEnvelope := func(t *testing.T, fixture *atomicAssignmentCLIFixture, result atomicAssignmentProcessResult, wantCode string) {
		t.Helper()
		if result.exitCode != 1 || len(bytes.TrimSpace(result.stderr)) != 0 {
			t.Fatalf("fail-closed rebalance exit=%d stdout=%s stderr=%s", result.exitCode, result.stdout, result.stderr)
		}
		var envelope failureEnvelope
		decodeAtomicAssignmentJSON(t, result.stdout, &envelope)
		if envelope.Success || envelope.Session != fixture.session || envelope.ErrorCode != wantCode || envelope.Error == "" ||
			envelope.Workloads == nil || envelope.Transfers == nil || len(envelope.Workloads) != 0 || len(envelope.Transfers) != 0 {
			t.Fatalf("fail-closed rebalance envelope = %+v, want error_code=%s and non-null empty collections", envelope, wantCode)
		}
	}

	t.Run("active_assignment_physical_pane_disappeared", func(t *testing.T) {
		fixture := newAtomicAssignmentCLIFixture(t)
		source := fixture.panes[0]
		remaining := fixture.panes[1]
		beadID := fixture.createBead(t, "Rebalance fail closed missing physical pane")
		seedPrompt := fmt.Sprintf("NTM_ATOMIC_REBALANCE_MISSING_SEED_%d", time.Now().UnixNano())
		seed := fixture.runNTM(t, nil, atomicDirectArgsForSelector(fixture, source.ID, beadID, seedPrompt, false)...)
		if seed.exitCode != 0 || len(bytes.TrimSpace(seed.stderr)) != 0 {
			t.Fatalf("seed missing-pane rebalance exit=%d stdout=%s stderr=%s", seed.exitCode, seed.stdout, seed.stderr)
		}
		fixture.waitForEndpointMarkerCount(t, source, seedPrompt, 1)
		fixture.driveAssignmentStatus(t, source, beadID, "• Working (4s · esc to interrupt)", "working")
		beforeRecord := fixture.readLedgerAssignment(t, beadID)
		_, beforeLedger := readAtomicAssignmentLedgerAt(t, fixture.ledgerPath())
		_, beforeBackup := readAtomicAssignmentLedgerAt(t, fixture.ledgerPath()+".bak")
		fixture.mustTMUX(t, "kill-pane", "-t", source.ID)

		result := fixture.runNTM(t, nil,
			"rebalance", fixture.session,
			"--format=json",
			"--apply",
		)
		assertFailureEnvelope(t, fixture, result, "PANE_NOT_FOUND")
		if !bytes.Contains(result.stdout, []byte("physical pane "+source.ID+" is not present")) {
			t.Fatalf("missing-pane rebalance error did not identify durable physical pane %s: %s", source.ID, result.stdout)
		}
		if afterRecord := fixture.readLedgerAssignment(t, beadID); !reflect.DeepEqual(afterRecord, beforeRecord) {
			t.Fatalf("missing-pane rebalance mutated durable assignment: before=%+v after=%+v", beforeRecord, afterRecord)
		}
		_, afterLedger := readAtomicAssignmentLedgerAt(t, fixture.ledgerPath())
		if !bytes.Equal(afterLedger, beforeLedger) {
			t.Fatalf("missing-pane rebalance changed assignment ledger bytes:\nbefore=%s\nafter=%s", beforeLedger, afterLedger)
		}
		_, afterBackup := readAtomicAssignmentLedgerAt(t, fixture.ledgerPath()+".bak")
		if !bytes.Equal(afterBackup, beforeBackup) {
			t.Fatalf("missing-pane rebalance changed assignment backup bytes:\nbefore=%s\nafter=%s", beforeBackup, afterBackup)
		}
		fixture.assertEndpointMarkerCounts(t, "This work was transferred by ntm rebalance.", map[string]atomicAssignmentPane{
			remaining.Target: remaining,
		}, nil)
		fixture.assertBead(t, beadID, "in_progress", beforeRecord.ClaimActor)
	})

	t.Run("active_assignment_requires_pane_identity_migration", func(t *testing.T) {
		fixture := newAtomicAssignmentCLIFixture(t)
		source := fixture.panes[0]
		beadID := fixture.createBead(t, "Rebalance fail closed migration-required pane")
		seedPrompt := fmt.Sprintf("NTM_ATOMIC_REBALANCE_LEGACY_SEED_%d", time.Now().UnixNano())
		seed := fixture.runNTM(t, nil, atomicDirectArgsForSelector(fixture, source.ID, beadID, seedPrompt, false)...)
		if seed.exitCode != 0 || len(bytes.TrimSpace(seed.stderr)) != 0 {
			t.Fatalf("seed migration rebalance exit=%d stdout=%s stderr=%s", seed.exitCode, seed.stdout, seed.stderr)
		}
		fixture.waitForEndpointMarkerCount(t, source, seedPrompt, 1)
		fixture.driveAssignmentStatus(t, source, beadID, "• Working (4s · esc to interrupt)", "working")
		fixture.setLedgerAssignmentLegacyPaneTarget(t, beadID)
		_, legacyLedger := readAtomicAssignmentLedgerAt(t, fixture.ledgerPath())
		if err := os.WriteFile(fixture.ledgerPath()+".bak", legacyLedger, 0o600); err != nil {
			t.Fatalf("mirror migration fixture to assignment backup: %v", err)
		}
		beforeRecord := fixture.readLedgerAssignment(t, beadID)
		_, beforeLedger := readAtomicAssignmentLedgerAt(t, fixture.ledgerPath())
		_, beforeBackup := readAtomicAssignmentLedgerAt(t, fixture.ledgerPath()+".bak")

		result := fixture.runNTM(t, nil,
			"rebalance", fixture.session,
			"--format=json",
			"--apply",
		)
		assertFailureEnvelope(t, fixture, result, "PANE_IDENTITY_MIGRATION_REQUIRED")
		if afterRecord := fixture.readLedgerAssignment(t, beadID); !reflect.DeepEqual(afterRecord, beforeRecord) {
			t.Fatalf("migration-required rebalance mutated durable assignment: before=%+v after=%+v", beforeRecord, afterRecord)
		}
		_, afterLedger := readAtomicAssignmentLedgerAt(t, fixture.ledgerPath())
		if !bytes.Equal(afterLedger, beforeLedger) {
			t.Fatalf("migration-required rebalance changed assignment ledger bytes:\nbefore=%s\nafter=%s", beforeLedger, afterLedger)
		}
		_, afterBackup := readAtomicAssignmentLedgerAt(t, fixture.ledgerPath()+".bak")
		if !bytes.Equal(afterBackup, beforeBackup) {
			t.Fatalf("migration-required rebalance changed assignment backup bytes:\nbefore=%s\nafter=%s", beforeBackup, afterBackup)
		}
		fixture.assertEndpointMarkerCounts(t, "This work was transferred by ntm rebalance.", map[string]atomicAssignmentPane{
			fixture.panes[0].Target: fixture.panes[0],
			fixture.panes[1].Target: fixture.panes[1],
		}, nil)
		fixture.assertBead(t, beadID, "in_progress", beforeRecord.ClaimActor)
	})
}

func TestE2EAtomicAssignmentReservationSafetyContracts(t *testing.T) {
	CommonE2EPrerequisites(t)
	testutil.RequireTmuxThrottled(t)

	t.Run("occupied_target_does_not_release_source_leases", func(t *testing.T) {
		fixture := newAtomicAssignmentCLIFixture(t)
		mail := newAtomicAssignmentMailStub(fixture.projectDir)
		defer mail.close()
		sourceBeadID, _, sourceBefore := seedReservedWorkingAssignment(t, fixture, mail, fixture.panes[0], "Reserved occupied source internal/cli/occupied_source.go")
		targetBeadID := fixture.createBead(t, "Reserved occupied target")
		targetPrompt := fmt.Sprintf("NTM_ATOMIC_RESERVED_OCCUPIED_TARGET_%d", time.Now().UnixNano())
		seedTarget := fixture.runNTM(t, nil, atomicDirectArgsForPane(fixture, 1, targetBeadID, targetPrompt, false)...)
		if seedTarget.exitCode != 0 || len(bytes.TrimSpace(seedTarget.stderr)) != 0 {
			t.Fatalf("seed occupied target exit=%d stdout=%s stderr=%s", seedTarget.exitCode, seedTarget.stdout, seedTarget.stderr)
		}
		fixture.waitForMarkerCount(t, 1, targetPrompt, 1)
		beforeCalls := mail.snapshot()
		candidatePrompt := fmt.Sprintf("NTM_ATOMIC_RESERVED_OCCUPIED_CANDIDATE_%d", time.Now().UnixNano())
		result := fixture.runNTM(t, mail.env(),
			"--json", "assign", fixture.session,
			"--repo="+fixture.projectDir,
			"--reassign="+sourceBeadID,
			"--to-pane="+fixture.panes[1].ID,
			"--prompt="+candidatePrompt,
			"--force",
			"--reserve-files=true",
			"--timeout=15s",
		)
		if result.exitCode != 1 || len(bytes.TrimSpace(result.stderr)) != 0 {
			t.Fatalf("reserved occupied reassign exit=%d stdout=%s stderr=%s", result.exitCode, result.stdout, result.stderr)
		}
		var envelope atomicAssignmentReassignEnvelope
		decodeAtomicAssignmentJSON(t, result.stdout, &envelope)
		if envelope.Success || envelope.Error == nil || envelope.Error.Code != "TARGET_BUSY" || !strings.Contains(envelope.Error.Message, targetBeadID) {
			t.Fatalf("reserved occupied reassign envelope = %+v", envelope)
		}
		afterCalls := mail.snapshot()
		if afterCalls.ReleaseCalls != beforeCalls.ReleaseCalls || afterCalls.ReserveCalls != beforeCalls.ReserveCalls {
			t.Fatalf("occupied preflight touched Agent Mail: before=%+v after=%+v", beforeCalls, afterCalls)
		}
		if sourceAfter := fixture.readLedgerAssignment(t, sourceBeadID); !reflect.DeepEqual(sourceAfter, sourceBefore) {
			t.Fatalf("occupied preflight mutated source leases: before=%+v after=%+v", sourceBefore, sourceAfter)
		}
		fixture.assertMarkerCounts(t, candidatePrompt, map[int]int{0: 0, 1: 0})
	})

	t.Run("legacy_ambiguous_target_does_not_release_source_leases", func(t *testing.T) {
		fixture := newAtomicAssignmentCLIFixture(t)
		panes := fixture.addSecondAgentWindow(t)
		mail := newAtomicAssignmentMailStub(fixture.projectDir)
		defer mail.close()
		sourceBeadID, _, sourceBefore := seedReservedWorkingAssignment(t, fixture, mail, panes["0.1"], "Reserved legacy source internal/cli/legacy_source.go")
		legacyBeadID := fixture.createBead(t, "Legacy ambiguous reassign owner")
		legacyPrompt := fmt.Sprintf("NTM_ATOMIC_RESERVED_LEGACY_OWNER_%d", time.Now().UnixNano())
		legacySource := panes["0.0"]
		legacySeed := fixture.runNTM(t, nil, atomicDirectArgsForSelector(fixture, legacySource.ID, legacyBeadID, legacyPrompt, false)...)
		if legacySeed.exitCode != 0 || len(bytes.TrimSpace(legacySeed.stderr)) != 0 {
			t.Fatalf("seed legacy owner exit=%d stdout=%s stderr=%s", legacySeed.exitCode, legacySeed.stdout, legacySeed.stderr)
		}
		fixture.waitForEndpointMarkerCount(t, legacySource, legacyPrompt, 1)
		fixture.setLedgerAssignmentLegacyPaneTarget(t, legacyBeadID)
		legacyOwner := fixture.readLedgerAssignment(t, legacyBeadID)
		if legacyOwner.Pane != panes["0.0"].Index || legacyOwner.OccupancyKey != "" || legacyOwner.DispatchTarget != "" ||
			(legacyOwner.Status != "assigned" && legacyOwner.Status != "working") {
			t.Fatalf("legacy occupancy fixture is not an active index-only row: %+v", legacyOwner)
		}
		beforeCalls := mail.snapshot()
		candidatePrompt := fmt.Sprintf("NTM_ATOMIC_RESERVED_LEGACY_CANDIDATE_%d", time.Now().UnixNano())
		result := fixture.runNTM(t, mail.env(),
			"--json", "assign", fixture.session,
			"--repo="+fixture.projectDir,
			"--reassign="+sourceBeadID,
			"--to-pane="+panes["1.0"].ID,
			"--prompt="+candidatePrompt,
			"--force",
			"--reserve-files=true",
			"--timeout=15s",
		)
		if result.exitCode != 1 || len(bytes.TrimSpace(result.stderr)) != 0 {
			t.Fatalf("legacy ambiguous reassign exit=%d stdout=%s stderr=%s", result.exitCode, result.stdout, result.stderr)
		}
		var envelope atomicAssignmentReassignEnvelope
		decodeAtomicAssignmentJSON(t, result.stdout, &envelope)
		if envelope.Success || envelope.Error == nil || envelope.Error.Code != "PANE_IDENTITY_MIGRATION_REQUIRED" ||
			!strings.Contains(envelope.Error.Message, legacyBeadID) || !strings.Contains(envelope.Error.Message, "canonical pane identity migration") {
			t.Fatalf("legacy ambiguous reassign envelope = %+v", envelope)
		}
		afterCalls := mail.snapshot()
		if afterCalls.ReleaseCalls != beforeCalls.ReleaseCalls || afterCalls.ReserveCalls != beforeCalls.ReserveCalls {
			t.Fatalf("legacy target preflight touched Agent Mail: before=%+v after=%+v", beforeCalls, afterCalls)
		}
		if sourceAfter := fixture.readLedgerAssignment(t, sourceBeadID); !reflect.DeepEqual(sourceAfter, sourceBefore) {
			t.Fatalf("legacy target preflight mutated source leases: before=%+v after=%+v", sourceBefore, sourceAfter)
		}
		fixture.assertEndpointMarkerCounts(t, candidatePrompt, panes, nil)
	})

	t.Run("redaction_block_does_not_release_source_leases", func(t *testing.T) {
		fixture := newAtomicAssignmentCLIFixture(t)
		mail := newAtomicAssignmentMailStub(fixture.projectDir)
		defer mail.close()
		beadID, _, before := seedReservedWorkingAssignment(t, fixture, mail, fixture.panes[0], "Reserved redaction source internal/cli/redaction_source.go")
		beforeCalls := mail.snapshot()
		secret := "NTM_E2E_RESERVED_REDACTION_SECRET_987654321"
		result := fixture.runNTM(t, mail.env(),
			"--json", "assign", fixture.session,
			"--repo="+fixture.projectDir,
			"--reassign="+beadID,
			"--to-pane="+fixture.panes[1].ID,
			"--prompt=Use password="+secret+" while reassigning",
			"--force",
			"--reserve-files=true",
			"--redact=block",
			"--timeout=15s",
		)
		if result.exitCode != 1 || len(bytes.TrimSpace(result.stderr)) != 0 {
			t.Fatalf("reserved redaction reassign exit=%d stdout=%s stderr=%s", result.exitCode, result.stdout, result.stderr)
		}
		fixture.assertSecretAbsent(t, secret, result.stdout, result.stderr)
		var envelope atomicAssignmentReassignEnvelope
		decodeAtomicAssignmentJSON(t, result.stdout, &envelope)
		if envelope.Success || envelope.Error == nil || envelope.Error.Code != "REDACTION_BLOCKED" {
			t.Fatalf("reserved redaction reassign envelope = %+v", envelope)
		}
		afterCalls := mail.snapshot()
		if afterCalls.ReleaseCalls != beforeCalls.ReleaseCalls || afterCalls.ReserveCalls != beforeCalls.ReserveCalls {
			t.Fatalf("redaction preflight touched Agent Mail: before=%+v after=%+v", beforeCalls, afterCalls)
		}
		if after := fixture.readLedgerAssignment(t, beadID); !reflect.DeepEqual(after, before) {
			t.Fatalf("redaction preflight mutated source lease metadata: before=%+v after=%+v", before, after)
		}
		fixture.assertMarkerCounts(t, secret, map[int]int{0: 0, 1: 0})
	})

	for _, terminal := range []struct {
		name          string
		trackerStatus string
		set           func(*testing.T, *atomicAssignmentCLIFixture, string)
	}{
		{name: "closed", trackerStatus: "closed", set: func(t *testing.T, fixture *atomicAssignmentCLIFixture, beadID string) {
			fixture.mustBR(t, "close", beadID, "--reason=reserved-terminal-e2e", "--json")
		}},
		{name: "tombstoned", trackerStatus: "tombstone", set: func(t *testing.T, fixture *atomicAssignmentCLIFixture, beadID string) {
			fixture.mustBR(t, "delete", beadID, "--reason=reserved-terminal-e2e", "--json")
		}},
	} {
		terminal := terminal
		t.Run("externally_"+terminal.name+"_source_has_zero_side_effects", func(t *testing.T) {
			fixture := newAtomicAssignmentCLIFixture(t)
			mail := newAtomicAssignmentMailStub(fixture.projectDir)
			defer mail.close()
			beadID, _, before := seedReservedWorkingAssignment(t, fixture, mail, fixture.panes[0], "Reserved terminal source internal/cli/terminal_source.go")
			terminal.set(t, fixture, beadID)
			beforeCalls := mail.snapshot()
			candidatePrompt := fmt.Sprintf("NTM_ATOMIC_RESERVED_TERMINAL_CANDIDATE_%d", time.Now().UnixNano())
			result := fixture.runNTM(t, mail.env(),
				"--json", "assign", fixture.session,
				"--repo="+fixture.projectDir,
				"--reassign="+beadID,
				"--to-pane="+fixture.panes[1].ID,
				"--prompt="+candidatePrompt,
				"--force",
				"--reserve-files=true",
				"--timeout=15s",
			)
			if result.exitCode != 1 || len(bytes.TrimSpace(result.stderr)) != 0 {
				t.Fatalf("terminal source reassign exit=%d stdout=%s stderr=%s", result.exitCode, result.stdout, result.stderr)
			}
			var envelope atomicAssignmentReassignEnvelope
			decodeAtomicAssignmentJSON(t, result.stdout, &envelope)
			if envelope.Success || envelope.Error == nil || envelope.Error.Code != "BEAD_INELIGIBLE" ||
				!strings.Contains(envelope.Error.Message, terminal.trackerStatus) || !strings.Contains(envelope.Error.Message, "authorized generation state") {
				t.Fatalf("terminal source reassign envelope = %+v", envelope)
			}
			afterCalls := mail.snapshot()
			if afterCalls.ReleaseCalls != beforeCalls.ReleaseCalls || afterCalls.ReserveCalls != beforeCalls.ReserveCalls {
				t.Fatalf("terminal tracker gate touched Agent Mail: before=%+v after=%+v", beforeCalls, afterCalls)
			}
			if after := fixture.readLedgerAssignment(t, beadID); !reflect.DeepEqual(after, before) {
				t.Fatalf("terminal tracker gate mutated source: before=%+v after=%+v", before, after)
			}
			fixture.assertMarkerCounts(t, candidatePrompt, map[int]int{0: 0, 1: 0})
		})
	}

	t.Run("changed_live_owner_does_not_release_source_leases", func(t *testing.T) {
		fixture := newAtomicAssignmentCLIFixture(t)
		mail := newAtomicAssignmentMailStub(fixture.projectDir)
		defer mail.close()
		beadID, prompt, before := seedReservedWorkingAssignment(t, fixture, mail, fixture.panes[0], "Reserved owner source internal/cli/owner_source.go")
		fixture.mustBR(t, "update", beadID, "--assignee=DifferentLiveOwner", "--json")
		fixture.assertBead(t, beadID, "in_progress", "DifferentLiveOwner")
		callsBefore := mail.snapshot()

		result := fixture.runNTM(t, mail.env(),
			"--json", "assign", fixture.session,
			"--repo="+fixture.projectDir,
			"--reassign="+beadID,
			"--to-pane="+fixture.panes[1].ID,
			"--prompt="+prompt+"_CHANGED_OWNER",
			"--force",
			"--reserve-files=true",
			"--timeout=15s",
		)
		if result.exitCode != 1 || len(bytes.TrimSpace(result.stderr)) != 0 {
			t.Fatalf("changed-owner reassign exit=%d stdout=%s stderr=%s", result.exitCode, result.stdout, result.stderr)
		}
		var envelope atomicAssignmentReassignEnvelope
		decodeAtomicAssignmentJSON(t, result.stdout, &envelope)
		if envelope.Success || envelope.Error == nil || envelope.Error.Code != "BEAD_INELIGIBLE" ||
			!strings.Contains(envelope.Error.Message, "DifferentLiveOwner") || !strings.Contains(envelope.Error.Message, "authorized generation state") {
			t.Fatalf("changed-owner reassign envelope = %+v", envelope)
		}
		callsAfter := mail.snapshot()
		if callsAfter.ReserveCalls != callsBefore.ReserveCalls || callsAfter.ReleaseCalls != callsBefore.ReleaseCalls ||
			callsAfter.SendCalls != callsBefore.SendCalls {
			t.Fatalf("changed-owner authorization touched Agent Mail: before=%+v after=%+v", callsBefore, callsAfter)
		}
		if after := fixture.readLedgerAssignment(t, beadID); !reflect.DeepEqual(after, before) {
			t.Fatalf("changed-owner authorization mutated source assignment: before=%+v after=%+v", before, after)
		}
		fixture.assertMarkerCounts(t, prompt+"_CHANGED_OWNER", map[int]int{0: 0, 1: 0})
	})

	t.Run("wrong_reason_grant_preserves_handles_and_blocks_dispatch", func(t *testing.T) {
		fixture := newAtomicAssignmentCLIFixture(t)
		mail := newAtomicAssignmentMailStub(fixture.projectDir)
		defer mail.close()
		mail.setGrantReasonOverride("bead assignment: wrong-bead")
		beadID := fixture.createBead(t, "Wrong reason reservation internal/cli/wrong_reason.go")
		marker := fmt.Sprintf("NTM_ATOMIC_WRONG_REASON_%d", time.Now().UnixNano())
		templatePath := filepath.Join(fixture.projectDir, "atomic-wrong-reason-template.txt")
		if err := os.WriteFile(templatePath, []byte(marker+"::{TITLE}"), 0o600); err != nil {
			t.Fatalf("write wrong-reason template: %v", err)
		}
		result := fixture.runNTM(t, mail.env(),
			"assign", fixture.session,
			"--repo="+fixture.projectDir,
			"--pane="+fixture.panes[0].ID,
			"--beads="+beadID,
			"--template=custom",
			"--template-file="+templatePath,
			"--force",
			"--ignore-deps",
			"--reserve-files=true",
			"--timeout=15s",
			"--json",
		)
		if result.exitCode != 1 || len(bytes.TrimSpace(result.stderr)) != 0 {
			t.Fatalf("wrong-reason assignment exit=%d stdout=%s stderr=%s", result.exitCode, result.stdout, result.stderr)
		}
		var envelope atomicAssignmentDirectEnvelope
		decodeAtomicAssignmentJSON(t, result.stdout, &envelope)
		if envelope.Success || envelope.Error == nil || envelope.Error.Code != "ASSIGN_ERROR" {
			t.Fatalf("wrong-reason assignment envelope: success=%t error=%+v", envelope.Success, envelope.Error)
		}
		record := fixture.readLedgerAssignment(t, beadID)
		if record.ReservationCompleted || record.ReservationState != "reserved" || len(record.ReservationIDs) == 0 ||
			len(record.ReservedPaths) == 0 || !strings.Contains(record.ReservationError, "reason mismatch") || record.DispatchAttempts != 0 {
			t.Fatalf("wrong-reason durable handles = %+v", record)
		}
		snapshot := mail.snapshot()
		if snapshot.ReserveCalls != 1 || snapshot.ReleaseCalls != 0 || len(snapshot.Active) != len(record.ReservationIDs) {
			t.Fatalf("wrong-reason Agent Mail state = %+v record=%+v", snapshot, record)
		}
		fixture.assertMarkerCounts(t, marker, map[int]int{0: 0, 1: 0})
	})

	for _, operation := range []string{"clear", "reassign"} {
		operation := operation
		t.Run(operation+"_refuses_false_release_count_while_ids_remain_active", func(t *testing.T) {
			fixture := newAtomicAssignmentCLIFixture(t)
			mail := newAtomicAssignmentMailStub(fixture.projectDir)
			defer mail.close()
			beadID, _, before := seedReservedWorkingAssignment(t, fixture, mail, fixture.panes[0], "Sticky release source internal/cli/sticky_release.go")
			mail.setRetainAfterRelease(true)
			beforeCalls := mail.snapshot()
			var result atomicAssignmentProcessResult
			if operation == "clear" {
				result = fixture.runNTM(t, mail.env(), "--json", "assign", fixture.session, "--repo="+fixture.projectDir, "--clear="+beadID, "--timeout=15s")
			} else {
				result = fixture.runNTM(t, mail.env(),
					"--json", "assign", fixture.session,
					"--repo="+fixture.projectDir,
					"--reassign="+beadID,
					"--to-pane="+fixture.panes[1].ID,
					"--prompt=sticky release must not dispatch",
					"--force",
					"--reserve-files=true",
					"--timeout=15s",
				)
			}
			if result.exitCode != 1 || len(bytes.TrimSpace(result.stderr)) != 0 || !bytes.Contains(result.stdout, []byte("remain active after")) {
				t.Fatalf("sticky %s exit=%d stdout=%s stderr=%s", operation, result.exitCode, result.stdout, result.stderr)
			}
			after := fixture.readLedgerAssignment(t, beadID)
			if after.Status != "working" || after.ClearState != "reservation_releasing" || !strings.Contains(after.ClearError, "remain active after") ||
				after.IdempotencyKey != before.IdempotencyKey || after.DispatchReceiptID != before.DispatchReceiptID ||
				!reflect.DeepEqual(after.ReservationIDs, before.ReservationIDs) || !reflect.DeepEqual(after.ReservedPaths, before.ReservedPaths) {
				t.Fatalf("sticky %s lost durable release barrier: before=%+v after=%+v", operation, before, after)
			}
			afterCalls := mail.snapshot()
			expectedReleasedIDs := make([]int, 0, 3*len(before.ReservationIDs))
			for range 3 {
				expectedReleasedIDs = append(expectedReleasedIDs, before.ReservationIDs...)
			}
			if afterCalls.ReleaseCalls != beforeCalls.ReleaseCalls+3 || afterCalls.ReserveCalls != beforeCalls.ReserveCalls ||
				!reflect.DeepEqual(afterCalls.ReleasedIDs, expectedReleasedIDs) || len(afterCalls.Active) != len(before.ReservationIDs) {
				t.Fatalf("sticky %s Agent Mail verification = %+v before=%+v", operation, afterCalls, before)
			}
			fixture.assertMarkerCounts(t, "sticky release must not dispatch", map[int]int{0: 0, 1: 0})
		})
	}

	t.Run("reassign_uses_exact_durable_paths_when_title_has_no_path", func(t *testing.T) {
		fixture := newAtomicAssignmentCLIFixture(t)
		mail := newAtomicAssignmentMailStub(fixture.projectDir)
		defer mail.close()
		beadID, _, before := seedReservedWorkingAssignment(t, fixture, mail, fixture.panes[0], "Durable path source internal/cli/durable_path.go")
		fixture.setLedgerAssignmentFields(t, beadID, map[string]any{"bead_title": "Durable path source without a file scope"})
		prompt := fmt.Sprintf("NTM_ATOMIC_DURABLE_PATH_TARGET_%d", time.Now().UnixNano())
		result := fixture.runNTM(t, mail.env(),
			"--json", "assign", fixture.session,
			"--repo="+fixture.projectDir,
			"--reassign="+beadID,
			"--to-pane="+fixture.panes[1].ID,
			"--prompt="+prompt,
			"--force",
			"--reserve-files=true",
			"--timeout=15s",
		)
		if result.exitCode != 0 || len(bytes.TrimSpace(result.stderr)) != 0 {
			t.Fatalf("durable-path reassign exit=%d stdout=%s stderr=%s", result.exitCode, result.stdout, result.stderr)
		}
		fixture.waitForMarkerCount(t, 1, prompt, 1)
		snapshot := mail.snapshot()
		if snapshot.ReserveCalls != 2 || snapshot.ReleaseCalls != 1 || len(snapshot.GrantedPaths) != 2 ||
			!reflect.DeepEqual(snapshot.GrantedPaths[1], before.ReservationRequested) {
			t.Fatalf("durable-path transfer requests=%+v before=%+v", snapshot, before)
		}
		after := fixture.readLedgerAssignment(t, beadID)
		if !reflect.DeepEqual(after.ReservationRequested, before.ReservationRequested) || !reflect.DeepEqual(after.ReservedPaths, before.ReservationRequested) {
			t.Fatalf("durable-path transfer drifted: before=%+v after=%+v", before, after)
		}
	})
}

func TestE2EAtomicAssignmentRecoveryCancellationBuiltProcess(t *testing.T) {
	CommonE2EPrerequisites(t)
	testutil.RequireTmuxThrottled(t)

	t.Run("retry_preflight_cancellation_is_recoverable", func(t *testing.T) {
		fixture := newAtomicAssignmentCLIFixture(t)
		prompt := fmt.Sprintf("NTM_ATOMIC_CANCEL_RETRY_%d", time.Now().UnixNano())
		originalPaneID := fixture.panes[0].ID
		beadID, pending := fixture.createClaimedPendingViaTmuxOutage(
			t, "retry-cancel", "Canceled retry remains recoverable", prompt,
		)
		fixture.startAgentPanes(t)
		if fixture.panes[0].ID != originalPaneID {
			t.Fatalf("retry cancellation restart changed pane ID: before=%s after=%s", originalPaneID, fixture.panes[0].ID)
		}
		fixture.primePaneForSafeDispatch(t, 0)
		time.Sleep(5500 * time.Millisecond)
		tmuxBinary, reached := fixture.armNthTMUXCommandBlock(t, "retry", "list-panes", 2)
		args := []string{
			"assign", fixture.session,
			"--repo=" + fixture.projectDir,
			"--retry=" + beadID,
			"--to-pane=" + fixture.panes[0].ID,
			"--reserve-files=false",
			"--timeout=30s",
			"--json",
		}
		canceled := fixture.runNTMInterrupted(t, map[string]string{"NTM_TMUX_BINARY": tmuxBinary}, func(pid int) {
			waitForAtomicAssignmentPath(t, reached, pid)
		}, args...)
		assertAtomicAssignmentSingleTimeoutJSON(t, canceled)
		time.Sleep(500 * time.Millisecond)
		fixture.assertMarkerCounts(t, prompt, map[int]int{0: 0, 1: 0})
		if after := fixture.readLedgerAssignment(t, beadID); !reflect.DeepEqual(after, pending) {
			t.Fatalf("canceled retry mutated durable generation: before=%+v after=%+v", pending, after)
		}

		recovered := fixture.runNTM(t, nil, args...)
		if recovered.exitCode != 0 || len(bytes.TrimSpace(recovered.stderr)) != 0 {
			t.Fatalf("retry recovery exit=%d stdout=%s stderr=%s", recovered.exitCode, recovered.stdout, recovered.stderr)
		}
		var envelope atomicAssignmentRetryEnvelope
		decodeAtomicAssignmentJSON(t, recovered.stdout, &envelope)
		if !envelope.Success || envelope.Data == nil || envelope.Data.Summary.RetriedCount != 1 ||
			envelope.Data.Summary.SkippedCount != 0 || len(envelope.Data.Retried) != 1 ||
			envelope.Data.Retried[0].BeadID != beadID || !envelope.Data.Retried[0].PromptSent {
			t.Fatalf("retry recovery envelope = %+v", envelope)
		}
		fixture.waitForMarkerCount(t, 0, prompt, 1)
		fixture.assertMarkerCounts(t, prompt, map[int]int{0: 1, 1: 0})
		after := fixture.readLedgerAssignment(t, beadID)
		if after.IdempotencyKey != pending.IdempotencyKey || after.ClaimActor != pending.ClaimActor ||
			after.DispatchState != "sent" || after.DispatchAttempts != pending.DispatchAttempts+1 ||
			after.DispatchReceiptID == "" || after.RetryCount != pending.RetryCount+1 {
			t.Fatalf("retry recovery duplicated or replaced durable identity: pending=%+v after=%+v", pending, after)
		}
	})

	t.Run("clear_release_cancellation_is_recoverable", func(t *testing.T) {
		fixture := newAtomicAssignmentCLIFixture(t)
		mail := newAtomicAssignmentMailStub(fixture.projectDir)
		defer mail.close()
		beadID, _, before := seedReservedWorkingAssignment(
			t, fixture, mail, fixture.panes[0], "Canceled clear internal/cli/cancel_clear.go",
		)
		paneBefore := map[int]string{0: fixture.capturePane(t, 0), 1: fixture.capturePane(t, 1)}
		started, aborted := mail.blockNextRelease(t)
		args := []string{
			"--json", "assign", fixture.session,
			"--repo=" + fixture.projectDir,
			"--clear=" + beadID,
			"--timeout=30s",
		}
		canceled := fixture.runNTMInterrupted(t, mail.env(), func(int) {
			waitForAtomicAssignmentSignal(t, started, "clear reservation release")
		}, args...)
		assertAtomicAssignmentSingleTimeoutJSON(t, canceled)
		waitForAtomicAssignmentSignal(t, aborted, "canceled clear HTTP request")
		mail.allowBlockedRelease(t)
		time.Sleep(500 * time.Millisecond)
		for pane, capture := range paneBefore {
			if afterCapture := fixture.capturePane(t, pane); afterCapture != capture {
				t.Fatalf("canceled clear mutated pane %d: before=%q after=%q", pane, capture, afterCapture)
			}
		}
		pending := fixture.readLedgerAssignment(t, beadID)
		if pending.Status != "working" || pending.ClearState != "reservation_releasing" ||
			pending.IdempotencyKey != before.IdempotencyKey || pending.DispatchReceiptID != before.DispatchReceiptID ||
			!reflect.DeepEqual(pending.ReservationIDs, before.ReservationIDs) {
			t.Fatalf("canceled clear lost durable release barrier: before=%+v pending=%+v", before, pending)
		}
		if snapshot := mail.snapshot(); snapshot.ReleaseCalls != 0 || snapshot.ReserveCalls != 1 ||
			len(snapshot.Active) != len(before.ReservationIDs) {
			t.Fatalf("canceled clear external effects = %+v", snapshot)
		}

		recovered := fixture.runNTM(t, mail.env(), args...)
		if recovered.exitCode != 0 || len(bytes.TrimSpace(recovered.stderr)) != 0 {
			t.Fatalf("clear recovery exit=%d stdout=%s stderr=%s", recovered.exitCode, recovered.stdout, recovered.stderr)
		}
		fixture.assertLedgerHasNoAssignment(t, beadID)
		fixture.assertBead(t, beadID, "open", "")
		snapshot := mail.snapshot()
		if snapshot.ReleaseCalls != 1 || snapshot.ReserveCalls != 1 ||
			!reflect.DeepEqual(snapshot.ReleasedIDs, before.ReservationIDs) || len(snapshot.Active) != 0 {
			t.Fatalf("clear recovery duplicated release effects = %+v", snapshot)
		}
	})

	t.Run("clear_pane_release_cancellation_is_recoverable", func(t *testing.T) {
		fixture := newAtomicAssignmentCLIFixture(t)
		mail := newAtomicAssignmentMailStub(fixture.projectDir)
		defer mail.close()
		pane := fixture.panes[0]
		beadID, _, before := seedReservedWorkingAssignment(
			t, fixture, mail, pane, "Canceled clear-pane internal/cli/cancel_clear_pane.go",
		)
		paneBefore := map[int]string{0: fixture.capturePane(t, 0), 1: fixture.capturePane(t, 1)}
		started, aborted := mail.blockNextRelease(t)
		args := []string{
			"--json", "assign", fixture.session,
			"--repo=" + fixture.projectDir,
			"--clear-pane=" + pane.ID,
			"--timeout=30s",
		}
		canceled := fixture.runNTMInterrupted(t, mail.env(), func(int) {
			waitForAtomicAssignmentSignal(t, started, "clear-pane reservation release")
		}, args...)
		assertAtomicAssignmentSingleTimeoutJSON(t, canceled)
		waitForAtomicAssignmentSignal(t, aborted, "canceled clear-pane HTTP request")
		mail.allowBlockedRelease(t)
		time.Sleep(500 * time.Millisecond)
		for index, capture := range paneBefore {
			if afterCapture := fixture.capturePane(t, index); afterCapture != capture {
				t.Fatalf("canceled clear-pane mutated pane %d: before=%q after=%q", index, capture, afterCapture)
			}
		}
		pending := fixture.readLedgerAssignment(t, beadID)
		if pending.Status != "working" || pending.ClearState != "reservation_releasing" ||
			pending.IdempotencyKey != before.IdempotencyKey || pending.DispatchReceiptID != before.DispatchReceiptID ||
			!reflect.DeepEqual(pending.ReservationIDs, before.ReservationIDs) {
			t.Fatalf("canceled clear-pane lost durable release barrier: before=%+v pending=%+v", before, pending)
		}
		if snapshot := mail.snapshot(); snapshot.ReleaseCalls != 0 || snapshot.ReserveCalls != 1 ||
			len(snapshot.Active) != len(before.ReservationIDs) {
			t.Fatalf("canceled clear-pane external effects = %+v", snapshot)
		}

		recovered := fixture.runNTM(t, mail.env(), args...)
		if recovered.exitCode != 0 || len(bytes.TrimSpace(recovered.stderr)) != 0 {
			t.Fatalf("clear-pane recovery exit=%d stdout=%s stderr=%s", recovered.exitCode, recovered.stdout, recovered.stderr)
		}
		var envelope struct {
			Success bool `json:"success"`
			Data    struct {
				Cleared []struct {
					BeadID  string `json:"bead_id"`
					Success bool   `json:"success"`
				} `json:"cleared"`
				Summary struct {
					ClearedCount int `json:"cleared_count"`
					FailedCount  int `json:"failed_count"`
				} `json:"summary"`
			} `json:"data"`
		}
		decodeAtomicAssignmentJSON(t, recovered.stdout, &envelope)
		if !envelope.Success || envelope.Data.Summary.ClearedCount != 1 || envelope.Data.Summary.FailedCount != 0 ||
			len(envelope.Data.Cleared) != 1 || envelope.Data.Cleared[0].BeadID != beadID || !envelope.Data.Cleared[0].Success {
			t.Fatalf("clear-pane recovery envelope = %+v", envelope)
		}
		fixture.assertLedgerHasNoAssignment(t, beadID)
		fixture.assertBead(t, beadID, "open", "")
		snapshot := mail.snapshot()
		if snapshot.ReleaseCalls != 1 || snapshot.ReserveCalls != 1 ||
			!reflect.DeepEqual(snapshot.ReleasedIDs, before.ReservationIDs) || len(snapshot.Active) != 0 {
			t.Fatalf("clear-pane recovery duplicated release effects = %+v", snapshot)
		}
	})

	t.Run("reassign_release_cancellation_is_recoverable", func(t *testing.T) {
		fixture := newAtomicAssignmentCLIFixture(t)
		mail := newAtomicAssignmentMailStub(fixture.projectDir)
		defer mail.close()
		source := fixture.panes[0]
		target := fixture.panes[1]
		beadID, _, before := seedReservedWorkingAssignment(
			t, fixture, mail, source, "Canceled reassign internal/cli/cancel_reassign.go",
		)
		fixture.primeEndpointForSafeDispatch(t, target)
		prompt := fmt.Sprintf("NTM_ATOMIC_CANCEL_REASSIGN_%d", time.Now().UnixNano())
		started, aborted := mail.blockNextRelease(t)
		args := []string{
			"--json", "assign", fixture.session,
			"--repo=" + fixture.projectDir,
			"--reassign=" + beadID,
			"--to-pane=" + target.ID,
			"--prompt=" + prompt,
			"--force",
			"--reserve-files=true",
			"--timeout=30s",
		}
		canceled := fixture.runNTMInterrupted(t, mail.env(), func(int) {
			waitForAtomicAssignmentSignal(t, started, "reassignment reservation release")
		}, args...)
		assertAtomicAssignmentSingleTimeoutJSON(t, canceled)
		waitForAtomicAssignmentSignal(t, aborted, "canceled reassignment HTTP request")
		mail.allowBlockedRelease(t)
		time.Sleep(500 * time.Millisecond)
		fixture.assertMarkerCounts(t, prompt, map[int]int{0: 0, 1: 0})
		pending := fixture.readLedgerAssignment(t, beadID)
		if pending.Status != "working" || pending.ClearState != "reservation_releasing" ||
			pending.IdempotencyKey != before.IdempotencyKey || pending.DispatchReceiptID != before.DispatchReceiptID ||
			!reflect.DeepEqual(pending.ReservationIDs, before.ReservationIDs) {
			t.Fatalf("canceled reassignment lost durable source: before=%+v pending=%+v", before, pending)
		}
		if snapshot := mail.snapshot(); snapshot.ReleaseCalls != 0 || snapshot.ReserveCalls != 1 ||
			len(snapshot.Active) != len(before.ReservationIDs) {
			t.Fatalf("canceled reassignment external effects = %+v", snapshot)
		}

		recovered := fixture.runNTM(t, mail.env(), args...)
		if recovered.exitCode != 0 || len(bytes.TrimSpace(recovered.stderr)) != 0 {
			t.Fatalf("reassignment recovery exit=%d stdout=%s stderr=%s", recovered.exitCode, recovered.stdout, recovered.stderr)
		}
		fixture.waitForEndpointMarkerCount(t, target, prompt, 1)
		fixture.assertMarkerCounts(t, prompt, map[int]int{0: 0, 1: 1})
		after := fixture.readLedgerAssignment(t, beadID)
		if after.Status != "assigned" || after.ClaimActor != before.ClaimActor || after.IdempotencyKey == before.IdempotencyKey ||
			after.OccupancyKey != target.ID || after.DispatchTarget != target.ID || after.DispatchState != "sent" ||
			after.DispatchReceiptID == "" || after.PromptSent != prompt {
			t.Fatalf("reassignment recovery durable state: before=%+v after=%+v", before, after)
		}
		snapshot := mail.snapshot()
		if snapshot.ReleaseCalls != 1 || snapshot.ReserveCalls != 2 ||
			!reflect.DeepEqual(snapshot.ReleasedIDs, before.ReservationIDs) || len(snapshot.Active) != len(after.ReservationIDs) {
			t.Fatalf("reassignment recovery duplicated external effects = %+v", snapshot)
		}
	})

	t.Run("reassign_to_type_discovery_cancellation_is_recoverable", func(t *testing.T) {
		fixture := newAtomicAssignmentCLIFixture(t)
		source := fixture.panes[0]
		target := fixture.panes[1]
		beadID := fixture.createBead(t, "Canceled reassign idle-agent discovery")
		sourcePrompt := fmt.Sprintf("NTM_ATOMIC_CANCEL_REASSIGN_TYPE_SOURCE_%d", time.Now().UnixNano())
		seed := fixture.runNTM(t, nil, atomicDirectArgsForSelector(fixture, source.ID, beadID, sourcePrompt, false)...)
		if seed.exitCode != 0 || len(bytes.TrimSpace(seed.stderr)) != 0 {
			t.Fatalf("seed to-type cancellation assignment exit=%d stdout=%s stderr=%s", seed.exitCode, seed.stdout, seed.stderr)
		}
		fixture.waitForEndpointMarkerCount(t, source, sourcePrompt, 1)
		fixture.driveAssignmentStatus(t, source, beadID, "• Working (4s · esc to interrupt)", "working")
		before := fixture.readLedgerAssignment(t, beadID)
		fixture.primeEndpointForSafeDispatch(t, target)
		prompt := fmt.Sprintf("NTM_ATOMIC_CANCEL_REASSIGN_TYPE_TARGET_%d", time.Now().UnixNano())
		tmuxBinary, reached := fixture.armNthTMUXCommandBlock(t, "reassign-to-type", "list-panes", 2)
		args := []string{
			"--json", "assign", fixture.session,
			"--repo=" + fixture.projectDir,
			"--reassign=" + beadID,
			"--to-type=claude",
			"--prompt=" + prompt,
			"--force",
			"--reserve-files=false",
			"--timeout=30s",
		}
		canceled := fixture.runNTMInterrupted(t, map[string]string{"NTM_TMUX_BINARY": tmuxBinary}, func(pid int) {
			waitForAtomicAssignmentPath(t, reached, pid)
		}, args...)
		assertAtomicAssignmentSingleTimeoutJSON(t, canceled)
		time.Sleep(500 * time.Millisecond)
		fixture.assertMarkerCounts(t, prompt, map[int]int{0: 0, 1: 0})
		if after := fixture.readLedgerAssignment(t, beadID); !reflect.DeepEqual(after, before) {
			t.Fatalf("canceled to-type discovery mutated durable assignment: before=%+v after=%+v", before, after)
		}
		fixture.assertBead(t, beadID, "in_progress", before.ClaimActor)

		recovered := fixture.runNTM(t, nil, args...)
		if recovered.exitCode != 0 || len(bytes.TrimSpace(recovered.stderr)) != 0 {
			t.Fatalf("to-type reassignment recovery exit=%d stdout=%s stderr=%s", recovered.exitCode, recovered.stdout, recovered.stderr)
		}
		var envelope atomicAssignmentReassignEnvelope
		decodeAtomicAssignmentJSON(t, recovered.stdout, &envelope)
		if !envelope.Success || envelope.Error != nil || envelope.Data == nil || envelope.Data.BeadID != beadID ||
			envelope.Data.Pane != target.Index || envelope.Data.AgentType != "claude" || !envelope.Data.PromptSent {
			t.Fatalf("to-type reassignment recovery envelope = %+v", envelope)
		}
		fixture.waitForEndpointMarkerCount(t, target, prompt, 1)
		fixture.assertMarkerCounts(t, prompt, map[int]int{0: 0, 1: 1})
		after := fixture.readLedgerAssignment(t, beadID)
		if after.Status != "assigned" || after.ClaimActor != before.ClaimActor || after.IdempotencyKey == before.IdempotencyKey ||
			after.OccupancyKey != target.ID || after.DispatchTarget != target.ID || after.DispatchState != "sent" ||
			after.DispatchReceiptID == "" || after.PromptSent != prompt {
			t.Fatalf("to-type reassignment recovery durable state: before=%+v after=%+v", before, after)
		}
	})

	t.Run("rebalance_release_cancellation_is_recoverable", func(t *testing.T) {
		fixture := newAtomicAssignmentCLIFixture(t)
		mail := newAtomicAssignmentMailStub(fixture.projectDir)
		defer mail.close()
		source := fixture.panes[0]
		target := fixture.panes[1]
		beadID, _, before := seedReservedWorkingAssignment(
			t, fixture, mail, source, "Canceled rebalance internal/cli/cancel_rebalance.go",
		)
		shadowBeadID := fixture.createBead(t, "Canceled rebalance shadow assignment")
		shadowPrompt := fmt.Sprintf("NTM_ATOMIC_CANCEL_REBALANCE_SHADOW_%d", time.Now().UnixNano())
		seedShadow := fixture.runNTM(t, nil, atomicDirectArgsForSelector(fixture, target.ID, shadowBeadID, shadowPrompt, false)...)
		if seedShadow.exitCode != 0 || len(bytes.TrimSpace(seedShadow.stderr)) != 0 {
			t.Fatalf("seed cancellation rebalance shadow exit=%d stdout=%s stderr=%s", seedShadow.exitCode, seedShadow.stdout, seedShadow.stderr)
		}
		fixture.waitForEndpointMarkerCount(t, target, shadowPrompt, 1)
		fixture.setLedgerAssignmentFields(t, shadowBeadID, map[string]any{
			"pane":            source.Index,
			"occupancy_key":   source.ID,
			"dispatch_target": source.ID,
			"agent_type":      before.AgentType,
			"agent_name":      before.AgentName,
			"status":          "assigned",
		})
		shadowBefore := fixture.readLedgerAssignment(t, shadowBeadID)
		fixture.primeEndpointForSafeDispatch(t, target)
		marker := "This work was transferred by ntm rebalance."
		started, aborted := mail.blockNextRelease(t)
		args := []string{"rebalance", fixture.session, "--format=json", "--apply"}
		canceled := fixture.runNTMInterrupted(t, mail.env(), func(int) {
			waitForAtomicAssignmentSignal(t, started, "rebalance reservation release")
		}, args...)
		assertAtomicAssignmentSingleTimeoutJSON(t, canceled)
		waitForAtomicAssignmentSignal(t, aborted, "canceled rebalance HTTP request")
		mail.allowBlockedRelease(t)
		time.Sleep(500 * time.Millisecond)
		fixture.assertMarkerCounts(t, marker, map[int]int{0: 0, 1: 0})
		pending := fixture.readLedgerAssignment(t, beadID)
		if pending.Status != "working" || pending.ClearState != "reservation_releasing" ||
			pending.IdempotencyKey != before.IdempotencyKey || pending.DispatchReceiptID != before.DispatchReceiptID ||
			!reflect.DeepEqual(pending.ReservationIDs, before.ReservationIDs) {
			t.Fatalf("canceled rebalance lost durable source: before=%+v pending=%+v", before, pending)
		}
		if shadowAfter := fixture.readLedgerAssignment(t, shadowBeadID); !reflect.DeepEqual(shadowAfter, shadowBefore) {
			t.Fatalf("canceled rebalance mutated shadow: before=%+v after=%+v", shadowBefore, shadowAfter)
		}
		if snapshot := mail.snapshot(); snapshot.ReleaseCalls != 0 || snapshot.ReserveCalls != 1 ||
			len(snapshot.Active) != len(before.ReservationIDs) {
			t.Fatalf("canceled rebalance external effects = %+v", snapshot)
		}

		recovered := fixture.runNTM(t, mail.env(), args...)
		if recovered.exitCode != 0 || len(bytes.TrimSpace(recovered.stderr)) != 0 {
			t.Fatalf("rebalance recovery exit=%d stdout=%s stderr=%s", recovered.exitCode, recovered.stdout, recovered.stderr)
		}
		fixture.waitForEndpointMarkerCount(t, target, marker, 1)
		fixture.assertMarkerCounts(t, marker, map[int]int{0: 0, 1: 1})
		after := fixture.readLedgerAssignment(t, beadID)
		if after.Status != "assigned" || after.ClaimActor != before.ClaimActor || after.IdempotencyKey == before.IdempotencyKey ||
			after.OccupancyKey != target.ID || after.DispatchTarget != target.ID || after.DispatchState != "sent" ||
			after.DispatchReceiptID == "" {
			t.Fatalf("rebalance recovery durable state: before=%+v after=%+v", before, after)
		}
		if shadowAfter := fixture.readLedgerAssignment(t, shadowBeadID); !reflect.DeepEqual(shadowAfter, shadowBefore) {
			t.Fatalf("rebalance recovery mutated shadow: before=%+v after=%+v", shadowBefore, shadowAfter)
		}
		snapshot := mail.snapshot()
		if snapshot.ReleaseCalls != 1 || snapshot.ReserveCalls != 2 ||
			!reflect.DeepEqual(snapshot.ReleasedIDs, before.ReservationIDs) || len(snapshot.Active) != len(after.ReservationIDs) {
			t.Fatalf("rebalance recovery duplicated external effects = %+v", snapshot)
		}
	})
}

func TestE2EAtomicBulkAssignmentCanonicalMultiWindow(t *testing.T) {
	CommonE2EPrerequisites(t)
	testutil.RequireTmuxThrottled(t)

	fixture := newAtomicAssignmentCLIFixture(t)
	panes := fixture.addSecondAgentWindow(t)
	templatePath := filepath.Join(fixture.projectDir, "atomic-multi-window-template.txt")
	templateBody := fmt.Sprintf("NTM_ATOMIC_MULTI_%d_{bead_id}", time.Now().UnixNano())
	if err := os.WriteFile(templatePath, []byte(templateBody), 0o600); err != nil {
		t.Fatalf("write multi-window prompt template: %v", err)
	}
	promptFor := func(beadID string) string {
		return strings.ReplaceAll(templateBody, "{bead_id}", beadID)
	}
	runBulk := func(tb *testing.T, allocation map[string]string, extra ...string) (atomicAssignmentProcessResult, atomicAssignmentBulkEnvelope) {
		tb.Helper()
		encoded, err := json.Marshal(allocation)
		if err != nil {
			tb.Fatalf("encode allocation: %v", err)
		}
		args := []string{
			"--robot-format=json",
			"--robot-bulk-assign=" + fixture.session,
			"--allocation=" + string(encoded),
			"--template=" + templatePath,
		}
		args = append(args, extra...)
		result := fixture.runNTM(tb, nil, args...)
		var envelope atomicAssignmentBulkEnvelope
		decodeAtomicAssignmentJSON(tb, result.stdout, &envelope)
		return result, envelope
	}
	assertOneSuccess := func(tb *testing.T, result atomicAssignmentProcessResult, envelope atomicAssignmentBulkEnvelope, beadID string, pane atomicAssignmentPane, agentType string) {
		tb.Helper()
		if result.exitCode != 0 || len(bytes.TrimSpace(result.stderr)) != 0 {
			tb.Fatalf("bulk assignment exit=%d stdout=%s stderr=%s", result.exitCode, result.stdout, result.stderr)
		}
		if !envelope.Success || envelope.Summary.Assigned != 1 || envelope.Summary.Failed != 0 || len(envelope.Assignments) != 1 {
			tb.Fatalf("bulk envelope = %+v", envelope)
		}
		assignment := envelope.Assignments[0]
		if assignment.Pane != pane.Target || assignment.PaneID != pane.ID || assignment.Bead != beadID ||
			assignment.AgentType != agentType || assignment.Status != "assigned" || !assignment.PromptSent ||
			!assignment.Claimed || assignment.ClaimActor == "" || assignment.IdempotencyKey == "" ||
			assignment.DispatchReceiptID == "" || assignment.Error != "" {
			tb.Fatalf("bulk assignment = %+v", assignment)
		}
		record := fixture.readLedgerAssignment(tb, beadID)
		assertAtomicAssignmentRecord(tb, record, beadID, promptFor(beadID), pane, agentType)
		if assignment.ClaimActor != record.ClaimActor || assignment.IdempotencyKey != record.IdempotencyKey || assignment.DispatchReceiptID != record.DispatchReceiptID {
			tb.Fatalf("bulk response does not match durable receipt: response=%+v ledger=%+v", assignment, record)
		}
		fixture.assertBead(tb, beadID, "in_progress", record.ClaimActor)
	}
	assertFailedBeforeSideEffects := func(tb *testing.T, result atomicAssignmentProcessResult, envelope atomicAssignmentBulkEnvelope, beadIDs []string, marker string) {
		tb.Helper()
		if result.exitCode == 0 || envelope.Success || envelope.Summary.Failed == 0 {
			tb.Fatalf("invalid allocation did not fail: exit=%d envelope=%+v", result.exitCode, envelope)
		}
		for _, beadID := range beadIDs {
			fixture.assertBead(tb, beadID, "open", "")
			fixture.assertLedgerHasNoAssignment(tb, beadID)
		}
		fixture.assertEndpointMarkerCounts(tb, marker, panes, nil)
	}

	t.Run("legacy_local_index_occupancy_blocks_duplicate_window_index", func(t *testing.T) {
		fixture := newAtomicAssignmentCLIFixture(t)
		panes := fixture.addSecondAgentWindow(t)
		legacyBeadID := fixture.createBead(t, "Legacy duplicate-index occupancy")
		candidateBeadID := fixture.createBead(t, "Candidate duplicate-index occupancy")
		source := panes["0.0"]
		target := panes["1.0"]
		legacyPrompt := fmt.Sprintf("NTM_ATOMIC_LEGACY_OCCUPANCY_%d", time.Now().UnixNano())
		candidatePrompt := fmt.Sprintf("NTM_ATOMIC_LEGACY_CANDIDATE_%d", time.Now().UnixNano())
		seed := fixture.runNTM(t, nil, atomicDirectArgsForSelector(fixture, source.ID, legacyBeadID, legacyPrompt, false)...)
		if seed.exitCode != 0 || len(bytes.TrimSpace(seed.stderr)) != 0 {
			t.Fatalf("seed legacy occupancy exit=%d stdout=%s stderr=%s", seed.exitCode, seed.stdout, seed.stderr)
		}
		fixture.waitForEndpointMarkerCount(t, source, legacyPrompt, 1)
		before := fixture.readLedgerAssignment(t, legacyBeadID)
		fixture.setLedgerAssignmentLegacyPaneTarget(t, legacyBeadID)

		result := fixture.runNTM(t, nil, atomicDirectArgsForSelector(fixture, target.ID, candidateBeadID, candidatePrompt, false)...)
		if result.exitCode != 1 || len(bytes.TrimSpace(result.stderr)) != 0 {
			t.Fatalf("legacy duplicate-index occupancy exit=%d stdout=%s stderr=%s", result.exitCode, result.stdout, result.stderr)
		}
		var envelope atomicAssignmentDirectEnvelope
		decodeAtomicAssignmentJSON(t, result.stdout, &envelope)
		if envelope.Success || envelope.Error == nil || envelope.Error.Code != "PANE_IDENTITY_MIGRATION_REQUIRED" ||
			!strings.Contains(envelope.Error.Message, "canonical pane identity migration") ||
			!strings.Contains(envelope.Error.Message, legacyBeadID) || envelope.Data == nil ||
			envelope.Data.Assignment.BeadID != candidateBeadID || envelope.Data.Assignment.Prompt != "" ||
			envelope.Data.Assignment.PromptSent || envelope.Data.Receipt != nil {
			t.Fatalf("legacy duplicate-index occupancy envelope = %+v", envelope)
		}
		fixture.assertBead(t, candidateBeadID, "open", "")
		fixture.assertLedgerHasNoAssignment(t, candidateBeadID)
		fixture.assertEndpointMarkerCounts(t, candidatePrompt, panes, nil)
		legacy := fixture.readLedgerAssignment(t, legacyBeadID)
		if legacy.OccupancyKey != "" || legacy.DispatchTarget != "" || legacy.Pane != target.Index || source.ID == target.ID {
			t.Fatalf("fixture did not preserve conservative legacy duplicate index: source=%+v target=%+v record=%+v", source, target, legacy)
		}
		if legacy.IdempotencyKey != before.IdempotencyKey || legacy.DispatchReceiptID != before.DispatchReceiptID || legacy.DispatchAttempts != before.DispatchAttempts {
			t.Fatalf("legacy occupancy refusal mutated owner receipt: before=%+v after=%+v", before, legacy)
		}
	})

	t.Run("clear_pane_targets_one_physical_duplicate_index", func(t *testing.T) {
		retainedBeadID := fixture.createBead(t, "Retained duplicate-index assignment")
		clearedBeadID := fixture.createBead(t, "Cleared duplicate-index assignment")
		retainedPane := panes["0.0"]
		clearedPane := panes["1.0"]
		retainedPrompt := fmt.Sprintf("NTM_ATOMIC_CLEAR_RETAIN_%d", time.Now().UnixNano())
		clearedPrompt := fmt.Sprintf("NTM_ATOMIC_CLEAR_TARGET_%d", time.Now().UnixNano())
		for _, assignment := range []struct {
			pane   atomicAssignmentPane
			beadID string
			prompt string
		}{
			{pane: retainedPane, beadID: retainedBeadID, prompt: retainedPrompt},
			{pane: clearedPane, beadID: clearedBeadID, prompt: clearedPrompt},
		} {
			result := fixture.runNTM(t, nil, atomicDirectArgsForSelector(fixture, assignment.pane.Target, assignment.beadID, assignment.prompt, false)...)
			if result.exitCode != 0 || len(bytes.TrimSpace(result.stderr)) != 0 {
				t.Fatalf("seed %s assignment exit=%d stdout=%s stderr=%s", assignment.pane.Target, result.exitCode, result.stdout, result.stderr)
			}
			fixture.waitForEndpointMarkerCount(t, assignment.pane, assignment.prompt, 1)
		}
		retainedBefore := fixture.readLedgerAssignment(t, retainedBeadID)
		clearedBefore := fixture.readLedgerAssignment(t, clearedBeadID)

		result := fixture.runNTM(t, nil,
			"--json", "assign", fixture.session,
			"--repo="+fixture.projectDir,
			"--clear-pane="+clearedPane.ID,
			"--timeout=15s",
		)
		if result.exitCode != 0 || len(bytes.TrimSpace(result.stderr)) != 0 {
			t.Fatalf("canonical clear-pane exit=%d stdout=%s stderr=%s", result.exitCode, result.stdout, result.stderr)
		}
		var envelope struct {
			Success bool `json:"success"`
			Data    struct {
				Cleared []struct {
					BeadID  string `json:"bead_id"`
					Success bool   `json:"success"`
				} `json:"cleared"`
				Summary struct {
					ClearedCount int `json:"cleared_count"`
					FailedCount  int `json:"failed_count"`
				} `json:"summary"`
			} `json:"data"`
		}
		decodeAtomicAssignmentJSON(t, result.stdout, &envelope)
		if !envelope.Success || envelope.Data.Summary.ClearedCount != 1 || envelope.Data.Summary.FailedCount != 0 ||
			len(envelope.Data.Cleared) != 1 || envelope.Data.Cleared[0].BeadID != clearedBeadID || !envelope.Data.Cleared[0].Success {
			t.Fatalf("canonical clear-pane envelope = %+v", envelope)
		}
		fixture.assertLedgerHasNoAssignment(t, clearedBeadID)
		assertAtomicAssignmentReceiptUnchanged(t, retainedBefore, fixture.readLedgerAssignment(t, retainedBeadID))
		fixture.assertEndpointMarkerCounts(t, retainedPrompt, panes, map[string]int{retainedPane.Target: 1})
		fixture.assertEndpointMarkerCounts(t, clearedPrompt, panes, map[string]int{clearedPane.Target: 1})
		if clearedBefore.OccupancyKey == retainedBefore.OccupancyKey || clearedBefore.Pane != retainedBefore.Pane {
			t.Fatalf("fixture did not exercise duplicate local pane indexes: retained=%+v cleared=%+v", retainedBefore, clearedBefore)
		}
	})

	t.Run("reassign_pane_id_targets_one_physical_duplicate_index", func(t *testing.T) {
		fixture := newAtomicAssignmentCLIFixture(t)
		panes := fixture.addSecondAgentWindow(t)
		beadID := fixture.createBead(t, "Canonical physical reassignment")
		source := panes["0.0"]
		target := panes["1.0"]
		originalPrompt := fmt.Sprintf("NTM_ATOMIC_REASSIGN_SOURCE_%d", time.Now().UnixNano())
		reassignedPrompt := fmt.Sprintf("NTM_ATOMIC_REASSIGN_TARGET_%d", time.Now().UnixNano())
		seed := fixture.runNTM(t, nil, atomicDirectArgsForSelector(fixture, source.ID, beadID, originalPrompt, false)...)
		if seed.exitCode != 0 || len(bytes.TrimSpace(seed.stderr)) != 0 {
			t.Fatalf("seed reassignment exit=%d stdout=%s stderr=%s", seed.exitCode, seed.stdout, seed.stderr)
		}
		fixture.waitForEndpointMarkerCount(t, source, originalPrompt, 1)
		fixture.driveAssignmentStatus(t, source, beadID, "• Working (4s · esc to interrupt)", "working")
		before := fixture.readLedgerAssignment(t, beadID)
		fixture.primeEndpointForSafeDispatch(t, target)

		result := fixture.runNTM(t, nil,
			"--json", "assign", fixture.session, "--repo="+fixture.projectDir,
			"--reassign="+beadID, "--to-pane="+target.ID,
			"--prompt="+reassignedPrompt, "--force", "--reserve-files=false", "--timeout=60s",
		)
		if result.exitCode != 0 || len(bytes.TrimSpace(result.stderr)) != 0 {
			t.Fatalf("canonical reassign exit=%d stdout=%s stderr=%s", result.exitCode, result.stdout, result.stderr)
		}
		var envelope struct {
			Success bool `json:"success"`
			Data    struct {
				BeadID       string `json:"bead_id"`
				Pane         int    `json:"pane"`
				PreviousPane int    `json:"previous_pane"`
				PromptSent   bool   `json:"prompt_sent"`
			} `json:"data"`
		}
		decodeAtomicAssignmentJSON(t, result.stdout, &envelope)
		if !envelope.Success || envelope.Data.BeadID != beadID || envelope.Data.Pane != target.Index ||
			envelope.Data.PreviousPane != source.Index || !envelope.Data.PromptSent {
			t.Fatalf("canonical reassign envelope = %+v", envelope)
		}
		fixture.waitForEndpointMarkerCount(t, target, reassignedPrompt, 1)
		fixture.assertEndpointMarkerCounts(t, reassignedPrompt, panes, map[string]int{target.Target: 1})
		reassigned := fixture.readLedgerAssignment(t, beadID)
		if reassigned.OccupancyKey != target.ID || reassigned.DispatchTarget != target.ID || reassigned.Pane != target.Index || reassigned.Status != "assigned" {
			t.Fatalf("canonical reassign durable target = %+v", reassigned)
		}
		if reassigned.ClaimActor != before.ClaimActor || reassigned.IdempotencyKey == before.IdempotencyKey ||
			reassigned.ClaimState != "claimed" || reassigned.ClaimStatus != "in_progress" || reassigned.ClaimAttempts != 1 ||
			reassigned.ReservationRequired || reassigned.ReservationCompleted || len(reassigned.ReservationIDs) != 0 || len(reassigned.ReservedPaths) != 0 {
			t.Fatalf("canonical reassign did not preserve claim actor and disabled reservation contract: before=%+v after=%+v", before, reassigned)
		}
		fixture.assertBead(t, beadID, "in_progress", before.ClaimActor)
		if source.Index != target.Index || source.ID == target.ID {
			t.Fatalf("fixture did not exercise duplicate local indexes: source=%+v target=%+v", source, target)
		}
	})

	t.Run("retry_window_pane_targets_one_physical_duplicate_index", func(t *testing.T) {
		fixture := newAtomicAssignmentCLIFixture(t)
		panes := fixture.addSecondAgentWindow(t)
		beadID := fixture.createBead(t, "Canonical physical retry")
		source := panes["0.1"]
		target := panes["1.1"]
		originalPrompt := fmt.Sprintf("NTM_ATOMIC_RETRY_SOURCE_%d", time.Now().UnixNano())
		retryMarker := fmt.Sprintf("NTM_ATOMIC_RETRY_TARGET_%d", time.Now().UnixNano())
		seed := fixture.runNTM(t, nil, atomicDirectArgsForSelector(fixture, source.ID, beadID, originalPrompt, false)...)
		if seed.exitCode != 0 || len(bytes.TrimSpace(seed.stderr)) != 0 {
			t.Fatalf("seed retry exit=%d stdout=%s stderr=%s", seed.exitCode, seed.stdout, seed.stderr)
		}
		fixture.waitForEndpointMarkerCount(t, source, originalPrompt, 1)
		before := fixture.readLedgerAssignment(t, beadID)
		fixture.driveAssignmentStatusWithCompletionEvent(t, source, beadID, "ERROR: fatal assignment failure", "failed")
		fixture.primeEndpointForSafeDispatch(t, target)
		time.Sleep(5500 * time.Millisecond)
		failed := fixture.readLedgerAssignment(t, beadID)
		if failed.PendingCompletionEventID == "" {
			t.Fatalf("canonical retry failure has no durable completion event: %+v", failed)
		}
		t.Setenv("HOME", fixture.homeDir)
		retryStore, err := assignment.LoadStoreStrict(fixture.session)
		if err != nil {
			t.Fatalf("load canonical retry store for completion acknowledgement: %v", err)
		}
		consumerToken := failed.CompletionConsumerToken
		if consumerToken == "" {
			consumerToken = "canonical-retry-e2e-consumer"
		}
		if _, acquired, claimErr := retryStore.ClaimPendingCompletionEvent(
			t.Context(), beadID, failed.PendingCompletionEventID, consumerToken, time.Minute,
		); claimErr != nil || !acquired {
			t.Fatalf("claim canonical retry completion event acquired=%v error=%v", acquired, claimErr)
		}
		if acknowledged, ackErr := retryStore.AcknowledgeCompletionEvent(
			t.Context(), beadID, failed.PendingCompletionEventID, consumerToken,
		); ackErr != nil || !acknowledged {
			t.Fatalf("acknowledge canonical retry completion event applied=%v error=%v", acknowledged, ackErr)
		}
		templatePath := filepath.Join(fixture.projectDir, "atomic-canonical-retry-template.txt")
		if err := os.WriteFile(templatePath, []byte(retryMarker+"::{BEAD_ID}"), 0o600); err != nil {
			t.Fatalf("write canonical retry template: %v", err)
		}

		result := fixture.runNTM(t, nil,
			"assign", fixture.session, "--repo="+fixture.projectDir,
			"--retry="+beadID, "--to-pane="+target.Target,
			"--template=custom", "--template-file="+templatePath,
			"--reserve-files=false", "--timeout=60s", "--json",
		)
		if result.exitCode != 0 || len(bytes.TrimSpace(result.stderr)) != 0 {
			t.Fatalf("canonical retry exit=%d stdout=%s stderr=%s", result.exitCode, result.stdout, result.stderr)
		}
		var envelope atomicAssignmentRetryEnvelope
		decodeAtomicAssignmentJSON(t, result.stdout, &envelope)
		if !envelope.Success || envelope.Data == nil || envelope.Data.Summary.RetriedCount != 1 ||
			envelope.Data.Summary.SkippedCount != 0 || len(envelope.Data.Retried) != 1 || envelope.Data.Retried[0].BeadID != beadID {
			t.Fatalf("canonical retry envelope = %+v", envelope)
		}
		prompt := retryMarker + "::" + beadID
		fixture.waitForEndpointMarkerCount(t, target, prompt, 1)
		fixture.assertEndpointMarkerCounts(t, prompt, panes, map[string]int{target.Target: 1})
		retried := fixture.readLedgerAssignment(t, beadID)
		if retried.OccupancyKey != target.ID || retried.DispatchTarget != target.ID || retried.Pane != target.Index ||
			retried.Status != "assigned" || retried.IdempotencyKey == before.IdempotencyKey || retried.DispatchReceiptID == "" {
			t.Fatalf("canonical retry durable target = %+v before=%+v", retried, before)
		}
		if source.Index != target.Index || source.ID == target.ID {
			t.Fatalf("fixture did not exercise duplicate local indexes: source=%+v target=%+v", source, target)
		}
	})

	t.Run("window_pane_and_id_aliases_dispatch_once", func(t *testing.T) {
		beadID := fixture.createBead(t, "Canonical alias deduplication")
		target := panes["1.1"]
		fixture.primeEndpointForSafeDispatch(t, target)
		result, envelope := runBulk(t, map[string]string{target.Target: beadID, target.ID: beadID})
		assertOneSuccess(t, result, envelope, beadID, target, "claude")
		fixture.waitForEndpointMarkerCount(t, target, promptFor(beadID), 1)
		fixture.assertEndpointMarkerCounts(t, promptFor(beadID), panes, map[string]int{target.Target: 1})
	})

	t.Run("pane_id_selects_exact_duplicate_local_index", func(t *testing.T) {
		beadID := fixture.createBead(t, "Canonical pane ID assignment")
		target := panes["0.1"]
		fixture.primeEndpointForSafeDispatch(t, target)
		result, envelope := runBulk(t, map[string]string{target.ID: beadID})
		assertOneSuccess(t, result, envelope, beadID, target, "claude")
		fixture.waitForEndpointMarkerCount(t, target, promptFor(beadID), 1)
		fixture.assertEndpointMarkerCounts(t, promptFor(beadID), panes, map[string]int{target.Target: 1})
	})

	t.Run("canonical_skip_excludes_only_selected_physical_pane", func(t *testing.T) {
		beadID := fixture.createBead(t, "Canonical skipped assignment")
		target := panes["1.0"]
		result, envelope := runBulk(t, map[string]string{target.Target: beadID}, "--skip="+target.ID)
		assertFailedBeforeSideEffects(t, result, envelope, []string{beadID}, promptFor(beadID))
		if len(envelope.Assignments) != 1 || !strings.Contains(envelope.Assignments[0].Error, "not found") {
			t.Fatalf("skip failure did not identify the excluded target: %+v", envelope.Assignments)
		}
	})

	t.Run("bare_window_selector_is_ambiguous_before_claim", func(t *testing.T) {
		beadID := fixture.createBead(t, "Ambiguous window assignment")
		result, envelope := runBulk(t, map[string]string{"1": beadID})
		assertFailedBeforeSideEffects(t, result, envelope, []string{beadID}, promptFor(beadID))
		if len(envelope.Assignments) != 1 || !strings.Contains(envelope.Assignments[0].Error, "matched 2 panes") {
			t.Fatalf("ambiguous selector failure = %+v", envelope.Assignments)
		}
	})

	t.Run("conflicting_aliases_fail_before_either_claim", func(t *testing.T) {
		firstBead := fixture.createBead(t, "Conflicting alias one")
		secondBead := fixture.createBead(t, "Conflicting alias two")
		target := panes["0.0"]
		result, envelope := runBulk(t, map[string]string{target.Target: firstBead, target.ID: secondBead})
		assertFailedBeforeSideEffects(t, result, envelope, []string{firstBead, secondBead}, promptFor(firstBead))
		fixture.assertEndpointMarkerCounts(t, promptFor(secondBead), panes, nil)
		if len(envelope.Assignments) != 2 {
			t.Fatalf("conflicting alias results = %+v", envelope.Assignments)
		}
		for _, assignment := range envelope.Assignments {
			if !strings.Contains(assignment.Error, "same physical pane") {
				t.Fatalf("conflicting alias failure = %+v", assignment)
			}
		}
	})

	t.Run("direct_window_pane_replays_through_pane_id_alias", func(t *testing.T) {
		beadID := fixture.createBead(t, "Canonical direct assignment")
		target := panes["1.0"]
		prompt := fmt.Sprintf("NTM_ATOMIC_DIRECT_MULTI_%d", time.Now().UnixNano())
		// Earlier bulk sends left prompt text on this pane; re-render the
		// composer glyph so the bd-dp9oy delivery gate accepts the send.
		fixture.primeEndpointForSafeDispatch(t, target)
		firstResult := fixture.runNTM(t, nil, atomicDirectArgsForSelector(fixture, target.Target, beadID, prompt, false)...)
		if firstResult.exitCode != 0 || len(bytes.TrimSpace(firstResult.stderr)) != 0 {
			t.Fatalf("direct W.P assignment exit=%d stdout=%s stderr=%s", firstResult.exitCode, firstResult.stdout, firstResult.stderr)
		}
		var first atomicAssignmentDirectEnvelope
		decodeAtomicAssignmentJSON(t, firstResult.stdout, &first)
		assertDirectAssignmentEnvelope(t, first, fixture.session, beadID, prompt, target)
		if first.Data == nil || first.Data.Receipt == nil || first.Data.Assignment.PaneTarget != target.Target || first.Data.Assignment.PaneID != target.ID ||
			first.Data.Receipt.Pane.Target != target.Target || first.Data.Receipt.Pane.WindowIndex != target.Window {
			t.Fatalf("direct canonical pane identity = %+v", first.Data)
		}
		fixture.waitForEndpointMarkerCount(t, target, prompt, 1)
		fixture.assertEndpointMarkerCounts(t, prompt, panes, map[string]int{target.Target: 1})
		firstRecord := fixture.readLedgerAssignment(t, beadID)
		assertAtomicAssignmentRecord(t, firstRecord, beadID, prompt, target, "codex")

		replayResult := fixture.runNTM(t, nil, atomicDirectArgsForSelector(fixture, target.ID, beadID, prompt, false)...)
		if replayResult.exitCode != 0 || len(bytes.TrimSpace(replayResult.stderr)) != 0 {
			t.Fatalf("direct %%N replay exit=%d stdout=%s stderr=%s", replayResult.exitCode, replayResult.stdout, replayResult.stderr)
		}
		var replay atomicAssignmentDirectEnvelope
		decodeAtomicAssignmentJSON(t, replayResult.stdout, &replay)
		assertDirectAssignmentEnvelope(t, replay, fixture.session, beadID, prompt, target)
		if replay.Data == nil || replay.Data.Receipt == nil || first.Data.Receipt.Transport.DeliveryID != replay.Data.Receipt.Transport.DeliveryID ||
			first.Data.Receipt.Timestamp != replay.Data.Receipt.Timestamp {
			t.Fatalf("direct alias replay receipt changed: first=%+v replay=%+v", first.Data, replay.Data)
		}
		fixture.assertEndpointMarkerCounts(t, prompt, panes, map[string]int{target.Target: 1})
		replayedRecord := fixture.readLedgerAssignment(t, beadID)
		if replayedRecord.IdempotencyKey != firstRecord.IdempotencyKey || replayedRecord.DispatchAttempts != 1 || replayedRecord.DispatchReceiptID != firstRecord.DispatchReceiptID {
			t.Fatalf("direct alias replay mutated durable state: first=%+v replay=%+v", firstRecord, replayedRecord)
		}
	})

	t.Run("direct_bare_window_selector_fails_before_claim", func(t *testing.T) {
		beadID := fixture.createBead(t, "Ambiguous direct window assignment")
		prompt := fmt.Sprintf("NTM_ATOMIC_DIRECT_AMBIGUOUS_%d", time.Now().UnixNano())
		result := fixture.runNTM(t, nil, atomicDirectArgsForSelector(fixture, "1", beadID, prompt, false)...)
		if result.exitCode == 0 || len(bytes.TrimSpace(result.stderr)) != 0 {
			t.Fatalf("ambiguous direct selector exit=%d stdout=%s stderr=%s", result.exitCode, result.stdout, result.stderr)
		}
		var envelope atomicAssignmentDirectEnvelope
		decodeAtomicAssignmentJSON(t, result.stdout, &envelope)
		if envelope.Success || envelope.Error == nil || envelope.Error.Code != "PANE_AMBIGUOUS" || !strings.Contains(envelope.Error.Message, "matched 2 panes") {
			t.Fatalf("ambiguous direct selector envelope = %+v", envelope)
		}
		fixture.assertBead(t, beadID, "open", "")
		fixture.assertLedgerHasNoAssignment(t, beadID)
		fixture.assertEndpointMarkerCounts(t, prompt, panes, nil)
	})
}

func TestE2EAtomicAssignmentPromptSafety(t *testing.T) {
	CommonE2EPrerequisites(t)
	testutil.RequireTmuxThrottled(t)

	fixture := newAtomicAssignmentCLIFixture(t)
	redactBead := fixture.createBead(t, "Atomic prompt redaction")
	blockBead := fixture.createBead(t, "Atomic prompt blocking")
	secret := "sk-proj-NTM_E2E_SECRET_1234567890123456789012345678901234567890"
	rawPrompt := "Use OPENAI_API_KEY=" + secret + " for this assignment"

	redactArgs := append(atomicDirectArgs(fixture, redactBead, rawPrompt, false), "--redact=redact")
	redacted := fixture.runNTM(t, nil, redactArgs...)
	if redacted.exitCode != 0 {
		t.Fatalf("redacted assignment exit=%d stdout=%s stderr=%s", redacted.exitCode, redacted.stdout, redacted.stderr)
	}
	fixture.assertSecretAbsent(t, secret, redacted.stdout, redacted.stderr)
	var envelope atomicAssignmentDirectEnvelope
	decodeAtomicAssignmentJSON(t, redacted.stdout, &envelope)
	if !envelope.Success || envelope.Data == nil || envelope.Data.Assignment.Prompt == "" || strings.Contains(envelope.Data.Assignment.Prompt, secret) {
		t.Fatalf("redacted assignment envelope = %+v", envelope)
	}
	durablePrompt := envelope.Data.Assignment.Prompt
	fixture.waitForMarkerCount(t, 0, "Use OPENAI_API_KEY=", 1)
	fixture.assertMarkerCounts(t, secret, map[int]int{0: 0, 1: 0})
	record := fixture.readLedgerAssignment(t, redactBead)
	if record.PromptSent != durablePrompt || strings.Contains(record.PromptSent, secret) || record.PendingPrompt != "" {
		t.Fatalf("durable redacted prompt = %+v", record)
	}
	rawHash := sha256.Sum256([]byte(rawPrompt))
	durableHash := sha256.Sum256([]byte(durablePrompt))
	if record.IntentSHA256 != hex.EncodeToString(rawHash[:]) || record.PromptSHA256 != hex.EncodeToString(durableHash[:]) {
		t.Fatalf("prompt hashes = intent:%q durable:%q", record.IntentSHA256, record.PromptSHA256)
	}
	fixture.assertAssignmentArtifactsExclude(t, secret)

	blockedPrompt := "Use password=NTM_E2E_BLOCKED_SECRET_987654321 for this assignment"
	blockedArgs := append(atomicDirectArgsForPane(fixture, 1, blockBead, blockedPrompt, false), "--redact=block")
	blocked := fixture.runNTM(t, nil, blockedArgs...)
	if blocked.exitCode == 0 {
		t.Fatalf("blocked assignment exited 0: stdout=%s stderr=%s", blocked.stdout, blocked.stderr)
	}
	fixture.assertSecretAbsent(t, "NTM_E2E_BLOCKED_SECRET_987654321", blocked.stdout, blocked.stderr)
	var blockedEnvelope atomicAssignmentDirectEnvelope
	decodeAtomicAssignmentJSON(t, blocked.stdout, &blockedEnvelope)
	if blockedEnvelope.Success || blockedEnvelope.Error == nil || blockedEnvelope.Error.Code != "ASSIGN_ERROR" ||
		!strings.Contains(strings.ToLower(blockedEnvelope.Error.Message), "block") {
		t.Fatalf("blocked assignment envelope = %+v", blockedEnvelope)
	}
	fixture.assertBead(t, blockBead, "open", "")
	fixture.assertLedgerHasNoAssignment(t, blockBead)
	fixture.assertMarkerCounts(t, blockedPrompt, map[int]int{0: 0, 1: 0})
	fixture.assertAssignmentArtifactsExclude(t, "NTM_E2E_BLOCKED_SECRET_987654321")
}

func TestE2EAtomicAssignmentHumanOutputRedaction(t *testing.T) {
	CommonE2EPrerequisites(t)
	testutil.RequireTmuxThrottled(t)

	t.Run("direct_prints_only_durable_redacted_prompt", func(t *testing.T) {
		fixture := newAtomicAssignmentCLIFixture(t)
		beadID := fixture.createBead(t, "Human direct redaction")
		secret := "sk-proj-NTM_HUMAN_DIRECT_1234567890123456789012345678901234567890"
		rawPrompt := "Use OPENAI_API_KEY=" + secret + " for the direct assignment"
		templatePath := filepath.Join(fixture.projectDir, "atomic-human-direct-redaction-template.txt")
		if err := os.WriteFile(templatePath, []byte(rawPrompt), 0o600); err != nil {
			t.Fatalf("write human direct redaction template: %v", err)
		}

		result := fixture.runNTM(t, nil,
			"assign", fixture.session,
			"--repo="+fixture.projectDir,
			"--pane="+fixture.panes[0].ID,
			"--beads="+beadID,
			"--template=custom",
			"--template-file="+templatePath,
			"--force",
			"--ignore-deps",
			"--reserve-files=false",
			"--redact=redact",
			"--timeout=60s",
		)
		if result.exitCode != 0 {
			t.Fatalf("human direct redaction exit=%d stdout=%s stderr=%s", result.exitCode, result.stdout, result.stderr)
		}
		fixture.assertSecretAbsent(t, secret, result.stdout, result.stderr)
		if !bytes.Contains(result.stdout, []byte("[REDACTED:")) {
			t.Fatalf("human direct output omitted redacted prompt: %s", result.stdout)
		}
		fixture.waitForMarkerCount(t, 0, "[REDACTED:", 1)
		fixture.assertMarkerCounts(t, secret, map[int]int{0: 0, 1: 0})
		record := fixture.readLedgerAssignment(t, beadID)
		if strings.Contains(record.PromptSent, secret) || !strings.Contains(record.PromptSent, "[REDACTED:") {
			t.Fatalf("human direct durable prompt = %q", record.PromptSent)
		}
		fixture.assertAssignmentArtifactsExclude(t, secret)
	})

	t.Run("reassign_prints_only_durable_redacted_prompt", func(t *testing.T) {
		fixture := newAtomicAssignmentCLIFixture(t)
		beadID := fixture.createBead(t, "Human reassign redaction")
		sourcePrompt := fmt.Sprintf("NTM_ATOMIC_HUMAN_REASSIGN_SOURCE_%d", time.Now().UnixNano())
		seed := fixture.runNTM(t, nil, atomicDirectArgs(fixture, beadID, sourcePrompt, false)...)
		if seed.exitCode != 0 || len(bytes.TrimSpace(seed.stderr)) != 0 {
			t.Fatalf("seed human reassign redaction exit=%d stdout=%s stderr=%s", seed.exitCode, seed.stdout, seed.stderr)
		}
		fixture.waitForMarkerCount(t, 0, sourcePrompt, 1)
		fixture.setLedgerAssignmentFields(t, beadID, map[string]any{
			"status":     "working",
			"started_at": time.Now().UTC(),
		})
		secret := "sk-proj-NTM_HUMAN_REASSIGN_1234567890123456789012345678901234567890"
		rawPrompt := "Use OPENAI_API_KEY=" + secret + " for reassignment"
		templatePath := filepath.Join(fixture.projectDir, "atomic-human-reassign-redaction-template.txt")
		if err := os.WriteFile(templatePath, []byte(rawPrompt), 0o600); err != nil {
			t.Fatalf("write human reassign redaction template: %v", err)
		}

		result := fixture.runNTM(t, nil,
			"assign", fixture.session,
			"--repo="+fixture.projectDir,
			"--reassign="+beadID,
			"--to-pane="+fixture.panes[1].ID,
			"--template=custom",
			"--template-file="+templatePath,
			"--force",
			"--reserve-files=false",
			"--redact=redact",
			"--timeout=60s",
		)
		if result.exitCode != 0 {
			t.Fatalf("human reassign redaction exit=%d stdout=%s stderr=%s", result.exitCode, result.stdout, result.stderr)
		}
		fixture.assertSecretAbsent(t, secret, result.stdout, result.stderr)
		if !bytes.Contains(result.stdout, []byte("[REDACTED:")) {
			t.Fatalf("human reassign output omitted redacted prompt: %s", result.stdout)
		}
		fixture.waitForMarkerCount(t, 1, "[REDACTED:", 1)
		fixture.assertMarkerCounts(t, secret, map[int]int{0: 0, 1: 0})
		record := fixture.readLedgerAssignment(t, beadID)
		if strings.Contains(record.PromptSent, secret) || !strings.Contains(record.PromptSent, "[REDACTED:") {
			t.Fatalf("human reassign durable prompt = %q", record.PromptSent)
		}
		fixture.assertAssignmentArtifactsExclude(t, secret)
	})

	t.Run("fresh_human_title_is_redacted_before_first_output", func(t *testing.T) {
		fixture := newAtomicAssignmentCLIFixture(t)
		secret := "sk-proj-NTM_HUMAN_TITLE_FIRST_1234567890123456789012345678901234567890"
		beadID := fixture.createBead(t, "Fresh human title "+secret)
		result := fixture.runNTM(t, nil,
			"assign", fixture.session,
			"--repo="+fixture.projectDir,
			"--pane="+fixture.panes[0].ID,
			"--beads="+beadID,
			"--template=impl",
			"--force",
			"--ignore-deps",
			"--reserve-files=false",
			"--redact=redact",
			"--timeout=60s",
		)
		if result.exitCode != 0 || len(bytes.TrimSpace(result.stderr)) != 0 {
			t.Fatalf("fresh human title assignment exit=%d stdout=%s stderr=%s", result.exitCode, result.stdout, result.stderr)
		}
		fixture.assertSecretAbsent(t, secret, result.stdout, result.stderr)
		if !bytes.Contains(result.stdout, []byte("[REDACTED:")) {
			t.Fatalf("fresh human title output omitted redaction marker: %s", result.stdout)
		}
		fixture.waitForMarkerCount(t, 0, "[REDACTED:", 1)
		fixture.assertMarkerCounts(t, secret, map[int]int{0: 0, 1: 0})
		recordJSON, err := json.Marshal(fixture.readLedgerAssignment(t, beadID))
		if err != nil {
			t.Fatalf("encode fresh human title record: %v", err)
		}
		fixture.assertSecretAbsent(t, secret, recordJSON)
	})

	t.Run("active_conflict_does_not_project_legacy_raw_title", func(t *testing.T) {
		fixture := newAtomicAssignmentCLIFixture(t)
		beadID := fixture.createBead(t, "Direct conflict title projection")
		prompt := fmt.Sprintf("NTM_ATOMIC_CONFLICT_TITLE_%d", time.Now().UnixNano())
		seed := fixture.runNTM(t, nil, atomicDirectArgs(fixture, beadID, prompt, false)...)
		if seed.exitCode != 0 || len(bytes.TrimSpace(seed.stderr)) != 0 {
			t.Fatalf("seed direct conflict title exit=%d stdout=%s stderr=%s", seed.exitCode, seed.stdout, seed.stderr)
		}
		fixture.waitForMarkerCount(t, 0, prompt, 1)
		secret := "sk-proj-NTM_LEGACY_CONFLICT_TITLE_1234567890123456789012345678901234567890"
		fixture.setLedgerAssignmentFields(t, beadID, map[string]any{"bead_title": "Legacy raw title " + secret})
		conflict := fixture.runNTM(t, nil, atomicDirectArgs(fixture, beadID, prompt+"_DIFFERENT", false)...)
		if conflict.exitCode != 1 || len(bytes.TrimSpace(conflict.stderr)) != 0 {
			t.Fatalf("direct conflict title exit=%d stdout=%s stderr=%s", conflict.exitCode, conflict.stdout, conflict.stderr)
		}
		fixture.assertSecretAbsent(t, secret, conflict.stdout, conflict.stderr)
		var envelope atomicAssignmentDirectEnvelope
		decodeAtomicAssignmentJSON(t, conflict.stdout, &envelope)
		if envelope.Success || envelope.Error == nil || envelope.Error.Code != "CLAIM_CONFLICT" || envelope.Data == nil ||
			envelope.Data.Assignment.BeadTitle != "" || envelope.Data.Assignment.Prompt != "" {
			t.Fatalf("direct conflict projected nondurable data: %+v", envelope)
		}
	})

	t.Run("title_path_secret_is_absent_from_json_text_ledger_pane_and_mail", func(t *testing.T) {
		fixture := newAtomicAssignmentCLIFixture(t)
		mail := newAtomicAssignmentMailStub(fixture.projectDir)
		defer mail.close()
		secret := "sk-proj-NTM_TITLE_PATH_1234567890123456789012345678901234567890"
		title := "Title path secret internal/cli/safe_title.go internal/private/" + secret + ".txt"
		beadID := fixture.createBead(t, title)
		args := []string{
			"assign", fixture.session,
			"--repo=" + fixture.projectDir,
			"--pane=" + fixture.panes[0].ID,
			"--beads=" + beadID,
			"--template=impl",
			"--force",
			"--ignore-deps",
			"--reserve-files=true",
			"--redact=redact",
			"--timeout=60s",
		}
		jsonResult := fixture.runNTM(t, mail.env(), append(append([]string(nil), args...), "--json")...)
		if jsonResult.exitCode != 0 || len(bytes.TrimSpace(jsonResult.stderr)) != 0 {
			t.Fatalf("title-secret JSON assignment exit=%d stdout=%s stderr=%s", jsonResult.exitCode, jsonResult.stdout, jsonResult.stderr)
		}
		fixture.assertSecretAbsent(t, secret, jsonResult.stdout, jsonResult.stderr)
		if !bytes.Contains(jsonResult.stdout, []byte("[REDACTED:")) {
			t.Fatalf("title-secret JSON output omitted redaction marker: %s", jsonResult.stdout)
		}
		var envelope atomicAssignmentDirectEnvelope
		decodeAtomicAssignmentJSON(t, jsonResult.stdout, &envelope)
		if !envelope.Success || envelope.Error != nil || envelope.Data == nil ||
			strings.Contains(envelope.Data.Assignment.BeadTitle, secret) || !strings.Contains(envelope.Data.Assignment.BeadTitle, "[REDACTED:") {
			t.Fatalf("title-secret JSON envelope = %+v", envelope)
		}
		fixture.waitForMarkerCount(t, 0, "[REDACTED:", 1)
		fixture.assertMarkerCounts(t, secret, map[int]int{0: 0, 1: 0})
		record := fixture.readLedgerAssignment(t, beadID)
		recordJSON, err := json.Marshal(record)
		if err != nil {
			t.Fatalf("encode title-secret durable record: %v", err)
		}
		fixture.assertSecretAbsent(t, secret, recordJSON)
		if !strings.Contains(record.BeadTitle, "[REDACTED:") || !strings.Contains(record.PromptSent, "[REDACTED:") ||
			len(record.ReservationRequested) == 0 || len(record.ReservedPaths) == 0 {
			t.Fatalf("title-secret durable redaction/reservation = %+v", record)
		}
		for _, paths := range [][]string{record.ReservationInputPaths, record.ReservationRequested, record.ReservedPaths} {
			for _, path := range paths {
				if strings.Contains(path, secret) {
					t.Fatalf("title secret leaked in durable reservation path %q", path)
				}
			}
		}
		mailAfterJSON := mail.snapshot()
		fixture.assertSecretAbsent(t, secret, mailAfterJSON.RawRequests)

		textResult := fixture.runNTM(t, mail.env(), args...)
		if textResult.exitCode != 0 || len(bytes.TrimSpace(textResult.stderr)) != 0 {
			t.Fatalf("title-secret human replay exit=%d stdout=%s stderr=%s", textResult.exitCode, textResult.stdout, textResult.stderr)
		}
		fixture.assertSecretAbsent(t, secret, textResult.stdout, textResult.stderr)
		if !bytes.Contains(textResult.stdout, []byte("[REDACTED:")) {
			t.Fatalf("title-secret human output omitted redaction marker: %s", textResult.stdout)
		}
		fixture.assertMarkerCounts(t, "[REDACTED:", map[int]int{0: 1, 1: 0})
		textRecordJSON, err := json.Marshal(fixture.readLedgerAssignment(t, beadID))
		if err != nil {
			t.Fatalf("encode title-secret human record: %v", err)
		}
		fixture.assertSecretAbsent(t, secret, textRecordJSON)
		mailAfterText := mail.snapshot()
		fixture.assertSecretAbsent(t, secret, mailAfterText.RawRequests)
		fixture.assertPrivateStateExcludes(t, secret)
	})
}

func TestE2EAtomicAssignmentEnhancedTitleRedaction(t *testing.T) {
	CommonE2EPrerequisites(t)
	testutil.RequireTmuxThrottled(t)

	fixture := newAtomicAssignmentCLIFixture(t)
	secret := "sk-proj-NTM_ENHANCED_TITLE_1234567890123456789012345678901234567890"
	beadID := fixture.createBead(t, "Enhanced assignment title "+secret)
	fixture.primePaneForSafeDispatch(t, 0)
	fixture.waitForEndpointIdle(t, fixture.panes[0], 6*time.Second, 60*time.Second)

	// On a saturated E2E host the process can be descheduled after collecting a
	// complete observation but before validating its five-second freshness
	// window. Retry only that explicit pre-actuation error, and prove the failed
	// attempt left no durable assignment before refreshing the idle observation.
	runRedacted := func(args ...string) atomicAssignmentProcessResult {
		t.Helper()
		const maxStaleObservationAttempts = 3
		var result atomicAssignmentProcessResult
		for attempt := 1; attempt <= maxStaleObservationAttempts; attempt++ {
			result = fixture.runNTM(t, nil, args...)
			fixture.assertSecretAbsent(t, secret, result.stdout, result.stderr)
			if !atomicAssignmentStaleObservationFailure(result) || attempt == maxStaleObservationAttempts {
				return result
			}
			fixture.assertLedgerHasNoAssignment(t, beadID)
			fixture.assertBead(t, beadID, "open", "")
			fixture.assertMarkerCounts(t, secret, map[int]int{0: 0, 1: 0})
			fixture.primePaneForSafeDispatch(t, 0)
			fixture.waitForEndpointIdle(t, fixture.panes[0], 6*time.Second, 60*time.Second)
		}
		return result
	}

	plan := runRedacted(
		"--json", "assign", fixture.session,
		"--repo="+fixture.projectDir,
		"--beads="+beadID,
		"--limit=1",
		"--dry-run",
		"--reserve-files=false",
		"--redact=redact",
	)
	if plan.exitCode != 0 || len(bytes.TrimSpace(plan.stderr)) != 0 {
		t.Fatalf("enhanced redacted plan exit=%d stdout=%s stderr=%s", plan.exitCode, plan.stdout, plan.stderr)
	}
	fixture.assertSecretAbsent(t, secret, plan.stdout, plan.stderr)
	if !bytes.Contains(plan.stdout, []byte("[REDACTED:")) {
		t.Fatalf("enhanced plan omitted redaction marker: %s", plan.stdout)
	}

	executed := runRedacted(
		"assign", fixture.session,
		"--repo="+fixture.projectDir,
		"--beads="+beadID,
		"--limit=1",
		"--auto",
		"--reserve-files=false",
		"--redact=redact",
		"--timeout=60s",
	)
	if executed.exitCode != 0 || len(bytes.TrimSpace(executed.stderr)) != 0 {
		t.Fatalf("enhanced redacted execution exit=%d stdout=%s stderr=%s", executed.exitCode, executed.stdout, executed.stderr)
	}
	fixture.assertSecretAbsent(t, secret, executed.stdout, executed.stderr)
	if !bytes.Contains(executed.stdout, []byte("[REDACTED:")) {
		t.Fatalf("enhanced execution omitted redaction marker: %s", executed.stdout)
	}
	fixture.waitForMarkerCount(t, 0, "[REDACTED:", 1)
	fixture.assertMarkerCounts(t, secret, map[int]int{0: 0, 1: 0})
	recordJSON, err := json.Marshal(fixture.readLedgerAssignment(t, beadID))
	if err != nil {
		t.Fatalf("encode enhanced assignment record: %v", err)
	}
	fixture.assertSecretAbsent(t, secret, recordJSON)
}

func TestE2EAtomicRobotBulkSecretSafety(t *testing.T) {
	CommonE2EPrerequisites(t)
	testutil.RequireTmuxThrottled(t)

	t.Run("secret_title_is_redacted_before_ledger_and_dispatch", func(t *testing.T) {
		fixture := newAtomicAssignmentCLIFixture(t)
		secret := "sk-proj-NTM_ROBOT_TITLE_1234567890123456789012345678901234567890"
		beadID := fixture.createBead(t, "Robot title secret "+secret)
		templatePath := filepath.Join(fixture.projectDir, "atomic-robot-title-secret-template.txt")
		if err := os.WriteFile(templatePath, []byte("ROBOT_TITLE_SECRET::{bead_id}::{bead_title}"), 0o600); err != nil {
			t.Fatalf("write robot title-secret template: %v", err)
		}
		allocation, err := json.Marshal(map[string]string{"0": beadID})
		if err != nil {
			t.Fatalf("encode robot title-secret allocation: %v", err)
		}
		fixture.primePaneForSafeDispatch(t, 0)
		time.Sleep(5500 * time.Millisecond)
		result := fixture.runNTM(t, nil,
			"--robot-format=json",
			"--robot-bulk-assign="+fixture.session,
			"--allocation="+string(allocation),
			"--template="+templatePath,
		)
		if result.exitCode != 0 || len(bytes.TrimSpace(result.stderr)) != 0 {
			t.Fatalf("robot title-secret assignment exit=%d stdout=%s stderr=%s", result.exitCode, result.stdout, result.stderr)
		}
		fixture.assertSecretAbsent(t, secret, result.stdout, result.stderr)
		if !bytes.Contains(result.stdout, []byte("[REDACTED:")) {
			t.Fatalf("robot title-secret output omitted redaction marker: %s", result.stdout)
		}
		var envelope atomicAssignmentBulkEnvelope
		decodeAtomicAssignmentJSON(t, result.stdout, &envelope)
		if !envelope.Success || envelope.Summary.Assigned != 1 || envelope.Summary.Failed != 0 || len(envelope.Assignments) != 1 ||
			envelope.Assignments[0].Bead != beadID || !envelope.Assignments[0].PromptSent {
			t.Fatalf("robot title-secret envelope = %+v", envelope)
		}
		fixture.waitForMarkerCount(t, 0, "ROBOT_TITLE_SECRET", 1)
		fixture.assertMarkerCounts(t, secret, map[int]int{0: 0, 1: 0})
		record := fixture.readLedgerAssignment(t, beadID)
		recordJSON, err := json.Marshal(record)
		if err != nil {
			t.Fatalf("encode robot title-secret record: %v", err)
		}
		fixture.assertSecretAbsent(t, secret, recordJSON)
		if !strings.Contains(record.BeadTitle, "[REDACTED:") || !strings.Contains(record.PromptSent, "[REDACTED:") {
			t.Fatalf("robot title-secret durable record = %+v", record)
		}
		fixture.assertPrivateStateExcludes(t, secret)
	})

	t.Run("secret_reservation_path_blocks_before_claim_reserve_and_dispatch", func(t *testing.T) {
		fixture := newAtomicAssignmentCLIFixture(t)
		mail := newAtomicAssignmentMailStub(fixture.projectDir)
		defer mail.close()
		beadID := fixture.createBead(t, "Robot explicit path secret")
		secret := "sk-proj-NTM_ROBOT_PATH_1234567890123456789012345678901234567890"
		secretPath := "internal/private/" + secret + ".txt"
		marker := fmt.Sprintf("NTM_ATOMIC_ROBOT_SECRET_PATH_%d", time.Now().UnixNano())
		templatePath := filepath.Join(fixture.projectDir, "atomic-robot-path-secret-template.txt")
		if err := os.WriteFile(templatePath, []byte(marker+"::{bead_id}"), 0o600); err != nil {
			t.Fatalf("write robot path-secret template: %v", err)
		}
		allocation, err := json.Marshal(map[string]string{"1": beadID})
		if err != nil {
			t.Fatalf("encode robot path-secret allocation: %v", err)
		}
		result := fixture.runNTM(t, mail.env(),
			"--robot-format=json",
			"--robot-bulk-assign="+fixture.session,
			"--allocation="+string(allocation),
			"--template="+templatePath,
			"--require-reservation",
			"--reservation-paths="+secretPath,
		)
		if result.exitCode != 1 || len(bytes.TrimSpace(result.stderr)) != 0 {
			t.Fatalf("robot path-secret assignment exit=%d stdout=%s stderr=%s", result.exitCode, result.stdout, result.stderr)
		}
		fixture.assertSecretAbsent(t, secret, result.stdout, result.stderr)
		var envelope atomicAssignmentBulkEnvelope
		decodeAtomicAssignmentJSON(t, result.stdout, &envelope)
		if envelope.Success || envelope.Summary.Assigned != 0 || envelope.Summary.Failed != 1 || len(envelope.Assignments) != 1 ||
			envelope.Assignments[0].Bead != beadID || envelope.Assignments[0].PromptSent || envelope.Assignments[0].Claimed ||
			!strings.Contains(strings.ToLower(envelope.Assignments[0].Error), "redaction") {
			t.Fatalf("robot path-secret envelope = %+v", envelope)
		}
		snapshot := mail.snapshot()
		if snapshot.ReserveCalls != 0 || snapshot.ReleaseCalls != 0 || snapshot.SendCalls != 0 {
			t.Fatalf("robot path-secret preflight touched Agent Mail: %+v", snapshot)
		}
		fixture.assertSecretAbsent(t, secret, snapshot.RawRequests)
		fixture.assertBead(t, beadID, "open", "")
		fixture.assertLedgerHasNoAssignment(t, beadID)
		fixture.assertMarkerCounts(t, marker, map[int]int{0: 0, 1: 0})
	})
}

func newAtomicAssignmentCLIFixture(t *testing.T) *atomicAssignmentCLIFixture {
	t.Helper()

	ntmPath, err := ensureE2ENTMBin()
	if err != nil {
		t.Fatalf("resolve E2E ntm binary: %v", err)
	}
	tmuxPath, err := exec.LookPath(tmux.BinaryPath())
	if err != nil {
		t.Fatalf("resolve tmux: %v", err)
	}
	brPath, err := exec.LookPath("br")
	if err != nil {
		t.Skipf("br is required for atomic assignment E2E: %v", err)
	}

	root := t.TempDir()
	// macOS t.TempDir() lives under /var, a symlink to /private/var. The
	// production coordinator resolves the project path before calling Agent
	// Mail ensure_project, so the fixture must hold the resolved form or the
	// mail stubs' human_key/project_key comparisons reject with HTTP 400.
	if resolved, err := filepath.EvalSymlinks(root); err == nil {
		root = resolved
	}
	tmuxRoot := testutil.ShortTmuxTempDir(t)
	fixture := &atomicAssignmentCLIFixture{
		ntmPath:    ntmPath,
		tmuxPath:   tmuxPath,
		brPath:     brPath,
		session:    fmt.Sprintf("ntm-e2e-atomic-%d-%d", os.Getpid(), time.Now().UnixNano()),
		root:       root,
		projectDir: filepath.Join(root, "project"),
		homeDir:    filepath.Join(root, "home"),
		panes:      make(map[int]atomicAssignmentPane),
	}
	for _, dir := range []string{
		fixture.projectDir,
		fixture.homeDir,
		filepath.Join(root, "config"),
		filepath.Join(root, "data"),
		tmuxRoot,
	} {
		if err := os.MkdirAll(dir, 0o700); err != nil {
			t.Fatalf("create fixture directory %s: %v", dir, err)
		}
	}
	fixture.env = atomicAssignmentIsolatedEnv(map[string]string{
		"HOME":              fixture.homeDir,
		"XDG_CONFIG_HOME":   filepath.Join(root, "config"),
		"XDG_DATA_HOME":     filepath.Join(root, "data"),
		"TMUX_TMPDIR":       tmuxRoot,
		"AGENT_MAIL_URL":    "http://127.0.0.1:1/mcp/",
		"AGENT_MAIL_TOKEN":  "",
		"HTTP_PROXY":        "",
		"HTTPS_PROXY":       "",
		"ALL_PROXY":         "",
		"NO_PROXY":          "127.0.0.1,localhost",
		"NO_COLOR":          "1",
		"TERM":              "xterm-256color",
		"NTM_OUTPUT_FORMAT": "",
		"NTM_ROBOT_FORMAT":  "",
		"NTM_TEST_ASSIGNMENT_SAVE_FAIL_AFTER_BACKUP": "",
		"NTM_TEST_COMPLETION_ACK_FAIL_ONCE":          "",
		"NTM_TEST_COMPLETION_LEASE_DURATION":         "",
		"NTM_TEST_COMPLETION_HANDLER_DELAY":          "",
		"TOON_DEFAULT_FORMAT":                        "",
	})

	fixture.mustBR(t, "init", "--prefix=ate2e", "--json")

	fixture.tmuxConfig = filepath.Join(root, "tmux.conf")
	config := strings.Join([]string{
		"set -g base-index 0",
		"setw -g pane-base-index 0",
		"set -g renumber-windows off",
		"set -g status off",
		"setw -g allow-rename off",
		"setw -g automatic-rename off",
		"",
	}, "\n")
	if err := os.WriteFile(fixture.tmuxConfig, []byte(config), 0o600); err != nil {
		t.Fatalf("write tmux config: %v", err)
	}
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), defaultTmuxSetupTimeout)
		defer cancel()
		err := fixture.runTMUX(ctx, "kill-server")
		if ctxErr := ctx.Err(); ctxErr != nil {
			t.Errorf("atomic assignment private tmux cleanup context ended: %v (command error: %v)", ctxErr, err)
			return
		}
		if err != nil && !isBenignTmuxCleanupError([]byte(err.Error())) {
			t.Errorf("clean up atomic assignment private tmux server: %v", err)
		}
	})
	fixture.startAgentPanes(t)
	return fixture
}

func atomicDirectArgs(f *atomicAssignmentCLIFixture, beadID, prompt string, requireReservation bool) []string {
	return atomicDirectArgsForPane(f, 0, beadID, prompt, requireReservation)
}

func atomicDirectArgsForPane(f *atomicAssignmentCLIFixture, pane int, beadID, prompt string, requireReservation bool) []string {
	return atomicDirectArgsForSelector(f, fmt.Sprintf("%d", pane), beadID, prompt, requireReservation)
}

func atomicDirectArgsForSelector(f *atomicAssignmentCLIFixture, selector, beadID, prompt string, requireReservation bool) []string {
	args := []string{
		"assign", f.session,
		"--repo=" + f.projectDir,
		"--pane=" + selector,
		"--beads=" + beadID,
		"--prompt=" + prompt,
		"--force",
		"--ignore-deps",
		"--timeout=60s",
	}
	if !requireReservation {
		args = append(args, "--reserve-files=false")
	}
	args = append(args, "--json")
	return args
}

func seedReservedWorkingAssignment(t *testing.T, fixture *atomicAssignmentCLIFixture, mail *atomicAssignmentMailStub, pane atomicAssignmentPane, title string) (string, string, *atomicAssignmentRecord) {
	t.Helper()
	beadID := fixture.createBead(t, title)
	marker := fmt.Sprintf("NTM_ATOMIC_RESERVED_SOURCE_%d", time.Now().UnixNano())
	templatePath := filepath.Join(fixture.projectDir, "atomic-reserved-source-"+beadID+".txt")
	if err := os.WriteFile(templatePath, []byte(marker+"::{TITLE}"), 0o600); err != nil {
		t.Fatalf("write reserved source template: %v", err)
	}
	prompt := marker + "::" + title
	result := fixture.runNTM(t, mail.env(),
		"assign", fixture.session,
		"--repo="+fixture.projectDir,
		"--pane="+pane.ID,
		"--beads="+beadID,
		"--template=custom",
		"--template-file="+templatePath,
		"--force",
		"--ignore-deps",
		"--reserve-files=true",
		"--timeout=30s",
		"--json",
	)
	if result.exitCode != 0 || len(bytes.TrimSpace(result.stderr)) != 0 {
		t.Fatalf("seed reserved assignment exit=%d stdout=%s stderr=%s", result.exitCode, result.stdout, result.stderr)
	}
	fixture.waitForEndpointMarkerCount(t, pane, prompt, 1)
	fixture.driveAssignmentStatus(t, pane, beadID, "• Working (4s · esc to interrupt)", "working")
	record := fixture.readLedgerAssignment(t, beadID)
	if record.Status != "working" || !record.ReservationRequired || !record.ReservationCompleted || record.ReservationState != "reserved" ||
		len(record.ReservationIDs) == 0 || len(record.ReservedPaths) == 0 || len(record.ReservationRequested) == 0 || record.ReservationExpiresAt == nil {
		t.Fatalf("seed reserved assignment metadata = %+v", record)
	}
	return beadID, prompt, record
}

func atomicDirectTemplateArgs(f *atomicAssignmentCLIFixture, pane int, beadID, templatePath string) []string {
	return []string{
		"assign", f.session,
		"--repo=" + f.projectDir,
		fmt.Sprintf("--pane=%d", pane),
		"--beads=" + beadID,
		"--template=custom",
		"--template-file=" + templatePath,
		"--reserve-files=false",
		"--force",
		"--ignore-deps",
		"--timeout=60s",
		"--json",
	}
}

func (f *atomicAssignmentCLIFixture) armClaimThenKillTmux(t *testing.T, name string) (path string, fired string) {
	t.Helper()
	wrapperDir := filepath.Join(f.root, "claim-outage-bin-"+name)
	if err := os.MkdirAll(wrapperDir, 0o700); err != nil {
		t.Fatalf("create br outage wrapper directory: %v", err)
	}
	sentinel := filepath.Join(wrapperDir, "stop-private-tmux-after-claim")
	fired = sentinel + ".fired"
	if err := os.WriteFile(sentinel, []byte("armed\n"), 0o600); err != nil {
		t.Fatalf("arm br outage wrapper: %v", err)
	}
	wrapper := strings.Join([]string{
		"#!/bin/sh",
		"real_br=" + tmux.ShellQuote(f.brPath),
		"real_tmux=" + tmux.ShellQuote(f.tmuxPath),
		"sentinel=" + tmux.ShellQuote(sentinel),
		"fired=" + tmux.ShellQuote(fired),
		`"$real_br" "$@"`,
		"status=$?",
		"sync_command=0",
		"flush_only=0",
		`for arg in "$@"; do`,
		`  if [ "$arg" = "sync" ]; then sync_command=1; fi`,
		`  if [ "$arg" = "--flush-only" ]; then flush_only=1; fi`,
		"done",
		`guarded_sync=$((sync_command * flush_only))`,
		`if [ "$status" -eq 0 ] && [ "$guarded_sync" -eq 1 ] && [ -e "$sentinel" ] && [ ! -e "$fired" ]; then`,
		`  mv "$sentinel" "$fired"`,
		`  "$real_tmux" kill-server >/dev/null 2>&1 || true`,
		"fi",
		`exit "$status"`,
		"",
	}, "\n")
	if err := os.WriteFile(filepath.Join(wrapperDir, "br"), []byte(wrapper), 0o700); err != nil {
		t.Fatalf("write br outage wrapper: %v", err)
	}
	return wrapperDir + string(os.PathListSeparator) + atomicAssignmentEnvValue(f.env, "PATH"), fired
}

func (f *atomicAssignmentCLIFixture) armAssignmentEligibilityMutation(t *testing.T, name string, mutationArgs []string) (path, fired, failed string) {
	t.Helper()
	wrapperDir := filepath.Join(f.root, "assignment-eligibility-bin-"+strings.ReplaceAll(name, " ", "-"))
	if err := os.MkdirAll(wrapperDir, 0o700); err != nil {
		t.Fatalf("create assignment eligibility wrapper directory: %v", err)
	}
	fired = filepath.Join(wrapperDir, "assignment-eligibility-mutation-fired")
	failed = fired + ".failed"
	mutationCommand := []string{tmux.ShellQuote(f.brPath)}
	for _, arg := range mutationArgs {
		mutationCommand = append(mutationCommand, tmux.ShellQuote(arg))
	}
	wrapper := strings.Join([]string{
		"#!/bin/sh",
		"real_br=" + tmux.ShellQuote(f.brPath),
		"fired=" + tmux.ShellQuote(fired),
		"failed=" + tmux.ShellQuote(failed),
		`"$real_br" "$@"`,
		"status=$?",
		"info_command=0",
		"no_auto_import=0",
		"no_auto_flush=0",
		`for arg in "$@"; do`,
		`  if [ "$arg" = "info" ]; then info_command=1; fi`,
		`  if [ "$arg" = "--no-auto-import" ]; then no_auto_import=1; fi`,
		`  if [ "$arg" = "--no-auto-flush" ]; then no_auto_flush=1; fi`,
		"done",
		`assignment_claim_info=$((info_command * no_auto_import * no_auto_flush))`,
		`if [ "$status" -eq 0 ] && [ "$assignment_claim_info" -eq 1 ] && [ ! -e "$fired" ]; then`,
		`  printf 'fired\n' > "$fired"`,
		"  if ! " + strings.Join(mutationCommand, " ") + ` >/dev/null 2>&1; then`,
		`    printf 'failed\n' > "$failed"`,
		"    exit 97",
		"  fi",
		"fi",
		`exit "$status"`,
		"",
	}, "\n")
	if err := os.WriteFile(filepath.Join(wrapperDir, "br"), []byte(wrapper), 0o700); err != nil {
		t.Fatalf("write assignment eligibility wrapper: %v", err)
	}
	return wrapperDir + string(os.PathListSeparator) + atomicAssignmentEnvValue(f.env, "PATH"), fired, failed
}

func (f *atomicAssignmentCLIFixture) armGuardedClaimInfoThenKillTmux(t *testing.T, name string) (path string, fired string) {
	t.Helper()
	wrapperDir := filepath.Join(f.root, "claim-info-outage-bin-"+name)
	if err := os.MkdirAll(wrapperDir, 0o700); err != nil {
		t.Fatalf("create guarded-info outage wrapper directory: %v", err)
	}
	sentinel := filepath.Join(wrapperDir, "stop-private-tmux-after-guarded-info")
	fired = sentinel + ".fired"
	if err := os.WriteFile(sentinel, []byte("armed\n"), 0o600); err != nil {
		t.Fatalf("arm guarded-info outage wrapper: %v", err)
	}
	wrapper := strings.Join([]string{
		"#!/bin/sh",
		"real_br=" + tmux.ShellQuote(f.brPath),
		"real_tmux=" + tmux.ShellQuote(f.tmuxPath),
		"sentinel=" + tmux.ShellQuote(sentinel),
		"fired=" + tmux.ShellQuote(fired),
		`"$real_br" "$@"`,
		"status=$?",
		"info_command=0",
		"no_auto_flush=0",
		`for arg in "$@"; do`,
		`  if [ "$arg" = "info" ]; then info_command=1; fi`,
		`  if [ "$arg" = "--no-auto-flush" ]; then no_auto_flush=1; fi`,
		"done",
		`guarded_info=$((info_command * no_auto_flush))`,
		`if [ "$status" -eq 0 ] && [ "$guarded_info" -eq 1 ] && [ -e "$sentinel" ] && [ ! -e "$fired" ]; then`,
		`  mv "$sentinel" "$fired"`,
		`  "$real_tmux" kill-server >/dev/null 2>&1 || true`,
		"fi",
		`exit "$status"`,
		"",
	}, "\n")
	if err := os.WriteFile(filepath.Join(wrapperDir, "br"), []byte(wrapper), 0o700); err != nil {
		t.Fatalf("write guarded-info outage wrapper: %v", err)
	}
	return wrapperDir + string(os.PathListSeparator) + atomicAssignmentEnvValue(f.env, "PATH"), fired
}

func (f *atomicAssignmentCLIFixture) createClaimedPendingViaTmuxOutage(t *testing.T, name, title, prompt string) (string, *atomicAssignmentRecord) {
	t.Helper()
	beadID := f.createBead(t, title)
	path, fired := f.armClaimThenKillTmux(t, name)
	failed := f.runNTM(t, map[string]string{"PATH": path}, atomicDirectArgs(f, beadID, prompt, false)...)
	if failed.exitCode == 0 || len(bytes.TrimSpace(failed.stderr)) != 0 {
		t.Fatalf("post-claim outage exit=%d stdout=%s stderr=%s", failed.exitCode, failed.stdout, failed.stderr)
	}
	var envelope atomicAssignmentDirectEnvelope
	decodeAtomicAssignmentJSON(t, failed.stdout, &envelope)
	if envelope.Success || envelope.Error == nil || envelope.Error.Code != "SEND_ERROR" {
		if envelope.Error == nil {
			t.Fatalf("post-claim outage envelope has no error: %+v", envelope)
		}
		t.Fatalf("post-claim outage error code=%q message=%q", envelope.Error.Code, envelope.Error.Message)
	}
	pending := f.readLedgerAssignment(t, beadID)
	if pending.Status != "claimed" || pending.ClaimState != "claimed" || pending.ClaimStatus != "in_progress" ||
		pending.ClaimAttempts != 1 || pending.DispatchState != "pending" || pending.DispatchAttempts != 1 ||
		pending.PendingPrompt != prompt || pending.PromptSent != "" || pending.LastDispatchError == "" ||
		pending.DispatchedAt != nil || pending.DispatchReceiptID != "" {
		t.Fatalf("post-claim outage was not durably recoverable: %+v", pending)
	}
	f.assertBead(t, beadID, "in_progress", pending.ClaimActor)
	if _, err := os.Stat(fired); err != nil {
		t.Fatalf("guarded claim finalization outage wrapper did not fire: %v", err)
	}
	return beadID, pending
}

func atomicBulkArgs(f *atomicAssignmentCLIFixture, beadID, templatePath string) []string {
	allocation, _ := json.Marshal(map[string]string{"1": beadID})
	return []string{
		"--robot-format=json",
		"--robot-bulk-assign=" + f.session,
		"--allocation=" + string(allocation),
		"--template=" + templatePath,
	}
}

func (f *atomicAssignmentCLIFixture) createBead(t *testing.T, title string) string {
	t.Helper()
	output := f.mustBR(t, "create", title, "--type=task", "--priority=1", "--silent")
	id := strings.TrimSpace(string(output))
	if id == "" || strings.ContainsAny(id, " \t\r\n") {
		t.Fatalf("unexpected br create output %q", output)
	}
	f.assertBead(t, id, "open", "")
	return id
}

func (f *atomicAssignmentCLIFixture) assertBead(t *testing.T, beadID, wantStatus, wantAssignee string) {
	t.Helper()
	f.assertBeadAt(t, f.projectDir, beadID, wantStatus, wantAssignee)
}

func (f *atomicAssignmentCLIFixture) assertBeadAt(t *testing.T, dir, beadID, wantStatus, wantAssignee string) {
	t.Helper()
	output := f.mustBRAt(t, dir, "show", beadID, "--json")
	var rows []atomicAssignmentBead
	if err := json.Unmarshal(output, &rows); err != nil {
		var row atomicAssignmentBead
		if objectErr := json.Unmarshal(output, &row); objectErr != nil {
			t.Fatalf("decode br show output: array=%v object=%v raw=%s", err, objectErr, output)
		}
		rows = []atomicAssignmentBead{row}
	}
	if len(rows) != 1 {
		t.Fatalf("br show %s returned %d rows: %s", beadID, len(rows), output)
	}
	if rows[0].ID != beadID || rows[0].Status != wantStatus || rows[0].Assignee != wantAssignee {
		t.Fatalf("bead state = %+v, want id=%s status=%s assignee=%s", rows[0], beadID, wantStatus, wantAssignee)
	}
}

func (f *atomicAssignmentCLIFixture) assertBeadAbsentAt(t *testing.T, dir, beadID string) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	cmd := exec.CommandContext(ctx, f.brPath, "show", beadID, "--json")
	cmd.Dir = dir
	cmd.Env = append([]string(nil), f.env...)
	output, err := cmd.CombinedOutput()
	if ctx.Err() == context.DeadlineExceeded {
		t.Fatalf("br show %s timed out in %s", beadID, dir)
	}
	if err == nil {
		t.Fatalf("bead %s unexpectedly exists in %s: %s", beadID, dir, output)
	}
}

func (f *atomicAssignmentCLIFixture) ledgerPath() string {
	return filepath.Join(f.homeDir, ".ntm", "sessions", f.session, "assignments.json")
}

func atomicAssignmentBeadOperationLockPath(ledgerPath, beadID string) string {
	digest := sha256.Sum256([]byte("bead\x00" + beadID))
	return ledgerPath + ".atomic-bead-" + hex.EncodeToString(digest[:16]) + ".lock"
}

func atomicAssignmentWaitForProcessOpenPath(t *testing.T, pid int, path string, timeout time.Duration) {
	t.Helper()
	processFDDir := filepath.Join("/proc", fmt.Sprintf("%d", pid), "fd")
	if _, err := os.Stat("/proc/self/fd"); err != nil {
		// The process-boundary assertion is exact on Linux. Other Unix systems
		// lack procfs, so allow the first two-second watch tick to enter its
		// guarded transition before continuing.
		time.Sleep(2250 * time.Millisecond)
		return
	}

	deadline := time.Now().Add(timeout)
	for {
		entries, err := os.ReadDir(processFDDir)
		if err == nil {
			for _, entry := range entries {
				target, readErr := os.Readlink(filepath.Join(processFDDir, entry.Name()))
				if readErr == nil && target == path {
					return
				}
			}
		} else if os.IsNotExist(err) {
			t.Fatalf("process %d exited before opening %s", pid, path)
		}
		if time.Now().After(deadline) {
			t.Fatalf("process %d did not open %s before timeout", pid, path)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func (f *atomicAssignmentCLIFixture) assertLedgerHasNoAssignment(t *testing.T, beadID string) {
	t.Helper()
	ledger, err := f.readLedger()
	if os.IsNotExist(err) {
		return
	}
	if err != nil {
		t.Fatalf("read ledger after reservation refusal: %v", err)
	}
	if _, exists := ledger.Assignments[beadID]; exists {
		t.Fatalf("reservation refusal persisted assignment for %s: %+v", beadID, ledger.Assignments[beadID])
	}
}

func (f *atomicAssignmentCLIFixture) assertLedgerReplicasHaveNoAssignment(t *testing.T, beadID string) {
	t.Helper()
	for _, path := range []string{f.ledgerPath(), f.ledgerPath() + ".bak"} {
		info, err := os.Lstat(path)
		if os.IsNotExist(err) {
			continue
		}
		if err != nil {
			t.Fatalf("inspect assignment ledger replica %s: %v", path, err)
		}
		t.Fatalf("assignment safety path created ledger replica %s while rejecting %s: mode=%s size=%d", path, beadID, info.Mode(), info.Size())
	}
}

func snapshotAtomicAssignmentFiles(t *testing.T, paths ...string) map[string]atomicAssignmentFileSnapshot {
	t.Helper()
	snapshots := make(map[string]atomicAssignmentFileSnapshot, len(paths))
	for _, path := range paths {
		info, err := os.Lstat(path)
		if os.IsNotExist(err) {
			snapshots[path] = atomicAssignmentFileSnapshot{}
			continue
		}
		if err != nil {
			t.Fatalf("snapshot file %s: %v", path, err)
		}
		if !info.Mode().IsRegular() {
			t.Fatalf("snapshot file %s has non-regular mode %s", path, info.Mode())
		}
		data, err := os.ReadFile(path)
		if err != nil {
			t.Fatalf("read snapshot file %s: %v", path, err)
		}
		snapshots[path] = atomicAssignmentFileSnapshot{
			Exists: true,
			Mode:   info.Mode(),
			Data:   append([]byte(nil), data...),
		}
	}
	return snapshots
}

func assertAtomicAssignmentFilesUnchanged(t *testing.T, before map[string]atomicAssignmentFileSnapshot) {
	t.Helper()
	paths := make([]string, 0, len(before))
	for path := range before {
		paths = append(paths, path)
	}
	after := snapshotAtomicAssignmentFiles(t, paths...)
	if !reflect.DeepEqual(after, before) {
		t.Fatalf("command mutated protected files:\nbefore=%#v\nafter=%#v", before, after)
	}
}

func snapshotAtomicAssignmentTree(t *testing.T, root string) map[string]atomicAssignmentFileSnapshot {
	t.Helper()
	snapshots := make(map[string]atomicAssignmentFileSnapshot)
	if _, err := os.Lstat(root); os.IsNotExist(err) {
		return snapshots
	} else if err != nil {
		t.Fatalf("inspect snapshot tree root %s: %v", root, err)
	}
	if err := filepath.WalkDir(root, func(path string, entry os.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		relative, err := filepath.Rel(root, path)
		if err != nil {
			return err
		}
		info, err := entry.Info()
		if err != nil {
			return err
		}
		snapshot := atomicAssignmentFileSnapshot{Exists: true, Mode: info.Mode()}
		if info.Mode().IsRegular() {
			data, err := os.ReadFile(path)
			if err != nil {
				return err
			}
			snapshot.Data = append([]byte(nil), data...)
		}
		snapshots[relative] = snapshot
		return nil
	}); err != nil {
		t.Fatalf("snapshot tree %s: %v", root, err)
	}
	return snapshots
}

func assertAtomicAssignmentTreeUnchanged(t *testing.T, root string, before map[string]atomicAssignmentFileSnapshot) {
	t.Helper()
	after := snapshotAtomicAssignmentTree(t, root)
	if !reflect.DeepEqual(after, before) {
		t.Fatalf("command mutated protected tree %s:\nbefore=%#v\nafter=%#v", root, before, after)
	}
}

func snapshotAtomicAssignmentPanes(t *testing.T, fixture *atomicAssignmentCLIFixture) map[int]string {
	t.Helper()
	return map[int]string{
		0: fixture.capturePane(t, 0),
		1: fixture.capturePane(t, 1),
	}
}

func assertAtomicAssignmentPanesUnchanged(t *testing.T, fixture *atomicAssignmentCLIFixture, before map[int]string) {
	t.Helper()
	for pane, capture := range before {
		if after := fixture.capturePane(t, pane); after != capture {
			t.Fatalf("command mutated pane %d:\nbefore=%q\nafter=%q", pane, capture, after)
		}
	}
}

func assertAtomicAssignmentMailUntouched(t *testing.T, snapshot atomicAssignmentMailSnapshot) {
	t.Helper()
	// Safety preflight may perform health/project discovery and reservation-list
	// reads. It must not reserve, release, send, or leave any mutation behind.
	if snapshot.ReserveCalls != 0 || snapshot.ReleaseCalls != 0 || snapshot.SendCalls != 0 || len(snapshot.ReleasedIDs) != 0 ||
		len(snapshot.GrantedAgents) != 0 || len(snapshot.GrantedPaths) != 0 || len(snapshot.Active) != 0 {
		t.Fatalf("failure mutated Agent Mail: %+v", snapshot)
	}
}

func (f *atomicAssignmentCLIFixture) assertSecretAbsent(t *testing.T, secret string, payloads ...[]byte) {
	t.Helper()
	for index, payload := range payloads {
		if bytes.Contains(payload, []byte(secret)) {
			t.Fatalf("secret leaked in payload %d: %s", index, payload)
		}
	}
}

func (f *atomicAssignmentCLIFixture) assertAssignmentArtifactsExclude(t *testing.T, secret string) {
	t.Helper()
	for _, path := range []string{f.ledgerPath(), f.ledgerPath() + ".bak"} {
		data, err := os.ReadFile(path)
		if err != nil {
			t.Fatalf("read assignment artifact %s: %v", path, err)
		}
		if bytes.Contains(data, []byte(secret)) {
			t.Fatalf("secret leaked in assignment artifact %s", path)
		}
	}
}

func (f *atomicAssignmentCLIFixture) assertPrivateStateExcludes(t *testing.T, secret string) {
	t.Helper()
	for _, root := range []string{f.homeDir, filepath.Join(f.root, "config"), filepath.Join(f.root, "data")} {
		err := filepath.WalkDir(root, func(path string, entry os.DirEntry, walkErr error) error {
			if walkErr != nil {
				return walkErr
			}
			if entry.IsDir() {
				return nil
			}
			data, readErr := os.ReadFile(path)
			if readErr != nil {
				return readErr
			}
			if bytes.Contains(data, []byte(secret)) {
				return fmt.Errorf("secret leaked in private state artifact %s", path)
			}
			return nil
		})
		if err != nil {
			t.Fatal(err)
		}
	}
}

func (f *atomicAssignmentCLIFixture) readLedgerAssignment(t *testing.T, beadID string) *atomicAssignmentRecord {
	t.Helper()
	ledger, err := f.readLedger()
	if err != nil {
		t.Fatalf("read assignment ledger: %v", err)
	}
	if ledger.SessionName != f.session || ledger.Version < 2 {
		t.Fatalf("ledger header = session:%q version:%d", ledger.SessionName, ledger.Version)
	}
	record := ledger.Assignments[beadID]
	if record == nil {
		t.Fatalf("assignment ledger missing %s: %+v", beadID, ledger.Assignments)
	}
	return record
}

func (f *atomicAssignmentCLIFixture) readLedger() (*atomicAssignmentLedger, error) {
	data, err := os.ReadFile(f.ledgerPath())
	if err != nil {
		return nil, err
	}
	var ledger atomicAssignmentLedger
	if err := json.Unmarshal(data, &ledger); err != nil {
		return nil, err
	}
	return &ledger, nil
}

func readAtomicAssignmentLedgerAt(t *testing.T, path string) (*atomicAssignmentLedger, []byte) {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read assignment ledger %s: %v", path, err)
	}
	var ledger atomicAssignmentLedger
	if err := json.Unmarshal(data, &ledger); err != nil {
		t.Fatalf("decode assignment ledger %s: %v", path, err)
	}
	return &ledger, data
}

func (f *atomicAssignmentCLIFixture) writeLedgerReplicas(t *testing.T, data []byte, operation string) {
	t.Helper()
	for _, path := range []string{f.ledgerPath() + ".bak", f.ledgerPath()} {
		if err := os.WriteFile(path, data, 0o600); err != nil {
			t.Fatalf("write assignment ledger %s during %s: %v", path, operation, err)
		}
	}
}

func (f *atomicAssignmentCLIFixture) setLedgerAssignmentStatus(t *testing.T, beadID, status string) {
	t.Helper()
	data, err := os.ReadFile(f.ledgerPath())
	if err != nil {
		t.Fatalf("read assignment ledger for status update: %v", err)
	}
	var ledger map[string]json.RawMessage
	if err := json.Unmarshal(data, &ledger); err != nil {
		t.Fatalf("decode assignment ledger for status update: %v", err)
	}
	var assignments map[string]json.RawMessage
	if err := json.Unmarshal(ledger["assignments"], &assignments); err != nil {
		t.Fatalf("decode assignments for status update: %v", err)
	}
	rawRecord, ok := assignments[beadID]
	if !ok {
		t.Fatalf("assignment ledger missing %s", beadID)
	}
	var record map[string]json.RawMessage
	if err := json.Unmarshal(rawRecord, &record); err != nil {
		t.Fatalf("decode assignment %s for status update: %v", beadID, err)
	}
	encodedStatus, _ := json.Marshal(status)
	completedAt, _ := json.Marshal(time.Now().UTC())
	record["status"] = encodedStatus
	record["completed_at"] = completedAt
	assignments[beadID], err = json.Marshal(record)
	if err != nil {
		t.Fatalf("encode assignment %s status update: %v", beadID, err)
	}
	ledger["assignments"], err = json.Marshal(assignments)
	if err != nil {
		t.Fatalf("encode assignment map status update: %v", err)
	}
	updated, err := json.MarshalIndent(ledger, "", "  ")
	if err != nil {
		t.Fatalf("encode assignment ledger status update: %v", err)
	}
	f.writeLedgerReplicas(t, append(updated, '\n'), "status update")
}

func (f *atomicAssignmentCLIFixture) setLedgerAssignmentFields(t *testing.T, beadID string, fields map[string]any) {
	t.Helper()
	data, err := os.ReadFile(f.ledgerPath())
	if err != nil {
		t.Fatalf("read assignment ledger for field update: %v", err)
	}
	var ledger map[string]json.RawMessage
	if err := json.Unmarshal(data, &ledger); err != nil {
		t.Fatalf("decode assignment ledger for field update: %v", err)
	}
	var assignments map[string]json.RawMessage
	if err := json.Unmarshal(ledger["assignments"], &assignments); err != nil {
		t.Fatalf("decode assignments for field update: %v", err)
	}
	var record map[string]json.RawMessage
	if err := json.Unmarshal(assignments[beadID], &record); err != nil {
		t.Fatalf("decode assignment %s for field update: %v", beadID, err)
	}
	for key, value := range fields {
		encoded, marshalErr := json.Marshal(value)
		if marshalErr != nil {
			t.Fatalf("encode assignment %s field %s: %v", beadID, key, marshalErr)
		}
		record[key] = encoded
	}
	assignments[beadID], err = json.Marshal(record)
	if err != nil {
		t.Fatalf("encode assignment %s field update: %v", beadID, err)
	}
	ledger["assignments"], err = json.Marshal(assignments)
	if err != nil {
		t.Fatalf("encode assignment map field update: %v", err)
	}
	updated, err := json.MarshalIndent(ledger, "", "  ")
	if err != nil {
		t.Fatalf("encode assignment ledger field update: %v", err)
	}
	f.writeLedgerReplicas(t, append(updated, '\n'), "field update")
}

func (f *atomicAssignmentCLIFixture) setLedgerAssignmentReservations(t *testing.T, beadID string, paths []string, ids []int) {
	t.Helper()
	data, err := os.ReadFile(f.ledgerPath())
	if err != nil {
		t.Fatalf("read assignment ledger for reservation update: %v", err)
	}
	var ledger map[string]json.RawMessage
	if err := json.Unmarshal(data, &ledger); err != nil {
		t.Fatalf("decode assignment ledger for reservation update: %v", err)
	}
	var assignments map[string]json.RawMessage
	if err := json.Unmarshal(ledger["assignments"], &assignments); err != nil {
		t.Fatalf("decode assignments for reservation update: %v", err)
	}
	var record map[string]json.RawMessage
	if err := json.Unmarshal(assignments[beadID], &record); err != nil {
		t.Fatalf("decode assignment %s for reservation update: %v", beadID, err)
	}
	for key, value := range map[string]any{
		"reservation_required":   true,
		"reservation_completed":  true,
		"reservation_state":      "reserved",
		"reservation_attempts":   1,
		"reservation_agent":      recordString(record["agent_name"]),
		"reservation_target":     recordString(record["occupancy_key"]),
		"reservation_requested":  paths,
		"reserved_paths":         paths,
		"reservation_ids":        ids,
		"reservation_expires_at": time.Now().UTC().Add(time.Hour),
	} {
		encoded, marshalErr := json.Marshal(value)
		if marshalErr != nil {
			t.Fatalf("encode assignment %s field %s: %v", beadID, key, marshalErr)
		}
		record[key] = encoded
	}
	assignments[beadID], err = json.Marshal(record)
	if err != nil {
		t.Fatalf("encode assignment %s reservation update: %v", beadID, err)
	}
	ledger["assignments"], err = json.Marshal(assignments)
	if err != nil {
		t.Fatalf("encode assignment map reservation update: %v", err)
	}
	updated, err := json.MarshalIndent(ledger, "", "  ")
	if err != nil {
		t.Fatalf("encode assignment ledger reservation update: %v", err)
	}
	f.writeLedgerReplicas(t, append(updated, '\n'), "reservation update")
}

func (f *atomicAssignmentCLIFixture) setLedgerAssignmentLegacyPaneTarget(t *testing.T, beadID string) {
	t.Helper()
	data, err := os.ReadFile(f.ledgerPath())
	if err != nil {
		t.Fatalf("read assignment ledger for legacy target update: %v", err)
	}
	var ledger map[string]json.RawMessage
	if err := json.Unmarshal(data, &ledger); err != nil {
		t.Fatalf("decode assignment ledger for legacy target update: %v", err)
	}
	var assignments map[string]json.RawMessage
	if err := json.Unmarshal(ledger["assignments"], &assignments); err != nil {
		t.Fatalf("decode assignments for legacy target update: %v", err)
	}
	var record map[string]json.RawMessage
	if err := json.Unmarshal(assignments[beadID], &record); err != nil {
		t.Fatalf("decode assignment %s for legacy target update: %v", beadID, err)
	}
	delete(record, "occupancy_key")
	delete(record, "dispatch_target")
	assignments[beadID], err = json.Marshal(record)
	if err != nil {
		t.Fatalf("encode assignment %s legacy target update: %v", beadID, err)
	}
	ledger["assignments"], err = json.Marshal(assignments)
	if err != nil {
		t.Fatalf("encode assignment map legacy target update: %v", err)
	}
	updated, err := json.MarshalIndent(ledger, "", "  ")
	if err != nil {
		t.Fatalf("encode assignment ledger legacy target update: %v", err)
	}
	f.writeLedgerReplicas(t, append(updated, '\n'), "legacy target update")
}

func recordString(raw json.RawMessage) string {
	var value string
	_ = json.Unmarshal(raw, &value)
	return value
}

func assertAtomicAssignmentRecord(t *testing.T, record *atomicAssignmentRecord, beadID, prompt string, pane atomicAssignmentPane, agentType string) {
	t.Helper()
	assertAtomicAssignmentRecordWithClaimIdentity(t, record, beadID, prompt, pane, agentType, true)
}

func assertAtomicAssignmentRecordWithClaimIdentity(t *testing.T, record *atomicAssignmentRecord, beadID, prompt string, pane atomicAssignmentPane, agentType string, requireKeyedClaimActor bool) {
	t.Helper()
	if record.BeadID != beadID || record.Pane != pane.Index || record.AgentType != agentType ||
		record.Status != "assigned" || record.PromptSent != prompt {
		t.Fatalf("assignment identity/state = %+v", record)
	}
	if record.AgentName == "" || record.IdempotencyKey == "" || record.ClaimActor == "" || record.ClaimStatus != "in_progress" {
		t.Fatalf("claim metadata incomplete: %+v", record)
	}
	decodedKey, err := hex.DecodeString(record.IdempotencyKey)
	if err != nil || len(decodedKey) != sha256.Size {
		t.Fatalf("idempotency key %q is not a 256-bit hex key: decoded=%d err=%v", record.IdempotencyKey, len(decodedKey), err)
	}
	if requireKeyedClaimActor && !strings.HasSuffix(record.ClaimActor, "/ntm-"+record.IdempotencyKey[:12]) {
		t.Fatalf("claim actor %q does not carry idempotency identity %q", record.ClaimActor, record.IdempotencyKey[:12])
	}
	if record.ClaimedAt == nil || record.DispatchStartedAt == nil || record.DispatchedAt == nil {
		t.Fatalf("claim/dispatch timestamps incomplete: %+v", record)
	}
	if record.ClaimedAt.After(*record.DispatchStartedAt) || record.DispatchStartedAt.After(*record.DispatchedAt) {
		t.Fatalf("claim-before-dispatch order violated: claimed=%s started=%s dispatched=%s", record.ClaimedAt, record.DispatchStartedAt, record.DispatchedAt)
	}
	canonicalTarget := pane.Target
	if pane.Window == 0 {
		canonicalTarget = fmt.Sprintf("%d", pane.Index)
	}
	dispatchTargetMatches := record.DispatchTarget == pane.ID || record.DispatchTarget == canonicalTarget
	if record.DispatchState != "sent" || !dispatchTargetMatches || record.OccupancyKey != pane.ID || record.DispatchAttempts != 1 || record.DispatchReceiptID == "" {
		t.Fatalf("dispatch metadata incomplete: %+v", record)
	}
	if !strings.Contains(record.DispatchReceiptID, pane.ID) {
		t.Fatalf("dispatch receipt %q does not identify target %q", record.DispatchReceiptID, pane.ID)
	}
	if record.PendingPrompt != "" {
		t.Fatalf("sent assignment retained pending prompt %q", record.PendingPrompt)
	}
	hash := sha256.Sum256([]byte(prompt))
	if record.PromptSHA256 != hex.EncodeToString(hash[:]) {
		t.Fatalf("prompt hash = %q, want %x", record.PromptSHA256, hash)
	}
	if record.ReservationRequired || record.ReservationDiscovery || record.ReservationCompleted || len(record.ReservationInputPaths) != 0 {
		t.Fatalf("reservation-disabled assignment persisted reservation state: %+v", record)
	}
}

func assertDirectAssignmentEnvelope(t *testing.T, envelope atomicAssignmentDirectEnvelope, session, beadID, prompt string, pane atomicAssignmentPane) {
	t.Helper()
	assertDirectAssignmentEnvelopeWithReceiptPane(t, envelope, session, beadID, prompt, pane.Index, pane)
}

func assertDirectAssignmentEnvelopeWithReceiptPane(
	t *testing.T,
	envelope atomicAssignmentDirectEnvelope,
	session, beadID, prompt string,
	assignmentPane int,
	receiptPane atomicAssignmentPane,
) {
	t.Helper()
	if !envelope.Success || envelope.Command != "assign" || envelope.Session != session || envelope.Error != nil || envelope.Data == nil {
		t.Fatalf("direct envelope = %+v", envelope)
	}
	assignment := envelope.Data.Assignment
	receiptTarget := receiptPane.Target
	if receiptPane.Window == 0 {
		receiptTarget = fmt.Sprintf("%d", receiptPane.Index)
	}
	if assignment.BeadID != beadID || assignment.Pane != assignmentPane || assignment.PaneTarget != receiptTarget ||
		assignment.PaneID != receiptPane.ID || assignment.AgentType != "codex" || assignment.Prompt != prompt || !assignment.PromptSent {
		t.Fatalf("direct assignment response = %+v", assignment)
	}
	receipt := envelope.Data.Receipt
	if receipt == nil || receipt.WorkItemID != beadID || receipt.Pane.Session != session ||
		receipt.Pane.Target != receiptTarget || receipt.Pane.WindowIndex != receiptPane.Window ||
		receipt.Pane.Index != receiptPane.Index || receipt.Pane.ID != receiptPane.ID || !receipt.Transport.Sent || receipt.Transport.Error != "" {
		t.Fatalf("direct dispatch receipt = %+v", receipt)
	}
	hash := sha256.Sum256([]byte(prompt))
	if receipt.Prompt.Length != len(prompt) || receipt.Prompt.HashSHA256 != hex.EncodeToString(hash[:]) {
		t.Fatalf("direct prompt receipt = %+v", receipt.Prompt)
	}
}

func assertSameDirectReceipt(t *testing.T, first, replay atomicAssignmentDirectEnvelope) {
	t.Helper()
	if first.Data == nil || first.Data.Receipt == nil || replay.Data == nil || replay.Data.Receipt == nil {
		t.Fatalf("missing direct receipt: first=%+v replay=%+v", first.Data, replay.Data)
	}
	if first.Data.Receipt.WorkItemID != replay.Data.Receipt.WorkItemID ||
		first.Data.Receipt.Pane != replay.Data.Receipt.Pane ||
		first.Data.Receipt.Prompt != replay.Data.Receipt.Prompt ||
		first.Data.Receipt.Transport != replay.Data.Receipt.Transport ||
		first.Data.Receipt.Timestamp != replay.Data.Receipt.Timestamp {
		t.Fatalf("direct replay receipt changed: first=%+v replay=%+v", first.Data.Receipt, replay.Data.Receipt)
	}
}

func assertAtomicAssignmentReceiptUnchanged(t *testing.T, first, replay *atomicAssignmentRecord) {
	t.Helper()
	if first.IdempotencyKey != replay.IdempotencyKey || first.ClaimActor != replay.ClaimActor ||
		first.ClaimAttempts != replay.ClaimAttempts || first.DispatchAttempts != replay.DispatchAttempts ||
		first.DispatchedAt == nil || replay.DispatchedAt == nil || !first.DispatchedAt.Equal(*replay.DispatchedAt) ||
		first.DispatchReceiptID != replay.DispatchReceiptID || first.PromptSent != replay.PromptSent {
		t.Fatalf("durable receipt changed: first=%+v replay=%+v", first, replay)
	}
}

func assertBulkAssignmentEnvelope(t *testing.T, envelope atomicAssignmentBulkEnvelope, beadID, pane string) *struct {
	Pane              string `json:"pane"`
	PaneID            string `json:"pane_id"`
	Bead              string `json:"bead"`
	AgentType         string `json:"agent_type"`
	Status            string `json:"status"`
	PromptSent        bool   `json:"prompt_sent"`
	Claimed           bool   `json:"claimed"`
	ClaimActor        string `json:"claim_actor"`
	IdempotencyKey    string `json:"idempotency_key"`
	DispatchReceiptID string `json:"dispatch_receipt_id"`
	Error             string `json:"error"`
} {
	t.Helper()
	if !envelope.Success || envelope.Error != "" || envelope.ErrorCode != "" || envelope.Summary.Assigned != 1 || envelope.Summary.Failed != 0 {
		t.Fatalf("bulk envelope summary = %+v", envelope)
	}
	if len(envelope.Assignments) != 1 {
		t.Fatalf("bulk assignments = %+v", envelope.Assignments)
	}
	assignment := &envelope.Assignments[0]
	if assignment.Bead != beadID || assignment.Pane != pane || assignment.AgentType != "claude" ||
		assignment.Status != "assigned" || !assignment.PromptSent || !assignment.Claimed ||
		assignment.ClaimActor == "" || assignment.IdempotencyKey == "" || assignment.DispatchReceiptID == "" || assignment.Error != "" {
		t.Fatalf("bulk assignment = %+v", assignment)
	}
	return assignment
}

func decodeAtomicAssignmentJSON(t *testing.T, data []byte, target any) {
	t.Helper()
	if err := json.Unmarshal(bytes.TrimSpace(data), target); err != nil {
		t.Fatalf("decode JSON: %v raw=%s", err, data)
	}
}

func assertAtomicAssignmentSingleFailureJSON(t *testing.T, result atomicAssignmentProcessResult) (string, string) {
	t.Helper()
	if result.exitCode != 1 || len(bytes.TrimSpace(result.stderr)) != 0 {
		t.Fatalf("failed command exit=%d, want 1; stdout=%s stderr=%s", result.exitCode, result.stdout, result.stderr)
	}
	var envelope struct {
		Success   *bool           `json:"success"`
		ErrorCode string          `json:"error_code"`
		Error     json.RawMessage `json:"error"`
	}
	decoder := json.NewDecoder(bytes.NewReader(bytes.TrimSpace(result.stdout)))
	if err := decoder.Decode(&envelope); err != nil {
		t.Fatalf("decode failure JSON: %v raw=%s", err, result.stdout)
	}
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		t.Fatalf("failure emitted more than one JSON document: err=%v trailing=%v raw=%s", err, trailing, result.stdout)
	}
	if envelope.Success == nil || *envelope.Success {
		t.Fatalf("failure envelope must contain success=false: raw=%s", result.stdout)
	}

	code := envelope.ErrorCode
	message := ""
	trimmedError := bytes.TrimSpace(envelope.Error)
	if len(trimmedError) > 0 && !bytes.Equal(trimmedError, []byte("null")) {
		switch trimmedError[0] {
		case '{':
			var nested struct {
				Code    string `json:"code"`
				Message string `json:"message"`
			}
			if err := json.Unmarshal(trimmedError, &nested); err != nil {
				t.Fatalf("decode failure error object: %v raw=%s", err, trimmedError)
			}
			if nested.Code != "" {
				code = nested.Code
			}
			message = nested.Message
		case '"':
			if err := json.Unmarshal(trimmedError, &message); err != nil {
				t.Fatalf("decode failure error string: %v raw=%s", err, trimmedError)
			}
		default:
			t.Fatalf("failure error must be a string or object: raw=%s", trimmedError)
		}
	}
	if code == "" || message == "" {
		t.Fatalf("failure envelope omitted code or message: code=%q message=%q raw=%s", code, message, result.stdout)
	}
	return code, message
}

func (f *atomicAssignmentCLIFixture) startAgentPanes(t *testing.T) {
	t.Helper()
	agentCommand := atomicAgentPaneCommand
	f.mustTMUX(t, "-f", f.tmuxConfig, "new-session", "-d", "-s", f.session, "-x", "160", "-y", "48", "-c", f.projectDir, agentCommand)
	f.mustTMUX(t, "split-window", "-d", "-t", f.session+":0", "-c", f.projectDir, agentCommand)
	f.mustTMUX(t, "select-pane", "-t", f.session+":0.0", "-T", f.session+"__cod_1")
	f.mustTMUX(t, "select-pane", "-t", f.session+":0.1", "-T", f.session+"__cc_2")
	f.waitForPanes(t)
}

func (f *atomicAssignmentCLIFixture) waitForPanes(t *testing.T) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for {
		output, err := f.tmuxOutput(context.Background(), "list-panes", "-s", "-t", f.session, "-F", "#{window_index}|#{pane_index}|#{pane_id}|#{pane_title}|#{pane_current_command}")
		if err == nil {
			panes := make(map[int]atomicAssignmentPane)
			ready := true
			for _, line := range strings.Split(strings.TrimSpace(string(output)), "\n") {
				parts := strings.SplitN(line, "|", 5)
				if len(parts) != 5 {
					ready = false
					break
				}
				var window, index int
				if _, scanErr := fmt.Sscanf(parts[0]+" "+parts[1], "%d %d", &window, &index); scanErr != nil || window != 0 || parts[4] != "cat" {
					ready = false
					break
				}
				panes[index] = atomicAssignmentPane{
					Window: window,
					Index:  index,
					Target: fmt.Sprintf("%d.%d", window, index),
					ID:     parts[2],
					Title:  parts[3],
				}
			}
			if ready && len(panes) == 2 && panes[0].Title == f.session+"__cod_1" && panes[1].Title == f.session+"__cc_2" {
				f.panes = panes
				return
			}
		}
		if time.Now().After(deadline) {
			t.Fatalf("isolated cat panes did not become ready: output=%s err=%v", output, err)
		}
		time.Sleep(50 * time.Millisecond)
	}
}

func (f *atomicAssignmentCLIFixture) addSecondAgentWindow(t *testing.T) map[string]atomicAssignmentPane {
	t.Helper()
	agentCommand := atomicAgentPaneCommand
	f.mustTMUX(t, "new-window", "-d", "-t", f.session+":1", "-c", f.projectDir, agentCommand)
	f.mustTMUX(t, "split-window", "-d", "-t", f.session+":1", "-c", f.projectDir, agentCommand)
	f.mustTMUX(t, "select-pane", "-t", f.session+":1.0", "-T", f.session+"__cod_3")
	f.mustTMUX(t, "select-pane", "-t", f.session+":1.1", "-T", f.session+"__cc_4")
	return f.waitForTopology(t, 4)
}

func (f *atomicAssignmentCLIFixture) waitForTopology(t *testing.T, want int) map[string]atomicAssignmentPane {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for {
		output, err := f.tmuxOutput(context.Background(), "list-panes", "-s", "-t", f.session, "-F", "#{window_index}|#{pane_index}|#{pane_id}|#{pane_title}|#{pane_current_command}")
		if err == nil {
			panes := make(map[string]atomicAssignmentPane)
			ready := true
			for _, line := range strings.Split(strings.TrimSpace(string(output)), "\n") {
				parts := strings.SplitN(line, "|", 5)
				if len(parts) != 5 {
					ready = false
					break
				}
				var window, index int
				if _, scanErr := fmt.Sscanf(parts[0]+" "+parts[1], "%d %d", &window, &index); scanErr != nil || parts[4] != "cat" {
					ready = false
					break
				}
				target := fmt.Sprintf("%d.%d", window, index)
				panes[target] = atomicAssignmentPane{
					Window: window,
					Index:  index,
					Target: target,
					ID:     parts[2],
					Title:  parts[3],
				}
			}
			if ready && len(panes) == want {
				return panes
			}
		}
		if time.Now().After(deadline) {
			t.Fatalf("isolated topology did not reach %d ready cat panes: output=%s err=%v", want, output, err)
		}
		time.Sleep(50 * time.Millisecond)
	}
}

func (f *atomicAssignmentCLIFixture) primeEndpointForSafeDispatch(t *testing.T, pane atomicAssignmentPane) {
	t.Helper()
	// Render the pane's own live-composer glyph ("❯" Claude, "›" codex).
	// Dispatch refuses to type into a Claude/codex pane whose non-empty
	// capture shows neither the composer marker nor working-state chrome
	// (bd-dp9oy), so the historical ASCII "claude>"/"codex>" idle prompts
	// no longer count as delivery-ready.
	prompt := "❯"
	if strings.Contains(pane.Title, "__cod_") {
		prompt = "›"
	}
	f.mustTMUX(t, "send-keys", "-t", pane.ID, "-l", prompt)
	f.mustTMUX(t, "send-keys", "-t", pane.ID, "Enter")

	deadline := time.Now().Add(5 * time.Second)
	for {
		capture := strings.TrimSpace(f.captureEndpoint(t, pane))
		if strings.HasSuffix(capture, prompt) {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("pane %s did not reach explicit idle prompt %q: %q", pane.Target, prompt, capture)
		}
		time.Sleep(25 * time.Millisecond)
	}
}

func (f *atomicAssignmentCLIFixture) waitForEndpointIdle(t *testing.T, pane atomicAssignmentPane, minimum, timeout time.Duration) {
	t.Helper()
	timer := time.NewTimer(timeout)
	defer timer.Stop()
	ticker := time.NewTicker(25 * time.Millisecond)
	defer ticker.Stop()

	var lastActivity string
	var lastErr error
	for {
		ctx, cancel := context.WithTimeout(t.Context(), 2*time.Second)
		output, err := f.tmuxOutput(ctx, "display-message", "-p", "-t", pane.ID, "#{window_activity}")
		cancel()
		lastActivity = strings.TrimSpace(string(output))
		lastErr = err
		if err == nil {
			activityUnix, parseErr := strconv.ParseInt(lastActivity, 10, 64)
			if parseErr == nil {
				if time.Since(time.Unix(activityUnix, 0)) >= minimum {
					return
				}
			} else {
				lastErr = parseErr
			}
		}

		select {
		case <-ticker.C:
		case <-timer.C:
			t.Fatalf("pane %s did not remain idle for %s within %s: activity=%q error=%v", pane.Target, minimum, timeout, lastActivity, lastErr)
		case <-t.Context().Done():
			t.Fatalf("wait for pane %s idle duration: %v", pane.Target, t.Context().Err())
		}
	}
}

func (f *atomicAssignmentCLIFixture) driveAssignmentStatus(t *testing.T, pane atomicAssignmentPane, beadID, paneOutput, wantStatus string) {
	f.driveAssignmentStatusUntil(t, pane, beadID, paneOutput, wantStatus, false)
}

func (f *atomicAssignmentCLIFixture) driveAssignmentStatusWithCompletionEvent(t *testing.T, pane atomicAssignmentPane, beadID, paneOutput, wantStatus string) {
	f.driveAssignmentStatusUntil(t, pane, beadID, paneOutput, wantStatus, true)
}

func (f *atomicAssignmentCLIFixture) driveAssignmentStatusUntil(t *testing.T, pane atomicAssignmentPane, beadID, paneOutput, wantStatus string, requireCompletionEvent bool) {
	t.Helper()
	f.mustTMUX(t, "send-keys", "-t", pane.ID, "-l", paneOutput)
	f.mustTMUX(t, "send-keys", "-t", pane.ID, "Enter")

	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	watch := exec.CommandContext(ctx, f.ntmPath,
		"assign", f.session,
		"--repo="+f.projectDir,
		"--watch",
		"--watch-interval=100ms",
		"--dry-run",
		"--reserve-files=false",
		"--quiet",
	)
	watch.Dir = f.projectDir
	watch.Env = atomicAssignmentMergeEnv(f.env, f.completionWatchEnv(t, nil))
	if requireCompletionEvent {
		// The normal watch loop acknowledges failed events immediately. Hold its
		// handler after the detector has durably claimed the event so this helper
		// can observe the outbox boundary, stop the watcher, and let the caller
		// exercise acknowledgement and retry deterministically.
		watch.Env = atomicAssignmentMergeEnv(watch.Env, map[string]string{
			"NTM_TEST_COMPLETION_HANDLER_DELAY": "2m",
		})
	}
	var stdout bytes.Buffer
	var stderr bytes.Buffer
	watch.Stdout = &stdout
	watch.Stderr = &stderr
	group, groupErr := testutil.NewProcessGroupForTest(ctx, watch)
	if groupErr != nil {
		t.Fatalf("create owned process group for assignment status watch: %v", groupErr)
	}
	watch.Cancel = func() error {
		return group.Signal(os.Kill)
	}
	watch.WaitDelay = 10 * time.Second
	if err := watch.Start(); err != nil {
		closeErr := group.Close()
		t.Fatalf("start assignment status watch: %v; close owned process group: %v", err, closeErr)
	}
	joined := false
	var waitErr error
	stopWatch := func(signal os.Signal) error {
		if joined {
			return waitErr
		}
		signalErr := group.Signal(signal)
		if errors.Is(signalErr, os.ErrProcessDone) {
			signalErr = nil
		}
		commandWaitErr := watch.Wait()
		groupCloseErr := group.Close()
		waitErr = errors.Join(signalErr, commandWaitErr, groupCloseErr)
		joined = true
		return waitErr
	}
	defer func() {
		if !joined {
			_ = stopWatch(os.Kill)
		}
	}()

	deadline := time.Now().Add(75 * time.Second)
	for {
		ledger, readErr := f.readLedger()
		if readErr == nil {
			if record := ledger.Assignments[beadID]; record != nil && record.Status == wantStatus {
				completionEventReady := !requireCompletionEvent ||
					(strings.TrimSpace(record.PendingCompletionEventID) != "" &&
						strings.TrimSpace(record.CompletionConsumerToken) != "" &&
						record.CompletionLeaseExpiresAt != nil &&
						record.CompletionLeaseExpiresAt.After(time.Now().UTC()))
				if completionEventReady {
					break
				}
			}
		}
		if time.Now().After(deadline) {
			var durable *atomicAssignmentRecord
			if ledger != nil {
				durable = ledger.Assignments[beadID]
			}
			joinErr := stopWatch(os.Kill)
			paneSnapshot := f.captureEndpoint(t, pane)
			t.Fatalf("assignment detector did not reach %s with pending_event=%v for %s: durable=%+v pane_output=%q read_err=%v join_err=%v stdout=%s stderr=%s",
				wantStatus, requireCompletionEvent, beadID, durable, paneSnapshot, readErr, joinErr, stdout.String(), stderr.String())
		}
		time.Sleep(50 * time.Millisecond)
	}
	if err := stopWatch(syscall.SIGTERM); err != nil {
		t.Fatalf("assignment status watch did not exit cleanly: %v stdout=%s stderr=%s", err, stdout.String(), stderr.String())
	}
	if ctx.Err() != nil {
		t.Fatalf("assignment status watch exceeded deadline: %v", ctx.Err())
	}
	watchStderr := strings.TrimSpace(stderr.String())
	if unexpected := atomicAssignmentUnexpectedWatchStderr(watchStderr); unexpected != "" {
		t.Fatalf("assignment status watch stderr=%s", unexpected)
	}
	if watchStderr != "" {
		t.Logf("assignment status watch tolerated circuit-breaker warning: %s", watchStderr)
	}
}

func atomicAssignmentUnexpectedWatchStderr(stderr string) string {
	const circuitBreakerWarning = "WARN tmux circuit breaker opened consecutive_failures=5 backoff=10s"

	var unexpected []string
	tolerated := false
	for _, line := range strings.Split(stderr, "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		if line == circuitBreakerWarning && !tolerated {
			tolerated = true
			continue
		}
		unexpected = append(unexpected, line)
	}
	return strings.Join(unexpected, "\n")
}

func (f *atomicAssignmentCLIFixture) captureEndpoint(t *testing.T, pane atomicAssignmentPane) string {
	t.Helper()
	output, err := f.tmuxOutput(context.Background(), "capture-pane", "-p", "-t", pane.ID, "-S", "-200")
	if err != nil {
		t.Fatalf("capture pane %s (%s): %v", pane.Target, pane.ID, err)
	}
	return string(output)
}

func (f *atomicAssignmentCLIFixture) waitForEndpointMarkerCount(t *testing.T, pane atomicAssignmentPane, marker string, want int) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for {
		capture := f.captureEndpoint(t, pane)
		if strings.Count(capture, marker) == want {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("pane %s marker %q count=%d want=%d capture=%q", pane.Target, marker, strings.Count(capture, marker), want, capture)
		}
		time.Sleep(25 * time.Millisecond)
	}
}

func (f *atomicAssignmentCLIFixture) assertEndpointMarkerCounts(t *testing.T, marker string, panes map[string]atomicAssignmentPane, wants map[string]int) {
	t.Helper()
	for target, pane := range panes {
		want := wants[target]
		capture := f.captureEndpoint(t, pane)
		if got := strings.Count(capture, marker); got != want {
			t.Fatalf("pane %s marker %q count=%d want=%d capture=%q", target, marker, got, want, capture)
		}
	}
}

func (f *atomicAssignmentCLIFixture) waitForMarkerCount(t *testing.T, pane int, marker string, want int) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for {
		capture := f.capturePane(t, pane)
		if strings.Count(capture, marker) == want {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("pane %d marker %q count=%d want=%d capture=%q", pane, marker, strings.Count(capture, marker), want, capture)
		}
		time.Sleep(25 * time.Millisecond)
	}
}

func (f *atomicAssignmentCLIFixture) assertMarkerCounts(t *testing.T, marker string, wants map[int]int) {
	t.Helper()
	for pane, want := range wants {
		capture := f.capturePane(t, pane)
		if got := strings.Count(capture, marker); got != want {
			t.Fatalf("pane %d marker %q count=%d want=%d capture=%q", pane, marker, got, want, capture)
		}
	}
}

func (f *atomicAssignmentCLIFixture) primePaneForSafeDispatch(t *testing.T, pane int) {
	t.Helper()
	endpoint, ok := f.panes[pane]
	if !ok {
		t.Fatalf("unknown fixture pane %d", pane)
	}
	f.primeEndpointForSafeDispatch(t, endpoint)
}

func (f *atomicAssignmentCLIFixture) capturePane(t *testing.T, pane int) string {
	t.Helper()
	endpoint, ok := f.panes[pane]
	if !ok {
		t.Fatalf("unknown fixture pane %d", pane)
	}
	output, err := f.tmuxOutput(context.Background(), "capture-pane", "-p", "-t", endpoint.ID, "-S", "-200")
	if err != nil {
		t.Fatalf("capture pane %d: %v", pane, err)
	}
	return string(output)
}

func (f *atomicAssignmentCLIFixture) startCompletionWatch(t *testing.T, env map[string]string) *atomicCompletionWatchProcess {
	t.Helper()
	return f.startCompletionWatchWithInterval(t, env, 100*time.Millisecond)
}

func (f *atomicAssignmentCLIFixture) completionWatchEnv(t *testing.T, overrides map[string]string) map[string]string {
	t.Helper()
	result := make(map[string]string, len(overrides)+1)
	for key, value := range overrides {
		result[key] = value
	}
	basePath := atomicAssignmentEnvValue(f.env, "PATH")
	if overridePath := strings.TrimSpace(result["PATH"]); overridePath != "" {
		basePath = overridePath
	}
	binDir := t.TempDir()
	writeSpawnEmptyBV(t, filepath.Join(binDir, "bv"))
	result["PATH"] = binDir + string(os.PathListSeparator) + basePath
	return result
}

func (f *atomicAssignmentCLIFixture) startCompletionWatchWithInterval(t *testing.T, env map[string]string, watchInterval time.Duration) *atomicCompletionWatchProcess {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Minute)
	cmd := exec.CommandContext(ctx, f.ntmPath,
		"assign", f.session,
		"--repo="+f.projectDir,
		"--watch",
		"--watch-interval="+watchInterval.String(),
		"--dry-run",
		"--auto-reassign=false",
		"--reserve-files=false",
	)
	cmd.Dir = f.projectDir
	cmd.Env = atomicAssignmentMergeEnv(f.env, f.completionWatchEnv(t, env))
	stdout, err := os.CreateTemp(f.root, "completion-watch-stdout-*.log")
	if err != nil {
		cancel()
		t.Fatalf("create completion watcher stdout file: %v", err)
	}
	stderr, err := os.CreateTemp(f.root, "completion-watch-stderr-*.log")
	if err != nil {
		_ = stdout.Close()
		cancel()
		t.Fatalf("create completion watcher stderr file: %v", err)
	}
	group, groupErr := testutil.NewProcessGroupForTest(ctx, cmd)
	if groupErr != nil {
		_ = stdout.Close()
		_ = stderr.Close()
		cancel()
		t.Fatalf("create owned process group for completion watcher: %v", groupErr)
	}
	cmd.Cancel = func() error {
		return group.Signal(syscall.SIGTERM)
	}
	cmd.WaitDelay = 10 * time.Second
	cmd.Stdout = stdout
	cmd.Stderr = stderr
	process := &atomicCompletionWatchProcess{
		cmd: cmd, group: group, ctx: ctx, cancel: cancel, stdout: stdout, stderr: stderr,
	}
	if err := cmd.Start(); err != nil {
		closeErr := group.Close()
		_ = stdout.Close()
		_ = stderr.Close()
		cancel()
		t.Fatalf("start completion watcher: %v; close owned process group: %v", err, closeErr)
	}
	t.Cleanup(func() {
		if !process.waited {
			if err := process.terminate(10 * time.Second); err != nil {
				t.Errorf("terminate completion watcher during cleanup: %v", err)
			}
		}
		process.cancel()
		_ = process.stdout.Close()
		_ = process.stderr.Close()
	})
	process.waitForOutput(t, "Starting watch mode", 90*time.Second)
	return process
}

func (p *atomicCompletionWatchProcess) waitForOutput(t *testing.T, marker string, timeout time.Duration) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for {
		stdout, stderr := p.output(t)
		if strings.Contains(stdout, marker) {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("completion watcher did not emit %q within %v: stdout=%s stderr=%s", marker, timeout, stdout, stderr)
		}
		time.Sleep(25 * time.Millisecond)
	}
}

func (p *atomicCompletionWatchProcess) output(t *testing.T) (string, string) {
	t.Helper()
	stdout, err := os.ReadFile(p.stdout.Name())
	if err != nil {
		t.Fatalf("read completion watcher stdout %s: %v", p.stdout.Name(), err)
	}
	stderr, err := os.ReadFile(p.stderr.Name())
	if err != nil {
		t.Fatalf("read completion watcher stderr %s: %v", p.stderr.Name(), err)
	}
	return string(stdout), string(stderr)
}

func (p *atomicCompletionWatchProcess) terminate(grace time.Duration) error {
	if p.waited {
		return nil
	}
	if p.cmd.Process == nil {
		closeErr := p.group.Close()
		p.cancel()
		p.waited = true
		return closeErr
	}
	signalErr := p.group.Signal(syscall.SIGTERM)
	if errors.Is(signalErr, os.ErrProcessDone) {
		signalErr = nil
	}
	waitResult := make(chan error, 1)
	go func() {
		waitResult <- p.cmd.Wait()
	}()

	timer := time.NewTimer(grace)
	defer timer.Stop()
	var waitErr error
	select {
	case waitErr = <-waitResult:
	case <-timer.C:
		killErr := p.group.Signal(os.Kill)
		if errors.Is(killErr, os.ErrProcessDone) {
			killErr = nil
		}
		_ = p.cmd.Process.Kill()
		waitErr = <-waitResult
		waitErr = errors.Join(
			fmt.Errorf("completion watcher did not stop within %v", grace),
			waitErr,
			killErr,
		)
	}
	waitErr = errors.Join(waitErr, p.group.Close())
	p.waited = true
	p.cancel()
	return errors.Join(signalErr, waitErr)
}

func (p *atomicCompletionWatchProcess) stopClean(t *testing.T) (string, string) {
	t.Helper()
	if p.waited {
		t.Fatal("completion watcher was already waited")
	}
	if err := p.terminate(10 * time.Second); err != nil {
		stdout, stderr := p.output(t)
		t.Fatalf("completion watcher did not exit cleanly: %v stdout=%s stderr=%s", err, stdout, stderr)
	}
	if p.ctx.Err() == context.DeadlineExceeded {
		t.Fatalf("completion watcher exceeded deadline: %v", p.ctx.Err())
	}
	return p.output(t)
}

func (p *atomicCompletionWatchProcess) kill(t *testing.T) (string, string) {
	t.Helper()
	if p.waited {
		t.Fatal("completion watcher was already waited")
	}
	if err := p.group.Signal(os.Kill); err != nil && !errors.Is(err, os.ErrProcessDone) {
		t.Fatalf("kill completion watcher: %v", err)
	}
	waitErr := p.cmd.Wait()
	closeErr := p.group.Close()
	p.waited = true
	p.cancel()
	if closeErr != nil {
		t.Fatalf("close owned process group for killed completion watcher: %v", closeErr)
	}
	if waitErr == nil {
		t.Fatal("killed completion watcher exited successfully")
	}
	return p.output(t)
}

func (f *atomicAssignmentCLIFixture) runNTM(t *testing.T, env map[string]string, args ...string) atomicAssignmentProcessResult {
	t.Helper()
	return f.runNTMInDir(t, f.projectDir, env, args...)
}

func (f *atomicAssignmentCLIFixture) runNTMInterrupted(
	t *testing.T,
	env map[string]string,
	ready func(int),
	args ...string,
) atomicAssignmentProcessResult {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, f.ntmPath, args...)
	cmd.Dir = f.projectDir
	cmd.Env = atomicAssignmentMergeEnv(f.env, env)
	var stdout bytes.Buffer
	var stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	group, groupErr := testutil.NewProcessGroupForTest(ctx, cmd)
	if groupErr != nil {
		t.Fatalf("create owned process group for interrupted ntm %q: %v", args, groupErr)
	}
	cmd.Cancel = func() error {
		return group.Signal(syscall.SIGTERM)
	}
	cmd.WaitDelay = 10 * time.Second
	if err := cmd.Start(); err != nil {
		closeErr := group.Close()
		t.Fatalf("start interruptible ntm %q: %v; close owned process group: %v", args, err, closeErr)
	}
	waited := false
	t.Cleanup(func() {
		if !waited {
			if cmd.Process != nil {
				_ = group.Signal(os.Kill)
				_ = cmd.Process.Kill()
				_ = cmd.Wait()
			}
			if closeErr := group.Close(); closeErr != nil {
				t.Errorf("close owned process group for interrupted ntm %q: %v", args, closeErr)
			}
		}
	})
	ready(cmd.Process.Pid)
	if err := cmd.Process.Signal(os.Interrupt); err != nil {
		t.Fatalf("interrupt ntm %q: %v", args, err)
	}
	commandErr := cmd.Wait()
	closeErr := group.Close()
	waited = true
	if ctx.Err() == context.DeadlineExceeded {
		t.Fatalf("interrupted ntm command timed out: %q", args)
	}
	exitCode := 0
	if closeErr != nil {
		t.Fatalf("close owned process group for interrupted ntm %q: %v", args, closeErr)
	}
	if commandErr != nil {
		var exitErr *exec.ExitError
		if errors.As(commandErr, &exitErr) {
			exitCode = exitErr.ExitCode()
		} else {
			t.Fatalf("wait for interrupted ntm %q: %v", args, commandErr)
		}
	}
	t.Logf("[E2E-ATOMIC-CANCEL] exit=%d args=%q stdout=%s stderr=%s", exitCode, args, truncateString(stdout.String(), 500), truncateString(stderr.String(), 500))
	return atomicAssignmentProcessResult{stdout: stdout.Bytes(), stderr: stderr.Bytes(), exitCode: exitCode}
}

func assertAtomicAssignmentSingleTimeoutJSON(t *testing.T, result atomicAssignmentProcessResult) {
	t.Helper()
	if result.exitCode != 1 || len(bytes.TrimSpace(result.stderr)) != 0 {
		t.Fatalf("canceled command exit=%d stdout=%s stderr=%s", result.exitCode, result.stdout, result.stderr)
	}
	var envelope struct {
		Success   bool            `json:"success"`
		ErrorCode string          `json:"error_code"`
		Error     json.RawMessage `json:"error"`
	}
	decoder := json.NewDecoder(bytes.NewReader(result.stdout))
	if err := decoder.Decode(&envelope); err != nil {
		t.Fatalf("decode cancellation JSON: %v raw=%s", err, result.stdout)
	}
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		t.Fatalf("cancellation emitted more than one JSON document: err=%v trailing=%v raw=%s", err, trailing, result.stdout)
	}
	code := envelope.ErrorCode
	if len(envelope.Error) > 0 && envelope.Error[0] == '{' {
		var nested struct {
			Code string `json:"code"`
		}
		if err := json.Unmarshal(envelope.Error, &nested); err != nil {
			t.Fatalf("decode cancellation error object: %v raw=%s", err, envelope.Error)
		}
		if nested.Code != "" {
			code = nested.Code
		}
	}
	if envelope.Success || code != "TIMEOUT" {
		t.Fatalf("cancellation envelope success=%v code=%q raw=%s", envelope.Success, code, result.stdout)
	}
}

func atomicAssignmentStaleObservationFailure(result atomicAssignmentProcessResult) bool {
	if result.exitCode == 0 {
		return false
	}
	var envelope atomicAssignmentAutoEnvelope
	if err := json.Unmarshal(bytes.TrimSpace(result.stdout), &envelope); err == nil &&
		envelope.Error != nil && envelope.Error.Code == "ASSIGN_ERROR" {
		return atomicAssignmentStaleObservationMessage(envelope.Error.Message)
	}
	return atomicAssignmentStaleObservationMessage(string(result.stdout) + "\n" + string(result.stderr))
}

func atomicAssignmentStaleObservationMessage(message string) bool {
	const prefix = "assignment observation for pane "
	const suffix = " is stale"
	for _, line := range strings.Split(message, "\n") {
		line = strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(line), "Error:"))
		if !strings.HasPrefix(line, prefix) || !strings.HasSuffix(line, suffix) {
			continue
		}
		pane := strings.TrimSuffix(strings.TrimPrefix(line, prefix), suffix)
		if strings.TrimSpace(pane) != "" {
			return true
		}
	}
	return false
}

func waitForAtomicAssignmentSignal(t *testing.T, signal <-chan struct{}, description string) {
	t.Helper()
	select {
	case <-signal:
	case <-time.After(15 * time.Second):
		t.Fatalf("timed out waiting for %s", description)
	}
}

func (f *atomicAssignmentCLIFixture) armTMUXCaptureDelay(t *testing.T, delay time.Duration) (string, string) {
	t.Helper()
	if delay <= 0 {
		t.Fatalf("tmux capture delay=%s, want positive", delay)
	}
	wrapperDir := t.TempDir()
	reached := filepath.Join(wrapperDir, "capture-reached")
	wrapper := strings.Join([]string{
		"#!/bin/sh",
		"real_tmux=" + tmux.ShellQuote(f.tmuxPath),
		"reached=" + tmux.ShellQuote(reached),
		fmt.Sprintf("delay_seconds=%.3f", delay.Seconds()),
		`for arg in "$@"; do`,
		`  if [ "$arg" = "capture-pane" ]; then`,
		`    printf 'reached\n' > "$reached"`,
		`    sleep "$delay_seconds"`,
		`    break`,
		`  fi`,
		`done`,
		`exec "$real_tmux" "$@"`,
		"",
	}, "\n")
	wrapperPath := filepath.Join(wrapperDir, "tmux")
	// ubs:ignore -- wrapperPath is confined to the directory returned by t.TempDir.
	writeErr := os.WriteFile(wrapperPath, []byte(wrapper), 0o700)
	if writeErr != nil {
		t.Fatalf("write tmux delay wrapper: %v", writeErr)
	}
	return wrapperPath, reached
}

func (f *atomicAssignmentCLIFixture) armBRInvocationTrace(t *testing.T) (string, string) {
	t.Helper()
	wrapperDir := t.TempDir()
	tracePath := filepath.Join(wrapperDir, "invocations")
	wrapper := strings.Join([]string{
		"#!/bin/sh",
		"real_br=" + tmux.ShellQuote(f.brPath),
		"trace=" + tmux.ShellQuote(tracePath),
		`printf '%s\n' "$*" >> "$trace"`,
		`exec "$real_br" "$@"`,
		"",
	}, "\n")
	wrapperPath := filepath.Join(wrapperDir, "br")
	// ubs:ignore -- wrapperPath is confined to the directory returned by t.TempDir.
	writeErr := os.WriteFile(wrapperPath, []byte(wrapper), 0o700)
	if writeErr != nil {
		t.Fatalf("write br trace wrapper: %v", writeErr)
	}
	return wrapperDir + string(os.PathListSeparator) + atomicAssignmentEnvValue(f.env, "PATH"), tracePath
}

func (f *atomicAssignmentCLIFixture) armTMUXPostActuationFailure(t *testing.T) (string, string) {
	t.Helper()
	return f.armNthTMUXCommandFault(t, "ambiguous-delivery", "Enter", f.panes[0].ID, 1, true)
}

func (f *atomicAssignmentCLIFixture) armNthTMUXCommandBlock(t *testing.T, name, command string, occurrence int) (string, string) {
	t.Helper()
	return f.armNthTMUXCommandFault(t, name, command, "", occurrence, false)
}

func (f *atomicAssignmentCLIFixture) armNthTMUXCommandFault(
	t *testing.T,
	name, command, requiredArg string,
	occurrence int,
	failAfterActuation bool,
) (string, string) {
	t.Helper()
	if occurrence < 1 {
		t.Fatalf("tmux block occurrence=%d, want positive", occurrence)
	}
	wrapperDir := filepath.Join(f.root, "tmux-cancel-bin-"+name)
	if err := os.MkdirAll(wrapperDir, 0o700); err != nil {
		t.Fatalf("create tmux cancellation wrapper directory: %v", err)
	}
	counter := filepath.Join(wrapperDir, "count")
	reached := filepath.Join(wrapperDir, "reached")
	wrapper := strings.Join([]string{
		"#!/bin/sh",
		"real_tmux=" + tmux.ShellQuote(f.tmuxPath),
		"counter=" + tmux.ShellQuote(counter),
		"reached=" + tmux.ShellQuote(reached),
		"wanted_command=" + tmux.ShellQuote(command),
		"wanted_arg=" + tmux.ShellQuote(requiredArg),
		fmt.Sprintf("wanted_occurrence=%d", occurrence),
		fmt.Sprintf("fail_after_actuation=%t", failAfterActuation),
		"matched=0",
		`has_wanted_arg=0; if [ -z "$wanted_arg" ]; then has_wanted_arg=1; fi`,
		`for arg in "$@"; do`,
		`  if [ "$arg" = "$wanted_command" ]; then matched=1; fi`,
		`  if [ "$arg" = "$wanted_arg" ]; then has_wanted_arg=1; fi`,
		"done",
		`if [ "$matched" -eq 1 ] && [ "$has_wanted_arg" -eq 1 ]; then`,
		"  count=0",
		`  if [ -f "$counter" ]; then IFS= read -r count < "$counter"; fi`,
		`  case "$count" in ''|*[!0-9]*) count=0 ;; esac`,
		"  count=$((count + 1))",
		`  printf '%s\n' "$count" > "$counter"`,
		`  if [ "$count" -eq "$wanted_occurrence" ]; then`,
		`    if [ "$fail_after_actuation" = true ]; then "$real_tmux" "$@" || exit $?; fi`,
		`    printf 'reached\n' > "$reached"`,
		`    if [ "$fail_after_actuation" = true ]; then printf 'injected tmux failure after actuation\n' >&2; exit 91; fi`,
		"    while :; do :; done",
		"  fi",
		"fi",
		`exec "$real_tmux" "$@"`,
		"",
	}, "\n")
	if err := os.WriteFile(filepath.Join(wrapperDir, "tmux"), []byte(wrapper), 0o700); err != nil {
		t.Fatalf("write tmux cancellation wrapper: %v", err)
	}
	return filepath.Join(wrapperDir, "tmux"), reached
}

func waitForAtomicAssignmentPath(t *testing.T, path string, pid int) {
	t.Helper()
	deadline := time.Now().Add(15 * time.Second)
	for {
		if _, err := os.Stat(path); err == nil {
			return
		} else if !os.IsNotExist(err) {
			t.Fatalf("inspect cancellation sentinel %s: %v", path, err)
		}
		// Liveness probe: signal 0 works on every Unix, unlike /proc which
		// does not exist on macOS (stat("/proc/<pid>") there reported every
		// process as exited and failed these subtests unconditionally). The
		// probe is best-effort: an un-reaped child zombie still counts as
		// alive, in which case the deadline below reports the failure.
		if proc, findErr := os.FindProcess(pid); findErr == nil {
			if sigErr := proc.Signal(syscall.Signal(0)); errors.Is(sigErr, os.ErrProcessDone) {
				t.Fatalf("ntm process %d exited before reaching cancellation sentinel %s", pid, path)
			}
		}
		if time.Now().After(deadline) {
			t.Fatalf("ntm process %d did not reach cancellation sentinel %s", pid, path)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func (f *atomicAssignmentCLIFixture) runNTMInDir(t *testing.T, dir string, env map[string]string, args ...string) atomicAssignmentProcessResult {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	cmd := exec.CommandContext(ctx, f.ntmPath, args...)
	cmd.Dir = dir
	cmd.Env = atomicAssignmentMergeEnv(f.env, env)
	var stdout bytes.Buffer
	var stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	group, groupErr := testutil.NewProcessGroupForTest(ctx, cmd)
	if groupErr != nil {
		t.Fatalf("create owned process group for ntm %q: %v", args, groupErr)
	}
	cmd.Cancel = func() error {
		return group.Signal(os.Kill)
	}
	cmd.WaitDelay = 10 * time.Second
	commandErr := cmd.Run()
	closeErr := group.Close()
	if ctx.Err() == context.DeadlineExceeded {
		t.Fatalf("ntm command timed out: %q", args)
	}
	exitCode := 0
	if closeErr != nil {
		t.Fatalf("close owned process group for ntm %q: %v", args, closeErr)
	}
	if commandErr != nil {
		var exitErr *exec.ExitError
		if errors.As(commandErr, &exitErr) {
			exitCode = exitErr.ExitCode()
		} else {
			t.Fatalf("run ntm %q: %v", args, commandErr)
		}
	}
	t.Logf("[E2E-ATOMIC] exit=%d args=%q stdout=%s stderr=%s", exitCode, args, truncateString(stdout.String(), 500), truncateString(stderr.String(), 500))
	return atomicAssignmentProcessResult{stdout: stdout.Bytes(), stderr: stderr.Bytes(), exitCode: exitCode}
}

func (f *atomicAssignmentCLIFixture) runNTMConcurrent(t *testing.T, count int, env map[string]string, args ...string) []atomicAssignmentProcessResult {
	t.Helper()
	return f.runNTMConcurrentAfterStart(t, count, env, nil, args...)
}

func (f *atomicAssignmentCLIFixture) runNTMConcurrentAfterStart(t *testing.T, count int, env map[string]string, afterStart func(), args ...string) []atomicAssignmentProcessResult {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()

	type runningProcess struct {
		cmd         *exec.Cmd
		group       *testutil.ProcessGroupForTest
		stdout      bytes.Buffer
		stderr      bytes.Buffer
		gateReader  *os.File
		gateWriter  *os.File
		readyReader *os.File
		readyWriter *os.File
		waited      bool
	}
	processes := make([]runningProcess, count)
	t.Cleanup(func() {
		for i := range processes {
			process := &processes[i]
			if process.cmd != nil && process.cmd.Process != nil && !process.waited {
				if process.group != nil {
					_ = process.group.Signal(os.Kill)
				}
				_ = process.cmd.Process.Kill()
				_ = process.cmd.Wait()
				process.waited = true
			}
			if process.group != nil {
				if closeErr := process.group.Close(); closeErr != nil {
					t.Errorf("close owned process group for contender %d: %v", i, closeErr)
				}
			}
			for _, file := range []*os.File{process.gateReader, process.gateWriter, process.readyReader, process.readyWriter} {
				if file != nil {
					_ = file.Close()
				}
			}
		}
	})
	for i := range processes {
		gateReader, gateWriter, err := os.Pipe()
		if err != nil {
			t.Fatalf("create contender %d gate: %v", i, err)
		}
		readyReader, readyWriter, err := os.Pipe()
		if err != nil {
			_ = gateReader.Close()
			_ = gateWriter.Close()
			t.Fatalf("create contender %d readiness pipe: %v", i, err)
		}
		processes[i].gateReader = gateReader
		processes[i].gateWriter = gateWriter
		processes[i].readyReader = readyReader
		processes[i].readyWriter = readyWriter
		barrierArgs := []string{"-c", `printf x >&4; IFS= read -r _ <&3; exec "$@"`, "ntm-e2e-barrier", f.ntmPath}
		barrierArgs = append(barrierArgs, args...)
		cmd := exec.CommandContext(ctx, "/bin/sh", barrierArgs...)
		cmd.Dir = f.projectDir
		cmd.Env = atomicAssignmentMergeEnv(f.env, env)
		cmd.ExtraFiles = []*os.File{gateReader, readyWriter}
		cmd.Stdout = &processes[i].stdout
		cmd.Stderr = &processes[i].stderr
		group, groupErr := testutil.NewProcessGroupForTest(ctx, cmd)
		if groupErr != nil {
			t.Fatalf("create owned process group for contender %d: %v", i, groupErr)
		}
		cmd.Cancel = func() error {
			return group.Signal(syscall.SIGTERM)
		}
		cmd.WaitDelay = 10 * time.Second
		processes[i].cmd = cmd
		processes[i].group = group
	}
	for i := range processes {
		if err := processes[i].cmd.Start(); err != nil {
			t.Fatalf("start ntm contender %d %q: %v", i, args, err)
		}
		_ = processes[i].gateReader.Close()
		_ = processes[i].readyWriter.Close()
	}
	for i := range processes {
		if err := processes[i].readyReader.SetReadDeadline(time.Now().Add(45 * time.Second)); err != nil {
			t.Fatalf("set contender %d readiness deadline: %v", i, err)
		}
		var ready [1]byte
		if _, err := io.ReadFull(processes[i].readyReader, ready[:]); err != nil {
			t.Fatalf("wait for contender %d at start barrier: %v", i, err)
		}
		_ = processes[i].readyReader.Close()
	}
	for i := range processes {
		if _, err := processes[i].gateWriter.Write([]byte("go\n")); err != nil {
			t.Fatalf("release contender %d start barrier: %v", i, err)
		}
		_ = processes[i].gateWriter.Close()
	}
	if afterStart != nil {
		afterStart()
	}

	results := make([]atomicAssignmentProcessResult, count)
	for i := range processes {
		commandErr := processes[i].cmd.Wait()
		closeErr := processes[i].group.Close()
		processes[i].waited = true
		if ctx.Err() == context.DeadlineExceeded {
			t.Fatalf("concurrent ntm commands timed out: %q", args)
		}
		exitCode := 0
		if closeErr != nil {
			t.Fatalf("close owned process group for ntm contender %d: %v", i, closeErr)
		}
		if commandErr != nil {
			var exitErr *exec.ExitError
			if errors.As(commandErr, &exitErr) {
				exitCode = exitErr.ExitCode()
			} else {
				t.Fatalf("wait for ntm contender %d %q: %v", i, args, commandErr)
			}
		}
		results[i] = atomicAssignmentProcessResult{
			stdout:   append([]byte(nil), processes[i].stdout.Bytes()...),
			stderr:   append([]byte(nil), processes[i].stderr.Bytes()...),
			exitCode: exitCode,
		}
		t.Logf("[E2E-ATOMIC] contender=%d exit=%d args=%q stdout=%s stderr=%s", i, exitCode, args,
			truncateString(processes[i].stdout.String(), 500), truncateString(processes[i].stderr.String(), 500))
	}
	return results
}

func (f *atomicAssignmentCLIFixture) mustBR(t *testing.T, args ...string) []byte {
	t.Helper()
	return f.mustBRAt(t, f.projectDir, args...)
}

func (f *atomicAssignmentCLIFixture) mustBRAt(t *testing.T, dir string, args ...string) []byte {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	cmd := exec.CommandContext(ctx, f.brPath, args...)
	cmd.Dir = dir
	cmd.Env = append([]string(nil), f.env...)
	output, err := cmd.CombinedOutput()
	if ctx.Err() == context.DeadlineExceeded {
		t.Fatalf("br command timed out: %q", args)
	}
	if err != nil {
		t.Fatalf("br %q: %v output=%s", args, err, output)
	}
	return output
}

func (f *atomicAssignmentCLIFixture) mustTMUX(t *testing.T, args ...string) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), defaultTmuxSetupTimeout)
	defer cancel()
	if err := f.runTMUX(ctx, args...); err != nil {
		t.Fatalf("tmux %s: %v", strings.Join(args, " "), err)
	}
}

func (f *atomicAssignmentCLIFixture) runTMUX(ctx context.Context, args ...string) error {
	cmd := exec.CommandContext(ctx, f.tmuxPath, args...)
	cmd.Env = append([]string(nil), f.env...)
	output, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("%w: %s", err, strings.TrimSpace(string(output)))
	}
	return nil
}

func (f *atomicAssignmentCLIFixture) tmuxOutput(ctx context.Context, args ...string) ([]byte, error) {
	cmd := exec.CommandContext(ctx, f.tmuxPath, args...)
	cmd.Env = append([]string(nil), f.env...)
	output, err := cmd.CombinedOutput()
	if err != nil {
		return nil, fmt.Errorf("%w: %s", err, strings.TrimSpace(string(output)))
	}
	return output, nil
}

func atomicAssignmentAnyIntSlice(value any) ([]int, bool) {
	values, ok := value.([]any)
	if !ok {
		return nil, false
	}
	result := make([]int, 0, len(values))
	for _, value := range values {
		number, ok := value.(float64)
		if !ok || number != float64(int(number)) {
			return nil, false
		}
		result = append(result, int(number))
	}
	return result, true
}

func atomicAssignmentIsolatedEnv(overrides map[string]string) []string {
	replaced := map[string]struct{}{
		"HOME": {}, "XDG_CONFIG_HOME": {}, "XDG_DATA_HOME": {}, "XDG_STATE_HOME": {}, "XDG_CACHE_HOME": {}, "PWD": {}, "OLDPWD": {},
		"GIT_DIR": {}, "GIT_WORK_TREE": {}, "BR_DB": {}, "BD_DB": {}, "BEADS_DB": {}, "AGENT_NAME": {},
		"TMUX": {}, "TMUX_PANE": {}, "TMUX_TMPDIR": {},
		"NTM_CONFIG": {}, "NTM_OUTPUT_FORMAT": {}, "NTM_ROBOT_FORMAT": {}, "NTM_TEST_ASSIGNMENT_SAVE_FAIL_AFTER_BACKUP": {}, "NTM_TEST_COMPLETION_ACK_FAIL_ONCE": {}, "NTM_TEST_COMPLETION_LEASE_DURATION": {}, "NTM_TEST_COMPLETION_HANDLER_DELAY": {}, "TOON_DEFAULT_FORMAT": {},
		"AGENT_MAIL_URL": {}, "AGENT_MAIL_TOKEN": {},
		"HTTP_PROXY": {}, "HTTPS_PROXY": {}, "ALL_PROXY": {}, "NO_PROXY": {},
	}
	for key := range overrides {
		replaced[key] = struct{}{}
	}
	result := make([]string, 0, len(os.Environ())+len(overrides))
	for _, entry := range os.Environ() {
		key, _, _ := strings.Cut(entry, "=")
		if _, skip := replaced[key]; !skip {
			result = append(result, entry)
		}
	}
	for key, value := range overrides {
		result = append(result, key+"="+value)
	}
	sort.Strings(result)
	return result
}

func atomicAssignmentMergeEnv(base []string, overrides map[string]string) []string {
	if len(overrides) == 0 {
		return append([]string(nil), base...)
	}
	values := make(map[string]string, len(base)+len(overrides))
	for _, entry := range base {
		key, value, _ := strings.Cut(entry, "=")
		values[key] = value
	}
	for key, value := range overrides {
		values[key] = value
	}
	result := make([]string, 0, len(values))
	for key, value := range values {
		result = append(result, key+"="+value)
	}
	sort.Strings(result)
	return result
}

func atomicAssignmentEnvValue(env []string, name string) string {
	for _, entry := range env {
		key, value, _ := strings.Cut(entry, "=")
		if key == name {
			return value
		}
	}
	return ""
}
