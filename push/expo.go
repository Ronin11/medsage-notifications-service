package push

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
)

const expoPushURL = "https://exp.host/--/api/v2/push/send"

// Message represents an Expo push notification.
type Message struct {
	To       string            `json:"to"`
	Title    string            `json:"title"`
	Body     string            `json:"body"`
	Sound    string            `json:"sound,omitempty"`
	Data     map[string]string `json:"data,omitempty"`
	Priority string            `json:"priority,omitempty"`
	// ChannelID is the Android notification channel. The app creates
	// AndroidChannel; a message naming a channel that does not exist yet
	// falls back to the default one rather than being dropped.
	ChannelID string `json:"channelId,omitempty"`
}

// Response from the Expo push API.
type Response struct {
	Data []struct {
		Status  string `json:"status"`
		ID      string `json:"id,omitempty"`
		Message string `json:"message,omitempty"`
	} `json:"data"`
}

// Send sends push notifications to one or more Expo push tokens.
func Send(ctx context.Context, messages []Message) error {
	if len(messages) == 0 {
		return nil
	}

	body, err := json.Marshal(messages)
	if err != nil {
		return fmt.Errorf("marshal push messages: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, expoPushURL, bytes.NewReader(body))
	if err != nil {
		return fmt.Errorf("create push request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return fmt.Errorf("send push request: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("expo push API returned %d", resp.StatusCode)
	}

	var result Response
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return fmt.Errorf("decode push response: %w", err)
	}

	ok := 0
	for _, d := range result.Data {
		if d.Status != "ok" {
			slog.Warn("Push notification failed", "status", d.Status, "message", d.Message)
			continue
		}
		ok++
	}
	// Every ticket rejected is not a delivery; the caller falls back to email.
	if ok == 0 {
		return fmt.Errorf("expo: all %d messages rejected", len(messages))
	}
	return nil
}
