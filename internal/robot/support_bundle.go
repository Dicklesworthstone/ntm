// Package robot provides machine-readable output for AI agents and automation.
package robot

import (
	"fmt"
	"os"
	"time"

	"github.com/Dicklesworthstone/ntm/internal/bundle"
	"github.com/Dicklesworthstone/ntm/internal/privacy"
	"github.com/Dicklesworthstone/ntm/internal/redaction"
	"github.com/Dicklesworthstone/ntm/internal/tmux"
)

// SupportBundleOptions configures support bundle generation.
type SupportBundleOptions struct {
	// Session is the target session name (optional).
	Session string

	// OutputPath is the destination file path (auto-generated if empty).
	OutputPath string

	// Format is the archive format: "zip" or "tar.gz".
	Format string

	// Since filters content to entries after this duration (e.g., "1h", "24h").
	Since string

	// Lines limits scrollback capture per pane (0 = unlimited).
	Lines int

	// MaxSizeMB is the maximum bundle size in MB (0 = unlimited).
	MaxSizeMB int

	// RedactMode is the redaction mode: "off", "warn", "redact", "block".
	RedactMode string

	// NoRedact disables redaction entirely.
	NoRedact bool

	// AllSessions includes all sessions when no session specified.
	AllSessions bool

	// AllowPersist overrides privacy mode to include private content.
	AllowPersist bool

	// NTMVersion is the version string to include in the manifest.
	NTMVersion string
}

// SupportBundleOutput represents the output for --robot-support-bundle.
type SupportBundleOutput struct {
	RobotResponse
	Path              string                  `json:"path,omitempty"`
	Format            string                  `json:"format,omitempty"`
	FileCount         int                     `json:"file_count,omitempty"`
	TotalSize         int64                   `json:"total_size,omitempty"`
	RedactionSummary  *BundleRedactionSummary `json:"redaction_summary,omitempty"`
	Errors            []string                `json:"errors,omitempty"`
	Warnings          []string                `json:"warnings,omitempty"`
	PrivacyMode       bool                    `json:"privacy_mode"`
	PrivacySessions   []string                `json:"privacy_sessions,omitempty"`
	ContentSuppressed bool                    `json:"content_suppressed"`
}

// BundleRedactionSummary provides aggregate redaction statistics for the bundle.
type BundleRedactionSummary struct {
	Mode           string         `json:"mode"`
	TotalFindings  int            `json:"total_findings"`
	FilesScanned   int            `json:"files_scanned"`
	FilesRedacted  int            `json:"files_redacted"`
	CategoryCounts map[string]int `json:"category_counts,omitempty"`
}

// GenerateSupportBundle creates a support bundle and returns structured output.
func GenerateSupportBundle(opts SupportBundleOptions) (*SupportBundleOutput, error) {
	output := &SupportBundleOutput{
		RobotResponse:   NewRobotResponse(true),
		Errors:          []string{},
		Warnings:        []string{},
		PrivacySessions: []string{},
	}

	// Determine format
	format := bundle.FormatZip
	if opts.Format == "tar.gz" || opts.Format == "tgz" {
		format = bundle.FormatTarGz
	}
	output.Format = string(format)

	// Determine output path
	outputPath := opts.OutputPath
	if outputPath == "" {
		outputPath = bundle.SuggestOutputPath(opts.Session, format)
	}

	// Parse since duration
	var sinceTime *time.Time
	if opts.Since != "" {
		duration, err := time.ParseDuration(opts.Since)
		if err != nil {
			output.RobotResponse = NewErrorResponse(
				err,
				ErrCodeInvalidFlag,
				"Invalid --since duration format (use e.g., 1h, 24h)",
			)
			return output, nil
		}
		t := time.Now().Add(-duration)
		sinceTime = &t
	}

	// Determine redaction mode
	redactConfig := redaction.DefaultConfig()
	if opts.NoRedact {
		redactConfig.Mode = redaction.ModeOff
	} else {
		switch opts.RedactMode {
		case "warn":
			redactConfig.Mode = redaction.ModeWarn
		case "redact", "":
			redactConfig.Mode = redaction.ModeRedact
		case "block":
			redactConfig.Mode = redaction.ModeBlock
		case "off":
			redactConfig.Mode = redaction.ModeOff
		default:
			output.RobotResponse = NewErrorResponse(
				nil,
				ErrCodeInvalidFlag,
				"Invalid --redact mode: use warn, redact, block, or off",
			)
			return output, nil
		}
	}

	// Create generator config
	genConfig := bundle.GeneratorConfig{
		Session:         opts.Session,
		OutputPath:      outputPath,
		Format:          format,
		NTMVersion:      opts.NTMVersion,
		Since:           sinceTime,
		Lines:           opts.Lines,
		MaxSizeBytes:    int64(opts.MaxSizeMB) * 1024 * 1024,
		RedactionConfig: redactConfig,
	}

	// Create generator and collect content
	gen := bundle.NewGenerator(genConfig)

	// Track privacy mode status
	var privacySessions []string
	var contentSuppressed bool

	// Collect session data
	if opts.Session != "" {
		suppressed, err := collectSessionDataWithPrivacy(gen, opts.Session, opts.Lines, opts.AllowPersist)
		if err != nil {
			output.Errors = append(output.Errors, err.Error())
		}
		if suppressed {
			contentSuppressed = true
			privacySessions = append(privacySessions, opts.Session)
		}
	} else if opts.AllSessions {
		sessions, err := tmux.ListSessions()
		if err == nil {
			for _, s := range sessions {
				suppressed, err := collectSessionDataWithPrivacy(gen, s.Name, opts.Lines, opts.AllowPersist)
				if err != nil {
					// Record error but continue
					gen.AddFile(
						"errors/"+s.Name+".txt",
						[]byte("Error collecting session data: "+err.Error()),
						bundle.ContentTypeLogs,
						time.Now(),
					)
				}
				if suppressed {
					contentSuppressed = true
					privacySessions = append(privacySessions, s.Name)
				}
			}
		}
	}

	// Collect config files
	if err := collectConfigFiles(gen); err != nil {
		// Non-fatal
		gen.AddFile(
			"errors/config.txt",
			[]byte("Error collecting config: "+err.Error()),
			bundle.ContentTypeLogs,
			time.Now(),
		)
	}

	// Generate the bundle
	result, err := gen.Generate()
	if err != nil {
		output.RobotResponse = NewErrorResponse(
			err,
			ErrCodeInternalError,
			"Failed to generate support bundle",
		)
		return output, nil
	}

	// Populate output
	output.Path = result.Path
	output.FileCount = result.FileCount
	output.TotalSize = result.TotalSize
	output.Errors = result.Errors
	output.Warnings = result.Warnings
	output.PrivacyMode = contentSuppressed
	output.PrivacySessions = privacySessions
	output.ContentSuppressed = contentSuppressed

	if result.RedactionSummary != nil {
		output.RedactionSummary = &BundleRedactionSummary{
			Mode:           result.RedactionSummary.Mode,
			TotalFindings:  result.RedactionSummary.TotalFindings,
			FilesScanned:   result.RedactionSummary.FilesScanned,
			FilesRedacted:  result.RedactionSummary.FilesRedacted,
			CategoryCounts: result.RedactionSummary.CategoryCounts,
		}
	}

	return output, nil
}

// PrintSupportBundle generates a support bundle and outputs as JSON.
func PrintSupportBundle(opts SupportBundleOptions) error {
	output, err := GenerateSupportBundle(opts)
	if err != nil {
		return err
	}
	return encodeJSON(output)
}

// collectSessionDataWithPrivacy adds session data to the bundle, respecting privacy mode.
// Returns true if content was suppressed due to privacy mode.
func collectSessionDataWithPrivacy(gen *bundle.Generator, session string, lines int, allowPersist bool) (bool, error) {
	if !tmux.SessionExists(session) {
		return false, nil // Session doesn't exist, skip silently
	}

	// Check privacy mode for this session
	privacyMgr := privacy.GetDefaultManager()
	privacyEnabled := privacyMgr.IsPrivacyEnabled(session)
	contentSuppressed := false

	// Get panes
	panes, err := tmux.GetPanes(session)
	if err != nil {
		return false, err
	}

	// Add session metadata (safe to include even in privacy mode)
	privacyStatus := "disabled"
	if privacyEnabled {
		privacyStatus = "enabled"
	}
	metadata := fmt.Sprintf("Session: %s\nPanes: %d\nCaptured: %s\nPrivacy Mode: %s\n",
		session, len(panes), time.Now().Format(time.RFC3339), privacyStatus)

	if err := gen.AddFile(
		fmt.Sprintf("sessions/%s/metadata.txt", session),
		[]byte(metadata),
		bundle.ContentTypeMetadata,
		time.Now(),
	); err != nil {
		return false, err
	}

	// If privacy mode is enabled and no override, skip scrollback capture
	if privacyEnabled && !allowPersist {
		contentSuppressed = true
		suppressedMsg := fmt.Sprintf(`Scrollback content suppressed due to privacy mode.

Session: %s
Privacy Mode: enabled
Time: %s

To include private content, use: ntm --robot-support-bundle=%s --allow-persist
`, session, time.Now().Format(time.RFC3339), session)
		gen.AddFile(
			fmt.Sprintf("sessions/%s/PRIVACY_SUPPRESSED.txt", session),
			[]byte(suppressedMsg),
			bundle.ContentTypeMetadata,
			time.Now(),
		)
		return contentSuppressed, nil
	}

	// Capture scrollback for each pane
	for _, pane := range panes {
		target := pane.ID
		content, err := tmux.CapturePaneOutput(target, lines)
		if err != nil {
			// Record error and continue
			gen.AddFile(
				fmt.Sprintf("sessions/%s/errors/pane_%d.txt", session, pane.Index),
				[]byte(fmt.Sprintf("Error capturing pane: %v", err)),
				bundle.ContentTypeLogs,
				time.Now(),
			)
			continue
		}

		paneName := fmt.Sprintf("pane_%d", pane.Index)
		if pane.Title != "" {
			paneName = pane.Title
		}

		if err := gen.AddScrollback(
			fmt.Sprintf("sessions/%s/%s", session, paneName),
			content,
			lines,
		); err != nil {
			// Continue even if one pane fails
			continue
		}
	}

	return contentSuppressed, nil
}

// collectConfigFiles adds relevant config files to the bundle.
func collectConfigFiles(gen *bundle.Generator) error {
	// Check for .ntm directory
	home, err := os.UserHomeDir()
	if err != nil {
		return err
	}

	ntmDir := home + "/.ntm"
	configFiles := []string{"config.toml", "palettes.yaml", "themes.yaml"}
	for _, name := range configFiles {
		path := ntmDir + "/" + name
		if data, err := os.ReadFile(path); err == nil {
			gen.AddFile("config/"+name, data, bundle.ContentTypeConfig, time.Now())
		}
	}

	return nil
}
