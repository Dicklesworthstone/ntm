//go:build e2e
// +build e2e

// Package e2e contains end-to-end tests for NTM robot mode commands.
// [E2E-DOCTOR] Tests for ntm doctor (system diagnostics and health checks).
package e2e

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"
)

// DoctorReport mirrors the CLI output structure for JSON parsing
type DoctorReport struct {
	Timestamp     time.Time        `json:"timestamp"`
	Overall       string           `json:"overall"` // "healthy", "warning", "unhealthy"
	Tools         []ToolCheck      `json:"tools"`
	Dependencies  []DepCheck       `json:"dependencies"`
	Daemons       []DaemonCheck    `json:"daemons"`
	Configuration []ConfigCheck    `json:"configuration"`
	Invariants    []InvariantCheck `json:"invariants"`
	Warnings      int              `json:"warnings"`
	Errors        int              `json:"errors"`
}

// ToolCheck represents a tool health check result
type ToolCheck struct {
	Name         string   `json:"name"`
	Installed    bool     `json:"installed"`
	Version      string   `json:"version,omitempty"`
	Path         string   `json:"path,omitempty"`
	Capabilities []string `json:"capabilities,omitempty"`
	Required     bool     `json:"required"`
	Status       string   `json:"status"`
	Message      string   `json:"message,omitempty"`
}

// DepCheck represents a dependency check result
type DepCheck struct {
	Name       string `json:"name"`
	Installed  bool   `json:"installed"`
	Version    string `json:"version,omitempty"`
	MinVersion string `json:"min_version,omitempty"`
	Status     string `json:"status"`
	Message    string `json:"message,omitempty"`
}

// DaemonCheck represents a daemon health check result
type DaemonCheck struct {
	Name    string `json:"name"`
	Running bool   `json:"running"`
	Port    int    `json:"port,omitempty"`
	PID     int    `json:"pid,omitempty"`
	Status  string `json:"status"`
	Message string `json:"message,omitempty"`
}

// ConfigCheck represents a configuration check result
type ConfigCheck struct {
	Name    string `json:"name"`
	Valid   bool   `json:"valid"`
	Status  string `json:"status"`
	Message string `json:"message,omitempty"`
}

// InvariantCheck represents a design invariant check result
type InvariantCheck struct {
	ID      string   `json:"id"`
	Name    string   `json:"name"`
	Status  string   `json:"status"`
	Message string   `json:"message,omitempty"`
	Details []string `json:"details,omitempty"`
}

// DoctorTestSuite manages E2E tests for the doctor command
type DoctorTestSuite struct {
	t       *testing.T
	logger  *TestLogger
	tempDir string
	cleanup []func()
}

// NewDoctorTestSuite creates a new doctor test suite
func NewDoctorTestSuite(t *testing.T, scenario string) *DoctorTestSuite {
	logger := NewTestLogger(t, scenario)

	s := &DoctorTestSuite{
		t:      t,
		logger: logger,
	}

	return s
}

// Setup creates a temporary directory for testing
func (s *DoctorTestSuite) Setup() error {
	s.logger.Log("[E2E-DOCTOR] Setting up test environment")

	// Create temp directory
	tempDir, err := os.MkdirTemp("", "ntm-doctor-e2e-")
	if err != nil {
		return err
	}
	s.tempDir = tempDir

	s.cleanup = append(s.cleanup, func() {
		os.RemoveAll(tempDir)
	})

	return nil
}

// Cleanup runs all cleanup functions
func (s *DoctorTestSuite) Cleanup() {
	s.logger.Log("[E2E-DOCTOR] Running cleanup")
	for i := len(s.cleanup) - 1; i >= 0; i-- {
		s.cleanup[i]()
	}
}

// TempDir returns the temp directory path
func (s *DoctorTestSuite) TempDir() string {
	return s.tempDir
}

// RunDoctor executes ntm doctor and returns the raw output
func (s *DoctorTestSuite) RunDoctor(args ...string) ([]byte, error) {
	allArgs := append([]string{"doctor"}, args...)
	cmd := exec.Command("ntm", allArgs...)
	cmd.Dir = s.tempDir

	s.logger.Log("[E2E-DOCTOR] Running: ntm %v", allArgs)

	output, err := cmd.CombinedOutput()
	s.logger.Log("[E2E-DOCTOR] Output: %s", string(output))
	if err != nil {
		s.logger.Log("[E2E-DOCTOR] Exit error: %v", err)
	}

	return output, err
}

// RunDoctorJSON executes ntm doctor --json and parses the result
func (s *DoctorTestSuite) RunDoctorJSON() (*DoctorReport, error) {
	output, err := s.RunDoctor("--json")
	// Note: doctor can return non-zero exit code for unhealthy state
	// but still produce valid JSON output

	var report DoctorReport
	if jsonErr := json.Unmarshal(output, &report); jsonErr != nil {
		if err != nil {
			return nil, err
		}
		return nil, jsonErr
	}

	return &report, nil
}

// CreateBeadsDir creates a .beads directory in temp
func (s *DoctorTestSuite) CreateBeadsDir() error {
	beadsDir := filepath.Join(s.tempDir, ".beads")
	return os.MkdirAll(beadsDir, 0755)
}

// TestDoctorBasicRun tests that doctor command runs without crashing
func TestDoctorBasicRun(t *testing.T) {
	SkipIfShort(t)
	SkipIfNoNTM(t)

	suite := NewDoctorTestSuite(t, "doctor-basic")
	if err := suite.Setup(); err != nil {
		t.Fatalf("[E2E-DOCTOR] Setup failed: %v", err)
	}
	defer suite.Cleanup()

	output, _ := suite.RunDoctor()
	// Doctor should always produce some output
	if len(output) == 0 {
		t.Error("[E2E-DOCTOR] Expected non-empty output from doctor command")
	}

	suite.logger.Log("[E2E-DOCTOR] Basic run test passed")
}

// TestDoctorJSONOutput tests that --json flag produces valid JSON
func TestDoctorJSONOutput(t *testing.T) {
	SkipIfShort(t)
	SkipIfNoNTM(t)

	suite := NewDoctorTestSuite(t, "doctor-json")
	if err := suite.Setup(); err != nil {
		t.Fatalf("[E2E-DOCTOR] Setup failed: %v", err)
	}
	defer suite.Cleanup()

	report, err := suite.RunDoctorJSON()
	if err != nil {
		t.Fatalf("[E2E-DOCTOR] Failed to get JSON output: %v", err)
	}

	// Validate required fields
	if report.Overall == "" {
		t.Error("[E2E-DOCTOR] Expected non-empty overall status")
	}

	validOverall := map[string]bool{"healthy": true, "warning": true, "unhealthy": true}
	if !validOverall[report.Overall] {
		t.Errorf("[E2E-DOCTOR] Invalid overall status: %s", report.Overall)
	}

	if report.Timestamp.IsZero() {
		t.Error("[E2E-DOCTOR] Expected non-zero timestamp")
	}

	suite.logger.Log("[E2E-DOCTOR] JSON output valid: overall=%s, warnings=%d, errors=%d",
		report.Overall, report.Warnings, report.Errors)
}

// TestDoctorToolsCheck tests that tools section is populated
func TestDoctorToolsCheck(t *testing.T) {
	SkipIfShort(t)
	SkipIfNoNTM(t)

	suite := NewDoctorTestSuite(t, "doctor-tools")
	if err := suite.Setup(); err != nil {
		t.Fatalf("[E2E-DOCTOR] Setup failed: %v", err)
	}
	defer suite.Cleanup()

	report, err := suite.RunDoctorJSON()
	if err != nil {
		t.Fatalf("[E2E-DOCTOR] Failed to get JSON output: %v", err)
	}

	// Tools array should exist
	if report.Tools == nil {
		t.Error("[E2E-DOCTOR] Expected non-nil tools array")
	}

	// Check that each tool has required fields
	for _, tool := range report.Tools {
		if tool.Name == "" {
			t.Error("[E2E-DOCTOR] Tool missing name")
		}
		if tool.Status == "" {
			t.Errorf("[E2E-DOCTOR] Tool %s missing status", tool.Name)
		}

		validStatus := map[string]bool{"ok": true, "warning": true, "error": true}
		if !validStatus[tool.Status] {
			t.Errorf("[E2E-DOCTOR] Tool %s has invalid status: %s", tool.Name, tool.Status)
		}

		suite.logger.Log("[E2E-DOCTOR] Tool: %s installed=%v status=%s", tool.Name, tool.Installed, tool.Status)
	}
}

// TestDoctorDependenciesCheck tests that dependencies are checked
func TestDoctorDependenciesCheck(t *testing.T) {
	SkipIfShort(t)
	SkipIfNoNTM(t)

	suite := NewDoctorTestSuite(t, "doctor-deps")
	if err := suite.Setup(); err != nil {
		t.Fatalf("[E2E-DOCTOR] Setup failed: %v", err)
	}
	defer suite.Cleanup()

	report, err := suite.RunDoctorJSON()
	if err != nil {
		t.Fatalf("[E2E-DOCTOR] Failed to get JSON output: %v", err)
	}

	// Should check at least tmux
	foundTmux := false
	for _, dep := range report.Dependencies {
		if dep.Name == "tmux" {
			foundTmux = true
			suite.logger.Log("[E2E-DOCTOR] tmux: installed=%v version=%s status=%s",
				dep.Installed, dep.Version, dep.Status)
		}
	}

	if !foundTmux {
		t.Error("[E2E-DOCTOR] Expected tmux in dependencies check")
	}

	// Should also check Go
	foundGo := false
	for _, dep := range report.Dependencies {
		if dep.Name == "go" {
			foundGo = true
			suite.logger.Log("[E2E-DOCTOR] go: installed=%v version=%s status=%s",
				dep.Installed, dep.Version, dep.Status)
		}
	}

	if !foundGo {
		t.Error("[E2E-DOCTOR] Expected go in dependencies check")
	}
}

// TestDoctorDaemonsCheck tests that daemon ports are checked
func TestDoctorDaemonsCheck(t *testing.T) {
	SkipIfShort(t)
	SkipIfNoNTM(t)

	suite := NewDoctorTestSuite(t, "doctor-daemons")
	if err := suite.Setup(); err != nil {
		t.Fatalf("[E2E-DOCTOR] Setup failed: %v", err)
	}
	defer suite.Cleanup()

	report, err := suite.RunDoctorJSON()
	if err != nil {
		t.Fatalf("[E2E-DOCTOR] Failed to get JSON output: %v", err)
	}

	// Should have daemon checks
	if len(report.Daemons) == 0 {
		t.Error("[E2E-DOCTOR] Expected non-empty daemons array")
	}

	// Check expected daemons
	expectedDaemons := map[string]bool{
		"agent-mail": false,
		"cm-server":  false,
	}

	for _, daemon := range report.Daemons {
		if _, expected := expectedDaemons[daemon.Name]; expected {
			expectedDaemons[daemon.Name] = true
		}
		suite.logger.Log("[E2E-DOCTOR] Daemon: %s port=%d running=%v status=%s",
			daemon.Name, daemon.Port, daemon.Running, daemon.Status)
	}

	for name, found := range expectedDaemons {
		if !found {
			t.Errorf("[E2E-DOCTOR] Expected daemon check for: %s", name)
		}
	}
}

// TestDoctorConfigurationCheck tests configuration validation
func TestDoctorConfigurationCheck(t *testing.T) {
	SkipIfShort(t)
	SkipIfNoNTM(t)

	suite := NewDoctorTestSuite(t, "doctor-config")
	if err := suite.Setup(); err != nil {
		t.Fatalf("[E2E-DOCTOR] Setup failed: %v", err)
	}
	defer suite.Cleanup()

	report, err := suite.RunDoctorJSON()
	if err != nil {
		t.Fatalf("[E2E-DOCTOR] Failed to get JSON output: %v", err)
	}

	// Should have configuration checks
	if len(report.Configuration) == 0 {
		t.Error("[E2E-DOCTOR] Expected non-empty configuration array")
	}

	for _, config := range report.Configuration {
		if config.Name == "" {
			t.Error("[E2E-DOCTOR] Configuration check missing name")
		}
		if config.Status == "" {
			t.Errorf("[E2E-DOCTOR] Configuration %s missing status", config.Name)
		}
		suite.logger.Log("[E2E-DOCTOR] Config: %s valid=%v status=%s message=%s",
			config.Name, config.Valid, config.Status, config.Message)
	}
}

// TestDoctorWithBeadsDir tests doctor behavior when .beads exists
func TestDoctorWithBeadsDir(t *testing.T) {
	SkipIfShort(t)
	SkipIfNoNTM(t)

	suite := NewDoctorTestSuite(t, "doctor-beads")
	if err := suite.Setup(); err != nil {
		t.Fatalf("[E2E-DOCTOR] Setup failed: %v", err)
	}
	defer suite.Cleanup()

	// Create .beads directory
	if err := suite.CreateBeadsDir(); err != nil {
		t.Fatalf("[E2E-DOCTOR] Failed to create .beads directory: %v", err)
	}

	report, err := suite.RunDoctorJSON()
	if err != nil {
		t.Fatalf("[E2E-DOCTOR] Failed to get JSON output: %v", err)
	}

	// Find beads configuration check
	foundBeads := false
	for _, config := range report.Configuration {
		if config.Name == ".beads directory" {
			foundBeads = true
			if config.Status != "ok" {
				t.Errorf("[E2E-DOCTOR] Expected .beads directory status ok, got: %s", config.Status)
			}
			suite.logger.Log("[E2E-DOCTOR] .beads directory check: status=%s valid=%v",
				config.Status, config.Valid)
		}
	}

	if !foundBeads {
		t.Error("[E2E-DOCTOR] Expected .beads directory configuration check")
	}
}

// TestDoctorInvariantsCheck tests that design invariants are checked
func TestDoctorInvariantsCheck(t *testing.T) {
	SkipIfShort(t)
	SkipIfNoNTM(t)

	suite := NewDoctorTestSuite(t, "doctor-invariants")
	if err := suite.Setup(); err != nil {
		t.Fatalf("[E2E-DOCTOR] Setup failed: %v", err)
	}
	defer suite.Cleanup()

	report, err := suite.RunDoctorJSON()
	if err != nil {
		t.Fatalf("[E2E-DOCTOR] Failed to get JSON output: %v", err)
	}

	// Should have invariant checks
	if report.Invariants == nil {
		t.Error("[E2E-DOCTOR] Expected non-nil invariants array")
	}

	for _, inv := range report.Invariants {
		if inv.ID == "" {
			t.Error("[E2E-DOCTOR] Invariant missing ID")
		}
		if inv.Name == "" {
			t.Errorf("[E2E-DOCTOR] Invariant %s missing name", inv.ID)
		}
		if inv.Status == "" {
			t.Errorf("[E2E-DOCTOR] Invariant %s missing status", inv.ID)
		}
		suite.logger.Log("[E2E-DOCTOR] Invariant: %s (%s) status=%s", inv.Name, inv.ID, inv.Status)
	}
}

// TestDoctorOverallStatusConsistency tests that overall status matches counts
func TestDoctorOverallStatusConsistency(t *testing.T) {
	SkipIfShort(t)
	SkipIfNoNTM(t)

	suite := NewDoctorTestSuite(t, "doctor-consistency")
	if err := suite.Setup(); err != nil {
		t.Fatalf("[E2E-DOCTOR] Setup failed: %v", err)
	}
	defer suite.Cleanup()

	report, err := suite.RunDoctorJSON()
	if err != nil {
		t.Fatalf("[E2E-DOCTOR] Failed to get JSON output: %v", err)
	}

	// Count errors and warnings from all sections
	var countedErrors, countedWarnings int

	for _, t := range report.Tools {
		switch t.Status {
		case "error":
			countedErrors++
		case "warning":
			countedWarnings++
		}
	}
	for _, d := range report.Dependencies {
		switch d.Status {
		case "error":
			countedErrors++
		case "warning":
			countedWarnings++
		}
	}
	for _, d := range report.Daemons {
		switch d.Status {
		case "error":
			countedErrors++
		case "warning":
			countedWarnings++
		}
	}
	for _, c := range report.Configuration {
		switch c.Status {
		case "error":
			countedErrors++
		case "warning":
			countedWarnings++
		}
	}
	for _, i := range report.Invariants {
		switch i.Status {
		case "error":
			countedErrors++
		case "warning":
			countedWarnings++
		}
	}

	suite.logger.Log("[E2E-DOCTOR] Counted: errors=%d warnings=%d", countedErrors, countedWarnings)
	suite.logger.Log("[E2E-DOCTOR] Reported: errors=%d warnings=%d overall=%s",
		report.Errors, report.Warnings, report.Overall)

	// Verify counts match
	if report.Errors != countedErrors {
		t.Errorf("[E2E-DOCTOR] Error count mismatch: reported=%d counted=%d",
			report.Errors, countedErrors)
	}
	if report.Warnings != countedWarnings {
		t.Errorf("[E2E-DOCTOR] Warning count mismatch: reported=%d counted=%d",
			report.Warnings, countedWarnings)
	}

	// Verify overall status is consistent with counts
	switch {
	case countedErrors > 0 && report.Overall != "unhealthy":
		t.Errorf("[E2E-DOCTOR] Expected unhealthy with %d errors, got: %s",
			countedErrors, report.Overall)
	case countedErrors == 0 && countedWarnings > 0 && report.Overall != "warning":
		t.Errorf("[E2E-DOCTOR] Expected warning with %d warnings, got: %s",
			countedWarnings, report.Overall)
	case countedErrors == 0 && countedWarnings == 0 && report.Overall != "healthy":
		t.Errorf("[E2E-DOCTOR] Expected healthy with no issues, got: %s", report.Overall)
	}
}

// TestDoctorVerboseFlag tests that --verbose produces additional output
func TestDoctorVerboseFlag(t *testing.T) {
	SkipIfShort(t)
	SkipIfNoNTM(t)

	suite := NewDoctorTestSuite(t, "doctor-verbose")
	if err := suite.Setup(); err != nil {
		t.Fatalf("[E2E-DOCTOR] Setup failed: %v", err)
	}
	defer suite.Cleanup()

	// Get normal output
	normalOutput, _ := suite.RunDoctor()

	// Get verbose output
	verboseOutput, _ := suite.RunDoctor("--verbose")

	// Verbose output should be at least as long as normal output
	if len(verboseOutput) < len(normalOutput) {
		t.Errorf("[E2E-DOCTOR] Verbose output (%d bytes) shorter than normal (%d bytes)",
			len(verboseOutput), len(normalOutput))
	}

	suite.logger.Log("[E2E-DOCTOR] Normal output: %d bytes", len(normalOutput))
	suite.logger.Log("[E2E-DOCTOR] Verbose output: %d bytes", len(verboseOutput))
}

// TestDoctorTimestampFormat tests that timestamp is in correct format
func TestDoctorTimestampFormat(t *testing.T) {
	SkipIfShort(t)
	SkipIfNoNTM(t)

	suite := NewDoctorTestSuite(t, "doctor-timestamp")
	if err := suite.Setup(); err != nil {
		t.Fatalf("[E2E-DOCTOR] Setup failed: %v", err)
	}
	defer suite.Cleanup()

	report, err := suite.RunDoctorJSON()
	if err != nil {
		t.Fatalf("[E2E-DOCTOR] Failed to get JSON output: %v", err)
	}

	// Timestamp should be recent (within last minute)
	now := time.Now()
	diff := now.Sub(report.Timestamp)

	if diff < 0 {
		t.Error("[E2E-DOCTOR] Timestamp is in the future")
	}

	if diff > time.Minute {
		t.Errorf("[E2E-DOCTOR] Timestamp too old: %v ago", diff)
	}

	suite.logger.Log("[E2E-DOCTOR] Timestamp: %v (%.2f seconds ago)",
		report.Timestamp, diff.Seconds())
}

// TestDoctorRequiredToolsMarked tests that required tools are properly marked
func TestDoctorRequiredToolsMarked(t *testing.T) {
	SkipIfShort(t)
	SkipIfNoNTM(t)

	suite := NewDoctorTestSuite(t, "doctor-required")
	if err := suite.Setup(); err != nil {
		t.Fatalf("[E2E-DOCTOR] Setup failed: %v", err)
	}
	defer suite.Cleanup()

	report, err := suite.RunDoctorJSON()
	if err != nil {
		t.Fatalf("[E2E-DOCTOR] Failed to get JSON output: %v", err)
	}

	// bv and bd should be marked as required
	requiredTools := map[string]bool{"bv": false, "bd": false}

	for _, tool := range report.Tools {
		if _, isRequired := requiredTools[tool.Name]; isRequired {
			if !tool.Required {
				t.Errorf("[E2E-DOCTOR] Tool %s should be marked as required", tool.Name)
			}
			requiredTools[tool.Name] = true
			suite.logger.Log("[E2E-DOCTOR] Required tool: %s installed=%v status=%s",
				tool.Name, tool.Installed, tool.Status)
		}
	}

	for name, found := range requiredTools {
		if !found {
			t.Errorf("[E2E-DOCTOR] Required tool %s not found in tools list", name)
		}
	}
}

// TestDoctorDaemonPortsPopulated tests that daemon ports are properly populated
func TestDoctorDaemonPortsPopulated(t *testing.T) {
	SkipIfShort(t)
	SkipIfNoNTM(t)

	suite := NewDoctorTestSuite(t, "doctor-ports")
	if err := suite.Setup(); err != nil {
		t.Fatalf("[E2E-DOCTOR] Setup failed: %v", err)
	}
	defer suite.Cleanup()

	report, err := suite.RunDoctorJSON()
	if err != nil {
		t.Fatalf("[E2E-DOCTOR] Failed to get JSON output: %v", err)
	}

	// Expected ports
	expectedPorts := map[string]int{
		"agent-mail": 8765,
		"cm-server":  8766,
	}

	for _, daemon := range report.Daemons {
		if expectedPort, ok := expectedPorts[daemon.Name]; ok {
			if daemon.Port != expectedPort {
				t.Errorf("[E2E-DOCTOR] Daemon %s expected port %d, got %d",
					daemon.Name, expectedPort, daemon.Port)
			}
			suite.logger.Log("[E2E-DOCTOR] Daemon %s: port=%d (expected %d)",
				daemon.Name, daemon.Port, expectedPort)
		}
	}
}

// TestDoctorOutputFormat tests TUI output contains expected sections
func TestDoctorOutputFormat(t *testing.T) {
	SkipIfShort(t)
	SkipIfNoNTM(t)

	suite := NewDoctorTestSuite(t, "doctor-format")
	if err := suite.Setup(); err != nil {
		t.Fatalf("[E2E-DOCTOR] Setup failed: %v", err)
	}
	defer suite.Cleanup()

	output, _ := suite.RunDoctor()
	outputStr := string(output)

	// Check for expected section headers
	expectedSections := []string{
		"Tools",
		"Dependencies",
		"Daemons",
		"Configuration",
	}

	for _, section := range expectedSections {
		found := false
		// Check for section name in output (case insensitive check)
		if containsSection(outputStr, section) {
			found = true
		}
		if !found {
			t.Errorf("[E2E-DOCTOR] Expected section '%s' not found in output", section)
		}
	}

	suite.logger.Log("[E2E-DOCTOR] Output format test passed")
}

// containsSection checks if the output contains a section header
func containsSection(output, section string) bool {
	// Look for section in various formats (with/without colon, styled)
	patterns := []string{
		section + ":",
		section,
	}
	for _, pattern := range patterns {
		if containsIgnoreCase(output, pattern) {
			return true
		}
	}
	return false
}

// containsIgnoreCase performs a case-insensitive contains check
func containsIgnoreCase(s, substr string) bool {
	for i := 0; i <= len(s)-len(substr); i++ {
		if equalFoldSubstring(s[i:i+len(substr)], substr) {
			return true
		}
	}
	return false
}

// equalFoldSubstring compares two strings case-insensitively
func equalFoldSubstring(a, b string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := 0; i < len(a); i++ {
		ca, cb := a[i], b[i]
		if ca >= 'A' && ca <= 'Z' {
			ca += 'a' - 'A'
		}
		if cb >= 'A' && cb <= 'Z' {
			cb += 'a' - 'A'
		}
		if ca != cb {
			return false
		}
	}
	return true
}
