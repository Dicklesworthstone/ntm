package cli

// Tests for --with-cass / --no-cass send-time CASS injection wiring (C10,
// bd-ws2-wire-or-delete-ykmcz.11): flag/config precedence mirrors
// --with-memory, [cass.context] keys really parameterize the engines (the
// G2 liveness claims are honest), and the degraded path records a skip.

import (
	"strings"
	"testing"
	"time"

	"github.com/Dicklesworthstone/ntm/internal/cass"
	"github.com/Dicklesworthstone/ntm/internal/config"
)

func cassTestConfig(topEnabled, ctxEnabled bool) *config.Config {
	cfg := config.Default()
	cfg.CASS.Enabled = topEnabled
	cfg.CASS.Context.Enabled = ctxEnabled
	return cfg
}

func TestResolveSendCASSEnabled_Precedence(t *testing.T) {
	cases := []struct {
		name     string
		withFlag bool
		noFlag   bool
		cfg      *config.Config
		want     bool
	}{
		{"default off", false, false, cassTestConfig(true, false), false},
		{"flag turns on", true, false, cassTestConfig(true, false), true},
		{"config default on", false, false, cassTestConfig(true, true), true},
		{"context on but cass disabled", false, false, cassTestConfig(false, true), false},
		{"no-cass overrides config on", false, true, cassTestConfig(true, true), false},
		{"no-cass overrides explicit with-cass", true, true, cassTestConfig(true, true), false},
		{"nil config flag on", true, false, nil, true},
		{"nil config default off", false, false, nil, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := resolveSendCASSEnabled(tc.withFlag, tc.noFlag, tc.cfg); got != tc.want {
				t.Fatalf("resolveSendCASSEnabled(%v,%v) = %v, want %v", tc.withFlag, tc.noFlag, got, tc.want)
			}
		})
	}
}

// TestRobotSendCASSOptions_ConfigKeysParameterizeEngine flips every claimed
// [cass.context] key (plus [cass] binary_path/timeout) and observes it land
// in the robot engine configs — the liveness claims are readers in fact.
func TestRobotSendCASSOptions_ConfigKeysParameterizeEngine(t *testing.T) {
	cfg := cassTestConfig(true, true)
	cfg.CASS.BinaryPath = "/opt/bin/cass"
	cfg.CASS.Timeout = 7
	cfg.CASS.Context.MaxSessions = 9
	cfg.CASS.Context.LookbackDays = 14
	cfg.CASS.Context.MaxTokens = 1234
	cfg.CASS.Context.MinRelevance = 0.42
	cfg.CASS.Context.SkipIfContextAbove = 77
	cfg.CASS.Context.PreferSameProject = false

	enabled, query, filter, inject := robotSendCASSOptions(false, false, cfg)
	if !enabled {
		t.Fatal("enabled = false, want true via [cass.context] enabled")
	}
	if query.BinaryPath != "/opt/bin/cass" {
		t.Errorf("BinaryPath = %q", query.BinaryPath)
	}
	if query.Timeout != 7*time.Second {
		t.Errorf("Timeout = %v", query.Timeout)
	}
	if query.MaxResults != 9 || filter.MaxItems != 9 {
		t.Errorf("max_sessions not applied: query %d filter %d", query.MaxResults, filter.MaxItems)
	}
	if query.MaxAgeDays != 14 || filter.MaxAgeDays != 14 {
		t.Errorf("lookback_days not applied: query %d filter %d", query.MaxAgeDays, filter.MaxAgeDays)
	}
	if inject.MaxTokens != 1234 {
		t.Errorf("max_tokens not applied: %d", inject.MaxTokens)
	}
	if query.MinRelevance != 0.42 || filter.MinRelevance != 0.42 {
		t.Errorf("min_relevance not applied: query %v filter %v", query.MinRelevance, filter.MinRelevance)
	}
	if inject.SkipThreshold != 77 {
		t.Errorf("skip_if_context_above not applied: %d", inject.SkipThreshold)
	}
	if query.PreferSameProject || filter.PreferSameProject {
		t.Error("prefer_same_project=false not applied")
	}

	// [cass] enabled=false degrades to a recorded skip inside the engine
	// (query disabled) rather than vetoing an explicit --with-cass.
	cfg.CASS.Enabled = false
	enabled, query, _, _ = robotSendCASSOptions(true, false, cfg)
	if !enabled {
		t.Fatal("explicit --with-cass must stay enabled (degrades in engine)")
	}
	if query.Enabled {
		t.Fatal("query.Enabled should be false when [cass] enabled=false")
	}
}

func TestSendCASSInjectionConfigs_MirrorsRobotMapping(t *testing.T) {
	cfg := cassTestConfig(true, true)
	cfg.CASS.Context.MaxSessions = 3
	cfg.CASS.Context.MaxTokens = 800
	cfg.CASS.Context.MinRelevance = 0.5

	enabled, query, filter, inject := sendCASSInjectionConfigs(false, false, cfg)
	if !enabled {
		t.Fatal("enabled = false, want true via config default")
	}
	if query.MaxResults != 3 || filter.MaxItems != 3 {
		t.Errorf("max_sessions not applied: %d/%d", query.MaxResults, filter.MaxItems)
	}
	if inject.MaxTokens != 800 {
		t.Errorf("max_tokens not applied: %d", inject.MaxTokens)
	}
	if query.MinRelevance != 0.5 || filter.MinRelevance != 0.5 {
		t.Errorf("min_relevance not applied: %v/%v", query.MinRelevance, filter.MinRelevance)
	}
	if _, _, _, inj := sendCASSInjectionConfigs(true, false, nil); inj.MaxTokens == 0 {
		t.Error("nil config should fall back to engine defaults")
	}
}

func TestSendCASSInjectionInfo_DegradedRecordsSkip(t *testing.T) {
	info := sendCASSInjectionInfo(cass.InjectionResult{
		Success: false,
		Error:   "CASS query failed: cass command not found",
		Metadata: cass.InjectionMetadata{
			Enabled: true,
		},
	}, "rate limiting", nil)
	if info.SkippedReason == "" || !strings.Contains(info.SkippedReason, "not found") {
		t.Fatalf("degraded cobra envelope must record the skip: %+v", info)
	}
}

func TestSendCommand_CASSFlagsRegistered(t *testing.T) {
	cmd := newSendCmd()
	for _, name := range []string{"with-cass", "no-cass"} {
		if cmd.Flags().Lookup(name) == nil {
			t.Errorf("ntm send missing --%s", name)
		}
	}
}
