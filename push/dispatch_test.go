package push

import (
	"context"
	"errors"
	"testing"
)

func TestDispatcherSplitsByPlatformAndSetsTheChannel(t *testing.T) {
	var got []Message
	d := &Dispatcher{Expo: func(_ context.Context, m []Message) error { got = m; return nil }}
	n := Notification{Title: "Missed dose", Body: "Mom missed…", Data: map[string]string{"kind": "missed_dose"}}

	ok := d.Push(t.Context(), []Token{
		{UserID: "u1", Token: "ExponentPushToken[a]", Platform: "expo"},
		{UserID: "u1", Token: "legacy", Platform: ""},
		{UserID: "u2", Token: "fcm-token", Platform: "fcm"},
	}, n)
	if !ok {
		t.Fatal("expected delivery via Expo")
	}
	if len(got) != 2 {
		t.Fatalf("expo got %d messages, want 2 (the FCM token must not go to Expo)", len(got))
	}
	for _, m := range got {
		if m.ChannelID != AndroidChannel || m.Priority != "high" || m.Data["kind"] != "missed_dose" {
			t.Errorf("message %+v", m)
		}
	}
}

func TestDispatcherReportsNoDelivery(t *testing.T) {
	// FCM tokens with no FCM client, and an Expo failure: nothing delivered,
	// so the caller falls back to email.
	d := &Dispatcher{Expo: func(context.Context, []Message) error { return errors.New("down") }}
	if d.Push(t.Context(), []Token{{Token: "x", Platform: "fcm"}, {Token: "y", Platform: "expo"}}, Notification{}) {
		t.Error("reported delivered")
	}
}
