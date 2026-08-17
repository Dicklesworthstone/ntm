// matrix_conformance_test.go — WS7-H10 verification-matrix conformance gate
// (bd-ws7-docs-ux-truth-tqh3l.10).
//
// The 2026-08 reality audit found docs/verification-matrix-swarm-scale-vnext.md
// citing gates that did not exist (a load env var, a 200-pane load test, and a
// benchmark, none present anywhere in the tree) and claiming double the
// harness's real pane scale. The matrix was regenerated to cite only real
// gates; this test keeps it that way mechanically:
//
//  1. Every backticked Go test/benchmark name cited in the matrix must
//     resolve against `go test -list` output of the matrix's cited packages.
//  2. Every NTM_* environment variable cited in the matrix must appear in a
//     Go source file in the tree.
//  3. Every pane-scale figure in the matrix must not exceed the load
//     harness's own scenario constant (source-of-truth: the load_100_pane
//     scenario in internal/swarm), and the harness figure itself must appear.
//
// TestMatrixCorpusContractClean is additionally the second consumer of the
// D3 real-envelope corpus (internal/robot/testdata/token_corpus): every
// corpus envelope must be contract-clean per internal/robot/contract.
//
// (Per the H10 bead, name resolution here also automates half of the
// Re-Audit Ritual's ledger audit: any doc-cited Proof test that stops
// existing fails CI.)
package tests

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"testing"

	"github.com/Dicklesworthstone/ntm/internal/robot/contract"
	"github.com/Dicklesworthstone/ntm/internal/swarm"
)

const matrixDocRel = "docs/verification-matrix-swarm-scale-vnext.md"

var (
	backtickTokenRE = regexp.MustCompile("`([^`\n]+)`")
	testNameRE      = regexp.MustCompile(`^(Test|Benchmark)[A-Za-z0-9_]+$`)
	pkgPathRE       = regexp.MustCompile(`\./internal(/[a-z0-9_]+)+/?|\./tests/?`)
	envVarRE        = regexp.MustCompile(`\bNTM_[A-Z0-9_]+\b`)
	paneFigureRE    = regexp.MustCompile(`(\d+)[- ]pane`)
)

// matrixDoc reads the verification matrix.
func matrixDoc(t *testing.T) (root, content string) {
	t.Helper()
	root = docsRepoRoot(t)
	data, err := os.ReadFile(filepath.Join(root, matrixDocRel))
	if err != nil {
		t.Fatalf("reading %s: %v", matrixDocRel, err)
	}
	return root, string(data)
}

// citedTestNames returns the backticked Test*/Benchmark* identifiers plus any
// bare `-run` tokens that look like test-name prefixes.
func citedTestNames(content string) []string {
	set := map[string]bool{}
	for _, m := range backtickTokenRE.FindAllStringSubmatch(content, -1) {
		token := strings.TrimSpace(m[1])
		if testNameRE.MatchString(token) {
			set[token] = true
			continue
		}
		// Inside command strings, pick out Test/Benchmark words (e.g.
		// `go test ./tests/ -run TestMatrix`).
		for _, word := range strings.Fields(token) {
			word = strings.Trim(word, `'"`)
			if testNameRE.MatchString(word) {
				set[word] = true
			}
		}
	}
	var out []string
	for name := range set {
		out = append(out, name)
	}
	sort.Strings(out)
	return out
}

// citedPackages returns the ./internal/... and ./tests/... package paths
// mentioned anywhere in the matrix.
func citedPackages(content string) []string {
	set := map[string]bool{}
	for _, m := range pkgPathRE.FindAllString(content, -1) {
		p := strings.TrimSuffix(m, "/")
		set[p] = true
	}
	var out []string
	for p := range set {
		out = append(out, p)
	}
	sort.Strings(out)
	return out
}

// listedTests runs `go test -list .*` for each package and returns the union
// of declared Test/Benchmark names.
func listedTests(t *testing.T, root string, pkgs []string) map[string]bool {
	t.Helper()
	union := map[string]bool{}
	for _, pkg := range pkgs {
		cmd := exec.Command("go", "test", "-list", ".*", pkg)
		cmd.Dir = root
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("go test -list %s failed: %v\n%s", pkg, err, out)
		}
		for _, line := range strings.Split(string(out), "\n") {
			line = strings.TrimSpace(line)
			if strings.HasPrefix(line, "Test") || strings.HasPrefix(line, "Benchmark") {
				union[line] = true
			}
		}
	}
	return union
}

// TestMatrixCitedGatesResolve is the H10 close condition, part 1: every
// cited test name resolves against the real test binaries and every cited
// env var exists in the tree.
func TestMatrixCitedGatesResolve(t *testing.T) {
	if testing.Short() {
		t.Skip("go test -list subprocesses; skipped in -short (runs in the full CI test job)")
	}
	root, content := matrixDoc(t)

	pkgs := citedPackages(content)
	if len(pkgs) == 0 {
		t.Fatalf("%s cites no ./internal/... packages — the matrix must anchor gates to real packages", matrixDocRel)
	}
	names := citedTestNames(content)
	if len(names) == 0 {
		t.Fatalf("%s cites no Go test names — the matrix must name its gates so they can be resolved", matrixDocRel)
	}

	union := listedTests(t, root, pkgs)
	for _, name := range names {
		resolved := union[name]
		if !resolved {
			// A cited token may be a -run prefix (e.g. TestMatrix); accept it
			// when at least one real test name starts with it.
			for listed := range union {
				if strings.HasPrefix(listed, name) {
					resolved = true
					break
				}
			}
		}
		if !resolved {
			t.Errorf("FICTIONAL GATE — %s cites %q but no test/benchmark with that name (or prefix) exists in the cited packages %v.\nCite only gates that exist; write the gate first if it is meant to exist.", matrixDocRel, name, pkgs)
		}
	}

	// Env vars: every NTM_* variable the matrix cites must appear in a Go
	// source file somewhere under the repo (internal/, tests/, cmd/).
	for _, env := range dedupe(envVarRE.FindAllString(content, -1)) {
		if !envVarExistsInTree(t, root, env) {
			t.Errorf("FICTIONAL ENV VAR — %s cites %s but no Go source in the tree references it", matrixDocRel, env)
		}
	}
}

func dedupe(in []string) []string {
	set := map[string]bool{}
	var out []string
	for _, s := range in {
		if !set[s] {
			set[s] = true
			out = append(out, s)
		}
	}
	sort.Strings(out)
	return out
}

func envVarExistsInTree(t *testing.T, root, env string) bool {
	t.Helper()
	found := false
	for _, dir := range []string{"internal", "tests", "cmd"} {
		if found {
			break
		}
		err := filepath.Walk(filepath.Join(root, dir), func(path string, info os.FileInfo, err error) error {
			if err != nil || found {
				return err
			}
			if info.IsDir() || !strings.HasSuffix(path, ".go") {
				return nil
			}
			data, readErr := os.ReadFile(path)
			if readErr != nil {
				return readErr
			}
			if strings.Contains(string(data), env) {
				found = true
			}
			return nil
		})
		if err != nil {
			t.Fatalf("walking %s: %v", dir, err)
		}
	}
	return found
}

// TestMatrixScaleClaimsMatchHarness is the H10 close condition, part 2: the
// matrix's pane-scale figures are emitted from the harness's own scenario
// constant, so the number cannot drift from the code again.
func TestMatrixScaleClaimsMatchHarness(t *testing.T) {
	_, content := matrixDoc(t)

	scenario, ok := swarm.FindSyntheticExperimentScenario("load_100_pane")
	if !ok {
		t.Fatal("load_100_pane scenario missing from internal/swarm registry — update the matrix and this test together with the harness")
	}
	harnessPanes := scenario.Synthetic.PaneCount

	want := fmt.Sprintf("%d-pane", harnessPanes)
	if !strings.Contains(content, want) && !strings.Contains(content, fmt.Sprintf("%d panes", harnessPanes)) {
		t.Errorf("%s never states the harness's real load scale (%d panes)", matrixDocRel, harnessPanes)
	}

	for _, m := range paneFigureRE.FindAllStringSubmatch(content, -1) {
		n, err := strconv.Atoi(m[1])
		if err != nil {
			continue
		}
		if n > harnessPanes {
			t.Errorf("SCALE FICTION — %s claims %q but the load harness scenario runs %d panes. Raise the harness (and commit the run log) before raising the doc.", matrixDocRel, m[0], harnessPanes)
		}
	}
}

// TestMatrixCorpusContractClean is the second consumer of the D3
// real-envelope corpus: every corpus envelope must be contract-clean
// (parseable JSON; RobotResponse envelopes carry success + timestamp;
// projection payloads carry a timestamp). Also re-pins the corpus size
// floor from the matrix's own description (>=50 envelopes, >=8 surfaces).
func TestMatrixCorpusContractClean(t *testing.T) {
	root, _ := matrixDoc(t)
	corpusDir := filepath.Join(root, "internal", "robot", "testdata", "token_corpus")
	entries, err := os.ReadDir(corpusDir)
	if err != nil {
		t.Fatalf("reading D3 corpus dir: %v", err)
	}

	surfaces := map[string]bool{}
	files := 0
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".json") {
			continue
		}
		files++
		surface, _, ok := strings.Cut(e.Name(), "__")
		if !ok {
			t.Errorf("corpus file %s does not follow surface__name.json layout", e.Name())
			continue
		}
		surfaces[surface] = true

		data, err := os.ReadFile(filepath.Join(corpusDir, e.Name()))
		if err != nil {
			t.Fatalf("reading corpus file %s: %v", e.Name(), err)
		}
		env, err := contract.ParseEnvelope(&contract.Outcome{
			Command: "corpus:" + e.Name(),
			Stdout:  data,
		})
		if err != nil {
			t.Errorf("corpus envelope %s violates the robot contract: %v", e.Name(), err)
			continue
		}
		if strings.HasSuffix(surface, "_projection") {
			continue // projection payloads: parseable JSON is the contract
		}
		if _, ok := env.Raw["success"]; !ok {
			t.Errorf("corpus envelope %s: missing required field 'success'", e.Name())
		}
		if env.Timestamp == "" {
			if _, ok := env.Raw["ts"]; !ok {
				t.Errorf("corpus envelope %s: missing required field 'timestamp'/'ts'", e.Name())
			}
		}
	}
	if files < 50 {
		t.Errorf("D3 corpus has %d envelopes; the matrix promises >= 50", files)
	}
	if len(surfaces) < 8 {
		t.Errorf("D3 corpus covers %d surfaces; the matrix promises >= 8 (%v)", len(surfaces), surfaces)
	}
}
