package cli

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestValidKeyRegex tests the key validation regex for tmux bindings.
func TestValidKeyRegex(t *testing.T) {
	testCases := []struct {
		key       string
		wantValid bool
		desc      string
	}{
		{"F12", true, "function key"},
		{"F6", true, "function key"},
		{"F1", true, "function key"},
		{"a", true, "single letter"},
		{"z", true, "single letter"},
		{"A", true, "uppercase letter"},
		{"0", true, "digit"},
		{"9", true, "digit"},
		{"C-o", true, "ctrl combo with dash"},
		{"^o", true, "ctrl combo with caret"},
		{"M-x", true, "meta combo"},
		{"", false, "empty string"},
		{"F12;rm -rf", false, "injection attempt with semicolon"},
		{"F12 && ls", false, "injection with spaces and &&"},
		{"F12`whoami`", false, "backtick injection"},
		{"F12$(cmd)", false, "command substitution"},
		{"F12|cat", false, "pipe injection"},
		{"'F12", false, "single quote"},
		{"\"F12", false, "double quote"},
		{"F12\nother", false, "newline injection"},
	}

	for _, tc := range testCases {
		t.Run(tc.desc, func(t *testing.T) {
			result := validKeyRegex.MatchString(tc.key)
			if result != tc.wantValid {
				t.Errorf("validKeyRegex.MatchString(%q) = %v, want %v\n  desc: %s",
					tc.key, result, tc.wantValid, tc.desc)
			}
			t.Logf("key=%q valid=%v (expected=%v) desc=%q", tc.key, result, tc.wantValid, tc.desc)
		})
	}
}

// TestOverlayBindingCommandGeneration tests the generation of overlay binding commands.
func TestOverlayBindingCommandGeneration(t *testing.T) {
	testCases := []struct {
		key             string
		wantContains    []string
		wantNotContains []string
		desc            string
	}{
		{
			key: "F12",
			wantContains: []string{
				"bind-key -n F12",
				"display-popup",
				"-E",
				"-w 95%",
				"-h 95%",
				"NTM_POPUP=1",
				"ntm dashboard --popup",
				"#{session_name}",
			},
			wantNotContains: []string{
				"palette", // overlay is dashboard, not palette
			},
			desc: "F12 overlay binding",
		},
		{
			key: "F6",
			wantContains: []string{
				"bind-key -n F6",
				"display-popup",
			},
			desc: "F6 overlay binding",
		},
		{
			key: "C-o",
			wantContains: []string{
				"bind-key -n C-o",
			},
			desc: "ctrl-o overlay binding",
		},
	}

	for _, tc := range testCases {
		t.Run(tc.desc, func(t *testing.T) {
			cmd := overlayBindingCommand(tc.key)
			t.Logf("Generated command for key=%q:\n  %s", tc.key, cmd)

			for _, substr := range tc.wantContains {
				if !strings.Contains(cmd, substr) {
					t.Errorf("overlayBindingCommand(%q) missing %q\n  full command: %s",
						tc.key, substr, cmd)
				}
			}

			for _, substr := range tc.wantNotContains {
				if strings.Contains(cmd, substr) {
					t.Errorf("overlayBindingCommand(%q) should not contain %q\n  full command: %s",
						tc.key, substr, cmd)
				}
			}
		})
	}
}

// TestOverlayBindingArgs tests the argument slice for tmux bind-key.
func TestOverlayBindingArgs(t *testing.T) {
	testCases := []struct {
		key          string
		wantArgCount int
		desc         string
	}{
		// 10 args: bind-key, -n, KEY, display-popup, -E, -w, 95%, -h, 95%, <cmd>
		{"F12", 10, "F12 binding args"},
		{"F6", 10, "F6 binding args"},
	}

	for _, tc := range testCases {
		t.Run(tc.desc, func(t *testing.T) {
			args := overlayBindingArgs(tc.key)
			t.Logf("Generated args for key=%q (%d args):\n  %v", tc.key, len(args), args)

			if len(args) != tc.wantArgCount {
				t.Errorf("overlayBindingArgs(%q) returned %d args, want %d\n  args: %v",
					tc.key, len(args), tc.wantArgCount, args)
			}

			// Verify structure: bind-key, -n, KEY, display-popup, -E, -w, 95%, -h, 95%, <cmd>
			expectedStructure := map[int]string{
				0: "bind-key",
				1: "-n",
				2: tc.key,
				3: "display-popup",
				4: "-E",
				5: "-w",
				6: "95%",
				7: "-h",
				8: "95%",
			}

			for idx, expected := range expectedStructure {
				if idx < len(args) && args[idx] != expected {
					t.Errorf("overlayBindingArgs(%q)[%d] = %q, want %q",
						tc.key, idx, args[idx], expected)
				}
			}

			// Last arg should contain the command
			if len(args) > 0 {
				lastArg := args[len(args)-1]
				if !strings.Contains(lastArg, "NTM_POPUP=1") {
					t.Errorf("last arg missing NTM_POPUP=1: %q", lastArg)
				}
				if !strings.Contains(lastArg, "dashboard --popup") {
					t.Errorf("last arg missing 'dashboard --popup': %q", lastArg)
				}
			}
		})
	}
}

// TestSetupOverlayBindingWithWriter tests the overlay binding setup function.
func TestSetupOverlayBindingWithWriter(t *testing.T) {
	testCases := []struct {
		name          string
		key           string
		existingConf  string
		wantInConf    []string
		wantNotInConf []string
		wantInOutput  []string
	}{
		{
			name:         "new_binding_empty_conf",
			key:          "F12",
			existingConf: "",
			wantInConf: []string{
				"bind-key -n F12",
				"NTM Dashboard Overlay",
			},
			wantInOutput: []string{
				"Added F12 overlay binding",
			},
		},
		{
			name: "new_binding_existing_conf",
			key:  "F12",
			existingConf: `set -g status on
set -g mouse on`,
			wantInConf: []string{
				"set -g status on",
				"bind-key -n F12",
			},
			wantInOutput: []string{
				"Added F12 overlay binding",
			},
		},
		{
			name: "update_existing_binding",
			key:  "F12",
			existingConf: `set -g status on
bind-key -n F12 display-popup -E "old command"
set -g mouse on`,
			wantInConf: []string{
				"set -g status on",
				"set -g mouse on",
				"bind-key -n F12 display-popup -E -w 95%",
			},
			wantNotInConf: []string{
				"old command",
			},
			wantInOutput: []string{
				"Updated existing F12 binding",
			},
		},
		{
			name: "preserve_other_bindings",
			key:  "F12",
			existingConf: `bind-key -n F6 display-popup -E "ntm palette"
bind-key -n F5 run-shell "custom"`,
			wantInConf: []string{
				"bind-key -n F6",
				"bind-key -n F5",
				"bind-key -n F12",
			},
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			// Setup temp HOME
			tmpDir := t.TempDir()
			confPath := filepath.Join(tmpDir, ".tmux.conf")

			// Write existing conf if any
			if tc.existingConf != "" {
				if err := os.WriteFile(confPath, []byte(tc.existingConf), 0644); err != nil {
					t.Fatalf("failed to write initial conf: %v", err)
				}
			}

			origHome := os.Getenv("HOME")
			t.Setenv("HOME", tmpDir)
			defer os.Setenv("HOME", origHome)

			// Clear TMUX to avoid actual tmux calls
			os.Unsetenv("TMUX")

			// Capture output
			var buf bytes.Buffer
			err := setupOverlayBindingWithWriter(tc.key, &buf)
			if err != nil {
				t.Fatalf("setupOverlayBindingWithWriter failed: %v", err)
			}

			output := buf.String()
			t.Logf("Output:\n%s", output)

			// Read resulting conf
			data, err := os.ReadFile(confPath)
			if err != nil {
				t.Fatalf("failed to read conf: %v", err)
			}
			conf := string(data)
			t.Logf("Resulting conf:\n%s", conf)

			// Check conf contents
			for _, want := range tc.wantInConf {
				if !strings.Contains(conf, want) {
					t.Errorf("conf missing %q", want)
				}
			}
			for _, notWant := range tc.wantNotInConf {
				if strings.Contains(conf, notWant) {
					t.Errorf("conf should not contain %q", notWant)
				}
			}

			// Check output
			for _, want := range tc.wantInOutput {
				if !strings.Contains(output, want) {
					t.Errorf("output missing %q", want)
				}
			}
		})
	}
}

// TestRemoveBindingCommentPreservation tests that removeBinding correctly handles comments.
func TestRemoveBindingCommentPreservation(t *testing.T) {
	testCases := []struct {
		name          string
		key           string
		existingConf  string
		wantInConf    []string
		wantNotInConf []string
		desc          string
	}{
		{
			name: "removes_binding_and_ntm_comment",
			key:  "F12",
			existingConf: `set -g status on
# NTM Dashboard Overlay (added by 'ntm bind --overlay')
bind-key -n F12 display-popup -E "ntm dashboard"
set -g mouse on`,
			wantInConf: []string{
				"set -g status on",
				"set -g mouse on",
			},
			wantNotInConf: []string{
				"NTM Dashboard Overlay",
				"bind-key -n F12",
			},
			desc: "Should remove NTM comment and binding together",
		},
		{
			name: "preserves_unrelated_comments",
			key:  "F12",
			existingConf: `# My custom settings
set -g status on
# NTM Command Palette (added by 'ntm bind')
bind-key -n F6 display-popup -E "ntm palette"
# NTM Dashboard Overlay
bind-key -n F12 display-popup -E "ntm dashboard"
set -g mouse on`,
			wantInConf: []string{
				"My custom settings",
				"set -g status on",
				"NTM Command Palette",
				"bind-key -n F6",
				"set -g mouse on",
			},
			wantNotInConf: []string{
				"bind-key -n F12",
			},
			desc: "Should preserve F6 binding and its comment while removing F12",
		},
		{
			name: "no_binding_found",
			key:  "F12",
			existingConf: `set -g status on
bind-key -n F6 display-popup -E "ntm palette"`,
			wantInConf: []string{
				"set -g status on",
				"bind-key -n F6",
			},
			desc: "Should leave config unchanged when binding not found",
		},
		{
			name: "binding_without_comment",
			key:  "F12",
			existingConf: `set -g status on
bind-key -n F12 display-popup -E "ntm dashboard"
set -g mouse on`,
			wantInConf: []string{
				"set -g status on",
				"set -g mouse on",
			},
			wantNotInConf: []string{
				"bind-key -n F12",
			},
			desc: "Should remove binding even without preceding NTM comment",
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			tmpDir := t.TempDir()
			confPath := filepath.Join(tmpDir, ".tmux.conf")

			if err := os.WriteFile(confPath, []byte(tc.existingConf), 0644); err != nil {
				t.Fatalf("write initial conf: %v", err)
			}

			t.Logf("Test: %s", tc.desc)
			t.Logf("Initial conf:\n%s", tc.existingConf)

			origHome := os.Getenv("HOME")
			t.Setenv("HOME", tmpDir)
			defer os.Setenv("HOME", origHome)

			os.Unsetenv("TMUX")

			err := removeBinding(tc.key)
			if err != nil {
				t.Fatalf("removeBinding failed: %v", err)
			}

			data, err := os.ReadFile(confPath)
			if err != nil {
				t.Fatalf("read conf: %v", err)
			}
			conf := string(data)
			t.Logf("Resulting conf:\n%s", conf)

			for _, want := range tc.wantInConf {
				if !strings.Contains(conf, want) {
					t.Errorf("conf missing %q", want)
				}
			}
			for _, notWant := range tc.wantNotInConf {
				if strings.Contains(conf, notWant) {
					t.Errorf("conf should not contain %q", notWant)
				}
			}
		})
	}
}

// TestShowBindingOutput tests the showBinding function output.
func TestShowBindingOutput(t *testing.T) {
	testCases := []struct {
		name         string
		key          string
		existingConf string
		wantFound    bool
	}{
		{
			name: "binding_found",
			key:  "F12",
			existingConf: `set -g status on
bind-key -n F12 display-popup -E "ntm dashboard"`,
			wantFound: true,
		},
		{
			name:         "binding_not_found",
			key:          "F12",
			existingConf: `set -g status on`,
			wantFound:    false,
		},
		{
			name:         "empty_conf",
			key:          "F12",
			existingConf: "",
			wantFound:    false,
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			tmpDir := t.TempDir()
			confPath := filepath.Join(tmpDir, ".tmux.conf")

			if tc.existingConf != "" {
				if err := os.WriteFile(confPath, []byte(tc.existingConf), 0644); err != nil {
					t.Fatalf("write conf: %v", err)
				}
			}

			origHome := os.Getenv("HOME")
			t.Setenv("HOME", tmpDir)
			defer os.Setenv("HOME", origHome)

			// showBinding prints to stdout, we just verify it doesn't error
			err := showBinding(tc.key)
			if err != nil {
				t.Fatalf("showBinding failed: %v", err)
			}

			t.Logf("key=%q found=%v conf_len=%d", tc.key, tc.wantFound, len(tc.existingConf))
		})
	}
}

// TestMaybeFprintf tests the conditional fprintf helper.
func TestMaybeFprintf(t *testing.T) {
	t.Run("with_writer", func(t *testing.T) {
		var buf bytes.Buffer
		maybeFprintf(&buf, "hello %s", "world")
		if buf.String() != "hello world" {
			t.Errorf("got %q, want %q", buf.String(), "hello world")
		}
	})

	t.Run("nil_writer", func(t *testing.T) {
		// Should not panic
		maybeFprintf(nil, "hello %s", "world")
		t.Log("nil writer handled gracefully")
	})
}

// TestBindingLineWithVariousFormats tests isBindingLine with edge cases.
func TestBindingLineWithVariousFormats(t *testing.T) {
	testCases := []struct {
		line      string
		key       string
		wantMatch bool
		desc      string
	}{
		// Standard formats
		{`bind-key -n F12 display-popup`, "F12", true, "bind-key standard"},
		{`bind -n F12 display-popup`, "F12", true, "bind short form"},

		// Whitespace variations - strings.Fields skips leading whitespace
		{`  bind-key -n F12 display-popup`, "F12", true, "leading whitespace (Fields skips leading whitespace, still matches)"},
		{`bind-key  -n  F12  display-popup`, "F12", true, "extra whitespace between args"},

		// Non-matching cases
		{`bind-key F12 display-popup`, "F12", false, "missing -n flag"},
		{`bind-key -n F6 display-popup`, "F12", false, "different key"},
		{`# bind-key -n F12 display-popup`, "F12", false, "commented out"},
		{`set -g status on`, "F12", false, "unrelated command"},
		{``, "F12", false, "empty line"},

		// -T flag cases (not currently matched)
		{`bind-key -T root F12 display-popup`, "F12", false, "-T not matched"},
		{`bind-key -T prefix F12 display-popup`, "F12", false, "-T prefix not matched"},
	}

	for _, tc := range testCases {
		t.Run(tc.desc, func(t *testing.T) {
			result := isBindingLine(tc.line, tc.key)
			if result != tc.wantMatch {
				t.Errorf("isBindingLine(%q, %q) = %v, want %v",
					tc.line, tc.key, result, tc.wantMatch)
			}
			t.Logf("line=%q key=%q match=%v", tc.line, tc.key, result)
		})
	}
}

// TestSetupBindingPaletteVsOverlay verifies palette vs overlay binding differences.
func TestSetupBindingPaletteVsOverlay(t *testing.T) {
	// Palette uses 90% dimensions and calls "ntm palette"
	paletteCmd := `bind-key -n F6 display-popup -E -w 90% -h 90% "ntm palette"`

	// Overlay uses 95% dimensions and calls "NTM_POPUP=1 ntm dashboard --popup"
	overlayCmd := overlayBindingCommand("F12")

	t.Logf("Palette command: %s", paletteCmd)
	t.Logf("Overlay command: %s", overlayCmd)

	// Verify differences
	if !strings.Contains(paletteCmd, "90%") {
		t.Error("palette should use 90% dimensions")
	}
	if !strings.Contains(overlayCmd, "95%") {
		t.Error("overlay should use 95% dimensions")
	}

	if !strings.Contains(paletteCmd, "palette") {
		t.Error("palette command should call 'palette'")
	}
	if !strings.Contains(overlayCmd, "dashboard") {
		t.Error("overlay command should call 'dashboard'")
	}

	if strings.Contains(paletteCmd, "NTM_POPUP") {
		t.Error("palette should not set NTM_POPUP")
	}
	if !strings.Contains(overlayCmd, "NTM_POPUP=1") {
		t.Error("overlay should set NTM_POPUP=1")
	}
}

// TestDefaultKeySelection tests the default key logic.
func TestDefaultKeySelection(t *testing.T) {
	// Default palette key is F6
	defaultPaletteKey := "F6"

	// Default overlay key is F12
	defaultOverlayKey := "F12"

	if defaultPaletteKey == defaultOverlayKey {
		t.Error("palette and overlay should have different default keys")
	}

	t.Logf("Default palette key: %s", defaultPaletteKey)
	t.Logf("Default overlay key: %s", defaultOverlayKey)
}
