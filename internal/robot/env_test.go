package robot

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

// =============================================================================
// Tmux Environment Detection Tests (bd-35xyt)
// =============================================================================

func TestDetectTmuxEnv(t *testing.T) {
	info := DetectTmuxEnv()

	// RecommendedPath should always be /usr/bin/tmux
	if info.RecommendedPath != "/usr/bin/tmux" {
		t.Errorf("RecommendedPath = %q, want %q", info.RecommendedPath, "/usr/bin/tmux")
	}

	// BinaryPath should be a valid path or empty
	if info.BinaryPath != "" && !fileExists(info.BinaryPath) {
		// Only fail if we claimed to find a path that doesn't exist
		t.Errorf("BinaryPath %q does not exist", info.BinaryPath)
	}

	// Warning should be set only if alias detected
	if info.ShellAliasDetected && info.Warning == "" {
		t.Error("ShellAliasDetected=true but Warning is empty")
	}
	if !info.ShellAliasDetected && info.Warning != "" {
		t.Error("ShellAliasDetected=false but Warning is set")
	}
}

func TestFindTmuxBinaryPath(t *testing.T) {
	path := findTmuxBinaryPath()

	// Should return a path
	if path == "" {
		t.Error("findTmuxBinaryPath() returned empty string")
	}

	// Path should either exist or be the default fallback
	if !fileExists(path) && path != "/usr/bin/tmux" {
		t.Errorf("findTmuxBinaryPath() returned non-existent path: %q", path)
	}
}

func TestGetTmuxVersion(t *testing.T) {
	tests := []struct {
		name       string
		binaryPath string
		wantEmpty  bool
	}{
		{
			name:       "valid tmux path",
			binaryPath: "/usr/bin/tmux",
			wantEmpty:  false,
		},
		{
			name:       "invalid path",
			binaryPath: "/nonexistent/path/to/tmux",
			wantEmpty:  true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			version := getTmuxVersion(tt.binaryPath)

			if tt.wantEmpty && version != "" {
				t.Errorf("getTmuxVersion(%q) = %q, want empty", tt.binaryPath, version)
			}
			if !tt.wantEmpty && version == "" {
				// Skip if tmux isn't installed
				if !fileExists(tt.binaryPath) {
					t.Skipf("tmux not found at %q", tt.binaryPath)
				}
				t.Errorf("getTmuxVersion(%q) returned empty, want version string", tt.binaryPath)
			}

			// Version should be the version number without "tmux " prefix
			if version != "" && contains(version, "tmux ") {
				t.Errorf("getTmuxVersion() = %q, should not contain 'tmux ' prefix", version)
			}
		})
	}
}

func TestTmuxEnvInfo_JSONStructure(t *testing.T) {
	info := TmuxEnvInfo{
		BinaryPath:         "/usr/bin/tmux",
		Version:            "tmux 3.4",
		ShellAliasDetected: true,
		RecommendedPath:    "/usr/bin/tmux",
		Warning:            "Use binary_path to avoid shell plugin interference",
		OhMyZshTmuxPlugin:  false,
		TmuxinatorDetected: false,
		TmuxResurrect:      false,
	}

	data, err := json.Marshal(info)
	if err != nil {
		t.Fatalf("Failed to marshal TmuxEnvInfo: %v", err)
	}

	var decoded map[string]interface{}
	if err := json.Unmarshal(data, &decoded); err != nil {
		t.Fatalf("Failed to unmarshal: %v", err)
	}

	// Check required fields are present
	requiredFields := []string{
		"binary_path",
		"version",
		"shell_alias_detected",
		"recommended_path",
		"oh_my_zsh_tmux_plugin",
		"tmuxinator_detected",
		"tmux_resurrect",
	}

	for _, field := range requiredFields {
		if _, ok := decoded[field]; !ok {
			t.Errorf("Missing field %q in JSON output", field)
		}
	}

	// Warning should be present when alias detected
	if _, ok := decoded["warning"]; !ok {
		t.Error("Missing 'warning' field when shell_alias_detected=true")
	}
}

func TestTmuxEnvInfo_NoWarningWhenNoAlias(t *testing.T) {
	info := TmuxEnvInfo{
		BinaryPath:         "/usr/bin/tmux",
		Version:            "tmux 3.4",
		ShellAliasDetected: false,
		RecommendedPath:    "/usr/bin/tmux",
		// Warning should be empty
	}

	data, err := json.Marshal(info)
	if err != nil {
		t.Fatalf("Failed to marshal: %v", err)
	}

	var decoded map[string]interface{}
	if err := json.Unmarshal(data, &decoded); err != nil {
		t.Fatalf("Failed to unmarshal: %v", err)
	}

	// Warning should be omitted when empty (omitempty)
	if _, ok := decoded["warning"]; ok {
		t.Error("Warning field should be omitted when empty (omitempty)")
	}
}

func TestEnvOutput_JSONEnvelope(t *testing.T) {
	output := EnvOutput{
		RobotResponse: NewRobotResponse(true),
		Session:       "test-session",
		Tmux: TmuxEnvInfo{
			BinaryPath:      "/usr/bin/tmux",
			Version:         "tmux 3.4",
			RecommendedPath: "/usr/bin/tmux",
		},
	}

	data, err := json.Marshal(output)
	if err != nil {
		t.Fatalf("Failed to marshal EnvOutput: %v", err)
	}

	var decoded map[string]interface{}
	if err := json.Unmarshal(data, &decoded); err != nil {
		t.Fatalf("Failed to unmarshal: %v", err)
	}

	// Check RobotResponse envelope fields
	if _, ok := decoded["success"]; !ok {
		t.Error("Missing 'success' field from RobotResponse envelope")
	}
	if _, ok := decoded["timestamp"]; !ok {
		t.Error("Missing 'timestamp' field from RobotResponse envelope")
	}

	// Check env-specific fields
	if _, ok := decoded["tmux"]; !ok {
		t.Error("Missing 'tmux' field in EnvOutput")
	}
	if _, ok := decoded["session"]; !ok {
		t.Error("Missing 'session' field in EnvOutput")
	}
}

func TestTimingInfo_Defaults(t *testing.T) {
	timing := &TimingInfo{
		CtrlCGapMs:          100,
		PostExitWaitMs:      3000,
		CCInitWaitMs:        6000,
		PromptSubmitDelayMs: 1000,
	}

	// Verify reasonable defaults
	if timing.CtrlCGapMs != 100 {
		t.Errorf("CtrlCGapMs = %d, want 100", timing.CtrlCGapMs)
	}
	if timing.PostExitWaitMs != 3000 {
		t.Errorf("PostExitWaitMs = %d, want 3000", timing.PostExitWaitMs)
	}
	if timing.CCInitWaitMs != 6000 {
		t.Errorf("CCInitWaitMs = %d, want 6000", timing.CCInitWaitMs)
	}
	if timing.PromptSubmitDelayMs != 1000 {
		t.Errorf("PromptSubmitDelayMs = %d, want 1000", timing.PromptSubmitDelayMs)
	}
}

func TestTargetingInfo_Format(t *testing.T) {
	targeting := &TargetingInfo{
		PaneFormat:         "session:window.pane",
		ExampleAgentPane:   "myproject:1.2",
		ExampleControlPane: "myproject:1.1",
	}

	if targeting.PaneFormat != "session:window.pane" {
		t.Errorf("PaneFormat = %q, want %q", targeting.PaneFormat, "session:window.pane")
	}

	// Examples should match session name in format
	if !contains(targeting.ExampleAgentPane, "myproject:1") {
		t.Errorf("ExampleAgentPane %q should contain session reference", targeting.ExampleAgentPane)
	}
	if !contains(targeting.ExampleControlPane, "myproject:1") {
		t.Errorf("ExampleControlPane %q should contain session reference", targeting.ExampleControlPane)
	}
}

func TestSessionStructureInfo(t *testing.T) {
	structure := &SessionStructureInfo{
		WindowIndex:     1,
		ControlPane:     1,
		AgentPaneStart:  2,
		AgentPaneEnd:    16,
		TotalAgentPanes: 15,
	}

	// Validate relationships
	if structure.ControlPane >= structure.AgentPaneStart {
		t.Error("ControlPane should be less than AgentPaneStart")
	}
	if structure.AgentPaneEnd < structure.AgentPaneStart {
		t.Error("AgentPaneEnd should be >= AgentPaneStart")
	}
	if structure.TotalAgentPanes != structure.AgentPaneEnd-structure.AgentPaneStart+1 {
		t.Errorf("TotalAgentPanes %d doesn't match range [%d, %d]",
			structure.TotalAgentPanes, structure.AgentPaneStart, structure.AgentPaneEnd)
	}
}

func TestDetectOhMyZshTmuxPlugin(t *testing.T) {
	// This test checks the detection logic works
	// It may return true or false depending on the actual environment
	result := detectOhMyZshTmuxPlugin()

	// Just verify it doesn't panic and returns a bool
	_ = result
}

func TestDetectTmuxinator(t *testing.T) {
	result := detectTmuxinator()
	// Just verify it doesn't panic and returns a bool
	_ = result
}

func TestDetectTmuxResurrect(t *testing.T) {
	result := detectTmuxResurrect()
	// Just verify it doesn't panic and returns a bool
	_ = result
}

func TestFileExists(t *testing.T) {
	tests := []struct {
		name string
		path string
		want bool
	}{
		{"root exists", "/", false}, // directory, not file
		{"nonexistent", "/nonexistent/path/file.txt", false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := fileExists(tt.path)
			if got != tt.want {
				t.Errorf("fileExists(%q) = %v, want %v", tt.path, got, tt.want)
			}
		})
	}

	// Test with a file that should exist
	t.Run("usr_bin_sh", func(t *testing.T) {
		// /bin/sh should exist on any Unix system
		if !fileExists("/bin/sh") && !fileExists("/usr/bin/sh") {
			t.Skip("Neither /bin/sh nor /usr/bin/sh exists")
		}
	})
}

func TestDirExists(t *testing.T) {
	tests := []struct {
		name string
		path string
		want bool
	}{
		{"root exists", "/", true},
		{"tmp exists", "/tmp", true},
		{"nonexistent", "/nonexistent/path/dir", false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := dirExists(tt.path)
			if got != tt.want {
				t.Errorf("dirExists(%q) = %v, want %v", tt.path, got, tt.want)
			}
		})
	}
}

func TestDetectShellEnv(t *testing.T) {
	// Save original SHELL and restore after test
	originalShell := os.Getenv("SHELL")
	defer os.Setenv("SHELL", originalShell)

	tests := []struct {
		name     string
		shell    string
		wantType string
		wantNil  bool
	}{
		{"zsh", "/bin/zsh", "zsh", false},
		{"bash", "/bin/bash", "bash", false},
		{"empty shell", "", "", true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			os.Setenv("SHELL", tt.shell)
			info := detectShellEnv()

			if tt.wantNil {
				if info != nil {
					t.Error("Expected nil, got non-nil ShellEnvInfo")
				}
				return
			}

			if info == nil {
				t.Fatal("Expected non-nil ShellEnvInfo, got nil")
			}

			if info.Type != tt.wantType {
				t.Errorf("Type = %q, want %q", info.Type, tt.wantType)
			}
		})
	}
}

// Integration test - only run if HOME is set
func TestDetectOhMyZshTmuxPlugin_Integration(t *testing.T) {
	home := os.Getenv("HOME")
	if home == "" {
		t.Skip("HOME not set")
	}

	omzDir := filepath.Join(home, ".oh-my-zsh")
	if !dirExists(omzDir) {
		t.Skip("oh-my-zsh not installed")
	}

	// Run detection - just verify it doesn't error
	result := detectOhMyZshTmuxPlugin()
	t.Logf("oh-my-zsh tmux plugin detected: %v", result)
}

// =============================================================================
// Shell Environment Detection Tests (bd-28n0d, bd-3eu7h)
// =============================================================================

func TestDetectShellEnv_ConfigPath(t *testing.T) {
	// Save original env and restore after test
	originalShell := os.Getenv("SHELL")
	originalHome := os.Getenv("HOME")
	defer func() {
		os.Setenv("SHELL", originalShell)
		os.Setenv("HOME", originalHome)
	}()

	tests := []struct {
		name           string
		shell          string
		home           string
		wantConfigPath string
	}{
		{
			name:           "zsh config path",
			shell:          "/bin/zsh",
			home:           "/home/testuser",
			wantConfigPath: "/home/testuser/.zshrc",
		},
		{
			name:           "bash config path (falls back to .bash_profile when .bashrc missing)",
			shell:          "/bin/bash",
			home:           "/home/testuser",
			wantConfigPath: "/home/testuser/.bash_profile", // .bashrc doesn't exist, falls back
		},
		{
			name:           "fish config path",
			shell:          "/bin/fish",
			home:           "/home/testuser",
			wantConfigPath: "/home/testuser/.config/fish/config.fish",
		},
		{
			name:           "unknown shell config path",
			shell:          "/bin/ksh",
			home:           "/home/testuser",
			wantConfigPath: "/home/testuser/.kshrc",
		},
		{
			name:           "usr prefix zsh",
			shell:          "/usr/bin/zsh",
			home:           "/Users/test",
			wantConfigPath: "/Users/test/.zshrc",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			os.Setenv("SHELL", tt.shell)
			os.Setenv("HOME", tt.home)

			info := detectShellEnv()

			if info == nil {
				t.Fatal("detectShellEnv() returned nil")
			}

			t.Logf("TEST: %s | Shell=%s | Home=%s | Expected ConfigPath=%s | Got ConfigPath=%s",
				tt.name, tt.shell, tt.home, tt.wantConfigPath, info.ConfigPath)

			if info.ConfigPath != tt.wantConfigPath {
				t.Errorf("ConfigPath = %q, want %q", info.ConfigPath, tt.wantConfigPath)
			}
		})
	}
}

func TestDetectShellEnv_NoHome(t *testing.T) {
	originalShell := os.Getenv("SHELL")
	originalHome := os.Getenv("HOME")
	defer func() {
		os.Setenv("SHELL", originalShell)
		os.Setenv("HOME", originalHome)
	}()

	os.Setenv("SHELL", "/bin/zsh")
	os.Setenv("HOME", "")

	info := detectShellEnv()

	if info == nil {
		t.Fatal("detectShellEnv() returned nil")
	}

	t.Logf("TEST: no HOME | Shell=%s | HOME='' | Expected ConfigPath='' | Got ConfigPath=%s",
		"/bin/zsh", info.ConfigPath)

	if info.ConfigPath != "" {
		t.Errorf("ConfigPath = %q, want empty when HOME not set", info.ConfigPath)
	}
}

// =============================================================================
// Targeting Info Tests (bd-cqnm8, bd-3eu7h)
// =============================================================================

func TestTargetingInfo_SpecialSessionNames(t *testing.T) {
	tests := []struct {
		name        string
		sessionName string
		wantAgent   string
		wantControl string
	}{
		{
			name:        "simple name",
			sessionName: "myproject",
			wantAgent:   "myproject:1.2",
			wantControl: "myproject:1.1",
		},
		{
			name:        "hyphenated name",
			sessionName: "my-project-name",
			wantAgent:   "my-project-name:1.2",
			wantControl: "my-project-name:1.1",
		},
		{
			name:        "underscored name",
			sessionName: "my_project_name",
			wantAgent:   "my_project_name:1.2",
			wantControl: "my_project_name:1.1",
		},
		{
			name:        "numeric suffix",
			sessionName: "project123",
			wantAgent:   "project123:1.2",
			wantControl: "project123:1.1",
		},
		{
			name:        "single char",
			sessionName: "p",
			wantAgent:   "p:1.2",
			wantControl: "p:1.1",
		},
		{
			name:        "all caps",
			sessionName: "MYPROJECT",
			wantAgent:   "MYPROJECT:1.2",
			wantControl: "MYPROJECT:1.1",
		},
		{
			name:        "mixed case",
			sessionName: "MyProject",
			wantAgent:   "MyProject:1.2",
			wantControl: "MyProject:1.1",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			targeting := &TargetingInfo{
				PaneFormat:         "session:window.pane",
				ExampleAgentPane:   tt.sessionName + ":1.2",
				ExampleControlPane: tt.sessionName + ":1.1",
			}

			t.Logf("TEST: %s | Session=%q | Expected Agent=%q Control=%q | Got Agent=%q Control=%q",
				tt.name, tt.sessionName, tt.wantAgent, tt.wantControl,
				targeting.ExampleAgentPane, targeting.ExampleControlPane)

			if targeting.ExampleAgentPane != tt.wantAgent {
				t.Errorf("ExampleAgentPane = %q, want %q", targeting.ExampleAgentPane, tt.wantAgent)
			}
			if targeting.ExampleControlPane != tt.wantControl {
				t.Errorf("ExampleControlPane = %q, want %q", targeting.ExampleControlPane, tt.wantControl)
			}
		})
	}
}

// =============================================================================
// Timing Info Tests (bd-2wkrv, bd-3eu7h)
// =============================================================================

func TestTimingInfo_JSONStructure(t *testing.T) {
	timing := &TimingInfo{
		CtrlCGapMs:          100,
		PostExitWaitMs:      3000,
		CCInitWaitMs:        6000,
		PromptSubmitDelayMs: 1000,
	}

	data, err := json.Marshal(timing)
	if err != nil {
		t.Fatalf("Failed to marshal TimingInfo: %v", err)
	}

	t.Logf("TEST: TimingInfo JSON | Raw=%s", string(data))

	var decoded map[string]interface{}
	if err := json.Unmarshal(data, &decoded); err != nil {
		t.Fatalf("Failed to unmarshal: %v", err)
	}

	// Check all required fields are present with correct JSON keys
	requiredFields := map[string]int{
		"ctrl_c_gap_ms":          100,
		"post_exit_wait_ms":      3000,
		"cc_init_wait_ms":        6000,
		"prompt_submit_delay_ms": 1000,
	}

	for field, expectedVal := range requiredFields {
		val, ok := decoded[field]
		if !ok {
			t.Errorf("Missing field %q in JSON output", field)
			continue
		}
		// JSON numbers are float64
		if int(val.(float64)) != expectedVal {
			t.Errorf("%s = %v, want %d", field, val, expectedVal)
		}
	}
}

func TestTimingInfo_ReasonableRanges(t *testing.T) {
	timing := &TimingInfo{
		CtrlCGapMs:          100,
		PostExitWaitMs:      3000,
		CCInitWaitMs:        6000,
		PromptSubmitDelayMs: 1000,
	}

	tests := []struct {
		name     string
		value    int
		minValid int
		maxValid int
	}{
		{"CtrlCGapMs", timing.CtrlCGapMs, 50, 500},
		{"PostExitWaitMs", timing.PostExitWaitMs, 1000, 10000},
		{"CCInitWaitMs", timing.CCInitWaitMs, 3000, 15000},
		{"PromptSubmitDelayMs", timing.PromptSubmitDelayMs, 500, 5000},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Logf("TEST: %s | Value=%d | Valid range=[%d, %d]",
				tt.name, tt.value, tt.minValid, tt.maxValid)

			if tt.value < tt.minValid || tt.value > tt.maxValid {
				t.Errorf("%s = %d, should be in range [%d, %d]",
					tt.name, tt.value, tt.minValid, tt.maxValid)
			}
		})
	}
}

// =============================================================================
// Session Structure Info Tests (bd-1ws17, bd-3eu7h)
// =============================================================================

func TestSessionStructureInfo_JSONStructure(t *testing.T) {
	structure := &SessionStructureInfo{
		WindowIndex:     1,
		ControlPane:     1,
		AgentPaneStart:  2,
		AgentPaneEnd:    5,
		TotalAgentPanes: 4,
	}

	data, err := json.Marshal(structure)
	if err != nil {
		t.Fatalf("Failed to marshal SessionStructureInfo: %v", err)
	}

	t.Logf("TEST: SessionStructureInfo JSON | Raw=%s", string(data))

	var decoded map[string]interface{}
	if err := json.Unmarshal(data, &decoded); err != nil {
		t.Fatalf("Failed to unmarshal: %v", err)
	}

	// Check all required fields
	requiredFields := []string{
		"window_index",
		"control_pane",
		"agent_pane_start",
		"agent_pane_end",
		"total_agent_panes",
	}

	for _, field := range requiredFields {
		if _, ok := decoded[field]; !ok {
			t.Errorf("Missing field %q in JSON output", field)
		}
	}
}

func TestSessionStructureInfo_EdgeCases(t *testing.T) {
	tests := []struct {
		name      string
		structure SessionStructureInfo
		valid     bool
		reason    string
	}{
		{
			name: "standard layout",
			structure: SessionStructureInfo{
				WindowIndex:     1,
				ControlPane:     1,
				AgentPaneStart:  2,
				AgentPaneEnd:    5,
				TotalAgentPanes: 4,
			},
			valid:  true,
			reason: "standard NTM layout",
		},
		{
			name: "single agent",
			structure: SessionStructureInfo{
				WindowIndex:     1,
				ControlPane:     1,
				AgentPaneStart:  2,
				AgentPaneEnd:    2,
				TotalAgentPanes: 1,
			},
			valid:  true,
			reason: "minimal agent configuration",
		},
		{
			name: "control only",
			structure: SessionStructureInfo{
				WindowIndex:     1,
				ControlPane:     1,
				AgentPaneStart:  0,
				AgentPaneEnd:    0,
				TotalAgentPanes: 0,
			},
			valid:  true,
			reason: "no agents, control pane only",
		},
		{
			name: "many agents",
			structure: SessionStructureInfo{
				WindowIndex:     1,
				ControlPane:     1,
				AgentPaneStart:  2,
				AgentPaneEnd:    16,
				TotalAgentPanes: 15,
			},
			valid:  true,
			reason: "maximum typical agent count",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Logf("TEST: %s | Structure={window=%d, control=%d, start=%d, end=%d, total=%d} | Reason=%s",
				tt.name, tt.structure.WindowIndex, tt.structure.ControlPane,
				tt.structure.AgentPaneStart, tt.structure.AgentPaneEnd,
				tt.structure.TotalAgentPanes, tt.reason)

			// For control-only, no range validation needed
			if tt.structure.TotalAgentPanes == 0 {
				if tt.structure.AgentPaneStart != 0 || tt.structure.AgentPaneEnd != 0 {
					t.Errorf("Control-only session should have zero agent pane indices")
				}
				return
			}

			// Validate relationships
			if tt.structure.ControlPane >= tt.structure.AgentPaneStart {
				t.Error("ControlPane should be less than AgentPaneStart")
			}
			if tt.structure.AgentPaneEnd < tt.structure.AgentPaneStart {
				t.Error("AgentPaneEnd should be >= AgentPaneStart")
			}
		})
	}
}

// =============================================================================
// EnvOutput Full Integration Tests (bd-3eu7h)
// =============================================================================

func TestEnvOutput_FullStructure(t *testing.T) {
	output := EnvOutput{
		RobotResponse: NewRobotResponse(true),
		Session:       "test-project",
		Tmux: TmuxEnvInfo{
			BinaryPath:         "/usr/bin/tmux",
			Version:            "tmux 3.4",
			ShellAliasDetected: false,
			RecommendedPath:    "/usr/bin/tmux",
			OhMyZshTmuxPlugin:  false,
			TmuxinatorDetected: false,
			TmuxResurrect:      false,
		},
		SessionStructure: &SessionStructureInfo{
			WindowIndex:     1,
			ControlPane:     1,
			AgentPaneStart:  2,
			AgentPaneEnd:    4,
			TotalAgentPanes: 3,
		},
		Shell: &ShellEnvInfo{
			Type:               "zsh",
			TmuxPluginDetected: false,
			OhMyZshDetected:    false,
			ConfigPath:         "/home/user/.zshrc",
		},
		Timing: &TimingInfo{
			CtrlCGapMs:          100,
			PostExitWaitMs:      3000,
			CCInitWaitMs:        6000,
			PromptSubmitDelayMs: 1000,
		},
		Targeting: &TargetingInfo{
			PaneFormat:         "session:window.pane",
			ExampleAgentPane:   "test-project:1.2",
			ExampleControlPane: "test-project:1.1",
		},
	}

	data, err := json.Marshal(output)
	if err != nil {
		t.Fatalf("Failed to marshal EnvOutput: %v", err)
	}

	t.Logf("TEST: EnvOutput full structure | Size=%d bytes", len(data))

	var decoded map[string]interface{}
	if err := json.Unmarshal(data, &decoded); err != nil {
		t.Fatalf("Failed to unmarshal: %v", err)
	}

	// Verify all top-level fields
	topLevelFields := []string{
		"success",
		"timestamp",
		"session",
		"tmux",
		"session_structure",
		"shell",
		"timing",
		"targeting",
	}

	for _, field := range topLevelFields {
		if _, ok := decoded[field]; !ok {
			t.Errorf("Missing top-level field %q in EnvOutput JSON", field)
		}
	}

	// Verify nested tmux fields
	tmux, ok := decoded["tmux"].(map[string]interface{})
	if !ok {
		t.Fatal("tmux field is not an object")
	}
	tmuxFields := []string{"binary_path", "version", "recommended_path"}
	for _, field := range tmuxFields {
		if _, ok := tmux[field]; !ok {
			t.Errorf("Missing tmux.%s field", field)
		}
	}

	// Verify nested shell fields
	shell, ok := decoded["shell"].(map[string]interface{})
	if !ok {
		t.Fatal("shell field is not an object")
	}
	if _, ok := shell["config_path"]; !ok {
		t.Error("Missing shell.config_path field")
	}
}

func TestEnvOutput_OmitEmptyFields(t *testing.T) {
	// Test that optional fields are omitted when empty/nil
	output := EnvOutput{
		RobotResponse: NewRobotResponse(true),
		Session:       "", // empty session
		Tmux: TmuxEnvInfo{
			BinaryPath:      "/usr/bin/tmux",
			Version:         "tmux 3.4",
			RecommendedPath: "/usr/bin/tmux",
		},
		// SessionStructure: nil - should be omitted
		// Shell: nil - should be omitted
		// Timing: nil - should be omitted
		// Targeting: nil - should be omitted
	}

	data, err := json.Marshal(output)
	if err != nil {
		t.Fatalf("Failed to marshal: %v", err)
	}

	t.Logf("TEST: EnvOutput omitempty | Raw=%s", string(data))

	var decoded map[string]interface{}
	if err := json.Unmarshal(data, &decoded); err != nil {
		t.Fatalf("Failed to unmarshal: %v", err)
	}

	// These should be omitted when nil/empty
	optionalFields := []string{"session_structure", "shell", "timing", "targeting"}
	for _, field := range optionalFields {
		if _, ok := decoded[field]; ok {
			t.Errorf("Field %q should be omitted when nil", field)
		}
	}
}

// =============================================================================
// Tmux Version Parsing Tests (bd-35xyt, bd-3eu7h)
// =============================================================================

func TestGetTmuxVersion_Formats(t *testing.T) {
	// Test that version parsing handles various output formats
	// These are typical tmux -V outputs we might encounter
	expectedFormats := []string{
		"tmux 3.4",
		"tmux 3.3a",
		"tmux 2.9a",
		"tmux next-3.4",
	}

	t.Logf("TEST: Expected tmux version formats: %v", expectedFormats)

	// We can't test the actual parsing without mocking exec.Command
	// But we can verify the function handles the actual system tmux
	if fileExists("/usr/bin/tmux") {
		version := getTmuxVersion("/usr/bin/tmux")
		t.Logf("TEST: Actual system tmux version: %q", version)

		if version == "" {
			t.Error("getTmuxVersion returned empty for valid tmux binary")
		}
	}
}

// =============================================================================
// Helper Contains Function Test (bd-3eu7h)
// =============================================================================

// =============================================================================
// GetEnv Integration Tests (bd-18gwh)
// =============================================================================

func TestGetEnv_GlobalMode(t *testing.T) {
	output, err := GetEnv("global")
	if err != nil {
		t.Fatalf("GetEnv('global') returned error: %v", err)
	}

	if !output.RobotResponse.Success {
		t.Errorf("GetEnv('global') returned success=false: %s", output.RobotResponse.Error)
	}

	if output.Session != "global" {
		t.Errorf("Session = %q, want %q", output.Session, "global")
	}

	// Global mode should include tmux, shell, and timing info
	if output.Tmux.BinaryPath == "" {
		t.Error("Tmux.BinaryPath should not be empty")
	}
	if output.Shell == nil {
		t.Error("Shell should not be nil in global mode")
	}
	if output.Timing == nil {
		t.Error("Timing should not be nil")
	}

	// Global mode should NOT include session-specific structure
	if output.SessionStructure != nil {
		t.Error("SessionStructure should be nil in global mode")
	}
	if output.Targeting != nil {
		t.Error("Targeting should be nil in global mode")
	}
}

func TestGetEnv_EmptySession(t *testing.T) {
	output, err := GetEnv("")
	if err != nil {
		t.Fatalf("GetEnv('') returned error: %v", err)
	}

	if !output.RobotResponse.Success {
		t.Errorf("GetEnv('') returned success=false: %s", output.RobotResponse.Error)
	}

	// Empty session is valid (global env info)
	if output.Tmux.BinaryPath == "" {
		t.Error("Tmux.BinaryPath should not be empty")
	}
}

func TestGetEnv_NonExistentSession(t *testing.T) {
	output, err := GetEnv("nonexistent_session_xyz_12345")
	if err != nil {
		t.Fatalf("GetEnv should not return Go error for missing session: %v", err)
	}

	if output.RobotResponse.Success {
		t.Error("GetEnv for non-existent session should return success=false")
	}

	if output.RobotResponse.ErrorCode != ErrCodeSessionNotFound {
		t.Errorf("ErrorCode = %q, want %q", output.RobotResponse.ErrorCode, ErrCodeSessionNotFound)
	}

	if output.RobotResponse.Hint == "" {
		t.Error("Hint should be set for error responses")
	}
}

func TestGetEnv_VersionClean(t *testing.T) {
	output, err := GetEnv("global")
	if err != nil {
		t.Fatalf("GetEnv('global') returned error: %v", err)
	}

	version := output.Tmux.Version
	if version == "" {
		t.Skip("tmux not installed or version not detectable")
	}

	// Version should not contain the "tmux " prefix
	if contains(version, "tmux ") {
		t.Errorf("Tmux.Version = %q, should not contain 'tmux ' prefix", version)
	}
}

func TestGetEnv_TimingDefaults(t *testing.T) {
	output, err := GetEnv("global")
	if err != nil {
		t.Fatalf("GetEnv('global') returned error: %v", err)
	}

	if output.Timing == nil {
		t.Fatal("Timing should not be nil")
	}

	if output.Timing.CtrlCGapMs != 100 {
		t.Errorf("CtrlCGapMs = %d, want 100", output.Timing.CtrlCGapMs)
	}
	if output.Timing.PostExitWaitMs != 3000 {
		t.Errorf("PostExitWaitMs = %d, want 3000", output.Timing.PostExitWaitMs)
	}
	if output.Timing.CCInitWaitMs != 6000 {
		t.Errorf("CCInitWaitMs = %d, want 6000", output.Timing.CCInitWaitMs)
	}
	if output.Timing.PromptSubmitDelayMs != 1000 {
		t.Errorf("PromptSubmitDelayMs = %d, want 1000", output.Timing.PromptSubmitDelayMs)
	}
}

func TestGetEnv_ResponseEnvelope(t *testing.T) {
	output, err := GetEnv("global")
	if err != nil {
		t.Fatalf("GetEnv('global') returned error: %v", err)
	}

	if output.RobotResponse.Version != EnvelopeVersion {
		t.Errorf("Version = %q, want %q", output.RobotResponse.Version, EnvelopeVersion)
	}

	if output.RobotResponse.Timestamp == "" {
		t.Error("Timestamp should not be empty")
	}
}

// =============================================================================
// Helper Contains Function Test (bd-3eu7h)
// =============================================================================

func TestContains(t *testing.T) {
	tests := []struct {
		str      string
		substr   string
		expected bool
	}{
		{"hello world", "world", true},
		{"hello world", "foo", false},
		{"tmux 3.4", "tmux", true},
		{"", "test", false},
		{"test", "", true},
	}

	for _, tt := range tests {
		result := contains(tt.str, tt.substr)
		t.Logf("TEST: contains(%q, %q) | Expected=%v | Got=%v",
			tt.str, tt.substr, tt.expected, result)
		if result != tt.expected {
			t.Errorf("contains(%q, %q) = %v, want %v",
				tt.str, tt.substr, result, tt.expected)
		}
	}
}
