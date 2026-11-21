package serve

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/Dicklesworthstone/ntm/internal/robot"
	"github.com/Dicklesworthstone/ntm/internal/session"
	"github.com/Dicklesworthstone/ntm/internal/tmux"
)

// handleSavedSessionsRestoreV1 restores topology, starts saved agents afresh, or
// resumes their provider sessions through the shared session recovery engine.
func (s *Server) handleSavedSessionsRestoreV1(w http.ResponseWriter, r *http.Request) {
	reqID := requestIDFromContext(r.Context())
	name, err := validateSafeWorktreeIdentifier("name", chi.URLParam(r, "name"))
	if err != nil {
		writeErrorResponse(w, http.StatusBadRequest, ErrCodeBadRequest, err.Error(), nil, reqID)
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, maxServePersistenceBodyBytes)
	req := new(SavedSessionRestoreRequest)
	decoder := json.NewDecoder(r.Body)
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&req); err != nil && !errors.Is(err, io.EOF) {
		writeErrorResponse(w, http.StatusBadRequest, ErrCodeBadRequest, "invalid request body: "+err.Error(), nil, reqID)
		return
	}
	if err := decoder.Decode(new(any)); !errors.Is(err, io.EOF) || req == nil {
		writeErrorResponse(w, http.StatusBadRequest, ErrCodeBadRequest, "request body must contain one JSON object", nil, reqID)
		return
	}
	mode := strings.TrimSpace(req.Mode)
	if mode == "" {
		mode = "topology"
		if req.Launch {
			// Older clients used launch=true to request provider-session resume.
			mode = "resume"
		}
	} else if req.Launch {
		writeErrorResponse(w, http.StatusBadRequest, ErrCodeBadRequest, "choose mode or legacy launch, not both", nil, reqID)
		return
	}
	if mode != "topology" && mode != "launch" && mode != "resume" {
		writeErrorResponse(w, http.StatusBadRequest, ErrCodeBadRequest, "mode must be topology, launch, or resume", nil, reqID)
		return
	}
	if req.Native && mode != "resume" {
		writeErrorResponse(w, http.StatusBadRequest, ErrCodeBadRequest, "native requires resume mode", nil, reqID)
		return
	}
	// Launching agents requires an additional capability, before state is read
	// or the force-restore path can replace an existing session.
	if mode != "topology" {
		allowed := false
		s.RequirePermission(PermWriteAgents)(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
			allowed = true
		})).ServeHTTP(w, r)
		if !allowed {
			return
		}
	}
	ctx, cancel := context.WithTimeout(r.Context(), 2*time.Minute)
	defer cancel()
	if err := ctx.Err(); err != nil {
		writeErrorResponse(w, http.StatusRequestTimeout, ErrCodeTimeout, err.Error(), nil, reqID)
		return
	}
	saved, err := robot.GetSavedSessions(robot.SavedSessionsOptions{Action: "show", Name: name})
	if err != nil {
		writeErrorResponse(w, http.StatusInternalServerError, ErrCodeInternalError, err.Error(), nil, reqID)
		return
	}
	if !saved.Success {
		writeRobotEnvelopeOrError(w, saved, saved.RobotResponse, "saved session restore", reqID)
		return
	}
	if saved.State == nil {
		writeErrorResponse(w, http.StatusInternalServerError, ErrCodeInternalError, "saved session contains no state", nil, reqID)
		return
	}
	target := strings.TrimSpace(req.TargetName)
	if target == "" {
		target = saved.State.Name
	}
	if err := tmux.ValidateSessionName(target); err != nil {
		writeErrorResponse(w, http.StatusBadRequest, ErrCodeBadRequest, "invalid target_name: "+err.Error(), nil, reqID)
		return
	}
	if err := tmux.EnsureInstalled(); err != nil {
		writeErrorResponse(w, http.StatusServiceUnavailable, ErrCodeServiceUnavail, err.Error(), nil, reqID)
		return
	}
	var result *session.ResumeResult
	opts := session.RestoreOptions{Name: target, Force: req.Force, SkipGitCheck: true}
	if mode != "topology" {
		if s.recoveryConfig == nil {
			writeErrorResponse(w, http.StatusServiceUnavailable, ErrCodeServiceUnavail, "saved-session recovery policy is unavailable", nil, reqID)
			return
		}
		effective, loadErr := s.recoveryConfig(saved.State.WorkDir)
		if loadErr != nil || effective == nil {
			message := "saved-session recovery policy is unavailable"
			if loadErr != nil {
				message += ": " + loadErr.Error()
			}
			writeErrorResponse(w, http.StatusServiceUnavailable, ErrCodeServiceUnavail, message, nil, reqID)
			return
		}
		// Legacy templates use the requested target. Recorded launch envelopes
		// remain authoritative, and the loaded snapshot keeps its identity.
		renderState := *saved.State
		renderState.Name = target
		session.ApplyRestoreModels(&renderState, effective)
		commands := session.RestoreAgentCommands(&renderState, effective)
		// An engine error can follow a successful replacement or partial launch.
		// Retain the terminal outcome so same-key retries cannot repeat it.
		markIdempotencyResultRetained(r)
		if mode == "resume" {
			result, err = session.ResumeContext(ctx, saved.State, commands, session.ResumeOptions{
				Name: target, Force: req.Force, PreferCASR: !req.Native, Config: effective,
			})
		} else {
			result, err = session.RestoreWithAgents(ctx, saved.State, commands, effective, opts)
		}
	} else {
		markIdempotencyResultRetained(r)
		err = session.RestoreContext(ctx, saved.State, opts)
	}
	data := map[string]interface{}{
		"action": "restore", "name": name, "session": target, "mode": mode,
		"launch": mode != "topology", "state": saved.State,
	}
	if result != nil {
		data["resume"] = result
	}
	if err != nil {
		status, code := http.StatusUnprocessableEntity, ErrCodeInvalidRequest
		switch {
		case errors.Is(err, context.Canceled), errors.Is(err, context.DeadlineExceeded):
			status, code = http.StatusGatewayTimeout, ErrCodeTimeout
		case result != nil:
			status, code = http.StatusInternalServerError, ErrCodeInternalError
		case strings.Contains(err.Error(), "already exists"):
			status, code = http.StatusConflict, ErrCodeConflict
		}
		slog.Warn("saved session restore failed", "saved_name", name, "session", target, "mode", mode, "request_id", reqID, "error", err)
		writeErrorResponse(w, status, code, err.Error(), data, reqID)
		return
	}
	slog.Info("saved session restored", "saved_name", name, "session", target, "mode", mode, "request_id", reqID)
	writeSuccessResponse(w, http.StatusOK, data, reqID)
}
