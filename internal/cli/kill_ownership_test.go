package cli

import (
	"reflect"
	"testing"
)

// The case that matters is the third one. It is the shape of the incident this
// guard exists for: tmux answered a bare target with panes from somewhere else,
// and the kill path was about to signal their process subtrees.
func TestForeignPanePIDs(t *testing.T) {
	tests := []struct {
		name       string
		candidates []int
		owned      []int
		want       []int
	}{
		{
			name:       "every candidate is owned",
			candidates: []int{101, 102, 103},
			owned:      []int{101, 102, 103},
			want:       nil,
		},
		{
			name:       "session owns more panes than the candidate list",
			candidates: []int{101},
			owned:      []int{101, 102},
			want:       nil,
		},
		{
			name:       "candidates resolved to another session entirely",
			candidates: []int{201, 202, 203},
			owned:      []int{101},
			want:       []int{201, 202, 203},
		},
		{
			name:       "a single foreign pane mixed into owned ones",
			candidates: []int{101, 999, 102},
			owned:      []int{101, 102},
			want:       []int{999},
		},
		{
			name:       "session owns nothing, so nothing may be reaped",
			candidates: []int{101},
			owned:      nil,
			want:       []int{101},
		},
		{
			name:       "no candidates is not a violation",
			candidates: nil,
			owned:      []int{101},
			want:       nil,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := foreignPanePIDs(tt.candidates, tt.owned)
			if !reflect.DeepEqual(got, tt.want) {
				t.Fatalf("foreignPanePIDs(%v, %v) = %v, want %v", tt.candidates, tt.owned, got, tt.want)
			}
		})
	}
}
