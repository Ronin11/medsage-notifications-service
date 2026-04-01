package nats

import (
	"context"
	"fmt"
	"log/slog"
	"time"

	eventsv1 "medsage/proto/medsage/events/v1"

	"github.com/nats-io/nats.go"
	"github.com/nats-io/nats.go/jetstream"
	"google.golang.org/protobuf/proto"
)

const (
	StreamName   = "EVENTS"
	ConsumerName = "notifications-service"
)

// EventHandler is called for each incoming protobuf DeviceEvent.
type EventHandler func(ctx context.Context, event *eventsv1.DeviceEvent) error

// Subscriber consumes events from the NATS JetStream EVENTS stream.
type Subscriber struct {
	conn     *nats.Conn
	js       jetstream.JetStream
	consumer jetstream.Consumer
	cancel   context.CancelFunc
}

// Connect creates a NATS subscriber with a durable JetStream consumer.
func Connect(url string, subjects []string) (*Subscriber, error) {
	nc, err := nats.Connect(url,
		nats.RetryOnFailedConnect(true),
		nats.MaxReconnects(-1),
		nats.ReconnectWait(2*time.Second),
		nats.DisconnectErrHandler(func(_ *nats.Conn, err error) {
			slog.Warn("NATS disconnected", "error", err)
		}),
		nats.ReconnectHandler(func(_ *nats.Conn) {
			slog.Info("NATS reconnected")
		}),
	)
	if err != nil {
		return nil, fmt.Errorf("nats connect: %w", err)
	}

	js, err := jetstream.New(nc)
	if err != nil {
		nc.Close()
		return nil, fmt.Errorf("jetstream init: %w", err)
	}

	// Ensure the EVENTS stream exists (create if missing).
	_, err = js.CreateOrUpdateStream(context.Background(), jetstream.StreamConfig{
		Name:     StreamName,
		Subjects: []string{"medsage.events.>"},
	})
	if err != nil {
		nc.Close()
		return nil, fmt.Errorf("jetstream create stream: %w", err)
	}

	// Create a durable consumer that filters to the subjects we care about.
	consumer, err := js.CreateOrUpdateConsumer(context.Background(), StreamName, jetstream.ConsumerConfig{
		Name:           ConsumerName,
		Durable:        ConsumerName,
		FilterSubjects: subjects,
		AckPolicy:      jetstream.AckExplicitPolicy,
		DeliverPolicy:  jetstream.DeliverNewPolicy,
		MaxDeliver:     5,
		AckWait:        30 * time.Second,
	})
	if err != nil {
		nc.Close()
		return nil, fmt.Errorf("jetstream create consumer: %w", err)
	}

	slog.Info("NATS subscriber connected",
		"url", url,
		"consumer", ConsumerName,
		"subjects", subjects,
	)

	return &Subscriber{conn: nc, js: js, consumer: consumer}, nil
}

// Start begins consuming messages and calling the handler for each one.
// It blocks until the context is cancelled or Close is called.
func (s *Subscriber) Start(handler EventHandler) error {
	ctx, cancel := context.WithCancel(context.Background())
	s.cancel = cancel

	cons, err := s.consumer.Consume(func(msg jetstream.Msg) {
		var evt eventsv1.DeviceEvent
		if err := proto.Unmarshal(msg.Data(), &evt); err != nil {
			slog.Error("Failed to unmarshal NATS event", "error", err, "subject", msg.Subject())
			msg.Term()
			return
		}

		slog.Info("Received event from NATS",
			"subject", msg.Subject(),
			"event_id", evt.EventId,
			"device_id", evt.DeviceId,
			"type", evt.EventType.String(),
		)

		if err := handler(ctx, &evt); err != nil {
			slog.Error("Failed to handle event", "error", err, "event_id", evt.EventId)
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

// Close stops consuming and closes the NATS connection.
func (s *Subscriber) Close() {
	if s.cancel != nil {
		s.cancel()
	}
	if s.conn != nil {
		s.conn.Drain()
	}
}
