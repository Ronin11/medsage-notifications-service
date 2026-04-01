package main

import (
	"context"
	"fmt"
	"html"
	"log/slog"
	"time"

	eventsv1 "medsage/proto/medsage/events/v1"

	"medsage/notifications-service/email"
	"medsage/notifications-service/push"
)

// EventNotifier handles NATS events and sends email + push notifications.
type EventNotifier struct {
	emailClient *email.Client
	tokenStore  *push.TokenStore
	fcmClient   *push.FCMClient
	alertTo     string // recipient for device alerts / medication events
}

func NewEventNotifier(emailClient *email.Client, tokenStore *push.TokenStore, fcmClient *push.FCMClient, alertTo string) *EventNotifier {
	return &EventNotifier{emailClient: emailClient, tokenStore: tokenStore, fcmClient: fcmClient, alertTo: alertTo}
}

// Handle processes a protobuf DeviceEvent: tries push first, falls back to email.
func (n *EventNotifier) Handle(ctx context.Context, evt *eventsv1.DeviceEvent) error {
	if evt.Metadata["test"] == "true" {
		slog.Info("Skipping notification for test event",
			"event_id", evt.EventId,
			"device_id", evt.DeviceId,
			"type", evt.EventType.String(),
		)
		return nil
	}

	var title, body string
	var emailSender func(context.Context, *eventsv1.DeviceEvent) error

	switch evt.EventType {
	case eventsv1.EventType_EVENT_TYPE_MEDICATION_DISPENSED:
		title = "Medication Dispensed"
		body = fmt.Sprintf("Device %s dispensed medication at %s", shortID(evt.DeviceId), formatTimestamp(evt.TimestampUnix))
		emailSender = n.sendMedicationDispensed
	case eventsv1.EventType_EVENT_TYPE_MEDICATION_MISSED:
		title = "Medication Missed"
		body = fmt.Sprintf("Device %s: patient missed their scheduled medication", shortID(evt.DeviceId))
		emailSender = n.sendMedicationMissed
	case eventsv1.EventType_EVENT_TYPE_MEDICATION_CONFIRMED:
		title = "Medication Confirmed"
		body = fmt.Sprintf("Device %s: patient confirmed taking medication", shortID(evt.DeviceId))
		emailSender = n.sendMedicationConfirmed
	case eventsv1.EventType_EVENT_TYPE_ALARM_TRIGGERED:
		title = "Alarm Triggered"
		p := evt.GetAlarmTriggered()
		if p != nil {
			body = fmt.Sprintf("Device %s: alarm triggered at %02d:%02d", shortID(evt.DeviceId), p.Hour, p.Minute)
		} else {
			body = fmt.Sprintf("Device %s: alarm triggered", shortID(evt.DeviceId))
		}
		emailSender = n.sendAlarmTriggered
	case eventsv1.EventType_EVENT_TYPE_BUG_REPORT:
		title = "Bug Report"
		body = fmt.Sprintf("Device %s submitted a bug report", shortID(evt.DeviceId))
		emailSender = n.sendBugReport
	default:
		slog.Debug("Ignoring unhandled event type", "type", evt.EventType.String())
		return nil
	}

	// Try push first; fall back to email if push didn't reach anyone
	if pushed := n.sendPush(ctx, evt.DeviceId, title, body); !pushed {
		slog.Info("Push notification not delivered, falling back to email", "title", title)
		if err := emailSender(ctx, evt); err != nil {
			slog.Error("Email fallback failed", "error", err)
		}
	}

	return nil
}

// sendPush sends push notifications via Expo and FCM.
// Returns true if at least one push notification was delivered successfully.
func (n *EventNotifier) sendPush(ctx context.Context, deviceID, title, body string) bool {
	if n.tokenStore == nil {
		return false
	}

	delivered := false
	data := map[string]string{"deviceId": deviceID}

	// Send via Expo
	expoTokens, err := n.tokenStore.GetTokensForDevice(ctx, deviceID)
	if err != nil {
		slog.Error("Failed to get Expo push tokens", "error", err)
	} else if len(expoTokens) > 0 {
		messages := make([]push.Message, len(expoTokens))
		for i, token := range expoTokens {
			messages[i] = push.Message{
				To:    token,
				Title: title,
				Body:  body,
				Sound: "default",
				Data:  data,
			}
		}
		if err := push.Send(ctx, messages); err != nil {
			slog.Error("Failed to send Expo push notifications", "error", err)
		} else {
			slog.Info("Expo push notifications sent", "count", len(expoTokens), "title", title)
			delivered = true
		}
	}

	// Send via FCM
	if n.fcmClient != nil {
		fcmTokens, err := n.tokenStore.GetFCMTokensForDevice(ctx, deviceID)
		if err != nil {
			slog.Error("Failed to get FCM tokens", "error", err)
		} else if len(fcmTokens) > 0 {
			if err := n.fcmClient.Send(ctx, fcmTokens, title, body, data); err != nil {
				slog.Error("Failed to send FCM notifications", "error", err)
			} else {
				delivered = true
			}
		}
	}

	return delivered
}

func (n *EventNotifier) sendMedicationDispensed(ctx context.Context, evt *eventsv1.DeviceEvent) error {
	subject := fmt.Sprintf("[Medsage] Medication Dispensed — Device %s", shortID(evt.DeviceId))
	body := fmt.Sprintf(`<h2>Medication Dispensed</h2>
<p><strong>Device:</strong> %s</p>
<p><strong>Time:</strong> %s</p>`,
		html.EscapeString(evt.DeviceId),
		formatTimestamp(evt.TimestampUnix),
	)

	return n.send(ctx, subject, body)
}

func (n *EventNotifier) sendMedicationMissed(ctx context.Context, evt *eventsv1.DeviceEvent) error {
	p := evt.GetMedicationMissed()
	detail := ""
	if p != nil {
		detail = fmt.Sprintf("<p><strong>Scheduled:</strong> %02d:%02d</p><p><strong>Timeout:</strong> %ds</p>",
			p.Hour, p.Minute, p.TimeoutSecs)
	}

	subject := fmt.Sprintf("[Medsage] MISSED Medication — Device %s", shortID(evt.DeviceId))
	body := fmt.Sprintf(`<h2>Medication Missed</h2>
<p><strong>Device:</strong> %s</p>
<p><strong>Time:</strong> %s</p>
%s
<p>The patient did not take their scheduled medication. Please follow up.</p>`,
		html.EscapeString(evt.DeviceId),
		formatTimestamp(evt.TimestampUnix),
		detail,
	)

	return n.send(ctx, subject, body)
}

func (n *EventNotifier) sendMedicationConfirmed(ctx context.Context, evt *eventsv1.DeviceEvent) error {
	p := evt.GetMedicationConfirmed()
	detail := ""
	if p != nil && p.DelaySecs > 0 {
		detail = fmt.Sprintf("<p><strong>Delay:</strong> %ds after alert</p>", p.DelaySecs)
	}

	subject := fmt.Sprintf("[Medsage] Medication Confirmed — Device %s", shortID(evt.DeviceId))
	body := fmt.Sprintf(`<h2>Medication Confirmed</h2>
<p><strong>Device:</strong> %s</p>
<p><strong>Time:</strong> %s</p>
%s
<p>The patient confirmed taking their medication.</p>`,
		html.EscapeString(evt.DeviceId),
		formatTimestamp(evt.TimestampUnix),
		detail,
	)

	return n.send(ctx, subject, body)
}

func (n *EventNotifier) sendAlarmTriggered(ctx context.Context, evt *eventsv1.DeviceEvent) error {
	p := evt.GetAlarmTriggered()
	timeStr := ""
	if p != nil {
		timeStr = fmt.Sprintf("%02d:%02d", p.Hour, p.Minute)
	}

	subject := fmt.Sprintf("[Medsage] ALERT: Alarm Triggered — Device %s", shortID(evt.DeviceId))
	body := fmt.Sprintf(`<h2>Device Alert</h2>
<p><strong>Device:</strong> %s</p>
<p><strong>Alarm Time:</strong> %s</p>
<p><strong>Event Time:</strong> %s</p>`,
		html.EscapeString(evt.DeviceId),
		timeStr,
		formatTimestamp(evt.TimestampUnix),
	)

	return n.send(ctx, subject, body)
}

func (n *EventNotifier) sendBugReport(ctx context.Context, evt *eventsv1.DeviceEvent) error {
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

	return n.send(ctx, subject, body)
}

func (n *EventNotifier) send(ctx context.Context, subject, htmlBody string) error {
	_, err := n.emailClient.Send(ctx, email.SendRequest{
		To:      []string{n.alertTo},
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
