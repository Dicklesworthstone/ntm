# Durable project webhook delivery

Project webhooks can retain accepted deliveries across NTM process exits and
crashes. Enable this before running the NTM commands that produce events:

```sh
export NTM_WEBHOOK_OUTBOX_DIR="${XDG_STATE_HOME:-$HOME/.local/state}/ntm/webhooks"
```

Continue configuring endpoints, event subscriptions, formatters, signing secrets,
and retries in the project's existing `.ntm.yaml` or `.ntm.yml` `webhooks:` list.
No new daemon or endpoint configuration format is required. With the environment
variable unset, existing volatile delivery remains unchanged. Durable mode uses
Unix file locking; it is not supported on Windows.

## Acceptance, recovery, and delivery identity

The project event-bus bridge writes a receipt before an HTTP attempt. The same
bounded HTTP workers used by volatile delivery send durable requests; there is
no second delivery engine. Failed durable acceptance is reported as a warning
rather than silently falling back to the memory queue. An internal caller of
`WebhookManager.Dispatch` receives the error directly. A fanout can be partially
accepted when storage fails partway through; accepted destinations remain safe.
This does not make events durable before they reach the bridge: event-emitter
buffer drops and a crash before durable admission remain outside this guarantee.

After restart, a manager for the same project and matching endpoint configuration
recovers pending receipts automatically, without publishing the event again.
Another NTM process with the same outbox can recover a crashed sender's work;
a live sender retains an OS lock through each request. No delivery happens while
all managers are stopped. Shutdown retains unfinished durable work instead of
converting it into an evicted in-memory dead letter.

Requests retain their `X-NTM-Delivery-ID`, exact serialized body, and signing
configuration. Attempt counts and retry deadlines, including the receiver's
`Retry-After`, survive restart. Custom headers cannot replace the delivery ID,
event type, or attempt metadata. The receiver should deduplicate side effects by
`X-NTM-Delivery-ID`: a crash after the receiver accepts a request but before the
sender durably acknowledges it can cause another attempt. This is not an
exactly-once guarantee. Receipts for acknowledged successes are removed; submitting
an event again after that removal is a new acceptance, not a durable deduplication
ledger. Internal callers can supply a stable event ID **and timestamp** to retry
partial admission; matching pending receipts are retained without resetting their
attempts or cooldown, and conflicting content for an existing ID is rejected.

## Storage and privacy

An absolute outbox root is shared safely across projects by adding a SHA-256
namespace for each canonical project directory. A relative root is anchored to
that project, not the caller's working directory. Symlink aliases of the same
project resolve to the same namespace. The leaf directory must be private
(mode `0700`); receipts and lock files use `0600`. Temporary writes are synchronized,
atomically renamed, and followed by directory synchronization. Do not place the
outbox on a filesystem that lacks reliable advisory locks, atomic rename, or sync.
Do not remove or replace its fixed lock files while managers are running.

Receipts contain the already-sanitized event and frozen payload, plus an endpoint
configuration fingerprint. Endpoint URLs, HTTP authorization headers, and signing
secrets are not copied into receipts; transport errors and receiver bodies are
not persisted verbatim. Payloads can still contain sensitive data when redaction
is disabled or a secret is not recognized. Protect the storage accordingly.
Changing the endpoint, headers, signing key, formatter, retry configuration, or
redaction policy prevents automatic replay of old receipts. Those receipts stay
on disk for inspection; they are not redirected to a newly configured endpoint.
Restoring the original enabled configuration permits pending receipts to resume.

The project bridge uses NTM's session privacy policy at admission and replay.
Sessions forbidden from persisting retain their existing volatile delivery and
do not write event receipts. Internal callers can set `Event.Transient` or supply
`ManagerConfig.PersistenceAllowed` for the same behavior. Turning on privacy does
not erase receipts already written, and privacy mode is not a network-delivery
opt-out.

## Bounded retention and failure inspection

The default limit is 1,000 disk receipts per project, each at most 2 MiB in its
serialized form. Pending deliveries, permanent failures, configuration-mismatched
receipts, and malformed `.json` records all consume the disk-count limit. The
memory queue remains independently bounded. Internal manager callers can set
`OutboxLimit`; this initial project-bridge surface uses the default.

Permanent HTTP failures and exhausted retries are retained with `failed: true`
and sanitized attempt metadata. They do not replay automatically. Malformed,
future-version, non-private, or inconsistent records are retained without sending.
A full outbox rejects further acceptance instead of deleting older work.

Use `ntm webhook-outbox list --project /path/to/project` to inspect retained work.
The command prints JSON without sending HTTP, creating storage, or exposing request
bodies, event messages, or endpoint credentials. Invalid records remain visible
because they consume capacity. A missing outbox is an empty result. `blocked_by`
explains configuration, privacy, and recovery-evidence restrictions. The snapshot
is per-record, not transactional across a directory that workers may be updating.

After repairing the receiver, explicitly authorize recovery of one failed receipt:

```sh
ntm webhook-outbox retry RECORD --revision REVISION --project /path/to/project
```

Copy `record` and `revision` from the inspected entry. Pending, malformed, changed,
busy, privacy-blocked, or configuration-mismatched records cannot be retried. The
revision is checked under the same OS lock used by senders, so concurrent operators
cannot authorize the same observed failure twice. Inspect again after any conflict;
do not blindly regenerate revisions in a retry loop.

Authorization is durable but does not itself send HTTP. A live or later project
manager picks up the receipt after the preserved cooldown. Recovery keeps the same
delivery ID, frozen body, and cumulative `X-NTM-Attempt`; the configured retry budget
starts a new cycle. Previous terminal evidence and authorization time are appended
to the receipt (at most 64 recovery cycles). Terminal `Retry-After` deadlines are
retained too. Repairing the receiver does not authorize changing the recorded
destination, signing credentials, or payload. Ordinary at-least-once behavior and
receiver deduplication remain necessary across crashes.

There is no automatic dead-letter replay or pruning. Never clear pending receipts
or lock files merely to silence an admission error.

## Reclaim active capacity without discarding evidence

```sh
ntm webhook-outbox archive RECORD --revision REVISION --project /path/to/project
```

Use the inspected `record` and `revision`, just as for retry. Archive only accepts
valid, unchanged failed receipts. It never removes pending or malformed records,
overrides sender ownership, or sends a request. Retiring a terminal record does
not require its old endpoint to remain configured and does not authorize replay
of a privacy-blocked event.

The command preserves the exact receipt bytes (including recovery history) under
`archived/REVISION/RECORD` in the same private project outbox. It publishes a private
manifest with the inspection revision and archive time. The receipt, manifest and
all directory entries are synchronized before the active name is removed. The
admission and sender locks prevent races with new acceptance, recovery and HTTP
workers. Repeating an interrupted or completed archive is safe; a changed active
receipt or conflicting archive is refused rather than overwritten. The filesystem
must support hard links; failure leaves the original active receipt intact.

Archived receipts no longer consume the active outbox record limit and are never
scanned for delivery. This frees **admission capacity, not disk bytes**: the evidence
remains on disk. Archive retention is operator-managed; there is no automatic
pruning or archived-record replay. Submitting an archived event ID again can create
a new acceptance, since the outbox is not a permanent deduplication ledger. Preserve
receiver-side idempotency and do not resubmit old events merely to work around a
failed or refused administrative command.

## Restrict destinations by session, provider, and severity

The existing project `filter` settings are enforced before either memory or disk
admission, and again during durable replay and operator retry authorization:

```yaml
webhooks:
  - name: production-codex-errors
    url: https://example.com/ntm-events
    events: [agent.error, agent.crashed]
    filter:
      session: prod-*
      agent_type: [codex]
      severity: [error]
```

All specified dimensions must match; allowlist entries within a dimension are
alternatives. Session globs are case-sensitive and use Go `path.Match` semantics
(`*` does not cross `/`). Provider aliases use NTM's canonical agent types.
Explicit `agent_type` metadata takes precedence over `details.agent_type`, then a
recognized provider alias in the legacy `agent` field. Missing or unknown provider
metadata cannot satisfy a provider restriction; identity names and pane titles
are not guessed. Severity is classified from the event type exactly as in the
built-in message formatters (`info`, `success`, `warning`, `error`; `warn` is an
alias for `warning`). Free-form message text and details cannot relabel severity.

Invalid filter values fail registration without replacing an existing policy.
Filters are snapshotted, so caller mutation cannot broaden accepted destinations.
A changed restriction changes the durable configuration fingerprint and retains
old receipts for inspection instead of sending them. Equivalent aliases and list
ordering have the same fingerprint, and empty filters leave pre-existing
unfiltered receipts recoverable. Inspection can report `event_filtered` when a
retained event does not satisfy an otherwise matching configuration. Archival
remains available for valid terminal evidence without authorizing another send.
