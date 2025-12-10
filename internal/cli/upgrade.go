package cli

import (
	"archive/tar"
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
	TagName     string         `json:"tag_name"`
	Name        string         `json:"name"`
	Draft       bool           `json:"draft"`
	Prerelease  bool           `json:"prerelease"`
	PublishedAt time.Time      `json:"published_at"`
	Body        string         `json:"body"`
	Assets      []GitHubAsset  `json:"assets"`
	HTMLURL     string         `json:"html_url"`
}

// GitHubAsset represents a release asset
type GitHubAsset struct {
	Name               string `json:"name"`
	Size               int64  `json:"size"`
	BrowserDownloadURL string `json:"browser_download_url"`
	ContentType        string `json:"content_type"`
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
	assetName := getAssetName()
	var asset *GitHubAsset
	for i := range release.Assets {
		if release.Assets[i].Name == assetName || release.Assets[i].Name == assetName+".tar.gz" {
			asset = &release.Assets[i]
			break
		}
	}

	if asset == nil {
		// Try without extension (raw binary)
		for i := range release.Assets {
			if strings.HasPrefix(release.Assets[i].Name, assetName) {
				asset = &release.Assets[i]
				break
			}
		}
	}

	if asset == nil {
		return fmt.Errorf("no suitable release asset found for %s/%s\nAvailable assets: %v",
			runtime.GOOS, runtime.GOARCH, getAssetNames(release.Assets))
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

	// Extract if it's a tar.gz
	var binaryPath string
	if strings.HasSuffix(asset.Name, ".tar.gz") {
		fmt.Print("  Extracting... ")
		binaryPath, err = extractTarGz(downloadPath, tempDir)
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

// getAssetName returns the expected asset name for the current platform
func getAssetName() string {
	return fmt.Sprintf("ntm-%s-%s", runtime.GOOS, runtime.GOARCH)
}

// getAssetNames returns a list of asset names for debugging
func getAssetNames(assets []GitHubAsset) []string {
	names := make([]string, len(assets))
	for i, a := range assets {
		names[i] = a.Name
	}
	return names
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

// replaceBinary replaces the current binary with a new one
func replaceBinary(newBinaryPath, currentBinaryPath string) error {
	// Make the new binary executable
	if err := os.Chmod(newBinaryPath, 0755); err != nil {
		return fmt.Errorf("failed to set permissions: %w", err)
	}

	// On Unix, we can atomically rename over the running binary
	// The old binary stays in memory until the process exits

	// First, try to backup the current binary
	backupPath := currentBinaryPath + ".backup"
	_ = os.Remove(backupPath) // Remove any existing backup

	// Rename current to backup
	if err := os.Rename(currentBinaryPath, backupPath); err != nil {
		return fmt.Errorf("failed to backup current binary: %w", err)
	}

	// Copy new binary to the target location
	// (We copy instead of rename because they might be on different filesystems)
	newFile, err := os.Open(newBinaryPath)
	if err != nil {
		// Restore backup
		os.Rename(backupPath, currentBinaryPath)
		return fmt.Errorf("failed to open new binary: %w", err)
	}
	defer newFile.Close()

	destFile, err := os.OpenFile(currentBinaryPath, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0755)
	if err != nil {
		// Restore backup
		os.Rename(backupPath, currentBinaryPath)
		return fmt.Errorf("failed to create destination: %w", err)
	}

	if _, err := io.Copy(destFile, newFile); err != nil {
		destFile.Close()
		// Restore backup
		os.Remove(currentBinaryPath)
		os.Rename(backupPath, currentBinaryPath)
		return fmt.Errorf("failed to copy binary: %w", err)
	}

	// Close file before setting permissions (ensure data is flushed)
	if err := destFile.Close(); err != nil {
		os.Remove(currentBinaryPath)
		os.Rename(backupPath, currentBinaryPath)
		return fmt.Errorf("failed to finalize binary: %w", err)
	}

	// Set executable permissions
	if err := os.Chmod(currentBinaryPath, 0755); err != nil {
		return fmt.Errorf("failed to set executable permissions: %w", err)
	}

	// Remove backup on success
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
