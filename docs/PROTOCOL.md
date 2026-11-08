# Protocols

All times are unix milliseconds unless stated. All bodies are JSON.

## 1. Pairing: `POST /v1/pair`

No auth header. The single-use token from the QR authorises the call.

```json
// request
{ "token": "smp_…", "device_name": "Pixel 8", "phone_number": "+255712345678", "sim_subscription_id": 2 }

// 200
{ "device_id": "dev_…", "device_token": "smd_…", "phone_number": "+255712345678",
  "verify_message": "smsmcp verify 482913", "ws_path": "/v1/device/ws" }

// 401 token used/expired, 400 bad number
{ "error": "pairing token is invalid, used, or expired; run `smsmcp pair` again" }
```

Pairing link format: `smsmcp://pair?url=<urlencoded server URL>&token=<token>`

## 2. Device WebSocket: `GET /v1/device/ws`

Header: `Authorization: Bearer smd_…`. One connection per device; a new one replaces the old. The server pings every 25 s.

Every frame is a JSON object with `type`.

### Server → phone

| type | fields | phone must |
|---|---|---|
| `hello` | `device_id`, `device_status` (`pending_verification`\|`active`) | (info) |
| `send` | `id`, `to`, `body` | store by `id` (ignore if known), then send `status` with its current state |
| `status_ack` | `id`, `status` | stop re-sending that status |
| `inbound_ack` | `dedup` | stop re-sending that inbound |

### Phone → server

| type | fields | server does |
|---|---|---|
| `status` | `id`, `status` (`accepted`\|`sending`\|`unknown`\|`sent`\|`delivered`\|`failed`), `error?` | applies the status only if it moves forward, **always** replies `status_ack` |
| `inbound` | `dedup`, `from`, `body`, `received_at` | stores once per `(device, dedup)`, **always** replies `inbound_ack` |
| `heartbeat` | `heartbeat: { battery?, signal?, app_version? }` | updates the device's last-seen time |

Delivery rules:
- The server re-pushes `send` until it receives `status: accepted` (or later) for that id. The lease is 45 s.
- The phone re-sends un-acked `status` and `inbound` frames on every reconnect and every local change.
- `dedup` = hex SHA-256 of `from + 0x1F + pdu_timestamp + 0x1F + body`.

## 3. MCP: `/mcp`

Streamable HTTP. Header: `Authorization: Bearer smk_…` (create one with `smsmcp keys create <name>`). Stdio: `smsmcp stdio`.

| tool | input | output |
|---|---|---|
| `send_sms` | `to`, `body`, `idempotency_key?`, `allow_duplicate?`, `device_id?` | `{ message, duplicate }` |
| `get_message` | `message_id` | message |
| `list_messages` | `number?`, `direction?`, `status?`, `before?` (RFC3339), `limit?` | `{ messages }` |
| `list_conversations` | `limit?` | `{ conversations }` |
| `wait_for_reply` | `from`, `since?` (RFC3339), `timeout_seconds?` (≤600) | `{ message?, timed_out }` |
| `list_devices` | none | `{ devices }` |

Idempotency for `send_sms`: keys are scoped per API key. Without `idempotency_key`, the server derives one from `(to, body, 10-minute bucket)`. With `allow_duplicate: true` every call sends.

## 4. Webhooks

Register a receiver with `smsmcp webhooks add <url>`. It prints a `whsec_…` secret.

```
POST <url>
Content-Type: application/json
Webhook-Id: evt_…            # same value on every retry, so dedupe on it
Webhook-Timestamp: 1731090000 # unix seconds
Webhook-Signature: v1,<base64 HMAC-SHA256(key, "{id}.{timestamp}.{body}")>
```

The key is the base64-decoded part of the secret after `whsec_` (Standard Webhooks spec, so their verifier libraries work).

```json
{ "type": "sms.received", "timestamp": 1731090000123, "data": { /* message */ } }
```

Event types: `sms.received`, `sms.sent`, `sms.delivered`, `sms.failed`, `sms.unknown`.

Any 2xx counts as delivered. Otherwise the server retries after 5 s, 30 s, 2 m, 10 m, 30 m, 1 h, 3 h and 6 h, then marks the delivery dead.
