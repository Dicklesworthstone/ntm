package config

import (
	"os"
	"path/filepath"
	"testing"
)

// =============================================================================
// GetValue tests (26.7% → target >80%)
// =============================================================================

func TestGetValue_NilConfig(t *testing.T) {
	t.Parallel()
	_, err := GetValue(nil, "theme")
	if err == nil {
		t.Error("GetValue(nil, ...) should return error")
	}
}

func TestGetValue_EmptyPath(t *testing.T) {
	t.Parallel()
	cfg := Default()
	_, err := GetValue(cfg, "")
	if err == nil {
		t.Error("GetValue(cfg, \"\") should return error")
	}
}

func TestGetValue_UnknownRoot(t *testing.T) {
	t.Parallel()
	cfg := Default()
	_, err := GetValue(cfg, "nonexistent")
	if err == nil {
		t.Error("GetValue with unknown root should return error")
	}
}

func TestGetValue_TopLevelScalars(t *testing.T) {
	t.Parallel()
	cfg := Default()

	tests := []struct {
		path string
	}{
		{"projects_base"},
		{"theme"},
	}
	for _, tt := range tests {
		t.Run(tt.path, func(t *testing.T) {
			t.Parallel()
			_, err := GetValue(cfg, tt.path)
			if err != nil {
				t.Errorf("GetValue(%q) error = %v", tt.path, err)
			}
		})
	}
}

func TestGetValue_Agents(t *testing.T) {
	t.Parallel()
	cfg := Default()

	tests := []struct {
		path string
	}{
		{"agents"},
		{"agents.claude"},
		{"agents.codex"},
		{"agents.gemini"},
	}
	for _, tt := range tests {
		t.Run(tt.path, func(t *testing.T) {
			t.Parallel()
			val, err := GetValue(cfg, tt.path)
			if err != nil {
				t.Errorf("GetValue(%q) error = %v", tt.path, err)
			}
			if val == nil {
				t.Errorf("GetValue(%q) = nil", tt.path)
			}
		})
	}
}

func TestGetValue_Tmux(t *testing.T) {
	t.Parallel()
	cfg := Default()

	tests := []struct {
		path string
	}{
		{"tmux"},
		{"tmux.default_panes"},
		{"tmux.palette_key"},
		{"tmux.pane_init_delay_ms"},
	}
	for _, tt := range tests {
		t.Run(tt.path, func(t *testing.T) {
			t.Parallel()
			_, err := GetValue(cfg, tt.path)
			if err != nil {
				t.Errorf("GetValue(%q) error = %v", tt.path, err)
			}
		})
	}
}

func TestGetValue_AgentMail(t *testing.T) {
	t.Parallel()
	cfg := Default()

	tests := []struct {
		path    string
		wantStr string // if non-empty, check string value
	}{
		{"agent_mail", ""},
		{"agent_mail.enabled", ""},
		{"agent_mail.url", ""},
		{"agent_mail.token", "[redacted]"},
		{"agent_mail.auto_register", ""},
	}
	for _, tt := range tests {
		t.Run(tt.path, func(t *testing.T) {
			t.Parallel()
			val, err := GetValue(cfg, tt.path)
			if err != nil {
				t.Errorf("GetValue(%q) error = %v", tt.path, err)
			}
			if tt.wantStr != "" {
				if s, ok := val.(string); !ok || s != tt.wantStr {
					t.Errorf("GetValue(%q) = %v, want %q", tt.path, val, tt.wantStr)
				}
			}
		})
	}
}

func TestGetValue_Integrations(t *testing.T) {
	t.Parallel()
	cfg := Default()

	tests := []struct {
		path string
	}{
		{"integrations"},
		{"integrations.dcg"},
		{"integrations.dcg.enabled"},
		{"integrations.dcg.binary_path"},
		{"integrations.dcg.custom_blocklist"},
		{"integrations.dcg.custom_whitelist"},
		{"integrations.dcg.audit_log"},
		{"integrations.dcg.allow_override"},
	}
	for _, tt := range tests {
		t.Run(tt.path, func(t *testing.T) {
			t.Parallel()
			_, err := GetValue(cfg, tt.path)
			if err != nil {
				t.Errorf("GetValue(%q) error = %v", tt.path, err)
			}
		})
	}
}

func TestGetValue_Alerts(t *testing.T) {
	t.Parallel()
	cfg := Default()

	tests := []struct {
		path string
	}{
		{"alerts"},
		{"alerts.enabled"},
		{"alerts.agent_stuck_minutes"},
		{"alerts.disk_low_threshold_gb"},
		{"alerts.mail_backlog_threshold"},
		{"alerts.bead_stale_hours"},
		{"alerts.context_warning_threshold"},
		{"alerts.resolved_prune_minutes"},
	}
	for _, tt := range tests {
		t.Run(tt.path, func(t *testing.T) {
			t.Parallel()
			_, err := GetValue(cfg, tt.path)
			if err != nil {
				t.Errorf("GetValue(%q) error = %v", tt.path, err)
			}
		})
	}
}

func TestGetValue_Checkpoints(t *testing.T) {
	t.Parallel()
	cfg := Default()

	tests := []struct {
		path string
	}{
		{"checkpoints"},
		{"checkpoints.enabled"},
		{"checkpoints.before_broadcast"},
		{"checkpoints.max_auto_checkpoints"},
	}
	for _, tt := range tests {
		t.Run(tt.path, func(t *testing.T) {
			t.Parallel()
			_, err := GetValue(cfg, tt.path)
			if err != nil {
				t.Errorf("GetValue(%q) error = %v", tt.path, err)
			}
		})
	}
}

func TestGetValue_Resilience(t *testing.T) {
	t.Parallel()
	cfg := Default()

	tests := []struct {
		path string
	}{
		{"resilience"},
		{"resilience.auto_restart"},
		{"resilience.max_restarts"},
	}
	for _, tt := range tests {
		t.Run(tt.path, func(t *testing.T) {
			t.Parallel()
			_, err := GetValue(cfg, tt.path)
			if err != nil {
				t.Errorf("GetValue(%q) error = %v", tt.path, err)
			}
		})
	}
}

func TestGetValue_ContextRotation(t *testing.T) {
	t.Parallel()
	cfg := Default()

	tests := []struct {
		path string
	}{
		{"context_rotation"},
		{"context_rotation.enabled"},
		{"context_rotation.warning_threshold"},
		{"context_rotation.rotate_threshold"},
	}
	for _, tt := range tests {
		t.Run(tt.path, func(t *testing.T) {
			t.Parallel()
			_, err := GetValue(cfg, tt.path)
			if err != nil {
				t.Errorf("GetValue(%q) error = %v", tt.path, err)
			}
		})
	}
}

func TestGetValue_Context(t *testing.T) {
	t.Parallel()
	cfg := Default()

	tests := []struct {
		path string
	}{
		{"context"},
		{"context.ms_skills"},
	}
	for _, tt := range tests {
		t.Run(tt.path, func(t *testing.T) {
			t.Parallel()
			_, err := GetValue(cfg, tt.path)
			if err != nil {
				t.Errorf("GetValue(%q) error = %v", tt.path, err)
			}
		})
	}
}

func TestGetValue_Ensemble(t *testing.T) {
	t.Parallel()
	cfg := Default()

	tests := []struct {
		path string
	}{
		{"ensemble"},
		{"ensemble.default_ensemble"},
		{"ensemble.agent_mix"},
		{"ensemble.assignment"},
		{"ensemble.mode_tier_default"},
		{"ensemble.allow_advanced"},
		// Nested: synthesis
		{"ensemble.synthesis"},
		{"ensemble.synthesis.strategy"},
		{"ensemble.synthesis.min_confidence"},
		{"ensemble.synthesis.max_findings"},
		{"ensemble.synthesis.include_raw_outputs"},
		{"ensemble.synthesis.conflict_resolution"},
		// Nested: cache
		{"ensemble.cache"},
		{"ensemble.cache.enabled"},
		{"ensemble.cache.ttl_minutes"},
		{"ensemble.cache.cache_dir"},
		{"ensemble.cache.max_entries"},
		{"ensemble.cache.share_across_modes"},
		// Nested: budget
		{"ensemble.budget"},
		{"ensemble.budget.per_agent"},
		{"ensemble.budget.total"},
		{"ensemble.budget.synthesis"},
		{"ensemble.budget.context_pack"},
		// Nested: early_stop
		{"ensemble.early_stop"},
		{"ensemble.early_stop.enabled"},
		{"ensemble.early_stop.min_agents"},
		{"ensemble.early_stop.findings_threshold"},
		{"ensemble.early_stop.similarity_threshold"},
		{"ensemble.early_stop.window_size"},
	}
	for _, tt := range tests {
		t.Run(tt.path, func(t *testing.T) {
			t.Parallel()
			_, err := GetValue(cfg, tt.path)
			if err != nil {
				t.Errorf("GetValue(%q) error = %v", tt.path, err)
			}
		})
	}
}

func TestGetValue_CASS(t *testing.T) {
	t.Parallel()
	cfg := Default()

	tests := []struct {
		path string
	}{
		{"cass"},
		{"cass.enabled"},
		{"cass.timeout"},
		{"cass.context"},
		{"cass.context.enabled"},
		{"cass.context.max_sessions"},
		{"cass.context.lookback_days"},
		{"cass.context.max_tokens"},
		{"cass.context.min_relevance"},
		{"cass.context.skip_if_context_above"},
		{"cass.context.prefer_same_project"},
	}
	for _, tt := range tests {
		t.Run(tt.path, func(t *testing.T) {
			t.Parallel()
			_, err := GetValue(cfg, tt.path)
			if err != nil {
				t.Errorf("GetValue(%q) error = %v", tt.path, err)
			}
		})
	}
}

func TestGetValue_Health(t *testing.T) {
	t.Parallel()
	cfg := Default()

	tests := []struct {
		path string
	}{
		{"health"},
		{"health.enabled"},
		{"health.check_interval"},
		{"health.stall_threshold"},
		{"health.auto_restart"},
		{"health.max_restarts"},
		{"health.restart_backoff_base"},
		{"health.restart_backoff_max"},
	}
	for _, tt := range tests {
		t.Run(tt.path, func(t *testing.T) {
			t.Parallel()
			_, err := GetValue(cfg, tt.path)
			if err != nil {
				t.Errorf("GetValue(%q) error = %v", tt.path, err)
			}
		})
	}
}

func TestGetValue_UnknownSubPath(t *testing.T) {
	t.Parallel()
	cfg := Default()

	tests := []struct {
		path string
	}{
		{"agents.unknown"},
		{"tmux.unknown"},
		{"agent_mail.unknown"},
		{"alerts.unknown"},
		{"checkpoints.unknown"},
		{"resilience.unknown"},
		{"context_rotation.unknown"},
		{"context.unknown"},
		{"ensemble.unknown"},
		{"ensemble.synthesis.unknown"},
		{"ensemble.cache.unknown"},
		{"ensemble.budget.unknown"},
		{"ensemble.early_stop.unknown"},
		{"cass.unknown"},
		{"cass.context.unknown"},
		{"health.unknown"},
		{"integrations.unknown"},
		{"integrations.dcg.unknown"},
	}
	for _, tt := range tests {
		t.Run(tt.path, func(t *testing.T) {
			t.Parallel()
			_, err := GetValue(cfg, tt.path)
			if err == nil {
				t.Errorf("GetValue(%q) should return error for unknown sub-path", tt.path)
			}
		})
	}
}

// =============================================================================
// ValidateDCGConfig tests (40% → target >80%)
// =============================================================================

func TestValidateDCGConfig_Nil(t *testing.T) {
	t.Parallel()
	if err := ValidateDCGConfig(nil); err != nil {
		t.Errorf("ValidateDCGConfig(nil) = %v, want nil", err)
	}
}

func TestValidateDCGConfig_Empty(t *testing.T) {
	t.Parallel()
	if err := ValidateDCGConfig(&DCGConfig{}); err != nil {
		t.Errorf("ValidateDCGConfig(empty) = %v, want nil", err)
	}
}

func TestValidateDCGConfig_BinaryPathNotExists(t *testing.T) {
	t.Parallel()
	cfg := &DCGConfig{BinaryPath: "/nonexistent/path/to/binary"}
	err := ValidateDCGConfig(cfg)
	if err == nil {
		t.Error("ValidateDCGConfig with nonexistent binary should return error")
	}
}

func TestValidateDCGConfig_BinaryPathIsDir(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	cfg := &DCGConfig{BinaryPath: dir}
	err := ValidateDCGConfig(cfg)
	if err == nil {
		t.Error("ValidateDCGConfig with directory as binary should return error")
	}
}

func TestValidateDCGConfig_ValidBinaryPath(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	binPath := filepath.Join(dir, "dcg")
	if err := os.WriteFile(binPath, []byte("#!/bin/sh"), 0o755); err != nil {
		t.Fatal(err)
	}
	cfg := &DCGConfig{BinaryPath: binPath}
	if err := ValidateDCGConfig(cfg); err != nil {
		t.Errorf("ValidateDCGConfig with valid binary = %v", err)
	}
}

func TestValidateDCGConfig_AuditLogDirNotExists(t *testing.T) {
	t.Parallel()
	cfg := &DCGConfig{AuditLog: "/nonexistent/dir/audit.log"}
	err := ValidateDCGConfig(cfg)
	if err == nil {
		t.Error("ValidateDCGConfig with nonexistent audit dir should return error")
	}
}

func TestValidateDCGConfig_AuditLogParentIsFile(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	// Create a regular file where the parent should be a directory
	fakeDirPath := filepath.Join(dir, "notadir")
	if err := os.WriteFile(fakeDirPath, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	cfg := &DCGConfig{AuditLog: filepath.Join(fakeDirPath, "audit.log")}
	err := ValidateDCGConfig(cfg)
	if err == nil {
		t.Error("ValidateDCGConfig with file as audit parent should return error")
	}
}

func TestValidateDCGConfig_ValidAuditLog(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	cfg := &DCGConfig{AuditLog: filepath.Join(dir, "audit.log")}
	if err := ValidateDCGConfig(cfg); err != nil {
		t.Errorf("ValidateDCGConfig with valid audit dir = %v", err)
	}
}

// =============================================================================
// ValidateRanoConfig tests (62.5% → target >90%)
// =============================================================================

func TestValidateRanoConfig_Nil(t *testing.T) {
	t.Parallel()
	if err := ValidateRanoConfig(nil); err != nil {
		t.Errorf("ValidateRanoConfig(nil) = %v, want nil", err)
	}
}

func TestValidateRanoConfig_Unconfigured(t *testing.T) {
	t.Parallel()
	// All zero values → skip validation
	cfg := &RanoConfig{}
	if err := ValidateRanoConfig(cfg); err != nil {
		t.Errorf("ValidateRanoConfig(zero) = %v, want nil", err)
	}
}

func TestValidateRanoConfig_PollIntervalBoundary(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		ms      int
		wantErr bool
	}{
		{"99ms too low", 99, true},
		{"100ms boundary", 100, false},
		{"1000ms ok", 1000, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			cfg := &RanoConfig{
				Enabled:        true,
				PollIntervalMs: tt.ms,
			}
			err := ValidateRanoConfig(cfg)
			if (err != nil) != tt.wantErr {
				t.Errorf("ValidateRanoConfig(PollIntervalMs=%d) error = %v, wantErr %v", tt.ms, err, tt.wantErr)
			}
		})
	}
}

func TestValidateRanoConfig_HistoryDaysBoundary(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		days    int
		wantErr bool
	}{
		{"negative", -1, true},
		{"zero boundary", 0, false},
		{"positive", 7, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			cfg := &RanoConfig{
				Enabled:        true,
				PollIntervalMs: 200,
				HistoryDays:    tt.days,
			}
			err := ValidateRanoConfig(cfg)
			if (err != nil) != tt.wantErr {
				t.Errorf("ValidateRanoConfig(HistoryDays=%d) error = %v, wantErr %v", tt.days, err, tt.wantErr)
			}
		})
	}
}

func TestValidateRanoConfig_BinaryPathNotExists(t *testing.T) {
	t.Parallel()
	cfg := &RanoConfig{
		Enabled:        true,
		BinaryPath:     "/nonexistent/rano",
		PollIntervalMs: 200,
	}
	err := ValidateRanoConfig(cfg)
	if err == nil {
		t.Error("ValidateRanoConfig with nonexistent binary should return error")
	}
}

func TestValidateRanoConfig_BinaryPathIsDir(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	cfg := &RanoConfig{
		Enabled:        true,
		BinaryPath:     dir,
		PollIntervalMs: 200,
	}
	err := ValidateRanoConfig(cfg)
	if err == nil {
		t.Error("ValidateRanoConfig with directory binary should return error")
	}
}

// =============================================================================
// applyEnvOverrides tests (46.2% → target >80%)
// =============================================================================

func TestApplyEnvOverrides_UBSPath(t *testing.T) {
	t.Setenv("UBS_PATH", "/custom/ubs")
	cfg := &ScannerConfig{}
	applyEnvOverrides(cfg)
	if cfg.UBSPath != "/custom/ubs" {
		t.Errorf("UBSPath = %q, want /custom/ubs", cfg.UBSPath)
	}
}

func TestApplyEnvOverrides_Timeout(t *testing.T) {
	t.Setenv("NTM_SCANNER_TIMEOUT", "120s")
	cfg := &ScannerConfig{}
	applyEnvOverrides(cfg)
	if cfg.Defaults.Timeout != "120s" {
		t.Errorf("Timeout = %q, want 120s", cfg.Defaults.Timeout)
	}
}

func TestApplyEnvOverrides_AutoBeads(t *testing.T) {
	tests := []struct {
		name string
		val  string
		want bool
	}{
		{"1", "1", true},
		{"true", "true", true},
		{"True", "True", true},
		{"0", "0", false},
		{"false", "false", false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Setenv("NTM_SCANNER_AUTO_BEADS", tt.val)
			cfg := &ScannerConfig{}
			applyEnvOverrides(cfg)
			if cfg.Beads.AutoCreate != tt.want {
				t.Errorf("AutoCreate with %q = %v, want %v", tt.val, cfg.Beads.AutoCreate, tt.want)
			}
		})
	}
}

func TestApplyEnvOverrides_MinSeverity(t *testing.T) {
	t.Setenv("NTM_SCANNER_MIN_SEVERITY", "critical")
	cfg := &ScannerConfig{}
	applyEnvOverrides(cfg)
	if cfg.Beads.MinSeverity != "critical" {
		t.Errorf("MinSeverity = %q, want critical", cfg.Beads.MinSeverity)
	}
}

func TestApplyEnvOverrides_BlockCritical(t *testing.T) {
	tests := []struct {
		name string
		val  string
		want bool
	}{
		{"1", "1", true},
		{"true", "true", true},
		{"0", "0", false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Setenv("NTM_SCANNER_BLOCK_CRITICAL", tt.val)
			cfg := &ScannerConfig{}
			applyEnvOverrides(cfg)
			if cfg.Thresholds.PreCommit.BlockCritical != tt.want {
				t.Errorf("BlockCritical with %q = %v, want %v", tt.val, cfg.Thresholds.PreCommit.BlockCritical, tt.want)
			}
		})
	}
}

func TestApplyEnvOverrides_FailErrors(t *testing.T) {
	tests := []struct {
		name string
		val  string
		want int
	}{
		{"valid int", "5", 5},
		{"zero", "0", 0},
		{"invalid", "abc", 0}, // silent failure keeps default
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Setenv("NTM_SCANNER_FAIL_ERRORS", tt.val)
			cfg := &ScannerConfig{}
			applyEnvOverrides(cfg)
			if cfg.Thresholds.CI.FailErrors != tt.want {
				t.Errorf("FailErrors with %q = %d, want %d", tt.val, cfg.Thresholds.CI.FailErrors, tt.want)
			}
		})
	}
}

func TestApplyEnvOverrides_NoEnvVars(t *testing.T) {
	// Ensure unset env vars don't change config
	t.Setenv("UBS_PATH", "")
	t.Setenv("NTM_SCANNER_TIMEOUT", "")
	t.Setenv("NTM_SCANNER_AUTO_BEADS", "")
	t.Setenv("NTM_SCANNER_MIN_SEVERITY", "")
	t.Setenv("NTM_SCANNER_BLOCK_CRITICAL", "")
	t.Setenv("NTM_SCANNER_FAIL_ERRORS", "")
	cfg := &ScannerConfig{
		UBSPath: "original",
	}
	applyEnvOverrides(cfg)
	if cfg.UBSPath != "original" {
		t.Errorf("UBSPath changed to %q when env was empty", cfg.UBSPath)
	}
}

func TestApplyEnvOverrides_AllAtOnce(t *testing.T) {
	t.Setenv("UBS_PATH", "/bin/ubs")
	t.Setenv("NTM_SCANNER_TIMEOUT", "60s")
	t.Setenv("NTM_SCANNER_AUTO_BEADS", "1")
	t.Setenv("NTM_SCANNER_MIN_SEVERITY", "error")
	t.Setenv("NTM_SCANNER_BLOCK_CRITICAL", "true")
	t.Setenv("NTM_SCANNER_FAIL_ERRORS", "3")

	cfg := &ScannerConfig{}
	applyEnvOverrides(cfg)

	if cfg.UBSPath != "/bin/ubs" {
		t.Errorf("UBSPath = %q", cfg.UBSPath)
	}
	if cfg.Defaults.Timeout != "60s" {
		t.Errorf("Timeout = %q", cfg.Defaults.Timeout)
	}
	if !cfg.Beads.AutoCreate {
		t.Error("AutoCreate should be true")
	}
	if cfg.Beads.MinSeverity != "error" {
		t.Errorf("MinSeverity = %q", cfg.Beads.MinSeverity)
	}
	if !cfg.Thresholds.PreCommit.BlockCritical {
		t.Error("BlockCritical should be true")
	}
	if cfg.Thresholds.CI.FailErrors != 3 {
		t.Errorf("FailErrors = %d", cfg.Thresholds.CI.FailErrors)
	}
}

// =============================================================================
// dirWritable tests
// =============================================================================

func TestDirWritable_Nil(t *testing.T) {
	t.Parallel()
	if dirWritable(nil) {
		t.Error("dirWritable(nil) should return false")
	}
}

func TestDirWritable_WritableDir(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	info, err := os.Stat(dir)
	if err != nil {
		t.Fatal(err)
	}
	if !dirWritable(info) {
		t.Error("dirWritable should return true for writable temp dir")
	}
}

// =============================================================================
// ValidateProcessTriageConfig tests (71.4% → target >90%)
// =============================================================================

func TestValidateProcessTriageConfig_Nil(t *testing.T) {
	t.Parallel()
	if err := ValidateProcessTriageConfig(nil); err != nil {
		t.Errorf("ValidateProcessTriageConfig(nil) = %v, want nil", err)
	}
}

func TestValidateProcessTriageConfig_Unconfigured(t *testing.T) {
	t.Parallel()
	// All zero values → skip validation
	cfg := &ProcessTriageConfig{}
	if err := ValidateProcessTriageConfig(cfg); err != nil {
		t.Errorf("ValidateProcessTriageConfig(zero) = %v, want nil", err)
	}
}

func TestValidateProcessTriageConfig_BinaryNotExists(t *testing.T) {
	t.Parallel()
	cfg := &ProcessTriageConfig{
		Enabled:        true,
		BinaryPath:     "/nonexistent/pt",
		CheckInterval:  10,
		IdleThreshold:  60,
		StuckThreshold: 120,
		OnStuck:        "alert",
	}
	err := ValidateProcessTriageConfig(cfg)
	if err == nil {
		t.Error("should error for nonexistent binary")
	}
}

func TestValidateProcessTriageConfig_BinaryIsDir(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	cfg := &ProcessTriageConfig{
		Enabled:        true,
		BinaryPath:     dir,
		CheckInterval:  10,
		IdleThreshold:  60,
		StuckThreshold: 120,
		OnStuck:        "alert",
	}
	err := ValidateProcessTriageConfig(cfg)
	if err == nil {
		t.Error("should error for directory as binary")
	}
}

func TestValidateProcessTriageConfig_CheckIntervalTooLow(t *testing.T) {
	t.Parallel()
	cfg := &ProcessTriageConfig{
		Enabled:        true,
		CheckInterval:  4, // below minimum of 5
		IdleThreshold:  60,
		StuckThreshold: 120,
		OnStuck:        "alert",
	}
	err := ValidateProcessTriageConfig(cfg)
	if err == nil {
		t.Error("should error for check_interval < 5")
	}
}

func TestValidateProcessTriageConfig_CheckIntervalBoundary(t *testing.T) {
	t.Parallel()
	cfg := &ProcessTriageConfig{
		Enabled:        true,
		CheckInterval:  5, // exactly at boundary
		IdleThreshold:  30,
		StuckThreshold: 60,
		OnStuck:        "alert",
	}
	if err := ValidateProcessTriageConfig(cfg); err != nil {
		t.Errorf("check_interval=5 should be valid: %v", err)
	}
}

func TestValidateProcessTriageConfig_IdleThresholdTooLow(t *testing.T) {
	t.Parallel()
	cfg := &ProcessTriageConfig{
		Enabled:        true,
		CheckInterval:  10,
		IdleThreshold:  29, // below minimum of 30
		StuckThreshold: 120,
		OnStuck:        "alert",
	}
	err := ValidateProcessTriageConfig(cfg)
	if err == nil {
		t.Error("should error for idle_threshold < 30")
	}
}

func TestValidateProcessTriageConfig_IdleThresholdBoundary(t *testing.T) {
	t.Parallel()
	cfg := &ProcessTriageConfig{
		Enabled:        true,
		CheckInterval:  10,
		IdleThreshold:  30, // exactly at boundary
		StuckThreshold: 60,
		OnStuck:        "alert",
	}
	if err := ValidateProcessTriageConfig(cfg); err != nil {
		t.Errorf("idle_threshold=30 should be valid: %v", err)
	}
}

func TestValidateProcessTriageConfig_StuckLessThanIdle(t *testing.T) {
	t.Parallel()
	cfg := &ProcessTriageConfig{
		Enabled:        true,
		CheckInterval:  10,
		IdleThreshold:  60,
		StuckThreshold: 50, // less than idle
		OnStuck:        "alert",
	}
	err := ValidateProcessTriageConfig(cfg)
	if err == nil {
		t.Error("should error for stuck_threshold < idle_threshold")
	}
}

func TestValidateProcessTriageConfig_StuckEqualIdle(t *testing.T) {
	t.Parallel()
	cfg := &ProcessTriageConfig{
		Enabled:        true,
		CheckInterval:  10,
		IdleThreshold:  60,
		StuckThreshold: 60, // equal to idle — valid (>=)
		OnStuck:        "alert",
	}
	if err := ValidateProcessTriageConfig(cfg); err != nil {
		t.Errorf("stuck_threshold=idle_threshold should be valid: %v", err)
	}
}

func TestValidateProcessTriageConfig_InvalidOnStuck(t *testing.T) {
	t.Parallel()
	cfg := &ProcessTriageConfig{
		Enabled:        true,
		CheckInterval:  10,
		IdleThreshold:  60,
		StuckThreshold: 120,
		OnStuck:        "restart", // not in valid set
	}
	err := ValidateProcessTriageConfig(cfg)
	if err == nil {
		t.Error("should error for invalid on_stuck action")
	}
}

func TestValidateProcessTriageConfig_AllValidActions(t *testing.T) {
	t.Parallel()
	for _, action := range []string{"alert", "kill", "ignore"} {
		t.Run(action, func(t *testing.T) {
			t.Parallel()
			cfg := &ProcessTriageConfig{
				Enabled:        true,
				CheckInterval:  10,
				IdleThreshold:  60,
				StuckThreshold: 120,
				OnStuck:        action,
			}
			if err := ValidateProcessTriageConfig(cfg); err != nil {
				t.Errorf("on_stuck=%q should be valid: %v", action, err)
			}
		})
	}
}

func TestValidateProcessTriageConfig_ValidBinary(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	binPath := filepath.Join(dir, "pt")
	if err := os.WriteFile(binPath, []byte("#!/bin/sh"), 0o755); err != nil {
		t.Fatal(err)
	}
	cfg := &ProcessTriageConfig{
		Enabled:        true,
		BinaryPath:     binPath,
		CheckInterval:  10,
		IdleThreshold:  60,
		StuckThreshold: 120,
		OnStuck:        "alert",
	}
	if err := ValidateProcessTriageConfig(cfg); err != nil {
		t.Errorf("valid config should pass: %v", err)
	}
}

// =============================================================================
// Validate (main aggregator) tests (64.6% → target >80%)
// =============================================================================

func TestValidate_NilConfig(t *testing.T) {
	t.Parallel()
	errs := Validate(nil)
	if len(errs) == 0 {
		t.Error("Validate(nil) should return errors")
	}
}

func TestValidate_DefaultConfig(t *testing.T) {
	t.Parallel()
	cfg := Default()
	errs := Validate(cfg)
	if len(errs) != 0 {
		t.Errorf("Validate(Default()) returned %d errors: %v", len(errs), errs)
	}
}

func TestValidate_RelativeProjectsBase(t *testing.T) {
	t.Parallel()
	cfg := Default()
	cfg.ProjectsBase = "relative/path"
	errs := Validate(cfg)
	found := false
	for _, e := range errs {
		if errContains(e.Error(), "projects_base") {
			found = true
			break
		}
	}
	if !found {
		t.Error("Validate should error for relative projects_base")
	}
}

func TestValidate_AbsoluteProjectsBase(t *testing.T) {
	t.Parallel()
	cfg := Default()
	cfg.ProjectsBase = "/tmp/ntm-test"
	errs := Validate(cfg)
	for _, e := range errs {
		if errContains(e.Error(), "projects_base") {
			t.Errorf("should not error for absolute projects_base: %v", e)
		}
	}
}

func TestValidate_InvalidHelpVerbosity(t *testing.T) {
	t.Parallel()
	cfg := Default()
	cfg.HelpVerbosity = "verbose" // invalid
	errs := Validate(cfg)
	found := false
	for _, e := range errs {
		if errContains(e.Error(), "help_verbosity") {
			found = true
			break
		}
	}
	if !found {
		t.Error("Validate should error for invalid help_verbosity")
	}
}

func TestValidate_ValidHelpVerbosity(t *testing.T) {
	t.Parallel()
	for _, v := range []string{"minimal", "full", "Minimal", "FULL"} {
		t.Run(v, func(t *testing.T) {
			t.Parallel()
			cfg := Default()
			cfg.HelpVerbosity = v
			errs := Validate(cfg)
			for _, e := range errs {
				if errContains(e.Error(), "help_verbosity") {
					t.Errorf("help_verbosity=%q should be valid: %v", v, e)
				}
			}
		})
	}
}

func TestValidate_NegativeAlerts(t *testing.T) {
	t.Parallel()
	cfg := Default()
	cfg.Alerts.AgentStuckMinutes = -1
	cfg.Alerts.DiskLowThresholdGB = -0.5
	cfg.Alerts.MailBacklogThreshold = -2
	cfg.Alerts.BeadStaleHours = -3
	cfg.Alerts.ContextWarningThreshold = 150
	cfg.Alerts.ResolvedPruneMinutes = -4
	errs := Validate(cfg)
	alertCount := 0
	for _, e := range errs {
		if errContains(e.Error(), "alerts.") {
			alertCount++
		}
	}
	if alertCount < 6 {
		t.Errorf("expected at least 6 alert errors, got %d", alertCount)
	}
}

func TestValidate_NegativeCheckpoints(t *testing.T) {
	t.Parallel()
	cfg := Default()
	cfg.Checkpoints.MaxAutoCheckpoints = -1
	cfg.Checkpoints.ScrollbackLines = -1
	cfg.Checkpoints.IntervalMinutes = -1
	errs := Validate(cfg)
	cpCount := 0
	for _, e := range errs {
		if errContains(e.Error(), "checkpoints.") {
			cpCount++
		}
	}
	if cpCount < 3 {
		t.Errorf("expected at least 3 checkpoint errors, got %d", cpCount)
	}
}

func TestValidate_NegativeResilience(t *testing.T) {
	t.Parallel()
	cfg := Default()
	cfg.Resilience.MaxRestarts = -1
	cfg.Resilience.RestartDelaySeconds = -1
	errs := Validate(cfg)
	rCount := 0
	for _, e := range errs {
		if errContains(e.Error(), "resilience.") {
			rCount++
		}
	}
	if rCount < 2 {
		t.Errorf("expected at least 2 resilience errors, got %d", rCount)
	}
}

func TestValidate_NegativeCASSTimeout(t *testing.T) {
	t.Parallel()
	cfg := Default()
	cfg.CASS.Timeout = -1
	errs := Validate(cfg)
	found := false
	for _, e := range errs {
		if errContains(e.Error(), "cass.timeout") {
			found = true
			break
		}
	}
	if !found {
		t.Error("Validate should error for negative CASS timeout")
	}
}

func TestValidate_CASSContextMinRelevanceOutOfRange(t *testing.T) {
	t.Parallel()
	cfg := Default()
	cfg.CASS.Context.MinRelevance = 1.5 // above 1.0
	errs := Validate(cfg)
	found := false
	for _, e := range errs {
		if errContains(e.Error(), "min_relevance") {
			found = true
			break
		}
	}
	if !found {
		t.Error("Validate should error for min_relevance > 1.0")
	}
}

func TestValidate_CASSContextSkipAboveOutOfRange(t *testing.T) {
	t.Parallel()
	cfg := Default()
	cfg.CASS.Context.SkipIfContextAbove = 101 // above 100
	errs := Validate(cfg)
	found := false
	for _, e := range errs {
		if errContains(e.Error(), "skip_if_context_above") {
			found = true
			break
		}
	}
	if !found {
		t.Error("Validate should error for skip_if_context_above > 100")
	}
}

func TestValidate_CASSContextNegatives(t *testing.T) {
	t.Parallel()
	cfg := Default()
	cfg.CASS.Context.MaxSessions = -1
	cfg.CASS.Context.MaxTokens = -1
	cfg.CASS.Context.LookbackDays = -1
	errs := Validate(cfg)
	cassCount := 0
	for _, e := range errs {
		if errContains(e.Error(), "cass.context.") {
			cassCount++
		}
	}
	if cassCount < 3 {
		t.Errorf("expected at least 3 CASS context errors, got %d", cassCount)
	}
}

func TestValidate_TmuxDefaultPanesZero(t *testing.T) {
	t.Parallel()
	cfg := Default()
	cfg.Tmux.DefaultPanes = 0 // below minimum of 1
	errs := Validate(cfg)
	found := false
	for _, e := range errs {
		if errContains(e.Error(), "tmux.default_panes") {
			found = true
			break
		}
	}
	if !found {
		t.Error("Validate should error for tmux.default_panes < 1")
	}
}

func TestValidate_TmuxPaneInitDelayNegative(t *testing.T) {
	t.Parallel()
	cfg := Default()
	cfg.Tmux.PaneInitDelayMs = -1
	errs := Validate(cfg)
	found := false
	for _, e := range errs {
		if errContains(e.Error(), "tmux.pane_init_delay_ms") {
			found = true
			break
		}
	}
	if !found {
		t.Error("Validate should error for negative pane_init_delay_ms")
	}
}

func TestValidate_InvalidContextRotation(t *testing.T) {
	t.Parallel()
	cfg := Default()
	cfg.ContextRotation.WarningThreshold = 2.0 // out of 0.0-1.0 range
	errs := Validate(cfg)
	found := false
	for _, e := range errs {
		if errContains(e.Error(), "context_rotation") {
			found = true
			break
		}
	}
	if !found {
		t.Error("Validate should report context_rotation error")
	}
}

func TestValidate_InvalidRobotOutput(t *testing.T) {
	t.Parallel()
	cfg := Default()
	cfg.Robot.Output.Format = "yaml" // invalid format
	errs := Validate(cfg)
	found := false
	for _, e := range errs {
		if errContains(e.Error(), "robot.output") {
			found = true
			break
		}
	}
	if !found {
		t.Error("Validate should report robot.output error")
	}
}

func TestValidate_InvalidSafetyProfile(t *testing.T) {
	t.Parallel()
	cfg := Default()
	cfg.Safety.Profile = "nonexistent-profile"
	errs := Validate(cfg)
	found := false
	for _, e := range errs {
		if errContains(e.Error(), "safety") {
			found = true
			break
		}
	}
	if !found {
		t.Error("Validate should report safety error")
	}
}

func TestValidate_InvalidRedactionMode(t *testing.T) {
	t.Parallel()
	cfg := Default()
	cfg.Redaction.Mode = "scramble" // invalid mode
	errs := Validate(cfg)
	found := false
	for _, e := range errs {
		if errContains(e.Error(), "redaction") {
			found = true
			break
		}
	}
	if !found {
		t.Error("Validate should report redaction error")
	}
}

func TestValidate_InvalidEncryption(t *testing.T) {
	t.Parallel()
	cfg := Default()
	cfg.Encryption.Enabled = true
	cfg.Encryption.KeySource = "magic" // invalid key source
	errs := Validate(cfg)
	found := false
	for _, e := range errs {
		if errContains(e.Error(), "encryption") {
			found = true
			break
		}
	}
	if !found {
		t.Error("Validate should report encryption error")
	}
}

func TestValidate_MultipleErrors(t *testing.T) {
	t.Parallel()
	cfg := Default()
	cfg.ProjectsBase = "relative"
	cfg.HelpVerbosity = "debug"
	cfg.Alerts.AgentStuckMinutes = -5
	cfg.Checkpoints.MaxAutoCheckpoints = -1
	cfg.Resilience.MaxRestarts = -1
	cfg.CASS.Timeout = -1
	cfg.Tmux.DefaultPanes = 0
	errs := Validate(cfg)
	if len(errs) < 7 {
		t.Errorf("expected at least 7 errors, got %d: %v", len(errs), errs)
	}
}

// =============================================================================
// MergeConfig tests (42.9% → target >70%)
// =============================================================================

func TestMergeConfig_EmptyProject(t *testing.T) {
	t.Parallel()
	global := Default()
	project := &ProjectConfig{}
	result := MergeConfig(global, project, t.TempDir())
	if result != global {
		t.Error("MergeConfig should return the global config pointer")
	}
}

func TestMergeConfig_ProjectDefaults(t *testing.T) {
	t.Parallel()
	global := Default()
	project := &ProjectConfig{
		Defaults: ProjectDefaults{
			Agents: map[string]int{"cc": 3, "cod": 1},
		},
	}
	result := MergeConfig(global, project, t.TempDir())
	if result.ProjectDefaults["cc"] != 3 {
		t.Errorf("ProjectDefaults[cc] = %d, want 3", result.ProjectDefaults["cc"])
	}
	if result.ProjectDefaults["cod"] != 1 {
		t.Errorf("ProjectDefaults[cod] = %d, want 1", result.ProjectDefaults["cod"])
	}
}

func TestMergeConfig_PaletteFileTraversal(t *testing.T) {
	t.Parallel()
	global := Default()
	project := &ProjectConfig{
		Palette: ProjectPalette{File: "../../../etc/passwd"},
	}
	// Should silently ignore the traversal attempt
	result := MergeConfig(global, project, t.TempDir())
	_ = result // just verify no panic
}

func TestMergeConfig_PaletteFileAbsolute(t *testing.T) {
	t.Parallel()
	global := Default()
	project := &ProjectConfig{
		Palette: ProjectPalette{File: "/etc/passwd"},
	}
	// Absolute path should be ignored (unsafe)
	result := MergeConfig(global, project, t.TempDir())
	_ = result // just verify no panic
}

func TestMergeConfig_PaletteFileFromNtmDir(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	// Create .ntm/palette.md with a valid command
	ntmDir := filepath.Join(dir, ".ntm")
	if err := os.MkdirAll(ntmDir, 0o755); err != nil {
		t.Fatal(err)
	}
	paletteContent := "## Testing\n### test-cmd | Test Command\nDo something cool\n"
	if err := os.WriteFile(filepath.Join(ntmDir, "palette.md"), []byte(paletteContent), 0o644); err != nil {
		t.Fatal(err)
	}

	global := Default()
	global.Palette = []PaletteCmd{{Key: "existing", Label: "Existing", Prompt: "do existing"}}
	project := &ProjectConfig{
		Palette: ProjectPalette{File: "palette.md"},
	}
	result := MergeConfig(global, project, dir)
	if len(result.Palette) < 2 {
		t.Errorf("expected at least 2 palette commands, got %d", len(result.Palette))
	}
	// Project commands should come first
	if len(result.Palette) > 0 && result.Palette[0].Key != "test-cmd" {
		t.Errorf("first palette cmd key = %q, want test-cmd", result.Palette[0].Key)
	}
}

func TestMergeConfig_PaletteFileFallbackToRoot(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	// Don't create .ntm/ dir — should fall back to project root
	paletteContent := "## Testing\n### root-cmd | Root Command\nFrom root\n"
	if err := os.WriteFile(filepath.Join(dir, "palette.md"), []byte(paletteContent), 0o644); err != nil {
		t.Fatal(err)
	}

	global := Default()
	project := &ProjectConfig{
		Palette: ProjectPalette{File: "palette.md"},
	}
	result := MergeConfig(global, project, dir)
	if len(result.Palette) < 1 {
		t.Errorf("expected at least 1 palette command from root, got %d", len(result.Palette))
	}
}

func TestMergeConfig_PaletteFileDeduplicates(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	ntmDir := filepath.Join(dir, ".ntm")
	if err := os.MkdirAll(ntmDir, 0o755); err != nil {
		t.Fatal(err)
	}
	paletteContent := "## Testing\n### dup-key | Project Cmd\nProject prompt\n"
	if err := os.WriteFile(filepath.Join(ntmDir, "palette.md"), []byte(paletteContent), 0o644); err != nil {
		t.Fatal(err)
	}

	global := Default()
	global.Palette = []PaletteCmd{{Key: "dup-key", Label: "Global Cmd", Prompt: "global prompt"}}
	project := &ProjectConfig{
		Palette: ProjectPalette{File: "palette.md"},
	}
	result := MergeConfig(global, project, dir)
	// Should deduplicate — project cmd takes precedence
	dupCount := 0
	for _, cmd := range result.Palette {
		if cmd.Key == "dup-key" {
			dupCount++
		}
	}
	if dupCount != 1 {
		t.Errorf("expected 1 entry for dup-key, got %d", dupCount)
	}
	// First one should be from project (takes precedence)
	if result.Palette[0].Label != "Project Cmd" {
		t.Errorf("first dup-key label = %q, want 'Project Cmd'", result.Palette[0].Label)
	}
}

func TestMergeConfig_PaletteFileNotFound(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	global := Default()
	origLen := len(global.Palette)
	project := &ProjectConfig{
		Palette: ProjectPalette{File: "nonexistent.md"},
	}
	result := MergeConfig(global, project, dir)
	// Palette should remain unchanged
	if len(result.Palette) != origLen {
		t.Errorf("palette changed unexpectedly: %d → %d", origLen, len(result.Palette))
	}
}

func TestMergeConfig_PaletteFileAllowsDoubleDotFilename(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	ntmDir := filepath.Join(dir, ".ntm")
	if err := os.MkdirAll(ntmDir, 0o755); err != nil {
		t.Fatal(err)
	}
	paletteContent := "## Testing\n### dotted-cmd | Dotted Command\nAllowed double-dot filename\n"
	if err := os.WriteFile(filepath.Join(ntmDir, "palette..md"), []byte(paletteContent), 0o644); err != nil {
		t.Fatal(err)
	}

	global := Default()
	project := &ProjectConfig{
		Palette: ProjectPalette{File: "palette..md"},
	}
	result := MergeConfig(global, project, dir)
	found := false
	for _, cmd := range result.Palette {
		if cmd.Key == "dotted-cmd" {
			found = true
			break
		}
	}
	if !found {
		t.Fatal("expected palette command from palette..md to be loaded")
	}
}

func TestMergeConfig_PaletteStatePreferFirst(t *testing.T) {
	t.Parallel()
	global := Default()
	global.PaletteState.Pinned = []string{"global-pin"}
	global.PaletteState.Favorites = []string{"global-fav"}
	project := &ProjectConfig{
		PaletteState: PaletteState{
			Pinned:    []string{"project-pin"},
			Favorites: []string{"project-fav"},
		},
	}
	result := MergeConfig(global, project, t.TempDir())
	// Project entries come first
	if len(result.PaletteState.Pinned) < 2 {
		t.Errorf("expected at least 2 pinned entries, got %d", len(result.PaletteState.Pinned))
	}
	if result.PaletteState.Pinned[0] != "project-pin" {
		t.Errorf("first pinned should be project-pin, got %q", result.PaletteState.Pinned[0])
	}
}

// =============================================================================
// mergeStringListPreferFirst tests
// =============================================================================

func TestMergeStringListPreferFirst_BothEmpty(t *testing.T) {
	t.Parallel()
	got := mergeStringListPreferFirst(nil, nil)
	if got != nil {
		t.Errorf("mergeStringListPreferFirst(nil, nil) = %v, want nil", got)
	}
}

func TestMergeStringListPreferFirst_PrimaryOnly(t *testing.T) {
	t.Parallel()
	got := mergeStringListPreferFirst([]string{"a", "b"}, nil)
	if len(got) != 2 || got[0] != "a" || got[1] != "b" {
		t.Errorf("mergeStringListPreferFirst(primary, nil) = %v", got)
	}
}

func TestMergeStringListPreferFirst_SecondaryOnly(t *testing.T) {
	t.Parallel()
	got := mergeStringListPreferFirst(nil, []string{"x", "y"})
	if len(got) != 2 || got[0] != "x" || got[1] != "y" {
		t.Errorf("mergeStringListPreferFirst(nil, secondary) = %v", got)
	}
}

func TestMergeStringListPreferFirst_Duplicates(t *testing.T) {
	t.Parallel()
	got := mergeStringListPreferFirst([]string{"a", "b"}, []string{"b", "c"})
	if len(got) != 3 {
		t.Errorf("expected 3 unique items, got %d: %v", len(got), got)
	}
	// "a" should come first (from primary)
	if got[0] != "a" {
		t.Errorf("first item should be 'a', got %q", got[0])
	}
}

func TestMergeStringListPreferFirst_WhitespaceAndEmpty(t *testing.T) {
	t.Parallel()
	got := mergeStringListPreferFirst([]string{" a ", "", "  "}, []string{" a ", "b"})
	// " a " trimmed to "a", empty strings skipped, deduped
	if len(got) != 2 {
		t.Errorf("expected 2 items, got %d: %v", len(got), got)
	}
}

func TestMergeStringListPreferFirst_AllEmpty(t *testing.T) {
	t.Parallel()
	got := mergeStringListPreferFirst([]string{"", " "}, []string{"", " "})
	if got != nil {
		t.Errorf("all empty/whitespace should return nil, got %v", got)
	}
}

// helper for string contains check (prefixed to avoid conflict with swarm_test.go)
func errContains(s, substr string) bool {
	return len(s) >= len(substr) && errSearch(s, substr)
}

func errSearch(s, substr string) bool {
	for i := 0; i <= len(s)-len(substr); i++ {
		if s[i:i+len(substr)] == substr {
			return true
		}
	}
	return false
}
