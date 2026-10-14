# Initial work and paced delivery in swarm jobs

`POST /api/v1/jobs` with `type: "swarm_spawn"` accepts `prompt`,
`stagger_mode`, and `stagger_delay` inside `params`:

```json
{
  "type": "swarm_spawn",
  "params": {
    "session": "review",
    "cc_count": 2,
    "cod_count": 1,
    "prompt": "Read the project plan, then implement the highest-priority ready work.",
    "stagger_mode": "fixed",
    "stagger_delay": "20s",
    "startup_timeout": "5m"
  }
}
```

The shared spawn engine waits for readiness before delivering a non-blank
prompt. With `assign_work: true`, this text prefixes each agent's assignment
in **one delivery**, not a separate turn before or after the assignment.
`dry_run: true` retains the engine's preview behavior; the adapter never sends
prompts itself. Cancellation and `startup_timeout` cover delivery as well as
agent startup. A failed delivery fails the job and retains the engine's partial
result so callers can inspect created panes before retrying.

`stagger_mode` defaults to no pacing. `fixed` spaces prompt deliveries by
`stagger_delay`, defaulting to 30 seconds when omitted or null. The delay is a
Go duration string between `0s` and `5m`, inclusive; explicit `"0s"` disables
spacing. `smart` uses the spawn engine's learned provider rate-limit delays.
An explicit delay requires `fixed` mode. Active pacing requires either a
non-blank `prompt` or `assign_work: true`; it does not pace an empty swarm.

This is distinct from `launch_interval`, which spaces **process creation**,
and `launch_ready_timeout`, which gates each launch on readiness. These
controls can be combined. Use the returned spawn `stagger` schedule as the
execution evidence, rather than calculating a second schedule in the client.

Only inline prompt text is accepted. `prompt_file` is not a supported job
parameter. Existing job operation IDs bind the complete request, including
prompt and pacing controls; changing them requires a new operation ID.
