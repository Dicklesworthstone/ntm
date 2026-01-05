package cli

import (
	"archive/tar"
	"archive/zip"
	"bufio"
	"compress/gzip"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
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
		case "close":
			marker = warnStyle.Render("≈")
			suffix = warnStyle.Render(" ← closest match")
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
		} else if info.Arch == "amd64" || info.Arch == "arm64" {
			// Different specific arch - close match for same OS
			info.Match = "close"
			info.Reason = fmt.Sprintf("same OS, different arch (got %s, want %s)", info.Arch, targetArch)
		}
	}

	return info
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

		// Track closest match
		if info.Match == "close" && err.ClosestMatch == nil {
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

	cmd := &cobra.Command{
		Use:   "upgrade",
		Short: "Upgrade NTM to the latest version",
		Long: `Check for and install the latest version of NTM from GitHub releases.

Examples:
  ntm upgrade           # Check and upgrade (with confirmation)
  ntm upgrade --check   # Only check for updates, don't install
  ntm upgrade --yes     # Auto-confirm, skip confirmation prompt
  ntm upgrade --force   # Force reinstall even if already on latest`,
		RunE: func(cmd *cobra.Command, args []string) error {
			return runUpgrade(checkOnly, force, yes)
		},
	}

	cmd.Flags().BoolVar(&checkOnly, "check", false, "Only check for updates, don't install")
	cmd.Flags().BoolVarP(&force, "force", "f", false, "Force reinstall even if already on latest version")
	cmd.Flags().BoolVarP(&yes, "yes", "y", false, "Auto-confirm upgrade without prompting")

	return cmd
}

func runUpgrade(checkOnly, force, yes bool) error {
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
	binaryAssetName := getAssetName() // e.g., ntm_darwin_all

	var asset *GitHubAsset
	for i := range release.Assets {
		// Exact match for versioned archive (preferred)
		if release.Assets[i].Name == archiveAssetName {
			asset = &release.Assets[i]
			break
		}
	}

	if asset == nil {
		// Try raw binary name (without version)
		for i := range release.Assets {
			if release.Assets[i].Name == binaryAssetName {
				asset = &release.Assets[i]
				break
			}
		}
	}

	if asset == nil {
		// Try prefix matching as fallback (e.g., for arm variants like armv7)
		for i := range release.Assets {
			name := release.Assets[i].Name
			if strings.HasPrefix(name, binaryAssetName) ||
				strings.HasPrefix(name, fmt.Sprintf("ntm_%s_%s", latestVersion, runtime.GOOS)) {
				asset = &release.Assets[i]
				break
			}
		}
	}

	if asset == nil {
		triedNames := []string{archiveAssetName, binaryAssetName}
		return newUpgradeError(
			runtime.GOOS,
			runtime.GOARCH,
			latestVersion,
			triedNames,
			release.Assets,
			release.HTMLURL,
		)
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
	if err := downloadFile(downloadPath, asset.BrowserDownloadURL); err != nil {
		fmt.Println(errorStyle.Render("✗"))
		return fmt.Errorf("failed to download: %w", err)
	}
	fmt.Println(successStyle.Render("✓"))

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

	fmt.Println()
	fmt.Println(successStyle.Render("  ✓ Successfully upgraded to " + latestVersion + "!"))
	fmt.Println()
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
	if err := json.NewDecoder(resp.Body).Decode(&release); err != nil {
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
	arch := runtime.GOARCH
	// macOS uses universal binary ("all") that works on both amd64 and arm64
	if runtime.GOOS == "darwin" {
		arch = "all"
	}
	return fmt.Sprintf("ntm_%s_%s", runtime.GOOS, arch)
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
	arch := runtime.GOARCH
	// macOS uses universal binary ("all") that works on both amd64 and arm64
	if runtime.GOOS == "darwin" {
		arch = "all"
	}
	ext := "tar.gz"
	if runtime.GOOS == "windows" {
		ext = "zip"
	}
	return fmt.Sprintf("ntm_%s_%s_%s.%s", version, runtime.GOOS, arch, ext)
}

// downloadFile downloads a file with progress indication
func downloadFile(destPath string, url string) error {
	client := &http.Client{Timeout: 5 * time.Minute}
	resp, err := client.Get(url)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("download failed with status %d", resp.StatusCode)
	}

	out, err := os.Create(destPath)
	if err != nil {
		return err
	}
	defer out.Close()

	_, err = io.Copy(out, resp.Body)
	return err
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
		dstFile.Close()
		// Only remove if it still exists (rename moves it)
		if _, err := os.Stat(tmpDstPath); err == nil {
			os.Remove(tmpDstPath)
		}
	}()

	if _, err := io.Copy(dstFile, srcFile); err != nil {
		return fmt.Errorf("failed to copy binary: %w", err)
	}

	// Ensure data is flushed to disk
	if err := dstFile.Sync(); err != nil {
		return fmt.Errorf("failed to sync binary: %w", err)
	}
	dstFile.Close()

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

	// Success! Remove backup (or leave it? Usually nice to clean up)
	// We'll leave it for manual recovery in extreme cases, or remove it?
	// Let's remove it to keep clean.
	os.Remove(backupPath)

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
