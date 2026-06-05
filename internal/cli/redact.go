package cli

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/Dicklesworthstone/ntm/internal/config"
	"github.com/Dicklesworthstone/ntm/internal/output"
	"github.com/Dicklesworthstone/ntm/internal/redaction"
	"github.com/Dicklesworthstone/ntm/internal/util"
)

type RedactPreviewFinding struct {
	Category redaction.Category `json:"category"`
	Redacted string             `json:"redacted"`
	Start    int                `json:"start"`
	End      int                `json:"end"`
	Line     int                `json:"line,omitempty"`
	Column   int                `json:"column,omitempty"`
}

type RedactPreviewResponse struct {
	output.TimestampedResponse

	Source   string                 `json:"source"`         // text|file
	Path     string                 `json:"path,omitempty"` // only when source=file
	InputLen int                    `json:"input_len"`      // bytes
	Findings []RedactPreviewFinding `json:"findings"`       // never includes raw matches
	Output   string                 `json:"output"`         // redacted output (safe)
}

func newRedactCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "redact",
		Short: "Redaction utilities",
		Long: `Redaction utilities for previewing and debugging secret detection.

These commands NEVER print raw matched secrets. Output is always safe-redacted.`,
	}

	cmd.AddCommand(
		newRedactPreviewCmd(),
		newRedactPrepareMailCmd(),
	)

	return cmd
}

// =============================================================================
// `ntm redact prepare-mail` (ntm#126)
// =============================================================================
//
// Token-handle contract for Agent Mail prepare/send workflows. Raw token
// material is read from an env var or file by `prepare-mail` itself,
// scanned for redaction findings, and stashed in a per-user store keyed
// by a random handle. The caller then invokes
// `ntm mail send … --prepared-redaction <handle>` (a *separate* process)
// and the raw bytes never leave the redaction surface (no wrapper logs,
// no prompt packets, no dry-run output carry the token text).
//
// Storage lives in `$XDG_RUNTIME_DIR/ntm/redaction-handles/` (typically
// `/run/user/<uid>`, a per-user tmpfs cleared on reboot). Each handle
// file is 0600 and contains JSON {raw, redacted_body, findings,
// created_at}. The directory itself is created 0700. Files are deleted
// on consume; opportunistic sweep of expired entries runs on every
// stash. The 10-minute TTL caps how long a forgotten handle keeps a
// token resident.

const preparedRedactionTTL = 10 * time.Minute

// preparedRedactionPayload is the on-disk schema for a stashed handle.
// Field names are stable wire contract for any external diagnostic
// tooling that needs to inspect them.
type preparedRedactionPayload struct {
	Raw          string                 `json:"raw"`
	RedactedBody string                 `json:"redacted_body"`
	Findings     []RedactPreviewFinding `json:"findings"`
	CreatedAt    time.Time              `json:"created_at"`
}

// preparedRedactionStorageDir returns the directory where handle files
// are persisted. Uses `$XDG_RUNTIME_DIR/ntm/redaction-handles` when the
// XDG variable is set (this is a per-user tmpfs cleared on reboot, the
// right home for short-lived secret material). Falls back to
// `os.TempDir()/ntm-<uid>/redaction-handles` when the XDG variable is
// unset so non-systemd platforms still work. The directory is created
// with 0700 so other users on the host cannot enumerate handles.
func preparedRedactionStorageDir() (string, error) {
	var base string
	if x := strings.TrimSpace(os.Getenv("XDG_RUNTIME_DIR")); x != "" {
		base = x
	} else {
		base = filepath.Join(os.TempDir(), fmt.Sprintf("ntm-%d", os.Getuid()))
	}
	dir := filepath.Join(base, "ntm", "redaction-handles")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", fmt.Errorf("create redaction-handle store: %w", err)
	}
	return dir, nil
}

// validPreparedRedactionHandle defends consumePreparedRedaction against
// path-traversal attempts: handles must match the exact shape produced
// by stashPreparedRedaction (`rh_` + 32 hex chars).
func validPreparedRedactionHandle(handle string) bool {
	if !strings.HasPrefix(handle, "rh_") {
		return false
	}
	body := handle[3:]
	if len(body) != 32 {
		return false
	}
	for i := 0; i < len(body); i++ {
		c := body[i]
		switch {
		case c >= '0' && c <= '9':
		case c >= 'a' && c <= 'f':
		default:
			return false
		}
	}
	return true
}

// sweepExpiredPreparedRedactions removes handle files older than the
// TTL. Best-effort: errors during sweep are silently ignored so a
// transient ENOENT never blocks a fresh stash.
func sweepExpiredPreparedRedactions(dir string) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return
	}
	cutoff := time.Now().Add(-preparedRedactionTTL)
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		info, err := e.Info()
		if err != nil {
			continue
		}
		if info.ModTime().Before(cutoff) {
			_ = os.Remove(filepath.Join(dir, e.Name()))
		}
	}
}

// stashPreparedRedaction writes the raw secret + its redaction summary
// to a per-user handle file (mode 0600) under
// `$XDG_RUNTIME_DIR/ntm/redaction-handles/`. Sweeps expired entries
// opportunistically. Returns the handle the caller passes to
// `ntm mail send --prepared-redaction`.
func stashPreparedRedaction(raw, redactedBody string, findings []RedactPreviewFinding) (string, error) {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", fmt.Errorf("generate handle: %w", err)
	}
	handle := "rh_" + hex.EncodeToString(b[:])

	dir, err := preparedRedactionStorageDir()
	if err != nil {
		return "", err
	}
	sweepExpiredPreparedRedactions(dir)

	payload := preparedRedactionPayload{
		Raw:          raw,
		RedactedBody: redactedBody,
		Findings:     findings,
		CreatedAt:    time.Now(),
	}
	data, err := json.Marshal(payload)
	if err != nil {
		return "", fmt.Errorf("serialize prepared redaction: %w", err)
	}

	// O_EXCL ensures we never clobber an existing handle (handle is
	// 16 random bytes so collision is negligible, but defend anyway).
	path := filepath.Join(dir, handle+".json")
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return "", fmt.Errorf("create prepared-redaction handle: %w", err)
	}
	if _, writeErr := f.Write(data); writeErr != nil {
		_ = f.Close()
		_ = os.Remove(path)
		return "", fmt.Errorf("write prepared-redaction handle: %w", writeErr)
	}
	if closeErr := f.Close(); closeErr != nil {
		_ = os.Remove(path)
		return "", fmt.Errorf("close prepared-redaction handle: %w", closeErr)
	}
	return handle, nil
}

// consumePreparedRedaction is the send-side accessor. Reads the handle
// file, removes it (consume-once semantics), and returns the raw secret
// bytes. The returned `redacted` value is what should be surfaced in
// JSON envelopes and logs; `raw` is intended only for the wire payload
// to the downstream transport. Returns an error when the handle is
// missing, malformed, expired, or the file can't be read.
func consumePreparedRedaction(handle string) (raw, redacted string, findings []RedactPreviewFinding, err error) {
	if !validPreparedRedactionHandle(handle) {
		return "", "", nil, fmt.Errorf("invalid prepared-redaction handle: %q", handle)
	}
	dir, err := preparedRedactionStorageDir()
	if err != nil {
		return "", "", nil, err
	}
	path := filepath.Join(dir, handle+".json")
	data, readErr := os.ReadFile(path)
	if readErr != nil {
		if os.IsNotExist(readErr) {
			return "", "", nil, fmt.Errorf("prepared-redaction handle %q not found (expired or never created)", handle)
		}
		return "", "", nil, fmt.Errorf("read prepared-redaction handle: %w", readErr)
	}
	// Drop the file before validating TTL so even an expired handle
	// is cleaned up by the act of trying to consume it.
	_ = os.Remove(path)

	var payload preparedRedactionPayload
	if jsonErr := json.Unmarshal(data, &payload); jsonErr != nil {
		return "", "", nil, fmt.Errorf("decode prepared-redaction handle: %w", jsonErr)
	}
	if time.Since(payload.CreatedAt) > preparedRedactionTTL {
		return "", "", nil, fmt.Errorf("prepared-redaction handle %q expired", handle)
	}
	return payload.Raw, payload.RedactedBody, payload.Findings, nil
}

// RedactPrepareMailResponse is the JSON envelope returned by
// `ntm redact prepare-mail`. The raw token never appears; callers
// receive a handle they can pass to `ntm mail send --prepared-redaction`.
type RedactPrepareMailResponse struct {
	output.TimestampedResponse

	Handle       string                 `json:"handle"`
	ExpiresIn    string                 `json:"expires_in"`
	InputSource  string                 `json:"input_source"` // env|file
	InputLen     int                    `json:"input_len"`
	Findings     []RedactPreviewFinding `json:"findings"`
	RedactedView string                 `json:"redacted_view"`
}

func newRedactPrepareMailCmd() *cobra.Command {
	var (
		senderTokenEnv  string
		senderTokenFile string
	)

	cmd := &cobra.Command{
		Use:   "prepare-mail",
		Short: "Prepare an Agent Mail payload with a redaction handle (ntm#126)",
		Long: `Read sensitive sender/token material from --sender-token-env or
--sender-token-file, scan it for redaction findings, and return a
short-lived handle. The raw bytes never leave this process — they are
never echoed to stdout, never logged, never serialized into the JSON
envelope. The caller then passes the handle to:

  ntm mail send <session> --prepared-redaction <handle>

…which consumes the handle exactly once. Handles expire after 10
minutes if not consumed.

Examples:
  SENDER_TOKEN=secret-value ntm redact prepare-mail --sender-token-env=SENDER_TOKEN --json
  ntm redact prepare-mail --sender-token-file=./secret.txt --json`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			env := senderTokenEnv
			file := senderTokenFile
			senderTokenEnv = ""
			senderTokenFile = ""

			if env == "" && file == "" {
				return fmt.Errorf("must provide exactly one of --sender-token-env or --sender-token-file")
			}
			if env != "" && file != "" {
				return fmt.Errorf("flags --sender-token-env and --sender-token-file are mutually exclusive")
			}

			var (
				raw    string
				source string
			)
			if env != "" {
				source = "env"
				raw = os.Getenv(env)
				if raw == "" {
					return fmt.Errorf("environment variable %q is empty or unset", env)
				}
			} else {
				source = "file"
				p, err := filepath.Abs(util.ExpandPath(file))
				if err != nil {
					return fmt.Errorf("resolve --sender-token-file %q: %w", file, err)
				}
				b, err := os.ReadFile(p)
				if err != nil {
					return fmt.Errorf("read %q: %w", p, err)
				}
				raw = string(b)
			}

			if cfg == nil {
				cfg = config.Default()
			}
			redactCfg := cfg.Redaction.ToRedactionLibConfig()
			redactCfg.Mode = redaction.ModeRedact

			res := redaction.ScanAndRedact(raw, redactCfg)
			redaction.AddLineInfo(raw, res.Findings)
			findings := make([]RedactPreviewFinding, 0, len(res.Findings))
			for _, f := range res.Findings {
				findings = append(findings, RedactPreviewFinding{
					Category: f.Category,
					Redacted: f.Redacted,
					Start:    f.Start,
					End:      f.End,
					Line:     f.Line,
					Column:   f.Column,
				})
			}

			handle, err := stashPreparedRedaction(raw, res.Output, findings)
			if err != nil {
				return err
			}

			resp := RedactPrepareMailResponse{
				TimestampedResponse: output.NewTimestamped(),
				Handle:              handle,
				ExpiresIn:           preparedRedactionTTL.String(),
				InputSource:         source,
				InputLen:            len(raw),
				Findings:            findings,
				RedactedView:        res.Output,
			}

			if IsJSONOutput() {
				return output.PrintJSON(resp)
			}
			fmt.Printf("handle: %s\n", resp.Handle)
			fmt.Printf("expires in: %s\n", resp.ExpiresIn)
			fmt.Printf("input source: %s (%d bytes)\n", resp.InputSource, resp.InputLen)
			fmt.Printf("findings: %d\n", len(resp.Findings))
			for _, f := range resp.Findings {
				fmt.Printf("- %s %s\n", f.Category, f.Redacted)
			}
			fmt.Println()
			fmt.Println("Pass the handle to: ntm mail send <session> --prepared-redaction <handle>")
			return nil
		},
	}

	cmd.Flags().StringVar(&senderTokenEnv, "sender-token-env", "", "Environment variable holding the sensitive token to prepare")
	cmd.Flags().StringVar(&senderTokenFile, "sender-token-file", "", "File path holding the sensitive token to prepare")

	return cmd
}

func newRedactPreviewCmd() *cobra.Command {
	var (
		text string
		file string
	)

	cmd := &cobra.Command{
		Use:   "preview",
		Short: "Preview redaction findings and safe-redacted output",
		Long: `Preview secret detection on input text (or a file) and print:
- A list of findings (category + position + placeholder)
- A safe-redacted output

This command never prints raw matched secrets, even if your configured redaction mode is warn/off.

Examples:
  ntm redact preview --text "password=hunter2hunter2"
  ntm redact preview --file ./notes.txt
  ntm redact preview --text "..." --json`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			// Cobra commands are reused across tests within the same process; flags bound via
			// StringVar can retain values between Execute() calls when a flag is omitted.
			// Snapshot and then reset to keep behavior deterministic for both tests and CLI.
			currentText := text
			currentFile := file
			text = ""
			file = ""

			if currentText == "" && currentFile == "" {
				return fmt.Errorf("must provide exactly one of --text or --file")
			}
			if currentText != "" && currentFile != "" {
				return fmt.Errorf("flags --text and --file are mutually exclusive")
			}

			source := "text"
			absPath := ""
			input := currentText
			if currentFile != "" {
				source = "file"
				p := util.ExpandPath(currentFile)
				abs, err := filepath.Abs(p)
				if err != nil {
					return fmt.Errorf("resolve --file %q: %w", currentFile, err)
				}
				b, err := os.ReadFile(abs)
				if err != nil {
					return fmt.Errorf("read %q: %w", abs, err)
				}
				absPath = abs
				input = string(b)
			}

			if cfg == nil {
				cfg = config.Default()
			}

			// Always compute a safe-redacted output for preview. This prevents accidental leaks
			// when the global config/flags are set to warn/off.
			redactCfg := cfg.Redaction.ToRedactionLibConfig()
			redactCfg.Mode = redaction.ModeRedact

			res := redaction.ScanAndRedact(input, redactCfg)
			redaction.AddLineInfo(input, res.Findings)

			findings := make([]RedactPreviewFinding, 0, len(res.Findings))
			for _, f := range res.Findings {
				findings = append(findings, RedactPreviewFinding{
					Category: f.Category,
					Redacted: f.Redacted,
					Start:    f.Start,
					End:      f.End,
					Line:     f.Line,
					Column:   f.Column,
				})
			}

			resp := RedactPreviewResponse{
				TimestampedResponse: output.NewTimestamped(),
				Source:              source,
				Path:                absPath,
				InputLen:            len(input),
				Findings:            findings,
				Output:              res.Output,
			}

			if IsJSONOutput() {
				return output.PrintJSON(resp)
			}

			if resp.Source == "file" {
				fmt.Printf("Source: %s\n", resp.Path)
			} else {
				fmt.Println("Source: text")
			}
			fmt.Printf("Findings: %d\n", len(resp.Findings))
			for _, f := range resp.Findings {
				if f.Line > 0 && f.Column > 0 {
					fmt.Printf("- %d:%d %s %s\n", f.Line, f.Column, f.Category, f.Redacted)
					continue
				}
				fmt.Printf("- %d-%d %s %s\n", f.Start, f.End, f.Category, f.Redacted)
			}
			fmt.Println()
			fmt.Println("Redacted output:")
			fmt.Print(resp.Output)
			if resp.Output != "" && !strings.HasSuffix(resp.Output, "\n") {
				fmt.Println()
			}
			return nil
		},
	}

	cmd.Flags().StringVar(&text, "text", "", "Input text to scan/redact (mutually exclusive with --file)")
	cmd.Flags().StringVar(&file, "file", "", "File to scan/redact (mutually exclusive with --text)")

	return cmd
}
