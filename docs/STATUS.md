# Project status

Last updated: 2026-11-08

## Where things stand

The scaffold is complete and builds. The server runs end to end against a simulated phone in tests. The Android app compiles and its unit tests pass, but **it has not yet been run on a real phone**.

## Done

### Server (`server/`)
- [x] SQLite store with WAL, migrations, single-connection writer
- [x] Outbound messages with idempotency keys (explicit, auto 10-minute window, `allow_duplicate`)
- [x] Message state machine with monotonic `status_rank`
- [x] Device hub: WebSocket per phone, lease + redispatch, status/inbound acks, keepalive pings
- [x] Pairing: single-use QR token (10 min), `POST /v1/pair`, number verification by self-SMS
- [x] Inbound SMS dedup, verification SMS hidden from agents
- [x] Transactional outbox + webhook worker (Standard Webhooks headers/signature, backoff, dead-lettering)
- [x] MCP tools: `send_sms`, `get_message`, `list_messages`, `list_conversations`, `wait_for_reply`, `list_devices`
- [x] MCP over Streamable HTTP (`/mcp`, API key bearer auth) and stdio (`smsmcp stdio`)
- [x] CLI: `init`, `serve`, `stdio`, `pair` (terminal QR), `devices`, `keys`, `webhooks`, `version`
- [x] Tests: store invariants, end-to-end hub test with a fake phone, MCP auth, webhook signature vector

### Android (`client/`)
- [x] Setup screen: scan QR / paste link, SIM picker, number, pair, start/stop, battery exemption
- [x] `smsmcp://pair` deep link
- [x] Foreground service (`remoteMessaging` type) with WebSocket reconnect + backoff
- [x] Local job store (dedup by message_id), persist-before-send, `sending` → `unknown` expiry
- [x] Multipart send with per-part sent/delivered tracking
- [x] SMS receiver filtered to the chosen SIM, multipart join, dedup hash
- [x] Boot receiver restarts the gateway
- [x] JVM unit tests for link parsing, state ranks, dedup hash

### Repo
- [x] `install.sh` (release binary or `go install` fallback, systemd user unit / launchd)
- [x] `Dockerfile` (distroless, `/data` volume)
- [x] CI (`go vet`, `go test`, Android build + unit tests) and tag-triggered release workflow

## Next up (in priority order)

1. **Run on a real phone.** Install the debug APK, pair against `smsmcp serve` on the LAN, and confirm verification, send, delivery report and inbound. Fix whatever breaks. Watch logcat with `adb logcat -s smsmcp`.
2. **Verify the dual-SIM extras.** Check that `SmsReceiver` reads the subscription id correctly on the test phone (`EXTRA_SUBSCRIPTION_INDEX` vs `"subscription"`).
3. **Server-side rate limit** per device (e.g. N per minute, configurable) so a runaway agent can't get the SIM flagged.
4. **REST API** `POST /v1/messages` with an `Idempotency-Key` header, for non-MCP callers. Reuse `CreateOutboundMessage`.
5. **Release signing** for the APK (keystore in GitHub secrets). See TODO in `.github/workflows/release.yml`.
6. **TLS guidance**: document running behind Caddy or a Cloudflare Tunnel, and pin the server certificate fingerprint in the pairing link.
7. **Admin views**: `smsmcp messages` CLI and/or a small web status page.
8. Prune old `inbound_seen` and `events_outbox` rows on the server.
9. Optional MCP resource `sms://conversations/{number}`.

## Known gaps and risks

- `JobStore.expireSending` marks jobs `unknown` after 3 minutes with no carrier result. A slow carrier can cause false `unknown`s, but a late `sent` still resolves them.
- If the phone app's data is wiped after it accepted a job, the server keeps that job in `accepted` forever. A future "reconcile" frame should fix this.
- The pairing link has no TLS fingerprint yet, so use `https://` public URLs outside a trusted LAN.
- `wait_for_reply` polls SQLite once a second. That's fine at this scale, but switch to an in-process notifier if it becomes hot.
- Gradle picks up the system JDK 25, which has no `javac`. Build with `JAVA_HOME=/opt/android-studio/jbr` locally.

## Session log

- **2026-11-08**: Initial scaffold of server, Android app, installer, CI and docs. All server tests pass, and the APK builds. Stopped before on-device testing (item 1 above).
