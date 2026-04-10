package cli

import (
	"strings"
	"testing"
)

// =============================================================================
// upgrade.go: isNewerVersion
// =============================================================================

func TestIsNewerVersion(t *testing.T) {

	tests := []struct {
		name    string
		current string
		latest  string
		want    bool
	}{
		{"same version", "1.0.0", "1.0.0", false},
		{"newer patch", "1.0.0", "1.0.1", true},
		{"newer minor", "1.0.0", "1.1.0", true},
		{"newer major", "1.0.0", "2.0.0", true},
		{"older version", "2.0.0", "1.0.0", false},
		{"dev always upgrades", "dev", "1.0.0", true},
		{"empty current", "", "1.0.0", true},
		{"v prefix stripped", "v1.0.0", "v1.0.1", true},
		{"beta suffix stripped", "1.0.0-beta", "1.0.0", false},
		{"different lengths", "1.0", "1.0.1", true},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := isNewerVersion(tc.current, tc.latest)
			if got != tc.want {
				t.Errorf("isNewerVersion(%q, %q) = %v, want %v", tc.current, tc.latest, got, tc.want)
			}
		})
	}
}

// =============================================================================
// upgrade.go: parseVersionPart
// =============================================================================

func TestParseVersionPart(t *testing.T) {

	tests := []struct {
		name string
		part string
		want int
	}{
		{"simple", "3", 3},
		{"zero", "0", 0},
		{"ten", "10", 10},
		{"non-numeric", "abc", 0},
		{"empty", "", 0},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := parseVersionPart(tc.part)
			if got != tc.want {
				t.Errorf("parseVersionPart(%q) = %d, want %d", tc.part, got, tc.want)
			}
		})
	}
}

// =============================================================================
// upgrade.go: trimAssetExt
// =============================================================================

func TestTrimAssetExt(t *testing.T) {

	tests := []struct {
		name  string
		input string
		want  string
	}{
		{"tar.gz", "ntm_linux_amd64.tar.gz", "ntm_linux_amd64"},
		{"zip", "ntm_darwin_arm64.zip", "ntm_darwin_arm64"},
		{"exe", "ntm_windows_amd64.exe", "ntm_windows_amd64"},
		{"no ext", "ntm_linux_amd64", "ntm_linux_amd64"},
		{"other ext", "ntm.deb", "ntm.deb"},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := trimAssetExt(tc.input)
			if got != tc.want {
				t.Errorf("trimAssetExt(%q) = %q, want %q", tc.input, got, tc.want)
			}
		})
	}
}

// =============================================================================
// upgrade.go: archCandidates
// =============================================================================

func TestArchCandidates(t *testing.T) {

	tests := []struct {
		name string
		os   string
		arch string
		want []string
	}{
		{"darwin arm64", "darwin", "arm64", []string{"all", "arm64", "amd64"}},
		{"darwin amd64", "darwin", "amd64", []string{"all", "amd64"}},
		{"linux amd64", "linux", "amd64", []string{"amd64"}},
		{"linux arm", "linux", "arm", []string{"armv7", "arm"}},
		{"linux arm64", "linux", "arm64", []string{"arm64"}},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := archCandidates(tc.os, tc.arch)
			if len(got) != len(tc.want) {
				t.Fatalf("archCandidates(%q, %q) = %v, want %v", tc.os, tc.arch, got, tc.want)
			}
			for i := range tc.want {
				if got[i] != tc.want[i] {
					t.Errorf("[%d] = %q, want %q", i, got[i], tc.want[i])
				}
			}
		})
	}
}

// =============================================================================
// upgrade.go: legacyDashNames
// =============================================================================

func TestLegacyDashNames(t *testing.T) {

	names := legacyDashNames("linux", "amd64", "1.0.0")
	found := false
	for _, n := range names {
		if n == "ntm-1.0.0-linux-amd64" {
			found = true
		}
	}
	if !found {
		t.Errorf("expected ntm-1.0.0-linux-amd64 in %v", names)
	}

	// Without version
	names2 := legacyDashNames("darwin", "arm64", "")
	for _, n := range names2 {
		if n == "ntm--darwin-arm64" {
			t.Errorf("empty version should not produce double dash")
		}
	}
}

// =============================================================================
// models.go: blankDash
// =============================================================================

func TestBlankDash(t *testing.T) {

	tests := []struct {
		name  string
		input string
		want  string
	}{
		{"empty", "", "-"},
		{"whitespace", "   ", "-"},
		{"value", "hello", "hello"},
		{"with spaces", "  hello  ", "hello"},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := blankDash(tc.input)
			if got != tc.want {
				t.Errorf("blankDash(%q) = %q, want %q", tc.input, got, tc.want)
			}
		})
	}
}

// =============================================================================
// Verify test helpers are stable
// =============================================================================

func TestSortBatchStableOrder(t *testing.T) {

	// All same priority should maintain insertion order (stable sort)
	prompts := []BatchPrompt{
		{Text: "first", Priority: 1},
		{Text: "second", Priority: 1},
		{Text: "third", Priority: 1},
	}

	sortBatchByPriority(prompts)
	if prompts[0].Text != "first" || prompts[1].Text != "second" || prompts[2].Text != "third" {
		t.Errorf("stable sort violated: %+v", prompts)
	}
}

// Verify archCandidates + legacyDashNames integration
func TestLegacyDashNamesWithDarwin(t *testing.T) {

	names := legacyDashNames("darwin", "arm64", "2.0.0")
	// Should include all, arm64, amd64 variants
	if len(names) < 3 {
		t.Errorf("darwin arm64 should have at least 3 legacy names, got %v", names)
	}

	// Verify sorted or at least contains expected
	found := map[string]bool{}
	for _, n := range names {
		found[n] = true
	}
	if !found["ntm-2.0.0-darwin-all"] {
		t.Errorf("missing ntm-2.0.0-darwin-all in %v", names)
	}
}

// Verify parseAssetInfo extension parsing
func TestParseAssetInfoExtension(t *testing.T) {

	info := parseAssetInfo("ntm_1.0.0_linux_amd64.tar.gz", "linux", "amd64", "1.0.0")
	if info.Extension != ".tar.gz" {
		t.Errorf("Extension = %q, want .tar.gz", info.Extension)
	}
	if info.Version != "1.0.0" {
		t.Errorf("Version = %q, want 1.0.0", info.Version)
	}

	info2 := parseAssetInfo("ntm_linux_amd64.zip", "linux", "amd64", "")
	if info2.Extension != ".zip" {
		t.Errorf("Extension = %q, want .zip", info2.Extension)
	}
}

func TestFindUpgradeAssetSkipsUnsupportedLinuxFormats(t *testing.T) {

	assets := []GitHubAsset{
		{Name: "ntm_linux_amd64.zip"},
		{Name: "ntm_linux_amd64.tar.gz"},
	}

	match, _ := findUpgradeAsset(assets, "linux", "amd64", "1.11.0", false)
	if match == nil {
		t.Fatal("findUpgradeAsset() = nil, want tar.gz match")
	}
	if match.Asset == nil || match.Asset.Name != "ntm_linux_amd64.tar.gz" {
		t.Fatalf("findUpgradeAsset() picked %q, want ntm_linux_amd64.tar.gz", match.Asset.Name)
	}
}

func TestFindUpgradeAssetRejectsPackageArtifacts(t *testing.T) {

	assets := []GitHubAsset{
		{Name: "ntm_1.11.0_linux_amd64.deb"},
		{Name: "ntm_1.11.0_linux_amd64.rpm"},
		{Name: "ntm_1.11.0_linux_amd64.apk"},
	}

	match, _ := findUpgradeAsset(assets, "linux", "amd64", "1.11.0", false)
	if match != nil {
		t.Fatalf("findUpgradeAsset() picked %q from package artifacts, want nil", match.Asset.Name)
	}
}

func TestFindChecksumsAssetPrefersModernChecksumName(t *testing.T) {

	assets := []GitHubAsset{
		{Name: "README.md"},
		{Name: "checksums.txt"},
		{Name: "SHA256SUMS"},
	}

	match := findChecksumsAsset(assets)
	if match == nil {
		t.Fatal("findChecksumsAsset() = nil, want SHA256SUMS asset")
	}
	if match.Name != "SHA256SUMS" {
		t.Fatalf("findChecksumsAsset() = %q, want SHA256SUMS", match.Name)
	}
}

func TestParseChecksumsParsesModernAndLegacyFormats(t *testing.T) {

	input := strings.Join([]string{
		"# generated by release pipeline",
		"abc123  ntm_1.11.0_linux_amd64.tar.gz",
		"def456 ntm_1.11.0_linux_arm64.tar.gz",
		"789abc dist/ntm_1.11.0_windows_amd64.zip",
	}, "\n")

	checksums, err := parseChecksums(strings.NewReader(input))
	if err != nil {
		t.Fatalf("parseChecksums() error = %v", err)
	}

	if got := checksums["ntm_1.11.0_linux_amd64.tar.gz"]; got != "abc123" {
		t.Fatalf("linux_amd64 checksum = %q, want abc123", got)
	}
	if got := checksums["ntm_1.11.0_linux_arm64.tar.gz"]; got != "def456" {
		t.Fatalf("linux_arm64 checksum = %q, want def456", got)
	}
	if got := checksums["dist/ntm_1.11.0_windows_amd64.zip"]; got != "" {
		t.Fatalf("unexpected key with path preserved: %q", got)
	}
	if got := checksums["ntm_1.11.0_windows_amd64.zip"]; got != "789abc" {
		t.Fatalf("windows checksum = %q, want 789abc", got)
	}
}
