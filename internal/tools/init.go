package tools

// init registers all built-in tool adapters
func init() {
	// Register BV adapter (Beads Viewer - graph-aware triage)
	Register(NewBVAdapter())

	// Register BD adapter (Beads CLI - issue tracking)
	Register(NewBDAdapter())

	// Register AM adapter (Agent Mail MCP server)
	Register(NewAMAdapter())

	// Register CM adapter (CASS Memory)
	Register(NewCMAdapter())

	// Register CASS adapter (Cross-Agent Semantic Search)
	Register(NewCASSAdapter())

	// Register S2P adapter (Source to Prompt)
	Register(NewS2PAdapter())

	// Register JFP adapter (JeffreysPrompts CLI - prompt library)
	Register(NewJFPAdapter())

	// Register DCG adapter (Destructive Command Guard - blocks dangerous commands)
	Register(NewDCGAdapter())

	// Register SLB adapter (Simultaneous Launch Button - two-person authorization)
	Register(NewSLBAdapter())
}
