package cli

import "testing"

func TestRunHistoryListRejectsNonPositiveLimit(t *testing.T) {
	err := runHistoryList(0, "", "", "", "", "", false)
	if err == nil {
		t.Fatalf("expected error for limit <= 0")
	}
}
