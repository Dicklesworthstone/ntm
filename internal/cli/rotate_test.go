package cli

import (
	"strings"
	"testing"
)

func TestRotateCmdValidation(t *testing.T) {
	tests := []struct {
		name                     string
		args                     []string
		flags                    map[string]string
		wantError                string
		skipIfAutoSelectPossible bool // Skip if exactly one session is running (auto-select applies)
	}{
		{
			name:                     "missing session and not in tmux",
			args:                     []string{},
			wantError:                "session",
			skipIfAutoSelectPossible: true, // Session auto-selected when only one exists
		},
		{
			name:      "missing pane index",
			args:      []string{"mysession"},
			wantError: "pane index required",
		},
		{
			name: "dry run requires valid session/pane",
			args: []string{"mysession"},
			flags: map[string]string{
				"pane":    "0",
				"dry-run": "true",
			},
			// Dry run still needs to look up pane info, which fails without tmux
			wantError: "getting panes",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if tt.skipIfAutoSelectPossible && sessionAutoSelectPossible() {
				t.Skip("Skipping: exactly one tmux session running (auto-selection applies)")
			}

			cmd := newRotateCmd()
			// Redirect output to discard
			cmd.SetOut(nil)
			cmd.SetErr(nil)

			// Set args
			if len(tt.args) > 0 {
				cmd.SetArgs(tt.args)
			} else {
				cmd.SetArgs([]string{})
			}

			// Set flags
			for k, v := range tt.flags {
				_ = cmd.Flags().Set(k, v)
			}

			// Execute
			err := cmd.Execute()

			if tt.wantError != "" {
				if err == nil {
					t.Errorf("expected error containing %q, got nil", tt.wantError)
				} else if len(tt.wantError) > 0 && err.Error() != "" && !strings.Contains(err.Error(), tt.wantError) {
					t.Errorf("expected error containing %q, got %q", tt.wantError, err.Error())
				}
			} else {
				if err != nil {
					t.Errorf("unexpected error: %v", err)
				}
			}
		})
	}
}
