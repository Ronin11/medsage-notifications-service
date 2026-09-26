package push

import (
	"context"
	"database/sql"
	"fmt"
	"log/slog"

	"github.com/lib/pq"
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

// Token is one registered device of one user. UserID travels with it so the
// caller can apply that person's notification preferences.
type Token struct {
	UserID   string
	Token    string
	Platform string // "expo" | "fcm"
}

// ForDevice returns the push tokens of the users who own or caretake a device.
// An empty result is a legitimate answer — a device nobody is responsible for
// yet — and is returned as an empty slice, not an error.
func (s *TokenStore) ForDevice(ctx context.Context, deviceID string) ([]Token, error) {
	const query = `
		SELECT DISTINCT pt.user_id::text, pt.token, pt.platform
		FROM push_tokens pt
		WHERE pt.user_id IN (SELECT user_id FROM devices WHERE id = $1::uuid)
		   OR pt.user_id IN (SELECT user_id FROM device_caretakers WHERE device_id = $1::uuid)`

	rows, err := s.db.QueryContext(ctx, query, deviceID)
	if err != nil {
		// Report the failure. Notifying the wrong people is worse than not
		// notifying anyone, so there is no fallback to widen the audience.
		return nil, fmt.Errorf("query push tokens for device %s: %w", deviceID, err)
	}
	defer rows.Close()

	var tokens []Token
	for rows.Next() {
		var t Token
		if err := rows.Scan(&t.UserID, &t.Token, &t.Platform); err != nil {
			return nil, fmt.Errorf("scan push token: %w", err)
		}
		tokens = append(tokens, t)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate push tokens: %w", err)
	}
	return tokens, nil
}

// Forget deletes tokens the push provider says are no longer registered.
// Failure is logged, not returned: pruning is housekeeping, and the alert
// that found them has already been sent or fallen back to email.
func (s *TokenStore) Forget(ctx context.Context, tokens []string) {
	if len(tokens) == 0 {
		return
	}
	res, err := s.db.ExecContext(ctx, `DELETE FROM push_tokens WHERE token = ANY($1)`, pq.Array(tokens))
	if err != nil {
		slog.Warn("Could not remove unregistered push tokens", "error", err, "count", len(tokens))
		return
	}
	n, _ := res.RowsAffected()
	slog.Info("Removed unregistered push tokens", "count", n)
}
