package errsig

import (
	"regexp"
	"testing"
)

// failedReport is the anchored failure-report body the pattern surfaces use.
const failedReport = `(?i:failed)\b(?:\s+to\b|\s+with\b|\s*[:.]|\s*$)`

func TestAnchoredIgnoresProseButKeepsReportLines(t *testing.T) {
	re := regexp.MustCompile(Anchored(failedReport))

	reports := []string{
		"Failed to connect to api.anthropic.com",
		"failed: could not open database",
		"  ⎿  Failed to write file",
		"│ Failed with status 500",
		"[stderr] Failed to sync",
		"2026-09-03T10:00:00Z ERROR failed to flush",
		"12:04:59.123 WARN  failed to renew lease",
		"- Failed to install dependency",
	}
	for _, line := range reports {
		if !re.MatchString(line) {
			t.Errorf("failure report not detected: %q", line)
		}
	}

	prose := []string{
		"... it failed naming exactly the seven against the HEAD ci.yml before the edit,",
		"the planted negative failed as expected, and 19/19 real-tree switches discriminate after",
		"I checked whether the retry failed to matter and it did not",
		"we should decide what happens when the upload failed with no retry budget left",
	}
	for _, line := range prose {
		if re.MatchString(line) {
			t.Errorf("prose classified as a failure report: %q", line)
		}
	}
}

func TestAnchoredIsMultiline(t *testing.T) {
	re := regexp.MustCompile(Anchored(failedReport))
	capture := "some transcript\nmore transcript\nFailed to connect\n❯ \n"
	if !re.MatchString(capture) {
		t.Fatal("Anchored must match a report line anywhere in a multi-line capture")
	}
	if re.MatchString("some transcript\nand then it failed quietly in the background\n❯ \n") {
		t.Fatal("Anchored must not match mid-sentence prose in a multi-line capture")
	}
}

func TestHTTPStatusRequiresStructuredPosition(t *testing.T) {
	re := regexp.MustCompile(HTTPStatus("429", "too many requests", "rate limit"))

	structured := []string{
		"HTTP 429",
		"HTTP/1.1 429 Too Many Requests",
		"HTTP/2 429",
		`{"status": 429}`,
		"status_code=429",
		"429 Too Many Requests",
		"429 - rate limit",
		"error 429",
	}
	for _, line := range structured {
		if !re.MatchString(line) {
			t.Errorf("structured status not detected: %q", line)
		}
	}

	// The exact SHA-256 from the ntm#299 report, plus other bare numbers.
	prose := []string{
		"6f764f0c7344e437767e61f189f493065dca56cadc7048610948be7a5d42963f",
		"429",
		"scanned 429 files",
		"we saw 1429 items",
		"commit 429abc",
		"budget: 4290ms",
	}
	for _, line := range prose {
		if re.MatchString(line) {
			t.Errorf("bare number classified as an HTTP status: %q", line)
		}
	}

	// Without reason words only the explicitly structured forms match.
	bare := regexp.MustCompile(HTTPStatus("401"))
	if !bare.MatchString("HTTP 401") || bare.MatchString("401 Unauthorized") {
		t.Fatal("HTTPStatus with no reasons must match only explicitly structured forms")
	}
}

func TestNonzeroExit(t *testing.T) {
	positives := []string{
		"command exited with code 2",
		"exit status 1",
		"exit code: 137",
		"process returned exit code 3",
		"exited with status 12",
	}
	for _, line := range positives {
		if !HasNonzeroExit(line) {
			t.Errorf("nonzero exit not detected: %q", line)
		}
	}
	negatives := []string{
		"exit status 0",
		"the command exited with code 0",
		"exit code: 0",
		"we will exit soon",
	}
	for _, line := range negatives {
		if HasNonzeroExit(line) {
			t.Errorf("clean exit classified as a failure: %q", line)
		}
	}
}

func TestFatalSignal(t *testing.T) {
	for _, line := range []string{"signal SIGSEGV", "SIGKILL received", "fatal: SIGABRT"} {
		if !HasFatalSignal(line) {
			t.Errorf("fatal signal not detected: %q", line)
		}
	}
	for _, line := range []string{"we handle SIGINT gracefully", "sigsegvish", "SIGHUP"} {
		if HasFatalSignal(line) {
			t.Errorf("non-fatal signal classified as fatal: %q", line)
		}
	}
}

func TestTrimLead(t *testing.T) {
	cases := map[string]string{
		"  ⎿  Failed to write":              "Failed to write",
		"[stderr] boom":                     "boom",
		"2026-09-03T10:00:00Z ERROR failed": "failed",
		// "Error"/"WARN"/… are stripped as log-level prefixes; the regex
		// alternation still matches because that group is optional.
		"│ Error: overloaded":                     "overloaded",
		"plain line":                              "plain line",
		"12:04:59 WARN  failed to renew":          "failed to renew",
		"- the planted negative failed as needed": "the planted negative failed as needed",
	}
	for in, want := range cases {
		if got := TrimLead(in); got != want {
			t.Errorf("TrimLead(%q) = %q, want %q", in, got, want)
		}
	}
}

// TestHTTPStatusIsCaseInsensitive keeps the structured forms recognizable in
// the casings real logs actually use.
func TestHTTPStatusIsCaseInsensitive(t *testing.T) {
	re := regexp.MustCompile(HTTPStatus("429", "too many requests"))
	for _, line := range []string{"http/1.1 429", `"Status": 429`, "Error 429", "STATUS_CODE=429", "429 TOO MANY REQUESTS"} {
		if !re.MatchString(line) {
			t.Errorf("case variant not detected: %q", line)
		}
	}
	if re.MatchString("6f764f0c7344e437767e61f189f493065dca56cadc7048610948be7a5d42963f") {
		t.Error("case-insensitivity must not make a hash match")
	}
}
