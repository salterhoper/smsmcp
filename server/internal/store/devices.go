package store

import (
	"context"
	"database/sql"
	"errors"
	"time"

	"github.com/salterhoper/smsmcp/server/internal/ids"
)

const (
	DevicePending  = "pending_verification"
	DeviceActive   = "active"
	DeviceDisabled = "disabled"
)

type Device struct {
	ID                string `json:"id"`
	Name              string `json:"name"`
	PhoneNumber       string `json:"phone_number"`
	SimSubscriptionID int    `json:"sim_subscription_id"`
	Status            string `json:"status"`
	VerifyCode        string `json:"-"`
	Battery           *int   `json:"battery,omitempty"`
	Signal            *int   `json:"signal,omitempty"`
	AppVersion        string `json:"app_version,omitempty"`
	LastSeenAt        int64  `json:"last_seen_at,omitempty"`
	CreatedAt         int64  `json:"created_at"`
}

const devCols = `id, name, phone_number, sim_subscription_id, status, COALESCE(verify_code,''), battery, signal, COALESCE(app_version,''), COALESCE(last_seen_at,0), created_at`

func scanDev(r interface{ Scan(...any) error }) (Device, error) {
	var d Device
	var bat, sig sql.NullInt64
	err := r.Scan(&d.ID, &d.Name, &d.PhoneNumber, &d.SimSubscriptionID, &d.Status, &d.VerifyCode, &bat, &sig, &d.AppVersion, &d.LastSeenAt, &d.CreatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return d, ErrNotFound
	}
	if bat.Valid {
		v := int(bat.Int64)
		d.Battery = &v
	}
	if sig.Valid {
		v := int(sig.Int64)
		d.Signal = &v
	}
	return d, err
}

// CreatePairingToken stores the hash of a single-use pairing token.
func (s *Store) CreatePairingToken(ctx context.Context, tokenHash string, ttl time.Duration) error {
	_, err := s.db.ExecContext(ctx, `INSERT INTO pairing_tokens (hash, expires_at) VALUES (?, ?)`,
		tokenHash, time.Now().Add(ttl).UnixMilli())
	return err
}

type NewDevice struct {
	PairingTokenHash  string
	Name              string
	PhoneNumber       string
	SimSubscriptionID int
	DeviceTokenHash   string
	VerifyCode        string
}

// PairDevice consumes a pairing token and creates a device awaiting number
// verification, atomically. A token can pair exactly one device.
func (s *Store) PairDevice(ctx context.Context, in NewDevice) (Device, error) {
	var d Device
	err := s.tx(ctx, func(tx *sql.Tx) error {
		now := nowMs()
		res, err := tx.ExecContext(ctx, `UPDATE pairing_tokens SET used_at = ?
			WHERE hash = ? AND used_at IS NULL AND expires_at > ?`, now, in.PairingTokenHash, now)
		if err != nil {
			return err
		}
		if n, _ := res.RowsAffected(); n == 0 {
			return ErrTokenExpired
		}
		d = Device{ID: ids.New("dev"), Name: in.Name, PhoneNumber: in.PhoneNumber,
			SimSubscriptionID: in.SimSubscriptionID, Status: DevicePending, VerifyCode: in.VerifyCode, CreatedAt: now}
		_, err = tx.ExecContext(ctx, `INSERT INTO devices
			(id, name, phone_number, sim_subscription_id, token_hash, status, verify_code, created_at)
			VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
			d.ID, d.Name, d.PhoneNumber, d.SimSubscriptionID, in.DeviceTokenHash, d.Status, d.VerifyCode, now)
		return err
	})
	return d, err
}

func (s *Store) DeviceByTokenHash(ctx context.Context, h string) (Device, error) {
	return scanDev(s.db.QueryRowContext(ctx, `SELECT `+devCols+` FROM devices WHERE token_hash = ? AND status != 'disabled'`, h))
}

func (s *Store) GetDevice(ctx context.Context, id string) (Device, error) {
	return scanDev(s.db.QueryRowContext(ctx, `SELECT `+devCols+` FROM devices WHERE id = ?`, id))
}

func (s *Store) ListDevices(ctx context.Context) ([]Device, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT `+devCols+` FROM devices ORDER BY created_at`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Device
	for rows.Next() {
		d, err := scanDev(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, d)
	}
	return out, rows.Err()
}

// ActivateDevice marks a device verified. Returns false if it was not pending.
func (s *Store) ActivateDevice(ctx context.Context, id string) (bool, error) {
	res, err := s.db.ExecContext(ctx, `UPDATE devices SET status = 'active', verify_code = NULL
		WHERE id = ? AND status = 'pending_verification'`, id)
	if err != nil {
		return false, err
	}
	n, _ := res.RowsAffected()
	return n == 1, nil
}

func (s *Store) SetDeviceStatus(ctx context.Context, id, status string) error {
	_, err := s.db.ExecContext(ctx, `UPDATE devices SET status = ? WHERE id = ?`, status, id)
	return err
}

type Heartbeat struct {
	Battery    *int   `json:"battery,omitempty"`
	Signal     *int   `json:"signal,omitempty"`
	AppVersion string `json:"app_version,omitempty"`
}

func (s *Store) TouchDevice(ctx context.Context, id string, hb *Heartbeat) error {
	if hb == nil {
		_, err := s.db.ExecContext(ctx, `UPDATE devices SET last_seen_at = ? WHERE id = ?`, nowMs(), id)
		return err
	}
	_, err := s.db.ExecContext(ctx, `UPDATE devices SET last_seen_at = ?,
		battery = COALESCE(?, battery), signal = COALESCE(?, signal), app_version = COALESCE(NULLIF(?, ''), app_version)
		WHERE id = ?`, nowMs(), hb.Battery, hb.Signal, hb.AppVersion, id)
	return err
}
