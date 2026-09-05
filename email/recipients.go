package email

import (
	"context"
	"database/sql"
	"fmt"
)

// RecipientStore resolves who should be emailed about a device.
//
// Scoped the same way push is: the device's owner plus anyone listed as its
// caretaker, and nobody else. Addresses come from the user_profiles projection
// (migration V9) that frontend-api maintains — Keycloak owns identity, but
// putting an auth server in the delivery path would mean a credential able to
// read every user just to send one email.
//
// An empty result is a real answer, not a reason to widen the audience: it
// means nobody is registered to hear about this device yet.
type RecipientStore struct {
	db *sql.DB
}

func NewRecipientStore(db *sql.DB) *RecipientStore {
	return &RecipientStore{db: db}
}

// ForDevice returns the distinct email addresses of a device's owner and
// caretakers. Users with no recorded address are simply absent.
func (s *RecipientStore) ForDevice(ctx context.Context, deviceID string) ([]string, error) {
	if s == nil || s.db == nil {
		return nil, nil
	}

	const query = `
		SELECT DISTINCT up.email
		FROM user_profiles up
		WHERE up.email IS NOT NULL
		  AND up.email <> ''
		  AND (
		    up.user_id IN (SELECT user_id FROM devices WHERE id = $1::uuid)
		    OR up.user_id IN (SELECT user_id FROM device_caretakers WHERE device_id = $1::uuid)
		  )`

	rows, err := s.db.QueryContext(ctx, query, deviceID)
	if err != nil {
		return nil, fmt.Errorf("query recipients for device %s: %w", deviceID, err)
	}
	defer rows.Close()

	var addrs []string
	for rows.Next() {
		var addr string
		if err := rows.Scan(&addr); err != nil {
			return nil, fmt.Errorf("scan recipient: %w", err)
		}
		addrs = append(addrs, addr)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate recipients: %w", err)
	}
	return addrs, nil
}
