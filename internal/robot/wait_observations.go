package robot

// recordWaitTransitions establishes each pane's baseline at its first successful
// observation. An unreadable pane on the first poll must not bypass the requested
// leave-and-return cycle when it first becomes readable in the target state.
func recordWaitTransitions(
	activities []*AgentActivity,
	conditions []string,
	initiallyInTarget map[string]bool,
	sawTransition map[string]bool,
) {
	for _, activity := range activities {
		inTarget := meetsAllWaitConditions(activity, conditions)
		if _, observed := initiallyInTarget[activity.PaneID]; !observed {
			initiallyInTarget[activity.PaneID] = inTarget
		} else if initiallyInTarget[activity.PaneID] && !inTarget {
			sawTransition[activity.PaneID] = true
		}
	}
}

// checkWaitPaneObservations keeps failed captures in the selected target set.
// ANY/count waits can still succeed on enough positively observed matches, but
// ALL waits cannot. ExitOnError also requires observing every selected pane:
// unreadable output cannot establish the absence of an agent error.
func checkWaitPaneObservations(
	activities []*AgentActivity,
	unobservedPanes []string,
	opts WaitOptions,
	conditions []string,
	initiallyInTarget map[string]bool,
	sawTransition map[string]bool,
) (bool, []WaitAgentInfo, []string) {
	met := len(conditions) == 0
	var matching []WaitAgentInfo
	var pending []string
	if len(conditions) > 0 {
		met, matching, pending = checkWaitConditionMetWithTransition(
			activities, opts, conditions, initiallyInTarget, sawTransition,
		)
	}

	if len(unobservedPanes) > 0 && (len(conditions) > 0 || opts.ExitOnError) {
		pending = append(pending, unobservedPanes...)
		if !opts.WaitForAny || opts.ExitOnError {
			met = false
		}
	}
	return met, matching, pending
}
