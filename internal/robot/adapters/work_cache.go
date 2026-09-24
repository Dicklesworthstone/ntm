package adapters

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/Dicklesworthstone/ntm/internal/state"
	"github.com/Dicklesworthstone/ntm/internal/worksource"
)

// WorkCacheLifetime is the lifetime of the original candidate observation,
// never an extension granted by a successful cache read.
const WorkCacheLifetime = 45 * time.Second

type durableWorkCollector interface {
	Collect(context.Context) (*SignalBatch, error)
	RestoreWorkSnapshot(context.Context, []byte) (*WorkSection, error)
}

// CollectWork reads only the work section. Source verification and the complete
// live reservation check remain mandatory, but unrelated inbox, handoff and
// conflict enrichment cannot consume a work query's deadline after it succeeds.
func (a *WorkCoordinationAdapter) CollectWork(ctx context.Context) (*WorkSection, error) {
	if ctx == nil || a == nil {
		return nil, errors.New("work collection requires a context and adapter")
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	return a.collectVerifiedWork(ctx)
}

// A durable work query narrows the batch adapter to one section. Cache reuse
// retains the same adapter's RestoreWorkSnapshot and reservation client.
type workSnapshotCollector struct{ *WorkCoordinationAdapter }

func (c workSnapshotCollector) Collect(ctx context.Context) (*SignalBatch, error) {
	work, err := c.CollectWork(ctx)
	return &SignalBatch{Work: work}, err
}

// CollectDurableWork is a project-scoped, restart-safe work query. A cache miss,
// expired collection or unavailable marker permits a fresh observation. Source
// mismatch and corrupt evidence do not: refresh must be explicitly requested.
// It uses the same live eligibility and reservation verifier as Collect, stores
// all candidates before preview limiting, and never claims or dispatches work.
// The caller owns store and must have applied its migrations.
func CollectDurableWork(ctx context.Context, store *state.Store, cfg WorkCoordinationAdapterConfig, refresh bool) (*WorkSection, error) {
	if ctx == nil || store == nil {
		return nil, errors.New("durable work requires a context and state store")
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	project, err := canonicalWorkSnapshotProject(cfg.ProjectDir)
	if err != nil {
		return nil, &worksource.StaleError{Reason: "cannot resolve durable work project", Cause: err}
	}
	cfg.ProjectDir = project
	return collectDurableWork(ctx, store, project, workSnapshotCollector{NewWorkCoordinationAdapter(cfg)}, refresh, time.Now)
}

func collectDurableWork(ctx context.Context, store *state.Store, project string, collector durableWorkCollector, refresh bool, now func() time.Time) (*WorkSection, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if !refresh {
		work, err := readDurableWork(ctx, store, project, collector, now)
		if !errors.Is(err, ErrWorkSnapshotUnavailable) {
			return work, err
		}
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	started := now().UTC()
	batch, collectErr := collector.Collect(ctx)
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	var work *WorkSection
	if batch != nil {
		work = batch.Work
	}
	// Do not turn a restored success OR failure into a new observation. In
	// particular, rejection must not strip the private marker before stamping.
	if work != nil && work.Verification != nil && (work.Verification.FromCache ||
		(work.Verification.snapshot != nil && work.Verification.snapshot.readOnly)) {
		return rejectWorkSource(work, ErrWorkSnapshotReadOnly), errors.Join(collectErr, ErrWorkSnapshotReadOnly)
	}
	if collectErr != nil || work == nil || !work.Available {
		if collectErr == nil {
			collectErr = fmt.Errorf("%w: live work collection is unavailable", ErrWorkSnapshotUnavailable)
		}
		work = rejectWorkSource(work, collectErr)
	}
	stampWorkSnapshotProject(work, project, started)
	storedProject, payload, observedAt, err := marshalWorkSnapshotObservation(work)
	if err != nil {
		return rejectWorkSource(work, err), errors.Join(collectErr, err)
	}
	if storedProject != project {
		err := &worksource.StaleError{Reason: "collector returned a different work project"}
		return rejectWorkSource(work, err), err
	}
	record, err := state.NewRuntimeWorkSnapshot(project, payload, observedAt, observedAt.Add(WorkCacheLifetime))
	if err != nil {
		return rejectWorkSource(work, err), errors.Join(collectErr, err)
	}
	if err := store.PublishRuntimeWorkSnapshot(ctx, record); err != nil {
		return rejectWorkSource(work, err), errors.Join(collectErr, err)
	}
	if collectErr != nil {
		return work, collectErr
	}
	// Publication can wait behind another collector. Do not present a result
	// that expired or was superseded while its durable receipt was committed.
	if err := checkDurableWorkCurrent(ctx, store, record, now); err != nil {
		return rejectWorkSource(work, err), err
	}
	return work, nil
}

func readDurableWork(ctx context.Context, store *state.Store, project string, collector durableWorkCollector, now func() time.Time) (*WorkSection, error) {
	record, err := store.GetRuntimeWorkSnapshot(ctx, project)
	if err != nil {
		return nil, workSourceFailure(err)
	}
	if record == nil || !record.IsFreshAt(now()) {
		return nil, ErrWorkSnapshotUnavailable
	}
	if record.ProjectDir != project {
		return nil, &worksource.StaleError{Reason: "persisted work belongs to a different project"}
	}
	work, err := collector.RestoreWorkSnapshot(ctx, record.Payload)
	if err != nil {
		return work, err
	}
	if work == nil || !work.Available || work.Verification == nil || work.Verification.Source == nil || !work.Verification.Source.Bound() || work.Verification.Source.ProjectDir != project {
		return nil, &worksource.StaleError{Reason: "persisted work restoration returned no source-bound observation"}
	}
	observedAt, stampErr := time.Parse(time.RFC3339Nano, work.Verification.CacheCollectedAt)
	if stampErr != nil || !observedAt.Equal(record.CollectedAt) || record.StaleAfter.Sub(record.CollectedAt) > WorkCacheLifetime {
		return nil, &worksource.StaleError{Reason: "persisted work timestamps do not match the original collection"}
	}
	if err := checkDurableWorkCurrent(ctx, store, record, now); err != nil {
		return nil, err
	}
	verification := *work.Verification
	out := copyWorkForVerification(work)
	out.Verification = &verification
	verification.FromCache = true
	verification.snapshot = &workSnapshotPublication{readOnly: true}
	verification.CacheCollectedAt = record.CollectedAt.UTC().Format(time.RFC3339Nano)
	verification.CacheExpiresAt = record.StaleAfter.UTC().Format(time.RFC3339Nano)
	return out, nil
}

func checkDurableWorkCurrent(ctx context.Context, store *state.Store, record *state.RuntimeWorkSnapshot, now func() time.Time) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if !record.IsFreshAt(now()) {
		return ErrWorkSnapshotUnavailable
	}
	current, err := store.RuntimeWorkSnapshotIsCurrent(ctx, record.ProjectDir, record.Revision)
	if err != nil {
		return workSourceFailure(err)
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if !current || !record.IsFreshAt(now()) {
		return ErrWorkSnapshotUnavailable
	}
	return nil
}
