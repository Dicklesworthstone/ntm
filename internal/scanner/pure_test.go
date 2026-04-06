package scanner

import (
	"sync"
	"testing"
	"time"

	"github.com/Dicklesworthstone/ntm/internal/config"
)

func TestParseBeadForDedup(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name     string
		title    string
		desc     string
		wantSig  string
		wantFile string
	}{
		{
			name:     "empty inputs",
			title:    "",
			desc:     "",
			wantSig:  "",
			wantFile: "",
		},
		{
			name:     "file line in description",
			title:    "some finding",
			desc:     "**File:** `internal/robot/robot.go:42`\nSome details",
			wantSig:  "internal/robot/robot.go:42:ubs-legacy",
			wantFile: "internal/robot/robot.go",
		},
		{
			name:     "severity and rule in title overrides signature",
			title:    "[CRITICAL] null_deref: possible null pointer",
			desc:     "**File:** `src/main.go:10`\nDetails",
			wantSig:  "src/main.go:10:null_deref",
			wantFile: "src/main.go",
		},
		{
			name:     "rule with spaces is not treated as rule id",
			title:    "[WARNING] some long message: explanation",
			desc:     "**File:** `pkg/util.go:5`\n",
			wantSig:  "pkg/util.go:5:ubs-legacy",
			wantFile: "pkg/util.go",
		},
		{
			name:     "no file prefix in description",
			title:    "[INFO] rule_x: msg",
			desc:     "Some description without file prefix",
			wantSig:  "",
			wantFile: "",
		},
		{
			name:     "file line with no backticks",
			title:    "test",
			desc:     "**File:** internal/foo.go:1",
			wantSig:  "",
			wantFile: "",
		},
		{
			name:     "file with three part path",
			title:    "[WARNING] unused_var: unused variable",
			desc:     "**File:** `internal/cli/cmd.go:100`\nMore info",
			wantSig:  "internal/cli/cmd.go:100:unused_var",
			wantFile: "internal/cli/cmd.go",
		},
		{
			name:     "title without severity bracket",
			title:    "plain title",
			desc:     "**File:** `foo.go:1`\n",
			wantSig:  "foo.go:1:ubs-legacy",
			wantFile: "foo.go",
		},
		{
			name:     "file line at end of description",
			title:    "test",
			desc:     "First line\nSecond line\n**File:** `a.go:99`\nLast",
			wantSig:  "a.go:99:ubs-legacy",
			wantFile: "a.go",
		},
		{
			name:     "backticks with no colon in file",
			title:    "test",
			desc:     "**File:** `justfile`\n",
			wantSig:  "",
			wantFile: "",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			gotSig, gotFile := parseBeadForDedup(tt.title, tt.desc)
			if gotSig != tt.wantSig {
				t.Errorf("signature = %q, want %q", gotSig, tt.wantSig)
			}
			if gotFile != tt.wantFile {
				t.Errorf("file = %q, want %q", gotFile, tt.wantFile)
			}
		})
	}
}

func TestDedupIndex_ExistsAndAdd(t *testing.T) {
	t.Parallel()
	idx := &DedupIndex{
		BySignature: make(map[string]string),
		ByFile:      make(map[string][]string),
	}

	f := Finding{File: "main.go", Line: 10, RuleID: "test_rule", Severity: SeverityWarning}

	if idx.Exists(f) {
		t.Error("expected finding to not exist initially")
	}

	idx.Add(f, "bd-123")

	if !idx.Exists(f) {
		t.Error("expected finding to exist after Add")
	}
	if idx.Total != 1 {
		t.Errorf("Total = %d, want 1", idx.Total)
	}
}

func TestDedupIndex_GetBeadID(t *testing.T) {
	t.Parallel()
	idx := &DedupIndex{
		BySignature: make(map[string]string),
		ByFile:      make(map[string][]string),
	}

	f := Finding{File: "util.go", Line: 5, RuleID: "leak", Severity: SeverityCritical}
	idx.Add(f, "bd-456")

	id, ok := idx.GetBeadID(f)
	if !ok {
		t.Error("expected GetBeadID to find the finding")
	}
	if id != "bd-456" {
		t.Errorf("GetBeadID = %q, want %q", id, "bd-456")
	}

	// Non-existent finding
	other := Finding{File: "other.go", Line: 1}
	_, ok = idx.GetBeadID(other)
	if ok {
		t.Error("expected GetBeadID to not find non-existent finding")
	}
}

func TestDedupIndex_FindingsForFile(t *testing.T) {
	t.Parallel()
	idx := &DedupIndex{
		BySignature: make(map[string]string),
		ByFile:      make(map[string][]string),
	}

	f1 := Finding{File: "main.go", Line: 10, RuleID: "r1", Severity: SeverityWarning}
	f2 := Finding{File: "main.go", Line: 20, RuleID: "r2", Severity: SeverityCritical}
	f3 := Finding{File: "other.go", Line: 1, RuleID: "r3", Severity: SeverityInfo}

	idx.Add(f1, "bd-1")
	idx.Add(f2, "bd-2")
	idx.Add(f3, "bd-3")

	sigs := idx.FindingsForFile("main.go")
	if len(sigs) != 2 {
		t.Fatalf("FindingsForFile(main.go) returned %d sigs, want 2", len(sigs))
	}

	sigs = idx.FindingsForFile("other.go")
	if len(sigs) != 1 {
		t.Fatalf("FindingsForFile(other.go) returned %d sigs, want 1", len(sigs))
	}

	sigs = idx.FindingsForFile("nonexistent.go")
	if len(sigs) != 0 {
		t.Fatalf("FindingsForFile(nonexistent.go) returned %d sigs, want 0", len(sigs))
	}
}

func TestDedupIndex_CheckFindings(t *testing.T) {
	t.Parallel()
	idx := &DedupIndex{
		BySignature: make(map[string]string),
		ByFile:      make(map[string][]string),
	}

	existing := Finding{File: "a.go", Line: 1, RuleID: "rule1", Severity: SeverityWarning}
	idx.Add(existing, "bd-existing")

	newFinding := Finding{File: "b.go", Line: 2, RuleID: "rule2", Severity: SeverityCritical}

	newFindings, dups := idx.CheckFindings([]Finding{existing, newFinding})
	if len(newFindings) != 1 {
		t.Fatalf("CheckFindings new count = %d, want 1", len(newFindings))
	}
	if len(dups) != 1 {
		t.Fatalf("CheckFindings dups count = %d, want 1", len(dups))
	}
	if dups[0].BeadID != "bd-existing" {
		t.Errorf("duplicate bead ID = %q, want %q", dups[0].BeadID, "bd-existing")
	}
}

func TestDedupIndex_CheckFindings_AllNew(t *testing.T) {
	t.Parallel()
	idx := &DedupIndex{
		BySignature: make(map[string]string),
		ByFile:      make(map[string][]string),
	}

	findings := []Finding{
		{File: "x.go", Line: 1, RuleID: "r1"},
		{File: "y.go", Line: 2, RuleID: "r2"},
	}
	newFindings, dups := idx.CheckFindings(findings)
	if len(newFindings) != 2 {
		t.Errorf("expected 2 new, got %d", len(newFindings))
	}
	if len(dups) != 0 {
		t.Errorf("expected 0 dups, got %d", len(dups))
	}
}

func TestDedupIndex_CheckFindings_Empty(t *testing.T) {
	t.Parallel()
	idx := &DedupIndex{
		BySignature: make(map[string]string),
		ByFile:      make(map[string][]string),
	}
	newFindings, dups := idx.CheckFindings(nil)
	if len(newFindings) != 0 || len(dups) != 0 {
		t.Errorf("expected empty results for nil input")
	}
}

func TestDedupIndex_Stats(t *testing.T) {
	t.Parallel()
	idx := &DedupIndex{
		BySignature: make(map[string]string),
		ByFile:      make(map[string][]string),
	}

	stats := idx.Stats()
	if stats.TotalBeads != 0 || stats.UniqueFiles != 0 {
		t.Errorf("empty index stats: total=%d files=%d, want 0/0", stats.TotalBeads, stats.UniqueFiles)
	}

	idx.Add(Finding{File: "a.go", Line: 1, RuleID: "r1"}, "bd-1")
	idx.Add(Finding{File: "a.go", Line: 2, RuleID: "r2"}, "bd-2")
	idx.Add(Finding{File: "b.go", Line: 1, RuleID: "r3"}, "bd-3")

	stats = idx.Stats()
	if stats.TotalBeads != 3 {
		t.Errorf("TotalBeads = %d, want 3", stats.TotalBeads)
	}
	if stats.UniqueFiles != 2 {
		t.Errorf("UniqueFiles = %d, want 2", stats.UniqueFiles)
	}
}

func TestDedupIndex_ConcurrentAccess(t *testing.T) {
	t.Parallel()
	idx := &DedupIndex{
		BySignature: make(map[string]string),
		ByFile:      make(map[string][]string),
	}

	var wg sync.WaitGroup
	for i := 0; i < 10; i++ {
		wg.Add(1)
		go func(n int) {
			defer wg.Done()
			f := Finding{File: "concurrent.go", Line: n, RuleID: "rule", Severity: SeverityWarning}
			idx.Add(f, "bd-concurrent")
			idx.Exists(f)
			idx.Stats()
		}(i)
	}
	wg.Wait()

	if idx.Total != 10 {
		t.Errorf("Total = %d after concurrent adds, want 10", idx.Total)
	}
}

func TestScanOptionsFromConfig(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name          string
		cfg           *config.ScannerConfig
		context       string
		wantFOW       bool
		wantLanguages []string
	}{
		{
			name: "pre_commit context with block",
			cfg: &config.ScannerConfig{
				Defaults: config.ScannerDefaults{
					Timeout:   "30s",
					Languages: []string{"go"},
				},
				Thresholds: config.ScannerThresholds{
					PreCommit: config.ThresholdConfig{
						ShowWarnings:  true,
						BlockErrors:   1,
						BlockCritical: true,
					},
				},
			},
			context:       "pre_commit",
			wantFOW:       true,
			wantLanguages: []string{"go"},
		},
		{
			name: "precommit alias",
			cfg: &config.ScannerConfig{
				Defaults: config.ScannerDefaults{},
				Thresholds: config.ScannerThresholds{
					PreCommit: config.ThresholdConfig{
						ShowWarnings: true,
						BlockErrors:  1,
					},
				},
			},
			context: "precommit",
			wantFOW: true,
		},
		{
			name: "ci context",
			cfg: &config.ScannerConfig{
				Defaults: config.ScannerDefaults{},
				Thresholds: config.ScannerThresholds{
					CI: config.ThresholdConfig{
						ShowWarnings:  false,
						BlockCritical: true,
					},
				},
			},
			context: "ci",
			wantFOW: false,
		},
		{
			name: "dashboard context",
			cfg: &config.ScannerConfig{
				Defaults: config.ScannerDefaults{},
				Thresholds: config.ScannerThresholds{
					Dashboard: config.ThresholdConfig{
						ShowWarnings: true,
						BlockErrors:  0,
					},
				},
			},
			context: "dashboard",
			wantFOW: false,
		},
		{
			name: "unknown context uses interactive",
			cfg: &config.ScannerConfig{
				Defaults: config.ScannerDefaults{},
				Thresholds: config.ScannerThresholds{
					Interactive: config.ThresholdConfig{
						ShowWarnings:  true,
						BlockCritical: true,
					},
				},
			},
			context: "unknown_context",
			wantFOW: true,
		},
		{
			name: "warnings shown but no blocking",
			cfg: &config.ScannerConfig{
				Defaults: config.ScannerDefaults{},
				Thresholds: config.ScannerThresholds{
					PreCommit: config.ThresholdConfig{
						ShowWarnings:  true,
						BlockErrors:   0,
						BlockCritical: false,
					},
				},
			},
			context: "pre_commit",
			wantFOW: false,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			opts := ScanOptionsFromConfig(tt.cfg, tt.context)
			if opts.FailOnWarning != tt.wantFOW {
				t.Errorf("FailOnWarning = %v, want %v", opts.FailOnWarning, tt.wantFOW)
			}
			if tt.wantLanguages != nil {
				if len(opts.Languages) != len(tt.wantLanguages) {
					t.Errorf("Languages count = %d, want %d", len(opts.Languages), len(tt.wantLanguages))
				}
			}
		})
	}
}

func TestScanOptionsFromConfig_Timeout(t *testing.T) {
	t.Parallel()
	cfg := &config.ScannerConfig{
		Defaults: config.ScannerDefaults{
			Timeout: "45s",
		},
	}
	opts := ScanOptionsFromConfig(cfg, "dashboard")
	if opts.Timeout != 45*time.Second {
		t.Errorf("Timeout = %v, want 45s", opts.Timeout)
	}
}

func TestBridgeConfigFromConfig(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name        string
		minSeverity string
		wantSev     Severity
	}{
		{"critical", "critical", SeverityCritical},
		{"warning", "warning", SeverityWarning},
		{"info", "info", SeverityInfo},
		{"empty defaults to warning", "", SeverityWarning},
		{"unknown defaults to warning", "bogus", SeverityWarning},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			cfg := &config.ScannerConfig{
				Beads: config.ScannerBeads{
					MinSeverity: tt.minSeverity,
				},
			}
			bc := BridgeConfigFromConfig(cfg)
			if bc.MinSeverity != tt.wantSev {
				t.Errorf("MinSeverity = %q, want %q", bc.MinSeverity, tt.wantSev)
			}
			if bc.DryRun {
				t.Error("expected DryRun to be false")
			}
			if bc.Verbose {
				t.Error("expected Verbose to be false")
			}
		})
	}
}

func TestShouldAutoCreateBeads(t *testing.T) {
	t.Parallel()
	cfg := &config.ScannerConfig{Beads: config.ScannerBeads{AutoCreate: true}}
	if !ShouldAutoCreateBeads(cfg) {
		t.Error("expected true when AutoCreate is true")
	}
	cfg.Beads.AutoCreate = false
	if ShouldAutoCreateBeads(cfg) {
		t.Error("expected false when AutoCreate is false")
	}
}

func TestShouldAutoCloseBeads(t *testing.T) {
	t.Parallel()
	cfg := &config.ScannerConfig{Beads: config.ScannerBeads{AutoClose: true}}
	if !ShouldAutoCloseBeads(cfg) {
		t.Error("expected true when AutoClose is true")
	}
	cfg.Beads.AutoClose = false
	if ShouldAutoCloseBeads(cfg) {
		t.Error("expected false when AutoClose is false")
	}
}

func TestEstimateFileCentrality(t *testing.T) {
	t.Parallel()
	// Currently a stub that returns 0.0
	result := estimateFileCentrality("any/file.go", map[string]float64{"key": 1.0})
	if result != 0.0 {
		t.Errorf("estimateFileCentrality = %f, want 0.0", result)
	}

	// Also works with nil map
	result = estimateFileCentrality("file.go", nil)
	if result != 0.0 {
		t.Errorf("estimateFileCentrality with nil map = %f, want 0.0", result)
	}
}
