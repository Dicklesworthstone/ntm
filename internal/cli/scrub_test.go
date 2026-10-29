package cli

import (
	"encoding/json"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/spf13/cobra"

	"github.com/Dicklesworthstone/ntm/internal/redaction"
	"github.com/Dicklesworthstone/ntm/internal/startup"
)

type jsonCredentialOverlapFixture struct {
	name       string
	content    string
	categories []redaction.Category
	hidden     []string
	allowlist  []string
	visible    string
}

func jsonCredentialOverlapFixtures() []jsonCredentialOverlapFixture {
	provider := "gh" + "p_" + strings.Repeat("q", 40)
	escaped := "gh" + "p_" + strings.Repeat("q", 31) + "\\u0071SECRETL8"
	composite := "{\"password\":\"left-sensitive " + provider + " right-sensitive\",\"note\":\"public-neighbor\"}"
	exact := "{\"password\":\"" + provider + "\",\"note\":\"public-neighbor\"}"
	return []jsonCredentialOverlapFixture{
		{
			name:       "escaped provider suffix",
			content:    "{\"token\":\"" + escaped + "\",\"note\":\"public-neighbor\"}",
			categories: []redaction.Category{redaction.CategoryGitHubToken, redaction.CategoryGenericSecret},
			hidden:     []string{strings.Repeat("q", 31), "SECRETL8"},
		},
		{
			name:       "password around provider",
			content:    composite,
			categories: []redaction.Category{redaction.CategoryPassword, redaction.CategoryGitHubToken, redaction.CategoryPassword},
			hidden:     []string{"left-sensitive", "right-sensitive", provider},
		},
		{
			name:       "allowlisted provider inside password",
			content:    composite,
			categories: []redaction.Category{redaction.CategoryPassword, redaction.CategoryPassword},
			hidden:     []string{"left-sensitive", "right-sensitive"},
			allowlist:  []string{"^" + regexp.QuoteMeta(provider) + "$"},
			visible:    provider,
		},
		{
			name:       "exact provider value",
			content:    exact,
			categories: []redaction.Category{redaction.CategoryGitHubToken},
			hidden:     []string{provider},
		},
		{
			name:      "allowlisted exact provider value",
			content:   exact,
			allowlist: []string{"^" + regexp.QuoteMeta(provider) + "$"},
			visible:   provider,
		},
	}
}

func TestScrub_ProviderOverlapPreviews(t *testing.T) {
	for _, tc := range jsonCredentialOverlapFixtures() {
		t.Run(tc.name, func(t *testing.T) {
			if !json.Valid([]byte(tc.content)) {
				t.Fatal("fixture must be valid JSON")
			}
			path := filepath.Join(t.TempDir(), "artifact.json")
			if err := os.WriteFile(path, []byte(tc.content), 0o600); err != nil {
				t.Fatal(err)
			}
			res := runScrub([]string{path}, nil, redaction.Config{Allowlist: tc.allowlist})
			if res.FilesScanned != 1 || len(res.Warnings) != 0 {
				t.Fatalf("files=%d warnings=%v, want one file without warnings", res.FilesScanned, res.Warnings)
			}
			if len(res.Findings) != len(tc.categories) {
				t.Fatalf("findings=%d, want %d", len(res.Findings), len(tc.categories))
			}
			for i, finding := range res.Findings {
				if finding.Category != tc.categories[i] {
					t.Errorf("finding %d category=%s, want %s", i, finding.Category, tc.categories[i])
				}
				for _, fragment := range tc.hidden {
					if strings.Contains(finding.Preview, fragment) {
						t.Errorf("finding %d preview exposed a secret fragment", i)
					}
				}
			}
			encoded, err := json.Marshal(res)
			if err != nil {
				t.Fatal(err)
			}
			textOutput, err := captureStdout(t, func() error {
				printScrubText(res)
				return nil
			})
			if err != nil {
				t.Fatal(err)
			}
			for _, output := range []string{string(encoded), textOutput} {
				for _, fragment := range tc.hidden {
					if strings.Contains(output, fragment) {
						t.Error("scrub output exposed a secret fragment")
					}
				}
				if len(tc.categories) > 0 && !strings.Contains(output, "public-neighbor") {
					t.Error("scrub previews removed the neighboring public field")
				}
			}
		})
	}
}

func TestScrub_QuotedCredentialsAndSafePreviews(t *testing.T) {
	left := strings.Repeat("left-sensitive-fragment-", 8)
	right := strings.Repeat("right-sensitive-fragment-", 8)
	cases := []struct {
		name      string
		content   string
		lines     []int
		hidden    []string
		allowlist []string
	}{
		{
			name:    "JSON",
			content: "{\"password\":\"synthetic-never-valid-json-value\",\"note\":\"public-neighbor\"}",
			lines:   []int{1},
			hidden:  []string{"synthetic-never-valid-json-value"},
		},
		{
			name:    "JSONL escaped strings",
			content: "{\"password\":\"jsonl-first-sensitive\"}\n{\"passwd\":\"head-sensitive\\\"quote-sensitive\\\\slash-sensitive with spaces\",\"note\":\"public-neighbor\"}\n",
			lines:   []int{1, 2},
			hidden:  []string{"jsonl-first-sensitive", "head-sensitive", "quote-sensitive", "slash-sensitive", "with spaces"},
		},
		{
			name:    "ordinary assignments",
			content: "PASSWORD=synthetic-never-valid-control-value\npasswd=second-control-value\n",
			lines:   []int{1, 2},
			hidden:  []string{"synthetic-never-valid-control-value", "second-control-value"},
		},
		{
			name:    "adjacent long values",
			content: "{\"password\":\"" + left + "\",\"passwd\":\"" + right + "\",\"note\":\"public-neighbor\"}",
			lines:   []int{1, 1},
			hidden:  []string{"left-sensitive-fragment", "right-sensitive-fragment"},
		},
		{
			name:      "allowlisted JSON neighbor",
			content:   "{\"password\":\"allowlisted-never-valid\"}\n{\"password\":\"blocked-never-valid\"}\n",
			lines:     []int{2},
			hidden:    []string{"blocked-never-valid"},
			allowlist: []string{"allowlisted-never-valid"},
		},
		{
			name:    "placeholders and noncredential fields",
			content: "{\"password\":\"<redacted>\",\"pwd\":\"/home/user/worktree\",\"password_hint\":\"public-hint-value\",\"token_count\":\"1234567890123456\",\"note\":\"public-neighbor\"}",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "artifact.log")
			if err := os.WriteFile(path, []byte(tc.content), 0o600); err != nil {
				t.Fatal(err)
			}
			scanCfg := redaction.DefaultConfig()
			scanCfg.Allowlist = tc.allowlist
			res := runScrub([]string{path}, nil, scanCfg)
			if res.FilesScanned != 1 || len(res.Warnings) != 0 {
				t.Fatalf("files=%d warnings=%v, want one file without warnings", res.FilesScanned, res.Warnings)
			}
			if len(res.Findings) != len(tc.lines) {
				t.Fatalf("findings=%d, want %d", len(res.Findings), len(tc.lines))
			}
			for i, finding := range res.Findings {
				if finding.Category != redaction.CategoryPassword || finding.Line != tc.lines[i] {
					t.Errorf("finding %d category=%s line=%d, want PASSWORD on line %d", i, finding.Category, finding.Line, tc.lines[i])
				}
				if finding.Start < 0 || finding.End <= finding.Start || finding.End > len(tc.content) || finding.Column < 1 {
					t.Errorf("invalid finding offsets: %+v", finding)
				}
				if !strings.Contains(finding.Preview, "[REDACTED:PASSWORD:") {
					t.Errorf("finding %d preview lacks a password placeholder", i)
				}
				for _, fragment := range tc.hidden {
					if strings.Contains(finding.Preview, fragment) {
						t.Errorf("finding %d preview exposed a secret fragment", i)
					}
				}
			}
			encoded, err := json.Marshal(res)
			if err != nil {
				t.Fatal(err)
			}
			textOutput, err := captureStdout(t, func() error {
				printScrubText(res)
				return nil
			})
			if err != nil {
				t.Fatal(err)
			}
			for _, output := range []string{string(encoded), textOutput} {
				for _, fragment := range tc.hidden {
					if strings.Contains(output, fragment) {
						t.Error("scrub output exposed a secret fragment")
					}
				}
			}
			after, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			if string(after) != tc.content {
				t.Error("read-only scrub modified the source artifact")
			}
		})
	}
}

func TestRedactionAllowlistEnvironmentCommands(t *testing.T) {
	const fixture = "ANTHROPIC_API_KEY=ecs2a-bogus-not-a-real-key\nCLAUDE_CODE_OAUTH_TOKEN=fixture-never-log-this\npassword=unlisted-fixture-secret\n"
	const allowlist = "^ANTHROPIC_API_KEY=ecs2a-bogus-not-a-real-key$,^TOKEN=fixture-never-log-this$"
	for _, command := range []string{"scrub", "preview"} {
		for _, source := range []string{"none", "TOML", "environment", "empty override"} {
			t.Run(command+"/"+source, func(t *testing.T) {
				resetFlags()
				t.Cleanup(resetFlags)
				startup.ResetConfig()
				t.Cleanup(startup.ResetConfig)
				dir := t.TempDir()
				t.Chdir(dir)
				t.Setenv("HOME", dir)
				t.Setenv("XDG_CONFIG_HOME", dir)
				t.Setenv("NTM_CONFIG", "")
				t.Setenv("NTM_REDACTION_ALLOWLIST", "")
				if err := os.Unsetenv("NTM_REDACTION_ALLOWLIST"); err != nil {
					t.Fatal(err)
				}
				artifact := filepath.Join(dir, "fixtures.txt")
				if err := os.WriteFile(artifact, []byte(fixture), 0600); err != nil {
					t.Fatal(err)
				}
				configPath := filepath.Join(dir, "scrub.toml")
				settings := "[redaction]\nmode = 'redact'\n"
				wantFindings := 3
				switch source {
				case "TOML", "empty override":
					settings += "allowlist = ['^ANTHROPIC_API_KEY=ecs2a-bogus-not-a-real-key$', '^TOKEN=fixture-never-log-this$']\n"
					wantFindings = 1
					if source == "empty override" {
						t.Setenv("NTM_REDACTION_ALLOWLIST", "")
						wantFindings = 3
					}
				case "environment":
					t.Setenv("NTM_REDACTION_ALLOWLIST", allowlist)
					wantFindings = 1
				}
				if err := os.WriteFile(configPath, []byte(settings), 0600); err != nil {
					t.Fatal(err)
				}
				oldCfg, oldCfgFile := cfg, cfgFile
				cfg = nil
				t.Cleanup(func() { cfg, cfgFile = oldCfg, oldCfgFile })
				// Each case models a fresh process, including fresh command-local
				// slice flags and the production configuration-loading hook.
				commandRoot := &cobra.Command{Use: "ntm", PersistentPreRunE: rootCmd.PersistentPreRunE}
				commandRoot.PersistentFlags().StringVar(&cfgFile, "config", "", "config file")
				commandRoot.PersistentFlags().BoolVar(&jsonOutput, "json", false, "JSON output")
				commandRoot.AddCommand(newScrubCmd(), newRedactCmd())
				args := []string{"--config", configPath}
				if command == "scrub" {
					args = append(args, "scrub", "--path", artifact, "--format", "json")
				} else {
					args = append(args, "redact", "preview", "--file", artifact, "--json")
				}
				out, err := captureStdout(t, func() error {
					commandRoot.SetArgs(args)
					return commandRoot.Execute()
				})
				if err != nil {
					t.Fatalf("command failed: %v\n%s", err, out)
				}
				var response struct {
					Findings []json.RawMessage `json:"findings"`
				}
				if err := json.Unmarshal([]byte(out), &response); err != nil {
					t.Fatalf("decode response: %v\n%s", err, out)
				}
				if len(response.Findings) != wantFindings {
					t.Errorf("findings = %d, want %d\n%s", len(response.Findings), wantFindings, out)
				}
				if strings.Contains(out, "unlisted-fixture-secret") {
					t.Fatal("environment allowlist exposed an unrelated secret")
				}
			})
		}
	}
}

func TestScrub_CommandJSONOutputIsRedacted(t *testing.T) {
	resetFlags()

	// Avoid reading any user config by isolating XDG config home.
	tmpDir := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", tmpDir)

	artifactPath := filepath.Join(tmpDir, "artifact.log")
	if err := os.WriteFile(artifactPath, []byte("prefix password=hunter2hunter2 suffix\n"), 0644); err != nil {
		t.Fatalf("WriteFile failed: %v", err)
	}

	// Ensure this test is deterministic regardless of prior tests.
	oldCfg := cfg
	cfg = nil
	t.Cleanup(func() { cfg = oldCfg })

	out, err := captureStdout(t, func() error {
		rootCmd.SetArgs([]string{"scrub", "--path", artifactPath, "--format", "json"})
		return rootCmd.Execute()
	})
	if err != nil {
		t.Fatalf("scrub command failed: %v\noutput:\n%s", err, out)
	}

	var res scrubResult
	if err := json.Unmarshal([]byte(out), &res); err != nil {
		t.Fatalf("failed to parse scrub JSON: %v\noutput:\n%s", err, out)
	}

	if len(res.Roots) != 1 {
		t.Fatalf("expected 1 root, got %d (%v)", len(res.Roots), res.Roots)
	}
	if res.Roots[0] != artifactPath {
		t.Fatalf("expected root %q, got %q", artifactPath, res.Roots[0])
	}
	if res.FilesScanned != 1 {
		t.Fatalf("expected FilesScanned=1, got %d", res.FilesScanned)
	}
	if len(res.Findings) != 1 {
		t.Fatalf("expected 1 finding, got %d (%v)", len(res.Findings), res.Findings)
	}

	f := res.Findings[0]
	if f.Path != artifactPath {
		t.Fatalf("expected finding path %q, got %q", artifactPath, f.Path)
	}
	if f.Category == "" {
		t.Fatalf("expected non-empty category")
	}
	if f.Start <= 0 || f.End <= f.Start {
		t.Fatalf("expected valid start/end offsets, got start=%d end=%d", f.Start, f.End)
	}
	if f.Line == 0 || f.Column == 0 {
		t.Fatalf("expected line/column to be populated, got line=%d col=%d", f.Line, f.Column)
	}
	if strings.Contains(f.Preview, "hunter2hunter2") {
		t.Fatalf("expected preview to be redacted, got %q", f.Preview)
	}
	if !strings.Contains(f.Preview, "[REDACTED:PASSWORD:") {
		t.Fatalf("expected password redaction placeholder, got %q", f.Preview)
	}
}

func TestScrub_CommandSinceSkipsOldFiles(t *testing.T) {
	resetFlags()

	tmpDir := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", tmpDir)

	artifactPath := filepath.Join(tmpDir, "old.log")
	if err := os.WriteFile(artifactPath, []byte("password=hunter2hunter2\n"), 0644); err != nil {
		t.Fatalf("WriteFile failed: %v", err)
	}

	oldTime := time.Now().Add(-2 * time.Hour)
	if err := os.Chtimes(artifactPath, oldTime, oldTime); err != nil {
		t.Fatalf("Chtimes failed: %v", err)
	}

	oldCfg := cfg
	cfg = nil
	t.Cleanup(func() { cfg = oldCfg })

	out, err := captureStdout(t, func() error {
		rootCmd.SetArgs([]string{"scrub", "--path", artifactPath, "--since", "1h", "--format", "json"})
		return rootCmd.Execute()
	})
	if err != nil {
		t.Fatalf("scrub command failed: %v\noutput:\n%s", err, out)
	}

	var res scrubResult
	if err := json.Unmarshal([]byte(out), &res); err != nil {
		t.Fatalf("failed to parse scrub JSON: %v\noutput:\n%s", err, out)
	}
	if res.FilesScanned != 0 {
		t.Fatalf("expected FilesScanned=0, got %d", res.FilesScanned)
	}
	if len(res.Findings) != 0 {
		t.Fatalf("expected no findings, got %d", len(res.Findings))
	}
	if res.Findings == nil {
		t.Fatal("expected empty findings array, got null")
	}
}

func TestResolveScrubRootsUsesSelectedConfigDir(t *testing.T) {
	oldCfgFile := cfgFile
	cfgFile = ""
	t.Cleanup(func() {
		cfgFile = oldCfgFile
	})

	base := t.TempDir()
	t.Setenv("HOME", base)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(base, "xdg"))

	customPath := filepath.Join(base, "custom", "ntm.toml")
	customDir := filepath.Dir(customPath)
	if err := os.MkdirAll(customDir, 0o755); err != nil {
		t.Fatalf("MkdirAll(customDir) failed: %v", err)
	}
	cfgFile = customPath

	roots, err := resolveScrubRoots(nil)
	if err != nil {
		t.Fatalf("resolveScrubRoots(nil) failed: %v", err)
	}
	if len(roots) != 1 {
		t.Fatalf("expected 1 scrub root, got %d (%v)", len(roots), roots)
	}
	if roots[0] != customDir {
		t.Fatalf("scrub root = %q, want %q", roots[0], customDir)
	}
}

func TestScrubPreviewZeroWidthPatternsDoNotExposeCredentials(t *testing.T) {
	tests := []struct {
		name     string
		input    string
		pattern  string
		fragment string
	}{
		{
			name:     "same start as assignment",
			input:    "password=unmistakablesensitivevalue",
			pattern:  `^`,
			fragment: "unmistakablesensitivevalue",
		},
		{
			name:     "inside long JSON value",
			input:    `{"password":"` + strings.Repeat("clippedsecret", 12) + `"}`,
			pattern:  `\b`,
			fragment: "clippedsecret",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			findings := redaction.Scan(tt.input, redaction.Config{
				ExtraPatterns: map[redaction.Category][]string{
					"CUSTOM_ANCHOR": {tt.pattern},
				},
			})
			passwords, anchors := 0, 0
			for _, finding := range findings {
				if finding.Category == redaction.CategoryPassword {
					passwords++
				}
				if finding.Start == finding.End {
					anchors++
				}
			}
			if passwords != 1 || anchors == 0 {
				t.Fatalf("password findings=%d, zero-width findings=%d; want one password and at least one anchor", passwords, anchors)
			}
			for i, finding := range findings {
				preview := scrubPreview(tt.input, finding, findings)
				if strings.Contains(preview, tt.fragment) {
					t.Errorf("preview %d exposed a secret around a zero-width custom finding", i)
				}
			}
		})
	}
}

func TestScrubPreviewClippedProviderToken(t *testing.T) {
	token := "gh" + "p_" + strings.Repeat("q", 90)
	password := strings.Repeat("rightsecret", 8)
	input := `{"token":"` + token + `","password":"` + password + `"}`
	findings := redaction.Scan(input, redaction.Config{})
	if len(findings) != 2 || findings[0].Category != redaction.CategoryGitHubToken || findings[1].Category != redaction.CategoryPassword {
		t.Fatalf("expected a GitHub token and a password, got %d findings", len(findings))
	}
	for i, finding := range findings {
		preview := scrubPreview(input, finding, findings)
		for _, fragment := range []string{strings.Repeat("q", 12), "rightsecret"} {
			if strings.Contains(preview, fragment) {
				t.Errorf("preview %d exposed part of a neighboring credential crossing its context boundary", i)
			}
		}
	}
}
