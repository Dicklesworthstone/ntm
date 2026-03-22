// Package serve provides REST API endpoints for Agent Mail and file reservations.
// mail.go implements the /api/v1/mail and /api/v1/reservations endpoints.
package serve

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"strconv"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/Dicklesworthstone/ntm/internal/agentmail"
)

// Mail-specific error codes
const (
	ErrCodeMailUnavailable = "MAIL_UNAVAILABLE"
	ErrCodeAgentNotFound   = "AGENT_NOT_FOUND"
	ErrCodeMessageNotFound = "MESSAGE_NOT_FOUND"
	ErrCodeThreadNotFound  = "THREAD_NOT_FOUND"
	ErrCodeContactDenied   = "CONTACT_DENIED"
)

// Mail request/response types

// CreateAgentRequest is the request body for POST /api/v1/mail/agents
type CreateAgentRequest struct {
	Program         string `json:"program"`
	Model           string `json:"model"`
	Name            string `json:"name,omitempty"`
	TaskDescription string `json:"task_description,omitempty"`
}

// SendMessageRequest is the request body for POST /api/v1/mail/messages
type SendMessageRequest struct {
	SenderName  string   `json:"sender_name"`
	To          []string `json:"to"`
	Subject     string   `json:"subject"`
	BodyMD      string   `json:"body_md"`
	CC          []string `json:"cc,omitempty"`
	BCC         []string `json:"bcc,omitempty"`
	Importance  string   `json:"importance,omitempty"`
	AckRequired bool     `json:"ack_required,omitempty"`
	ThreadID    string   `json:"thread_id,omitempty"`
}

// ReplyMessageRequest is the request body for POST /api/v1/mail/messages/{id}/reply
type ReplyMessageRequest struct {
	SenderName string   `json:"sender_name"`
	BodyMD     string   `json:"body_md"`
	To         []string `json:"to,omitempty"`
	CC         []string `json:"cc,omitempty"`
}

// ContactRequest is the request body for POST /api/v1/mail/contacts/request
type ContactRequestBody struct {
	FromAgent  string `json:"from_agent"`
	ToAgent    string `json:"to_agent"`
	ToProject  string `json:"to_project,omitempty"`
	Reason     string `json:"reason,omitempty"`
	TTLSeconds int    `json:"ttl_seconds,omitempty"`
}

// ContactRespondRequest is the request body for POST /api/v1/mail/contacts/respond
type ContactRespondRequest struct {
	ToAgent    string `json:"to_agent"`
	FromAgent  string `json:"from_agent"`
	Accept     bool   `json:"accept"`
	TTLSeconds int    `json:"ttl_seconds,omitempty"`
}

// SetContactPolicyRequest is the request body for PUT /api/v1/mail/contacts/policy
type SetContactPolicyRequest struct {
	AgentName string `json:"agent_name"`
	Policy    string `json:"policy"` // open, auto, contacts_only, block_all
}

// ReservePathsRequest is the request body for POST /api/v1/reservations
type ReservePathsRequest struct {
	AgentName  string   `json:"agent_name"`
	Paths      []string `json:"paths"`
	TTLSeconds int      `json:"ttl_seconds,omitempty"`
	Exclusive  bool     `json:"exclusive,omitempty"`
	Reason     string   `json:"reason,omitempty"`
}

// ReleaseReservationsRequest is the request body for DELETE /api/v1/reservations
type ReleaseReservationsRequest struct {
	AgentName string   `json:"agent_name"`
	Paths     []string `json:"paths,omitempty"`
	IDs       []int    `json:"ids,omitempty"`
}

// RenewReservationsRequest is the request body for POST /api/v1/reservations/{id}/renew
type RenewReservationsRequest struct {
	AgentName     string `json:"agent_name"`
	ExtendSeconds int    `json:"extend_seconds,omitempty"`
}

// ForceReleaseRequest is the request body for POST /api/v1/reservations/{id}/force-release
type ForceReleaseRequest struct {
	AgentName      string `json:"agent_name"`
	Note           string `json:"note,omitempty"`
	NotifyPrevious bool   `json:"notify_previous,omitempty"`
}

func writeAgentMailHandlerError(
	w http.ResponseWriter,
	reqID string,
	err error,
	notFoundCode string,
	notFoundMatch func(error) bool,
	notImplementedMessage string,
	hint string,
) {
	switch {
	case agentmail.IsNotImplemented(err):
		var details map[string]interface{}
		if hint != "" {
			details = map[string]interface{}{}
			details["hint"] = hint
		}
		writeErrorResponse(w, http.StatusNotImplemented, ErrCodeNotImplemented, notImplementedMessage, details, reqID)
	case notFoundMatch != nil && notFoundMatch(err):
		writeErrorResponse(w, http.StatusNotFound, notFoundCode, err.Error(), nil, reqID)
	case agentmail.IsUnauthorized(err):
		writeErrorResponse(w, http.StatusUnauthorized, ErrCodeUnauthorized, err.Error(), nil, reqID)
	case agentmail.IsServerUnavailable(err):
		writeErrorResponse(w, http.StatusServiceUnavailable, ErrCodeMailUnavailable, err.Error(), nil, reqID)
	case agentmail.IsTimeout(err):
		writeErrorResponse(w, http.StatusGatewayTimeout, ErrCodeTimeout, err.Error(), nil, reqID)
	default:
		writeErrorResponse(w, http.StatusInternalServerError, ErrCodeInternalError, err.Error(), nil, reqID)
	}
}

func writeAgentMailAgentActionError(w http.ResponseWriter, reqID string, err error, notImplementedMessage string) {
	writeAgentMailHandlerError(w, reqID, err, ErrCodeAgentNotFound, func(err error) bool {
		return errors.Is(err, agentmail.ErrAgentNotRegistered) || agentmail.IsNotFound(err)
	}, notImplementedMessage, "")
}

func writeAgentMailMessageActionError(w http.ResponseWriter, reqID string, err error, notImplementedMessage string) {
	switch {
	case errors.Is(err, agentmail.ErrAgentNotRegistered):
		writeErrorResponse(w, http.StatusNotFound, ErrCodeAgentNotFound, err.Error(), nil, reqID)
	default:
		writeAgentMailHandlerError(w, reqID, err, ErrCodeMessageNotFound, func(err error) bool {
			return errors.Is(err, agentmail.ErrMessageNotFound) || agentmail.IsNotFound(err)
		}, notImplementedMessage, "")
	}
}

func writeAgentMailReservationError(w http.ResponseWriter, reqID string, err error, notImplementedMessage string) {
	switch {
	case errors.Is(err, agentmail.ErrAgentNotRegistered):
		writeErrorResponse(w, http.StatusNotFound, ErrCodeAgentNotFound, err.Error(), nil, reqID)
	case agentmail.IsReservationConflict(err):
		writeErrorResponse(w, http.StatusConflict, ErrCodeConflict, err.Error(), nil, reqID)
	default:
		writeAgentMailHandlerError(w, reqID, err, ErrCodeNotFound, agentmail.IsNotFound, notImplementedMessage, "")
	}
}

// registerMailRoutes registers mail and reservation REST endpoints
func (s *Server) registerMailRoutes(r chi.Router) {
	r.Route("/mail", func(r chi.Router) {
		// Health check
		r.With(s.RequirePermission(PermReadHealth)).Get("/health", s.handleMailHealth)

		// Projects
		r.With(s.RequirePermission(PermReadMail)).Get("/projects", s.handleListMailProjects)

		// Agents
		r.Route("/agents", func(r chi.Router) {
			r.With(s.RequirePermission(PermReadMail)).Get("/", s.handleListMailAgents)
			r.With(s.RequirePermission(PermWriteMail)).Post("/", s.handleCreateMailAgent)
			r.With(s.RequirePermission(PermReadMail)).Get("/{name}", s.handleGetMailAgent)
		})

		// Inbox
		r.With(s.RequirePermission(PermReadMail)).Get("/inbox", s.handleMailInbox)

		// Messages
		r.Route("/messages", func(r chi.Router) {
			r.With(s.RequirePermission(PermWriteMail)).Post("/", s.handleSendMessage)
			r.Route("/{id}", func(r chi.Router) {
				r.With(s.RequirePermission(PermReadMail)).Get("/", s.handleGetMessage)
				r.With(s.RequirePermission(PermWriteMail)).Post("/reply", s.handleReplyMessage)
				r.With(s.RequirePermission(PermWriteMail)).Post("/read", s.handleMarkMessageRead)
				r.With(s.RequirePermission(PermWriteMail)).Post("/ack", s.handleAckMessage)
			})
		})

		// Search
		r.With(s.RequirePermission(PermReadMail)).Get("/search", s.handleSearchMessages)

		// Threads
		r.With(s.RequirePermission(PermReadMail)).Get("/threads/{id}/summary", s.handleThreadSummary)

		// Contacts
		r.Route("/contacts", func(r chi.Router) {
			r.With(s.RequirePermission(PermReadMail)).Get("/", s.handleListContacts)
			r.With(s.RequirePermission(PermWriteMail)).Post("/request", s.handleRequestContact)
			r.With(s.RequirePermission(PermWriteMail)).Post("/respond", s.handleRespondContact)
			r.With(s.RequirePermission(PermWriteMail)).Put("/policy", s.handleSetContactPolicy)
		})
	})

	r.Route("/reservations", func(r chi.Router) {
		r.With(s.RequirePermission(PermReadReservations)).Get("/", s.handleListReservations)
		r.With(s.RequirePermission(PermWriteReservations)).Post("/", s.handleReservePaths)
		r.With(s.RequirePermission(PermWriteReservations)).Delete("/", s.handleReleaseReservations)
		r.With(s.RequirePermission(PermReadReservations)).Get("/conflicts", s.handleReservationConflicts)

		r.Route("/{id}", func(r chi.Router) {
			r.With(s.RequirePermission(PermReadReservations)).Get("/", s.handleGetReservation)
			r.With(s.RequirePermission(PermWriteReservations)).Post("/release", s.handleReleaseReservationByID)
			r.With(s.RequirePermission(PermWriteReservations)).Post("/renew", s.handleRenewReservation)
			r.With(s.RequirePermission(PermForceRelease)).Post("/force-release", s.handleForceReleaseReservation)
		})
	})
}

// getMailClient returns the Agent Mail client, creating it if necessary.
func (s *Server) getMailClient() (*agentmail.Client, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	if s.mailClient != nil {
		return s.mailClient, nil
	}

	// Create client with project key derived from working directory
	client := agentmail.NewClient(
		agentmail.WithProjectKey(s.projectDir),
	)

	// Check if server is available
	if !client.IsAvailable() {
		return nil, nil // Return nil, nil to indicate unavailable (not an error)
	}

	s.mailClient = client
	return client, nil
}

func (s *Server) publishMailEvent(agentName, eventType string, payload map[string]interface{}) {
	if s.wsHub == nil {
		return
	}
	topic := "mail:*"
	if agentName != "" {
		topic = "mail:" + agentName
	}
	s.wsHub.Publish(topic, eventType, payload)
}

func (s *Server) publishReservationEvent(agentName, eventType string, payload map[string]interface{}) {
	if s.wsHub == nil {
		return
	}
	topic := "reservations:*"
	if agentName != "" {
		topic = "reservations:" + agentName
	}
	s.wsHub.Publish(topic, eventType, payload)
}

// handleMailHealth handles GET /api/v1/mail/health
func (s *Server) handleMailHealth(w http.ResponseWriter, r *http.Request) {
	reqID := requestIDFromContext(r.Context())
	slog.Info("mail health check", "request_id", reqID)

	client, err := s.getMailClient()
	if err != nil {
		writeErrorResponse(w, http.StatusInternalServerError, ErrCodeInternalError, "failed to create mail client", nil, reqID)
		return
	}

	if client == nil {
		writeSuccessResponse(w, http.StatusOK, map[string]interface{}{
			"status":    "unavailable",
			"available": false,
		}, reqID)
		return
	}

	ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
	defer cancel()

	health, err := client.HealthCheck(ctx)
	if err != nil {
		writeSuccessResponse(w, http.StatusOK, map[string]interface{}{
			"status":    "error",
			"available": false,
			"error":     err.Error(),
		}, reqID)
		return
	}

	writeSuccessResponse(w, http.StatusOK, map[string]interface{}{
		"status":    health.Status,
		"available": true,
	}, reqID)
}

// handleListMailProjects handles GET /api/v1/mail/projects
func (s *Server) handleListMailProjects(w http.ResponseWriter, r *http.Request) {
	reqID := requestIDFromContext(r.Context())
	slog.Info("list mail projects", "request_id", reqID)

	client, err := s.getMailClient()
	if err != nil {
		writeErrorResponse(w, http.StatusInternalServerError, ErrCodeInternalError, "failed to create mail client", nil, reqID)
		return
	}
	if client == nil {
		writeErrorResponse(w, http.StatusServiceUnavailable, ErrCodeMailUnavailable, "Agent Mail server is not available", nil, reqID)
		return
	}

	ctx, cancel := context.WithTimeout(r.Context(), 10*time.Second)
	defer cancel()

	project, err := client.EnsureProject(ctx, s.projectDir)
	if err != nil {
		writeErrorResponse(w, http.StatusInternalServerError, ErrCodeInternalError, err.Error(), nil, reqID)
		return
	}

	writeSuccessResponse(w, http.StatusOK, map[string]interface{}{
		"projects": []interface{}{project},
		"count":    1,
	}, reqID)
}

// handleListMailAgents handles GET /api/v1/mail/agents
func (s *Server) handleListMailAgents(w http.ResponseWriter, r *http.Request) {
	reqID := requestIDFromContext(r.Context())
	slog.Info("list mail agents", "request_id", reqID)

	client, err := s.getMailClient()
	if err != nil {
		writeErrorResponse(w, http.StatusInternalServerError, ErrCodeInternalError, "failed to create mail client", nil, reqID)
		return
	}
	if client == nil {
		writeErrorResponse(w, http.StatusServiceUnavailable, ErrCodeMailUnavailable, "Agent Mail server is not available", nil, reqID)
		return
	}

	ctx, cancel := context.WithTimeout(r.Context(), 10*time.Second)
	defer cancel()

	agents, err := client.ListAgents(ctx, s.projectDir)
	if err != nil {
		writeErrorResponse(w, http.StatusInternalServerError, ErrCodeInternalError, err.Error(), nil, reqID)
		return
	}

	writeSuccessResponse(w, http.StatusOK, map[string]interface{}{
		"agents": agents,
		"count":  len(agents),
	}, reqID)
}

// handleCreateMailAgent handles POST /api/v1/mail/agents
func (s *Server) handleCreateMailAgent(w http.ResponseWriter, r *http.Request) {
	reqID := requestIDFromContext(r.Context())
	slog.Info("create mail agent", "request_id", reqID)

	var req CreateAgentRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeErrorResponse(w, http.StatusBadRequest, ErrCodeBadRequest, "invalid request body", nil, reqID)
		return
	}

	if req.Program == "" || req.Model == "" {
		writeErrorResponse(w, http.StatusBadRequest, ErrCodeBadRequest, "program and model are required", nil, reqID)
		return
	}

	client, err := s.getMailClient()
	if err != nil {
		writeErrorResponse(w, http.StatusInternalServerError, ErrCodeInternalError, "failed to create mail client", nil, reqID)
		return
	}
	if client == nil {
		writeErrorResponse(w, http.StatusServiceUnavailable, ErrCodeMailUnavailable, "Agent Mail server is not available", nil, reqID)
		return
	}

	ctx, cancel := context.WithTimeout(r.Context(), 10*time.Second)
	defer cancel()

	agent, err := client.RegisterAgent(ctx, agentmail.RegisterAgentOptions{
		ProjectKey:      s.projectDir,
		Program:         req.Program,
		Model:           req.Model,
		Name:            req.Name,
		TaskDescription: req.TaskDescription,
	})
	if err != nil {
		writeErrorResponse(w, http.StatusInternalServerError, ErrCodeInternalError, err.Error(), nil, reqID)
		return
	}

	writeSuccessResponse(w, http.StatusCreated, map[string]interface{}{
		"agent": agent,
	}, reqID)
}

// handleGetMailAgent handles GET /api/v1/mail/agents/{name}
func (s *Server) handleGetMailAgent(w http.ResponseWriter, r *http.Request) {
	reqID := requestIDFromContext(r.Context())
	agentName := chi.URLParam(r, "name")

	slog.Info("get mail agent", "request_id", reqID, "agent", agentName)

	if agentName == "" {
		writeErrorResponse(w, http.StatusBadRequest, ErrCodeBadRequest, "agent name required", nil, reqID)
		return
	}

	client, err := s.getMailClient()
	if err != nil {
		writeErrorResponse(w, http.StatusInternalServerError, ErrCodeInternalError, "failed to create mail client", nil, reqID)
		return
	}
	if client == nil {
		writeErrorResponse(w, http.StatusServiceUnavailable, ErrCodeMailUnavailable, "Agent Mail server is not available", nil, reqID)
		return
	}

	ctx, cancel := context.WithTimeout(r.Context(), 10*time.Second)
	defer cancel()

	agent, err := client.Whois(ctx, s.projectDir, agentName, true)
	if err != nil {
		writeAgentMailAgentActionError(w, reqID, err, "agent lookup is not supported by the configured Agent Mail server")
		return
	}

	writeSuccessResponse(w, http.StatusOK, map[string]interface{}{
		"agent": agent,
	}, reqID)
}

// handleMailInbox handles GET /api/v1/mail/inbox
func (s *Server) handleMailInbox(w http.ResponseWriter, r *http.Request) {
	reqID := requestIDFromContext(r.Context())

	agentName := r.URL.Query().Get("agent_name")
	if agentName == "" {
		writeErrorResponse(w, http.StatusBadRequest, ErrCodeBadRequest, "agent_name query parameter required", nil, reqID)
		return
	}

	slog.Info("fetch mail inbox", "request_id", reqID, "agent", agentName)

	client, err := s.getMailClient()
	if err != nil {
		writeErrorResponse(w, http.StatusInternalServerError, ErrCodeInternalError, "failed to create mail client", nil, reqID)
		return
	}
	if client == nil {
		writeErrorResponse(w, http.StatusServiceUnavailable, ErrCodeMailUnavailable, "Agent Mail server is not available", nil, reqID)
		return
	}

	ctx, cancel := context.WithTimeout(r.Context(), 10*time.Second)
	defer cancel()

	opts := agentmail.FetchInboxOptions{
		ProjectKey: s.projectDir,
		AgentName:  agentName,
	}

	// Parse optional query params
	if sinceTS := r.URL.Query().Get("since_ts"); sinceTS != "" {
		if t, err := time.Parse(time.RFC3339, sinceTS); err == nil {
			opts.SinceTS = &t
		}
	}
	if limit := r.URL.Query().Get("limit"); limit != "" {
		if l, err := strconv.Atoi(limit); err == nil {
			opts.Limit = l
		}
	}
	if urgentOnly := r.URL.Query().Get("urgent_only"); urgentOnly == "true" {
		opts.UrgentOnly = true
	}
	if includeBodies := r.URL.Query().Get("include_bodies"); includeBodies == "true" {
		opts.IncludeBodies = true
	}

	messages, err := client.FetchInbox(ctx, opts)
	if err != nil {
		writeErrorResponse(w, http.StatusInternalServerError, ErrCodeInternalError, err.Error(), nil, reqID)
		return
	}

	messageIDs := make([]int, 0, len(messages))
	for _, m := range messages {
		messageIDs = append(messageIDs, m.ID)
	}
	s.publishMailEvent(agentName, "mail.received", map[string]interface{}{
		"agent_name":  agentName,
		"count":       len(messages),
		"message_ids": messageIDs,
	})

	writeSuccessResponse(w, http.StatusOK, map[string]interface{}{
		"messages": messages,
		"count":    len(messages),
	}, reqID)
}

// handleSendMessage handles POST /api/v1/mail/messages
func (s *Server) handleSendMessage(w http.ResponseWriter, r *http.Request) {
	reqID := requestIDFromContext(r.Context())
	slog.Info("send mail message", "request_id", reqID)

	var req SendMessageRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeErrorResponse(w, http.StatusBadRequest, ErrCodeBadRequest, "invalid request body", nil, reqID)
		return
	}

	if req.SenderName == "" || len(req.To) == 0 || req.Subject == "" || req.BodyMD == "" {
		writeErrorResponse(w, http.StatusBadRequest, ErrCodeBadRequest, "sender_name, to, subject, and body_md are required", nil, reqID)
		return
	}

	client, err := s.getMailClient()
	if err != nil {
		writeErrorResponse(w, http.StatusInternalServerError, ErrCodeInternalError, "failed to create mail client", nil, reqID)
		return
	}
	if client == nil {
		writeErrorResponse(w, http.StatusServiceUnavailable, ErrCodeMailUnavailable, "Agent Mail server is not available", nil, reqID)
		return
	}

	ctx, cancel := context.WithTimeout(r.Context(), 15*time.Second)
	defer cancel()

	result, err := client.SendMessage(ctx, agentmail.SendMessageOptions{
		ProjectKey:  s.projectDir,
		SenderName:  req.SenderName,
		To:          req.To,
		Subject:     req.Subject,
		BodyMD:      req.BodyMD,
		CC:          req.CC,
		BCC:         req.BCC,
		Importance:  req.Importance,
		AckRequired: req.AckRequired,
		ThreadID:    req.ThreadID,
	})
	if err != nil {
		writeErrorResponse(w, http.StatusInternalServerError, ErrCodeInternalError, err.Error(), nil, reqID)
		return
	}

	writeSuccessResponse(w, http.StatusCreated, map[string]interface{}{
		"deliveries": result.Deliveries,
		"count":      result.Count,
	}, reqID)
}

// handleGetMessage handles GET /api/v1/mail/messages/{id}
func (s *Server) handleGetMessage(w http.ResponseWriter, r *http.Request) {
	reqID := requestIDFromContext(r.Context())
	idStr := chi.URLParam(r, "id")

	slog.Info("get mail message", "request_id", reqID, "message_id", idStr)

	messageID, err := strconv.Atoi(idStr)
	if err != nil {
		writeErrorResponse(w, http.StatusBadRequest, ErrCodeBadRequest, "invalid message ID", nil, reqID)
		return
	}

	client, err := s.getMailClient()
	if err != nil {
		writeErrorResponse(w, http.StatusInternalServerError, ErrCodeInternalError, "failed to create mail client", nil, reqID)
		return
	}
	if client == nil {
		writeErrorResponse(w, http.StatusServiceUnavailable, ErrCodeMailUnavailable, "Agent Mail server is not available", nil, reqID)
		return
	}

	ctx, cancel := context.WithTimeout(r.Context(), 10*time.Second)
	defer cancel()

	message, err := client.GetMessage(ctx, s.projectDir, messageID)
	if err != nil {
		writeAgentMailHandlerError(w, reqID, err, ErrCodeMessageNotFound, func(err error) bool {
			return errors.Is(err, agentmail.ErrMessageNotFound) || agentmail.IsNotFound(err)
		}, "single-message retrieval is not supported by the configured Agent Mail server", "Use inbox responses with include_bodies=true or thread summaries instead.")
		return
	}

	writeSuccessResponse(w, http.StatusOK, map[string]interface{}{
		"message": message,
	}, reqID)
}

// handleReplyMessage handles POST /api/v1/mail/messages/{id}/reply
func (s *Server) handleReplyMessage(w http.ResponseWriter, r *http.Request) {
	reqID := requestIDFromContext(r.Context())
	idStr := chi.URLParam(r, "id")

	slog.Info("reply to mail message", "request_id", reqID, "message_id", idStr)

	messageID, err := strconv.Atoi(idStr)
	if err != nil {
		writeErrorResponse(w, http.StatusBadRequest, ErrCodeBadRequest, "invalid message ID", nil, reqID)
		return
	}

	var req ReplyMessageRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeErrorResponse(w, http.StatusBadRequest, ErrCodeBadRequest, "invalid request body", nil, reqID)
		return
	}

	if req.SenderName == "" || req.BodyMD == "" {
		writeErrorResponse(w, http.StatusBadRequest, ErrCodeBadRequest, "sender_name and body_md are required", nil, reqID)
		return
	}

	client, err := s.getMailClient()
	if err != nil {
		writeErrorResponse(w, http.StatusInternalServerError, ErrCodeInternalError, "failed to create mail client", nil, reqID)
		return
	}
	if client == nil {
		writeErrorResponse(w, http.StatusServiceUnavailable, ErrCodeMailUnavailable, "Agent Mail server is not available", nil, reqID)
		return
	}

	ctx, cancel := context.WithTimeout(r.Context(), 15*time.Second)
	defer cancel()

	message, err := client.ReplyMessage(ctx, agentmail.ReplyMessageOptions{
		ProjectKey: s.projectDir,
		MessageID:  messageID,
		SenderName: req.SenderName,
		BodyMD:     req.BodyMD,
		To:         req.To,
		CC:         req.CC,
	})
	if err != nil {
		writeAgentMailMessageActionError(w, reqID, err, "message replies are not supported by the configured Agent Mail server")
		return
	}

	writeSuccessResponse(w, http.StatusCreated, map[string]interface{}{
		"message": message,
	}, reqID)
}

// handleMarkMessageRead handles POST /api/v1/mail/messages/{id}/read
func (s *Server) handleMarkMessageRead(w http.ResponseWriter, r *http.Request) {
	reqID := requestIDFromContext(r.Context())
	idStr := chi.URLParam(r, "id")

	messageID, err := strconv.Atoi(idStr)
	if err != nil {
		writeErrorResponse(w, http.StatusBadRequest, ErrCodeBadRequest, "invalid message ID", nil, reqID)
		return
	}

	agentName := r.URL.Query().Get("agent_name")
	if agentName == "" {
		writeErrorResponse(w, http.StatusBadRequest, ErrCodeBadRequest, "agent_name query parameter required", nil, reqID)
		return
	}

	slog.Info("mark message read", "request_id", reqID, "message_id", messageID, "agent", agentName)

	client, err := s.getMailClient()
	if err != nil {
		writeErrorResponse(w, http.StatusInternalServerError, ErrCodeInternalError, "failed to create mail client", nil, reqID)
		return
	}
	if client == nil {
		writeErrorResponse(w, http.StatusServiceUnavailable, ErrCodeMailUnavailable, "Agent Mail server is not available", nil, reqID)
		return
	}

	ctx, cancel := context.WithTimeout(r.Context(), 10*time.Second)
	defer cancel()

	err = client.MarkMessageRead(ctx, s.projectDir, agentName, messageID)
	if err != nil {
		writeAgentMailMessageActionError(w, reqID, err, "marking messages as read is not supported by the configured Agent Mail server")
		return
	}

	s.publishMailEvent(agentName, "mail.read", map[string]interface{}{
		"agent_name": agentName,
		"message_id": messageID,
	})

	writeSuccessResponse(w, http.StatusOK, map[string]interface{}{
		"message_id": messageID,
		"read":       true,
	}, reqID)
}

// handleAckMessage handles POST /api/v1/mail/messages/{id}/ack
func (s *Server) handleAckMessage(w http.ResponseWriter, r *http.Request) {
	reqID := requestIDFromContext(r.Context())
	idStr := chi.URLParam(r, "id")

	messageID, err := strconv.Atoi(idStr)
	if err != nil {
		writeErrorResponse(w, http.StatusBadRequest, ErrCodeBadRequest, "invalid message ID", nil, reqID)
		return
	}

	agentName := r.URL.Query().Get("agent_name")
	if agentName == "" {
		writeErrorResponse(w, http.StatusBadRequest, ErrCodeBadRequest, "agent_name query parameter required", nil, reqID)
		return
	}

	slog.Info("acknowledge message", "request_id", reqID, "message_id", messageID, "agent", agentName)

	client, err := s.getMailClient()
	if err != nil {
		writeErrorResponse(w, http.StatusInternalServerError, ErrCodeInternalError, "failed to create mail client", nil, reqID)
		return
	}
	if client == nil {
		writeErrorResponse(w, http.StatusServiceUnavailable, ErrCodeMailUnavailable, "Agent Mail server is not available", nil, reqID)
		return
	}

	ctx, cancel := context.WithTimeout(r.Context(), 10*time.Second)
	defer cancel()

	err = client.AcknowledgeMessage(ctx, s.projectDir, agentName, messageID)
	if err != nil {
		writeAgentMailMessageActionError(w, reqID, err, "message acknowledgements are not supported by the configured Agent Mail server")
		return
	}

	s.publishMailEvent(agentName, "mail.acknowledged", map[string]interface{}{
		"agent_name": agentName,
		"message_id": messageID,
	})

	writeSuccessResponse(w, http.StatusOK, map[string]interface{}{
		"message_id":   messageID,
		"acknowledged": true,
	}, reqID)
}

// handleSearchMessages handles GET /api/v1/mail/search
func (s *Server) handleSearchMessages(w http.ResponseWriter, r *http.Request) {
	reqID := requestIDFromContext(r.Context())

	query := r.URL.Query().Get("q")
	if query == "" {
		writeErrorResponse(w, http.StatusBadRequest, ErrCodeBadRequest, "q query parameter required", nil, reqID)
		return
	}

	slog.Info("search messages", "request_id", reqID, "query", query)

	client, err := s.getMailClient()
	if err != nil {
		writeErrorResponse(w, http.StatusInternalServerError, ErrCodeInternalError, "failed to create mail client", nil, reqID)
		return
	}
	if client == nil {
		writeErrorResponse(w, http.StatusServiceUnavailable, ErrCodeMailUnavailable, "Agent Mail server is not available", nil, reqID)
		return
	}

	ctx, cancel := context.WithTimeout(r.Context(), 15*time.Second)
	defer cancel()

	limit := 20
	if l := r.URL.Query().Get("limit"); l != "" {
		if parsed, err := strconv.Atoi(l); err == nil && parsed > 0 {
			limit = parsed
		}
	}

	results, err := client.SearchMessages(ctx, agentmail.SearchOptions{
		ProjectKey: s.projectDir,
		Query:      query,
		Limit:      limit,
	})
	if err != nil {
		writeAgentMailHandlerError(w, reqID, err, "", nil, "message search is not supported by the configured Agent Mail server", "")
		return
	}

	writeSuccessResponse(w, http.StatusOK, map[string]interface{}{
		"results": results,
		"count":   len(results),
		"query":   query,
	}, reqID)
}

// handleThreadSummary handles GET /api/v1/mail/threads/{id}/summary
func (s *Server) handleThreadSummary(w http.ResponseWriter, r *http.Request) {
	reqID := requestIDFromContext(r.Context())
	threadID := chi.URLParam(r, "id")

	slog.Info("get thread summary", "request_id", reqID, "thread_id", threadID)

	if threadID == "" {
		writeErrorResponse(w, http.StatusBadRequest, ErrCodeBadRequest, "thread ID required", nil, reqID)
		return
	}

	client, err := s.getMailClient()
	if err != nil {
		writeErrorResponse(w, http.StatusInternalServerError, ErrCodeInternalError, "failed to create mail client", nil, reqID)
		return
	}
	if client == nil {
		writeErrorResponse(w, http.StatusServiceUnavailable, ErrCodeMailUnavailable, "Agent Mail server is not available", nil, reqID)
		return
	}

	ctx, cancel := context.WithTimeout(r.Context(), 30*time.Second)
	defer cancel()

	includeExamples := r.URL.Query().Get("include_examples") == "true"
	llmMode := r.URL.Query().Get("llm_mode") != "false"

	summary, err := client.SummarizeThread(ctx, agentmail.SummarizeThreadOptions{
		ProjectKey:      s.projectDir,
		ThreadID:        threadID,
		IncludeExamples: includeExamples,
		LLMMode:         llmMode,
	})
	if err != nil {
		writeAgentMailHandlerError(w, reqID, err, ErrCodeThreadNotFound, agentmail.IsNotFound, "thread summaries are not supported by the configured Agent Mail server", "")
		return
	}

	writeSuccessResponse(w, http.StatusOK, map[string]interface{}{
		"summary": summary,
	}, reqID)
}

// handleListContacts handles GET /api/v1/mail/contacts
func (s *Server) handleListContacts(w http.ResponseWriter, r *http.Request) {
	reqID := requestIDFromContext(r.Context())

	agentName := r.URL.Query().Get("agent_name")
	if agentName == "" {
		writeErrorResponse(w, http.StatusBadRequest, ErrCodeBadRequest, "agent_name query parameter required", nil, reqID)
		return
	}

	slog.Info("list contacts", "request_id", reqID, "agent", agentName)

	client, err := s.getMailClient()
	if err != nil {
		writeErrorResponse(w, http.StatusInternalServerError, ErrCodeInternalError, "failed to create mail client", nil, reqID)
		return
	}
	if client == nil {
		writeErrorResponse(w, http.StatusServiceUnavailable, ErrCodeMailUnavailable, "Agent Mail server is not available", nil, reqID)
		return
	}

	ctx, cancel := context.WithTimeout(r.Context(), 10*time.Second)
	defer cancel()

	contacts, err := client.ListContacts(ctx, s.projectDir, agentName)
	if err != nil {
		writeAgentMailAgentActionError(w, reqID, err, "listing contacts is not supported by the configured Agent Mail server")
		return
	}

	writeSuccessResponse(w, http.StatusOK, map[string]interface{}{
		"contacts": contacts,
		"count":    len(contacts),
	}, reqID)
}

// handleRequestContact handles POST /api/v1/mail/contacts/request
func (s *Server) handleRequestContact(w http.ResponseWriter, r *http.Request) {
	reqID := requestIDFromContext(r.Context())
	slog.Info("request contact", "request_id", reqID)

	var req ContactRequestBody
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeErrorResponse(w, http.StatusBadRequest, ErrCodeBadRequest, "invalid request body", nil, reqID)
		return
	}

	if req.FromAgent == "" || req.ToAgent == "" {
		writeErrorResponse(w, http.StatusBadRequest, ErrCodeBadRequest, "from_agent and to_agent are required", nil, reqID)
		return
	}

	client, err := s.getMailClient()
	if err != nil {
		writeErrorResponse(w, http.StatusInternalServerError, ErrCodeInternalError, "failed to create mail client", nil, reqID)
		return
	}
	if client == nil {
		writeErrorResponse(w, http.StatusServiceUnavailable, ErrCodeMailUnavailable, "Agent Mail server is not available", nil, reqID)
		return
	}

	ctx, cancel := context.WithTimeout(r.Context(), 10*time.Second)
	defer cancel()

	result, err := client.RequestContact(ctx, agentmail.RequestContactOptions{
		ProjectKey: s.projectDir,
		FromAgent:  req.FromAgent,
		ToAgent:    req.ToAgent,
		ToProject:  req.ToProject,
		Reason:     req.Reason,
		TTLSeconds: req.TTLSeconds,
	})
	if err != nil {
		writeErrorResponse(w, http.StatusInternalServerError, ErrCodeInternalError, err.Error(), nil, reqID)
		return
	}

	writeSuccessResponse(w, http.StatusOK, map[string]interface{}{
		"from_agent": req.FromAgent,
		"to_agent":   req.ToAgent,
		"status":     result.Status,
	}, reqID)
}

// handleRespondContact handles POST /api/v1/mail/contacts/respond
func (s *Server) handleRespondContact(w http.ResponseWriter, r *http.Request) {
	reqID := requestIDFromContext(r.Context())
	slog.Info("respond to contact request", "request_id", reqID)

	var req ContactRespondRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeErrorResponse(w, http.StatusBadRequest, ErrCodeBadRequest, "invalid request body", nil, reqID)
		return
	}

	if req.ToAgent == "" || req.FromAgent == "" {
		writeErrorResponse(w, http.StatusBadRequest, ErrCodeBadRequest, "to_agent and from_agent are required", nil, reqID)
		return
	}

	client, err := s.getMailClient()
	if err != nil {
		writeErrorResponse(w, http.StatusInternalServerError, ErrCodeInternalError, "failed to create mail client", nil, reqID)
		return
	}
	if client == nil {
		writeErrorResponse(w, http.StatusServiceUnavailable, ErrCodeMailUnavailable, "Agent Mail server is not available", nil, reqID)
		return
	}

	ctx, cancel := context.WithTimeout(r.Context(), 10*time.Second)
	defer cancel()

	err = client.RespondContact(ctx, agentmail.RespondContactOptions{
		ProjectKey: s.projectDir,
		ToAgent:    req.ToAgent,
		FromAgent:  req.FromAgent,
		Accept:     req.Accept,
		TTLSeconds: req.TTLSeconds,
	})
	if err != nil {
		writeErrorResponse(w, http.StatusInternalServerError, ErrCodeInternalError, err.Error(), nil, reqID)
		return
	}

	writeSuccessResponse(w, http.StatusOK, map[string]interface{}{
		"to_agent":   req.ToAgent,
		"from_agent": req.FromAgent,
		"accepted":   req.Accept,
	}, reqID)
}

// handleSetContactPolicy handles PUT /api/v1/mail/contacts/policy
func (s *Server) handleSetContactPolicy(w http.ResponseWriter, r *http.Request) {
	reqID := requestIDFromContext(r.Context())
	slog.Info("set contact policy", "request_id", reqID)

	var req SetContactPolicyRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeErrorResponse(w, http.StatusBadRequest, ErrCodeBadRequest, "invalid request body", nil, reqID)
		return
	}

	if req.AgentName == "" || req.Policy == "" {
		writeErrorResponse(w, http.StatusBadRequest, ErrCodeBadRequest, "agent_name and policy are required", nil, reqID)
		return
	}

	validPolicies := map[string]bool{
		"open": true, "auto": true, "contacts_only": true, "block_all": true,
	}
	if !validPolicies[req.Policy] {
		writeErrorResponse(w, http.StatusBadRequest, ErrCodeBadRequest, "policy must be one of: open, auto, contacts_only, block_all", nil, reqID)
		return
	}

	client, err := s.getMailClient()
	if err != nil {
		writeErrorResponse(w, http.StatusInternalServerError, ErrCodeInternalError, "failed to create mail client", nil, reqID)
		return
	}
	if client == nil {
		writeErrorResponse(w, http.StatusServiceUnavailable, ErrCodeMailUnavailable, "Agent Mail server is not available", nil, reqID)
		return
	}

	ctx, cancel := context.WithTimeout(r.Context(), 10*time.Second)
	defer cancel()

	err = client.SetContactPolicy(ctx, s.projectDir, req.AgentName, req.Policy)
	if err != nil {
		writeAgentMailAgentActionError(w, reqID, err, "setting contact policy is not supported by the configured Agent Mail server")
		return
	}

	writeSuccessResponse(w, http.StatusOK, map[string]interface{}{
		"agent_name": req.AgentName,
		"policy":     req.Policy,
	}, reqID)
}

// Reservation handlers

// handleListReservations handles GET /api/v1/reservations
func (s *Server) handleListReservations(w http.ResponseWriter, r *http.Request) {
	reqID := requestIDFromContext(r.Context())

	agentName := r.URL.Query().Get("agent_name")
	slog.Info("list reservations", "request_id", reqID, "agent", agentName)

	client, err := s.getMailClient()
	if err != nil {
		writeErrorResponse(w, http.StatusInternalServerError, ErrCodeInternalError, "failed to create mail client", nil, reqID)
		return
	}
	if client == nil {
		writeErrorResponse(w, http.StatusServiceUnavailable, ErrCodeMailUnavailable, "Agent Mail server is not available", nil, reqID)
		return
	}

	ctx, cancel := context.WithTimeout(r.Context(), 10*time.Second)
	defer cancel()

	// If no agent specified, list all reservations
	allAgents := agentName == ""
	reservations, err := client.ListReservations(ctx, s.projectDir, agentName, allAgents)
	if err != nil {
		writeAgentMailReservationError(w, reqID, err, "listing reservations is not supported by the configured Agent Mail server")
		return
	}

	writeSuccessResponse(w, http.StatusOK, map[string]interface{}{
		"reservations": reservations,
		"count":        len(reservations),
	}, reqID)
}

// handleReservePaths handles POST /api/v1/reservations
func (s *Server) handleReservePaths(w http.ResponseWriter, r *http.Request) {
	reqID := requestIDFromContext(r.Context())
	slog.Info("reserve paths", "request_id", reqID)

	var req ReservePathsRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeErrorResponse(w, http.StatusBadRequest, ErrCodeBadRequest, "invalid request body", nil, reqID)
		return
	}

	if req.AgentName == "" || len(req.Paths) == 0 {
		writeErrorResponse(w, http.StatusBadRequest, ErrCodeBadRequest, "agent_name and paths are required", nil, reqID)
		return
	}

	client, err := s.getMailClient()
	if err != nil {
		writeErrorResponse(w, http.StatusInternalServerError, ErrCodeInternalError, "failed to create mail client", nil, reqID)
		return
	}
	if client == nil {
		writeErrorResponse(w, http.StatusServiceUnavailable, ErrCodeMailUnavailable, "Agent Mail server is not available", nil, reqID)
		return
	}

	ctx, cancel := context.WithTimeout(r.Context(), 15*time.Second)
	defer cancel()

	ttl := req.TTLSeconds
	if ttl == 0 {
		ttl = 3600 // Default 1 hour
	}

	result, err := client.ReservePaths(ctx, agentmail.FileReservationOptions{
		ProjectKey: s.projectDir,
		AgentName:  req.AgentName,
		Paths:      req.Paths,
		TTLSeconds: ttl,
		Exclusive:  req.Exclusive,
		Reason:     req.Reason,
	})
	if err != nil && (!agentmail.IsReservationConflict(err) || result == nil) {
		writeAgentMailReservationError(w, reqID, err, "creating reservations is not supported by the configured Agent Mail server")
		return
	}
	if result == nil {
		result = &agentmail.ReservationResult{}
	}

	if len(result.Granted) > 0 {
		s.publishReservationEvent(req.AgentName, "reservation.granted", map[string]interface{}{
			"agent_name":    req.AgentName,
			"exclusive":     req.Exclusive,
			"ttl_seconds":   ttl,
			"reason":        req.Reason,
			"granted":       result.Granted,
			"granted_count": len(result.Granted),
		})
	}
	if len(result.Conflicts) > 0 {
		s.publishReservationEvent(req.AgentName, "reservation.conflict", map[string]interface{}{
			"agent_name":     req.AgentName,
			"paths":          req.Paths,
			"conflicts":      result.Conflicts,
			"conflict_count": len(result.Conflicts),
		})
	}

	status := http.StatusCreated
	if len(result.Conflicts) > 0 {
		status = http.StatusConflict
	}

	writeSuccessResponse(w, status, map[string]interface{}{
		"granted":   result.Granted,
		"conflicts": result.Conflicts,
	}, reqID)
}

// handleReleaseReservations handles DELETE /api/v1/reservations
func (s *Server) handleReleaseReservations(w http.ResponseWriter, r *http.Request) {
	reqID := requestIDFromContext(r.Context())
	slog.Info("release reservations", "request_id", reqID)

	var req ReleaseReservationsRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeErrorResponse(w, http.StatusBadRequest, ErrCodeBadRequest, "invalid request body", nil, reqID)
		return
	}

	if req.AgentName == "" {
		writeErrorResponse(w, http.StatusBadRequest, ErrCodeBadRequest, "agent_name is required", nil, reqID)
		return
	}

	client, err := s.getMailClient()
	if err != nil {
		writeErrorResponse(w, http.StatusInternalServerError, ErrCodeInternalError, "failed to create mail client", nil, reqID)
		return
	}
	if client == nil {
		writeErrorResponse(w, http.StatusServiceUnavailable, ErrCodeMailUnavailable, "Agent Mail server is not available", nil, reqID)
		return
	}

	ctx, cancel := context.WithTimeout(r.Context(), 10*time.Second)
	defer cancel()

	releaseResult, err := client.ReleaseReservations(ctx, s.projectDir, req.AgentName, req.Paths, req.IDs)
	if err != nil {
		writeAgentMailReservationError(w, reqID, err, "releasing reservations is not supported by the configured Agent Mail server")
		return
	}

	s.publishReservationEvent(req.AgentName, "reservation.released", map[string]interface{}{
		"agent_name": req.AgentName,
		"paths":      req.Paths,
		"ids":        req.IDs,
	})

	writeSuccessResponse(w, http.StatusOK, map[string]interface{}{
		"released": releaseResult.Released,
	}, reqID)
}

// handleReservationConflicts handles GET /api/v1/reservations/conflicts
func (s *Server) handleReservationConflicts(w http.ResponseWriter, r *http.Request) {
	reqID := requestIDFromContext(r.Context())

	paths := r.URL.Query()["paths"]
	if len(paths) == 0 {
		writeErrorResponse(w, http.StatusBadRequest, ErrCodeBadRequest, "paths query parameter required", nil, reqID)
		return
	}

	slog.Info("check reservation conflicts", "request_id", reqID, "paths", paths)

	client, err := s.getMailClient()
	if err != nil {
		writeErrorResponse(w, http.StatusInternalServerError, ErrCodeInternalError, "failed to create mail client", nil, reqID)
		return
	}
	if client == nil {
		writeErrorResponse(w, http.StatusServiceUnavailable, ErrCodeMailUnavailable, "Agent Mail server is not available", nil, reqID)
		return
	}

	ctx, cancel := context.WithTimeout(r.Context(), 10*time.Second)
	defer cancel()

	conflicts, err := client.CheckConflicts(ctx, s.projectDir, paths)
	if err != nil {
		writeAgentMailReservationError(w, reqID, err, "reservation conflict inspection is not supported by the configured Agent Mail server")
		return
	}

	if len(conflicts) > 0 {
		s.publishReservationEvent("", "reservation.conflict", map[string]interface{}{
			"paths":          paths,
			"conflicts":      conflicts,
			"conflict_count": len(conflicts),
		})
	}

	writeSuccessResponse(w, http.StatusOK, map[string]interface{}{
		"conflicts":     conflicts,
		"has_conflicts": len(conflicts) > 0,
	}, reqID)
}

// handleGetReservation handles GET /api/v1/reservations/{id}
func (s *Server) handleGetReservation(w http.ResponseWriter, r *http.Request) {
	reqID := requestIDFromContext(r.Context())
	idStr := chi.URLParam(r, "id")

	slog.Info("get reservation", "request_id", reqID, "reservation_id", idStr)

	reservationID, err := strconv.Atoi(idStr)
	if err != nil {
		writeErrorResponse(w, http.StatusBadRequest, ErrCodeBadRequest, "invalid reservation ID", nil, reqID)
		return
	}

	client, err := s.getMailClient()
	if err != nil {
		writeErrorResponse(w, http.StatusInternalServerError, ErrCodeInternalError, "failed to create mail client", nil, reqID)
		return
	}
	if client == nil {
		writeErrorResponse(w, http.StatusServiceUnavailable, ErrCodeMailUnavailable, "Agent Mail server is not available", nil, reqID)
		return
	}

	ctx, cancel := context.WithTimeout(r.Context(), 10*time.Second)
	defer cancel()

	reservation, err := client.GetReservation(ctx, s.projectDir, reservationID)
	if err != nil {
		writeAgentMailReservationError(w, reqID, err, "reservation lookup is not supported by the configured Agent Mail server")
		return
	}

	writeSuccessResponse(w, http.StatusOK, map[string]interface{}{
		"reservation": reservation,
	}, reqID)
}

// handleReleaseReservationByID handles POST /api/v1/reservations/{id}/release
func (s *Server) handleReleaseReservationByID(w http.ResponseWriter, r *http.Request) {
	reqID := requestIDFromContext(r.Context())
	idStr := chi.URLParam(r, "id")

	reservationID, err := strconv.Atoi(idStr)
	if err != nil {
		writeErrorResponse(w, http.StatusBadRequest, ErrCodeBadRequest, "invalid reservation ID", nil, reqID)
		return
	}

	agentName := r.URL.Query().Get("agent_name")
	if agentName == "" {
		writeErrorResponse(w, http.StatusBadRequest, ErrCodeBadRequest, "agent_name query parameter required", nil, reqID)
		return
	}

	slog.Info("release reservation by ID", "request_id", reqID, "reservation_id", reservationID, "agent", agentName)

	client, err := s.getMailClient()
	if err != nil {
		writeErrorResponse(w, http.StatusInternalServerError, ErrCodeInternalError, "failed to create mail client", nil, reqID)
		return
	}
	if client == nil {
		writeErrorResponse(w, http.StatusServiceUnavailable, ErrCodeMailUnavailable, "Agent Mail server is not available", nil, reqID)
		return
	}

	ctx, cancel := context.WithTimeout(r.Context(), 10*time.Second)
	defer cancel()

	releaseResult, err := client.ReleaseReservations(ctx, s.projectDir, agentName, nil, []int{reservationID})
	if err != nil {
		writeAgentMailReservationError(w, reqID, err, "releasing reservations is not supported by the configured Agent Mail server")
		return
	}
	if releaseResult == nil || releaseResult.Released == 0 {
		writeErrorResponse(w, http.StatusNotFound, ErrCodeNotFound, "reservation not found", nil, reqID)
		return
	}

	s.publishReservationEvent(agentName, "reservation.released", map[string]interface{}{
		"agent_name":     agentName,
		"reservation_id": reservationID,
	})

	writeSuccessResponse(w, http.StatusOK, map[string]interface{}{
		"reservation_id": reservationID,
		"released":       true,
	}, reqID)
}

// handleRenewReservation handles POST /api/v1/reservations/{id}/renew
func (s *Server) handleRenewReservation(w http.ResponseWriter, r *http.Request) {
	reqID := requestIDFromContext(r.Context())
	idStr := chi.URLParam(r, "id")

	reservationID, err := strconv.Atoi(idStr)
	if err != nil {
		writeErrorResponse(w, http.StatusBadRequest, ErrCodeBadRequest, "invalid reservation ID", nil, reqID)
		return
	}

	var req RenewReservationsRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeErrorResponse(w, http.StatusBadRequest, ErrCodeBadRequest, "invalid request body", nil, reqID)
		return
	}

	if req.AgentName == "" {
		writeErrorResponse(w, http.StatusBadRequest, ErrCodeBadRequest, "agent_name is required", nil, reqID)
		return
	}

	slog.Info("renew reservation", "request_id", reqID, "reservation_id", reservationID, "agent", req.AgentName)

	client, err := s.getMailClient()
	if err != nil {
		writeErrorResponse(w, http.StatusInternalServerError, ErrCodeInternalError, "failed to create mail client", nil, reqID)
		return
	}
	if client == nil {
		writeErrorResponse(w, http.StatusServiceUnavailable, ErrCodeMailUnavailable, "Agent Mail server is not available", nil, reqID)
		return
	}

	ctx, cancel := context.WithTimeout(r.Context(), 10*time.Second)
	defer cancel()

	extendSeconds := req.ExtendSeconds
	if extendSeconds == 0 {
		extendSeconds = 1800 // Default 30 minutes
	}

	result, err := client.RenewReservationsWithOptions(ctx, agentmail.RenewReservationsOptions{
		ProjectKey:     s.projectDir,
		AgentName:      req.AgentName,
		ExtendSeconds:  extendSeconds,
		ReservationIDs: []int{reservationID},
	})
	if err != nil {
		writeAgentMailReservationError(w, reqID, err, "renewing reservations is not supported by the configured Agent Mail server")
		return
	}

	s.publishReservationEvent(req.AgentName, "reservation.renewed", map[string]interface{}{
		"agent_name":     req.AgentName,
		"reservation_id": reservationID,
		"extend_seconds": extendSeconds,
		"renewed":        result.Renewed,
	})

	writeSuccessResponse(w, http.StatusOK, map[string]interface{}{
		"renewed":      result.Renewed,
		"reservations": result.Reservations,
	}, reqID)
}

// handleForceReleaseReservation handles POST /api/v1/reservations/{id}/force-release
func (s *Server) handleForceReleaseReservation(w http.ResponseWriter, r *http.Request) {
	reqID := requestIDFromContext(r.Context())
	idStr := chi.URLParam(r, "id")

	reservationID, err := strconv.Atoi(idStr)
	if err != nil {
		writeErrorResponse(w, http.StatusBadRequest, ErrCodeBadRequest, "invalid reservation ID", nil, reqID)
		return
	}

	var req ForceReleaseRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeErrorResponse(w, http.StatusBadRequest, ErrCodeBadRequest, "invalid request body", nil, reqID)
		return
	}

	if req.AgentName == "" {
		writeErrorResponse(w, http.StatusBadRequest, ErrCodeBadRequest, "agent_name is required", nil, reqID)
		return
	}

	slog.Info("force release reservation", "request_id", reqID, "reservation_id", reservationID, "agent", req.AgentName)

	client, err := s.getMailClient()
	if err != nil {
		writeErrorResponse(w, http.StatusInternalServerError, ErrCodeInternalError, "failed to create mail client", nil, reqID)
		return
	}
	if client == nil {
		writeErrorResponse(w, http.StatusServiceUnavailable, ErrCodeMailUnavailable, "Agent Mail server is not available", nil, reqID)
		return
	}

	ctx, cancel := context.WithTimeout(r.Context(), 10*time.Second)
	defer cancel()

	_, err = client.ForceReleaseReservation(ctx, agentmail.ForceReleaseOptions{
		ProjectKey:     s.projectDir,
		AgentName:      req.AgentName,
		ReservationID:  reservationID,
		Note:           req.Note,
		NotifyPrevious: req.NotifyPrevious,
	})
	if err != nil {
		writeAgentMailReservationError(w, reqID, err, "force releasing reservations is not supported by the configured Agent Mail server")
		return
	}

	s.publishReservationEvent(req.AgentName, "reservation.released", map[string]interface{}{
		"agent_name":      req.AgentName,
		"reservation_id":  reservationID,
		"force_released":  true,
		"notify_previous": req.NotifyPrevious,
	})

	writeSuccessResponse(w, http.StatusOK, map[string]interface{}{
		"reservation_id": reservationID,
		"force_released": true,
	}, reqID)
}
