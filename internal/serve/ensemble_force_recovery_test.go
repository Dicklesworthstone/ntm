package serve

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Dicklesworthstone/ntm/internal/ensemble"
	statepkg "github.com/Dicklesworthstone/ntm/internal/state"
)

func TestEnsembleHTTPForceRecoversOnlySavedOutputsAfterRefreshFailure(t *testing.T) {
	for _, tc := range []struct {
		name            string
		force           bool
		corruptSaved    bool
		failPersistence bool
		contextError    error
		wantStatus      int
	}{
		{name: "without force", wantStatus: http.StatusInternalServerError},
		{name: "force saved recovery", force: true, wantStatus: http.StatusOK},
		{name: "force rejects invalid saved output", force: true, corruptSaved: true, wantStatus: http.StatusUnprocessableEntity},
		{name: "force after persistence failure", force: true, failPersistence: true, wantStatus: http.StatusOK},
		{name: "force canceled", force: true, contextError: context.Canceled},
		{name: "force deadline", force: true, contextError: context.DeadlineExceeded},
	} {
		t.Run(tc.name, func(t *testing.T) {
			session, dir := setupStandaloneHTTPSynthesis(t, standaloneHTTPModeOutput)
			state, err := ensemble.LoadSession(session)
			if err != nil {
				t.Fatal(err)
			}
			savedOutput := ensemble.ModeOutput{
				ModeID:      "deductive",
				Thesis:      "Only the durable saved analysis should be used.",
				Confidence:  0.8,
				TopFindings: []ensemble.Finding{{Finding: "Keep the saved finding.", Impact: ensemble.ImpactHigh, Confidence: 0.8}},
			}
			savedPath := filepath.Join(dir, "saved.json")
			writeModeOutputFile(t, savedPath, savedOutput)
			if tc.corruptSaved {
				if err := os.WriteFile(savedPath, []byte("not valid saved output"), 0o600); err != nil {
					t.Fatal(err)
				}
			}
			liveOutput := savedOutput
			liveOutput.ModeID = "inductive"
			liveOutput.Thesis = "Discard this unpublished live analysis."
			liveOutput.TopFindings = []ensemble.Finding{{Finding: "Discard the unpublished live finding.", Impact: ensemble.ImpactHigh, Confidence: 0.8}}
			liveJSON, err := json.Marshal(liveOutput)
			if err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(dir, "member.txt"), []byte("\x60\x60\x60json\n"+string(liveJSON)+"\n\x60\x60\x60\nclaude>\n"), 0o600); err != nil {
				t.Fatal(err)
			}
			t.Setenv("NTM_SYNTH_PANES", os.Getenv("NTM_SYNTH_PANES")+"\n"+
				"%2_NTM_SEP_1_NTM_SEP_"+session+"__cc_2_NTM_SEP_claude_NTM_SEP_120_NTM_SEP_40_NTM_SEP_0_NTM_SEP_0_NTM_SEP_0_NTM_SEP_cc_NTM_SEP__NTM_SEP__NTM_SEP_0_NTM_SEP_[]")
			state.Assignments = []ensemble.ModeAssignment{
				{ModeID: "deductive", PaneName: "%1", AgentType: "cc", Status: ensemble.AssignmentDone, OutputPath: savedPath},
				{ModeID: "inductive", PaneName: "%2", AgentType: "cc", Status: ensemble.AssignmentActive},
			}
			if !tc.failPersistence {
				state.Assignments = append(state.Assignments, ensemble.ModeAssignment{
					ModeID: "abductive", PaneName: "%99", AgentType: "cc", Status: ensemble.AssignmentActive,
				})
			}
			if err := ensemble.SaveSession(session, state); err != nil {
				t.Fatal(err)
			}
			if tc.failPersistence {
				path := filepath.Join(filepath.Dir(statepkg.DefaultPath()), "ensemble-outputs")
				if err := os.WriteFile(path, []byte("block capture directory"), 0o600); err != nil {
					t.Fatal(err)
				}
			}

			srv := NewHermeticServer("forced-saved-synthesis")
			t.Cleanup(srv.Stop)
			ctx := t.Context()
			if tc.contextError == context.Canceled {
				var cancel context.CancelFunc
				ctx, cancel = context.WithCancel(ctx)
				cancel()
			} else if tc.contextError == context.DeadlineExceeded {
				var cancel context.CancelFunc
				ctx, cancel = context.WithDeadline(ctx, time.Now().Add(-time.Second))
				defer cancel()
			}
			body, err := json.Marshal(EnsembleSynthesizeRequest{Force: tc.force, OutputFormat: "json"})
			if err != nil {
				t.Fatal(err)
			}
			req := httptest.NewRequest(http.MethodPost, "/api/v1/ensemble/sessions/"+session+"/synthesize",
				strings.NewReader(string(body))).WithContext(ctx)
			req.Header.Set("Content-Type", "application/json")
			rec := httptest.NewRecorder()
			srv.Router().ServeHTTP(rec, req)
			if tc.contextError != nil {
				if rec.Code < http.StatusBadRequest || !strings.Contains(rec.Body.String(), tc.contextError.Error()) {
					t.Fatalf("forced request concealed cancellation: status=%d body=%s", rec.Code, rec.Body.String())
				}
			} else if rec.Code != tc.wantStatus {
				t.Fatalf("status=%d, want %d: %s", rec.Code, tc.wantStatus, rec.Body.String())
			}
			if tc.wantStatus == http.StatusOK {
				var response EnsembleSynthesizeEnvelopeResponse
				if err := json.Unmarshal(rec.Body.Bytes(), &response); err != nil || response.Result == nil {
					t.Fatalf("invalid result: %v\n%s", err, rec.Body.String())
				}
				result := response.Result
				if !response.Success || result.SynthesizedBy != ensemble.SynthesisPathMechanical ||
					!strings.Contains(result.FallbackReason, "validated saved outputs") ||
					!strings.Contains(result.FallbackReason, "failed") ||
					result.Summary != savedOutput.Thesis || len(result.Findings) != 1 ||
					result.Findings[0].Finding != savedOutput.TopFindings[0].Finding {
					t.Fatalf("forced recovery accepted live work or hid failure: %+v", response)
				}
			}
			if _, err := os.Stat(filepath.Join(dir, "prompt.txt")); !errors.Is(err, os.ErrNotExist) {
				t.Fatalf("failed refresh authorized lead dispatch: %v", err)
			}
			if _, err := os.Stat(filepath.Join(dir, "submitted")); !errors.Is(err, os.ErrNotExist) {
				t.Fatalf("failed refresh submitted a new prompt: %v", err)
			}
			reloaded, err := ensemble.LoadSession(session)
			if err != nil {
				t.Fatal(err)
			}
			for _, assignment := range reloaded.Assignments[1:] {
				if assignment.Status != ensemble.AssignmentActive || assignment.OutputPath != "" {
					t.Fatalf("failed refresh published live work: %+v", assignment)
				}
			}
		})
	}
}
