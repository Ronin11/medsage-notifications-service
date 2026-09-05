package main

import (
	"context"
	"strings"
	"testing"
	"time"

	eventsv1 "medsage/proto/medsage/events/v1"

	"medsage/notifications-service/email"
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
		ts := time.Now().Unix()
		got := formatTimestamp(ts)
		if got == "unknown" || got == "" {
			t.Errorf("got %q, want a formatted date", got)
		}
		// Format string includes a comma between day and year.
		if !strings.Contains(got, ", ") {
			t.Errorf("got %q, expected to contain ', '", got)
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
	// Unhandled event types fall through to the default branch and return
	// nil without invoking any client.
	n := &EventNotifier{}
	evt := &eventsv1.DeviceEvent{
		EventId:   "e1",
		DeviceId:  "d1",
		EventType: eventsv1.EventType_EVENT_TYPE_UNSPECIFIED,
	}
	if err := n.Handle(t.Context(), evt); err != nil {
		t.Errorf("expected nil error for unknown type, got %v", err)
	}
}

// --- recipient routing -------------------------------------------------
//
// These cover the fix for T1.4 / AUDIT SEC-016+SEC-162. The bug was that a
// device with no registered caretaker fell back to broadcasting to every push
// token in the system, and every alert email went to one hardcoded personal
// address. Both were reachable with no hardware and no network, so they are
// pinned here.

type recordingMailer struct {
	sent []email.SendRequest
}

func (m *recordingMailer) Send(_ context.Context, req email.SendRequest) (*email.SendResponse, error) {
	m.sent = append(m.sent, req)
	return &email.SendResponse{ID: "test"}, nil
}

func medEvent(t eventsv1.EventType) *eventsv1.DeviceEvent {
	return &eventsv1.DeviceEvent{EventId: "e1", DeviceId: "d1", EventType: t}
}

func TestPatientEventIsDroppedWhenNobodyIsRegistered(t *testing.T) {
	// No token store (so push reaches nobody) and no ALERT_TO. The event must
	// be dropped rather than mailed to whoever is configured for ops.
	m := &recordingMailer{}
	n := NewEventNotifier(m, nil, nil, nil, "", "ops@medsage.test")

	if err := n.Handle(t.Context(), medEvent(eventsv1.EventType_EVENT_TYPE_MEDICATION_MISSED)); err != nil {
		t.Fatalf("Handle: %v", err)
	}
	if len(m.sent) != 0 {
		t.Fatalf("patient event was emailed with no configured alert recipient: %+v", m.sent)
	}
}

func TestPatientEventUsesAlertRecipientNotOps(t *testing.T) {
	m := &recordingMailer{}
	n := NewEventNotifier(m, nil, nil, nil, "caretaker@example.test", "ops@medsage.test")

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

func TestBugReportGoesToOpsNotTheAlertRecipient(t *testing.T) {
	// Diagnostics are for the vendor; they must not follow the patient alert
	// path, and must still work when no alert recipient is configured.
	m := &recordingMailer{}
	n := NewEventNotifier(m, nil, nil, nil, "", "ops@medsage.test")

	if err := n.Handle(t.Context(), medEvent(eventsv1.EventType_EVENT_TYPE_BUG_REPORT)); err != nil {
		t.Fatalf("Handle: %v", err)
	}
	if len(m.sent) != 1 {
		t.Fatalf("expected one email, got %d", len(m.sent))
	}
	if got := m.sent[0].To; len(got) != 1 || got[0] != "ops@medsage.test" {
		t.Errorf("bug report went to %v, want [ops@medsage.test]", got)
	}
}

func TestEmailRecipientRouting(t *testing.T) {
	n := NewEventNotifier(nil, nil, nil, nil, "alert@example.test", "ops@example.test")
	for _, tc := range []struct {
		evt  eventsv1.EventType
		want string
	}{
		{eventsv1.EventType_EVENT_TYPE_MEDICATION_DISPENSED, "alert@example.test"},
		{eventsv1.EventType_EVENT_TYPE_MEDICATION_MISSED, "alert@example.test"},
		{eventsv1.EventType_EVENT_TYPE_MEDICATION_CONFIRMED, "alert@example.test"},
		{eventsv1.EventType_EVENT_TYPE_ALARM_TRIGGERED, "alert@example.test"},
		{eventsv1.EventType_EVENT_TYPE_BUG_REPORT, "ops@example.test"},
	} {
		got := n.emailRecipients(t.Context(), tc.evt, "d1")
		if len(got) != 1 || got[0] != tc.want {
			t.Errorf("%s routed to %v, want [%s]", tc.evt, got, tc.want)
		}
	}
}

func TestSendRefusesAnEmptyRecipient(t *testing.T) {
	// Belt and braces: even if a future caller loses the routing check, the
	// send path itself will not address an email to nobody.
	n := NewEventNotifier(&recordingMailer{}, nil, nil, nil, "", "")
	if err := n.send(t.Context(), nil, "subject", "<p>body</p>"); err == nil {
		t.Error("expected an error sending with no recipient")
	}
}
