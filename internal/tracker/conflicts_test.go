package tracker

import (
	"testing"
	"time"
)

func TestDetectConflicts(t *testing.T) {
	now := time.Now()

	changes := []RecordedFileChange{
		{
			Timestamp: now.Add(-10 * time.Minute),
			Session:   "s1",
			Agents:    []string{"cc_1"},
			Change: FileChange{
				Path: "/src/api.go",
				Type: FileModified,
			},
		},
		{
			Timestamp: now.Add(-5 * time.Minute),
			Session:   "s1",
			Agents:    []string{"cod_1"},
			Change: FileChange{
				Path: "/src/api.go",
				Type: FileModified,
			},
		},
		{
			Timestamp: now.Add(-2 * time.Minute),
			Session:   "s1",
			Agents:    []string{"cc_1"}, // Same agent as first, but different from second
			Change: FileChange{
				Path: "/src/api.go",
				Type: FileModified,
			},
		},
		{
			Timestamp: now.Add(-1 * time.Minute),
			Session:   "s1",
			Agents:    []string{"cc_1"},
			Change: FileChange{
				Path: "/src/other.go",
				Type: FileModified,
			},
		},
	}

	conflicts := DetectConflicts(changes)

	if len(conflicts) != 1 {
		t.Fatalf("expected 1 conflict, got %d", len(conflicts))
	}

	c := conflicts[0]
	if c.Path != "/src/api.go" {
		t.Errorf("expected conflict in /src/api.go, got %s", c.Path)
	}
	if len(c.Changes) != 3 {
		t.Errorf("expected 3 conflicting changes, got %d", len(c.Changes))
	}

	if c.Severity != "critical" {
		t.Errorf("expected critical severity due to tight timing, got %s", c.Severity)
	}
	if c.LastAt.IsZero() {
		t.Errorf("expected LastAt to be set")
	}
}

func TestNoConflictSameAgent(t *testing.T) {
	now := time.Now()

	changes := []RecordedFileChange{
		{
			Timestamp: now.Add(-10 * time.Minute),
			Session:   "s1",
			Agents:    []string{"cc_1"},
			Change: FileChange{
				Path: "/src/api.go",
				Type: FileModified,
			},
		},
		{
			Timestamp: now.Add(-5 * time.Minute),
			Session:   "s1",
			Agents:    []string{"cc_1"},
			Change: FileChange{
				Path: "/src/api.go",
				Type: FileModified,
			},
		},
	}

	conflicts := DetectConflicts(changes)

	if len(conflicts) != 0 {
		t.Errorf("expected 0 conflicts, got %d", len(conflicts))
	}
}

func TestConflictSeverityAgentCount(t *testing.T) {
	now := time.Now()
	changes := []RecordedFileChange{
		{Timestamp: now.Add(-3 * time.Minute), Session: "s1", Agents: []string{"a1"}, Change: FileChange{Path: "/p", Type: FileModified}},
		{Timestamp: now.Add(-2 * time.Minute), Session: "s1", Agents: []string{"a2"}, Change: FileChange{Path: "/p", Type: FileModified}},
		{Timestamp: now.Add(-1 * time.Minute), Session: "s1", Agents: []string{"a3"}, Change: FileChange{Path: "/p", Type: FileModified}},
	}
	conflicts := DetectConflicts(changes)
	if len(conflicts) != 1 {
		t.Fatalf("expected 1 conflict, got %d", len(conflicts))
	}
	if conflicts[0].Severity != "critical" {
		t.Errorf("expected critical severity with 3 agents, got %s", conflicts[0].Severity)
	}
}
