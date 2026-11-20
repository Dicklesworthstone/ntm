package terminal

import (
	"sync"
	"testing"
)

func resetCapabilityCacheForTest() {
	capabilityCache.Lock()
	capabilityCache.valid = false
	capabilityCache.value = Capabilities{}
	capabilityCache.Unlock()
}

func TestSupportsTrueColor(t *testing.T) {
	t.Parallel()

	cases := []struct {
		term      string
		colorterm string
		want      bool
	}{
		{"xterm-256color", "truecolor", true},
		{"xterm", "24bit", true},
		{"xterm-truecolor", "", true},
		{"xterm-24bit", "", true},
		{"xterm-kitty", "", true},
		{"wezterm", "", true},
		{"xterm-ghostty", "", true},
		{"alacritty", "", true},
		{"iterm2", "", true},
		{"xterm-256color", "", false},
		{"dumb", "", false},
		{"", "", false},
	}

	for _, tc := range cases {
		if got := supportsTrueColor(tc.term, tc.colorterm); got != tc.want {
			t.Errorf("supportsTrueColor(%q, %q) = %v, want %v", tc.term, tc.colorterm, got, tc.want)
		}
	}
}

func TestSupportsUnicodeBlocks(t *testing.T) {
	t.Setenv("NO_UNICODE", "")
	t.Setenv("LANG", "en_US.UTF-8")
	t.Setenv("LC_ALL", "")
	t.Setenv("LC_CTYPE", "")

	for _, term := range []string{"xterm-256color", "tmux-256color", "screen", "linux", "ghostty"} {
		if !supportsUnicodeBlocks(term) {
			t.Errorf("supportsUnicodeBlocks(%q) under UTF-8 = false, want true", term)
		}
	}
	for _, limited := range []string{"dumb", "vt100", ""} {
		if supportsUnicodeBlocks(limited) {
			t.Errorf("supportsUnicodeBlocks(%q) = true, want false", limited)
		}
	}

	t.Setenv("NO_UNICODE", "1")
	if supportsUnicodeBlocks("xterm-256color") {
		t.Error("supportsUnicodeBlocks must return false when NO_UNICODE=1")
	}

	t.Setenv("NO_UNICODE", "")
	t.Setenv("LANG", "C")
	if supportsUnicodeBlocks("custom-non-utf8-term") {
		t.Error("supportsUnicodeBlocks must return false for unknown term without UTF-8 locale")
	}
}

func TestDetectCachesAndIsRaceFree(t *testing.T) {
	t.Cleanup(resetCapabilityCacheForTest)
	resetCapabilityCacheForTest()

	t.Setenv("TERM", "xterm-ghostty")
	t.Setenv("COLORTERM", "truecolor")
	t.Setenv("NO_UNICODE", "")
	t.Setenv("LANG", "en_US.UTF-8")

	var wg sync.WaitGroup
	results := make([]Capabilities, 16)
	for i := range results {
		wg.Add(1)
		go func(idx int) {
			defer wg.Done()
			results[idx] = Detect()
		}(i)
	}
	wg.Wait()

	for i, got := range results {
		if !got.TrueColor || !got.UnicodeBlocks || got.Term != "xterm-ghostty" {
			t.Fatalf("goroutine %d Detect() = %+v, want TrueColor=true UnicodeBlocks=true Term=xterm-ghostty", i, got)
		}
	}
	if !SupportsTrueColor() || !SupportsUnicodeBlocks() {
		t.Fatal("wrapper helpers disagreed with Detect()")
	}
}
