package store

// migrations are applied in order; PRAGMA user_version records how many ran.
// Never edit a shipped migration: append a new one.
var migrations = []string{
	// 1: initial schema. All timestamps are unix milliseconds.
	`
CREATE TABLE api_keys (
  id         TEXT PRIMARY KEY,
  name       TEXT NOT NULL,
  prefix     TEXT NOT NULL,
  hash       TEXT NOT NULL UNIQUE,
  created_at INTEGER NOT NULL,
  revoked_at INTEGER
);

CREATE TABLE pairing_tokens (
  hash       TEXT PRIMARY KEY,
  expires_at INTEGER NOT NULL,
  used_at    INTEGER
);

CREATE TABLE devices (
  id                  TEXT PRIMARY KEY,
  name                TEXT NOT NULL,
  phone_number        TEXT NOT NULL,
  sim_subscription_id INTEGER NOT NULL,
  token_hash          TEXT NOT NULL UNIQUE,
  status              TEXT NOT NULL,          -- pending_verification | active | disabled
  verify_code         TEXT,
  battery             INTEGER,
  signal              INTEGER,
  app_version         TEXT,
  last_seen_at        INTEGER,
  created_at          INTEGER NOT NULL
);

CREATE TABLE messages (
  id              TEXT PRIMARY KEY,
  device_id       TEXT REFERENCES devices(id),
  direction       TEXT NOT NULL,              -- out | in
  peer_number     TEXT NOT NULL,
  body            TEXT NOT NULL,
  status          TEXT NOT NULL,
  status_rank     INTEGER NOT NULL,           -- see statusRank; only ever increases
  api_key_id      TEXT,
  idempotency_key TEXT,
  request_hash    TEXT,
  error           TEXT,
  lease_until     INTEGER,
  attempts        INTEGER NOT NULL DEFAULT 0,
  created_at      INTEGER NOT NULL,
  updated_at      INTEGER NOT NULL
);
-- Idempotency hop 1 (agent -> server).
CREATE UNIQUE INDEX messages_idem ON messages(api_key_id, idempotency_key);
CREATE INDEX messages_peer ON messages(peer_number, created_at);
CREATE INDEX messages_dispatch ON messages(device_id, status, lease_until);

-- Idempotency hop 4 (phone -> server, inbound SMS).
CREATE TABLE inbound_seen (
  device_id   TEXT NOT NULL,
  dedup_hash  TEXT NOT NULL,
  message_id  TEXT,
  received_at INTEGER NOT NULL,
  PRIMARY KEY (device_id, dedup_hash)
);

CREATE TABLE webhook_endpoints (
  id          TEXT PRIMARY KEY,
  url         TEXT NOT NULL,
  secret      TEXT NOT NULL,
  created_at  INTEGER NOT NULL,
  disabled_at INTEGER
);

-- Transactional outbox: rows are written in the same transaction as the
-- state change they describe. event id == Webhook-Id header.
CREATE TABLE events_outbox (
  id         TEXT PRIMARY KEY,
  type       TEXT NOT NULL,
  payload    TEXT NOT NULL,
  created_at INTEGER NOT NULL
);

CREATE TABLE webhook_deliveries (
  event_id         TEXT NOT NULL REFERENCES events_outbox(id),
  endpoint_id      TEXT NOT NULL REFERENCES webhook_endpoints(id),
  status           TEXT NOT NULL,             -- pending | delivered | dead
  attempts         INTEGER NOT NULL DEFAULT 0,
  next_attempt_at  INTEGER NOT NULL,
  last_status_code INTEGER,
  last_error       TEXT,
  PRIMARY KEY (event_id, endpoint_id)
);
CREATE INDEX webhook_due ON webhook_deliveries(status, next_attempt_at);
`,
}
