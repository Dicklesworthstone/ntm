// Package serve provides RBAC (Role-Based Access Control) for the NTM HTTP server.
package serve

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"strings"
	"time"
)

// Role represents a user's access level in the system.
type Role string

const (
	// RoleViewer can read data but not make changes.
	RoleViewer Role = "viewer"

	// RoleOperator can perform standard operations and view data.
	RoleOperator Role = "operator"

	// RoleAdmin has full access including dangerous operations and approvals.
	RoleAdmin Role = "admin"
)

// ParseRole converts a string to a Role, defaulting to viewer if unknown.
func ParseRole(s string) Role {
	switch strings.ToLower(s) {
	case "admin":
		return RoleAdmin
	case "operator":
		return RoleOperator
	case "viewer":
		return RoleViewer
	default:
		return RoleViewer
	}
}

// Permission represents a specific action that can be authorized.
type Permission string

const (
	// Read permissions
	PermReadSessions    Permission = "sessions:read"
	PermReadAgents      Permission = "agents:read"
	PermReadPipelines   Permission = "pipelines:read"
	PermReadApprovals   Permission = "approvals:read"
	PermReadJobs        Permission = "jobs:read"
	PermReadHealth      Permission = "health:read"
	PermReadEvents       Permission = "events:read"
	PermReadWebSocket    Permission = "ws:read"
	PermReadMail         Permission = "mail:read"
	PermReadReservations Permission = "reservations:read"
	PermReadBeads        Permission = "beads:read"
	PermReadAccounts     Permission = "accounts:read"
	PermReadMemory       Permission = "memory:read"

	// Write/operation permissions
	PermWriteSessions     Permission = "sessions:write"
	PermWriteAgents       Permission = "agents:write"
	PermWritePipelines    Permission = "pipelines:write"
	PermWriteJobs         Permission = "jobs:write"
	PermWriteMail         Permission = "mail:write"
	PermWriteReservations Permission = "reservations:write"
	PermWriteBeads        Permission = "beads:write"
	PermWriteAccounts     Permission = "accounts:write"
	PermWriteMemory       Permission = "memory:write"
	PermApproveRequests   Permission = "approvals:approve"

	// Dangerous operations (require admin or approval)
	PermDangerousOps    Permission = "dangerous:execute"
	PermForceRelease    Permission = "dangerous:force_release"
	PermKillAgent       Permission = "dangerous:kill_agent"
	PermSystemConfig    Permission = "system:config"
)

// rolePermissions maps roles to their granted permissions.
var rolePermissions = map[Role][]Permission{
	RoleViewer: {
		PermReadSessions,
		PermReadAgents,
		PermReadPipelines,
		PermReadApprovals,
		PermReadJobs,
		PermReadHealth,
		PermReadEvents,
		PermReadWebSocket,
		PermReadMail,
		PermReadReservations,
		PermReadBeads,
		PermReadAccounts,
		PermReadMemory,
	},
	RoleOperator: {
		// Viewer permissions
		PermReadSessions,
		PermReadAgents,
		PermReadPipelines,
		PermReadApprovals,
		PermReadJobs,
		PermReadHealth,
		PermReadEvents,
		PermReadWebSocket,
		PermReadMail,
		PermReadReservations,
		PermReadBeads,
		PermReadAccounts,
		PermReadMemory,
		// Operator permissions
		PermWriteSessions,
		PermWriteAgents,
		PermWritePipelines,
		PermWriteJobs,
		PermWriteMail,
		PermWriteReservations,
		PermWriteBeads,
		PermWriteAccounts,
		PermWriteMemory,
	},
	RoleAdmin: {
		// All viewer and operator permissions
		PermReadSessions,
		PermReadAgents,
		PermReadPipelines,
		PermReadApprovals,
		PermReadJobs,
		PermReadHealth,
		PermReadEvents,
		PermReadWebSocket,
		PermReadMail,
		PermReadReservations,
		PermReadBeads,
		PermReadAccounts,
		PermReadMemory,
		PermWriteSessions,
		PermWriteAgents,
		PermWritePipelines,
		PermWriteJobs,
		PermWriteMail,
		PermWriteReservations,
		PermWriteBeads,
		PermWriteAccounts,
		PermWriteMemory,
		// Admin-only permissions
		PermApproveRequests,
		PermDangerousOps,
		PermForceRelease,
		PermKillAgent,
		PermSystemConfig,
	},
}

// HasPermission checks if a role has a specific permission.
func (r Role) HasPermission(p Permission) bool {
	perms, ok := rolePermissions[r]
	if !ok {
		return false
	}
	for _, perm := range perms {
		if perm == p {
			return true
		}
	}
	return false
}

// RoleContext holds RBAC information for a request.
type RoleContext struct {
	Role       Role
	UserID     string
	ClaimsRaw  map[string]interface{}
}

// ctxKeyRole is the context key for RBAC context.
type ctxKeyRole struct{}

var roleContextKey = ctxKeyRole{}

// RoleFromContext extracts RBAC context from a request context.
func RoleFromContext(ctx context.Context) *RoleContext {
	if rc, ok := ctx.Value(roleContextKey).(*RoleContext); ok {
		return rc
	}
	return nil
}

// withRoleContext adds RBAC context to a context.
func withRoleContext(ctx context.Context, rc *RoleContext) context.Context {
	return context.WithValue(ctx, roleContextKey, rc)
}

// rbacMiddleware extracts role from auth claims and enforces RBAC.
func (s *Server) rbacMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Extract auth claims from context (set by authMiddleware)
		claims := extractAuthClaims(r)

		// Determine role from claims
		role := s.extractRoleFromClaims(claims)

		// Extract user ID from claims
		userID := extractUserIDFromClaims(claims)

		// Create RBAC context
		rc := &RoleContext{
			Role:      role,
			UserID:    userID,
			ClaimsRaw: claims,
		}

		// Add RBAC context to request
		ctx := withRoleContext(r.Context(), rc)
		next.ServeHTTP(w, r.WithContext(ctx))
	})
}

// extractRoleFromClaims determines the user's role from auth claims.
func (s *Server) extractRoleFromClaims(claims map[string]interface{}) Role {
	// For local mode without auth, grant admin access
	if s.auth.Mode == AuthModeLocal || s.auth.Mode == "" {
		return RoleAdmin
	}

	// Check standard claim locations for role
	// 1. "role" claim (direct)
	if role, ok := claims["role"].(string); ok {
		return ParseRole(role)
	}

	// 2. "roles" claim (array)
	if roles, ok := claims["roles"].([]interface{}); ok {
		// Find highest privilege role
		maxRole := RoleViewer
		for _, r := range roles {
			if roleStr, ok := r.(string); ok {
				parsed := ParseRole(roleStr)
				if roleHierarchy(parsed) > roleHierarchy(maxRole) {
					maxRole = parsed
				}
			}
		}
		return maxRole
	}

	// 3. Custom claim path (e.g., "ntm_role" or "https://ntm.dev/role")
	for _, key := range []string{"ntm_role", "https://ntm.dev/role", "user_role"} {
		if role, ok := claims[key].(string); ok {
			return ParseRole(role)
		}
	}

	// 4. Check realm_access.roles (Keycloak format)
	if realmAccess, ok := claims["realm_access"].(map[string]interface{}); ok {
		if roles, ok := realmAccess["roles"].([]interface{}); ok {
			for _, r := range roles {
				if roleStr, ok := r.(string); ok {
					parsed := ParseRole(roleStr)
					if parsed == RoleAdmin {
						return RoleAdmin
					}
					if parsed == RoleOperator {
						return RoleOperator
					}
				}
			}
		}
	}

	// Default to viewer if no role found
	return RoleViewer
}

// extractUserIDFromClaims gets a user identifier from claims.
func extractUserIDFromClaims(claims map[string]interface{}) string {
	// Try standard claims in order of preference
	for _, key := range []string{"sub", "user_id", "email", "preferred_username"} {
		if val, ok := claims[key].(string); ok && val != "" {
			return val
		}
	}
	return "anonymous"
}

// roleHierarchy returns a numeric hierarchy value for role comparison.
func roleHierarchy(r Role) int {
	switch r {
	case RoleAdmin:
		return 3
	case RoleOperator:
		return 2
	case RoleViewer:
		return 1
	default:
		return 0
	}
}

// RequirePermission creates a middleware that enforces a specific permission.
func (s *Server) RequirePermission(perm Permission) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			rc := RoleFromContext(r.Context())
			if rc == nil {
				reqID := requestIDFromContext(r.Context())
				log.Printf("RBAC: no role context path=%s request_id=%s", r.URL.Path, reqID)
				writeErrorResponse(w, http.StatusForbidden, ErrCodeForbidden, "access denied: no role context", nil, reqID)
				return
			}

			if !rc.Role.HasPermission(perm) {
				reqID := requestIDFromContext(r.Context())
				log.Printf("RBAC: permission denied role=%s perm=%s path=%s user=%s request_id=%s",
					rc.Role, perm, r.URL.Path, rc.UserID, reqID)
				writeErrorResponse(w, http.StatusForbidden, ErrCodeForbidden,
					fmt.Sprintf("access denied: role '%s' lacks permission '%s'", rc.Role, perm), nil, reqID)
				return
			}

			next.ServeHTTP(w, r)
		})
	}
}

// RequireRole creates a middleware that enforces a minimum role.
func (s *Server) RequireRole(minRole Role) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			rc := RoleFromContext(r.Context())
			if rc == nil {
				reqID := requestIDFromContext(r.Context())
				log.Printf("RBAC: no role context path=%s request_id=%s", r.URL.Path, reqID)
				writeErrorResponse(w, http.StatusForbidden, ErrCodeForbidden, "access denied: no role context", nil, reqID)
				return
			}

			if roleHierarchy(rc.Role) < roleHierarchy(minRole) {
				reqID := requestIDFromContext(r.Context())
				log.Printf("RBAC: insufficient role current=%s required=%s path=%s user=%s request_id=%s",
					rc.Role, minRole, r.URL.Path, rc.UserID, reqID)
				writeErrorResponse(w, http.StatusForbidden, ErrCodeForbidden,
					fmt.Sprintf("access denied: requires role '%s' or higher", minRole), nil, reqID)
				return
			}

			next.ServeHTTP(w, r)
		})
	}
}

// CheckPermission is a helper for handlers to check permissions inline.
// Returns true if permission is granted, false otherwise.
// When false, it also writes an error response.
func CheckPermission(w http.ResponseWriter, r *http.Request, perm Permission) bool {
	rc := RoleFromContext(r.Context())
	if rc == nil {
		reqID := requestIDFromContext(r.Context())
		writeErrorResponse(w, http.StatusForbidden, ErrCodeForbidden, "access denied: no role context", nil, reqID)
		return false
	}

	if !rc.Role.HasPermission(perm) {
		reqID := requestIDFromContext(r.Context())
		writeErrorResponse(w, http.StatusForbidden, ErrCodeForbidden,
			fmt.Sprintf("access denied: role '%s' lacks permission '%s'", rc.Role, perm), nil, reqID)
		return false
	}

	return true
}

// ApprovalRequired is returned when an operation requires approval.
type ApprovalRequired struct {
	Action      string `json:"action"`
	Resource    string `json:"resource"`
	ApprovalID  string `json:"approval_id"`
	ApprovalURL string `json:"approval_url,omitempty"`
	ExpiresAt   string `json:"expires_at,omitempty"`
	Message     string `json:"message"`
}

// ErrCodeApprovalRequired is the error code for operations requiring approval.
const ErrCodeApprovalRequired = "APPROVAL_REQUIRED"

// writeApprovalRequired writes a 409 response indicating approval is needed.
func writeApprovalRequired(w http.ResponseWriter, ar *ApprovalRequired, reqID string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusConflict)

	resp := struct {
		Success    bool              `json:"success"`
		Timestamp  string            `json:"timestamp"`
		RequestID  string            `json:"request_id,omitempty"`
		Error      string            `json:"error"`
		ErrorCode  string            `json:"error_code"`
		Approval   *ApprovalRequired `json:"approval"`
	}{
		Success:   false,
		Timestamp: time.Now().UTC().Format(time.RFC3339),
		RequestID: reqID,
		Error:     ar.Message,
		ErrorCode: ErrCodeApprovalRequired,
		Approval:  ar,
	}

	if err := json.NewEncoder(w).Encode(resp); err != nil {
		log.Printf("failed to encode approval required response: %v", err)
	}
}

// RBACConfig holds RBAC configuration options.
type RBACConfig struct {
	// Enabled controls whether RBAC is enforced.
	Enabled bool

	// DefaultRole is the role assigned when no role claim is found.
	DefaultRole Role

	// RoleClaimKey is the JWT claim key for role extraction.
	RoleClaimKey string

	// AllowAnonymous permits requests without authentication (as viewer).
	AllowAnonymous bool
}

// DefaultRBACConfig returns sensible RBAC defaults.
func DefaultRBACConfig() RBACConfig {
	return RBACConfig{
		Enabled:        true,
		DefaultRole:    RoleViewer,
		RoleClaimKey:   "role",
		AllowAnonymous: false,
	}
}
