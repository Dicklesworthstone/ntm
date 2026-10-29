//go:build e2e
// +build e2e

// Package e2e contains end-to-end tests for NTM robot mode commands.
// [E2E-SCRUB] Tests for ntm scrub secret scanning.
package e2e

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

type scrubResult struct {
	Roots        []string       `json:"roots"`
	FilesScanned int            `json:"files_scanned"`
	Findings     []scrubFinding `json:"findings"`
	Warnings     []string       `json:"warnings,omitempty"`
}

type scrubFinding struct {
	Path     string `json:"path"`
	Category string `json:"category"`
	Start    int    `json:"start"`
	End      int    `json:"end"`
	Line     int    `json:"line,omitempty"`
	Column   int    `json:"column,omitempty"`
	Preview  string `json:"preview"`
}

func TestE2E_ScrubQuotedCredentials(t *testing.T) {
	SkipIfShort(t)
	SkipIfNoNTM(t)

	// Restore the caller's environment after testing both clean and explicit
	// allowlist configurations without inheriting a redaction override.
	t.Setenv("NTM_REDACTION_ALLOWLIST", "")
	if err := os.Unsetenv("NTM_REDACTION_ALLOWLIST"); err != nil {
		t.Fatal(err)
	}
	provider := "gh" + "p_" + strings.Repeat("q", 40)
	escapedProvider := "gh" + "p_" + strings.Repeat("q", 31) + "\\u0071SECRETL8"
	compositePassword := "{\"password\":\"left-sensitive " + provider + " right-sensitive\",\"note\":\"public-neighbor\"}\n"
	embeddedPassword := "{\"message\":\"PASSWORD=\\\"left-sensitive:" + provider + ":right-sensitive\\\"\",\"note\":\"public-neighbor\"}\n"
	allowlistedAssignments := "{\"message\":\"PASSWORD=\\\"allowlisted-sensitive-control\\\"\",\"note\":\"public-neighbor\"}\n{\"message\":\"PASSWORD=\\\"unlisted-sensitive-secret\\\"\",\"note\":\"public-neighbor\"}\n"
	cases := []struct {
		name       string
		content    string
		categories []string
		hidden     []string
		allowlist  string
		public     string
		visible    string
		lines      []int
	}{
		{
			name:       "assignment control",
			content:    "PASSWORD=synthetic-never-valid-control-value\n",
			categories: []string{"PASSWORD"},
			hidden:     []string{"synthetic-never-valid-control-value"},
		},
		{
			name:       "quoted JSON",
			content:    "{\"password\":\"synthetic-never-valid-json-value\",\"note\":\"public-neighbor\"}\n",
			categories: []string{"PASSWORD"},
			hidden:     []string{"synthetic-never-valid-json-value"},
		},
		{
			name:       "JSONL escapes and neighbors",
			content:    "{\"password\":\"e2e-head\\\"e2e-middle\\\\e2e-tail with spaces\",\"note\":\"public-neighbor\"}\n{\"passwd\":\"second-e2e-secret\",\"pwd\":\"/home/user/worktree\",\"password_hint\":\"public-hint\"}\n{\"password\":\"<redacted>\"}\n",
			categories: []string{"PASSWORD", "PASSWORD"},
			hidden:     []string{"e2e-head", "e2e-middle", "e2e-tail", "with spaces", "second-e2e-secret"},
		},
		{
			name:       "quoted JSON allowlist",
			content:    "{\"password\":\"allowlisted-never-valid\"}\n{\"password\":\"unlisted-e2e-secret\"}\n",
			categories: []string{"PASSWORD"},
			hidden:     []string{"unlisted-e2e-secret"},
			allowlist:  "'allowlisted-never-valid'",
		},
		{
			name:       "escaped provider suffix",
			content:    "{\"token\":\"" + escapedProvider + "\",\"note\":\"public-neighbor\"}\n",
			categories: []string{"GITHUB_TOKEN", "GENERIC_SECRET"},
			hidden:     []string{strings.Repeat("q", 31), "SECRETL8"},
			public:     "public-neighbor",
		},
		{
			name:       "password around provider",
			content:    compositePassword,
			categories: []string{"PASSWORD", "GITHUB_TOKEN", "PASSWORD"},
			hidden:     []string{"left-sensitive", "right-sensitive", provider},
			public:     "public-neighbor",
		},
		{
			name:       "allowlisted provider inside password",
			content:    compositePassword,
			categories: []string{"PASSWORD", "PASSWORD"},
			hidden:     []string{"left-sensitive", "right-sensitive"},
			allowlist:  "'^" + provider + "$'",
			public:     "public-neighbor",
			visible:    provider,
		},
		{
			name:      "allowlisted exact provider value",
			content:   "{\"password\":\"" + provider + "\",\"note\":\"public-neighbor\"}\n",
			allowlist: "'^" + provider + "$'",
			public:    "public-neighbor",
			visible:   provider,
		},
		{
			name:       "embedded password around provider",
			content:    embeddedPassword,
			categories: []string{"PASSWORD", "GITHUB_TOKEN", "PASSWORD"},
			hidden:     []string{"left-sensitive", "right-sensitive", provider},
			public:     "public-neighbor",
			lines:      []int{1, 1, 1},
		},
		{
			name:       "embedded password allowlists only provider",
			content:    embeddedPassword,
			categories: []string{"PASSWORD", "PASSWORD"},
			hidden:     []string{"left-sensitive", "right-sensitive"},
			allowlist:  "'^" + provider + "$'",
			public:     "public-neighbor",
			visible:    provider,
			lines:      []int{1, 1},
		},
		{
			name:       "embedded quoted assignment",
			content:    "{\"message\":\"PASSWORD=\\\"embedded-sensitive-head-embedded-sensitive-tail\\\"\",\"note\":\"public-neighbor\"}\n",
			categories: []string{"PASSWORD"},
			hidden:     []string{"embedded-sensitive-head", "embedded-sensitive-tail"},
			public:     "public-neighbor",
			lines:      []int{1},
		},
		{
			name:       "fully encoded key and separator",
			content:    "{\"message\":\"\\u0050\\u0041\\u0053\\u0053\\u0057\\u004f\\u0052\\u0044\\u003dfully-encoded-key-secret\",\"note\":\"public-neighbor\"}\n",
			categories: []string{"PASSWORD"},
			hidden:     []string{"fully-encoded-key-secret"},
			public:     "public-neighbor",
			lines:      []int{1},
		},
		{
			name:       "encoded property with escaped value",
			content:    "{\"pass\\u0077ord\":\"encoded-sensitive-head\\\"encoded-sensitive-tail\",\"note\":\"public-neighbor\"}\n",
			categories: []string{"PASSWORD"},
			hidden:     []string{"encoded-sensitive-head", "encoded-sensitive-tail"},
			public:     "public-neighbor",
			lines:      []int{1},
		},
		{
			name:       "physical JSONL lines",
			content:    "{\"message\":\"harmless first record\",\"note\":\"public-neighbor\"}\n\n{\"message\":\"before\\nPASSWORD=\\\"jsonl-sensitive-first\\\"\\nafter\",\"note\":\"public-neighbor\"}\n{\"pass\\u0077ord\":\"jsonl-sensitive-second\",\"note\":\"public-neighbor\"}\n",
			categories: []string{"PASSWORD", "PASSWORD"},
			hidden:     []string{"jsonl-sensitive-first", "jsonl-sensitive-second"},
			public:     "public-neighbor",
			lines:      []int{3, 4},
		},
		{
			name:       "raw embedded assignment allowlist",
			content:    allowlistedAssignments,
			categories: []string{"PASSWORD"},
			hidden:     []string{"unlisted-sensitive-secret"},
			allowlist:  "'^" + regexp.QuoteMeta("PASSWORD=\\\"allowlisted-sensitive-control\\\"") + "$'",
			public:     "public-neighbor",
			visible:    "allowlisted-sensitive-control",
			lines:      []int{2},
		},
		{
			name:       "logical embedded assignment allowlist",
			content:    allowlistedAssignments,
			categories: []string{"PASSWORD"},
			hidden:     []string{"unlisted-sensitive-secret"},
			allowlist:  "'^" + regexp.QuoteMeta("PASSWORD=\"allowlisted-sensitive-control\"") + "$'",
			public:     "public-neighbor",
			visible:    "allowlisted-sensitive-control",
			lines:      []int{2},
		},
		{
			name: "decoded noncredentials",
			content: "{\"message\":\"password=\\u0061\\u0062\\u0063\\u0064\\u0065\\u0066\\u0067\",\"note\":\"public-neighbor\"}\n" +
				"{\"message\":\"PWD=\\u002fhome/user/project\",\"note\":\"public-neighbor\"}\n" +
				"{\"message\":\"OLDPWD=C:\\\\Users\\\\user\\\\project\",\"note\":\"public-neighbor\"}\n" +
				"{\"message\":\"password=\\u003credacted\\u003e\",\"note\":\"public-neighbor\"}\n" +
				"{\"password\\u005fhint\":\"public-neighbor-long\",\"token\\u005fcount\":\"public-count-value\",\"note\":\"public-neighbor\"}\n" +
				"{\"pass\\u0077ord\\u0022\\u003a\\u0022token\":\"public-neighbor-long\",\"note\":\"public-neighbor\"}\n",
			public: "public-neighbor",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			artifact := filepath.Join(dir, "artifact.log")
			configPath := filepath.Join(dir, "clean.toml")
			if err := os.WriteFile(artifact, []byte(tc.content), 0o600); err != nil {
				t.Fatal(err)
			}
			settings := "[redaction]\nmode = 'redact'\nallowlist = [" + tc.allowlist + "]\n"
			if err := os.WriteFile(configPath, []byte(settings), 0o600); err != nil {
				t.Fatal(err)
			}
			for _, command := range []string{"scrub", "preview"} {
				for _, format := range []string{"json", "text"} {
					t.Run(command+"/"+format, func(t *testing.T) {
						args := []string{"--config", configPath}
						if command == "scrub" {
							args = append(args, "scrub", "--path", artifact, "--format", format)
						} else {
							args = append(args, "redact", "preview", "--file", artifact)
							if format == "json" {
								args = append(args, "--json")
							}
						}
						cmd := exec.Command(resolveNTMBin(), args...)
						cmd.Dir = dir
						cmd.Env = append(os.Environ(), "HOME="+dir, "XDG_CONFIG_HOME="+dir)
						output, err := cmd.CombinedOutput()
						if err != nil {
							t.Fatalf("command failed: %v\n%s", err, output)
						}
						for _, fragment := range tc.hidden {
							if strings.Contains(string(output), fragment) {
								t.Error("CLI output exposed a secret fragment")
							}
						}
						for _, category := range tc.categories {
							if !strings.Contains(string(output), "[REDACTED:"+category+":") {
								t.Errorf("CLI output lacks a %s placeholder", category)
							}
						}
						if tc.public != "" && (command == "preview" || len(tc.categories) > 0) && !strings.Contains(string(output), tc.public) {
							t.Error("CLI output removed the neighboring public field")
						}
						if command == "preview" && tc.visible != "" && !strings.Contains(string(output), tc.visible) {
							t.Error("CLI output did not preserve the explicitly allowlisted provider")
						}
						if format == "json" {
							var response struct {
								Findings []scrubFinding
								Output   string
							}
							if err := json.Unmarshal(output, &response); err != nil {
								t.Fatalf("decode response: %v\n%s", err, output)
							}
							if len(response.Findings) != len(tc.categories) {
								t.Fatalf("findings=%d, want %d", len(response.Findings), len(tc.categories))
							}
							for i, finding := range response.Findings {
								if finding.Category != tc.categories[i] {
									t.Errorf("category=%s, want %s", finding.Category, tc.categories[i])
								}
								if finding.Start < 0 || finding.End <= finding.Start || finding.End > len(tc.content) || finding.Line < 1 || finding.Column < 1 {
									t.Errorf("invalid finding coordinates: %+v", finding)
								}
								if len(tc.lines) > 0 && finding.Line != tc.lines[i] {
									t.Errorf("finding %d line=%d, want physical line %d", i, finding.Line, tc.lines[i])
								}
							}
							if command == "preview" && len(tc.categories) == 0 && response.Output != tc.content {
								t.Error("preview changed an input with no findings")
							}
						}
					})
				}
			}
		})
	}
}

func TestE2E_ScrubScansArtifactsSafely(t *testing.T) {
	SkipIfShort(t)
	SkipIfNoNTM(t)

	logger := NewTestLogger(t, "scrub")
	t.Cleanup(func() {
		logger.Close()
	})

	tempDir := t.TempDir()
	artifactPath := filepath.Join(tempDir, "artifact.txt")
	content := strings.Join([]string{
		"openai_key=" + fakeOpenAIKey,
		"github_token=" + fakeGitHubToken,
		fakeAWSSecret,
		fakePassword,
		fakeJWT,
	}, "\n")

	if err := os.WriteFile(artifactPath, []byte(content), 0o644); err != nil {
		t.Fatalf("[E2E-SCRUB] failed to write artifact: %v", err)
	}

	logger.Log("[E2E-SCRUB] Created artifact: %s", artifactPath)

	cmd := exec.Command("ntm", "scrub", "--path", tempDir, "--format", "json")
	cmd.Env = append(os.Environ(), "HOME="+tempDir, "XDG_CONFIG_HOME="+tempDir)
	output, err := cmd.CombinedOutput()
	logger.Log("[E2E-SCRUB] Running: ntm scrub --path %s --format json", tempDir)
	logger.Log("[E2E-SCRUB] Output: %s", string(output))
	if err != nil {
		t.Fatalf("[E2E-SCRUB] scrub command failed: %v", err)
	}

	var res scrubResult
	if jsonErr := json.Unmarshal(output, &res); jsonErr != nil {
		t.Fatalf("[E2E-SCRUB] failed to parse JSON: %v", jsonErr)
	}
	logger.Log("[E2E-SCRUB] roots=%v files=%d findings=%d", res.Roots, res.FilesScanned, len(res.Findings))

	if res.FilesScanned < 1 {
		t.Fatalf("[E2E-SCRUB] expected files_scanned >= 1, got %d", res.FilesScanned)
	}
	if len(res.Findings) == 0 {
		t.Fatalf("[E2E-SCRUB] expected findings, got 0")
	}

	secretMarkers := []string{fakeOpenAIKey, fakeGitHubToken, fakeAWSSecret, fakePassword, fakeJWT}
	categorySet := make(map[string]struct{})

	for _, f := range res.Findings {
		categorySet[f.Category] = struct{}{}
		if f.Path == "" {
			t.Fatalf("[E2E-SCRUB] empty path in finding")
		}
		if !strings.HasPrefix(f.Path, tempDir) {
			t.Fatalf("[E2E-SCRUB] finding path outside temp dir: %s", f.Path)
		}
		if f.Preview == "" {
			t.Fatalf("[E2E-SCRUB] empty preview for %s", f.Path)
		}
		if !strings.Contains(f.Preview, redactedMarker) {
			t.Fatalf("[E2E-SCRUB] preview missing redaction marker: %s", f.Preview)
		}
		for _, secret := range secretMarkers {
			if strings.Contains(f.Preview, secret) {
				t.Fatalf("[E2E-SCRUB] preview leaked raw secret: %s", secret)
			}
		}
		logger.Log("[E2E-SCRUB] finding category=%s line=%d preview=%s", f.Category, f.Line, f.Preview)
	}

	if _, ok := categorySet["OPENAI_KEY"]; !ok {
		t.Fatalf("[E2E-SCRUB] expected OPENAI_KEY finding")
	}
	if _, ok := categorySet["PASSWORD"]; !ok {
		t.Fatalf("[E2E-SCRUB] expected PASSWORD finding")
	}

	if len(res.Warnings) > 0 {
		logger.Log("[E2E-SCRUB] warnings: %v", res.Warnings)
	}
}
