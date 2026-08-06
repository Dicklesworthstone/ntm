//go:build darwin

package swarm

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"time"
)

// claudeKeychainService is the login-Keychain service name Claude Code stores
// its OAuth credential under on macOS.
const claudeKeychainService = "Claude Code-credentials"

// keychainProbeTimeout bounds the `security` call. It normally answers in
// milliseconds, but it can block on a locked Keychain, and this runs on the
// spawn path for every Claude pane.
const keychainProbeTimeout = 5 * time.Second

// security maps errSecItemNotFound (-25300) to this process exit status.
// Other non-zero statuses include authentication and interaction failures and
// must not be mistaken for proof that no shared credential exists.
const keychainItemNotFoundExitCode = 44

// credentialStoreIsolable reports whether excluding .credentials.json from the
// pane's config dir can actually isolate the credential on this platform.
//
// On macOS it usually cannot. Claude Code stores the OAuth credential in the
// login Keychain, which is process-independent and shared by every pane no
// matter what CLAUDE_CONFIG_DIR says, so the symlink farm excludes a file that
// does not exist and the isolation check passes vacuously. Detect that and fail
// closed rather than reporting a protection that is not in force.
//
// If no Keychain entry exists, the file-based mechanism is the one in play (an
// operator who has only ever authenticated via CLAUDE_CODE_OAUTH_TOKEN, or on a
// machine where Claude Code fell back to the file), and isolation proceeds as
// it does on Linux.
func credentialStoreIsolable() error {
	// An operator who has removed the Keychain item and authenticates with a
	// setup token can say so explicitly. This does not skip the isolation
	// check — the .credentials.json check still runs — it only declares which
	// store Claude Code is using on this machine, which is the one thing this
	// probe cannot infer when `security` is unavailable.
	if store := strings.TrimSpace(os.Getenv(ClaudeCredentialStoreEnvVar)); store != "" {
		switch strings.ToLower(store) {
		case "file":
			return nil
		case "keychain":
			return fmt.Errorf("%w: %s=keychain declares the credential is Keychain-held", ErrCredentialStoreNotIsolable, ClaudeCredentialStoreEnvVar)
		default:
			return fmt.Errorf("%w: %s=%q is not a recognized store (use \"file\" or \"keychain\")", ErrCredentialStoreNotIsolable, ClaudeCredentialStoreEnvVar, store)
		}
	}

	ctx, cancel := context.WithTimeout(context.Background(), keychainProbeTimeout)
	defer cancel()

	cmd := exec.CommandContext(ctx, "security", "find-generic-password", "-s", claudeKeychainService)
	err := cmd.Run()
	if ctx.Err() != nil {
		return fmt.Errorf("%w: could not determine whether the macOS Keychain holds a Claude credential (probe timed out)", ErrCredentialStoreNotIsolable)
	}
	if err == nil {
		// The entry exists: every pane reads and refreshes it regardless of
		// CLAUDE_CONFIG_DIR.
		return fmt.Errorf("%w: macOS Keychain item %q is shared by every pane. Remove it (`security delete-generic-password -s %q`) and authenticate the panes with a setup token instead",
			ErrCredentialStoreNotIsolable, claudeKeychainService, claudeKeychainService)
	}
	if exitErr, ok := err.(*exec.ExitError); ok && exitErr.ExitCode() == keychainItemNotFoundExitCode {
		// Only errSecItemNotFound means the file-backed mechanism can apply.
		return nil
	}
	// The `security` binary is missing or unrunnable, so the Keychain state is
	// unknown. Unknown is not proof of isolation.
	return fmt.Errorf("%w: could not query the macOS Keychain for %q: %v", ErrCredentialStoreNotIsolable, claudeKeychainService, err)
}
