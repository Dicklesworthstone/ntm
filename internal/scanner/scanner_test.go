package scanner

import (
	"context"
	"errors"
	"testing"
	"time"
)

func TestIsAvailable(t *testing.T) {
	// This test checks if UBS is installed on the system
	available := IsAvailable()
	t.Logf("UBS available: %v", available)
	// We don't fail if UBS is not installed - it's optional
}

func TestNew(t *testing.T) {
	scanner, err := New()
	if err != nil {
		if err == ErrNotInstalled {
			t.Skip("UBS not installed, skipping")
		}
		t.Fatalf("unexpected error: %v", err)
	}
	if scanner == nil {
		t.Fatal("scanner is nil")
	}
	if scanner.binaryPath == "" {
		t.Fatal("binaryPath is empty")
	}
}

func TestVersion(t *testing.T) {
	scanner, err := New()
	if err != nil {
		t.Skip("UBS not installed")
	}

	version, err := scanner.Version()
	if err != nil {
		t.Fatalf("getting version: %v", err)
	}
	if version == "" {
		t.Fatal("version is empty")
	}
	t.Logf("UBS version: %s", version)
}

func TestScanFile(t *testing.T) {
	scanner, err := New()
	if err != nil {
		t.Skip("UBS not installed")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	// Scan a real file in the project
	result, err := scanner.ScanFile(ctx, "types.go")
	if err != nil {
		// Skip on timeout since UBS may be slow in CI (bd-1ihar)
		if err == ErrTimeout {
			t.Skipf("UBS scan timed out after 30s: %v", err)
		}
		t.Fatalf("scanning file: %v", err)
	}
	if result == nil {
		t.Fatal("result is nil")
	}

	t.Logf("Scan result: files=%d, critical=%d, warning=%d, info=%d",
		result.Totals.Files, result.Totals.Critical, result.Totals.Warning, result.Totals.Info)
}

func TestScanDirectory(t *testing.T) {
	scanner, err := New()
	if err != nil {
		t.Skip("UBS not installed")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	// Scan the scanner package itself
	result, err := scanner.ScanDirectory(ctx, ".")
	if err != nil {
		t.Fatalf("scanning directory: %v", err)
	}
	if result == nil {
		t.Fatal("result is nil")
	}

	t.Logf("Directory scan: files=%d, critical=%d, warning=%d, info=%d",
		result.Totals.Files, result.Totals.Critical, result.Totals.Warning, result.Totals.Info)
}

func TestQuickScan(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	result, err := QuickScan(ctx, "types.go")
	if err != nil {
		// Skip on timeout since UBS may be slow in CI (bd-1ihar)
		if err == ErrTimeout {
			t.Skipf("UBS quick scan timed out after 30s: %v", err)
		}
		t.Fatalf("quick scan: %v", err)
	}
	// result can be nil if UBS is not installed (graceful degradation)
	if result != nil {
		t.Logf("Quick scan: files=%d, critical=%d, warning=%d",
			result.Totals.Files, result.Totals.Critical, result.Totals.Warning)
	} else {
		t.Log("Quick scan returned nil (UBS not installed)")
	}
}

func TestScanOptions(t *testing.T) {
	scanner, err := New()
	if err != nil {
		t.Skip("UBS not installed")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	opts := ScanOptions{
		Languages:     []string{"golang"},
		CI:            true,
		FailOnWarning: false,
		Timeout:       30 * time.Second,
	}

	result, err := scanner.Scan(ctx, ".", opts)
	if err != nil {
		// Skip on timeout since UBS may be slow in CI (bd-1ihar)
		if err == ErrTimeout {
			t.Skipf("UBS scan with options timed out after 30s: %v", err)
		}
		t.Fatalf("scan with options: %v", err)
	}
	if result == nil {
		t.Fatal("result is nil")
	}

	t.Logf("Scan with options: files=%d, duration=%v",
		result.Totals.Files, result.Duration)
}

func TestScanResultMethods(t *testing.T) {
	result := &ScanResult{
		Totals: ScanTotals{
			Critical: 2,
			Warning:  5,
			Info:     10,
			Files:    3,
		},
		Findings: []Finding{
			{File: "a.go", Severity: SeverityCritical, Message: "critical 1"},
			{File: "a.go", Severity: SeverityCritical, Message: "critical 2"},
			{File: "b.go", Severity: SeverityWarning, Message: "warning 1"},
			{File: "b.go", Severity: SeverityInfo, Message: "info 1"},
		},
	}

	if result.IsHealthy() {
		t.Error("expected IsHealthy() to be false")
	}
	if !result.HasCritical() {
		t.Error("expected HasCritical() to be true")
	}
	if !result.HasWarning() {
		t.Error("expected HasWarning() to be true")
	}
	if result.TotalIssues() != 17 {
		t.Errorf("expected TotalIssues() = 17, got %d", result.TotalIssues())
	}

	criticals := result.FilterBySeverity(SeverityCritical)
	if len(criticals) != 2 {
		t.Errorf("expected 2 critical findings, got %d", len(criticals))
	}

	fileAFindings := result.FilterByFile("a.go")
	if len(fileAFindings) != 2 {
		t.Errorf("expected 2 findings for a.go, got %d", len(fileAFindings))
	}
}

func TestDefaultOptions(t *testing.T) {
	opts := DefaultOptions()
	if opts.Timeout != 60*time.Second {
		t.Errorf("expected timeout 60s, got %v", opts.Timeout)
	}
}

func TestBuildArgs(t *testing.T) {
	scanner := &Scanner{binaryPath: "ubs"}

	tests := []struct {
		name     string
		path     string
		opts     ScanOptions
		expected []string
	}{
		{
			name:     "default",
			path:     ".",
			opts:     ScanOptions{},
			expected: []string{"--format=json", "."},
		},
		{
			name: "with languages",
			path: "src/",
			opts: ScanOptions{
				Languages: []string{"golang", "rust"},
			},
			expected: []string{"--format=json", "--only=golang,rust", "src/"},
		},
		{
			name: "CI mode",
			path: ".",
			opts: ScanOptions{
				CI:            true,
				FailOnWarning: true,
			},
			expected: []string{"--format=json", "--ci", "--fail-on-warning", "."},
		},
		{
			name: "staged only",
			path: ".",
			opts: ScanOptions{
				StagedOnly: true,
			},
			expected: []string{"--format=json", "--staged", "."},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			args := scanner.buildArgs(tt.path, tt.opts)
			if len(args) != len(tt.expected) {
				t.Errorf("expected %d args, got %d: %v", len(tt.expected), len(args), args)
				return
			}
			for i, arg := range args {
				if arg != tt.expected[i] {
					t.Errorf("arg[%d]: expected %q, got %q", i, tt.expected[i], arg)
				}
			}
		})
	}
}

func TestParseOutput_WithWarningsPrefix(t *testing.T) {
	scanner := &Scanner{binaryPath: "ubs"}
	jsonPayload := `{"project":"test","timestamp":"2026-01-01T00:00:00Z","scanners":[],"totals":{"critical":0,"warning":0,"info":0,"files":0},"findings":[],"exit_code":0}`
	output := []byte("ℹ Created filtered scan workspace at /tmp\n" + jsonPayload + "\n")

	result, warnings, err := scanner.parseOutput(output)
	if err != nil {
		t.Fatalf("parseOutput error: %v", err)
	}
	if result == nil {
		t.Fatal("expected result, got nil")
	}
	if result.Project != "test" {
		t.Fatalf("expected project=test, got %q", result.Project)
	}
	if len(warnings) != 1 {
		t.Fatalf("expected 1 warning, got %d", len(warnings))
	}
	if warnings[0] != "ℹ Created filtered scan workspace at /tmp" {
		t.Fatalf("unexpected warning: %q", warnings[0])
	}
}

func TestParseOutput_WarningsOnly(t *testing.T) {
	scanner := &Scanner{binaryPath: "ubs"}
	output := []byte("✓ No changed files to scan.\n")

	result, warnings, err := scanner.parseOutput(output)
	if err == nil || !errors.Is(err, ErrOutputNotJSON) {
		t.Fatalf("expected ErrOutputNotJSON, got %v", err)
	}
	if result != nil {
		t.Fatalf("expected nil result when no JSON, got %+v", result)
	}
	if len(warnings) != 1 {
		t.Fatalf("expected 1 warning, got %d", len(warnings))
	}
	if warnings[0] != "✓ No changed files to scan." {
		t.Fatalf("unexpected warning: %q", warnings[0])
	}
}
