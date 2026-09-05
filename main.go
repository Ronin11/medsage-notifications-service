package main

import (
	"context"
	"database/sql"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	_ "github.com/lib/pq"

	"medsage/notifications-service/api"
	"medsage/notifications-service/email"
	natsbus "medsage/notifications-service/nats"
	"medsage/notifications-service/push"
)

type Config struct {
	Port                string
	ResendAPIKey        string
	FromAddress         string
	ContactTo           string
	AlertTo             string
	AllowedOrigins      string
	NATSURL             string
	DatabaseURL         string
	FirebaseCredentials string
}

func loadConfig() Config {
	return Config{
		Port:         getEnv("PORT", "8080"),
		ResendAPIKey: getEnv("RESEND_API_KEY", ""),
		FromAddress:  getEnv("FROM_ADDRESS", "Medsage <onboarding@resend.dev>"),
		// No default: this is where the site's contact form lands, and a
		// hardcoded personal address here also became the destination for every
		// patient's medication alerts.
		ContactTo: getEnv("CONTACT_TO", ""),
		// Optional, and empty on purpose. Patient events are addressed via push
		// tokens scoped to the device's owner and caretakers; this is only a
		// single-tenant escape hatch for a bench or demo rig. Setting it in a
		// deployment with more than one patient sends everyone's medication
		// events to one inbox.
		AlertTo:             getEnv("ALERT_TO", ""),
		AllowedOrigins:      getEnv("ALLOWED_ORIGINS", "*"),
		NATSURL:             getEnv("NATS_URL", "nats://nats:4222"),
		DatabaseURL:         getEnv("DATABASE_URL", ""),
		FirebaseCredentials: getEnv("GOOGLE_APPLICATION_CREDENTIALS", ""),
	}
}

func getEnv(key, defaultValue string) string {
	if value := os.Getenv(key); value != "" {
		return value
	}
	return defaultValue
}

func main() {
	logger := slog.New(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{
		Level: slog.LevelInfo,
	}))
	slog.SetDefault(logger)

	cfg := loadConfig()

	if cfg.ResendAPIKey == "" {
		slog.Error("RESEND_API_KEY is required")
		os.Exit(1)
	}
	if cfg.ContactTo == "" {
		slog.Error("CONTACT_TO is required (destination for the site contact form and device bug reports)")
		os.Exit(1)
	}
	if cfg.AlertTo != "" {
		slog.Warn("ALERT_TO is set: undelivered patient events will be emailed to this single address. "+
			"Safe only for a single-patient bench or demo rig — in a multi-patient deployment it sends "+
			"every patient's medication events to one inbox.",
			"alert_to", cfg.AlertTo)
	}

	slog.Info("Starting Medsage Notifications Service")

	emailClient := email.NewClient(cfg.ResendAPIKey, cfg.FromAddress)

	// Connect to postgres for push token and email recipient lookups
	var tokenStore *push.TokenStore
	var recipientStore *email.RecipientStore
	if cfg.DatabaseURL != "" {
		db, err := sql.Open("postgres", cfg.DatabaseURL)
		if err != nil {
			slog.Error("Failed to connect to database", "error", err)
		} else if err := db.Ping(); err != nil {
			slog.Warn("Database not reachable, push notifications disabled", "error", err)
		} else {
			tokenStore = push.NewTokenStore(db)
			recipientStore = email.NewRecipientStore(db)
			slog.Info("Push token and email recipient stores connected")
		}
	} else {
		slog.Warn("DATABASE_URL not set: notifications cannot be addressed to anyone")
	}

	// Initialize FCM client
	var fcmClient *push.FCMClient
	if cfg.FirebaseCredentials != "" {
		fc, err := push.NewFCMClient(context.Background(), cfg.FirebaseCredentials)
		if err != nil {
			slog.Error("Failed to initialize FCM client", "error", err)
		} else {
			fcmClient = fc
			slog.Info("FCM push notifications enabled")
		}
	} else {
		slog.Warn("GOOGLE_APPLICATION_CREDENTIALS not set, FCM push notifications disabled")
	}

	server := api.NewServer(":"+cfg.Port, emailClient, cfg.ContactTo, cfg.AllowedOrigins)

	// Graceful shutdown
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	// Start HTTP server (always available, even without NATS)
	go func() {
		if err := server.Start(); err != nil && err != http.ErrServerClosed {
			slog.Error("HTTP server error", "error", err)
			os.Exit(1)
		}
	}()

	// Connect to NATS in the background with retries
	natsSubjects := []string{
		"medsage.events.medication.>",
		"medsage.events.alerts",
		"medsage.events.bug.report",
	}
	notifier := NewEventNotifier(emailClient, tokenStore, recipientStore, fcmClient, cfg.AlertTo, cfg.ContactTo)

	go func() {
		for {
			subscriber, err := natsbus.Connect(cfg.NATSURL, natsSubjects)
			if err != nil {
				slog.Warn("NATS not available, retrying in 5s", "error", err)
				select {
				case <-ctx.Done():
					return
				case <-time.After(5 * time.Second):
					continue
				}
			}

			slog.Info("NATS event consumer started")
			if err := subscriber.Start(notifier.Handle); err != nil {
				slog.Error("NATS subscriber error", "error", err)
			}
			subscriber.Close()

			// If context is done, stop retrying
			select {
			case <-ctx.Done():
				return
			default:
				slog.Warn("NATS subscriber stopped, reconnecting in 5s")
				time.Sleep(5 * time.Second)
			}
		}
	}()

	// Commands subscriber — handles SendEmail (and future) command messages.
	cmdHandler := NewCommandHandler(emailClient)
	go func() {
		for {
			subscriber, err := natsbus.ConnectCommands(cfg.NATSURL)
			if err != nil {
				slog.Warn("NATS commands not available, retrying in 5s", "error", err)
				select {
				case <-ctx.Done():
					return
				case <-time.After(5 * time.Second):
					continue
				}
			}

			slog.Info("NATS commands consumer started")
			if err := subscriber.Start(cmdHandler.HandleSendEmail); err != nil {
				slog.Error("NATS commands subscriber error", "error", err)
			}
			subscriber.Close()

			select {
			case <-ctx.Done():
				return
			default:
				slog.Warn("NATS commands subscriber stopped, reconnecting in 5s")
				time.Sleep(5 * time.Second)
			}
		}
	}()

	<-ctx.Done()
	slog.Info("Shutting down...")

	shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	if err := server.Shutdown(shutdownCtx); err != nil {
		slog.Error("Shutdown error", "error", err)
	}

	slog.Info("Notifications service stopped")
}
