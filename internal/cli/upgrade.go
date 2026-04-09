package cli

import (
	"archive/tar"
	"archive/zip"
	"bufio"
	"compress/gzip"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"github.com/charmbracelet/lipgloss"
	"github.com/spf13/cobra"
)

const (
	githubOwner = "Dicklesworthstone"
	githubRepo  = "ntm"
	githubAPI   = "https://api.github.com"
)

// GitHubRelease represents a GitHub release
type GitHubRelease struct {
	TagName     string        `json:"tag_name"`
	Name        string        `json:"name"`
	Draft       bool          `json:"draft"`
	Prerelease  bool          `json:"prerelease"`
	PublishedAt time.Time     `json:"published_at"`
	Body        string        `json:"body"`
	Assets      []GitHubAsset `json:"assets"`
	HTMLURL     string        `json:"html_url"`
}

// GitHubAsset represents a release asset
type GitHubAsset struct {
	Name               string `json:"name"`
	Size               int64  `json:"size"`
	BrowserDownloadURL string `json:"browser_download_url"`
	ContentType        string `json:"content_type"`
}

// assetInfo contains parsed information about a release asset
type assetInfo struct {
	Name      string `json:"name"`
	OS        string `json:"os,omitempty"`
	Arch      string `json:"arch,omitempty"`
	Version   string `json:"version,omitempty"`
	Extension string `json:"extension,omitempty"`
	Match     string `json:"match"` // "exact", "close", "none"
	Reason    string `json:"reason,omitempty"`
}

type assetMatch struct {
	Asset      *GitHubAsset
	Strategy   string
	Confidence float64
	Reason     string
}

// upgradeError provides structured diagnostic information when asset lookup fails
type upgradeError struct {
	Platform        string      `json:"platform"`
	Convention      string      `json:"convention"`
	TriedNames      []string    `json:"tried_names"`
	AvailableAssets []assetInfo `json:"available_assets"`
	ReleaseURL      string      `json:"release_url"`
	ClosestMatch    *assetInfo  `json:"closest_match,omitempty"`
}

// Error implements the error interface with a styled diagnostic output
func (e *upgradeError) Error() string {
	errorStyle := lipgloss.NewStyle().Foreground(lipgloss.Color("#f38ba8"))
	dimStyle := lipgloss.NewStyle().Foreground(lipgloss.Color("#6c7086"))
	warnStyle := lipgloss.NewStyle().Foreground(lipgloss.Color("#f9e2af"))
	hintStyle := lipgloss.NewStyle().Foreground(lipgloss.Color("#89b4fa"))

	var sb strings.Builder

	// Header box
	boxStyle := lipgloss.NewStyle().
		Border(lipgloss.RoundedBorder()).
		BorderForeground(lipgloss.Color("#f38ba8")).
		Padding(0, 1).
		Width(66)

	headerContent := fmt.Sprintf("%s\n\n", errorStyle.Render("Upgrade Asset Lookup Failed"))
	headerContent += fmt.Sprintf("  Platform:      %s\n", e.Platform)
	headerContent += fmt.Sprintf("  Convention:    %s\n", e.Convention)
	if len(e.TriedNames) > 0 {
		headerContent += fmt.Sprintf("  Tried:         %s", e.TriedNames[0])
		for _, name := range e.TriedNames[1:] {
			headerContent += fmt.Sprintf("\n                 %s", name)
		}
	} else {
		headerContent += "  Tried:         [none]"
	}
	headerContent += fmt.Sprintf("\n  Found:         %s", errorStyle.Render("[none matching]"))

	sb.WriteString(boxStyle.Render(headerContent))
	sb.WriteString("\n\n")

	// Available assets with annotations
	sb.WriteString("Available release assets:\n")
	for _, asset := range e.AvailableAssets {
		var marker, suffix string
		switch asset.Match {
		case "exact":
			// Semantic exact match (OS+Arch) but name didn't match - unusual, check version/naming
			marker = warnStyle.Render("?")
			suffix = warnStyle.Render(" ← platform match, name mismatch (check version?)")
		case "close":
			marker = warnStyle.Render("≈")
			if asset.Reason != "" {
				suffix = warnStyle.Render(fmt.Sprintf(" ← %s", asset.Reason))
			} else {
				suffix = warnStyle.Render(" ← closest match")
			}
		default:
			marker = dimStyle.Render("✗")
		}
		platformInfo := ""
		if asset.OS != "" && asset.Arch != "" {
			platformInfo = fmt.Sprintf(" (%s/%s)", asset.OS, asset.Arch)
		}
		sb.WriteString(fmt.Sprintf("  %s %s%s%s\n", marker, asset.Name, dimStyle.Render(platformInfo), suffix))
	}
	sb.WriteString("\n")

	// Troubleshooting hints
	sb.WriteString(hintStyle.Render("This usually indicates a naming convention mismatch between:"))
	sb.WriteString("\n")
	sb.WriteString(fmt.Sprintf("  • %s (how assets are built)\n", dimStyle.Render(".goreleaser.yaml")))
	sb.WriteString(fmt.Sprintf("  • %s (how assets are found)\n", dimStyle.Render("internal/cli/upgrade.go")))
	sb.WriteString("\n")

	sb.WriteString(hintStyle.Render("To diagnose:"))
	sb.WriteString("\n")
	sb.WriteString(fmt.Sprintf("  1. Run: %s\n", dimStyle.Render("go test -v -run TestUpgradeAssetNaming ./internal/cli/")))
	sb.WriteString(fmt.Sprintf("  2. Check: %s\n", dimStyle.Render(e.ReleaseURL)))
	sb.WriteString("  3. Compare asset names against expected patterns above\n")
	sb.WriteString("\n")

	// Self-service links
	sb.WriteString(hintStyle.Render("Resources:"))
	sb.WriteString("\n")
	sb.WriteString(fmt.Sprintf("  • Releases: %s\n", dimStyle.Render("https://github.com/Dicklesworthstone/ntm/releases")))
	sb.WriteString(fmt.Sprintf("  • Report issue: %s\n", dimStyle.Render("https://github.com/Dicklesworthstone/ntm/issues/new")))

	return sb.String()
}

// JSON returns a machine-readable JSON representation of the error
func (e *upgradeError) JSON() string {
	data, _ := json.MarshalIndent(e, "", "  ")
	return string(data)
}

// parseAssetInfo extracts OS/arch information from an asset name
func parseAssetInfo(name, targetOS, targetArch, targetVersion string) assetInfo {
	info := assetInfo{Name: name, Match: "none"}

	// Common extensions
	ext := ""
	for _, suffix := range []string{".tar.gz", ".zip", ".exe"} {
		if strings.HasSuffix(name, suffix) {
			ext = suffix
			break
		}
	}
	info.Extension = ext

	// Parse ntm_VERSION_OS_ARCH.ext or ntm_OS_ARCH patterns
	baseName := strings.TrimSuffix(name, ext)
	parts := strings.Split(baseName, "_")

	if len(parts) >= 3 && parts[0] == "ntm" {
		// Could be ntm_VERSION_OS_ARCH or ntm_OS_ARCH
		if len(parts) == 4 {
			// ntm_VERSION_OS_ARCH
			info.Version = parts[1]
			info.OS = parts[2]
			info.Arch = parts[3]
		} else if len(parts) == 3 {
			// ntm_OS_ARCH (no version)
			info.OS = parts[1]
			info.Arch = parts[2]
		}
	}

	if info.OS == "" && strings.HasPrefix(baseName, "ntm-") {
		dashParts := strings.Split(baseName, "-")
		if len(dashParts) == 4 {
			// ntm-VERSION-OS-ARCH
			info.Version = dashParts[1]
			info.OS = dashParts[2]
			info.Arch = dashParts[3]
		} else if len(dashParts) == 3 {
			// ntm-OS-ARCH
			info.OS = dashParts[1]
			info.Arch = dashParts[2]
		}
	}

	// Determine match quality
	if info.OS == targetOS {
		if info.Arch == targetArch {
			// Exact architecture match
			info.Match = "exact"
		} else if targetArch == "all" && (info.Arch == "arm64" || info.Arch == "amd64") {
			// We want universal ("all"), but found specific arch - close match
			info.Match = "close"
			info.Reason = fmt.Sprintf("same OS, specific arch (got %s, want universal)", info.Arch)
		} else if info.Arch == "all" {
			// We want specific arch, but found universal - close match (universal should work)
			info.Match = "close"
			info.Reason = fmt.Sprintf("same OS, universal binary available (got all, want %s)", targetArch)
		} else if info.Arch != "" {
			// Different specific arch - close match for same OS (includes armv7, etc.)
			info.Match = "close"
			info.Reason = fmt.Sprintf("same OS, different arch (got %s, want %s)", info.Arch, targetArch)
		}
	}

	return info
}

func trimAssetExt(name string) string {
	for _, suffix := range []string{".tar.gz", ".zip", ".exe"} {
		if strings.HasSuffix(name, suffix) {
			return strings.TrimSuffix(name, suffix)
		}
	}
	return name
}

func archCandidates(targetOS, targetArch string) []string {
	switch targetOS {
	case "darwin":
		switch targetArch {
		case "arm64":
			return []string{"all", "arm64", "amd64"}
		case "amd64":
			return []string{"all", "amd64"}
		default:
			return []string{targetArch}
		}
	default:
		if targetArch == "arm" {
			return []string{"armv7", "arm"}
		}
		return []string{targetArch}
	}
}

func legacyDashNames(targetOS, targetArch, version string) []string {
	var names []string
	for _, arch := range archCandidates(targetOS, targetArch) {
		if version != "" {
			names = append(names, fmt.Sprintf("ntm-%s-%s-%s", version, targetOS, arch))
		}
		names = append(names, fmt.Sprintf("ntm-%s-%s", targetOS, arch))
	}
	return names
}

func findUpgradeAsset(assets []GitHubAsset, targetOS, targetArch, version string, strict bool) (*assetMatch, []string) {
	archiveAssetName := archiveAssetNameFor(version, targetOS, targetArch)
	binaryAssetName := assetNameFor(targetOS, targetArch)

	tried := []string{archiveAssetName, binaryAssetName}

	if match := matchExactArchive(assets, archiveAssetName); match != nil {
		return match, tried
	}

	if match := matchExactBinary(assets, binaryAssetName, targetOS); match != nil {
		return match, tried
	}

	if strict {
		return nil, tried
	}

	arch := normalizedArch(targetOS, targetArch)
	tried = append(tried, binaryAssetName+"*", fmt.Sprintf("ntm_%s_%s_%s*", version, targetOS, arch))
	if match := matchPrefix(assets, binaryAssetName, version, targetOS, targetArch); match != nil {
		return match, tried
	}

	tried = append(tried, fmt.Sprintf("any %s asset with compatible arch", targetOS))
	if match := matchSameOS(assets, targetOS, targetArch); match != nil {
		return match, tried
	}

	legacyNames := legacyDashNames(targetOS, targetArch, version)
	tried = append(tried, legacyNames...)
	if match := matchLegacyDash(assets, legacyNames, targetOS); match != nil {
		return match, tried
	}

	return nil, tried
}

func matchExactArchive(assets []GitHubAsset, archiveName string) *assetMatch {
	for i := range assets {
		if assets[i].Name == archiveName {
			return &assetMatch{
				Asset:      &assets[i],
				Strategy:   "exact_archive",
				Confidence: 1.0,
				Reason:     "exact archive match",
			}
		}
	}
	return nil
}

func upgradeAssetKind(name string) string {
	switch {
	case strings.HasSuffix(name, ".tar.gz"):
		return "tar.gz"
	case strings.HasSuffix(name, ".zip"):
		return "zip"
	case strings.HasSuffix(name, ".exe"):
		return "exe"
	case !strings.Contains(filepath.Base(name), "."):
		return "raw"
	default:
		return "unsupported"
	}
}

func isSupportedUpgradeAsset(name, targetOS string) bool {
	switch targetOS {
	case "windows":
		kind := upgradeAssetKind(name)
		return kind == "zip" || kind == "exe"
	default:
		kind := upgradeAssetKind(name)
		return kind == "tar.gz" || kind == "raw"
	}
}

func matchExactBinary(assets []GitHubAsset, binaryName, targetOS string) *assetMatch {
	for i := range assets {
		if trimAssetExt(assets[i].Name) == binaryName && isSupportedUpgradeAsset(assets[i].Name, targetOS) {
			return &assetMatch{
				Asset:      &assets[i],
				Strategy:   "exact_binary",
				Confidence: 0.9,
				Reason:     "exact binary match",
			}
		}
	}
	return nil
}

func matchPrefix(assets []GitHubAsset, binaryName, version, targetOS, targetArch string) *assetMatch {
	arch := normalizedArch(targetOS, targetArch)
	versionPrefix := fmt.Sprintf("ntm_%s_%s_%s", version, targetOS, arch)
	for i := range assets {
		if !isSupportedUpgradeAsset(assets[i].Name, targetOS) {
			continue
		}
		baseName := trimAssetExt(assets[i].Name)
		if strings.HasPrefix(baseName, binaryName) || strings.HasPrefix(baseName, versionPrefix) {
			return &assetMatch{
				Asset:      &assets[i],
				Strategy:   "prefix_match",
				Confidence: 0.7,
				Reason:     "prefix match",
			}
		}
	}
	return nil
}

func matchSameOS(assets []GitHubAsset, targetOS, targetArch string) *assetMatch {
	candidates := archCandidates(targetOS, targetArch)
	for _, arch := range candidates {
		for i := range assets {
			if !isSupportedUpgradeAsset(assets[i].Name, targetOS) {
				continue
			}
			baseName := trimAssetExt(assets[i].Name)
			if strings.HasPrefix(baseName, "ntm-") {
				continue
			}
			info := parseAssetInfo(assets[i].Name, targetOS, targetArch, "")
			if info.OS == targetOS && info.Arch == arch {
				reason := fmt.Sprintf("same OS, compatible arch (%s)", arch)
				if targetOS == "darwin" && targetArch == "arm64" && arch == "amd64" {
					reason = "same OS, amd64 via Rosetta 2"
				} else if targetOS == "darwin" && arch == "all" {
					reason = "same OS, universal binary"
				}
				return &assetMatch{
					Asset:      &assets[i],
					Strategy:   "fuzzy_same_os",
					Confidence: 0.5,
					Reason:     reason,
				}
			}
		}
	}
	return nil
}

func matchLegacyDash(assets []GitHubAsset, names []string, targetOS string) *assetMatch {
	nameSet := make(map[string]struct{}, len(names))
	for _, name := range names {
		nameSet[name] = struct{}{}
	}
	for i := range assets {
		if !isSupportedUpgradeAsset(assets[i].Name, targetOS) {
			continue
		}
		baseName := trimAssetExt(assets[i].Name)
		if _, ok := nameSet[baseName]; ok {
			return &assetMatch{
				Asset:      &assets[i],
				Strategy:   "legacy_dash",
				Confidence: 0.3,
				Reason:     "legacy dash naming",
			}
		}
	}
	return nil
}

// newUpgradeError creates a structured upgrade error with diagnostic information
func newUpgradeError(targetOS, targetArch, version string, triedNames []string, assets []GitHubAsset, releaseURL string) *upgradeError {
	// Determine target arch (darwin uses "all" for universal binaries)
	displayArch := targetArch
	if targetOS == "darwin" {
		displayArch = "all"
	}

	err := &upgradeError{
		Platform:   fmt.Sprintf("%s/%s", targetOS, targetArch),
		Convention: "ntm_{version}_{os}_{arch}.tar.gz",
		TriedNames: triedNames,
		ReleaseURL: releaseURL,
	}

	// Parse and annotate available assets
	for _, asset := range assets {
		info := parseAssetInfo(asset.Name, targetOS, displayArch, version)
		err.AvailableAssets = append(err.AvailableAssets, info)

		// Track closest match (prefer "exact" over "close" - exact means platform matches but name didn't)
		if info.Match == "exact" {
			infoCopy := info
			err.ClosestMatch = &infoCopy
		} else if info.Match == "close" && err.ClosestMatch == nil {
			infoCopy := info
			err.ClosestMatch = &infoCopy
		}
	}

	return err
}

func newUpgradeCmd() *cobra.Command {
	var checkOnly bool
	var force bool
	var yes bool
	var strict bool
	var verbose bool

	cmd := &cobra.Command{
		Use:     "upgrade",
		Aliases: []string{"update"},
		Short:   "Upgrade NTM to the latest version",
		Long: `Check for and install the latest version of NTM from GitHub releases.

Examples:
  ntm upgrade           # Check and upgrade (with confirmation)
  ntm upgrade --check   # Only check for updates, don't install
  ntm upgrade --yes     # Auto-confirm, skip confirmation prompt
  ntm upgrade --force   # Force reinstall even if already on latest
  ntm upgrade --strict  # Only allow exact asset matches (CI/testing)`,
		RunE: func(cmd *cobra.Command, args []string) error {
			return runUpgrade(checkOnly, force, yes, strict, verbose)
		},
	}

	cmd.Flags().BoolVar(&checkOnly, "check", false, "Only check for updates, don't install")
	cmd.Flags().BoolVarP(&force, "force", "f", false, "Force reinstall even if already on latest version")
	cmd.Flags().BoolVarP(&yes, "yes", "y", false, "Auto-confirm upgrade without prompting")
	cmd.Flags().BoolVar(&strict, "strict", false, "Require exact asset name matches (disable fallback)")
	cmd.Flags().BoolVarP(&verbose, "verbose", "v", false, "Show detailed asset matching info")

	return cmd
}

func runUpgrade(checkOnly, force, yes, strict, verbose bool) error {
	// Styles for output
	titleStyle := lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("#89b4fa"))
	successStyle := lipgloss.NewStyle().Foreground(lipgloss.Color("#a6e3a1"))
	warnStyle := lipgloss.NewStyle().Foreground(lipgloss.Color("#f9e2af"))
	errorStyle := lipgloss.NewStyle().Foreground(lipgloss.Color("#f38ba8"))
	dimStyle := lipgloss.NewStyle().Foreground(lipgloss.Color("#6c7086"))

	currentVersion := Version
	if currentVersion == "" {
		currentVersion = "dev"
	}

	fmt.Println(titleStyle.Render("🔄 NTM Upgrade"))
	fmt.Println()
	fmt.Printf("  Current version: %s\n", dimStyle.Render(currentVersion))
	fmt.Printf("  Platform: %s/%s\n", dimStyle.Render(runtime.GOOS), dimStyle.Render(runtime.GOARCH))
	fmt.Println()

	// Fetch latest release info
	fmt.Print("  Checking for updates... ")
	release, err := fetchLatestRelease()
	if err != nil {
		fmt.Println(errorStyle.Render("✗"))
		fmt.Println()
		fmt.Printf("  %s %s\n", errorStyle.Render("Error:"), err)
		fmt.Println()
		fmt.Println(dimStyle.Render("  If this is a development build, releases may not exist yet."))
		fmt.Println(dimStyle.Render("  Check: https://github.com/Dicklesworthstone/ntm/releases"))
		return nil
	}
	fmt.Println(successStyle.Render("✓"))

	latestVersion := strings.TrimPrefix(release.TagName, "v")
	fmt.Printf("  Latest version:  %s\n", successStyle.Render(latestVersion))
	fmt.Println()

	// Compare versions
	isNewer := isNewerVersion(currentVersion, latestVersion)
	isSame := normalizeVersion(currentVersion) == normalizeVersion(latestVersion)

	if isSame && !force {
		fmt.Println(successStyle.Render("  ✓ You're already on the latest version!"))
		return nil
	}

	if !isNewer && !force {
		fmt.Printf("  %s Your version (%s) appears to be newer than the latest release (%s)\n",
			warnStyle.Render("⚠"),
			currentVersion,
			latestVersion)
		fmt.Println(dimStyle.Render("    Use --force to reinstall anyway"))
		return nil
	}

	if checkOnly {
		if isNewer {
			fmt.Printf("  %s New version available: %s → %s\n",
				warnStyle.Render("⬆"),
				currentVersion,
				successStyle.Render(latestVersion))
			fmt.Println()
			fmt.Println(dimStyle.Render("  Run 'ntm upgrade' to install"))
		}
		return nil
	}

	// Find the appropriate asset for this platform
	// Try the versioned archive name first (e.g., ntm_1.4.1_darwin_all.tar.gz)
	archiveAssetName := getArchiveAssetName(latestVersion)

	match, triedNames := findUpgradeAsset(release.Assets, runtime.GOOS, runtime.GOARCH, latestVersion, strict)
	if match == nil {
		return newUpgradeError(
			runtime.GOOS,
			runtime.GOARCH,
			latestVersion,
			triedNames,
			release.Assets,
			release.HTMLURL,
		)
	}
	asset := match.Asset

	if match.Strategy != "exact_archive" {
		fmt.Printf("  %s Note: using fallback asset discovery (%s)\n",
			warnStyle.Render("⚠"),
			match.Strategy)
		fmt.Printf("    Expected: %s\n", archiveAssetName)
		fmt.Printf("    Found:    %s\n", asset.Name)
		if match.Reason != "" {
			fmt.Printf("    Reason:   %s\n", match.Reason)
		}
		if verbose && len(triedNames) > 0 {
			fmt.Println(dimStyle.Render("    Tried:"))
			for _, name := range triedNames {
				fmt.Printf("      - %s\n", name)
			}
		}
		fmt.Println()
	} else if verbose {
		fmt.Println(dimStyle.Render("  Asset match: exact archive"))
	}

	fmt.Printf("  Download: %s (%s)\n", asset.Name, formatSize(asset.Size))
	fmt.Println()

	// Confirmation prompt
	if !yes {
		fmt.Print(warnStyle.Render("  Upgrade to ") + successStyle.Render(latestVersion) + warnStyle.Render("? [y/N] "))
		reader := bufio.NewReader(os.Stdin)
		response, err := reader.ReadString('\n')
		if err != nil {
			return fmt.Errorf("failed to read response: %w", err)
		}
		response = strings.TrimSpace(strings.ToLower(response))
		if response != "y" && response != "yes" {
			fmt.Println(dimStyle.Render("  Upgrade cancelled"))
			return nil
		}
		fmt.Println()
	}

	// Download the asset
	fmt.Print("  Downloading... ")
	tempDir, err := os.MkdirTemp("", "ntm-upgrade-*")
	if err != nil {
		fmt.Println(errorStyle.Render("✗"))
		return fmt.Errorf("failed to create temp directory: %w", err)
	}
	defer os.RemoveAll(tempDir)

	downloadPath := filepath.Join(tempDir, asset.Name)
	if err := downloadFile(downloadPath, asset.BrowserDownloadURL, asset.Size); err != nil {
		fmt.Println(errorStyle.Render("✗"))
		return fmt.Errorf("failed to download: %w", err)
	}
	fmt.Println(successStyle.Render("✓"))

	// Verify checksum if available
	fmt.Print("  Verifying checksum... ")
	checksums, checksumErr := fetchChecksums(release)
	if checksumErr != nil {
		// checksums.txt not available (old releases or missing)
		fmt.Println(warnStyle.Render("⚠ (not available)"))
		fmt.Println(dimStyle.Render("    checksums.txt not found - skipping verification"))
	} else {
		expectedHash, ok := checksums[asset.Name]
		if !ok {
			// Asset not in checksums file - warn but continue
			fmt.Println(warnStyle.Render("⚠ (not in checksums.txt)"))
			fmt.Println(dimStyle.Render("    asset not listed in checksums.txt - skipping verification"))
		} else {
			// Verify the checksum
			if err := verifyChecksum(downloadPath, expectedHash); err != nil {
				fmt.Println(errorStyle.Render("✗"))
				fmt.Println()
				fmt.Printf("  %s\n", errorStyle.Render("Checksum verification failed!"))
				fmt.Printf("  %s\n", dimStyle.Render("The download may be corrupted."))
				fmt.Println()
				fmt.Println(dimStyle.Render("  Try again, or download manually from:"))
				fmt.Println(dimStyle.Render("  " + release.HTMLURL))
				return fmt.Errorf("checksum verification failed: %w", err)
			}
			fmt.Println(successStyle.Render("✓"))
		}
	}

	// Extract if it's an archive
	var binaryPath string
	if strings.HasSuffix(asset.Name, ".tar.gz") {
		fmt.Print("  Extracting... ")
		binaryPath, err = extractTarGz(downloadPath, tempDir)
		if err != nil {
			fmt.Println(errorStyle.Render("✗"))
			return fmt.Errorf("failed to extract: %w", err)
		}
		fmt.Println(successStyle.Render("✓"))
	} else if strings.HasSuffix(asset.Name, ".zip") {
		fmt.Print("  Extracting... ")
		binaryPath, err = extractZip(downloadPath, tempDir)
		if err != nil {
			fmt.Println(errorStyle.Render("✗"))
			return fmt.Errorf("failed to extract: %w", err)
		}
		fmt.Println(successStyle.Render("✓"))
	} else {
		binaryPath = downloadPath
	}

	// Get current executable path
	execPath, err := os.Executable()
	if err != nil {
		return fmt.Errorf("failed to get executable path: %w", err)
	}
	execPath, err = filepath.EvalSymlinks(execPath)
	if err != nil {
		return fmt.Errorf("failed to resolve executable path: %w", err)
	}

	// Replace the binary
	fmt.Print("  Installing... ")
	if err := replaceBinary(binaryPath, execPath); err != nil {
		fmt.Println(errorStyle.Render("✗"))
		return fmt.Errorf("failed to install: %w", err)
	}
	fmt.Println(successStyle.Render("✓"))

	// Verify the new binary works correctly
	backupPath := execPath + ".old"
	fmt.Print("  Verifying... ")
	if err := verifyUpgrade(execPath, latestVersion); err != nil {
		fmt.Println(errorStyle.Render("✗"))
		fmt.Println()
		fmt.Printf("  %s %s\n", warnStyle.Render("⚠ Verification failed:"), err)
		fmt.Println()
		fmt.Println(dimStyle.Render("  The new binary may be corrupted or incompatible."))

		// Check if backup exists for rollback
		if _, backupErr := os.Stat(backupPath); backupErr == nil {
			fmt.Print(warnStyle.Render("  Restore previous version? [Y/n] "))
			reader := bufio.NewReader(os.Stdin)
			response, readErr := reader.ReadString('\n')
			if readErr != nil {
				// On read error, default to restore for safety
				response = "y"
			}
			response = strings.TrimSpace(strings.ToLower(response))
			if response == "" || response == "y" || response == "yes" {
				if restoreErr := restoreBackup(execPath, backupPath); restoreErr != nil {
					fmt.Printf("  %s Failed to restore: %s\n", errorStyle.Render("✗"), restoreErr)
					return fmt.Errorf("upgrade verification failed and rollback failed: %w", restoreErr)
				}
				fmt.Println(successStyle.Render("  ✓ Previous version restored"))
				fmt.Println()
				fmt.Println(dimStyle.Render("  Please report this issue:"))
				fmt.Println(dimStyle.Render("  https://github.com/Dicklesworthstone/ntm/issues"))
				return fmt.Errorf("upgrade rolled back due to verification failure")
			}
			// User chose not to restore - warn them
			fmt.Println()
			fmt.Println(warnStyle.Render("  ⚠ Keeping potentially broken binary. Backup available at:"))
			fmt.Println(dimStyle.Render("    " + backupPath))
		} else {
			fmt.Println(errorStyle.Render("  No backup available for rollback."))
		}
		return fmt.Errorf("upgrade verification failed: %w", err)
	}
	fmt.Println(successStyle.Render("✓"))

	// Verification passed - safe to remove backup
	os.Remove(backupPath)

	fmt.Println()
	fmt.Println(successStyle.Render("  ✓ Successfully upgraded to " + latestVersion + "!"))
	fmt.Println()

	// Check for legacy shell integration that needs updating
	migrateShellIntegration(warnStyle, successStyle, dimStyle)

	fmt.Println(dimStyle.Render("  Release notes: " + release.HTMLURL))

	return nil
}

// fetchLatestRelease fetches the latest release info from GitHub
func fetchLatestRelease() (*GitHubRelease, error) {
	url := fmt.Sprintf("%s/repos/%s/%s/releases/latest", githubAPI, githubOwner, githubRepo)

	client := &http.Client{Timeout: 30 * time.Second}
	req, err := http.NewRequest("GET", url, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Accept", "application/vnd.github.v3+json")
	req.Header.Set("User-Agent", "ntm-upgrade/"+Version)

	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode == http.StatusNotFound {
		return nil, fmt.Errorf("no releases found - this is a development version")
	}
	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("GitHub API returned %d: %s", resp.StatusCode, string(body))
	}

	var release GitHubRelease
	if err := json.NewDecoder(io.LimitReader(resp.Body, 10*1024*1024)).Decode(&release); err != nil {
		return nil, fmt.Errorf("failed to parse response: %w", err)
	}

	return &release, nil
}

// getAssetName returns the expected asset name prefix for the current platform.
// GoReleaser uses underscore separators and creates universal binaries for macOS.
//
// IMPORTANT: This function is part of the upgrade naming contract with .goreleaser.yaml.
// If you change the naming logic here, you MUST also update:
//   - .goreleaser.yaml (archives.name_template)
//   - TestUpgradeAssetNamingContract in cli_test.go
//
// See CONTRIBUTING.md "Release Infrastructure" section for full documentation.
func getAssetName() string {
	return assetNameFor(runtime.GOOS, runtime.GOARCH)
}

// getArchiveAssetName returns the expected archive asset name for a given version.
// Archive format: ntm_VERSION_OS_ARCH.tar.gz (or .zip for Windows).
//
// IMPORTANT: This function is part of the upgrade naming contract with .goreleaser.yaml.
// If you change the naming logic here, you MUST also update:
//   - .goreleaser.yaml (archives.name_template)
//   - TestUpgradeAssetNamingContract in cli_test.go
//
// See CONTRIBUTING.md "Release Infrastructure" section for full documentation.
func getArchiveAssetName(version string) string {
	return archiveAssetNameFor(version, runtime.GOOS, runtime.GOARCH)
}

func assetNameFor(targetOS, targetArch string) string {
	arch := normalizedArch(targetOS, targetArch)
	return fmt.Sprintf("ntm_%s_%s", targetOS, arch)
}

func archiveAssetNameFor(version, targetOS, targetArch string) string {
	arch := normalizedArch(targetOS, targetArch)
	ext := "tar.gz"
	if targetOS == "windows" {
		ext = "zip"
	}
	return fmt.Sprintf("ntm_%s_%s_%s.%s", version, targetOS, arch, ext)
}

func normalizedArch(targetOS, targetArch string) string {
	arch := targetArch
	// macOS uses universal binary ("all") that works on both amd64 and arm64
	if targetOS == "darwin" {
		arch = "all"
	}
	// 32-bit ARM uses "armv7" suffix (GoReleaser builds with goarm=7)
	if targetArch == "arm" {
		arch = "armv7"
	}
	return arch
}

// progressWriter wraps an io.Writer and reports download progress
type progressWriter struct {
	writer     io.Writer
	total      int64
	downloaded int64
	startTime  time.Time
	lastUpdate time.Time
	isTTY      bool
}

// Write implements io.Writer and updates progress
func (pw *progressWriter) Write(p []byte) (int, error) {
	n, err := pw.writer.Write(p)
	if err != nil {
		return n, err
	}

	pw.downloaded += int64(n)

	// Update progress display at most every 100ms to avoid flickering
	if time.Since(pw.lastUpdate) > 100*time.Millisecond {
		pw.displayProgress()
		pw.lastUpdate = time.Now()
	}

	return n, nil
}

// displayProgress shows the current download progress
func (pw *progressWriter) displayProgress() {
	if pw.total <= 0 {
		// Unknown total size - just show downloaded bytes
		if pw.isTTY {
			fmt.Printf("\r  Downloading... %s", formatSize(pw.downloaded))
		}
		return
	}

	percentage := float64(pw.downloaded) / float64(pw.total) * 100
	elapsed := time.Since(pw.startTime).Seconds()
	var speed float64
	if elapsed > 0 {
		speed = float64(pw.downloaded) / elapsed
	}

	if pw.isTTY {
		// In-place update for TTY
		fmt.Printf("\r  Downloading... %.0f%% %s/%s (%s/s)    ",
			percentage,
			formatSize(pw.downloaded),
			formatSize(pw.total),
			formatSize(int64(speed)))
	}
}

// finish clears the progress line and prepares for the checkmark
func (pw *progressWriter) finish() {
	if pw.isTTY {
		// Clear the progress line and reset cursor
		// Works for both known total (full progress) and unknown total (just bytes)
		fmt.Printf("\r  Downloading... ")
	}
}

// isTerminal checks if stdout is a TTY
func isTerminal() bool {
	fileInfo, err := os.Stdout.Stat()
	if err != nil {
		return false
	}
	return (fileInfo.Mode() & os.ModeCharDevice) != 0
}

// downloadFile downloads a file with progress indication
func downloadFile(destPath string, url string, expectedSize int64) error {
	client := &http.Client{Timeout: 5 * time.Minute}
	resp, err := client.Get(url)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("download failed with status %d", resp.StatusCode)
	}

	// Use Content-Length if available, otherwise fall back to expectedSize
	totalSize := resp.ContentLength
	if totalSize <= 0 {
		totalSize = expectedSize
	}

	out, err := os.Create(destPath)
	if err != nil {
		return err
	}
	defer out.Close()

	// Wrap the file writer with progress tracking
	pw := &progressWriter{
		writer:     out,
		total:      totalSize,
		startTime:  time.Now(),
		lastUpdate: time.Now(),
		isTTY:      isTerminal(),
	}

	_, err = io.Copy(pw, resp.Body)
	pw.finish()
	return err
}

func findChecksumsAsset(assets []GitHubAsset) *GitHubAsset {
	preferredNames := []string{"SHA256SUMS", "checksums.txt", "sha256sums.txt"}
	for _, preferred := range preferredNames {
		for i := range assets {
			if strings.EqualFold(assets[i].Name, preferred) {
				return &assets[i]
			}
		}
	}
	return nil
}

func parseChecksums(r io.Reader) (map[string]string, error) {
	checksums := make(map[string]string)
	scanner := bufio.NewScanner(r)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}

		parts := strings.Fields(line)
		if len(parts) < 2 {
			continue
		}

		hash := parts[0]
		filename := filepath.Base(parts[len(parts)-1])
		checksums[filename] = hash
	}

	if err := scanner.Err(); err != nil {
		return nil, fmt.Errorf("failed to read checksums: %w", err)
	}
	if len(checksums) == 0 {
		return nil, fmt.Errorf("no checksums found in checksum file")
	}

	return checksums, nil
}

// fetchChecksums downloads and parses the checksum asset from a GitHub release.
// The release pipeline may publish either SHA256SUMS or checksums.txt depending on the path used.
// Format: "<sha256hash>  <filename>" or "<sha256hash> <filename>".
func fetchChecksums(release *GitHubRelease) (map[string]string, error) {
	checksumAsset := findChecksumsAsset(release.Assets)
	if checksumAsset == nil {
		return nil, fmt.Errorf("checksum asset not found in release")
	}

	client := &http.Client{Timeout: 30 * time.Second}
	resp, err := client.Get(checksumAsset.BrowserDownloadURL)
	if err != nil {
		return nil, fmt.Errorf("failed to download checksums: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("checksums download failed with status %d", resp.StatusCode)
	}

	return parseChecksums(resp.Body)
}

// verifyChecksum computes the SHA256 hash of a file and compares it to the expected hash.
// Returns nil if the checksum matches, or an error describing the mismatch.
func verifyChecksum(filePath string, expectedHash string) error {
	f, err := os.Open(filePath)
	if err != nil {
		return fmt.Errorf("failed to open file for checksum: %w", err)
	}
	defer f.Close()

	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return fmt.Errorf("failed to compute checksum: %w", err)
	}

	actualHash := hex.EncodeToString(h.Sum(nil))
	expectedHash = strings.ToLower(strings.TrimSpace(expectedHash))
	actualHash = strings.ToLower(actualHash)

	if actualHash != expectedHash {
		return fmt.Errorf("checksum mismatch: expected %s, got %s", expectedHash, actualHash)
	}

	return nil
}

// extractTarGz extracts a tar.gz file and returns the path to the ntm binary
func extractTarGz(archivePath, destDir string) (string, error) {
	f, err := os.Open(archivePath)
	if err != nil {
		return "", err
	}
	defer f.Close()

	gzr, err := gzip.NewReader(f)
	if err != nil {
		return "", err
	}
	defer gzr.Close()

	tr := tar.NewReader(gzr)

	var binaryPath string
	for {
		header, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			return "", err
		}

		target := filepath.Join(destDir, header.Name)
		// Check for Zip Slip vulnerability
		if !strings.HasPrefix(target, filepath.Clean(destDir)+string(os.PathSeparator)) {
			return "", fmt.Errorf("illegal file path in archive: %s", header.Name)
		}

		switch header.Typeflag {
		case tar.TypeDir:
			if err := os.MkdirAll(target, 0755); err != nil {
				return "", err
			}
		case tar.TypeReg:
			// Check if this is the ntm binary
			if header.Name == "ntm" || filepath.Base(header.Name) == "ntm" {
				binaryPath = target
			}

			outFile, err := os.OpenFile(target, os.O_CREATE|os.O_RDWR|os.O_TRUNC, os.FileMode(header.Mode))
			if err != nil {
				return "", err
			}
			if _, err := io.Copy(outFile, tr); err != nil {
				outFile.Close()
				return "", err
			}
			outFile.Close()
		}
	}

	if binaryPath == "" {
		return "", fmt.Errorf("ntm binary not found in archive")
	}

	return binaryPath, nil
}

// extractZip extracts a zip file and returns the path to the ntm binary
func extractZip(archivePath, destDir string) (string, error) {
	r, err := zip.OpenReader(archivePath)
	if err != nil {
		return "", err
	}
	defer r.Close()

	var binaryPath string
	binaryName := "ntm"
	if runtime.GOOS == "windows" {
		binaryName = "ntm.exe"
	}

	for _, f := range r.File {
		target := filepath.Join(destDir, f.Name)
		// Check for Zip Slip vulnerability
		if !strings.HasPrefix(target, filepath.Clean(destDir)+string(os.PathSeparator)) {
			return "", fmt.Errorf("illegal file path in archive: %s", f.Name)
		}

		if f.FileInfo().IsDir() {
			if err := os.MkdirAll(target, 0755); err != nil {
				return "", err
			}
			continue
		}

		// Check if this is the ntm binary
		if f.Name == binaryName || filepath.Base(f.Name) == binaryName {
			binaryPath = target
		}

		if err := os.MkdirAll(filepath.Dir(target), 0755); err != nil {
			return "", err
		}

		outFile, err := os.OpenFile(target, os.O_CREATE|os.O_RDWR|os.O_TRUNC, f.Mode())
		if err != nil {
			return "", err
		}

		rc, err := f.Open()
		if err != nil {
			outFile.Close()
			return "", err
		}

		_, err = io.Copy(outFile, rc)
		rc.Close()
		outFile.Close()
		if err != nil {
			return "", err
		}
	}

	if binaryPath == "" {
		return "", fmt.Errorf("ntm binary not found in archive")
	}

	return binaryPath, nil
}

// replaceBinary replaces the current binary with a new one atomically
func replaceBinary(newBinaryPath, currentBinaryPath string) error {
	// Create a temporary file in the same directory as the target
	// This ensures we can atomically rename it later (same filesystem)
	dstDir := filepath.Dir(currentBinaryPath)
	tmpDstName := filepath.Base(currentBinaryPath) + ".new"
	tmpDstPath := filepath.Join(dstDir, tmpDstName)

	// Clean up any previous failed attempt
	os.Remove(tmpDstPath)

	// Copy new binary to the temporary destination
	srcFile, err := os.Open(newBinaryPath)
	if err != nil {
		return fmt.Errorf("failed to open new binary: %w", err)
	}
	defer srcFile.Close()

	dstFile, err := os.OpenFile(tmpDstPath, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0755)
	if err != nil {
		return fmt.Errorf("failed to create temp binary: %w", err)
	}
	// Ensure we close and remove if something fails before the rename
	defer func() {
		if dstFile != nil {
			dstFile.Close()
		}
		// Only remove if it still exists (rename moves it)
		if _, err := os.Stat(tmpDstPath); err == nil {
			os.Remove(tmpDstPath)
		}
	}()

	if _, err := io.Copy(dstFile, srcFile); err != nil {
		return fmt.Errorf("failed to copy binary: %w", err)
	}

	// Ensure data is flushed to disk and close before rename
	if err := dstFile.Sync(); err != nil {
		return fmt.Errorf("failed to sync binary: %w", err)
	}
	if err := dstFile.Close(); err != nil {
		return fmt.Errorf("failed to close temp binary: %w", err)
	}
	dstFile = nil // Prevent defer from closing again

	// Rename the current binary to .old (backup) to allow rollback if needed,
	// and also to work around Windows locking issues if running.
	// On Unix we can rename over it directly, but Windows prevents it if running.
	// Common strategy: Rename old -> old.bak, Rename new -> old.
	backupPath := currentBinaryPath + ".old"
	os.Remove(backupPath) // Remove ancient backup

	if err := os.Rename(currentBinaryPath, backupPath); err != nil {
		return fmt.Errorf("failed to backup current binary: %w", err)
	}

	// Rename the new binary to the target path
	if err := os.Rename(tmpDstPath, currentBinaryPath); err != nil {
		// Try to restore backup
		os.Rename(backupPath, currentBinaryPath)
		return fmt.Errorf("failed to install new binary: %w", err)
	}

	// Success! Keep backup until verification completes.
	// The backup will be removed after verifyUpgrade succeeds.
	return nil
}

// verifyUpgrade runs the new binary with "version --short" and verifies
// it returns the expected version. This catches corrupted downloads,
// wrong-architecture binaries (e.g., x64 on ARM without Rosetta),
// and other GoReleaser misconfigurations.
//
// If verification fails, the caller should offer to restore from backup.
func verifyUpgrade(binaryPath, expectedVersion string) error {
	// Run the new binary with version flag
	cmd := exec.Command(binaryPath, "version", "--short")
	output, err := cmd.Output()
	if err != nil {
		// Check if it's an exec error (binary won't run at all)
		if exitErr, ok := err.(*exec.ExitError); ok {
			return fmt.Errorf("new binary exited with code %d: %s", exitErr.ExitCode(), string(exitErr.Stderr))
		}
		return fmt.Errorf("failed to run new binary: %w", err)
	}

	// Parse the version from output
	actualVersion := strings.TrimSpace(string(output))

	// Normalize both versions for comparison
	normalizedExpected := normalizeVersion(expectedVersion)
	normalizedActual := normalizeVersion(actualVersion)

	// Check if the actual version matches expected
	// Use flexible matching: actual should contain expected or be equal when normalized
	if normalizedActual != normalizedExpected && !strings.Contains(actualVersion, normalizedExpected) {
		return fmt.Errorf("version mismatch: expected %s, got %s", expectedVersion, actualVersion)
	}

	return nil
}

// restoreBackup restores the previous binary from backup
func restoreBackup(currentPath, backupPath string) error {
	// Check if backup exists
	if _, err := os.Stat(backupPath); os.IsNotExist(err) {
		return fmt.Errorf("backup file not found at %s", backupPath)
	}

	// Remove the failed new binary
	if err := os.Remove(currentPath); err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("failed to remove new binary: %w", err)
	}

	// Restore backup
	if err := os.Rename(backupPath, currentPath); err != nil {
		return fmt.Errorf("failed to restore backup: %w", err)
	}

	return nil
}

// isNewerVersion compares two version strings and returns true if latest is newer
func isNewerVersion(current, latest string) bool {
	current = normalizeVersion(current)
	latest = normalizeVersion(latest)

	// Handle dev versions
	if current == "dev" || current == "" {
		return true
	}

	// Simple version comparison (assumes semver-like versions)
	currentParts := strings.Split(current, ".")
	latestParts := strings.Split(latest, ".")

	// Pad to same length
	for len(currentParts) < len(latestParts) {
		currentParts = append(currentParts, "0")
	}
	for len(latestParts) < len(currentParts) {
		latestParts = append(latestParts, "0")
	}

	for i := 0; i < len(currentParts); i++ {
		c := parseVersionPart(currentParts[i])
		l := parseVersionPart(latestParts[i])
		if l > c {
			return true
		}
		if c > l {
			return false
		}
	}

	return false
}

// normalizeVersion removes 'v' prefix and any suffixes
func normalizeVersion(v string) string {
	v = strings.TrimPrefix(v, "v")
	// Remove suffixes like -beta, -rc, -next, etc. for comparison
	if idx := strings.IndexAny(v, "-+"); idx != -1 {
		v = v[:idx]
	}
	return v
}

// parseVersionPart parses a version part as an integer
func parseVersionPart(part string) int {
	var n int
	fmt.Sscanf(part, "%d", &n)
	return n
}

// formatSize formats a byte count as a human-readable string
func formatSize(bytes int64) string {
	const unit = 1024
	if bytes < unit {
		return fmt.Sprintf("%d B", bytes)
	}
	div, exp := int64(unit), 0
	for n := bytes / unit; n >= unit; n /= unit {
		div *= unit
		exp++
	}
	return fmt.Sprintf("%.1f %cB", float64(bytes)/float64(div), "KMGTPE"[exp])
}

// migrateShellIntegration checks for and offers to migrate legacy "ntm init" shell
// integration to the new "ntm shell" command. This handles users upgrading from
// v1.5.0 or earlier where "ntm init <shell>" was used instead of "ntm shell <shell>".
func migrateShellIntegration(warnStyle, successStyle, dimStyle lipgloss.Style) {
	// Find shell rc files that might have legacy integration
	rcFiles := []string{
		filepath.Join(os.Getenv("HOME"), ".bashrc"),
		filepath.Join(os.Getenv("HOME"), ".zshrc"),
		filepath.Join(os.Getenv("HOME"), ".config", "fish", "config.fish"),
	}

	var legacyFiles []string
	for _, rcFile := range rcFiles {
		if hasLegacyShellIntegration(rcFile) {
			legacyFiles = append(legacyFiles, rcFile)
		}
	}

	if len(legacyFiles) == 0 {
		return
	}

	fmt.Println()
	fmt.Printf("  %s Legacy shell integration detected\n", warnStyle.Render("⚠"))
	fmt.Println(dimStyle.Render("    'ntm init' was renamed to 'ntm shell' in v1.6.0"))
	fmt.Println()
	fmt.Println("  Files with legacy integration:")
	for _, f := range legacyFiles {
		fmt.Printf("    • %s\n", f)
	}
	fmt.Println()

	// Prompt for migration
	fmt.Print(warnStyle.Render("  Update to 'ntm shell' automatically? [Y/n] "))
	reader := bufio.NewReader(os.Stdin)
	response, err := reader.ReadString('\n')
	if err != nil {
		fmt.Println(dimStyle.Render("  (could not read response, skipping)"))
		fmt.Println()
		fmt.Println(dimStyle.Render("  To update manually, replace 'ntm init' with 'ntm shell' in your rc files."))
		return
	}
	response = strings.TrimSpace(strings.ToLower(response))

	if response == "n" || response == "no" {
		fmt.Println()
		fmt.Println(dimStyle.Render("  To update manually, replace 'ntm init' with 'ntm shell' in your rc files."))
		return
	}

	// Perform migration
	fmt.Println()
	for _, rcFile := range legacyFiles {
		if err := upgradeShellRCFile(rcFile); err != nil {
			fmt.Printf("  %s Failed to update %s: %s\n", warnStyle.Render("⚠"), rcFile, err)
		} else {
			fmt.Printf("  %s Updated %s\n", successStyle.Render("✓"), rcFile)
		}
	}
	fmt.Println()
	fmt.Println(dimStyle.Render("  Restart your shell or source your rc file to activate."))
}

// hasLegacyShellIntegration checks if a file contains legacy "ntm init" commands
func hasLegacyShellIntegration(filePath string) bool {
	content, err := os.ReadFile(filePath)
	if err != nil {
		return false
	}
	return strings.Contains(string(content), "ntm init")
}

// upgradeShellRCFile replaces "ntm init" with "ntm shell" in a shell rc file
func upgradeShellRCFile(filePath string) error {
	content, err := os.ReadFile(filePath)
	if err != nil {
		return err
	}

	// Create backup
	backupPath := filePath + ".ntm-backup"
	if err := os.WriteFile(backupPath, content, 0644); err != nil {
		return fmt.Errorf("failed to create backup: %w", err)
	}

	// Replace "ntm init" with "ntm shell"
	newContent := strings.ReplaceAll(string(content), "ntm init", "ntm shell")

	// Write updated content
	if err := os.WriteFile(filePath, []byte(newContent), 0644); err != nil {
		// Try to restore backup
		os.WriteFile(filePath, content, 0644)
		return err
	}

	return nil
}
