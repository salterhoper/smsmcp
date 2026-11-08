package store

import (
	"context"
	"database/sql"
	"errors"
	"time"

	"github.com/salterhoper/smsmcp/server/internal/ids"
)

type APIKey struct {
	ID        string `json:"id"`
	Name      string `json:"name"`
	Prefix    string `json:"prefix"`
	CreatedAt int64  `json:"created_at"`
	RevokedAt int64  `json:"revoked_at,omitempty"`
}

// CreateAPIKey stores a new key and returns its record plus the plaintext,
// which is shown once and never stored.
func (s *Store) CreateAPIKey(ctx context.Context, name string) (APIKey, string, error) {
	secret := ids.Secret("smk", 32)
	k := APIKey{ID: ids.New("key"), Name: name, Prefix: secret[:12], CreatedAt: nowMs()}
	_, err := s.db.ExecContext(ctx, `INSERT INTO api_keys (id, name, prefix, hash, created_at) VALUES (?, ?, ?, ?, ?)`,
		k.ID, k.Name, k.Prefix, ids.Hash(secret), k.CreatedAt)
	return k, secret, err
}

// APIKeyBySecret returns the active key matching a plaintext secret.
func (s *Store) APIKeyBySecret(ctx context.Context, secret string) (APIKey, error) {
	var k APIKey
	err := s.db.QueryRowContext(ctx, `SELECT id, name, prefix, created_at FROM api_keys
		WHERE hash = ? AND revoked_at IS NULL`, ids.Hash(secret)).Scan(&k.ID, &k.Name, &k.Prefix, &k.CreatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return k, ErrNotFound
	}
	return k, err
}

func (s *Store) ListAPIKeys(ctx context.Context) ([]APIKey, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT id, name, prefix, created_at, COALESCE(revoked_at,0) FROM api_keys ORDER BY created_at`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []APIKey
	for rows.Next() {
		var k APIKey
		if err := rows.Scan(&k.ID, &k.Name, &k.Prefix, &k.CreatedAt, &k.RevokedAt); err != nil {
			return nil, err
		}
		out = append(out, k)
	}
	return out, rows.Err()
}

func (s *Store) RevokeAPIKey(ctx context.Context, id string) error {
	res, err := s.db.ExecContext(ctx, `UPDATE api_keys SET revoked_at = ? WHERE id = ? AND revoked_at IS NULL`, nowMs(), id)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	return nil
}

type WebhookEndpoint struct {
	ID        string `json:"id"`
	URL       string `json:"url"`
	Secret    string `json:"-"`
	CreatedAt int64  `json:"created_at"`
}

// AddWebhookEndpoint registers a receiver. The secret follows the Standard
// Webhooks format ("whsec_" + base64 key).
func (s *Store) AddWebhookEndpoint(ctx context.Context, url string) (WebhookEndpoint, error) {
	e := WebhookEndpoint{ID: ids.New("whe"), URL: url, Secret: ids.WebhookSecret(), CreatedAt: nowMs()}
	_, err := s.db.ExecContext(ctx, `INSERT INTO webhook_endpoints (id, url, secret, created_at) VALUES (?, ?, ?, ?)`,
		e.ID, e.URL, e.Secret, e.CreatedAt)
	return e, err
}

func (s *Store) ListWebhookEndpoints(ctx context.Context) ([]WebhookEndpoint, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT id, url, secret, created_at FROM webhook_endpoints WHERE disabled_at IS NULL ORDER BY created_at`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []WebhookEndpoint
	for rows.Next() {
		var e WebhookEndpoint
		if err := rows.Scan(&e.ID, &e.URL, &e.Secret, &e.CreatedAt); err != nil {
			return nil, err
		}
		out = append(out, e)
	}
	return out, rows.Err()
}

type DueDelivery struct {
	EventID    string
	EndpointID string
	URL        string
	Secret     string
	Payload    string
	Attempts   int
}

func (s *Store) DueDeliveries(ctx context.Context, limit int) ([]DueDelivery, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT d.event_id, d.endpoint_id, e.url, e.secret, o.payload, d.attempts
		FROM webhook_deliveries d
		JOIN webhook_endpoints e ON e.id = d.endpoint_id AND e.disabled_at IS NULL
		JOIN events_outbox o ON o.id = d.event_id
		WHERE d.status = 'pending' AND d.next_attempt_at <= ?
		ORDER BY d.next_attempt_at LIMIT ?`, nowMs(), limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []DueDelivery
	for rows.Next() {
		var d DueDelivery
		if err := rows.Scan(&d.EventID, &d.EndpointID, &d.URL, &d.Secret, &d.Payload, &d.Attempts); err != nil {
			return nil, err
		}
		out = append(out, d)
	}
	return out, rows.Err()
}

// RecordDelivery stores the outcome of one attempt. next == 0 means give up.
func (s *Store) RecordDelivery(ctx context.Context, eventID, endpointID string, ok bool, code int, errMsg string, next time.Duration) error {
	status := "pending"
	switch {
	case ok:
		status = "delivered"
	case next == 0:
		status = "dead"
	}
	_, err := s.db.ExecContext(ctx, `UPDATE webhook_deliveries
		SET status = ?, attempts = attempts + 1, next_attempt_at = ?, last_status_code = ?, last_error = ?
		WHERE event_id = ? AND endpoint_id = ?`,
		status, time.Now().Add(next).UnixMilli(), code, nullStr(errMsg), eventID, endpointID)
	return err
}
