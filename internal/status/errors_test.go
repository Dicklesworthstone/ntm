package status

import (
	"strings"
	"testing"
)

func TestDetectErrorInOutput_RateLimiting(t *testing.T) {
	tests := []struct {
		name     string
		output   string
		expected ErrorType
	}{
		{
			name:     "rate limit message",
			output:   "Error: rate limit exceeded",
			expected: ErrorRateLimit,
		},
		{
			name:     "rate-limit with hyphen",
			output:   "rate-limit error",
			expected: ErrorRateLimit,
		},
		{
			name:     "HTTP 429",
			output:   "HTTP Error 429: Too Many Requests",
			expected: ErrorRateLimit,
		},
		{
			name:     "too many requests",
			output:   "Error: Too many requests, please slow down",
			expected: ErrorRateLimit,
		},
		{
			name:     "quota exceeded",
			output:   "API quota exceeded for project",
			expected: ErrorRateLimit,
		},
		{
			name:     "try again later",
			output:   "Please try again later",
			expected: ErrorRateLimit,
		},
		{
			name:     "requests per minute",
			output:   "Limit: 60 requests per minute",
			expected: ErrorRateLimit,
		},
		{
			name:     "throttled",
			output:   "Request was throttled",
			expected: ErrorRateLimit,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := DetectErrorInOutput(tt.output)
			if result != tt.expected {
				t.Errorf("DetectErrorInOutput(%q) = %v, want %v", tt.output, result, tt.expected)
			}
		})
	}
}

func TestDetectErrorInOutput_Authentication(t *testing.T) {
	tests := []struct {
		name     string
		output   string
		expected ErrorType
	}{
		{
			name:     "HTTP 401",
			output:   "HTTP Error 401: Unauthorized",
			expected: ErrorAuth,
		},
		{
			name:     "HTTP 403",
			output:   "403 Forbidden - Access Denied",
			expected: ErrorAuth,
		},
		{
			name:     "unauthorized",
			output:   "Request unauthorized, check your credentials",
			expected: ErrorAuth,
		},
		{
			name:     "invalid token",
			output:   "Error: invalid token provided",
			expected: ErrorAuth,
		},
		{
			name:     "expired api key",
			output:   "Your expired api key cannot be used",
			expected: ErrorAuth,
		},
		{
			name:     "authentication failed",
			output:   "Authentication failed: bad credentials",
			expected: ErrorAuth,
		},
		{
			name:     "access denied",
			output:   "Access denied to resource",
			expected: ErrorAuth,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := DetectErrorInOutput(tt.output)
			if result != tt.expected {
				t.Errorf("DetectErrorInOutput(%q) = %v, want %v", tt.output, result, tt.expected)
			}
		})
	}
}

func TestDetectErrorInOutput_Connection(t *testing.T) {
	tests := []struct {
		name     string
		output   string
		expected ErrorType
	}{
		{
			name:     "connection refused",
			output:   "Error: connection refused",
			expected: ErrorConnection,
		},
		{
			name:     "connection reset",
			output:   "connection reset by peer",
			expected: ErrorConnection,
		},
		{
			name:     "connection timed out",
			output:   "Error: connection timed out",
			expected: ErrorConnection,
		},
		{
			name:     "ECONNREFUSED",
			output:   "Error: ECONNREFUSED",
			expected: ErrorConnection,
		},
		{
			name:     "ETIMEDOUT",
			output:   "syscall error: ETIMEDOUT",
			expected: ErrorConnection,
		},
		{
			name:     "network error",
			output:   "Network error: unable to reach server",
			expected: ErrorConnection,
		},
		{
			name:     "dns resolution error",
			output:   "DNS resolution failed for host",
			expected: ErrorConnection,
		},
		{
			name:     "socket hang up",
			output:   "Error: socket hang up",
			expected: ErrorConnection,
		},
		{
			name:     "host not found",
			output:   "Error: host not found",
			expected: ErrorConnection,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := DetectErrorInOutput(tt.output)
			if result != tt.expected {
				t.Errorf("DetectErrorInOutput(%q) = %v, want %v", tt.output, result, tt.expected)
			}
		})
	}
}

func TestDetectErrorInOutput_Crash(t *testing.T) {
	tests := []struct {
		name     string
		output   string
		expected ErrorType
	}{
		{
			name:     "go panic",
			output:   "panic: runtime error: index out of range",
			expected: ErrorCrash,
		},
		{
			name:     "fatal error",
			output:   "fatal: unable to access repository",
			expected: ErrorCrash,
		},
		{
			name:     "segmentation fault",
			output:   "segmentation fault (core dumped)",
			expected: ErrorCrash,
		},
		{
			name:     "SIGSEGV",
			output:   "received signal SIGSEGV",
			expected: ErrorCrash,
		},
		{
			name:     "python traceback",
			output:   "Traceback (most recent call last):\n  File \"main.py\"",
			expected: ErrorCrash,
		},
		{
			name:     "unhandled exception",
			output:   "Unhandled exception in main thread",
			expected: ErrorCrash,
		},
		{
			name:     "stack trace",
			output:   "Stack trace:\n  at main.go:42",
			expected: ErrorCrash,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := DetectErrorInOutput(tt.output)
			if result != tt.expected {
				t.Errorf("DetectErrorInOutput(%q) = %v, want %v", tt.output, result, tt.expected)
			}
		})
	}
}

func TestDetectErrorInOutput_NoError(t *testing.T) {
	tests := []struct {
		name   string
		output string
	}{
		{
			name:   "normal output",
			output: "Successfully completed operation",
		},
		{
			name:   "status message",
			output: "Processing files...\nDone.",
		},
		{
			name:   "empty string",
			output: "",
		},
		{
			name:   "claude prompt",
			output: "Here's the code:\n```go\nfunc main() {}\n```\nclaude>",
		},
		{
			name:   "numbers that look like status codes",
			output: "Processed 401 records in 403ms",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := DetectErrorInOutput(tt.output)
			if result != ErrorNone {
				t.Errorf("DetectErrorInOutput(%q) = %v, want ErrorNone", tt.output, result)
			}
		})
	}
}

func TestDetectErrorInOutput_OnlyChecksRecent(t *testing.T) {
	// Create output with old error (more than 50 lines ago)
	var lines []string
	lines = append(lines, "Error: rate limit exceeded") // Old error
	for i := 0; i < 60; i++ {
		lines = append(lines, "Normal output line")
	}
	output := strings.Join(lines, "\n")

	result := DetectErrorInOutput(output)
	if result != ErrorNone {
		t.Errorf("DetectErrorInOutput should not detect errors beyond 50 lines, got %v", result)
	}
}

func TestDetectErrorInOutput_WithANSI(t *testing.T) {
	// Test that ANSI codes don't interfere with detection
	output := "\x1b[31mError: rate limit exceeded\x1b[0m"

	result := DetectErrorInOutput(output)
	if result != ErrorRateLimit {
		t.Errorf("DetectErrorInOutput with ANSI = %v, want ErrorRateLimit", result)
	}
}

func TestDetectAllErrorsInOutput(t *testing.T) {
	// Output with multiple error types
	output := "rate limit exceeded\nconnection refused\n401 Unauthorized"

	errors := DetectAllErrorsInOutput(output)

	// Should find at least 3 distinct error types
	if len(errors) < 3 {
		t.Errorf("DetectAllErrorsInOutput found %d errors, want at least 3", len(errors))
	}

	// Check that all expected types are present
	found := make(map[ErrorType]bool)
	for _, e := range errors {
		found[e] = true
	}

	if !found[ErrorRateLimit] {
		t.Error("Expected ErrorRateLimit in results")
	}
	if !found[ErrorConnection] {
		t.Error("Expected ErrorConnection in results")
	}
	if !found[ErrorAuth] {
		t.Error("Expected ErrorAuth in results")
	}
}

func TestIsError(t *testing.T) {
	tests := []struct {
		errorType ErrorType
		expected  bool
	}{
		{ErrorNone, false},
		{ErrorRateLimit, true},
		{ErrorAuth, true},
		{ErrorConnection, true},
		{ErrorCrash, true},
		{ErrorGeneric, true},
	}

	for _, tt := range tests {
		t.Run(string(tt.errorType), func(t *testing.T) {
			result := IsError(tt.errorType)
			if result != tt.expected {
				t.Errorf("IsError(%v) = %v, want %v", tt.errorType, result, tt.expected)
			}
		})
	}
}
