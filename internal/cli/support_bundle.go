package cli

import (
	"archive/zip"
	"bufio"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"time"

	"github.com/BurntSushi/toml"
	"github.com/spf13/cobra"

	"github.com/Dicklesworthstone/ntm/internal/config"
	"github.com/Dicklesworthstone/ntm/internal/redaction"
	"github.com/Dicklesworthstone/ntm/internal/robot"
	"github.com/Dicklesworthstone/ntm/internal/tmux"
	"github.com/Dicklesworthstone/ntm/internal/util"
)

// supportBundleOptions holds the command options.
type supportBundleOptions struct {
	Output          string
	Session         string
	Panes           string
	Lines           int
	Since           string
	MaxSize         int64
	NoRedact        bool
	IncludeSessions bool
	IncludeEvents   bool
	RobotMode       bool
}

func newSupportBundleCmd() *cobra.Command {
	opts := supportBundleOptions{
		Lines:           200,
		MaxSize:         50 * 1024 * 1024, // 50MB default max
		IncludeSessions: false,
		IncludeEvents:   true,
	}

	cmd := &cobra.Command{
		Use:   "support-bundle",
		Short: "Generate a diagnostic support bundle archive",
		Long: `Generate a support bundle archive containing diagnostic information
for troubleshooting NTM issues. The bundle includes:

  - manifest.json      Versioned schema with checksums
  - doctor.json        System health check results
  - config_effective.toml  Effective configuration (redacted)
  - versions.json      NTM, Go, tmux, and tool versions
  - events.jsonl       Recent events (configurable time window)
  - sessions/          Session snapshots (optional)

By default, sensitive data is redacted using the configured redaction engine.
Use --no-redact to include raw data (only for trusted recipients).

Examples:
  ntm support-bundle --output bundle.zip
  ntm support-bundle --session myproj --lines 500 -o bundle.zip
  ntm support-bundle --since 2h -o bundle.zip
  ntm support-bundle --include-sessions -o bundle.zip`,
		RunE: func(cmd *cobra.Command, args []string) error {
			return runSupportBundle(cmd.Context(), opts)
		},
	}

	cmd.Flags().StringVarP(&opts.Output, "output", "o", "", "Output file path (required)")
	cmd.Flags().StringVar(&opts.Session, "session", "", "Include specific session only")
	cmd.Flags().StringVar(&opts.Panes, "panes", "", "Pane indices to include (comma-separated)")
	cmd.Flags().IntVar(&opts.Lines, "lines", opts.Lines, "Lines of pane output to capture")
	cmd.Flags().StringVar(&opts.Since, "since", "", "Include events since duration (e.g., 2h, 24h)")
	cmd.Flags().Int64Var(&opts.MaxSize, "max-size", opts.MaxSize, "Maximum bundle size in bytes")
	cmd.Flags().BoolVar(&opts.NoRedact, "no-redact", false, "Skip redaction (unsafe)")
	cmd.Flags().BoolVar(&opts.IncludeSessions, "include-sessions", opts.IncludeSessions, "Include session snapshots and pane output")
	cmd.Flags().BoolVar(&opts.IncludeEvents, "include-events", opts.IncludeEvents, "Include events log")
	cmd.Flags().BoolVar(&opts.RobotMode, "json", false, "Output result as JSON")

	_ = cmd.MarkFlagRequired("output")

	return cmd
}

// BundleManifest describes the support bundle contents.
type BundleManifest struct {
	Version       string         `json:"version"`
	SchemaVersion int            `json:"schema_version"`
	CreatedAt     time.Time      `json:"created_at"`
	CreatedBy     string         `json:"created_by"`
	Files         []ManifestFile `json:"files"`
	Checksum      string         `json:"checksum"`
	Redacted      bool           `json:"redacted"`
	Platform      PlatformInfo   `json:"platform"`
}

// ManifestFile describes a file in the bundle.
type ManifestFile struct {
	Path     string `json:"path"`
	Size     int64  `json:"size"`
	Checksum string `json:"checksum"`
	Redacted bool   `json:"redacted,omitempty"`
}

// PlatformInfo captures runtime environment details.
type PlatformInfo struct {
	OS     string `json:"os"`
	Arch   string `json:"arch"`
	NumCPU int    `json:"num_cpu"`
}

// VersionsInfo captures tool versions.
type VersionsInfo struct {
	NTM       string            `json:"ntm"`
	Go        string            `json:"go"`
	Tmux      string            `json:"tmux"`
	Platform  string            `json:"platform"`
	BuildTime string            `json:"build_time,omitempty"`
	Tools     map[string]string `json:"tools"`
}

// BundleResult is the robot mode response.
type BundleResult struct {
	Success  bool   `json:"success"`
	Path     string `json:"path"`
	Size     int64  `json:"size"`
	Files    int    `json:"files"`
	Redacted bool   `json:"redacted"`
	Error    string `json:"error,omitempty"`
}

func runSupportBundle(ctx context.Context, opts supportBundleOptions) error {
	if ctx == nil {
		ctx = context.Background()
	}

	// Validate output path
	if opts.Output == "" {
		return fmt.Errorf("output path is required")
	}

	// Expand path
	outputPath := util.ExpandPath(opts.Output)
	if !strings.HasSuffix(outputPath, ".zip") {
		outputPath += ".zip"
	}

	// Create the bundle
	result, err := createSupportBundle(ctx, outputPath, opts)
	if err != nil {
		if opts.RobotMode {
			return robot.Output(BundleResult{
				Success: false,
				Error:   err.Error(),
			}, robot.FormatJSON)
		}
		return err
	}

	if opts.RobotMode {
		return robot.Output(result, robot.FormatJSON)
	}

	fmt.Printf("Support bundle created: %s\n", result.Path)
	fmt.Printf("  Size: %d bytes\n", result.Size)
	fmt.Printf("  Files: %d\n", result.Files)
	fmt.Printf("  Redacted: %v\n", result.Redacted)
	return nil
}

func createSupportBundle(ctx context.Context, outputPath string, opts supportBundleOptions) (result *BundleResult, retErr error) {
	// Create output directory if needed
	dir := filepath.Dir(outputPath)
	if err := os.MkdirAll(dir, 0755); err != nil {
		return nil, fmt.Errorf("create output directory: %w", err)
	}

	// Create zip file
	zipFile, err := os.Create(outputPath)
	if err != nil {
		return nil, fmt.Errorf("create zip file: %w", err)
	}
	defer func() {
		if zipFile == nil {
			return
		}
		if err := zipFile.Close(); err != nil && retErr == nil {
			retErr = fmt.Errorf("close zip file: %w", err)
		}
	}()

	zipWriter := zip.NewWriter(zipFile)
	defer func() {
		if zipWriter == nil {
			return
		}
		if err := zipWriter.Close(); err != nil && retErr == nil {
			retErr = fmt.Errorf("close zip: %w", err)
		}
	}()

	manifest := &BundleManifest{
		Version:       "1.0.0",
		SchemaVersion: 1,
		CreatedAt:     time.Now().UTC(),
		CreatedBy:     "ntm support-bundle",
		Redacted:      !opts.NoRedact,
		Platform: PlatformInfo{
			OS:     runtime.GOOS,
			Arch:   runtime.GOARCH,
			NumCPU: runtime.NumCPU(),
		},
	}

	var files []ManifestFile

	// Get redaction config if enabled
	var redactCfg *redaction.Config
	if !opts.NoRedact && cfg != nil {
		redactCfg = buildBundleRedactionConfig(cfg)
	}

	// 1. Add versions.json
	versionsData, err := collectBundleVersions(ctx)
	if err == nil {
		if file, err := addJSONToBundle(zipWriter, "versions.json", versionsData); err == nil {
			files = append(files, file)
		}
	}

	// 2. Add doctor.json
	doctorReport := performDoctorCheck(ctx)
	if file, err := addJSONToBundle(zipWriter, "doctor.json", doctorReport); err == nil {
		files = append(files, file)
	}

	// 3. Add config_effective.toml (redacted)
	if cfg != nil {
		configData, err := exportBundleConfigTOML(cfg, redactCfg)
		if err == nil {
			if file, err := addDataToBundle(zipWriter, "config_effective.toml", configData); err == nil {
				file.Redacted = redactCfg != nil
				files = append(files, file)
			}
		}
	}

	// 4. Add events.jsonl (bounded)
	if opts.IncludeEvents {
		eventsData, err := collectBundleEvents(opts.Since, redactCfg)
		if err == nil && len(eventsData) > 0 {
			if file, err := addDataToBundle(zipWriter, "events.jsonl", eventsData); err == nil {
				file.Redacted = redactCfg != nil
				files = append(files, file)
			}
		}
	}

	// 5. Add session snapshots (optional)
	if opts.IncludeSessions {
		sessionFiles, err := collectBundleSessionSnapshots(ctx, opts, redactCfg)
		if err == nil {
			for _, sf := range sessionFiles {
				if file, err := addDataToBundle(zipWriter, sf.path, sf.data); err == nil {
					file.Redacted = redactCfg != nil
					files = append(files, file)
				}
			}
		}
	}

	// Finalize manifest
	manifest.Files = files

	// Calculate overall checksum
	var checksumData strings.Builder
	for _, f := range files {
		checksumData.WriteString(f.Path)
		checksumData.WriteString(f.Checksum)
	}
	manifest.Checksum = bundleSHA256([]byte(checksumData.String()))

	// Add manifest as last file
	if file, err := addJSONToBundle(zipWriter, "manifest.json", manifest); err == nil {
		files = append(files, file)
	}

	// Close zip writer to flush
	if err := zipWriter.Close(); err != nil {
		return nil, fmt.Errorf("close zip: %w", err)
	}
	zipWriter = nil

	// Close file before stat so any buffered writes are fully persisted.
	if err := zipFile.Close(); err != nil {
		return nil, fmt.Errorf("close zip file: %w", err)
	}
	zipFile = nil

	// Get final file size
	info, err := os.Stat(outputPath)
	if err != nil {
		return nil, fmt.Errorf("stat output: %w", err)
	}

	// Check size limit
	if info.Size() > opts.MaxSize {
		os.Remove(outputPath)
		return nil, fmt.Errorf("bundle size %d exceeds limit %d", info.Size(), opts.MaxSize)
	}

	return &BundleResult{
		Success:  true,
		Path:     outputPath,
		Size:     info.Size(),
		Files:    len(files),
		Redacted: !opts.NoRedact,
	}, nil
}

func collectBundleVersions(ctx context.Context) (*VersionsInfo, error) {
	versions := &VersionsInfo{
		NTM:      Version,
		Go:       runtime.Version(),
		Platform: fmt.Sprintf("%s/%s", runtime.GOOS, runtime.GOARCH),
		Tools:    make(map[string]string),
	}

	// Get tmux version
	if tmux.DefaultClient.IsInstalled() {
		cmd := exec.CommandContext(ctx, tmux.BinaryPath(), "-V")
		if out, err := cmd.Output(); err == nil {
			versions.Tmux = strings.TrimSpace(string(out))
		}
	}

	// Get tool versions (non-blocking)
	toolChecks := []string{"bv", "br", "cm", "cass", "ubs", "dcg"}
	for _, tool := range toolChecks {
		if path, err := exec.LookPath(tool); err == nil {
			cmd := exec.CommandContext(ctx, path, "--version")
			cmd.Env = append(os.Environ(), "NO_COLOR=1")
			if out, err := cmd.Output(); err == nil {
				version := strings.TrimSpace(string(out))
				// Take first line only
				if idx := strings.Index(version, "\n"); idx > 0 {
					version = version[:idx]
				}
				versions.Tools[tool] = version
			}
		}
	}

	return versions, nil
}

func exportBundleConfigTOML(cfg *config.Config, redactCfg *redaction.Config) ([]byte, error) {
	// Create a copy and redact sensitive fields
	var buf strings.Builder
	encoder := toml.NewEncoder(&buf)
	if err := encoder.Encode(cfg); err != nil {
		return nil, err
	}

	data := buf.String()

	// Apply redaction if enabled
	if redactCfg != nil {
		data, _ = redaction.Redact(data, *redactCfg)
	}

	return []byte(data), nil
}

func collectBundleEvents(sinceStr string, redactCfg *redaction.Config) ([]byte, error) {
	// Default: last 24 hours
	since := time.Now().Add(-24 * time.Hour)

	if sinceStr != "" {
		dur, err := time.ParseDuration(sinceStr)
		if err == nil {
			since = time.Now().Add(-dur)
		}
	}

	// Read events log
	eventsPath := util.ExpandPath("~/.config/ntm/analytics/events.jsonl")
	file, err := os.Open(eventsPath)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	defer func() {
		// Best-effort close: event logs are read-only here, but we still check the error
		// so scanners don't treat it as an accidentally-ignored Close() result.
		if err := file.Close(); err != nil {
			// Intentionally ignored.
		}
	}()

	var buf strings.Builder
	scanner := bufio.NewScanner(file)
	scanner.Buffer(make([]byte, 1024*1024), 10*1024*1024) // 10MB max line

	for scanner.Scan() {
		line := scanner.Text()
		if line == "" {
			continue
		}

		// Parse timestamp from JSON
		var event struct {
			Timestamp time.Time `json:"timestamp"`
		}
		if err := json.Unmarshal([]byte(line), &event); err != nil {
			continue
		}

		// Filter by time
		if event.Timestamp.Before(since) {
			continue
		}

		// Apply redaction
		if redactCfg != nil {
			line, _ = redaction.Redact(line, *redactCfg)
		}

		buf.WriteString(line)
		buf.WriteString("\n")
	}

	return []byte(buf.String()), scanner.Err()
}

type bundleSessionFile struct {
	path string
	data []byte
}

func collectBundleSessionSnapshots(ctx context.Context, opts supportBundleOptions, redactCfg *redaction.Config) ([]bundleSessionFile, error) {
	var files []bundleSessionFile

	client := tmux.DefaultClient
	if !client.IsInstalled() {
		return files, nil
	}

	// List sessions
	sessions, err := client.ListSessions()
	if err != nil {
		return files, err
	}

	for _, sess := range sessions {
		// Filter by session name if specified
		if opts.Session != "" && sess.Name != opts.Session {
			continue
		}

		// Get session info
		sessionDir := fmt.Sprintf("sessions/%s", sess.Name)

		// Create session snapshot
		snapshot := map[string]interface{}{
			"name":     sess.Name,
			"created":  sess.Created,
			"attached": sess.Attached,
			"windows":  sess.Windows,
		}

		snapshotData, _ := json.MarshalIndent(snapshot, "", "  ")
		files = append(files, bundleSessionFile{
			path: filepath.Join(sessionDir, "snapshot.json"),
			data: snapshotData,
		})

		// Parse pane filter
		var paneFilter map[int]bool
		if opts.Panes != "" {
			paneFilter = make(map[int]bool)
			for _, p := range strings.Split(opts.Panes, ",") {
				if idx, err := strconv.Atoi(strings.TrimSpace(p)); err == nil {
					paneFilter[idx] = true
				}
			}
		}

		// Capture pane output
		panes, err := client.GetPanesContext(ctx, sess.Name)
		if err != nil {
			continue
		}

		for _, pane := range panes {
			// Filter by pane index if specified
			if paneFilter != nil && !paneFilter[pane.Index] {
				continue
			}

			// Capture output
			target := fmt.Sprintf("%s:%d", sess.Name, pane.Index)
			output, err := client.CapturePaneOutputContext(ctx, target, opts.Lines)
			if err != nil {
				continue
			}

			// Apply redaction
			if redactCfg != nil {
				output, _ = redaction.Redact(output, *redactCfg)
			}

			files = append(files, bundleSessionFile{
				path: filepath.Join(sessionDir, "panes", fmt.Sprintf("pane_%d.txt", pane.Index)),
				data: []byte(output),
			})
		}
	}

	return files, nil
}

func addJSONToBundle(zw *zip.Writer, path string, v interface{}) (ManifestFile, error) {
	data, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return ManifestFile{}, err
	}
	return addDataToBundle(zw, path, data)
}

func addDataToBundle(zw *zip.Writer, path string, data []byte) (ManifestFile, error) {
	w, err := zw.Create(path)
	if err != nil {
		return ManifestFile{}, err
	}

	n, err := w.Write(data)
	if err != nil {
		return ManifestFile{}, err
	}

	return ManifestFile{
		Path:     path,
		Size:     int64(n),
		Checksum: bundleSHA256(data),
	}, nil
}

func bundleSHA256(data []byte) string {
	h := sha256.Sum256(data)
	return hex.EncodeToString(h[:])
}

func buildBundleRedactionConfig(cfg *config.Config) *redaction.Config {
	if cfg == nil || strings.TrimSpace(cfg.Redaction.Mode) == "" || cfg.Redaction.Mode == "off" {
		return nil
	}

	libCfg := cfg.Redaction.ToRedactionLibConfig()
	if err := libCfg.Validate(); err != nil {
		return nil
	}
	return &libCfg
}
