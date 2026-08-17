package cli

import (
	"testing"

	"github.com/Dicklesworthstone/ntm/internal/robot"
)

// TestSchemaPaginationExhaustiveWithCLIRegistrations re-runs the D1
// registry-walk conformance check (bd-ws3-contract-breadth-psvyu.1) with all
// cli/pipeline package registrations loaded: every list-shaped schema type —
// including the ones this package registers — must be explicitly flagged
// paginated true/false, and every flagged-paginated type must implement the
// PaginationInfo contract.
func TestSchemaPaginationExhaustiveWithCLIRegistrations(t *testing.T) {
	for _, v := range robot.SchemaPaginationViolations() {
		t.Errorf("pagination flag violation: %s", v)
	}

	// The five D1 surfaces must be registered and flagged paginated.
	for _, name := range []string{"session_list", "history_list", "audit_query", "checkpoint_list", "approvals_list"} {
		flag, ok := robot.SchemaPagination[name]
		if !ok {
			t.Errorf("schema type %q missing from SchemaPagination", name)
			continue
		}
		if !flag.Paginated {
			t.Errorf("schema type %q must be flagged Paginated=true", name)
		}
		registry := robot.GetRobotRegistry()
		if _, bound := registry.SchemaBinding(name); !bound {
			t.Errorf("schema type %q is flagged but not registered in the schema registry", name)
		}
	}
}
