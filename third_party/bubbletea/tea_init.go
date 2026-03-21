package tea

func init() {
	// NTM configures Lip Gloss background/profile explicitly during command
	// startup. The eager global background probe here adds multi-second startup
	// latency on terminals that ignore OSC background-color queries, so the
	// local dependency override intentionally disables it.
}
