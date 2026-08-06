//go:build darwin

package swarm

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func TestCredentialStoreIsolableDarwinDistinguishesKeychainErrors(t *testing.T) {
	t.Setenv(ClaudeCredentialStoreEnvVar, "")
	binDir := t.TempDir()
	securityPath := filepath.Join(binDir, "security")

	tests := []struct {
		name         string
		exitCode     string
		wantIsolable bool
	}{
		{
			name:         "item not found",
			exitCode:     "44",
			wantIsolable: true,
		},
		{
			name:         "keychain interaction denied",
			exitCode:     "36",
			wantIsolable: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			script := "#!/bin/sh\nexit " + tt.exitCode + "\n"
			if err := os.WriteFile(securityPath, []byte(script), 0o700); err != nil {
				t.Fatalf("write fake security: %v", err)
			}
			t.Setenv("PATH", binDir)

			err := credentialStoreIsolable()
			if tt.wantIsolable {
				if err != nil {
					t.Fatalf("credentialStoreIsolable() error = %v, want nil", err)
				}
				return
			}
			if !errors.Is(err, ErrCredentialStoreNotIsolable) {
				t.Fatalf("credentialStoreIsolable() error = %v, want ErrCredentialStoreNotIsolable", err)
			}
		})
	}
}
