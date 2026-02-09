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
