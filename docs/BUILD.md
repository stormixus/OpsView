# OpsView - Build & Run Guide

## Prerequisites

- Go 1.21+
- (Viewer only) Wails v2 CLI: `go install github.com/wailsapp/wails/v2/cmd/wails@latest`
- (Agent on Windows) Windows 10+ with Desktop Duplication API support

## Project Structure

```
opsview/
  proto/        # OVP protocol (shared Go package)
  relay/        # opsview-relay server
  agent/        # opsview-agent (screen capture)
  viewer/       # Wails desktop viewer
  web/          # Web viewer (static HTML/JS)
  docs/         # Documentation
```

## Build

### Relay Server

```bash
cd relay
go build -o opsview-relay .
```

### Agent

```bash
cd agent
# On macOS/Linux (dummy capturer for testing):
go build -o opsview-agent .

# On Windows (DXGI capture):
GOOS=windows GOARCH=amd64 go build -o opsview-agent.exe .
```

### Wails Desktop Viewer

```bash
./scripts/generate_icons.sh   # tray.ico + viewer/appicon.png (from icon-*.svg)
make viewer
# Or: cd viewer && mkdir -p build && cp appicon.png build/ && wails build
# Output: build/bin/opsview-viewer
```

App icons are hand-edited SVGs at the repo root (`icon-dark.svg`, `icon-light.svg`, `icon-*-apple.svg`). Re-run the script after changing them.

### Web Viewer

No build needed. Static files served by relay or any HTTP server.

## Run (Development)

### 1. Start Relay

```bash
cd relay
./opsview-relay
# Listens on :8080 by default
# Also serves web viewer from ../web/
```

### 2. Start Agent

```bash
cd agent
./opsview-agent
# Connects to ws://127.0.0.1:8080/publish by default
```

### 3. Open Web Viewer

Open browser to `http://127.0.0.1:8080/` and click Connect.

### 4. Or use Desktop Viewer

```bash
cd viewer
wails dev
```

## Environment Variables

### Relay

| Variable | Default | Description |
|----------|---------|-------------|
| `RELAY_PORT` | `8080` | Listen port |
| `RELAY_PUBLISHER_TOKEN` | `dev-publisher-token` | Publisher auth token |
| `RELAY_WATCHER_TOKENS` | `dev-watcher-token` | Comma-separated watcher tokens |
| `RELAY_WEB_DIR` | `../web` | Path to web viewer static files |

### Agent

| Variable | Default | Description |
|----------|---------|-------------|
| `AGENT_RELAY_URL` | `ws://127.0.0.1:8080/publish` | Relay WebSocket URL |
| `AGENT_TOKEN` | `dev-publisher-token` | Publisher auth token |
| `AGENT_PROFILE` | `1080` | Resolution profile (1080 or 720) |
| `AGENT_FPS_MIN` | `5` | Minimum FPS |
| `AGENT_FPS_MAX` | `10` | Maximum FPS |
| `AGENT_TILE_SIZE` | `128` | Tile size in pixels |

### Viewer / Web

| Variable | Default | Description |
|----------|---------|-------------|
| `WATCH_URL` | `ws://127.0.0.1:8080/watch` | Relay watch URL |
| `WATCH_TOKEN` | `dev-watcher-token` | Watcher auth token |

## Network Modes

### LAN Mode

Default configuration. All components connect via internal IP.

```
Agent → ws://relay.lan:8080/publish
Viewer → ws://relay.lan:8080/watch
```

### Public Mode

For remote access over the internet:

1. Set up TLS termination (nginx, caddy, etc.) in front of relay
2. Use `wss://` URLs
3. Only port 443 needs to be open

```
Agent → wss://opsview.example.com/publish
Viewer → wss://opsview.example.com/watch
```

Example nginx config:
```nginx
server {
    listen 443 ssl;
    server_name opsview.example.com;

    ssl_certificate /path/to/cert.pem;
    ssl_certificate_key /path/to/key.pem;

    location / {
        proxy_pass http://127.0.0.1:8080;
        proxy_http_version 1.1;
        proxy_set_header Upgrade $http_upgrade;
        proxy_set_header Connection "upgrade";
        proxy_set_header Host $host;
        proxy_read_timeout 3600s;
    }
}
```

## Windows Service Installation

```batch
cd agent\scripts
install-service.bat
net start opsview-agent
```

To remove:
```batch
uninstall-service.bat
```

## Update Signing (viewer auto-updater)

The viewer auto-updater is **fail-closed**: it downloads an installer only over
HTTPS from a pinned GitHub host, then verifies an **Ed25519 signature** over the
installer's SHA-256 digest before executing it. With no key configured, the
updater refuses to install anything.

**One-time setup:**

1. Generate a keypair:
   ```bash
   go run ./viewer/cmd/updatesign gen
   ```
   It prints a base64 **public key** (32 bytes) and **private key** (64 bytes).

2. Embed the public key: paste it into `updatePublicKeyB64` in
   `viewer/updater.go`, or inject it at build time:
   ```bash
   wails build -ldflags "-X main.updatePublicKeyB64=<PUBLIC_KEY_B64>"
   ```

3. Store the private key as the **`ED25519_UPDATE_PRIVATE_KEY`** GitHub Actions
   secret (Settings → Secrets and variables → Actions). Keep it secret — anyone
   with it can sign malicious updates. Prefer an offline-generated key.

On every tagged release, CI signs each viewer asset (`*.dmg`, `*-setup.exe`,
`*.tar.gz`) and uploads a detached `<asset>.sig`. The updater fetches
`<download_url>.sig` and verifies it before installing. If the secret is unset,
the release is unsigned and clients will not auto-update (by design).

To sign assets manually:
```bash
ED25519_UPDATE_PRIVATE_KEY=<PRIVATE_KEY_B64> \
  go run ./viewer/cmd/updatesign sign path/to/asset.dmg
```
