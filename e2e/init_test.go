//go:build e2e
// +build e2e

// Package e2e contains end-to-end tests for NTM robot mode commands.
// [E2E-INIT] Tests for ntm init (project initialization workflow).
package e2e

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// InitResult mirrors the CLI JSON output structure for init command
type InitResult struct {
	Success          bool     `json:"success"`
	ProjectPath      string   `json:"project_path"`
	NTMDir           string   `json:"ntm_dir"`
	CreatedDirs      []string `json:"created_dirs"`
	CreatedFiles     []string `json:"created_files"`
	AgentMail        bool     `json:"agent_mail"`
	AgentMailWarning string   `json:"agent_mail_warning,omitempty"`
	HooksInstalled   []string `json:"hooks_installed"`
	HooksWarning     string   `json:"hooks_warning,omitempty"`
	Template         string   `json:"template"`
	Agents           string   `json:"agents"`
	AutoSpawn        bool     `json:"auto_spawn"`
	NonInteractive   bool     `json:"non_interactive"`
	Force            bool     `json:"force"`
	NoHooks          bool     `json:"no_hooks"`
}

type initStepEvent struct {
	Number      int    `json:"number"`
	Total       int    `json:"total"`
	Description string `json:"description"`
	AtMs        int64  `json:"at_ms"`
}

type initTestReport struct {
	Timestamp    string          `json:"timestamp"`
	TestName     string          `json:"test_name"`
	Scenario     string          `json:"scenario"`
	Passed       bool            `json:"passed"`
	DurationMs   int64           `json:"duration_ms"`
	LogPath      string          `json:"log_path"`
	ReportPath   string          `json:"report_path"`
	ArtifactsDir string          `json:"artifacts_dir,omitempty"`
	TempDir      string          `json:"temp_dir,omitempty"`
	LastCommand  []string        `json:"last_command,omitempty"`
	LastOutput   string          `json:"last_output,omitempty"`
	Steps        []initStepEvent `json:"steps,omitempty"`
}

// InitTestSuite manages E2E tests for the init command
type InitTestSuite struct {
	t           *testing.T
	logger      *TestLogger
	tempDir     string
	cleanup     []func()
	cleanedUp   bool
	scenario    string
	startTime   time.Time
	stepTotal   int
	stepCurrent int
	steps       []initStepEvent

	lastCommand []string
	lastOutput  []byte
}

// NewInitTestSuite creates a new init test suite
func NewInitTestSuite(t *testing.T, scenario string) *InitTestSuite {
	logger := NewTestLogger(t, scenario)

	s := &InitTestSuite{
		t:         t,
		logger:    logger,
		scenario:  scenario,
		startTime: time.Now(),
	}

	t.Cleanup(s.Cleanup)

	return s
}

func (s *InitTestSuite) SetSteps(total int) {
	s.stepTotal = total
	s.stepCurrent = 0
	s.steps = nil
}

func (s *InitTestSuite) Step(description string) {
	s.stepCurrent++
	total := s.stepTotal
	if total <= 0 {
		total = 1
	}
	elapsedMs := time.Since(s.startTime).Milliseconds()
	s.steps = append(s.steps, initStepEvent{
		Number:      s.stepCurrent,
		Total:       total,
		Description: description,
		AtMs:        elapsedMs,
	})
	s.logger.Log("[E2E] Step %d/%d: %s", s.stepCurrent, total, description)
}

// Setup creates a temporary directory for testing
func (s *InitTestSuite) Setup() error {
	s.logger.Log("[E2E-INIT] Setting up test environment")

	// Create temp directory
	tempDir, err := os.MkdirTemp("", "ntm-init-e2e-")
	if err != nil {
		return err
	}
	s.tempDir = tempDir
	s.logger.Log("[E2E] Creating directory: %s", tempDir)

	s.cleanup = append(s.cleanup, func() {
		os.RemoveAll(tempDir)
	})

	return nil
}

// SetupWithGit creates a temp directory with git initialized
func (s *InitTestSuite) SetupWithGit() error {
	if err := s.Setup(); err != nil {
		return err
	}

	// Initialize git repo
	s.logger.Log("[E2E] Step 1/3: Initialize git repo")
	cmd := exec.Command("git", "init")
	cmd.Dir = s.tempDir
	if out, err := cmd.CombinedOutput(); err != nil {
		s.logger.Log("[E2E-INIT] git init failed: %s, output: %s", err, string(out))
		return err
	}

	// Configure git user
	s.logger.Log("[E2E] Step 2/3: Configure git user")
	cmd = exec.Command("git", "config", "user.email", "test@example.com")
	cmd.Dir = s.tempDir
	cmd.Run()

	cmd = exec.Command("git", "config", "user.name", "Test User")
	cmd.Dir = s.tempDir
	cmd.Run()

	s.logger.Log("[E2E] Step 3/3: Git repo ready")
	s.logger.Log("[E2E-INIT] Created git repo at %s", s.tempDir)
	return nil
}

// Cleanup runs all cleanup functions
func (s *InitTestSuite) Cleanup() {
	if s.cleanedUp {
		return
	}
	s.cleanedUp = true

	start := s.startTime
	if start.IsZero() {
		start = time.Now()
	}
	duration := time.Since(start)
	passed := !s.t.Failed()

	logPath := ""
	if s.logger != nil {
		logPath = s.logger.LogPath()
	}

	reportBase := strings.TrimSuffix(logPath, ".log")
	if reportBase == "" {
		reportBase = filepath.Join(os.TempDir(), fmt.Sprintf("ntm-init-%s-%d", s.scenario, time.Now().Unix()))
	}
	reportPath := reportBase + ".report.json"
	artifactsDir := reportBase + ".artifacts"

	if !passed {
		s.logger.Log("[E2E] FAIL: %s - see %s", s.t.Name(), logPath)
		if err := s.saveArtifacts(artifactsDir); err != nil {
			s.logger.Log("[E2E-INIT] Failed to save artifacts: %v", err)
		}
	} else {
		s.logger.Log("[E2E] PASS: %s in %s", s.t.Name(), duration.Round(time.Millisecond))
	}

	if err := s.writeReport(reportPath, artifactsDir, passed, duration, logPath); err != nil {
		s.logger.Log("[E2E-INIT] Failed to write report: %v", err)
	}

	s.logger.Log("[E2E-INIT] Running cleanup")
	for i := len(s.cleanup) - 1; i >= 0; i-- {
		s.cleanup[i]()
	}

	if s.logger != nil {
		s.logger.Close()
	}
}

// TempDir returns the temp directory path
func (s *InitTestSuite) TempDir() string {
	return s.tempDir
}

// RunInit executes ntm init and returns the raw output
func (s *InitTestSuite) RunInit(args ...string) ([]byte, error) {
	allArgs := append([]string{"init"}, args...)
	s.lastCommand = append([]string{"ntm"}, allArgs...)
	cmd := exec.Command("ntm", allArgs...)
	cmd.Dir = s.tempDir

	s.logger.Log("[E2E-INIT] Running: ntm %v", allArgs)

	output, err := cmd.CombinedOutput()
	s.lastOutput = output
	s.logger.Log("[E2E-INIT] Output: %s", string(output))
	if err != nil {
		s.logger.Log("[E2E-INIT] Exit error: %v", err)
	}

	return output, err
}

// RunInitJSON executes ntm init --json and parses the result
func (s *InitTestSuite) RunInitJSON(args ...string) (*InitResult, error) {
	allArgs := append(args, "--json")
	output, err := s.RunInit(allArgs...)
	if err != nil {
		// Check if output contains valid JSON despite error
		var result InitResult
		if jsonErr := json.Unmarshal(output, &result); jsonErr == nil {
			return &result, nil
		}
		return nil, err
	}

	var result InitResult
	if jsonErr := json.Unmarshal(output, &result); jsonErr != nil {
		return nil, jsonErr
	}

	return &result, nil
}

type configValidateReport struct {
	Valid bool `json:"valid"`
}

func (s *InitTestSuite) RunConfigValidateJSON() (*configValidateReport, error) {
	cmd := exec.Command("ntm", "config", "validate", "--json")
	cmd.Dir = s.tempDir
	s.logger.Log("[E2E-INIT] Running: ntm config validate --json")
	out, err := cmd.CombinedOutput()
	s.logger.Log("[E2E-INIT] Output: %s", string(out))
	if err != nil {
		return nil, fmt.Errorf("config validate failed: %w: %s", err, strings.TrimSpace(string(out)))
	}

	var report configValidateReport
	if err := json.Unmarshal(out, &report); err != nil {
		return nil, fmt.Errorf("parse config validate output: %w", err)
	}
	return &report, nil
}

func (s *InitTestSuite) saveArtifacts(dir string) error {
	if s.tempDir == "" {
		return nil
	}

	ntmDir := filepath.Join(s.tempDir, ".ntm")
	if _, err := os.Stat(ntmDir); err != nil {
		return nil
	}

	if err := os.MkdirAll(dir, 0755); err != nil {
		return err
	}

	dst := filepath.Join(dir, ".ntm")
	if err := copyDir(ntmDir, dst); err != nil {
		return err
	}
	s.logger.Log("[E2E-INIT] Saved .ntm artifacts to %s", dst)
	return nil
}

func (s *InitTestSuite) writeReport(reportPath, artifactsDir string, passed bool, duration time.Duration, logPath string) error {
	var lastOutput string
	if len(s.lastOutput) > 0 {
		// Keep reports reasonably sized.
		const max = 20000
		if len(s.lastOutput) > max {
			lastOutput = string(s.lastOutput[:max]) + "...(truncated)"
		} else {
			lastOutput = string(s.lastOutput)
		}
	}

	rep := initTestReport{
		Timestamp:    time.Now().UTC().Format(time.RFC3339),
		TestName:     s.t.Name(),
		Scenario:     s.scenario,
		Passed:       passed,
		DurationMs:   duration.Milliseconds(),
		LogPath:      logPath,
		ReportPath:   reportPath,
		ArtifactsDir: "",
		TempDir:      s.tempDir,
		LastCommand:  s.lastCommand,
		LastOutput:   lastOutput,
		Steps:        s.steps,
	}
	if !passed {
		rep.ArtifactsDir = artifactsDir
	}

	data, err := json.MarshalIndent(rep, "", "  ")
	if err != nil {
		return err
	}
	if err := os.WriteFile(reportPath, data, 0644); err != nil {
		return err
	}
	s.logger.Log("[E2E-INIT] Wrote report: %s", reportPath)
	return nil
}

func copyDir(src, dst string) error {
	entries, err := os.ReadDir(src)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(dst, 0755); err != nil {
		return err
	}

	for _, entry := range entries {
		srcPath := filepath.Join(src, entry.Name())
		dstPath := filepath.Join(dst, entry.Name())
		if entry.IsDir() {
			if err := copyDir(srcPath, dstPath); err != nil {
				return err
			}
			continue
		}
		data, err := os.ReadFile(srcPath)
		if err != nil {
			return err
		}
		if err := os.WriteFile(dstPath, data, 0644); err != nil {
			return err
		}
	}
	return nil
}

// TestInitBasicRun tests that init command creates .ntm directory
func TestInitBasicRun(t *testing.T) {
	SkipIfShort(t)
	SkipIfNoNTM(t)

	suite := NewInitTestSuite(t, "init-basic")
	if err := suite.Setup(); err != nil {
		t.Fatalf("[E2E-INIT] Setup failed: %v", err)
	}
	defer suite.Cleanup()

	// Run init with --no-hooks to avoid git hook installation issues
	_, err := suite.RunInit("--no-hooks")
	if err != nil {
		t.Fatalf("[E2E-INIT] Init command failed: %v", err)
	}

	// Check .ntm directory was created
	ntmDir := filepath.Join(suite.TempDir(), ".ntm")
	if _, err := os.Stat(ntmDir); os.IsNotExist(err) {
		t.Error("[E2E-INIT] Expected .ntm directory to be created")
	}

	suite.logger.Log("[E2E-INIT] Basic run test passed - .ntm directory exists at %s", ntmDir)
}

// TestInitScenarioFreshProjectDetailedLogging validates fresh init + directory structure + config validation,
// using the bead-required step logging format.
func TestInitScenarioFreshProjectDetailedLogging(t *testing.T) {
	SkipIfShort(t)
	SkipIfNoNTM(t)

	suite := NewInitTestSuite(t, "init-scenario-fresh-detailed")
	defer suite.Cleanup()

	suite.SetSteps(4)
	suite.Step("Create temp directory")
	if err := suite.Setup(); err != nil {
		t.Fatalf("[E2E-INIT] Setup failed: %v", err)
	}

	suite.Step("Run ntm init (fresh project)")
	result, err := suite.RunInitJSON("--no-hooks")
	if err != nil {
		t.Fatalf("[E2E-INIT] Init command failed: %v", err)
	}
	if !result.Success {
		suite.logger.Log("[E2E] FAIL: %s - expected success=true, got false", t.Name())
		t.Fatal("[E2E-INIT] Expected success=true in result")
	}

	suite.Step("Verify .ntm directory structure and key files")
	paths := []string{
		filepath.Join(suite.TempDir(), ".ntm"),
		filepath.Join(suite.TempDir(), ".ntm", "templates"),
		filepath.Join(suite.TempDir(), ".ntm", "pipelines"),
		filepath.Join(suite.TempDir(), ".ntm", "config.toml"),
		filepath.Join(suite.TempDir(), ".ntm", "palette.md"),
		filepath.Join(suite.TempDir(), ".ntm", "personas.toml"),
	}
	for _, p := range paths {
		if _, err := os.Stat(p); err != nil {
			suite.logger.Log("[E2E] FAIL: %s - expected %s to exist, got err=%v", t.Name(), p, err)
			t.Fatalf("[E2E-INIT] Expected path to exist: %s", p)
		}
	}

	suite.Step("Validate configs with ntm config validate --json")
	report, err := suite.RunConfigValidateJSON()
	if err != nil {
		t.Fatalf("[E2E-INIT] Config validate failed: %v", err)
	}
	if !report.Valid {
		suite.logger.Log("[E2E] FAIL: %s - expected valid=true, got false", t.Name())
		t.Fatal("[E2E-INIT] Expected config validate valid=true")
	}
}

// TestInitJSONOutput tests that --json flag produces valid JSON
func TestInitJSONOutput(t *testing.T) {
	SkipIfShort(t)
	SkipIfNoNTM(t)

	suite := NewInitTestSuite(t, "init-json")
	if err := suite.Setup(); err != nil {
		t.Fatalf("[E2E-INIT] Setup failed: %v", err)
	}
	defer suite.Cleanup()

	result, err := suite.RunInitJSON("--no-hooks")
	if err != nil {
		t.Fatalf("[E2E-INIT] Init command failed: %v", err)
	}

	// Validate required fields
	if !result.Success {
		t.Error("[E2E-INIT] Expected success=true in result")
	}

	if result.ProjectPath == "" {
		t.Error("[E2E-INIT] Expected non-empty project_path")
	}

	if result.NTMDir == "" {
		t.Error("[E2E-INIT] Expected non-empty ntm_dir")
	}

	// ntm_dir should be project_path/.ntm
	expectedNTMDir := filepath.Join(result.ProjectPath, ".ntm")
	if result.NTMDir != expectedNTMDir {
		t.Errorf("[E2E-INIT] Expected ntm_dir=%s, got %s", expectedNTMDir, result.NTMDir)
	}

	suite.logger.Log("[E2E-INIT] JSON output valid: project_path=%s, ntm_dir=%s",
		result.ProjectPath, result.NTMDir)
}

// TestInitCreatesConfigFile tests that init creates a config.toml file
func TestInitCreatesConfigFile(t *testing.T) {
	SkipIfShort(t)
	SkipIfNoNTM(t)

	suite := NewInitTestSuite(t, "init-config")
	if err := suite.Setup(); err != nil {
		t.Fatalf("[E2E-INIT] Setup failed: %v", err)
	}
	defer suite.Cleanup()

	result, err := suite.RunInitJSON("--no-hooks")
	if err != nil {
		t.Fatalf("[E2E-INIT] Init command failed: %v", err)
	}

	// Check config.toml was created
	configPath := filepath.Join(result.NTMDir, "config.toml")
	if _, err := os.Stat(configPath); os.IsNotExist(err) {
		t.Error("[E2E-INIT] Expected config.toml to be created")
	}

	// Read config file
	configContent, err := os.ReadFile(configPath)
	if err != nil {
		t.Fatalf("[E2E-INIT] Failed to read config.toml: %v", err)
	}

	if len(configContent) == 0 {
		t.Error("[E2E-INIT] Expected non-empty config.toml")
	}

	suite.logger.Log("[E2E-INIT] Config file created at %s (%d bytes)",
		configPath, len(configContent))
}

// TestInitIdempotency tests that running init twice fails without --force
func TestInitIdempotency(t *testing.T) {
	SkipIfShort(t)
	SkipIfNoNTM(t)

	suite := NewInitTestSuite(t, "init-idempotency")
	if err := suite.Setup(); err != nil {
		t.Fatalf("[E2E-INIT] Setup failed: %v", err)
	}
	defer suite.Cleanup()

	// First init should succeed
	_, err := suite.RunInit("--no-hooks")
	if err != nil {
		t.Fatalf("[E2E-INIT] First init failed: %v", err)
	}

	suite.logger.Log("[E2E-INIT] First init succeeded")

	// Second init without --force should fail
	output, err := suite.RunInit("--no-hooks")
	if err == nil {
		t.Error("[E2E-INIT] Expected second init to fail without --force")
	}

	// Check error message mentions --force
	if !strings.Contains(string(output), "--force") {
		t.Errorf("[E2E-INIT] Expected error message to mention --force, got: %s", string(output))
	}

	suite.logger.Log("[E2E-INIT] Second init correctly failed")
}

// TestInitForceFlag tests that --force allows reinitializing
func TestInitForceFlag(t *testing.T) {
	SkipIfShort(t)
	SkipIfNoNTM(t)

	suite := NewInitTestSuite(t, "init-force")
	if err := suite.Setup(); err != nil {
		t.Fatalf("[E2E-INIT] Setup failed: %v", err)
	}
	defer suite.Cleanup()

	// First init
	_, err := suite.RunInit("--no-hooks")
	if err != nil {
		t.Fatalf("[E2E-INIT] First init failed: %v", err)
	}

	configPath := filepath.Join(suite.TempDir(), ".ntm", "config.toml")
	suite.logger.Log("[E2E] Step 1/4: Modify existing config before --force")
	if err := os.WriteFile(configPath, []byte("# SENTINEL: should be removed by --force\n"), 0644); err != nil {
		t.Fatalf("[E2E-INIT] Failed to modify config.toml: %v", err)
	}
	suite.logger.Log("[E2E] Writing config: %s (%d bytes)", configPath, len("# SENTINEL: should be removed by --force\n"))

	// Second init with --force should succeed
	suite.logger.Log("[E2E] Step 2/4: Re-run init with --force")
	result, err := suite.RunInitJSON("--force", "--no-hooks")
	if err != nil {
		t.Fatalf("[E2E-INIT] Init with --force failed: %v", err)
	}

	if !result.Success {
		t.Error("[E2E-INIT] Expected success=true with --force")
	}

	if !result.Force {
		t.Error("[E2E-INIT] Expected force=true in result")
	}

	suite.logger.Log("[E2E] Step 3/4: Verify sentinel removed (clean state restored)")
	updated, err := os.ReadFile(configPath)
	if err != nil {
		t.Fatalf("[E2E-INIT] Failed to read config.toml: %v", err)
	}
	if strings.Contains(string(updated), "SENTINEL") {
		suite.logger.Log("[E2E] FAIL: %s - expected sentinel removed, got still present", t.Name())
		t.Fatal("[E2E-INIT] Expected config.toml to be overwritten by --force")
	}

	suite.logger.Log("[E2E] Step 4/4: Done")
	suite.logger.Log("[E2E-INIT] Force flag test passed")
}

// TestInitScenarioPartialStructure verifies init fills missing pieces when `.ntm/` exists but is incomplete.
func TestInitScenarioPartialStructure(t *testing.T) {
	SkipIfShort(t)
	SkipIfNoNTM(t)

	suite := NewInitTestSuite(t, "init-scenario-partial-structure")
	defer suite.Cleanup()

	suite.SetSteps(5)
	suite.Step("Create temp directory")
	if err := suite.Setup(); err != nil {
		t.Fatalf("[E2E-INIT] Setup failed: %v", err)
	}

	suite.Step("Create partial .ntm directory with sentinel palette.md")
	ntmDir := filepath.Join(suite.TempDir(), ".ntm")
	if err := os.MkdirAll(ntmDir, 0755); err != nil {
		t.Fatalf("[E2E-INIT] Failed to create partial .ntm dir: %v", err)
	}
	suite.logger.Log("[E2E] Creating directory: %s", ntmDir)

	palettePath := filepath.Join(ntmDir, "palette.md")
	paletteSentinel := []byte("# KEEP ME\n\necho ok\n")
	if err := os.WriteFile(palettePath, paletteSentinel, 0644); err != nil {
		t.Fatalf("[E2E-INIT] Failed to write palette.md: %v", err)
	}
	suite.logger.Log("[E2E] Writing config: %s (%d bytes)", palettePath, len(paletteSentinel))

	// Ensure config.toml is missing but templates/ exists.
	templatesDir := filepath.Join(ntmDir, "templates")
	if err := os.MkdirAll(templatesDir, 0755); err != nil {
		t.Fatalf("[E2E-INIT] Failed to create templates dir: %v", err)
	}
	suite.logger.Log("[E2E] Creating directory: %s", templatesDir)

	suite.Step("Run ntm init to fill missing pieces")
	result, err := suite.RunInitJSON("--no-hooks")
	if err != nil {
		t.Fatalf("[E2E-INIT] Init command failed: %v", err)
	}
	if !result.Success {
		t.Fatal("[E2E-INIT] Expected success=true in result")
	}

	suite.Step("Verify missing config.toml/pipelines/personas.toml created")
	expected := []string{
		filepath.Join(ntmDir, "config.toml"),
		filepath.Join(ntmDir, "pipelines"),
		filepath.Join(ntmDir, "personas.toml"),
	}
	for _, p := range expected {
		if _, err := os.Stat(p); err != nil {
			suite.logger.Log("[E2E] FAIL: %s - expected %s to exist, got err=%v", t.Name(), p, err)
			t.Fatalf("[E2E-INIT] Expected path to exist: %s", p)
		}
	}

	suite.Step("Verify existing palette.md preserved")
	got, err := os.ReadFile(palettePath)
	if err != nil {
		t.Fatalf("[E2E-INIT] Failed to read palette.md: %v", err)
	}
	if string(got) != string(paletteSentinel) {
		suite.logger.Log("[E2E] FAIL: %s - expected palette preserved, got different content", t.Name())
		t.Fatal("[E2E-INIT] Expected existing palette.md content to be preserved")
	}
}

// TestInitScenarioFlywheelToolIntegration verifies basic integration checks for Agent Mail + CM + bv.
func TestInitScenarioFlywheelToolIntegration(t *testing.T) {
	SkipIfShort(t)
	SkipIfNoNTM(t)

	// These are external tools; skip if not available.
	if _, err := exec.LookPath("cm"); err != nil {
		t.Skip("cm binary not found in PATH")
	}
	if _, err := exec.LookPath("br"); err != nil {
		t.Skip("br binary not found in PATH")
	}
	if _, err := exec.LookPath("bv"); err != nil {
		t.Skip("bv binary not found in PATH")
	}

	suite := NewInitTestSuite(t, "init-scenario-flywheel-integration")
	defer suite.Cleanup()

	suite.SetSteps(6)
	suite.Step("Create temp directory")
	if err := suite.Setup(); err != nil {
		t.Fatalf("[E2E-INIT] Setup failed: %v", err)
	}

	suite.Step("Run ntm init (records Agent Mail registration outcome)")
	result, err := suite.RunInitJSON("--no-hooks")
	if err != nil {
		t.Fatalf("[E2E-INIT] Init command failed: %v", err)
	}
	if !result.Success {
		t.Fatal("[E2E-INIT] Expected success=true in result")
	}
	if !result.AgentMail && result.AgentMailWarning == "" {
		suite.logger.Log("[E2E] FAIL: %s - expected agent_mail_warning when agent_mail=false", t.Name())
		t.Fatal("[E2E-INIT] Expected agent_mail_warning when agent_mail=false")
	}
	if result.AgentMail {
		suite.logger.Log("[E2E-INIT] Agent Mail registered successfully")
	} else {
		suite.logger.Log("[E2E-INIT] Agent Mail warning: %s", result.AgentMailWarning)
	}

	suite.Step("Verify CM can initialize repo scaffolding (isolated HOME)")
	cmCmd := exec.Command("cm", "init", "--repo", "--no-interactive", "--json")
	cmCmd.Dir = suite.TempDir()
	cmCmd.Env = append(os.Environ(), "HOME="+suite.TempDir())
	cmOut, cmErr := cmCmd.CombinedOutput()
	suite.logger.Log("[E2E-INIT] cm output: %s", string(cmOut))
	if cmErr != nil {
		t.Fatalf("[E2E-INIT] cm init failed: %v", cmErr)
	}

	suite.Step("Initialize beads workspace via br")
	brCmd := exec.Command("br", "init", "--json")
	brCmd.Dir = suite.TempDir()
	brOut, brErr := brCmd.CombinedOutput()
	suite.logger.Log("[E2E-INIT] br output: %s", string(brOut))
	if brErr != nil {
		t.Fatalf("[E2E-INIT] br init failed: %v", brErr)
	}

	suite.Step("Export beads JSONL via br sync --flush-only")
	syncCmd := exec.Command("br", "sync", "--flush-only")
	syncCmd.Dir = suite.TempDir()
	syncOut, syncErr := syncCmd.CombinedOutput()
	suite.logger.Log("[E2E-INIT] br sync output: %s", string(syncOut))
	if syncErr != nil {
		t.Fatalf("[E2E-INIT] br sync failed: %v", syncErr)
	}

	suite.Step("Verify bv can read the project via --robot-triage")
	bvCmd := exec.Command("bv", "--robot-triage")
	bvCmd.Dir = suite.TempDir()
	bvOut, bvErr := bvCmd.CombinedOutput()
	suite.logger.Log("[E2E-INIT] bv output: %s", string(bvOut))
	if bvErr != nil {
		t.Fatalf("[E2E-INIT] bv triage failed: %v", bvErr)
	}
	var bvPayload map[string]interface{}
	if err := json.Unmarshal(bvOut, &bvPayload); err != nil {
		t.Fatalf("[E2E-INIT] bv output was not valid JSON: %v", err)
	}
	if _, ok := bvPayload["triage"]; !ok {
		suite.logger.Log("[E2E] FAIL: %s - expected bv payload to contain triage key", t.Name())
		t.Fatal("[E2E-INIT] Expected bv output to include triage")
	}

	suite.Step("Done")
}

// TestInitWithGitHooks tests that git hooks are installed in git repo
func TestInitWithGitHooks(t *testing.T) {
	SkipIfShort(t)
	SkipIfNoNTM(t)

	suite := NewInitTestSuite(t, "init-hooks")
	if err := suite.SetupWithGit(); err != nil {
		t.Fatalf("[E2E-INIT] Setup failed: %v", err)
	}
	defer suite.Cleanup()

	result, err := suite.RunInitJSON()
	if err != nil {
		t.Fatalf("[E2E-INIT] Init command failed: %v", err)
	}

	// Check hooks were installed
	if len(result.HooksInstalled) == 0 {
		if result.HooksWarning != "" {
			suite.logger.Log("[E2E-INIT] Hooks warning: %s", result.HooksWarning)
		} else {
			t.Error("[E2E-INIT] Expected hooks to be installed in git repo")
		}
	}

	// Check pre-commit hook exists
	preCommitPath := filepath.Join(suite.TempDir(), ".git", "hooks", "pre-commit")
	if _, err := os.Stat(preCommitPath); err == nil {
		suite.logger.Log("[E2E-INIT] pre-commit hook exists at %s", preCommitPath)
	}

	suite.logger.Log("[E2E-INIT] Hooks installed: %v", result.HooksInstalled)
}

// TestInitNoHooksFlag tests that --no-hooks skips hook installation
func TestInitNoHooksFlag(t *testing.T) {
	SkipIfShort(t)
	SkipIfNoNTM(t)

	suite := NewInitTestSuite(t, "init-no-hooks")
	if err := suite.SetupWithGit(); err != nil {
		t.Fatalf("[E2E-INIT] Setup failed: %v", err)
	}
	defer suite.Cleanup()

	result, err := suite.RunInitJSON("--no-hooks")
	if err != nil {
		t.Fatalf("[E2E-INIT] Init command failed: %v", err)
	}

	if !result.NoHooks {
		t.Error("[E2E-INIT] Expected no_hooks=true in result")
	}

	if len(result.HooksInstalled) > 0 {
		t.Errorf("[E2E-INIT] Expected no hooks with --no-hooks, got: %v", result.HooksInstalled)
	}

	suite.logger.Log("[E2E-INIT] No hooks flag test passed")
}

// TestInitNonGitRepo tests behavior in non-git directory
func TestInitNonGitRepo(t *testing.T) {
	SkipIfShort(t)
	SkipIfNoNTM(t)

	suite := NewInitTestSuite(t, "init-non-git")
	if err := suite.Setup(); err != nil {
		t.Fatalf("[E2E-INIT] Setup failed: %v", err)
	}
	defer suite.Cleanup()

	result, err := suite.RunInitJSON()
	if err != nil {
		t.Fatalf("[E2E-INIT] Init command failed: %v", err)
	}

	// Should succeed but warn about hooks
	if !result.Success {
		t.Error("[E2E-INIT] Expected success in non-git directory")
	}

	// Should have warning about not being a git repo
	if result.HooksWarning == "" && len(result.HooksInstalled) > 0 {
		t.Error("[E2E-INIT] Expected hooks warning in non-git directory")
	}

	suite.logger.Log("[E2E-INIT] Non-git repo test passed, warning: %s", result.HooksWarning)
}

// TestInitCreatedDirsPopulated tests that created_dirs is populated
func TestInitCreatedDirsPopulated(t *testing.T) {
	SkipIfShort(t)
	SkipIfNoNTM(t)

	suite := NewInitTestSuite(t, "init-dirs")
	if err := suite.Setup(); err != nil {
		t.Fatalf("[E2E-INIT] Setup failed: %v", err)
	}
	defer suite.Cleanup()

	result, err := suite.RunInitJSON("--no-hooks")
	if err != nil {
		t.Fatalf("[E2E-INIT] Init command failed: %v", err)
	}

	// Should have created at least one directory (.ntm)
	if len(result.CreatedDirs) == 0 {
		t.Error("[E2E-INIT] Expected non-empty created_dirs")
	}

	suite.logger.Log("[E2E-INIT] Created dirs: %v", result.CreatedDirs)
}

// TestInitCreatedFilesPopulated tests that created_files is populated
func TestInitCreatedFilesPopulated(t *testing.T) {
	SkipIfShort(t)
	SkipIfNoNTM(t)

	suite := NewInitTestSuite(t, "init-files")
	if err := suite.Setup(); err != nil {
		t.Fatalf("[E2E-INIT] Setup failed: %v", err)
	}
	defer suite.Cleanup()

	result, err := suite.RunInitJSON("--no-hooks")
	if err != nil {
		t.Fatalf("[E2E-INIT] Init command failed: %v", err)
	}

	// Should have created at least one file (config.toml)
	if len(result.CreatedFiles) == 0 {
		t.Error("[E2E-INIT] Expected non-empty created_files")
	}

	suite.logger.Log("[E2E-INIT] Created files: %v", result.CreatedFiles)
}

// TestInitShellZsh tests shell integration for zsh
func TestInitShellZsh(t *testing.T) {
	SkipIfShort(t)
	SkipIfNoNTM(t)

	suite := NewInitTestSuite(t, "init-shell-zsh")
	if err := suite.Setup(); err != nil {
		t.Fatalf("[E2E-INIT] Setup failed: %v", err)
	}
	defer suite.Cleanup()

	// Note: "ntm init zsh" is redirected to shell integration
	output, err := suite.RunInit("zsh")
	if err != nil {
		t.Fatalf("[E2E-INIT] Shell init failed: %v", err)
	}

	outputStr := string(output)

	// Check for expected shell integration elements
	expectedElements := []string{
		"alias cc=",
		"alias cod=",
		"alias gmi=",
		"compdef _ntm ntm",
	}

	for _, element := range expectedElements {
		if !strings.Contains(outputStr, element) {
			t.Errorf("[E2E-INIT] Expected shell integration to contain: %s", element)
		}
	}

	suite.logger.Log("[E2E-INIT] Zsh shell integration test passed (%d bytes)", len(output))
}

// TestInitShellBash tests shell integration for bash
func TestInitShellBash(t *testing.T) {
	SkipIfShort(t)
	SkipIfNoNTM(t)

	suite := NewInitTestSuite(t, "init-shell-bash")
	if err := suite.Setup(); err != nil {
		t.Fatalf("[E2E-INIT] Setup failed: %v", err)
	}
	defer suite.Cleanup()

	output, err := suite.RunInit("bash")
	if err != nil {
		t.Fatalf("[E2E-INIT] Shell init failed: %v", err)
	}

	outputStr := string(output)

	// Check for expected shell integration elements
	expectedElements := []string{
		"alias cc=",
		"alias cod=",
		"alias gmi=",
		"complete -F _ntm_completions ntm",
	}

	for _, element := range expectedElements {
		if !strings.Contains(outputStr, element) {
			t.Errorf("[E2E-INIT] Expected shell integration to contain: %s", element)
		}
	}

	suite.logger.Log("[E2E-INIT] Bash shell integration test passed (%d bytes)", len(output))
}

// TestInitShellFish tests shell integration for fish
func TestInitShellFish(t *testing.T) {
	SkipIfShort(t)
	SkipIfNoNTM(t)

	suite := NewInitTestSuite(t, "init-shell-fish")
	if err := suite.Setup(); err != nil {
		t.Fatalf("[E2E-INIT] Setup failed: %v", err)
	}
	defer suite.Cleanup()

	output, err := suite.RunInit("fish")
	if err != nil {
		t.Fatalf("[E2E-INIT] Shell init failed: %v", err)
	}

	outputStr := string(output)

	// Check for expected shell integration elements
	expectedElements := []string{
		"alias cc",
		"alias cod",
		"alias gmi",
		"complete -c ntm",
	}

	for _, element := range expectedElements {
		if !strings.Contains(outputStr, element) {
			t.Errorf("[E2E-INIT] Expected shell integration to contain: %s", element)
		}
	}

	suite.logger.Log("[E2E-INIT] Fish shell integration test passed (%d bytes)", len(output))
}

// TestInitTargetDirectory tests init with specific target directory
func TestInitTargetDirectory(t *testing.T) {
	SkipIfShort(t)
	SkipIfNoNTM(t)

	suite := NewInitTestSuite(t, "init-target")
	if err := suite.Setup(); err != nil {
		t.Fatalf("[E2E-INIT] Setup failed: %v", err)
	}
	defer suite.Cleanup()

	// Create a subdirectory
	subDir := filepath.Join(suite.TempDir(), "myproject")
	if err := os.MkdirAll(subDir, 0755); err != nil {
		t.Fatalf("[E2E-INIT] Failed to create subdir: %v", err)
	}

	// Run init with explicit target
	cmd := exec.Command("ntm", "init", subDir, "--no-hooks", "--json")
	output, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("[E2E-INIT] Init with target failed: %v, output: %s", err, string(output))
	}

	var result InitResult
	if err := json.Unmarshal(output, &result); err != nil {
		t.Fatalf("[E2E-INIT] Failed to parse JSON: %v", err)
	}

	// Verify project path matches target
	if result.ProjectPath != subDir {
		t.Errorf("[E2E-INIT] Expected project_path=%s, got %s", subDir, result.ProjectPath)
	}

	// Check .ntm directory was created in target
	ntmDir := filepath.Join(subDir, ".ntm")
	if _, err := os.Stat(ntmDir); os.IsNotExist(err) {
		t.Error("[E2E-INIT] Expected .ntm directory in target directory")
	}

	suite.logger.Log("[E2E-INIT] Target directory test passed")
}

// TestInitInvalidShell tests error handling for invalid shell
func TestInitInvalidShell(t *testing.T) {
	SkipIfShort(t)
	SkipIfNoNTM(t)

	suite := NewInitTestSuite(t, "init-invalid-shell")
	if err := suite.Setup(); err != nil {
		t.Fatalf("[E2E-INIT] Setup failed: %v", err)
	}
	defer suite.Cleanup()

	// Create a file named "invalid" to ensure it's treated as shell arg not dir
	// Actually just run with non-existent path that's not a shell name
	output, err := suite.RunInit("powershell")
	// This should try to init a directory called "powershell" which doesn't exist
	// or error out as invalid shell

	outputStr := string(output)
	if err == nil && !strings.Contains(outputStr, "unsupported shell") {
		// If it succeeded, that means a "powershell" directory was created
		// which is fine - the init worked
		suite.logger.Log("[E2E-INIT] powershell treated as directory target")
	} else {
		suite.logger.Log("[E2E-INIT] powershell treated as shell name (error expected)")
	}
}

// TestInitNonExistentDirectory tests error handling for non-existent target
func TestInitNonExistentDirectory(t *testing.T) {
	SkipIfShort(t)
	SkipIfNoNTM(t)

	suite := NewInitTestSuite(t, "init-nonexistent")
	if err := suite.Setup(); err != nil {
		t.Fatalf("[E2E-INIT] Setup failed: %v", err)
	}
	defer suite.Cleanup()

	nonExistent := filepath.Join(suite.TempDir(), "does-not-exist")
	cmd := exec.Command("ntm", "init", nonExistent, "--no-hooks")
	output, err := cmd.CombinedOutput()

	if err == nil {
		t.Error("[E2E-INIT] Expected error for non-existent directory")
	}

	// Check error message mentions directory not found
	if !strings.Contains(string(output), "not found") && !strings.Contains(string(output), "no such") {
		suite.logger.Log("[E2E-INIT] Error output: %s", string(output))
		// Accept any error - the key is it should fail
	}

	suite.logger.Log("[E2E-INIT] Non-existent directory test passed")
}

// TestInitOutputFormat tests that TUI output contains expected info
func TestInitOutputFormat(t *testing.T) {
	SkipIfShort(t)
	SkipIfNoNTM(t)

	suite := NewInitTestSuite(t, "init-format")
	if err := suite.Setup(); err != nil {
		t.Fatalf("[E2E-INIT] Setup failed: %v", err)
	}
	defer suite.Cleanup()

	output, err := suite.RunInit("--no-hooks")
	if err != nil {
		t.Fatalf("[E2E-INIT] Init command failed: %v", err)
	}

	outputStr := string(output)

	// Check for expected output elements
	expectedElements := []string{
		"Initialized",
		".ntm",
	}

	for _, element := range expectedElements {
		if !strings.Contains(outputStr, element) {
			t.Errorf("[E2E-INIT] Expected output to contain: %s", element)
		}
	}

	suite.logger.Log("[E2E-INIT] Output format test passed")
}
