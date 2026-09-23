# Active probe execution

`--robot-probe` resolves the complete requested target set before sending input.
A missing selector, invalid/duplicate physical pane ID, unsupported target, or
explicitly selected dead/service pane rejects the batch before any probe starts.
The default agent set skips dead and service panes. `wake_ping` preflights its
entire target set as agents, rather than sending input to earlier panes before
rejecting a later user shell.

Execution binds capture, stimulus, cleanup, and aggressive escalation to the
admitted physical `%N` pane ID. Index or window renumbering cannot redirect it.
The target must still belong to the selected session and match its admitted
provider, PID, and foreground command after baseline capture, after response
capture, and before cleanup or escalation. A disappeared, moved-to-another-session,
retagged, dead, or replaced observed process fails rather than targeting its
replacement. `pane_ref` remains the admission-time address; `pane_id` identifies
the physical recipient of the probe.

Capture, stimulus, identity-check, cleanup, and wake-tail failures are operational
errors, not evidence of a stuck agent. They produce a failing result with
`recommendation: "unknown"` and never authorize an aggressive interrupt.
`summary.errors` counts these separately from measured `unresponsive` probes.
Independent completed results survive a later pane failure. A response already
observed remains visible even when subsequent cleanup fails, but that probe is
not counted as a successful responsive probe.

`probe_details.input_attempted` marks an attempted input call. `input_sent` only
names acknowledged input; a failed call can still have had an uncertain external
effect. The space probe reports `cleanup_attempted` and `cleanup_succeeded`; the
latter acknowledges the backspace call, not proof the UI consumed it. A failed
space call is not followed by blind backspace, and a changed recipient receives
no cleanup. Inspect the named pane before retrying an uncertain probe. A normal
observed timeout can still escalate when explicitly requested; `probe_method`
then names the interrupt method actually used.

These checks are observations, not atomic compare-and-send or exclusive ownership
of a TUI composer. They do not prevent a concurrent operator from typing, detect
every same-PID process change, or prove that unrelated screen activity was caused
by the probe. Existing integer selector syntax and cancellation behavior are
unchanged by this execution update; this does not close the full selector-grammar
request in issue #329.
