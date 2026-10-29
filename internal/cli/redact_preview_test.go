package cli

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Dicklesworthstone/ntm/internal/config"
)

func TestRedactPreview_DecodedJSONCredentials(t *testing.T) {
	for _, tc := range jsonEscapedCredentialFixtures() {
		for _, format := range []string{"text", "json"} {
			t.Run(tc.name+"/"+format, func(t *testing.T) {
				resetFlags()
				t.Cleanup(resetFlags)
				oldCfg := cfg
				t.Cleanup(func() { cfg = oldCfg })
				cfg = config.Default()
				cfg.Redaction.Mode = "off"
				cfg.Redaction.Allowlist = tc.allowlist
				jsonOutput = format == "json"
				cmd := newRedactPreviewCmd()
				cmd.SetArgs([]string{"--text", tc.content})
				out, err := captureStdout(t, cmd.Execute)
				if err != nil {
					t.Fatalf("preview failed: %v", err)
				}
				for _, fragment := range tc.hidden {
					if strings.Contains(out, fragment) {
						t.Error("preview exposed a secret fragment")
					}
				}
				if !strings.Contains(out, "public-neighbor") {
					t.Error("preview removed the neighboring public field")
				}
				if tc.visible != "" && !strings.Contains(out, tc.visible) {
					t.Error("preview did not preserve the explicitly allowlisted assignment")
				}
				if format == "json" {
					var response RedactPreviewResponse
					if err := json.Unmarshal([]byte(out), &response); err != nil {
						t.Fatal(err)
					}
					if len(response.Findings) != len(tc.lines) {
						t.Fatalf("findings=%d, want %d", len(response.Findings), len(tc.lines))
					}
					for i, finding := range response.Findings {
						if finding.Category != "PASSWORD" || finding.Line != tc.lines[i] {
							t.Errorf("finding %d category=%s line=%d, want PASSWORD on physical line %d", i, finding.Category, finding.Line, tc.lines[i])
						}
						if finding.Start < 0 || finding.End <= finding.Start || finding.End > len(tc.content) {
							t.Errorf("finding %d has invalid source offsets", i)
						}
					}
					if len(tc.lines) == 0 && response.Output != tc.content {
						t.Error("preview changed a decoded noncredential value")
					}
				}
			})
		}
	}
}

func TestRedactPreview_ProviderOverlapValues(t *testing.T) {
	for _, tc := range jsonCredentialOverlapFixtures() {
		for _, format := range []string{"text", "json"} {
			t.Run(tc.name+"/"+format, func(t *testing.T) {
				resetFlags()
				t.Cleanup(resetFlags)
				oldCfg := cfg
				t.Cleanup(func() { cfg = oldCfg })
				cfg = config.Default()
				cfg.Redaction.Mode = "off"
				cfg.Redaction.Allowlist = tc.allowlist
				jsonOutput = format == "json"

				cmd := newRedactPreviewCmd()
				cmd.SetArgs([]string{"--text", tc.content})
				out, err := captureStdout(t, cmd.Execute)
				if err != nil {
					t.Fatalf("preview failed: %v", err)
				}
				for _, fragment := range tc.hidden {
					if strings.Contains(out, fragment) {
						t.Error("preview exposed a secret fragment")
					}
				}
				if !strings.Contains(out, "public-neighbor") {
					t.Error("preview removed the neighboring public field")
				}
				if tc.visible != "" && !strings.Contains(out, tc.visible) {
					t.Error("preview did not preserve the explicitly allowlisted provider")
				}
				for _, category := range tc.categories {
					if !strings.Contains(out, "[REDACTED:"+string(category)+":") {
						t.Errorf("preview lacks a %s placeholder", category)
					}
				}
				if format == "json" {
					var response RedactPreviewResponse
					if err := json.Unmarshal([]byte(out), &response); err != nil {
						t.Fatal(err)
					}
					if len(response.Findings) != len(tc.categories) {
						t.Fatalf("findings=%d, want %d", len(response.Findings), len(tc.categories))
					}
					for i, finding := range response.Findings {
						if finding.Category != tc.categories[i] {
							t.Errorf("finding %d category=%s, want %s", i, finding.Category, tc.categories[i])
						}
					}
				}
			})
		}
	}
}

func TestRedactPreview_QuotedJSONStrings(t *testing.T) {
	const input = "{\"password\":\"preview-head\\\"preview-middle\\\\preview-tail with spaces\",\"note\":\"public-neighbor\",\"password_hint\":\"public-hint\"}\n{\"password\":\"second-preview-value\"}\n"
	for _, line := range strings.Split(strings.TrimSpace(input), "\n") {
		if !json.Valid([]byte(line)) {
			t.Fatal("test fixture must contain valid JSONL")
		}
	}
	for _, format := range []string{"text", "json"} {
		t.Run(format, func(t *testing.T) {
			resetFlags()
			t.Cleanup(resetFlags)
			oldCfg := cfg
			t.Cleanup(func() { cfg = oldCfg })
			cfg = config.Default()
			cfg.Redaction.Mode = "off"
			jsonOutput = format == "json"

			cmd := newRedactPreviewCmd()
			cmd.SetArgs([]string{"--text", input})
			out, err := captureStdout(t, cmd.Execute)
			if err != nil {
				t.Fatalf("preview failed: %v", err)
			}
			for _, fragment := range []string{"preview-head", "preview-middle", "preview-tail", "with spaces", "second-preview-value"} {
				if strings.Contains(out, fragment) {
					t.Error("preview output exposed a secret fragment")
				}
			}
			if !strings.Contains(out, "[REDACTED:PASSWORD:") {
				t.Error("preview lacks a password placeholder")
			}
			for _, public := range []string{"public-neighbor", "public-hint"} {
				if !strings.Contains(out, public) {
					t.Errorf("preview removed noncredential field %q", public)
				}
			}
			if format == "json" {
				var response RedactPreviewResponse
				if err := json.Unmarshal([]byte(out), &response); err != nil {
					t.Fatal(err)
				}
				if len(response.Findings) != 2 {
					t.Fatalf("findings=%d, want 2", len(response.Findings))
				}
				for i, finding := range response.Findings {
					if finding.Category != "PASSWORD" || finding.Line != i+1 {
						t.Errorf("finding %d category=%s line=%d, want PASSWORD on line %d", i, finding.Category, finding.Line, i+1)
					}
				}
				if response.Source != "text" || response.InputLen != len(input) {
					t.Errorf("source=%q input_len=%d", response.Source, response.InputLen)
				}
				if strings.Contains(out, "\"match\"") {
					t.Error("preview JSON includes raw match data")
				}
			} else if !strings.Contains(out, "Findings: 2") {
				t.Error("text preview should report two findings")
			}
		})
	}
}

func TestRedactPreview_CommandJSONOutputIsRedacted(t *testing.T) {
	resetFlags()

	// Avoid reading any user config by isolating XDG config home.
	tmpDir := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", tmpDir)

	// Ensure deterministic behavior regardless of prior tests.
	oldCfg := cfg
	cfg = nil
	t.Cleanup(func() { cfg = oldCfg })

	input := "prefix password=hunter2hunter2 suffix\n"

	out, err := captureStdout(t, func() error {
		rootCmd.SetArgs([]string{"redact", "preview", "--text", input, "--json"})
		return rootCmd.Execute()
	})
	if err != nil {
		t.Fatalf("redact preview failed: %v\noutput:\n%s", err, out)
	}

	if strings.Contains(out, "hunter2hunter2") {
		t.Fatalf("expected JSON output to be redacted, got:\n%s", out)
	}
	if strings.Contains(out, "\"match\"") {
		t.Fatalf("expected JSON output to never include raw matches, got:\n%s", out)
	}

	var resp RedactPreviewResponse
	if err := json.Unmarshal([]byte(out), &resp); err != nil {
		t.Fatalf("failed to parse JSON: %v\noutput:\n%s", err, out)
	}

	if resp.Source != "text" {
		t.Fatalf("expected source=text, got %q", resp.Source)
	}
	if resp.Path != "" {
		t.Fatalf("expected empty path for source=text, got %q", resp.Path)
	}
	if resp.InputLen != len(input) {
		t.Fatalf("expected input_len=%d, got %d", len(input), resp.InputLen)
	}
	if len(resp.Findings) == 0 {
		t.Fatalf("expected at least 1 finding")
	}
	if strings.Contains(resp.Output, "hunter2hunter2") {
		t.Fatalf("expected output field to be redacted, got %q", resp.Output)
	}
	if !strings.Contains(resp.Output, "[REDACTED:PASSWORD:") {
		t.Fatalf("expected password placeholder, got %q", resp.Output)
	}
}

func TestRedactPreview_CommandTextOutputIsRedacted(t *testing.T) {
	resetFlags()

	tmpDir := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", tmpDir)

	oldCfg := cfg
	cfg = nil
	t.Cleanup(func() { cfg = oldCfg })

	out, err := captureStdout(t, func() error {
		rootCmd.SetArgs([]string{"redact", "preview", "--text", "password=hunter2hunter2"})
		return rootCmd.Execute()
	})
	if err != nil {
		t.Fatalf("redact preview failed: %v\noutput:\n%s", err, out)
	}
	if strings.Contains(out, "hunter2hunter2") {
		t.Fatalf("expected text output to be redacted, got:\n%s", out)
	}
	if !strings.Contains(out, "[REDACTED:PASSWORD:") {
		t.Fatalf("expected placeholder in output, got:\n%s", out)
	}
}

func TestRedactPreview_CommandFileInputIsRedacted(t *testing.T) {
	resetFlags()

	tmpDir := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", tmpDir)

	artifactPath := filepath.Join(tmpDir, "artifact.txt")
	if err := os.WriteFile(artifactPath, []byte("password=hunter2hunter2\n"), 0644); err != nil {
		t.Fatalf("WriteFile failed: %v", err)
	}

	oldCfg := cfg
	cfg = nil
	t.Cleanup(func() { cfg = oldCfg })

	out, err := captureStdout(t, func() error {
		rootCmd.SetArgs([]string{"redact", "preview", "--file", artifactPath, "--json"})
		return rootCmd.Execute()
	})
	if err != nil {
		t.Fatalf("redact preview failed: %v\noutput:\n%s", err, out)
	}

	if strings.Contains(out, "hunter2hunter2") {
		t.Fatalf("expected JSON output to be redacted, got:\n%s", out)
	}

	var resp RedactPreviewResponse
	if err := json.Unmarshal([]byte(out), &resp); err != nil {
		t.Fatalf("failed to parse JSON: %v\noutput:\n%s", err, out)
	}

	if resp.Source != "file" {
		t.Fatalf("expected source=file, got %q", resp.Source)
	}
	abs, err := filepath.Abs(artifactPath)
	if err != nil {
		t.Fatalf("Abs failed: %v", err)
	}
	if resp.Path != abs {
		t.Fatalf("expected path %q, got %q", abs, resp.Path)
	}
	if strings.Contains(resp.Output, "hunter2hunter2") {
		t.Fatalf("expected output field to be redacted, got %q", resp.Output)
	}
}

func TestRedactPreview_RequiresTextOrFile(t *testing.T) {
	resetFlags()

	tmpDir := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", tmpDir)

	oldCfg := cfg
	cfg = nil
	t.Cleanup(func() { cfg = oldCfg })

	_, err := captureStdout(t, func() error {
		rootCmd.SetArgs([]string{"redact", "preview", "--json"})
		return rootCmd.Execute()
	})
	if err == nil {
		t.Fatalf("expected error when neither --text nor --file is provided")
	}
}

func TestRedactPreview_TextAndFileMutuallyExclusive(t *testing.T) {
	resetFlags()

	tmpDir := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", tmpDir)

	artifactPath := filepath.Join(tmpDir, "artifact.txt")
	if err := os.WriteFile(artifactPath, []byte("password=hunter2hunter2\n"), 0644); err != nil {
		t.Fatalf("WriteFile failed: %v", err)
	}

	oldCfg := cfg
	cfg = nil
	t.Cleanup(func() { cfg = oldCfg })

	_, err := captureStdout(t, func() error {
		rootCmd.SetArgs([]string{"redact", "preview", "--text", "hi", "--file", artifactPath, "--json"})
		return rootCmd.Execute()
	})
	if err == nil {
		t.Fatalf("expected error when both --text and --file are provided")
	}
}
