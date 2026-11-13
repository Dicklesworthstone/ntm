package pipeline

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"math/rand"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Dicklesworthstone/ntm/internal/state"
	"github.com/Dicklesworthstone/ntm/internal/tmux"
)

// TestPipelineCrashRecoveryModelCheck is a bounded state-machine & fault-injection
// model checker (Alien Graveyard §6.12 + §0.21, Bead bd-rc-track-e-uplift-xkt68.4)
// executing 500 deterministic randomized DAG + adversarial fault schedules across:
//   - Foreach loops (intra-loop output wiring + dynamic runtime step IDs)
//   - Branch routing (selected vs unselected branch subgraphs + on_success hooks)
//   - Agent Mail file_reservation_paths / file_reservation_release leases
//   - OnSuccess & OnFailure recovery hooks
//   - Multi-pass crash + Resume() cycles, mid-step/between-hook lease revocations,
//     and tampered step/parent receipt hashes.
//
// Invariants checked mechanically on every schedule:
//   - I1 (Exact-Once Consumed Side Effects): Across any number of crash + Resume()
//     cycles until completion, every committed step and on_success hook executes
//     at most once (and exactly once on the selected completion path); unselected
//     branches execute 0 times.
//   - I2 (Fail-Closed Lease Safety): Zero step or hook bodies execute while any
//     declared non-released file reservation is expired or revoked.
//   - I3 (Monotonic Integrity Failure): Corrupt receipt/artifact integrity errors
//     (StepHash, PromptHash, PanePID, ResolutionBoundaryHash, parent CommandExecution
//     identity) are NEVER cleared or masked into StatusCompleted by Resume() or
//     on_failure fallback handlers.
func TestPipelineCrashRecoveryModelCheck(t *testing.T) {
	totalSchedules := 500
	if testing.Short() {
		totalSchedules = 100
	}

	server, fixture := newReservationJournalFixture(t, "")
	t.Cleanup(server.Close)
	t.Setenv("AGENT_MAIL_URL", server.URL+"/")
	t.Setenv("AGENT_MAIL_TOKEN", "")

	baseDir := t.TempDir()

	idemStore, err := state.Open(filepath.Join(baseDir, "modelcheck_state.db"))
	if err != nil {
		t.Fatalf("state.Open for I4 modelcheck: %v", err)
	}
	t.Cleanup(func() { _ = idemStore.Close() })
	if err := idemStore.Migrate(); err != nil {
		t.Fatalf("idemStore.Migrate: %v", err)
	}

	var (
		i1Schedules int
		i2Schedules int
		i3Schedules int
		i4Schedules int
	)

	for seed := int64(1); seed <= int64(totalSchedules); seed++ {
		rng := rand.New(rand.NewSource(seed))
		mode := rng.Intn(10)
		switch {
		case mode < 4:
			// 40% (~200 schedules): Multi-crash + multi-Resume() over randomized
			// 3..8-step DAGs combining Foreach, Branch, FileReservation, OnSuccess,
			// plus shell command side effects on every 5th seed -> verifies I1.
			i1Schedules++
			runModelCheckCrashResumeI1(t, rng, seed, baseDir, fixture)
		case mode < 7:
			// 30% (~150 schedules): External Agent Mail lease revocation mid-DAG
			// or between partial FileReservationRelease(globA) and OnSuccess hook
			// requiring globB -> verifies I2 (and I1 prefix at-most-once).
			i2Schedules++
			runModelCheckLeaseRevocationI2(t, rng, seed, baseDir, fixture)
		case mode < 9:
			// 20% (~100 schedules): Tampered AgentDelivery / CommandExecution
			// receipt & parent identity hashes (including OnFailure fallback with
			// suppress_failure=true) -> verifies I3 monotonic integrity failure.
			i3Schedules++
			runModelCheckTamperedReceiptsI3(t, rng, seed, baseDir)
		default:
			// 10% (~50 schedules): Concurrent duplicate --op-id retries with
			// identical vs mutated payload bindings -> verifies I4 idempotency
			// determinism & exact-once dispatch ownership.
			i4Schedules++
			runModelCheckIdempotencyDeterminismI4(t, rng, seed, idemStore)
		}
		server.CloseClientConnections()
	}

	t.Logf("modelcheck summary: total_schedules=%d i1_crash_resume=%d i2_lease_revocation=%d i3_tampered_receipts=%d i4_idempotency=%d",
		totalSchedules, i1Schedules, i2Schedules, i3Schedules, i4Schedules)
}

func resetSharedMailFixture(fixture *reservationJournalFixture) {
	fixture.mu.Lock()
	fixture.recorded = nil
	fixture.failInbox = false
	fixture.failReadback = false
	fixture.cancelInbox = nil
	fixture.renewMode = ""
	fixture.renewed = nil
	fixture.renewOnce = sync.Once{}
	fixture.inboxWait = nil
	fixture.inboxAction = nil
	fixture.beforeInbox = nil
	fixture.releaseWait = nil
	fixture.releaseStart = nil
	fixture.releaseOnce = sync.Once{}
	fixture.releaseAction = nil
	fixture.mu.Unlock()

	fixture.ledger.mu.Lock()
	fixture.ledger.rows = nil
	fixture.ledger.mu.Unlock()
}

func extractEffectTokens(pastes []MockTmuxPaste) map[string]int {
	counts := make(map[string]int, len(pastes))
	for _, p := range pastes {
		const prefix = "EFFECT:"
		idx := strings.Index(p.Content, prefix)
		if idx < 0 {
			continue
		}
		rest := p.Content[idx+len(prefix):]
		if end := strings.IndexAny(rest, " \n\r\t"); end >= 0 {
			rest = rest[:end]
		}
		if rest != "" {
			counts[rest]++
		}
	}
	return counts
}

func runModelCheckCrashResumeI1(
	t *testing.T,
	rng *rand.Rand,
	seed int64,
	baseDir string,
	fixture *reservationJournalFixture,
) {
	t.Helper()
	resetSharedMailFixture(fixture)

	projDir := filepath.Join(baseDir, fmt.Sprintf("i1-seed-%d", seed))
	if err := os.MkdirAll(projDir, 0o700); err != nil {
		t.Fatalf("[seed=%d] mkdir: %v", seed, err)
	}

	useLease := rng.Intn(2) == 1
	useForeach := rng.Intn(2) == 1
	useBranch := rng.Intn(2) == 1
	useHook := rng.Intn(2) == 1
	useShellStep := seed%10 == 0 // real /bin/sh command step every 10th seed
	extraLinear := 1 + rng.Intn(3)

	var (
		steps            []Step
		expectedSelected []string
		forbiddenBranch  []string
		prevID           string
	)

	if useLease {
		steps = append(steps, Step{
			ID: "reserve_lease",
			FileReservationPaths: &FileReservationPathsStep{
				ProjectKey: "/data/projects/ntm",
				AgentName:  "ModelAgent",
				Paths:      StringOrList{"internal/a.go", "internal/b.go"},
				TTLSeconds: 3600,
				Exclusive:  true,
				Reason:     fmt.Sprintf("seed-%d", seed),
			},
		})
		prevID = "reserve_lease"
	}

	for i := 0; i < extraLinear; i++ {
		stepID := fmt.Sprintf("lin_%d", i)
		token := fmt.Sprintf("s%d_%s", seed, stepID)
		step := Step{
			ID:     stepID,
			Pane:   PaneSpec{Index: 1},
			Prompt: "EFFECT:" + token,
			Wait:   WaitNone,
		}
		if prevID != "" {
			step.DependsOn = []string{prevID}
		}
		expectedSelected = append(expectedSelected, token)
		if useHook && i == 0 {
			hookToken := token + "_hook"
			step.OnSuccess = []Step{{
				ID:     stepID + "_on_success",
				Pane:   PaneSpec{Index: 1},
				Prompt: "EFFECT:" + hookToken,
				Wait:   WaitNone,
			}}
			expectedSelected = append(expectedSelected, hookToken)
		}
		steps = append(steps, step)
		prevID = stepID
	}

	if useForeach {
		items := []string{"alpha", "beta"}
		feStep := Step{
			ID: "loop_stage",
			Foreach: &ForeachConfig{
				Items: `["alpha","beta"]`,
				Steps: []Step{
					{
						ID:     "iter_work",
						Pane:   PaneSpec{Index: 1},
						Prompt: fmt.Sprintf("EFFECT:s%d_iter_${loop.item}", seed),
						Wait:   WaitNone,
					},
				},
			},
		}
		if prevID != "" {
			feStep.DependsOn = []string{prevID}
		}
		for _, item := range items {
			expectedSelected = append(expectedSelected, fmt.Sprintf("s%d_iter_%s", seed, item))
		}
		steps = append(steps, feStep)
		prevID = "loop_stage"
	}

	if useBranch {
		pickLeft := rng.Intn(2) == 0
		chosenKey := "right"
		otherKey := "left"
		if pickLeft {
			chosenKey = "left"
			otherKey = "right"
		}
		chosenToken := fmt.Sprintf("s%d_branch_%s", seed, chosenKey)
		otherToken := fmt.Sprintf("s%d_branch_%s", seed, otherKey)
		expectedSelected = append(expectedSelected, chosenToken)
		forbiddenBranch = append(forbiddenBranch, otherToken)

		brStep := Step{
			ID:     "branch_stage",
			Branch: chosenKey,
			Branches: map[string]interface{}{
				chosenKey: []Step{{
					ID:     "br_" + chosenKey,
					Pane:   PaneSpec{Index: 1},
					Prompt: "EFFECT:" + chosenToken,
					Wait:   WaitNone,
				}},
				otherKey: []Step{{
					ID:     "br_" + otherKey,
					Pane:   PaneSpec{Index: 1},
					Prompt: "EFFECT:" + otherToken,
					Wait:   WaitNone,
				}},
			},
		}
		if prevID != "" {
			brStep.DependsOn = []string{prevID}
		}
		steps = append(steps, brStep)
		prevID = "branch_stage"
	}

	shellEffectFile := filepath.Join(projDir, "shell_effects.log")
	if useShellStep {
		cmdStep := Step{
			ID:      "shell_commit",
			Command: fmt.Sprintf("printf 'shell-committed-%d\\n' >> %q", seed, shellEffectFile),
		}
		if prevID != "" {
			cmdStep.DependsOn = []string{prevID}
		}
		steps = append(steps, cmdStep)
		prevID = "shell_commit"
	}

	tailToken := fmt.Sprintf("s%d_tail", seed)
	tailStep := Step{
		ID:     "tail_sink",
		Pane:   PaneSpec{Index: 1},
		Prompt: "EFFECT:" + tailToken,
		Wait:   WaitNone,
	}
	if prevID != "" {
		tailStep.DependsOn = []string{prevID}
	}
	expectedSelected = append(expectedSelected, tailToken)
	steps = append(steps, tailStep)
	prevID = "tail_sink"

	if useLease {
		rel := Step{
			ID:        "release_lease",
			DependsOn: []string{prevID},
			FileReservationRelease: &FileReservationReleaseStep{
				ProjectKey: "/data/projects/ntm",
				AgentName:  "ModelAgent",
				Paths:      StringOrList{"internal/a.go", "internal/b.go"},
			},
		}
		steps = append(steps, rel)
	}

	workflow := &Workflow{
		SchemaVersion: SchemaVersion,
		Name:          fmt.Sprintf("modelcheck-i1-%d", seed),
		Settings:      DefaultWorkflowSettings(),
		Steps:         steps,
	}
	frozen, wfPath, err := SnapshotWorkflow(context.Background(), projDir, workflow)
	if err != nil {
		t.Fatalf("[seed=%d] SnapshotWorkflow: %v", seed, err)
	}

	cfg := DefaultExecutorConfig("mc-session")
	cfg.ProjectDir = projDir
	cfg.RunID = fmt.Sprintf("mc-run-%d", seed)
	cfg.WorkflowFile = wfPath
	cfg.DefaultTimeout = 5 * time.Second
	cfg.GlobalTimeout = 15 * time.Second

	client := NewMockTmuxClient(tmux.Pane{
		ID:       "%17",
		PID:      4242,
		Index:    1,
		NTMIndex: 1,
		Type:     tmux.AgentClaude,
		Command:  "claude",
		Width:    120,
	})
	t.Cleanup(client.Reset)
	if err := client.SetPaneOutput("%17", "READY\n"); err != nil {
		t.Fatalf("[seed=%d] SetPaneOutput: %v", seed, err)
	}

	// Alternate between between-step crashes (triggered on step_complete progress
	// events) and mid-submission crashes (triggered inside SetSubmissionVerifier,
	// leaving an uncertain sending receipt that ordinary Resume() must fence
	// until attested via acceptAgentDeliveryContext).
	midPasteCrash := rng.Intn(3) == 0
	totalPastes := len(expectedSelected)
	cut1 := 1 + rng.Intn(max(1, totalPastes-1))
	cut2 := 0
	if totalPastes >= 4 && rng.Intn(2) == 1 && cut1 < totalPastes-1 {
		cut2 = cut1 + 1 + rng.Intn(totalPastes-1-cut1)
	}

	var eventSeq atomic.Int64
	var activeCancelMu sync.Mutex
	var activeCancel context.CancelFunc
	fireCancel := func() {
		activeCancelMu.Lock()
		cancel := activeCancel
		activeCancelMu.Unlock()
		if cancel != nil {
			cancel()
		}
	}

	if midPasteCrash {
		client.SetSubmissionVerifier(func(_, _, _ string) error {
			n := int(eventSeq.Add(1))
			if n == cut1 || (cut2 > 0 && n == cut2) {
				fireCancel()
				return errors.New("simulated crash mid-submission")
			}
			return nil
		})
	}

	makeProgress := func() (chan ProgressEvent, func()) {
		ch := make(chan ProgressEvent, 256)
		done := make(chan struct{})
		go func() {
			defer close(done)
			for ev := range ch {
				if !midPasteCrash && ev.Type == "step_complete" {
					n := int(eventSeq.Add(1))
					if n == cut1 || (cut2 > 0 && n == cut2) {
						fireCancel()
					}
				}
			}
		}()
		return ch, func() {
			close(ch)
			<-done
		}
	}

	runCtx, cancelRun := context.WithCancel(context.Background())
	activeCancelMu.Lock()
	activeCancel = cancelRun
	activeCancelMu.Unlock()

	prog1, stopProg1 := makeProgress()
	exec1 := NewExecutor(cfg)
	exec1.SetTmuxClient(client)
	state, runErr := exec1.Run(runCtx, frozen, nil, prog1)
	cancelRun()
	stopProg1()

	// Resume loop until StatusCompleted (at most 5 passes).
	for attempt := 1; attempt <= 5 && (state == nil || state.Status != StatusCompleted); attempt++ {
		prior, loadErr := LoadState(projDir, cfg.RunID)
		if loadErr != nil {
			t.Fatalf("[seed=%d attempt=%d] LoadState after crash (runErr=%v): %v", seed, attempt, runErr, loadErr)
		}
		// I1 intermediate check: no effect token may ever exceed count 1!
		pastes, _ := client.PasteHistory("%17")
		for tok, cnt := range extractEffectTokens(pastes) {
			if cnt > 1 {
				t.Fatalf("[seed=%d attempt=%d] I1 VIOLATION mid-resume: effect %q executed %d times", seed, attempt, tok, cnt)
			}
		}

		// If any AgentDelivery crashed mid-submission (agentDeliverySending),
		// verify that unaccepted Resume() fails closed (fenced safety), then
		// attest the delivery (which MockTmuxClient already appended to %17)
		// and reload prior state for clean continuation.
		for stepID, rec := range prior.AgentDeliveries {
			if rec.Status != agentDeliverySending {
				continue
			}
			probeExec := NewExecutor(cfg)
			probeExec.SetTmuxClient(client)
			if probeState, probeErr := probeExec.Resume(context.Background(), frozen, prior, nil); probeErr == nil || (probeState != nil && probeState.Status == StatusCompleted) {
				t.Fatalf("[seed=%d attempt=%d] I3 VIOLATION: ordinary Resume() bypassed uncertain sending fence on %q!", seed, attempt, stepID)
			}
			entry, err := agentDeliveryRecoveryEntry(prior, rec)
			if err != nil {
				t.Fatalf("[seed=%d attempt=%d] agentDeliveryRecoveryEntry(%q): %v", seed, attempt, stepID, err)
			}
			accExec := NewExecutor(cfg)
			accExec.SetTmuxClient(client)
			if _, err := accExec.acceptAgentDeliveryContext(context.Background(), stepID, entry.ReceiptToken, "modelcheck attested pane echo"); err != nil {
				t.Fatalf("[seed=%d attempt=%d] acceptAgentDeliveryContext(%q): %v", seed, attempt, stepID, err)
			}
			prior, loadErr = LoadState(projDir, cfg.RunID)
			if loadErr != nil {
				t.Fatalf("[seed=%d attempt=%d] reload after acceptance: %v", seed, attempt, loadErr)
			}
		}

		resCtx, cancelRes := context.WithCancel(context.Background())
		activeCancelMu.Lock()
		activeCancel = cancelRes
		activeCancelMu.Unlock()

		progR, stopProgR := makeProgress()
		resExec := NewExecutor(cfg)
		resExec.SetTmuxClient(client)
		state, runErr = resExec.Resume(resCtx, frozen, prior, progR)
		cancelRes()
		stopProgR()
	}

	if runErr != nil || state == nil || state.Status != StatusCompleted {
		t.Fatalf("[seed=%d] I1/Liveness VIOLATION: pipeline did not converge to completed: state=%+v err=%v", seed, state, runErr)
	}

	pastes, err := client.PasteHistory("%17")
	if err != nil {
		t.Fatalf("[seed=%d] PasteHistory: %v", seed, err)
	}
	gotEffects := extractEffectTokens(pastes)
	for _, wantTok := range expectedSelected {
		if got := gotEffects[wantTok]; got != 1 {
			t.Fatalf("[seed=%d] I1 VIOLATION: selected effect %q executed %d times, want 1 (all=%v)", seed, wantTok, got, gotEffects)
		}
	}
	for _, forbidTok := range forbiddenBranch {
		if got := gotEffects[forbidTok]; got != 0 {
			t.Fatalf("[seed=%d] I1 VIOLATION: unselected branch effect %q executed %d times, want 0", seed, forbidTok, got)
		}
	}
	if useShellStep {
		raw, err := os.ReadFile(shellEffectFile)
		if err != nil {
			t.Fatalf("[seed=%d] read shell_effects.log: %v", seed, err)
		}
		lines := strings.Split(strings.TrimSpace(string(raw)), "\n")
		if len(lines) != 1 || lines[0] != fmt.Sprintf("shell-committed-%d", seed) {
			t.Fatalf("[seed=%d] I1 VIOLATION on shell step: lines=%q, want 1 exact line", seed, lines)
		}
	}
}

func runModelCheckLeaseRevocationI2(
	t *testing.T,
	rng *rand.Rand,
	seed int64,
	baseDir string,
	fixture *reservationJournalFixture,
) {
	t.Helper()
	resetSharedMailFixture(fixture)

	projDir := filepath.Join(baseDir, fmt.Sprintf("i2-seed-%d", seed))
	if err := os.MkdirAll(projDir, 0o700); err != nil {
		t.Fatalf("[seed=%d] mkdir: %v", seed, err)
	}

	client := NewMockTmuxClient(tmux.Pane{
		ID:       "%17",
		PID:      5151,
		Index:    1,
		NTMIndex: 1,
		Type:     tmux.AgentClaude,
		Command:  "claude",
		Width:    120,
	})
	t.Cleanup(client.Reset)
	_ = client.SetPaneOutput("%17", "READY\n")

	variant := rng.Intn(2)
	if variant == 0 {
		// Variant A: Lease revoked between step 1 and protected downstream step 2
		// (either holder stolen by another agent or lease expired).
		stealHolder := rng.Intn(2) == 0
		guardedToken := fmt.Sprintf("s%d_guarded_must_not_run", seed)
		prefixToken := fmt.Sprintf("s%d_prefix_ok", seed)

		workflow := &Workflow{
			SchemaVersion: SchemaVersion,
			Name:          fmt.Sprintf("modelcheck-i2a-%d", seed),
			Settings:      DefaultWorkflowSettings(),
			Steps: []Step{
				{
					ID: "reserve",
					FileReservationPaths: &FileReservationPathsStep{
						ProjectKey: "/data/projects/ntm",
						AgentName:  "TealCrane",
						Paths:      StringOrList{"a.go", "b.go"},
						TTLSeconds: 3600,
						Exclusive:  true,
						Reason:     "i2-check",
					},
				},
				{
					ID:        "step_before_revocation",
					DependsOn: []string{"reserve"},
					Pane:      PaneSpec{Index: 1},
					Prompt:    "EFFECT:" + prefixToken,
					Wait:      WaitNone,
				},
				{
					ID:        "step_after_revocation",
					DependsOn: []string{"step_before_revocation"},
					Pane:      PaneSpec{Index: 1},
					Prompt:    "EFFECT:" + guardedToken,
					Wait:      WaitNone,
				},
			},
		}

		client.SetSubmissionVerifier(func(_, content, _ string) error {
			if strings.Contains(content, prefixToken) {
				fixture.ledger.mu.Lock()
				for _, row := range fixture.ledger.rows {
					if stealHolder {
						row["agent"] = "IntruderAgent"
					} else {
						row["expires_ts"] = time.Now().Add(-10 * time.Second).UTC().Format(time.RFC3339Nano)
					}
				}
				fixture.ledger.mu.Unlock()
			}
			return nil
		})

		cfg := DefaultExecutorConfig("mc-i2")
		cfg.ProjectDir = projDir
		cfg.RunID = fmt.Sprintf("mc-i2a-%d", seed)
		exec := NewExecutor(cfg)
		exec.SetTmuxClient(client)

		state, err := exec.Run(context.Background(), workflow, nil, nil)
		if err == nil || state == nil || state.Status != StatusFailed || !strings.Contains(err.Error(), "reservation protection") {
			t.Fatalf("[seed=%d] I2 VIOLATION: revoked lease did not fail closed: state=%+v err=%v", seed, state, err)
		}
		pastes, _ := client.PasteHistory("%17")
		effects := extractEffectTokens(pastes)
		if effects[guardedToken] != 0 {
			t.Fatalf("[seed=%d] I2 VIOLATION: guarded step executed %d times under revoked lease!", seed, effects[guardedToken])
		}
		if effects[prefixToken] > 1 {
			t.Fatalf("[seed=%d] I1 VIOLATION: prefix step executed %d times", seed, effects[prefixToken])
		}
		return
	}

	// Variant B: Partial release of a.go while b.go is still held, with b.go
	// externally revoked during release — OnSuccess hook MUST NOT execute!
	hookToken := fmt.Sprintf("s%d_hook_after_revoked_remaining_lease", seed)
	fixture.releaseAction = func() {
		fixture.ledger.mu.Lock()
		for _, row := range fixture.ledger.rows {
			if row["path_pattern"] == "b.go" {
				row["agent"] = "OrangeFalcon"
			}
		}
		fixture.ledger.mu.Unlock()
	}

	workflow := &Workflow{
		SchemaVersion: SchemaVersion,
		Name:          fmt.Sprintf("modelcheck-i2b-%d", seed),
		Settings:      DefaultWorkflowSettings(),
		Steps: []Step{
			{
				ID: "reserve_both",
				FileReservationPaths: &FileReservationPathsStep{
					ProjectKey: "/data/projects/ntm",
					AgentName:  "TealCrane",
					Paths:      StringOrList{"a.go", "b.go"},
					TTLSeconds: 3600,
					Exclusive:  true,
					Reason:     "i2-partial-release",
				},
			},
			{
				ID:        "release_first",
				DependsOn: []string{"reserve_both"},
				FileReservationRelease: &FileReservationReleaseStep{
					ProjectKey: "/data/projects/ntm",
					AgentName:  "TealCrane",
					Paths:      StringOrList{"a.go"},
				},
				OnSuccess: []Step{{
					ID:     "hook_requires_b",
					Pane:   PaneSpec{Index: 1},
					Prompt: "EFFECT:" + hookToken,
					Wait:   WaitNone,
				}},
			},
		},
	}

	cfg := DefaultExecutorConfig("mc-i2b")
	cfg.ProjectDir = projDir
	cfg.RunID = fmt.Sprintf("mc-i2b-%d", seed)
	exec := NewExecutor(cfg)
	exec.SetTmuxClient(client)

	state, err := exec.Run(context.Background(), workflow, nil, nil)
	if err == nil || state == nil || state.Status != StatusFailed || !strings.Contains(err.Error(), "reservation protection") {
		t.Fatalf("[seed=%d] I2 VIOLATION (partial release hook): state=%+v err=%v", seed, state, err)
	}
	pastes, _ := client.PasteHistory("%17")
	if got := extractEffectTokens(pastes)[hookToken]; got != 0 {
		t.Fatalf("[seed=%d] I2 VIOLATION: OnSuccess hook executed %d times after remaining lease b.go was revoked", seed, got)
	}
}

func runModelCheckTamperedReceiptsI3(
	t *testing.T,
	rng *rand.Rand,
	seed int64,
	baseDir string,
) {
	t.Helper()
	projDir := filepath.Join(baseDir, fmt.Sprintf("i3-seed-%d", seed))
	if err := os.MkdirAll(projDir, 0o700); err != nil {
		t.Fatalf("[seed=%d] mkdir: %v", seed, err)
	}

	tamperKind := rng.Intn(5)
	if tamperKind == 4 {
		// Parent CommandExecution receipt identity tamper with OnFailure fallback
		// suppress_failure=true: executeOnFailureRecovery MUST NOT mask the
		// parent receipt identity violation into StatusCompleted!
		if err := os.WriteFile(filepath.Join(projDir, "recover.md"), []byte("RECOVER"), 0o600); err != nil {
			t.Fatalf("[seed=%d] write recover.md: %v", seed, err)
		}
		step := Step{
			ID:      "work",
			Command: "exit 7",
			OnFailure: OnFailureSpec{
				Fallback: map[string]interface{}{
					"pane":             1,
					"template":         "recover.md",
					"wait":             string(WaitNone),
					"suppress_failure": true,
				},
			},
		}
		workflow := &Workflow{
			SchemaVersion: SchemaVersion,
			Name:          fmt.Sprintf("mc-i3-parent-%d", seed),
			Settings:      DefaultWorkflowSettings(),
			Steps:         []Step{step},
		}
		now := time.Now().UTC()
		outHash := sha256.Sum256(nil)
		corruptParent := CommandExecutionState{
			StepID:               "work",
			StepHash:             strings.Repeat("f", 64), // mismatched step hash
			Status:               commandExecutionSettled,
			StartedAt:            now,
			SettledAt:            now,
			ResolutionAction:     CommandExecutionResolutionAccept,
			ResolutionToken:      strings.Repeat("0", 64),
			ResolutionReason:     "operator accepted",
			ResolutionExitCode:   7,
			ResolutionOutputHash: hex.EncodeToString(outHash[:]),
			ResolvedAt:           now,
			Result: StepResult{
				StepID:     "work",
				AgentType:  "command",
				Status:     StatusFailed,
				StartedAt:  now,
				FinishedAt: now,
			},
		}
		doneFailureResult := StepResult{
			StepID:     "work.on_failure",
			Status:     StatusCompleted,
			StartedAt:  now,
			FinishedAt: now,
		}
		doneDelivery := AgentDeliveryState{
			StepID:       "work.on_failure",
			Status:       agentDeliveryCompleted,
			StartedAt:    now,
			DeliveredAt:  now,
			CompletedAt:  now,
			PaneID:       "%17",
			PanePID:      9001,
			AgentType:    "claude",
			BeforeOutput: "READY\n",
		}

		cfg := DefaultExecutorConfig("mc-i3-parent")
		cfg.ProjectDir = projDir
		cfg.RunID = fmt.Sprintf("mc-i3p-%d", seed)
		guard := NewExecutor(cfg)
		guard.graph = NewDependencyGraph(workflow)
		guard.state = &ExecutionState{
			RunID:             cfg.RunID,
			Session:           cfg.Session,
			Status:            StatusRunning,
			Steps:             map[string]StepResult{"work.on_failure": doneFailureResult},
			CommandExecutions: map[string]CommandExecutionState{"work": corruptParent},
			AgentDeliveries:   map[string]AgentDeliveryState{"work.on_failure": doneDelivery},
			Variables:         map[string]interface{}{},
		}
		failed := StepResult{
			StepID: "work",
			Status: StatusFailed,
			Error:  &StepError{Type: "recovery", Message: "parent receipt identity mismatch"},
		}
		res := guard.executeOnFailureRecovery(context.Background(), &workflow.Steps[0], workflow, failed)
		if res.Status == StatusCompleted || res.Error == nil {
			t.Fatalf("[seed=%d] I3 VIOLATION: suppress_failure fallback masked corrupt parent receipt identity into %+v", seed, res)
		}
		return
	}

	// Uncertain AgentDelivery created through Executor.Run, accepted by operator,
	// and then tampered on disk before Resume() (PanePID, StepHash, PromptHash,
	// or ResolutionBoundaryHash). Resume() MUST reject and NEVER complete!
	workflow := &Workflow{
		SchemaVersion: SchemaVersion,
		Name:          fmt.Sprintf("mc-i3-agent-%d", seed),
		Settings:      DefaultWorkflowSettings(),
		Steps: []Step{
			{
				ID:      "agent_step",
				Pane:    PaneSpec{Index: 1},
				Prompt:  fmt.Sprintf("EFFECT:s%d_uncertain", seed),
				Wait:    WaitTime,
				Timeout: Duration{Duration: time.Millisecond},
			},
			{
				ID:        "downstream",
				DependsOn: []string{"agent_step"},
				Pane:      PaneSpec{Index: 1},
				Prompt:    fmt.Sprintf("EFFECT:s%d_downstream_must_not_run", seed),
				Wait:      WaitNone,
			},
		},
	}
	frozen, wfPath, err := SnapshotWorkflow(context.Background(), projDir, workflow)
	if err != nil {
		t.Fatalf("[seed=%d] SnapshotWorkflow: %v", seed, err)
	}

	cfg := DefaultExecutorConfig("mc-i3")
	cfg.ProjectDir = projDir
	cfg.RunID = fmt.Sprintf("mc-i3a-%d", seed)
	cfg.WorkflowFile = wfPath
	cfg.DefaultTimeout = time.Second
	cfg.GlobalTimeout = 5 * time.Second

	client := NewMockTmuxClient(tmux.Pane{
		ID:       "%17",
		PID:      7777,
		Index:    1,
		NTMIndex: 1,
		Type:     tmux.AgentClaude,
		Command:  "claude",
		Width:    120,
	})
	t.Cleanup(client.Reset)
	_ = client.SetPaneOutput("%17", "BOUNDARY\n")
	client.SetSubmissionVerifier(func(_, _, _ string) error {
		return errors.New("interrupted submission verification")
	})

	exec1 := NewExecutor(cfg)
	exec1.SetTmuxClient(client)
	initial, runErr := exec1.Run(context.Background(), frozen, nil, nil)
	if runErr == nil || initial == nil || initial.Status != StatusFailed {
		t.Fatalf("[seed=%d] expected uncertain failure, got state=%+v err=%v", seed, initial, runErr)
	}

	loaded, err := LoadState(projDir, cfg.RunID)
	if err != nil {
		t.Fatalf("[seed=%d] LoadState: %v", seed, err)
	}
	rec := loaded.AgentDeliveries["agent_step"]
	_ = client.SetPaneOutput("%17", rec.BeforeOutput+"\nAGENT-REPLY\n")

	entry, err := agentDeliveryRecoveryEntry(loaded, rec)
	if err != nil {
		t.Fatalf("[seed=%d] agentDeliveryRecoveryEntry: %v", seed, err)
	}

	acceptExec := NewExecutor(cfg)
	acceptExec.SetTmuxClient(client)
	if _, err := acceptExec.acceptAgentDeliveryContext(context.Background(), "agent_step", entry.ReceiptToken, "verified reply in pane"); err != nil {
		t.Fatalf("[seed=%d] acceptAgentDeliveryContext: %v", seed, err)
	}

	tampered, err := LoadState(projDir, cfg.RunID)
	if err != nil {
		t.Fatalf("[seed=%d] LoadState post-accept: %v", seed, err)
	}
	tRec := tampered.AgentDeliveries["agent_step"]
	switch tamperKind {
	case 0:
		tRec.PanePID += 99
	case 1:
		tRec.StepHash = strings.Repeat("a", 64)
	case 2:
		tRec.PromptHash = strings.Repeat("b", 64)
	case 3:
		tRec.ResolutionBoundaryHash = strings.Repeat("c", 64)
	}
	tampered.AgentDeliveries["agent_step"] = tRec
	if err := SaveState(projDir, tampered); err != nil {
		t.Fatalf("[seed=%d] SaveState tampered: %v", seed, err)
	}

	client.SetSubmissionVerifier(nil)
	resExec := NewExecutor(cfg)
	resExec.SetTmuxClient(client)
	resState, resErr := resExec.Resume(context.Background(), frozen, tampered, nil)
	if resErr == nil || (resState != nil && resState.Status == StatusCompleted) {
		t.Fatalf("[seed=%d tamper=%d] I3 VIOLATION: Resume() accepted tampered receipt! state=%+v err=%v", seed, tamperKind, resState, resErr)
	}

	pastes, _ := client.PasteHistory("%17")
	downstreamTok := fmt.Sprintf("s%d_downstream_must_not_run", seed)
	if got := extractEffectTokens(pastes)[downstreamTok]; got != 0 {
		t.Fatalf("[seed=%d tamper=%d] I3 VIOLATION: downstream step executed %d times after receipt tamper", seed, tamperKind, got)
	}
}

func runModelCheckIdempotencyDeterminismI4(
	t *testing.T,
	rng *rand.Rand,
	seed int64,
	store *state.Store,
) {
	t.Helper()
	opID := fmt.Sprintf("mc-op-%d", seed)
	session := fmt.Sprintf("mc-idem-sess-%d", seed%4)
	canonicalBind := fmt.Sprintf("bind-canonical-%d", seed)
	canonicalSHA := fmt.Sprintf("sha-canonical-%d", seed)
	outcomeJSON := fmt.Sprintf(`{"success":true,"seed":%d}`, seed)

	workers := 4 + rng.Intn(5) // 4..8 concurrent callers
	mutatedMask := make([]bool, workers)
	// Guarantee Worker 0 uses canonical binding and at least 1 worker uses a mutated binding.
	mutatedMask[0] = false
	mutatedMask[1] = true
	for i := 2; i < workers; i++ {
		mutatedMask[i] = rng.Intn(2) == 1
	}

	// Shuffle arrival order deterministically while preserving Worker 0 vs mutated mix:
	// wait — any caller could win the initial claim if mutated raced ahead, so first
	// let canonical vs concurrent identical callers race, AND concurrently/subsequently
	// fire mutated-payload callers to prove deterministic conflict detection!
	identicalWorkers := 2 + rng.Intn(4)
	var dispatchCount atomic.Int32
	var claimedWinners atomic.Int32
	errCh := make(chan error, identicalWorkers+workers)

	var wg sync.WaitGroup
	startGate := make(chan struct{})
	for w := 0; w < identicalWorkers; w++ {
		wg.Add(1)
		go func(workerIdx int) {
			defer wg.Done()
			<-startGate
			rec, claimed, err := store.ClaimSendOperation(&state.SendOperation{
				OperationID:   opID,
				SessionName:   session,
				BindingHash:   canonicalBind,
				PayloadSHA256: canonicalSHA,
				PayloadBytes:  64,
				Targets:       []string{session + ":1"},
			})
			if err != nil {
				errCh <- fmt.Errorf("worker %d claim: %w", workerIdx, err)
				return
			}
			if rec.BindingHash != canonicalBind {
				errCh <- fmt.Errorf("worker %d saw unexpected binding %q != %q", workerIdx, rec.BindingHash, canonicalBind)
				return
			}
			if claimed {
				claimedWinners.Add(1)
				started, err := store.StartSendOperation(rec)
				if err != nil {
					errCh <- fmt.Errorf("winner %d StartSendOperation: %w", workerIdx, err)
					return
				}
				dispatchCount.Add(1)
				if err := store.CompleteSendOperation(opID, session, started.ClaimToken, outcomeJSON, time.Now().UTC()); err != nil {
					errCh <- fmt.Errorf("winner %d CompleteSendOperation: %w", workerIdx, err)
					return
				}
			}
		}(w)
	}
	close(startGate)
	wg.Wait()
	close(errCh)
	for err := range errCh {
		t.Fatalf("[seed=%d] I4 VIOLATION during concurrent identical claims: %v", seed, err)
	}
	if got := claimedWinners.Load(); got != 1 {
		t.Fatalf("[seed=%d] I4 VIOLATION: claimedWinners=%d, want exactly 1", seed, got)
	}
	if got := dispatchCount.Load(); got != 1 {
		t.Fatalf("[seed=%d] I4 VIOLATION: dispatchCount=%d, want exactly 1", seed, got)
	}

	// Replay phase: fire mixed identical & mutated payload retries concurrently.
	replayErrCh := make(chan error, workers)
	var replayWG sync.WaitGroup
	for w := 0; w < workers; w++ {
		mutated := mutatedMask[w]
		replayWG.Add(1)
		go func(workerIdx int, isMutated bool) {
			defer replayWG.Done()
			bind := canonicalBind
			sha := canonicalSHA
			if isMutated {
				bind = fmt.Sprintf("bind-MUTATED-%d-%d", seed, workerIdx)
				sha = fmt.Sprintf("sha-MUTATED-%d-%d", seed, workerIdx)
			}
			existing, claimedAgain, err := store.ClaimSendOperation(&state.SendOperation{
				OperationID:   opID,
				SessionName:   session,
				BindingHash:   bind,
				PayloadSHA256: sha,
				PayloadBytes:  99,
				Targets:       []string{session + ":2"},
			})
			if err != nil {
				replayErrCh <- fmt.Errorf("replay worker %d: %w", workerIdx, err)
				return
			}
			if claimedAgain {
				replayErrCh <- fmt.Errorf("replay worker %d unexpectedly re-claimed completed op %s", workerIdx, opID)
				return
			}
			if existing.Status != state.SendOperationCompleted || existing.OutcomeJSON != outcomeJSON {
				replayErrCh <- fmt.Errorf("replay worker %d saw corrupted receipt: %+v", workerIdx, existing)
				return
			}
			conflict := existing.BindingHash != bind
			if isMutated && !conflict {
				replayErrCh <- fmt.Errorf("replay worker %d mutated payload was NOT detected as IDEMPOTENCY_CONFLICT", workerIdx)
				return
			}
			if !isMutated && conflict {
				replayErrCh <- fmt.Errorf("replay worker %d identical payload falsely flagged conflict", workerIdx)
				return
			}
		}(w, mutated)
	}
	replayWG.Wait()
	close(replayErrCh)
	for err := range replayErrCh {
		t.Fatalf("[seed=%d] I4 VIOLATION during mixed replay: %v", seed, err)
	}
}
