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
	"go/ast"
	"go/parser"
	"go/token"
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

// allowedStdoutPrefixes lists UI/CLI presentation packages that legitimately
// render to stdout. Non-UI library packages under internal/ must use log/slog
// or an explicit io.Writer so robot/JSON stdout envelopes are never corrupted
// (bd-rc-track-b-core-bugs-y2bre.2).
var allowedStdoutPrefixes = []string{
	"internal/cli/",
	"internal/tui/",
	"internal/output/",
	"internal/robot/",
	"internal/palette/",
}

// allowedStdoutFiles lists explicit human-CLI helper files outside UI packages.
var allowedStdoutFiles = map[string]bool{
	"internal/hooks/precommit.go": true,
}

func TestNoLibraryStdoutPrints(t *testing.T) {
	root := docsRepoRoot(t)
	internalRoot := filepath.Join(root, "internal")
	fset := token.NewFileSet()

	var offenders []string
	err := filepath.WalkDir(internalRoot, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			if d.Name() == "testdata" {
				return filepath.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
			return nil
		}
		rel, rerr := filepath.Rel(root, path)
		if rerr != nil {
			return rerr
		}
		rel = filepath.ToSlash(rel)
		if allowedStdoutFiles[rel] {
			return nil
		}
		for _, prefix := range allowedStdoutPrefixes {
			if strings.HasPrefix(rel, prefix) {
				return nil
			}
		}

		fileAST, perr := parser.ParseFile(fset, path, nil, 0)
		if perr != nil {
			return fmt.Errorf("parse %s: %w", rel, perr)
		}

		fmtIdent := ""
		osIdent := ""
		for _, imp := range fileAST.Imports {
			pkgPath := strings.Trim(imp.Path.Value, `"`)
			switch pkgPath {
			case "fmt":
				if imp.Name != nil {
					fmtIdent = imp.Name.Name
				} else {
					fmtIdent = "fmt"
				}
			case "os":
				if imp.Name != nil {
					osIdent = imp.Name.Name
				} else {
					osIdent = "os"
				}
			}
		}
		if fmtIdent == "" {
			return nil
		}

		ast.Inspect(fileAST, func(n ast.Node) bool {
			call, ok := n.(*ast.CallExpr)
			if !ok {
				return true
			}
			sel, ok := call.Fun.(*ast.SelectorExpr)
			if !ok {
				return true
			}
			pkg, ok := sel.X.(*ast.Ident)
			if !ok || pkg.Name != fmtIdent {
				return true
			}
			switch sel.Sel.Name {
			case "Print", "Printf", "Println":
				pos := fset.Position(call.Pos())
				offenders = append(offenders, fmt.Sprintf("%s:%d: %s.%s", rel, pos.Line, fmtIdent, sel.Sel.Name))
			case "Fprint", "Fprintf", "Fprintln":
				if osIdent != "" && len(call.Args) > 0 {
					if argSel, ok := call.Args[0].(*ast.SelectorExpr); ok {
						if argPkg, ok := argSel.X.(*ast.Ident); ok && argPkg.Name == osIdent && argSel.Sel.Name == "Stdout" {
							pos := fset.Position(call.Pos())
							offenders = append(offenders, fmt.Sprintf("%s:%d: %s.%s(%s.Stdout, ...)", rel, pos.Line, fmtIdent, sel.Sel.Name, osIdent))
						}
					}
				}
			}
			return true
		})
		return nil
	})
	if err != nil {
		t.Fatalf("walking internal/: %v", err)
	}

	if len(offenders) > 0 {
		t.Errorf("LIBRARY STDOUT LEAK — non-UI internal packages must not write directly to stdout (use slog or an io.Writer; bd-rc-track-b-core-bugs-y2bre.2):\n  %s",
			strings.Join(offenders, "\n  "))
	}
}
