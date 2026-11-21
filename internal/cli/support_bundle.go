package cli

import (
	"archive/zip"
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/BurntSushi/toml"
	"github.com/spf13/cobra"

	"github.com/Dicklesworthstone/ntm/internal/bundle"
	"github.com/Dicklesworthstone/ntm/internal/config"
	"github.com/Dicklesworthstone/ntm/internal/encryption"
	"github.com/Dicklesworthstone/ntm/internal/redaction"
	"github.com/Dicklesworthstone/ntm/internal/robot"
	"github.com/Dicklesworthstone/ntm/internal/tmux"
	"github.com/Dicklesworthstone/ntm/internal/util"
)

// supportBundleOptions holds the command options.
type supportBundleOptions struct {
	Output          string
	Verify          string
	Decrypt         string
	Encrypt         bool
	AllowPlaintext  bool
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
		Short: "Generate or verify a diagnostic support bundle archive",
		Long: `Generate a support bundle archive containing diagnostic information
for troubleshooting NTM issues, or verify an existing bundle archive. The bundle includes:

  - manifest.json      Versioned schema with checksums
  - doctor.json        System health check results
  - config_effective.toml  Effective configuration (redacted)
  - versions.json      NTM, Go, tmux, and tool versions
  - events.jsonl       Recent events (configurable time window)
  - sessions/          Session snapshots (optional)

By default, sensitive data is redacted using the configured redaction engine.
Use --no-redact to include raw data (only for trusted recipients).
Encryption follows [encryption] enabled. Use --encrypt for this bundle only, or
--allow-plaintext to explicitly export without encryption. Encrypted bundles use
the .zip.enc extension. Verification and decryption use the configured key source
and rotation keyring; plaintext is published only after authentication succeeds.

Examples:
  ntm support-bundle --output bundle.zip
  ntm support-bundle --session myproj --lines 500 -o bundle.zip
  ntm support-bundle --since 2h -o bundle.zip
  ntm support-bundle --include-sessions -o bundle.zip
  ntm support-bundle --verify bundle.zip
  ntm support-bundle --encrypt -o bundle.zip.enc
  ntm support-bundle --verify bundle.zip.enc
  ntm support-bundle --decrypt bundle.zip.enc -o bundle.zip`,
		RunE: func(cmd *cobra.Command, args []string) error {
			effectiveOpts := opts
			effectiveOpts.Encrypt = effectiveOpts.Encrypt || encryptFlag
			effectiveOpts.AllowPlaintext = effectiveOpts.AllowPlaintext || allowPlaintextFlag
			effectiveOpts.RobotMode = effectiveOpts.RobotMode || IsJSONOutput()
			return runSupportBundle(cmd.Context(), effectiveOpts)
		},
	}

	cmd.Flags().StringVarP(&opts.Output, "output", "o", "", "Output file path (required unless --verify is set)")
	cmd.Flags().StringVar(&opts.Verify, "verify", "", "Verify an existing support bundle archive")
	cmd.Flags().StringVar(&opts.Decrypt, "decrypt", "", "Authenticate, verify and decrypt an encrypted bundle to --output")
	cmd.Flags().BoolVar(&opts.Encrypt, "encrypt", false, "Encrypt the bundle with the configured AES-256-GCM key")
	cmd.Flags().BoolVar(&opts.AllowPlaintext, "allow-plaintext", false, "Explicitly export this bundle without encryption")
	cmd.Flags().StringVar(&opts.Session, "session", "", "Include specific session only")
	cmd.Flags().StringVar(&opts.Panes, "panes", "", "Pane indices to include (comma-separated)")
	cmd.Flags().IntVar(&opts.Lines, "lines", opts.Lines, "Lines of pane output to capture")
	cmd.Flags().StringVar(&opts.Since, "since", "", "Include events since duration (e.g., 2h, 24h)")
	cmd.Flags().Int64Var(&opts.MaxSize, "max-size", opts.MaxSize, "Maximum bundle size in bytes")
	cmd.Flags().BoolVar(&opts.NoRedact, "no-redact", false, "Skip redaction (unsafe)")
	cmd.Flags().BoolVar(&opts.IncludeSessions, "include-sessions", opts.IncludeSessions, "Include session snapshots and pane output")
	cmd.Flags().BoolVar(&opts.IncludeEvents, "include-events", opts.IncludeEvents, "Include events log")

	return cmd
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
	Success          bool                     `json:"success"`
	Path             string                   `json:"path"`
	Format           string                   `json:"format,omitempty"`
	Size             int64                    `json:"size"`
	TotalSize        int64                    `json:"total_size"`
	ContentBytes     int64                    `json:"content_bytes"`
	Files            int                      `json:"files"`
	FileCount        int                      `json:"file_count"`
	Redacted         bool                     `json:"redacted"`
	Encrypted        bool                     `json:"encrypted,omitempty"`
	RedactionSummary *bundle.RedactionSummary `json:"redaction_summary,omitempty"`
	Errors           []string                 `json:"errors,omitempty"`
	Error            string                   `json:"error,omitempty"`
}

// BundleVerifyResponse is the JSON output for --verify.
type BundleVerifyResponse struct {
	Success bool                 `json:"success"`
	Path    string               `json:"path"`
	Result  *bundle.VerifyResult `json:"result,omitempty"`
	Error   string               `json:"error,omitempty"`
}

func runSupportBundle(ctx context.Context, opts supportBundleOptions) error {
	if ctx == nil {
		ctx = context.Background()
	}
	if opts.Encrypt && opts.AllowPlaintext {
		return fmt.Errorf("--encrypt and --allow-plaintext are mutually exclusive")
	}
	if opts.Verify != "" && (opts.Decrypt != "" || opts.Output != "" || opts.Encrypt || opts.AllowPlaintext) {
		return fmt.Errorf("--verify cannot be combined with --decrypt, --output, --encrypt or --allow-plaintext")
	}
	if opts.Decrypt != "" && (opts.Encrypt || opts.AllowPlaintext) {
		return fmt.Errorf("--decrypt explicitly exports plaintext and cannot be combined with --encrypt or --allow-plaintext")
	}

	if strings.TrimSpace(opts.Verify) != "" {
		return runSupportBundleVerify(opts.Verify, opts.RobotMode)
	}

	// Validate output path
	if opts.Output == "" {
		return fmt.Errorf("output path is required")
	}
	if strings.TrimSpace(opts.Decrypt) != "" {
		return runSupportBundleDecrypt(opts)
	}

	// Expand path
	outputPath := util.ExpandPath(opts.Output)
	if supportBundleEncryptionRequired(opts) {
		outputPath = strings.TrimSuffix(outputPath, ".enc")
		if !strings.HasSuffix(outputPath, ".zip") {
			outputPath += ".zip"
		}
		outputPath += ".enc"
	} else {
		if strings.HasSuffix(outputPath, ".enc") {
			return fmt.Errorf("plaintext bundle output must not use the .enc extension")
		}
		if !strings.HasSuffix(outputPath, ".zip") {
			outputPath += ".zip"
		}
	}

	// Create the bundle
	result, err := createSupportBundle(ctx, outputPath, opts)
	if err != nil {
		if opts.RobotMode {
			// bd-oqwmf: signal non-zero exit after writing the
			// success:false envelope so `ntm support-bundle --json`
			// automation can gate on `$?`.
			if outErr := robot.Output(BundleResult{
				Success: false,
				Error:   err.Error(),
			}, robot.FormatJSON); outErr != nil {
				return outErr
			}
			return jsonFailureExit()
		}
		return err
	}

	if opts.RobotMode {
		return robot.Output(result, robot.FormatJSON)
	}

	fmt.Printf("Support bundle created: %s\n", result.Path)
	fmt.Printf("  Size: %d bytes (%d content bytes)\n", result.Size, result.ContentBytes)
	fmt.Printf("  Files: %d\n", result.FileCount)
	fmt.Printf("  Redacted: %v\n", result.Redacted)
	if result.Encrypted {
		fmt.Printf("  Encrypted: true (NTMENC1 AES-256-GCM)\n")
	}
	return nil
}

func runSupportBundleVerify(verifyPath string, robotMode bool) error {
	resolved := util.ExpandPath(verifyPath)
	keys, err := supportBundleReadKeys(resolved)
	var res *bundle.VerifyResult
	if err == nil {
		res, err = bundle.Verify(resolved, keys...)
	}
	if err != nil {
		if robotMode {
			if outErr := robot.Output(BundleVerifyResponse{
				Success: false,
				Path:    resolved,
				Error:   err.Error(),
			}, robot.FormatJSON); outErr != nil {
				return outErr
			}
			return jsonFailureExit()
		}
		return err
	}

	if robotMode {
		if outErr := robot.Output(BundleVerifyResponse{
			Success: res.Valid,
			Path:    resolved,
			Result:  res,
		}, robot.FormatJSON); outErr != nil {
			return outErr
		}
		if !res.Valid {
			return jsonFailureExit()
		}
		return nil
	}

	status := "VALID"
	if !res.Valid {
		status = "INVALID"
	}
	fmt.Printf("Support bundle verification: %s (%s)\n", status, resolved)
	if res.Manifest != nil {
		fmt.Printf("  Schema version: %d\n", res.Manifest.SchemaVersion)
		fmt.Printf("  NTM version: %s\n", res.Manifest.NTMVersion)
		fmt.Printf("  Files: %d (%d content bytes)\n", res.Manifest.FileCount(), res.Manifest.TotalSize())
	}
	fmt.Printf("  Manifest valid: %v\n", res.ManifestValid)
	fmt.Printf("  Files present: %v\n", res.FilesPresent)
	fmt.Printf("  Checksums valid: %v\n", res.ChecksumsValid)
	for _, warn := range res.Warnings {
		fmt.Printf("  Warning: %s\n", warn)
	}
	for _, errMsg := range res.Errors {
		fmt.Printf("  Error: %s\n", errMsg)
	}
	if !res.Valid {
		return fmt.Errorf("bundle verification failed with %d error(s)", len(res.Errors))
	}
	return nil
}

func supportBundleEncryptionRequired(opts supportBundleOptions) bool {
	return !opts.AllowPlaintext && (opts.Encrypt || (cfg != nil && cfg.Encryption.Enabled))
}

// Only the direct diagnostic command may defer startup key resolution. Other
// commands retain fail-closed encryption for their persisted history and events.
func supportBundleHandlesEncryption(cmd *cobra.Command) bool {
	if cmd == nil || cmd.Name() != "support-bundle" {
		return false
	}
	parent := cmd.Parent()
	return parent != nil && parent.Parent() == nil && parent.Name() == "ntm"
}

// Both bundle surfaces must load selected policy strictly. A skipped project
// overlay or missing explicit config cannot authorize an unencrypted export.
func supportBundleRequiresStrictConfig(cmd *cobra.Command) bool {
	if supportBundleHandlesEncryption(cmd) {
		return true
	}
	return cmd != nil && cmd.Name() == "ntm" && cmd.Parent() == nil &&
		(robotSupportBundle != "" || cmd.Flags().Changed("robot-support-bundle"))
}

func supportBundleKeyConfig() (encryption.KeyConfig, error) {
	enc := config.DefaultEncryptionConfig()
	if cfg != nil {
		enc = cfg.Encryption
	}
	// --encrypt also validates a key configuration whose global toggle is off.
	enc.Enabled = true
	if err := config.ValidateEncryptionConfig(&enc); err != nil {
		return encryption.KeyConfig{}, err
	}
	return encryption.KeyConfig{
		KeySource: enc.KeySource, KeyEnv: enc.KeyEnv,
		KeyFile: util.ExpandPath(enc.KeyFile), KeyCommand: enc.KeyCommand,
		KeyFormat: enc.KeyFormat, ActiveKeyID: enc.ActiveKeyID, Keyring: enc.Keyring,
	}, nil
}

func supportBundleReadKeys(path string) ([][]byte, error) {
	encrypted, err := bundle.IsEncrypted(path)
	if err != nil || !encrypted {
		return nil, err
	}
	keyCfg, err := supportBundleKeyConfig()
	if err != nil {
		return nil, err
	}
	return encryption.ResolveKeyring(keyCfg)
}

func runSupportBundleDecrypt(opts supportBundleOptions) error {
	inputPath := util.ExpandPath(opts.Decrypt)
	outputPath := util.ExpandPath(opts.Output)
	keys, err := supportBundleReadKeys(inputPath)
	var res *bundle.VerifyResult
	if err == nil {
		res, err = bundle.Decrypt(inputPath, outputPath, keys...)
	}
	if opts.RobotMode {
		response := BundleVerifyResponse{Success: err == nil, Path: outputPath, Result: res}
		if err != nil {
			response.Error = err.Error()
		}
		if outputErr := robot.Output(response, robot.FormatJSON); outputErr != nil {
			return outputErr
		}
		if err != nil {
			return jsonFailureExit()
		}
		return nil
	}
	if err != nil {
		return err
	}
	fmt.Printf("Support bundle decrypted and verified: %s\n", outputPath)
	return nil
}

func createSupportBundle(ctx context.Context, outputPath string, opts supportBundleOptions) (result *BundleResult, retErr error) {
	var encryptKey []byte
	if supportBundleEncryptionRequired(opts) {
		keyCfg, err := supportBundleKeyConfig()
		if err != nil {
			return nil, err
		}
		encryptKey, err = encryption.ResolveKey(keyCfg)
		if err != nil {
			return nil, fmt.Errorf("support bundle encryption required: %w; use --allow-plaintext only for an intentional unencrypted export", err)
		}
	}

	// No plaintext archive is ever written to disk before encryption.
	var archive bytes.Buffer
	zipWriter := zip.NewWriter(&archive)
	defer func() {
		if zipWriter == nil {
			return
		}
		if err := zipWriter.Close(); err != nil && retErr == nil {
			retErr = fmt.Errorf("close zip: %w", err)
		}
	}()

	ntmVersion := strings.TrimSpace(Version)
	if ntmVersion == "" {
		ntmVersion = "dev"
	}
	manifest := bundle.NewManifest(ntmVersion)
	manifest.Filters = &bundle.BundleFilters{
		Lines:        opts.Lines,
		MaxSizeBytes: opts.MaxSize,
	}
	if opts.Since != "" {
		if dur, err := time.ParseDuration(opts.Since); err == nil {
			manifest.Filters.Since = time.Now().UTC().Add(-dur).Format(time.RFC3339)
		}
	}
	if opts.Session != "" {
		manifest.Session = &bundle.SessionInfo{
			Name: opts.Session,
		}
	}

	// Get redaction config if enabled
	var redactCfg *redaction.Config
	redactMode := string(redaction.ModeOff)
	if !opts.NoRedact {
		redactMode = string(redaction.ModeRedact)
		if cfg != nil {
			redactCfg = buildBundleRedactionConfig(cfg)
			if redactCfg != nil {
				redactMode = string(redactCfg.Mode)
			}
		}
	}
	redactionSummary := &bundle.RedactionSummary{
		Mode:           redactMode,
		CategoryCounts: make(map[string]int),
	}

	nowStr := time.Now().UTC().Format(time.RFC3339)

	// 1 & 2. Collect versions.json and doctor.json concurrently (they are
	// read-only external probes), then append them to the archive in their
	// canonical deterministic order.
	var (
		versionsData *VersionsInfo
		versionsErr  error
		doctorReport *DoctorReport
		probeWG      sync.WaitGroup
	)
	probeWG.Add(2)
	go func() {
		defer probeWG.Done()
		versionsData, versionsErr = collectBundleVersions(ctx)
	}()
	go func() {
		defer probeWG.Done()
		doctorReport = performDoctorCheck(ctx)
	}()
	probeWG.Wait()

	// 1. Add versions.json
	if versionsErr != nil {
		manifest.AddError(fmt.Sprintf("collect versions: %v", versionsErr))
	} else {
		if entry, addErr := addJSONToBundle(zipWriter, "versions.json", versionsData, bundle.ContentTypeMetadata, nowStr); addErr != nil {
			manifest.AddError(fmt.Sprintf("write versions.json: %v", addErr))
		} else {
			manifest.AddFile(entry)
		}
	}

	// 2. Add doctor.json
	if entry, addErr := addJSONToBundle(zipWriter, "doctor.json", doctorReport, bundle.ContentTypeMetadata, nowStr); addErr != nil {
		manifest.AddError(fmt.Sprintf("write doctor.json: %v", addErr))
	} else {
		manifest.AddFile(entry)
	}

	// 3. Add config_effective.toml (redacted)
	if cfg != nil {
		configData, err := exportBundleConfigTOML(cfg, redactCfg)
		if err != nil {
			manifest.AddError(fmt.Sprintf("export config: %v", err))
		} else {
			if entry, addErr := addTypedDataToBundle(zipWriter, "config_effective.toml", configData, bundle.ContentTypeConfig, nowStr); addErr != nil {
				manifest.AddError(fmt.Sprintf("write config_effective.toml: %v", addErr))
			} else {
				redactionSummary.FilesScanned++
				if redactCfg != nil {
					entry.Redaction = &bundle.FileRedaction{WasRedacted: true}
					redactionSummary.FilesRedacted++
				}
				manifest.AddFile(entry)
			}
		}
	}

	// 4. Add events.jsonl (bounded)
	if opts.IncludeEvents {
		eventsData, err := collectBundleEvents(opts.Since, redactCfg)
		if err != nil {
			manifest.AddError(fmt.Sprintf("collect events: %v", err))
		} else if len(eventsData) > 0 {
			if entry, addErr := addTypedDataToBundle(zipWriter, "events.jsonl", eventsData, bundle.ContentTypeLogs, nowStr); addErr != nil {
				manifest.AddError(fmt.Sprintf("write events.jsonl: %v", addErr))
			} else {
				redactionSummary.FilesScanned++
				if redactCfg != nil {
					entry.Redaction = &bundle.FileRedaction{WasRedacted: true}
					redactionSummary.FilesRedacted++
				}
				manifest.AddFile(entry)
			}
		}
	}

	// 5. Add session snapshots (optional)
	if opts.IncludeSessions {
		sessionFiles, err := collectBundleSessionSnapshots(ctx, opts, redactCfg)
		if err != nil {
			manifest.AddError(fmt.Sprintf("collect session snapshots: %v", err))
		} else {
			for _, sf := range sessionFiles {
				contentType := bundle.ContentTypeState
				if strings.Contains(sf.path, "/panes/") {
					contentType = bundle.ContentTypeScrollback
				}
				if entry, addErr := addTypedDataToBundle(zipWriter, sf.path, sf.data, contentType, nowStr); addErr != nil {
					manifest.AddError(fmt.Sprintf("write %s: %v", sf.path, addErr))
				} else {
					redactionSummary.FilesScanned++
					if redactCfg != nil {
						entry.Redaction = &bundle.FileRedaction{WasRedacted: true}
						redactionSummary.FilesRedacted++
					}
					manifest.AddFile(entry)
				}
			}
		}
	}

	manifest.RedactionSummary = redactionSummary

	if err := manifest.Validate(); err != nil {
		return nil, fmt.Errorf("validate bundle manifest: %w", err)
	}

	// Write manifest.json with canonical bundle.Manifest schema
	if _, err := addJSONToBundle(zipWriter, bundle.ManifestFileName, manifest, bundle.ContentTypeMetadata, nowStr); err != nil {
		return nil, fmt.Errorf("write %s: %w", bundle.ManifestFileName, err)
	}

	// Close zip writer to flush
	if err := zipWriter.Close(); err != nil {
		return nil, fmt.Errorf("close zip: %w", err)
	}
	zipWriter = nil

	size, err := bundle.WriteArchive(outputPath, archive.Bytes(), encryptKey, opts.MaxSize)
	if err != nil {
		return nil, err
	}

	contentBytes := manifest.TotalSize()
	fileCount := manifest.FileCount()

	return &BundleResult{
		Success:          true,
		Path:             outputPath,
		Format:           string(bundle.FormatZip),
		Size:             size,
		TotalSize:        size,
		ContentBytes:     contentBytes,
		Files:            fileCount,
		FileCount:        fileCount,
		Redacted:         !opts.NoRedact,
		Encrypted:        encryptKey != nil,
		RedactionSummary: redactionSummary,
		Errors:           manifest.Errors,
	}, nil
}

func collectBundleVersions(ctx context.Context) (*VersionsInfo, error) {
	versions := &VersionsInfo{
		NTM:      Version,
		Go:       runtime.Version(),
		Platform: fmt.Sprintf("%s/%s", runtime.GOOS, runtime.GOARCH),
		Tools:    make(map[string]string),
	}

	tmpRoot := filepath.Clean(os.TempDir())
	probeFirstLine := func(binPath string, arg string, extraEnv ...string) (string, bool) {
		if ctx != nil && ctx.Err() != nil {
			return "", false
		}
		var cacheKey depVersionCacheKey
		cacheable := false
		cleanPath := filepath.Clean(binPath)
		if !strings.HasPrefix(cleanPath, tmpRoot+string(os.PathSeparator)) && cleanPath != tmpRoot {
			if info, statErr := os.Stat(binPath); statErr == nil && info.Mode().IsRegular() {
				cacheKey = depVersionCacheKey{
					path:        cleanPath,
					modUnix:     info.ModTime().UnixNano(),
					size:        info.Size(),
					versionArgs: "bundle:" + arg,
				}
				cacheable = true
				depVersionCacheMu.RLock()
				entry, ok := depVersionCache[cacheKey]
				depVersionCacheMu.RUnlock()
				if ok && time.Since(entry.checkedAt) < depVersionCacheTTL {
					return entry.version, entry.version != ""
				}
			}
		}

		cmd := exec.CommandContext(ctx, binPath, arg)
		cmd.WaitDelay = 2 * time.Second
		if len(extraEnv) > 0 {
			cmd.Env = append(os.Environ(), extraEnv...)
		}
		out, err := cmd.Output()
		if err != nil {
			return "", false
		}
		version := strings.TrimSpace(string(out))
		if idx := strings.Index(version, "\n"); idx > 0 {
			version = version[:idx]
		}
		if version == "" {
			return "", false
		}
		if cacheable && (ctx == nil || ctx.Err() == nil) {
			depVersionCacheMu.Lock()
			if len(depVersionCache) >= maxDepVersionCache {
				clear(depVersionCache)
			}
			depVersionCache[cacheKey] = depVersionCacheEntry{version: version, checkedAt: time.Now()}
			depVersionCacheMu.Unlock()
		}
		return version, true
	}

	toolChecks := []string{"bv", "br", "cm", "cass", "ubs", "dcg"}
	var (
		mu sync.Mutex
		wg sync.WaitGroup
	)

	// Get tmux version concurrently with companion tool versions.
	if tmux.DefaultClient.IsInstalled() {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if v, ok := probeFirstLine(tmux.BinaryPath(), "-V"); ok {
				mu.Lock()
				versions.Tmux = v
				mu.Unlock()
			}
		}()
	}

	for _, tool := range toolChecks {
		tool := tool
		wg.Add(1)
		go func() {
			defer wg.Done()
			path, err := exec.LookPath(tool)
			if err != nil {
				return
			}
			if v, ok := probeFirstLine(path, "--version", "NO_COLOR=1"); ok {
				mu.Lock()
				versions.Tools[tool] = v
				mu.Unlock()
			}
		}()
	}
	wg.Wait()

	return versions, nil
}

func exportBundleConfigTOML(cfg *config.Config, redactCfg *redaction.Config) ([]byte, error) {
	// Encryption credentials must never be exported, even with --no-redact.
	// A shallow copy is sufficient: only replace fields, never mutate the map.
	safeConfig := *cfg
	safeConfig.Encryption.Keyring = nil
	safeConfig.Encryption.KeyCommand = ""
	var buf strings.Builder
	encoder := toml.NewEncoder(&buf)
	if err := encoder.Encode(&safeConfig); err != nil {
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
	var decryptKeys [][]byte
	scanner := bufio.NewScanner(file)
	scanner.Buffer(make([]byte, 1024*1024), 10*1024*1024) // 10MB max line

	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" {
			continue
		}
		if encryption.IsEncryptedLine([]byte(line)) {
			if decryptKeys == nil {
				keyCfg, err := supportBundleKeyConfig()
				if err != nil {
					return nil, fmt.Errorf("read encrypted events: %w", err)
				}
				decryptKeys, err = encryption.ResolveKeyring(keyCfg)
				if err != nil {
					return nil, fmt.Errorf("read encrypted events: %w", err)
				}
			}
			plain, err := encryption.DecryptLineWithKeyring(decryptKeys, []byte(line))
			if err != nil {
				return nil, fmt.Errorf("read encrypted events: %w", err)
			}
			line = string(plain)
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

		// Create session snapshot
		snapshot := map[string]interface{}{
			"name":     sess.Name,
			"created":  sess.Created,
			"attached": sess.Attached,
			"windows":  sess.Windows,
		}

		snapshotData, _ := json.MarshalIndent(snapshot, "", "  ")
		snapshotPath, err := supportBundleSessionPath(sess.Name, "snapshot.json")
		if err != nil {
			return nil, err
		}
		files = append(files, bundleSessionFile{
			path: snapshotPath,
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
			target := pane.ID
			output, err := client.CapturePaneOutputContext(ctx, target, opts.Lines)
			if err != nil {
				continue
			}

			// Apply redaction
			if redactCfg != nil {
				output, _ = redaction.Redact(output, *redactCfg)
			}

			panePath, err := supportBundleSessionPath(sess.Name, "panes", fmt.Sprintf("pane_%d.txt", pane.Index))
			if err != nil {
				return nil, err
			}
			files = append(files, bundleSessionFile{
				path: panePath,
				data: []byte(output),
			})
		}
	}

	return files, nil
}

func supportBundleSessionPath(session string, elems ...string) (string, error) {
	parts := append([]string{"sessions", session}, elems...)
	return bundle.SanitizeArchivePath(strings.Join(parts, "/"))
}

func addJSONToBundle(zw *zip.Writer, path string, v interface{}, contentType, modTime string) (bundle.FileEntry, error) {
	data, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return bundle.FileEntry{}, err
	}
	return addTypedDataToBundle(zw, path, data, contentType, modTime)
}

func addTypedDataToBundle(zw *zip.Writer, path string, data []byte, contentType, modTime string) (bundle.FileEntry, error) {
	entry, err := addDataToBundle(zw, path, data)
	if err != nil {
		return bundle.FileEntry{}, err
	}
	entry.ContentType = contentType
	entry.ModTime = modTime
	return entry, nil
}

func addDataToBundle(zw *zip.Writer, path string, data []byte) (bundle.FileEntry, error) {
	archivePath, err := bundle.SanitizeArchivePath(path)
	if err != nil {
		return bundle.FileEntry{}, err
	}

	w, err := zw.Create(archivePath)
	if err != nil {
		return bundle.FileEntry{}, err
	}

	n, err := w.Write(data)
	if err != nil {
		return bundle.FileEntry{}, err
	}

	return bundle.FileEntry{
		Path:      archivePath,
		SizeBytes: int64(n),
		SHA256:    bundle.HashBytes(data),
	}, nil
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
