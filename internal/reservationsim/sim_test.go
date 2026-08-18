package reservationsim

import (
	"testing"
	"time"
)

// stepClock is a tiny mutable Clock for tests.
type stepClock struct{ now time.Time }

func (c *stepClock) Now() time.Time          { return c.now }
func (c *stepClock) Advance(d time.Duration) { c.now = c.now.Add(d) }

func newClockAt(t time.Time) *stepClock { return &stepClock{now: t} }

func anchor() time.Time { return time.Date(2026, 5, 8, 12, 0, 0, 0, time.UTC) }

// bd-zshpx: an unrelated stale lease that ages out during another
// pattern's acquire MUST NOT promote that acquire to
// OutcomeExpiredReclaimed. Only a reaped lease whose pattern
// overlapped the new acquire's pattern justifies that outcome.

// Companion to the bd-zshpx fix: when a stale lease overlapping the
// new acquire's pattern is reaped during the same call, the outcome
// must still be OutcomeExpiredReclaimed (the pre-existing semantics
// remain).

// Companion: when MULTIPLE stale leases age out during one acquire and
// only ONE of them overlaps the request, the outcome must still be
// expired_reclaimed (the overlap fired) and the unrelated reap must
// not be required for that signal.

func TestPatternsOverlap_GlobMatrix(t *testing.T) {
	t.Parallel()
	cases := []struct {
		a, b string
		want bool
	}{
		{"a/**", "a/b/c.go", true},
		{"a/b/c.go", "a/**", true},
		{"a/**", "b/**", false},
		{"a/**", "a/**", true},
		{"a/*", "a/b", true},
		{"a/*", "a/b/c", false},
		{"*.go", "foo.go", true},
		{"foo.go", "*.go", true},
		{"foo.go", "bar.go", false},
		{"a/**", "a", true},
		{"", "a/**", false},

		// bd-6286k: bare "**" must be a catch-all just like "/**".
		// Pre-fix, patternsOverlap("**", "foo/bar.go") returned false
		// because HasSuffix("**", "/**") is false and path.Match's `*`
		// cannot cross `/`.
		{"**", "foo/bar.go", true},
		{"**", "deep/nested/file.go", true},
		{"**", "anyfile.go", true},
		{"foo/bar.go", "**", true},  // symmetric
		{"/**", "foo/bar.go", true}, // already worked — pin it
	}
	for _, c := range cases {
		got := patternsOverlap(c.a, c.b)
		if got != c.want {
			t.Errorf("patternsOverlap(%q, %q) = %v, want %v", c.a, c.b, got, c.want)
		}
	}
}
