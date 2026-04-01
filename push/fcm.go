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
func (f *FCMClient) Send(ctx context.Context, tokens []string, title, body string, data map[string]string) error {
	if len(tokens) == 0 {
		return nil
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
		},
	}

	resp, err := f.client.SendEachForMulticast(ctx, msg)
	if err != nil {
		return fmt.Errorf("fcm send: %w", err)
	}

	if resp.FailureCount > 0 {
		for i, r := range resp.Responses {
			if r.Error != nil {
				slog.Warn("FCM send failed for token", "index", i, "error", r.Error)
			}
		}
	}

	slog.Info("FCM notifications sent", "success", resp.SuccessCount, "failure", resp.FailureCount)
	return nil
}
