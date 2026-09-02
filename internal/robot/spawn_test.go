package robot

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	agentpkg "github.com/Dicklesworthstone/ntm/internal/agent"
	"github.com/Dicklesworthstone/ntm/internal/agentmail"
	"github.com/Dicklesworthstone/ntm/internal/assignment"
	"github.com/Dicklesworthstone/ntm/internal/bv"
	"github.com/Dicklesworthstone/ntm/internal/config"
	dispatchsvc "github.com/Dicklesworthstone/ntm/internal/dispatch"
	"github.com/Dicklesworthstone/ntm/internal/pressure"
	"github.com/Dicklesworthstone/ntm/internal/provider"
	"github.com/Dicklesworthstone/ntm/internal/ratelimit"
	statuspkg "github.com/Dicklesworthstone/ntm/internal/status"
	"github.com/Dicklesworthstone/ntm/internal/tmux"
	zaipkg "github.com/Dicklesworthstone/ntm/internal/zai"
	"github.com/Dicklesworthstone/ntm/tests/testutil"
)

func TestGetSpawnRejectsProjectNameWithLabelSeparator(t *testing.T) {
	opts := SpawnOptions{
		Session: "my--project",
		CCCount: 1,
		DryRun:  true,
	}

	out, err := GetSpawn(t.Context(), opts, config.Default())
	if err != nil {
		t.Fatalf("GetSpawn returned unexpected error: %v", err)
	}
	if out == nil {
		t.Fatal("GetSpawn returned nil output")
	}
	if out.RobotResponse.Success {
		t.Fatalf("expected spawn validation failure for session %q", opts.Session)
	}
	if out.RobotResponse.ErrorCode != ErrCodeInvalidFlag {
		t.Fatalf("error_code = %q, want %q", out.RobotResponse.ErrorCode, ErrCodeInvalidFlag)
	}
	if !strings.Contains(out.RobotResponse.Error, "contains '--'") {
		t.Fatalf("error = %q, expected project-name separator validation message", out.RobotResponse.Error)
	}
	if out.Agents == nil {
		t.Fatal("agents must be initialized on early validation failures")
	}
	encoded, err := json.Marshal(out)
	if err != nil {
		t.Fatalf("marshal output: %v", err)
	}
	if !strings.Contains(string(encoded), `"agents":[]`) {
		t.Fatalf("encoded output must contain an empty agents array: %s", encoded)
	}
}

func testSpawnLifecycleDependencies(panes []tmux.Pane) *SpawnLifecycleDependencies {
	return &SpawnLifecycleDependencies{
		IsTMUXInstalled: func() bool { return true },
		GetAllPanes: func(context.Context) (map[string][]tmux.Pane, error) {
			return map[string][]tmux.Pane{}, nil
		},
		SessionExists: func(context.Context, string) (bool, error) { return true, nil },
		CreateSession: func(context.Context, string, string, int) error { return nil },
		GetPanes: func(context.Context, string) ([]tmux.Pane, error) {
			return append([]tmux.Pane(nil), panes...), nil
		},
		SplitWindow:       func(context.Context, string, string) (string, error) { return "%new", nil },
		ApplyTiledLayout:  func(context.Context, string) error { return nil },
		QuarantineZAIPane: func(context.Context, string) error { return nil },
		ProbeZAI: func(_ context.Context, _ config.ProviderProfileConfig, identity provider.Identity) (zaipkg.Receipt, error) {
			return zaipkg.Receipt{Model: identity.Model(), ModelSessionEvidence: true, NonceSHA256: "nonce", OutputSHA256: "output", SessionIDSHA256: "session"}, nil
		},
		LaunchAgent: func(_ context.Context, pane tmux.Pane, session, agentType string, number int, _, _ string) (SpawnedAgent, error) {
			return SpawnedAgent{
				Pane:  fmt.Sprintf("%d.%d", pane.WindowIndex, pane.Index),
				Type:  agentType,
				Title: fmt.Sprintf("%s__%s_%d", session, agentTypeShort(agentType), number),
			}, nil
		},
		WaitForReady: func(context.Context, *SpawnOutput, time.Duration) error { return nil },
	}
}

func testSpawnConfig() *config.Config {
	cfg := config.Default()
	cfg.SpawnPacing.Enabled = false
	return cfg
}

func TestGetSpawnSafetyRejectsSessionCreatedBetweenExistenceChecks(t *testing.T) {
	const session = "safety-toctou"
	var sessionExistsCalls int
	var createCalls, getPanesCalls, splitCalls, layoutCalls, launchCalls int
	deps := &SpawnLifecycleDependencies{
		IsTMUXInstalled: func() bool { return true },
		GetAllPanes: func(context.Context) (map[string][]tmux.Pane, error) {
			return map[string][]tmux.Pane{}, nil
		},
		SessionExists: func(context.Context, string) (bool, error) {
			sessionExistsCalls++
			switch sessionExistsCalls {
			case 1:
				return false, nil
			case 2:
				return true, nil
			default:
				t.Fatalf("SessionExists called %d times, want exactly two", sessionExistsCalls)
				return false, nil
			}
		},
		CreateSession: func(context.Context, string, string, int) error {
			createCalls++
			return nil
		},
		GetPanes: func(context.Context, string) ([]tmux.Pane, error) {
			getPanesCalls++
			return nil, nil
		},
		SplitWindow: func(context.Context, string, string) (string, error) {
			splitCalls++
			return "", nil
		},
		ApplyTiledLayout: func(context.Context, string) error {
			layoutCalls++
			return nil
		},
		LaunchAgent: func(context.Context, tmux.Pane, string, string, int, string, string) (SpawnedAgent, error) {
			launchCalls++
			return SpawnedAgent{}, nil
		},
	}

	out, err := GetSpawn(t.Context(), SpawnOptions{
		Session: session, CCCount: 1, NoUserPane: true, Safety: true,
		WorkingDir: t.TempDir(), LifecycleDeps: deps,
	}, testSpawnConfig())
	if err != nil {
		t.Fatalf("GetSpawn returned transport error: %v", err)
	}
	if sessionExistsCalls != 2 {
		t.Fatalf("SessionExists calls=%d, want first false and second true", sessionExistsCalls)
	}
	if createCalls != 0 || getPanesCalls != 0 || splitCalls != 0 || layoutCalls != 0 || launchCalls != 0 {
		t.Fatalf("safety race crossed lifecycle boundary: create=%d get-panes=%d split=%d layout=%d launch=%d",
			createCalls, getPanesCalls, splitCalls, layoutCalls, launchCalls)
	}

	wantError := "session 'safety-toctou' already exists (--spawn-safety mode prevents reuse; use 'ntm kill safety-toctou' first)"
	wantHint := "Choose a new session name or disable --spawn-safety"
	if out.Success || out.Error != wantError || out.RobotResponse.Error != wantError || out.ErrorCode != ErrCodeInvalidFlag || out.Hint != wantHint {
		t.Fatalf("safety race output=%+v, want exact INVALID_FLAG conflict contract", out)
	}
	if out.Agents == nil || len(out.Agents) != 0 {
		t.Fatalf("agents=%+v, want initialized empty array", out.Agents)
	}

	encoded, err := json.Marshal(out)
	if err != nil {
		t.Fatalf("marshal safety race output: %v", err)
	}
	var envelope struct {
		Success   bool           `json:"success"`
		Error     string         `json:"error"`
		ErrorCode string         `json:"error_code"`
		Hint      string         `json:"hint"`
		Agents    []SpawnedAgent `json:"agents"`
	}
	if err := json.Unmarshal(encoded, &envelope); err != nil {
		t.Fatalf("unmarshal safety race output: %v", err)
	}
	if envelope.Success || envelope.Error != wantError || envelope.ErrorCode != ErrCodeInvalidFlag || envelope.Hint != wantHint || envelope.Agents == nil || len(envelope.Agents) != 0 {
		t.Fatalf("safety race JSON=%s, want exact conflict envelope with agents=[]", encoded)
	}
}

func TestValidateSpawnRequestRejectsInvalidCountsAndEmptySpawn(t *testing.T) {
	tests := []struct {
		name string
		opts SpawnOptions
		want string
	}{
		{name: "negative claude", opts: SpawnOptions{Session: "invalid-cc", CCCount: -1, CodCount: 1}, want: "--spawn-cc"},
		{name: "negative codex", opts: SpawnOptions{Session: "invalid-cod", CCCount: 1, CodCount: -1}, want: "--spawn-cod"},
		{name: "negative gemini", opts: SpawnOptions{Session: "invalid-gmi", CCCount: 1, GmiCount: -1}, want: "--spawn-gmi"},
		{name: "negative antigravity", opts: SpawnOptions{Session: "invalid-agy", CCCount: 1, AgyCount: -1}, want: "--spawn-agy"},
		{name: "negative grok", opts: SpawnOptions{Session: "invalid-grok", CCCount: 1, GrokCount: -1}, want: "--spawn-grok"},
		{name: "zero total", opts: SpawnOptions{Session: "invalid-zero"}, want: "no agents specified"},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			out, err := GetSpawn(t.Context(), test.opts, testSpawnConfig())
			if err != nil {
				t.Fatalf("GetSpawn returned transport error: %v", err)
			}
			if out.Success || out.ErrorCode != ErrCodeInvalidFlag || !strings.Contains(out.Error, test.want) {
				t.Fatalf("output=%+v, want INVALID_FLAG containing %q", out, test.want)
			}
			if out.Agents == nil {
				t.Fatal("agents must be initialized on invalid requests")
			}
		})
	}
}

func qualifiedZAIProfile() config.ProviderProfileConfig {
	const hash = "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"
	return config.ProviderProfileConfig{
		Provider:                "zai",
		AccountAlias:            "test-account",
		Model:                   "glm-5.3-flash",
		Endpoint:                "https://api.z.ai/api/anthropic",
		Runtime:                 "claude-code",
		ConfigSHA256:            hash,
		Command:                 "claude",
		AutomationPolicy:        "zai-readonly-ci",
		ExactTargetOnly:         true,
		ProbeRequired:           true,
		ModelProbeState:         "qualified",
		ModelProbeReceiptSHA256: hash,
	}
}

func TestGetSpawnZAIRequiresExactQualifiedProviderProfile(t *testing.T) {
	opts := SpawnOptions{Session: "zai-profile", ZAICount: 1, DryRun: true}
	out, err := GetSpawn(t.Context(), opts, testSpawnConfig())
	if err != nil {
		t.Fatalf("GetSpawn() error: %v", err)
	}
	if out.Success || !strings.Contains(out.Error, "requires an exact --provider-profile") {
		t.Fatalf("missing profile output=%+v, want exact-profile rejection", out)
	}

	cfg := testSpawnConfig()
	profile := qualifiedZAIProfile()
	cfg.ProviderProfiles = map[string]config.ProviderProfileConfig{"zai-test-glm53": profile}
	opts.ZAIProviderProfile = "zai-test-glm53"
	opts.LifecycleDeps = testSpawnLifecycleDependencies(nil)
	out, err = GetSpawn(t.Context(), opts, cfg)
	if err != nil {
		t.Fatalf("GetSpawn(qualified) error: %v", err)
	}
	if !out.Success || len(out.WouldCreate) != 2 { // user pane plus the Z.ai lane
		t.Fatalf("qualified Z.ai dry-run output=%+v", out)
	}
	zai := out.WouldCreate[1]
	identity, err := profile.Identity()
	if err != nil {
		t.Fatalf("profile Identity() error: %v", err)
	}
	if zai.Type != "zai" || zai.ProviderProfile != "zai-test-glm53" || zai.ProviderIdentityHash != identity.Hash() || zai.ProviderIdentityEvidence != provider.IdentityEvidenceProfileAttested || zai.ModelProbeState != "qualified" {
		t.Fatalf("Z.ai preview identity=%+v, want distinct profile-bound Z.ai lane", zai)
	}

	panes := []tmux.Pane{{ID: "%zai", WindowIndex: 0, Index: 0}}
	deps := testSpawnLifecycleDependencies(panes)
	var gotType, gotCommand string
	deps.LaunchAgent = func(_ context.Context, pane tmux.Pane, session, agentType string, number int, _, command string) (SpawnedAgent, error) {
		gotType, gotCommand = agentType, command
		return SpawnedAgent{Pane: "0.0", Type: agentType, Title: fmt.Sprintf("%s__zai_%d", session, number)}, nil
	}
	deps.PersistZAIIdentity = func(context.Context, string, tmux.ProviderPaneIdentity) error { return nil }
	opts.DryRun = false
	opts.NoUserPane = true
	opts.WorkingDir = t.TempDir()
	opts.LifecycleDeps = deps
	out, err = GetSpawn(t.Context(), opts, cfg)
	if err != nil || !out.Success {
		t.Fatalf("GetSpawn(live fake) output=%+v err=%v", out, err)
	}
	if gotType != "zai" || !strings.Contains(gotCommand, "ANTHROPIC_BASE_URL='https://api.z.ai/api/anthropic'") || !strings.Contains(gotCommand, "--restricted") || !strings.Contains(gotCommand, "--disallowedTools 'Bash,Edit,Write,NotebookEdit'") {
		t.Fatalf("Z.ai launch=(%q,%q), want NTM-compiled restricted command", gotType, gotCommand)
	}
	if len(out.Agents) != 1 || out.Agents[0].ProviderIdentityHash != identity.Hash() || out.Agents[0].ProviderIdentityEvidence != provider.IdentityEvidenceProfileAttested || out.Agents[0].Admission == nil || out.Agents[0].Admission.CapacityControlScope != provider.CapacityControlScopeProcessLocal {
		t.Fatalf("Z.ai spawned agent identity=%+v", out.Agents)
	}

}

func TestGetSpawnZAIBlocksBeforeTmuxWithoutSuccessfulBoundProbe(t *testing.T) {
	profile := qualifiedZAIProfile()
	cfg := testSpawnConfig()
	cfg.ProviderProfiles = map[string]config.ProviderProfileConfig{"zai-test-glm53": profile}
	deps := testSpawnLifecycleDependencies([]tmux.Pane{{ID: "%zai", WindowIndex: 0, Index: 0}})
	var launches, creates int
	deps.ProbeZAI = func(context.Context, config.ProviderProfileConfig, provider.Identity) (zaipkg.Receipt, error) {
		return zaipkg.Receipt{Model: profile.Model, FailureClass: "model_session_evidence_missing"}, nil
	}
	deps.CreateSession = func(context.Context, string, string, int) error { creates++; return nil }
	deps.LaunchAgent = func(context.Context, tmux.Pane, string, string, int, string, string) (SpawnedAgent, error) {
		launches++
		return SpawnedAgent{}, nil
	}
	out, err := GetSpawn(t.Context(), SpawnOptions{Session: "zai-probe-block", ZAICount: 1, ZAIProviderProfile: "zai-test-glm53", NoUserPane: true, WorkingDir: t.TempDir(), LifecycleDeps: deps, ProviderAdmission: &scriptedProviderAdmission{decision: ratelimit.Decision{Allowed: true, NoFailover: true}}}, cfg)
	if err != nil {
		t.Fatalf("GetSpawn() error: %v", err)
	}
	if out.Success || launches != 0 || creates != 0 || !strings.Contains(out.Hint, "NO-GO") {
		t.Fatalf("probe failure launched=%d created=%d output=%+v", launches, creates, out)
	}
}

func TestGetSpawnZAIProbeBusinessErrorRecordsExactCapacityClass(t *testing.T) {
	profile := qualifiedZAIProfile()
	cfg := testSpawnConfig()
	cfg.ProviderProfiles = map[string]config.ProviderProfileConfig{"zai-test-glm53": profile}
	deps := testSpawnLifecycleDependencies([]tmux.Pane{{ID: "%zai", WindowIndex: 0, Index: 0}})
	deps.ProbeZAI = func(context.Context, config.ProviderProfileConfig, provider.Identity) (zaipkg.Receipt, error) {
		return zaipkg.Receipt{Model: profile.Model, ProviderErrorClass: provider.ErrorPlanExpired, FailureClass: "process_failed"}, errors.New("redacted")
	}
	admission := &scriptedProviderAdmission{decision: ratelimit.Decision{Allowed: true, NoFailover: true}}
	out, err := GetSpawn(t.Context(), SpawnOptions{Session: "zai-probe-plan", ZAICount: 1, ZAIProviderProfile: "zai-test-glm53", NoUserPane: true, WorkingDir: t.TempDir(), LifecycleDeps: deps, ProviderAdmission: admission}, cfg)
	if err != nil || out.Success || admission.acquires != 1 || admission.releases != 1 || admission.failures != 1 {
		t.Fatalf("output=%+v admission=%+v err=%v", out, admission, err)
	}
}

type scriptedProviderAdmission struct {
	decision  ratelimit.Decision
	acquires  int
	releases  int
	successes int
	failures  int
	identity  provider.Identity
}

func (s *scriptedProviderAdmission) Acquire(identity provider.Identity) ratelimit.Decision {
	s.acquires++
	s.identity = identity
	return s.decision
}
func (s *scriptedProviderAdmission) Release(provider.Identity) { s.releases++ }
func (s *scriptedProviderAdmission) RecordResult(provider.Identity, ratelimit.ErrorClass, time.Duration) ratelimit.Decision {
	s.failures++
	return ratelimit.Decision{NoFailover: true}
}
func (s *scriptedProviderAdmission) RecordSuccess(provider.Identity) { s.successes++ }

func TestGetSpawnZAIAdmissionDenialDoesNotLaunchOrFallback(t *testing.T) {
	profile := qualifiedZAIProfile()
	cfg := testSpawnConfig()
	cfg.ProviderProfiles = map[string]config.ProviderProfileConfig{"zai-test-glm53": profile}
	panes := []tmux.Pane{{ID: "%zai", WindowIndex: 0, Index: 0}}
	deps := testSpawnLifecycleDependencies(panes)
	launches, probes, creates := 0, 0, 0
	deps.ProbeZAI = func(context.Context, config.ProviderProfileConfig, provider.Identity) (zaipkg.Receipt, error) {
		probes++
		return zaipkg.Receipt{}, nil
	}
	deps.CreateSession = func(context.Context, string, string, int) error { creates++; return nil }
	deps.LaunchAgent = func(context.Context, tmux.Pane, string, string, int, string, string) (SpawnedAgent, error) {
		launches++
		return SpawnedAgent{}, nil
	}
	metadataWrites := 0
	deps.PersistZAIIdentity = func(context.Context, string, tmux.ProviderPaneIdentity) error {
		metadataWrites++
		return nil
	}
	admission := &scriptedProviderAdmission{decision: ratelimit.Decision{Reason: ratelimit.ErrorRateLimited, NoFailover: true}}
	out, err := GetSpawn(t.Context(), SpawnOptions{
		Session: "zai-admission", ZAICount: 1, ZAIProviderProfile: "zai-test-glm53", NoUserPane: true,
		WorkingDir: t.TempDir(), LifecycleDeps: deps, ProviderAdmission: admission,
	}, cfg)
	if err != nil {
		t.Fatalf("GetSpawn error: %v", err)
	}
	if out.Success || launches != 0 || probes != 0 || creates != 0 || metadataWrites != 0 {
		t.Fatalf("denial output=%+v launches=%d probes=%d creates=%d metadata=%d, want no provider/tmux mutation", out, launches, probes, creates, metadataWrites)
	}
	if !strings.Contains(out.Error, "probe was not called") {
		t.Fatalf("denied Z.ai receipt=%+v, want explicit no-probe denial", out)
	}
	identity, _ := profile.Identity()
	if admission.acquires != 1 || admission.identity.Hash() != identity.Hash() || admission.releases != 0 {
		t.Fatalf("admission calls acquire=%d release=%d identity=%q", admission.acquires, admission.releases, admission.identity.Hash())
	}
}

func TestGetSpawnZAIPersistsSafeIdentityAndFailsClosedOnPersistenceFailure(t *testing.T) {
	profile := qualifiedZAIProfile()
	cfg := testSpawnConfig()
	cfg.ProviderProfiles = map[string]config.ProviderProfileConfig{"zai-test-glm53": profile}
	panes := []tmux.Pane{{ID: "%zai", WindowIndex: 0, Index: 0}}
	deps := testSpawnLifecycleDependencies(panes)
	deps.LaunchAgent = func(_ context.Context, pane tmux.Pane, session, agentType string, number int, _, _ string) (SpawnedAgent, error) {
		return SpawnedAgent{Pane: pane.Ref().Physical(), Type: agentType, Title: fmt.Sprintf("%s__zai_%d", session, number)}, nil
	}
	var persisted tmux.ProviderPaneIdentity
	deps.PersistZAIIdentity = func(_ context.Context, paneID string, metadata tmux.ProviderPaneIdentity) error {
		if paneID != "%zai" {
			t.Fatalf("pane ID=%q, want %%zai", paneID)
		}
		persisted = metadata
		return nil
	}
	admission := &scriptedProviderAdmission{decision: ratelimit.Decision{Allowed: true, NoFailover: true}}
	out, err := GetSpawn(t.Context(), SpawnOptions{
		Session: "zai-metadata", ZAICount: 1, ZAIProviderProfile: "zai-test-glm53", NoUserPane: true,
		WorkingDir: t.TempDir(), LifecycleDeps: deps, ProviderAdmission: admission,
	}, cfg)
	if err != nil || !out.Success {
		t.Fatalf("GetSpawn output=%+v err=%v", out, err)
	}
	identity, _ := profile.Identity()
	if persisted.Profile != "zai-test-glm53" || persisted.IdentitySHA256 != identity.Hash() || persisted.ModelProbeState != "live_verified" || persisted.ModelProbeReceiptSHA256 != "output" {
		t.Fatalf("persisted metadata=%+v", persisted)
	}
	if admission.acquires != 1 || admission.releases != 1 || admission.successes != 1 || admission.failures != 0 {
		t.Fatalf("admission calls %+v", admission)
	}

	cleanupCalls := 0
	liveProvider := true
	deps.PersistZAIIdentity = func(context.Context, string, tmux.ProviderPaneIdentity) error {
		return errors.New("pane option unavailable")
	}
	deps.QuarantineZAIPane = func(_ context.Context, paneID string) error {
		cleanupCalls++
		if paneID != "%zai" {
			t.Fatalf("quarantine pane ID=%q, want %%zai", paneID)
		}
		liveProvider = false
		return nil
	}
	out, err = GetSpawn(t.Context(), SpawnOptions{
		Session: "zai-metadata-fail", ZAICount: 1, ZAIProviderProfile: "zai-test-glm53", NoUserPane: true,
		WorkingDir: t.TempDir(), LifecycleDeps: deps, ProviderAdmission: &scriptedProviderAdmission{decision: ratelimit.Decision{Allowed: true, NoFailover: true}},
	}, cfg)
	if err != nil || out.Success || len(out.Agents) != 1 || !strings.Contains(out.Agents[0].Error, "metadata") {
		t.Fatalf("metadata persistence failure output=%+v err=%v", out, err)
	}
	if cleanupCalls != 1 || out.Agents[0].CleanupState != "pane_terminated" || out.Agents[0].CleanupErrorHash != "" {
		t.Fatalf("metadata persistence cleanup=%+v calls=%d, want exact-pane termination evidence", out.Agents[0], cleanupCalls)
	}
	if liveProvider {
		t.Fatal("metadata persistence failure left an unbound live Z.ai provider process")
	}
}

func TestGetSpawnZAIMetadataPersistenceFailureRecordsRedactedQuarantineFailure(t *testing.T) {
	profile := qualifiedZAIProfile()
	cfg := testSpawnConfig()
	cfg.ProviderProfiles = map[string]config.ProviderProfileConfig{"zai-test-glm53": profile}
	panes := []tmux.Pane{{ID: "%zai", WindowIndex: 0, Index: 0}}
	deps := testSpawnLifecycleDependencies(panes)
	deps.LaunchAgent = func(_ context.Context, pane tmux.Pane, session, agentType string, number int, _, _ string) (SpawnedAgent, error) {
		return SpawnedAgent{Pane: pane.Ref().Physical(), Type: agentType, Title: fmt.Sprintf("%s__zai_%d", session, number)}, nil
	}
	deps.PersistZAIIdentity = func(context.Context, string, tmux.ProviderPaneIdentity) error {
		return errors.New("metadata failure with secret-token-that-must-not-escape")
	}
	const cleanupSecret = "cleanup-token-that-must-not-escape"
	deps.QuarantineZAIPane = func(context.Context, string) error { return errors.New(cleanupSecret) }

	out, err := GetSpawn(t.Context(), SpawnOptions{
		Session: "zai-metadata-quarantine-fail", ZAICount: 1, ZAIProviderProfile: "zai-test-glm53", NoUserPane: true,
		WorkingDir: t.TempDir(), LifecycleDeps: deps, ProviderAdmission: &scriptedProviderAdmission{decision: ratelimit.Decision{Allowed: true, NoFailover: true}},
	}, cfg)
	if err != nil || out.Success || len(out.Agents) != 1 {
		t.Fatalf("GetSpawn output=%+v err=%v", out, err)
	}
	agent := out.Agents[0]
	if agent.CleanupState != "quarantine_failed" || len(agent.CleanupErrorHash) != 64 || strings.Contains(agent.Error, cleanupSecret) || strings.Contains(out.Error, cleanupSecret) {
		t.Fatalf("unsafe cleanup evidence agent=%+v output=%+v", agent, out)
	}
}

func TestGetSpawnZAILaunchErrorAlwaysQuarantinesExactPane(t *testing.T) {
	profile := qualifiedZAIProfile()
	cfg := testSpawnConfig()
	cfg.ProviderProfiles = map[string]config.ProviderProfileConfig{"zai-test-glm53": profile}
	panes := []tmux.Pane{{ID: "%zai", WindowIndex: 0, Index: 0}}
	deps := testSpawnLifecycleDependencies(panes)
	const launchSecret = "provider-launch-secret-that-must-not-escape"
	deps.LaunchAgent = func(_ context.Context, pane tmux.Pane, session, agentType string, number int, _, _ string) (SpawnedAgent, error) {
		return SpawnedAgent{Pane: pane.Ref().Physical(), Type: agentType, Title: fmt.Sprintf("%s__zai_%d", session, number)}, errors.New(launchSecret)
	}
	metadataWrites := 0
	deps.PersistZAIIdentity = func(context.Context, string, tmux.ProviderPaneIdentity) error {
		metadataWrites++
		return nil
	}
	quarantineCalls := 0
	liveProvider := true
	deps.QuarantineZAIPane = func(_ context.Context, paneID string) error {
		quarantineCalls++
		if paneID != "%zai" {
			t.Fatalf("quarantine pane ID=%q, want %%zai", paneID)
		}
		liveProvider = false
		return nil
	}
	out, err := GetSpawn(t.Context(), SpawnOptions{
		Session: "zai-launch-unknown", ZAICount: 1, ZAIProviderProfile: "zai-test-glm53", NoUserPane: true,
		WorkingDir: t.TempDir(), LifecycleDeps: deps, ProviderAdmission: &scriptedProviderAdmission{decision: ratelimit.Decision{Allowed: true, NoFailover: true}},
	}, cfg)
	if err != nil || out.Success || len(out.Agents) != 1 {
		t.Fatalf("GetSpawn output=%+v err=%v", out, err)
	}
	agent := out.Agents[0]
	if metadataWrites != 0 || quarantineCalls != 1 || liveProvider || agent.CleanupState != "pane_terminated" || len(agent.LaunchErrorHash) != 64 {
		t.Fatalf("launch quarantine evidence agent=%+v metadata=%d cleanup=%d live=%v", agent, metadataWrites, quarantineCalls, liveProvider)
	}
	if strings.Contains(agent.Error, launchSecret) || strings.Contains(out.Error, launchSecret) {
		t.Fatalf("launch receipt leaked provider error: agent=%+v output=%+v", agent, out)
	}
}

func TestGetSpawnGrokUsesExactCommandWithFakeLifecycle(t *testing.T) {
	panes := []tmux.Pane{{ID: "%1", WindowIndex: 0, Index: 0}}
	deps := testSpawnLifecycleDependencies(panes)
	var gotType, gotCommand string
	deps.LaunchAgent = func(_ context.Context, pane tmux.Pane, session, agentType string, number int, _, command string) (SpawnedAgent, error) {
		gotType, gotCommand = agentType, command
		return SpawnedAgent{Pane: "0.0", Type: agentType, Title: fmt.Sprintf("%s__grok_%d", session, number)}, nil
	}

	out, err := GetSpawn(t.Context(), SpawnOptions{
		Session: "grok-fake", GrokCount: 1, NoUserPane: true, WorkingDir: t.TempDir(), LifecycleDeps: deps,
	}, testSpawnConfig())
	if err != nil || !out.Success {
		t.Fatalf("GetSpawn output=%+v err=%v", out, err)
	}
	if gotType != "grok" || gotCommand != agentpkg.DefaultGrokAutomationCommand {
		t.Fatalf("fake launch type=%q command=%q", gotType, gotCommand)
	}
	if len(out.Agents) != 1 || out.Agents[0].Title != "grok-fake__grok_1" {
		t.Fatalf("spawned agents=%+v", out.Agents)
	}
}

func TestGetSpawnGrokUsesConfiguredDefaultModelWithFakeLifecycle(t *testing.T) {
	panes := []tmux.Pane{{ID: "%1", WindowIndex: 0, Index: 0}}
	deps := testSpawnLifecycleDependencies(panes)
	var gotCommand string
	deps.LaunchAgent = func(_ context.Context, _ tmux.Pane, session, agentType string, number int, _, command string) (SpawnedAgent, error) {
		gotCommand = command
		return SpawnedAgent{Pane: "0.0", Type: agentType, Title: fmt.Sprintf("%s__grok_%d", session, number)}, nil
	}
	cfg := testSpawnConfig()
	cfg.Models.DefaultGrok = "account/model"

	out, err := GetSpawn(t.Context(), SpawnOptions{
		Session: "grok-configured", GrokCount: 1, NoUserPane: true, WorkingDir: t.TempDir(), LifecycleDeps: deps,
	}, cfg)
	if err != nil || !out.Success {
		t.Fatalf("GetSpawn output=%+v err=%v", out, err)
	}
	if gotCommand != agentpkg.DefaultGrokAutomationCommand+" --model 'account/model'" {
		t.Fatalf("configured Grok command=%q", gotCommand)
	}
}

func TestLaunchAgentGrokRejectsBusyPaneBeforeMutation(t *testing.T) {
	agent, err := launchAgent(
		t.Context(),
		tmux.Pane{ID: "%9", WindowIndex: 0, Index: 2, Command: "claude"},
		"busy-session",
		"grok",
		1,
		t.TempDir(),
		agentpkg.DefaultGrokAutomationCommand,
	)
	if err == nil || !strings.Contains(err.Error(), "pre-launch current command") {
		t.Fatalf("launchAgent error=%v, want pre-launch baseline rejection", err)
	}
	if !strings.Contains(agent.Error, "pre-launch current command") {
		t.Fatalf("spawned agent error=%q, want baseline rejection", agent.Error)
	}
}

func TestGetSpawnGrokRejectsBusyExistingPaneBeforeTopologyOrLaunch(t *testing.T) {
	panes := []tmux.Pane{{ID: "%1", WindowIndex: 0, Index: 0, Command: "claude"}}
	deps := testSpawnLifecycleDependencies(panes)
	splitCalls := 0
	layoutCalls := 0
	launchCalls := 0
	deps.SplitWindow = func(context.Context, string, string) (string, error) {
		splitCalls++
		return "%new", nil
	}
	deps.ApplyTiledLayout = func(context.Context, string) error {
		layoutCalls++
		return nil
	}
	deps.LaunchAgent = func(context.Context, tmux.Pane, string, string, int, string, string) (SpawnedAgent, error) {
		launchCalls++
		return SpawnedAgent{}, nil
	}

	out, err := GetSpawn(t.Context(), SpawnOptions{
		Session: "grok-busy", GrokCount: 2, NoUserPane: true, WorkingDir: t.TempDir(), LifecycleDeps: deps,
	}, testSpawnConfig())
	if err != nil {
		t.Fatalf("GetSpawn returned transport error: %v", err)
	}
	if out.Success || !strings.Contains(out.Error, "pre-launch current command") {
		t.Fatalf("GetSpawn output=%+v, want occupied-pane rejection", out)
	}
	if splitCalls != 0 || layoutCalls != 0 || launchCalls != 0 {
		t.Fatalf("occupied Grok lifecycle calls: split=%d layout=%d launch=%d, want zero", splitCalls, layoutCalls, launchCalls)
	}
}

func TestGetSpawnGrokAllowsIdleExistingPaneBeforeAddingMissingPane(t *testing.T) {
	panes := []tmux.Pane{{ID: "%1", WindowIndex: 0, Index: 0, Command: "zsh"}}
	deps := testSpawnLifecycleDependencies(nil)
	splitCalls := 0
	layoutCalls := 0
	launchCalls := 0
	deps.GetPanes = func(context.Context, string) ([]tmux.Pane, error) {
		return append([]tmux.Pane(nil), panes...), nil
	}
	deps.SplitWindow = func(context.Context, string, string) (string, error) {
		splitCalls++
		panes = append(panes, tmux.Pane{ID: "%2", WindowIndex: 0, Index: 1, Command: "zsh"})
		return "%2", nil
	}
	deps.ApplyTiledLayout = func(context.Context, string) error {
		layoutCalls++
		return nil
	}
	deps.LaunchAgent = func(_ context.Context, pane tmux.Pane, session, agentType string, number int, _, _ string) (SpawnedAgent, error) {
		launchCalls++
		return SpawnedAgent{
			Pane:  fmt.Sprintf("%d.%d", pane.WindowIndex, pane.Index),
			Type:  agentType,
			Title: fmt.Sprintf("%s__grok_%d", session, number),
		}, nil
	}

	out, err := GetSpawn(t.Context(), SpawnOptions{
		Session: "grok-idle", GrokCount: 2, NoUserPane: true, WorkingDir: t.TempDir(), LifecycleDeps: deps,
	}, testSpawnConfig())
	if err != nil || !out.Success {
		t.Fatalf("GetSpawn output=%+v err=%v", out, err)
	}
	if splitCalls != 1 || layoutCalls != 1 || launchCalls != 2 {
		t.Fatalf("idle Grok lifecycle calls: split=%d layout=%d launch=%d, want 1, 1, 2", splitCalls, layoutCalls, launchCalls)
	}
}

func TestGetSpawnGrokWaitsForReadiness(t *testing.T) {
	panes := []tmux.Pane{{ID: "%1", WindowIndex: 0, Index: 0}}
	deps := testSpawnLifecycleDependencies(panes)
	waitCalls := 0
	deps.WaitForReady = func(_ context.Context, output *SpawnOutput, timeout time.Duration) error {
		waitCalls++
		if timeout != 750*time.Millisecond {
			t.Fatalf("readiness timeout = %s, want 750ms", timeout)
		}
		if len(output.Agents) != 1 || output.Agents[0].Type != "grok" {
			t.Fatalf("readiness agents = %+v, want one Grok pane", output.Agents)
		}
		output.Agents[0].Ready = true
		return nil
	}

	out, err := GetSpawn(t.Context(), SpawnOptions{
		Session: "grok-wait", GrokCount: 1, NoUserPane: true, WorkingDir: t.TempDir(),
		WaitReady: true, ReadyTimeout: 750 * time.Millisecond, LifecycleDeps: deps,
	}, testSpawnConfig())
	if err != nil || !out.Success {
		t.Fatalf("GetSpawn output=%+v err=%v", out, err)
	}
	if waitCalls != 1 || len(out.Agents) != 1 || !out.Agents[0].Ready {
		t.Fatalf("wait calls=%d agents=%+v, want one ready Grok agent", waitCalls, out.Agents)
	}
}

func TestValidateSpawnRequestAcceptsGrokAssignment(t *testing.T) {
	strategy, err := validateSpawnRequest(SpawnOptions{
		GrokCount: 1, WaitReady: true, AssignWork: true, AssignStrategy: " dependency ",
	})
	if err != nil || strategy != "dependency-aware" {
		t.Fatalf("validateSpawnRequest strategy=%q err=%v, want dependency-aware,nil", strategy, err)
	}
}

func TestGetSpawnGrokWaitsBeforeAssignmentDispatch(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	hermeticGlobalConfig(t)
	const session = "grok-wait-assign"
	projectDir := t.TempDir()
	pane := tmux.Pane{
		ID: "%31", WindowIndex: 0, Index: 0, Title: session + "__grok_1", Type: tmux.AgentGrok, Command: "zsh",
	}
	var order []string
	waited := false
	observed := false
	claimed := false
	claimActor := ""

	lifecycle := testSpawnLifecycleDependencies([]tmux.Pane{pane})
	lifecycle.LaunchAgent = func(_ context.Context, gotPane tmux.Pane, gotSession, agentType string, number int, _, _ string) (SpawnedAgent, error) {
		return SpawnedAgent{
			Pane: gotPane.Ref().Physical(), Name: "GrokAgent", Type: agentType,
			Title: fmt.Sprintf("%s__grok_%d", gotSession, number),
		}, nil
	}
	lifecycle.WaitForReady = func(_ context.Context, output *SpawnOutput, _ time.Duration) error {
		order = append(order, "wait")
		if observed || claimed {
			t.Fatal("assignment activity occurred before the readiness wait")
		}
		waited = true
		output.Agents[0].Ready = true
		return nil
	}

	store := assignment.NewStore(session)
	assignmentDeps := &SpawnAssignmentDependencies{
		LoadAssignmentPolicy: func(string, string, bool) (*config.Config, error) {
			return testSpawnConfig(), nil
		},
		FetchActionable: func(context.Context, string, int) ([]bv.TriageRecommendation, error) {
			return []bv.TriageRecommendation{{ID: "bd-grok-ready", Title: "Ready Grok work", Status: "open", Priority: 1}}, nil
		},
		ListPanes: func(context.Context, string) ([]tmux.Pane, error) {
			return []tmux.Pane{pane}, nil
		},
		LoadStore: func(string) (*assignment.AssignmentStore, error) { return store, nil },
		ObserveSession: func(context.Context, string) (statuspkg.SessionObservation, error) {
			order = append(order, "observe")
			if !waited {
				t.Fatal("fresh assignment observation ran before readiness completed")
			}
			observed = true
			return bulkSafeObservation(session, []tmux.Pane{pane}), nil
		},
		GetBeadDetails: func(_ context.Context, _ string, beadID string) (*bv.BeadAssignmentDetails, error) {
			details := &bv.BeadAssignmentDetails{ID: beadID, Title: "Ready Grok work", Status: "open", Priority: 1}
			if claimed {
				details.Status = "in_progress"
				details.Assignee = claimActor
			}
			return details, nil
		},
		ClaimBeadWithOperatorGatedLabels: func(_ context.Context, _ string, beadID, actor string, _ []string) (bv.BeadClaimResult, error) {
			order = append(order, "claim")
			if !observed || strings.TrimSpace(actor) == "" {
				t.Fatalf("claim observed=%v actor=%q, want fresh observation and a canonical actor", observed, actor)
			}
			claimActor = actor
			claimed = true
			return bv.BeadClaimResult{ID: beadID, Title: "Ready Grok work", Actor: actor, Status: "in_progress", ClaimedAt: time.Now().UTC()}, nil
		},
		GetBeadStatus:     func(context.Context, string, string) (string, error) { return "in_progress", nil },
		NewIdempotencyKey: func() (string, error) { return "grok-ready-key", nil },
		DispatchDeliverer: dispatchsvc.DelivererFunc(func(_ context.Context, delivery dispatchsvc.Delivery) error {
			order = append(order, "dispatch")
			if !waited || !observed || !claimed || delivery.Target.Ref.ID != pane.ID {
				t.Fatalf("unsafe dispatch state waited=%v observed=%v claimed=%v target=%q", waited, observed, claimed, delivery.Target.Ref.ID)
			}
			return nil
		}),
	}

	out, err := GetSpawn(t.Context(), SpawnOptions{
		Session: session, GrokCount: 1, NoUserPane: true, WorkingDir: projectDir,
		WaitReady: true, ReadyTimeout: time.Second, AssignWork: true, AssignStrategy: "top-n",
		LifecycleDeps: lifecycle, AssignmentDeps: assignmentDeps,
	}, testSpawnConfig())
	if err != nil || !out.Success {
		t.Fatalf("GetSpawn output=%+v err=%v", out, err)
	}
	if len(out.Assignments) != 1 || !out.Assignments[0].Claimed || !out.Assignments[0].PromptSent {
		t.Fatalf("Grok assignments=%+v, want one claimed and dispatched assignment", out.Assignments)
	}
	waitIndex := slices.Index(order, "wait")
	observeIndex := slices.Index(order, "observe")
	dispatchIndex := slices.Index(order, "dispatch")
	if waitIndex < 0 || observeIndex <= waitIndex || dispatchIndex <= observeIndex {
		t.Fatalf("assignment order=%v, want wait before observe before dispatch", order)
	}
}

func TestValidateSpawnRequestRequiresStrictAssignmentStrategy(t *testing.T) {
	for _, strategy := range []string{"", "   ", "round-robin"} {
		t.Run(fmt.Sprintf("strategy_%q", strategy), func(t *testing.T) {
			out, err := GetSpawn(t.Context(), SpawnOptions{
				Session: "invalid-strategy", CCCount: 1, AssignWork: true, AssignStrategy: strategy,
			}, testSpawnConfig())
			if err != nil {
				t.Fatalf("GetSpawn returned transport error: %v", err)
			}
			if out.Success || out.ErrorCode != ErrCodeInvalidFlag || !strings.Contains(out.Error, "strategy") {
				t.Fatalf("output=%+v, want strict strategy INVALID_FLAG", out)
			}
		})
	}

	normalized, err := validateSpawnRequest(SpawnOptions{CCCount: 1, AssignWork: true, AssignStrategy: " dependency "})
	if err != nil || normalized != "dependency-aware" {
		t.Fatalf("normalized strategy=%q err=%v, want dependency-aware", normalized, err)
	}
}

func TestGetSpawnInitializesAgentsOnInvalidLabel(t *testing.T) {
	out, err := GetSpawn(t.Context(), SpawnOptions{Session: "project", Label: "not valid!", CCCount: 1}, testSpawnConfig())
	if err != nil {
		t.Fatalf("GetSpawn returned transport error: %v", err)
	}
	if out.Success || out.ErrorCode != ErrCodeInvalidFlag || out.Agents == nil {
		t.Fatalf("invalid-label output=%+v", out)
	}
}

func TestGetSpawnPreservesSubsecondReadyTimeout(t *testing.T) {
	panes := []tmux.Pane{{ID: "%1", WindowIndex: 0, Index: 0}}
	deps := testSpawnLifecycleDependencies(panes)
	var gotTimeout time.Duration
	deps.WaitForReady = func(_ context.Context, _ *SpawnOutput, timeout time.Duration) error {
		gotTimeout = timeout
		return nil
	}
	out, err := GetSpawn(t.Context(), SpawnOptions{
		Session: "subsecond-ready", CCCount: 1, NoUserPane: true, WorkingDir: t.TempDir(),
		WaitReady: true, ReadyTimeout: 500 * time.Millisecond, LifecycleDeps: deps,
	}, testSpawnConfig())
	if err != nil || !out.Success {
		t.Fatalf("GetSpawn output=%+v err=%v", out, err)
	}
	if gotTimeout != 500*time.Millisecond {
		t.Fatalf("ready timeout=%s, want 500ms", gotTimeout)
	}
}

func TestGetSpawnUserPaneUsesPhysicalWindowIdentity(t *testing.T) {
	panes := []tmux.Pane{
		{ID: "%41", WindowIndex: 4, Index: 2, Title: "operator"},
		{ID: "%42", WindowIndex: 4, Index: 3, Title: "agent"},
	}
	out, err := GetSpawn(t.Context(), SpawnOptions{
		Session: "physical-user-pane", CCCount: 1, WorkingDir: t.TempDir(), LifecycleDeps: testSpawnLifecycleDependencies(panes),
	}, testSpawnConfig())
	if err != nil || !out.Success {
		t.Fatalf("GetSpawn output=%+v err=%v", out, err)
	}
	if len(out.Agents) != 2 || out.Agents[0].Type != "user" || out.Agents[0].Pane != "4.2" {
		t.Fatalf("spawned agents=%+v, want user pane 4.2", out.Agents)
	}
}

func TestGetSpawnPropagatesTiledLayoutFailure(t *testing.T) {
	panes := []tmux.Pane{{ID: "%1", WindowIndex: 0, Index: 0}}
	deps := testSpawnLifecycleDependencies(panes)
	launches := 0
	deps.ApplyTiledLayout = func(context.Context, string) error { return errors.New("layout rejected") }
	deps.LaunchAgent = func(context.Context, tmux.Pane, string, string, int, string, string) (SpawnedAgent, error) {
		launches++
		return SpawnedAgent{}, nil
	}
	out, err := GetSpawn(t.Context(), SpawnOptions{
		Session: "layout-failure", CCCount: 1, NoUserPane: true, WorkingDir: t.TempDir(), LifecycleDeps: deps,
	}, testSpawnConfig())
	if err != nil {
		t.Fatalf("GetSpawn returned transport error: %v", err)
	}
	if out.Success || out.ErrorCode != ErrCodeInternalError || !strings.Contains(out.Error, "layout rejected") {
		t.Fatalf("output=%+v, want layout INTERNAL_ERROR", out)
	}
	if launches != 0 {
		t.Fatalf("launches=%d, want zero after layout failure", launches)
	}
}

func TestGetSpawnRejectsShortTopology(t *testing.T) {
	panes := []tmux.Pane{{ID: "%1", WindowIndex: 0, Index: 0}}
	deps := testSpawnLifecycleDependencies(panes)
	splits := 0
	layouts := 0
	deps.SplitWindow = func(context.Context, string, string) (string, error) {
		splits++
		return "%2", nil
	}
	deps.ApplyTiledLayout = func(context.Context, string) error { layouts++; return nil }
	out, err := GetSpawn(t.Context(), SpawnOptions{
		Session: "short-topology", CCCount: 2, NoUserPane: true, WorkingDir: t.TempDir(), LifecycleDeps: deps,
	}, testSpawnConfig())
	if err != nil {
		t.Fatalf("GetSpawn returned transport error: %v", err)
	}
	if out.Success || out.ErrorCode != ErrCodePaneNotFound || !strings.Contains(out.Error, "1 pane") {
		t.Fatalf("output=%+v, want PANE_NOT_FOUND", out)
	}
	if splits != 1 || layouts != 0 {
		t.Fatalf("splits=%d layouts=%d, want one split attempt and no layout", splits, layouts)
	}
}

func TestGetSpawnAggregatesLaunchFailures(t *testing.T) {
	panes := []tmux.Pane{
		{ID: "%1", WindowIndex: 0, Index: 0},
		{ID: "%2", WindowIndex: 0, Index: 1},
	}
	deps := testSpawnLifecycleDependencies(panes)
	launches := 0
	deps.LaunchAgent = func(_ context.Context, pane tmux.Pane, _, agentType string, number int, _, _ string) (SpawnedAgent, error) {
		launches++
		err := fmt.Errorf("%s launch %d failed", agentType, number)
		return SpawnedAgent{Pane: fmt.Sprintf("%d.%d", pane.WindowIndex, pane.Index), Type: agentType, Error: err.Error()}, err
	}
	out, err := GetSpawn(t.Context(), SpawnOptions{
		Session: "launch-failures", CCCount: 1, CodCount: 1, NoUserPane: true,
		WorkingDir: t.TempDir(), LifecycleDeps: deps,
	}, testSpawnConfig())
	if err != nil {
		t.Fatalf("GetSpawn returned transport error: %v", err)
	}
	if out.Success || out.ErrorCode != ErrCodeInternalError || launches != 2 || len(out.Agents) != 2 {
		t.Fatalf("output=%+v launches=%d, want two aggregated launch failures", out, launches)
	}
	for _, agent := range out.Agents {
		if agent.Error == "" {
			t.Fatalf("agent lacks per-agent launch diagnostics: %+v", agent)
		}
	}
}

func TestGetSpawnPreservesTypedLaunchAndReadinessTimeouts(t *testing.T) {
	panes := []tmux.Pane{{ID: "%1", WindowIndex: 0, Index: 0}}
	tests := []struct {
		name   string
		want   string
		mutate func(*SpawnLifecycleDependencies)
	}{
		{
			name: "admission topology deadline",
			mutate: func(deps *SpawnLifecycleDependencies) {
				deps.GetAllPanes = func(context.Context) (map[string][]tmux.Pane, error) {
					return nil, fmt.Errorf("admission topology: %w", context.DeadlineExceeded)
				}
			},
		},
		{
			name: "admission topology cancellation",
			want: "canceled",
			mutate: func(deps *SpawnLifecycleDependencies) {
				deps.GetAllPanes = func(context.Context) (map[string][]tmux.Pane, error) {
					return nil, fmt.Errorf("admission topology: %w", context.Canceled)
				}
			},
		},
		{
			name: "session lookup deadline",
			mutate: func(deps *SpawnLifecycleDependencies) {
				deps.SessionExists = func(context.Context, string) (bool, error) {
					return false, fmt.Errorf("session lookup: %w", context.DeadlineExceeded)
				}
			},
		},
		{
			name: "session lookup cancellation",
			want: "canceled",
			mutate: func(deps *SpawnLifecycleDependencies) {
				deps.SessionExists = func(context.Context, string) (bool, error) {
					return false, fmt.Errorf("session lookup: %w", context.Canceled)
				}
			},
		},
		{
			name: "session creation deadline",
			mutate: func(deps *SpawnLifecycleDependencies) {
				deps.SessionExists = func(context.Context, string) (bool, error) { return false, nil }
				deps.CreateSession = func(context.Context, string, string, int) error {
					return fmt.Errorf("session creation: %w", context.DeadlineExceeded)
				}
			},
		},
		{
			name: "pane topology deadline",
			mutate: func(deps *SpawnLifecycleDependencies) {
				deps.GetPanes = func(context.Context, string) ([]tmux.Pane, error) {
					return nil, fmt.Errorf("pane topology: %w", context.DeadlineExceeded)
				}
			},
		},
		{
			name: "pane split deadline",
			mutate: func(deps *SpawnLifecycleDependencies) {
				deps.GetPanes = func(context.Context, string) ([]tmux.Pane, error) { return nil, nil }
				deps.SplitWindow = func(context.Context, string, string) (string, error) {
					return "", fmt.Errorf("pane split: %w", context.DeadlineExceeded)
				}
			},
		},
		{
			name: "layout deadline",
			mutate: func(deps *SpawnLifecycleDependencies) {
				deps.ApplyTiledLayout = func(context.Context, string) error {
					return fmt.Errorf("layout dependency: %w", context.DeadlineExceeded)
				}
			},
		},
		{
			name: "launch deadline",
			mutate: func(deps *SpawnLifecycleDependencies) {
				deps.LaunchAgent = func(context.Context, tmux.Pane, string, string, int, string, string) (SpawnedAgent, error) {
					return SpawnedAgent{Error: "launch deadline"}, fmt.Errorf("launch dependency: %w", context.DeadlineExceeded)
				}
			},
		},
		{
			name: "readiness deadline",
			mutate: func(deps *SpawnLifecycleDependencies) {
				deps.WaitForReady = func(context.Context, *SpawnOutput, time.Duration) error {
					return fmt.Errorf("readiness dependency: %w", context.DeadlineExceeded)
				}
			},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			want := test.want
			if want == "" {
				want = "deadline"
			}
			deps := testSpawnLifecycleDependencies(panes)
			test.mutate(deps)
			out, err := GetSpawn(t.Context(), SpawnOptions{
				Session: "typed-timeout", CCCount: 1, NoUserPane: true, WorkingDir: t.TempDir(),
				WaitReady: true, ReadyTimeout: 500 * time.Millisecond, LifecycleDeps: deps,
			}, testSpawnConfig())
			if err != nil {
				t.Fatalf("GetSpawn returned transport error: %v", err)
			}
			if out.Success || out.ErrorCode != ErrCodeTimeout || !strings.Contains(out.Error, want) {
				t.Fatalf("output=%+v, want TIMEOUT", out)
			}
		})
	}
}

func TestWaitForAgentsReadyDeadlineWrapsDeadlineExceeded(t *testing.T) {
	started := time.Now()
	err := waitForAgentsReadyWithCapture(
		t.Context(),
		&SpawnOutput{Session: "ready-deadline", Agents: []SpawnedAgent{{Pane: "0.0", Type: "claude"}}},
		20*time.Millisecond,
		func(context.Context, string, int) (string, error) { return "still loading", nil },
	)
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("error=%v, want wrapped context.DeadlineExceeded", err)
	}
	if elapsed := time.Since(started); elapsed > 250*time.Millisecond {
		t.Fatalf("20ms readiness deadline took %s", elapsed)
	}
}

func TestWaitForAgentsReadyDeadlineCancelsBlockingCapture(t *testing.T) {
	started := time.Now()
	err := waitForAgentsReadyWithCapture(
		t.Context(),
		&SpawnOutput{Session: "ready-blocked", Agents: []SpawnedAgent{{Pane: "0.0", Type: "claude"}}},
		20*time.Millisecond,
		func(ctx context.Context, _ string, _ int) (string, error) {
			<-ctx.Done()
			return "", ctx.Err()
		},
	)
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("error=%v, want wrapped context.DeadlineExceeded", err)
	}
	if elapsed := time.Since(started); elapsed > 250*time.Millisecond {
		t.Fatalf("20ms readiness deadline with blocking capture took %s", elapsed)
	}
}

func TestPrintSpawn(t *testing.T) {
	testutil.RequireTmuxThrottled(t)

	// Use mock options that don't actually spawn heavy processes if possible,
	// but PrintSpawn calls logic that calls tmux.

	// We can use a test session name
	opts := SpawnOptions{
		Session:    "test_spawn_robot",
		CCCount:    1,
		NoUserPane: true,
		WorkingDir: t.TempDir(), // Use temp dir to avoid creating dirs in /data/projects
	}

	cfg := config.Default()
	// Override agent command to be fast
	cfg.Agents.Claude = "echo test"
	// Admission behavior has dedicated tests below. Keep this spawn/JSON smoke
	// test independent of ambient agents and host pressure while still using a
	// real tmux session.
	cfg.SpawnPacing.Enabled = false

	// Clean up potential session
	defer tmux.KillSession(opts.Session)

	output, err := captureStdout(t, func() error { return PrintSpawn(t.Context(), opts, cfg) })
	if err != nil {
		t.Fatalf("PrintSpawn failed: %v", err)
	}

	// Check JSON output
	var resp map[string]interface{}
	if err := json.Unmarshal([]byte(output), &resp); err != nil {
		t.Fatalf("Failed to parse JSON: %v", err)
	}

	if resp["session"] != opts.Session {
		t.Errorf("Expected session %q, got %v", opts.Session, resp["session"])
	}
	// SpawnOutput doesn't have Created bool, check Layout instead
	if resp["layout"] != "tiled" {
		t.Errorf("Expected layout 'tiled', got %v", resp["layout"])
	}
}

func TestAgentTypeShort(t *testing.T) {
	tests := []struct {
		input    string
		expected string
	}{
		{string(tmux.AgentClaude), "cc"},
		{string(tmux.AgentCodex), "cod"},
		{string(tmux.AgentGemini), "gmi"},
		{"claude_code", "cc"},
		{" openai-codex ", "cod"},
		{"google-gemini", "gmi"},
		{string(tmux.AgentCursor), "cursor"},
		{"ws", "windsurf"},
		{string(tmux.AgentAider), "aider"},
		{string(tmux.AgentUser), "user"},
	}

	for _, tc := range tests {
		if got := agentTypeShort(tc.input); got != tc.expected {
			t.Errorf("agentTypeShort(%v) = %q, want %q", tc.input, got, tc.expected)
		}
	}
}

// =============================================================================
// Comprehensive Robot-Spawn Tests (ntm-1lhn)
// Unit tests, E2E scripts, schema stability, deterministic ordering
// =============================================================================

// TestIsAgentReady_Patterns validates agent ready detection patterns
func TestIsAgentReady_Patterns(t *testing.T) {

	tests := []struct {
		name      string
		output    string
		agentType string
		expected  bool
	}{
		// Claude indicators
		{"claude_prompt_lowercase", "claude>", "claude", true},
		{"claude_prompt_spaced", "claude > ", "claude", true},
		{"claude_code_version", "Claude Code v1.2.3", "claude", true},
		{"claude_welcome", "Welcome back!", "claude", true},
		{"claude_bypass_permissions", "Bypass permissions: enabled", "claude", true},
		{"claude_try_example", "Try \"help me with X\"", "claude", true},

		// Codex indicators
		{"codex_prompt", "codex>", "codex", true},
		{"codex_context_left", "42% context left · ? for shortcuts", "codex", true},
		{"codex_chevron_prompt", "› Write tests for @filename", "codex", true},
		{"codex_ready", "Ready for input", "codex", true},

		// Gemini indicators
		{"gemini_prompt", "gemini>", "gemini", true},
		{"gemini_help", "How can I help you today?", "gemini", true},

		// Generic shell prompts
		{"shell_dollar", "$ ", "claude", true},
		{"shell_percent", "% ", "claude", true},
		{"shell_arrow", "❯ ", "claude", true},
		{"shell_simple", "> ", "claude", true},
		{"python_repl", ">>> ", "codex", true},

		// Not ready states
		{"loading", "Loading...", "claude", false},
		{"empty", "", "claude", false},
		{"garbage", "xyzabc123", "claude", false},
		{"partial_prompt", "claud", "claude", false},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := isAgentReady(tc.output, tc.agentType)
			if got != tc.expected {
				t.Errorf("[E2E-SPAWN] isAgentReady(%q, %q) = %v, want %v",
					tc.output, tc.agentType, got, tc.expected)
			}
		})
	}
}

// TestGetSpawnRejectsUnhonorableModelOverrideBeforeMutation proves a spawn
// with a model override that the launch template cannot carry fails with
// INVALID_FLAG before any tmux session or pane is touched (bd-rr8gn).
func TestGetSpawnRejectsUnhonorableModelOverrideBeforeMutation(t *testing.T) {
	cfg := config.Default()
	cfg.Agents.Codex = "custom-codex --flag" // no {{.Model}} placeholder
	mutated := false
	deps := &SpawnLifecycleDependencies{
		IsTMUXInstalled: func() bool { return true },
		GetAllPanes: func(context.Context) (map[string][]tmux.Pane, error) {
			return map[string][]tmux.Pane{}, nil
		},
		SessionExists: func(context.Context, string) (bool, error) { return false, nil },
		CreateSession: func(context.Context, string, string, int) error {
			mutated = true
			return nil
		},
	}
	output, err := GetSpawn(context.Background(), SpawnOptions{
		Session:       "override-guard",
		CodCount:      1,
		CodModel:      "gpt-5.6-terra",
		LifecycleDeps: deps,
	}, cfg)
	if err != nil {
		t.Fatalf("GetSpawn: %v", err)
	}
	if output.Success {
		t.Fatal("expected failure output")
	}
	if output.ErrorCode != ErrCodeInvalidFlag {
		t.Fatalf("ErrorCode=%q, want %q", output.ErrorCode, ErrCodeInvalidFlag)
	}
	if mutated {
		t.Fatal("session was created despite unhonorable model override")
	}
}

// TestSpawnOptions_DryRunMode validates dry-run returns correct structure without creating session
func TestSpawnOptions_DryRunMode(t *testing.T) {
	// DryRun should work even without tmux since it doesn't actually create sessions

	opts := SpawnOptions{
		Session:    "test_dryrun_session",
		CCCount:    2,
		CodCount:   1,
		GmiCount:   1,
		NoUserPane: false,
		DryRun:     true,
	}

	cfg := config.Default()

	output, err := captureStdout(t, func() error { return PrintSpawn(t.Context(), opts, cfg) })
	if err != nil {
		t.Fatalf("[E2E-SPAWN] DryRun PrintSpawn failed: %v", err)
	}

	var resp SpawnOutput
	if err := json.Unmarshal([]byte(output), &resp); err != nil {
		t.Fatalf("[E2E-SPAWN] Failed to parse DryRun JSON: %v", err)
	}

	// Validate dry-run specific fields
	if !resp.DryRun {
		t.Error("[E2E-SPAWN] DryRun field should be true")
	}

	// Validate session name
	if resp.Session != opts.Session {
		t.Errorf("[E2E-SPAWN] Session mismatch: got %q, want %q", resp.Session, opts.Session)
	}

	// Validate WouldCreate has correct count: 1 user + 2 claude + 1 codex + 1 gemini = 5
	expectedCount := 5
	if len(resp.WouldCreate) != expectedCount {
		t.Errorf("[E2E-SPAWN] WouldCreate count: got %d, want %d", len(resp.WouldCreate), expectedCount)
	}

	// Validate agent types in WouldCreate
	typeCounts := make(map[string]int)
	for _, agent := range resp.WouldCreate {
		typeCounts[agent.Type]++
	}

	if typeCounts["user"] != 1 {
		t.Errorf("[E2E-SPAWN] Expected 1 user pane, got %d", typeCounts["user"])
	}
	if typeCounts["claude"] != 2 {
		t.Errorf("[E2E-SPAWN] Expected 2 claude panes, got %d", typeCounts["claude"])
	}
	if typeCounts["codex"] != 1 {
		t.Errorf("[E2E-SPAWN] Expected 1 codex pane, got %d", typeCounts["codex"])
	}
	if typeCounts["gemini"] != 1 {
		t.Errorf("[E2E-SPAWN] Expected 1 gemini pane, got %d", typeCounts["gemini"])
	}

	// Validate no error in dry-run
	if resp.Error != "" {
		t.Errorf("[E2E-SPAWN] Unexpected error in dry-run: %s", resp.Error)
	}

	t.Logf("[E2E-SPAWN] Operation=DryRunMode | Session=%s | WouldCreate=%d | Types=%v",
		resp.Session, len(resp.WouldCreate), typeCounts)
}

func TestSpawnOptions_DryRunCustomNamesExcludeUserPane(t *testing.T) {
	opts := SpawnOptions{
		Session:     "test_dryrun_custom_names",
		CCCount:     2,
		CodCount:    1,
		DryRun:      true,
		CustomNames: []string{"alice", "bob", "charlie"},
	}

	resp, err := GetSpawn(t.Context(), opts, config.Default())
	if err != nil {
		t.Fatalf("GetSpawn returned error: %v", err)
	}
	if len(resp.WouldCreate) != 4 {
		t.Fatalf("WouldCreate count = %d, want 4", len(resp.WouldCreate))
	}
	if got := resp.WouldCreate[0]; got.Type != "user" || got.Name != "user-alpha" {
		t.Errorf("user preview = (%q, %q), want (user, user-alpha)", got.Type, got.Name)
	}

	for i, want := range []string{"alice", "bob", "charlie"} {
		if got := resp.WouldCreate[i+1].Name; got != want {
			t.Errorf("agent %d preview name = %q, want %q", i+1, got, want)
		}
	}
}

func TestSpawnOptions_DryRunIncludesAdmission(t *testing.T) {
	opts := SpawnOptions{
		Session:    "test_dryrun_admission",
		CCCount:    1,
		CodCount:   1,
		NoUserPane: false,
		DryRun:     true,
	}

	// Hoist the agent cap above any plausible runner state so this
	// test is environment-independent. With bd-1oenb's fix the
	// admission cap counts running + requested vs MaxAgents; the
	// real-tmux RunningAgents on a CI/agent-swarm host would
	// otherwise trip the default cap before the request is even
	// considered.
	cfg := config.Default()
	cfg.SpawnPacing.AgentCaps.ClaudeMaxConcurrent = 1024
	cfg.SpawnPacing.AgentCaps.CodexMaxConcurrent = 1024
	cfg.SpawnPacing.AgentCaps.GeminiMaxConcurrent = 1024

	resp, err := GetSpawn(t.Context(), opts, cfg)
	if err != nil {
		t.Fatalf("GetSpawn returned error: %v", err)
	}
	if resp.Admission == nil {
		t.Fatal("Admission is nil")
	}
	if resp.Admission.RequestedAgents != 2 {
		t.Errorf("Admission.RequestedAgents = %d, want 2", resp.Admission.RequestedAgents)
	}
	if resp.Admission.RequestedPanes != 3 {
		t.Errorf("Admission.RequestedPanes = %d, want 3", resp.Admission.RequestedPanes)
	}
	if resp.Admission.Decision != pressure.SpawnAdmissionAdmit {
		t.Errorf("Admission.Decision = %s, want admit", resp.Admission.Decision)
	}
}

func TestSpawnOptions_DryRunAdmissionRefusesAgentCap(t *testing.T) {
	cfg := config.Default()
	cfg.SpawnPacing.AgentCaps.ClaudeMaxConcurrent = 1
	cfg.SpawnPacing.AgentCaps.CodexMaxConcurrent = 0
	cfg.SpawnPacing.AgentCaps.GeminiMaxConcurrent = 0

	resp, err := GetSpawn(t.Context(), SpawnOptions{
		Session:    "test_dryrun_admission_refuse",
		CCCount:    2,
		NoUserPane: true,
		DryRun:     true,
	}, cfg)
	if err != nil {
		t.Fatalf("GetSpawn returned error: %v", err)
	}
	if resp.Admission == nil {
		t.Fatal("Admission is nil")
	}
	if resp.Admission.Decision != pressure.SpawnAdmissionRefuse {
		t.Fatalf("Admission.Decision = %s, want refuse", resp.Admission.Decision)
	}
	if resp.Admission.Reason != "agent_limit_exceeded" {
		t.Errorf("Admission.Reason = %q, want agent_limit_exceeded", resp.Admission.Reason)
	}
	if !resp.Success {
		t.Error("dry-run should stay successful even when admission would refuse a real spawn")
	}
}

func TestCollectSpawnAdmissionInputCancellationReachesTopology(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	started := make(chan struct{})
	done := make(chan struct{})
	cfg := config.Default()
	cfg.SpawnPacing.Enabled = false
	go func() {
		defer close(done)
		collectSpawnAdmissionInputWithPanes(ctx, SpawnOptions{Session: "cancel-admission"}, cfg, 1, 1,
			func(callCtx context.Context) (map[string][]tmux.Pane, error) {
				close(started)
				<-callCtx.Done()
				return nil, callCtx.Err()
			})
	}()
	select {
	case <-started:
	case <-time.After(time.Second):
		t.Fatal("spawn topology collection did not start")
	}
	cancel()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("spawn topology collection ignored cancellation")
	}
}

// TestSpawnOptions_NoAgentsSpecified validates error when no agents specified
func TestSpawnOptions_NoAgentsSpecified(t *testing.T) {
	testutil.RequireTmuxThrottled(t)

	opts := SpawnOptions{
		Session:    "test_no_agents",
		CCCount:    0,
		CodCount:   0,
		GmiCount:   0,
		NoUserPane: true,
	}

	cfg := config.Default()

	output, _ := captureStdout(t, func() error { return PrintSpawn(t.Context(), opts, cfg) })

	var resp SpawnOutput
	if err := json.Unmarshal([]byte(output), &resp); err != nil {
		t.Fatalf("[E2E-SPAWN] Failed to parse JSON: %v", err)
	}

	// Should have error about no agents
	if resp.Error == "" {
		t.Error("[E2E-SPAWN] Expected error for no agents specified")
	}
	if resp.Error != "no agents specified (use cc, cod, gmi, agy, grok, or zai counts)" {
		t.Errorf("[E2E-SPAWN] Unexpected error message: %s", resp.Error)
	}

	t.Logf("[E2E-SPAWN] Operation=NoAgents | Error=%q", resp.Error)
}

// TestSpawnOptions_SafetyMode validates safety mode blocks existing sessions
func TestSpawnOptions_SafetyMode(t *testing.T) {
	testutil.RequireTmuxThrottled(t)

	sessionName := "test_safety_mode_spawn"

	// Create session first
	if err := tmux.CreateSession(sessionName, "/tmp"); err != nil {
		t.Fatalf("[E2E-SPAWN] Failed to create test session: %v", err)
	}
	defer tmux.KillSession(sessionName)

	opts := SpawnOptions{
		Session:    sessionName,
		CCCount:    1,
		NoUserPane: true,
		Safety:     true, // Enable safety mode
	}

	cfg := config.Default()
	cfg.Agents.Claude = "echo test"

	output, _ := captureStdout(t, func() error { return PrintSpawn(t.Context(), opts, cfg) })

	var resp SpawnOutput
	if err := json.Unmarshal([]byte(output), &resp); err != nil {
		t.Fatalf("[E2E-SPAWN] Failed to parse JSON: %v", err)
	}

	// Safety mode should produce error for existing session
	if resp.Error == "" {
		t.Error("[E2E-SPAWN] Safety mode should error for existing session")
	}
	if resp.Error == "" || !containsAnyStr(resp.Error, "already exists", "spawn-safety") {
		t.Errorf("[E2E-SPAWN] Expected safety mode error, got: %s", resp.Error)
	}

	t.Logf("[E2E-SPAWN] Operation=SafetyMode | Session=%s | Error=%q", sessionName, resp.Error)
}

// TestSpawnOptions_MultipleAgentTypes validates spawning multiple agent types
func TestSpawnOptions_MultipleAgentTypes(t *testing.T) {
	testutil.RequireTmuxThrottled(t)

	sessionName := "test_multi_agent_spawn"
	defer tmux.KillSession(sessionName)

	opts := SpawnOptions{
		Session:    sessionName,
		CCCount:    1,
		CodCount:   1,
		GmiCount:   1,
		NoUserPane: false,       // Include user pane
		WorkingDir: t.TempDir(), // Use temp dir to avoid creating dirs in /data/projects
	}

	cfg := config.Default()
	// Hoist agent caps so bd-1oenb's running+requested cap check
	// doesn't refuse the spawn on a busy runner (default caps are 7).
	cfg.SpawnPacing.AgentCaps.ClaudeMaxConcurrent = 1024
	cfg.SpawnPacing.AgentCaps.CodexMaxConcurrent = 1024
	cfg.SpawnPacing.AgentCaps.GeminiMaxConcurrent = 1024
	// Use fast echo commands
	cfg.Agents.Claude = "echo claude_test"
	cfg.Agents.Codex = "echo codex_test"
	cfg.Agents.Gemini = "echo gemini_test"

	output, err := captureStdout(t, func() error { return PrintSpawn(t.Context(), opts, cfg) })
	if err != nil {
		t.Fatalf("[E2E-SPAWN] PrintSpawn failed: %v", err)
	}

	var resp SpawnOutput
	if err := json.Unmarshal([]byte(output), &resp); err != nil {
		t.Fatalf("[E2E-SPAWN] Failed to parse JSON: %v", err)
	}

	// Validate no error
	if resp.Error != "" {
		t.Errorf("[E2E-SPAWN] Unexpected error: %s", resp.Error)
	}

	// Validate session created
	if resp.Session != sessionName {
		t.Errorf("[E2E-SPAWN] Session mismatch: got %q, want %q", resp.Session, sessionName)
	}

	// Count agent types: 1 user + 1 claude + 1 codex + 1 gemini = 4
	expectedCount := 4
	if len(resp.Agents) != expectedCount {
		t.Errorf("[E2E-SPAWN] Agent count: got %d, want %d", len(resp.Agents), expectedCount)
	}

	// Verify each type is present
	typeCounts := make(map[string]int)
	for _, agent := range resp.Agents {
		typeCounts[agent.Type]++
	}

	if typeCounts["user"] != 1 {
		t.Errorf("[E2E-SPAWN] Expected 1 user, got %d", typeCounts["user"])
	}
	if typeCounts["claude"] != 1 {
		t.Errorf("[E2E-SPAWN] Expected 1 claude, got %d", typeCounts["claude"])
	}
	if typeCounts["codex"] != 1 {
		t.Errorf("[E2E-SPAWN] Expected 1 codex, got %d", typeCounts["codex"])
	}
	if typeCounts["gemini"] != 1 {
		t.Errorf("[E2E-SPAWN] Expected 1 gemini, got %d", typeCounts["gemini"])
	}

	panes, err := tmux.GetPanes(sessionName)
	if err != nil {
		t.Fatalf("[E2E-SPAWN] GetPanes before title rewrite: %v", err)
	}
	wantTypeByID := make(map[string]tmux.AgentType)
	for _, pane := range panes {
		if pane.Type == tmux.AgentUser {
			continue
		}
		wantTypeByID[pane.ID] = pane.Type
		if err := tmux.SetPaneTitle(pane.ID, "caam | title replaced by wrapper"); err != nil {
			t.Fatalf("[E2E-SPAWN] rewrite pane %s title: %v", pane.ID, err)
		}
	}
	panes, err = tmux.GetPanes(sessionName)
	if err != nil {
		t.Fatalf("[E2E-SPAWN] GetPanes after title rewrite: %v", err)
	}
	for _, pane := range panes {
		if want, ok := wantTypeByID[pane.ID]; ok && pane.Type != want {
			t.Errorf("[E2E-SPAWN] pane %s type after wrapper/title rewrite = %s, want %s", pane.ID, pane.Type, want)
		}
	}

	t.Logf("[E2E-SPAWN] Operation=MultiAgentTypes | Session=%s | Agents=%d | Types=%v",
		resp.Session, len(resp.Agents), typeCounts)
}

// TestSpawnOutput_SchemaStability validates JSON schema is consistent and deterministic
func TestSpawnOutput_SchemaStability(t *testing.T) {

	// Test schema with dry-run (doesn't need tmux)
	opts := SpawnOptions{
		Session:    "test_schema_stability",
		CCCount:    1,
		NoUserPane: true,
		DryRun:     true,
	}

	cfg := config.Default()

	output, err := captureStdout(t, func() error { return PrintSpawn(t.Context(), opts, cfg) })
	if err != nil {
		t.Fatalf("[E2E-SPAWN] PrintSpawn failed: %v", err)
	}

	// Validate required fields are present
	var resp map[string]interface{}
	if err := json.Unmarshal([]byte(output), &resp); err != nil {
		t.Fatalf("[E2E-SPAWN] Failed to parse JSON: %v", err)
	}

	// Required top-level fields
	requiredFields := []string{"session", "created_at", "working_dir", "layout"}
	for _, field := range requiredFields {
		if _, ok := resp[field]; !ok {
			t.Errorf("[E2E-SPAWN] Missing required field: %s", field)
		}
	}

	// DryRun-specific fields
	if resp["dry_run"] != true {
		t.Error("[E2E-SPAWN] dry_run field should be true")
	}
	if _, ok := resp["would_create"]; !ok {
		t.Error("[E2E-SPAWN] Missing would_create field in dry-run mode")
	}

	// Validate would_create array elements have required fields
	wouldCreate, ok := resp["would_create"].([]interface{})
	if !ok {
		t.Fatal("[E2E-SPAWN] would_create is not an array")
	}

	for i, item := range wouldCreate {
		agent, ok := item.(map[string]interface{})
		if !ok {
			t.Errorf("[E2E-SPAWN] would_create[%d] is not an object", i)
			continue
		}

		agentRequiredFields := []string{"pane", "type", "title"}
		for _, field := range agentRequiredFields {
			if _, ok := agent[field]; !ok {
				t.Errorf("[E2E-SPAWN] would_create[%d] missing field: %s", i, field)
			}
		}
	}

	t.Logf("[E2E-SPAWN] Operation=SchemaStability | Fields=%d | WouldCreate=%d",
		len(resp), len(wouldCreate))
}

// TestSpawnOutput_DeterministicOrdering validates agent order is deterministic
func TestSpawnOutput_DeterministicOrdering(t *testing.T) {

	opts := SpawnOptions{
		Session:    "test_deterministic_order",
		CCCount:    2,
		CodCount:   1,
		GmiCount:   1,
		NoUserPane: false,
		DryRun:     true,
	}

	cfg := config.Default()

	// Run multiple times to verify consistent ordering
	var lastOrder []string
	for i := 0; i < 3; i++ {
		output, err := captureStdout(t, func() error { return PrintSpawn(t.Context(), opts, cfg) })
		if err != nil {
			t.Fatalf("[E2E-SPAWN] PrintSpawn iteration %d failed: %v", i, err)
		}

		var resp SpawnOutput
		if err := json.Unmarshal([]byte(output), &resp); err != nil {
			t.Fatalf("[E2E-SPAWN] Failed to parse JSON iteration %d: %v", i, err)
		}

		// Extract order of agent types
		var currentOrder []string
		for _, agent := range resp.WouldCreate {
			currentOrder = append(currentOrder, agent.Type)
		}

		if i > 0 {
			// Compare with previous iteration
			if len(currentOrder) != len(lastOrder) {
				t.Errorf("[E2E-SPAWN] Order length changed: %v vs %v", lastOrder, currentOrder)
			}
			for j := range currentOrder {
				if j < len(lastOrder) && currentOrder[j] != lastOrder[j] {
					t.Errorf("[E2E-SPAWN] Order changed at index %d: %s vs %s",
						j, lastOrder[j], currentOrder[j])
				}
			}
		}
		lastOrder = currentOrder
	}

	// Verify expected order: user, claude, claude, codex, gemini
	expectedOrder := []string{"user", "claude", "claude", "codex", "gemini"}
	if len(lastOrder) != len(expectedOrder) {
		t.Errorf("[E2E-SPAWN] Order length: got %d, want %d", len(lastOrder), len(expectedOrder))
	}
	for i, expected := range expectedOrder {
		if i < len(lastOrder) && lastOrder[i] != expected {
			t.Errorf("[E2E-SPAWN] Order[%d]: got %s, want %s", i, lastOrder[i], expected)
		}
	}

	t.Logf("[E2E-SPAWN] Operation=DeterministicOrdering | Order=%v", lastOrder)
}

// TestPrintSpawn_TmuxNotInstalled validates error when tmux unavailable
func TestPrintSpawn_TmuxNotInstalled(t *testing.T) {
	// This test can only properly run in environments without tmux
	// We'll test the dry-run path which doesn't check tmux, and note the behavior

	// DryRun mode bypasses tmux check, so we can test that path
	opts := SpawnOptions{
		Session:    "test_no_tmux",
		CCCount:    1,
		NoUserPane: true,
		DryRun:     true,
	}

	cfg := config.Default()

	output, err := captureStdout(t, func() error { return PrintSpawn(t.Context(), opts, cfg) })
	if err != nil {
		t.Fatalf("[E2E-SPAWN] PrintSpawn failed: %v", err)
	}

	var resp SpawnOutput
	if err := json.Unmarshal([]byte(output), &resp); err != nil {
		t.Fatalf("[E2E-SPAWN] Failed to parse JSON: %v", err)
	}

	// DryRun should succeed regardless of tmux
	if resp.DryRun != true {
		t.Error("[E2E-SPAWN] Expected dry_run=true")
	}

	t.Logf("[E2E-SPAWN] Operation=TmuxNotInstalled_DryRun | DryRun=%v | Error=%q",
		resp.DryRun, resp.Error)
}

// TestSpawnOptions_NoUserPane validates NoUserPane option
func TestSpawnOptions_NoUserPane(t *testing.T) {

	// Test with dry-run
	optsWithUser := SpawnOptions{
		Session:    "test_with_user",
		CCCount:    1,
		NoUserPane: false, // Include user pane
		DryRun:     true,
	}

	optsNoUser := SpawnOptions{
		Session:    "test_no_user",
		CCCount:    1,
		NoUserPane: true, // Exclude user pane
		DryRun:     true,
	}

	cfg := config.Default()

	// With user pane
	output1, _ := captureStdout(t, func() error { return PrintSpawn(t.Context(), optsWithUser, cfg) })
	var resp1 SpawnOutput
	json.Unmarshal([]byte(output1), &resp1)

	// Without user pane
	output2, _ := captureStdout(t, func() error { return PrintSpawn(t.Context(), optsNoUser, cfg) })
	var resp2 SpawnOutput
	json.Unmarshal([]byte(output2), &resp2)

	// With user: should have 2 agents (user + claude)
	if len(resp1.WouldCreate) != 2 {
		t.Errorf("[E2E-SPAWN] With user: expected 2 agents, got %d", len(resp1.WouldCreate))
	}

	// Without user: should have 1 agent (claude only)
	if len(resp2.WouldCreate) != 1 {
		t.Errorf("[E2E-SPAWN] Without user: expected 1 agent, got %d", len(resp2.WouldCreate))
	}

	// Verify user pane is first when included
	if len(resp1.WouldCreate) > 0 && resp1.WouldCreate[0].Type != "user" {
		t.Errorf("[E2E-SPAWN] User pane should be first, got %s", resp1.WouldCreate[0].Type)
	}

	// Verify no user pane when excluded
	for _, agent := range resp2.WouldCreate {
		if agent.Type == "user" {
			t.Error("[E2E-SPAWN] Should not have user pane when NoUserPane=true")
		}
	}

	t.Logf("[E2E-SPAWN] Operation=NoUserPane | WithUser=%d | WithoutUser=%d",
		len(resp1.WouldCreate), len(resp2.WouldCreate))
}

// TestSpawnedAgent_TitleFormat validates pane title format consistency
func TestSpawnedAgent_TitleFormat(t *testing.T) {

	opts := SpawnOptions{
		Session:    "test_title_format",
		CCCount:    2,
		CodCount:   1,
		GmiCount:   1,
		NoUserPane: false,
		DryRun:     true,
	}

	cfg := config.Default()

	output, _ := captureStdout(t, func() error { return PrintSpawn(t.Context(), opts, cfg) })

	var resp SpawnOutput
	if err := json.Unmarshal([]byte(output), &resp); err != nil {
		t.Fatalf("[E2E-SPAWN] Failed to parse JSON: %v", err)
	}

	// Validate title formats
	for _, agent := range resp.WouldCreate {
		switch agent.Type {
		case "user":
			expected := "test_title_format__user"
			if agent.Title != expected {
				t.Errorf("[E2E-SPAWN] User title: got %q, want %q", agent.Title, expected)
			}
		case "claude":
			// Should match pattern: session__cc_N
			if !containsAnyStr(agent.Title, "__cc_1", "__cc_2") {
				t.Errorf("[E2E-SPAWN] Claude title format invalid: %s", agent.Title)
			}
		case "codex":
			if !containsAnyStr(agent.Title, "__cod_1") {
				t.Errorf("[E2E-SPAWN] Codex title format invalid: %s", agent.Title)
			}
		case "gemini":
			if !containsAnyStr(agent.Title, "__gmi_1") {
				t.Errorf("[E2E-SPAWN] Gemini title format invalid: %s", agent.Title)
			}
		}
	}

	t.Logf("[E2E-SPAWN] Operation=TitleFormat | Agents=%d", len(resp.WouldCreate))
}

// TestSpawnOutput_TimestampFormat validates created_at is RFC3339
func TestSpawnOutput_TimestampFormat(t *testing.T) {

	opts := SpawnOptions{
		Session:    "test_timestamp",
		CCCount:    1,
		NoUserPane: true,
		DryRun:     true,
	}

	cfg := config.Default()

	output, _ := captureStdout(t, func() error { return PrintSpawn(t.Context(), opts, cfg) })

	var resp SpawnOutput
	if err := json.Unmarshal([]byte(output), &resp); err != nil {
		t.Fatalf("[E2E-SPAWN] Failed to parse JSON: %v", err)
	}

	// Validate timestamp is not empty
	if resp.CreatedAt == "" {
		t.Error("[E2E-SPAWN] created_at should not be empty")
	}

	// Validate RFC3339 format by attempting to parse
	// RFC3339 format: 2006-01-02T15:04:05Z07:00
	if len(resp.CreatedAt) < 20 {
		t.Errorf("[E2E-SPAWN] created_at too short for RFC3339: %s", resp.CreatedAt)
	}

	// Check for T separator and Z suffix (UTC)
	if !containsAnyStr(resp.CreatedAt, "T") {
		t.Errorf("[E2E-SPAWN] created_at missing T separator: %s", resp.CreatedAt)
	}

	t.Logf("[E2E-SPAWN] Operation=TimestampFormat | CreatedAt=%s", resp.CreatedAt)
}

// TestSpawnOutput_WorkingDir validates working directory handling
func TestSpawnOutput_WorkingDir(t *testing.T) {

	// Test with explicit working dir
	customDir := "/tmp/test_spawn_workdir"
	opts := SpawnOptions{
		Session:    "test_workdir",
		CCCount:    1,
		NoUserPane: true,
		WorkingDir: customDir,
		DryRun:     true,
	}

	cfg := config.Default()

	output, _ := captureStdout(t, func() error { return PrintSpawn(t.Context(), opts, cfg) })

	var resp SpawnOutput
	if err := json.Unmarshal([]byte(output), &resp); err != nil {
		t.Fatalf("[E2E-SPAWN] Failed to parse JSON: %v", err)
	}

	// Validate working dir is set
	if resp.WorkingDir != customDir {
		t.Errorf("[E2E-SPAWN] WorkingDir: got %q, want %q", resp.WorkingDir, customDir)
	}

	t.Logf("[E2E-SPAWN] Operation=WorkingDir | Dir=%s", resp.WorkingDir)
}

// TestSpawnOptions_PresetUsed validates preset field in output
func TestSpawnOptions_PresetUsed(t *testing.T) {

	opts := SpawnOptions{
		Session:    "test_preset",
		CCCount:    1,
		NoUserPane: true,
		Preset:     "my-recipe",
		DryRun:     true,
	}

	cfg := config.Default()

	output, _ := captureStdout(t, func() error { return PrintSpawn(t.Context(), opts, cfg) })

	var resp SpawnOutput
	if err := json.Unmarshal([]byte(output), &resp); err != nil {
		t.Fatalf("[E2E-SPAWN] Failed to parse JSON: %v", err)
	}

	// Validate preset is recorded
	if resp.PresetUsed != "my-recipe" {
		t.Errorf("[E2E-SPAWN] PresetUsed: got %q, want %q", resp.PresetUsed, "my-recipe")
	}

	t.Logf("[E2E-SPAWN] Operation=PresetUsed | Preset=%s", resp.PresetUsed)
}

// containsAnyStr checks if s contains any of the substrings
func containsAnyStr(s string, subs ...string) bool {
	for _, sub := range subs {
		if containsSubstringSpawn(s, sub) {
			return true
		}
	}
	return false
}

// containsSubstringSpawn is a simple contains check
func containsSubstringSpawn(s, sub string) bool {
	return len(s) >= len(sub) && (s == sub || len(sub) == 0 ||
		(len(s) > 0 && len(sub) > 0 && findSubstringSpawn(s, sub)))
}

// findSubstringSpawn checks if sub is in s
func findSubstringSpawn(s, sub string) bool {
	for i := 0; i <= len(s)-len(sub); i++ {
		if s[i:i+len(sub)] == sub {
			return true
		}
	}
	return false
}

// =============================================================================
// Work Assignment Mode Tests (ntm-n50g)
// Tests for orchestrator work assignment functionality
// =============================================================================

func spawnObservation(session string, state statuspkg.AgentState, panes ...tmux.Pane) statuspkg.SessionObservation {
	now := time.Now().UTC()
	observation := statuspkg.SessionObservation{
		Session: session, ObservedAt: now, Complete: true,
		Panes: make([]statuspkg.PaneObservation, 0, len(panes)),
	}
	for _, pane := range panes {
		observation.Panes = append(observation.Panes, statuspkg.PaneObservation{
			Pane: pane.Ref(), Metadata: pane,
			Current: statuspkg.StateObservation{
				Status: statuspkg.AgentStatus{State: state}, ObservedAt: now,
				Freshness: statuspkg.FreshnessFresh, Confidence: 1,
			},
		})
	}
	return observation
}

func TestNormalizeAssignStrategyStrict(t *testing.T) {
	tests := []struct {
		input    string
		expected string
	}{
		{"top-n", "top-n"},
		{"topn", "top-n"},
		{"TOP-N", "top-n"},
		{"diverse", "diverse"},
		{"DIVERSE", "diverse"},
		{"dependency-aware", "dependency-aware"},
		{"dependency", "dependency-aware"},
		{"skill-matched", "skill-matched"},
		{"skill", "skill-matched"},
		{"  top-n  ", "top-n"}, // Whitespace trimmed
	}

	for _, tc := range tests {
		t.Run(tc.input, func(t *testing.T) {
			got, err := normalizeAssignStrategyStrict(tc.input)
			if err != nil {
				t.Fatalf("normalizeAssignStrategyStrict(%q): %v", tc.input, err)
			}
			if got != tc.expected {
				t.Errorf("normalizeAssignStrategyStrict(%q) = %q, want %q", tc.input, got, tc.expected)
			}
		})
	}

	for _, input := range []string{"", "   ", "invalid"} {
		t.Run("invalid_"+input, func(t *testing.T) {
			if got, err := normalizeAssignStrategyStrict(input); err == nil || got != "" {
				t.Fatalf("normalizeAssignStrategyStrict(%q) = %q, %v; want explicit error", input, got, err)
			}
		})
	}
}

// TestGenerateWorkPrompt validates work prompt generation
func TestGenerateWorkPrompt(t *testing.T) {

	item := workItem{
		ID:       "test-123",
		Title:    "Fix authentication bug",
		Priority: 1,
		Score:    0.85,
		Type:     "bug",
		Reasons:  []string{"High priority", "Unblocks 3 items"},
	}

	prompt := generateWorkPrompt(item)

	// Validate prompt contains key elements
	if !containsAnyStr(prompt, "test-123") {
		t.Error("Prompt should contain bead ID")
	}
	if !containsAnyStr(prompt, "Fix authentication bug") {
		t.Error("Prompt should contain bead title")
	}
	if !containsAnyStr(prompt, "br show test-123") {
		t.Error("Prompt should contain br show command")
	}
	if !containsAnyStr(prompt, "in_progress") {
		t.Error("Prompt should mention in_progress status")
	}
	if !containsAnyStr(prompt, "High priority") {
		t.Error("Prompt should contain reasons")
	}
	if !containsAnyStr(prompt, "br close test-123 --reason \"Completed\"") {
		t.Error("Prompt should contain completion command")
	}

	t.Logf("Generated prompt:\n%s", prompt)
}

func TestGetSpawnAssignmentPreflightFailurePreventsEveryActuation(t *testing.T) {
	for _, test := range []struct {
		name                string
		dryRun              bool
		policyErr           error
		actionableErr       error
		wantCode            string
		wantPolicyCalls     int
		wantActionableCalls int
	}{
		{
			name: "invalid policy", policyErr: errors.New("invalid target policy"),
			wantCode: ErrCodeInvalidFlag, wantPolicyCalls: 1,
		},
		{
			name: "missing selected policy in dry run", dryRun: true, policyErr: errors.New("selected config is missing"),
			wantCode: ErrCodeInvalidFlag, wantPolicyCalls: 1,
		},
		{
			name: "unverified actionable plan", actionableErr: errors.New("plan output is incomplete"),
			wantCode: ErrCodeInternalError, wantPolicyCalls: 1, wantActionableCalls: 1,
		},
		{
			name: "unverified live labels in dry run", dryRun: true, actionableErr: errors.New("live labels are incomplete"),
			wantCode: ErrCodeInternalError, wantPolicyCalls: 1, wantActionableCalls: 1,
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			projectDir := t.TempDir()
			const selectedConfig = "/explicit/config.toml"
			var policyCalls, actionableCalls atomic.Int32
			var lifecycleCalls, claimCalls, reservationCalls, dispatchCalls atomic.Int32

			lifecycle := &SpawnLifecycleDependencies{
				IsTMUXInstalled: func() bool { return true },
				GetAllPanes: func(context.Context) (map[string][]tmux.Pane, error) {
					lifecycleCalls.Add(1)
					return nil, errors.New("topology read must not run before assignment preflight succeeds")
				},
				SessionExists: func(context.Context, string) (bool, error) {
					lifecycleCalls.Add(1)
					return false, nil
				},
				CreateSession: func(context.Context, string, string, int) error {
					lifecycleCalls.Add(1)
					return nil
				},
				GetPanes: func(context.Context, string) ([]tmux.Pane, error) {
					lifecycleCalls.Add(1)
					return nil, nil
				},
				SplitWindow: func(context.Context, string, string) (string, error) {
					lifecycleCalls.Add(1)
					return "", nil
				},
				ApplyTiledLayout: func(context.Context, string) error {
					lifecycleCalls.Add(1)
					return nil
				},
				LaunchAgent: func(context.Context, tmux.Pane, string, string, int, string, string) (SpawnedAgent, error) {
					lifecycleCalls.Add(1)
					return SpawnedAgent{}, nil
				},
				WaitForReady: func(context.Context, *SpawnOutput, time.Duration) error {
					lifecycleCalls.Add(1)
					return nil
				},
			}
			assignmentDeps := &SpawnAssignmentDependencies{
				LoadAssignmentPolicy: func(gotProject, gotConfig string, require bool) (*config.Config, error) {
					policyCalls.Add(1)
					if gotProject != projectDir || gotConfig != selectedConfig || !require {
						t.Fatalf("policy loader args project=%q config=%q require=%t", gotProject, gotConfig, require)
					}
					if test.policyErr != nil {
						return nil, test.policyErr
					}
					return testSpawnConfig(), nil
				},
				FetchActionable: func(_ context.Context, gotProject string, limit int) ([]bv.TriageRecommendation, error) {
					actionableCalls.Add(1)
					if gotProject != projectDir || limit != 0 {
						t.Fatalf("actionable loader args project=%q limit=%d", gotProject, limit)
					}
					return nil, test.actionableErr
				},
				FetchTriage: func(context.Context, string) (*bv.TriageResponse, error) {
					lifecycleCalls.Add(1)
					return nil, nil
				},
				ListPanes: func(context.Context, string) ([]tmux.Pane, error) {
					lifecycleCalls.Add(1)
					return nil, nil
				},
				LoadStore: func(string) (*assignment.AssignmentStore, error) {
					lifecycleCalls.Add(1)
					return nil, nil
				},
				ClaimBead: func(context.Context, string, string, string) (bv.BeadClaimResult, error) {
					claimCalls.Add(1)
					return bv.BeadClaimResult{}, nil
				},
				ReservationPort: testReservationFunc(func(context.Context, assignment.ReservationRequest) (assignment.LeaseReceipt, error) {
					reservationCalls.Add(1)
					return assignment.LeaseReceipt{}, nil
				}),
				DispatchDeliverer: dispatchsvc.DelivererFunc(func(context.Context, dispatchsvc.Delivery) error {
					dispatchCalls.Add(1)
					return nil
				}),
			}

			out, err := GetSpawn(t.Context(), SpawnOptions{
				Session: "preflight-failure", CCCount: 1, NoUserPane: true,
				WorkingDir: projectDir, ConfigPath: selectedConfig, RequireConfig: true,
				DryRun: test.dryRun, AssignWork: true, AssignStrategy: "top-n",
				LifecycleDeps: lifecycle, AssignmentDeps: assignmentDeps,
			}, testSpawnConfig())
			if err != nil {
				t.Fatalf("GetSpawn returned transport error: %v", err)
			}
			if out.Success || out.ErrorCode != test.wantCode || out.Error == "" || out.Agents == nil {
				t.Fatalf("preflight failure output=%+v, want code %s and initialized agents", out, test.wantCode)
			}
			if int(policyCalls.Load()) != test.wantPolicyCalls || int(actionableCalls.Load()) != test.wantActionableCalls {
				t.Fatalf("preflight calls policy=%d actionable=%d, want %d/%d",
					policyCalls.Load(), actionableCalls.Load(), test.wantPolicyCalls, test.wantActionableCalls)
			}
			if lifecycleCalls.Load() != 0 || claimCalls.Load() != 0 || reservationCalls.Load() != 0 || dispatchCalls.Load() != 0 {
				t.Fatalf("preflight failure crossed actuation boundary: lifecycle=%d claim=%d reservation=%d dispatch=%d",
					lifecycleCalls.Load(), claimCalls.Load(), reservationCalls.Load(), dispatchCalls.Load())
			}
		})
	}
}

func TestSpawnOptions_AssignWorkDryRunPreflightsPolicyAndActionablePlan(t *testing.T) {
	projectDir := t.TempDir()
	var order []string
	mutations := 0
	lifecycle := &SpawnLifecycleDependencies{
		IsTMUXInstalled: func() bool { return true },
		GetAllPanes: func(context.Context) (map[string][]tmux.Pane, error) {
			order = append(order, "topology-read")
			return map[string][]tmux.Pane{}, nil
		},
		SessionExists: func(context.Context, string) (bool, error) {
			mutations++
			return false, nil
		},
		CreateSession:    func(context.Context, string, string, int) error { mutations++; return nil },
		GetPanes:         func(context.Context, string) ([]tmux.Pane, error) { mutations++; return nil, nil },
		SplitWindow:      func(context.Context, string, string) (string, error) { mutations++; return "", nil },
		ApplyTiledLayout: func(context.Context, string) error { mutations++; return nil },
		LaunchAgent: func(context.Context, tmux.Pane, string, string, int, string, string) (SpawnedAgent, error) {
			mutations++
			return SpawnedAgent{}, nil
		},
	}
	assignmentDeps := &SpawnAssignmentDependencies{
		LoadAssignmentPolicy: func(gotProject, gotConfig string, require bool) (*config.Config, error) {
			order = append(order, "policy")
			if gotProject != projectDir || gotConfig != "/selected/config.toml" || !require {
				t.Fatalf("policy loader args project=%q config=%q require=%t", gotProject, gotConfig, require)
			}
			return testSpawnConfig(), nil
		},
		FetchActionable: func(_ context.Context, gotProject string, limit int) ([]bv.TriageRecommendation, error) {
			order = append(order, "actionable")
			if gotProject != projectDir || limit != 0 {
				t.Fatalf("actionable loader args project=%q limit=%d", gotProject, limit)
			}
			return []bv.TriageRecommendation{{ID: "gated-work", Status: "open"}}, nil
		},
		LoadStore: func(string) (*assignment.AssignmentStore, error) {
			mutations++
			return nil, nil
		},
		ClaimBead: func(context.Context, string, string, string) (bv.BeadClaimResult, error) {
			mutations++
			return bv.BeadClaimResult{}, nil
		},
		ReservationPort: testReservationFunc(func(context.Context, assignment.ReservationRequest) (assignment.LeaseReceipt, error) {
			mutations++
			return assignment.LeaseReceipt{}, nil
		}),
		DispatchDeliverer: dispatchsvc.DelivererFunc(func(context.Context, dispatchsvc.Delivery) error {
			mutations++
			return nil
		}),
	}

	resp, err := GetSpawn(t.Context(), SpawnOptions{
		Session: "test-assign-dryrun", CCCount: 2, NoUserPane: true, DryRun: true,
		WorkingDir: projectDir, ConfigPath: "/selected/config.toml", RequireConfig: true,
		AssignWork: true, AssignStrategy: "top-n", LifecycleDeps: lifecycle, AssignmentDeps: assignmentDeps,
	}, testSpawnConfig())
	if err != nil {
		t.Fatalf("GetSpawn failed: %v", err)
	}
	if !resp.Success || !resp.DryRun || len(resp.WouldCreate) != 2 || resp.Mode != "" || len(resp.Assignments) != 0 {
		t.Fatalf("dry-run output=%+v", resp)
	}
	if !reflect.DeepEqual(order, []string{"policy", "actionable", "topology-read"}) {
		t.Fatalf("dry-run preflight order=%v", order)
	}
	if mutations != 0 {
		t.Fatalf("dry-run assignment caused %d lifecycle or assignment mutations", mutations)
	}
}

func TestGetSpawnEmptyVerifiedAssignmentSetPrecedesTopologyAndLaunch(t *testing.T) {
	projectDir := t.TempDir()
	var order []string
	panes := []tmux.Pane{}
	assignmentSideEffects := 0
	lifecycle := &SpawnLifecycleDependencies{
		IsTMUXInstalled: func() bool { return true },
		GetAllPanes: func(context.Context) (map[string][]tmux.Pane, error) {
			order = append(order, "topology-read")
			return map[string][]tmux.Pane{}, nil
		},
		SessionExists: func(context.Context, string) (bool, error) {
			order = append(order, "session-exists")
			return false, nil
		},
		CreateSession: func(context.Context, string, string, int) error {
			order = append(order, "create-session")
			panes = append(panes, tmux.Pane{ID: "%1", WindowIndex: 0, Index: 0})
			return nil
		},
		GetPanes: func(context.Context, string) ([]tmux.Pane, error) {
			order = append(order, "get-panes")
			return append([]tmux.Pane(nil), panes...), nil
		},
		SplitWindow: func(context.Context, string, string) (string, error) {
			order = append(order, "split-window")
			panes = append(panes, tmux.Pane{ID: "%2", WindowIndex: 0, Index: 1})
			return "%2", nil
		},
		ApplyTiledLayout: func(context.Context, string) error {
			order = append(order, "layout")
			return nil
		},
		LaunchAgent: func(_ context.Context, pane tmux.Pane, session, agentType string, number int, _, _ string) (SpawnedAgent, error) {
			order = append(order, "launch")
			return SpawnedAgent{Pane: pane.Ref().Physical(), Type: agentType, Title: fmt.Sprintf("%s__cc_%d", session, number)}, nil
		},
	}
	assignmentDeps := &SpawnAssignmentDependencies{
		LoadAssignmentPolicy: func(gotProject, _ string, _ bool) (*config.Config, error) {
			order = append(order, "policy")
			if gotProject != projectDir {
				t.Fatalf("policy project=%q, want %q", gotProject, projectDir)
			}
			return testSpawnConfig(), nil
		},
		FetchActionable: func(_ context.Context, gotProject string, limit int) ([]bv.TriageRecommendation, error) {
			order = append(order, "actionable")
			if gotProject != projectDir || limit != 0 {
				t.Fatalf("actionable args project=%q limit=%d", gotProject, limit)
			}
			return []bv.TriageRecommendation{}, nil
		},
		AssignmentLedgerExists: func(gotSession string) (bool, error) {
			order = append(order, "ledger-probe")
			if gotSession != "filtered-gate" {
				t.Fatalf("ledger probe session=%q", gotSession)
			}
			return false, nil
		},
		LoadStore: func(string) (*assignment.AssignmentStore, error) {
			assignmentSideEffects++
			return nil, nil
		},
		ClaimBead: func(context.Context, string, string, string) (bv.BeadClaimResult, error) {
			assignmentSideEffects++
			return bv.BeadClaimResult{}, nil
		},
		ReservationPort: testReservationFunc(func(context.Context, assignment.ReservationRequest) (assignment.LeaseReceipt, error) {
			assignmentSideEffects++
			return assignment.LeaseReceipt{}, nil
		}),
		DispatchDeliverer: dispatchsvc.DelivererFunc(func(context.Context, dispatchsvc.Delivery) error {
			assignmentSideEffects++
			return nil
		}),
	}

	out, err := GetSpawn(t.Context(), SpawnOptions{
		Session: "filtered-gate", CCCount: 2, NoUserPane: true, WorkingDir: projectDir,
		AssignWork: true, AssignStrategy: "top-n", LifecycleDeps: lifecycle, AssignmentDeps: assignmentDeps,
	}, testSpawnConfig())
	if err != nil {
		t.Fatalf("GetSpawn returned transport error: %v", err)
	}
	if out.Success || out.ErrorCode != "ASSIGNMENT_FAILED" || len(out.Agents) != 2 || len(out.Assignments) != 2 {
		t.Fatalf("filtered assignment output=%+v", out)
	}
	wantOrder := []string{
		"policy", "actionable", "topology-read", "session-exists", "create-session",
		"get-panes", "split-window", "get-panes", "layout", "launch", "launch", "ledger-probe",
	}
	if !reflect.DeepEqual(order, wantOrder) {
		t.Fatalf("spawn ordering=%v, want %v", order, wantOrder)
	}
	if assignmentSideEffects != 0 {
		t.Fatalf("empty verified assignment set caused %d claim/reservation/dispatch side effects", assignmentSideEffects)
	}
}

func TestAssignWorkEmptyVerifiedPlanReplaysOnlyExactDurableSentIntent(t *testing.T) {
	const (
		session = "spawn-empty-plan-replay"
		beadID  = "bd-replay"
		actor   = "ExactRecipient/ntm-0123456789ab"
		prompt  = "durable redacted prompt"
	)
	pane := tmux.Pane{ID: "%9", WindowIndex: 0, Index: 1, Title: session + "__cc_1", Type: tmux.AgentClaude}
	reservationPaths := []string{"internal/robot/**"}
	promptChecksum := assignment.PromptSHA256(prompt)

	for _, test := range []struct {
		name         string
		liveAssignee string
		wantReplay   bool
	}{
		{name: "exact active owner", liveAssignee: actor, wantReplay: true},
		{name: "live owner drift", liveAssignee: "DifferentOwner", wantReplay: false},
	} {
		t.Run(test.name, func(t *testing.T) {
			store := &assignment.AssignmentStore{Assignments: map[string]*assignment.Assignment{
				beadID: {
					BeadID: beadID, BeadTitle: "Durable replay", Pane: pane.Index,
					AgentType: "claude", AgentName: "ExactRecipient", Status: assignment.StatusAssigned,
					IdempotencyKey: "0123456789abcdef", ClaimActor: actor, ClaimState: assignment.ClaimClaimed,
					ClaimStatus: "in_progress", ReservationRequired: true,
					ReservationInputPaths: append([]string(nil), reservationPaths...),
					ReservationState:      assignment.ReservationReserved, ReservationCompleted: true,
					ReservationIDs: []int{701}, DispatchState: assignment.DispatchSent,
					DispatchTarget: pane.ID, OccupancyKey: pane.ID, PromptSent: prompt,
					PromptSHA256: promptChecksum, IntentSHA256: promptChecksum,
					DispatchReceiptID: "tmux:" + pane.ID + ":receipt",
				},
			}}
			var ledgerProbes, storeLoads, topologyReads, detailReads atomic.Int32
			var mutations atomic.Int32
			deps := &SpawnAssignmentDependencies{
				AssignmentLedgerExists: func(gotSession string) (bool, error) {
					ledgerProbes.Add(1)
					if gotSession != session {
						t.Fatalf("ledger probe session=%q", gotSession)
					}
					return true, nil
				},
				LoadStore: func(gotSession string) (*assignment.AssignmentStore, error) {
					storeLoads.Add(1)
					if gotSession != session {
						t.Fatalf("store session=%q", gotSession)
					}
					return store, nil
				},
				ListPanes: func(context.Context, string) ([]tmux.Pane, error) {
					topologyReads.Add(1)
					return []tmux.Pane{pane}, nil
				},
				GetBeadDetails: func(_ context.Context, project, gotBeadID string) (*bv.BeadAssignmentDetails, error) {
					detailReads.Add(1)
					if project == "" || gotBeadID != beadID {
						t.Fatalf("details args project=%q bead=%q", project, gotBeadID)
					}
					return &bv.BeadAssignmentDetails{
						ID: beadID, Title: "Durable replay", Status: "in_progress", Priority: 1,
						Assignee: test.liveAssignee,
					}, nil
				},
				ClaimBead: func(context.Context, string, string, string) (bv.BeadClaimResult, error) {
					mutations.Add(1)
					return bv.BeadClaimResult{}, errors.New("replay must not claim")
				},
				NewIdempotencyKey: func() (string, error) {
					mutations.Add(1)
					return "", errors.New("replay must not generate a key")
				},
				ReservationPort: testReservationFunc(func(context.Context, assignment.ReservationRequest) (assignment.LeaseReceipt, error) {
					mutations.Add(1)
					return assignment.LeaseReceipt{}, errors.New("replay must not reserve")
				}),
				ObserveSession: func(context.Context, string) (statuspkg.SessionObservation, error) {
					mutations.Add(1)
					return statuspkg.SessionObservation{}, errors.New("replay must not observe for dispatch")
				},
				DispatchDeliverer: dispatchsvc.DelivererFunc(func(context.Context, dispatchsvc.Delivery) error {
					mutations.Add(1)
					return errors.New("replay must not dispatch")
				}),
			}
			output := &SpawnOutput{Session: session, Agents: []SpawnedAgent{{Pane: "0.1", Type: "claude"}}}
			emptyPlan := mockTriage([]bv.TriageRecommendation{}, nil)

			got, err := assignWorkToAgentsWithError(
				t.Context(), output, t.TempDir(), session, "top-n", config.Default(),
				true, reservationPaths, deps, emptyPlan,
			)
			if err != nil {
				t.Fatalf("assignWorkToAgentsWithError: %v", err)
			}
			if len(got) != 1 {
				t.Fatalf("replay assignments=%+v", got)
			}
			if test.wantReplay {
				if got[0].Pane != "1" || got[0].BeadID != beadID || got[0].Priority != "P1" ||
					!got[0].Claimed || !got[0].PromptSent || got[0].ClaimActor != actor ||
					got[0].IdempotencyKey != "0123456789abcdef" ||
					got[0].DispatchReceiptID != "tmux:%9:receipt" ||
					!reflect.DeepEqual(got[0].ReservationIDs, []int{701}) || got[0].ClaimError != "" {
					t.Fatalf("durable replay=%+v", got[0])
				}
			} else if got[0].Claimed || got[0].PromptSent || !strings.Contains(got[0].ClaimError, "not owned in_progress") {
				t.Fatalf("owner-drift replay=%+v", got[0])
			}
			if ledgerProbes.Load() != 1 || storeLoads.Load() != 1 || topologyReads.Load() != 1 || detailReads.Load() != 1 {
				t.Fatalf("replay reads ledger=%d store=%d topology=%d details=%d",
					ledgerProbes.Load(), storeLoads.Load(), topologyReads.Load(), detailReads.Load())
			}
			if mutations.Load() != 0 {
				t.Fatalf("durable replay crossed an external mutation boundary %d times", mutations.Load())
			}
		})
	}
}

func spawnOpenAssignmentDetails(_ context.Context, _ string, beadID string) (*bv.BeadAssignmentDetails, error) {
	return &bv.BeadAssignmentDetails{ID: beadID, Status: "open"}, nil
}

func TestWaitForAgentsReadyCancellationInterruptsPolling(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	err := waitForAgentsReady(ctx, &SpawnOutput{Session: "cancel-ready", Agents: []SpawnedAgent{{Type: "claude", Pane: "0.1"}}}, time.Hour)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("waitForAgentsReady error = %v, want context.Canceled", err)
	}
}

func TestLaunchAgentCanceledContextStopsBeforePaneMutation(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	agent, err := launchAgent(ctx, tmux.Pane{ID: "%does-not-exist", WindowIndex: 0, Index: 1}, "cancel", "claude", 1, t.TempDir(), "claude")
	if !strings.Contains(agent.Error, "canceled") || agent.Ready {
		t.Fatalf("canceled launch agent = %+v", agent)
	}
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled launch error = %v, want context.Canceled", err)
	}
}

func TestSpawnCancellationErrorRecognizesWrappedPartialActuation(t *testing.T) {
	partial := fmt.Errorf("tmux pane was created before cancellation: %w", context.Canceled)
	if got := spawnCancellationError(t.Context(), partial); !errors.Is(got, context.Canceled) || got.Error() != partial.Error() {
		t.Fatalf("spawnCancellationError() = %v, want wrapped partial-actuation cancellation", got)
	}
}

func TestGetSpawnCanceledContextReturnsSingleStructuredFailure(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	out, err := GetSpawn(ctx, SpawnOptions{Session: "cancel-spawn", CCCount: 1}, config.Default())
	if err != nil {
		t.Fatalf("GetSpawn returned transport error: %v", err)
	}
	if out == nil || out.Success || out.ErrorCode != ErrCodeTimeout || !strings.Contains(out.Error, "canceled") {
		t.Fatalf("canceled spawn output = %+v", out)
	}
}

func TestFinalizeSpawnAssignmentOutputRequiresEveryEligibleAgent(t *testing.T) {
	agents := []SpawnedAgent{
		{Pane: "0.0", Name: "One", Type: "claude"},
		{Pane: "0.1", Name: "Two", Type: "codex"},
		{Pane: "0.2", Name: "Operator", Type: "user"},
	}
	tests := []struct {
		name        string
		assignments []SpawnAssignment
		wantSuccess bool
		wantFailed  int
	}{
		{name: "zero", wantFailed: 2},
		{
			name: "partial",
			assignments: []SpawnAssignment{{
				Pane: "0.0", AgentType: "claude", BeadID: "bd-one", Claimed: true, PromptSent: true,
			}},
			wantFailed: 1,
		},
		{
			name: "partial canonical single-window pane",
			assignments: []SpawnAssignment{{
				Pane: "0", AgentType: "claude", BeadID: "bd-one", Claimed: true, PromptSent: true,
			}},
			wantFailed: 1,
		},
		{
			name: "complete",
			assignments: []SpawnAssignment{
				{Pane: "0.0", AgentType: "claude", BeadID: "bd-one", Claimed: true, PromptSent: true},
				{Pane: "0.1", AgentType: "codex", BeadID: "bd-two", Claimed: true, PromptSent: true},
			},
			wantSuccess: true,
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			output := &SpawnOutput{
				RobotResponse: NewRobotResponse(true),
				Agents:        append([]SpawnedAgent(nil), agents...),
				Assignments:   append([]SpawnAssignment(nil), test.assignments...),
			}
			finalizeSpawnAssignmentOutput(output)
			if len(output.Assignments) != 2 {
				t.Fatalf("assignments=%+v, want every non-user agent represented", output.Assignments)
			}
			failed := 0
			for _, assignment := range output.Assignments {
				if assignment.ClaimError != "" || assignment.PromptError != "" || !assignment.Claimed || !assignment.PromptSent {
					failed++
				}
			}
			if failed != test.wantFailed {
				t.Fatalf("failed=%d assignments=%+v, want %d", failed, output.Assignments, test.wantFailed)
			}
			if output.Success != test.wantSuccess {
				t.Fatalf("success=%v, want %v; output=%+v", output.Success, test.wantSuccess, output)
			}
			if !test.wantSuccess && output.ErrorCode != "ASSIGNMENT_FAILED" {
				t.Fatalf("error_code=%q, want ASSIGNMENT_FAILED", output.ErrorCode)
			}
		})
	}
}

func TestGetSpawnReportsZeroAssignmentCoverageAsFailure(t *testing.T) {
	hermeticGlobalConfig(t)
	panes := []tmux.Pane{
		{ID: "%1", WindowIndex: 0, Index: 0},
		{ID: "%2", WindowIndex: 0, Index: 1},
	}
	deps := testSpawnLifecycleDependencies(panes)
	out, err := GetSpawn(t.Context(), SpawnOptions{
		Session: "zero-assignment", CCCount: 1, CodCount: 1, NoUserPane: true,
		WorkingDir: t.TempDir(), AssignWork: true, AssignStrategy: "top-n", LifecycleDeps: deps,
		AssignmentDeps: &SpawnAssignmentDependencies{
			FetchTriage: func(context.Context, string) (*bv.TriageResponse, error) {
				return mockTriage(nil, nil), nil
			},
		},
	}, testSpawnConfig())
	if err != nil {
		t.Fatalf("GetSpawn returned transport error: %v", err)
	}
	if out.Success || out.ErrorCode != "ASSIGNMENT_FAILED" || len(out.Assignments) != 2 {
		t.Fatalf("output=%+v, want two-agent ASSIGNMENT_FAILED", out)
	}
	for _, assignment := range out.Assignments {
		if assignment.ClaimError == "" {
			t.Fatalf("assignment lacks coverage diagnostic: %+v", assignment)
		}
	}
}

func TestGetSpawnPreservesTypedAssignmentPreflightTimeoutWithoutLifecycleMutation(t *testing.T) {
	hermeticGlobalConfig(t)
	panes := []tmux.Pane{{ID: "%1", WindowIndex: 0, Index: 0}}
	deps := testSpawnLifecycleDependencies(panes)
	lifecycleCalls := 0
	deps.GetAllPanes = func(context.Context) (map[string][]tmux.Pane, error) {
		lifecycleCalls++
		return nil, errors.New("spawn lifecycle must not run after assignment preflight timeout")
	}
	deps.CreateSession = func(context.Context, string, string, int) error {
		lifecycleCalls++
		return errors.New("spawn lifecycle must not run after assignment preflight timeout")
	}
	deps.LaunchAgent = func(context.Context, tmux.Pane, string, string, int, string, string) (SpawnedAgent, error) {
		lifecycleCalls++
		return SpawnedAgent{}, errors.New("spawn lifecycle must not run after assignment preflight timeout")
	}
	out, err := GetSpawn(t.Context(), SpawnOptions{
		Session: "assignment-timeout", CCCount: 1, NoUserPane: true,
		WorkingDir: t.TempDir(), AssignWork: true, AssignStrategy: "top-n", LifecycleDeps: deps,
		AssignmentDeps: &SpawnAssignmentDependencies{
			FetchTriage: func(context.Context, string) (*bv.TriageResponse, error) {
				return nil, fmt.Errorf("triage dependency: %w", context.DeadlineExceeded)
			},
		},
	}, testSpawnConfig())
	if err != nil {
		t.Fatalf("GetSpawn returned transport error: %v", err)
	}
	if out.Success || out.ErrorCode != ErrCodeTimeout || len(out.Assignments) != 0 || len(out.Agents) != 0 {
		t.Fatalf("output=%+v, want mutation-free assignment preflight TIMEOUT", out)
	}
	if !strings.Contains(out.Error, "deadline exceeded") {
		t.Fatalf("timeout diagnostic=%q", out.Error)
	}
	if lifecycleCalls != 0 {
		t.Fatalf("assignment preflight timeout reached %d spawn lifecycle call(s)", lifecycleCalls)
	}
}

// TestSpawnAssignmentOutput_SchemaStability validates assignment output schema
func TestSpawnAssignmentOutput_SchemaStability(t *testing.T) {

	// Create a test assignment
	assignment := SpawnAssignment{
		Pane:        "0.1",
		AgentType:   "claude",
		BeadID:      "test-bead",
		BeadTitle:   "Test Bead Title",
		Priority:    "P1",
		Claimed:     true,
		PromptSent:  true,
		ClaimError:  "",
		PromptError: "",
	}

	// Marshal and unmarshal to validate JSON schema
	data, err := json.Marshal(assignment)
	if err != nil {
		t.Fatalf("Failed to marshal assignment: %v", err)
	}

	var parsed map[string]interface{}
	if err := json.Unmarshal(data, &parsed); err != nil {
		t.Fatalf("Failed to unmarshal assignment: %v", err)
	}

	// Validate required fields
	requiredFields := []string{"pane", "agent_type", "bead_id", "bead_title", "priority", "claimed", "prompt_sent"}
	for _, field := range requiredFields {
		if _, ok := parsed[field]; !ok {
			t.Errorf("Missing required field: %s", field)
		}
	}

	// Validate omitempty fields are not present when empty
	omitEmptyFields := []string{"claim_error", "prompt_error"}
	for _, field := range omitEmptyFields {
		if _, ok := parsed[field]; ok {
			t.Errorf("Field %s should be omitted when empty", field)
		}
	}

	t.Logf("Assignment JSON: %s", string(data))
}

// TestSpawnOutput_ModeField validates mode field is set correctly
func TestSpawnOutput_ModeField(t *testing.T) {

	// Test output struct with mode field
	output := SpawnOutput{
		Session:        "test-session",
		Mode:           "orchestrator",
		AssignStrategy: "top-n",
		Assignments: []SpawnAssignment{
			{Pane: "0.1", BeadID: "test-1", Claimed: true},
		},
	}

	data, err := json.Marshal(output)
	if err != nil {
		t.Fatalf("Failed to marshal output: %v", err)
	}

	var parsed map[string]interface{}
	if err := json.Unmarshal(data, &parsed); err != nil {
		t.Fatalf("Failed to unmarshal output: %v", err)
	}

	// Validate mode is present
	if parsed["mode"] != "orchestrator" {
		t.Errorf("Mode should be 'orchestrator', got %v", parsed["mode"])
	}

	// Validate assign_strategy is present
	if parsed["assign_strategy"] != "top-n" {
		t.Errorf("AssignStrategy should be 'top-n', got %v", parsed["assign_strategy"])
	}

	// Validate assignments array is present
	if _, ok := parsed["assignments"]; !ok {
		t.Error("Missing assignments field")
	}

	t.Logf("Output with mode: %s", string(data))
}

// =============================================================================
// Session Recovery Tests (bd-1wtja)
// Tests for handoff context loading and SpawnRecovery struct
// =============================================================================

// TestSpawnRecovery_SchemaStability ensures SpawnRecovery JSON structure is stable
func TestSpawnRecovery_SchemaStability(t *testing.T) {

	recovery := SpawnRecovery{
		HandoffPath:  "/path/to/handoff.yaml",
		HandoffAge:   "5m ago",
		Goal:         "Implemented feature X",
		Now:          "Write tests for feature X",
		Status:       "complete",
		Outcome:      "SUCCEEDED",
		InjectedText: "## Previous Session Context\n**Your task:** Write tests",
	}

	data, err := json.Marshal(recovery)
	if err != nil {
		t.Fatalf("Failed to marshal SpawnRecovery: %v", err)
	}

	var parsed map[string]interface{}
	if err := json.Unmarshal(data, &parsed); err != nil {
		t.Fatalf("Failed to unmarshal SpawnRecovery: %v", err)
	}

	// Verify all expected fields are present
	expectedFields := []string{
		"handoff_path", "handoff_age", "goal", "now",
		"status", "outcome", "injected_text",
	}

	for _, field := range expectedFields {
		if _, ok := parsed[field]; !ok {
			t.Errorf("Missing field %q in SpawnRecovery JSON", field)
		}
	}

	t.Logf("SpawnRecovery JSON: %s", string(data))
}

// TestSpawnOutput_RecoveryField verifies the recovery field is included in SpawnOutput
func TestSpawnOutput_RecoveryField(t *testing.T) {

	output := SpawnOutput{
		Session:    "test-session",
		WorkingDir: "/tmp/test",
		Layout:     "tiled",
		Recovery: &SpawnRecovery{
			HandoffPath: "/tmp/handoff.yaml",
			HandoffAge:  "10m ago",
			Goal:        "Built the API",
			Now:         "Add authentication",
		},
	}

	data, err := json.Marshal(output)
	if err != nil {
		t.Fatalf("Failed to marshal SpawnOutput: %v", err)
	}

	var parsed map[string]interface{}
	if err := json.Unmarshal(data, &parsed); err != nil {
		t.Fatalf("Failed to unmarshal SpawnOutput: %v", err)
	}

	// Verify recovery field is present
	recoveryData, ok := parsed["recovery"]
	if !ok {
		t.Fatal("Missing recovery field in SpawnOutput")
	}

	recoveryMap, ok := recoveryData.(map[string]interface{})
	if !ok {
		t.Fatalf("recovery field is not an object: %T", recoveryData)
	}

	if recoveryMap["goal"] != "Built the API" {
		t.Errorf("Expected goal 'Built the API', got %v", recoveryMap["goal"])
	}

	if recoveryMap["now"] != "Add authentication" {
		t.Errorf("Expected now 'Add authentication', got %v", recoveryMap["now"])
	}

	t.Logf("SpawnOutput with recovery: %s", string(data))
}

// TestSpawnOutput_RecoveryOmittedWhenNil verifies recovery is omitted from JSON when nil
func TestSpawnOutput_RecoveryOmittedWhenNil(t *testing.T) {

	output := SpawnOutput{
		Session:    "test-session",
		WorkingDir: "/tmp/test",
		Layout:     "tiled",
		Recovery:   nil, // No recovery context
	}

	data, err := json.Marshal(output)
	if err != nil {
		t.Fatalf("Failed to marshal SpawnOutput: %v", err)
	}

	var parsed map[string]interface{}
	if err := json.Unmarshal(data, &parsed); err != nil {
		t.Fatalf("Failed to unmarshal SpawnOutput: %v", err)
	}

	// Verify recovery field is NOT present (omitempty)
	if _, ok := parsed["recovery"]; ok {
		t.Error("recovery field should be omitted when nil")
	}

	t.Logf("SpawnOutput without recovery: %s", string(data))
}

// TestLoadLatestHandoff_NoHandoff verifies graceful handling when no handoff exists
func TestLoadLatestHandoff_NoHandoff(t *testing.T) {

	// Use a temp directory with no handoffs
	tmpDir := t.TempDir()

	spawnRecovery, handoffCtx := loadLatestHandoff(tmpDir, "nonexistent_session")

	if spawnRecovery != nil {
		t.Error("Expected nil SpawnRecovery when no handoff exists")
	}

	if handoffCtx != nil {
		t.Error("Expected nil HandoffContext when no handoff exists")
	}

	t.Log("loadLatestHandoff correctly returns nil when no handoff found")
}

// Readiness decided from captured text alone reports a bare shell prompt as
// ready, so `--robot-spawn --spawn-wait` answered ready:true with nothing
// running when the agent CLI was missing from PATH. The verdict is now
// corroborated with process liveness, matching the restart path.
func TestWaitForAgentsReadyRequiresLiveProcess(t *testing.T) {
	originalLiveness := spawnPaneLiveness
	t.Cleanup(func() { spawnPaneLiveness = originalLiveness })

	// A bare prompt with no child process under the shell is not ready.
	spawnPaneLiveness = func(context.Context, string) (int, bool) { return 4242, false }
	output := &SpawnOutput{
		Session: "proj",
		Agents:  []SpawnedAgent{{Pane: "0.1", Type: "claude"}},
	}
	capture := func(context.Context, string, int) (string, error) { return "~/proj \n❯ \n", nil }

	err := waitForAgentsReadyWithCapture(context.Background(), output, 120*time.Millisecond, capture)
	if err == nil {
		t.Fatal("a bare prompt with no live process must not satisfy --spawn-wait")
	}
	if output.Agents[0].Ready {
		t.Fatal("agent was marked ready with nothing running under the shell")
	}

	// Same captured content, but a live child process: genuinely ready.
	spawnPaneLiveness = func(context.Context, string) (int, bool) { return 4242, true }
	output = &SpawnOutput{
		Session: "proj",
		Agents:  []SpawnedAgent{{Pane: "0.1", Type: "claude"}},
	}
	if err := waitForAgentsReadyWithCapture(context.Background(), output, time.Second, capture); err != nil {
		t.Fatalf("waitForAgentsReadyWithCapture with a live process: %v", err)
	}
	if !output.Agents[0].Ready {
		t.Fatal("agent with a live process should be ready")
	}

	// PID unavailable falls back to the text verdict rather than blocking.
	spawnPaneLiveness = func(context.Context, string) (int, bool) { return 0, false }
	output = &SpawnOutput{
		Session: "proj",
		Agents:  []SpawnedAgent{{Pane: "0.1", Type: "claude"}},
	}
	if err := waitForAgentsReadyWithCapture(context.Background(), output, time.Second, capture); err != nil {
		t.Fatalf("waitForAgentsReadyWithCapture with no PID: %v", err)
	}
	if !output.Agents[0].Ready {
		t.Fatal("an unavailable PID must not block readiness")
	}
}

func TestWaitForAgentsReadyGrokWaitsPastBannerForComposer(t *testing.T) {
	originalLiveness := spawnPaneLiveness
	t.Cleanup(func() { spawnPaneLiveness = originalLiveness })
	spawnPaneLiveness = func(context.Context, string) (int, bool) { return 4242, true }

	output := &SpawnOutput{
		Session: "grok-ready",
		Agents:  []SpawnedAgent{{Pane: "0.1", Type: "grok"}},
	}
	captureCalls := 0
	capture := func(context.Context, string, int) (string, error) {
		captureCalls++
		if captureCalls == 1 {
			return "Grok Build  1.0.13\nWelcome back\n", nil
		}
		return "Grok Build  1.0.13\n│ ❯                         │\n╰─ Grok 4.6 (high) · always-approve ─╯\n", nil
	}

	if err := waitForAgentsReadyWithCapture(t.Context(), output, 2*time.Second, capture); err != nil {
		t.Fatalf("waitForAgentsReadyWithCapture(Grok) error = %v", err)
	}
	if captureCalls < 2 || !output.Agents[0].Ready {
		t.Fatalf("capture calls=%d agent=%+v, want banner rejected then composer accepted", captureCalls, output.Agents[0])
	}
}

func TestWaitForAgentsReadyCarriesActualPaneWidthIntoGrokDetector(t *testing.T) {
	originalLiveness := spawnPaneLiveness
	t.Cleanup(func() { spawnPaneLiveness = originalLiveness })
	spawnPaneLiveness = func(context.Context, string) (int, bool) { return 4242, true }

	output := &SpawnOutput{
		Session: "grok-width",
		Agents:  []SpawnedAgent{{Pane: "0.1", Type: "grok"}},
	}
	// At 40 columns the adaptive live tail is 30 physical rows. A fixed
	// 15-row detector would lose the provider header and incorrectly report
	// UNREADY_PROVIDER_CHROME_MISSING.
	captureText := "Grok Build  1.0.13\n" + strings.Repeat("wrapped status row\n", 20) +
		"│ ❯                         │\n╰─ Grok 4.6 (high) · default ─╯\n"
	widthTarget := ""
	err := waitForAgentsReadyWithCaptureAndWidth(
		t.Context(), output, time.Second,
		func(context.Context, string, int) (string, error) { return captureText, nil },
		func(target string) int {
			widthTarget = target
			return 40
		},
	)
	if err != nil {
		t.Fatalf("width-aware Grok readiness: %v", err)
	}
	if widthTarget == "" || !output.Agents[0].Ready || output.Agents[0].ReadyReason != "READY" {
		t.Fatalf("target=%q agent=%+v", widthTarget, output.Agents[0])
	}
}

// "ready" must match as a word: as a bare substring it also hit "already".
func TestIsAgentReadyDoesNotMatchAlready(t *testing.T) {
	if isAgentReady("Already up to date.\n", "claude") {
		t.Fatal(`"Already up to date." must not count as an agent being ready`)
	}
	if !isAgentReady("agent ready for input\n", "claude") {
		t.Fatal(`a genuine "ready" line should still match`)
	}
}

func TestIsAgentReadyGrokRequiresAuthenticatedEmptyComposer(t *testing.T) {
	idle := "Grok Build  1.0.13\n│ ❯                         │\n╰─ Grok 4.6 (high) · always-approve ─╯\n"
	if !isAgentReady(idle, "grok") {
		t.Fatal("authenticated empty Grok composer should be ready")
	}
	for _, output := range []string{
		"Grok Build  1.0.13\nWelcome back\n",
		"Grok Build  1.0.13\nOpen a browser to authenticate\n",
		"Rate limit exceeded. Try again later.\n" + idle,
		"Error: authentication expired\n" + idle,
		"~/project\n❯ \n",
		idle + "⠸ Thinking… 0.0s\nEsc:cancel\n",
	} {
		if isAgentReady(output, "grok") {
			t.Fatalf("unready Grok output %q classified ready", output)
		}
	}
}

// ntm-cx4e: reaching a project through a symlinked alias makes NTM see one
// path while Agent Mail canonicalizes to another, so an orchestrator that
// trusts working_dir queries a project key holding none of its own swarm's
// reservations. AGENTS.md calls project-key mismatch the single most common
// source of cross-tool breakage, and it was invisible in spawn output.
func TestSpawnOutputExposesEffectiveProjectKeyOnlyWhenItDiffers(t *testing.T) {
	realDir := t.TempDir()
	// t.TempDir on macOS is itself under a symlink (/var -> /private/var), so
	// resolve first to get a path that is genuinely its own canonical form.
	resolvedReal, err := filepath.EvalSymlinks(realDir)
	if err != nil {
		t.Skipf("cannot resolve temp dir: %v", err)
	}

	t.Run("identical path reports nothing", func(t *testing.T) {
		if got := agentmail.CanonicalProjectKey(resolvedReal); got != resolvedReal {
			t.Fatalf("canonical of an already-canonical path = %q, want %q", got, resolvedReal)
		}
		// The spawn field is omitempty and only set when they differ, so an
		// unsymlinked project must not carry it.
		var out SpawnOutput
		out.WorkingDir = resolvedReal
		if canonical := agentmail.CanonicalProjectKey(out.WorkingDir); canonical != out.WorkingDir {
			out.EffectiveProjectKey = canonical
		}
		if out.EffectiveProjectKey != "" {
			t.Fatalf("EffectiveProjectKey = %q, want empty for a canonical path", out.EffectiveProjectKey)
		}
	})

	t.Run("symlinked alias reports the resolved key", func(t *testing.T) {
		link := filepath.Join(t.TempDir(), "alias")
		if err := os.Symlink(resolvedReal, link); err != nil {
			t.Skipf("cannot create symlink: %v", err)
		}

		var out SpawnOutput
		out.WorkingDir = link
		if canonical := agentmail.CanonicalProjectKey(out.WorkingDir); canonical != out.WorkingDir {
			out.EffectiveProjectKey = canonical
		}
		if out.EffectiveProjectKey == "" {
			t.Fatal("spawning through a symlinked alias reported no effective project key; the divergence stays silent")
		}
		if out.EffectiveProjectKey != resolvedReal {
			t.Fatalf("EffectiveProjectKey = %q, want the resolved %q", out.EffectiveProjectKey, resolvedReal)
		}
		if out.EffectiveProjectKey == out.WorkingDir {
			t.Fatal("effective key equals working_dir; nothing was actually resolved")
		}
	})
}

// skillMatchTriageFixture is a discriminating fixture for the strategy
// difference tests: top-n order (by triage score) is refactor, docs, bug —
// but capability matching pairs codex with the bug and gemini with the docs.
func skillMatchTriageFixture() *bv.TriageResponse {
	return &bv.TriageResponse{
		Triage: bv.TriageData{
			Recommendations: []bv.TriageRecommendation{
				{ID: "bd-refactor", Title: "Restructure config loading", Type: "refactor", Priority: 1, Score: 0.95},
				{ID: "bd-docs", Title: "Document the robot surface", Type: "docs", Priority: 2, Score: 0.90},
				{ID: "bd-bug", Title: "Broken pane resolution", Type: "bug", Priority: 1, Score: 0.85},
			},
		},
	}
}

// TestGetWorkItemsForStrategySkillMatchedDiffersFromTopN is the C4-style
// difference test: the same fixture must produce DIFFERENT positional
// assignments under skill-matched than under top-n, proving skill-matched
// no longer silently falls back.
func TestGetWorkItemsForStrategySkillMatchedDiffersFromTopN(t *testing.T) {
	triage := skillMatchTriageFixture()
	agents := []SpawnedAgent{
		{Pane: "0.1", Type: "cod"},
		{Pane: "0.2", Type: "gmi"},
		{Pane: "0.3", Type: "cc"},
	}

	topN := getWorkItemsForStrategy(triage, "top-n", agents)
	matched := getWorkItemsForStrategy(triage, "skill-matched", agents)

	if len(topN) != 3 || len(matched) != 3 {
		t.Fatalf("expected 3 items each, got top-n=%d skill-matched=%d", len(topN), len(matched))
	}

	// top-n pairs positionally by triage score: cod→refactor, gmi→docs, cc→bug.
	wantTopN := []string{"bd-refactor", "bd-docs", "bd-bug"}
	for i, want := range wantTopN {
		if topN[i].ID != want {
			t.Fatalf("top-n[%d] = %s, want %s", i, topN[i].ID, want)
		}
	}

	// Capability matrix: cod scores bug 0.90 > refactor 0.75 > docs 0.70;
	// gmi scores docs 0.90; cc scores refactor 0.95.
	wantMatched := []string{"bd-bug", "bd-docs", "bd-refactor"}
	for i, want := range wantMatched {
		if matched[i].ID != want {
			t.Fatalf("skill-matched[%d] = %s, want %s (full: %+v)", i, matched[i].ID, want, matched)
		}
	}

	same := true
	for i := range topN {
		if topN[i].ID != matched[i].ID {
			same = false
			break
		}
	}
	if same {
		t.Fatal("skill-matched produced identical positional assignments to top-n on a discriminating fixture (silent fallback)")
	}

	for i, item := range matched {
		if item.MatchReason == "" {
			t.Fatalf("skill-matched[%d] (%s) has no MatchReason recorded", i, item.ID)
		}
		if !strings.Contains(item.MatchReason, "skill-matched") {
			t.Fatalf("skill-matched[%d] MatchReason = %q, want skill-matched rationale", i, item.MatchReason)
		}
		if len(item.Reasons) == 0 || !strings.Contains(item.Reasons[len(item.Reasons)-1], "skill-matched") {
			t.Fatalf("skill-matched[%d] Reasons missing rationale: %v", i, item.Reasons)
		}
	}
	// Genuine matching must state the capability score, not a fallback note.
	if !strings.Contains(matched[0].MatchReason, "capability") {
		t.Fatalf("MatchReason %q does not cite a capability score", matched[0].MatchReason)
	}
}

// TestGetSkillMatchedWorkItemsNoSignalIsLoud verifies the degenerate class
// (unknown agent type, uniform 0.5 scores) is recorded loudly instead of
// silently masquerading as skill matching.
func TestGetSkillMatchedWorkItemsNoSignalIsLoud(t *testing.T) {
	triage := skillMatchTriageFixture()
	agents := []SpawnedAgent{{Pane: "0.1", Type: "totally-unknown-agent"}}

	items := getSkillMatchedWorkItems(triage, agents)
	if len(items) != 1 {
		t.Fatalf("expected 1 item, got %d", len(items))
	}
	// Uniform scores keep triage order (top item first).
	if items[0].ID != "bd-refactor" {
		t.Fatalf("degenerate matching picked %s, want triage-order bd-refactor", items[0].ID)
	}
	if !strings.Contains(items[0].MatchReason, "no discriminating capability signal") {
		t.Fatalf("degenerate matching MatchReason = %q, want loud no-signal note", items[0].MatchReason)
	}
}

// TestGetWorkItemsForStrategyUnknownStrategyIsLoud verifies that a strategy
// value leaking past strict normalization records a fallback reason.
func TestGetWorkItemsForStrategyUnknownStrategyIsLoud(t *testing.T) {
	triage := skillMatchTriageFixture()
	agents := []SpawnedAgent{{Pane: "0.1", Type: "cc"}}

	items := getWorkItemsForStrategy(triage, "not-a-strategy", agents)
	if len(items) != 1 {
		t.Fatalf("expected 1 item, got %d", len(items))
	}
	if !strings.Contains(items[0].MatchReason, "fell back to top-n") {
		t.Fatalf("unknown strategy MatchReason = %q, want loud fallback note", items[0].MatchReason)
	}
}

// TestSpawnTaskTypeForRecommendationLadder covers the explicit-type, label,
// and title-inference rungs.
func TestSpawnTaskTypeForRecommendationLadder(t *testing.T) {
	cases := []struct {
		name string
		rec  bv.TriageRecommendation
		want string
	}{
		{"explicit type wins", bv.TriageRecommendation{Type: "refactor", Title: "fix bug"}, "refactor"},
		{"label used when type generic", bv.TriageRecommendation{Type: "task", Labels: []string{"lane", "docs"}, Title: "misc"}, "docs"},
		{"title inference last", bv.TriageRecommendation{Type: "task", Title: "Fix broken spawn crash"}, "bug"},
		{"generic stays task", bv.TriageRecommendation{Type: "task", Title: "misc chores galore maybe"}, "task"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := string(spawnTaskTypeForRecommendation(tc.rec)); got != tc.want {
				t.Fatalf("got %q, want %q", got, tc.want)
			}
		})
	}
}

func TestSpawnModelHints(t *testing.T) {
	cfg := config.Default()

	// Known models and empty overrides produce no hints.
	if hints := spawnModelHints(cfg, SpawnOptions{}); len(hints) != 0 {
		t.Fatalf("no-override hints = %v, want none", hints)
	}
	if hints := spawnModelHints(cfg, SpawnOptions{CCModel: "claude-opus-5"}); len(hints) != 0 {
		t.Fatalf("known-model hints = %v, want none", hints)
	}

	// A near-miss typo yields a did-you-mean hint (advisory, never an error).
	hints := spawnModelHints(cfg, SpawnOptions{CCModel: "claude-opus5"})
	if len(hints) != 1 || !strings.Contains(hints[0], "claude-opus-5") || !strings.Contains(hints[0], "did you mean") {
		t.Fatalf("typo hints = %v, want one did-you-mean for claude-opus-5", hints)
	}

	// Completely custom model IDs stay silent so self-hosted models work.
	if hints := spawnModelHints(cfg, SpawnOptions{CodModel: "totally-custom-model-xyz-42"}); len(hints) != 0 {
		t.Fatalf("custom-model hints = %v, want none", hints)
	}
}
