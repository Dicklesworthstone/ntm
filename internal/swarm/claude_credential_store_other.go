//go:build !darwin

package swarm

// credentialStoreIsolable reports whether excluding .credentials.json from the
// pane's config dir can actually isolate the credential on this platform.
//
// Everywhere except macOS, Claude Code stores the OAuth credential as a file in
// the config dir, which is exactly what ClaudeConfigProvisioner makes
// unreachable, so the mechanism applies. The macOS build of this function
// probes the login Keychain instead; see claude_credential_store_darwin.go.
func credentialStoreIsolable() error {
	return nil
}
