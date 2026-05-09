package main

import (
	"strings"
	"testing"

	commandsv1 "medsage/proto/medsage/commands/v1"
)

func TestHandleSendEmailMissingRecipient(t *testing.T) {
	// The validation guard runs before any client call, so a handler with
	// no email client is sufficient to exercise the error path.
	h := &CommandHandler{}
	cmd := &commandsv1.SendEmail{
		CommandId: "cmd-1",
		To:        "",
		Subject:   "anything",
	}
	err := h.HandleSendEmail(t.Context(), cmd)
	if err == nil {
		t.Fatal("expected error for missing recipient")
	}
	if !strings.Contains(err.Error(), "cmd-1") {
		t.Errorf("error should mention command id, got %q", err.Error())
	}
	if !strings.Contains(strings.ToLower(err.Error()), "recipient") {
		t.Errorf("error should mention recipient, got %q", err.Error())
	}
}
