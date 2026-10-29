package cli

import (
	"encoding/json"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/Dicklesworthstone/ntm/internal/config"
	"github.com/Dicklesworthstone/ntm/internal/redaction"
	"github.com/Dicklesworthstone/ntm/internal/util"
)

type scrubFinding struct {
	Path     string             `json:"path"`
	Category redaction.Category `json:"category"`
	Start    int                `json:"start"`
	End      int                `json:"end"`
	Line     int                `json:"line,omitempty"`
	Column   int                `json:"column,omitempty"`
	Preview  string             `json:"preview"`
}

type scrubResult struct {
	Roots        []string       `json:"roots"`
	FilesScanned int            `json:"files_scanned"`
	Findings     []scrubFinding `json:"findings"`
	Warnings     []string       `json:"warnings,omitempty"`
}

func newScrubCmd() *cobra.Command {
	var (
		paths  []string
		since  string
		format string
	)

	cmd := &cobra.Command{
		Use:   "scrub",
		Short: "Scan NTM artifacts for leaked secrets (read-only)",
		Long: `Scan files/directories using the built-in redaction engine and report findings.

By default, this scans common NTM artifact locations:
  - The directory containing the active config file
  - ~/.ntm

Output is always redacted (placeholders only). Raw secret matches are never printed.

Examples:
  ntm scrub
  ntm scrub --path .ntm --path ~/.config/ntm --since 24h
  ntm scrub --format json
  ntm scrub --json`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			outFormat := strings.ToLower(strings.TrimSpace(format))
			if IsJSONOutput() {
				outFormat = "json"
			} else if outFormat == "" {
				outFormat = "text"
			}
			switch outFormat {
			case "text", "json":
			default:
				return fmt.Errorf("invalid --format %q: must be text or json", format)
			}

			roots, err := resolveScrubRoots(paths)
			if err != nil {
				return err
			}
			if len(roots) == 0 {
				if outFormat == "json" {
					return json.NewEncoder(os.Stdout).Encode(scrubResult{
						Roots:    []string{},
						Findings: []scrubFinding{},
					})
				}
				fmt.Println("No scrub roots found.")
				return nil
			}

			var cutoff *time.Time
			if since != "" {
				d, err := util.ParseDuration(since)
				if err != nil {
					return fmt.Errorf("invalid --since value: %w", err)
				}
				t := time.Now().Add(-d)
				cutoff = &t
			}

			if cfg == nil {
				cfg = config.Default()
			}
			redactCfg := cfg.Redaction.ToRedactionLibConfig()

			res := runScrub(roots, cutoff, redactCfg)
			switch outFormat {
			case "json":
				enc := json.NewEncoder(os.Stdout)
				enc.SetIndent("", "  ")
				return enc.Encode(res)
			default:
				printScrubText(res)
				return nil
			}
		},
	}

	cmd.Flags().StringArrayVar(&paths, "path", nil, "Path to scan (repeatable). Defaults to the active config directory and ~/.ntm")
	cmd.Flags().StringVar(&since, "since", "", "Only scan files modified within this duration (e.g. 1h, 7d)")
	cmd.Flags().StringVar(&format, "format", "", "Output format: text|json (default: text, or json if --json)")

	return cmd
}

func resolveScrubRoots(paths []string) ([]string, error) {
	var roots []string
	if len(paths) > 0 {
		for _, p := range paths {
			p = config.ExpandHome(p)
			abs, err := filepath.Abs(p)
			if err != nil {
				return nil, fmt.Errorf("resolving --path %q: %w", p, err)
			}
			roots = append(roots, abs)
		}
		return uniqueStrings(roots), nil
	}

	// Default roots: active config dir + ~/.ntm
	roots = append(roots, selectedConfigDir())
	if ntmDir, err := util.NTMDir(); err == nil {
		roots = append(roots, ntmDir)
	}

	// Keep only existing roots
	var existing []string
	for _, r := range roots {
		if _, err := os.Stat(r); err == nil {
			existing = append(existing, r)
		}
	}

	sort.Strings(existing)
	return existing, nil
}

func runScrub(roots []string, cutoff *time.Time, cfg redaction.Config) scrubResult {
	res := scrubResult{
		Roots:    append([]string{}, roots...),
		Findings: []scrubFinding{},
	}

	for _, root := range roots {
		info, err := os.Stat(root)
		if err != nil {
			res.Warnings = append(res.Warnings, fmt.Sprintf("stat %s: %v", root, err))
			continue
		}

		if info.Mode().IsRegular() {
			res.scanFile(root, cutoff, cfg)
			continue
		}

		// Walk directory
		_ = filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
			if err != nil {
				res.Warnings = append(res.Warnings, fmt.Sprintf("walk %s: %v", path, err))
				return nil
			}

			// Avoid giant irrelevant trees by default.
			if d.IsDir() && d.Name() == ".git" {
				return filepath.SkipDir
			}

			// Skip symlinks to avoid cycles.
			if d.Type()&os.ModeSymlink != 0 {
				if d.IsDir() {
					return filepath.SkipDir
				}
				return nil
			}

			if d.Type().IsRegular() {
				res.scanFile(path, cutoff, cfg)
			}
			return nil
		})
	}

	return res
}

func (r *scrubResult) scanFile(path string, cutoff *time.Time, cfg redaction.Config) {
	info, err := os.Stat(path)
	if err != nil {
		r.Warnings = append(r.Warnings, fmt.Sprintf("stat %s: %v", path, err))
		return
	}
	if cutoff != nil && info.ModTime().Before(*cutoff) {
		return
	}

	data, err := os.ReadFile(path)
	if err != nil {
		r.Warnings = append(r.Warnings, fmt.Sprintf("read %s: %v", path, err))
		return
	}
	if looksBinary(data) {
		return
	}

	r.FilesScanned++

	content := string(data)
	findings := redaction.Scan(content, cfg)
	redaction.AddLineInfo(content, findings)
	for _, f := range findings {
		preview := scrubPreview(content, f, findings)
		r.Findings = append(r.Findings, scrubFinding{
			Path:     path,
			Category: f.Category,
			Start:    f.Start,
			End:      f.End,
			Line:     f.Line,
			Column:   f.Column,
			Preview:  preview,
		})
	}
}

func scrubPreview(input string, f redaction.Finding, findings []redaction.Finding) string {
	const context = 40
	start := f.Start - context
	if start < 0 {
		start = 0
	}
	end := f.End + context
	if end > len(input) {
		end = len(input)
	}

	// Use findings from the complete input, including any that cross a preview
	// boundary. Rescanning a cropped snippet can miss a neighboring secret when
	// its identifying key or token prefix has fallen outside the context window.
	// Positive-length findings are disjoint, but custom patterns may also match
	// zero-width anchors inside them. Search by start, then include the preceding
	// positive-length finding in case it crosses the left boundary.
	first := sort.Search(len(findings), func(i int) bool { return findings[i].Start >= start })
	for first > 0 {
		first--
		if findings[first].End > findings[first].Start {
			break
		}
	}
	var preview strings.Builder
	pos := start
	for _, nearby := range findings[first:] {
		if nearby.Start > end || (nearby.Start == end && nearby.End > nearby.Start) {
			break
		}
		// Do not move backward into a credential already covered by a placeholder.
		if nearby.End < pos || (nearby.End == pos && nearby.Start < pos) {
			continue
		}
		if nearby.Start > pos {
			preview.WriteString(input[pos:nearby.Start])
		}
		preview.WriteString(nearby.Redacted)
		pos = min(nearby.End, end)
	}
	preview.WriteString(input[pos:end])
	snippet := preview.String()

	snippet = strings.ReplaceAll(snippet, "\r\n", "\n")
	snippet = strings.ReplaceAll(snippet, "\n", `\n`)
	snippet = strings.ReplaceAll(snippet, "\t", `\t`)

	if start > 0 {
		snippet = "…" + snippet
	}
	if end < len(input) {
		snippet = snippet + "…"
	}
	return snippet
}

func printScrubText(res scrubResult) {
	fmt.Println("Scrub roots:")
	for _, r := range res.Roots {
		fmt.Printf("- %s\n", r)
	}
	fmt.Println()

	if len(res.Warnings) > 0 {
		fmt.Println("Warnings:")
		for _, w := range res.Warnings {
			fmt.Printf("- %s\n", w)
		}
		fmt.Println()
	}

	if len(res.Findings) == 0 {
		fmt.Println("No findings.")
		return
	}

	for _, f := range res.Findings {
		loc := fmt.Sprintf("%s:%d:%d", f.Path, f.Line, f.Column)
		if f.Line == 0 || f.Column == 0 {
			loc = fmt.Sprintf("%s:%d", f.Path, f.Start)
		}
		fmt.Printf("%s %s %s\n", loc, f.Category, f.Preview)
	}

	fmt.Println()
	fmt.Printf("Total: %d findings across %d files.\n", len(res.Findings), res.FilesScanned)
}

func looksBinary(data []byte) bool {
	// Simple heuristic: if the file contains a NUL byte, treat as binary.
	for _, b := range data {
		if b == 0 {
			return true
		}
	}
	return false
}
