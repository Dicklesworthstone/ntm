package agentmail

import (
	"errors"
	"fmt"
)

// Common errors returned by the Agent Mail client.
var (
	// ErrServerUnavailable is returned when the Agent Mail server is not reachable.
	ErrServerUnavailable = errors.New("agent mail server unavailable")

	// ErrUnauthorized is returned when the bearer token is invalid or missing.
	ErrUnauthorized = errors.New("unauthorized: invalid or missing bearer token")

	// ErrNotFound is returned when a requested resource doesn't exist.
	ErrNotFound = errors.New("resource not found")

	// ErrInvalidRequest is returned when the request parameters are invalid.
	ErrInvalidRequest = errors.New("invalid request parameters")

	// ErrTimeout is returned when a request times out.
	ErrTimeout = errors.New("request timed out")

	// ErrAgentNotRegistered is returned when trying to use an unregistered agent.
	ErrAgentNotRegistered = errors.New("agent not registered")

	// ErrMessageNotFound is returned when a message ID doesn't exist.
	ErrMessageNotFound = errors.New("message not found")

	// ErrReservationConflict is returned when a file reservation conflicts with existing ones.
	ErrReservationConflict = errors.New("file reservation conflict")
)

// JSONRPCError represents a JSON-RPC 2.0 error response.
type JSONRPCError struct {
	Code    int         `json:"code"`
	Message string      `json:"message"`
	Data    interface{} `json:"data,omitempty"`
}

// Error implements the error interface.
func (e *JSONRPCError) Error() string {
	if e.Data != nil {
		return fmt.Sprintf("JSON-RPC error %d: %s (data: %v)", e.Code, e.Message, e.Data)
	}
	return fmt.Sprintf("JSON-RPC error %d: %s", e.Code, e.Message)
}

// APIError wraps errors from the Agent Mail API with additional context.
type APIError struct {
	Operation  string // The operation that failed (e.g., "send_message")
	StatusCode int    // HTTP status code (0 if not HTTP error)
	Err        error  // Underlying error
}

// Error implements the error interface.
func (e *APIError) Error() string {
	if e.StatusCode > 0 {
		return fmt.Sprintf("agentmail: %s failed (HTTP %d): %v", e.Operation, e.StatusCode, e.Err)
	}
	return fmt.Sprintf("agentmail: %s failed: %v", e.Operation, e.Err)
}

// Unwrap returns the underlying error for errors.Is/As support.
func (e *APIError) Unwrap() error {
	return e.Err
}

// NewAPIError creates a new APIError.
func NewAPIError(operation string, statusCode int, err error) *APIError {
	return &APIError{
		Operation:  operation,
		StatusCode: statusCode,
		Err:        err,
	}
}

// IsServerUnavailable returns true if the error indicates the server is unavailable.
func IsServerUnavailable(err error) bool {
	return errors.Is(err, ErrServerUnavailable)
}

// IsUnauthorized returns true if the error indicates an authentication failure.
func IsUnauthorized(err error) bool {
	return errors.Is(err, ErrUnauthorized)
}

// IsNotFound returns true if the error indicates a resource was not found.
func IsNotFound(err error) bool {
	return errors.Is(err, ErrNotFound)
}

// IsTimeout returns true if the error indicates a request timeout.
func IsTimeout(err error) bool {
	return errors.Is(err, ErrTimeout)
}

// IsReservationConflict returns true if the error indicates a file reservation conflict.
func IsReservationConflict(err error) bool {
	return errors.Is(err, ErrReservationConflict)
}

// mapJSONRPCError converts JSON-RPC error codes to Go errors.
func mapJSONRPCError(rpcErr *JSONRPCError) error {
	if rpcErr == nil {
		return nil
	}

	// Map common JSON-RPC error codes
	switch {
	case rpcErr.Code == -32600:
		return fmt.Errorf("%w: %s", ErrInvalidRequest, rpcErr.Message)
	case rpcErr.Code == -32601:
		return fmt.Errorf("%w: method not found", ErrInvalidRequest)
	case rpcErr.Code == -32602:
		return fmt.Errorf("%w: %s", ErrInvalidRequest, rpcErr.Message)
	default:
		// Return the raw error for application-specific codes
		return rpcErr
	}
}
