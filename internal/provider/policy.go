package provider

// DefaultZAIAutomationPolicyName is the only policy identifier accepted for
// unattended Z.ai provider profiles. Z.ai panes currently launch through an
// operator-supplied Claude-compatible runtime, so NTM binds this name to the
// immutable, reviewed config manifest hash and rejects known bypass flags. It
// does not claim to introspect an opaque wrapper after launch.
const DefaultZAIAutomationPolicyName = "zai-readonly-ci"
