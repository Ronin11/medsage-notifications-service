package push

import (
	"context"
	"log/slog"
)

// AndroidChannel is the notification channel the mobile app creates for
// caretaker alerts. Part of the contract with the app: a missed dose has to
// be able to make a sound and show as a heads-up even when the app's other
// notifications are quiet.
const AndroidChannel = "alerts"

// Notification is one alert, platform-neutral.
type Notification struct {
	Title string
	Body  string
	// Data drives the app's tap handling: deviceId, kind, route.
	Data map[string]string
}

// Dispatcher sends one notification to a set of tokens across Expo and FCM.
type Dispatcher struct {
	FCM *FCMClient // nil when no Firebase credentials are configured
	// Expo sends to Expo tokens; a variable so tests can stub the network.
	Expo func(ctx context.Context, messages []Message) error
	// Forget is called with tokens FCM reported as unregistered. Optional.
	Forget func(ctx context.Context, tokens []string)
}

func NewDispatcher(fcm *FCMClient) *Dispatcher {
	return &Dispatcher{FCM: fcm, Expo: Send}
}

// Push reports whether at least one platform accepted the notification. A
// platform that errors is logged and does not stop the other.
func (d *Dispatcher) Push(ctx context.Context, tokens []Token, n Notification) bool {
	var expo []Message
	var fcm []string
	for _, t := range tokens {
		switch t.Platform {
		case "fcm":
			fcm = append(fcm, t.Token)
		default:
			// Rows written before the platform column existed are Expo tokens.
			expo = append(expo, Message{
				To: t.Token, Title: n.Title, Body: n.Body, Sound: "default",
				Data: n.Data, Priority: "high", ChannelID: AndroidChannel,
			})
		}
	}

	delivered := false
	if len(expo) > 0 && d.Expo != nil {
		if err := d.Expo(ctx, expo); err != nil {
			slog.Error("Expo push failed", "error", err, "count", len(expo))
		} else {
			slog.Info("Expo push sent", "count", len(expo), "kind", n.Data["kind"])
			delivered = true
		}
	}
	if len(fcm) > 0 {
		if d.FCM == nil {
			slog.Warn("FCM tokens registered but FCM is not configured (GOOGLE_APPLICATION_CREDENTIALS)",
				"count", len(fcm), "kind", n.Data["kind"])
		} else {
			dead, err := d.FCM.Send(ctx, fcm, n.Title, n.Body, n.Data)
			if len(dead) > 0 && d.Forget != nil {
				d.Forget(ctx, dead)
			}
			if err != nil {
				slog.Error("FCM push failed", "error", err, "count", len(fcm))
			} else {
				delivered = true
			}
		}
	}
	return delivered
}
