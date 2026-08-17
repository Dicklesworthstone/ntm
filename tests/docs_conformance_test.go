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
	"fmt"
	"io"
	"os"
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

// docExample is one extracted `ntm ...` line from a fenced code block.
type docExample struct {
	file    string // repo-relative path
	line    int    // 1-based line number
	raw     string // original line content
	skipped bool   // governed by an ntm-docs: skip marker
}

// extractExamples walks a markdown file and returns every `ntm ` line found
// inside fenced code blocks, honoring backslash continuations and the
// `<!-- ntm-docs: skip -->` marker. A marker directly above a fenced block
// waives every ntm-line in that block; a marker inside a block waives the
// remaining ntm-lines of that block.
func extractExamples(relPath string, content string) []docExample {
	var out []docExample
	lines := strings.Split(content, "\n")
	inFence := false
	fenceSkipped := false
	pendingSkip := false

	for i := 0; i < len(lines); i++ {
		trimmed := strings.TrimSpace(lines[i])
		if strings.HasPrefix(trimmed, "```") || strings.HasPrefix(trimmed, "~~~") {
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
		if strings.Contains(trimmed, docsSkipMarker) {
			if inFence {
				fenceSkipped = true
			} else {
				pendingSkip = true
			}
			continue
		}
		if !inFence {
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
	cmd, rest, err := root.Find(argv)
	if err != nil {
		return fmt.Errorf("unknown command: %v", err)
	}

	fs := pflag.NewFlagSet("docs-conformance", pflag.ContinueOnError)
	fs.Usage = func() {}
	fs.SetOutput(io.Discard)
	fs.AddFlagSet(cmd.Flags())
	fs.AddFlagSet(cmd.InheritedFlags())
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
	files := []string{"README.md", "SKILL.md", "command_palette.md"}
	err := filepath.Walk(filepath.Join(root, "docs"), func(path string, info os.FileInfo, err error) error {
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
		t.Fatalf("walking docs/: %v", err)
	}
	sort.Strings(files)
	return files
}

// runDocsCheck extracts and checks every example in the given files.
// Returns violations (one string per broken example) and the skipped examples.
func runDocsCheck(t *testing.T, root string, files []string) (violations []string, skips []docExample) {
	t.Helper()
	for _, rel := range files {
		data, err := os.ReadFile(filepath.Join(root, rel))
		if err != nil {
			t.Fatalf("reading %s: %v", rel, err)
		}
		v, s := runDocsCheckOn(t, rel, string(data))
		violations = append(violations, v...)
		skips = append(skips, s...)
	}
	return violations, skips
}

// TestDocsExamplesCanary is the mandatory merge-blocking canary: the checker
// must FIRE on the fixture doc's bogus flag and bogus subcommand, or the gate
// is a placebo and this test fails hard.
func TestDocsExamplesCanary(t *testing.T) {
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
}

func runDocsCheckOn(t *testing.T, rel, content string) (violations []string, skips []docExample) {
	t.Helper()
	cobraRoot := cli.RootCommand()
	for _, ex := range extractExamples(rel, content) {
		if ex.skipped {
			skips = append(skips, ex)
			continue
		}
		argv, err := normalizeExample(ex.raw)
		if err != nil {
			violations = append(violations, fmt.Sprintf("%s:%d: %v (line: %s)", ex.file, ex.line, err, ex.raw))
			continue
		}
		if err := checkExample(cobraRoot, argv); err != nil {
			violations = append(violations, fmt.Sprintf("%s:%d: %v (line: %s)", ex.file, ex.line, err, ex.raw))
		}
	}
	return violations, skips
}

// TestDocsExamplesConform executes every fenced `ntm` example in the real
// docs against the real cobra tree, and ratchets the skip budget in both
// directions against ci/allowlists/docs.txt.
func TestDocsExamplesConform(t *testing.T) {
	root := docsRepoRoot(t)
	al := loadDocsAllowlist(t, root)
	files := docsFileSet(t, root)
	violations, skips := runDocsCheck(t, root, files)

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
