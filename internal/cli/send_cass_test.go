package cli

// Tests for --with-cass / --no-cass send-time CASS injection wiring (C10,
// bd-ws2-wire-or-delete-ykmcz.11): flag/config precedence mirrors
// --with-memory, [cass.context] keys really parameterize the engines (the
// G2 liveness claims are honest), and the degraded path records a skip.

import (
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
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
	cfg.CASS.Timeout = 7
	cfg.CASS.Context.MaxSessions = 3
	cfg.CASS.Context.MaxTokens = 800
	cfg.CASS.Context.MinRelevance = 0.5

	enabled, query, filter, inject := sendCASSInjectionConfigs(false, false, cfg)
	if !enabled {
		t.Fatal("enabled = false, want true via config default")
	}
	if query.Timeout != 7*time.Second {
		t.Errorf("timeout not applied: %v", query.Timeout)
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

// Exercise the same config, query, injection, and response path used by ntm
// send. An optional search must relinquish dispatch on its configured deadline,
// even when a subprocess keeps the search command's output pipes open.
func TestSendCASSConfiguredDeadlineRecordsSkippedEnrichment(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("CASS process fixture requires a POSIX shell")
	}
	for _, fixture := range []struct {
		name string
		body string
	}{
		{name: "hung-search", body: "exec sleep 4"},
		{name: "inherited-output", body: "sleep 4 &\nwait"},
	} {
		t.Run(fixture.name, func(t *testing.T) {
			binary := filepath.Join(t.TempDir(), "cass")
			if err := os.WriteFile(binary, []byte("#!/bin/sh\n"+fixture.body+"\n"), 0o700); err != nil {
				t.Fatal(err)
			}
			cfg := cassTestConfig(true, true)
			cfg.CASS.BinaryPath = binary
			cfg.CASS.Timeout = 1
			enabled, query, filter, inject := sendCASSInjectionConfigs(true, false, cfg)
			if !enabled {
				t.Fatal("explicit CASS enrichment was not enabled")
			}
			const prompt = "Investigate database transaction contention"
			started := time.Now()
			result, search, filtered := cass.InjectContextFromQuery(prompt, query, filter, inject)
			elapsed := time.Since(started)
			if elapsed >= 3*time.Second {
				t.Fatalf("optional enrichment held dispatch for %v with a 1s timeout", elapsed)
			}
			if search.Success || !strings.Contains(search.Error, "timed out after 1s") {
				t.Fatalf("search did not report its configured deadline: %+v", search)
			}
			if result.Success || result.ModifiedPrompt != prompt || result.InjectedContext != "" || result.Metadata.ItemsInjected != 0 {
				t.Fatalf("timed-out enrichment returned an injected replacement prompt: %+v", result)
			}
			info := sendCASSInjectionInfo(result, search.Query, filtered.Hits)
			if !strings.Contains(info.SkippedReason, "timed out after 1s") {
				t.Fatalf("send response lost the enrichment timeout: %+v", info)
			}
		})
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

func TestSendCASSInjectionInfo_UsesIndexedAge(t *testing.T) {
	indexed := time.Now().UTC().Add(-72 * time.Hour)
	for _, format := range []cass.InjectionFormat{cass.FormatMarkdown, cass.FormatMinimal, cass.FormatStructured} {
		inject := cass.DefaultInjectConfig()
		inject.Format = format
		var hit cass.CASSHit
		raw, err := json.Marshal(map[string]interface{}{
			"source_path": "/archive/2099/01/01/renamed.jsonl",
			"created_at":  indexed, "content": "INDEXED_HISTORY",
		})
		if err != nil {
			t.Fatal(err)
		}
		if err := json.Unmarshal(raw, &hit); err != nil {
			t.Fatal(err)
		}
		filtered := cass.FilterResults([]cass.CASSHit{hit}, cass.FilterConfig{MaxAgeDays: 30})
		result := cass.InjectContext("original task", filtered.Hits, inject)
		info := sendCASSInjectionInfo(result, "query", filtered.Hits)
		if !result.Success || len(info.Sources) != 1 || info.Sources[0].AgeDays != 3 || !strings.Contains(result.ModifiedPrompt, "INDEXED_HISTORY") {
			t.Fatalf("format %s lost indexed receipt evidence: result=%+v info=%+v", format, result, info)
		}
	}
}
