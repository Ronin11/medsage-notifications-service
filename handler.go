package main

import (
	"context"
	"fmt"
	"html"
	"log/slog"
	"time"

	eventsv1 "github.com/Ronin11/medsage-proto/medsage/events/v1"

	"medsage/notifications-service/email"
	"medsage/notifications-service/push"
	"medsage/notifications-service/store"
)

// EventNotifier turns device events, and the missed-dose check's findings,
// into alerts for the people looking after a patient.
//
// Who hears what:
//
//   - Patient alerts go to the device's owner and caretakers only, each
//     filtered by their own notification_prefs. Push first; a person whose
//     push reached no phone gets an email instead. alertTo is a single-tenant
//     escape hatch used only when a device has nobody at all, and is empty
//     unless ALERT_TO is set. It and opsTo used to be the same hardcoded
//     personal inbox, so every patient's events went to one person.
//   - opsTo receives device diagnostics (bug reports), which the patient
//     submits expecting the vendor to read them. They are never pushed to the
//     care circle, and a patient alert never falls back to opsTo.
//
// Each alert about a thing that happened is claimed in notification_sent
// before it is sent, so a dose noticed both by the dispenser and by the
// periodic check — or an event JetStream redelivers — is announced once.
type EventNotifier struct {
	mail        mailer
	store       Store  // nil without a database: no names, no dedupe, nobody to address
	push        pusher // nil disables push
	alertTo     string // last-resort single-tenant fallback for patient events
	opsTo       string // vendor inbox for device diagnostics
	defaultZone *time.Location
	now         func() time.Time
}

// mailer is the slice of email.Client this notifier uses, as an interface so
// that recipient routing — the thing that leaked PHI — can be tested without a
// network client. *email.Client satisfies it.
type mailer interface {
	Send(ctx context.Context, req email.SendRequest) (*email.SendResponse, error)
}

// pusher is push.Dispatcher, as an interface for the same reason.
type pusher interface {
	Push(ctx context.Context, tokens []push.Token, n push.Notification) bool
}

// Store is what the notifier reads and writes. *store.Postgres satisfies it.
type Store interface {
	Device(ctx context.Context, deviceID string) (store.Device, bool, error)
	DevicesWithActiveSchedules(ctx context.Context) ([]store.Device, error)
	ActiveSchedules(ctx context.Context, deviceID string) ([]store.Schedule, error)
	DoseEvents(ctx context.Context, deviceID string, since time.Time) ([]store.DoseEvent, error)
	Recipients(ctx context.Context, deviceID string) ([]store.Recipient, error)
	Claim(ctx context.Context, deviceID, kind, occurrence string) (bool, error)
	WasSent(ctx context.Context, deviceID, kind, occurrence string) (bool, error)
}

func NewEventNotifier(mail mailer, st Store, p pusher, alertTo, opsTo string, defaultZone *time.Location) *EventNotifier {
	if defaultZone == nil {
		defaultZone = time.UTC
	}
	return &EventNotifier{
		mail: mail, store: st, push: p,
		alertTo: alertTo, opsTo: opsTo,
		defaultZone: defaultZone, now: time.Now,
	}
}

// Handle processes a DeviceEvent from NATS. A returned error NAKs the message
// so JetStream redelivers it; that is reserved for failing to record the
// dedupe claim, where a retry is safe and dropping would lose the alert.
func (n *EventNotifier) Handle(ctx context.Context, evt *eventsv1.DeviceEvent) error {
	if evt.Metadata["test"] == "true" {
		slog.Info("Skipping notification for test event",
			"event_id", evt.EventId,
			"device_id", evt.DeviceId,
			"type", evt.EventType.String(),
		)
		return nil
	}

	switch evt.EventType {
	case eventsv1.EventType_EVENT_TYPE_MEDICATION_MISSED:
		p := evt.GetMedicationMissed()
		return n.handleDose(ctx, evt, KindMissedDose, int(p.GetHour()), int(p.GetMinute()), p.GetMedId())
	case eventsv1.EventType_EVENT_TYPE_MEDICATION_CONFIRMED:
		p := evt.GetMedicationConfirmed()
		return n.handleDose(ctx, evt, KindDoseTaken, int(p.GetHour()), int(p.GetMinute()), p.GetMedId())
	case eventsv1.EventType_EVENT_TYPE_MEDICATION_DISPENSED:
		// Every dose a dispenser releases produces one of these, and the
		// MEDICATION_CONFIRMED that follows already says the dose was taken.
		// Only a release that failed is news: the pills are still in the
		// compartment and the dose is still due.
		p := evt.GetMedicationDispensed()
		if !releaseFailed(p) {
			return nil
		}
		return n.handleDose(ctx, evt, KindDispenseFailed, int(p.GetHour()), int(p.GetMinute()), p.GetMedId())
	case eventsv1.EventType_EVENT_TYPE_ALARM_TRIGGERED:
		// The dispenser is ringing in the room; a push for every alarm to
		// everyone in the care circle is noise that trains them to ignore the
		// one that matters. Silence after the alarm is what the missed-dose
		// alert is for.
		return nil
	case eventsv1.EventType_EVENT_TYPE_BUG_REPORT:
		n.sendBugReport(ctx, evt)
		return nil
	default:
		slog.Debug("Ignoring unhandled event type", "type", evt.EventType.String())
		return nil
	}
}

// deviceContext loads what rendering and matching need. Without a database
// there is only the id, which still lets an alert reach ALERT_TO.
func (n *EventNotifier) deviceContext(ctx context.Context, deviceID string) (store.Device, *time.Location, []store.Schedule) {
	dev := store.Device{ID: deviceID, MissTimeoutMin: store.DefaultMissTimeoutMin}
	if n.store == nil {
		return dev, n.defaultZone, nil
	}
	if d, ok, err := n.store.Device(ctx, deviceID); err != nil {
		slog.Error("Loading device failed", "device_id", deviceID, "error", err)
	} else if ok {
		dev = d
	}
	loc := n.zone(dev)
	schedules, err := n.store.ActiveSchedules(ctx, deviceID)
	if err != nil {
		// Without schedules the alert says "the 8:00 AM dose" instead of
		// naming the medication; still worth sending.
		slog.Error("Loading schedules failed", "device_id", deviceID, "error", err)
	}
	return dev, loc, schedules
}

// zone is the device's IANA zone, or DEFAULT_TIMEZONE when it has not
// reported one (or reported one this build does not know).
func (n *EventNotifier) zone(dev store.Device) *time.Location {
	if dev.Timezone != "" {
		if loc, err := time.LoadLocation(dev.Timezone); err == nil {
			return loc
		}
		slog.Warn("Unknown device timezone, using default", "device_id", dev.ID, "timezone", dev.Timezone)
	}
	return n.defaultZone
}

func (n *EventNotifier) handleDose(ctx context.Context, evt *eventsv1.DeviceEvent, kind Kind, hour, minute int, medID uint32) error {
	dev, loc, schedules := n.deviceContext(ctx, evt.DeviceId)
	at := n.now()
	if evt.TimestampUnix > 0 {
		at = time.Unix(evt.TimestampUnix, 0)
	}
	ref := doseForEvent(hour, minute, medID, at, schedules, loc)
	subj := subjectFor(dev, loc)

	switch kind {
	case KindMissedDose:
		return n.announceDose(ctx, dev, kind, ref, wantsMissed, func(owner bool, meds []store.Schedule) (string, string) {
			return missedText(subj, owner, hour, minute, meds)
		})
	case KindDispenseFailed:
		p := evt.GetMedicationDispensed()
		return n.announceDose(ctx, dev, kind, ref, wantsProblems, func(owner bool, meds []store.Schedule) (string, string) {
			return dispenseFailedText(subj, owner, hour, minute, meds, p.GetCompartment(), p.GetResult())
		})
	case KindDoseTaken:
		// Doses taken are off by default. But someone told a dose was missed
		// should hear that it was taken after all, whether or not they want
		// every dose.
		afterMissed := n.missedAlreadySent(ctx, dev.ID, ref)
		wants := wantsTaken
		if afterMissed {
			wants = func(p store.Prefs) bool { return p.DosesTaken || p.MissedDoses }
		}
		return n.announceDose(ctx, dev, kind, ref, wants, func(owner bool, meds []store.Schedule) (string, string) {
			return takenText(subj, owner, hour, minute, meds, at, afterMissed)
		})
	}
	return nil
}

func wantsMissed(p store.Prefs) bool   { return p.MissedDoses }
func wantsProblems(p store.Prefs) bool { return p.DeviceProblems }
func wantsTaken(p store.Prefs) bool    { return p.DosesTaken }

func (n *EventNotifier) missedAlreadySent(ctx context.Context, deviceID string, ref doseRef) bool {
	if n.store == nil {
		return false
	}
	ids := []string{""}
	if len(ref.meds) > 0 {
		ids = ids[:0]
		for _, m := range ref.meds {
			ids = append(ids, m.ID)
		}
	}
	for _, id := range ids {
		sent, err := n.store.WasSent(ctx, deviceID, string(KindMissedDose), ref.occurrence(id))
		if err != nil {
			slog.Error("Checking for an earlier missed-dose alert failed", "device_id", deviceID, "error", err)
			continue
		}
		if sent {
			return true
		}
	}
	return false
}

// announceMissed is the periodic check's path: same kind and occurrence key
// as the dispenser's own MEDICATION_MISSED, so whichever notices first wins.
func (n *EventNotifier) announceMissed(ctx context.Context, dev store.Device, loc *time.Location, ref doseRef, offline bool, now time.Time) {
	subj := subjectFor(dev, loc)
	err := n.announceDose(ctx, dev, KindMissedDose, ref, wantsMissed, func(owner bool, meds []store.Schedule) (string, string) {
		if offline {
			return unrecordedText(subj, owner, ref.hour, ref.minute, meds, dev.LastSeen, now)
		}
		return missedText(subj, owner, ref.hour, ref.minute, meds)
	})
	if err != nil {
		slog.Error("Missed-dose alert failed; will retry next check", "device_id", dev.ID, "error", err)
	}
}

// announceOffline is one alert per offline spell: the key is the last_seen
// the spell started from, which only changes once the device has been back.
func (n *EventNotifier) announceOffline(ctx context.Context, dev store.Device, loc *time.Location, now time.Time) {
	if dev.LastSeen == nil {
		return
	}
	occurrence := dev.LastSeen.UTC().Format(time.RFC3339)
	ok, err := n.claim(ctx, dev.ID, KindDeviceOffline, occurrence)
	if err != nil {
		slog.Error("Offline alert claim failed; will retry next check", "device_id", dev.ID, "error", err)
		return
	}
	if !ok {
		return
	}
	subj := subjectFor(dev, loc)
	slog.Info("Device offline alert", "device_id", dev.ID, "occurrence", occurrence)
	n.deliver(ctx, dev.ID, KindDeviceOffline, wantsProblems, func(owner bool) (string, string) {
		return offlineText(subj, owner, *dev.LastSeen, now)
	})
}

// announceDose claims a dose alert for each schedule it covers and sends one
// alert naming the ones this caller claimed. Nothing is sent when every one
// was already announced.
func (n *EventNotifier) announceDose(ctx context.Context, dev store.Device, kind Kind, ref doseRef, wants func(store.Prefs) bool, render func(owner bool, meds []store.Schedule) (string, string)) error {
	var claimed []store.Schedule
	claimedAny := false
	if len(ref.meds) == 0 {
		ok, err := n.claim(ctx, dev.ID, kind, ref.occurrence(""))
		if err != nil {
			return err
		}
		claimedAny = ok
	}
	for _, m := range ref.meds {
		ok, err := n.claim(ctx, dev.ID, kind, ref.occurrence(m.ID))
		if err != nil {
			return err
		}
		if ok {
			claimed = append(claimed, m)
			claimedAny = true
		}
	}
	if !claimedAny {
		slog.Debug("Alert already sent, not repeating", "device_id", dev.ID, "kind", kind, "slot", ref.date+" "+ref.hhmm())
		return nil
	}
	slog.Info("Dose alert", "device_id", dev.ID, "kind", kind, "slot", ref.date+" "+ref.hhmm(), "schedules", len(claimed))
	n.deliver(ctx, dev.ID, kind, wants, func(owner bool) (string, string) { return render(owner, claimed) })
	return nil
}

// claim is Store.Claim, and true without a database — there is nothing to
// dedupe against, and ALERT_TO on a bench rig is still owed the alert.
func (n *EventNotifier) claim(ctx context.Context, deviceID string, kind Kind, occurrence string) (bool, error) {
	if n.store == nil {
		return true, nil
	}
	return n.store.Claim(ctx, deviceID, string(kind), occurrence)
}

// deliver sends one alert to everyone in the device's circle who wants it:
// push to each person's phones, and email to each person whose push reached
// none of them.
func (n *EventNotifier) deliver(ctx context.Context, deviceID string, kind Kind, wants func(store.Prefs) bool, render func(owner bool) (string, string)) {
	var recipients []store.Recipient
	if n.store != nil {
		r, err := n.store.Recipients(ctx, deviceID)
		if err != nil {
			// Do not widen the audience on failure — that was the original bug.
			slog.Error("Resolving recipients failed; alert not delivered", "device_id", deviceID, "kind", kind, "error", err)
			return
		}
		recipients = r
	}

	if len(recipients) == 0 {
		// Nobody is registered for this device. ALERT_TO is the bench-rig
		// escape hatch; without it, dropping is correct — the alternative is
		// mailing one patient's medication history to whoever is in an env var.
		if n.alertTo == "" {
			slog.Warn("Alert not delivered: nobody is registered for this device and no ALERT_TO is set",
				"device_id", deviceID, "kind", kind)
			return
		}
		title, body := render(false)
		n.sendAlertEmail(ctx, []string{n.alertTo}, title, body, deviceID, kind)
		return
	}

	data := map[string]string{"deviceId": deviceID, "kind": string(kind), "route": "today"}
	pushed, emailed, optedOut, unreachable := 0, 0, 0, 0
	for _, r := range recipients {
		if !wants(r.Prefs) {
			optedOut++
			continue
		}
		title, body := render(r.IsOwner)
		if len(r.Tokens) > 0 && n.push != nil &&
			n.push.Push(ctx, r.Tokens, push.Notification{Title: title, Body: body, Data: data}) {
			pushed++
			continue
		}
		if r.Email == "" {
			unreachable++
			continue
		}
		if n.sendAlertEmail(ctx, []string{r.Email}, title, body, deviceID, kind) {
			emailed++
		} else {
			unreachable++
		}
	}
	slog.Info("Alert delivered", "device_id", deviceID, "kind", kind,
		"pushed", pushed, "emailed", emailed, "opted_out", optedOut, "unreachable", unreachable)
}

func (n *EventNotifier) sendAlertEmail(ctx context.Context, to []string, title, body, deviceID string, kind Kind) bool {
	if n.mail == nil {
		return false
	}
	htmlBody := fmt.Sprintf(`<h2>%s</h2>
<p>%s</p>
<p style="color:#666">Open the Medsage app for today's doses. You can choose which alerts you get in the app's settings.</p>`,
		html.EscapeString(title), html.EscapeString(body))
	if err := n.send(ctx, to, "[Medsage] "+title, htmlBody); err != nil {
		slog.Error("Alert email failed", "device_id", deviceID, "kind", kind, "error", err)
		return false
	}
	return true
}

// releaseFailed is true for a release that did not open. An empty result is
// a pre-v0.2.0 event with no fields at all, which never described a failure.
func releaseFailed(p *eventsv1.MedicationDispensed) bool {
	r := p.GetResult()
	return r != "" && r != "opened"
}

// sendBugReport mails device diagnostics to the vendor, and only to them.
// It used to be pushed to the patient's care circle first and mailed only when
// that push failed, so the vendor never saw a report from a household with
// the app installed.
func (n *EventNotifier) sendBugReport(ctx context.Context, evt *eventsv1.DeviceEvent) {
	if n.opsTo == "" {
		slog.Warn("Bug report not delivered: CONTACT_TO is not set", "device_id", evt.DeviceId)
		return
	}
	p := evt.GetBugReport()
	detail := ""
	if p != nil {
		detail = fmt.Sprintf(`<p><strong>Firmware:</strong> %s</p>
<p><strong>IDF:</strong> %s</p>
<p><strong>Free Heap:</strong> %d bytes</p>
<p><strong>Uptime:</strong> %ds</p>
<p><strong>Message:</strong> %s</p>`,
			html.EscapeString(p.FwVersion),
			html.EscapeString(p.IdfVersion),
			p.FreeHeap,
			p.UptimeS,
			html.EscapeString(p.Message),
		)
	}

	subject := fmt.Sprintf("[Medsage] Bug Report — Device %s", shortID(evt.DeviceId))
	body := fmt.Sprintf(`<h2>Bug Report</h2>
<p><strong>Device:</strong> %s</p>
<p><strong>Time:</strong> %s</p>
%s`,
		html.EscapeString(evt.DeviceId),
		formatTimestamp(evt.TimestampUnix),
		detail,
	)

	if err := n.send(ctx, []string{n.opsTo}, subject, body); err != nil {
		slog.Error("Bug report email failed", "device_id", evt.DeviceId, "error", err)
	}
}

func (n *EventNotifier) send(ctx context.Context, to []string, subject, htmlBody string) error {
	if len(to) == 0 {
		return fmt.Errorf("refusing to send %q with no recipient", subject)
	}
	_, err := n.mail.Send(ctx, email.SendRequest{
		To:      to,
		Subject: subject,
		HTML:    htmlBody,
	})
	return err
}

func shortID(id string) string {
	if len(id) >= 8 {
		return id[:8]
	}
	return id
}

func formatTimestamp(unix int64) string {
	if unix == 0 {
		return "unknown"
	}
	return time.Unix(unix, 0).Format("Jan 2, 2006 3:04 PM MST")
}
