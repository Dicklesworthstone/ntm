package agentsession

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"
)

func nativeDiscoverer(home string) *Discoverer {
	discoverer := NewDiscoverer()
	discoverer.homeDir = func() (string, error) { return home, nil }
	discoverer.lookPath = func(string) (string, error) { return "", os.ErrNotExist }
	discoverer.findProcessSession = func(string, string, string, int) *Info { return nil }
	discoverer.findProcessStart = func(int, string) int64 { return 0 }
	return discoverer
}

type manualDeadlineContext struct {
	done chan struct{}
	once sync.Once
}

func newManualDeadlineContext() *manualDeadlineContext {
	return &manualDeadlineContext{done: make(chan struct{})}
}

func (c *manualDeadlineContext) Deadline() (time.Time, bool) { return time.Time{}, false }
func (c *manualDeadlineContext) Done() <-chan struct{}       { return c.done }
func (c *manualDeadlineContext) Err() error {
	select {
	case <-c.done:
		return context.DeadlineExceeded
	default:
		return nil
	}
}
func (c *manualDeadlineContext) Value(any) any { return nil }
func (c *manualDeadlineContext) expire()       { c.once.Do(func() { close(c.done) }) }

func TestResumeProvider(t *testing.T) {
	cases := map[string]string{
		"cc":              "claude",
		"claude":          "claude",
		"claude-code":     "claude",
		"CC":              "claude",
		"cod":             "codex",
		"codex":           "codex",
		"gmi":             "gemini",
		"gemini":          "gemini",
		"agy":             "antigravity",
		"antigravity":     "antigravity",
		"antigravity-cli": "antigravity",
		"AGY":             "antigravity",
		"user":            "",
		"cursor":          "",
		"":                "",
		"  cc  ":          "claude",
	}
	for in, want := range cases {
		if got := ResumeProvider(in); got != want {
			t.Errorf("ResumeProvider(%q) = %q, want %q", in, got, want)
		}
	}

	// gmi and agy are distinct providers (shared ~/.gemini parent, different
	// stores) and must never collapse into the same provider name.
	if ResumeProvider("gmi") == ResumeProvider("agy") {
		t.Errorf("gmi and agy must map to distinct providers, both = %q", ResumeProvider("gmi"))
	}
}

func TestEncodeClaudeProjectDir(t *testing.T) {
	// Claude Code encodes a cwd as `cwd.replace(/[^a-zA-Z0-9]/g, "-")` — ALL
	// non-alphanumerics (including '_' and '.') collapse to '-'. Expectations are
	// verified against the real ~/.claude/projects session dirs (e.g.
	// /data/projects/beads_rust -> -data-projects-beads-rust, which holds the
	// actual .jsonl sessions; the underscore variant is an empty stub).
	cases := map[string]string{
		"/data/projects/ntm":            "-data-projects-ntm",
		"/home/u/my.app":                "-home-u-my-app",
		"/a/b_c":                        "-a-b-c",
		"/data/projects/mcp_agent_mail": "-data-projects-mcp-agent-mail",
		"/data/projects/coding_agent_session_search": "-data-projects-coding-agent-session-search",
		"/data/projects/jeffreys-skills.md":          "-data-projects-jeffreys-skills-md",
		"/data/projects/ntm/":                        "-data-projects-ntm", // trailing slash cleaned
	}
	for in, want := range cases {
		if got := encodeClaudeProjectDir(in); got != want {
			t.Errorf("encodeClaudeProjectDir(%q) = %q, want %q", in, got, want)
		}
	}

	// Explicit regression guard for the underscore bug (#175): an interim fix
	// wrongly PRESERVED '_', which resolved to a non-existent (or a different
	// project's) directory and resumed the wrong Claude session. Underscores must
	// collapse to '-' to match Claude Code.
	if got := encodeClaudeProjectDir("/data/projects/coding_agent_session_search"); strings.Contains(got, "_") {
		t.Errorf("encodeClaudeProjectDir must collapse underscores to '-': got %q", got)
	}
}

func TestResumeCommandNative(t *testing.T) {
	// Force the native path by making casr unavailable.
	orig := lookPath
	lookPath = func(string) (string, error) { return "", os.ErrNotExist }
	defer func() { lookPath = orig }()

	cases := []struct {
		provider string
		id       string
		prefer   bool
		want     string
	}{
		{"claude", "abc-123", true, "claude --resume 'abc-123'"},
		{"codex", "r1", false, "codex resume 'r1'"},
		{"gemini", "g9", true, "gemini --resume 'g9'"},
		{"antigravity", "uuid-9", false, "agy --conversation 'uuid-9' --model 'Gemini 3.8 Flash (High)'"},
		{"antigravity", "uuid-9", true, "agy --conversation 'uuid-9' --model 'Gemini 3.8 Flash (High)'"},
		{"claude", "", true, ""},
		{"unknown", "x", true, ""},
	}
	for _, c := range cases {
		if got := ResumeCommand(c.provider, c.id, c.prefer); got != c.want {
			t.Errorf("ResumeCommand(%q,%q,%v) = %q, want %q", c.provider, c.id, c.prefer, got, c.want)
		}
	}
}

func TestResumeCommandCASR(t *testing.T) {
	// Make casr "available" so the casr path is taken.
	orig := lookPath
	lookPath = func(name string) (string, error) {
		if name == "casr" {
			return "/usr/bin/casr", nil
		}
		return "", os.ErrNotExist
	}
	defer func() { lookPath = orig }()

	cases := []struct {
		provider string
		id       string
		want     string
	}{
		{"claude", "abc-123", "casr -cc 'abc-123'"},
		{"codex", "r1", "casr -cod 'r1'"},
		{"gemini", "g9", "casr -gmi 'g9'"},
		// Antigravity has no casr short-flag; even with preferCASR=true and
		// casr available it must fall through to its native agy command.
		{"antigravity", "uuid-9", "agy --conversation 'uuid-9' --model 'Gemini 3.8 Flash (High)'"},
	}
	for _, c := range cases {
		if got := ResumeCommand(c.provider, c.id, true); got != c.want {
			t.Errorf("ResumeCommand(%q,%q,casr) = %q, want %q", c.provider, c.id, got, c.want)
		}
	}

	// preferCASR=false must still use native even when casr is available.
	if got := ResumeCommand("claude", "x", false); got != "claude --resume 'x'" {
		t.Errorf("native override failed: got %q", got)
	}
}

func TestObserveBindingRecordsProvenanceFreshnessAndKeepsSourcePathPrivate(t *testing.T) {
	t.Parallel()

	now := time.Date(2026, 7, 11, 12, 0, 0, 0, time.UTC)
	tests := []struct {
		name           string
		info           *Info
		wantSource     DiscoverySource
		wantFreshness  BindingFreshness
		wantConfidence float64
	}{
		{
			name: "current process tree binding",
			info: &Info{
				AgentType:  "cod",
				SessionID:  "session-current",
				Provider:   "codex",
				SourcePath: "/private/transcript/location",
			},
			wantSource:     DiscoverySourceProcessTree,
			wantFreshness:  BindingFresh,
			wantConfidence: 0.99,
		},
		{
			name: "native fallback just inside bounded horizon is fresh",
			info: &Info{
				AgentType:  "cod",
				SessionID:  "session-recent",
				Provider:   "codex",
				Source:     DiscoverySourceNativeStore,
				SourcePath: "/private/transcript/recent",
				UpdatedAt:  now.Add(-bindingFreshnessHorizon + time.Nanosecond),
			},
			wantSource:     DiscoverySourceNativeStore,
			wantFreshness:  BindingFresh,
			wantConfidence: 0.75,
		},
		{
			name: "native fallback at horizon is stale",
			info: &Info{
				AgentType:  "cod",
				SessionID:  "session-boundary",
				Provider:   "codex",
				Source:     DiscoverySourceNativeStore,
				SourcePath: "/private/transcript/boundary",
				UpdatedAt:  now.Add(-bindingFreshnessHorizon),
			},
			wantSource:     DiscoverySourceNativeStore,
			wantFreshness:  BindingStale,
			wantConfidence: 0.375,
		},
		{
			name: "future native fallback is stale",
			info: &Info{
				AgentType:  "cod",
				SessionID:  "session-future",
				Provider:   "codex",
				Source:     DiscoverySourceNativeStore,
				SourcePath: "/private/transcript/future",
				UpdatedAt:  now.Add(time.Second),
			},
			wantSource:     DiscoverySourceNativeStore,
			wantFreshness:  BindingStale,
			wantConfidence: 0.375,
		},
		{
			name: "zero-time native fallback is stale",
			info: &Info{
				AgentType:  "cod",
				SessionID:  "session-zero",
				Provider:   "codex",
				Source:     DiscoverySourceNativeStore,
				SourcePath: "/private/transcript/zero",
			},
			wantSource:     DiscoverySourceNativeStore,
			wantFreshness:  BindingStale,
			wantConfidence: 0.375,
		},
		{
			name: "old native fallback is explicitly stale",
			info: &Info{
				AgentType:  "cod",
				SessionID:  "session-old",
				Provider:   "codex",
				Source:     DiscoverySourceNativeStore,
				SourcePath: "/private/transcript/location",
				UpdatedAt:  now.Add(-bindingFreshnessHorizon - time.Minute),
			},
			wantSource:     DiscoverySourceNativeStore,
			wantFreshness:  BindingStale,
			wantConfidence: 0.375,
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			discoverer := NewDiscoverer()
			discoverer.findProcessSession = func(string, string, string, int) *Info {
				copy := *test.info
				return &copy
			}
			observation := discoverer.ObserveBinding(context.Background(), "cod", "/repo", 42, now)
			if observation.Source != test.wantSource || observation.Freshness != test.wantFreshness || observation.Confidence != test.wantConfidence {
				t.Fatalf("binding quality = source %q freshness %q confidence %v, want %q/%q/%v", observation.Source, observation.Freshness, observation.Confidence, test.wantSource, test.wantFreshness, test.wantConfidence)
			}
			if observation.SessionID != test.info.SessionID || observation.SourcePath != test.info.SourcePath {
				t.Fatalf("binding identity = %+v", observation)
			}
			encoded, err := json.Marshal(observation)
			if err != nil {
				t.Fatalf("Marshal: %v", err)
			}
			if strings.Contains(string(encoded), test.info.SourcePath) || strings.Contains(string(encoded), "source_path") {
				t.Fatalf("serialized binding leaked private source path: %s", encoded)
			}
		})
	}
}

func TestObserveBindingDeadlineIsUnavailableAndRetryable(t *testing.T) {
	t.Parallel()

	discoverer := NewDiscoverer()
	discoverer.homeDir = func() (string, error) { return t.TempDir(), nil }
	discoverer.lookPath = func(string) (string, error) { return "/fake/casr", nil }
	discoverer.findProcessSession = func(string, string, string, int) *Info { return nil }
	discoverer.findProcessStart = func(int, string) int64 { return 0 }
	runs := 0
	deadlineCtx := newManualDeadlineContext()
	discoverer.runCommand = func(ctx context.Context, _ string, _ ...string) ([]byte, error) {
		runs++
		if runs == 1 {
			deadlineCtx.expire()
			<-ctx.Done()
			return nil, context.DeadlineExceeded
		}
		return []byte(`{"items":[{"session_id":"retry-session","provider":"codex","workspace":"/repo","path":"/private/retry.jsonl","last_active_at":1783771200000}]}`), nil
	}

	first := discoverer.ObserveBinding(deadlineCtx, "cod", "/repo", 0, time.Now())
	if first.Freshness != BindingUnavailable || first.Confidence != 0 || first.FailureCode != "deadline_exceeded" {
		t.Fatalf("deadline observation = %+v", first)
	}
	second := discoverer.ObserveBinding(context.Background(), "cod", "/repo", 0, time.UnixMilli(1783771200000))
	if second.SessionID != "retry-session" || second.Source != DiscoverySourceCASR || second.Freshness != BindingFresh {
		t.Fatalf("retry observation = %+v", second)
	}
	if runs != 2 {
		t.Fatalf("CASR runs = %d, want 2 after cancelled lookup", runs)
	}
}

func TestDiscoverContextCancelledBeforeNativeScanIsRetryable(t *testing.T) {
	t.Parallel()

	home := t.TempDir()
	workDir := "/data/projects/native-cancel"
	projectDir := filepath.Join(home, ".claude", "projects", encodeClaudeProjectDir(workDir))
	if err := os.MkdirAll(projectDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(projectDir, "retry-session.jsonl"), []byte("{}\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	discoverer := NewDiscoverer()
	discoverer.findProcessSession = func(string, string, string, int) *Info { return nil }
	ctx, cancel := context.WithCancel(context.Background())
	homeCalls := 0
	discoverer.homeDir = func() (string, error) {
		homeCalls++
		if homeCalls == 1 {
			cancel()
		}
		return home, nil
	}
	if info := discoverer.DiscoverContext(ctx, "cc", workDir, 0); info != nil {
		t.Fatalf("cancelled native discovery = %+v, want nil", info)
	}
	info := discoverer.DiscoverContext(context.Background(), "cc", workDir, 0)
	if info == nil || info.SessionID != "retry-session" {
		t.Fatalf("native retry = %+v, want retry-session", info)
	}
}

func TestNativeDiscoveryScansHonorCancelledContext(t *testing.T) {
	t.Parallel()

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	home := t.TempDir()
	if info := discoverClaudeContext(ctx, home, "/repo"); info != nil {
		t.Fatalf("cancelled Claude scan = %+v", info)
	}
	if info := discoverCodexContext(ctx, home, "/repo", nil); info != nil {
		t.Fatalf("cancelled Codex scan = %+v", info)
	}
	if info := discoverGeminiContext(ctx, home, "/repo", 0, nil); info != nil {
		t.Fatalf("cancelled Gemini scan = %+v", info)
	}
	if info := discoverAntigravityContext(ctx, home, "/repo"); info != nil {
		t.Fatalf("cancelled Antigravity scan = %+v", info)
	}
}

func TestFileContainsCwdContextMatchesAcrossReadBoundary(t *testing.T) {
	t.Parallel()

	path := filepath.Join(t.TempDir(), "conversation.db")
	workDir := "/data/projects/boundary"
	payload := append(make([]byte, 64*1024-3), []byte(workDir)...)
	if err := os.WriteFile(path, payload, 0o644); err != nil {
		t.Fatal(err)
	}
	if !fileContainsCwdContext(context.Background(), path, workDir) {
		t.Fatal("cwd spanning read chunks was not detected")
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if fileContainsCwdContext(ctx, path, workDir) {
		t.Fatal("cancelled scan reported a match")
	}
}

func TestShellQuote(t *testing.T) {
	cases := map[string]string{
		"abc":      "'abc'",
		"":         "''",
		"a'b":      `'a'\''b'`,
		"uuid-123": "'uuid-123'",
	}
	for in, want := range cases {
		if got := shellQuote(in); got != want {
			t.Errorf("shellQuote(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestDiscoverClaude(t *testing.T) {
	home := t.TempDir()
	workDir := "/data/projects/demo"
	projDir := filepath.Join(home, ".claude", "projects", encodeClaudeProjectDir(workDir))
	if err := os.MkdirAll(projDir, 0o755); err != nil {
		t.Fatal(err)
	}

	// Write two session files; the newer one should win.
	older := filepath.Join(projDir, "old-session.jsonl")
	newer := filepath.Join(projDir, "new-session.jsonl")
	if err := os.WriteFile(older, []byte(`{"type":"user"}`), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(newer, []byte(`{"type":"user"}`), 0o644); err != nil {
		t.Fatal(err)
	}
	// Make newer file genuinely newer.
	old := time.Now().Add(-time.Hour)
	if err := os.Chtimes(older, old, old); err != nil {
		t.Fatal(err)
	}

	info := nativeDiscoverer(home).Discover("cc", workDir, 0)
	if info == nil {
		t.Fatal("expected to discover a claude session, got nil")
	}
	if info.SessionID != "new-session" {
		t.Errorf("SessionID = %q, want new-session", info.SessionID)
	}
	if info.Provider != "claude" {
		t.Errorf("Provider = %q, want claude", info.Provider)
	}
	if info.SourcePath != newer {
		t.Errorf("SourcePath = %q, want %q", info.SourcePath, newer)
	}
}

func TestDiscoverClaudeNoSession(t *testing.T) {
	home := t.TempDir()
	if info := nativeDiscoverer(home).Discover("cc", "/no/such/project", 0); info != nil {
		t.Errorf("expected nil for missing project, got %+v", info)
	}
}

func TestDiscoverNonResumableAgent(t *testing.T) {
	discoverer := NewDiscoverer()
	if info := discoverer.Discover("user", "/data/projects/demo", 0); info != nil {
		t.Errorf("expected nil for user pane, got %+v", info)
	}
	if info := discoverer.Discover("cursor", "/data/projects/demo", 0); info != nil {
		t.Errorf("expected nil for cursor pane, got %+v", info)
	}
}

func TestDiscoverCodex(t *testing.T) {
	home := t.TempDir()
	workDir := "/data/projects/codexdemo"
	dayDir := filepath.Join(home, ".codex", "sessions", "2026", "06", "07")
	if err := os.MkdirAll(dayDir, 0o755); err != nil {
		t.Fatal(err)
	}
	sessionID := "019f4e56-c2ba-7652-91d4-e4173bc0f302"
	roll := filepath.Join(dayDir, "rollout-2026-07-10T19-22-16-"+sessionID+".jsonl")
	content := `{"type":"session_meta","payload":{"id":"` + sessionID + `","cwd":"` + workDir + `"}}` + "\n"
	if err := os.WriteFile(roll, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	// A newer rollout in a sibling prefix must not match this workspace.
	sibling := filepath.Join(dayDir, "rollout-2026-07-10T20-00-00-aaaaaaaa-aaaa-aaaa-aaaa-aaaaaaaaaaaa.jsonl")
	if err := os.WriteFile(sibling, []byte(`{"type":"session_meta","payload":{"id":"aaaaaaaa-aaaa-aaaa-aaaa-aaaaaaaaaaaa","cwd":"`+workDir+`-other"}}`+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	future := time.Now().Add(time.Hour)
	if err := os.Chtimes(sibling, future, future); err != nil {
		t.Fatal(err)
	}
	subagent := filepath.Join(dayDir, "rollout-2026-07-10T21-00-00-bbbbbbbb-bbbb-bbbb-bbbb-bbbbbbbbbbbb.jsonl")
	if err := os.WriteFile(subagent, []byte(`{"type":"session_meta","payload":{"id":"bbbbbbbb-bbbb-bbbb-bbbb-bbbbbbbbbbbb","cwd":"`+workDir+`","thread_source":"subagent"}}`+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	furtherFuture := time.Now().Add(2 * time.Hour)
	if err := os.Chtimes(subagent, furtherFuture, furtherFuture); err != nil {
		t.Fatal(err)
	}

	discoverer := nativeDiscoverer(home)
	info := discoverer.Discover("cod", workDir, 0)
	if info == nil {
		t.Fatal("expected to discover a codex session, got nil")
	}
	if info.SessionID != sessionID {
		t.Errorf("SessionID = %q, want %q", info.SessionID, sessionID)
	}
	if info.Provider != "codex" {
		t.Errorf("Provider = %q, want codex", info.Provider)
	}

	// A rollout for a different cwd must not match.
	if info := discoverer.Discover("cod", "/some/other/dir", 0); info != nil {
		t.Errorf("expected nil for non-matching cwd, got %+v", info)
	}
}

func TestDiscoverGemini(t *testing.T) {
	home := t.TempDir()
	workDir := "/data/projects/gemdemo"
	geminiHome := filepath.Join(home, ".gemini")
	chatsDir := filepath.Join(geminiHome, "tmp", "gemdemo", "chats")
	if err := os.MkdirAll(chatsDir, 0o755); err != nil {
		t.Fatal(err)
	}
	registry := `{"projects":{"` + workDir + `":"gemdemo"}}`
	if err := os.WriteFile(filepath.Join(geminiHome, "projects.json"), []byte(registry), 0o644); err != nil {
		t.Fatal(err)
	}
	sess := filepath.Join(chatsDir, "session-2026-07-10T12-00-gemini42.jsonl")
	content := `{"sessionId":"gemini-native-42","workspace":"` + workDir + `"}`
	if err := os.WriteFile(sess, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}

	info := nativeDiscoverer(home).Discover("gmi", workDir, 0)
	if info == nil {
		t.Fatal("expected to discover a gemini session, got nil")
	}
	if info.SessionID != "gemini-native-42" {
		t.Errorf("SessionID = %q, want gemini-native-42", info.SessionID)
	}
}

func TestDiscoverPrefersPaneProcessSession(t *testing.T) {
	discoverer := NewDiscoverer()
	discoverer.homeDir = func() (string, error) { return t.TempDir(), nil }
	discoverer.findProcessSession = func(agentType, provider, _ string, panePID int) *Info {
		if agentType != "cod" || provider != "codex" || (panePID != 4242 && panePID != 5252) {
			t.Fatalf("unexpected process lookup: agent=%q provider=%q pid=%d", agentType, provider, panePID)
		}
		return &Info{AgentType: "cod", Provider: "codex", SessionID: "pane-owned"}
	}
	discoverer.lookPath = func(string) (string, error) {
		t.Fatal("CASR must not run when pane process discovery succeeds")
		return "", os.ErrNotExist
	}

	info := discoverer.Discover("cod", "/data/projects/demo", 4242)
	if info == nil || info.SessionID != "pane-owned" {
		t.Fatalf("process-owned discovery = %+v, want pane-owned session", info)
	}
	repeated := discoverer.Discover("cod", "/data/projects/demo", 5252)
	if repeated == nil || repeated.SessionID != "pane-owned" {
		t.Fatalf("second pane process discovery = %+v, want shared pane-owned session", repeated)
	}
}

func TestDiscoverProcessSessionDoesNotRequireHomeOrWorkDir(t *testing.T) {
	discoverer := NewDiscoverer()
	discoverer.homeDir = func() (string, error) { return "", os.ErrNotExist }
	discoverer.findProcessSession = func(agentType, provider, home string, panePID int) *Info {
		if agentType != "cod" || provider != "codex" || home != "" || panePID != 4242 {
			t.Fatalf("unexpected process lookup: agent=%q provider=%q home=%q pid=%d", agentType, provider, home, panePID)
		}
		return &Info{AgentType: "cod", Provider: "codex", SessionID: "pane-owned"}
	}
	discoverer.lookPath = func(string) (string, error) {
		t.Fatal("fallback discovery must not run after exact process ownership")
		return "", os.ErrNotExist
	}

	info := discoverer.Discover("cod", "", 4242)
	if info == nil || info.SessionID != "pane-owned" {
		t.Fatalf("process-owned discovery = %+v, want pane-owned session", info)
	}
}

func TestProcessNodeSessionPrefersExplicitResumeOverOpenFile(t *testing.T) {
	const resumedID = "019f4e56-c2ba-7652-91d4-e4173bc0f302"
	path := filepath.Join(t.TempDir(), "rollout-root.jsonl")
	if err := os.WriteFile(path, []byte(`{"type":"session_meta","payload":{"id":"open-file-id","cwd":"/data/projects/demo"}}`+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	info := processNodeSession("cod", "codex", []string{"codex", "resume", resumedID}, func() []string {
		t.Fatal("open files must not be inspected after a validated resume selector")
		return []string{path}
	})
	if info == nil || info.SessionID != resumedID || info.SourcePath != "" {
		t.Fatalf("process node session = %+v, want explicit resume %q", info, resumedID)
	}
}

func TestProcessNodeSessionIgnoresFilesForUnrelatedProcess(t *testing.T) {
	info := processNodeSession("cod", "codex", []string{"zsh", "-l"}, func() []string {
		t.Fatal("open files must not be inspected for an unrelated process")
		return nil
	})
	if info != nil {
		t.Fatalf("unrelated process session = %+v, want nil", info)
	}
}

func TestProcessNodeSessionSkipsEarlierSubagentFile(t *testing.T) {
	dayDir := filepath.Join(t.TempDir(), "sessions", "2026", "07", "10")
	if err := os.MkdirAll(dayDir, 0o755); err != nil {
		t.Fatal(err)
	}
	subagentPath := filepath.Join(dayDir, "rollout-subagent.jsonl")
	if err := os.WriteFile(subagentPath, []byte(`{"type":"session_meta","payload":{"id":"subagent","cwd":"/data/projects/demo","thread_source":"subagent"}}`+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	rootPath := filepath.Join(dayDir, "rollout-root.jsonl")
	if err := os.WriteFile(rootPath, []byte(`{"type":"session_meta","payload":{"id":"root-session","cwd":"/data/projects/demo","thread_source":"user"}}`+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	info := processNodeSession("cod", "codex", []string{"codex"}, func() []string {
		return []string{subagentPath, rootPath}
	})
	if info == nil || info.SessionID != "root-session" || info.SourcePath != rootPath {
		t.Fatalf("process discovery = %+v, want root-session from %s", info, rootPath)
	}
}

func TestDiscoverCASRStructuredResultAndCache(t *testing.T) {
	const activity = int64(1_783_726_430_137)
	casrDir := t.TempDir()
	subagentPath := filepath.Join(casrDir, "rollout-subagent.jsonl")
	if err := os.WriteFile(subagentPath, []byte(`{"type":"session_meta","payload":{"id":"subagent","cwd":"/data/projects/demo","thread_source":"subagent"}}`+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	rootPath := filepath.Join(casrDir, "rollout-root.jsonl")
	if err := os.WriteFile(rootPath, []byte(`{"type":"session_meta","payload":{"id":"casr-session","cwd":"/data/projects/demo","thread_source":"user"}}`+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	payload, err := json.Marshal(casrListEnvelope{Items: []casrListItem{
		{
			SessionID:    "subagent",
			Provider:     "codex",
			Workspace:    "/data/projects/demo/subdir",
			Path:         subagentPath,
			LastActiveAt: activity + 1,
		},
		{
			SessionID:    "casr-session",
			Provider:     "codex",
			Workspace:    "/data/projects/demo/subdir",
			Path:         rootPath,
			LastActiveAt: activity,
		},
	}})
	if err != nil {
		t.Fatal(err)
	}

	discoverer := NewDiscoverer()
	discoverer.homeDir = func() (string, error) { return t.TempDir(), nil }
	discoverer.findProcessSession = func(string, string, string, int) *Info { return nil }
	discoverer.lookPath = func(name string) (string, error) {
		if name != "casr" {
			t.Fatalf("lookPath(%q), want casr", name)
		}
		return "/usr/bin/casr", nil
	}
	calls := 0
	discoverer.runCommand = func(_ context.Context, name string, args ...string) ([]byte, error) {
		calls++
		if name != "/usr/bin/casr" {
			t.Fatalf("command = %q, want /usr/bin/casr", name)
		}
		wantArgs := []string{
			"list", "--workspace", "/data/projects/demo",
			"--provider", "codex", "--sort", "date",
			"--limit", "20", "--json",
		}
		if !reflect.DeepEqual(args, wantArgs) {
			t.Fatalf("args = %#v, want %#v", args, wantArgs)
		}
		return payload, nil
	}

	first := discoverer.Discover("cod", "/data/projects/demo", 100)
	second := discoverer.Discover("cod", "/data/projects/demo", 100)
	if calls != 1 {
		t.Fatalf("CASR calls = %d, want 1 cached workspace query", calls)
	}
	for _, info := range []*Info{first, second} {
		if info == nil || info.SessionID != "casr-session" || info.Provider != "codex" {
			t.Fatalf("CASR discovery = %+v", info)
		}
		if info.SourcePath != rootPath {
			t.Errorf("SourcePath = %q", info.SourcePath)
		}
		if got := info.UpdatedAt.UnixMilli(); got != activity {
			t.Errorf("UpdatedAt = %d, want %d", got, activity)
		}
	}
	if first == second {
		t.Fatal("cached discovery must return copies, not a shared mutable pointer")
	}
}

func TestDiscoverCASRRejectsSiblingPrefixAndFallsBack(t *testing.T) {
	home := t.TempDir()
	workDir := "/data/projects/demo"
	dayDir := filepath.Join(home, ".codex", "sessions", "2026", "07", "10")
	if err := os.MkdirAll(dayDir, 0o755); err != nil {
		t.Fatal(err)
	}
	nativeID := "11111111-1111-1111-1111-111111111111"
	nativePath := filepath.Join(dayDir, "rollout-2026-07-10T20-00-00-"+nativeID+".jsonl")
	if err := os.WriteFile(nativePath, []byte(`{"type":"session_meta","payload":{"id":"`+nativeID+`","cwd":"`+workDir+`"}}`+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	payload, err := json.Marshal(casrListEnvelope{Items: []casrListItem{{
		SessionID: "wrong-prefix-session",
		Provider:  "codex",
		Workspace: workDir + "-other",
		Path:      "/tmp/wrong.jsonl",
	}}})
	if err != nil {
		t.Fatal(err)
	}
	discoverer := nativeDiscoverer(home)
	discoverer.lookPath = func(string) (string, error) { return "/usr/bin/casr", nil }
	discoverer.runCommand = func(context.Context, string, ...string) ([]byte, error) {
		return payload, nil
	}

	info := discoverer.Discover("cod", workDir, 0)
	if info == nil || info.SessionID != nativeID || info.SourcePath != nativePath {
		t.Fatalf("fallback discovery = %+v, want native %q", info, nativeID)
	}
}

func TestDiscoverCASRCorrelatesSameWorkspacePanesByProcessStart(t *testing.T) {
	const (
		firstProcess  = int64(1_700_000_000_000)
		secondProcess = int64(1_700_000_100_000)
	)
	payload, err := json.Marshal(casrListEnvelope{Items: []casrListItem{
		{
			SessionID: "older-selector",
			Provider:  "gemini",
			Workspace: "/data/projects/demo",
			Path:      "/tmp/session-older.jsonl",
			StartedAt: firstProcess - 10,
		},
		{
			SessionID: "first-pane",
			Provider:  "gemini",
			Workspace: "/data/projects/demo",
			Path:      "/tmp/session-first.jsonl",
			StartedAt: firstProcess + 500,
		},
		{
			SessionID: "second-pane",
			Provider:  "gemini",
			Workspace: "/data/projects/demo",
			Path:      "/tmp/session-second.jsonl",
			StartedAt: secondProcess + 500,
		},
	}})
	if err != nil {
		t.Fatal(err)
	}

	discoverer := NewDiscoverer()
	discoverer.homeDir = func() (string, error) { return t.TempDir(), nil }
	discoverer.findProcessSession = func(string, string, string, int) *Info { return nil }
	discoverer.findProcessStart = func(pid int, _ string) int64 {
		switch pid {
		case 101:
			return firstProcess
		case 202:
			return secondProcess
		default:
			return 0
		}
	}
	discoverer.lookPath = func(string) (string, error) { return "/usr/bin/casr", nil }
	calls := 0
	discoverer.runCommand = func(context.Context, string, ...string) ([]byte, error) {
		calls++
		return payload, nil
	}

	first := discoverer.Discover("gmi", "/data/projects/demo", 101)
	second := discoverer.Discover("gmi", "/data/projects/demo", 202)
	if first == nil || first.SessionID != "first-pane" {
		t.Fatalf("first pane discovery = %+v, want first-pane", first)
	}
	if second == nil || second.SessionID != "second-pane" {
		t.Fatalf("second pane discovery = %+v, want second-pane", second)
	}
	if calls != 1 {
		t.Fatalf("CASR calls = %d, want one cached workspace enumeration", calls)
	}
}

func TestDiscoverCASRRejectsSessionPredatingProcess(t *testing.T) {
	const processStartedAt = int64(1_700_000_000_000)
	home := t.TempDir()
	workDir := "/data/projects/gemdemo"
	geminiHome := filepath.Join(home, ".gemini")
	chatsDir := filepath.Join(geminiHome, "tmp", "gemdemo", "chats")
	if err := os.MkdirAll(chatsDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(geminiHome, "projects.json"), []byte(`{"projects":{"`+workDir+`":"gemdemo"}}`), 0o644); err != nil {
		t.Fatal(err)
	}
	nativePath := filepath.Join(chatsDir, "session-current.jsonl")
	nativeStart := time.UnixMilli(processStartedAt + 1_000).UTC().Format(time.RFC3339Nano)
	if err := os.WriteFile(nativePath, []byte(`{"sessionId":"native-current","startTime":"`+nativeStart+`"}`+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	payload, err := json.Marshal(casrListEnvelope{Items: []casrListItem{{
		SessionID: "stale-prior-session",
		Provider:  "gemini",
		Workspace: workDir,
		Path:      "/tmp/session-stale.jsonl",
		StartedAt: processStartedAt - 10_000,
	}}})
	if err != nil {
		t.Fatal(err)
	}
	discoverer := nativeDiscoverer(home)
	discoverer.findProcessStart = func(int, string) int64 { return processStartedAt }
	discoverer.lookPath = func(string) (string, error) { return "/usr/bin/casr", nil }
	discoverer.runCommand = func(context.Context, string, ...string) ([]byte, error) {
		return payload, nil
	}

	info := discoverer.Discover("gmi", workDir, 101)
	if info == nil || info.SessionID != "native-current" || info.SourcePath != nativePath {
		t.Fatalf("stale-CASR fallback = %+v, want native-current", info)
	}
}

func TestDiscoverCASRMalformedOutputFallsBack(t *testing.T) {
	home := t.TempDir()
	workDir := "/data/projects/demo"
	dayDir := filepath.Join(home, ".codex", "sessions", "2026", "07", "10")
	if err := os.MkdirAll(dayDir, 0o755); err != nil {
		t.Fatal(err)
	}
	nativeID := "22222222-2222-2222-2222-222222222222"
	path := filepath.Join(dayDir, "rollout-modern-"+nativeID+".jsonl")
	if err := os.WriteFile(path, []byte(`{"type":"session_meta","payload":{"id":"`+nativeID+`","cwd":"`+workDir+`"}}`+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	discoverer := nativeDiscoverer(home)
	discoverer.lookPath = func(string) (string, error) { return "/usr/bin/casr", nil }
	discoverer.runCommand = func(context.Context, string, ...string) ([]byte, error) {
		return []byte("not json"), nil
	}
	info := discoverer.Discover("cod", workDir, 0)
	if info == nil || info.SessionID != nativeID {
		t.Fatalf("malformed-CASR fallback = %+v, want %q", info, nativeID)
	}
}

func TestDiscoverGeminiNativeCorrelatesSameWorkspacePanesByProcessStart(t *testing.T) {
	home := t.TempDir()
	workDir := "/data/projects/gemdemo"
	geminiHome := filepath.Join(home, ".gemini")
	chatsDir := filepath.Join(geminiHome, "tmp", "gemdemo", "chats")
	if err := os.MkdirAll(chatsDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(geminiHome, "projects.json"), []byte(`{"projects":{"`+workDir+`":"gemdemo"}}`), 0o644); err != nil {
		t.Fatal(err)
	}
	firstStart := time.Date(2026, 7, 10, 12, 0, 0, 0, time.UTC)
	secondStart := firstStart.Add(2 * time.Minute)
	firstPath := filepath.Join(chatsDir, "session-first.jsonl")
	if err := os.WriteFile(firstPath, []byte(`{"sessionId":"first-pane","startTime":"`+firstStart.Format(time.RFC3339Nano)+`"}`+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	secondPath := filepath.Join(chatsDir, "session-second.jsonl")
	if err := os.WriteFile(secondPath, []byte(`{"sessionId":"second-pane","startTime":"`+secondStart.Format(time.RFC3339Nano)+`"}`+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	discoverer := nativeDiscoverer(home)
	discoverer.findProcessStart = func(pid int, _ string) int64 {
		if pid == 101 {
			return firstStart.Add(-time.Second).UnixMilli()
		}
		return secondStart.Add(-time.Second).UnixMilli()
	}
	first := discoverer.Discover("gmi", workDir, 101)
	second := discoverer.Discover("gmi", workDir, 202)
	if first == nil || first.SessionID != "first-pane" || first.SourcePath != firstPath {
		t.Fatalf("first native Gemini pane = %+v", first)
	}
	if second == nil || second.SessionID != "second-pane" || second.SourcePath != secondPath {
		t.Fatalf("second native Gemini pane = %+v", second)
	}
}

func TestResumedSessionID(t *testing.T) {
	const codexID = "019f4e56-c2ba-7652-91d4-e4173bc0f302"
	tests := []struct {
		provider string
		argv     []string
		want     string
	}{
		{"claude", []string{"claude", "--resume", "cc-id"}, "cc-id"},
		{"codex", []string{"codex", "resume", codexID}, codexID},
		{"codex", []string{"codex", "resume", "-m", "gpt-5", codexID}, codexID},
		{"codex", []string{"codex", "resume", "--last"}, ""},
		{"gemini", []string{"gemini", "--resume", "gmi-id"}, "gmi-id"},
		{"gemini", []string{"gemini", "--resume", "latest"}, ""},
		{"gemini", []string{"gemini", "--resume", "5"}, ""},
		{"antigravity", []string{"agy", "--conversation", "agy-id", "--model", "x"}, "agy-id"},
		{"codex", []string{"codex", "exec", "task"}, ""},
		{"codex", []string{"rg", "resume", "not-a-session"}, ""},
	}
	for _, test := range tests {
		if got := resumedSessionID(test.provider, test.argv); got != test.want {
			t.Errorf("resumedSessionID(%q, %#v) = %q, want %q", test.provider, test.argv, got, test.want)
		}
	}
}

func TestPathWithin(t *testing.T) {
	tests := []struct {
		candidate string
		want      bool
	}{
		{"/data/projects/demo", true},
		{"/data/projects/demo/internal", true},
		{"/data/projects/demo-other", false},
		{"/data/projects", false},
	}
	for _, test := range tests {
		if got := pathWithin("/data/projects/demo", test.candidate); got != test.want {
			t.Errorf("pathWithin(candidate=%q) = %v, want %v", test.candidate, got, test.want)
		}
	}
}

func TestParseLsofOpenFiles(t *testing.T) {
	output := "p42\nfcwd\nn/data/projects/demo\nf12u\nn/tmp/sessions/rollout-root.jsonl\nf3r\nn/tmp/sessions/rollout-older.jsonl\n"
	want := []processOpenFile{
		{fd: 12, path: "/tmp/sessions/rollout-root.jsonl"},
		{fd: 3, path: "/tmp/sessions/rollout-older.jsonl"},
	}
	if got := parseLsofOpenFiles(output); !reflect.DeepEqual(got, want) {
		t.Fatalf("parseLsofOpenFiles() = %#v, want %#v", got, want)
	}
}

// writeAgyConversation creates a synthetic agy conversation database. Real agy
// DBs are stock SQLite, but discovery only relies on the cwd appearing as a
// substring in the file (the cheap cwd-affinity check), so a payload that embeds
// the cwd faithfully exercises the discovery path without a sqlite dependency.
func writeAgyConversation(t *testing.T, dir, uuid, cwd string, modAge time.Duration) string {
	t.Helper()
	path := filepath.Join(dir, uuid+".db")
	// Pad before the cwd so it lands past a naive small-prefix scan, matching
	// real DBs where the path lives ~16KB in.
	payload := append([]byte("SQLite format 3\x00"), make([]byte, 20*1024)...)
	payload = append(payload, []byte("...cwd="+cwd+"...")...)
	if err := os.WriteFile(path, payload, 0o644); err != nil {
		t.Fatal(err)
	}
	if modAge != 0 {
		ts := time.Now().Add(-modAge)
		if err := os.Chtimes(path, ts, ts); err != nil {
			t.Fatal(err)
		}
	}
	return path
}

func TestDiscoverAntigravity(t *testing.T) {
	home := t.TempDir()
	workDir := "/data/projects/agydemo"
	convDir := filepath.Join(home, ".gemini", "antigravity-cli", "conversations")
	if err := os.MkdirAll(convDir, 0o755); err != nil {
		t.Fatal(err)
	}

	// Two conversations for the SAME cwd; the newer one (by mtime) must win.
	writeAgyConversation(t, convDir, "11111111-1111-1111-1111-111111111111", workDir, time.Hour)
	newer := writeAgyConversation(t, convDir, "22222222-2222-2222-2222-222222222222", workDir, 0)
	// A conversation for a DIFFERENT cwd must be ignored even though it's newest.
	writeAgyConversation(t, convDir, "33333333-3333-3333-3333-333333333333", "/some/other/dir", -time.Hour)

	discoverer := nativeDiscoverer(home)
	info := discoverer.Discover("agy", workDir, 0)
	if info == nil {
		t.Fatal("expected to discover an agy session, got nil")
	}
	if info.SessionID != "22222222-2222-2222-2222-222222222222" {
		t.Errorf("SessionID = %q, want the newer uuid", info.SessionID)
	}
	if info.Provider != "antigravity" {
		t.Errorf("Provider = %q, want antigravity", info.Provider)
	}
	if info.AgentType != "agy" {
		t.Errorf("AgentType = %q, want agy", info.AgentType)
	}
	if info.SourcePath != newer {
		t.Errorf("SourcePath = %q, want %q", info.SourcePath, newer)
	}

	// A cwd with no matching conversation yields no session.
	if info := discoverer.Discover("agy", "/no/such/agy/project", 0); info != nil {
		t.Errorf("expected nil for non-matching cwd, got %+v", info)
	}
}

// TestDiscoverAgyGmiDisambiguation proves the two providers that share the
// ~/.gemini parent never leak into each other: a gmi session under
// ~/.gemini/tmp/<hash>/chats is NOT reported as agy, and an agy conversation
// under ~/.gemini/antigravity-cli/conversations is NOT reported as gemini.
func TestDiscoverAgyGmiDisambiguation(t *testing.T) {
	home := t.TempDir()
	workDir := "/data/projects/sharedcwd"

	// Lay down a gmi (legacy Gemini CLI) session.
	gmiChats := filepath.Join(home, ".gemini", "tmp", geminiProjectHash(workDir), "chats")
	if err := os.MkdirAll(gmiChats, 0o755); err != nil {
		t.Fatal(err)
	}
	gmiSess := filepath.Join(gmiChats, "session-77.json")
	if err := os.WriteFile(gmiSess, []byte(`{"workspace":"`+workDir+`"}`), 0o644); err != nil {
		t.Fatal(err)
	}

	// Lay down an agy conversation for the SAME cwd.
	convDir := filepath.Join(home, ".gemini", "antigravity-cli", "conversations")
	if err := os.MkdirAll(convDir, 0o755); err != nil {
		t.Fatal(err)
	}
	writeAgyConversation(t, convDir, "aaaaaaaa-aaaa-aaaa-aaaa-aaaaaaaaaaaa", workDir, 0)

	discoverer := nativeDiscoverer(home)

	// gmi discovery must find ONLY the legacy session, never the agy uuid.
	gmiInfo := discoverer.Discover("gmi", workDir, 0)
	if gmiInfo == nil {
		t.Fatal("expected gmi to discover its legacy session, got nil")
	}
	if gmiInfo.Provider != "gemini" || gmiInfo.SessionID != "77" {
		t.Errorf("gmi discovery leaked: got provider=%q id=%q, want gemini/77", gmiInfo.Provider, gmiInfo.SessionID)
	}

	// agy discovery must find ONLY the conversation db, never the gmi session.
	agyInfo := discoverer.Discover("agy", workDir, 0)
	if agyInfo == nil {
		t.Fatal("expected agy to discover its conversation, got nil")
	}
	if agyInfo.Provider != "antigravity" || agyInfo.SessionID != "aaaaaaaa-aaaa-aaaa-aaaa-aaaaaaaaaaaa" {
		t.Errorf("agy discovery leaked: got provider=%q id=%q, want antigravity/uuid", agyInfo.Provider, agyInfo.SessionID)
	}

	// Cross-check: removing the agy store entirely must NOT make gmi vanish,
	// and an agy lookup must never pick up the gmi tmp session.
	gmiOnlyHome := t.TempDir()
	gmiOnlyChats := filepath.Join(gmiOnlyHome, ".gemini", "tmp", geminiProjectHash(workDir), "chats")
	if err := os.MkdirAll(gmiOnlyChats, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(gmiOnlyChats, "session-99.json"),
		[]byte(`{"workspace":"`+workDir+`"}`), 0o644); err != nil {
		t.Fatal(err)
	}
	gmiOnlyDiscoverer := nativeDiscoverer(gmiOnlyHome)
	if info := gmiOnlyDiscoverer.Discover("agy", workDir, 0); info != nil {
		t.Errorf("agy must not discover a gmi tmp session, got %+v", info)
	}
}
