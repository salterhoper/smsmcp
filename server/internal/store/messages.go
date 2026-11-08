package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"strings"

	"github.com/salterhoper/smsmcp/server/internal/ids"
)

// Message statuses. Outbound: queued -> dispatched -> accepted -> sending ->
// sent -> delivered, with failed/unknown as terminal-ish exits. Inbound
// messages are always "received".
const (
	StatusQueued     = "queued"     // stored, waiting for the phone
	StatusDispatched = "dispatched" // sent to the phone over WebSocket, lease running
	StatusAccepted   = "accepted"   // phone stored it (phone owns it now, no redispatch)
	StatusSending    = "sending"    // phone handed it to SmsManager
	StatusUnknown    = "unknown"    // phone crashed mid-send; never auto-retried
	StatusSent       = "sent"       // carrier accepted all parts
	StatusDelivered  = "delivered"  // delivery report for all parts
	StatusFailed     = "failed"
	StatusReceived   = "received"
)

// statusRank orders statuses. A status update is applied only if its rank is
// strictly higher than the stored one, which makes repeated and out-of-order
// reports harmless (idempotency for status updates). "unknown" ranks below
// "sent" so a late carrier confirmation can still resolve it.
var statusRank = map[string]int{
	StatusQueued:     0,
	StatusDispatched: 1,
	StatusAccepted:   2,
	StatusSending:    3,
	StatusUnknown:    4,
	StatusSent:       5,
	StatusDelivered:  6,
	StatusFailed:     6,
	StatusReceived:   6,
}

// ValidPhoneStatus reports whether a phone may report this status.
func ValidPhoneStatus(s string) bool {
	switch s {
	case StatusAccepted, StatusSending, StatusUnknown, StatusSent, StatusDelivered, StatusFailed:
		return true
	}
	return false
}

type Message struct {
	ID         string `json:"id"`
	DeviceID   string `json:"device_id,omitempty"`
	Direction  string `json:"direction"`
	PeerNumber string `json:"peer_number"`
	Body       string `json:"body"`
	Status     string `json:"status"`
	Error      string `json:"error,omitempty"`
	Attempts   int    `json:"attempts"`
	CreatedAt  int64  `json:"created_at"`
	UpdatedAt  int64  `json:"updated_at"`
}

const msgCols = `id, COALESCE(device_id,''), direction, peer_number, body, status, COALESCE(error,''), attempts, created_at, updated_at`

func scanMsg(r interface{ Scan(...any) error }) (Message, error) {
	var m Message
	err := r.Scan(&m.ID, &m.DeviceID, &m.Direction, &m.PeerNumber, &m.Body, &m.Status, &m.Error, &m.Attempts, &m.CreatedAt, &m.UpdatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return m, ErrNotFound
	}
	return m, err
}

type CreateOutbound struct {
	APIKeyID       string
	IdempotencyKey string
	DeviceID       string // optional; default = first active device
	To             string
	Body           string
}

// CreateOutboundMessage stores a new outbound SMS, or returns the existing one
// if (APIKeyID, IdempotencyKey) was seen before. created is false for a replay.
func (s *Store) CreateOutboundMessage(ctx context.Context, in CreateOutbound) (m Message, created bool, err error) {
	reqHash := ids.Hash(in.To, in.Body)
	err = s.tx(ctx, func(tx *sql.Tx) error {
		var existingHash string
		row := tx.QueryRowContext(ctx,
			`SELECT `+msgCols+`, COALESCE(request_hash,'') FROM messages WHERE api_key_id = ? AND idempotency_key = ?`,
			in.APIKeyID, in.IdempotencyKey)
		err := row.Scan(&m.ID, &m.DeviceID, &m.Direction, &m.PeerNumber, &m.Body, &m.Status, &m.Error, &m.Attempts, &m.CreatedAt, &m.UpdatedAt, &existingHash)
		if err == nil {
			if existingHash != reqHash {
				return ErrIdemReused
			}
			return nil
		}
		if !errors.Is(err, sql.ErrNoRows) {
			return err
		}

		deviceID := in.DeviceID
		if deviceID == "" {
			err := tx.QueryRowContext(ctx,
				`SELECT id FROM devices WHERE status = 'active' ORDER BY last_seen_at DESC NULLS LAST, created_at LIMIT 1`).Scan(&deviceID)
			if errors.Is(err, sql.ErrNoRows) {
				return ErrNoDevice
			} else if err != nil {
				return err
			}
		} else {
			var st string
			err := tx.QueryRowContext(ctx, `SELECT status FROM devices WHERE id = ?`, deviceID).Scan(&st)
			if errors.Is(err, sql.ErrNoRows) || (err == nil && st != "active") {
				return ErrNoDevice
			} else if err != nil {
				return err
			}
		}

		now := nowMs()
		m = Message{
			ID: ids.New("msg"), DeviceID: deviceID, Direction: "out", PeerNumber: in.To, Body: in.Body,
			Status: StatusQueued, CreatedAt: now, UpdatedAt: now,
		}
		_, err = tx.ExecContext(ctx, `INSERT INTO messages
			(id, device_id, direction, peer_number, body, status, status_rank, api_key_id, idempotency_key, request_hash, created_at, updated_at)
			VALUES (?, ?, 'out', ?, ?, ?, 0, ?, ?, ?, ?, ?)`,
			m.ID, m.DeviceID, m.PeerNumber, m.Body, m.Status, in.APIKeyID, in.IdempotencyKey, reqHash, now, now)
		if err != nil {
			return err
		}
		created = true
		return nil
	})
	return m, created, err
}

func (s *Store) GetMessage(ctx context.Context, id string) (Message, error) {
	return scanMsg(s.db.QueryRowContext(ctx, `SELECT `+msgCols+` FROM messages WHERE id = ?`, id))
}

// Dispatchable returns outbound messages for a device that need to be (re)sent
// to the phone: queued ones, and dispatched ones whose lease expired without
// an ack. Messages the phone has accepted are never returned again.
func (s *Store) Dispatchable(ctx context.Context, deviceID string, limit int) ([]Message, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT `+msgCols+` FROM messages
		WHERE device_id = ? AND direction = 'out'
		  AND (status = 'queued' OR (status = 'dispatched' AND lease_until < ?))
		ORDER BY created_at LIMIT ?`, deviceID, nowMs(), limit)
	if err != nil {
		return nil, err
	}
	return collect(rows)
}

// MarkDispatched records that the message was pushed to the phone and holds a
// lease until leaseUntil. It is a no-op if the phone already acked it.
func (s *Store) MarkDispatched(ctx context.Context, id string, leaseUntil int64) error {
	_, err := s.db.ExecContext(ctx, `UPDATE messages
		SET status = 'dispatched', status_rank = 1, lease_until = ?, attempts = attempts + 1, updated_at = ?
		WHERE id = ? AND status_rank <= 1`, leaseUntil, nowMs(), id)
	return err
}

// AdvanceStatus moves a message forward. Returns changed=false when the
// update is stale or a repeat. When the status changes to one that callers
// care about, an outbox event is written in the same transaction.
func (s *Store) AdvanceStatus(ctx context.Context, id, deviceID, status, errMsg string) (changed bool, err error) {
	rank, ok := statusRank[status]
	if !ok {
		return false, errors.New("unknown status " + status)
	}
	err = s.tx(ctx, func(tx *sql.Tx) error {
		res, err := tx.ExecContext(ctx, `UPDATE messages
			SET status = ?, status_rank = ?, error = COALESCE(NULLIF(?, ''), error), updated_at = ?
			WHERE id = ? AND device_id = ? AND status_rank < ?`,
			status, rank, errMsg, nowMs(), id, deviceID, rank)
		if err != nil {
			return err
		}
		n, _ := res.RowsAffected()
		if n == 0 {
			return nil
		}
		changed = true
		evType := map[string]string{
			StatusSent: "sms.sent", StatusDelivered: "sms.delivered",
			StatusFailed: "sms.failed", StatusUnknown: "sms.unknown",
		}[status]
		if evType == "" {
			return nil
		}
		m, err := scanMsg(tx.QueryRowContext(ctx, `SELECT `+msgCols+` FROM messages WHERE id = ?`, id))
		if err != nil {
			return err
		}
		return enqueueEvent(ctx, tx, evType, m)
	})
	return changed, err
}

// InsertInbound stores an SMS received by a phone. dup=true means this
// (device, dedupHash) was already stored and nothing was written.
func (s *Store) InsertInbound(ctx context.Context, deviceID, dedupHash, from, body string, receivedAt int64) (m Message, dup bool, err error) {
	err = s.tx(ctx, func(tx *sql.Tx) error {
		res, err := tx.ExecContext(ctx, `INSERT INTO inbound_seen (device_id, dedup_hash, received_at)
			VALUES (?, ?, ?) ON CONFLICT DO NOTHING`, deviceID, dedupHash, nowMs())
		if err != nil {
			return err
		}
		if n, _ := res.RowsAffected(); n == 0 {
			dup = true
			return nil
		}
		now := nowMs()
		if receivedAt == 0 {
			receivedAt = now
		}
		m = Message{ID: ids.New("msg"), DeviceID: deviceID, Direction: "in", PeerNumber: from, Body: body,
			Status: StatusReceived, CreatedAt: receivedAt, UpdatedAt: now}
		if _, err := tx.ExecContext(ctx, `INSERT INTO messages
			(id, device_id, direction, peer_number, body, status, status_rank, created_at, updated_at)
			VALUES (?, ?, 'in', ?, ?, 'received', 6, ?, ?)`,
			m.ID, deviceID, from, body, receivedAt, now); err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, `UPDATE inbound_seen SET message_id = ? WHERE device_id = ? AND dedup_hash = ?`,
			m.ID, deviceID, dedupHash); err != nil {
			return err
		}
		return enqueueEvent(ctx, tx, "sms.received", m)
	})
	return m, dup, err
}

// MarkInboundSeen records a dedup hash without creating a message (used for
// the pairing verification SMS, which agents never need to see).
func (s *Store) MarkInboundSeen(ctx context.Context, deviceID, dedupHash string) error {
	_, err := s.db.ExecContext(ctx, `INSERT INTO inbound_seen (device_id, dedup_hash, received_at)
		VALUES (?, ?, ?) ON CONFLICT DO NOTHING`, deviceID, dedupHash, nowMs())
	return err
}

type ListFilter struct {
	Peer      string
	Direction string
	Status    string
	Before    int64 // created_at < Before (ms); 0 = no bound
	After     int64 // created_at > After (ms); 0 = no bound
	Limit     int
}

func (s *Store) ListMessages(ctx context.Context, f ListFilter) ([]Message, error) {
	var where []string
	var args []any
	if f.Peer != "" {
		where, args = append(where, "peer_number = ?"), append(args, f.Peer)
	}
	if f.Direction != "" {
		where, args = append(where, "direction = ?"), append(args, f.Direction)
	}
	if f.Status != "" {
		where, args = append(where, "status = ?"), append(args, f.Status)
	}
	if f.Before > 0 {
		where, args = append(where, "created_at < ?"), append(args, f.Before)
	}
	if f.After > 0 {
		where, args = append(where, "created_at > ?"), append(args, f.After)
	}
	if f.Limit <= 0 || f.Limit > 200 {
		f.Limit = 50
	}
	q := `SELECT ` + msgCols + ` FROM messages`
	if len(where) > 0 {
		q += " WHERE " + strings.Join(where, " AND ")
	}
	q += " ORDER BY created_at DESC LIMIT ?"
	rows, err := s.db.QueryContext(ctx, q, append(args, f.Limit)...)
	if err != nil {
		return nil, err
	}
	return collect(rows)
}

type Conversation struct {
	Peer          string `json:"peer_number"`
	LastBody      string `json:"last_body"`
	LastDirection string `json:"last_direction"`
	LastAt        int64  `json:"last_at"`
	Count         int    `json:"message_count"`
}

func (s *Store) ListConversations(ctx context.Context, limit int) ([]Conversation, error) {
	if limit <= 0 || limit > 200 {
		limit = 50
	}
	rows, err := s.db.QueryContext(ctx, `
		SELECT m.peer_number, m.body, m.direction, m.created_at, c.n
		FROM (SELECT peer_number, MAX(created_at) AS last, COUNT(*) AS n FROM messages GROUP BY peer_number) c
		JOIN messages m ON m.peer_number = c.peer_number AND m.created_at = c.last
		GROUP BY m.peer_number
		ORDER BY m.created_at DESC LIMIT ?`, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Conversation
	for rows.Next() {
		var c Conversation
		if err := rows.Scan(&c.Peer, &c.LastBody, &c.LastDirection, &c.LastAt, &c.Count); err != nil {
			return nil, err
		}
		out = append(out, c)
	}
	return out, rows.Err()
}

func collect(rows *sql.Rows) ([]Message, error) {
	defer rows.Close()
	var out []Message
	for rows.Next() {
		m, err := scanMsg(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, m)
	}
	return out, rows.Err()
}

// enqueueEvent writes an outbox event plus one pending delivery per active
// webhook endpoint. Must run inside the transaction that made the change.
func enqueueEvent(ctx context.Context, tx *sql.Tx, typ string, data any) error {
	id := ids.New("evt")
	now := nowMs()
	payload, err := json.Marshal(map[string]any{"type": typ, "timestamp": now, "data": data})
	if err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO events_outbox (id, type, payload, created_at) VALUES (?, ?, ?, ?)`,
		id, typ, string(payload), now); err != nil {
		return err
	}
	_, err = tx.ExecContext(ctx, `INSERT INTO webhook_deliveries (event_id, endpoint_id, status, next_attempt_at)
		SELECT ?, id, 'pending', ? FROM webhook_endpoints WHERE disabled_at IS NULL`, id, now)
	return err
}
