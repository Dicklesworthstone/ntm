package tracker

import (
	"testing"
)

func TestFileChangeStore(t *testing.T) {
	store := NewFileChangeStore(2)

	store.Add(RecordedFileChange{Session: "s1"})
	store.Add(RecordedFileChange{Session: "s2"})
	store.Add(RecordedFileChange{Session: "s3"}) // Should drop s1

	all := store.All()
	if len(all) != 2 {
		t.Errorf("expected 2 entries (limit), got %d", len(all))
	}
	if all[0].Session != "s2" {
		t.Errorf("expected oldest to be s2, got %s", all[0].Session)
	}
	if all[1].Session != "s3" {
		t.Errorf("expected newest to be s3, got %s", all[1].Session)
	}
}
