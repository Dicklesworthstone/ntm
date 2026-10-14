package util

import (
	"fmt"
	"math/rand"
	"strings"
	"testing"
)

// The oracle deliberately does not share chunk search or prefix-function
// logic with production. Exhaust small inputs and sample repetitive captures
// large enough to enter the long-overlap fallback.
func bruteForceNewOutput(before, after string) string {
	for overlap := min(len(before), len(after)); overlap > 0; overlap-- {
		if before[len(before)-overlap:] == after[:overlap] {
			return after[overlap:]
		}
	}
	return after
}

func TestExtractNewOutputExhaustiveByteOverlaps(t *testing.T) {
	inputs := []string{""}
	for n := 1; n <= 7; n++ {
		for bits := 0; bits < 1<<n; bits++ {
			value := make([]byte, n)
			for i := range value {
				value[i] = byte((bits >> i) & 1)
			}
			inputs = append(inputs, string(value))
		}
	}
	for _, before := range inputs {
		for _, after := range inputs {
			if got, want := ExtractNewOutput(before, after), bruteForceNewOutput(before, after); got != want {
				t.Fatalf("before=%q after=%q: got %q, want %q", before, after, got, want)
			}
		}
	}
}

func TestExtractNewOutputRepetitiveCaptureOracle(t *testing.T) {
	rng := rand.New(rand.NewSource(343))
	for iteration := 0; iteration < 6000; iteration++ {
		period := make([]byte, 1+rng.Intn(12))
		for i := range period {
			period[i] = byte(rng.Intn(4))
		}
		before := []byte(strings.Repeat(string(period), 16+rng.Intn(32)))
		after := []byte(strings.Repeat(string(period), 16+rng.Intn(32)))
		for mutation := 0; mutation < 1+rng.Intn(4); mutation++ {
			before[rng.Intn(len(before))] = byte(rng.Intn(8))
			after[rng.Intn(len(after))] = byte(rng.Intn(8))
		}
		old, current := string(before), string(after)
		if got, want := ExtractNewOutput(old, current), bruteForceNewOutput(old, current); got != want {
			t.Fatalf("iteration=%d before=%q after=%q: got %q, want %q", iteration, old, current, got, want)
		}
	}
}

func TestExtractNewOutputLongCandidateCollisions(t *testing.T) {
	const block = "0123456789012345678901234567890123456789"
	for _, tc := range []struct{ name, before, after, want string }{
		{"late valid long overlap", block + "wrong" + block + "right", block + "right:" + strings.Repeat("new", 30), ":" + strings.Repeat("new", 30)},
		{"many false candidates then valid overlap", strings.Repeat(block+"wrong", 12) + block + "right", block + "right:" + strings.Repeat("new", 300), ":" + strings.Repeat("new", 300)},
		{"false chunk without overlap", block + "Xabc", block + strings.Repeat("Y", 20), block + strings.Repeat("Y", 20)},
		{"false chunk then three byte overlap", block + "X012", block + strings.Repeat("Y", 20), block[3:] + strings.Repeat("Y", 20)},
		{"short after repeated before", "aaaaab", "aaa", "aaa"},
		{"short complete suffix", "aaaaabaaa", "aaa", ""},
		{"null bytes", strings.Repeat("\x00", 100) + "X\x00\x00", strings.Repeat("\x00", 80) + "Y", strings.Repeat("\x00", 78) + "Y"},
		{"unicode capture", "old\n" + strings.Repeat("日本語\n", 30), strings.Repeat("日本語\n", 30) + "next", "next"},
		{"ansi and carriage returns", "old\r\x1b[32mready\x1b[0m\r\n", "\x1b[32mready\x1b[0m\r\nnew", "new"},
		{"invalid UTF-8 preserved", strings.Repeat("\xff\xfe", 60) + "X", strings.Repeat("\xff\xfe", 60) + "Y", strings.Repeat("\xff\xfe", 60) + "Y"},
		{"large terminal repaint", strings.Repeat(" ", 128*1024) + "old", strings.Repeat(" ", 128*1024) + "new", strings.Repeat(" ", 128*1024) + "new"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := ExtractNewOutput(tc.before, tc.after); got != tc.want {
				t.Fatalf("got %d bytes, want %d; before=%d after=%d", len(got), len(tc.want), len(tc.before), len(tc.after))
			}
		})
	}
}

func TestExtractNewOutputCommonPathsDoNotAllocate(t *testing.T) {
	const overlap = "a distinctive line that is longer than forty characters\n"
	for _, tc := range []struct{ name, before, after string }{
		{"append", "old", "old\nnew"},
		{"unchanged", "same", "same"},
		{"scrolled", "previous\n" + overlap, overlap + "new"},
		{"no overlap", strings.Repeat("x", 128), strings.Repeat("y", 128)},
		{"one false candidate", overlap + "failed\n" + overlap + "done", overlap + "done" + strings.Repeat("new", 100)},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := testing.AllocsPerRun(100, func() { captureDeltaSink = ExtractNewOutput(tc.before, tc.after) }); got != 0 {
				t.Fatalf("common path allocates: %v", got)
			}
		})
	}
}

func FuzzExtractNewOutputExactOverlap(f *testing.F) {
	f.Add("", "new")
	f.Add("old\ncurrent\n", "current\nnew\n")
	f.Add(strings.Repeat("a", 80)+"b", strings.Repeat("a", 80)+"c")
	f.Add(strings.Repeat("\xff\x00", 40), strings.Repeat("\xff\x00", 40)+"x")
	f.Fuzz(func(t *testing.T, before, after string) {
		// Bound only the quadratic test oracle, not production extraction.
		if len(before) > 2048 || len(after) > 2048 {
			return
		}
		if got, want := ExtractNewOutput(before, after), bruteForceNewOutput(before, after); got != want {
			t.Fatalf("before=%q after=%q: got %q, want %q", before, after, got, want)
		}
	})
}

var captureDeltaSink string

// Run the incumbent and candidate in the same benchmark invocation. This is
// helper-level evidence, not a claim of end-to-end pipeline speedup. The
// fallback trades O(overlap) auxiliary memory for a bound on repeated work;
// ordinary appends and first-candidate overlaps should remain allocation-free.
func BenchmarkExtractNewOutputComparison(b *testing.B) {
	const line = "a distinctive line that is longer than forty characters\n"
	cases := []struct{ name, before, after, want string }{
		{"append", strings.Repeat(line, 100), strings.Repeat(line, 100) + "new", "new"},
		{"scrolled", "old\n" + line, line + "new", "new"},
		{"no_overlap", strings.Repeat("x", 8192), strings.Repeat("y", 8192), strings.Repeat("y", 8192)},
		{"short_false_candidate", line + "failed\n" + line + "done", line + "done" + strings.Repeat("new", 100), strings.Repeat("new", 100)},
	}
	for _, n := range []int{8192, 32768, 131072} {
		prefix := strings.Repeat("a", n)
		cases = append(cases, struct{ name, before, after, want string }{
			fmt.Sprintf("repaint_%d", n), prefix + "b", prefix + "c", prefix + "c",
		})
	}
	for _, tc := range cases {
		b.Run(tc.name, func(b *testing.B) {
			for _, implementation := range []struct {
				name string
				run  func(string, string) string
			}{{"incumbent", extractNewOutputIncumbent}, {"candidate", ExtractNewOutput}} {
				b.Run(implementation.name, func(b *testing.B) {
					if got := implementation.run(tc.before, tc.after); got != tc.want {
						b.Fatal("benchmark output differs from expected delta")
					}
					b.ReportAllocs()
					b.ResetTimer()
					for i := 0; i < b.N; i++ {
						captureDeltaSink = implementation.run(tc.before, tc.after)
					}
				})
			}
		})
	}
}

// Test-only incumbent from text.go at eb2449c. Keep the production algorithm
// single: this copy is compiled only for reproducible comparative benchmarks.
func extractNewOutputIncumbent(before, after string) string {
	if before == "" {
		return after
	}
	if after == "" {
		return ""
	}
	if len(after) >= len(before) && after[:len(before)] == before {
		return after[len(before):]
	}
	const chunkSize = 40
	var searchChunk string
	if len(after) >= chunkSize {
		searchChunk = after[:chunkSize]
	} else {
		searchChunk = after
	}
	scanStart := len(before) - len(after)
	if scanStart < 0 {
		scanStart = 0
	}
	searchRegion := before[scanStart:]
	remaining := searchRegion
	offset := 0
	for {
		idx := strings.Index(remaining, searchChunk)
		if idx == -1 {
			break
		}
		absIdx := scanStart + offset + idx
		suffixLen := len(before) - absIdx
		if len(after) >= suffixLen && after[:suffixLen] == before[absIdx:] {
			return after[suffixLen:]
		}
		step := idx + 1
		remaining = remaining[step:]
		offset += step
	}
	maxOverlap := len(after) - 1
	if maxOverlap > len(before) {
		maxOverlap = len(before)
	}
	if maxOverlap >= chunkSize {
		maxOverlap = chunkSize - 1
	}
	for k := maxOverlap; k > 0; k-- {
		if before[len(before)-k:] == after[:k] {
			return after[k:]
		}
	}
	return after
}
