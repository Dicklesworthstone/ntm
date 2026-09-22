# Paced swarm-spawn jobs

Use the asynchronous Jobs API to launch a batch without sending every agent's
startup request at once. Submit `POST /api/v1/jobs` with type `swarm_spawn`:

```json
{
  "type": "swarm_spawn",
  "params": {
    "session": "myproject",
    "cc_count": 4,
    "cod_count": 2,
    "safety": true,
    "launch_interval": "2s",
    "wait_ready": true,
    "ready_timeout": "90s"
  }
}
```

`launch_interval` is a non-negative Go duration string, such as `500ms`, `2s`,
or `1m`. Omit it or set it to `0s` to keep the existing unpaced behavior.
Malformed, negative, overflowing, numeric, boolean, or object values fail the job before the
spawn service is called. Read the terminal job error, not just the initial
HTTP acceptance response.

The first launch starts immediately after normal preflight and session setup.
Later launch attempts start at least one interval apart, across agent types.
Slow launches consume their interval rather than adding an unconditional sleep.
Failed attempts also consume a slot; they do not cause a catch-up burst. There
is no final sleep. Pacing is local to a job, not a global provider rate limiter.
Resource-pressure admission, model validation, occupied-pane checks, reservation
policy, readiness detection, and assignment still use the shared spawn engine.

Add `"dry_run": true` to preview the batch. Preview never waits for the launch
interval or invokes the launcher. When explicitly supplied, the effective
normalized `launch_interval` is retained in the job result, including partial
failure results. The existing two-hour job timeout includes pacing time;
`ready_timeout` remains the separate agent-readiness budget.

Poll `GET /api/v1/jobs/{id}` to inspect progress and terminal results. Cancel via
`DELETE /api/v1/jobs/{id}`. Cancellation interrupts a pacing wait and prevents
later launches. It does not roll back a session or agents already created.
A cancelled job may acquire its partial result after its worker has unwound;
inspect that result before retrying to avoid duplicating a swarm.

This control is on the asynchronous `swarm_spawn` Jobs API. It does not add a
`--spawn-stagger` flag to `--robot-spawn`, and does not change the human
`ntm spawn --stagger` prompt-stagger behavior.

Regression coverage: `TestSpawnLaunchInterval*` in `internal/robot` exercises
real pacing, receipt preservation, and cancellation; `TestSwarmJobLaunchPacing*`
in `internal/serve` exercises HTTP job dispatch, strict validation, previews,
and recovery-result propagation.
