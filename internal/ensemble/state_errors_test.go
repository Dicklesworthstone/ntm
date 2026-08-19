package ensemble

import "testing"

func TestSessionStore_Errors(t *testing.T) {
	if _, err := LoadSession(""); err == nil {
		t.Fatal("expected error for empty session name")
	}
	if err := SaveSession("", nil); err == nil {
		t.Fatal("expected error for nil session")
	}
}
