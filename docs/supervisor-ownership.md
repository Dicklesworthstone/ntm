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
