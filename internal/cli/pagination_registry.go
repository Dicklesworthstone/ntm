package cli

// pagination_registry.go registers the CLI-owned paginated list surfaces
// (D1, bd-ws3-contract-breadth-psvyu.1) in the robot schema registry and
// declares their pagination flags through the WS0-G6 single-declaration map
// (robot.SchemaPagination). The registry-walk conformance tests
// (internal/robot and internal/cli) enforce that every list-shaped type is
// flagged and that flagged-paginated types carry the PaginationInfo contract.

import (
	"github.com/Dicklesworthstone/ntm/internal/output"
	"github.com/Dicklesworthstone/ntm/internal/robot"
)

func init() {
	// The five unbounded-growth list surfaces (sessions, history, audit,
	// checkpoints, approvals) — all paginated via --limit/--offset with
	// count/total_matches/has_more plus _agent_hints.next_offset.
	robot.MustRegisterSchemaCommand("session_list", output.ListResponse{})
	robot.MustRegisterSchemaPagination("session_list", robot.SchemaPaginationFlag{
		Paginated: true, Reason: "sessions array pages via ntm list --limit/--offset",
	})

	robot.MustRegisterSchemaCommand("history_list", HistoryListJSON{})
	robot.MustRegisterSchemaPagination("history_list", robot.SchemaPaginationFlag{
		Paginated: true, Reason: "entries array pages via ntm history --limit/--offset (offset counts back from newest)",
	})

	robot.MustRegisterSchemaCommand("audit_query", AuditQueryOutput{})
	robot.MustRegisterSchemaPagination("audit_query", robot.SchemaPaginationFlag{
		Paginated: true, Reason: "entries array pages via ntm audit show/search --limit/--offset",
	})

	robot.MustRegisterSchemaCommand("checkpoint_list", CheckpointListOutput{})
	robot.MustRegisterSchemaPagination("checkpoint_list", robot.SchemaPaginationFlag{
		Paginated: true, Reason: "checkpoints array pages via ntm checkpoint list <session> --limit/--offset",
	})

	robot.MustRegisterSchemaCommand("checkpoint_sessions", CheckpointSessionsOutput{})
	robot.MustRegisterSchemaPagination("checkpoint_sessions", robot.SchemaPaginationFlag{
		Paginated: true, Reason: "sessions array pages via ntm checkpoint list --limit/--offset",
	})

	robot.MustRegisterSchemaCommand("approvals_list", ApprovalsListOutput{})
	robot.MustRegisterSchemaPagination("approvals_list", robot.SchemaPaginationFlag{
		Paginated: true, Reason: "pending array pages via ntm approve list --limit/--offset",
	})
}
