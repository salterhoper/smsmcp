# smsmcp

Give AI agents SMS through your own Android phone.

smsmcp is an [MCP](https://modelcontextprotocol.io) server. Agents call tools like `send_sms` and `wait_for_reply`, and a phone you pair sends and receives the messages on a SIM you choose. Everything runs on your machine: one Go binary with an embedded SQLite database.

## Install

```sh
curl -fsSL https://raw.githubusercontent.com/salterhoper/smsmcp/main/install.sh | sh
```

This installs `smsmcp` and starts it as a background service. It then prints:
- an **API key** and the command to connect your agent
- a **QR code** to pair your phone

If the phone reaches this machine on a different address, set it first:

```sh
curl -fsSL https://raw.githubusercontent.com/salterhoper/smsmcp/main/install.sh | SMSMCP_PUBLIC_URL=https://sms.example.com sh
```

With Docker:

```sh
docker build -t smsmcp https://github.com/salterhoper/smsmcp.git
docker run -d --name smsmcp -p 8080:8080 -v smsmcp:/data -e SMSMCP_PUBLIC_URL=http://192.168.1.10:8080 smsmcp
docker exec -it smsmcp smsmcp init
```

## Pair your phone

1. Install the gateway app: [smsmcp-gateway.apk](https://github.com/salterhoper/smsmcp/releases/latest/download/smsmcp-gateway.apk)
2. Open it, grant SMS permissions, and tap **Scan pairing QR**.
3. Pick the SIM agents should use, check its number, and tap **Pair this phone**.
4. The phone texts a code to itself to prove it owns that number. When that arrives, the phone is active.

Need a new code? Run `smsmcp pair`.

## Connect an agent

```sh
claude mcp add --transport http smsmcp http://<your-host>:8080/mcp --header "Authorization: Bearer smk_…"
```

Or run it locally over stdio: `smsmcp stdio` (the `smsmcp serve` service must also be running).

## Tools

| Tool | What it does |
|---|---|
| `send_sms` | Send a text. Safe to retry: the same `idempotency_key` never sends twice |
| `get_message` | Status of one message (queued → sent → delivered) |
| `list_messages` | Search messages by number, direction or status |
| `list_conversations` | Threads grouped by number |
| `wait_for_reply` | Wait for the next SMS from a number, e.g. a reply or a one-time code |
| `list_devices` | Paired phones, battery and last seen |

## Webhooks

```sh
smsmcp webhooks add https://example.com/sms-hook
```

Events (`sms.received`, `sms.delivered`, …) are signed with the [Standard Webhooks](https://www.standardwebhooks.com) scheme. Retries keep the same `Webhook-Id`, so you can drop repeats. See [docs/PROTOCOL.md](docs/PROTOCOL.md).

## Limits to know

- Android pauses apps after about 30 SMS in 30 minutes. To raise the limit: `adb shell settings put global sms_outgoing_check_max_count 1000`
- Turn off battery optimisation for the app (there's a button for it) so it stays connected.
- Your carrier's fair-use rules still apply.

## Development

See [AGENTS.md](AGENTS.md) for setup, commands and the rules that keep sends duplicate-free. Progress is tracked in [docs/STATUS.md](docs/STATUS.md).
