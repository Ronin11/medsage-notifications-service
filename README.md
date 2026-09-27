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

## What caretakers are told

Each person in the circle gets an alert only for the kinds they asked for
(`notification_prefs`; no row means missed doses and device problems on,
doses taken off). Messages name the person and the medication, never an id,
and read "You …" on the owner's own phone.

| Kind (`data.kind`) | Pref | When | Example |
|---|---|---|---|
| `missed_dose` | missed_doses | Dispenser's MEDICATION_MISSED, or the server check finding no record | "Mom missed the 8:00 AM Metformin 500mg dose" / "No record of Mom's 8:00 AM Metformin 500mg — the dispenser has been offline since 6:40 AM" |
| `device_offline` | device_problems | Offline ≥ `OFFLINE_ALERT_MIN` with doses scheduled; once per offline spell | "Mom's dispenser has been offline since 6:40 AM. Doses taken meanwhile won't show until it reconnects — check its power and Wi-Fi." |
| `dispense_failed` | device_problems | A release that did not open | "Mom's dispenser couldn't open compartment 3 for the 8:00 AM Metformin 500mg dose (it jammed). The dose is still due." |
| `dose_taken` | doses_taken (or missed_doses, when a missed alert for that dose already went out) | MEDICATION_CONFIRMED | "Mom took the 8:00 AM Metformin 500mg dose at 8:12 AM" |

Alarms are not pushed — the dispenser is ringing in the room, and silence
afterwards is what `missed_dose` is for. Bug reports are mailed to
`CONTACT_TO` only.

Push carries `data: {deviceId, kind, route: "today"}` for the app's tap
handling, on Android channel `alerts` at high priority. A person whose push
reached none of their phones gets the same text by email.

### The server-side missed check

A dispenser reports a missed dose only while it is powered and online. Every
`MISS_CHECK_INTERVAL` the service looks at each device with active schedules:
a slot whose miss window (`devices.miss_timeout_min`, NULL → 15, 0 = Never)
has passed within the last 6 h with no confirmed, missed or reconciled event
for it is announced. Slots are wall clock in the device's zone
(`devices.timezone`, else `DEFAULT_TIMEZONE`).

Both paths claim the same `notification_sent` key —
`<date>|<HH:MM>|<schedule id>` — so a dose is announced once, whichever
notices first.

## Configuration

`RESEND_API_KEY` (required), `CONTACT_TO` (required), `ALERT_TO` (optional),
`FROM_ADDRESS`, `DATABASE_URL`, `NATS_URL`, `GOOGLE_APPLICATION_CREDENTIALS`
(FCM), `ALLOWED_ORIGINS`, `PORT`, `DEFAULT_TIMEZONE` (default
`America/Denver`), `MISS_CHECK_INTERVAL` (Go duration, default `15s`),
`OFFLINE_ALERT_MIN` (default 30), `LOG_LEVEL` (`debug` shows each tick's
findings, including the ones the dedupe ledger turns away).

## Tests

```sh
go test ./...
```

The routing rules above are pinned by tests — that a patient event is dropped
when nobody is registered, that a bug report does not follow the patient
path, preference filtering, message text, dedupe, and the slot/window maths
across zones and DST. Both were reachable with no hardware and no network, which is why they
are tests rather than a runbook.

Cannot be built outside the superproject: `go.mod` replaces `medsage/proto`
with `../proto/gen/go`.
