// Package reservationpath compares repository-relative reservation patterns.
// It does not inspect the filesystem: a reservation protects future files too.
package reservationpath

import (
	"sort"
	"strings"
	"unicode/utf8"
)

const (
	maxPatternBytes  = 4096
	maxProductStates = 65536
	maxRangeChecks   = 1 << 20
)

type span struct{ lo, hi rune }
type edge struct {
	to    int
	chars []span // nil is an epsilon transition; an empty non-nil set matches nothing.
}
type node struct {
	edges  []edge
	accept bool
}
type machine struct{ nodes []node }

var (
	anyRune     = []span{{1, 0xd7ff}, {0xe000, utf8.MaxRune}}
	segmentRune = []span{{1, '/' - 1}, {'/' + 1, 0xd7ff}, {0xe000, utf8.MaxRune}}
)

// MayOverlap reports whether two reservations may protect a common path.
// Patterns use path.Match's case-sensitive *, ?, escapes and [^a-z] classes,
// plus ** across directories and **/ for zero or more directory components.
// Unqualified globs apply to basenames at any depth. A literal reservation
// protects both its exact path and descendants (for example, "src").
//
// Invalid, oversized or excessively complex patterns conservatively overlap.
// A false result is therefore evidence of disjointness, not a failed parse or
// a search budget exhaustion. Empty strings do not describe reservations.
// This is read-only planning evidence, not an atomic reservation or a release
// authorization. In particular, callers must never use it to broaden releases.
func MayOverlap(a, b string) bool {
	a, b = strings.TrimSpace(a), strings.TrimSpace(b)
	if a == "" || b == "" {
		return false
	}
	left, ok := compile(a)
	if !ok {
		return true
	}
	right, ok := compile(b)
	if !ok {
		return true
	}
	return intersect(left, right)
}

// Matches checks one concrete path against a reservation. Unlike MayOverlap,
// glob metacharacters in name are always literal filename characters. Invalid
// patterns and exhausted bounds fail closed just as they do in MayOverlap.
func Matches(pattern, name string) bool {
	pattern, name = strings.TrimSpace(pattern), strings.TrimSpace(name)
	if pattern == "" || name == "" {
		return false
	}
	left, ok := compile(pattern)
	if !ok || len(name) > maxPatternBytes || !utf8.ValidString(name) || strings.ContainsRune(name, 0) {
		return true
	}
	right := machine{nodes: []node{{}}}
	at := 0
	for _, r := range name {
		at = right.character(at, []span{{r, r}})
	}
	right.nodes[at].accept = true
	return intersect(left, right)
}

func (m *machine) next() int {
	m.nodes = append(m.nodes, node{})
	return len(m.nodes) - 1
}

func (m *machine) character(from int, chars []span) int {
	next := m.next()
	m.nodes[from].edges = append(m.nodes[from].edges, edge{to: next, chars: chars})
	return next
}

func (m *machine) star(from int, chars []span) int {
	next := m.next()
	m.nodes[from].edges = append(m.nodes[from].edges, edge{to: from, chars: chars}, edge{to: next})
	return next
}

// directories accepts an optional prefix ending at a directory separator.
// The slash belongs to the optional group, so **/ cannot swallow an arbitrary
// filename prefix. Absolute and repeated separators remain representable too.
func (m *machine) directories(from int) int {
	next := m.next()
	inside := m.next()
	m.nodes[from].edges = append(m.nodes[from].edges, edge{to: next}, edge{to: inside})
	m.nodes[inside].edges = append(m.nodes[inside].edges,
		edge{to: inside, chars: anyRune}, edge{to: next, chars: []span{{'/', '/'}}})
	return next
}

func compile(pattern string) (machine, bool) {
	m := machine{nodes: []node{{}}}
	if len(pattern) > maxPatternBytes || !utf8.ValidString(pattern) || strings.ContainsRune(pattern, 0) {
		return m, false
	}
	at, glob := 0, false
	// Reserve state zero as the start. For a basename glob it will acquire
	// a recursive-directory alternative after we know whether syntax is glob.
	start := m.next()
	m.nodes[at].edges = append(m.nodes[at].edges, edge{to: start})
	at = start
	p := []rune(pattern)
	for i := 0; i < len(p); {
		switch p[i] {
		case '\\':
			i++
			if i == len(p) {
				return m, false
			}
			at = m.character(at, []span{{p[i], p[i]}})
			i++
		case '*':
			glob = true
			begin := i
			for i < len(p) && p[i] == '*' {
				i++
			}
			if i-begin >= 2 {
				if (begin == 0 || p[begin-1] == '/') && i < len(p) && p[i] == '/' {
					at = m.directories(at)
					i++
				} else {
					at = m.star(at, anyRune)
				}
			} else {
				at = m.star(at, segmentRune)
			}
		case '?':
			glob = true
			at = m.character(at, segmentRune)
			i++
		case '[':
			glob = true
			chars, end, ok := characterClass(p, i+1)
			if !ok {
				return m, false
			}
			at = m.character(at, chars)
			i = end
		default:
			at = m.character(at, []span{{p[i], p[i]}})
			i++
		}
	}
	m.nodes[at].accept = true
	if glob && !strings.ContainsRune(pattern, '/') {
		prefix := m.directories(0)
		m.nodes[prefix].edges = append(m.nodes[prefix].edges, edge{to: start})
	}
	if !glob {
		// The exact literal remains accepted as well as its subtree. Avoid a
		// duplicate slash for an explicitly directory-shaped reservation.
		if !strings.HasSuffix(pattern, "/") {
			at = m.character(at, []span{{'/', '/'}})
		}
		at = m.star(at, anyRune)
		m.nodes[at].accept = true
	}
	return m, true
}

func characterClass(p []rune, i int) ([]span, int, bool) {
	negated := i < len(p) && p[i] == '^'
	if negated {
		i++
	}
	chars := make([]span, 0)
	count := 0
	read := func() (rune, bool) {
		if i >= len(p) || p[i] == ']' || p[i] == '-' {
			return 0, false
		}
		if p[i] == '\\' {
			i++
			if i == len(p) {
				return 0, false
			}
		}
		r := p[i]
		i++
		return r, true
	}
	for i < len(p) {
		if p[i] == ']' && count > 0 {
			chars = mergeSpans(chars)
			if negated {
				inverse := make([]span, 0, len(chars)+1)
				low := rune(1)
				for _, s := range chars {
					if low < s.lo {
						inverse = append(inverse, span{low, s.lo - 1})
					}
					low = s.hi + 1
				}
				if low <= utf8.MaxRune {
					inverse = append(inverse, span{low, utf8.MaxRune})
				}
				chars = inverse
			}
			return chars, i + 1, true
		}
		lo, ok := read()
		if !ok {
			break
		}
		hi := lo
		if i < len(p) && p[i] == '-' {
			i++
			hi, ok = read()
			if !ok {
				break
			}
		}
		if lo <= hi {
			chars = append(chars, span{lo, hi})
		}
		count++
	}
	return nil, 0, false
}

func mergeSpans(chars []span) []span {
	sort.Slice(chars, func(i, j int) bool { return chars[i].lo < chars[j].lo })
	out := chars[:0]
	for _, s := range chars {
		if len(out) > 0 && s.lo <= out[len(out)-1].hi+1 {
			if s.hi > out[len(out)-1].hi {
				out[len(out)-1].hi = s.hi
			}
		} else {
			out = append(out, s)
		}
	}
	return out
}

// intersect explores pairs of NFA states, not concrete filenames or possible
// wildcard expansions. Epsilon transitions advance either side independently;
// consuming transitions advance both only if their character sets intersect.
func intersect(a, b machine) bool {
	type pair struct{ a, b int }
	queue := []pair{{}}
	seen := map[pair]bool{{}: true}
	budget := maxRangeChecks
	add := func(p pair) {
		if !seen[p] {
			seen[p] = true
			queue = append(queue, p)
		}
	}
	for head := 0; head < len(queue); head++ {
		if len(queue) > maxProductStates || budget <= 0 {
			return true // Unverified is never disjoint.
		}
		p := queue[head]
		left, right := a.nodes[p.a], b.nodes[p.b]
		if left.accept && right.accept {
			return true
		}
		for _, e := range left.edges {
			if e.chars == nil {
				add(pair{e.to, p.b})
			}
		}
		for _, e := range right.edges {
			if e.chars == nil {
				add(pair{p.a, e.to})
			}
		}
		for _, l := range left.edges {
			if l.chars == nil {
				continue
			}
			for _, r := range right.edges {
				if r.chars == nil {
					continue
				}
				for i, j := 0, 0; i < len(l.chars) && j < len(r.chars); {
					budget--
					if budget <= 0 {
						return true
					}
					if l.chars[i].hi < r.chars[j].lo {
						i++
					} else if r.chars[j].hi < l.chars[i].lo {
						j++
					} else {
						add(pair{l.to, r.to})
						break
					}
				}
			}
		}
	}
	return false
}
