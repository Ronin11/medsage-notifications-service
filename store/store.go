// Package store is everything notifications-service reads from and writes to
// Postgres, behind plain types so the decisions made on top of it — who is
// told, what they are told, whether a dose was missed — can be tested without
// a database.
package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"medsage/notifications-service/push"
)

// DefaultMissTimeoutMin is the firmware's default "missed after" window, used
// until a dispenser has reported its own (devices.miss_timeout_min NULL).
const DefaultMissTimeoutMin = 15

// Device is what a notification needs to know about a dispenser and the
// person it belongs to.
type Device struct {
	ID               string
	Name             string // devices.name, e.g. "Kitchen dispenser"; may be empty
	OwnerID          string // devices.user_id; empty for an unclaimed device
	OwnerDisplayName string // user_profiles.display_name of the owner; may be empty
	Timezone         string // IANA zone, or empty when the device never reported one
	Online           bool
	LastSeen         *time.Time
	// MissTimeoutMin is the dispenser's "missed after" window in minutes:
	// 0 means Never (the dispenser does not count misses, so neither do we).
	MissTimeoutMin int
}

// Schedule is one active medication schedule.
type Schedule struct {
	ID             string // raw UUID string; med_id is fnv1a-32 of exactly this
	MedicationName string
	Dosage         string
	Times          []string // "HH:MM", wall clock in the device's zone
	CreatedAt      time.Time
}

// DoseEvent is a stored event that records what happened to a dose.
type DoseEvent struct {
	Type      string // "medication_confirmed" | "medication_missed" | "medication_reconciled"
	Hour      int
	Minute    int
	MedID     uint32 // 0 when the firmware did not say which medication
	CreatedAt time.Time
}

// Prefs is what one person wants to be told. The zero value is not the
// default; use DefaultPrefs.
type Prefs struct {
	MissedDoses    bool
	DeviceProblems bool
	DosesTaken     bool
}

// DefaultPrefs mirrors the column defaults in V15: a user who never opened
// the settings gets missed doses and device problems, not every dose taken.
func DefaultPrefs() Prefs { return Prefs{MissedDoses: true, DeviceProblems: true} }

// Recipient is one member of a device's care circle.
type Recipient struct {
	UserID  string
	IsOwner bool
	Email   string
	Prefs   Prefs
	Tokens  []push.Token
}

// Postgres implements the service's reads and its one write (the sent ledger).
type Postgres struct {
	db     *sql.DB
	tokens *push.TokenStore
}

func NewPostgres(db *sql.DB) *Postgres {
	return &Postgres{db: db, tokens: push.NewTokenStore(db)}
}

const deviceColumns = `
	d.id::text, COALESCE(d.name, ''), COALESCE(d.user_id::text, ''),
	COALESCE(up.display_name, ''), COALESCE(d.timezone, ''),
	d.online, d.last_seen, d.miss_timeout_min`

func scanDevice(sc interface{ Scan(...any) error }) (Device, error) {
	var d Device
	var lastSeen sql.NullTime
	var miss sql.NullInt64
	if err := sc.Scan(&d.ID, &d.Name, &d.OwnerID, &d.OwnerDisplayName, &d.Timezone, &d.Online, &lastSeen, &miss); err != nil {
		return Device{}, err
	}
	if lastSeen.Valid {
		t := lastSeen.Time
		d.LastSeen = &t
	}
	d.MissTimeoutMin = DefaultMissTimeoutMin
	if miss.Valid {
		d.MissTimeoutMin = int(miss.Int64)
	}
	return d, nil
}

// Device returns one device. ok is false when it does not exist.
func (s *Postgres) Device(ctx context.Context, deviceID string) (Device, bool, error) {
	row := s.db.QueryRowContext(ctx, `SELECT `+deviceColumns+`
		FROM devices d LEFT JOIN user_profiles up ON up.user_id = d.user_id
		WHERE d.id = $1::uuid`, deviceID)
	d, err := scanDevice(row)
	if errors.Is(err, sql.ErrNoRows) {
		return Device{}, false, nil
	}
	if err != nil {
		return Device{}, false, fmt.Errorf("device %s: %w", deviceID, err)
	}
	return d, true, nil
}

// DevicesWithActiveSchedules is the set the periodic check looks at: a device
// with nothing scheduled has no dose to miss and nobody waiting on it.
func (s *Postgres) DevicesWithActiveSchedules(ctx context.Context) ([]Device, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT `+deviceColumns+`
		FROM devices d LEFT JOIN user_profiles up ON up.user_id = d.user_id
		WHERE EXISTS (SELECT 1 FROM medication_schedules ms
		              WHERE ms.device_id = d.id AND ms.status = 'active')`)
	if err != nil {
		return nil, fmt.Errorf("devices with schedules: %w", err)
	}
	defer rows.Close()
	var out []Device
	for rows.Next() {
		d, err := scanDevice(rows)
		if err != nil {
			return nil, fmt.Errorf("scan device: %w", err)
		}
		out = append(out, d)
	}
	return out, rows.Err()
}

// ActiveSchedules returns a device's active schedules.
func (s *Postgres) ActiveSchedules(ctx context.Context, deviceID string) ([]Schedule, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT id::text, medication_name, dosage, schedule_times, created_at
		FROM medication_schedules
		WHERE device_id = $1::uuid AND status = 'active'
		ORDER BY created_at`, deviceID)
	if err != nil {
		return nil, fmt.Errorf("schedules for %s: %w", deviceID, err)
	}
	defer rows.Close()
	var out []Schedule
	for rows.Next() {
		var sc Schedule
		var times []byte
		if err := rows.Scan(&sc.ID, &sc.MedicationName, &sc.Dosage, &times, &sc.CreatedAt); err != nil {
			return nil, fmt.Errorf("scan schedule: %w", err)
		}
		// A malformed times array is one bad schedule, not a reason to stop
		// checking the device's other medications.
		_ = json.Unmarshal(times, &sc.Times)
		out = append(out, sc)
	}
	return out, rows.Err()
}

// DoseEvents returns the events that settle a dose, recorded since `since`.
//
// A reconciliation counts: it means someone has already dealt with the dose
// (recorded it taken, or acknowledged the miss) and does not need telling.
func (s *Postgres) DoseEvents(ctx context.Context, deviceID string, since time.Time) ([]DoseEvent, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT event_type::text,
		       COALESCE((payload->>'hour')::int, 0),
		       COALESCE((payload->>'minute')::int, 0),
		       COALESCE((payload->>'med_id')::bigint, 0),
		       created_at
		FROM events
		WHERE stream_id = $1::uuid
		  AND event_type IN ('medication_confirmed', 'medication_missed', 'medication_reconciled')
		  AND created_at >= $2`, deviceID, since)
	if err != nil {
		return nil, fmt.Errorf("dose events for %s: %w", deviceID, err)
	}
	defer rows.Close()
	var out []DoseEvent
	for rows.Next() {
		var e DoseEvent
		var medID int64
		if err := rows.Scan(&e.Type, &e.Hour, &e.Minute, &medID, &e.CreatedAt); err != nil {
			return nil, fmt.Errorf("scan dose event: %w", err)
		}
		e.MedID = uint32(medID)
		out = append(out, e)
	}
	return out, rows.Err()
}

// Recipients returns the device's owner and caretakers with their contact
// details and preferences. Nobody outside that circle is ever returned — see
// push.TokenStore for why that is the only safe failure mode.
func (s *Postgres) Recipients(ctx context.Context, deviceID string) ([]Recipient, error) {
	rows, err := s.db.QueryContext(ctx, `
		WITH circle AS (
		    SELECT user_id, TRUE AS is_owner FROM devices
		     WHERE id = $1::uuid AND user_id IS NOT NULL
		    UNION
		    SELECT user_id, FALSE FROM device_caretakers WHERE device_id = $1::uuid
		)
		SELECT c.user_id::text, bool_or(c.is_owner), COALESCE(MAX(up.email), ''),
		       COALESCE(bool_and(np.missed_doses), TRUE),
		       COALESCE(bool_and(np.device_problems), TRUE),
		       COALESCE(bool_and(np.doses_taken), FALSE)
		FROM circle c
		LEFT JOIN user_profiles up ON up.user_id = c.user_id
		LEFT JOIN notification_prefs np ON np.user_id = c.user_id
		GROUP BY c.user_id`, deviceID)
	if err != nil {
		return nil, fmt.Errorf("recipients for %s: %w", deviceID, err)
	}
	defer rows.Close()
	byUser := map[string]*Recipient{}
	var out []*Recipient
	for rows.Next() {
		r := &Recipient{}
		if err := rows.Scan(&r.UserID, &r.IsOwner, &r.Email, &r.Prefs.MissedDoses, &r.Prefs.DeviceProblems, &r.Prefs.DosesTaken); err != nil {
			return nil, fmt.Errorf("scan recipient: %w", err)
		}
		byUser[r.UserID] = r
		out = append(out, r)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate recipients: %w", err)
	}

	tokens, err := s.tokens.ForDevice(ctx, deviceID)
	if err != nil {
		return nil, err
	}
	for _, t := range tokens {
		if r := byUser[t.UserID]; r != nil {
			r.Tokens = append(r.Tokens, t)
		}
	}

	res := make([]Recipient, len(out))
	for i, r := range out {
		res[i] = *r
	}
	return res, nil
}

// Claim records that an alert is being sent and reports whether this caller
// is the first. The primary key makes it atomic across the event consumer and
// the periodic check, and across replicas.
func (s *Postgres) Claim(ctx context.Context, deviceID, kind, occurrence string) (bool, error) {
	res, err := s.db.ExecContext(ctx, `
		INSERT INTO notification_sent (device_id, kind, occurrence)
		VALUES ($1::uuid, $2, $3)
		ON CONFLICT DO NOTHING`, deviceID, kind, occurrence)
	if err != nil {
		return false, fmt.Errorf("claim %s/%s for %s: %w", kind, occurrence, deviceID, err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return false, fmt.Errorf("claim rows affected: %w", err)
	}
	return n == 1, nil
}

// WasSent reports whether an alert was already sent, without claiming it.
func (s *Postgres) WasSent(ctx context.Context, deviceID, kind, occurrence string) (bool, error) {
	var exists bool
	err := s.db.QueryRowContext(ctx, `
		SELECT EXISTS (SELECT 1 FROM notification_sent
		               WHERE device_id = $1::uuid AND kind = $2 AND occurrence = $3)`,
		deviceID, kind, occurrence).Scan(&exists)
	if err != nil {
		return false, fmt.Errorf("was sent %s/%s for %s: %w", kind, occurrence, deviceID, err)
	}
	return exists, nil
}
