package events

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"

	"github.com/Dicklesworthstone/ntm/internal/config"
	"github.com/Dicklesworthstone/ntm/internal/privacy"
)

// These tests change the process-wide privacy manager and must not run in parallel.
func TestLoggerPrivacyBoundary(t *testing.T) {
	previous := privacy.GetDefaultManager()
	t.Cleanup(func() { privacy.SetDefaultManager(previous) })

	cases := []struct {
		name         string
		global       bool
		disableLogs  bool
		session      string
		registered   bool
		private      bool
		allowPersist bool
		wantStored   bool
	}{
		{name: "private session", disableLogs: true, session: "private", registered: true, private: true},
		{name: "global sessionless", global: true, disableLogs: true},
		{name: "global unregistered", global: true, disableLogs: true, session: "unregistered"},
		{name: "global registered", global: true, disableLogs: true, session: "registered", registered: true},
		{name: "explicit persistence", global: true, disableLogs: true, session: "permitted", registered: true, private: true, allowPersist: true, wantStored: true},
		{name: "event logging permitted by policy", disableLogs: false, session: "private", registered: true, private: true, wantStored: true},
		{name: "public session", disableLogs: true, session: "public", registered: true, wantStored: true},
		{name: "public sessionless", disableLogs: true, wantStored: true},
	}
	for _, tc := range cases {
		for _, direct := range []bool{true, false} {
			entry := "LogEvent"
			if direct {
				entry = "Log"
			}
			t.Run(tc.name+"/"+entry, func(t *testing.T) {
				manager := privacy.New(config.PrivacyConfig{Enabled: tc.global, DisableEventLogs: tc.disableLogs})
				if tc.registered {
					manager.RegisterSession(tc.session, tc.private, tc.allowPersist)
				}
				privacy.SetDefaultManager(manager)
				path := filepath.Join(t.TempDir(), "events.jsonl")
				logger, err := NewLogger(LoggerOptions{Path: path, Enabled: true})
				if err != nil {
					t.Fatal(err)
				}
				t.Cleanup(func() { closeLogger(logger) })
				payload := map[string]interface{}{"message": "privacy-boundary-payload"}
				if direct {
					err = logger.Log(NewEvent(EventError, tc.session, payload))
				} else {
					err = logger.LogEvent(EventError, tc.session, payload)
				}
				if err != nil {
					t.Fatalf("policy denial must be a silent skip, not an application failure: %v", err)
				}
				data, err := os.ReadFile(path)
				if err != nil {
					t.Fatal(err)
				}
				if !tc.wantStored {
					if len(data) != 0 || logger.eventCount != 0 {
						t.Fatalf("private event reached storage: bytes=%d events=%d", len(data), logger.eventCount)
					}
					return
				}
				var event Event
				if err := json.Unmarshal(data, &event); err != nil {
					t.Fatalf("permitted event missing: %v", err)
				}
				if event.Session != tc.session || event.Data["message"] != payload["message"] || logger.eventCount != 1 {
					t.Fatalf("permitted event changed: %+v (count=%d)", event, logger.eventCount)
				}
			})
		}
	}
}

type privacyBoundaryMarshalProbe struct{ calls *atomic.Int32 }

func (p privacyBoundaryMarshalProbe) MarshalJSON() ([]byte, error) {
	p.calls.Add(1)
	return []byte(`"private-marshaler-was-called"`), nil
}

func TestLoggerPrivacyDenialPrecedesSerializationAndTailRepair(t *testing.T) {
	previous := privacy.GetDefaultManager()
	t.Cleanup(func() { privacy.SetDefaultManager(previous) })
	manager := privacy.New(config.PrivacyConfig{DisableEventLogs: true})
	manager.RegisterSession("private", true, false)
	privacy.SetDefaultManager(manager)

	path := filepath.Join(t.TempDir(), "events.jsonl")
	interrupted := []byte(`{"interrupted":`)
	if err := os.WriteFile(path, interrupted, 0600); err != nil {
		t.Fatal(err)
	}
	logger, err := NewLogger(LoggerOptions{Path: path, Enabled: true})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { closeLogger(logger) })
	var calls atomic.Int32
	if err := logger.Log(NewEvent(EventError, "private", map[string]interface{}{
		"payload": privacyBoundaryMarshalProbe{calls: &calls},
	})); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if calls.Load() != 0 || !bytes.Equal(data, interrupted) || logger.eventCount != 0 {
		t.Fatalf("denied event had side effects: marshal calls=%d bytes=%q count=%d", calls.Load(), data, logger.eventCount)
	}
}

func TestLoggerPrivacyLookupRunsOutsideWriterLock(t *testing.T) {
	previous := privacy.GetDefaultManager()
	t.Cleanup(func() { privacy.SetDefaultManager(previous) })
	logger, err := NewLogger(LoggerOptions{Path: filepath.Join(t.TempDir(), "events.jsonl"), Enabled: true})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { closeLogger(logger) })
	var calls atomic.Int32
	manager := privacy.New(config.PrivacyConfig{DisableEventLogs: true}).WithSessionLookup(func(session string) (*privacy.SessionState, bool) {
		calls.Add(1)
		if !logger.mu.TryLock() {
			t.Error("privacy lookup ran under the logger mutex")
		} else {
			logger.mu.Unlock()
		}
		return &privacy.SessionState{PrivacyMode: true}, true
	})
	privacy.SetDefaultManager(manager)
	if err := logger.Log(NewEvent(EventError, "recorded-by-another-process", nil)); err != nil {
		t.Fatal(err)
	}
	if calls.Load() != 1 || logger.eventCount != 0 {
		t.Fatalf("recorded privacy was bypassed: lookups=%d stored=%d", calls.Load(), logger.eventCount)
	}
}

func TestLoggerPrivacyBoundaryRejectsNilEvent(t *testing.T) {
	logger, err := NewLogger(LoggerOptions{Path: filepath.Join(t.TempDir(), "events.jsonl"), Enabled: true})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { closeLogger(logger) })
	if err := logger.Log(nil); err == nil {
		t.Fatal("nil event must not become a null log record")
	}
	if logger.eventCount != 0 {
		t.Fatal("nil event was counted as persisted")
	}
}
