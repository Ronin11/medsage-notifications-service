package main

import (
	"strings"
	"testing"
	"time"

	eventsv1 "medsage/proto/medsage/events/v1"
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
