#!/bin/sh
# smsmcp installer.
#   curl -fsSL https://raw.githubusercontent.com/salterhoper/smsmcp/main/install.sh | sh
#
# Installs the smsmcp binary, runs first-time setup (API key + pairing QR),
# and registers a background service (systemd user unit on Linux, launchd on macOS).
#
# Env overrides:
#   SMSMCP_VERSION     release tag to install (default: latest)
#   SMSMCP_BIN_DIR     where to put the binary (default: ~/.local/bin)
#   SMSMCP_PUBLIC_URL  URL the phone uses to reach this machine
#   SMSMCP_NO_SERVICE  set to 1 to skip service registration
set -eu

REPO="salterhoper/smsmcp"
BIN_DIR="${SMSMCP_BIN_DIR:-$HOME/.local/bin}"
VERSION="${SMSMCP_VERSION:-latest}"

say() { printf '\033[1m==>\033[0m %s\n' "$*"; }
die() { printf 'error: %s\n' "$*" >&2; exit 1; }

os=$(uname -s | tr '[:upper:]' '[:lower:]')
case "$os" in linux|darwin) ;; *) die "unsupported OS: $os (use Docker instead)";; esac
arch=$(uname -m)
case "$arch" in
  x86_64|amd64) arch=amd64 ;;
  aarch64|arm64) arch=arm64 ;;
  *) die "unsupported CPU: $arch" ;;
esac

mkdir -p "$BIN_DIR"
asset="smsmcp_${os}_${arch}"
if [ "$VERSION" = "latest" ]; then
  url="https://github.com/$REPO/releases/latest/download/$asset"
else
  url="https://github.com/$REPO/releases/download/$VERSION/$asset"
fi

say "Downloading $asset ($VERSION)"
tmp=$(mktemp)
if curl -fsSL "$url" -o "$tmp"; then
  install -m 0755 "$tmp" "$BIN_DIR/smsmcp"
  rm -f "$tmp"
elif command -v go >/dev/null 2>&1; then
  rm -f "$tmp"
  say "No release binary found; building from source with Go"
  GOBIN="$BIN_DIR" go install "github.com/$REPO/server/cmd/smsmcp@latest"
else
  rm -f "$tmp"
  die "could not download $url and Go is not installed"
fi

case ":$PATH:" in *":$BIN_DIR:"*) ;; *) say "Add $BIN_DIR to your PATH to run smsmcp directly";; esac
SMSMCP="$BIN_DIR/smsmcp"

if [ "${SMSMCP_NO_SERVICE:-0}" != "1" ]; then
  if [ "$os" = "linux" ] && command -v systemctl >/dev/null 2>&1; then
    say "Registering systemd user service"
    unit_dir="$HOME/.config/systemd/user"
    mkdir -p "$unit_dir"
    cat > "$unit_dir/smsmcp.service" <<UNIT
[Unit]
Description=smsmcp SMS gateway for AI agents
After=network-online.target

[Service]
ExecStart=$SMSMCP serve
Environment=SMSMCP_PUBLIC_URL=${SMSMCP_PUBLIC_URL:-}
Restart=on-failure
RestartSec=3

[Install]
WantedBy=default.target
UNIT
    systemctl --user daemon-reload
    systemctl --user enable --now smsmcp.service
    # Keep running after logout.
    loginctl enable-linger "$(id -un)" 2>/dev/null || true
  elif [ "$os" = "darwin" ]; then
    say "Registering launchd agent"
    plist="$HOME/Library/LaunchAgents/dev.smsmcp.plist"
    mkdir -p "$(dirname "$plist")"
    cat > "$plist" <<PLIST
<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0"><dict>
  <key>Label</key><string>dev.smsmcp</string>
  <key>ProgramArguments</key><array><string>$SMSMCP</string><string>serve</string></array>
  <key>EnvironmentVariables</key><dict><key>SMSMCP_PUBLIC_URL</key><string>${SMSMCP_PUBLIC_URL:-}</string></dict>
  <key>RunAtLoad</key><true/>
  <key>KeepAlive</key><true/>
</dict></plist>
PLIST
    launchctl unload "$plist" 2>/dev/null || true
    launchctl load "$plist"
  else
    say "No service manager found; start the server with: $SMSMCP serve"
  fi
fi

say "First-time setup"
"$SMSMCP" init
cat <<DONE

Next:
  1. Install the gateway app on your Android phone:
     https://github.com/$REPO/releases/latest/download/smsmcp-gateway.apk
  2. Scan the QR code above and pick the SIM number agents should use.
  3. Add the MCP server to your agent with the command printed above.
DONE
