// docs_conformance_test.go — WS0-G3 docs-example conformance test
// (bd-ws0-guards-klz98.4).
//
// Docs become EXECUTED artifacts: every line beginning `ntm ` inside a fenced
// code block of README.md, SKILL.md, command_palette.md, and docs/**.md is
// resolved against the REAL cobra tree (cli.RootCommand().Find) and its flags
// are parsed with a no-execute pflag parse. This ends both-direction doc drift
// (H1 flattering-stale, H2 unflattering-stale — root-cause mechanism #3:
// "docs are not executed by anything").
//
// Real-world doc syntax handling (per the bead — decided, not hand-waved):
//   - Angle/brace placeholders (<session>, {name}) are substituted with `x`
//     before parsing.
//   - Commands are truncated at the first unquoted `|`, `>`, `&&`, `;`, and
//     trailing ` # comment`s are stripped.
//   - Lines governed by a `<!-- ntm-docs: skip -->` marker are skipped AND
//     counted; per-file counts must EXACTLY equal the `skip:<file>:<n>`
//     budget lines recorded in ci/allowlists/docs.txt (over budget = new
//     debt, under budget = stale budget — shrink it in the same PR). Same
//     comm-ratchet discipline as the other WS0 gates, in both directions.
//   - docs/planning/** is excluded: it is a proposals archive whose examples
//     are hypothetical designs by construction; executing them would ratchet
//     fiction, not reality (adaptation recorded on the G3 bead).
//   - The go.mod Go version must appear verbatim in the README badge, unless
//     a `badge-waiver:go-version` line is present in ci/allowlists/docs.txt
//     (tied to an open bead; also ratcheted: a waiver with a matching badge
//     is a stale line and fails).
//
// Mandatory canary: the fixture doc scripts/guards/testdata/docs_canary/
// canary.md contains a bogus `ntm --no-such-flag` example and a bogus
// subcommand; the checker MUST flag both or this test fails hard — a gate
// that cannot demonstrate a catch is a placebo with a green badge.
//
// Known blind spot (recorded in the bead): this proves examples PARSE, not
// that prose is truthful or pipelines behave; H1's per-section status markers
// and the Re-Audit Ritual's behavioral spot-probes compensate.
package tests

import (
	"archive/zip"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"testing"

	"github.com/spf13/cobra"
	"github.com/spf13/pflag"

	"github.com/Dicklesworthstone/ntm/internal/cli"
	"github.com/Dicklesworthstone/ntm/internal/plugins"
	"github.com/Dicklesworthstone/ntm/internal/robot"
)

const (
	docsAllowlistRel  = "ci/allowlists/docs.txt"
	docsSkipMarker    = "<!-- ntm-docs: skip -->"
	docsCanaryRel     = "scripts/guards/testdata/docs_canary/canary.md"
	skipBudgetEntry   = "skip-budget"
	badgeWaiverEntry  = "badge-waiver:go-version"
	docsWaiverProto   = "Waiver protocol: open a bead FIRST, then add/adjust the line in ci/allowlists/docs.txt (see ci/allowlists/README.md)."
	docsSkipHowTo     = "To waive a known-stale example, put `" + docsSkipMarker + "` on the line above it (or above its fenced block) AND bump the skip-budget line in ci/allowlists/docs.txt (bead-first)."
	placeholderSubst  = "x"
	readmeBadgePrefix = "img.shields.io/badge/go-"
)

// docsRepoRoot locates the repository root from this test file's location.
func docsRepoRoot(t *testing.T) string {
	t.Helper()
	_, thisFile, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("runtime.Caller failed; cannot locate repo root")
	}
	root, err := filepath.Abs(filepath.Join(filepath.Dir(thisFile), ".."))
	if err != nil {
		t.Fatalf("resolving repo root: %v", err)
	}
	return root
}

// docsWithShippedAgents starts the same test in an isolated configuration with
// the documented presets installed. The command constructors register plugin
// flags during package initialization, so changing the environment after cli
// has been imported cannot exercise that production path. Re-execution lets
// the real constructors load the real manifests before the gate reads the tree;
// the checker never invents flags or executes the documented commands.
func docsWithShippedAgents(t *testing.T) bool {
	t.Helper()
	const childConfigEnv = "NTM_DOCS_TEST_CONFIG"
	if childConfig := os.Getenv(childConfigEnv); childConfig != "" {
		if os.Getenv("NTM_CONFIG") != childConfig {
			t.Fatal("docs child did not select its isolated plugin configuration")
		}
		return true
	}

	configDir := t.TempDir()
	configPath := filepath.Join(configDir, "config.toml")
	if err := os.WriteFile(configPath, []byte("# Isolated configuration for documented agent presets.\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	agentsDir := filepath.Join(configDir, "agents")
	if err := os.MkdirAll(agentsDir, 0o700); err != nil {
		t.Fatal(err)
	}
	manifests, err := filepath.Glob(filepath.Join(docsRepoRoot(t), "examples", "agents", "*.toml"))
	if err != nil || len(manifests) == 0 {
		t.Fatalf("locate shipped agent manifests: files=%v error=%v", manifests, err)
	}
	for _, path := range manifests {
		contents, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(agentsDir, filepath.Base(path)), contents, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	args := []string{"-test.run=^" + regexp.QuoteMeta(t.Name()) + "$", "-test.count=1", "-test.timeout=2m", "-test.v"}
	if testing.Short() {
		args = append(args, "-test.short")
	}
	child := exec.CommandContext(t.Context(), executable, args...)
	for _, entry := range os.Environ() {
		key, _, _ := strings.Cut(entry, "=")
		if key != "NTM_CONFIG" && key != childConfigEnv {
			child.Env = append(child.Env, entry)
		}
	}
	child.Env = append(child.Env, "NTM_CONFIG="+configPath, childConfigEnv+"="+configPath)
	output, err := child.CombinedOutput()
	if err != nil {
		t.Fatalf("docs gate with installed shipped presets failed: %v\n%s", err, output)
	}
	for _, line := range strings.Split(string(output), "\n") {
		if strings.Contains(line, "docs conformance scanned") {
			t.Log(strings.TrimSpace(line))
		}
	}
	return false // The child ran this complete test against the initialized tree.
}

// docExample is one extracted `ntm ...` line from a fenced code block.
type docExample struct {
	file    string // repo-relative path
	line    int    // 1-based line number
	raw     string // original line content
	skipped bool   // governed by an ntm-docs: skip marker
}

// extractExamples walks a markdown file and returns every `ntm ` line found
// inside fenced code blocks as well as standalone “ `ntm ...` “ command
// cells in markdown tables, honoring backslash continuations and the
// `<!-- ntm-docs: skip -->` marker. A marker directly above a fenced block or
// table waives every ntm-example in that block/table; a marker inside a block
// waives the remaining ntm-lines of that block.
func extractExamples(relPath string, content string) []docExample {
	var out []docExample
	lines := strings.Split(content, "\n")
	inFence := false
	fenceSkipped := false
	pendingSkip := false

	inTable := false
	tableSkipped := false
	var tableHeaders []string

	for i := 0; i < len(lines); i++ {
		trimmed := strings.TrimSpace(lines[i])
		if strings.HasPrefix(trimmed, "```") || strings.HasPrefix(trimmed, "~~~") {
			inTable = false
			tableSkipped = false
			tableHeaders = nil
			if inFence {
				inFence = false
				fenceSkipped = false
			} else {
				inFence = true
				fenceSkipped = pendingSkip
				pendingSkip = false
			}
			continue
		}
		if strings.Contains(trimmed, docsSkipMarker) && !strings.HasPrefix(trimmed, "|") {
			if inFence {
				fenceSkipped = true
			} else {
				pendingSkip = true
			}
			continue
		}
		if !inFence {
			rowSkipped := false
			rowText := trimmed
			if strings.Contains(rowText, docsSkipMarker) {
				rowSkipped = true
				rowText = strings.TrimSpace(strings.ReplaceAll(rowText, docsSkipMarker, ""))
			}

			if cells, ok := splitMarkdownTableRow(rowText); ok {
				if !inTable {
					if i+1 < len(lines) {
						if nextCells, nextOK := splitMarkdownTableRow(strings.TrimSpace(lines[i+1])); nextOK && isMarkdownTableSeparator(nextCells) {
							inTable = true
							tableSkipped = pendingSkip
							pendingSkip = false
							tableHeaders = cells
							i++ // consume separator row
							continue
						}
					}
				} else {
					for colIdx, cell := range cells {
						header := ""
						if colIdx < len(tableHeaders) {
							header = tableHeaders[colIdx]
						}
						for _, cmd := range extractTableCellCommands(header, cell) {
							out = append(out, docExample{
								file:    relPath,
								line:    i + 1,
								raw:     cmd,
								skipped: tableSkipped || rowSkipped,
							})
						}
					}
					continue
				}
			}

			inTable = false
			tableSkipped = false
			tableHeaders = nil

			// A marker only reaches across blank lines, not across prose.
			if pendingSkip && trimmed != "" {
				pendingSkip = false
			}
			continue
		}

		cmd, ok := docsCommandLine(trimmed)
		if !ok {
			continue
		}
		startLine := i + 1
		// Join backslash continuations.
		for strings.HasSuffix(cmd, "\\") && i+1 < len(lines) {
			i++
			cmd = strings.TrimSuffix(cmd, "\\") + " " + strings.TrimSpace(lines[i])
		}
		out = append(out, docExample{
			file:    relPath,
			line:    startLine,
			raw:     cmd,
			skipped: fenceSkipped,
		})
	}
	return out
}

var tableSepCellRE = regexp.MustCompile(`^:?-{2,}:?$`)

func splitMarkdownTableRow(trimmed string) ([]string, bool) {
	if !strings.HasPrefix(trimmed, "|") || !strings.HasSuffix(trimmed, "|") || len(trimmed) < 2 {
		return nil, false
	}
	inner := trimmed[1 : len(trimmed)-1]
	var cells []string
	var cur strings.Builder
	inCode := false
	runes := []rune(inner)
	for i := 0; i < len(runes); i++ {
		c := runes[i]
		if c == '\\' && i+1 < len(runes) && runes[i+1] == '|' {
			cur.WriteRune('|')
			i++
			continue
		}
		if c == '`' {
			inCode = !inCode
			cur.WriteRune(c)
			continue
		}
		if c == '|' && !inCode {
			cells = append(cells, strings.TrimSpace(cur.String()))
			cur.Reset()
			continue
		}
		cur.WriteRune(c)
	}
	cells = append(cells, strings.TrimSpace(cur.String()))
	return cells, len(cells) > 0
}

func isMarkdownTableSeparator(cells []string) bool {
	if len(cells) == 0 {
		return false
	}
	for _, cell := range cells {
		if !tableSepCellRE.MatchString(cell) {
			return false
		}
	}
	return true
}

func extractTableCellCommands(header string, cell string) []string {
	headerLower := strings.ToLower(strings.TrimSpace(header))
	isExampleCol := strings.Contains(headerLower, "example")

	if strings.HasPrefix(cell, "`") && strings.HasSuffix(cell, "`") && strings.Count(cell, "`") == 2 {
		inner := strings.TrimSpace(cell[1 : len(cell)-1])
		if cmd, ok := docsCommandLine(inner); ok && isStandaloneTableCommand(cmd, isExampleCol) {
			return []string{cmd}
		}
		return nil
	}

	if !isExampleCol {
		return nil
	}

	var cmds []string
	rest := cell
	for {
		start := strings.IndexByte(rest, '`')
		if start < 0 {
			break
		}
		rest = rest[start+1:]
		end := strings.IndexByte(rest, '`')
		if end < 0 {
			break
		}
		span := strings.TrimSpace(rest[:end])
		rest = rest[end+1:]
		if cmd, ok := docsCommandLine(span); ok && isStandaloneTableCommand(cmd, true) {
			cmds = append(cmds, cmd)
		}
	}
	return cmds
}

func isStandaloneTableCommand(cmd string, inExampleCol bool) bool {
	if strings.Contains(cmd, "...") || strings.Contains(cmd, "/--") {
		return false
	}
	if inExampleCol {
		return strings.HasPrefix(cmd, "ntm ")
	}
	// Outside an explicit Example(s) column, only check full command examples
	// (with `=value` or multiple flags/args), not bare single-flag reference labels.
	if !strings.HasPrefix(cmd, "ntm --") {
		return false
	}
	return strings.Contains(cmd, "=") || len(strings.Fields(cmd)) >= 3
}

// docsCommandLine reports whether a fenced-block line is an ntm example,
// returning the command text (shell prompt stripped).
func docsCommandLine(trimmed string) (string, bool) {
	if strings.HasPrefix(trimmed, "$ ") {
		trimmed = strings.TrimSpace(strings.TrimPrefix(trimmed, "$ "))
	}
	if trimmed == "ntm" || strings.HasPrefix(trimmed, "ntm ") {
		return trimmed, true
	}
	return "", false
}

var (
	anglePlaceholderRE = regexp.MustCompile(`<[^<>|]+>`)
	bracePlaceholderRE = regexp.MustCompile(`\{[^{}]*\}`)
)

// normalizeExample turns a raw doc line into argv for the cobra tree:
// placeholder substitution, truncation at unquoted shell operators, comment
// stripping, and shell-aware tokenization.
func normalizeExample(raw string) ([]string, error) {
	s := anglePlaceholderRE.ReplaceAllString(raw, placeholderSubst)
	s = bracePlaceholderRE.ReplaceAllString(s, placeholderSubst)

	tokens, err := shellTokens(s)
	if err != nil {
		return nil, err
	}
	if len(tokens) == 0 || tokens[0] != "ntm" {
		return nil, fmt.Errorf("not an ntm command after normalization: %q", raw)
	}
	return tokens[1:], nil
}

// shellTokens tokenizes a single command line, respecting single/double
// quotes and backslash escapes, truncating at the first unquoted `|`, `>`,
// `<`, `;`, `&&`, or ` # comment`.
func shellTokens(s string) ([]string, error) {
	var tokens []string
	var cur strings.Builder
	inTok := false
	quote := rune(0)

	flush := func() {
		if inTok {
			tokens = append(tokens, cur.String())
			cur.Reset()
			inTok = false
		}
	}

	runes := []rune(s)
	for i := 0; i < len(runes); i++ {
		c := runes[i]
		if quote != 0 {
			if c == quote {
				quote = 0
			} else {
				cur.WriteRune(c)
			}
			continue
		}
		switch c {
		case '\'', '"':
			quote = c
			inTok = true
		case '\\':
			if i+1 < len(runes) {
				i++
				cur.WriteRune(runes[i])
				inTok = true
			}
		case ' ', '\t':
			flush()
		case '|', ';', '<':
			flush()
			return tokens, nil
		case '>':
			// handles `>`, `2>`, `&>`: drop a trailing fd token like "2"
			if inTok {
				if t := cur.String(); t == "2" || t == "&" || t == "1" {
					cur.Reset()
					inTok = false
				}
			}
			flush()
			return tokens, nil
		case '&':
			if i+1 < len(runes) && runes[i+1] == '&' {
				flush()
				return tokens, nil
			}
			cur.WriteRune(c)
			inTok = true
		case '#':
			if !inTok {
				return tokens, nil
			}
			cur.WriteRune(c)
		default:
			cur.WriteRune(c)
			inTok = true
		}
	}
	if quote != 0 {
		return nil, fmt.Errorf("unbalanced %q quote", string(quote))
	}
	flush()
	return tokens, nil
}

// checkExample resolves argv against the real cobra tree and parses its flags
// without executing anything. Returns a non-nil error describing the drift.
func checkExample(root *cobra.Command, argv []string) error {
	return checkExampleForFile(root, "", argv)
}

func checkExampleForFile(root *cobra.Command, rel string, argv []string) error {
	cmd, rest, err := root.Find(argv)
	if err != nil {
		return fmt.Errorf("unknown command: %v", err)
	}

	fs := pflag.NewFlagSet("docs-conformance", pflag.ContinueOnError)
	fs.Usage = func() {}
	fs.SetOutput(io.Discard)
	fs.AddFlagSet(cmd.Flags())
	fs.AddFlagSet(cmd.InheritedFlags())
	fs.VisitAll(func(f *pflag.Flag) {
		f.Changed = false
	})
	defer func() {
		fs.VisitAll(func(f *pflag.Flag) {
			if !f.Changed {
				return
			}
			if sv, ok := f.Value.(pflag.SliceValue); ok && (f.DefValue == "[]" || f.DefValue == "") {
				_ = sv.Replace(nil)
			} else {
				_ = f.Value.Set(f.DefValue)
			}
			f.Changed = false
		})
	}()
	if err := fs.Parse(rest); err != nil {
		return fmt.Errorf("flag parse failed for %q: %v", cmd.CommandPath(), err)
	}

	// A non-runnable command that has subcommands and is left with positional
	// args means the doc names a subcommand that does not exist (cobra's
	// legacyArgs only enforces this at the root).
	if cmd.HasSubCommands() && !cmd.Runnable() {
		if pos := fs.Args(); len(pos) > 0 {
			return fmt.Errorf("unknown subcommand %q for %q", pos[0], cmd.CommandPath())
		}
	}

	if err := checkMarkdownSectionsFlag(fs); err != nil {
		return err
	}
	if err := checkRobotEventsModifierFlags(fs); err != nil {
		return err
	}
	if err := checkNoDeprecatedFlagsInAgentDocs(rel, fs); err != nil {
		return err
	}
	return nil
}

func checkNoDeprecatedFlagsInAgentDocs(rel string, fs *pflag.FlagSet) error {
	if rel != "SKILL.md" && rel != "AGENTS.md" && !strings.HasPrefix(rel, "references/") {
		return nil
	}
	var found error
	fs.VisitAll(func(f *pflag.Flag) {
		if found != nil || !f.Changed || f.Deprecated == "" {
			return
		}
		if rel == "AGENTS.md" && f.Name == "md-sections" {
			return
		}
		found = fmt.Errorf("deprecated flag --%s in %s (%s)", f.Name, rel, f.Deprecated)
	})
	return found
}

func checkMarkdownSectionsFlag(fs *pflag.FlagSet) error {
	valid := robot.ValidMarkdownSections()
	validSet := make(map[string]struct{}, len(valid))
	for _, sec := range valid {
		validSet[sec] = struct{}{}
	}

	flagNames := []string{}
	if fs.Changed("md-sections") {
		flagNames = append(flagNames, "md-sections")
	}
	if fs.Changed("sections") && fs.Changed("robot-markdown") {
		flagNames = append(flagNames, "sections")
	}

	for _, flagName := range flagNames {
		raw, err := fs.GetString(flagName)
		if err != nil {
			continue
		}
		parts := strings.Split(raw, ",")
		hasAny := false
		for _, part := range parts {
			sec := strings.ToLower(strings.TrimSpace(part))
			if sec == "" {
				continue
			}
			hasAny = true
			if _, ok := validSet[sec]; !ok {
				return fmt.Errorf("invalid --%s value %q (valid sections: %s)", flagName, part, strings.Join(valid, ", "))
			}
		}
		if !hasAny {
			return fmt.Errorf("empty --%s value (valid sections: %s)", flagName, strings.Join(valid, ", "))
		}
	}
	return nil
}

func checkRobotEventsModifierFlags(fs *pflag.FlagSet) error {
	if !fs.Changed("robot-events") {
		return nil
	}
	for _, pair := range [][2]string{
		{"limit", "events-limit"},
		{"category", "events-category"},
		{"session", "events-session"},
	} {
		if fs.Changed(pair[0]) {
			return fmt.Errorf("--robot-events ignores --%s; use --%s instead", pair[0], pair[1])
		}
	}
	return nil
}

// docsAllowlist holds the G3 entries parsed from ci/allowlists/docs.txt.
type docsAllowlist struct {
	skipBudgets    map[string]int // repo-relative file -> expected skip count
	badgeWaiverSet bool
}

func loadDocsAllowlist(t *testing.T, root string) docsAllowlist {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(root, docsAllowlistRel))
	if err != nil {
		t.Fatalf("reading %s: %v", docsAllowlistRel, err)
	}
	al := docsAllowlist{skipBudgets: map[string]int{}}
	for n, line := range strings.Split(string(data), "\n") {
		line = strings.TrimRight(line, "\r")
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		fields := strings.Split(line, "\t")
		if len(fields) != 3 {
			t.Fatalf("%s:%d: malformed line (need entry\\tbead-id\\treason): %q", docsAllowlistRel, n+1, line)
		}
		entry := fields[0]
		switch {
		case strings.HasPrefix(entry, skipBudgetEntry+":"):
			spec := strings.TrimPrefix(entry, skipBudgetEntry+":")
			colon := strings.LastIndex(spec, ":")
			if colon <= 0 {
				t.Fatalf("%s:%d: bad skip entry %q (want %s:<file>:<count>)", docsAllowlistRel, n+1, entry, skipBudgetEntry)
			}
			file, countStr := spec[:colon], spec[colon+1:]
			v, convErr := strconv.Atoi(countStr)
			if convErr != nil || v <= 0 {
				t.Fatalf("%s:%d: bad skip count in %q", docsAllowlistRel, n+1, entry)
			}
			if _, dup := al.skipBudgets[file]; dup {
				t.Fatalf("%s:%d: duplicate skip budget for %s", docsAllowlistRel, n+1, file)
			}
			al.skipBudgets[file] = v
		case entry == badgeWaiverEntry:
			al.badgeWaiverSet = true
		default:
			t.Fatalf("%s:%d: unknown G3 entry class %q (known: %s:<file>:<n>, %s)",
				docsAllowlistRel, n+1, entry, skipBudgetEntry, badgeWaiverEntry)
		}
	}
	return al
}

// docsFileSet returns the repo-relative markdown files the gate executes.
func docsFileSet(t *testing.T, root string) []string {
	t.Helper()
	files := []string{"AGENTS.md", "README.md", "SKILL.md", "command_palette.md"}
	for _, dirName := range []string{"docs", "references"} {
		dirPath := filepath.Join(root, dirName)
		if _, statErr := os.Stat(dirPath); os.IsNotExist(statErr) {
			continue
		}
		err := filepath.Walk(dirPath, func(path string, info os.FileInfo, err error) error {
			if err != nil {
				return err
			}
			rel, relErr := filepath.Rel(root, path)
			if relErr != nil {
				return relErr
			}
			rel = filepath.ToSlash(rel)
			if info.IsDir() {
				if rel == "docs/planning" { // proposals archive: hypothetical designs, excluded by design
					return filepath.SkipDir
				}
				return nil
			}
			if !strings.HasSuffix(path, ".md") {
				return nil
			}
			files = append(files, rel)
			return nil
		})
		if err != nil {
			t.Fatalf("walking %s/: %v", dirName, err)
		}
	}
	sort.Strings(files)
	return files
}

// runDocsCheck extracts and checks every example in the given files.
// Returns violations (one string per broken example), skipped examples, and total extracted examples.
func runDocsCheck(t *testing.T, root string, files []string) (violations []string, skips []docExample, totalExamples int) {
	t.Helper()
	for _, rel := range files {
		data, err := os.ReadFile(filepath.Join(root, rel))
		if err != nil {
			t.Fatalf("reading %s: %v", rel, err)
		}
		v, s, n := runDocsCheckCountOn(t, rel, string(data))
		violations = append(violations, v...)
		skips = append(skips, s...)
		totalExamples += n
	}
	return violations, skips, totalExamples
}

// TestDocsExamplesCanary is the mandatory merge-blocking canary: the checker
// must FIRE on the fixture doc's bogus flag and bogus subcommand, as well as
// bool-flag non-bool values, invalid --md-sections values, mismatched
// --robot-events modifier flags, and broken markdown table examples, or the
// gate is a placebo and this test fails hard.
func TestDocsExamplesCanary(t *testing.T) {
	if !docsWithShippedAgents(t) {
		return
	}
	root := docsRepoRoot(t)
	data, err := os.ReadFile(filepath.Join(root, docsCanaryRel))
	if err != nil {
		t.Fatalf("CANARY FAILURE — cannot read fixture %s: %v", docsCanaryRel, err)
	}
	violations, skips := runDocsCheckOn(t, docsCanaryRel, string(data))

	joined := strings.Join(violations, "\n")
	if !strings.Contains(joined, "--no-such-flag") {
		t.Fatalf("CANARY FAILURE — gate did not catch the bogus `ntm --no-such-flag` example in %s.\nViolations:\n%s", docsCanaryRel, joined)
	}
	if !strings.Contains(joined, "definitely-not-a-command") {
		t.Fatalf("CANARY FAILURE — gate did not catch the bogus `ntm definitely-not-a-command` example in %s.\nViolations:\n%s", docsCanaryRel, joined)
	}
	if len(skips) != 1 {
		t.Fatalf("CANARY FAILURE — skip-marker handling broken: fixture has exactly 1 skipped example, gate counted %d", len(skips))
	}
	// The fixture's valid example must NOT be flagged (no false positives).
	if strings.Contains(joined, "ntm version --short") {
		t.Fatalf("CANARY FAILURE — gate false-positived on the fixture's valid example.\nViolations:\n%s", joined)
	}

	// Extended G3 canaries: bool-flag=non-bool, invalid --md-sections,
	// --robot-events unprefixed modifiers, and markdown table extraction.
	syntheticDoc := strings.Join([]string{
		"```bash",
		"ntm --robot-overlay=myproject",
		"ntm --robot-markdown --md-sections=sessions,not_a_real_section",
		"ntm --robot-events --since-cursor=42 --limit=20",
		"ntm --robot-markdown --md-sections=sessions,beads",
		"```",
		"",
		"| Flag | Description | Example |",
		"|------|-------------|---------|",
		"| `--robot-markdown` | Valid table example | `ntm --robot-markdown --md-sections=sessions,beads` |",
		"| `--robot-bogus` | Broken table example | `ntm --no-such-table-flag` |",
	}, "\n")
	synViolations, synSkips := runDocsCheckOn(t, "synthetic-canary.md", syntheticDoc)
	if len(synSkips) != 0 {
		t.Fatalf("CANARY FAILURE — unexpected skips in synthetic canary: %v", synSkips)
	}
	synJoined := strings.Join(synViolations, "\n")
	for _, mustCatch := range []string{
		"--robot-overlay=myproject",
		"not_a_real_section",
		"--robot-events ignores --limit",
		"--no-such-table-flag",
	} {
		if !strings.Contains(synJoined, mustCatch) {
			t.Fatalf("CANARY FAILURE — gate did not catch %q.\nViolations:\n%s", mustCatch, synJoined)
		}
	}
	if len(synViolations) != 4 {
		t.Fatalf("CANARY FAILURE — expected exactly 4 synthetic violations (no false positive on valid beads section), got %d:\n%s", len(synViolations), synJoined)
	}

	depViolations, _ := runDocsCheckOn(t, "SKILL.md", "```bash\nntm --robot-ack=myproject --ack-timeout=30s\n```\n")
	if len(depViolations) != 1 || !strings.Contains(depViolations[0], "deprecated flag --ack-timeout") {
		t.Fatalf("CANARY FAILURE — gate did not catch deprecated flag in SKILL.md: %v", depViolations)
	}
}

func TestCheckExampleShippedPluginsUseProductionFlags(t *testing.T) {
	if !docsWithShippedAgents(t) {
		return
	}
	agentPlugins, err := plugins.LoadAgentPlugins(filepath.Join(docsRepoRoot(t), "examples", "agents"))
	if err != nil || len(agentPlugins) == 0 {
		t.Fatalf("load shipped presets: plugins=%v error=%v", agentPlugins, err)
	}
	var valid, invalid strings.Builder
	valid.WriteString("```bash\n")
	invalid.WriteString("```bash\n")
	wantInvalid := 0
	for _, plugin := range agentPlugins {
		for _, name := range []string{plugin.Name, plugin.Alias} {
			if name == "" {
				continue
			}
			for _, command := range []string{"spawn", "add", "resume"} {
				cmd, _, err := cli.RootCommand().Find([]string{command})
				if err != nil || cmd.Flags().Lookup(name) == nil {
					t.Fatalf("production %s constructor did not register --%s: %v", command, name, err)
				}
				if command == "resume" {
					fmt.Fprintf(&valid, "ntm resume demo --spawn --%s=1\n", name)
				} else {
					fmt.Fprintf(&valid, "ntm %s demo --%s=1\n", command, name)
				}
				// Real count/spec flag validators must run; a synthetic string
				// flag would incorrectly accept this malformed agent count.
				fmt.Fprintf(&invalid, "ntm %s demo --%s=not-a-count\n", command, name)
				wantInvalid++
			}
			fmt.Fprintf(&valid, "ntm send demo --%s 'review the change'\n", name)
			fmt.Fprintf(&valid, "ntm send demo --%s=reviewer 'review the change'\n", name)
			fmt.Fprintf(&invalid, "ntm status demo --%s\n", name)
			wantInvalid++
		}
	}
	valid.WriteString("```\n")
	invalid.WriteString("ntm resume demo --spawn --no-such-agent-plugin=1\n```\n")
	wantInvalid++
	if violations, skips := runDocsCheckOn(t, "shipped-plugin-examples.md", valid.String()); len(violations) != 0 || len(skips) != 0 {
		t.Fatalf("real shipped-plugin examples failed: violations=%v skips=%v", violations, skips)
	}
	violations, skips := runDocsCheckOn(t, "invalid-plugin-examples.md", invalid.String())
	if len(violations) != wantInvalid || len(skips) != 0 || !strings.Contains(strings.Join(violations, "\n"), "--no-such-agent-plugin") {
		t.Fatalf("plugin typo/unsupported-command/count canaries: got %d violations, want %d; violations=%v skips=%v", len(violations), wantInvalid, violations, skips)
	}
}

func runDocsCheckOn(t *testing.T, rel, content string) (violations []string, skips []docExample) {
	t.Helper()
	v, s, _ := runDocsCheckCountOn(t, rel, content)
	return v, s
}

func runDocsCheckCountOn(t *testing.T, rel, content string) (violations []string, skips []docExample, total int) {
	t.Helper()
	cobraRoot := cli.RootCommand()
	examples := extractExamples(rel, content)
	for _, ex := range examples {
		if ex.skipped {
			skips = append(skips, ex)
			continue
		}
		argv, err := normalizeExample(ex.raw)
		if err != nil {
			violations = append(violations, fmt.Sprintf("%s:%d: %v (line: %s)", ex.file, ex.line, err, ex.raw))
			continue
		}
		if err := checkExampleForFile(cobraRoot, rel, argv); err != nil {
			violations = append(violations, fmt.Sprintf("%s:%d: %v (line: %s)", ex.file, ex.line, err, ex.raw))
		}
	}
	return violations, skips, len(examples)
}

// TestDocsExamplesConform executes every fenced and table `ntm` example in the
// real docs against the real cobra tree, and ratchets the skip budget in both
// directions against ci/allowlists/docs.txt.
func TestDocsExamplesConform(t *testing.T) {
	if !docsWithShippedAgents(t) {
		return
	}
	root := docsRepoRoot(t)
	al := loadDocsAllowlist(t, root)
	files := docsFileSet(t, root)
	violations, skips, totalExamples := runDocsCheck(t, root, files)
	t.Logf("docs conformance scanned %d markdown files and extracted %d ntm examples (%d executed, %d skip-marked)",
		len(files), totalExamples, totalExamples-len(skips), len(skips))

	if len(violations) > 0 {
		t.Errorf("docs-example conformance FAIL — %d example(s) do not parse against the real ntm command tree:\n  %s\n%s\n%s",
			len(violations), strings.Join(violations, "\n  "), docsSkipHowTo, docsWaiverProto)
	}

	// Per-file skip-budget ratchet, both directions.
	actual := map[string]int{}
	byFile := map[string][]string{}
	for _, ex := range skips {
		actual[ex.file]++
		byFile[ex.file] = append(byFile[ex.file], fmt.Sprintf("%s:%d: %s", ex.file, ex.line, ex.raw))
	}
	files2 := map[string]bool{}
	for f := range actual {
		files2[f] = true
	}
	for f := range al.skipBudgets {
		files2[f] = true
	}
	var sorted []string
	for f := range files2 {
		sorted = append(sorted, f)
	}
	sort.Strings(sorted)
	for _, f := range sorted {
		got, want := actual[f], al.skipBudgets[f]
		switch {
		case got > want:
			t.Errorf("skip-budget RATCHET FAIL — %s has %d skip-marked example(s) but budget is %d (NEW DEBT — bead first, then bump %s:%s:<n> in %s):\n  %s",
				f, got, want, skipBudgetEntry, f, docsAllowlistRel, strings.Join(byFile[f], "\n  "))
		case got < want:
			t.Errorf("skip-budget RATCHET FAIL — %s budget is %d but only %d example(s) are skip-marked (STALE budget — shrink/delete the %s:%s:<n> line in %s in this PR)",
				f, want, got, skipBudgetEntry, f, docsAllowlistRel)
		}
	}
}

// TestReadmeGoVersionBadge asserts go.mod's Go version appears verbatim in
// the README badge (the H7 drift class), ratcheted through the
// badge-waiver:go-version line in ci/allowlists/docs.txt.
func TestReadmeGoVersionBadge(t *testing.T) {
	root := docsRepoRoot(t)
	al := loadDocsAllowlist(t, root)

	gomod, err := os.ReadFile(filepath.Join(root, "go.mod"))
	if err != nil {
		t.Fatalf("reading go.mod: %v", err)
	}
	goVersion := ""
	for _, line := range strings.Split(string(gomod), "\n") {
		if v, ok := strings.CutPrefix(strings.TrimSpace(line), "go "); ok {
			goVersion = strings.TrimSpace(v)
			break
		}
	}
	if goVersion == "" {
		t.Fatal("could not find `go <version>` directive in go.mod")
	}

	readme, err := os.ReadFile(filepath.Join(root, "README.md"))
	if err != nil {
		t.Fatalf("reading README.md: %v", err)
	}
	badgeLine := ""
	for _, line := range strings.Split(string(readme), "\n") {
		if strings.Contains(line, readmeBadgePrefix) {
			badgeLine = line
			break
		}
	}
	if badgeLine == "" {
		t.Fatalf("README.md has no Go version badge (expected a line containing %q)", readmeBadgePrefix)
	}

	badgeMatches := strings.Contains(badgeLine, readmeBadgePrefix+goVersion)
	switch {
	case !badgeMatches && !al.badgeWaiverSet:
		t.Errorf("README Go badge does not contain go.mod's version %q (badge line: %s)\n%s",
			goVersion, strings.TrimSpace(badgeLine), docsWaiverProto)
	case badgeMatches && al.badgeWaiverSet:
		t.Errorf("RATCHET FAIL — README badge matches go.mod (%s) but %s still carries a %q line (STALE — delete it in this PR)",
			goVersion, docsAllowlistRel, badgeWaiverEntry)
	}
}

// TestReadmeGhcrImageMatchesCI is the H7 ghcr grep-gate
// (bd-ws7-docs-ux-truth-tqh3l.7): the release workflow pushes multi-arch
// images to ${REGISTRY}/${github.repository}; the README must reference
// EXACTLY that image coordinate (with a docker-pull example), so the docs
// cannot cite a wrong image name. G3's example executor cannot cover this —
// docker lines are not `ntm ` commands — hence this dedicated gate. The
// coordinate is DERIVED (registry from release.yml, owner/repo from go.mod's
// module path, lowercased per docker naming), never hardcoded.
func TestReadmeGhcrImageMatchesCI(t *testing.T) {
	root := docsRepoRoot(t)

	workflow, err := os.ReadFile(filepath.Join(root, ".github/workflows/release.yml"))
	if err != nil {
		t.Fatalf("reading release.yml: %v", err)
	}
	wf := string(workflow)

	// Registry host from the workflow env block.
	registry := ""
	for _, line := range strings.Split(wf, "\n") {
		if v, ok := strings.CutPrefix(strings.TrimSpace(line), "REGISTRY:"); ok {
			registry = strings.TrimSpace(v)
			break
		}
	}
	if registry == "" {
		t.Fatal("release.yml has no `REGISTRY:` env entry — if the docker push moved, update this gate in the same PR")
	}

	// The workflow must still push to REGISTRY/IMAGE_NAME with
	// IMAGE_NAME = github.repository; anchor the derivation to reality.
	if !strings.Contains(wf, "IMAGE_NAME: ${{ github.repository }}") {
		t.Fatal("release.yml no longer sets IMAGE_NAME from github.repository — update this gate's coordinate derivation in the same PR")
	}
	if !strings.Contains(wf, "images: ${{ env.REGISTRY }}/${{ env.IMAGE_NAME }}") {
		t.Fatal("release.yml docker metadata no longer uses REGISTRY/IMAGE_NAME — update this gate in the same PR")
	}

	// Owner/repo from go.mod's module path (github.com/<owner>/<repo>),
	// lowercased: docker image names are lowercase even when the GitHub
	// repository is not.
	gomod, err := os.ReadFile(filepath.Join(root, "go.mod"))
	if err != nil {
		t.Fatalf("reading go.mod: %v", err)
	}
	repoPath := ""
	for _, line := range strings.Split(string(gomod), "\n") {
		if v, ok := strings.CutPrefix(strings.TrimSpace(line), "module "); ok {
			repoPath = strings.ToLower(strings.TrimPrefix(strings.TrimSpace(v), "github.com/"))
			break
		}
	}
	if repoPath == "" || strings.Count(repoPath, "/") != 1 {
		t.Fatalf("could not derive <owner>/<repo> from go.mod module path (got %q)", repoPath)
	}

	image := registry + "/" + repoPath // e.g. ghcr.io/dicklesworthstone/ntm

	readme, err := os.ReadFile(filepath.Join(root, "README.md"))
	if err != nil {
		t.Fatalf("reading README.md: %v", err)
	}
	rd := string(readme)

	if !strings.Contains(rd, image) {
		t.Errorf("README.md never references the CI-pushed image coordinate %q — the release workflow pushes multi-arch images there; document them (Installation → Docker)", image)
	}
	if !strings.Contains(rd, "docker pull "+image) {
		t.Errorf("README.md has no `docker pull %s` example matching the image CI pushes", image)
	}

	// The platforms CI builds must be the platforms the README claims.
	for _, line := range strings.Split(wf, "\n") {
		if v, ok := strings.CutPrefix(strings.TrimSpace(line), "platforms:"); ok {
			for _, plat := range strings.Split(strings.TrimSpace(v), ",") {
				plat = strings.TrimSpace(plat)
				if plat != "" && !strings.Contains(rd, plat) {
					t.Errorf("release.yml builds platform %q but README.md never mentions it in the Docker section", plat)
				}
			}
			break
		}
	}
}

// TestVSCodeExtensionManifestAndVSIXContract verifies that vscode/package.json
// stays version-locked to VERSION, every declared VS Code command is registered
// in vscode/src/*.ts, every CLI command/flag invoked by the VS Code extension
// parses cleanly against the real Cobra tree, and (when present) the built
// .vsix archive contains the compiled extension entrypoints.
func TestVSCodeExtensionManifestAndVSIXContract(t *testing.T) {
	root := docsRepoRoot(t)

	versionBytes, err := os.ReadFile(filepath.Join(root, "VERSION"))
	if err != nil {
		t.Fatalf("reading VERSION: %v", err)
	}
	repoVersion := strings.TrimPrefix(strings.TrimSpace(string(versionBytes)), "v")

	pkgBytes, err := os.ReadFile(filepath.Join(root, "vscode", "package.json"))
	if err != nil {
		t.Fatalf("reading vscode/package.json: %v", err)
	}
	var manifest struct {
		Name    string `json:"name"`
		Version string `json:"version"`
		Main    string `json:"main"`
		Engines struct {
			VSCode string `json:"vscode"`
		} `json:"engines"`
		Contributes struct {
			Commands []struct {
				Command string `json:"command"`
				Title   string `json:"title"`
			} `json:"commands"`
			Configuration struct {
				Properties map[string]struct {
					Type    string `json:"type"`
					Default any    `json:"default"`
				} `json:"properties"`
			} `json:"configuration"`
		} `json:"contributes"`
	}
	if err := json.Unmarshal(pkgBytes, &manifest); err != nil {
		t.Fatalf("unmarshaling vscode/package.json: %v", err)
	}

	if manifest.Version != repoVersion {
		t.Errorf("vscode/package.json version %q != VERSION %q", manifest.Version, repoVersion)
	}
	if manifest.Engines.VSCode != "^1.90.0" {
		t.Errorf("vscode/package.json engines.vscode = %q, want ^1.90.0", manifest.Engines.VSCode)
	}
	if manifest.Main != "./out/extension.js" {
		t.Errorf("vscode/package.json main = %q, want ./out/extension.js", manifest.Main)
	}
	if prop, ok := manifest.Contributes.Configuration.Properties["ntm.webPort"]; !ok || prop.Type != "number" {
		t.Errorf("vscode/package.json missing numeric ntm.webPort configuration property: %+v", prop)
	}

	tsFiles, err := filepath.Glob(filepath.Join(root, "vscode", "src", "*.ts"))
	if err != nil || len(tsFiles) == 0 {
		t.Fatalf("locating vscode/src/*.ts: files=%v err=%v", tsFiles, err)
	}
	var allTS strings.Builder
	for _, path := range tsFiles {
		data, readErr := os.ReadFile(path)
		if readErr != nil {
			t.Fatalf("reading %s: %v", path, readErr)
		}
		allTS.Write(data)
		allTS.WriteByte('\n')
	}
	tsCorpus := allTS.String()

	declaredCommands := make(map[string]bool, len(manifest.Contributes.Commands))
	for _, cmd := range manifest.Contributes.Commands {
		declaredCommands[cmd.Command] = true
		regRE := regexp.MustCompile(`registerCommand\(\s*['"]` + regexp.QuoteMeta(cmd.Command) + `['"]`)
		if !regRE.MatchString(tsCorpus) {
			t.Errorf("vscode/package.json command %q is never registered via registerCommand in vscode/src/*.ts", cmd.Command)
		}
	}
	for _, required := range []string{"ntm.spawnEnsemble", "ntm.triageAttention", "ntm.openWebCockpit"} {
		if !declaredCommands[required] {
			t.Errorf("vscode/package.json missing required Wave 5 command %q", required)
		}
	}

	// Verify every CLI command/flag invoked by vscode/src/{ntmClient,extension}.ts
	// resolves cleanly against the real Cobra command tree.
	cobraRoot := cli.RootCommand()
	extensionInvocations := [][]string{
		{"--robot-snapshot", "--robot-format=json"},
		{"--robot-status", "--robot-format=json"},
		{"--robot-mail", "--robot-format=json"},
		{"--robot-ensemble-presets", "--robot-format=json"},
		{"--robot-ensemble-suggest=architecture review", "--robot-format=json"},
		{"--robot-ensemble-spawn=proj-ensemble", "--preset=project-diagnosis", "--question=check risks", "--agents=cc=2", "--assignment=affinity", "--allow-advanced", "--project=/tmp/proj", "--robot-format=json"},
		{"attention", "list", "--session=proj", "--limit=30", "--all", "--json"},
		{"attention", "ack", "1", "--json"},
		{"attention", "pin", "1", "--json"},
		{"attention", "unpin", "1", "--json"},
		{"attention", "snooze", "1", "--until=30m", "--json"},
		{"attention", "dismiss", "1", "--json"},
		{"web", "--port", "7337"},
		{"spawn", "proj", "--cc=2", "--json"},
		{"send", "proj", "--all", "--", "hello"},
		{"palette", "proj"},
		{"attach", "proj"},
		{"view", "proj"},
		{"zoom", "proj", "1"},
		{"dashboard", "proj"},
	}
	for _, argv := range extensionInvocations {
		if err := checkExample(cobraRoot, argv); err != nil {
			t.Errorf("VS Code CLI invocation `ntm %s` failed Cobra parse contract: %v", strings.Join(argv, " "), err)
		}
	}

	vsixPath := filepath.Join(root, "vscode", "dist", fmt.Sprintf("ntm-vscode-%s.vsix", repoVersion))
	if info, statErr := os.Stat(vsixPath); statErr == nil && !info.IsDir() {
		zr, zipErr := zip.OpenReader(vsixPath)
		if zipErr != nil {
			t.Fatalf("opening %s: %v", vsixPath, zipErr)
		}
		defer zr.Close()
		entries := make(map[string]bool, len(zr.File))
		for _, f := range zr.File {
			entries[f.Name] = true
		}
		for _, wantEntry := range []string{
			"extension/package.json",
			"extension/out/extension.js",
			"extension/out/ntmClient.js",
		} {
			if !entries[wantEntry] {
				t.Errorf("built VSIX %s missing required entry %q", vsixPath, wantEntry)
			}
		}
	}
}
