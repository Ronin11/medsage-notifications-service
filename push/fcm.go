package push

import (
	"context"
	"fmt"
	"log/slog"

	firebase "firebase.google.com/go/v4"
	"firebase.google.com/go/v4/messaging"
	"google.golang.org/api/option"
)

// FCMClient sends push notifications via Firebase Cloud Messaging.
type FCMClient struct {
	client *messaging.Client
}

// NewFCMClient initializes the Firebase Admin SDK and returns an FCM client.
// credentialsFile is the path to the service account JSON key.
func NewFCMClient(ctx context.Context, credentialsFile string) (*FCMClient, error) {
	app, err := firebase.NewApp(ctx, nil, option.WithCredentialsFile(credentialsFile))
	if err != nil {
		return nil, fmt.Errorf("init firebase app: %w", err)
	}

	client, err := app.Messaging(ctx)
	if err != nil {
		return nil, fmt.Errorf("init firebase messaging: %w", err)
	}

	return &FCMClient{client: client}, nil
}

// Send sends a push notification to the given FCM registration tokens.
//
// dead lists the tokens FCM said are no longer registered (the app was
// uninstalled, or the phone replaced the token). They will never work again,
// and left in place they pile up — one account had 203 dead tokens to one
// live one — so the caller removes them.
func (f *FCMClient) Send(ctx context.Context, tokens []string, title, body string, data map[string]string) (dead []string, err error) {
	if len(tokens) == 0 {
		return nil, nil
	}

	msg := &messaging.MulticastMessage{
		Tokens: tokens,
		Notification: &messaging.Notification{
			Title: title,
			Body:  body,
		},
		Data: data,
		Android: &messaging.AndroidConfig{
			Priority: "high",
			Notification: &messaging.AndroidNotification{
				ChannelID: AndroidChannel,
			},
		},
	}

	resp, err := f.client.SendEachForMulticast(ctx, msg)
	if err != nil {
		return nil, fmt.Errorf("fcm send: %w", err)
	}

	if resp.FailureCount > 0 {
		// One line per distinct error, not per token: a user with a history of
		// reinstalls can have hundreds of dead registrations.
		byError := map[string]int{}
		for i, r := range resp.Responses {
			if r.Error != nil {
				byError[r.Error.Error()]++
				// Responses are in the order of the tokens sent.
				if messaging.IsUnregistered(r.Error) && i < len(tokens) {
					dead = append(dead, tokens[i])
				}
			}
		}
		for msg, n := range byError {
			slog.Warn("FCM send failed for tokens", "error", msg, "count", n)
		}
	}

	slog.Info("FCM notifications sent", "success", resp.SuccessCount, "failure", resp.FailureCount)
	// Every token rejected is not a delivery: the caller falls back to email
	// on false, and a stale token must not swallow a missed-dose alert.
	if resp.SuccessCount == 0 {
		return dead, fmt.Errorf("fcm: all %d tokens rejected", resp.FailureCount)
	}
	return dead, nil
}
