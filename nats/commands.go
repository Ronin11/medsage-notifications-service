package nats

import (
	"context"
	"fmt"
	"log/slog"
	"time"

	commandsv1 "medsage/proto/medsage/commands/v1"

	"github.com/nats-io/nats.go"
	"github.com/nats-io/nats.go/jetstream"
	"google.golang.org/protobuf/proto"
)

const (
	CommandsStreamName   = "COMMANDS"
	CommandsConsumerName = "notifications-service-commands"

	SubjectEmailSend = "medsage.commands.email.send"
)

// EmailHandler is invoked for each SendEmail command pulled from JetStream.
type EmailHandler func(ctx context.Context, cmd *commandsv1.SendEmail) error

// CommandsSubscriber consumes SendEmail commands from the COMMANDS JetStream
// stream. It is intentionally separate from the EVENTS subscriber: events
// describe history (read-many, retain-forever shape), commands describe
// pending work (consume-once, ack-and-discard shape).
type CommandsSubscriber struct {
	conn     *nats.Conn
	js       jetstream.JetStream
	consumer jetstream.Consumer
	cancel   context.CancelFunc
}

func ConnectCommands(url string) (*CommandsSubscriber, error) {
	nc, err := nats.Connect(url,
		nats.RetryOnFailedConnect(true),
		nats.MaxReconnects(-1),
		nats.ReconnectWait(2*time.Second),
	)
	if err != nil {
		return nil, fmt.Errorf("nats connect: %w", err)
	}

	js, err := jetstream.New(nc)
	if err != nil {
		nc.Close()
		return nil, fmt.Errorf("jetstream init: %w", err)
	}

	_, err = js.CreateOrUpdateStream(context.Background(), jetstream.StreamConfig{
		Name:     CommandsStreamName,
		Subjects: []string{"medsage.commands.>"},
	})
	if err != nil {
		nc.Close()
		return nil, fmt.Errorf("jetstream create stream: %w", err)
	}

	consumer, err := js.CreateOrUpdateConsumer(context.Background(), CommandsStreamName, jetstream.ConsumerConfig{
		Name:           CommandsConsumerName,
		Durable:        CommandsConsumerName,
		FilterSubjects: []string{SubjectEmailSend},
		AckPolicy:      jetstream.AckExplicitPolicy,
		DeliverPolicy:  jetstream.DeliverAllPolicy,
		MaxDeliver:     5,
		AckWait:        60 * time.Second,
	})
	if err != nil {
		nc.Close()
		return nil, fmt.Errorf("jetstream create consumer: %w", err)
	}

	slog.Info("NATS commands subscriber connected",
		"url", url,
		"consumer", CommandsConsumerName,
		"subjects", []string{SubjectEmailSend},
	)

	return &CommandsSubscriber{conn: nc, js: js, consumer: consumer}, nil
}

func (s *CommandsSubscriber) Start(handler EmailHandler) error {
	ctx, cancel := context.WithCancel(context.Background())
	s.cancel = cancel

	cons, err := s.consumer.Consume(func(msg jetstream.Msg) {
		var cmd commandsv1.SendEmail
		if err := proto.Unmarshal(msg.Data(), &cmd); err != nil {
			slog.Error("Failed to unmarshal SendEmail command", "error", err, "subject", msg.Subject())
			msg.Term()
			return
		}

		slog.Info("Received SendEmail command",
			"command_id", cmd.CommandId,
			"to", cmd.To,
			"source", cmd.Source,
		)

		if err := handler(ctx, &cmd); err != nil {
			slog.Error("Failed to handle SendEmail command", "error", err, "command_id", cmd.CommandId)
			msg.Nak()
			return
		}

		msg.Ack()
	})
	if err != nil {
		cancel()
		return fmt.Errorf("consume: %w", err)
	}

	<-ctx.Done()
	cons.Stop()
	return nil
}

func (s *CommandsSubscriber) Close() {
	if s.cancel != nil {
		s.cancel()
	}
	if s.conn != nil {
		s.conn.Drain()
	}
}
