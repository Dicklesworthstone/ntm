package tools

// Tests for the `caam limits --rank` integration behind GitHub issue #319.
//
// The reporter's host has three interchangeable Codex Pro seats. The quota law
// is: among included allowances with headroom, spend the one that refreshes
// SOONEST and preserve the later-resetting reserve. ntm had no spawn-time
// limits call at all, so a bare `ntm add --cod=1` fell through to whatever the
// host command template pinned statically — on that host, the reserve seat.
//
// caam#105 added the rank mode that answers this. These pin ntm's side of the
// contract: the right rank mode, the right provider vocabulary, a real answer
// when nothing is selectable, and no silent success on a broken pool.

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

// writeScriptedCAAM installs a fake `caam` that records its argv to argvPath
// and replies to `limits` with the given stdout/stderr and exit code.
func writeScriptedCAAM(t *testing.T, stdout, stderr string, exitCode int) (argvPath string) {
	t.Helper()
	dir := t.TempDir()
	argvPath = filepath.Join(dir, "argv.txt")

	stdoutFile := filepath.Join(dir, "stdout.json")
	if err := os.WriteFile(stdoutFile, []byte(stdout), 0o644); err != nil {
		t.Fatalf("write fake stdout: %v", err)
	}
	stderrFile := filepath.Join(dir, "stderr.txt")
	if err := os.WriteFile(stderrFile, []byte(stderr), 0o644); err != nil {
		t.Fatalf("write fake stderr: %v", err)
	}

	script := "#!/bin/sh\n" +
		"echo \"$@\" >> " + argvPath + "\n" +
		"if [ \"$1\" = \"limits\" ]; then\n" +
		"  cat " + stdoutFile + "\n" +
		"  cat " + stderrFile + " 1>&2\n" +
		"  exit " + strconv.Itoa(exitCode) + "\n" +
		"fi\n" +
		"echo 'unexpected subcommand' 1>&2\n" +
		"exit 1\n"

	fake := filepath.Join(dir, "caam")
	if err := os.WriteFile(fake, []byte(script), 0o755); err != nil {
		t.Fatalf("write fake caam: %v", err)
	}
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
	InvalidateCAAMLimitsCache()
	t.Cleanup(InvalidateCAAMLimitsCache)
	return argvPath
}

func readArgv(t *testing.T, path string) string {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read recorded argv: %v", err)
	}
	return string(data)
}

// threeSeatPool is the reporter's shape: a 34%-used seat whose weekly window
// refreshes sooner (the one to spend) and a 0%-used seat that refreshes later
// (the reserve to preserve). --best would pick the 0% reserve; the rank mode
// must pick the 34% seat.
const threeSeatPool = `{
  "rank": "earliest-reset-headroom",
  "provider": "codex",
  "headroom_ceiling_percent": 95,
  "require_model_window": false,
  "generated_at": "2026-09-10T12:00:00Z",
  "selected": {
    "provider": "codex",
    "profile": "spend-me",
    "rank": 1,
    "eligible": true,
    "tier": "included_headroom",
    "reason": "included allowance refreshes soonest",
    "used_percent": 34,
    "binding_window": "secondary",
    "headroom_percent": 66,
    "governing_window": "weekly",
    "resets_at": "2026-09-12T00:00:00Z",
    "resets_in_seconds": 129600,
    "availability_score": 66,
    "has_credits": false,
    "plan_type": "pro"
  },
  "profiles": [
    {
      "provider": "codex",
      "profile": "spend-me",
      "rank": 1,
      "eligible": true,
      "tier": "included_headroom",
      "reason": "included allowance refreshes soonest",
      "used_percent": 34,
      "headroom_percent": 66,
      "governing_window": "weekly",
      "resets_at": "2026-09-12T00:00:00Z",
      "resets_in_seconds": 129600,
      "availability_score": 66,
      "plan_type": "pro"
    },
    {
      "provider": "codex",
      "profile": "reserve",
      "rank": 2,
      "eligible": true,
      "tier": "included_headroom",
      "reason": "included allowance refreshes later; preserved",
      "used_percent": 0,
      "headroom_percent": 100,
      "governing_window": "weekly",
      "resets_at": "2026-09-16T00:00:00Z",
      "resets_in_seconds": 475200,
      "availability_score": 100,
      "plan_type": "pro"
    },
    {
      "provider": "codex",
      "profile": "burned",
      "rank": 0,
      "eligible": false,
      "tier": "exhausted",
      "reason": "included allowance at cap",
      "used_percent": 100,
      "headroom_percent": 0,
      "availability_score": 0,
      "plan_type": "pro"
    }
  ]
}`

// TestRankedSeatPrefersEarliestResetWithHeadroom is the core #319 property on
// ntm's side: the seat handed to new work is the one whose quota is about to
// refresh, not the idlest one.
func TestRankedSeatPrefersEarliestResetWithHeadroom(t *testing.T) {
	writeScriptedCAAM(t, threeSeatPool, "", 0)

	seat, err := NewCAAMAdapter().RankedSeat(context.Background(), "codex", "")
	if err != nil {
		t.Fatalf("RankedSeat() error = %v, want nil", err)
	}
	if seat.Profile != "spend-me" {
		t.Errorf("selected profile = %q, want spend-me — the 0%% reserve seat that resets later must be preserved", seat.Profile)
	}
	if seat.Persona() != "codex-spend-me" {
		t.Errorf("persona = %q, want codex-spend-me", seat.Persona())
	}
	if seat.UsedPercent != 34 || seat.GoverningWindow != "weekly" {
		t.Errorf("seat = %+v, want the 34%%-used weekly-governed row", seat)
	}
}

// TestRankedSeatUsesTheRankModeNeverBest guards the exact mistake caam#105
// warns about: --best ranks lowest utilization and would pick the reserve.
func TestRankedSeatUsesTheRankModeNeverBest(t *testing.T) {
	argvPath := writeScriptedCAAM(t, threeSeatPool, "", 0)

	if _, err := NewCAAMAdapter().RankedSeat(context.Background(), "codex", ""); err != nil {
		t.Fatalf("RankedSeat() error = %v", err)
	}

	argv := readArgv(t, argvPath)
	if !strings.Contains(argv, "--rank earliest-reset-headroom") {
		t.Errorf("argv = %q, want --rank earliest-reset-headroom", argv)
	}
	if strings.Contains(argv, "--best") {
		t.Errorf("argv = %q, must never pass --best: it ranks lowest utilization and picks the reserve seat", argv)
	}
}

// TestLimitsTranslatesProviderVocabulary is the ntm#319 item-4 trap: ntm calls
// Codex "openai" internally, caam's limits command accepts only "codex".
func TestLimitsTranslatesProviderVocabulary(t *testing.T) {
	for _, ntmProvider := range []string{"openai", "codex", "cod", "chatgpt"} {
		t.Run(ntmProvider, func(t *testing.T) {
			argvPath := writeScriptedCAAM(t, threeSeatPool, "", 0)
			if _, err := NewCAAMAdapter().RankedSeat(context.Background(), ntmProvider, ""); err != nil {
				t.Fatalf("RankedSeat(%q) error = %v", ntmProvider, err)
			}
			argv := readArgv(t, argvPath)
			if !strings.Contains(argv, "limits codex ") {
				t.Errorf("argv = %q, want the caam-side provider token %q", argv, "codex")
			}
			if strings.Contains(argv, "limits openai") {
				t.Errorf("argv = %q: caam limits rejects %q outright", argv, "openai")
			}
		})
	}
}

func TestLimitsRejectsUnsupportedProvider(t *testing.T) {
	writeScriptedCAAM(t, threeSeatPool, "", 0)

	_, err := NewCAAMAdapter().RankedSeat(context.Background(), "gemini", "")
	if err == nil {
		t.Fatal("RankedSeat(gemini) must fail: caam limits supports only claude and codex")
	}
	if !strings.Contains(err.Error(), "gemini") {
		t.Errorf("error = %v, want it to name the unsupported provider", err)
	}
}

// TestRankedSeatFailsVisiblyWhenNothingSelectable is the "fail visibly rather
// than fall through to a static pin" requirement. caam exits non-zero with a
// payload naming the reason; that is an ANSWER and must reach the caller,
// never be flattened into "caam unavailable".
func TestRankedSeatFailsVisiblyWhenNothingSelectable(t *testing.T) {
	const exhausted = `{
  "rank": "earliest-reset-headroom",
  "provider": "codex",
  "headroom_ceiling_percent": 95,
  "require_model_window": false,
  "generated_at": "2026-09-10T12:00:00Z",
  "selected": null,
  "profiles": [
    {"provider":"codex","profile":"burned","rank":0,"eligible":false,"tier":"exhausted","reason":"included allowance at cap","used_percent":100,"headroom_percent":0,"availability_score":0}
  ],
  "error": "no seat has included headroom below the 95% ceiling"
}`
	writeScriptedCAAM(t, exhausted, "no seat has included headroom below the 95% ceiling\n", 1)

	seat, err := NewCAAMAdapter().RankedSeat(context.Background(), "codex", "")
	if err == nil {
		t.Fatal("an exhausted pool must fail visibly, never silently fall through to a static pin")
	}
	if seat != nil {
		t.Errorf("seat = %+v, want nil when nothing is selectable", seat)
	}
	if !errors.Is(err, ErrCAAMNoSeatSelectable) {
		t.Errorf("error = %v, want ErrCAAMNoSeatSelectable so callers can tell a ranked refusal from a transport failure", err)
	}
	if !strings.Contains(err.Error(), "95% ceiling") {
		t.Errorf("error = %v, want caam's own reason carried through", err)
	}
}

// TestLimitsKeepsThePayloadOnANonZeroExit: caam keeps stdout pure JSON and
// sends the human message to stderr, so the per-profile reasons survive a
// refusal and can be shown to an operator.
func TestLimitsKeepsThePayloadOnANonZeroExit(t *testing.T) {
	const exhausted = `{
  "rank": "earliest-reset-headroom",
  "provider": "codex",
  "selected": null,
  "profiles": [
    {"provider":"codex","profile":"burned","rank":0,"eligible":false,"tier":"exhausted","reason":"included allowance at cap","used_percent":100}
  ],
  "error": "nothing selectable"
}`
	writeScriptedCAAM(t, exhausted, "nothing selectable\n", 1)

	result, err := NewCAAMAdapter().Limits(context.Background(), CAAMLimitsOptions{
		Provider: "codex",
		Rank:     CAAMRankEarliestResetHeadroom,
	})
	if err == nil {
		t.Fatal("Limits() must report the refusal")
	}
	if result == nil {
		t.Fatal("Limits() must still return the payload so per-profile reasons can be shown")
	}
	if len(result.Profiles) != 1 || result.Profiles[0].Reason != "included allowance at cap" {
		t.Errorf("profiles = %+v, want the per-seat reason preserved", result.Profiles)
	}
}

// TestLimitsMemoizesWithinAnAdd: one `ntm add --cod=4` must not shell out to
// caam (and across the network) once per pane.
func TestLimitsMemoizesWithinAnAdd(t *testing.T) {
	argvPath := writeScriptedCAAM(t, threeSeatPool, "", 0)

	adapter := NewCAAMAdapter()
	for i := 0; i < 4; i++ {
		if _, err := adapter.RankedSeat(context.Background(), "codex", ""); err != nil {
			t.Fatalf("RankedSeat() call %d error = %v", i+1, err)
		}
	}

	invocations := strings.Count(readArgv(t, argvPath), "limits")
	if invocations != 1 {
		t.Errorf("caam limits invoked %d times for 4 panes, want 1 (memoized)", invocations)
	}
}

func TestLimitsReportsMissingBinary(t *testing.T) {
	t.Setenv("PATH", t.TempDir())
	InvalidateCAAMLimitsCache()
	t.Cleanup(InvalidateCAAMLimitsCache)

	_, err := NewCAAMAdapter().RankedSeat(context.Background(), "codex", "")
	if !errors.Is(err, ErrToolNotInstalled) {
		t.Errorf("error = %v, want ErrToolNotInstalled when caam is absent", err)
	}
}

func TestCAAMSeatPersonaComposition(t *testing.T) {
	cases := []struct {
		provider string
		profile  string
		want     string
	}{
		{"codex", "acme-net", "codex-acme-net"},
		{"openai", "acme-net", "codex-acme-net"},
		{"claude", "acme-net", "claude-acme-net"},
		{"anthropic", "acme-net", "claude-acme-net"},
		{"codex", "", ""},
		{"", "acme-net", ""},
		{"gemini", "acme-net", ""},
	}
	for _, tc := range cases {
		if got := CAAMSeatPersona(tc.provider, tc.profile); got != tc.want {
			t.Errorf("CAAMSeatPersona(%q, %q) = %q, want %q", tc.provider, tc.profile, got, tc.want)
		}
	}
}
