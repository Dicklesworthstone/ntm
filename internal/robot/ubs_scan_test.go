package robot

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

func installStubUBSForRobotTest(t *testing.T, reportJSON string) string {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("stub ubs uses POSIX sh")
	}

	dir := t.TempDir()
	argsFile := filepath.Join(dir, "ubs_args.txt")
	outFile := filepath.Join(dir, "ubs_out.json")
	if err := os.WriteFile(outFile, []byte(reportJSON), 0o600); err != nil {
		t.Fatalf("write stub ubs output: %v", err)
	}

	script := "#!/bin/sh\n" +
		"set -eu\n" +
		"if [ \"${1:-}\" = \"--version\" ]; then\n" +
		"  echo \"ubs 1.9.0-stub\"\n" +
		"  exit 0\n" +
		"fi\n" +
		"printf '%s\\n' \"$*\" > \"" + argsFile + "\"\n" +
		"cat \"" + outFile + "\"\n"
	ubsPath := filepath.Join(dir, "ubs")
	if err := os.WriteFile(ubsPath, []byte(script), 0o755); err != nil {
		t.Fatalf("write stub ubs script: %v", err)
	}

	t.Setenv("PATH", dir+string(os.PathListSeparator)+"/usr/bin"+string(os.PathListSeparator)+"/bin")
	return argsFile
}

func TestGetUBSScan_IncrementalAndDirectoryModes(t *testing.T) {
	projectDir := t.TempDir()
	sampleFile := filepath.Join(projectDir, "sample.go")
	if err := os.WriteFile(sampleFile, []byte("package sample\n"), 0o644); err != nil {
		t.Fatalf("write sample.go: %v", err)
	}

	reportWithFindings := `{
  "project": "` + projectDir + `",
  "status": "ok",
  "totals": {"files": 2, "critical": 1, "warning": 1, "info": 0},
  "findings": [
    {"rule_id": "go.sec.sql", "severity": "critical", "file": "sample.go", "line": 12, "col": 4, "message": "SQL injection risk", "category_id": "security"},
    {"rule_id": "go.perf.alloc", "severity": "warning", "file": "sample.go", "line": 30, "col": 2, "message": "Unbounded slice growth", "category_id": "performance"}
  ]
}`
	argsFile := installStubUBSForRobotTest(t, reportWithFindings)

	t.Run("staged mode invokes ScanStaged and populates findings + triage_preview", func(t *testing.T) {
		out, err := GetUBSScan(context.Background(), UBSScanOptions{
			Path:   projectDir,
			Staged: true,
		})
		if err != nil {
			t.Fatalf("GetUBSScan(staged): %v", err)
		}
		if !out.Success {
			t.Fatalf("expected success=true, got error=%q code=%q", out.Error, out.ErrorCode)
		}
		if _, tsErr := time.Parse(time.RFC3339, out.Timestamp); tsErr != nil {
			t.Fatalf("invalid RFC3339 timestamp %q: %v", out.Timestamp, tsErr)
		}
		if out.Mode != "staged" {
			t.Fatalf("mode = %q, want staged", out.Mode)
		}
		if out.Summary.Critical != 1 || out.Summary.Warning != 1 || out.Summary.TotalIssues != 2 || out.Summary.Healthy {
			t.Fatalf("unexpected summary: %+v", out.Summary)
		}
		if len(out.Findings) != 2 {
			t.Fatalf("findings len = %d, want 2", len(out.Findings))
		}
		if len(out.TriagePreview) != 2 {
			t.Fatalf("triage_preview len = %d, want 2", len(out.TriagePreview))
		}

		recordedArgs, readErr := os.ReadFile(argsFile)
		if readErr != nil {
			t.Fatalf("read recorded ubs args: %v", readErr)
		}
		gotArgs := strings.TrimSpace(string(recordedArgs))
		if !strings.Contains(gotArgs, "--staged") || !strings.HasSuffix(gotArgs, projectDir) {
			t.Fatalf("ubs args = %q, want --staged and target %q", gotArgs, projectDir)
		}
	})

	t.Run("diff mode invokes ScanDiff", func(t *testing.T) {
		out, err := GetUBSScan(context.Background(), UBSScanOptions{
			Path: projectDir,
			Diff: true,
		})
		if err != nil {
			t.Fatalf("GetUBSScan(diff): %v", err)
		}
		if !out.Success || out.Mode != "diff" {
			t.Fatalf("unexpected diff output: %+v", out)
		}
		recordedArgs, readErr := os.ReadFile(argsFile)
		if readErr != nil {
			t.Fatalf("read recorded ubs args: %v", readErr)
		}
		gotArgs := strings.TrimSpace(string(recordedArgs))
		if !strings.Contains(gotArgs, "--diff") || strings.Contains(gotArgs, "--staged") {
			t.Fatalf("ubs args = %q, want --diff without --staged", gotArgs)
		}
	})

	t.Run("file mode invokes ScanFile", func(t *testing.T) {
		out, err := GetUBSScan(context.Background(), UBSScanOptions{
			File: sampleFile,
		})
		if err != nil {
			t.Fatalf("GetUBSScan(file): %v", err)
		}
		if !out.Success || out.Mode != "file" || out.Target != sampleFile {
			t.Fatalf("unexpected file output: %+v", out)
		}
		recordedArgs, readErr := os.ReadFile(argsFile)
		if readErr != nil {
			t.Fatalf("read recorded ubs args: %v", readErr)
		}
		gotArgs := strings.TrimSpace(string(recordedArgs))
		if !strings.HasSuffix(gotArgs, sampleFile) || strings.Contains(gotArgs, "--staged") || strings.Contains(gotArgs, "--diff") {
			t.Fatalf("ubs args = %q, want single-file target %q", gotArgs, sampleFile)
		}
	})

	t.Run("empty directory scan serializes non-null empty arrays", func(t *testing.T) {
		cleanReport := `{"project":"` + projectDir + `","status":"ok","totals":{"files":3,"critical":0,"warning":0,"info":0},"findings":[]}`
		installStubUBSForRobotTest(t, cleanReport)

		out, err := GetUBSScan(context.Background(), UBSScanOptions{
			Path: projectDir,
		})
		if err != nil {
			t.Fatalf("GetUBSScan(directory): %v", err)
		}
		if !out.Success || out.Mode != "directory" || !out.Summary.Healthy || out.Summary.TotalIssues != 0 {
			t.Fatalf("unexpected clean directory output: %+v", out)
		}
		raw, err := json.Marshal(out)
		if err != nil {
			t.Fatalf("marshal output: %v", err)
		}
		encoded := string(raw)
		if strings.Contains(encoded, `"findings":null`) || !strings.Contains(encoded, `"findings":[]`) {
			t.Fatalf("findings must serialize as [], got %s", encoded)
		}
		if strings.Contains(encoded, `"triage_preview":null`) || !strings.Contains(encoded, `"triage_preview":[]`) {
			t.Fatalf("triage_preview must serialize as [], got %s", encoded)
		}
	})
}

func TestGetUBSScan_InvalidFlagCombinations(t *testing.T) {
	dir := t.TempDir()
	file := filepath.Join(dir, "main.go")
	if err := os.WriteFile(file, []byte("package main\n"), 0o644); err != nil {
		t.Fatalf("write main.go: %v", err)
	}

	cases := []struct {
		name string
		opts UBSScanOptions
	}{
		{
			name: "staged and diff mutually exclusive",
			opts: UBSScanOptions{Path: dir, Staged: true, Diff: true},
		},
		{
			name: "file combined with staged",
			opts: UBSScanOptions{File: file, Staged: true},
		},
		{
			name: "file combined with path",
			opts: UBSScanOptions{File: file, Path: dir},
		},
		{
			name: "missing file target",
			opts: UBSScanOptions{File: filepath.Join(dir, "missing.go")},
		},
		{
			name: "directory passed to --scan-file",
			opts: UBSScanOptions{File: dir},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			out, err := GetUBSScan(context.Background(), tc.opts)
			if err != nil {
				t.Fatalf("GetUBSScan returned unexpected error: %v", err)
			}
			if out.Success || out.ErrorCode != ErrCodeInvalidFlag {
				t.Fatalf("got %+v, want INVALID_FLAG failure", out.RobotResponse)
			}
			if out.Meta == nil || out.Meta.ExitCode != 1 {
				t.Fatalf("meta = %+v, want exit code 1", out.Meta)
			}
			raw, err := json.Marshal(out)
			if err != nil {
				t.Fatalf("marshal error output: %v", err)
			}
			if !strings.Contains(string(raw), `"findings":[]`) || !strings.Contains(string(raw), `"triage_preview":[]`) {
				t.Fatalf("error envelope must preserve non-null arrays: %s", raw)
			}
		})
	}
}

func TestPrintUBSScan_MissingBinaryExitsCode2(t *testing.T) {
	t.Setenv("PATH", t.TempDir())
	projectDir := t.TempDir()

	stdout, err := captureStdout(t, func() error {
		return PrintUBSScan(context.Background(), UBSScanOptions{Path: projectDir, Staged: true})
	})

	var exitErr *ProcessExitError
	if !errors.As(err, &exitErr) || exitErr.ExitCode() != 2 || !exitErr.JSONWritten() {
		t.Fatalf("PrintUBSScan missing binary error = %T %v, want written exit-2 ProcessExitError", err, err)
	}

	var parsed map[string]interface{}
	if unmarshalErr := json.Unmarshal([]byte(stdout), &parsed); unmarshalErr != nil {
		t.Fatalf("unmarshal stdout JSON: %v\nstdout=%s", unmarshalErr, stdout)
	}
	if parsed["success"] != false || parsed["error_code"] != ErrCodeNotImplemented {
		t.Fatalf("unexpected missing-binary payload: %v", parsed)
	}
	if !strings.Contains(stdout, `"findings":[]`) && !strings.Contains(stdout, `"findings": []`) {
		t.Fatalf("expected non-null findings array in stdout: %s", stdout)
	}
}

func TestGetUBSScan_SeverityAndFileFilters(t *testing.T) {
	projectDir := t.TempDir()
	report := `{
  "project": "` + projectDir + `",
  "status": "ok",
  "totals": {"files": 2, "critical": 1, "warning": 1, "info": 1},
  "findings": [
    {"rule_id": "go.sec.sql", "severity": "critical", "file": "internal/auth/sql.go", "line": 12, "col": 4, "message": "SQL injection risk", "category_id": "security"},
    {"rule_id": "go.perf.alloc", "severity": "warning", "file": "internal/auth/sql.go", "line": 30, "col": 2, "message": "Slice growth", "category_id": "performance"},
    {"rule_id": "go.style.doc", "severity": "info", "file": "internal/ui/view.go", "line": 5, "col": 1, "message": "Missing comment", "category_id": "style"}
  ]
}`
	installStubUBSForRobotTest(t, report)

	out, err := GetUBSScan(context.Background(), UBSScanOptions{
		Path:       projectDir,
		Severity:   "warning",
		FilterFile: "auth/sql.go",
	})
	if err != nil {
		t.Fatalf("GetUBSScan filtered: %v", err)
	}
	if !out.Success || len(out.Findings) != 1 || out.Findings[0].RuleID != "go.perf.alloc" {
		t.Fatalf("filtered findings = %+v, want [go.perf.alloc]", out.Findings)
	}
	if out.Summary.Critical != 0 || out.Summary.Warning != 1 || out.Summary.Info != 0 || out.Summary.Files != 1 {
		t.Fatalf("filtered summary = %+v, want warning=1 files=1", out.Summary)
	}

	bad, err := GetUBSScan(context.Background(), UBSScanOptions{
		Path:     projectDir,
		Severity: "fatal",
	})
	if err != nil {
		t.Fatalf("GetUBSScan invalid severity: %v", err)
	}
	if bad.Success || bad.ErrorCode != ErrCodeInvalidFlag {
		t.Fatalf("expected INVALID_FLAG for bad severity, got %+v", bad.RobotResponse)
	}
}
