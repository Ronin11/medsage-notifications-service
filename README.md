# notifications-service

Delivers device events to the people responsible for a patient, and serves the
marketing site's contact form.

## Routing, and why it is careful

Notifications carry PHI — that a named patient's device missed a dose, and
when. Push is addressed to **the device's owner and caretakers**, resolved by
joining `push_tokens` against `devices` and `device_caretakers`. There is no
"all tokens" query, deliberately: an earlier version fell back to broadcasting
to every registered device when the scoped lookup failed *or returned nothing*,
so an unclaimed device sent one patient's medication events to every user of
the system.

Email is split in two, because the two audiences are not the same:

| Setting | Goes to | Contains |
|---------|---------|----------|
| `CONTACT_TO` | The vendor. **Required** — the service exits without it. | Site contact form, device bug reports |
| `ALERT_TO` | A single address. **Empty by default.** | Undeliverable patient events |

When push reaches nobody and `ALERT_TO` is unset, the event is logged as
undelivered rather than mailed to whoever is configured for ops. `ALERT_TO` is
a single-patient bench convenience; setting it in a deployment with more than
one patient sends everyone's events to one inbox, and the service warns loudly
at startup when it is set.

There is still no way to resolve a *per-patient* email address beyond the
`user_profiles` projection — see **T2.7** in
[`../docs/STATUS.md`](../docs/STATUS.md) for the care-team model that fixes it.

## Configuration

`RESEND_API_KEY` (required), `CONTACT_TO` (required), `ALERT_TO` (optional),
`FROM_ADDRESS`, `DATABASE_URL`, `NATS_URL`, `GOOGLE_APPLICATION_CREDENTIALS`
(FCM), `ALLOWED_ORIGINS`, `PORT`.

## Tests

```sh
go test ./...
```

The routing rules above are pinned by tests — that a patient event is dropped
when nobody is registered, and that a bug report does not follow the patient
path. Both were reachable with no hardware and no network, which is why they
are tests rather than a runbook.

Cannot be built outside the superproject: `go.mod` replaces `medsage/proto`
with `../proto/gen/go`.
