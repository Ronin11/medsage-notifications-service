package main

import (
	"context"
	"fmt"
	"html"
	"log/slog"
	"time"

	eventsv1 "medsage/proto/medsage/events/v1"

	"medsage/notifications-service/email"
)

// EventNotifier handles NATS events and sends email notifications.
type EventNotifier struct {
	emailClient *email.Client
	alertTo     string // recipient for device alerts / medication events
}

func NewEventNotifier(emailClient *email.Client, alertTo string) *EventNotifier {
	return &EventNotifier{emailClient: emailClient, alertTo: alertTo}
}

// Handle processes a protobuf DeviceEvent and sends the appropriate notification email.
func (n *EventNotifier) Handle(ctx context.Context, evt *eventsv1.DeviceEvent) error {
	switch evt.EventType {
	case eventsv1.EventType_EVENT_TYPE_MEDICATION_DISPENSED:
		return n.sendMedicationDispensed(ctx, evt)
	case eventsv1.EventType_EVENT_TYPE_MEDICATION_MISSED:
		return n.sendMedicationMissed(ctx, evt)
	case eventsv1.EventType_EVENT_TYPE_MEDICATION_CONFIRMED:
		return n.sendMedicationConfirmed(ctx, evt)
	case eventsv1.EventType_EVENT_TYPE_ALARM_TRIGGERED:
		return n.sendAlarmTriggered(ctx, evt)
	case eventsv1.EventType_EVENT_TYPE_BUG_REPORT:
		return n.sendBugReport(ctx, evt)
	default:
		slog.Debug("Ignoring unhandled event type", "type", evt.EventType.String())
		return nil
	}
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
