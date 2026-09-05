package tmux

import "testing"

func TestExactTarget(t *testing.T) {
	cases := []struct {
		in, want string
	}{
		{"", ""},
		{"foo", "=foo"},
		{"midas_edge", "=midas_edge"},
		{"foo:1", "=foo:1"},
		{"foo:1.2", "=foo:1.2"},
		{"foo:.3", "=foo:.3"},
		{"=foo", "=foo"},         // already exact
		{"=foo:1.2", "=foo:1.2"}, // already exact compound
		{"%12", "%12"},           // pane ID is always exact
		{"$3", "$3"},             // session ID is always exact
		{"@7", "@7"},             // window ID is always exact
		{":1.2", ":1.2"},         // current-session relative
		{".3", ".3"},             // current-window relative
		{"with-dashes", "=with-dashes"},
		{"MixedCase123", "=MixedCase123"},
	}
	for _, c := range cases {
		if got := ExactTarget(c.in); got != c.want {
			t.Errorf("ExactTarget(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}

func TestTargetSession(t *testing.T) {
	if got := TargetSession("midas_edge"); got != "=midas_edge" {
		t.Errorf("TargetSession(midas_edge) = %q, want =midas_edge", got)
	}
	if got := TargetSession("%5"); got != "%5" {
		t.Errorf("TargetSession(%%5) = %q, want %%5", got)
	}
}

func TestSessionOptionTarget(t *testing.T) {
	cases := []struct {
		in, want string
	}{
		{"", ""},
		{"foo", "=foo:"},
		{"=foo", "=foo:"},
		{"%12", "%12"},
		{"$3", "$3"},
		{"=foo:", "=foo:"},
		{"foo:0", "=foo:0"},
	}
	for _, c := range cases {
		if got := SessionOptionTarget(c.in); got != c.want {
			t.Errorf("SessionOptionTarget(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}

func TestSessionPaneTarget(t *testing.T) {
	cases := []struct {
		in, want string
	}{
		{"", ""},
		{"foo", "=foo:"},
		{"=foo", "=foo:"},
		{"%12", "%12"},
		{"$3", "$3"},
		{"@7", "@7"},
		{":1", ":1"},
		{".2", ".2"},
		{"=foo:", "=foo:"},
		{"foo:1", "=foo:1"},
		{"foo:1.2", "=foo:1.2"},
	}
	for _, c := range cases {
		if got := SessionPaneTarget(c.in); got != c.want {
			t.Errorf("SessionPaneTarget(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}
