package cli

import (
	"testing"
)

func TestDetectRateLimit(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		output   string
		expected bool
	}{
		// Positive cases
		{name: "rate limit exact", output: "You've hit your rate limit", expected: true},
		{name: "rate limit with space", output: "rate limit exceeded", expected: true},
		{name: "rate limit uppercase", output: "RATE LIMIT reached", expected: true},
		{name: "usage limit", output: "usage limit reached", expected: true},
		{name: "too many requests", output: "too many requests, please wait", expected: true},
		{name: "quota exceeded", output: "Your quota exceeded for today", expected: true},
		{name: "HTTP 429", output: "Error 429: Too many requests", expected: true},
		{name: "429 standalone", output: "got 429 from API", expected: true},
		{name: "try again later", output: "please try again later", expected: true},
		{name: "try again in", output: "please try again in 5 minutes", expected: true},
		{name: "youve hit limit", output: "youve hit your limit", expected: true},
		{name: "you hit limit no ve", output: "you hit your limit", expected: false}, // pattern requires 've or nothing, not just "you hit"
		{name: "hit limit simple", output: "You've hit limit", expected: true},

		// Negative cases
		{name: "normal output", output: "Processing file...", expected: false},
		{name: "empty string", output: "", expected: false},
		{name: "partial rate", output: "acceleration rate", expected: false},
		{name: "partial limit", output: "no limit to creativity", expected: false},
		{name: "different context", output: "the rating limit was good", expected: false},
		{name: "number 429 in context", output: "address 14290 main st", expected: false},
		{name: "try but not rate", output: "try running the command again", expected: false},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			result := detectRateLimit(tc.output)
			if result != tc.expected {
				t.Errorf("detectRateLimit(%q) = %v; want %v", tc.output, result, tc.expected)
			}
		})
	}
}

func TestDetectErrors(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name          string
		output        string
		expectedLen   int
		expectedFirst string
	}{
		// Positive cases - patterns require .{10,50} after prefix
		{
			name:          "error prefix",
			output:        "error: could not connect to server",
			expectedLen:   1,
			expectedFirst: "error: could not connect to server",
		},
		{
			name:          "exception prefix",
			output:        "exception: null pointer dereference in main",
			expectedLen:   1,
			expectedFirst: "exception: null pointer dereference in main",
		},
		{
			name:        "panic prefix",
			output:      "panic: runtime error: invalid memory address",
			expectedLen: 2, // Matches both "error:" pattern AND "panic:" pattern
		},
		{
			name:          "failed to",
			output:        "failed to open file config.yaml",
			expectedLen:   1,
			expectedFirst: "failed to open file config.yaml",
		},
		{
			name:          "SIGSEGV",
			output:        "received signal: SIGSEGV",
			expectedLen:   1,
			expectedFirst: "SIGSEGV",
		},
		{
			name:          "connection refused",
			output:        "connection refused when connecting to localhost:8080",
			expectedLen:   1,
			expectedFirst: "connection refused",
		},
		{
			name:          "unauthorized",
			output:        "request unauthorized: invalid API key",
			expectedLen:   1,
			expectedFirst: "unauthorized",
		},
		{
			name:          "authentication failed",
			output:        "authentication failed for user admin",
			expectedLen:   1,
			expectedFirst: "authentication failed",
		},
		{
			name:        "multiple errors max 2 per pattern",
			output:      "error: first issue here found\nerror: second issue there now",
			expectedLen: 2, // Max 2 matches per pattern
		},
		{
			name:        "no errors",
			output:      "Everything is working fine\nCompleted successfully",
			expectedLen: 0,
		},
		{
			name:        "empty string",
			output:      "",
			expectedLen: 0,
		},
		{
			name:        "error too short",
			output:      "error: bad", // Less than 10 chars after "error:"
			expectedLen: 0,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			result := detectErrors(tc.output)

			if len(result) != tc.expectedLen {
				t.Fatalf("detectErrors() returned %d errors; want %d\nGot: %v",
					len(result), tc.expectedLen, result)
			}

			if tc.expectedFirst != "" && len(result) > 0 {
				if result[0] != tc.expectedFirst {
					t.Errorf("detectErrors()[0] = %q; want %q", result[0], tc.expectedFirst)
				}
			}
		})
	}
}

func TestDetectErrors_NoDuplicates(t *testing.T) {
	t.Parallel()

	output := "error: same error message here\nerror: same error message here\nerror: same error message here"
	result := detectErrors(output)

	if len(result) != 1 {
		t.Errorf("detectErrors() returned %d errors; want 1 (duplicates should be removed)\nGot: %v",
			len(result), result)
	}
}

func TestDetectErrors_MaxThree(t *testing.T) {
	t.Parallel()

	output := `error: first unique error message
error: second unique error message
error: third unique error message
error: fourth unique error message
error: fifth unique error message`

	result := detectErrors(output)

	if len(result) > 3 {
		t.Errorf("detectErrors() returned %d errors; want max 3\nGot: %v",
			len(result), result)
	}
}
