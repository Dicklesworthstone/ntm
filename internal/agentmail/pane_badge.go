// Package agentmail — pane_badge.go
//
// Reconciliation policy for per-pane Agent Mail identity badges (ntm#312).
//
// The badge shows the name NTM ASSIGNED to a pane (from the session agent
// registry, keyed by stable pane id). It is compared against the canonical
// Agent Mail pane identity file — a legacy plain-text name or the structured
// record the Agent Mail server writes (mcp_agent_mail_rust#252) — and the
// outcome is reduced to a compact state plus a sanitised display label. A
// disagreement retains the assigned name and adds a warning marker; it never
// relabels the pane after whatever a later process wrote to the file.
//
// Everything here is pure policy over already-observed facts: callers supply
// the registry, the tmux pane listing and the identity-file observation, and
// publication is left to the tmux layer.
package agentmail

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"github.com/Dicklesworthstone/ntm/internal/tmux"
	"github.com/Dicklesworthstone/ntm/internal/util"
)

// PaneAssignmentState reports whether NTM's recorded assignment for a pane is
// current.
type PaneAssignmentState string

const (
	// PaneAssignmentCurrent: the registry binds this pane id and, when a pid
	// was recorded, the pane still carries it.
	PaneAssignmentCurrent PaneAssignmentState = "current"
	// PaneAssignmentStale: the registry binds this pane id to a different
	// generation (recorded pid != observed pid). tmux keeps %N across
	// respawn-pane and recycles it across server restarts.
	PaneAssignmentStale PaneAssignmentState = "stale"
	// PaneAssignmentUnobservable: tmux could not be read, so the binding's
	// liveness is unknown.
	PaneAssignmentUnobservable PaneAssignmentState = "unobservable"
	// PaneAssignmentUnregistered: the registry has no binding for the pane.
	PaneAssignmentUnregistered PaneAssignmentState = "unregistered"
)

// PaneObservationState is the outcome of comparing the assignment with the
// canonical identity file.
type PaneObservationState string

const (
	// PaneObservationMatched: structured record, same name, binding facts
	// (session, pane id, pid) agree with tmux.
	PaneObservationMatched PaneObservationState = "matched"
	// PaneObservationLegacyUnverified: plain-text file holding the same
	// name; carries no binding facts to verify.
	PaneObservationLegacyUnverified PaneObservationState = "legacy-unverified"
	// PaneObservationNameDisagreement: the file resolves a different name.
	PaneObservationNameDisagreement PaneObservationState = "name-disagreement"
	// PaneObservationMissingFile: no identity file at any known location.
	PaneObservationMissingFile PaneObservationState = "missing-file"
	// PaneObservationUnreadableFile: the file exists but cannot be read.
	PaneObservationUnreadableFile PaneObservationState = "unreadable-file"
	// PaneObservationInvalidFile: the file is empty, malformed JSON, a
	// symlink, or lacks a name.
	PaneObservationInvalidFile PaneObservationState = "invalid-file"
	// PaneObservationBindingUnverifiable: structured record with the same
	// name but without the fields (or the tmux facts) needed to verify it.
	PaneObservationBindingUnverifiable PaneObservationState = "binding-unverifiable"
	// PaneObservationBindingStale: structured record with the same name
	// whose binding facts identify another pane generation.
	PaneObservationBindingStale PaneObservationState = "binding-stale"
	// PaneObservationSkipped: no comparison was possible (no assignment to
	// compare against, or tmux unobservable).
	PaneObservationSkipped PaneObservationState = "skipped"
)

// PaneLifecycle is the agent process state observed at reconciliation.
type PaneLifecycle string

const (
	PaneLifecycleStarting PaneLifecycle = "starting"
	PaneLifecycleRunning  PaneLifecycle = "running"
	PaneLifecycleExited   PaneLifecycle = "exited"
	PaneLifecycleUnknown  PaneLifecycle = "unknown"
)

// PaneIdentityRecord mirrors the structured identity record written by the
// Agent Mail server (crates/mcp-agent-mail-core/src/pane_identity.rs).
type PaneIdentityRecord struct {
	Name        string `json:"name"`
	SessionName string `json:"session_name,omitempty"`
	PaneID      string `json:"pane_id,omitempty"`
	PanePID     int    `json:"pane_pid,omitempty"`
	SocketPath  string `json:"socket_path,omitempty"`
	WrittenAt   string `json:"written_at,omitempty"`
}

// IdentityObservation is what one read of a pane's identity file yielded.
type IdentityObservation struct {
	// Name is the resolved agent name ("" when Failure is set).
	Name string
	// Path is the file the observation came from ("" when missing).
	Path string
	// Structured is true when the file held a JSON record.
	Structured bool
	// Record is the decoded structured record (zero when !Structured).
	Record PaneIdentityRecord
	// Failure is set when the file could not be observed: missing-file,
	// unreadable-file or invalid-file.
	Failure PaneObservationState
	// Detail explains Failure for status output.
	Detail string
}

// ObservePaneIdentity reads the identity file for paneID under the first of
// projectKeys whose canonical path exists (session key first, then its
// published aliases). When no canonical file exists the legacy locations are
// consulted through ResolveIdentity; a hit there is a plain-text (legacy)
// observation. Access and parse failures are reported as distinct states
// instead of being folded into "no name".
func ObservePaneIdentity(projectKeys []string, paneID string) IdentityObservation {
	var missing IdentityObservation
	for _, key := range projectKeys {
		key = strings.TrimSpace(key)
		if key == "" {
			continue
		}
		obs := observeIdentityPath(CanonicalIdentityPath(key, paneID))
		if obs.Failure != PaneObservationMissingFile {
			return obs
		}
		if missing.Path == "" {
			missing = obs
		}
	}
	for _, key := range projectKeys {
		key = strings.TrimSpace(key)
		if key == "" {
			continue
		}
		if name, path := ResolveIdentity(key, paneID); name != "" {
			// ResolveIdentity already decoded structured files down to the
			// name; a canonical hit was handled above, so this is a legacy
			// location and carries no binding facts.
			return IdentityObservation{Name: name, Path: path}
		}
	}
	if missing.Path == "" {
		missing = IdentityObservation{Failure: PaneObservationMissingFile, Detail: "no identity file"}
	}
	return missing
}

// observeIdentityPath classifies one canonical identity file.
func observeIdentityPath(path string) IdentityObservation {
	info, err := os.Lstat(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return IdentityObservation{Path: path, Failure: PaneObservationMissingFile, Detail: "no identity file at " + path}
		}
		return IdentityObservation{Path: path, Failure: PaneObservationUnreadableFile, Detail: err.Error()}
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() {
		return IdentityObservation{Path: path, Failure: PaneObservationInvalidFile, Detail: "identity path is not a regular file"}
	}
	data, err := os.ReadFile(path) //nolint:gosec // canonical project/pane path
	if err != nil {
		return IdentityObservation{Path: path, Failure: PaneObservationUnreadableFile, Detail: err.Error()}
	}
	trimmed := strings.TrimSpace(string(data))
	if trimmed == "" {
		return IdentityObservation{Path: path, Failure: PaneObservationInvalidFile, Detail: "identity file is empty"}
	}
	if strings.HasPrefix(trimmed, "{") || strings.HasPrefix(trimmed, "[") {
		var record PaneIdentityRecord
		if err := json.Unmarshal([]byte(trimmed), &record); err != nil {
			return IdentityObservation{Path: path, Failure: PaneObservationInvalidFile, Detail: "malformed identity record: " + err.Error()}
		}
		record.Name = strings.TrimSpace(record.Name)
		if record.Name == "" {
			return IdentityObservation{Path: path, Failure: PaneObservationInvalidFile, Detail: "identity record has no name"}
		}
		record.PaneID = strings.TrimSpace(record.PaneID)
		record.SessionName = strings.TrimSpace(record.SessionName)
		record.SocketPath = strings.TrimSpace(record.SocketPath)
		return IdentityObservation{Name: record.Name, Path: path, Structured: true, Record: record}
	}
	return IdentityObservation{Name: trimmed, Path: path}
}

// PaneBindingFacts are the tmux-observed facts a structured record is
// verified against.
type PaneBindingFacts struct {
	SessionName string
	PaneID      string
	PanePID     int
	SocketPath  string
}

// Compare reduces the observation to a state relative to the assigned name.
// bindingVerified is true only for a structured record whose facts agree with
// tmux. problems lists every issue found, so a disagreement does not hide a
// stale binding behind it.
func (o IdentityObservation) Compare(assigned string, facts PaneBindingFacts) (state PaneObservationState, bindingVerified bool, problems []string) {
	if o.Failure != "" {
		return o.Failure, false, []string{string(o.Failure) + ": " + o.Detail}
	}
	assigned = strings.TrimSpace(assigned)
	if o.Name != assigned {
		problems = append(problems, fmt.Sprintf("name-disagreement: identity file resolves %q, NTM assigned %q (%s)", o.Name, assigned, o.Path))
		state = PaneObservationNameDisagreement
	}
	if !o.Structured {
		if state == "" {
			state = PaneObservationLegacyUnverified
		}
		return state, false, problems
	}
	bindingState, detail := o.bindingState(facts)
	if bindingState != PaneObservationMatched {
		problems = append(problems, string(bindingState)+": "+detail)
	}
	if state == "" {
		state = bindingState
	}
	return state, bindingState == PaneObservationMatched, problems
}

func (o IdentityObservation) bindingState(facts PaneBindingFacts) (PaneObservationState, string) {
	r := o.Record
	if r.PaneID == "" || r.PanePID <= 0 || r.SessionName == "" {
		return PaneObservationBindingUnverifiable, "identity record lacks session_name/pane_id/pane_pid binding fields"
	}
	if facts.PanePID <= 0 || facts.PaneID == "" {
		return PaneObservationBindingUnverifiable, "tmux pane pid not observed"
	}
	if r.PaneID != facts.PaneID {
		return PaneObservationBindingStale, fmt.Sprintf("identity record bound to pane %s, this pane is %s", r.PaneID, facts.PaneID)
	}
	if facts.SessionName != "" && r.SessionName != facts.SessionName {
		return PaneObservationBindingStale, fmt.Sprintf("identity record bound to session %q, this pane is in %q", r.SessionName, facts.SessionName)
	}
	if r.PanePID != facts.PanePID {
		return PaneObservationBindingStale, fmt.Sprintf("identity record bound to pane pid %d, pane now reports %d", r.PanePID, facts.PanePID)
	}
	if r.SocketPath != "" && facts.SocketPath != "" && r.SocketPath != facts.SocketPath {
		return PaneObservationBindingStale, fmt.Sprintf("identity record bound to tmux socket %s, this server is %s", r.SocketPath, facts.SocketPath)
	}
	return PaneObservationMatched, ""
}

// PaneBadgeRecord is the full status of one pane's badge as of its last
// reconciliation. Only the sanitised Label reaches tmux; the rest is for
// `ntm mapping` and JSON consumers.
type PaneBadgeRecord struct {
	PaneID      string `json:"pane_id"`
	PaneIndex   int    `json:"pane_index"`
	WindowIndex int    `json:"window_index"`
	// PanePID is the #{pane_pid} observed at reconciliation (0 if unknown).
	PanePID int `json:"pane_pid,omitempty"`
	// RecordedPID is the pid the registry stored at assignment time.
	RecordedPID int `json:"recorded_pid,omitempty"`

	AssignedName     string               `json:"assigned_name,omitempty"`
	ResolvedName     string               `json:"resolved_name,omitempty"`
	Source           string               `json:"source,omitempty"`
	AssignmentState  PaneAssignmentState  `json:"assignment_state"`
	ObservationState PaneObservationState `json:"observation_state"`
	BindingVerified  bool                 `json:"binding_verified"`
	Lifecycle        PaneLifecycle        `json:"lifecycle"`
	// Label is the display label composed for the pane ("" clears the badge).
	Label string `json:"label,omitempty"`
	// State is the compact value written to the pane's state option.
	State string `json:"state"`
	// Problems lists every discrepancy found, preserved side by side.
	Problems []string `json:"problems,omitempty"`

	// LastAttemptAt advances on every reconciliation attempt, including
	// failed ones. LastSuccessAt advances only when the observation
	// completed without an access or observation error — a successfully
	// observed disagreement still counts.
	LastAttemptAt time.Time  `json:"last_attempt_at"`
	LastSuccessAt *time.Time `json:"last_success_at"`

	// Published reports whether this pass's publication to tmux succeeded;
	// PublishError carries the reason when it did not. These describe
	// publication, not reconciliation.
	Published    bool   `json:"published"`
	PublishError string `json:"publish_error,omitempty"`
	// Cached reports whether badge options may still be present on the pane
	// (written by this or an earlier pass and not withdrawn since), so a
	// later disable knows what to clear even after a pass that could not
	// publish.
	Cached bool `json:"cached"`
}

// HasDiscrepancy reports whether the record should appear in a discrepancy
// report: any state other than a current assignment that matched or was
// legacy-unverified.
func (r PaneBadgeRecord) HasDiscrepancy() bool {
	if r.AssignmentState != PaneAssignmentCurrent {
		return true
	}
	switch r.ObservationState {
	case PaneObservationMatched, PaneObservationLegacyUnverified:
		return false
	}
	return true
}

// Observed reports whether the observation completed (advances
// LastSuccessAt).
func (r PaneBadgeRecord) observed() bool {
	if r.AssignmentState == PaneAssignmentUnobservable {
		return false
	}
	switch r.ObservationState {
	case PaneObservationMissingFile, PaneObservationUnreadableFile, PaneObservationInvalidFile:
		return false
	}
	return true
}

// LifecycleFromPane derives the lifecycle of a pane from its tmux facts. A
// retained dead pane or an agent that exited back to its shell is "exited";
// a pane whose foreground command could not be observed is "unknown", never
// proof of exit.
func LifecycleFromPane(pane tmux.Pane) PaneLifecycle {
	if pane.Dead {
		return PaneLifecycleExited
	}
	if strings.TrimSpace(pane.Command) == "" {
		return PaneLifecycleUnknown
	}
	if pane.IdleShell() {
		return PaneLifecycleExited
	}
	return PaneLifecycleRunning
}

// DefaultBadgeTemplate is the default badge label template. Tokens: {name}
// (assigned name, or "?" when unresolved), {drift} ("!" on any discrepancy,
// else empty) and {lifecycle} (" (starting)", " (exited)", " (unknown)", or
// empty while running).
const DefaultBadgeTemplate = "[{name}{drift}]{lifecycle}"

// badgeTemplateTokens are the substitutions a template may use.
var badgeTemplateTokens = []string{"{name}", "{drift}", "{lifecycle}"}

// ValidateBadgeTemplate rejects a template that would not survive
// sanitisation or that does not render the assigned name.
func ValidateBadgeTemplate(template string) error {
	if strings.TrimSpace(template) == "" {
		return nil
	}
	if !strings.Contains(template, "{name}") {
		return fmt.Errorf("badge template must contain {name}")
	}
	if len(template) > tmux.MaxBadgeTextLen {
		return fmt.Errorf("badge template longer than %d characters", tmux.MaxBadgeTextLen)
	}
	stripped := template
	for _, token := range badgeTemplateTokens {
		stripped = strings.ReplaceAll(stripped, token, "")
	}
	if strings.ContainsAny(stripped, "{}") {
		return fmt.Errorf("badge template contains an unknown {token}; supported: {name}, {drift}, {lifecycle}")
	}
	if tmux.SanitizeBadgeText(stripped) != strings.TrimSpace(stripped) {
		return fmt.Errorf("badge template contains characters that are not allowed in a pane border (tmux format syntax, quotes, control characters)")
	}
	return nil
}

// safeBadgeName is the only shape of assigned name rendered verbatim; any
// other assignment renders as unresolved.
var safeBadgeName = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_-]{0,63}$`)

// BadgeLabel composes the display label for a record. A non-current
// assignment (or an unsafe name) renders as "?" with the warning marker so a
// stale or unregistered pane is never presented as a current assignment;
// lifecycle markers stay visible in every case. The result is sanitised for
// tmux.
func BadgeLabel(rec PaneBadgeRecord, template string) string {
	if strings.TrimSpace(template) == "" {
		template = DefaultBadgeTemplate
	}
	name := "?"
	drift := "!"
	if rec.AssignmentState == PaneAssignmentCurrent && safeBadgeName.MatchString(rec.AssignedName) {
		name = rec.AssignedName
		switch rec.ObservationState {
		case PaneObservationMatched, PaneObservationLegacyUnverified:
			drift = ""
		}
	}
	lifecycle := ""
	switch rec.Lifecycle {
	case PaneLifecycleStarting, PaneLifecycleExited, PaneLifecycleUnknown:
		lifecycle = " (" + string(rec.Lifecycle) + ")"
	}
	label := strings.NewReplacer("{name}", name, "{drift}", drift, "{lifecycle}", lifecycle).Replace(template)
	return tmux.SanitizeBadgeText(label)
}

// ReconcileInput carries the observed facts for one pane.
type ReconcileInput struct {
	SessionName string
	// ProjectKeys are the identity-file namespaces to observe, session key
	// first.
	ProjectKeys []string
	SocketPath  string
	Pane        tmux.Pane
	// Lifecycle overrides derivation from the pane (pre-launch: starting).
	Lifecycle PaneLifecycle
	// Template is the badge label template ("" = default).
	Template string
	Now      time.Time
	// Observe reads the identity file; nil uses ObservePaneIdentity.
	Observe func(projectKeys []string, paneID string) IdentityObservation
}

// ReconcilePane compares the registry's assignment for the pane with the
// canonical identity file and returns the pane's badge record. prev carries
// the previous record for the pane so LastSuccessAt is retained across a
// failed observation. The registry is never mutated.
func ReconcilePane(registry *SessionAgentRegistry, in ReconcileInput, prev PaneBadgeRecord) PaneBadgeRecord {
	now := in.Now
	if now.IsZero() {
		now = time.Now()
	}
	pane := in.Pane
	rec := PaneBadgeRecord{
		PaneID:        pane.ID,
		PaneIndex:     pane.Index,
		WindowIndex:   pane.WindowIndex,
		PanePID:       pane.PID,
		LastAttemptAt: now,
		LastSuccessAt: prev.LastSuccessAt,
	}
	rec.Lifecycle = in.Lifecycle
	if rec.Lifecycle == "" {
		rec.Lifecycle = LifecycleFromPane(pane)
	}

	assigned, bound := registry.GetAgentByID(pane.ID)
	assigned = strings.TrimSpace(assigned)
	if !bound || assigned == "" {
		rec.AssignmentState = PaneAssignmentUnregistered
		rec.ObservationState = PaneObservationSkipped
		rec.Problems = append(rec.Problems, "unregistered: no NTM assignment recorded for pane "+pane.ID)
	} else {
		rec.AssignedName = assigned
		rec.RecordedPID = registry.PanePID(pane.ID)
		switch {
		case rec.RecordedPID > 0 && pane.PID > 0 && rec.RecordedPID != pane.PID:
			rec.AssignmentState = PaneAssignmentStale
			rec.Problems = append(rec.Problems, fmt.Sprintf("assignment-stale: registry recorded pane pid %d, pane now reports %d", rec.RecordedPID, pane.PID))
		case rec.RecordedPID <= 0:
			// Registries written before ntm#256 carry no pid: the binding is
			// taken as current (the pane id matches) but its generation
			// cannot be checked, which status output says explicitly.
			rec.AssignmentState = PaneAssignmentCurrent
			rec.Problems = append(rec.Problems, "pid-unrecorded: registry has no pane pid for this binding; generation not verified")
		default:
			rec.AssignmentState = PaneAssignmentCurrent
		}
		observe := in.Observe
		if observe == nil {
			observe = ObservePaneIdentity
		}
		obs := observe(in.ProjectKeys, pane.ID)
		rec.ResolvedName = obs.Name
		rec.Source = obs.Path
		state, verified, problems := obs.Compare(assigned, PaneBindingFacts{
			SessionName: in.SessionName,
			PaneID:      pane.ID,
			PanePID:     pane.PID,
			SocketPath:  in.SocketPath,
		})
		rec.ObservationState = state
		rec.BindingVerified = verified
		rec.Problems = append(rec.Problems, problems...)
	}

	rec.State = badgeState(rec)
	rec.Label = BadgeLabel(rec, in.Template)
	if rec.observed() {
		success := now
		rec.LastSuccessAt = &success
	}
	return rec
}

// UnobservableRecord is the record for a registry binding whose pane could
// not be observed because tmux was unavailable: the attempt is recorded, the
// previous success is retained, and nothing is published.
func UnobservableRecord(registry *SessionAgentRegistry, paneID, assigned string, now time.Time, prev PaneBadgeRecord) PaneBadgeRecord {
	if now.IsZero() {
		now = time.Now()
	}
	rec := PaneBadgeRecord{
		PaneID:           paneID,
		PaneIndex:        prev.PaneIndex,
		WindowIndex:      prev.WindowIndex,
		RecordedPID:      registry.PanePID(paneID),
		AssignedName:     assigned,
		AssignmentState:  PaneAssignmentUnobservable,
		ObservationState: PaneObservationSkipped,
		Lifecycle:        PaneLifecycleUnknown,
		Problems:         []string{"assignment-unobservable: tmux could not be read"},
		LastAttemptAt:    now,
		LastSuccessAt:    prev.LastSuccessAt,
		Cached:           prev.Cached,
	}
	rec.State = badgeState(rec)
	rec.Label = BadgeLabel(rec, "")
	return rec
}

// badgeState is the compact value written to the pane's state option: the
// assignment problem when there is one, else the observation outcome.
func badgeState(rec PaneBadgeRecord) string {
	if rec.AssignmentState != PaneAssignmentCurrent {
		return "assignment-" + string(rec.AssignmentState)
	}
	return string(rec.ObservationState)
}

// PaneBadgeStore persists the last reconciliation per pane beside the session
// registry (agent_badges.json). It is deliberately a separate file: status
// commands refresh it, and they must never race the spawn path's registry
// writes.
type PaneBadgeStore struct {
	SessionName string                     `json:"session_name"`
	ProjectKey  string                     `json:"project_key"`
	Panes       map[string]PaneBadgeRecord `json:"panes"`
	UpdatedAt   time.Time                  `json:"updated_at"`
}

func paneBadgeStorePath(sessionName, projectKey string) string {
	base := filepath.Join(getSessionsBaseDir(), sessionName)
	if projectKey != "" {
		base = filepath.Join(base, primaryProjectSlug(projectKey))
	}
	return filepath.Join(base, "agent_badges.json")
}

// LoadPaneBadgeStore loads the badge store for a session, returning an empty
// store when none exists.
func LoadPaneBadgeStore(sessionName, projectKey string) (*PaneBadgeStore, error) {
	if err := validateSessionStorageName(sessionName); err != nil {
		return nil, err
	}
	store := &PaneBadgeStore{SessionName: sessionName, ProjectKey: projectKey, Panes: map[string]PaneBadgeRecord{}}
	data, err := os.ReadFile(paneBadgeStorePath(sessionName, projectKey))
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return store, nil
		}
		return nil, fmt.Errorf("reading badge store: %w", err)
	}
	if err := json.Unmarshal(data, store); err != nil {
		return nil, fmt.Errorf("parsing badge store: %w", err)
	}
	if store.Panes == nil {
		store.Panes = map[string]PaneBadgeRecord{}
	}
	store.SessionName = sessionName
	store.ProjectKey = projectKey
	return store, nil
}

// Save persists the store atomically.
func (s *PaneBadgeStore) Save() error {
	if s == nil {
		return fmt.Errorf("cannot save nil badge store")
	}
	if err := validateSessionStorageName(s.SessionName); err != nil {
		return err
	}
	path := paneBadgeStorePath(s.SessionName, s.ProjectKey)
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return fmt.Errorf("creating badge store directory: %w", err)
	}
	s.UpdatedAt = time.Now()
	data, err := json.MarshalIndent(s, "", "  ")
	if err != nil {
		return fmt.Errorf("marshaling badge store: %w", err)
	}
	if err := util.AtomicWriteFile(path, data, 0o600); err != nil {
		return fmt.Errorf("writing badge store: %w", err)
	}
	return nil
}
