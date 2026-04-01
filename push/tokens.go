package push

import (
	"context"
	"database/sql"
	"fmt"
)

// TokenStore queries push tokens from the database.
type TokenStore struct {
	db *sql.DB
}

// NewTokenStore creates a token store backed by the given database.
func NewTokenStore(db *sql.DB) *TokenStore {
	return &TokenStore{db: db}
}

// GetAllTokens returns all registered Expo push tokens.
// For MVP we notify all registered users on every event.
func (s *TokenStore) GetAllTokens(ctx context.Context) ([]string, error) {
	rows, err := s.db.QueryContext(ctx, "SELECT token FROM push_tokens WHERE platform = 'expo'")
	if err != nil {
		return nil, fmt.Errorf("query push tokens: %w", err)
	}
	defer rows.Close()

	var tokens []string
	for rows.Next() {
		var token string
		if err := rows.Scan(&token); err != nil {
			return nil, fmt.Errorf("scan push token: %w", err)
		}
		tokens = append(tokens, token)
	}
	return tokens, rows.Err()
}

// GetTokensForDevice returns push tokens for users who own or caretake a device.
func (s *TokenStore) GetTokensForDevice(ctx context.Context, deviceID string) ([]string, error) {
	query := `
		SELECT DISTINCT pt.token
		FROM push_tokens pt
		WHERE pt.platform = 'expo'
		  AND (
		    pt.user_id IN (SELECT user_id FROM devices WHERE id = $1::uuid)
		    OR pt.user_id IN (SELECT user_id FROM device_caretakers WHERE device_id = $1::uuid)
		  )`

	rows, err := s.db.QueryContext(ctx, query, deviceID)
	if err != nil {
		// Fall back to all tokens if device lookup fails (e.g., unprovisioned device)
		return s.GetAllTokens(ctx)
	}
	defer rows.Close()

	var tokens []string
	for rows.Next() {
		var token string
		if err := rows.Scan(&token); err != nil {
			return nil, fmt.Errorf("scan push token: %w", err)
		}
		tokens = append(tokens, token)
	}

	// If no device-specific tokens found, fall back to all tokens
	if len(tokens) == 0 {
		return s.GetAllTokens(ctx)
	}
	return tokens, rows.Err()
}

// GetFCMTokens returns all registered FCM push tokens.
func (s *TokenStore) GetFCMTokens(ctx context.Context) ([]string, error) {
	rows, err := s.db.QueryContext(ctx, "SELECT token FROM push_tokens WHERE platform = 'fcm'")
	if err != nil {
		return nil, fmt.Errorf("query fcm tokens: %w", err)
	}
	defer rows.Close()

	var tokens []string
	for rows.Next() {
		var token string
		if err := rows.Scan(&token); err != nil {
			return nil, fmt.Errorf("scan fcm token: %w", err)
		}
		tokens = append(tokens, token)
	}
	return tokens, rows.Err()
}

// GetFCMTokensForDevice returns FCM tokens for users who own or caretake a device.
func (s *TokenStore) GetFCMTokensForDevice(ctx context.Context, deviceID string) ([]string, error) {
	query := `
		SELECT DISTINCT pt.token
		FROM push_tokens pt
		WHERE pt.platform = 'fcm'
		  AND (
		    pt.user_id IN (SELECT user_id FROM devices WHERE id = $1::uuid)
		    OR pt.user_id IN (SELECT user_id FROM device_caretakers WHERE device_id = $1::uuid)
		  )`

	rows, err := s.db.QueryContext(ctx, query, deviceID)
	if err != nil {
		return s.GetFCMTokens(ctx)
	}
	defer rows.Close()

	var tokens []string
	for rows.Next() {
		var token string
		if err := rows.Scan(&token); err != nil {
			return nil, fmt.Errorf("scan fcm token: %w", err)
		}
		tokens = append(tokens, token)
	}

	if len(tokens) == 0 {
		return s.GetFCMTokens(ctx)
	}
	return tokens, rows.Err()
}
