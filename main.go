package main

import (
	"context"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"compumed/notifications-service/api"
	"compumed/notifications-service/email"
)

type Config struct {
	Port           string
	ResendAPIKey   string
	FromAddress    string
	ContactTo      string
	AllowedOrigins string
}

func loadConfig() Config {
	return Config{
		Port:           getEnv("PORT", "8080"),
		ResendAPIKey:   getEnv("RESEND_API_KEY", ""),
		FromAddress:    getEnv("FROM_ADDRESS", "Compumed <onboarding@resend.dev>"),
		ContactTo:      getEnv("CONTACT_TO", "nate.ashby11@gmail.com"),
		AllowedOrigins: getEnv("ALLOWED_ORIGINS", "*"),
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

	slog.Info("Starting Compumed Notifications Service")

	emailClient := email.NewClient(cfg.ResendAPIKey, cfg.FromAddress)
	server := api.NewServer(":"+cfg.Port, emailClient, cfg.ContactTo, cfg.AllowedOrigins)

	// Graceful shutdown
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	go func() {
		if err := server.Start(); err != nil && err != http.ErrServerClosed {
			slog.Error("HTTP server error", "error", err)
			os.Exit(1)
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
