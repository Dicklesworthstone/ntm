# Project daemon ownership

`Supervisor.Start` acquires a project-scoped, cross-process fence for each daemon
name before inspecting ports or launching processes. Separate sessions using the
same project's daemon name cannot start a second instance during startup,
health degradation, automatic restart backoff, or graceful shutdown. Project
symlink aliases share the fence; different projects and daemon names remain
independent. This does not change the default external ownership of Agent Mail.

An occupied fence returns `ErrDaemonOwned`. The supervisor does not attach to,
stop, or silently adopt the other owner's daemon. Existing fixed-port checks
still apply, including `NoPortFallback`; a free port is not ownership evidence.

## Crash recovery

The fence is a stable hidden `.daemon-<hash>.lock` file under `.ntm/pids/`.
Do not unlink a live lock file. Its separately published `.json` record is private
and records only daemon name, owner/session identity, supervisor PID, child PID,
phase, and update time. It does not contain commands, arguments, or environment.

A `launching` record is flushed before exec, and a `running` record containing
the child PID is flushed after exec. If saving the PID fails, the current
supervisor stops and reaps the child it just launched rather than dispatching
another generation. A failed exec records that no child was created.

After a supervisor exits, its kernel lock is released. The next starter must
also check the retained launch record. A still-live recorded process (or Unix
process group) is refused with `ErrDaemonRecoveryRequired`; no recovered PID is
signaled. PID reuse can cause a conservative refusal, not a kill of an unrelated
process. A recorded process and group known to be absent allow a fresh launch.
An interrupted launch without a recorded PID, malformed history, or unavailable
process inspection requires operator inspection rather than automatic replay.
`DaemonOwnershipError` identifies the retained record and any known prior owner
and PID. An in-process lifecycle failure is also exposed as `last_error` in the
`GetDaemon` snapshot.

Normal stop records quiescence and removes its compatibility PID file while
still holding the fence. Delayed shutdown of a retired supervisor cannot remove
a newer owner's PID file. Restart exhaustion releases the fence only after
checking process quiescence; uncertain history remains available for inspection.

## Health evidence and lifecycle

Health monitors belong to one launched process generation. Stop, shutdown, and
process exit cancel in-flight HTTP body reads and command probes. Shutdown joins
the monitor before releasing ownership, and restart joins the old monitor before
launching its successor. A result arriving after cancellation or retirement does
not update the daemon's state or health timestamp. Cancelling observation does not
cancel the daemon's separate process context or skip its graceful stop window.

Command probes use the effective launch directory and environment captured when
the daemon starts, including `DaemonSpec.WorkDir` and `DaemonSpec.Env`. Later
changes to the supervisor process's environment do not redirect the probe. Unix
command probes run in their own process group, which is terminated on cancellation.

Snapshots expose `health_mode` (`process`, `http`, `mcp`, or `command`) and the
most recent `health_error`. A process with no configured health probe transitions
out of `starting` when observed alive, but leaves `last_health` unset: process
liveness is not protocol readiness. Protocol/command success records `last_health`;
failure preserves the last success and the existing unhealthy/recovery timing.
Health failure does not itself authorize killing or relaunching the daemon.

HTTP health responses must finish successfully within the bounded probe context
and one-MiB response limit. MCP responses additionally require the matching request
ID, JSON-RPC 2.0, no error, an initialize-shaped server identity, and no trailing
payload. Truncated or oversized responses are not successful health checks.
These checks do not authenticate a service or prove that it owns a particular
socket or data store. Existing default polling and startup timeouts are retained.

`ntm memory serve` now reports terminal ownership/persistence failures even when
they stop recovery before the restart budget is exhausted. Normal exhaustion
retires the compatibility PID file while still holding ownership; uncertain
history is retained rather than turned into an apparently successful shutdown.

## Boundaries

The fence coordinates updated NTM supervisors in the same project namespace.
It is not a global lock over arbitrary data stores, ports, external daemons,
other daemon names, copied projects, or older NTM processes that do not take the
fence. It does not implement adoption of orphaned processes or remote leases.
Processes that deliberately escape their Unix process group are not tracked.
Windows recovery checks the direct process only, not its descendants.

Unix uses advisory `flock`; Windows uses an exclusively opened file handle.
Linux behavior is exercised with real independent processes. Other platform
runtime behavior, hostile filesystem mutation, network filesystems, and
power-loss durability require separate validation. Record files are synced;
Windows skips directory sync, and newly created ancestors do not have a
recursive durability guarantee. Failed or uncertain records are never discarded
automatically to make a launch succeed.
