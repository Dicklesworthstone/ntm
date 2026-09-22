package worksource

import (
	"reflect"
	"testing"
)

func TestMutexKeyCanonicalAcrossPlannerAndClaims(t *testing.T) {
	for _, tc := range []struct {
		label, key string
		valid      bool
	}{
		{"mutex:db", "db", true}, {"\t MuTeX: \u2003ÄREA\u00a0", "ärea", true},
		{"mutex:db:primary", "db:primary", true}, {"mutex:a'b", "a'b", true},
		{"mutex", "mutex", false}, {"mutex:", "", false}, {"mutex:\u2003", "", false},
		{"prefix-mutex:db", "prefix-mutex:db", false}, {"mutex :db", "mutex :db", false},
	} {
		key, valid := MutexKey(tc.label)
		if key != tc.key || valid != tc.valid {
			t.Errorf("MutexKey(%q) = %q %v, want %q %v", tc.label, key, valid, tc.key, tc.valid)
		}
	}
}

func TestClaimHoldsMutexMatchesSourceLifecycle(t *testing.T) {
	for _, tc := range []struct {
		status, owner string
		held          bool
	}{
		{"in_progress", "", true}, {" IN_PROGRESS ", "\t", true},
		{"open", "worker", true}, {"blocked", "worker", true}, {"deferred", "worker", true},
		{"unrecognized", "worker", true}, {"open", "\u2003", false},
		{"closed", "historic", false}, {" TOMBSTONE ", "historic", false}, {"deferred", "", false},
	} {
		if held := ClaimHoldsMutex(tc.status, tc.owner); held != tc.held {
			t.Errorf("holder(%q, %q) = %v, want %v", tc.status, tc.owner, held, tc.held)
		}
		source := &Snapshot{issues: map[string]issue{
			"candidate": {ID: "candidate", Status: "open", Labels: []string{"mutex:db"}},
			"holder":    {ID: "holder", Status: normalized(tc.status), Assignee: tc.owner, Labels: []string{"mutex:db", "private"}},
		}}
		verdict := source.Filter([]string{"candidate"}, EligibilityPolicy{})
		if tc.held {
			if len(verdict.EligibleIDs) != 0 || len(verdict.Excluded) != 1 || !reflect.DeepEqual(verdict.Excluded[0].Reasons, []string{"mutex_held"}) {
				t.Errorf("planner lost holder %q/%q: %+v", tc.status, tc.owner, verdict)
			}
		} else if !reflect.DeepEqual(verdict.EligibleIDs, []string{"candidate"}) {
			t.Errorf("planner retained historic holder %q/%q: %+v", tc.status, tc.owner, verdict)
		}
	}
}
