package robot

import (
	"fmt"
	"strings"
	"time"

	"github.com/Dicklesworthstone/ntm/internal/config"
	"github.com/Dicklesworthstone/ntm/internal/state"
)

// Runtime carries explicit, dependency-injected runtime state for store-backed
// robot handlers (projection store, config, and clock). Passing *Runtime (or
// Options.Store) eliminates mutable package globals so CLI commands, HTTP
// servers, and parallel tests remain hermetic and data-race-free.
type Runtime struct {
	Store  *state.Store
	Config *config.Config
	Now    func() time.Time
}

// NewRuntime constructs a Runtime bound to an explicit projection store and config.
func NewRuntime(store *state.Store, cfg *config.Config) *Runtime {
	return &Runtime{
		Store:  store,
		Config: cfg,
		Now:    time.Now,
	}
}

// ProjectionStore returns the injected store or nil when rt is nil.
func (rt *Runtime) ProjectionStore() *state.Store {
	if rt == nil {
		return nil
	}
	return rt.Store
}

// PaginationOptions builds PaginationOptions bound to this Runtime's projection store.
func (rt *Runtime) PaginationOptions(limit, offset int) PaginationOptions {
	return PaginationOptions{
		Limit:         limit,
		Offset:        offset,
		Store:         rt.ProjectionStore(),
		ExplicitStore: true,
	}
}

// PrintSendReceipt handles --robot-send-receipt using the injected projection store.
func (rt *Runtime) PrintSendReceipt(operationID, session string) error {
	output, err := GetSendReceiptForStore(rt.ProjectionStore(), operationID, session)
	if err != nil {
		return err
	}
	return encodeTerminalRobotOutput(output, output.RobotResponse, "robot send-receipt failed")
}

// PrintActionStatus handles --robot-action-status using the injected projection store.
func (rt *Runtime) PrintActionStatus(requestID, session string) error {
	output, err := GetActionStatusWithStore(requestID, rt.ProjectionStore(), true, session)
	if err != nil {
		return err
	}
	return encodeTerminalRobotOutput(output, output.RobotResponse, "robot action-status failed")
}

// PrintCancelRequest handles --robot-cancel-request using the injected projection store.
func (rt *Runtime) PrintCancelRequest(requestID, session string) error {
	output, err := GetCancelRequestWithStore(requestID, rt.ProjectionStore(), true, session)
	if err != nil {
		return err
	}
	return encodeTerminalRobotOutput(output, output.RobotResponse, "robot cancel-request failed")
}

// PrintSnapshotDelta handles --robot-snapshot --since using the injected projection store.
func (rt *Runtime) PrintSnapshotDelta(since time.Time) error {
	output, err := GetSnapshotDeltaForStore(rt.ProjectionStore(), since)
	if err != nil {
		return err
	}
	return encodeTerminalRobotOutput(output, output.RobotResponse, "robot snapshot delta failed")
}

// PrintTerse handles --robot-terse using the injected projection store.
func (rt *Runtime) PrintTerse(cfg *config.Config) error {
	if cfg == nil && rt != nil {
		cfg = rt.Config
	}
	output, err := GetTerseForStore(rt.ProjectionStore(), cfg)
	if err != nil {
		return err
	}
	if !output.Success {
		return encodeTerminalRobotOutput(output, output.RobotResponse, "robot terse failed")
	}
	fmt.Println(strings.Join(output.TerseLines, ";"))
	return nil
}

// PrintIncidentResolve handles --robot-incident-resolve using the injected projection store.
func (rt *Runtime) PrintIncidentResolve(incidentID, note string) error {
	output, err := ResolveIncidentForStore(rt.ProjectionStore(), incidentID, note)
	if err != nil {
		return err
	}
	return encodeTerminalRobotOutput(output, output.RobotResponse, "robot incident-resolve failed")
}
