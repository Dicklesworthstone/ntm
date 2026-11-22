package robot

import (
	"fmt"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/Dicklesworthstone/ntm/internal/config"
	"github.com/Dicklesworthstone/ntm/internal/state"
)

// TestRobotRuntimeIsolationParallel proves that multiple Runtime instances with
// independent projection stores execute concurrently without cross-contamination
// or data races (zero package-global projectionStore state).
func TestRobotRuntimeIsolationParallel(t *testing.T) {
	t.Parallel()

	const workers = 6
	var wg sync.WaitGroup
	errs := make(chan error, workers)

	for i := 0; i < workers; i++ {
		workerID := i
		wg.Add(1)
		go func() {
			defer wg.Done()

			dbPath := filepath.Join(t.TempDir(), fmt.Sprintf("runtime-%d.db", workerID))
			store, err := state.Open(dbPath)
			if err != nil {
				errs <- fmt.Errorf("worker %d open: %w", workerID, err)
				return
			}
			defer store.Close()
			if err := store.Migrate(); err != nil {
				errs <- fmt.Errorf("worker %d migrate: %w", workerID, err)
				return
			}

			rt := NewRuntime(store, config.Default())
			now := time.Now().UTC()
			sessName := fmt.Sprintf("isolated-session-%d", workerID)
			incID := fmt.Sprintf("inc-isolated-%d", workerID)
			opID := fmt.Sprintf("op-isolated-%d", workerID)

			if err := store.UpsertRuntimeSession(&state.RuntimeSession{
				Name:         sessName,
				AgentCount:   1,
				HealthStatus: state.HealthStatusHealthy,
				CollectedAt:  now,
				StaleAfter:   now.Add(time.Hour),
			}); err != nil {
				errs <- fmt.Errorf("worker %d upsert session: %w", workerID, err)
				return
			}
			if err := store.CreateIncident(&state.Incident{
				ID:          incID,
				Title:       "Isolated incident",
				Fingerprint: "fp-" + incID,
				Family:      "test",
				Category:    "isolation",
				Status:      state.IncidentStatusOpen,
				Severity:    state.SeverityWarning,
				StartedAt:   now,
				LastEventAt: now,
			}); err != nil {
				errs <- fmt.Errorf("worker %d create incident: %w", workerID, err)
				return
			}
			if _, claimed, err := store.ClaimSendOperation(&state.SendOperation{
				OperationID:   opID,
				SessionName:   sessName,
				Kind:          state.OperationKindSend,
				BindingHash:   "bind-" + opID,
				PayloadSHA256: "sha",
				PayloadBytes:  3,
			}); err != nil || !claimed {
				errs <- fmt.Errorf("worker %d claim op: claimed=%v err=%v", workerID, claimed, err)
				return
			}

			// Verify inspect, receipt, action status, and incident resolve all see ONLY this worker's store.
			sessOut, err := GetInspectSession(InspectSessionOptions{Session: sessName, Store: rt.ProjectionStore()})
			if err != nil || !sessOut.Success || sessOut.Detail.Name != sessName {
				errs <- fmt.Errorf("worker %d inspect session = (%+v, %v)", workerID, sessOut, err)
				return
			}
			otherSess := fmt.Sprintf("isolated-session-%d", (workerID+1)%workers)
			otherOut, err := GetInspectSession(InspectSessionOptions{Session: otherSess, Store: rt.ProjectionStore()})
			if err != nil || otherOut.Success {
				errs <- fmt.Errorf("worker %d leaked sibling session %s: %+v", workerID, otherSess, otherOut)
				return
			}

			receipt, err := GetSendReceiptForStore(rt.ProjectionStore(), opID, "")
			if err != nil || !receipt.Success || receipt.Session != sessName {
				errs <- fmt.Errorf("worker %d receipt = (%+v, %v)", workerID, receipt, err)
				return
			}

			resolved, err := ResolveIncidentForStore(rt.ProjectionStore(), incID, "resolved in isolated runtime")
			if err != nil || !resolved.Success || resolved.Status != string(state.IncidentStatusResolved) {
				errs <- fmt.Errorf("worker %d resolve incident = (%+v, %v)", workerID, resolved, err)
				return
			}
		}()
	}

	wg.Wait()
	close(errs)
	for err := range errs {
		t.Error(err)
	}
}
