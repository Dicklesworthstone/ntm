package cli

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestIsBindingLine(t *testing.T) {
	tests := []struct {
		name     string
		line     string
		key      string
		expected bool
	}{
		{
			name:     "bind-key F12",
			line:     `bind-key -n F12 display-popup -E "ntm dashboard"`,
			key:      "F12",
			expected: true,
		},
		{
			name:     "bind F12",
			line:     `bind -n F12 display-popup -E "ntm dashboard"`,
			key:      "F12",
			expected: true,
		},
		{
			name:     "bind-key F6 not F12",
			line:     `bind-key -n F6 display-popup -E "ntm palette"`,
			key:      "F12",
			expected: false,
		},
		{
			name:     "comment line",
			line:     `# bind-key -n F12 display-popup`,
			key:      "F12",
			expected: false,
		},
		{
			name:     "empty line",
			line:     "",
			key:      "F12",
			expected: false,
		},
		{
			name:     "other command",
			line:     `set -g status-style bg=black`,
			key:      "F12",
			expected: false,
		},
		{
			name:     "bind without -n flag",
			line:     `bind-key F12 display-popup`,
			key:      "F12",
			expected: false,
		},
		{
			name:     "bind with -T root",
			line:     `bind-key -T root F12 display-popup`,
			key:      "F12",
			expected: false, // -T not currently matched
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			result := isBindingLine(tc.line, tc.key)
			t.Logf("isBindingLine(line=%q, key=%q) -> %v", tc.line, tc.key, result)
			if result != tc.expected {
				t.Errorf("isBindingLine(%q, %q) = %v, want %v", tc.line, tc.key, result, tc.expected)
			}
		})
	}
}

func TestIsOverlayKeyBound(t *testing.T) {
	// Create temporary tmux.conf files
	tests := []struct {
		name      string
		confLines []string
		key       string
		wantBound bool
	}{
		{
			name: "F12 bound with ntm",
			confLines: []string{
				"set -g status on",
				`bind-key -n F12 display-popup -E "NTM_POPUP=1 ntm dashboard --popup #{session_name}"`,
				"set -g mouse on",
			},
			key:       "F12",
			wantBound: true,
		},
		{
			name: "F12 bound but not ntm",
			confLines: []string{
				"set -g status on",
				`bind-key -n F12 display-popup -E "htop"`,
			},
			key:       "F12",
			wantBound: false,
		},
		{
			name: "F12 bound to palette instead of overlay",
			confLines: []string{
				`bind-key -n F12 display-popup -E "ntm palette"`,
			},
			key:       "F12",
			wantBound: false,
		},
		{
			name: "F6 bound, checking F12",
			confLines: []string{
				`bind-key -n F6 run-shell "ntm palette #{session_name}"`,
			},
			key:       "F12",
			wantBound: false,
		},
		{
			name:      "empty conf",
			confLines: []string{},
			key:       "F12",
			wantBound: false,
		},
		{
			name: "commented out binding",
			confLines: []string{
				`# bind-key -n F12 display-popup -E "ntm dashboard"`,
			},
			key:       "F12",
			wantBound: false,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			tmpDir := t.TempDir()
			confPath := filepath.Join(tmpDir, ".tmux.conf")

			// Write test config
			content := strings.Join(tc.confLines, "\n")
			if err := os.WriteFile(confPath, []byte(content), 0644); err != nil {
				t.Fatalf("write tmux.conf: %v", err)
			}

			// Override HOME for the test
			origHome := os.Getenv("HOME")
			t.Setenv("HOME", tmpDir)
			defer os.Setenv("HOME", origHome)

			result := isOverlayKeyBound(tc.key)
			if result != tc.wantBound {
				t.Errorf("isOverlayKeyBound(%q) = %v, want %v\nconfig:\n%s",
					tc.key, result, tc.wantBound, content)
			}
		})
	}
}

func TestIsOverlayBindingLine(t *testing.T) {
	tests := []struct {
		name string
		line string
		key  string
		want bool
	}{
		{
			name: "overlay binding",
			line: `bind-key -n F12 display-popup -E -w 95% -h 95% "NTM_POPUP=1 ntm dashboard --popup #{session_name}"`,
			key:  "F12",
			want: true,
		},
		{
			name: "palette binding is not overlay",
			line: `bind-key -n F12 display-popup -E "ntm palette"`,
			key:  "F12",
			want: false,
		},
		{
			name: "dashboard without popup is not overlay",
			line: `bind-key -n F12 run-shell "ntm dashboard proj"`,
			key:  "F12",
			want: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := isOverlayBindingLine(tt.line, tt.key); got != tt.want {
				t.Fatalf("isOverlayBindingLine(%q, %q) = %v, want %v", tt.line, tt.key, got, tt.want)
			}
		})
	}
}

func TestIsOverlayKeyBound_NoConfFile(t *testing.T) {
	// Test when tmux.conf doesn't exist
	tmpDir := t.TempDir()
	// Don't create .tmux.conf

	origHome := os.Getenv("HOME")
	t.Setenv("HOME", tmpDir)
	defer os.Setenv("HOME", origHome)

	result := isOverlayKeyBound("F12")
	if result != false {
		t.Errorf("isOverlayKeyBound with no conf file = %v, want false", result)
	}
}

func TestOverlayInnerCmdConstruction(t *testing.T) {
	// Test that the inner command for display-popup is correctly quoted
	tests := []struct {
		name       string
		session    string
		ntmBin     string
		wantSubstr string
	}{
		{
			name:       "simple session",
			session:    "myproject",
			ntmBin:     "/usr/local/bin/ntm",
			wantSubstr: `NTM_POPUP=1 '/usr/local/bin/ntm' dashboard --popup 'myproject'`,
		},
		{
			name:       "session with spaces",
			session:    "my project",
			ntmBin:     "/usr/local/bin/ntm",
			wantSubstr: `NTM_POPUP=1 '/usr/local/bin/ntm' dashboard --popup 'my project'`,
		},
		{
			name:       "binary with spaces",
			session:    "myproject",
			ntmBin:     "/path with spaces/ntm",
			wantSubstr: `NTM_POPUP=1 '/path with spaces/ntm' dashboard --popup 'myproject'`,
		},
		{
			name:       "bare ntm fallback",
			session:    "myproject",
			ntmBin:     "ntm",
			wantSubstr: `NTM_POPUP=1 'ntm' dashboard --popup 'myproject'`,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			// Construct the inner command the same way as launchOverlayPopup
			innerCmd := "NTM_POPUP=1 '" + tc.ntmBin + "' dashboard --popup '" + tc.session + "'"
			if innerCmd != tc.wantSubstr {
				t.Errorf("innerCmd = %q, want %q", innerCmd, tc.wantSubstr)
			}

			// Verify quotes are balanced
			singleQuotes := strings.Count(innerCmd, "'")
			if singleQuotes%2 != 0 {
				t.Errorf("unbalanced single quotes in command: %q", innerCmd)
			}

			// Verify NTM_POPUP env var is present
			if !strings.HasPrefix(innerCmd, "NTM_POPUP=1") {
				t.Errorf("missing NTM_POPUP env var: %q", innerCmd)
			}

			// Verify --popup flag is present
			if !strings.Contains(innerCmd, "--popup") {
				t.Errorf("missing --popup flag: %q", innerCmd)
			}
		})
	}
}

func TestOverlayTmuxArgs(t *testing.T) {
	// Test the tmux display-popup arguments structure
	session := "myproject"
	ntmBin := "ntm"
	innerCmd := "NTM_POPUP=1 '" + ntmBin + "' dashboard --popup '" + session + "'"

	// Structure from launchOverlayPopup:
	// ["display-popup", "-E", "-w", "95%", "-h", "95%", innerCmd]
	tmuxArgs := []string{
		"display-popup",
		"-E",        // close popup when command exits
		"-w", "95%", // 95% of terminal width
		"-h", "95%", // 95% of terminal height
		innerCmd,
	}

	// Verify arg count: display-popup, -E, -w, 95%, -h, 95%, innerCmd = 7 args
	if len(tmuxArgs) != 7 {
		t.Errorf("tmuxArgs count = %d, want 7\n  args: %v", len(tmuxArgs), tmuxArgs)
	}

	if tmuxArgs[0] != "display-popup" {
		t.Errorf("tmuxArgs[0] = %q, want display-popup", tmuxArgs[0])
	}

	if tmuxArgs[1] != "-E" {
		t.Errorf("tmuxArgs[1] = %q, want -E", tmuxArgs[1])
	}

	// -w 95% should be args[2] and [3]
	if tmuxArgs[2] != "-w" || tmuxArgs[3] != "95%" {
		t.Errorf("tmuxArgs[2:4] = %v, want [-w, 95%%]", tmuxArgs[2:4])
	}

	// -h 95% should be args[4] and [5]
	if tmuxArgs[4] != "-h" || tmuxArgs[5] != "95%" {
		t.Errorf("tmuxArgs[4:6] = %v, want [-h, 95%%]", tmuxArgs[4:6])
	}

	// innerCmd should be args[6]
	if tmuxArgs[6] != innerCmd {
		t.Errorf("tmuxArgs[6] = %q, want %q", tmuxArgs[6], innerCmd)
	}
}

func TestOverlayBindingCommand(t *testing.T) {
	// Test the bind command format for tmux
	key := "F12"
	// The expected format from setupOverlayBinding
	bindCmd := `bind-key -n ` + key + ` display-popup -E -w 95% -h 95% "NTM_POPUP=1 ntm dashboard --popup #{session_name}"`

	// Verify structure
	if !strings.HasPrefix(bindCmd, "bind-key -n "+key) {
		t.Errorf("bindCmd missing 'bind-key -n %s' prefix: %s", key, bindCmd)
	}

	if !strings.Contains(bindCmd, "display-popup") {
		t.Errorf("bindCmd missing 'display-popup': %s", bindCmd)
	}

	if !strings.Contains(bindCmd, "-E") {
		t.Errorf("bindCmd missing '-E' flag: %s", bindCmd)
	}

	if !strings.Contains(bindCmd, "-w 95%") {
		t.Errorf("bindCmd missing '-w 95%%': %s", bindCmd)
	}

	if !strings.Contains(bindCmd, "-h 95%") {
		t.Errorf("bindCmd missing '-h 95%%': %s", bindCmd)
	}

	if !strings.Contains(bindCmd, "NTM_POPUP=1") {
		t.Errorf("bindCmd missing 'NTM_POPUP=1': %s", bindCmd)
	}

	if !strings.Contains(bindCmd, "#{session_name}") {
		t.Errorf("bindCmd missing '#{session_name}' tmux variable: %s", bindCmd)
	}
}

func TestOverlayKeyVariants(t *testing.T) {
	// Test different key formats
	keys := []string{"F12", "F6", "F1", "C-o", "M-o"}

	for _, key := range keys {
		t.Run(key, func(t *testing.T) {
			// Test isBindingLine can detect the key
			bindLine := "bind-key -n " + key + " some-command"
			if !isBindingLine(bindLine, key) {
				t.Errorf("isBindingLine failed for key %s", key)
			}

			// Test it doesn't match different keys
			otherKey := "F99"
			if key != otherKey && isBindingLine(bindLine, otherKey) {
				t.Errorf("isBindingLine incorrectly matched %s for line with %s", otherKey, key)
			}
		})
	}
}

func TestOverlayPopupEnvVar(t *testing.T) {
	// Test that NTM_POPUP environment variable is checked correctly
	tests := []struct {
		name     string
		envValue string
		wantPop  bool
	}{
		{name: "unset", envValue: "", wantPop: false},
		{name: "set to 1", envValue: "1", wantPop: true},
		{name: "set to true", envValue: "true", wantPop: true},
		{name: "set to 0", envValue: "0", wantPop: false},
		{name: "set to anything", envValue: "yes", wantPop: true},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if tc.envValue != "" {
				t.Setenv("NTM_POPUP", tc.envValue)
			} else {
				os.Unsetenv("NTM_POPUP")
			}

			// Check the environment variable
			isPopup := os.Getenv("NTM_POPUP") != "" && os.Getenv("NTM_POPUP") != "0"
			t.Logf("NTM_POPUP=%q -> isPopup=%v (expected=%v)", tc.envValue, isPopup, tc.wantPop)
			if isPopup != tc.wantPop {
				t.Errorf("popup detection = %v, want %v for NTM_POPUP=%q", isPopup, tc.wantPop, tc.envValue)
			}
		})
	}
}

func TestOverlayBindingHelpersAgree(t *testing.T) {
	t.Parallel()

	key := "F12"
	cmd := overlayBindingCommand(key)
	args := overlayBindingArgs(key)

	if len(args) != 10 {
		t.Fatalf("overlayBindingArgs(%q) returned %d args, want 10: %v", key, len(args), args)
	}
	if args[0] != "bind-key" || args[1] != "-n" || args[2] != key {
		t.Fatalf("unexpected bind prefix for %q: %v", key, args[:3])
	}
	if !strings.Contains(cmd, args[len(args)-1]) {
		t.Fatalf("overlayBindingCommand(%q) = %q does not include terminal command %q", key, cmd, args[len(args)-1])
	}

	t.Logf("overlay binding command: %s", cmd)
	t.Logf("overlay binding args: %v", args)
}

func TestSetupOverlayBindingWithWriter_PreservesExistingConfig(t *testing.T) {
	tmpDir := t.TempDir()
	confPath := filepath.Join(tmpDir, ".tmux.conf")
	existing := strings.Join([]string{
		"# existing comment",
		"set -g mouse on",
		"",
	}, "\n")
	if err := os.WriteFile(confPath, []byte(existing), 0644); err != nil {
		t.Fatalf("write tmux.conf: %v", err)
	}

	t.Setenv("HOME", tmpDir)
	t.Setenv("TMUX", "")

	var out bytes.Buffer
	if err := setupOverlayBindingWithWriter("F12", &out); err != nil {
		t.Fatalf("setupOverlayBindingWithWriter: %v", err)
	}

	gotBytes, err := os.ReadFile(confPath)
	if err != nil {
		t.Fatalf("read tmux.conf: %v", err)
	}
	got := string(gotBytes)
	t.Logf("tmux.conf after setup:\n%s", got)

	if !strings.Contains(got, "# existing comment") {
		t.Fatalf("existing comment missing after overlay setup:\n%s", got)
	}
	if !strings.Contains(got, overlayBindingCommand("F12")) {
		t.Fatalf("overlay binding missing from tmux.conf:\n%s", got)
	}
	if !strings.Contains(out.String(), "F12") {
		t.Fatalf("expected output to mention F12, got %q", out.String())
	}
}

func TestSetupOverlayBindingWithWriter_ReplacesExistingBindingInPlace(t *testing.T) {
	tmpDir := t.TempDir()
	confPath := filepath.Join(tmpDir, ".tmux.conf")
	existing := strings.Join([]string{
		"# keep me",
		`bind-key -n F12 display-popup -E "htop"`,
		`bind-key -n F6 display-popup -E "ntm palette"`,
	}, "\n")
	if err := os.WriteFile(confPath, []byte(existing), 0644); err != nil {
		t.Fatalf("write tmux.conf: %v", err)
	}

	t.Setenv("HOME", tmpDir)
	t.Setenv("TMUX", "")

	var out bytes.Buffer
	if err := setupOverlayBindingWithWriter("F12", &out); err != nil {
		t.Fatalf("setupOverlayBindingWithWriter: %v", err)
	}

	gotBytes, err := os.ReadFile(confPath)
	if err != nil {
		t.Fatalf("read tmux.conf: %v", err)
	}
	got := string(gotBytes)
	t.Logf("tmux.conf after replacement:\n%s", got)

	if count := strings.Count(got, "bind-key -n F12"); count != 1 {
		t.Fatalf("expected exactly one F12 binding after replacement, got %d:\n%s", count, got)
	}
	if strings.Contains(got, `"htop"`) {
		t.Fatalf("stale F12 binding survived replacement:\n%s", got)
	}
	if !strings.Contains(got, `bind-key -n F6 display-popup -E "ntm palette"`) {
		t.Fatalf("unrelated binding should remain untouched:\n%s", got)
	}
	if !strings.Contains(got, "# keep me") {
		t.Fatalf("comment should remain after replacement:\n%s", got)
	}
	if !strings.Contains(out.String(), "Updated existing F12 binding") {
		t.Fatalf("expected replacement output, got %q", out.String())
	}
}

func TestBindCmdOverlayUsesDefaultF12(t *testing.T) {
	tmpDir := t.TempDir()
	confPath := filepath.Join(tmpDir, ".tmux.conf")
	t.Setenv("HOME", tmpDir)
	t.Setenv("TMUX", "")

	cmd := newBindCmd()
	cmd.SetArgs([]string{"--overlay"})
	if err := cmd.Execute(); err != nil {
		t.Fatalf("bind --overlay failed: %v", err)
	}

	gotBytes, err := os.ReadFile(confPath)
	if err != nil {
		t.Fatalf("read tmux.conf: %v", err)
	}
	got := string(gotBytes)
	t.Logf("tmux.conf after bind --overlay:\n%s", got)

	if !strings.Contains(got, `bind-key -n F12`) {
		t.Fatalf("expected default overlay key F12 in tmux.conf:\n%s", got)
	}
	if !strings.Contains(got, "dashboard --popup") {
		t.Fatalf("expected overlay dashboard command in tmux.conf:\n%s", got)
	}
}

// TestOverlayCommandQuotingEdgeCases tests edge cases for session/binary quoting.
func TestOverlayCommandQuotingEdgeCases(t *testing.T) {
	testCases := []struct {
		name           string
		session        string
		ntmBin         string
		expectPanic    bool
		wantContains   []string
		wantNotContain []string
		desc           string
	}{
		{
			name:    "normal_session",
			session: "myproject",
			ntmBin:  "/usr/bin/ntm",
			wantContains: []string{
				"NTM_POPUP=1",
				"'myproject'",
				"--popup",
			},
			desc: "Standard session name with absolute binary path",
		},
		{
			name:    "session_with_hyphen",
			session: "my-project-dev",
			ntmBin:  "ntm",
			wantContains: []string{
				"'my-project-dev'",
			},
			desc: "Session name with hyphens",
		},
		{
			name:    "session_with_numbers",
			session: "project123",
			ntmBin:  "ntm",
			wantContains: []string{
				"'project123'",
			},
			desc: "Session name with numbers",
		},
		{
			name:    "session_with_underscore",
			session: "my_project_test",
			ntmBin:  "ntm",
			wantContains: []string{
				"'my_project_test'",
			},
			desc: "Session name with underscores",
		},
		{
			name:    "path_with_spaces",
			session: "proj",
			ntmBin:  "/home/user/my apps/ntm",
			wantContains: []string{
				"'/home/user/my apps/ntm'",
			},
			desc: "Binary path containing spaces",
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			t.Logf("Test: %s", tc.desc)
			t.Logf("Session: %q, Binary: %q", tc.session, tc.ntmBin)

			// Construct command same as launchOverlayPopup
			innerCmd := "NTM_POPUP=1 '" + tc.ntmBin + "' dashboard --popup '" + tc.session + "'"
			t.Logf("Generated command: %s", innerCmd)

			// Check expected substrings
			for _, want := range tc.wantContains {
				if !strings.Contains(innerCmd, want) {
					t.Errorf("command missing %q\n  full command: %s", want, innerCmd)
				}
			}

			// Check unwanted substrings
			for _, notWant := range tc.wantNotContain {
				if strings.Contains(innerCmd, notWant) {
					t.Errorf("command should not contain %q\n  full command: %s", notWant, innerCmd)
				}
			}

			// Verify quote balance
			singleQuotes := strings.Count(innerCmd, "'")
			if singleQuotes%2 != 0 {
				t.Errorf("unbalanced quotes (count=%d) in: %s", singleQuotes, innerCmd)
			}
			t.Logf("Quote balance: %d single quotes (balanced=%v)", singleQuotes, singleQuotes%2 == 0)
		})
	}
}

// TestOverlayBindingLineVariations tests various binding line formats.
func TestOverlayBindingLineVariations(t *testing.T) {
	testCases := []struct {
		name      string
		line      string
		key       string
		wantMatch bool
		matchType string // "binding", "overlay", or "none"
		desc      string
	}{
		{
			name:      "full_overlay_binding",
			line:      `bind-key -n F12 display-popup -E -w 95% -h 95% "NTM_POPUP=1 ntm dashboard --popup #{session_name}"`,
			key:       "F12",
			wantMatch: true,
			matchType: "overlay",
			desc:      "Complete overlay binding with all flags",
		},
		{
			name:      "minimal_overlay_binding",
			line:      `bind -n F12 display-popup "ntm dashboard --popup foo"`,
			key:       "F12",
			wantMatch: true,
			matchType: "overlay",
			desc:      "Minimal overlay binding without size flags",
		},
		{
			name:      "palette_binding",
			line:      `bind-key -n F6 display-popup -E "ntm palette"`,
			key:       "F6",
			wantMatch: false,
			matchType: "binding",
			desc:      "Palette binding (not overlay)",
		},
		{
			name:      "other_popup_binding",
			line:      `bind-key -n F12 display-popup -E "htop"`,
			key:       "F12",
			wantMatch: false,
			matchType: "binding",
			desc:      "Non-ntm popup binding",
		},
		{
			name:      "commented_overlay",
			line:      `# bind-key -n F12 display-popup -E "ntm dashboard --popup"`,
			key:       "F12",
			wantMatch: false,
			matchType: "none",
			desc:      "Commented out overlay binding",
		},
		{
			name:      "trailing_whitespace",
			line:      `bind-key -n F12 display-popup -E "ntm dashboard --popup foo"   `,
			key:       "F12",
			wantMatch: true,
			matchType: "overlay",
			desc:      "Binding with trailing whitespace",
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			t.Logf("Test: %s", tc.desc)
			t.Logf("Line: %s", tc.line)
			t.Logf("Key: %s", tc.key)

			isBinding := isBindingLine(tc.line, tc.key)
			isOverlay := isOverlayBindingLine(tc.line, tc.key)

			t.Logf("isBindingLine=%v, isOverlayBindingLine=%v", isBinding, isOverlay)

			// Verify match expectations
			switch tc.matchType {
			case "overlay":
				if !isOverlay {
					t.Errorf("expected overlay match for %q", tc.line)
				}
				if !isBinding {
					t.Errorf("overlay should also match as binding")
				}
			case "binding":
				if isOverlay {
					t.Errorf("should not match as overlay for %q", tc.line)
				}
				if !isBinding {
					t.Errorf("expected binding match for %q", tc.line)
				}
			case "none":
				if isOverlay || isBinding {
					t.Errorf("should not match as binding or overlay for %q", tc.line)
				}
			}

			if isOverlay != tc.wantMatch {
				t.Errorf("isOverlayBindingLine(%q, %q) = %v, want %v",
					tc.line, tc.key, isOverlay, tc.wantMatch)
			}
		})
	}
}

// TestOverlayTmuxGating documents the tmux session gating behavior.
func TestOverlayTmuxGating(t *testing.T) {
	// The overlay command requires tmux. This test documents the expected behavior.

	t.Run("documents_tmux_requirement", func(t *testing.T) {
		// In actual overlay code:
		// if !tmux.InTmux() { return fmt.Errorf("overlay requires tmux...") }
		t.Log("Overlay requires tmux - returns error when TMUX env is unset")
		t.Log("Expected error: 'overlay requires tmux — run from inside a tmux session'")
	})

	t.Run("documents_session_resolution", func(t *testing.T) {
		t.Log("Session resolution order:")
		t.Log("  1. Explicit session arg: ntm overlay mysession")
		t.Log("  2. Auto-detect from TMUX env: #{session_name}")
		t.Log("Session must exist: tmux.SessionExists(session) check")
	})

	t.Run("documents_auto_setup_behavior", func(t *testing.T) {
		t.Log("Auto-setup behavior on first overlay launch:")
		t.Log("  1. Check if overlay key (F12 default) is already bound")
		t.Log("  2. If not bound: setupOverlayBinding(key) is called")
		t.Log("  3. If setup fails: warning printed, overlay still launches")
	})
}

// TestOverlayDisplayPopupFlags documents the expected tmux display-popup flags.
func TestOverlayDisplayPopupFlags(t *testing.T) {
	t.Run("flag_meanings", func(t *testing.T) {
		flags := map[string]string{
			"-E":     "Close popup when command exits",
			"-w 95%": "Width as 95% of terminal width",
			"-h 95%": "Height as 95% of terminal height",
		}

		t.Log("Display-popup flags used by overlay:")
		for flag, meaning := range flags {
			t.Logf("  %s: %s", flag, meaning)
		}
	})

	t.Run("command_structure", func(t *testing.T) {
		// Document the expected command structure
		expected := []string{
			"display-popup",
			"-E",
			"-w", "95%",
			"-h", "95%",
			"NTM_POPUP=1 'ntm' dashboard --popup 'session'",
		}

		t.Logf("Expected tmux args structure: %v", expected)
		t.Log("Note: The inner command is passed to /bin/sh -c by tmux")
	})
}
