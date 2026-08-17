// stale_tool_refs_test.go — WS7-H9 stale-tool-name grep-gate
// (bd-ws7-docs-ux-truth-tqh3l.9).
//
// The beads CLI is `br` (beads_rust); the retired `bd` tool was migrated out
// (see the bd-to-br migration), yet robot close-hints still told agents to
// run "bd list"/"bd sync". This gate greps every Go source under internal/
// and cmd/ for `bd <subcommand>` tool invocations so a stale hint cannot
// regress. Bead-ID prefixes like `bd-ws7-...` do not match — the pattern
// requires a word-boundary `bd` followed by a space and a known subcommand.
package tests

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// staleBdRefRE matches invocations/hints of the retired `bd` CLI.
var staleBdRefRE = regexp.MustCompile(`\bbd (list|sync|init|ready|show|close|create|update|dep|doctor|quickstart)\b`)

func TestNoStaleBdToolReferences(t *testing.T) {
	root := docsRepoRoot(t)

	var offenders []string
	for _, dir := range []string{"internal", "cmd"} {
		err := filepath.WalkDir(filepath.Join(root, dir), func(path string, d os.DirEntry, err error) error {
			if err != nil {
				return err
			}
			if d.IsDir() || !strings.HasSuffix(path, ".go") {
				return nil
			}
			data, rerr := os.ReadFile(path)
			if rerr != nil {
				return rerr
			}
			for i, line := range strings.Split(string(data), "\n") {
				if m := staleBdRefRE.FindString(line); m != "" {
					rel, _ := filepath.Rel(root, path)
					offenders = append(offenders, fmt.Sprintf("%s:%d: %s", rel, i+1, m))
				}
			}
			return nil
		})
		if err != nil {
			t.Fatalf("walking %s: %v", dir, err)
		}
	}

	if len(offenders) > 0 {
		t.Errorf("STALE TOOL REFERENCE — the beads CLI is `br`, not `bd`; fix these to `br <subcommand>` (bd-ws7-docs-ux-truth-tqh3l.9):\n  %s",
			strings.Join(offenders, "\n  "))
	}
}
