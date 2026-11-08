# Decisions

Append new entries. Don't rewrite old ones; supersede them with a new entry.

## ADR-001: One Go binary with embedded SQLite (2026-11-08)
One-command install matters more than horizontal scale. `modernc.org/sqlite` is pure Go, so `CGO_ENABLED=0` gives static binaries for every OS from one CI job. WAL mode plus one connection per process avoids `database is locked`. A second process (CLI, `stdio`) waits on `busy_timeout`.

## ADR-002: Phone dials out over WebSocket (2026-11-08)
Phones sit behind carrier NAT. The app opens the connection to the server, so the phone needs no public IP or port forwarding. No FCM dependency, and it works without Google Play Services.

## ADR-003: Idempotency at every hop (2026-11-08)
Transport between components is at-least-once, and every receiver dedupes on a stable key. The carrier handoff can't be made exactly-once, so the phone is **at most once** there: it persists `sending` before calling `SmsManager` and never auto-retries a job whose outcome is unknown. Texting someone twice is worse than reporting `unknown` and letting the agent decide.

| Hop | Key | Mechanism |
|---|---|---|
| agent → server | `(api_key_id, idempotency_key)` | UNIQUE; same key + different body = error |
| server → phone | `message_id` | lease/redispatch until `accepted`; phone PK dedup |
| phone → carrier | job state | persist-before-send; `sending` → `unknown`, no auto-retry |
| phone → server (inbound) | `(device, sha256(from, ts, body))` | UNIQUE in `inbound_seen` |
| status reports | `status_rank` | conditional UPDATE, forward only |
| server → webhooks | `event_id` | transactional outbox; `Webhook-Id` constant across retries |

## ADR-004: Auto idempotency key when the agent omits one (2026-11-08)
Agents often forget keys. Without one, `sha256(to, body, 10-minute bucket)` is used, so a tool-call retry within the window is a no-op. Trade-off: identical texts within 10 minutes need `allow_duplicate: true`. A send right at a bucket boundary can still be repeated, which is acceptable for a default.

## ADR-005: Idempotency key stored on `messages`, not a separate table (2026-11-08)
The early design had an `idempotency_keys` table. A UNIQUE index on `messages(api_key_id, idempotency_key)` gives the same guarantee with one less join. Revisit if keys need expiry independent of messages.

## ADR-006: Number verification by self-SMS (2026-11-08)
Android often can't read the SIM's own number. The user types it, and the phone texts a code to that number. Receiving the code on the same device from that number proves the number belongs to that SIM and tests send and receive in one step.

## ADR-007: SQLiteOpenHelper instead of Room on Android (2026-11-08)
Room needs KSP, which must match the Kotlin version exactly and adds build fragility. The phone schema is two small tables. Move to Room if it grows.

## ADR-008: Foreground service type `remoteMessaging` (2026-11-08)
It matches the use (relaying text messages between devices). It has no 6-hour cap like `dataSync` on Android 15, and it avoids `specialUse` justification text.

## ADR-009: Distribute the APK outside Google Play (2026-11-08)
Play only grants SMS permissions to default SMS handler apps. The APK ships from GitHub Releases.
