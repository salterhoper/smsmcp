# AGENTS.md

Start here if you are a coding agent (or a human) picking up this project.

## What this is

smsmcp gives AI agents SMS through an Android phone. Three parts:

| Part | Path | Stack |
|---|---|---|
| Server: MCP endpoint, phone gateway, webhooks, CLI | `server/` | Go 1.26, embedded SQLite (`modernc.org/sqlite`, no CGO) |
| Gateway app on the phone | `client/` | Kotlin, Android (minSdk 26), OkHttp WebSocket, SQLiteOpenHelper |
| Installer | `install.sh`, `Dockerfile` | POSIX sh, systemd / launchd |

## Before you change anything

1. Read **`docs/STATUS.md`**. It says what's done, what's in progress and what to do next. Pick up from "Next up".
2. Skim `docs/ARCHITECTURE.md` (how the parts fit) and `docs/PROTOCOL.md` (wire formats).
3. Check `docs/DECISIONS.md` before reversing a design choice.

## Before you finish a session

- Run the checks below. All must pass.
- Update **`docs/STATUS.md`**: move finished items, add new ones, and add a dated line to the session log saying what you did and where you stopped. The next agent depends on this.
- Record any new design choice in `docs/DECISIONS.md`.
- If you changed a wire format, update `docs/PROTOCOL.md` in the same commit.

## Commands

```sh
# Server
cd server
go vet ./... && go test ./...
go run ./cmd/smsmcp init  -data /tmp/smsmcp-dev   # API key + pairing QR
go run ./cmd/smsmcp serve -data /tmp/smsmcp-dev   # :8080

# Android (needs a full JDK 17+ with javac; the system Java 25 here is a JRE)
cd client
JAVA_HOME=/opt/android-studio/jbr ./gradlew assembleDebug testDebugUnitTest
adb install -r app/build/outputs/apk/debug/app-debug.apk
```

`client/local.properties` (`sdk.dir=...`) is machine-specific and git-ignored, and so is `client/keystore.properties` (the release signing key's passwords). **Never commit the keystore or its passwords, and never generate a new release key.** See `docs/RELEASING.md`. Releases: push a `vX.Y.Z` tag.

## Invariants you must not break

The core promise is **no SMS is sent twice and no event is reported twice**. These rules enforce it:

1. **Agent → server**: `messages(api_key_id, idempotency_key)` is UNIQUE. A repeat returns the original message. The same key with a different to/body is an error, never a silent new send.
2. **Server → phone**: jobs are re-pushed until the phone reports `accepted`. The phone stores jobs by `message_id` PRIMARY KEY and re-reports instead of re-sending.
3. **Phone → carrier**: the phone persists `sending` **before** calling `SmsManager`. A job stuck in `sending` becomes `unknown` and is **never retried automatically**.
4. **Status updates only move forward** (`status_rank` on the server, `JobState.canAdvance` on the phone). Repeated or late reports are acked but ignored.
5. **Inbound SMS** dedup on `(device_id, sha256(from, pdu_timestamp, body))`.
6. **Webhooks** use a transactional outbox: the event row is written in the same transaction as the change. Retries reuse the event id as `Webhook-Id`.

Each rule has a test in `server/internal/store/store_test.go` or `server/internal/hub/hub_test.go`. Add a test when you add a path that sends or reports.

## Conventions

- Go: standard library first. Errors are returned and wrapped, never panicked (except crypto/rand). Logs use `log/slog`.
- SQL lives in `server/internal/store` only. Migrations are append-only (`migrations.go`).
- Times are unix milliseconds in the database and on the wire.
- Kotlin: pure logic goes in `Protocol.kt` so it can be JVM unit-tested; Android-specific code goes elsewhere.
- User-facing text says what happened and what to do next.

## Git

- Remote: `https://github.com/salterhoper/smsmcp.git`, branch `main`.
- Commit messages: imperative, short subject, body explains why.
- **No AI attribution anywhere**: no `Co-Authored-By` trailers for AI tools, no "generated with" lines, no tool names or tags in commits, PR titles, PR bodies or code comments.
