package cli

// bugs_watch.go — UBS push routing (bd-eujr8).
//
// `ntm bugs watch` periodically runs the same UBS invocation `ntm bugs list`
// uses, fingerprints the findings, diffs them against the previously handled
// set persisted in the runtime store, and routes each NEW finding to the agent
// currently holding the affected file's Agent Mail reservation. Delivery goes
// through the gated dispatch service (composer-ready + submission-verified —
// never raw keystrokes), is rate-limited per pane, and never interrupts a
// working pane.
//
// Reuse notes:
//   - Reservation glob matching reuses the `ntm locks check` matcher
//     (locksCheckPathMatches / locksComparableReservationPath in locks.go),
//     which mirrors the reservation API's own pattern semantics.
//   - Holder identity -> live pane mapping uses the session agent registry
//     (agentmail.LoadBestSessionAgentRegistry + resolvePaneAgentName), the
//     same lookup family the relaunch identity hook restores.
//   - "Coordinator digest" delivery lives inside coordinator.Coordinator and
//     requires starting a full observation loop per call; instead of spinning
//     one up every tick, unreserved-path findings are batched into a single
//     coordinator-digest-style Agent Mail summary broadcast to the project's
//     agents (the same channel coordinator digests use). When Agent Mail is
//     unreachable the findings are logged and recorded as a deduplicated
//     attention item in the runtime store instead.

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/signal"
	"path/filepath"
	"sort"
	"strings"
	"syscall"
	"time"

	"github.com/spf13/cobra"

	"github.com/Dicklesworthstone/ntm/internal/agentmail"
	dispatchsvc "github.com/Dicklesworthstone/ntm/internal/dispatch"
	"github.com/Dicklesworthstone/ntm/internal/robot"
	"github.com/Dicklesworthstone/ntm/internal/scanner"
	"github.com/Dicklesworthstone/ntm/internal/state"
	statuspkg "github.com/Dicklesworthstone/ntm/internal/status"
	"github.com/Dicklesworthstone/ntm/internal/tmux"
)

const (
	// bugsFindingsWatermarkType persists the handled-fingerprint set as a
	// JSON blob in output_watermarks.baseline_hash, scoped per project.
	// Storing the set in an existing watermark row follows the runtime
	// store precedent instead of adding a migration for a one-off table.
	bugsFindingsWatermarkType = "ubs_findings"

	// bugsNudgeWatermarkType records the last bug-nudge timestamp per pane
	// (scope "session|paneID") for cooldown enforcement.
	bugsNudgeWatermarkType = "ubs_nudge"

	// bugsWatchMaxDigestFindings bounds how many findings are embedded in a
	// single digest/attention payload.
	bugsWatchMaxDigestFindings = 20
)

// bugsWatchFinding pairs a scanner finding with its stable fingerprint and
// the project-relative path used for reservation matching.
type bugsWatchFinding struct {
	Finding        scanner.Finding
	Fingerprint    string
	ComparablePath string
}

// bugsFindingFingerprint hashes file:line:category:message into a stable
// fingerprint. filepath.ToSlash keeps it identical across OS path styles.
func bugsFindingFingerprint(f scanner.Finding) string {
	key := fmt.Sprintf("%s:%d:%s:%s", filepath.ToSlash(f.File), f.Line, f.Category, f.Message)
	sum := sha256.Sum256([]byte(key))
	return hex.EncodeToString(sum[:16])
}

// bugsComparableFindingPath maps a finding's file (absolute, or relative to
// scanPath) to the project-relative comparable form the locks matcher uses.
func bugsComparableFindingPath(file, scanPath, projectKey string) string {
	file = strings.TrimSpace(file)
	if file == "" {
		return ""
	}
	if !filepath.IsAbs(file) && scanPath != "" {
		file = filepath.Join(scanPath, file)
	}
	return locksComparableReservationPath(file, projectKey)
}

// bugsWatchTick is the machine-readable per-tick summary.
type bugsWatchTick struct {
	Timestamp     string   `json:"timestamp"`
	Findings      int      `json:"findings"`
	New           int      `json:"new"`
	Nudged        int      `json:"nudged"`
	NudgedPanes   []string `json:"nudged_panes,omitempty"`
	Deferred      int      `json:"deferred"`
	Digested      int      `json:"digested"`
	AgentMailDown bool     `json:"agent_mail_down,omitempty"`
	Warnings      []string `json:"warnings,omitempty"`
}

// bugsWatchDeps are the injectable side-effect ports of the watch engine so
// unit tests can drive ticks without tmux, UBS, or a live Agent Mail server.
type bugsWatchDeps struct {
	scan             func(ctx context.Context) (*scanner.ScanResult, error)
	agentMailUp      func(ctx context.Context) bool
	listReservations func(ctx context.Context) ([]agentmail.FileReservation, error)
	listPanes        func(ctx context.Context) ([]tmux.Pane, error)
	agentForPane     func(pane tmux.Pane) string
	observe          func(ctx context.Context, session string) (statuspkg.SessionObservation, error)
	safeToDispatch   func(pane statuspkg.PaneObservation) bool
	dispatchNudge    func(ctx context.Context, panes []tmux.Pane, target tmux.Pane, message string) error
	publishDigest    func(ctx context.Context, findings []bugsWatchFinding) error
	publishOutage    func(findings []bugsWatchFinding, cause string)
	now              func() time.Time
	logf             func(format string, args ...interface{})
}

type bugsWatchEngine struct {
	session    string
	projectKey string
	scanPath   string
	cooldown   time.Duration
	store      *state.Store
	deps       bugsWatchDeps
}

func (e *bugsWatchEngine) nowTime() time.Time {
	if e.deps.now != nil {
		return e.deps.now()
	}
	return time.Now()
}

// paneSafeToDispatch applies the working-pane gate, defaulting to the same
// fresh-idle classification spawn readiness uses.
func (e *bugsWatchEngine) paneSafeToDispatch(pane statuspkg.PaneObservation) bool {
	if e.deps.safeToDispatch != nil {
		return e.deps.safeToDispatch(pane)
	}
	return spawnPaneObservationSafeToDispatch(pane)
}

func (e *bugsWatchEngine) logf(format string, args ...interface{}) {
	if e.deps.logf != nil {
		e.deps.logf(format, args...)
	}
}

// loadHandledFingerprints reads the persisted handled-fingerprint set.
func (e *bugsWatchEngine) loadHandledFingerprints() (map[string]bool, error) {
	wm, err := e.store.GetWatermark(bugsFindingsWatermarkType, bugsFindingsScope(e.projectKey))
	if err != nil {
		return nil, err
	}
	handled := make(map[string]bool)
	if wm == nil || strings.TrimSpace(wm.BaselineHash) == "" {
		return handled, nil
	}
	var fps []string
	if err := json.Unmarshal([]byte(wm.BaselineHash), &fps); err != nil {
		// A corrupt blob resets the diff baseline rather than wedging the
		// watcher; every current finding is treated as new once.
		return handled, nil
	}
	for _, fp := range fps {
		handled[fp] = true
	}
	return handled, nil
}

func (e *bugsWatchEngine) saveHandledFingerprints(handled map[string]bool) error {
	fps := make([]string, 0, len(handled))
	for fp := range handled {
		fps = append(fps, fp)
	}
	sort.Strings(fps)
	blob, err := json.Marshal(fps)
	if err != nil {
		return err
	}
	now := e.nowTime()
	return e.store.SetWatermark(&state.OutputWatermark{
		WatermarkType: bugsFindingsWatermarkType,
		Scope:         bugsFindingsScope(e.projectKey),
		LastCursor:    int64(len(fps)),
		LastTs:        &now,
		BaselineHash:  string(blob),
		Consumer:      "bugs_watch",
		CreatedAt:     now,
		UpdatedAt:     now,
	})
}

func bugsFindingsScope(projectKey string) string {
	return "project:" + projectKey
}

func bugsNudgeScope(session, paneID string) string {
	return session + "|" + paneID
}

// lastNudgeAt returns the persisted last bug-nudge time for a pane.
func (e *bugsWatchEngine) lastNudgeAt(paneID string) (time.Time, bool, error) {
	wm, err := e.store.GetWatermark(bugsNudgeWatermarkType, bugsNudgeScope(e.session, paneID))
	if err != nil {
		return time.Time{}, false, err
	}
	if wm == nil || wm.LastTs == nil {
		return time.Time{}, false, nil
	}
	return *wm.LastTs, true, nil
}

func (e *bugsWatchEngine) recordNudge(paneID string, at time.Time) error {
	return e.store.SetWatermark(&state.OutputWatermark{
		WatermarkType: bugsNudgeWatermarkType,
		Scope:         bugsNudgeScope(e.session, paneID),
		LastTs:        &at,
		Consumer:      "bugs_watch",
		CreatedAt:     at,
		UpdatedAt:     at,
	})
}

// bugsReservationHolder resolves the agent holding the reservation covering
// path. Exclusive reservations win over shared ones; within a class the
// lowest reservation ID wins for determinism.
func bugsReservationHolder(reservations []agentmail.FileReservation, comparablePath, projectKey string, now time.Time) string {
	if comparablePath == "" {
		return ""
	}
	var exclusive, shared *agentmail.FileReservation
	for i := range reservations {
		r := &reservations[i]
		if !locksReservationActiveAt(*r, now) {
			continue
		}
		pattern := locksComparableReservationPath(r.PathPattern, projectKey)
		if !locksCheckPathMatches(comparablePath, pattern) {
			continue
		}
		if r.Exclusive {
			if exclusive == nil || r.ID < exclusive.ID {
				exclusive = r
			}
		} else if shared == nil || r.ID < shared.ID {
			shared = r
		}
	}
	if exclusive != nil {
		return exclusive.AgentName
	}
	if shared != nil {
		return shared.AgentName
	}
	return ""
}

// routeWorthyFindings selects critical and warning findings (parity with
// `ntm bugs notify`, which never pushes info-level noise at agents) and
// annotates them with fingerprints and comparable paths.
func (e *bugsWatchEngine) routeWorthyFindings(result *scanner.ScanResult) []bugsWatchFinding {
	if result == nil {
		return nil
	}
	findings := make([]bugsWatchFinding, 0, len(result.Findings))
	for _, f := range result.Findings {
		if f.Severity != scanner.SeverityCritical && f.Severity != scanner.SeverityWarning {
			continue
		}
		findings = append(findings, bugsWatchFinding{
			Finding:        f,
			Fingerprint:    bugsFindingFingerprint(f),
			ComparablePath: bugsComparableFindingPath(f.File, e.scanPath, e.projectKey),
		})
	}
	return findings
}

// Tick runs one scan/diff/route cycle. Findings that could not be delivered
// (cooldown, working pane, dispatch failure, Agent Mail outage) stay out of
// the handled set so the next tick retries them; findings that were nudged or
// digested are marked handled so one finding never nudges twice.
func (e *bugsWatchEngine) Tick(ctx context.Context) (bugsWatchTick, error) {
	tick := bugsWatchTick{Timestamp: e.nowTime().UTC().Format(time.RFC3339)}

	result, err := e.deps.scan(ctx)
	if err != nil {
		return tick, fmt.Errorf("scan failed: %w", err)
	}

	candidates := e.routeWorthyFindings(result)
	tick.Findings = len(candidates)

	handled, err := e.loadHandledFingerprints()
	if err != nil {
		return tick, fmt.Errorf("loading finding fingerprints: %w", err)
	}

	current := make(map[string]bool, len(candidates))
	var newFindings []bugsWatchFinding
	for _, f := range candidates {
		if current[f.Fingerprint] {
			continue // same fingerprint reported twice in one scan
		}
		current[f.Fingerprint] = true
		if !handled[f.Fingerprint] {
			newFindings = append(newFindings, f)
		}
	}
	tick.New = len(newFindings)

	// Prune handled fingerprints for findings that no longer exist so the
	// blob tracks the live finding set. A finding that regresses later is
	// treated as new again, which is the desired behavior.
	retained := make(map[string]bool, len(handled))
	for fp := range handled {
		if current[fp] {
			retained[fp] = true
		}
	}

	persist := func() {
		if err := e.saveHandledFingerprints(retained); err != nil {
			tick.Warnings = append(tick.Warnings, fmt.Sprintf("persisting fingerprints: %v", err))
		}
	}

	if len(newFindings) == 0 {
		persist()
		return tick, nil
	}

	// Graceful degradation: Agent Mail down -> log + attention item, no
	// nudges, and nothing marked handled so routing retries when it returns.
	if e.deps.agentMailUp == nil || !e.deps.agentMailUp(ctx) {
		tick.AgentMailDown = true
		if e.deps.publishOutage != nil {
			e.deps.publishOutage(newFindings, "Agent Mail server unavailable")
		}
		persist()
		return tick, nil
	}

	reservations, err := e.deps.listReservations(ctx)
	if err != nil {
		tick.AgentMailDown = true
		if e.deps.publishOutage != nil {
			e.deps.publishOutage(newFindings, fmt.Sprintf("listing reservations: %v", err))
		}
		persist()
		return tick, nil
	}

	panes, err := e.deps.listPanes(ctx)
	if err != nil {
		persist()
		return tick, fmt.Errorf("listing panes: %w", err)
	}
	paneByAgent := make(map[string]tmux.Pane, len(panes))
	for _, p := range panes {
		if name := e.deps.agentForPane(p); name != "" {
			if _, exists := paneByAgent[name]; !exists {
				paneByAgent[name] = p
			}
		}
	}

	now := e.nowTime()
	perPane := make(map[string][]bugsWatchFinding)
	paneByID := make(map[string]tmux.Pane)
	var unrouted []bugsWatchFinding
	for _, f := range newFindings {
		holder := bugsReservationHolder(reservations, f.ComparablePath, e.projectKey, now)
		if holder == "" {
			unrouted = append(unrouted, f)
			continue
		}
		pane, ok := paneByAgent[holder]
		if !ok || pane.ID == "" {
			// Holder has no live pane in this session; fall back to the
			// digest path rather than dropping the finding.
			unrouted = append(unrouted, f)
			continue
		}
		perPane[pane.ID] = append(perPane[pane.ID], f)
		paneByID[pane.ID] = pane
	}

	var observation statuspkg.SessionObservation
	var observeErr error
	if len(perPane) > 0 {
		observation, observeErr = e.deps.observe(ctx, e.session)
		if observeErr != nil {
			tick.Warnings = append(tick.Warnings, fmt.Sprintf("observing session: %v", observeErr))
		}
	}
	// The freshness reference must be taken AFTER the observation completes
	// (the pattern every assign/send call site uses): observing takes real
	// time, so observation.ObservedAt lies after the tick-start `now`, and
	// DispatchObservationIsCurrent rejects observations stamped later than
	// its reference time. Reusing `now` here would classify every real
	// observation as stale and defer every nudge forever.
	obsCheckTime := e.nowTime()

	paneIDs := make([]string, 0, len(perPane))
	for id := range perPane {
		paneIDs = append(paneIDs, id)
	}
	sort.Strings(paneIDs)

	for _, paneID := range paneIDs {
		findings := perPane[paneID]
		pane := paneByID[paneID]

		// Observation failed entirely: defer, never dispatch blind.
		if observeErr != nil {
			tick.Deferred += len(findings)
			continue
		}

		// Cooldown: >= cooldown between bug nudges per pane, persisted.
		if last, ok, cErr := e.lastNudgeAt(paneID); cErr != nil {
			tick.Warnings = append(tick.Warnings, fmt.Sprintf("cooldown lookup for %s: %v", paneID, cErr))
			tick.Deferred += len(findings)
			continue
		} else if ok && now.Sub(last) < e.cooldown {
			e.logf("pane %s in cooldown (%s remaining); deferring %d finding(s)",
				paneID, (e.cooldown - now.Sub(last)).Round(time.Second), len(findings))
			tick.Deferred += len(findings)
			continue
		}

		// Never nudge a working pane: require a fresh, confidently idle
		// observation (the same gate spawn readiness uses).
		paneObs, obsErr := currentAssignPaneObservation(observation, paneID, obsCheckTime)
		if obsErr != nil || !e.paneSafeToDispatch(paneObs) {
			e.logf("pane %s not safe to dispatch; deferring %d finding(s)", paneID, len(findings))
			tick.Deferred += len(findings)
			continue
		}

		message := buildBugsNudgeMessage(findings)
		if err := e.deps.dispatchNudge(ctx, panes, pane, message); err != nil {
			tick.Warnings = append(tick.Warnings, fmt.Sprintf("nudge to pane %s failed: %v", paneID, err))
			tick.Deferred += len(findings)
			continue
		}

		if err := e.recordNudge(paneID, now); err != nil {
			tick.Warnings = append(tick.Warnings, fmt.Sprintf("recording nudge for %s: %v", paneID, err))
		}
		for _, f := range findings {
			retained[f.Fingerprint] = true
		}
		tick.Nudged += len(findings)
		tick.NudgedPanes = append(tick.NudgedPanes, paneID)
		e.logf("nudged pane %s with %d finding(s)", paneID, len(findings))
	}

	if len(unrouted) > 0 {
		if err := e.deps.publishDigest(ctx, unrouted); err != nil {
			tick.Warnings = append(tick.Warnings, fmt.Sprintf("digest for %d unrouted finding(s) failed: %v", len(unrouted), err))
			if e.deps.publishOutage != nil {
				e.deps.publishOutage(unrouted, fmt.Sprintf("digest delivery failed: %v", err))
			}
		}
		// Digested (or attention-logged) findings count as handled either
		// way: the fallback recorded them durably, so re-broadcasting the
		// same finding every tick would only spam agents.
		for _, f := range unrouted {
			retained[f.Fingerprint] = true
		}
		tick.Digested = len(unrouted)
	}

	persist()
	return tick, nil
}

// buildBugsNudgeMessage renders the templated nudge for one pane.
func buildBugsNudgeMessage(findings []bugsWatchFinding) string {
	var sb strings.Builder
	if len(findings) == 1 {
		sb.WriteString("[UBS] New bug finding in a file you have reserved:\n")
	} else {
		sb.WriteString(fmt.Sprintf("[UBS] %d new bug findings in files you have reserved:\n", len(findings)))
	}
	for i, f := range findings {
		if i >= 10 {
			sb.WriteString(fmt.Sprintf("...and %d more (run 'ntm bugs list' for the full set)\n", len(findings)-10))
			break
		}
		sb.WriteString(fmt.Sprintf("- %s:%d [%s/%s] %s\n",
			f.Finding.File, f.Finding.Line, f.Finding.Severity, f.Finding.Category, f.Finding.Message))
		if f.Finding.Suggestion != "" {
			sb.WriteString(fmt.Sprintf("  Suggested fix: %s\n", f.Finding.Suggestion))
		}
	}
	sb.WriteString("Please review and fix, or note a false positive. (ntm bugs watch)")
	return sb.String()
}

// buildBugsDigestBody renders the coordinator-digest-style summary of
// unrouted findings for the Agent Mail broadcast / attention item.
func buildBugsDigestBody(findings []bugsWatchFinding) string {
	var sb strings.Builder
	sb.WriteString(fmt.Sprintf("## UBS Watch: %d new finding(s) without a reservation holder\n\n", len(findings)))
	for i, f := range findings {
		if i >= bugsWatchMaxDigestFindings {
			sb.WriteString(fmt.Sprintf("\n...and %d more (run 'ntm bugs list')\n", len(findings)-bugsWatchMaxDigestFindings))
			break
		}
		sb.WriteString(fmt.Sprintf("- `%s:%d` [%s/%s] %s\n",
			f.Finding.File, f.Finding.Line, f.Finding.Severity, f.Finding.Category, f.Finding.Message))
	}
	sb.WriteString("\nNo agent currently holds a reservation covering these paths. Claim and fix as capacity allows.")
	return sb.String()
}

// =============================================================================
// Default (production) dependency wiring
// =============================================================================

func newBugsWatchEngine(session, projectKey, scanPath string, cooldown time.Duration, store *state.Store, verbose bool) *bugsWatchEngine {
	engine := &bugsWatchEngine{
		session:    session,
		projectKey: projectKey,
		scanPath:   scanPath,
		cooldown:   cooldown,
		store:      store,
	}
	observer := statuspkg.NewSessionObserver(statuspkg.NewDetector())

	engine.deps = bugsWatchDeps{
		scan: func(ctx context.Context) (*scanner.ScanResult, error) {
			s, err := scanner.NewScannerWithConfig(&cfg.Scanner)
			if err != nil {
				return nil, err
			}
			result, err := s.Scan(ctx, scanPath, scanner.DefaultOptions())
			if err != nil {
				return nil, err
			}
			// Refresh the shared scan cache best-effort so `ntm bugs
			// list` / `summary` see the same results (mirrors bugs.go).
			if cacheErr := saveCachedScanResult(scanPath, result); cacheErr != nil {
				fmt.Fprintf(os.Stderr, "Warning: failed to cache scan results: %v\n", cacheErr)
			}
			return result, nil
		},
		agentMailUp: func(ctx context.Context) bool {
			return newAgentMailClient(projectKey).IsAvailableContext(ctx)
		},
		listReservations: func(ctx context.Context) ([]agentmail.FileReservation, error) {
			listCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
			defer cancel()
			return fetchActiveReservations(listCtx, newAgentMailClient(projectKey), projectKey, "", true)
		},
		listPanes: func(ctx context.Context) ([]tmux.Pane, error) {
			return tmux.GetPanesContext(ctx, session)
		},
		agentForPane: func(pane tmux.Pane) string {
			registry, _ := agentmail.LoadBestSessionAgentRegistry(session, projectKey)
			return resolvePaneAgentName(pane, registry)
		},
		observe: func(ctx context.Context, sess string) (statuspkg.SessionObservation, error) {
			return observer.Observe(ctx, sess)
		},
		dispatchNudge: func(ctx context.Context, panes []tmux.Pane, target tmux.Pane, message string) error {
			return dispatchBugsNudge(ctx, session, panes, target, message)
		},
		publishDigest: func(ctx context.Context, findings []bugsWatchFinding) error {
			return publishBugsDigest(ctx, projectKey, findings)
		},
		publishOutage: func(findings []bugsWatchFinding, cause string) {
			publishBugsAttentionItem(store, session, projectKey, findings, cause)
		},
		now: time.Now,
	}
	if verbose {
		engine.deps.logf = func(format string, args ...interface{}) {
			fmt.Fprintf(os.Stderr, "bugs watch: "+format+"\n", args...)
		}
	}
	return engine
}

// dispatchBugsNudge delivers one nudge through the gated dispatch service:
// redaction policy, composer-ready gating, and per-agent submission
// verification all come from the shared ports (same wiring as `ntm replay`).
func dispatchBugsNudge(ctx context.Context, session string, panes []tmux.Pane, target tmux.Pane, message string) error {
	service, err := dispatchsvc.NewService(dispatchsvc.Ports{
		Redactor:  shellFinalMessageRedactor(activeShellDispatchRedactionConfig()),
		Protocols: shellDispatchProtocolPlanner{},
		Deliverer: dispatchsvc.TMUXDeliverer{},
		Lifecycle: dispatchsvc.LifecycleHooks{
			AfterReceipt: func(_ context.Context, delivery dispatchsvc.Delivery, receipt dispatchsvc.Receipt) {
				if receipt.Status == dispatchsvc.ReceiptDelivered {
					addTimelinePromptMarker(session, delivery.Target.Pane, delivery.Message)
				}
			},
		},
	})
	if err != nil {
		return fmt.Errorf("preparing bug nudge dispatch: %w", err)
	}
	result, err := service.Execute(ctx, dispatchsvc.Request{
		Session:       session,
		Panes:         panes,
		Selectors:     shellDispatchSelectors([]tmux.Pane{target}),
		IncludeUser:   true,
		Message:       message,
		Submit:        true,
		StopOnFailure: true,
	})
	if err != nil {
		return err
	}
	if result.Delivered != 1 {
		return fmt.Errorf("bug nudge delivered to %d of 1 target", result.Delivered)
	}
	return nil
}

// publishBugsDigest broadcasts one coordinator-digest-style summary of
// unrouted findings to the project's registered agents via Agent Mail (the
// same channel coordinator digests are delivered on), from the established
// "ntm_scanner" identity that `ntm bugs notify` already registers.
func publishBugsDigest(ctx context.Context, projectKey string, findings []bugsWatchFinding) error {
	if len(findings) == 0 {
		return nil
	}
	client := newAgentMailClient(projectKey)
	if !client.IsAvailableContext(ctx) {
		return errors.New("agent mail server unavailable")
	}
	sendCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()

	// Ensure the sender identity exists before sending: `ntm bugs notify`
	// registers "ntm_scanner" the same way, but on a project where notify has
	// never run the identity is absent and every send would fail. Best-effort
	// (mirrors scanner.ensureScannerRegistered): if registration fails the
	// send below surfaces the real error.
	if _, err := client.RegisterAgent(sendCtx, agentmail.RegisterAgentOptions{
		ProjectKey:      projectKey,
		Name:            "ntm_scanner",
		Program:         "ntm",
		Model:           "scanner",
		TaskDescription: "Automated vulnerability scanner",
	}); err != nil {
		fmt.Fprintf(os.Stderr, "bugs watch: registering ntm_scanner identity: %v\n", err)
	}

	agents, err := client.ListProjectAgents(sendCtx, projectKey)
	if err != nil {
		return fmt.Errorf("listing project agents: %w", err)
	}
	recipients := make([]string, 0, len(agents))
	for _, a := range agents {
		if a.Name == "ntm_scanner" || a.Name == "HumanOverseer" {
			continue
		}
		recipients = append(recipients, a.Name)
	}
	if len(recipients) == 0 {
		return errors.New("no registered agents to receive the digest")
	}
	_, err = client.SendMessage(sendCtx, agentmail.SendMessageOptions{
		ProjectKey: projectKey,
		SenderName: "ntm_scanner",
		To:         recipients,
		Subject:    fmt.Sprintf("[UBS watch] %d unrouted finding(s)", len(findings)),
		BodyMD:     buildBugsDigestBody(findings),
		Importance: "normal",
	})
	return err
}

// publishBugsAttentionItem logs findings and records a deduplicated
// attention event in the runtime store so the dashboard's attention panel
// surfaces them even when no delivery channel was reachable.
func publishBugsAttentionItem(store *state.Store, session, projectKey string, findings []bugsWatchFinding, cause string) {
	if len(findings) == 0 {
		return
	}
	severity := state.SeverityWarning
	lines := make([]string, 0, len(findings))
	for i, f := range findings {
		if f.Finding.Severity == scanner.SeverityCritical {
			severity = state.SeverityCritical
		}
		if i < bugsWatchMaxDigestFindings {
			lines = append(lines, fmt.Sprintf("%s:%d [%s/%s] %s",
				f.Finding.File, f.Finding.Line, f.Finding.Severity, f.Finding.Category, f.Finding.Message))
		}
	}
	for _, line := range lines {
		fmt.Fprintf(os.Stderr, "bugs watch: undelivered finding: %s\n", line)
	}
	if store == nil {
		return
	}
	details, _ := json.Marshal(map[string]interface{}{
		"cause":    cause,
		"count":    len(findings),
		"findings": lines,
	})
	if _, err := store.AppendAttentionEvent(&state.StoredAttentionEvent{
		Ts:            time.Now().UTC(),
		SessionName:   session,
		Category:      "alert",
		EventType:     "ubs_findings_undelivered",
		Source:        "bugs_watch",
		Actionability: state.ActionabilityActionRequired,
		Severity:      severity,
		ReasonCode:    "ubs_findings_undelivered",
		Summary:       fmt.Sprintf("%d UBS finding(s) could not be routed: %s", len(findings), cause),
		Details:       string(details),
		DedupKey:      "bugs_watch:undelivered:" + projectKey,
	}); err != nil {
		fmt.Fprintf(os.Stderr, "bugs watch: recording attention item: %v\n", err)
	}
}

// =============================================================================
// Command
// =============================================================================

func newBugsWatchCmd() *cobra.Command {
	var (
		sessionFlag string
		interval    time.Duration
		once        bool
		force       bool
		verbose     bool
	)

	cmd := &cobra.Command{
		Use:   "watch [path]",
		Short: "Watch for new UBS findings and route them to reservation holders",
		Long: `Periodically scan with UBS and push NEW findings to the agent holding
the affected file's Agent Mail reservation.

Each tick runs the same UBS invocation 'ntm bugs list' uses, fingerprints
findings (file:line:category:message), diffs them against the persisted set,
and routes new ones through the gated dispatch path (composer-ready,
submission-verified). Nudges are rate-limited per pane (default 10m
cooldown) and never interrupt a working pane. Findings in unreserved paths
are batched into one digest broadcast; if Agent Mail is unreachable they are
logged and recorded as an attention item instead.

Push routing is opt-in: set [bugs] push_routing = true in your ntm config,
or pass --force for a one-off run.

Examples:
  ntm bugs watch                       # Watch the current project
  ntm bugs watch --session myproj      # Explicit session
  ntm bugs watch --interval 10m        # Custom scan interval
  ntm bugs watch --once                # Single tick, then exit
  ntm bugs watch --once --force        # One-off run with push_routing disabled`,
		Args: cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			path := "."
			if len(args) > 0 {
				path = args[0]
			}
			if interval <= 0 {
				interval = cfg.Bugs.EffectiveInterval()
			}
			return runBugsWatch(cmd, sessionFlag, path, interval, once, force, verbose)
		},
	}

	cmd.Flags().StringVar(&sessionFlag, "session", "", "NTM session name (default: resolve from cwd)")
	cmd.Flags().DurationVar(&interval, "interval", 0, "Scan interval (default: [bugs] interval, 5m)")
	cmd.Flags().BoolVar(&once, "once", false, "Run exactly one scan/route tick, then exit")
	cmd.Flags().BoolVar(&force, "force", false, "Run even when [bugs] push_routing is disabled")
	cmd.Flags().BoolVar(&verbose, "verbose", false, "Log per-pane routing decisions to stderr")

	return cmd
}

func runBugsWatch(cmd *cobra.Command, sessionFlag, path string, interval time.Duration, once, force, verbose bool) error {
	// Opt-in gate: watch requires push_routing=true or an explicit --force.
	if !cfg.Bugs.PushRouting && !force {
		return fmt.Errorf("bug push routing is disabled; set [bugs] push_routing = true in your ntm config (or rerun with --force)")
	}

	// Graceful degradation: same friendly UBS-missing handling bugs.go uses.
	if !scanner.IsAvailable() && cfg.Scanner.UBSPath == "" {
		if jsonOutput {
			cause := scanner.ErrNotInstalled
			response := robot.NewErrorResponse(
				cause,
				robot.ErrCodeDependencyMissing,
				"Install UBS from https://github.com/nightowlai/ubs, then rerun 'ntm bugs watch --json'",
			)
			response.OutputFormat = string(robot.FormatJSON)
			response.Meta = robot.NewResponseMeta("bugs watch").WithExitCode(1)
			return emitJSONFailureEnvelopeWithCause(struct {
				robot.RobotResponse
				Available bool `json:"available"`
			}{
				RobotResponse: response,
				Available:     false,
			}, cause)
		}
		fmt.Println("UBS not installed. Install: https://github.com/nightowlai/ubs")
		return nil
	}

	absPath, err := filepath.Abs(path)
	if err != nil {
		return fmt.Errorf("resolving path: %w", err)
	}

	res, err := ResolveSessionWithOptions(sessionFlag, cmd.OutOrStdout(), SessionResolveOptions{
		TreatAsJSON: IsJSONOutput(),
	})
	if err != nil {
		return err
	}
	if res.Session == "" {
		return nil
	}
	res.ExplainIfInferred(cmd.ErrOrStderr())

	session, projectKey, err := resolveAgentMailScope(cmd.Context(), res.Session)
	if err != nil {
		return err
	}

	store, err := state.Open("")
	if err != nil {
		return fmt.Errorf("open state store: %w", err)
	}
	defer store.Close()
	if err := store.Migrate(); err != nil {
		return fmt.Errorf("migrate state store: %w", err)
	}

	engine := newBugsWatchEngine(session, projectKey, absPath, cfg.Bugs.EffectiveCooldown(), store, verbose)

	ctx, stop := signal.NotifyContext(cmd.Context(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	emitTick := func(tick bugsWatchTick) error {
		if jsonOutput {
			return json.NewEncoder(os.Stdout).Encode(tick)
		}
		fmt.Printf("[%s] findings=%d new=%d nudged=%d deferred=%d digested=%d",
			tick.Timestamp, tick.Findings, tick.New, tick.Nudged, tick.Deferred, tick.Digested)
		if tick.AgentMailDown {
			fmt.Print(" (agent mail unavailable)")
		}
		fmt.Println()
		for _, w := range tick.Warnings {
			fmt.Fprintf(os.Stderr, "  warning: %s\n", w)
		}
		return nil
	}

	for {
		tick, tickErr := engine.Tick(ctx)
		if tickErr != nil {
			if once {
				return tickErr
			}
			if ctx.Err() != nil {
				return nil
			}
			fmt.Fprintf(os.Stderr, "bugs watch: tick failed: %v\n", tickErr)
		} else if err := emitTick(tick); err != nil {
			return err
		}

		if once {
			return nil
		}
		select {
		case <-ctx.Done():
			return nil
		case <-time.After(interval):
		}
	}
}
