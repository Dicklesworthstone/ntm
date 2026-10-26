package pipeline

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/Dicklesworthstone/ntm/internal/agentmail"
)

// MailSendStep describes a first-class MCP Agent Mail send operation.
type MailSendStep struct {
	ProjectKey  string       `yaml:"project_key,omitempty" toml:"project_key,omitempty" json:"project_key,omitempty"`
	AgentName   string       `yaml:"agent_name,omitempty" toml:"agent_name,omitempty" json:"agent_name,omitempty"`
	To          StringOrList `yaml:"to,omitempty" toml:"to,omitempty" json:"to,omitempty"`
	Subject     string       `yaml:"subject,omitempty" toml:"subject,omitempty" json:"subject,omitempty"`
	Body        string       `yaml:"body,omitempty" toml:"body,omitempty" json:"body,omitempty"`
	ThreadID    string       `yaml:"thread_id,omitempty" toml:"thread_id,omitempty" json:"thread_id,omitempty"`
	AckRequired bool         `yaml:"ack_required,omitempty" toml:"ack_required,omitempty" json:"ack_required,omitempty"`
}

const (
	mailSendVersion  = 1
	mailSendSending  = "sending"
	mailSendSent     = "sent"
	mailSendRejected = "rejected"
)

// MailSendState fences a non-idempotent Agent Mail send through the same
// checkpoint as the workflow. A missing transport response is not proof that
// the recipient did not receive the work. The sent result is recorded before
// scheduler bookkeeping or success hooks can lose its recovery evidence.
type MailSendState struct {
	Version    int        `json:"version"`
	StepID     string     `json:"step_id"`
	StepHash   string     `json:"step_hash"`
	Status     string     `json:"status"`
	StartedAt  time.Time  `json:"started_at"`
	FinishedAt time.Time  `json:"finished_at,omitempty"`
	Result     StepResult `json:"result,omitempty"`
}

func cloneMailSend(record MailSendState) MailSendState {
	record.Result.ParsedData = cloneInterfaceValue(record.Result.ParsedData)
	if record.Result.Error != nil {
		copy := cloneStepErrorValue(*record.Result.Error)
		record.Result.Error = &copy
	}
	return record
}

func validateMailSend(key string, record MailSendState) error {
	invalid := func(reason string) error {
		return fmt.Errorf("invalid mail_send receipt for step %q: %s", key, reason)
	}
	digest, err := hex.DecodeString(record.StepHash)
	if key == "" || record.StepID != key || record.Version != mailSendVersion ||
		err != nil || len(digest) != sha256.Size || record.StepHash != strings.ToLower(record.StepHash) || record.StartedAt.IsZero() {
		return invalid("unsupported version or missing execution identity")
	}
	switch record.Status {
	case mailSendSending:
		if !record.FinishedAt.IsZero() || record.Result.Status != "" {
			return invalid("unresolved send contains a settled outcome")
		}
	case mailSendSent, mailSendRejected:
		if record.FinishedAt.Before(record.StartedAt) || record.Result.StepID != key || record.Result.FinishedAt.IsZero() ||
			(record.Result.Status != StatusCompleted && record.Result.Status != StatusFailed && record.Result.Status != StatusCancelled) {
			return invalid("missing or inconsistent settled result")
		}
		if record.Status == mailSendRejected && record.Result.Status == StatusCompleted {
			return invalid("rejected send reports successful delivery")
		}
	default:
		return invalid("unknown delivery status")
	}
	return nil
}

func (e *Executor) loadMailSend(stepID string) (MailSendState, bool) {
	e.stateMu.RLock()
	defer e.stateMu.RUnlock()
	if e.state == nil {
		return MailSendState{}, false
	}
	record, ok := e.state.MailSends[stepID]
	return cloneMailSend(record), ok
}

func (e *Executor) saveMailSend(record MailSendState) error {
	if err := validateMailSend(record.StepID, record); err != nil {
		return err
	}
	e.stateMu.Lock()
	if e.state == nil {
		e.stateMu.Unlock()
		return fmt.Errorf("cannot save mail_send receipt without execution state")
	}
	if e.state.MailSends == nil {
		e.state.MailSends = make(map[string]MailSendState)
	}
	e.state.MailSends[record.StepID] = cloneMailSend(record)
	e.stateMu.Unlock()
	return e.persistState()
}

func uncertainMailSendError(stepID string) error {
	return fmt.Errorf("mail_send step %q has an unresolved delivery; the recipient may already have received it. Ordinary resume and automatic retries will not send it again. Inspect the recipient inbox and original message before deliberately using --mode=restart-failed (or --keep-state=false for a whole-workflow replay)", stepID)
}

func resumeResetsMailSend(state *ExecutionState, opts ResumeOptions, stepID string) bool {
	if !opts.KeepState {
		return true
	}
	switch opts.Mode {
	case ResumeModeForceIter:
		return iterationIndexFromID(stepID, opts.StepID+"_iter") >= opts.Iteration
	case ResumeModeRestartFailed:
		if record, ok := state.MailSends[stepID]; ok {
			// Delivery is authoritative even when output parsing failed or
			// the scheduler never got to record a successful step result.
			return record.Status != mailSendSent
		}
		if _, inFlight := state.InFlightSteps[stepID]; inFlight {
			return true
		}
		if result, ok := state.Steps[stepID]; ok {
			return shouldRerunStep(result)
		}
	}
	return false
}

// The caller holds stateMu. Check uncertainty before resume can clear any
// in-flight markers or run an independent sibling. Legacy interrupted sends
// have no receipt and require the same deliberate replay decision.
func (e *Executor) validateMailSendResumeLocked(state *ExecutionState, opts ResumeOptions, workflow *Workflow) error {
	for id, record := range state.MailSends {
		if err := validateMailSend(id, record); err != nil {
			return err
		}
		reset := resumeResetsMailSend(state, opts, id)
		if record.Status == mailSendSending && !reset {
			return uncertainMailSendError(id)
		}
		if reset && opts.KeepState && opts.Mode != ResumeModeForceIter {
			if err := e.validateLeafReplayScopeLocked(workflow, state, id, StepKindMailSend); err != nil {
				return err
			}
		}
	}
	if !opts.KeepState {
		return nil
	}
	graph := e.graph
	if graph == nil && workflow != nil {
		graph = NewDependencyGraph(workflow)
	}
	resolve, err := resumeRuntimeStepResolver(graph, state)
	if err != nil {
		return err
	}
	unfinished := make(map[string]bool)
	for id := range state.InFlightSteps {
		unfinished[id] = true
	}
	for id, result := range state.Steps {
		if shouldRerunStep(result) {
			unfinished[id] = true
		}
	}
	for id := range unfinished {
		if _, recorded := state.MailSends[id]; recorded || resumeResetsMailSend(state, opts, id) {
			continue
		}
		mailSend := state.InFlightSteps[id].Kind == StepKindMailSend
		if step, ok := resolve(id); ok {
			mailSend = mailSend || step.MailSend != nil
		}
		if mailSend {
			return uncertainMailSendError(id)
		}
	}
	return nil
}

// FileReservationPathsStep describes an MCP Agent Mail file reservation
// acquisition operation.
type FileReservationPathsStep struct {
	ProjectKey string       `yaml:"project_key,omitempty" toml:"project_key,omitempty" json:"project_key,omitempty"`
	AgentName  string       `yaml:"agent_name,omitempty" toml:"agent_name,omitempty" json:"agent_name,omitempty"`
	Paths      StringOrList `yaml:"paths,omitempty" toml:"paths,omitempty" json:"paths,omitempty"`
	TTLSeconds int          `yaml:"ttl_seconds,omitempty" toml:"ttl_seconds,omitempty" json:"ttl_seconds,omitempty"`
	Exclusive  bool         `yaml:"exclusive,omitempty" toml:"exclusive,omitempty" json:"exclusive,omitempty"`
	Reason     string       `yaml:"reason,omitempty" toml:"reason,omitempty" json:"reason,omitempty"`
}

// MailInboxCheckStep describes a first-class MCP Agent Mail inbox polling
// operation.
type MailInboxCheckStep struct {
	ProjectKey    string `yaml:"project_key,omitempty" toml:"project_key,omitempty" json:"project_key,omitempty"`
	AgentName     string `yaml:"agent_name,omitempty" toml:"agent_name,omitempty" json:"agent_name,omitempty"`
	UntilAckCount int    `yaml:"until_ack_count,omitempty" toml:"until_ack_count,omitempty" json:"until_ack_count,omitempty"`
}

// FileReservationReleaseStep describes an MCP Agent Mail file reservation
// release operation.
type FileReservationReleaseStep struct {
	ProjectKey string       `yaml:"project_key,omitempty" toml:"project_key,omitempty" json:"project_key,omitempty"`
	AgentName  string       `yaml:"agent_name,omitempty" toml:"agent_name,omitempty" json:"agent_name,omitempty"`
	Paths      StringOrList `yaml:"paths,omitempty" toml:"paths,omitempty" json:"paths,omitempty"`
}

const (
	reservationStepVersion = 1
	reservationAcquiring   = "acquiring"
	reservationHeld        = "held"
	reservationPartial     = "partial"
	reservationUncertain   = "uncertain"
	reservationRejected    = "rejected"
	reservationReleasing   = "releasing"
	reservationReleased    = "released"
)

// ReservationStepState preserves the identity of leases acquired by this run.
// Uncertain grants are inspection evidence only. A release receipt contains
// copied, verified lease identities, never a path selector that could later
// identify a different run's replacement reservation.
type ReservationStepState struct {
	Version     int                             `json:"version"`
	StepID      string                          `json:"step_id"`
	StepHash    string                          `json:"step_hash"`
	Kind        string                          `json:"kind"`
	Status      string                          `json:"status"`
	Request     FileReservationPathsStep        `json:"request"`
	StartedAt   time.Time                       `json:"started_at"`
	FinishedAt  time.Time                       `json:"finished_at,omitempty"`
	Granted     []agentmail.FileReservation     `json:"granted,omitempty"`
	Conflicts   []agentmail.ReservationConflict `json:"conflicts,omitempty"`
	ReleasedIDs []int                           `json:"released_ids,omitempty"`
	Result      StepResult                      `json:"result,omitempty"`
}

func cloneReservationStep(record ReservationStepState) ReservationStepState {
	record.Request.Paths = append(StringOrList(nil), record.Request.Paths...)
	record.Granted = append([]agentmail.FileReservation(nil), record.Granted...)
	for i := range record.Granted {
		if record.Granted[i].ReleasedTS != nil {
			released := *record.Granted[i].ReleasedTS
			record.Granted[i].ReleasedTS = &released
		}
	}
	record.Conflicts = append([]agentmail.ReservationConflict(nil), record.Conflicts...)
	for i := range record.Conflicts {
		record.Conflicts[i].Holders = append([]string(nil), record.Conflicts[i].Holders...)
	}
	record.ReleasedIDs = append([]int(nil), record.ReleasedIDs...)
	record.Result.ParsedData = cloneInterfaceValue(record.Result.ParsedData)
	if record.Result.Error != nil {
		copy := cloneStepErrorValue(*record.Result.Error)
		record.Result.Error = &copy
	}
	return record
}

func validateReservationStep(key string, record ReservationStepState) error {
	invalid := func(reason string) error {
		return fmt.Errorf("invalid reservation receipt for step %q: %s", key, reason)
	}
	digest, err := hex.DecodeString(record.StepHash)
	if key == "" || record.StepID != key || record.Version != reservationStepVersion || record.StartedAt.IsZero() ||
		err != nil || len(digest) != sha256.Size || record.StepHash != strings.ToLower(record.StepHash) ||
		strings.TrimSpace(record.Request.ProjectKey) == "" || strings.TrimSpace(record.Request.AgentName) == "" || len(record.Request.Paths) == 0 {
		return invalid("unsupported version or incomplete execution identity")
	}
	wanted := make(map[string]bool)
	for _, path := range record.Request.Paths {
		if strings.TrimSpace(path) == "" {
			return invalid("empty requested path")
		}
		wanted[path] = true
	}
	if record.Kind != StepKindReservationAcquire && record.Kind != StepKindReservationRelease {
		return invalid("unknown reservation step kind")
	}
	switch record.Status {
	case reservationAcquiring:
		if record.Kind != StepKindReservationAcquire || len(record.Granted) != 0 || record.Result.Status != "" || !record.FinishedAt.IsZero() {
			return invalid("acquisition intent contains an outcome")
		}
	case reservationUncertain, reservationRejected:
		if record.Kind != StepKindReservationAcquire || record.Result.Status != StatusFailed && record.Result.Status != StatusCancelled ||
			record.Status == reservationRejected && len(record.Granted) != 0 {
			return invalid("inconsistent failed acquisition")
		}
	case reservationHeld, reservationPartial:
		if record.Kind != StepKindReservationAcquire || len(record.Granted) == 0 ||
			record.Status == reservationPartial && (len(record.Conflicts) == 0 || record.Result.Status != StatusFailed) {
			return invalid("inconsistent acquired leases")
		}
	case reservationReleasing:
		if record.Kind != StepKindReservationRelease || len(record.Granted) == 0 || record.Result.Status == StatusCompleted {
			return invalid("release intent contains no captured leases or reports completion")
		}
	case reservationReleased:
	default:
		return invalid("unknown reservation status")
	}
	if record.Status != reservationAcquiring && record.Status != reservationReleasing {
		if record.FinishedAt.Before(record.StartedAt) || record.Result.StepID != key || record.Result.FinishedAt.IsZero() ||
			(record.Result.Status != StatusCompleted && record.Result.Status != StatusFailed && record.Result.Status != StatusCancelled) {
			return invalid("missing settled result")
		}
	}
	if record.Status == reservationAcquiring || record.Status == reservationUncertain || record.Status == reservationRejected {
		if len(record.ReleasedIDs) != 0 {
			return invalid("unverified acquisition claims released leases")
		}
		return nil
	}
	ids, paths := make(map[int]bool), make(map[string]bool)
	projectID := 0
	for _, grant := range record.Granted {
		if grant.ID <= 0 || ids[grant.ID] || grant.ProjectID <= 0 || projectID != 0 && projectID != grant.ProjectID ||
			grant.AgentName != record.Request.AgentName || !wanted[grant.PathPattern] || grant.CreatedTS.IsZero() ||
			!grant.ExpiresTS.After(grant.CreatedTS.Time) || grant.ReleasedTS != nil {
			return invalid("missing or inconsistent captured lease identity")
		}
		if record.Kind == StepKindReservationAcquire && (paths[grant.PathPattern] || grant.Exclusive != record.Request.Exclusive ||
			record.Request.Reason != "" && grant.Reason != record.Request.Reason) {
			return invalid("grant differs from the acquisition request")
		}
		projectID, ids[grant.ID], paths[grant.PathPattern] = grant.ProjectID, true, true
	}
	if record.Status == reservationHeld && len(paths) != len(wanted) {
		return invalid("held leases do not cover the complete request")
	}
	released := make(map[int]bool)
	for _, id := range record.ReleasedIDs {
		if !ids[id] || released[id] || record.Kind != StepKindReservationAcquire {
			return invalid("released ID is missing, repeated, or foreign")
		}
		released[id] = true
	}
	if record.Kind == StepKindReservationAcquire && record.Status == reservationReleased && len(released) != len(ids) {
		return invalid("released acquisition retains unreleased lease IDs")
	}
	return nil
}

func (e *Executor) loadReservationStep(id string) (ReservationStepState, bool) {
	e.stateMu.RLock()
	defer e.stateMu.RUnlock()
	if e.state == nil {
		return ReservationStepState{}, false
	}
	record, ok := e.state.ReservationSteps[id]
	return cloneReservationStep(record), ok
}

func (e *Executor) saveReservationStep(record ReservationStepState) error {
	if err := validateReservationStep(record.StepID, record); err != nil {
		return err
	}
	e.stateMu.Lock()
	if e.state == nil {
		e.stateMu.Unlock()
		return fmt.Errorf("cannot save reservation receipt without execution state")
	}
	if e.state.ReservationSteps == nil {
		e.state.ReservationSteps = make(map[string]ReservationStepState)
	}
	e.state.ReservationSteps[record.StepID] = cloneReservationStep(record)
	e.stateMu.Unlock()
	if err := e.persistState(); err != nil {
		return err
	}
	e.wakeReservationProtection()
	return nil
}

func reservationRecoveryError(stepID string) error {
	return fmt.Errorf("reservation step %q has unresolved or partial acquisition evidence; inspect its recorded lease IDs and live reservations before an explicit restart-failed or whole-workflow reset. Automatic retries and ordinary resume will not acquire another lease", stepID)
}

func reservationIDSet(ids []int) map[int]bool {
	set := make(map[int]bool, len(ids))
	for _, id := range ids {
		set[id] = true
	}
	return set
}

// Readback proves the captured identities are still live, not merely that
// somebody now owns the same paths. Cleanup may tolerate absent IDs, but never
// changed ownership. Renewals may extend expiry without changing identity.
func (e *Executor) verifyReservationLeases(ctx context.Context, record ReservationStepState, grants []agentmail.FileReservation, allowMissing bool) ([]int, error) {
	rows, err := e.readReservationLeases(ctx, record, grants, allowMissing)
	if err != nil {
		return nil, err
	}
	ids := make([]int, len(rows))
	for i, row := range rows {
		ids[i] = row.ID
	}
	return ids, nil
}

func (e *Executor) readReservationLeases(ctx context.Context, record ReservationStepState, grants []agentmail.FileReservation, allowMissing bool) ([]agentmail.FileReservation, error) {
	if len(grants) == 0 {
		return nil, ctx.Err()
	}
	rows, err := e.mailClient(record.Request.ProjectKey).ReadActiveReservations(ctx, record.Request.ProjectKey)
	if err != nil {
		return nil, fmt.Errorf("verify reservation step %q: %w", record.StepID, err)
	}
	byID := make(map[int]agentmail.FileReservation, len(rows))
	for _, row := range rows {
		if _, duplicate := byID[row.ID]; duplicate {
			return nil, fmt.Errorf("reservation step %q has duplicate live lease ID %d", record.StepID, row.ID)
		}
		byID[row.ID] = row
	}
	var active []agentmail.FileReservation
	now := time.Now()
	for _, grant := range grants {
		row, found := byID[grant.ID]
		if !found && allowMissing {
			continue
		}
		if !found || !sameReservationLease(row, grant) {
			return nil, fmt.Errorf("reservation step %q lease %d for %q is absent, changed, expired, or released; restore verified protection or deliberately reset before resuming dependent work", record.StepID, grant.ID, grant.PathPattern)
		}
		if row.ReleasedTS != nil || !row.ExpiresTS.After(now) {
			if allowMissing {
				continue
			}
			return nil, fmt.Errorf("reservation step %q lease %d for %q is expired or released; restore verified protection or deliberately reset before resuming dependent work", record.StepID, grant.ID, grant.PathPattern)
		}
		active = append(active, row)
	}
	return active, ctx.Err()
}

// Explicit release has its own identity check and must remain possible after
// expiry. All other dispatches need currently verified reservation protection.
type reservationReleaseKey struct{}

func (e *Executor) wakeReservationProtection() {
	select {
	case e.reservationWake <- struct{}{}:
	default:
	}
}

func (e *Executor) reservationProtectionFailure() error {
	e.stateMu.RLock()
	defer e.stateMu.RUnlock()
	return e.reservationFailure
}

func (e *Executor) failReservationProtection(cause error) error {
	e.stateMu.Lock()
	first := e.reservationFailure == nil
	if first {
		e.reservationFailure = fmt.Errorf("pipeline reservation protection lost or unverified: %w", cause)
		e.state.Errors = append(e.state.Errors, ExecutionError{
			Type: "reservation_protection", Message: e.reservationFailure.Error(), Timestamp: time.Now(), Fatal: true,
		})
	}
	err := e.reservationFailure
	e.stateMu.Unlock()
	// Stop active waits/commands and let the existing owner join them. There
	// is no rollback of input already delivered to an external agent.
	e.Cancel()
	if first {
		return errors.Join(err, e.persistState())
	}
	return err
}

// A single heartbeat spans main work and post-pipeline hooks. A new receipt
// wakes it immediately; thereafter the next expiry determines its deadline.
// Cancellation joins the worker before Run/Resume returns or is reused.
func (e *Executor) startReservationProtection(ctx context.Context, workflow *Workflow) func() {
	if e.config.DryRun {
		return func() {}
	}
	e.stateMu.RLock()
	e.reservationFinalCheck = !reservationOnlyCleanupRemaining(workflow, e.state)
	e.stateMu.RUnlock()
	ctx, cancel := context.WithCancel(ctx)
	done := make(chan struct{})
	e.reservationWG.Add(1)
	go func() {
		defer e.reservationWG.Done()
		defer close(done)
		timer := time.NewTimer(0)
		defer timer.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-e.reservationStop:
				return
			case <-e.reservationWake:
			case <-timer.C:
			}
			select {
			case <-e.reservationStop:
				return
			default:
			}
			// Cleanup-only recovery intentionally tolerates disappeared IDs.
			// Once main work is complete, a tail made only of releases does
			// not need a heartbeat to extend protection for already-done work.
			e.stateMu.RLock()
			cleanupOnly := reservationOnlyCleanupRemaining(workflow, e.state)
			e.stateMu.RUnlock()
			next := 30 * time.Second
			if !cleanupOnly {
				var err error
				next, err = e.refreshReservationProtection(ctx)
				if err != nil {
					if ctx.Err() == nil {
						e.failReservationProtection(err)
					}
					return
				}
			}
			timer.Reset(next)
		}
	}()
	return func() {
		e.stopReservationProtection()
		cancel()
		<-done
	}
}

func (e *Executor) stopReservationProtection() {
	e.reservationStopOnce.Do(func() {
		if e.reservationStop != nil {
			close(e.reservationStop)
		}
	})
	e.reservationWG.Wait()
}

func reservationRenewalLead(record ReservationStepState) time.Duration {
	// Renew before the final third of short leases, with a 30-second lead
	// for ordinary leases. Avoid overflowing time.Duration for large TTLs.
	if ttl := record.Request.TTLSeconds; ttl > 0 && ttl < 90 {
		return time.Duration(ttl) * time.Second / 3
	}
	return 30 * time.Second
}

// refreshReservationProtection never reacquires by path. It serializes with
// authored release, verifies the original immutable identities, extends only
// due IDs, and treats independent readback as the authority even if the reply
// was lost. Fresh expiry evidence is checkpointed before dispatch can proceed.
func (e *Executor) refreshReservationProtection(ctx context.Context) (time.Duration, error) {
	e.reservationMu.Lock()
	defer e.reservationMu.Unlock()
	if err := ctx.Err(); err != nil {
		return 0, err
	}
	e.stateMu.RLock()
	var records []ReservationStepState
	if e.state != nil {
		for _, record := range e.state.ReservationSteps {
			if record.Kind == StepKindReservationAcquire && record.Status == reservationHeld {
				records = append(records, cloneReservationStep(record))
			}
		}
	}
	e.stateMu.RUnlock()
	sort.Slice(records, func(i, j int) bool { return records[i].StepID < records[j].StepID })
	// A slow project must not let another project's earlier lease expire
	// while this serialized pass is still waiting on its readback. Bound
	// the whole pass by the earliest captured deadline, not just each RPC.
	batchBudget := 30 * time.Second
	for _, record := range records {
		released := reservationIDSet(record.ReleasedIDs)
		for _, grant := range record.Granted {
			if !released[grant.ID] {
				if remaining := time.Until(grant.ExpiresTS.Time); remaining > 0 && remaining < batchBudget {
					batchBudget = remaining
				}
			}
		}
	}
	ctx, cancelBatch := context.WithTimeout(ctx, batchBudget)
	defer cancelBatch()
	next := 30 * time.Second
	for _, record := range records {
		if err := validateReservationStep(record.StepID, record); err != nil {
			return 0, err
		}
		released := reservationIDSet(record.ReleasedIDs)
		var grants []agentmail.FileReservation
		budget := 30 * time.Second
		for _, grant := range record.Granted {
			if !released[grant.ID] {
				grants = append(grants, grant)
				if remaining := time.Until(grant.ExpiresTS.Time); remaining < budget {
					budget = remaining
				}
			}
		}
		if len(grants) == 0 {
			continue
		}
		// Saved expiry can lag an external renewal (including after resume).
		// Allow a bounded read to reconcile it, without dispatching new work.
		if budget <= 0 {
			budget = 250 * time.Millisecond
		}
		readCtx, cancel := context.WithTimeout(ctx, budget)
		live, err := e.readReservationLeases(readCtx, record, grants, false)
		cancel()
		if err != nil {
			return 0, err
		}
		lead := reservationRenewalLead(record)
		dueIDs := make([]int, 0, len(live))
		before := make(map[int]time.Time, len(live))
		budget = 30 * time.Second
		for _, row := range live {
			before[row.ID] = row.ExpiresTS.Time
			if !row.ExpiresTS.After(time.Now().Add(lead)) {
				dueIDs = append(dueIDs, row.ID)
			}
			if remaining := time.Until(row.ExpiresTS.Time); remaining < budget {
				budget = remaining
			}
		}
		if len(dueIDs) > 0 {
			seconds := record.Request.TTLSeconds
			if seconds <= 0 {
				seconds = 3600
			}
			renewCtx, cancel := context.WithTimeout(ctx, budget)
			_, renewalErr := e.mailClient(record.Request.ProjectKey).RenewReservations(renewCtx, agentmail.RenewReservationsOptions{
				ProjectKey: record.Request.ProjectKey, AgentName: record.Request.AgentName,
				ExtendSeconds: seconds, ReservationIDs: dueIDs,
			})
			live, err = e.readReservationLeases(renewCtx, record, grants, false)
			cancel()
			if err != nil {
				return 0, errors.Join(renewalErr, err)
			}
			due := reservationIDSet(dueIDs)
			for _, row := range live {
				if due[row.ID] && (!row.ExpiresTS.After(before[row.ID]) || !row.ExpiresTS.After(time.Now().Add(lead))) {
					return 0, errors.Join(renewalErr, fmt.Errorf("reservation step %q lease %d renewal did not restore its protection window", record.StepID, row.ID))
				}
			}
		}
		changed := false
		for _, row := range live {
			if deadline, ok := ctx.Deadline(); !ok || row.ExpiresTS.Before(deadline) {
				var cancel context.CancelFunc
				ctx, cancel = context.WithDeadline(ctx, row.ExpiresTS.Time)
				defer cancel()
			}
			if delay := time.Until(row.ExpiresTS.Add(-lead)); delay < next {
				next = delay
			}
			for i := range record.Granted {
				if record.Granted[i].ID == row.ID && !record.Granted[i].ExpiresTS.Equal(row.ExpiresTS.Time) {
					record.Granted[i].ExpiresTS = row.ExpiresTS
					changed = true
				}
			}
		}
		if changed {
			if err := e.saveReservationStep(record); err != nil {
				return 0, err
			}
		}
	}
	if next < 10*time.Millisecond {
		next = 10 * time.Millisecond
	}
	return next, ctx.Err()
}

func resumeResetsReservationStep(state *ExecutionState, opts ResumeOptions, id string) bool {
	if !opts.KeepState {
		return true
	}
	if opts.Mode == ResumeModeForceIter {
		return iterationIndexFromID(id, opts.StepID+"_iter") >= opts.Iteration
	}
	if opts.Mode != ResumeModeRestartFailed {
		return false
	}
	if record, ok := state.ReservationSteps[id]; ok {
		return record.Kind == StepKindReservationAcquire && record.Result.Status != StatusCompleted
	}
	_, inFlight := state.InFlightSteps[id]
	return inFlight || shouldRerunStep(state.Steps[id])
}

// Pure admission runs even in reset mode, before any receipt is discarded.
func (e *Executor) validateReservationResumeLocked(state *ExecutionState, opts ResumeOptions, workflow *Workflow) error {
	identities := make(map[[2]int]agentmail.FileReservation)
	for id, record := range state.ReservationSteps {
		if err := validateReservationStep(id, record); err != nil {
			return err
		}
		if record.Status != reservationAcquiring && record.Status != reservationUncertain && record.Status != reservationRejected {
			for _, grant := range record.Granted {
				key := [2]int{grant.ProjectID, grant.ID}
				if prior, exists := identities[key]; exists && !sameReservationLease(prior, grant) {
					return fmt.Errorf("reservation step %q has conflicting captured identity for lease %d", id, grant.ID)
				}
				identities[key] = grant
			}
		}
		reset := resumeResetsReservationStep(state, opts, id)
		if !reset && (record.Status == reservationAcquiring || record.Status == reservationUncertain) {
			return reservationRecoveryError(id)
		}
		if reset && opts.KeepState && opts.Mode != ResumeModeForceIter {
			if err := e.validateLeafReplayScopeLocked(workflow, state, id, record.Kind); err != nil {
				return err
			}
		}
	}
	if !opts.KeepState {
		return nil
	}
	graph := e.graph
	if graph == nil && workflow != nil {
		graph = NewDependencyGraph(workflow)
	}
	resolve, err := resumeRuntimeStepResolver(graph, state)
	if err != nil {
		return err
	}
	ids := make(map[string]bool)
	for id := range state.Steps {
		ids[id] = true
	}
	for id := range state.InFlightSteps {
		ids[id] = true
	}
	for id := range ids {
		if _, ok := state.ReservationSteps[id]; ok || resumeResetsReservationStep(state, opts, id) {
			continue
		}
		acquisition := state.InFlightSteps[id].Kind == StepKindReservationAcquire
		if step, ok := resolve(id); ok {
			acquisition = acquisition || step.FileReservationPaths != nil
		}
		if acquisition && state.Steps[id].Status != StatusSkipped {
			return fmt.Errorf("reservation step %q has no durable lease identity; inspect live reservations and explicitly reset before repeating it", id)
		}
	}
	return nil
}

// Admission performs only read-only remote checks. It must run before resume
// can refresh a checkpoint, discard a receipt, or dispatch an unrelated sibling.
func (e *Executor) verifyReservationResume(ctx context.Context, workflow *Workflow, options ResumeOptions) error {
	opts, err := normalizeResumeOptions(options)
	if err != nil {
		return err
	}
	state := e.snapshotState()
	if state == nil {
		return fmt.Errorf("resume state is nil")
	}
	if err := e.validateReservationResumeLocked(state, opts, workflow); err != nil {
		return err
	}
	if len(state.ReservationSteps) == 0 {
		// No live reservation read is needed. Preserve the executor's normal
		// cancellation lifecycle, including authored on_cancel cleanup.
		return nil
	}
	ids := make([]string, 0, len(state.ReservationSteps))
	for id := range state.ReservationSteps {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	// Explicit iteration rewind reopens main work even when every previous
	// result is completed. Admission must use the requested replay scope.
	reopensMain := opts.Mode == ResumeModeForceIter || !opts.KeepState
	if workflow != nil {
		for _, step := range workflow.Steps {
			if result, found := state.Steps[step.ID]; !found || shouldRerunStep(result) || resumeResultInFlight(state, step.ID, result) {
				if !reservationReleaseOnlyStep(step) {
					reopensMain = true
				}
			}
		}
	}
	for _, id := range ids {
		record := state.ReservationSteps[id]
		reset := resumeResetsReservationStep(state, opts, id)
		if !reset {
			if err := validateRecoveryWorkflowStep(workflow, state, id, record.Kind, record.StepHash, agentStepHash); err != nil {
				return err
			}
		}
		if record.Status == reservationRejected {
			continue
		}
		if record.Status == reservationAcquiring || record.Status == reservationUncertain {
			// Explicit replay is the operator's decision for unknown effects.
			// Reported but unverified IDs never grant release authority.
			continue
		}
		if record.Kind == StepKindReservationRelease {
			if record.Status == reservationReleasing {
				active, err := e.verifyReservationLeases(ctx, record, record.Granted, true)
				if err != nil {
					return err
				}
				if reset && len(active) > 0 {
					return fmt.Errorf("reservation release %q still owns active lease IDs %v; finish its release before resetting its evidence", id, active)
				}
			}
			continue
		}
		released := reservationIDSet(record.ReleasedIDs)
		if !reset && opts.Mode == ResumeModeForceIter && len(released) > 0 {
			return fmt.Errorf("reservation step %q already released protection outside the requested iteration replay; use a whole-workflow reset to reacquire before repeating protected work", id)
		}
		var remaining []agentmail.FileReservation
		for _, grant := range record.Granted {
			if released[grant.ID] {
				continue
			}
			coveredByRelease := false
			for releaseID, release := range state.ReservationSteps {
				if release.Kind != StepKindReservationRelease || release.Status != reservationReleasing && release.Status != reservationReleased ||
					release.Request.AgentName != record.Request.AgentName || !agentmail.ProjectKeysEquivalent(release.Request.ProjectKey, record.Request.ProjectKey) {
					continue
				}
				selectedByThisRelease := false
				for _, selected := range release.Granted {
					if selected.ID == grant.ID {
						if !sameReservationLease(selected, grant) {
							return fmt.Errorf("reservation release %q does not match the captured identity of lease %d from step %q", releaseID, grant.ID, id)
						}
						coveredByRelease = true
						selectedByThisRelease = true
					}
				}
				if selectedByThisRelease && !reset && reopensMain && reservationCleanupStep(workflow, releaseID) {
					return fmt.Errorf("reservation step %q was released by cleanup while main work remains; use an explicit whole-workflow reset to reacquire protection before resuming", id)
				}
			}
			if !coveredByRelease || reset {
				remaining = append(remaining, grant)
			}
		}
		// Cleanup releases already confirmed on disk also end the old
		// attempt's protection. Do not reuse a completed acquisition for new work.
		if !reset && reopensMain && len(released) > 0 {
			for releaseID, release := range state.ReservationSteps {
				if release.Kind != StepKindReservationRelease || release.Request.AgentName != record.Request.AgentName || !reservationCleanupStep(workflow, releaseID) {
					continue
				}
				for _, grant := range release.Granted {
					if released[grant.ID] && agentmail.ProjectKeysEquivalent(release.Request.ProjectKey, record.Request.ProjectKey) {
						return fmt.Errorf("reservation step %q was released by cleanup while main work remains; use an explicit whole-workflow reset to reacquire protection before resuming", id)
					}
				}
			}
		}
		cleanupOnly := !reopensMain && reservationOnlyReleaseTail(workflow, state)
		active, err := e.verifyReservationLeases(ctx, record, remaining, reset || record.Status == reservationPartial || cleanupOnly)
		if err != nil {
			return err
		}
		if reset && len(active) > 0 {
			return fmt.Errorf("reservation step %q still owns active lease IDs %v; explicitly release those leases before resetting acquisition evidence", id, active)
		}
	}
	return ctx.Err()
}

func reservationOnlyReleaseTail(workflow *Workflow, state *ExecutionState) bool {
	if workflow == nil {
		return false
	}
	for i := range workflow.PostPipelineSteps {
		step := workflow.PostPipelineSteps[i]
		id := step.ID
		if id == "" {
			id = fmt.Sprintf("post_pipeline_%d", i+1)
		}
		if result, exists := state.Steps[id]; exists && !shouldRerunStep(result) && !resumeResultInFlight(state, id, result) {
			continue
		}
		if !reservationReleaseOnlyStep(step) {
			return false
		}
	}
	return true
}

func reservationReleaseOnlyStep(step Step) bool {
	return step.FileReservationRelease != nil && len(step.OnSuccess) == 0 && len(step.OnFailure.Fallback) == 0
}

// A pending release may be the last main step, including after its server
// response was lost. It must be allowed to reconcile absent original IDs.
// Ordinary unfinished work and release hooks that dispatch work still require
// protection, even when all other main steps have already completed.
func reservationOnlyCleanupRemaining(workflow *Workflow, state *ExecutionState) bool {
	if workflow == nil || state == nil {
		return false
	}
	for _, step := range workflow.Steps {
		if result, exists := state.Steps[step.ID]; exists && !shouldRerunStep(result) && !resumeResultInFlight(state, step.ID, result) {
			continue
		}
		if !reservationReleaseOnlyStep(step) {
			return false
		}
	}
	return reservationOnlyReleaseTail(workflow, state)
}

func reservationCleanupStep(workflow *Workflow, id string) bool {
	if _, ok := resolveOnCancelRuntimeStep(workflow, id); ok {
		return true
	}
	if workflow != nil {
		for i := range workflow.PostPipelineSteps {
			if _, ok := resolveHookRuntimeStep(&workflow.PostPipelineSteps[i], "post_pipeline", i+1, id); ok {
				return true
			}
		}
	}
	return false
}

// hasMailStep reports whether the step is configured as any of the Agent Mail
// dispatch kinds. The runtime executor uses this to short-circuit the prompt
// dispatch path so Agent Mail work executes through MCP instead of falling
// through to pane prompt validation (bd-hz1tl).
func (s *Step) hasMailStep() bool {
	if s == nil {
		return false
	}
	return s.MailSend != nil ||
		s.FileReservationPaths != nil ||
		s.MailInboxCheck != nil ||
		s.FileReservationRelease != nil
}

type agentMailClientCache struct {
	mu        sync.Mutex
	byProject map[string]*agentmail.Client
}

func (c *agentMailClientCache) get(projectKey string) *agentmail.Client {
	c.mu.Lock()
	defer c.mu.Unlock()

	if c.byProject == nil {
		c.byProject = make(map[string]*agentmail.Client)
	}
	if client := c.byProject[projectKey]; client != nil {
		return client
	}

	client := agentmail.NewClient(agentmail.WithProjectKey(projectKey))
	c.byProject[projectKey] = client
	return client
}

func (e *Executor) mailClient(projectKey string) *agentmail.Client {
	return e.mailClients.get(projectKey)
}

// executeMailStep is the executor dispatch branch for Agent Mail step kinds.
// These steps execute through MCP Agent Mail rather than tmux pane dispatch.
func (e *Executor) executeMailStep(ctx context.Context, step *Step) StepResult {
	now := time.Now()
	result := StepResult{
		StepID:    step.ID,
		Status:    StatusRunning,
		StartedAt: now,
	}

	if err := ctx.Err(); err != nil {
		result.Status = StatusCancelled
		result.FinishedAt = time.Now()
		result.SkipKind = SkipKindCancelled
		result.SkipReason = err.Error()
		return result
	}

	if e.config.DryRun {
		return e.completeMailStep(step, result, "dry_run", map[string]interface{}{
			"action": "dry_run",
			"kinds":  step.mailStepKindNames(),
		})
	}

	switch {
	case step.MailSend != nil:
		return e.executeMailSendStep(ctx, step, result)
	case step.FileReservationPaths != nil:
		return e.executeFileReservationPathsStep(ctx, step, result)
	case step.MailInboxCheck != nil:
		return e.executeMailInboxCheckStep(ctx, step, result)
	case step.FileReservationRelease != nil:
		return e.executeFileReservationReleaseStep(ctx, step, result)
	default:
		result.Status = StatusFailed
		result.FinishedAt = time.Now()
		result.Error = &StepError{
			Type:      "agent_mail",
			Message:   "Agent Mail step has no configured kind",
			Timestamp: time.Now(),
		}
		return result
	}
}

func (e *Executor) executeMailSendStep(ctx context.Context, step *Step, result StepResult) StepResult {
	stepHash, err := agentStepHash(step)
	if err != nil {
		return failMailStep(result, "recovery", err.Error())
	}
	if prior, ok := e.loadMailSend(step.ID); ok {
		if err := validateMailSend(step.ID, prior); err != nil {
			return failMailStep(result, "recovery", err.Error())
		}
		if prior.StepHash != stepHash {
			return failMailStep(result, "recovery", "saved mail_send receipt belongs to a different step definition; restore the original workflow or explicitly reset it")
		}
		switch prior.Status {
		case mailSendSending:
			return failMailStep(result, "delivery_unknown", uncertainMailSendError(step.ID).Error())
		case mailSendSent:
			return prior.Result
		}
	}
	send := step.MailSend
	projectKey, err := e.resolveMailStepString(ctx, send.ProjectKey)
	if err != nil {
		return failMailStep(result, "substitution", fmt.Sprintf("mail_send project_key: %v", err))
	}
	agentName, err := e.resolveMailStepString(ctx, send.AgentName)
	if err != nil {
		return failMailStep(result, "substitution", fmt.Sprintf("mail_send agent_name: %v", err))
	}
	to, err := e.resolveMailStepList(ctx, send.To)
	if err != nil {
		return failMailStep(result, "substitution", fmt.Sprintf("mail_send to: %v", err))
	}
	subject, err := e.resolveMailStepString(ctx, send.Subject)
	if err != nil {
		return failMailStep(result, "substitution", fmt.Sprintf("mail_send subject: %v", err))
	}
	body, err := e.resolveMailStepString(ctx, send.Body)
	if err != nil {
		return failMailStep(result, "substitution", fmt.Sprintf("mail_send body: %v", err))
	}
	threadID, err := e.resolveMailStepString(ctx, send.ThreadID)
	if err != nil {
		return failMailStep(result, "substitution", fmt.Sprintf("mail_send thread_id: %v", err))
	}

	if e.stopBeforeDispatch(ctx, &result) {
		return result
	}
	record := MailSendState{Version: mailSendVersion, StepID: step.ID, StepHash: stepHash, Status: mailSendSending, StartedAt: result.StartedAt}
	if err := e.saveMailSend(record); err != nil {
		return failMailStep(result, "checkpoint", err.Error())
	}
	if e.stopBeforeDispatch(ctx, &result) {
		// No request has been made; retain proof that a later attempt may send.
		record.Status, record.FinishedAt, record.Result = mailSendRejected, time.Now(), result
		_ = e.saveMailSend(record)
		return result
	}
	client := e.mailClient(projectKey)
	sent, err := client.SendMessage(ctx, agentmail.SendMessageOptions{
		ProjectKey:  projectKey,
		SenderName:  agentName,
		To:          to,
		Subject:     subject,
		BodyMD:      body,
		ThreadID:    threadID,
		AckRequired: send.AckRequired,
	})
	if err != nil {
		result = failMailStep(result, "agent_mail", fmt.Sprintf("mail_send failed: %v", err))
		if agentmail.IsToolRejection(err) {
			// An explicit MCP rejection proves no message was sent. Persist
			// that proof before permitting the existing step retry policy.
			record.Status, record.FinishedAt, record.Result = mailSendRejected, time.Now(), result
			if err := e.saveMailSend(record); err != nil {
				return failMailStep(result, "checkpoint", err.Error())
			}
			return result
		}
		return failMailStep(result, "delivery_unknown", fmt.Sprintf("%v: %v", uncertainMailSendError(step.ID), err))
	}
	if sent == nil || sent.Count <= 0 || len(sent.Deliveries) == 0 {
		return failMailStep(result, "delivery_unknown", fmt.Sprintf("%v: server returned no delivery receipt", uncertainMailSendError(step.ID)))
	}
	for _, delivery := range sent.Deliveries {
		if delivery.Payload == nil || delivery.Payload.ID <= 0 {
			return failMailStep(result, "delivery_unknown", fmt.Sprintf("%v: server returned an incomplete delivery receipt", uncertainMailSendError(step.ID)))
		}
	}
	result = e.completeMailStep(step, result, "mail_send", map[string]interface{}{
		"action":    "mail_send",
		"result":    sent,
		"sent":      sent.Count,
		"thread_id": threadID,
	})
	// A parsing failure is not a failed send. Preserve it as a sent outcome
	// so retry/resume cannot resend an already-delivered message to fix parsing.
	record.Status, record.FinishedAt, record.Result = mailSendSent, time.Now(), result
	if err := e.saveMailSend(record); err != nil {
		return failMailStep(result, "checkpoint", err.Error())
	}
	return result
}

func (e *Executor) executeFileReservationPathsStep(ctx context.Context, step *Step, result StepResult) StepResult {
	stepHash, err := agentStepHash(step)
	if err != nil {
		return failMailStep(result, "recovery", err.Error())
	}
	if prior, ok := e.loadReservationStep(step.ID); ok {
		if err := validateReservationStep(step.ID, prior); err != nil {
			return failMailStep(result, "recovery", err.Error())
		}
		if prior.StepHash != stepHash || prior.Kind != StepKindReservationAcquire {
			return failMailStep(result, "recovery", "saved reservation receipt belongs to a different step definition")
		}
		switch prior.Status {
		case reservationHeld:
			released := reservationIDSet(prior.ReleasedIDs)
			var remaining []agentmail.FileReservation
			for _, grant := range prior.Granted {
				if !released[grant.ID] {
					remaining = append(remaining, grant)
				}
			}
			if _, err := e.verifyReservationLeases(ctx, prior, remaining, false); err != nil {
				return failMailStep(result, "reservation_expired", err.Error())
			}
			return prior.Result
		case reservationReleased:
			return prior.Result
		case reservationAcquiring, reservationUncertain, reservationPartial:
			result.Output = prior.Result.Output
			return failMailStep(result, "reservation_unknown", reservationRecoveryError(step.ID).Error())
		}
	}
	reserve := step.FileReservationPaths
	projectKey, err := e.resolveMailStepString(ctx, reserve.ProjectKey)
	if err != nil {
		return failMailStep(result, "substitution", fmt.Sprintf("file_reservation_paths project_key: %v", err))
	}
	agentName, err := e.resolveMailStepString(ctx, reserve.AgentName)
	if err != nil {
		return failMailStep(result, "substitution", fmt.Sprintf("file_reservation_paths agent_name: %v", err))
	}
	paths, err := e.resolveMailStepList(ctx, reserve.Paths)
	if err != nil {
		return failMailStep(result, "substitution", fmt.Sprintf("file_reservation_paths paths: %v", err))
	}
	reason, err := e.resolveMailStepString(ctx, reserve.Reason)
	if err != nil {
		return failMailStep(result, "substitution", fmt.Sprintf("file_reservation_paths reason: %v", err))
	}

	record := ReservationStepState{
		Version: reservationStepVersion, StepID: step.ID, StepHash: stepHash, Kind: StepKindReservationAcquire,
		Status: reservationAcquiring, StartedAt: result.StartedAt,
		Request: FileReservationPathsStep{ProjectKey: projectKey, AgentName: agentName, Paths: StringOrList(paths),
			TTLSeconds: reserve.TTLSeconds, Exclusive: reserve.Exclusive, Reason: reason},
	}
	if e.stopBeforeDispatch(ctx, &result) {
		return result
	}
	if err := e.saveReservationStep(record); err != nil {
		return failMailStep(result, "checkpoint", err.Error())
	}
	if e.stopBeforeDispatch(ctx, &result) {
		record.Status, record.FinishedAt, record.Result = reservationRejected, time.Now(), result
		_ = e.saveReservationStep(record)
		return result
	}
	reservation, err := e.mailClient(projectKey).ReservePaths(ctx, agentmail.FileReservationOptions{
		ProjectKey: projectKey,
		AgentName:  agentName,
		Paths:      paths,
		TTLSeconds: reserve.TTLSeconds,
		Exclusive:  reserve.Exclusive,
		Reason:     reason,
	})
	if reservation != nil {
		record.Granted, record.Conflicts = reservation.Granted, reservation.Conflicts
	}
	result = e.completeMailStep(step, result, "file_reservation_paths", map[string]interface{}{
		"action":         "file_reservation_paths",
		"result":         reservation,
		"granted_count":  len(record.Granted),
		"conflict_count": len(record.Conflicts),
	})
	record.Status = reservationHeld
	if err != nil {
		record.Status = reservationUncertain
		if agentmail.IsToolRejection(err) || errors.Is(err, agentmail.ErrReservationConflict) && !errors.Is(err, agentmail.ErrReservationUnverified) {
			record.Status = reservationRejected
			if len(record.Granted) > 0 {
				record.Status = reservationPartial
			}
		}
		result = failMailStep(result, "agent_mail", fmt.Sprintf("file_reservation_paths failed: %v", err))
		if record.Status == reservationUncertain {
			result = failMailStep(result, "reservation_unknown", fmt.Sprintf("%v: %v", reservationRecoveryError(step.ID), err))
		}
	}
	record.FinishedAt, record.Result = time.Now(), result
	if err := validateReservationStep(step.ID, record); err != nil {
		// A response without durable identities is still valuable inspection
		// evidence, but cannot authorize work, release, or another acquisition.
		record.Status = reservationUncertain
		result = failMailStep(result, "reservation_unknown", fmt.Sprintf("%v: %v", reservationRecoveryError(step.ID), err))
		record.Result = result
	}
	if err := e.saveReservationStep(record); err != nil {
		return failMailStep(result, "checkpoint", err.Error())
	}
	return result
}

func (e *Executor) executeMailInboxCheckStep(ctx context.Context, step *Step, result StepResult) StepResult {
	inbox := step.MailInboxCheck
	projectKey, err := e.resolveMailStepString(ctx, inbox.ProjectKey)
	if err != nil {
		return failMailStep(result, "substitution", fmt.Sprintf("mail_inbox_check project_key: %v", err))
	}
	agentName, err := e.resolveMailStepString(ctx, inbox.AgentName)
	if err != nil {
		return failMailStep(result, "substitution", fmt.Sprintf("mail_inbox_check agent_name: %v", err))
	}

	client := e.mailClient(projectKey)
	messages, err := client.FetchInbox(ctx, agentmail.FetchInboxOptions{
		ProjectKey: projectKey,
		AgentName:  agentName,
	})
	if err != nil {
		return failMailStep(result, "agent_mail", fmt.Sprintf("mail_inbox_check failed: %v", err))
	}

	ackRequired := 0
	for _, msg := range messages {
		if msg.AckRequired {
			ackRequired++
		}
	}

	return e.completeMailStep(step, result, "mail_inbox_check", map[string]interface{}{
		"action":              "mail_inbox_check",
		"messages":            messages,
		"message_count":       len(messages),
		"ack_required_count":  ackRequired,
		"until_ack_count":     inbox.UntilAckCount,
		"until_ack_count_met": inbox.UntilAckCount == 0 || ackRequired >= inbox.UntilAckCount,
	})
}

func (e *Executor) executeFileReservationReleaseStep(ctx context.Context, step *Step, result StepResult) StepResult {
	ctx = context.WithValue(ctx, reservationReleaseKey{}, true)
	e.reservationMu.Lock()
	defer e.reservationMu.Unlock()
	// Release excludes heartbeat mutation. Bound that exclusion so a slow
	// release cannot starve renewal of a different still-active acquisition.
	budget := 30 * time.Second
	e.stateMu.RLock()
	if e.state != nil {
		for _, acquisition := range e.state.ReservationSteps {
			if acquisition.Kind != StepKindReservationAcquire || acquisition.Status != reservationHeld {
				continue
			}
			released := reservationIDSet(acquisition.ReleasedIDs)
			for _, grant := range acquisition.Granted {
				if remaining := time.Until(grant.ExpiresTS.Time); !released[grant.ID] && remaining > 0 && remaining < budget {
					budget = remaining
				}
			}
		}
	}
	e.stateMu.RUnlock()
	ctx, cancel := context.WithTimeout(ctx, budget)
	defer cancel()
	stepHash, err := agentStepHash(step)
	if err != nil {
		return failMailStep(result, "recovery", err.Error())
	}
	if prior, ok := e.loadReservationStep(step.ID); ok {
		if err := validateReservationStep(step.ID, prior); err != nil {
			return failMailStep(result, "recovery", err.Error())
		}
		if prior.StepHash != stepHash || prior.Kind != StepKindReservationRelease {
			return failMailStep(result, "recovery", "saved release receipt belongs to a different step definition")
		}
		if prior.Status == reservationReleased {
			return prior.Result
		}
		return e.executeRecordedReservationRelease(ctx, step, result, prior)
	}
	release := step.FileReservationRelease
	projectKey, err := e.resolveMailStepString(ctx, release.ProjectKey)
	if err != nil {
		return failMailStep(result, "substitution", fmt.Sprintf("file_reservation_release project_key: %v", err))
	}
	agentName, err := e.resolveMailStepString(ctx, release.AgentName)
	if err != nil {
		return failMailStep(result, "substitution", fmt.Sprintf("file_reservation_release agent_name: %v", err))
	}
	paths, err := e.resolveMailStepList(ctx, release.Paths)
	if err != nil {
		return failMailStep(result, "substitution", fmt.Sprintf("file_reservation_release paths: %v", err))
	}
	record := ReservationStepState{
		Version: reservationStepVersion, StepID: step.ID, StepHash: stepHash, Kind: StepKindReservationRelease,
		Status: reservationReleasing, StartedAt: result.StartedAt,
		Request: FileReservationPathsStep{ProjectKey: projectKey, AgentName: agentName, Paths: StringOrList(paths)},
	}
	scoped, grants, err := e.selectReservationRelease(record.Request)
	if err != nil {
		return failMailStep(result, "reservation_ownership", err.Error())
	}
	if scoped {
		record.Granted = grants
		return e.executeRecordedReservationRelease(ctx, step, result, record)
	}

	// Explicit standalone release steps retain their existing path semantics.
	// Once this run has acquisition evidence, an unmatched project/agent
	// cannot authorize a path fallback through an ambiguous project alias.
	client := e.mailClient(projectKey)
	released, err := client.ReleaseReservations(ctx, projectKey, agentName, paths, nil)
	if err != nil {
		return failMailStep(result, "agent_mail", fmt.Sprintf("file_reservation_release failed: %v", err))
	}

	return e.completeMailStep(step, result, "file_reservation_release", map[string]interface{}{
		"action": "file_reservation_release",
		"result": released,
	})
}

func (e *Executor) selectReservationRelease(request FileReservationPathsStep) (bool, []agentmail.FileReservation, error) {
	state := e.snapshotState()
	if state == nil {
		return false, nil, nil
	}
	wanted, matched := make(map[string]bool), make(map[string]bool)
	for _, path := range request.Paths {
		wanted[path] = true
	}
	scoped := false
	byID := make(map[int]agentmail.FileReservation)
	for id, record := range state.ReservationSteps {
		if record.Kind != StepKindReservationAcquire || record.Request.AgentName != request.AgentName ||
			!agentmail.ProjectKeysEquivalent(record.Request.ProjectKey, request.ProjectKey) {
			continue
		}
		scoped = true
		if err := validateReservationStep(id, record); err != nil {
			return true, nil, err
		}
		selected := false
		for _, path := range record.Request.Paths {
			if wanted[path] {
				matched[path], selected = true, true
			}
		}
		if !selected {
			continue
		}
		if record.Status == reservationAcquiring || record.Status == reservationUncertain {
			return true, nil, fmt.Errorf("reservation step %q has unverified grants; inspect them before releasing, without adopting leases by path", id)
		}
		released := reservationIDSet(record.ReleasedIDs)
		for _, grant := range record.Granted {
			if wanted[grant.PathPattern] && !released[grant.ID] {
				if prior, exists := byID[grant.ID]; exists && !sameReservationLease(prior, grant) {
					return true, nil, fmt.Errorf("reservation ID %d has conflicting acquisition identities", grant.ID)
				}
				byID[grant.ID] = grant
			}
		}
	}
	if scoped && len(matched) != len(wanted) {
		return true, nil, fmt.Errorf("release paths do not all belong to this run's recorded acquisitions; refusing to release another run's reservations by path")
	}
	if !scoped && len(state.ReservationSteps) > 0 {
		return true, nil, fmt.Errorf("release scope does not match this run's recorded acquisitions; use the original project key and agent name instead of an ambiguous path-based release")
	}
	grants := make([]agentmail.FileReservation, 0, len(byID))
	for _, grant := range byID {
		grants = append(grants, grant)
	}
	sort.Slice(grants, func(i, j int) bool { return grants[i].ID < grants[j].ID })
	return scoped, grants, nil
}

func sameReservationLease(a, b agentmail.FileReservation) bool {
	return a.ID == b.ID && a.ProjectID == b.ProjectID && a.AgentName == b.AgentName && a.PathPattern == b.PathPattern &&
		a.Exclusive == b.Exclusive && a.Reason == b.Reason && a.CreatedTS.Equal(b.CreatedTS.Time)
}

func (e *Executor) executeRecordedReservationRelease(ctx context.Context, step *Step, result StepResult, record ReservationStepState) StepResult {
	active, err := e.verifyReservationLeases(ctx, record, record.Granted, true)
	if err != nil {
		return failMailStep(result, "reservation_ownership", err.Error())
	}
	if e.stopBeforeDispatch(ctx, &result) {
		return result
	}
	if len(record.Granted) > 0 {
		if err := e.saveReservationStep(record); err != nil {
			return failMailStep(result, "checkpoint", err.Error())
		}
	}
	var released *agentmail.ReleaseReservationsResult
	var releaseErr error
	if len(active) > 0 {
		if e.stopBeforeDispatch(ctx, &result) {
			return result
		}
		released, releaseErr = e.mailClient(record.Request.ProjectKey).ReleaseReservations(ctx, record.Request.ProjectKey, record.Request.AgentName, nil, active)
	}
	// A count is not settlement. The exact original IDs must be absent from
	// independent readback, even if the mutation response itself was lost.
	remaining, readErr := e.verifyReservationLeases(ctx, record, record.Granted, true)
	if readErr != nil || len(remaining) > 0 {
		failure := errors.Join(releaseErr, readErr)
		if len(remaining) > 0 {
			failure = errors.Join(failure, fmt.Errorf("lease IDs %v remain active", remaining))
		}
		result = failMailStep(result, "reservation_unknown", fmt.Sprintf("reservation release %q is not verified; resume will reconcile only its captured IDs: %v", step.ID, failure))
		record.Result, record.FinishedAt = result, time.Now()
		if err := e.saveReservationStep(record); err != nil {
			return failMailStep(result, "checkpoint", err.Error())
		}
		return result
	}
	ids := make([]int, len(record.Granted))
	for i, grant := range record.Granted {
		ids[i] = grant.ID
	}
	// If no response survived, publish the independently verified absence
	// explicitly rather than inventing the server's release timestamp.
	recovered := released == nil
	if recovered {
		released = &agentmail.ReleaseReservationsResult{Released: len(ids)}
	}
	result = e.completeMailStep(step, result, "file_reservation_release", map[string]interface{}{
		"action": "file_reservation_release", "result": released, "verified_released_ids": ids, "recovered": recovered,
	})
	record.Status, record.FinishedAt, record.Result = reservationReleased, time.Now(), result
	if err := e.saveReservationRelease(record); err != nil {
		return failMailStep(result, "checkpoint", err.Error())
	}
	return result
}

func (e *Executor) saveReservationRelease(record ReservationStepState) error {
	if err := validateReservationStep(record.StepID, record); err != nil {
		return err
	}
	e.stateMu.Lock()
	if e.state == nil {
		e.stateMu.Unlock()
		return fmt.Errorf("cannot settle reservation release without execution state")
	}
	if e.state.ReservationSteps == nil {
		e.state.ReservationSteps = make(map[string]ReservationStepState)
	}
	for id, acquisition := range e.state.ReservationSteps {
		if acquisition.Kind != StepKindReservationAcquire || acquisition.Status != reservationHeld && acquisition.Status != reservationPartial && acquisition.Status != reservationReleased ||
			acquisition.Request.AgentName != record.Request.AgentName || !agentmail.ProjectKeysEquivalent(acquisition.Request.ProjectKey, record.Request.ProjectKey) {
			continue
		}
		released := reservationIDSet(acquisition.ReleasedIDs)
		for _, grant := range acquisition.Granted {
			for _, selected := range record.Granted {
				if sameReservationLease(grant, selected) && !released[grant.ID] {
					acquisition.ReleasedIDs = append(acquisition.ReleasedIDs, grant.ID)
					released[grant.ID] = true
				}
			}
		}
		if len(released) == len(acquisition.Granted) {
			acquisition.Status = reservationReleased
		}
		e.state.ReservationSteps[id] = cloneReservationStep(acquisition)
	}
	e.state.ReservationSteps[record.StepID] = cloneReservationStep(record)
	e.stateMu.Unlock()
	return e.persistState()
}

func (e *Executor) resolveMailStepString(ctx context.Context, value string) (string, error) {
	if value == "" {
		return "", nil
	}
	return e.substituteVariablesStrictCtx(ctx, value)
}

func (e *Executor) resolveMailStepList(ctx context.Context, values StringOrList) ([]string, error) {
	resolved := make([]string, len(values))
	for i, value := range values {
		item, err := e.resolveMailStepString(ctx, value)
		if err != nil {
			return nil, fmt.Errorf("item %d: %w", i, err)
		}
		resolved[i] = item
	}
	return resolved, nil
}

func (e *Executor) completeMailStep(step *Step, result StepResult, action string, payload interface{}) StepResult {
	output, err := json.Marshal(payload)
	if err != nil {
		return failMailStep(result, "agent_mail", fmt.Sprintf("%s output marshal failed: %v", action, err))
	}

	result.Status = StatusCompleted
	result.Output = string(output)
	result.FinishedAt = time.Now()

	if step.OutputParse.Type != "" && step.OutputParse.Type != "none" {
		parsed, err := e.parseOutput(result.Output, step.OutputParse)
		if err != nil {
			return failMailStep(result, "output_parse", fmt.Sprintf("failed to parse mail step output: %v", err))
		}
		result.ParsedData = parsed
	}

	return result
}

func failMailStep(result StepResult, errType, message string) StepResult {
	result.Status = StatusFailed
	result.FinishedAt = time.Now()
	result.Error = &StepError{
		Type:      errType,
		Message:   message,
		Timestamp: time.Now(),
	}
	return result
}

func (s *Step) mailStepKindNames() []string {
	if s == nil {
		return nil
	}

	var names []string
	if s.MailSend != nil {
		names = append(names, "mail_send")
	}
	if s.FileReservationPaths != nil {
		names = append(names, "file_reservation_paths")
	}
	if s.MailInboxCheck != nil {
		names = append(names, "mail_inbox_check")
	}
	if s.FileReservationRelease != nil {
		names = append(names, "file_reservation_release")
	}
	return names
}

// validateMailStepPayload reports per-kind required-field errors for the
// Agent Mail step kinds (bd-vv7ij). Mutual-exclusion checks live in
// Validate; this only fires when exactly one mail step kind is set.
func validateMailStepPayload(step *Step, stepField string, result *ValidationResult) {
	if step == nil {
		return
	}

	if send := step.MailSend; send != nil {
		field := stepField + ".mail_send"
		if strings.TrimSpace(send.ProjectKey) == "" {
			result.addError(ParseError{
				Field:   field + ".project_key",
				Message: "mail_send requires project_key",
				Hint:    "Set the absolute project root (e.g. /data/projects/ntm) so MCP Agent Mail can scope the send.",
			})
		}
		if strings.TrimSpace(send.AgentName) == "" {
			result.addError(ParseError{
				Field:   field + ".agent_name",
				Message: "mail_send requires agent_name",
				Hint:    "Set agent_name to the sender's roster name (e.g. TealCrane).",
			})
		}
		if len(send.To) == 0 {
			result.addError(ParseError{
				Field:   field + ".to",
				Message: "mail_send requires at least one recipient in to",
				Hint:    "Use a string for a single recipient or a list for multiple recipients.",
			})
		} else {
			for i, recipient := range send.To {
				if strings.TrimSpace(recipient) == "" {
					result.addError(ParseError{
						Field:   fmt.Sprintf("%s.to[%d]", field, i),
						Message: "mail_send recipient cannot be empty",
					})
				}
			}
		}
		if strings.TrimSpace(send.Subject) == "" && strings.TrimSpace(send.Body) == "" {
			result.addError(ParseError{
				Field:   field,
				Message: "mail_send requires subject or body",
				Hint:    "Set subject or body so the recipient has something to read.",
			})
		}
	}

	if reserve := step.FileReservationPaths; reserve != nil {
		field := stepField + ".file_reservation_paths"
		if strings.TrimSpace(reserve.ProjectKey) == "" {
			result.addError(ParseError{
				Field:   field + ".project_key",
				Message: "file_reservation_paths requires project_key",
			})
		}
		if strings.TrimSpace(reserve.AgentName) == "" {
			result.addError(ParseError{
				Field:   field + ".agent_name",
				Message: "file_reservation_paths requires agent_name",
			})
		}
		if len(reserve.Paths) == 0 {
			result.addError(ParseError{
				Field:   field + ".paths",
				Message: "file_reservation_paths requires at least one path",
				Hint:    "Use a string for a single path or a list for multiple paths.",
			})
		} else {
			for i, path := range reserve.Paths {
				if strings.TrimSpace(path) == "" {
					result.addError(ParseError{
						Field:   fmt.Sprintf("%s.paths[%d]", field, i),
						Message: "file_reservation_paths path cannot be empty",
					})
				}
			}
		}
		if reserve.TTLSeconds < 0 {
			result.addError(ParseError{
				Field:   field + ".ttl_seconds",
				Message: fmt.Sprintf("file_reservation_paths.ttl_seconds must be non-negative, got %d", reserve.TTLSeconds),
				Hint:    "Use 0 for the server default or a positive integer for the lock TTL in seconds.",
			})
		}
	}

	if inbox := step.MailInboxCheck; inbox != nil {
		field := stepField + ".mail_inbox_check"
		if strings.TrimSpace(inbox.ProjectKey) == "" {
			result.addError(ParseError{
				Field:   field + ".project_key",
				Message: "mail_inbox_check requires project_key",
			})
		}
		if strings.TrimSpace(inbox.AgentName) == "" {
			result.addError(ParseError{
				Field:   field + ".agent_name",
				Message: "mail_inbox_check requires agent_name",
			})
		}
		if inbox.UntilAckCount < 0 {
			result.addError(ParseError{
				Field:   field + ".until_ack_count",
				Message: fmt.Sprintf("mail_inbox_check.until_ack_count must be non-negative, got %d", inbox.UntilAckCount),
			})
		}
	}

	if release := step.FileReservationRelease; release != nil {
		field := stepField + ".file_reservation_release"
		if strings.TrimSpace(release.ProjectKey) == "" {
			result.addError(ParseError{
				Field:   field + ".project_key",
				Message: "file_reservation_release requires project_key",
			})
		}
		if strings.TrimSpace(release.AgentName) == "" {
			result.addError(ParseError{
				Field:   field + ".agent_name",
				Message: "file_reservation_release requires agent_name",
			})
		}
		if len(release.Paths) == 0 {
			result.addError(ParseError{
				Field:   field + ".paths",
				Message: "file_reservation_release requires at least one path",
				Hint:    "Use a string for a single path or a list for multiple paths.",
			})
		} else {
			for i, path := range release.Paths {
				if strings.TrimSpace(path) == "" {
					result.addError(ParseError{
						Field:   fmt.Sprintf("%s.paths[%d]", field, i),
						Message: "file_reservation_release path cannot be empty",
					})
				}
			}
		}
	}
}
