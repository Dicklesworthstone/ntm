package tools

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"
)

// ClassifyProcess uses the same passive snapshot as the fleet monitor.
func (a *PTAdapter) ClassifyProcess(ctx context.Context, pid int) (*PTProcessResult, error) {
	results, err := a.ClassifyProcesses(ctx, []int{pid})
	if err != nil {
		return nil, err
	}
	return &results[0], nil
}

func TestNewPTAdapter(t *testing.T) {
	t.Parallel()
	adapter := NewPTAdapter()
	if adapter == nil {
		t.Fatal("NewPTAdapter returned nil")
	}

	if adapter.Name() != ToolPT {
		t.Errorf("Expected name %s, got %s", ToolPT, adapter.Name())
	}

	if adapter.BinaryName() != "pt" {
		t.Errorf("Expected binary name 'pt', got %s", adapter.BinaryName())
	}
}

func TestPTAdapterImplementsInterface(t *testing.T) {
	t.Parallel()
	// Ensure PTAdapter implements the Adapter interface
	var _ Adapter = (*PTAdapter)(nil)
}

func TestPTAdapterDetect(t *testing.T) {
	t.Parallel()
	adapter := NewPTAdapter()

	// Test detection - result depends on whether pt is installed
	path, installed := adapter.Detect()

	// If installed, path should be non-empty
	if installed && path == "" {
		t.Error("pt detected but path is empty")
	}

	// If not installed, path should be empty
	if !installed && path != "" {
		t.Errorf("pt not detected but path is %s", path)
	}
}

func TestPTStatusStruct(t *testing.T) {
	t.Parallel()
	status := PTStatus{
		Available:   true,
		Compatible:  true,
		Version:     Version{Major: 1, Minor: 0, Patch: 0},
		Path:        "/usr/local/bin/pt",
		LastChecked: time.Now(),
	}

	if !status.Available {
		t.Error("Expected Available to be true")
	}

	if !status.Compatible {
		t.Error("Expected Compatible to be true")
	}

	if status.Path != "/usr/local/bin/pt" {
		t.Errorf("Expected Path '/usr/local/bin/pt', got %s", status.Path)
	}
}

func TestPTClassificationConstants(t *testing.T) {
	t.Parallel()
	tests := []struct {
		class    PTClassification
		expected string
	}{
		{PTClassUseful, "useful"},
		{PTClassAbandoned, "abandoned"},
		{PTClassZombie, "zombie"},
		{PTClassUnknown, "unknown"},
	}

	for _, tt := range tests {
		if string(tt.class) != tt.expected {
			t.Errorf("Expected classification %s, got %s", tt.expected, tt.class)
		}
	}
}

func TestPTProcessResultStruct(t *testing.T) {
	t.Parallel()
	result := PTProcessResult{
		PID:            12345,
		Name:           "claude-agent",
		Classification: PTClassUseful,
		Confidence:     0.95,
		Reason:         "Active process with recent I/O",
	}

	if result.PID != 12345 {
		t.Errorf("Expected PID 12345, got %d", result.PID)
	}

	if result.Name != "claude-agent" {
		t.Errorf("Expected Name 'claude-agent', got %s", result.Name)
	}

	if result.Classification != PTClassUseful {
		t.Errorf("Expected Classification 'useful', got %s", result.Classification)
	}

	if result.Confidence != 0.95 {
		t.Errorf("Expected Confidence 0.95, got %f", result.Confidence)
	}

	if result.Reason != "Active process with recent I/O" {
		t.Errorf("Expected Reason 'Active process with recent I/O', got %s", result.Reason)
	}
}

func TestPTAdapterCacheInvalidation(t *testing.T) {
	adapter := NewPTAdapter()

	// Invalidate cache should not panic
	adapter.InvalidateStatusCache()

	// Call again to ensure it's safe to call multiple times
	adapter.InvalidateStatusCache()
}

func TestPTAdapterHealthWhenNotInstalled(t *testing.T) {
	adapter := NewPTAdapter()

	// If pt is not installed, Health should return non-healthy status
	_, installed := adapter.Detect()
	if installed {
		t.Skip("pt is installed, skipping not-installed test")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	health, err := adapter.Health(ctx)
	if err != nil {
		t.Fatalf("Health returned unexpected error: %v", err)
	}

	if health.Healthy {
		t.Error("Expected unhealthy status when pt not installed")
	}

	if health.Message != "pt not installed" {
		t.Errorf("Expected message 'pt not installed', got %s", health.Message)
	}
}

func TestPTAdapterIsAvailable(t *testing.T) {
	adapter := NewPTAdapter()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	// IsAvailable should not panic regardless of whether pt is installed
	available := adapter.IsAvailable(ctx)

	// If pt is not installed, should return false
	_, installed := adapter.Detect()
	if !installed && available {
		t.Error("IsAvailable returned true but pt is not installed")
	}
}

func TestPTAdapterGetStatus(t *testing.T) {
	adapter := NewPTAdapter()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	// GetStatus should not panic and should return valid struct
	status, err := adapter.GetStatus(ctx)
	if err != nil {
		t.Fatalf("GetStatus returned unexpected error: %v", err)
	}

	if status == nil {
		t.Fatal("GetStatus returned nil")
	}

	// LastChecked should be set
	if status.LastChecked.IsZero() {
		t.Error("LastChecked should be set")
	}

	// If not installed, Available should be false
	_, installed := adapter.Detect()
	if !installed && status.Available {
		t.Error("Available should be false when pt not installed")
	}
}

func TestPTAdapterGetStatusCaching(t *testing.T) {
	adapter := NewPTAdapter()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	// First call
	status1, err := adapter.GetStatus(ctx)
	if err != nil {
		t.Fatalf("First GetStatus returned error: %v", err)
	}

	// Second call should return cached result
	status2, err := adapter.GetStatus(ctx)
	if err != nil {
		t.Fatalf("Second GetStatus returned error: %v", err)
	}

	// LastChecked should be the same (cached)
	if !status1.LastChecked.Equal(status2.LastChecked) {
		t.Error("Expected cached result with same LastChecked time")
	}
}

func TestToolPTInAllTools(t *testing.T) {
	t.Parallel()
	tools := AllTools()
	found := false
	for _, tool := range tools {
		if tool == ToolPT {
			found = true
			break
		}
	}

	if !found {
		t.Error("ToolPT not found in AllTools()")
	}
}

func TestPTMinVersionCompatibility(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name     string
		version  Version
		expected bool
	}{
		{
			name:     "exact min version",
			version:  Version{Major: 0, Minor: 1, Patch: 0},
			expected: true,
		},
		{
			name:     "above min version",
			version:  Version{Major: 1, Minor: 0, Patch: 0},
			expected: true,
		},
		{
			name:     "minor above",
			version:  Version{Major: 0, Minor: 2, Patch: 0},
			expected: true,
		},
		{
			name:     "below min version",
			version:  Version{Major: 0, Minor: 0, Patch: 9},
			expected: false,
		},
		{
			name:     "zero version",
			version:  Version{Major: 0, Minor: 0, Patch: 0},
			expected: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := ptCompatible(tt.version)
			if result != tt.expected {
				t.Errorf("ptCompatible(%v) = %v, want %v", tt.version, result, tt.expected)
			}
		})
	}
}

func TestPTStatusTTL(t *testing.T) {
	t.Parallel()
	// Verify that the TTL is set to 5 minutes
	if ptStatusTTL != 5*time.Minute {
		t.Errorf("Expected pt status TTL of 5m, got %v", ptStatusTTL)
	}
}

func TestPTAdapterInfo(t *testing.T) {
	t.Parallel()
	adapter := NewPTAdapter()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	info, err := adapter.Info(ctx)
	if err != nil {
		t.Fatalf("Info returned unexpected error: %v", err)
	}

	if info == nil {
		t.Fatal("Info returned nil")
	}

	if info.Name != ToolPT {
		t.Errorf("Expected name %s, got %s", ToolPT, info.Name)
	}

	// If not installed, Installed should be false
	_, installed := adapter.Detect()
	if !installed && info.Installed {
		t.Error("Installed should be false when pt not installed")
	}
}

func TestPTAdapterCapabilities(t *testing.T) {
	t.Parallel()
	adapter := NewPTAdapter()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	caps, err := adapter.Capabilities(ctx)
	if err != nil {
		t.Fatalf("Capabilities returned unexpected error: %v", err)
	}

	// If not installed, should return empty capabilities
	_, installed := adapter.Detect()
	if !installed && len(caps) > 0 {
		t.Error("Expected empty capabilities when pt not installed")
	}
}

func TestPTClassifyProcessWhenNotInstalled(t *testing.T) {
	adapter := NewPTAdapter()

	_, installed := adapter.Detect()
	if installed {
		t.Skip("pt is installed, skipping not-installed test")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	// ClassifyProcess should return error when pt is not installed
	_, err := adapter.ClassifyProcess(ctx, 1)
	if err == nil {
		t.Error("Expected error when pt is not installed")
	}
}

func TestPTClassifyProcessesWhenNotInstalled(t *testing.T) {
	adapter := NewPTAdapter()

	_, installed := adapter.Detect()
	if installed {
		t.Skip("pt is installed, skipping not-installed test")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	// ClassifyProcesses should return error when pt is not installed
	_, err := adapter.ClassifyProcesses(ctx, []int{1, 2, 3})
	if err == nil {
		t.Error("Expected error when pt is not installed")
	}
}

func TestPTToolNameConstant(t *testing.T) {
	t.Parallel()
	if ToolPT != "pt" {
		t.Errorf("Expected ToolPT to be 'pt', got %s", ToolPT)
	}
}

// TestPTClassifyProcessesReadsInstalledPT runs the installed pt's passive watch
// twice: once directly, whose output ntm's parser must accept with each
// candidate's recommendation intact, and once through ClassifyProcesses, which
// must answer for exactly the PIDs it was asked about.
func TestPTClassifyProcessesReadsInstalledPT(t *testing.T) {
	t.Parallel()
	if _, err := exec.LookPath("pt"); err != nil {
		t.Skip("pt not installed")
	}
	started := time.Now()
	out, err := exec.Command("pt", "agent", "watch", "--once", "--threshold", "low", "--format", "jsonl").Output()
	if err != nil {
		t.Fatalf("pt agent watch: %v", err)
	}
	direct := map[int]string{}
	for _, line := range strings.Split(string(out), "\n") {
		var event struct {
			Event          string `json:"event"`
			PID            int    `json:"pid"`
			Classification string `json:"classification"`
		}
		if json.Unmarshal([]byte(line), &event) == nil && event.Event == "candidate_detected" {
			direct[event.PID] = event.Classification
		}
	}
	wanted := map[int]struct{}{os.Getpid(): {}}
	for pid := range direct {
		wanted[pid] = struct{}{}
	}

	parsed, err := parsePTWatch(out, wanted, started, time.Now())
	if err != nil {
		t.Fatalf("ntm rejected pt's own watch output: %v", err)
	}
	for _, r := range parsed {
		if want, ok := direct[r.PID]; ok && r.Recommendation != want {
			t.Errorf("pid %d: recommendation %q, pt said %q", r.PID, r.Recommendation, want)
		}
	}

	pids := make([]int, 0, len(wanted))
	for pid := range wanted {
		pids = append(pids, pid)
	}
	results, err := NewPTAdapter().ClassifyProcesses(context.Background(), pids)
	if err != nil {
		t.Fatalf("ClassifyProcesses: %v", err)
	}
	if len(results) != len(wanted) {
		t.Fatalf("got %d results for %d PIDs", len(results), len(wanted))
	}
	for _, r := range results {
		if _, ok := wanted[r.PID]; !ok || r.Source != "pt_agent_watch" {
			t.Errorf("unexpected result %+v", r)
		}
	}
	t.Logf("pt reported %d candidates; classified %d PIDs", len(direct), len(results))
}

func TestParsePTWatch_AcceptsHostTelemetryEventsAndRejectsUnknown(t *testing.T) {
	t.Parallel()
	now := time.Now().UTC()
	ts := now.Format(time.RFC3339Nano)
	payload := strings.Join([]string{
		`{"event":"agent_activity","timestamp":"` + ts + `","status":"observed"}`,
		`{"event":"memory_limit_diagnostics","timestamp":"` + ts + `"}`,
		`{"event":"pressure_regime_observed","timestamp":"` + ts + `"}`,
		`{"event":"candidate_detected","timestamp":"` + ts + `","pid":4242,"classification":"kill","confidence":0.95}`,
	}, "\n") + "\n"

	results, err := parsePTWatch([]byte(payload), map[int]struct{}{4242: {}}, now.Add(-time.Second), now.Add(time.Second))
	if err != nil {
		t.Fatalf("parsePTWatch rejected valid host telemetry events: %v", err)
	}
	if len(results) != 1 || results[0].PID != 4242 || results[0].Classification != PTClassAbandoned {
		t.Fatalf("parsePTWatch results = %+v, want PID 4242 abandoned", results)
	}

	unknownPayload := `{"event":"totally_unknown_event","timestamp":"` + ts + `"}` + "\n"
	if _, err := parsePTWatch([]byte(unknownPayload), map[int]struct{}{4242: {}}, now.Add(-time.Second), now.Add(time.Second)); !errors.Is(err, ErrSchemaValidation) {
		t.Fatalf("parsePTWatch(unknown event) err = %v, want ErrSchemaValidation", err)
	}
}
