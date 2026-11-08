# Architecture

```
 AI agents ──MCP (HTTP /mcp or stdio)──▶ ┌──────────── smsmcp (one Go binary) ────────────┐
                                         │ MCP tools → idempotency guard → message service │
 Webhook receivers ◀──signed POSTs────── │ webhook outbox worker        device hub (WSS)   │ ◀──WebSocket── Android gateway app
                                         │                SQLite (embedded, WAL)           │    (phone dials out; no open ports)
                                         └─────────────────────────────────────────────────┘          │
                                                                                                SmsManager / SMS_RECEIVED
                                                                                                       │
                                                                                                 mobile network
```

## Components

### Server (`server/`)

| Package | Responsibility |
|---|---|
| `cmd/smsmcp` | CLI entry: `init`, `serve`, `stdio`, `pair`, `devices`, `keys`, `webhooks` |
| `internal/store` | All SQL. Schema, migrations, and every idempotency constraint |
| `internal/hub` | Device WebSocket: auth, dispatch with leases, status/inbound handling, number verification |
| `internal/mcpserver` | MCP tool definitions and handlers |
| `internal/outbox` | Webhook delivery worker and Standard Webhooks signing |
| `internal/pairing` | Pairing links and `POST /v1/pair` |
| `internal/api` | HTTP routing and API key auth for `/mcp` |
| `internal/ids`, `phone`, `config` | IDs/secrets/hashes, phone number normalisation, settings |

Background loops started by `serve`:
- **hub.Run**: every 3 s (or on notify) pushes dispatchable messages to connected phones.
- **outbox.Run**: every 2 s delivers due webhooks.

`smsmcp stdio` only opens the database. It queues messages, and the running `serve` process's dispatcher picks them up.

### Android app (`client/`)

| File | Responsibility |
|---|---|
| `MainActivity.kt` | Setup UI, QR scan, pairing request |
| `GatewayService.kt` | Foreground service: WebSocket, sender loop, report flushing, housekeeping |
| `JobStore.kt` | Local SQLite: jobs + inbound, with the same forward-only state rules as the server |
| `Sms.kt` | SIM listing, `SmsSender`, sent/delivered receiver, inbound receiver, boot receiver |
| `Protocol.kt` | Pure logic: pairing link parsing, state ranks, dedup hash (unit-tested) |
| `Prefs.kt` | Pairing result and settings |

## Message lifecycle

```
queued → dispatched → accepted → sending → sent → delivered
            ↑   │                    │        └──→ failed
            └───┘ lease expired      └─→ unknown (crash/timeout; can still become sent)
```

| Status | Set by | Meaning |
|---|---|---|
| queued | server | Stored and waiting for a connected phone |
| dispatched | server | Pushed over WebSocket; re-pushed if not accepted within the lease (45 s) |
| accepted | phone | Phone stored it; the server never pushes it again |
| sending | phone | Persisted just before `SmsManager` was called |
| unknown | phone | No carrier result within 3 min; **not retried**, so the agent decides |
| sent | phone | Carrier accepted every part |
| delivered | phone | Delivery report for every part |
| failed | phone | Carrier or delivery failure |

Ranks: queued 0, dispatched 1, accepted 2, sending 3, unknown 4, sent 5, delivered/failed 6. Updates apply only when the rank goes up.

## Pairing and setup

1. `install.sh` installs the binary and service, then runs `smsmcp init`. That creates an API key, prints the agent config line, and shows a pairing QR.
2. The QR encodes `smsmcp://pair?url=<public URL>&token=<single-use token, 10 min>`.
3. The app scans it, the user picks a SIM and confirms its number, and the app calls `POST /v1/pair`.
4. The server creates the device as `pending_verification` and returns a device token and `smsmcp verify <code>`.
5. The phone texts that message to its own number. When it arrives on the same phone from that number, the hub activates the device. The verification SMS is not stored as a message.

## Idempotency summary

See the "Invariants" section of `AGENTS.md`. The full table with rationale is in `docs/DECISIONS.md` (ADR-003).

## Data model

See `server/internal/store/migrations.go`. Its tables are `api_keys`, `pairing_tokens`, `devices`, `messages`, `inbound_seen`, `webhook_endpoints`, `events_outbox` and `webhook_deliveries`.
