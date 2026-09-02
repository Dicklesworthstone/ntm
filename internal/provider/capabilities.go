package provider

// EvidenceGrade states the strongest evidence a transport can produce for an
// operation. "submission" proves NTM handed work to the transport, not that a
// provider executed it. Callers must not promote a lower grade to a higher one.
type EvidenceGrade string

const (
	EvidenceUnavailable   EvidenceGrade = "unavailable"
	EvidenceSubmission    EvidenceGrade = "submission"
	EvidenceAuthoritative EvidenceGrade = "authoritative"
)

// CapacityControlScope states where admission/circuit state is coordinated.
// ProcessLocal must never be presented as a fleet-wide quota or provider
// reservation.
type CapacityControlScope string

const (
	CapacityControlScopeUnavailable  CapacityControlScope = "unavailable"
	CapacityControlScopeProcessLocal CapacityControlScope = "process_local"
)

// OperationCapabilities is a machine-readable, transport-level contract.
// IdentityProbeRequired means a launch cannot be admitted merely because the
// executable starts; the provider/model boundary must be proven independently.
type OperationCapabilities struct {
	// IdentityEvidence is the strongest evidence for the complete immutable
	// tuple (provider, account, model, endpoint, runtime, config hash). It is
	// intentionally separate from a model probe: a probe can qualify a model
	// without proving an opaque runtime still honors every configured setting.
	IdentityEvidence      IdentityEvidenceGrade `json:"identity_evidence"`
	CapacityControlScope  CapacityControlScope  `json:"capacity_control_scope"`
	Launch                EvidenceGrade         `json:"launch"`
	Delivery              EvidenceGrade         `json:"delivery"`
	Completion            EvidenceGrade         `json:"completion"`
	Cancellation          EvidenceGrade         `json:"cancellation"`
	Resume                EvidenceGrade         `json:"resume"`
	Cleanup               EvidenceGrade         `json:"cleanup"`
	IdentityProbeRequired bool                  `json:"identity_probe_required"`
	// LaunchCapacityControl covers admission of the provider process itself.
	// RequestCapacityControl and LiveErrorFeedback apply to actual model API
	// calls; an opaque TUI launch must never be promoted to request control.
	LaunchCapacityControl  EvidenceGrade `json:"launch_capacity_control"`
	RequestCapacityControl EvidenceGrade `json:"request_capacity_control"`
	LiveErrorFeedback      EvidenceGrade `json:"live_error_feedback"`
}

// CapabilityMatrix is a static declaration of transport evidence, not an
// assertion that a local account has been qualified. The key is a transport
// identifier intentionally separate from a provider profile/config surface.
func CapabilityMatrix() map[string]OperationCapabilities {
	return map[string]OperationCapabilities{
		// xAI ACP emits JSON-RPC completion metadata for session/prompt. The
		// published headless contract does not establish an authoritative cancel
		// receipt, so cancellation deliberately remains unavailable here.
		"xai_acp": {
			IdentityEvidence:     IdentityEvidenceProfileAttested,
			CapacityControlScope: CapacityControlScopeProcessLocal,
			Launch:               EvidenceAuthoritative, Delivery: EvidenceAuthoritative,
			Completion: EvidenceAuthoritative, Cancellation: EvidenceUnavailable,
			Resume: EvidenceUnavailable, Cleanup: EvidenceSubmission,
			LaunchCapacityControl: EvidenceAuthoritative, RequestCapacityControl: EvidenceAuthoritative,
			LiveErrorFeedback: EvidenceUnavailable,
		},
		// A Grok terminal pane can prove composer-ready keystroke submission;
		// output scraping is not an authoritative provider completion/cancel
		// receipt.
		"xai_grok_tui": {
			IdentityEvidence:     IdentityEvidenceProfileAttested,
			CapacityControlScope: CapacityControlScopeProcessLocal,
			Launch:               EvidenceSubmission, Delivery: EvidenceSubmission,
			Completion: EvidenceUnavailable, Cancellation: EvidenceUnavailable,
			Resume: EvidenceUnavailable, Cleanup: EvidenceSubmission,
			LaunchCapacityControl: EvidenceUnavailable, RequestCapacityControl: EvidenceUnavailable,
			LiveErrorFeedback: EvidenceUnavailable,
		},
		// Z.ai can use the Claude runtime, so an explicit endpoint/model probe
		// is mandatory before treating a process as a Z.ai lane. The runtime
		// alone has no authority to establish that provider identity.
		"zai_claude_runtime": {
			IdentityEvidence:     IdentityEvidenceProfileAttested,
			CapacityControlScope: CapacityControlScopeProcessLocal,
			Launch:               EvidenceSubmission, Delivery: EvidenceSubmission,
			Completion: EvidenceUnavailable, Cancellation: EvidenceUnavailable,
			Resume: EvidenceUnavailable, Cleanup: EvidenceSubmission,
			IdentityProbeRequired: true,
			LaunchCapacityControl: EvidenceAuthoritative, RequestCapacityControl: EvidenceUnavailable,
			LiveErrorFeedback: EvidenceUnavailable,
		},
	}
}
