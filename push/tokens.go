package push

import (
	"context"
	"database/sql"
	"fmt"
)

// TokenStore queries push tokens from the database.
//
// Every lookup here is scoped to a device's owner and caretakers. There is
// deliberately no "all tokens" query: these notifications carry PHI — that a
// named device missed a dose, and when — so the only safe failure mode is to
// deliver to nobody. An earlier version fell back to broadcasting to every
// registered token when the scoped lookup errored *or came back empty*, which
// meant an unclaimed device sent one patient's medication events to every user
// of the system.
type TokenStore struct {
	db *sql.DB
}

// NewTokenStore creates a token store backed by the given database.
func NewTokenStore(db *sql.DB) *TokenStore {
	return &TokenStore{db: db}
}

// tokensForDevice returns push tokens on one platform for the users who own or
// caretake a device. An empty result is a legitimate answer — a device nobody
// is responsible for yet — and is returned as an empty slice, not an error.
func (s *TokenStore) tokensForDevice(ctx context.Context, platform, deviceID string) ([]string, error) {
	const query = `
		SELECT DISTINCT pt.token
		FROM push_tokens pt
		WHERE pt.platform = $1
		  AND (
		    pt.user_id IN (SELECT user_id FROM devices WHERE id = $2::uuid)
		    OR pt.user_id IN (SELECT user_id FROM device_caretakers WHERE device_id = $2::uuid)
		  )`

	rows, err := s.db.QueryContext(ctx, query, platform, deviceID)
	if err != nil {
		// Report the failure. Notifying the wrong people is worse than not
		// notifying anyone, so there is no fallback to widen the audience.
		return nil, fmt.Errorf("query %s push tokens for device %s: %w", platform, deviceID, err)
	}
	defer rows.Close()

	var tokens []string
	for rows.Next() {
		var token string
		if err := rows.Scan(&token); err != nil {
			return nil, fmt.Errorf("scan %s push token: %w", platform, err)
		}
		tokens = append(tokens, token)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate %s push tokens: %w", platform, err)
	}
	return tokens, nil
}

// GetTokensForDevice returns Expo push tokens for users who own or caretake a device.
func (s *TokenStore) GetTokensForDevice(ctx context.Context, deviceID string) ([]string, error) {
	return s.tokensForDevice(ctx, "expo", deviceID)
}

// GetFCMTokensForDevice returns FCM tokens for users who own or caretake a device.
func (s *TokenStore) GetFCMTokensForDevice(ctx context.Context, deviceID string) ([]string, error) {
	return s.tokensForDevice(ctx, "fcm", deviceID)
}
