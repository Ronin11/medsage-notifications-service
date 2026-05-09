package main

import (
	"context"
	"fmt"

	commandsv1 "medsage/proto/medsage/commands/v1"

	"medsage/notifications-service/email"
)

// CommandHandler maps inbound SendEmail commands to the email client.
// Idempotency is currently best-effort: at-least-once delivery means a
// retried command can produce a duplicate send. Resend dedupes on
// command_id when we add a delivery log (TODO).
type CommandHandler struct {
	emailClient *email.Client
}

func NewCommandHandler(emailClient *email.Client) *CommandHandler {
	return &CommandHandler{emailClient: emailClient}
}

func (h *CommandHandler) HandleSendEmail(ctx context.Context, cmd *commandsv1.SendEmail) error {
	if cmd.To == "" {
		return fmt.Errorf("SendEmail command %s missing recipient", cmd.CommandId)
	}

	attachments := make([]email.Attachment, 0, len(cmd.Attachments))
	for _, a := range cmd.Attachments {
		attachments = append(attachments, email.Attachment{
			Filename:    a.Filename,
			ContentType: a.ContentType,
			Content:     a.Content,
		})
	}

	_, err := h.emailClient.Send(ctx, email.SendRequest{
		To:          []string{cmd.To},
		Subject:     cmd.Subject,
		HTML:        cmd.BodyHtml,
		Text:        cmd.BodyText,
		Attachments: attachments,
	})
	return err
}
