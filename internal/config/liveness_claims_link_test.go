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
	_ "github.com/Dicklesworthstone/ntm/internal/agentmail"   // retry.agent_mail.*
	_ "github.com/Dicklesworthstone/ntm/internal/cli"         // memory.send_* (robot send injection)
	_ "github.com/Dicklesworthstone/ntm/internal/coordinator" // rotation.thresholds.restart_*
	_ "github.com/Dicklesworthstone/ntm/internal/quota"       // rotation.thresholds.{warning,critical}_percent
	_ "github.com/Dicklesworthstone/ntm/internal/robot"       // retry.alerts.*
	_ "github.com/Dicklesworthstone/ntm/internal/webhook"     // retry globals + retry.webhook.*
)
