package main

import (
	"context"
	"strings"
	"testing"
	"time"

	eventsv1 "github.com/Ronin11/medsage-proto/medsage/events/v1"

	"medsage/notifications-service/email"
	"medsage/notifications-service/push"
	"medsage/notifications-service/store"
)

func TestShortID(t *testing.T) {
	tests := []struct {
		in, want string
	}{
		{"", ""},
		{"abc", "abc"},
		{"01234567", "01234567"},
		{"012345678", "01234567"},
		{"abcdef0123-4567", "abcdef01"},
	}
	for _, tc := range tests {
		if got := shortID(tc.in); got != tc.want {
			t.Errorf("shortID(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

func TestFormatTimestamp(t *testing.T) {
	t.Run("zero unix returns 'unknown'", func(t *testing.T) {
		if got := formatTimestamp(0); got != "unknown" {
			t.Errorf("got %q, want 'unknown'", got)
		}
	})
	t.Run("non-zero formats with date and time", func(t *testing.T) {
		// Verify the format pattern, not the local-zone output, since
		// time.Unix returns local time on the test runner.
		got := formatTimestamp(time.Now().Unix())
		if got == "unknown" || !strings.Contains(got, ", ") {
			t.Errorf("got %q, want a formatted date", got)
		}
	})
}

func TestEventNotifierHandleSkipsTestEvents(t *testing.T) {
	// A test-flagged event must short-circuit before any client is touched,
	// so a notifier with all nil deps must still succeed.
	n := &EventNotifier{}
	evt := &eventsv1.DeviceEvent{
		EventId:   "e1",
		DeviceId:  "d1",
		EventType: eventsv1.EventType_EVENT_TYPE_MEDICATION_DISPENSED,
		Metadata:  map[string]string{"test": "true"},
	}
	if err := n.Handle(t.Context(), evt); err != nil {
		t.Errorf("expected nil error for test event, got %v", err)
	}
}

func TestEventNotifierHandleIgnoresUnknownTypes(t *testing.T) {
	n := &EventNotifier{}
	evt := &eventsv1.DeviceEvent{EventId: "e1", DeviceId: "d1", EventType: eventsv1.EventType_EVENT_TYPE_UNSPECIFIED}
	if err := n.Handle(t.Context(), evt); err != nil {
		t.Errorf("expected nil error for unknown type, got %v", err)
	}
}

// --- fakes ---------------------------------------------------------------

type recordingMailer struct {
	sent []email.SendRequest
}

func (m *recordingMailer) Send(_ context.Context, req email.SendRequest) (*email.SendResponse, error) {
	m.sent = append(m.sent, req)
	return &email.SendResponse{ID: "test"}, nil
}

type pushCall struct {
	tokens []push.Token
	n      push.Notification
}

// recordingPusher records every push; tokens listed in fail are treated as
// rejected, like a stale registration.
type recordingPusher struct {
	calls []pushCall
	fail  map[string]bool
}

func (p *recordingPusher) Push(_ context.Context, tokens []push.Token, n push.Notification) bool {
	p.calls = append(p.calls, pushCall{tokens, n})
	for _, t := range tokens {
		if !p.fail[t.Token] {
			return true
		}
	}
	return false
}

// fakeStore is an in-memory Store. Claim implements the ON CONFLICT DO
// NOTHING contract: true only the first time.
type fakeStore struct {
	devices    map[string]store.Device
	schedules  map[string][]store.Schedule
	events     map[string][]store.DoseEvent
	recipients map[string][]store.Recipient
	sent       map[string]bool
}

func newFakeStore() *fakeStore {
	return &fakeStore{
		devices: map[string]store.Device{}, schedules: map[string][]store.Schedule{},
		events: map[string][]store.DoseEvent{}, recipients: map[string][]store.Recipient{},
		sent: map[string]bool{},
	}
}

func (f *fakeStore) Device(_ context.Context, id string) (store.Device, bool, error) {
	d, ok := f.devices[id]
	return d, ok, nil
}

func (f *fakeStore) DevicesWithActiveSchedules(context.Context) ([]store.Device, error) {
	var out []store.Device
	for id, d := range f.devices {
		if len(f.schedules[id]) > 0 {
			out = append(out, d)
		}
	}
	return out, nil
}

func (f *fakeStore) ActiveSchedules(_ context.Context, id string) ([]store.Schedule, error) {
	return f.schedules[id], nil
}

func (f *fakeStore) DoseEvents(_ context.Context, id string, since time.Time) ([]store.DoseEvent, error) {
	var out []store.DoseEvent
	for _, e := range f.events[id] {
		if !e.CreatedAt.Before(since) {
			out = append(out, e)
		}
	}
	return out, nil
}

func (f *fakeStore) Recipients(_ context.Context, id string) ([]store.Recipient, error) {
	return f.recipients[id], nil
}

func (f *fakeStore) Claim(_ context.Context, id, kind, occ string) (bool, error) {
	k := id + "/" + kind + "/" + occ
	if f.sent[k] {
		return false, nil
	}
	f.sent[k] = true
	return true, nil
}

func (f *fakeStore) WasSent(_ context.Context, id, kind, occ string) (bool, error) {
	return f.sent[id+"/"+kind+"/"+occ], nil
}

var denver = mustZone("America/Denver")

func mustZone(name string) *time.Location {
	loc, err := time.LoadLocation(name)
	if err != nil {
		panic(err)
	}
	return loc
}

const (
	devID   = "d0000000-0000-4000-8000-000000000001"
	schedID = "f2000000-0000-4000-8000-000000000001"
)

var metformin = store.Schedule{
	ID: schedID, MedicationName: "Metformin", Dosage: "500mg", Times: []string{"08:00", "20:00"},
	CreatedAt: time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC),
}

// scenario is one device owned by "Mom" with a daughter caretaking, each with
// one phone and an email address, default preferences.
func scenario() (*fakeStore, *recordingPusher, *recordingMailer, *EventNotifier) {
	st := newFakeStore()
	st.devices[devID] = store.Device{
		ID: devID, Name: "Kitchen dispenser", OwnerID: "u-owner", OwnerDisplayName: "Mom",
		Timezone: "America/Denver", Online: true, MissTimeoutMin: 15,
	}
	st.schedules[devID] = []store.Schedule{metformin}
	st.recipients[devID] = []store.Recipient{
		{UserID: "u-owner", IsOwner: true, Email: "mom@example.test", Prefs: store.DefaultPrefs(),
			Tokens: []push.Token{{UserID: "u-owner", Token: "tok-owner", Platform: "expo"}}},
		{UserID: "u-care", Email: "kid@example.test", Prefs: store.DefaultPrefs(),
			Tokens: []push.Token{{UserID: "u-care", Token: "tok-care", Platform: "fcm"}}},
	}
	p := &recordingPusher{fail: map[string]bool{}}
	m := &recordingMailer{}
	n := NewEventNotifier(m, st, p, "", "ops@medsage.test", denver)
	return st, p, m, n
}

func missedEvent(hour, minute, medID uint32, at time.Time) *eventsv1.DeviceEvent {
	return &eventsv1.DeviceEvent{
		EventId: "e1", DeviceId: devID, TimestampUnix: at.Unix(),
		EventType: eventsv1.EventType_EVENT_TYPE_MEDICATION_MISSED,
		Payload: &eventsv1.DeviceEvent_MedicationMissed{MedicationMissed: &eventsv1.MedicationMissed{
			Hour: hour, Minute: minute, MedId: medID, TimeoutSecs: 900,
		}},
	}
}

func confirmedEvent(hour, minute, medID uint32, at time.Time) *eventsv1.DeviceEvent {
	return &eventsv1.DeviceEvent{
		EventId: "e2", DeviceId: devID, TimestampUnix: at.Unix(),
		EventType: eventsv1.EventType_EVENT_TYPE_MEDICATION_CONFIRMED,
		Payload: &eventsv1.DeviceEvent_MedicationConfirmed{MedicationConfirmed: &eventsv1.MedicationConfirmed{
			Hour: hour, Minute: minute, MedId: medID,
		}},
	}
}

func bodiesByToken(p *recordingPusher) map[string]push.Notification {
	out := map[string]push.Notification{}
	for _, c := range p.calls {
		for _, t := range c.tokens {
			out[t.Token] = c.n
		}
	}
	return out
}

// --- event path ----------------------------------------------------------

func TestMissedEventNamesThePersonAndMedication(t *testing.T) {
	_, p, m, n := scenario()
	at := time.Date(2026, 9, 26, 8, 16, 0, 0, denver)
	if err := n.Handle(t.Context(), missedEvent(8, 0, medIDFor(schedID), at)); err != nil {
		t.Fatal(err)
	}
	got := bodiesByToken(p)
	if c := got["tok-care"]; c.Title != "Missed dose" || c.Body != "Mom missed the 8:00 AM Metformin 500mg dose" {
		t.Errorf("caretaker got %q / %q", c.Title, c.Body)
	}
	if o := got["tok-owner"]; o.Body != "You missed the 8:00 AM Metformin 500mg dose" {
		t.Errorf("owner got %q", o.Body)
	}
	want := map[string]string{"deviceId": devID, "kind": "missed_dose", "route": "today"}
	for k, v := range want {
		if got["tok-care"].Data[k] != v {
			t.Errorf("data[%s] = %q, want %q", k, got["tok-care"].Data[k], v)
		}
	}
	if len(m.sent) != 0 {
		t.Errorf("push was delivered, yet email went out: %+v", m.sent)
	}
}

func TestMissedEventIsAnnouncedOnce(t *testing.T) {
	// JetStream redelivery, or the periodic check having got there first:
	// the second attempt sends nothing.
	st, p, _, n := scenario()
	at := time.Date(2026, 9, 26, 8, 16, 0, 0, denver)
	for range 2 {
		if err := n.Handle(t.Context(), missedEvent(8, 0, medIDFor(schedID), at)); err != nil {
			t.Fatal(err)
		}
	}
	if len(p.calls) != 2 { // one call per recipient, once
		t.Errorf("expected one alert (2 recipients), got %d push calls", len(p.calls))
	}
	if !st.sent[devID+"/missed_dose/2026-09-26|08:00|"+schedID] {
		t.Errorf("claim key not recorded; have %v", st.sent)
	}
}

func TestDeviceMissAndCheckShareOneKey(t *testing.T) {
	// The periodic check claims the dose first (dispenser offline), then the
	// dispenser comes back and reports the same miss: nothing more is sent.
	st, p, _, n := scenario()
	d := st.devices[devID]
	d.Online = false
	seen := time.Date(2026, 9, 26, 6, 40, 0, 0, denver)
	d.LastSeen = &seen
	st.devices[devID] = d

	now := time.Date(2026, 9, 26, 8, 15, 0, 0, denver)
	NewChecker(n, time.Minute, 24*time.Hour).Tick(t.Context(), now)
	if len(p.calls) != 2 {
		t.Fatalf("check: expected 2 push calls, got %d", len(p.calls))
	}
	if b := bodiesByToken(p)["tok-care"].Body; b != "No record of Mom's 8:00 AM Metformin 500mg — the dispenser has been offline since 6:40 AM" {
		t.Errorf("offline miss body = %q", b)
	}
	if err := n.Handle(t.Context(), missedEvent(8, 0, medIDFor(schedID), now.Add(time.Hour))); err != nil {
		t.Fatal(err)
	}
	if len(p.calls) != 2 {
		t.Errorf("dispenser's own report was announced again: %d push calls", len(p.calls))
	}
}

func TestPreferencesFilterRecipients(t *testing.T) {
	cases := []struct {
		name      string
		evt       func() *eventsv1.DeviceEvent
		ownerPref store.Prefs
		carePref  store.Prefs
		want      []string // tokens pushed
	}{
		{"missed: defaults reach both", func() *eventsv1.DeviceEvent {
			return missedEvent(8, 0, 0, time.Date(2026, 9, 26, 8, 16, 0, 0, denver))
		}, store.DefaultPrefs(), store.DefaultPrefs(), []string{"tok-owner", "tok-care"}},
		{"missed: caretaker opted out", func() *eventsv1.DeviceEvent {
			return missedEvent(8, 0, 0, time.Date(2026, 9, 26, 8, 16, 0, 0, denver))
		}, store.DefaultPrefs(), store.Prefs{DeviceProblems: true}, []string{"tok-owner"}},
		{"taken: off by default", func() *eventsv1.DeviceEvent {
			return confirmedEvent(8, 0, 0, time.Date(2026, 9, 26, 8, 5, 0, 0, denver))
		}, store.DefaultPrefs(), store.DefaultPrefs(), nil},
		{"taken: caretaker opted in", func() *eventsv1.DeviceEvent {
			return confirmedEvent(8, 0, 0, time.Date(2026, 9, 26, 8, 5, 0, 0, denver))
		}, store.DefaultPrefs(), store.Prefs{MissedDoses: true, DosesTaken: true}, []string{"tok-care"}},
		{"dispense failed: device problems", func() *eventsv1.DeviceEvent {
			return &eventsv1.DeviceEvent{EventId: "e3", DeviceId: devID, EventType: eventsv1.EventType_EVENT_TYPE_MEDICATION_DISPENSED,
				Payload: &eventsv1.DeviceEvent_MedicationDispensed{MedicationDispensed: &eventsv1.MedicationDispensed{Hour: 8, Result: "jammed", Compartment: 1}}}
		}, store.Prefs{MissedDoses: true}, store.DefaultPrefs(), []string{"tok-care"}},
		{"alarm: nobody", func() *eventsv1.DeviceEvent {
			return &eventsv1.DeviceEvent{EventId: "e4", DeviceId: devID, EventType: eventsv1.EventType_EVENT_TYPE_ALARM_TRIGGERED}
		}, store.DefaultPrefs(), store.DefaultPrefs(), nil},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			st, p, m, n := scenario()
			st.recipients[devID][0].Prefs = tc.ownerPref
			st.recipients[devID][1].Prefs = tc.carePref
			if err := n.Handle(t.Context(), tc.evt()); err != nil {
				t.Fatal(err)
			}
			got := bodiesByToken(p)
			if len(got) != len(tc.want) {
				t.Errorf("pushed to %v, want %v", got, tc.want)
			}
			for _, tok := range tc.want {
				if _, ok := got[tok]; !ok {
					t.Errorf("%s not pushed", tok)
				}
			}
			if len(m.sent) != 0 {
				t.Errorf("unexpected email: %+v", m.sent)
			}
		})
	}
}

func TestTakenAfterAMissedAlertReachesThoseWhoWereTold(t *testing.T) {
	// Doses taken are off by default, but a caretaker told the 8:00 dose was
	// missed should hear it was taken after all.
	_, p, _, n := scenario()
	if err := n.Handle(t.Context(), missedEvent(8, 0, medIDFor(schedID), time.Date(2026, 9, 26, 8, 16, 0, 0, denver))); err != nil {
		t.Fatal(err)
	}
	p.calls = nil
	if err := n.Handle(t.Context(), confirmedEvent(8, 0, medIDFor(schedID), time.Date(2026, 9, 26, 9, 30, 0, 0, denver))); err != nil {
		t.Fatal(err)
	}
	c := bodiesByToken(p)["tok-care"]
	if c.Title != "Dose taken late" || c.Body != "Mom took the 8:00 AM Metformin 500mg dose at 9:30 AM" || c.Data["kind"] != "dose_taken" {
		t.Errorf("got %+v", c)
	}
}

func TestEmailOnlyForThoseWhosePushFailed(t *testing.T) {
	st, p, m, n := scenario()
	p.fail["tok-care"] = true
	st.recipients[devID][0].Prefs = store.Prefs{} // owner wants nothing
	if err := n.Handle(t.Context(), missedEvent(8, 0, 0, time.Date(2026, 9, 26, 8, 16, 0, 0, denver))); err != nil {
		t.Fatal(err)
	}
	if len(m.sent) != 1 || m.sent[0].To[0] != "kid@example.test" {
		t.Fatalf("want one email to the caretaker, got %+v", m.sent)
	}
	if m.sent[0].Subject != "[Medsage] Missed dose" || !strings.Contains(m.sent[0].HTML, "Mom missed the 8:00 AM Metformin 500mg dose") {
		t.Errorf("email = %q / %s", m.sent[0].Subject, m.sent[0].HTML)
	}
}

// --- recipient routing -------------------------------------------------
//
// These cover the fix for T1.4 / AUDIT SEC-016+SEC-162. The bug was that a
// device with no registered caretaker fell back to broadcasting to every push
// token in the system, and every alert email went to one hardcoded personal
// address. Both were reachable with no hardware and no network, so they are
// pinned here.

func medEvent(t eventsv1.EventType) *eventsv1.DeviceEvent {
	return &eventsv1.DeviceEvent{EventId: "e1", DeviceId: "d1", EventType: t}
}

func TestPatientEventIsDroppedWhenNobodyIsRegistered(t *testing.T) {
	// No database (so nobody to address) and no ALERT_TO. The event must be
	// dropped rather than mailed to whoever is configured for ops.
	m := &recordingMailer{}
	n := NewEventNotifier(m, nil, nil, "", "ops@medsage.test", denver)

	if err := n.Handle(t.Context(), medEvent(eventsv1.EventType_EVENT_TYPE_MEDICATION_MISSED)); err != nil {
		t.Fatalf("Handle: %v", err)
	}
	if len(m.sent) != 0 {
		t.Fatalf("patient event was emailed with no configured alert recipient: %+v", m.sent)
	}
}

func TestPatientEventUsesAlertRecipientNotOps(t *testing.T) {
	m := &recordingMailer{}
	n := NewEventNotifier(m, nil, nil, "caretaker@example.test", "ops@medsage.test", denver)

	if err := n.Handle(t.Context(), medEvent(eventsv1.EventType_EVENT_TYPE_MEDICATION_MISSED)); err != nil {
		t.Fatalf("Handle: %v", err)
	}
	if len(m.sent) != 1 {
		t.Fatalf("expected one email, got %d", len(m.sent))
	}
	if got := m.sent[0].To; len(got) != 1 || got[0] != "caretaker@example.test" {
		t.Errorf("patient event went to %v, want [caretaker@example.test]", got)
	}
}

func TestAlertToIsNotUsedWhenTheCircleOptedOut(t *testing.T) {
	// People are registered but none want this kind: that is their choice,
	// not a reason to reach for the bench-rig fallback.
	st, p, m, _ := scenario()
	n := NewEventNotifier(m, st, p, "alert@example.test", "ops@medsage.test", denver)
	if err := n.Handle(t.Context(), confirmedEvent(8, 0, 0, time.Date(2026, 9, 26, 8, 5, 0, 0, denver))); err != nil {
		t.Fatal(err)
	}
	if len(m.sent) != 0 || len(p.calls) != 0 {
		t.Errorf("opted-out alert was delivered: push %d, email %+v", len(p.calls), m.sent)
	}
}

func TestBugReportGoesToOpsOnlyAndIsNeverPushed(t *testing.T) {
	// Diagnostics are for the vendor; they must not follow the patient alert
	// path, and must still work when no alert recipient is configured.
	st, p, m, _ := scenario()
	n := NewEventNotifier(m, st, p, "", "ops@medsage.test", denver)
	evt := medEvent(eventsv1.EventType_EVENT_TYPE_BUG_REPORT)
	evt.DeviceId = devID

	if err := n.Handle(t.Context(), evt); err != nil {
		t.Fatalf("Handle: %v", err)
	}
	if len(p.calls) != 0 {
		t.Errorf("bug report was pushed to the care circle: %+v", p.calls)
	}
	if len(m.sent) != 1 || len(m.sent[0].To) != 1 || m.sent[0].To[0] != "ops@medsage.test" {
		t.Errorf("bug report went to %+v, want [ops@medsage.test]", m.sent)
	}
}

func TestSendRefusesAnEmptyRecipient(t *testing.T) {
	// Belt and braces: even if a future caller loses the routing check, the
	// send path itself will not address an email to nobody.
	n := NewEventNotifier(&recordingMailer{}, nil, nil, "", "", denver)
	if err := n.send(t.Context(), nil, "subject", "<p>body</p>"); err == nil {
		t.Error("expected an error sending with no recipient")
	}
}

// A dispenser publishes MEDICATION_DISPENSED for every release. The ones that
// opened are covered by the MEDICATION_CONFIRMED that follows; only a failed
// release may reach a caretaker, or every dose becomes two notifications.
func TestOnlyAFailedReleaseNotifies(t *testing.T) {
	dispensed := func(result string) *eventsv1.DeviceEvent {
		return &eventsv1.DeviceEvent{
			EventId: "e1", DeviceId: "d1",
			EventType: eventsv1.EventType_EVENT_TYPE_MEDICATION_DISPENSED,
			Payload: &eventsv1.DeviceEvent_MedicationDispensed{MedicationDispensed: &eventsv1.MedicationDispensed{
				Hour: 8, Minute: 30, Compartment: 5, Result: result,
			}},
		}
	}
	for _, result := range []string{"opened", ""} {
		m := &recordingMailer{}
		n := NewEventNotifier(m, nil, nil, "caretaker@example.test", "ops@medsage.test", denver)
		if err := n.Handle(t.Context(), dispensed(result)); err != nil {
			t.Fatalf("Handle(%q): %v", result, err)
		}
		if len(m.sent) != 0 {
			t.Errorf("result %q notified: %+v", result, m.sent)
		}
	}
	for result, words := range map[string]string{"jammed": "it jammed", "timeout": "it timed out"} {
		m := &recordingMailer{}
		n := NewEventNotifier(m, nil, nil, "caretaker@example.test", "ops@medsage.test", denver)
		if err := n.Handle(t.Context(), dispensed(result)); err != nil {
			t.Fatalf("Handle(%q): %v", result, err)
		}
		if len(m.sent) != 1 {
			t.Fatalf("result %q: expected one email, got %d", result, len(m.sent))
		}
		if !strings.Contains(m.sent[0].HTML, "compartment 5") || !strings.Contains(m.sent[0].HTML, words) {
			t.Errorf("result %q: email does not say what failed: %s", result, m.sent[0].HTML)
		}
	}
}
