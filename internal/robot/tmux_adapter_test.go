package robot

import (
	"testing"

	"github.com/Dicklesworthstone/ntm/internal/tmux"
)

// TestExtractSessionLabel pins bead bd-ws1-truth-safety-l5ddi.7 (B4): the robot
// listing's label extractor must split on the canonical "--" session-label
// separator (internal/config/label.go), not the "__" pane-title separator.
func TestExtractSessionLabel(t *testing.T) {
	tests := []struct {
		name string
		want string
	}{
		{"myproject", ""},                   // unlabeled -> no label
		{"myproject--frontend", "frontend"}, // labeled spawn
		{"my-project--fix-1", "fix-1"},      // dashes in base and label
		{"my--project--fix", "fix"},         // delimiter collision: LAST "--" wins
		{"myproject__cc_1", ""},             // pane-title-shaped name is NOT a session label
	}
	for _, tt := range tests {
		if got := extractSessionLabel(tt.name); got != tt.want {
			t.Errorf("extractSessionLabel(%q) = %q, want %q", tt.name, got, tt.want)
		}
	}
}

// TestNormalizeSession_Label asserts the projection surface (`ntm robot
// sessions --json` flows RuntimeSession.Label) carries the session label for a
// labeled session and omits it for an unlabeled one.
func TestNormalizeSession_Label(t *testing.T) {
	a := NewTmuxAdapter(DefaultTmuxAdapterConfig())

	labeled := &tmux.Session{Name: "myproject--frontend"}
	rs := a.NormalizeSession(labeled, nil)
	if rs.Label != "frontend" {
		t.Errorf("NormalizeSession(%q).Label = %q, want %q", labeled.Name, rs.Label, "frontend")
	}
	if rs.Name != "myproject--frontend" {
		t.Errorf("NormalizeSession(%q).Name = %q, want full session name", labeled.Name, rs.Name)
	}

	unlabeled := &tmux.Session{Name: "myproject"}
	rs = a.NormalizeSession(unlabeled, nil)
	if rs.Label != "" {
		t.Errorf("NormalizeSession(%q).Label = %q, want empty (unlabeled)", unlabeled.Name, rs.Label)
	}
}
