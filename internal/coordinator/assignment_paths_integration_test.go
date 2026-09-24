package coordinator

import (
	"reflect"
	"testing"

	"github.com/Dicklesworthstone/ntm/internal/bv"
)

func TestScoreAndSelectAssignmentsProducesLocalReservationPaths(t *testing.T) {
	selected := ScoreAndSelectAssignments(
		[]*AgentState{{PaneID: "%3", PaneIndex: 3}},
		[]bv.TriageRecommendation{{
			ID: "ntm-path-intent", Type: "task", Status: "open", Score: 1,
			Title:   "Fix src/main.go:42 using https://example.com/reference.go",
			Reasons: []string{"See [design](docs/design.md#layout); then inspect `src/main.go:43`."},
		}},
		ScoreConfig{}, nil,
	)
	if len(selected) != 1 || selected[0].Assignment == nil {
		t.Fatalf("expected one prepared assignment, got %+v", selected)
	}
	want := []string{"src/main.go", "docs/design.md"}
	if got := selected[0].Assignment.FilesToReserve; !reflect.DeepEqual(got, want) {
		t.Fatalf("prepared reservation paths = %q, want %q", got, want)
	}
}
