package config_test

// WS0-G2 liveness claim linkage (WS6-wire, bd-ws6-config-truth-ienmd.1).
//
// RegisterReader claims live in the CONSUMING packages (a claim is a real
// function reference from the package that reads the key). Those init()
// registrations only reach the readerRegistry that TestConfigKeyLiveness
// inspects when the consuming packages are linked into this test binary, so
// this external-test file blank-imports every claiming package. New claims in
// new packages must be added here.
import (
	_ "github.com/Dicklesworthstone/ntm/internal/agentmail"       // retry.agent_mail.*
	_ "github.com/Dicklesworthstone/ntm/internal/cli"             // cli-consumed keys (liveness_claims.go) + memory.send_* + cass.*
	_ "github.com/Dicklesworthstone/ntm/internal/context"         // context_rotation.*
	_ "github.com/Dicklesworthstone/ntm/internal/coordinator"     // integrations.caam.* + rotation.thresholds.restart_*
	_ "github.com/Dicklesworthstone/ntm/internal/integrations/pt" // integrations.process_triage.*
	_ "github.com/Dicklesworthstone/ntm/internal/privacy"         // privacy.*
	_ "github.com/Dicklesworthstone/ntm/internal/quota"           // rotation.thresholds.{warning,critical}_percent
	_ "github.com/Dicklesworthstone/ntm/internal/resilience"      // resilience.* + rotation gates + notifications.*
	_ "github.com/Dicklesworthstone/ntm/internal/robot"           // robot-consumed keys (liveness_claims.go) + retry.alerts.*
	_ "github.com/Dicklesworthstone/ntm/internal/scanner"         // scanner.ubs_path
	_ "github.com/Dicklesworthstone/ntm/internal/swarm"           // swarm tiers + agents.claude_isolate_*
	_ "github.com/Dicklesworthstone/ntm/internal/tui/dashboard"   // theme, help_verbosity, rano, compaction recovery
	_ "github.com/Dicklesworthstone/ntm/internal/webhook"         // retry globals + retry.webhook.*
)
