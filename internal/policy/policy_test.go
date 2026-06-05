package policy

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestDefaultPolicy(t *testing.T) {
	p := DefaultPolicy()

	blocked, approval, allowed := p.Stats()
	if blocked == 0 {
		t.Error("expected blocked rules in default policy")
	}
	if approval == 0 {
		t.Error("expected approval rules in default policy")
	}
	if allowed == 0 {
		t.Error("expected allowed rules in default policy")
	}
}

func TestCheck_Blocked(t *testing.T) {
	p := DefaultPolicy()

	cases := []struct {
		name    string
		command string
		blocked bool
	}{
		{"git reset --hard", "git reset --hard HEAD", true},
		{"git reset --hard with spaces", "git   reset   --hard", true},
		{"git clean -fd", "git clean -fd", true},
		{"git push --force", "git push --force", true},
		{"git push -f end", "git push origin main -f", true},
		{"git push -f space", "git push -f origin main", true},
		{"rm -rf /", "rm -rf /", true},
		{"rm -rf ~", "rm -rf ~", true},
		{"git branch -D", "git branch -D feature", true},
		{"git stash drop", "git stash drop", true},
		{"git stash clear", "git stash clear", true},

		// Not blocked
		{"git status", "git status", false},
		{"git add", "git add .", false},
		{"git commit", "git commit -m 'test'", false},
		{"git push", "git push origin main", false},
		{"rm file", "rm file.txt", false},
		{"git reset --soft", "git reset --soft HEAD~1", false},
		// force-with-lease is explicitly allowed (takes precedence)
		{"git push --force-with-lease", "git push --force-with-lease", false},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := p.IsBlocked(tc.command); got != tc.blocked {
				t.Errorf("IsBlocked(%q) = %v, want %v", tc.command, got, tc.blocked)
			}
		})
	}
}

func TestCheck_ApprovalRequired(t *testing.T) {
	p := DefaultPolicy()

	cases := []struct {
		name     string
		command  string
		approval bool
	}{
		{"git rebase -i", "git rebase -i HEAD~3", true},
		{"git commit --amend", "git commit --amend", true},
		{"rm -rf (general)", "rm -rf node_modules", true},

		// Not requiring approval
		{"git status", "git status", false},
		{"git add", "git add .", false},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := p.NeedsApproval(tc.command); got != tc.approval {
				t.Errorf("NeedsApproval(%q) = %v, want %v", tc.command, got, tc.approval)
			}
		})
	}
}

func TestCheck_Allowed(t *testing.T) {
	p := DefaultPolicy()

	cases := []struct {
		name    string
		command string
		action  Action
	}{
		{"force-with-lease allowed", "git push --force-with-lease origin main", ActionAllow},
		{"soft reset allowed", "git reset --soft HEAD~1", ActionAllow},
		{"mixed reset allowed", "git reset HEAD~1", ActionAllow},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			match := p.Check(tc.command)
			if match == nil {
				t.Errorf("Check(%q) = nil, want Action=%v", tc.command, tc.action)
				return
			}
			if match.Action != tc.action {
				t.Errorf("Check(%q).Action = %v, want %v", tc.command, match.Action, tc.action)
			}
		})
	}
}

func TestCheck_Precedence(t *testing.T) {
	// Allowed should take precedence over blocked
	p := DefaultPolicy()

	// git push --force-with-lease contains --force which could match block pattern
	// but should be allowed due to explicit allow rule
	match := p.Check("git push --force-with-lease")
	if match == nil {
		t.Error("expected match for git push --force-with-lease")
		return
	}
	if match.Action != ActionAllow {
		t.Errorf("expected ActionAllow, got %v", match.Action)
	}
}

func TestCheck_NoMatch(t *testing.T) {
	p := DefaultPolicy()

	match := p.Check("ls -la")
	if match != nil {
		t.Errorf("Check('ls -la') = %v, want nil", match)
	}
}

func TestAutomationConfig(t *testing.T) {
	p := DefaultPolicy()

	// Test default automation settings
	if !p.Automation.AutoCommit {
		t.Error("expected AutoCommit to be true by default")
	}
	if p.Automation.AutoPush {
		t.Error("expected AutoPush to be false by default")
	}
	if p.Automation.ForceRelease != "approval" {
		t.Errorf("expected ForceRelease to be 'approval', got %q", p.Automation.ForceRelease)
	}
}

func TestAutomationEnabled(t *testing.T) {
	p := DefaultPolicy()

	if !p.AutomationEnabled("auto_commit") {
		t.Error("expected auto_commit to be enabled")
	}
	if p.AutomationEnabled("auto_push") {
		t.Error("expected auto_push to be disabled")
	}
	if p.AutomationEnabled("unknown_feature") {
		t.Error("expected unknown feature to be disabled")
	}
}

func TestForceReleasePolicy(t *testing.T) {
	p := DefaultPolicy()

	// Default should be "approval"
	if got := p.ForceReleasePolicy(); got != "approval" {
		t.Errorf("ForceReleasePolicy() = %q, want 'approval'", got)
	}

	// Test with empty value
	p.Automation.ForceRelease = ""
	if got := p.ForceReleasePolicy(); got != "approval" {
		t.Errorf("ForceReleasePolicy() with empty = %q, want 'approval'", got)
	}

	// Test explicit values
	for _, val := range []string{"never", "approval", "auto"} {
		p.Automation.ForceRelease = val
		if got := p.ForceReleasePolicy(); got != val {
			t.Errorf("ForceReleasePolicy() = %q, want %q", got, val)
		}
	}
}

func TestNeedsSLBApproval(t *testing.T) {
	p := DefaultPolicy()

	// force_release rule has SLB=true in default policy
	if !p.NeedsSLBApproval("force_release lock-123") {
		t.Error("expected force_release to require SLB approval")
	}

	// Regular commands don't need SLB
	if p.NeedsSLBApproval("git commit --amend") {
		t.Error("git commit --amend should not require SLB approval")
	}

	// Unmatched commands don't need SLB
	if p.NeedsSLBApproval("ls -la") {
		t.Error("ls -la should not require SLB approval")
	}
}

func TestMatchSLBFlag(t *testing.T) {
	p := DefaultPolicy()

	match := p.Check("force_release lock-123")
	if match == nil {
		t.Fatal("expected match for force_release")
	}
	if !match.SLB {
		t.Error("expected SLB flag to be true for force_release match")
	}

	// Non-SLB rule should have SLB=false
	match = p.Check("git commit --amend")
	if match == nil {
		t.Fatal("expected match for git commit --amend")
	}
	if match.SLB {
		t.Error("expected SLB flag to be false for git commit --amend")
	}
}

func TestValidate(t *testing.T) {
	cases := []struct {
		name    string
		policy  func() *Policy
		wantErr bool
	}{
		{
			name: "default policy is valid",
			policy: func() *Policy {
				return DefaultPolicy()
			},
			wantErr: false,
		},
		{
			name: "invalid force_release value",
			policy: func() *Policy {
				p := DefaultPolicy()
				p.Automation.ForceRelease = "invalid"
				return p
			},
			wantErr: true,
		},
		{
			name: "zero version is corrected",
			policy: func() *Policy {
				p := DefaultPolicy()
				p.Version = 0
				return p
			},
			wantErr: false, // Should not error, just default to 1
		},
		{
			name: "valid force_release values",
			policy: func() *Policy {
				p := DefaultPolicy()
				p.Automation.ForceRelease = "never"
				return p
			},
			wantErr: false,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			p := tc.policy()
			err := p.Validate()
			if (err != nil) != tc.wantErr {
				t.Errorf("Validate() error = %v, wantErr %v", err, tc.wantErr)
			}
		})
	}
}

func TestPolicyVersion(t *testing.T) {
	p := DefaultPolicy()
	if p.Version != 1 {
		t.Errorf("expected Version to be 1, got %d", p.Version)
	}
}

func TestSplitLines(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name  string
		input []byte
		want  int // expected number of lines
	}{
		{"empty input", []byte{}, 0},
		{"single line no newline", []byte("hello"), 1},
		{"single line with newline", []byte("hello\n"), 1},
		{"two lines", []byte("hello\nworld"), 2},
		{"two lines trailing newline", []byte("hello\nworld\n"), 2},
		{"three lines", []byte("a\nb\nc"), 3},
		{"empty lines", []byte("\n\n\n"), 3},
		{"mixed empty and content", []byte("a\n\nb\n"), 3},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got := splitLines(tt.input)
			if len(got) != tt.want {
				t.Errorf("splitLines(%q) returned %d lines, want %d", tt.input, len(got), tt.want)
			}
		})
	}
}

func TestSplitLines_Content(t *testing.T) {
	t.Parallel()

	data := []byte("first\nsecond\nthird")
	lines := splitLines(data)

	if len(lines) != 3 {
		t.Fatalf("expected 3 lines, got %d", len(lines))
	}
	if string(lines[0]) != "first" {
		t.Errorf("lines[0] = %q, want %q", lines[0], "first")
	}
	if string(lines[1]) != "second" {
		t.Errorf("lines[1] = %q, want %q", lines[1], "second")
	}
	if string(lines[2]) != "third" {
		t.Errorf("lines[2] = %q, want %q", lines[2], "third")
	}
}

func TestSplitLines_NoAllocation(t *testing.T) {
	t.Parallel()

	// splitLines should return sub-slices of the original data, not copies
	data := []byte("abc\ndef\nghi")
	lines := splitLines(data)

	if len(lines) != 3 {
		t.Fatalf("expected 3 lines, got %d", len(lines))
	}
	// Modify the original data and verify the lines reflect the change
	data[0] = 'X'
	if lines[0][0] != 'X' {
		t.Error("splitLines should return sub-slices, not copies")
	}
}

func TestBlockedEntryAction(t *testing.T) {
	t.Parallel()

	entry := BlockedEntry{
		Session: "test-session",
		Agent:   "agent-1",
		Command: "rm -rf /",
		Pattern: `rm\s+-rf\s+/`,
		Reason:  "destructive command",
		Action:  ActionBlock,
	}

	if entry.Action != ActionBlock {
		t.Errorf("Action = %v, want %v", entry.Action, ActionBlock)
	}
	if entry.Session != "test-session" {
		t.Errorf("Session = %q, want %q", entry.Session, "test-session")
	}
}

func TestPolicyStats(t *testing.T) {
	t.Parallel()

	p := DefaultPolicy()
	blocked, approval, allowed := p.Stats()

	// Default policy should have all three types
	if blocked == 0 {
		t.Error("expected blocked > 0")
	}
	if approval == 0 {
		t.Error("expected approval > 0")
	}
	if allowed == 0 {
		t.Error("expected allowed > 0")
	}

	// Total should equal sum of all rules
	total := blocked + approval + allowed
	expectedTotal := len(p.Blocked) + len(p.ApprovalRequired) + len(p.Allowed)
	if total != expectedTotal {
		t.Errorf("Stats() total %d != rule count %d", total, expectedTotal)
	}
}

// =============================================================================
// Policy Load tests
// =============================================================================

func TestLoad(t *testing.T) {
	t.Parallel()

	t.Run("valid YAML", func(t *testing.T) {
		t.Parallel()
		dir := t.TempDir()
		path := filepath.Join(dir, "policy.yaml")
		content := `version: 1
blocked:
  - pattern: "rm -rf /"
    reason: "dangerous"
allowed:
  - pattern: "git status"
    reason: "safe"
approval_required:
  - pattern: "force_release"
    reason: "needs approval"
    slb: true
automation:
  auto_push: false
  auto_commit: true
  force_release: "approval"
`
		os.WriteFile(path, []byte(content), 0644)
		p, err := Load(path)
		if err != nil {
			t.Fatalf("Load: %v", err)
		}
		if len(p.Blocked) != 1 {
			t.Errorf("expected 1 blocked rule, got %d", len(p.Blocked))
		}
		if len(p.Allowed) != 1 {
			t.Errorf("expected 1 allowed rule, got %d", len(p.Allowed))
		}
		if !p.ApprovalRequired[0].SLB {
			t.Error("expected SLB flag on approval_required rule")
		}
	})

	t.Run("invalid regex", func(t *testing.T) {
		t.Parallel()
		dir := t.TempDir()
		path := filepath.Join(dir, "policy.yaml")
		content := `version: 1
blocked:
  - pattern: "[invalid"
    reason: "bad regex"
`
		os.WriteFile(path, []byte(content), 0644)
		_, err := Load(path)
		if err == nil {
			t.Error("expected error for invalid regex pattern")
		}
	})

	t.Run("nonexistent file", func(t *testing.T) {
		t.Parallel()
		_, err := Load("/nonexistent/path/policy.yaml")
		if err == nil {
			t.Error("expected error for nonexistent file")
		}
	})

	t.Run("invalid YAML", func(t *testing.T) {
		t.Parallel()
		dir := t.TempDir()
		path := filepath.Join(dir, "policy.yaml")
		os.WriteFile(path, []byte("{{{{not yaml"), 0644)
		_, err := Load(path)
		if err == nil {
			t.Error("expected error for invalid YAML")
		}
	})
	t.Run("unknown field", func(t *testing.T) {
		t.Parallel()
		dir := t.TempDir()
		path := filepath.Join(dir, "policy.yaml")
		content := `version: 1
blocked:
  - pattern: "rm -rf /"
legacy: true
`
		os.WriteFile(path, []byte(content), 0644)
		_, err := Load(path)
		if err == nil || !strings.Contains(err.Error(), "field legacy not found") {
			t.Fatalf("expected strict unknown-field error, got %v", err)
		}
	})

	t.Run("invalid force_release in load", func(t *testing.T) {
		t.Parallel()
		dir := t.TempDir()
		path := filepath.Join(dir, "policy.yaml")
		content := `version: 1
automation:
  force_release: invalid_value
`
		os.WriteFile(path, []byte(content), 0644)
		_, err := Load(path)
		if err == nil || !strings.Contains(err.Error(), "invalid force_release value") {
			t.Fatalf("expected force_release validation error, got %v", err)
		}
	})
}

func TestCompileInvalidPatterns(t *testing.T) {
	t.Parallel()

	t.Run("invalid blocked pattern", func(t *testing.T) {
		t.Parallel()
		p := &Policy{
			Blocked: []Rule{{Pattern: "[unclosed"}},
		}
		if err := p.compile(); err == nil {
			t.Error("expected error for invalid blocked pattern")
		}
	})

	t.Run("invalid approval pattern", func(t *testing.T) {
		t.Parallel()
		p := &Policy{
			ApprovalRequired: []Rule{{Pattern: "(?P<>invalid)"}},
		}
		if err := p.compile(); err == nil {
			t.Error("expected error for invalid approval_required pattern")
		}
	})

	t.Run("invalid allowed pattern", func(t *testing.T) {
		t.Parallel()
		p := &Policy{
			Allowed: []Rule{{Pattern: "*bad"}},
		}
		if err := p.compile(); err == nil {
			t.Error("expected error for invalid allowed pattern")
		}
	})
}

// =============================================================================
// BlockedLogger tests
// =============================================================================

func TestBlockedLogger(t *testing.T) {
	t.Parallel()

	t.Run("create log and write entries", func(t *testing.T) {
		t.Parallel()
		dir := t.TempDir()
		logPath := filepath.Join(dir, "blocked.jsonl")

		logger, err := NewBlockedLogger(logPath)
		if err != nil {
			t.Fatalf("NewBlockedLogger: %v", err)
		}
		defer logger.Close()

		// Write via Log
		err = logger.Log(&BlockedEntry{
			Timestamp: time.Date(2026, 1, 15, 10, 0, 0, 0, time.UTC),
			Session:   "sess1",
			Agent:     "agent1",
			Command:   "rm -rf /",
			Pattern:   `rm\s+-rf\s+/`,
			Reason:    "destructive",
			Action:    ActionBlock,
		})
		if err != nil {
			t.Fatalf("Log: %v", err)
		}

		// Write via LogBlocked
		err = logger.LogBlocked("sess2", "agent2", "git reset --hard", `git\s+reset\s+--hard`, "dangerous")
		if err != nil {
			t.Fatalf("LogBlocked: %v", err)
		}

		logger.Close()

		// Read back
		entries, err := ReadBlockedLog(logPath)
		if err != nil {
			t.Fatalf("ReadBlockedLog: %v", err)
		}
		if len(entries) != 2 {
			t.Fatalf("expected 2 entries, got %d", len(entries))
		}
		if entries[0].Command != "rm -rf /" {
			t.Errorf("entries[0].Command = %q, want %q", entries[0].Command, "rm -rf /")
		}
		if entries[1].Agent != "agent2" {
			t.Errorf("entries[1].Agent = %q, want %q", entries[1].Agent, "agent2")
		}
	})

	t.Run("log with nil file", func(t *testing.T) {
		t.Parallel()
		logger := &BlockedLogger{path: "unused"}
		// file is nil, should return nil error
		err := logger.Log(&BlockedEntry{Command: "test"})
		if err != nil {
			t.Errorf("Log with nil file should return nil, got %v", err)
		}
	})

	t.Run("log auto-sets timestamp", func(t *testing.T) {
		t.Parallel()
		dir := t.TempDir()
		logPath := filepath.Join(dir, "blocked.jsonl")

		logger, err := NewBlockedLogger(logPath)
		if err != nil {
			t.Fatalf("NewBlockedLogger: %v", err)
		}
		defer logger.Close()

		before := time.Now()
		logger.Log(&BlockedEntry{
			Command: "test",
			Pattern: "test",
			Reason:  "test",
			Action:  ActionBlock,
		})
		logger.Close()

		entries, _ := ReadBlockedLog(logPath)
		if len(entries) != 1 {
			t.Fatalf("expected 1 entry, got %d", len(entries))
		}
		if entries[0].Timestamp.Before(before) {
			t.Error("auto-set timestamp should be >= before")
		}
	})

	t.Run("close idempotent", func(t *testing.T) {
		t.Parallel()
		dir := t.TempDir()
		logPath := filepath.Join(dir, "blocked.jsonl")

		logger, err := NewBlockedLogger(logPath)
		if err != nil {
			t.Fatalf("NewBlockedLogger: %v", err)
		}

		if err := logger.Close(); err != nil {
			t.Errorf("first Close: %v", err)
		}
		if err := logger.Close(); err != nil {
			t.Errorf("second Close should be nil, got %v", err)
		}
	})
}

func TestReadBlockedLog(t *testing.T) {
	t.Parallel()

	t.Run("nonexistent file returns nil", func(t *testing.T) {
		t.Parallel()
		entries, err := ReadBlockedLog("/nonexistent/path.jsonl")
		if err != nil {
			t.Errorf("expected nil error for nonexistent, got %v", err)
		}
		if entries != nil {
			t.Errorf("expected nil entries, got %v", entries)
		}
	})

	t.Run("empty file", func(t *testing.T) {
		t.Parallel()
		dir := t.TempDir()
		path := filepath.Join(dir, "empty.jsonl")
		os.WriteFile(path, []byte(""), 0644)

		entries, err := ReadBlockedLog(path)
		if err != nil {
			t.Fatalf("ReadBlockedLog: %v", err)
		}
		if len(entries) != 0 {
			t.Errorf("expected 0 entries from empty file, got %d", len(entries))
		}
	})

	t.Run("skips malformed entries", func(t *testing.T) {
		t.Parallel()
		dir := t.TempDir()
		path := filepath.Join(dir, "mixed.jsonl")

		good, _ := json.Marshal(BlockedEntry{Command: "good", Action: ActionBlock})
		content := string(good) + "\n{invalid json}\n" + string(good) + "\n"
		os.WriteFile(path, []byte(content), 0644)

		entries, err := ReadBlockedLog(path)
		if err != nil {
			t.Fatalf("ReadBlockedLog: %v", err)
		}
		if len(entries) != 2 {
			t.Errorf("expected 2 valid entries (skipping malformed), got %d", len(entries))
		}
	})
}

func TestRecentBlocked(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	path := filepath.Join(dir, "blocked.jsonl")

	logger, err := NewBlockedLogger(path)
	if err != nil {
		t.Fatalf("NewBlockedLogger: %v", err)
	}

	// Write an old entry and a recent entry
	oldEntry := &BlockedEntry{
		Timestamp: time.Now().Add(-48 * time.Hour),
		Command:   "old command",
		Pattern:   "old",
		Reason:    "old",
		Action:    ActionBlock,
	}
	recentEntry := &BlockedEntry{
		Timestamp: time.Now(),
		Command:   "recent command",
		Pattern:   "recent",
		Reason:    "recent",
		Action:    ActionBlock,
	}
	logger.Log(oldEntry)
	logger.Log(recentEntry)
	logger.Close()

	// Recent within last 24 hours should return only 1
	recent, err := RecentBlocked(path, 24)
	if err != nil {
		t.Fatalf("RecentBlocked: %v", err)
	}
	if len(recent) != 1 {
		t.Errorf("expected 1 recent entry (last 24h), got %d", len(recent))
	}
	if len(recent) > 0 && recent[0].Command != "recent command" {
		t.Errorf("recent entry should be 'recent command', got %q", recent[0].Command)
	}

	// Recent within last 72 hours should return both
	recent, err = RecentBlocked(path, 72)
	if err != nil {
		t.Fatalf("RecentBlocked: %v", err)
	}
	if len(recent) != 2 {
		t.Errorf("expected 2 entries (last 72h), got %d", len(recent))
	}

	// Nonexistent path returns nil
	recent, err = RecentBlocked("/nonexistent.jsonl", 24)
	if err != nil {
		t.Fatalf("RecentBlocked nonexistent: %v", err)
	}
	if recent != nil {
		t.Errorf("expected nil for nonexistent, got %v", recent)
	}
}

// bd-dqpo3: a single malformed pattern in one slice must not silently
// disable every rule that follows it in the same slice.
func TestCompile_MalformedPatternDoesNotDisableLaterRules(t *testing.T) {
	p := &Policy{
		Blocked: []Rule{
			{Pattern: `(unclosed`, Reason: "bad regex"},
			{Pattern: `git\s+reset\s+--hard`, Reason: "still must block hard reset"},
		},
	}
	err := p.compile()
	if err == nil {
		t.Fatal("expected compile error for malformed pattern")
	}
	// The good rule must still have its regex set.
	if p.Blocked[1].regex == nil {
		t.Fatal("good rule (Blocked[1]) regex was nil — early-return killed it")
	}
	// And Check must still match the good rule.
	if got := p.Check("git reset --hard HEAD~1"); got == nil || got.Action != ActionBlock {
		t.Errorf("Check on good rule = %+v, want a block match", got)
	}
}

func TestCompile_AllErrorsJoinedAcrossSlices(t *testing.T) {
	p := &Policy{
		Allowed:          []Rule{{Pattern: `(bad-allowed`}},
		Blocked:          []Rule{{Pattern: `(bad-blocked`}},
		ApprovalRequired: []Rule{{Pattern: `(bad-approval`}},
	}
	err := p.compile()
	if err == nil {
		t.Fatal("expected compile error for all three malformed patterns")
	}
	msg := err.Error()
	for _, kind := range []string{"allowed", "blocked", "approval_required"} {
		if !strings.Contains(msg, kind) {
			t.Errorf("joined error missing %q category: %v", kind, err)
		}
	}
}

// bd-9iknu: recompiling after policy edits must not keep stale regex
// from a previous successful compile when the new pattern is malformed.
func TestCompile_RecompileMalformedPatternClearsStaleRegex(t *testing.T) {
	p := &Policy{
		Blocked: []Rule{
			{Pattern: `git\s+reset\s+--hard`, Reason: "block hard reset"},
		},
	}
	if err := p.compile(); err != nil {
		t.Fatalf("initial compile failed: %v", err)
	}
	if p.Blocked[0].regex == nil {
		t.Fatal("initial compile did not set regex")
	}

	// Mutate to malformed and recompile.
	p.Blocked[0].Pattern = `(bad`
	if err := p.compile(); err == nil {
		t.Fatal("expected compile error after malformed pattern update")
	}
	if p.Blocked[0].regex != nil {
		t.Fatal("stale regex survived malformed recompile; want nil")
	}
	if got := p.Check("git reset --hard HEAD~1"); got != nil {
		t.Fatalf("Check matched using stale regex: %+v", got)
	}
}
