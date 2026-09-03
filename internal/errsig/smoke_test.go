package errsig

import (
	"regexp"
	"testing"
)

func TestSmoke(t *testing.T) {
	re := regexp.MustCompile(Anchored(`(?i:failed)\b(?:\s+to\b|\s*:)`))
	pos := []string{"Failed to connect", "  ⎿  failed: boom", "[stderr] Failed to write", "2026-09-03T10:00:00Z ERROR failed to sync"}
	neg := []string{"it failed naming exactly the seven against the HEAD ci.yml before the edit,", "the planted negative failed as expected", "and it failed to matter"}
	for _, s := range pos {
		if !re.MatchString(s) { t.Errorf("want match: %q", s) }
	}
	for _, s := range neg {
		if re.MatchString(s) { t.Errorf("want NO match: %q", s) }
	}
	rl := regexp.MustCompile(HTTPStatus("429", "too many requests"))
	for _, s := range []string{"HTTP 429", "HTTP/1.1 429 Too Many Requests", `{"status": 429}`, "429 Too Many Requests", "error 429"} {
		if !rl.MatchString(s) { t.Errorf("want rl match: %q", s) }
	}
	for _, s := range []string{"6f764f0c7344e437767e61f189f493065dca56cadc7048610948be7a5d42963f", "429 files scanned", "we saw 1429 items"} {
		if rl.MatchString(s) { t.Errorf("want NO rl match: %q", s) }
	}
	if !HasNonzeroExit("command exited with code 2") || HasNonzeroExit("exit status 0") {
		t.Error("nonzero exit")
	}
}
