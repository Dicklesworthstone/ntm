package serve

// jobExecutionResources maps the normalized engine request to exclusive
// execution scopes. These are scheduling keys, not authorization decisions:
// the existing dispatcher still validates every option and enforces policy.
// Unknown or implicit targets use a global barrier rather than guessing a pane
// or racing an operation whose destination has not yet been resolved.
func jobExecutionResources(jobType string, params map[string]interface{}) []string {
	session, ok := params["session"].(string)
	if !ok || session == "" {
		return []string{"*"}
	}
	switch jobType {
	case JobTypePipelineRun, JobTypePipelineExec:
		return []string{"session:" + session}
	case JobTypeSwarmSpawn:
		if raw, present := params["label"]; present {
			label, ok := raw.(string)
			if !ok {
				return []string{"*"}
			}
			if label != "" {
				session += "--" + label
			}
		}
		return []string{"session:" + session}
	case JobTypeCheckpointRestore:
		// The source is an artifact namespace, not necessarily the destination.
		// Without an explicit target the checkpoint metadata selects it.
		target, ok := params["target_session"].(string)
		if !ok || target == "" {
			return []string{"*"}
		}
		return []string{"session:" + target}
	case JobTypePipelineResume:
		// A resume also writes its saved run. Until that identity and its
		// implicit session are pinned together, serialize it against all work.
		return []string{"*"}
	default:
		return []string{"*"}
	}
}
