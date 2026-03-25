package bv

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestUnmarshalBdList(t *testing.T) {
	t.Parallel()

	type bead struct {
		ID string `json:"id"`
	}

	tests := []struct {
		name    string
		input   string
		wantIDs []string
	}{
		{
			name:    "raw_array",
			input:   `[{"id":"bd-1"},{"id":"bd-2"}]`,
			wantIDs: []string{"bd-1", "bd-2"},
		},
		{
			name:    "issues_envelope",
			input:   `{"issues":[{"id":"bd-3"},{"id":"bd-4"}]}`,
			wantIDs: []string{"bd-3", "bd-4"},
		},
		{
			name:    "single_object",
			input:   `{"id":"bd-5"}`,
			wantIDs: []string{"bd-5"},
		},
		{
			name:    "empty_null",
			input:   `null`,
			wantIDs: []string{},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			got, err := UnmarshalBdList[bead](tt.input)
			if err != nil {
				t.Fatalf("UnmarshalBdList() error = %v", err)
			}

			if len(got) != len(tt.wantIDs) {
				t.Fatalf("len(got) = %d, want %d", len(got), len(tt.wantIDs))
			}
			for i, wantID := range tt.wantIDs {
				if got[i].ID != wantID {
					t.Fatalf("got[%d].ID = %q, want %q", i, got[i].ID, wantID)
				}
			}
		})
	}
}

func TestUnmarshalBdList_RawMessagesFromEnvelope(t *testing.T) {
	t.Parallel()

	raw, err := UnmarshalBdList[json.RawMessage](`{"issues":[{"id":"bd-7"}]}`)
	if err != nil {
		t.Fatalf("UnmarshalBdList() error = %v", err)
	}
	if len(raw) != 1 {
		t.Fatalf("len(raw) = %d, want 1", len(raw))
	}

	var decoded map[string]string
	if err := json.Unmarshal(raw[0], &decoded); err != nil {
		t.Fatalf("json.Unmarshal(raw[0]) error = %v", err)
	}
	if decoded["id"] != "bd-7" {
		t.Fatalf("decoded id = %q, want %q", decoded["id"], "bd-7")
	}
}

func TestCheckDrift_EarlyValidation(t *testing.T) {
	t.Parallel()

	t.Run("missing_project_dir", func(t *testing.T) {
		t.Parallel()

		res := CheckDrift("/path/that/does/not/exist")
		if res.Status != DriftNoBaseline {
			t.Fatalf("Status = %v, want %v", res.Status, DriftNoBaseline)
		}
		if IsInstalled() {
			if !strings.Contains(res.Message, "project directory does not exist") {
				t.Fatalf("Message = %q, want contains %q", res.Message, "project directory does not exist")
			}
		} else {
			if !strings.Contains(res.Message, "bv not installed") {
				t.Fatalf("Message = %q, want contains %q", res.Message, "bv not installed")
			}
		}
	})

	t.Run("missing_beads_dir", func(t *testing.T) {
		t.Parallel()

		dir := t.TempDir()
		res := CheckDrift(dir)
		if res.Status != DriftNoBaseline {
			t.Fatalf("Status = %v, want %v", res.Status, DriftNoBaseline)
		}
		if IsInstalled() {
			if !strings.Contains(res.Message, "no .beads directory") {
				t.Fatalf("Message = %q, want contains %q", res.Message, "no .beads directory")
			}
		} else {
			if !strings.Contains(res.Message, "bv not installed") {
				t.Fatalf("Message = %q, want contains %q", res.Message, "bv not installed")
			}
		}
	})
}

func TestGetBeadsSummary_EarlyValidation(t *testing.T) {
	t.Run("missing_project_dir", func(t *testing.T) {
		res := GetBeadsSummary("/path/that/does/not/exist", 3)
		if res.Available {
			t.Fatalf("Available = true, want false")
		}
		if !strings.Contains(res.Reason, "project directory does not exist") {
			t.Fatalf("Reason = %q, want contains %q", res.Reason, "project directory does not exist")
		}
	})

	t.Run("missing_beads_dir", func(t *testing.T) {
		dir := t.TempDir()
		res := GetBeadsSummary(dir, 3)
		if res.Available {
			t.Fatalf("Available = true, want false")
		}
		if !strings.Contains(res.Reason, "no .beads/ directory") {
			t.Fatalf("Reason = %q, want contains %q", res.Reason, "no .beads/ directory")
		}
	})

	t.Run("missing_br_binary_with_beads_dir", func(t *testing.T) {
		dir := t.TempDir()
		if err := os.Mkdir(filepath.Join(dir, ".beads"), 0755); err != nil {
			t.Fatalf("mkdir .beads: %v", err)
		}
		t.Setenv("PATH", "")

		res := GetBeadsSummary(dir, 3)
		if res.Available {
			t.Fatalf("Available = true, want false")
		}
		if res.Reason != "br not installed" {
			t.Fatalf("Reason = %q, want %q", res.Reason, "br not installed")
		}
	})
}

func TestGetHealthSummary_NonFatalBottlenecksError(t *testing.T) {
	t.Parallel()

	summary, err := GetHealthSummary("/path/that/does/not/exist")
	if err != nil {
		t.Fatalf("GetHealthSummary err = %v, want nil", err)
	}
	if summary == nil {
		t.Fatalf("GetHealthSummary summary = nil")
	}
	if summary.BottleneckCount != 0 {
		t.Fatalf("BottleneckCount = %d, want 0", summary.BottleneckCount)
	}
	if summary.DriftStatus != DriftNoBaseline {
		t.Fatalf("DriftStatus = %v, want %v", summary.DriftStatus, DriftNoBaseline)
	}
}

func TestGetDependencyContext_HandlesToolErrors(t *testing.T) {
	t.Parallel()

	ctx, err := GetDependencyContext("/path/that/does/not/exist", 3)
	if err != nil {
		t.Fatalf("GetDependencyContext err = %v, want nil", err)
	}
	if ctx == nil {
		t.Fatalf("GetDependencyContext ctx = nil")
	}
	if ctx.BlockedCount != 0 || ctx.ReadyCount != 0 {
		t.Fatalf("BlockedCount/ReadyCount = %d/%d, want 0/0", ctx.BlockedCount, ctx.ReadyCount)
	}
	if len(ctx.InProgressTasks) != 0 {
		t.Fatalf("len(InProgressTasks) = %d, want 0", len(ctx.InProgressTasks))
	}
	if len(ctx.TopBlockers) != 0 {
		t.Fatalf("len(TopBlockers) = %d, want 0", len(ctx.TopBlockers))
	}
}
