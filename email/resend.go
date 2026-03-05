package email

import (
	"context"
	"fmt"
	"log/slog"

	"github.com/resend/resend-go/v2"
)

type Client struct {
	resend  *resend.Client
	fromAddr string
}

func NewClient(apiKey, fromAddr string) *Client {
	return &Client{
		resend:  resend.NewClient(apiKey),
		fromAddr: fromAddr,
	}
}

type SendRequest struct {
	To      []string `json:"to"`
	Subject string   `json:"subject"`
	HTML    string   `json:"html"`
	Text    string   `json:"text"`
}

type SendResponse struct {
	ID string `json:"id"`
}

func (c *Client) Send(ctx context.Context, req SendRequest) (*SendResponse, error) {
	params := &resend.SendEmailRequest{
		From:    c.fromAddr,
		To:      req.To,
		Subject: req.Subject,
		Html:    req.HTML,
		Text:    req.Text,
	}

	sent, err := c.resend.Emails.SendWithContext(ctx, params)
	if err != nil {
		slog.Error("Failed to send email", "error", err, "to", req.To)
		return nil, fmt.Errorf("send email: %w", err)
	}

	slog.Info("Email sent", "id", sent.Id, "to", req.To, "subject", req.Subject)
	return &SendResponse{ID: sent.Id}, nil
}
