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
A full outbox rejects further acceptance instead of deleting older work. There
is not yet an automatic dead-letter replay or pruning command: inspect retained
receipts, resolve endpoint configuration, and explicitly archive terminal evidence
with the managers stopped before reclaiming capacity. Never clear pending receipts
or lock files merely to silence an admission error.
