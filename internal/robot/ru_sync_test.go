package robot

import (
	"reflect"
	"testing"
)

func TestParseRUSyncPayload(t *testing.T) {
	cases := []struct {
		name          string
		payload       string
		wantSynced    []string
		wantSkipped   []string
		wantConflicts []string
	}{
		{
			name:          "invalid",
			payload:       "not-json",
			wantSynced:    []string{},
			wantSkipped:   []string{},
			wantConflicts: []string{},
		},
		{
			name:          "top-level arrays",
			payload:       `{"synced":["repo-a"],"skipped":["repo-b"],"conflicts":["repo-c"]}`,
			wantSynced:    []string{"repo-a"},
			wantSkipped:   []string{"repo-b"},
			wantConflicts: []string{"repo-c"},
		},
		{
			name:          "nested repos object",
			payload:       `{"repos":{"synced":["repo-a"],"skipped":["repo-b"]},"conflicts":["repo-c"]}`,
			wantSynced:    []string{"repo-a"},
			wantSkipped:   []string{"repo-b"},
			wantConflicts: []string{"repo-c"},
		},
		{
			name:          "repos list with status",
			payload:       `{"repos":[{"name":"repo-a","status":"synced"},{"path":"/work/repo-b","status":"skipped"},{"repo":"repo-c","status":"merge-conflict"}]}`,
			wantSynced:    []string{"repo-a"},
			wantSkipped:   []string{"/work/repo-b"},
			wantConflicts: []string{"repo-c"},
		},
		{
			name:          "top-level list",
			payload:       `[{"name":"repo-a","status":"synced"},{"repo":"repo-b","status":"skipped"},{"name":"repo-c","status":"conflict"}]`,
			wantSynced:    []string{"repo-a"},
			wantSkipped:   []string{"repo-b"},
			wantConflicts: []string{"repo-c"},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			repos, conflicts := parseRUSyncPayload([]byte(tc.payload))
			if !reflect.DeepEqual(repos.Synced, tc.wantSynced) {
				t.Fatalf("synced = %v, want %v", repos.Synced, tc.wantSynced)
			}
			if !reflect.DeepEqual(repos.Skipped, tc.wantSkipped) {
				t.Fatalf("skipped = %v, want %v", repos.Skipped, tc.wantSkipped)
			}
			if !reflect.DeepEqual(conflicts, tc.wantConflicts) {
				t.Fatalf("conflicts = %v, want %v", conflicts, tc.wantConflicts)
			}
		})
	}
}
