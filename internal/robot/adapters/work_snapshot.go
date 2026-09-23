package adapters

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"time"

	"github.com/Dicklesworthstone/ntm/internal/worksource"
)

const workSnapshotVersion = 2
const maxWorkSnapshotBytes = 32 << 20

// ErrWorkSnapshotUnavailable means this collection cannot be reused as a
// source-verified cache. A caller may make an explicit fresh live collection;
// it must never fall back to inferring ready work from old RuntimeWork rows.
var ErrWorkSnapshotUnavailable = errors.New("WORK_SNAPSHOT_UNAVAILABLE")

// ErrWorkSnapshotReadOnly rejects publication of a cache read, including a
// failed read. Revalidation is not a new tracker observation and cannot extend
// the lifetime of the old candidate set or replace a newer failure marker.
var ErrWorkSnapshotReadOnly = errors.New("WORK_SNAPSHOT_READ_ONLY")

// Keep publication authority private and separate from mutable display fields.
// The payload is frozen when the complete candidate read is verified, not when
// a later consumer serializes the preview. Strings cannot share mutable slices
// or pointers with the collector's input or the returned WorkSection.
type workSnapshotPublication struct {
	project     string
	collectedAt time.Time
	payload     string
	readOnly    bool
}

type workSnapshotPolicy struct {
	RequiredRef   string   `json:"required_ref,omitempty"`
	RequireClean  bool     `json:"require_clean,omitempty"`
	ProgramLabels []string `json:"program_labels,omitempty"`
}

// workSnapshotEnvelope stores the complete pre-filter candidate collection,
// not the displayed preview. Reuse runs the SAME live verifier against these
// candidates, with the saved identity and a fresh reservation observation.
// No tool is executed to repair, refresh, import, or replay this envelope.
type workSnapshotEnvelope struct {
	Version     int                  `json:"version"`
	ProjectDir  string               `json:"project_dir"`
	CollectedAt time.Time            `json:"collected_at"`
	Source      *worksource.Identity `json:"source,omitempty"`
	Policy      workSnapshotPolicy   `json:"policy"`
	Work        *WorkSection         `json:"work,omitempty"`
	Unavailable string               `json:"unavailable,omitempty"`
	RecollectAt time.Time            `json:"recollect_at,omitempty"`
}

func canonicalWorkSnapshotProject(project string) (string, error) {
	if strings.TrimSpace(project) == "" {
		return "", errors.New("explicit work project is required")
	}
	absolute, err := filepath.Abs(project)
	if err != nil {
		return "", err
	}
	canonical, err := filepath.EvalSymlinks(absolute)
	if err != nil {
		return "", err
	}
	info, err := os.Stat(canonical)
	if err != nil {
		return "", err
	}
	if !info.IsDir() {
		return "", errors.New("work project is not a directory")
	}
	return canonical, nil
}

func savedWorkSnapshotPolicy(policy WorkVerificationPolicy) workSnapshotPolicy {
	var programs []string
	seen := make(map[string]bool)
	for _, label := range policy.ProgramLabels {
		label = strings.ToLower(strings.TrimSpace(label))
		if label != "" && !seen[label] {
			programs = append(programs, label)
			seen[label] = true
		}
	}
	sort.Strings(programs)
	return workSnapshotPolicy{
		RequiredRef:  strings.TrimSpace(policy.Source.RequiredRef),
		RequireClean: policy.Source.RequireClean, ProgramLabels: programs,
	}
}

func attachWorkSnapshot(verified, candidates *WorkSection, source *worksource.Snapshot, policy WorkVerificationPolicy, started time.Time) error {
	if verified == nil || verified.Verification == nil || !verified.Available || candidates == nil || !candidates.Available {
		return nil
	}
	if source == nil || !source.Identity.Bound() || started.IsZero() {
		return errors.New("complete work publication requires a source and collection time")
	}
	if candidates.Verification != nil && (candidates.Verification.FromCache ||
		(candidates.Verification.snapshot != nil && candidates.Verification.snapshot.readOnly)) {
		return ErrWorkSnapshotReadOnly
	}
	// The verifier copies candidates rather than mutating them. Drop any old
	// verification metadata so encoding cannot carry a recursive cache, an old
	// reservation verdict, or a precomputed ready count into a later read.
	input := copyWorkForVerification(candidates)
	input.Verification = nil
	identity := source.Identity
	envelope := &workSnapshotEnvelope{
		Version: workSnapshotVersion, ProjectDir: identity.ProjectDir,
		CollectedAt: started.UTC(),
		Source:      &identity, Policy: savedWorkSnapshotPolicy(policy), Work: input,
		RecollectAt: source.NextEligibilityChange(started),
	}
	if err := validateWorkSnapshotCandidates(input); err != nil {
		return err
	}
	payload, err := encodeWorkSnapshot(envelope)
	if err != nil {
		return err
	}
	verified.Verification.ProjectDir = identity.ProjectDir
	verified.Verification.snapshot = &workSnapshotPublication{
		project: identity.ProjectDir, collectedAt: started.UTC(), payload: string(payload),
	}
	return nil
}

func stampWorkSnapshotProject(work *WorkSection, project string, originalStart ...time.Time) {
	started := time.Now().UTC()
	if len(originalStart) > 0 {
		started = originalStart[0]
	}
	if work == nil || work.Verification == nil {
		return
	}
	// A verified collection already carries the identity and beginning of its
	// own read. Do not retimestamp it at the end of the enclosing collection.
	if work.Verification.snapshot != nil {
		return
	}
	if canonical, err := canonicalWorkSnapshotProject(project); err == nil {
		work.Verification.ProjectDir = canonical
		work.Verification.snapshot = &workSnapshotPublication{project: canonical, collectedAt: started.UTC()}
	}
}

// MarshalWorkSnapshot supplies the opaque payload persisted with the runtime
// projection. Even a failed/DB-only collection produces a scoped unavailable
// marker, so a newer failure cannot leave an older healthy cache in its place.
func MarshalWorkSnapshot(work *WorkSection) (string, []byte, error) {
	project, payload, _, err := marshalWorkSnapshotObservation(work)
	return project, payload, err
}

// marshalWorkSnapshotObservation also returns the immutable collection start.
// A publisher must not replace it with the time of serialization or cache reuse.
func marshalWorkSnapshotObservation(work *WorkSection) (string, []byte, time.Time, error) {
	if work == nil || work.Verification == nil {
		return "", nil, time.Time{}, ErrWorkSnapshotUnavailable
	}
	publication := work.Verification.snapshot
	if work.Verification.FromCache || (publication != nil && publication.readOnly) {
		return "", nil, time.Time{}, ErrWorkSnapshotReadOnly
	}
	if publication == nil || publication.project == "" || publication.collectedAt.IsZero() {
		return "", nil, time.Time{}, ErrWorkSnapshotUnavailable
	}
	if work.Verification.ProjectDir != publication.project {
		return "", nil, time.Time{}, errors.New("work publication project changed after collection")
	}
	payload := []byte(publication.payload)
	if len(payload) == 0 || !work.Available {
		var err error
		payload, err = encodeWorkSnapshot(&workSnapshotEnvelope{
			Version: workSnapshotVersion, ProjectDir: publication.project,
			CollectedAt: publication.collectedAt,
			Unavailable: firstNonEmpty(work.Reason, "collection has no complete source-verified candidate snapshot"),
		})
		if err != nil {
			return "", nil, time.Time{}, err
		}
	}
	return publication.project, payload, publication.collectedAt, nil
}

func encodeWorkSnapshot(envelope *workSnapshotEnvelope) ([]byte, error) {
	payload, err := json.Marshal(envelope)
	if err != nil {
		return nil, fmt.Errorf("encode work snapshot: %w", err)
	}
	if len(payload) > maxWorkSnapshotBytes {
		return nil, errors.New("work snapshot exceeds 32 MiB; refusing partial persistence")
	}
	return payload, nil
}

// RestoreWorkSnapshot is the cache counterpart of live Collect. Its caller
// owns the original observation/expiry timestamps and must check freshness
// both before and after this method, without advancing them on success.
func (a *WorkCoordinationAdapter) RestoreWorkSnapshot(ctx context.Context, payload []byte) (*WorkSection, error) {
	policy := a.config.VerificationPolicy
	policy.readReservations = a.mailClient().ReadWorkReservations
	return restoreWorkSnapshot(ctx, a.config.ProjectDir, policy, a.config.WorkItemLimit, payload)
}

func restoreWorkSnapshot(ctx context.Context, project string, policy WorkVerificationPolicy, limit int, payload []byte) (work *WorkSection, restoreErr error) {
	defer func() {
		if work != nil && work.Verification != nil {
			verification := *work.Verification
			verification.FromCache = true
			verification.snapshot = &workSnapshotPublication{readOnly: true}
			work.Verification = &verification
		}
	}()
	if ctx == nil {
		err := errors.New("work snapshot requires a context")
		return rejectWorkSource(nil, err), err
	}
	if err := ctx.Err(); err != nil {
		return rejectWorkSource(nil, err), err
	}
	fail := func(reason string, cause error) (*WorkSection, error) {
		err := &worksource.StaleError{Reason: reason, Cause: cause}
		return rejectWorkSource(nil, err), err
	}
	canonical, err := canonicalWorkSnapshotProject(project)
	if err != nil {
		return fail("cannot resolve cached work project", err)
	}
	if len(payload) == 0 || len(payload) > maxWorkSnapshotBytes {
		return fail("invalid cached work payload size", nil)
	}
	decoder := json.NewDecoder(bytes.NewReader(payload))
	decoder.DisallowUnknownFields()
	var envelope workSnapshotEnvelope
	if err := decoder.Decode(&envelope); err != nil {
		return fail("cannot decode cached work", err)
	}
	var trailing any
	if err := decoder.Decode(&trailing); err != io.EOF {
		return fail("cached work contains trailing data", nil)
	}
	// Version 1 did not bind an original observation time into the payload.
	// Treat that old format as a cache miss during upgrade, not as fresh work.
	if envelope.Version == 1 && envelope.ProjectDir == canonical {
		err := fmt.Errorf("%w: legacy work snapshot requires a fresh observation", ErrWorkSnapshotUnavailable)
		return rejectWorkSource(nil, err), err
	}
	if envelope.Version != workSnapshotVersion || envelope.ProjectDir != canonical {
		return fail("cached work version or project does not match the requested collection", nil)
	}
	if envelope.CollectedAt.IsZero() || envelope.CollectedAt.After(time.Now()) {
		return fail("cached work has no valid original collection time", nil)
	}
	if envelope.Unavailable != "" {
		err := fmt.Errorf("%w: %s", ErrWorkSnapshotUnavailable, envelope.Unavailable)
		return rejectWorkSource(nil, err), err
	}
	// A deferred task can become ready without changing a single JSONL byte.
	// A cache whose candidate read predates that boundary must be recollected,
	// not advertised as a checked-empty or complete current ready set.
	if !envelope.RecollectAt.IsZero() && !time.Now().Before(envelope.RecollectAt) {
		err := fmt.Errorf("%w: a deferred-work readiness boundary has passed", ErrWorkSnapshotUnavailable)
		return rejectWorkSource(nil, err), err
	}
	if envelope.Source == nil || !envelope.Source.Bound() || envelope.Source.ProjectDir != canonical {
		return fail("cached work has no matching canonical source identity", nil)
	}
	// An explicit strict/source/program policy may never be weakened by a
	// cache generated for a different caller. Recollect instead of silently
	// reinterpreting the scope of a persisted candidate set.
	if !reflect.DeepEqual(envelope.Policy, savedWorkSnapshotPolicy(policy)) {
		return fail("cached work was collected under a different source or program policy", nil)
	}
	if policy.Source.Expected != nil && !policy.Source.Expected.SameSource(*envelope.Source) {
		return fail("cached work does not match the caller's expected source", nil)
	}
	if err := validateWorkSnapshotCandidates(envelope.Work); err != nil {
		return fail("cached candidate collection is incomplete", err)
	}
	policy.Source.Expected = envelope.Source
	work, err = collectWorkWithSource(ctx, canonical, policy, func(context.Context) (*WorkSection, error) {
		return envelope.Work, nil
	})
	if err != nil {
		return work, err
	}
	if !envelope.RecollectAt.IsZero() && !time.Now().Before(envelope.RecollectAt) {
		err := fmt.Errorf("%w: readiness changed while validating the cached collection", ErrWorkSnapshotUnavailable)
		return rejectWorkSource(nil, err), err
	}
	work = limitVerifiedWorkPreview(work, limit)
	work.Verification.CacheCollectedAt = envelope.CollectedAt.UTC().Format(time.RFC3339Nano)
	return work, nil
}

func validateWorkSnapshotCandidates(work *WorkSection) error {
	if work == nil || !work.Available || work.Ready == nil || work.Verification != nil {
		return errors.New("missing complete pre-verification work section")
	}
	if len(work.Ready) > 100000 {
		return errors.New("cached work exceeds the complete candidate limit")
	}
	seen := make(map[string]bool, len(work.Ready))
	for _, item := range work.Ready {
		if item.ID == "" || item.ID != strings.TrimSpace(item.ID) || seen[item.ID] || item.Priority < 0 || item.Priority > 4 {
			return errors.New("cached work contains an invalid or duplicate candidate")
		}
		seen[item.ID] = true
	}
	if work.Summary != nil && (work.Summary.Ready < 0 || work.Summary.InProgress < 0) {
		return errors.New("cached work has invalid summary counts")
	}
	return nil
}
