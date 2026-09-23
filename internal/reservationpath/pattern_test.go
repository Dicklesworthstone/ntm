package reservationpath

import (
	"fmt"
	"math/rand"
	"path"
	"strings"
	"sync"
	"testing"
)

func TestMayOverlap(t *testing.T) {
	tests := []struct {
		a, b string
		want bool
	}{
		{"src/*/main.go", "src/service/*.go", true},
		{"src/*/main.go", "src/service/*.rs", false},
		{"src/a*.go", "src/*b.go", true},
		{"src/a*.go", "src/b*.go", false},
		{"src/*/a.go", "src/*/b.go", false},
		{"src/?/main.go", "src/[a-z]/*.go", true},
		{"src/[a-c]*", "src/[d-f]*", false},
		{"src/[^a-c]*", "src/[b-d]*", true},
		{"src/[^a-c]*", "src/[a-c]*", false},
		{"src/[α-γ]*.go", "src/β?.go", true},
		{"src/[α-γ]*.go", "src/[δ-ζ]*.go", false},
		{"src/**/test?.go", "src/test1.go", true},
		{"src/**/test?.go", "src/pkg/sub/test1.go", true},
		{"src/**/test?.go", "src/pkg/*1.go", true},
		{"src/**/test?.go", "other/**/*.go", false},
		{"src/*/test?.go", "src/test1.go", true}, // src/test1.go/test2.go: literals also reserve subtrees.
		{"src/*/test?.go", "src/a/b/test1.go", false},
		{"src/**/a/**/b.go", "src/a/b.go", true},
		{"src/**/a/**/b.go", "src/x/a/y/z/b.go", true},
		{"src/**/a/**/b.go", "src/x/ab.go", true}, // src/x/ab.go/a/b.go.
		{"src/**/a", "src/beta", true},            // src/beta/a.
		{"src/*", "src/nested/file.go", false},
		{"src/**", "src/nested/file.go", true},
		{"**", "a/b/c", true},
		{"*.go", "src/sub/main.go", true},
		{"main*.go", "src/sub/main_test.go", true},
		{"?.go", "src/sub/a.go", true},
		{"*.go", "*.rs", false},
		{"src", "src/sub/main.go", true},
		{"src/", "src/sub/main.go", true},
		{"src", "src/**", true},
		{"src", "src-extra/main.go", false},
		{"src", "other", false},
		{"src/file.go", "src/file.go", true},
		{"Src/*.go", "src/*.go", false},
		{`src/\*.go`, "src/?.go", true},
		{`src/\*.go`, "src/a.go", false},
		{`src/[\]\-].go`, `src/\].go`, true},
		{"src/[z-a]", "src/*", false},
		{"src/[^z-a]", "src/a", true},
		{"", "**", false},
		{"**", " \t", false},
	}
	for _, tc := range tests {
		t.Run(tc.a+" vs "+tc.b, func(t *testing.T) {
			t.Parallel()
			if got := MayOverlap(tc.a, tc.b); got != tc.want {
				t.Fatalf("MayOverlap(%q, %q) = %v, want %v", tc.a, tc.b, got, tc.want)
			}
			if got := MayOverlap(tc.b, tc.a); got != tc.want {
				t.Fatalf("reverse MayOverlap(%q, %q) = %v, want %v", tc.b, tc.a, got, tc.want)
			}
		})
	}
}

func TestMatchesKeepsConcretePathLiteral(t *testing.T) {
	for _, tc := range []struct {
		pattern, name string
		want          bool
	}{
		{"src/*/main.go", "src/service/main.go", true},
		{"src/*.go", "src/file[1].go", true},
		{"src/file[1].go", "src/file[1].go", false},
		{`src/file\[1].go`, "src/file[1].go", true},
		{"src/a*.go", "src/*a.go", false},
		{"src/a*.go", "src/a*.go", true},
		{"src/**/main.go", "src/main.go", true},
		{"src/**/main.go", "src/notmain.go", false},
		{"src/*/test?.go", "src/test1.go", false},
		{"src/**/a/**/b.go", "src/x/ab.go", false},
		{"src/**/a", "src/beta", false},
		{"*.go", "deep/path/main.go", true},
		{"main*.go", "deep/path/main.go", true},
		{"main*.go", "/absolute/path/main.go", true},
		{"src/**/main.go", "src//main.go", true},
		{"src", "src/main.go", true},
		{"src", "src-else/main.go", false},
		{"src/*.go", "src/deep/main.go", false},
		{"src/[!a]", "src/!", true}, // path.Match, not shell ! negation.
		{"src/[!a]", "src/a", true},
		{"src/[!a]", "src/b", false},
	} {
		if got := Matches(tc.pattern, tc.name); got != tc.want {
			t.Errorf("Matches(%q, %q) = %v, want %v", tc.pattern, tc.name, got, tc.want)
		}
	}
}

func TestUnverifiablePatternsNeverProveDisjointness(t *testing.T) {
	for _, pattern := range []string{
		"src/[", "src/[]", "src/[a-]", "src/[-a]", "src/\\", "src/\x00", "src/\xff", strings.Repeat("x", maxPatternBytes+1),
	} {
		if !MayOverlap(pattern, "unrelated/file") || !MayOverlap("unrelated/file", pattern) || !Matches(pattern, "unrelated/file") {
			t.Errorf("unverifiable pattern %q was treated as disjoint", pattern)
		}
	}
}

func TestMatchesAgreesWithPathMatchForQualifiedGlobs(t *testing.T) {
	rng := rand.New(rand.NewSource(712991))
	terms := []string{"a", "b", "α", "β", "/", "?", "*", "[ab]", "[^b]", "[α-γ]", `\*`, `\?`, `[\]\-]`}
	alphabet := []rune("abαβ/*?]-")
	for trial := 0; trial < 5000; trial++ {
		pattern := "src/?" // Always qualified and genuinely a glob, never a subtree literal.
		for n := rng.Intn(6); n > 0; n-- {
			term := terms[rng.Intn(len(terms))]
			if term == "*" && strings.HasSuffix(pattern, "*") {
				term = "a" // ** is the documented extension, not path.Match.
			}
			pattern += term
		}
		name := "src/"
		for n := rng.Intn(8); n > 0; n-- {
			name += string(alphabet[rng.Intn(len(alphabet))])
		}
		want, err := path.Match(pattern, name)
		if err != nil {
			t.Fatalf("invalid generated pattern %q: %v", pattern, err)
		}
		if got := Matches(pattern, name); got != want {
			t.Fatalf("Matches(%q, %q) = %v, path.Match = %v", pattern, name, got, want)
		}
	}
}

func TestIntersectionAgainstExhaustiveFilenameOracle(t *testing.T) {
	// Every possible constrained character in these tiny patterns is in the
	// alphabet. Unrestricted wildcards can use 'a'. Six characters suffice
	// for a shortest witness for any pair in this fixed corpus.
	patterns := []string{"a/?a", "a/a?", "a/[ab]b", "a/[b]a", "b/[ab]?", "[ab]/[ab]", "[a]/[b]", "[b]/[a]", "a/[a-b]", "a/*a", "a/b*", "a/*b", "a/a*", "b/*a"}
	names := []string{""}
	level := []string{""}
	for depth := 0; depth < 6; depth++ {
		next := make([]string, 0, len(level)*3)
		for _, prefix := range level {
			for _, r := range "ab/" {
				next = append(next, prefix+string(r))
			}
		}
		names = append(names, next...)
		level = next
	}
	accepted := make([]map[string]bool, len(patterns))
	for i, pattern := range patterns {
		accepted[i] = map[string]bool{}
		for _, name := range names {
			if matched, err := path.Match(pattern, name); err != nil {
				t.Fatal(err)
			} else if matched {
				accepted[i][name] = true
			}
		}
	}
	for i, a := range patterns {
		for j, b := range patterns {
			want := false
			for name := range accepted[i] {
				if accepted[j][name] {
					want = true
					break
				}
			}
			if got := MayOverlap(a, b); got != want {
				t.Fatalf("intersection(%q, %q) = %v, exhaustive oracle = %v", a, b, got, want)
			}
		}
	}
}

func TestComplexityBudgetFailsClosed(t *testing.T) {
	// Without memoized product states, alternating stars generate exponential
	// wildcard expansions. Even the polynomial product is bounded here.
	a := "src/" + strings.Repeat("*a", 400) + "x"
	b := "src/" + strings.Repeat("*a", 400) + "y"
	if !MayOverlap(a, b) {
		t.Fatal("budget exhaustion must not be reported as proven disjointness")
	}
}

func TestConcurrentPatternComparisons(t *testing.T) {
	var wg sync.WaitGroup
	for n := 0; n < 32; n++ {
		wg.Add(1)
		go func(n int) {
			defer wg.Done()
			for i := 0; i < 50; i++ {
				name := fmt.Sprintf("src/pkg%d/file%d.go", n, i)
				if !Matches("src/**/*.go", name) || !MayOverlap("src/*/file?.go", "src/pkg*/*.go") {
					t.Errorf("lost overlap for %s", name)
					return
				}
			}
		}(n)
	}
	wg.Wait()
}
