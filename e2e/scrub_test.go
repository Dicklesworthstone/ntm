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
	cases := []struct {
		name       string
		content    string
		categories []string
		hidden     []string
		allowlist  string
		public     string
		visible    string
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
