package cli

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"testing"

	"github.com/Dicklesworthstone/ntm/internal/robot"
	"github.com/Dicklesworthstone/ntm/internal/scanner"
)

type recordingIncrementalScanner struct {
	calledMethod string
	calledPath   string
	calledOpts   scanner.ScanOptions
	result       *scanner.ScanResult
}

func (r *recordingIncrementalScanner) Scan(_ context.Context, path string, opts scanner.ScanOptions) (*scanner.ScanResult, error) {
	r.calledMethod = "Scan"
	r.calledPath = path
	r.calledOpts = opts
	return r.result, nil
}

func (r *recordingIncrementalScanner) ScanFile(_ context.Context, file string) (*scanner.ScanResult, error) {
	r.calledMethod = "ScanFile"
	r.calledPath = file
	return r.result, nil
}

func (r *recordingIncrementalScanner) ScanStaged(_ context.Context, dir string) (*scanner.ScanResult, error) {
	r.calledMethod = "ScanStaged"
	r.calledPath = dir
	return r.result, nil
}

func (r *recordingIncrementalScanner) ScanDiff(_ context.Context, dir string) (*scanner.ScanResult, error) {
	r.calledMethod = "ScanDiff"
	r.calledPath = dir
	return r.result, nil
}

func TestScanCLI_IncrementalDispatchAndValidation(t *testing.T) {
	dir := t.TempDir()
	targetFile := filepath.Join(dir, "handler.go")
	if err := os.WriteFile(targetFile, []byte("package demo\n"), 0o644); err != nil {
		t.Fatalf("write handler.go: %v", err)
	}

	origAvail := scanIsAvailable
	origNewRunner := newScanRunner
	t.Cleanup(func() {
		scanIsAvailable = origAvail
		newScanRunner = origNewRunner
	})

	rec := &recordingIncrementalScanner{
		result: &scanner.ScanResult{
			Project:  dir,
			Totals:   scanner.ScanTotals{Files: 1},
			Findings: []scanner.Finding{},
		},
	}
	scanIsAvailable = func() bool { return true }
	newScanRunner = func() (scanRunner, error) { return rec, nil }

	t.Run("--staged dispatches Scanner.ScanStaged", func(t *testing.T) {
		rec.calledMethod = ""
		stdout, err := captureStdout(t, func() error {
			_, runErr := execCommand(t, "scan", dir, "--staged", "--json")
			return runErr
		})
		if err != nil {
			t.Fatalf("ntm scan --staged: %v", err)
		}
		if rec.calledMethod != "ScanStaged" || rec.calledPath != dir {
			t.Fatalf("called %s(%q), want ScanStaged(%q)", rec.calledMethod, rec.calledPath, dir)
		}
		var out map[string]interface{}
		if jsonErr := json.Unmarshal([]byte(stdout), &out); jsonErr != nil || out["success"] != true {
			t.Fatalf("unexpected scan JSON output: err=%v stdout=%s", jsonErr, stdout)
		}
	})

	t.Run("--diff dispatches Scanner.ScanDiff", func(t *testing.T) {
		rec.calledMethod = ""
		_, err := captureStdout(t, func() error {
			_, runErr := execCommand(t, "scan", dir, "--diff", "--json")
			return runErr
		})
		if err != nil {
			t.Fatalf("ntm scan --diff: %v", err)
		}
		if rec.calledMethod != "ScanDiff" || rec.calledPath != dir {
			t.Fatalf("called %s(%q), want ScanDiff(%q)", rec.calledMethod, rec.calledPath, dir)
		}
	})

	t.Run("--file dispatches Scanner.ScanFile", func(t *testing.T) {
		rec.calledMethod = ""
		_, err := captureStdout(t, func() error {
			_, runErr := execCommand(t, "scan", "--file", targetFile, "--json")
			return runErr
		})
		if err != nil {
			t.Fatalf("ntm scan --file: %v", err)
		}
		if rec.calledMethod != "ScanFile" || rec.calledPath != targetFile {
			t.Fatalf("called %s(%q), want ScanFile(%q)", rec.calledMethod, rec.calledPath, targetFile)
		}
	})

	t.Run("rejects conflicting incremental flags", func(t *testing.T) {
		for _, args := range [][]string{
			{"scan", dir, "--staged", "--diff"},
			{"scan", "--file", targetFile, "--staged"},
			{"scan", "--file", targetFile, dir},
			{"scan", "--file", filepath.Join(dir, "missing.go")},
			{"scan", "--file", dir},
		} {
			if _, err := execCommand(t, args...); err == nil {
				t.Fatalf("expected error for args %v, got nil", args)
			}
		}
	})
}

func TestRobotUBSScanRootFlagEndToEnd(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("stub ubs uses POSIX sh")
	}

	projectDir := t.TempDir()
	binDir := t.TempDir()
	argsFile := filepath.Join(binDir, "args.txt")
	reportFile := filepath.Join(binDir, "report.json")
	report := `{"project":"` + projectDir + `","status":"ok","totals":{"files":1,"critical":0,"warning":1,"info":0},"findings":[{"rule_id":"go.warn.1","severity":"warning","file":"a.go","line":7,"col":1,"message":"check error","category_id":"bug"}]}`
	if err := os.WriteFile(reportFile, []byte(report), 0o600); err != nil {
		t.Fatalf("write report: %v", err)
	}
	script := "#!/bin/sh\nset -eu\nif [ \"${1:-}\" = \"--version\" ]; then echo 'ubs 1.9.0'; exit 0; fi\nprintf '%s\\n' \"$*\" > \"" + argsFile + "\"\ncat \"" + reportFile + "\"\n"
	if err := os.WriteFile(filepath.Join(binDir, "ubs"), []byte(script), 0o755); err != nil {
		t.Fatalf("write ubs stub: %v", err)
	}
	t.Setenv("PATH", binDir+string(os.PathListSeparator)+"/usr/bin"+string(os.PathListSeparator)+"/bin")

	stdout, err := captureStdout(t, func() error {
		_, runErr := execCommand(t, "--robot-ubs-scan", "--scan-staged", "--scan-path", projectDir)
		return runErr
	})
	if err != nil {
		t.Fatalf("--robot-ubs-scan --scan-staged failed: %v\nstdout=%s", err, stdout)
	}

	var out robot.UBSScanOutput
	if jsonErr := json.Unmarshal([]byte(stdout), &out); jsonErr != nil {
		t.Fatalf("unmarshal robot ubs output: %v\nstdout=%s", jsonErr, stdout)
	}
	if !out.Success || out.Mode != "staged" || len(out.Findings) != 1 || out.Summary.TotalIssues != 1 {
		t.Fatalf("unexpected robot ubs output: %+v", out)
	}

	// Verify missing ubs returns exit code 2 through Execute().
	t.Setenv("PATH", t.TempDir())
	missingOut, missingErr := captureStdout(t, func() error {
		resetFlags()
		rootCmd.SetArgs([]string{"--robot-ubs-scan", "--scan-diff", "--scan-path", projectDir})
		return Execute()
	})
	var exitErr *robot.ProcessExitError
	if !errors.As(missingErr, &exitErr) || exitErr.ExitCode() != 2 {
		t.Fatalf("expected exit code 2 when ubs is missing, got %T %v (stdout=%s)", missingErr, missingErr, missingOut)
	}
}

func TestScanCLI_FiltersAndProjectThresholds(t *testing.T) {
	dir := t.TempDir()
	yamlCfg := "scanner:\n" +
		"  defaults:\n" +
		"    timeout: 45s\n" +
		"  tools:\n" +
		"    disabled:\n" +
		"      - rust\n" +
		"  thresholds:\n" +
		"    pre_commit:\n" +
		"      block_critical: true\n" +
		"      block_errors: 2\n" +
		"    ci:\n" +
		"      fail_critical: true\n" +
		"      fail_errors: 1\n"
	if err := os.WriteFile(filepath.Join(dir, ".ntm.yaml"), []byte(yamlCfg), 0o644); err != nil {
		t.Fatalf("write .ntm.yaml: %v", err)
	}

	origAvail := scanIsAvailable
	origNewRunner := newScanRunner
	t.Cleanup(func() {
		scanIsAvailable = origAvail
		newScanRunner = origNewRunner
	})

	rec := &recordingIncrementalScanner{
		result: &scanner.ScanResult{
			Project: dir,
			Totals:  scanner.ScanTotals{Files: 2, Critical: 0, Warning: 2, Info: 1},
			Scanners: []scanner.ScannerResult{
				{Language: "golang", Files: 1, Warning: 2},
				{Language: "rust", Files: 1, Info: 1},
			},
			Findings: []scanner.Finding{
				{File: "internal/auth/login.go", Line: 10, Severity: scanner.SeverityWarning, Message: "warn 1"},
				{File: "internal/auth/token.go", Line: 22, Severity: scanner.SeverityWarning, Message: "warn 2"},
				{File: "crates/core/lib.rs", Line: 5, Severity: scanner.SeverityInfo, Message: "info 1"},
			},
		},
	}
	scanIsAvailable = func() bool { return true }
	newScanRunner = func() (scanRunner, error) { return rec, nil }

	stdout, err := captureStdout(t, func() error {
		_, runErr := execCommand(t, "scan", dir, "--severity", "warning", "--filter-file", "auth/token.go", "--json")
		return runErr
	})
	if err != nil {
		t.Fatalf("scan with --severity and --filter-file failed: %v (stdout=%s)", err, stdout)
	}
	if rec.calledOpts.Timeout.Seconds() != 45 {
		t.Fatalf("timeout = %v, want 45s from .ntm.yaml scanner.defaults.timeout", rec.calledOpts.Timeout)
	}
	if len(rec.calledOpts.ExcludeLanguages) != 1 || rec.calledOpts.ExcludeLanguages[0] != "rust" {
		t.Fatalf("exclude languages = %v, want [rust] from .ntm.yaml scanner.tools.disabled", rec.calledOpts.ExcludeLanguages)
	}

	var payload struct {
		Success bool               `json:"success"`
		Scan    scanner.ScanResult `json:"scan"`
	}
	if err := json.Unmarshal([]byte(stdout), &payload); err != nil {
		t.Fatalf("unmarshal scan output: %v\nstdout=%s", err, stdout)
	}
	if !payload.Success || len(payload.Scan.Findings) != 1 || payload.Scan.Findings[0].File != "internal/auth/token.go" {
		t.Fatalf("filtered scan findings = %+v, want only internal/auth/token.go", payload.Scan.Findings)
	}
	if len(payload.Scan.Scanners) != 1 || payload.Scan.Scanners[0].Language != "golang" {
		t.Fatalf("filtered scanners = %+v, want only golang", payload.Scan.Scanners)
	}

	// Pre-commit threshold blocks when warnings >= block_errors (2 >= 2).
	_, stagedErr := captureStdout(t, func() error {
		_, runErr := execCommand(t, "scan", dir, "--staged")
		return runErr
	})
	if stagedErr == nil {
		t.Fatal("expected --staged scan to fail when warnings meet pre_commit.block_errors=2")
	}

	// CI threshold fails when warnings > fail_errors (2 > 1).
	_, ciErr := captureStdout(t, func() error {
		_, runErr := execCommand(t, "scan", dir, "--ci")
		return runErr
	})
	if ciErr == nil {
		t.Fatal("expected --ci scan to fail when warnings exceed ci.fail_errors=1")
	}
}

func TestBugsListCLI_SeverityAndFileFilters(t *testing.T) {
	dir := t.TempDir()
	cached := &scanner.ScanResult{
		Project: dir,
		Totals:  scanner.ScanTotals{Files: 2, Critical: 1, Warning: 1, Info: 1},
		Findings: []scanner.Finding{
			{File: "pkg/alpha/a.go", Line: 4, Severity: scanner.SeverityCritical, Message: "crit"},
			{File: "pkg/beta/b.go", Line: 8, Severity: scanner.SeverityWarning, Message: "warn"},
			{File: "pkg/beta/b.go", Line: 12, Severity: scanner.SeverityInfo, Message: "info"},
		},
	}
	if err := saveCachedScanResult(dir, cached); err != nil {
		t.Fatalf("saveCachedScanResult: %v", err)
	}

	stdout, err := captureStdout(t, func() error {
		_, runErr := execCommand(t, "bugs", "list", dir, "--severity", "warning", "--file", "beta/b.go", "--json")
		return runErr
	})
	if err != nil {
		t.Fatalf("bugs list --severity --file: %v", err)
	}
	var out struct {
		Findings []scanner.Finding `json:"findings"`
	}
	if err := json.Unmarshal([]byte(stdout), &out); err != nil {
		t.Fatalf("unmarshal bugs list JSON: %v\nstdout=%s", err, stdout)
	}
	if len(out.Findings) != 1 || out.Findings[0].File != "pkg/beta/b.go" || out.Findings[0].Severity != scanner.SeverityWarning {
		t.Fatalf("bugs list filtered findings = %+v, want single warning in pkg/beta/b.go", out.Findings)
	}
}

func runScan(path string, opts scanner.ScanOptions, createBeads, updateBeads, notifyAgents bool, bridgeCfg scanner.BridgeConfig, analyzeImpact, showHotspots, priorityReport bool) error {
	return runScanWithConfig(path, opts, nil, scanOutputFilters{}, createBeads, updateBeads, notifyAgents, bridgeCfg, analyzeImpact, showHotspots, priorityReport)
}
