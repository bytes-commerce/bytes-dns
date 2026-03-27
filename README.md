# bytes-dns

**Your own DynDNS via Hetzner, for free.**

`bytes-dns` keeps a [Hetzner DNS](https://hetzner.cloud/?ref=OTYBkaZD9yHx)-managed record in sync with the current public IP of your server or home connection. It runs as a `systemd` timer, updates only when the IP changes, and requires no external runtime — it compiles to a single static binary.

---

## Why

Hetzner offers free managed DNS. If you have a dynamic public IP (home router, cheap VPS with unstable IP, etc.) and a domain managed by Hetzner, `bytes-dns` replaces a paid DynDNS service at zero cost.

### API Note

`bytes-dns` uses the **Hetzner Cloud DNS API** (`dns.hetzner.com/api/v1`) — not the deprecated _DNS Console API_. Hetzner scheduled the old API for shutdown in May 2026; brownouts for write operations began in early 2026. This project targets only the current, supported API.

---

## Features

- **Automatic Zone Discovery**: Resolves the correct DNS zone by record name (longest suffix match).
- **Interactive Setup**: `bytes-dns setup` guides you through configuration and can even create missing zones.
- **Smart Updates**: Skips unnecessary API calls when IP is unchanged (local state cache).
- **Hardened Security**: systemd units use strict sandboxing; config permissions are enforced.
- **Dry Run & Test**: Preview changes with `--dry-run` or validate everything with `bytes-dns test`.
- **IPv4 & IPv6**: Supports `A` and `AAAA` records.
- **CI/CD Ready**: Automated testing and linting via GitHub Actions.

---

## Project Structure

```
bytes-dns/
├── cmd/bytes-dns/         # CLI entry point
├── internal/
│   ├── config/            # Config loading and validation
│   ├── dns/               # Hetzner Cloud DNS API client
│   ├── ip/                # Public IP detection
│   ├── logger/            # Minimal structured logger
│   ├── state/             # Local state file management
│   └── updater/           # Core update orchestration
├── systemd/
│   ├── bytes-dns.service  # systemd oneshot service template
│   └── bytes-dns.timer    # systemd timer template
├── examples/
│   └── config.json        # Annotated configuration example
├── install.sh             # System installer (systemd integration)
├── uninstall.sh           # Clean removal
├── Makefile               # Build, lint, release targets
└── .github/workflows/     # CI/CD and release pipelines
```

---

## Requirements

| Requirement | Version |
|-------------|---------|
| Linux       | Any modern distro with systemd |
| Go          | >= 1.22 (for building from source) |
| Root        | Required for `install.sh` |

No runtime dependencies. The binary is statically linked.

---

## Quick Install

### From a GitHub release (recommended)

```bash
# Replace vX.Y.Z and ARCH with actual values (e.g. v1.1.0, linux-amd64)
VERSION=v1.1.0
ARCH=linux-amd64

curl -Lo /usr/local/bin/bytes-dns \
  "https://github.com/bytes-commerce/bytes-dns/releases/download/${VERSION}/bytes-dns-${ARCH}"
chmod +x /usr/local/bin/bytes-dns

# Verify
bytes-dns version
```

Then follow the [Configuration](#configuration) and [systemd Setup](#systemd-setup) sections.

### From source

```bash
git clone https://github.com/bytes-commerce/bytes-dns
cd bytes-dns

# Build and install binary + systemd units in one step:
sudo bash install.sh
```

`install.sh` will:
1. Build the binary from source (`go build`)
2. Install it to `/usr/local/bin/bytes-dns`
3. Install systemd service and timer unit templates
4. Enable and start the timer for the current user
5. Launch an interactive setup if no config exists

---

## Configuration

Config lives at `~/.bytes-dns/config.json`. Permissions **must** be `600`.

### Interactive Setup (Recommended)

```bash
bytes-dns setup
```
This will prompt for your API token, record name, and zone. It can also automatically resolve your `zone_id` and save it to the config for faster lookups.

### Manual Configuration

```bash
mkdir -p ~/.bytes-dns
cp examples/config.json ~/.bytes-dns/config.json
chmod 600 ~/.bytes-dns/config.json
$EDITOR ~/.bytes-dns/config.json
```

### Config reference

```json
{
  "api_token":        "YOUR_HETZNER_API_TOKEN",
  "zone":             "example.com",
  "zone_id":          "optional_id",
  "record":           "home.example.com",
  "record_type":      "A",
  "ttl":              60,
  "interval_minutes": 5,
  "ip_source":        "https://api4.my-ip.io/ip.txt",
  "log_level":        "info",
  "allow_private_ip": false,
  "dry_run":          false
}
```

| Field              | Required | Default                          | Description |
|--------------------|----------|----------------------------------|-------------|
| `api_token`        | ✅        | —                                | Hetzner DNS API token (Project → API Tokens) |
| `zone`             | ✅        | —                                | Root DNS zone, e.g. `example.com` |
| `zone_id`          | ❌        | —                                | Optional: Hetzner Zone ID (resolved automatically if missing) |
| `record`           | ✅        | —                                | Full record name to update, e.g. `home.example.com` |
| `record_type`      | ❌        | `A`                              | Record type: `A` (IPv4) or `AAAA` (IPv6) |
| `ttl`              | ❌        | `60`                             | DNS TTL in seconds |
| `interval_minutes` | ❌        | `5`                              | Timer interval; used by `install.sh` |
| `ip_source`        | ❌        | `https://api4.my-ip.io/ip.txt`   | URL returning the public IP as plain text |
| `log_level`        | ❌        | `info`                           | `debug`, `info`, `warn`, `error` |
| `allow_private_ip` | ❌        | `false`                          | Set `true` only for NAT/internal setups |
| `dry_run`          | ❌        | `false`                          | Preview changes without writing to Hetzner |

### Getting a Hetzner DNS API token

1. Go to [dns.hetzner.com](https://dns.hetzner.com?ref=OTYBkaZD9yHx)
2. Click your account → **API Tokens**
3. Create a new token with DNS read/write scope
4. Paste it into `~/.bytes-dns/config.json` as `api_token`

---

## Manual Test (before committing to systemd)

```bash
# Validate config, detect IP, authenticate, resolve zone, preview changes:
bytes-dns test
```

Expected output:
```
=== bytes-dns connection test ===
  public IP  : 203.0.113.42
  zone       : example.com (id=abc123def456)
  record     : A home.example.com (label="home")
  status     : record does not exist — would CREATE with value=203.0.113.42
=== test passed ===
```

---

## CLI Reference

```
bytes-dns run              # Detect IP and update DNS (normal operation)
bytes-dns run --force      # Update even if cached IP matches
bytes-dns run --dry-run    # Preview without writing
bytes-dns test             # Full connectivity and config test
bytes-dns setup            # Interactive configuration wizard
bytes-dns status           # Show current state and systemd timer status
bytes-dns install          # Print installation instructions
bytes-dns uninstall        # Print uninstallation instructions
bytes-dns version          # Print version, commit, and Go runtime info
```

---

## systemd Setup

`install.sh` handles all of this automatically. Manual steps for reference:

```bash
# Install template units
sudo cp systemd/bytes-dns.service /etc/systemd/system/bytes-dns@.service
sudo sed 's/INTERVAL_PLACEHOLDER/5min/' \
    systemd/bytes-dns.timer > /etc/systemd/system/bytes-dns@.timer
sudo chmod 644 /etc/systemd/system/bytes-dns@.{service,timer}

# Enable for current user (replace 'youruser' with your username)
sudo systemctl daemon-reload
sudo systemctl enable --now "bytes-dns@youruser.timer"
```

### Timer behavior

- Fires **2 minutes** after every boot (catches IP changes from restarts)
- Fires every **`interval_minutes`** thereafter (default: 5 min)
- Up to 30-second randomised delay to avoid thundering herd
- `Persistent=true` — catches up if the system was off when the timer fired
- The service is `Type=oneshot` — runs once and exits; no persistent daemon

### Logs

```bash
# Follow live:
journalctl -u "bytes-dns@youruser.service" -f

# Last 50 lines:
journalctl -u "bytes-dns@youruser.service" -n 50

# Since last boot:
journalctl -u "bytes-dns@youruser.service" -b
```

---

## Uninstall

```bash
sudo bash uninstall.sh

# Keep your config and state:
KEEP_CONFIG=1 sudo bash uninstall.sh
```

Manual removal:
```bash
sudo systemctl disable --now "bytes-dns@youruser.timer" "bytes-dns@youruser.service"
sudo rm -f /etc/systemd/system/bytes-dns@.{service,timer}
sudo systemctl daemon-reload
sudo rm -f /usr/local/bin/bytes-dns
rm -rf ~/.bytes-dns     # optional — removes config and state
```

---

## Building from Source

```bash
# Build native binary:
make build

# Build for all release targets (linux/amd64, arm64, armv7):
make build-all

# Run tests:
make test

# Lint:
make lint
```

---

## CI/CD

This project uses GitHub Actions for quality assurance and releases:
- **CI**: Runs on every push/PR to `master`. Executes `go test` and `golangci-lint`.
- **Release**: Automatically builds binaries and creates a GitHub release when a new tag (e.g., `v1.1.0`) is pushed.

---

## Troubleshooting

### `config file not found`

The config does not exist yet. Run `bytes-dns setup` or create it manually:
```bash
cp examples/config.json ~/.bytes-dns/config.json
chmod 600 ~/.bytes-dns/config.json
$EDITOR ~/.bytes-dns/config.json
```

### `config file is readable by group or others`

```bash
chmod 600 ~/.bytes-dns/config.json
```

### `authentication failed (HTTP 401)`

Your `api_token` is wrong or expired. Create a new one at [dns.hetzner.com](https://dns.hetzner.com?ref=OTYBkaZD9yHx).

### `zone "example.com" not found`

The zone must exist in your Hetzner account. Check at [dns.hetzner.com](https://dns.hetzner.com?ref=OTYBkaZD9yHx) and ensure the API token has access to that zone. You can also use `bytes-dns setup` to create a missing zone.

### `record "home.example.com" must be within zone "example.com"`

The `record` field must be the zone itself or a subdomain of `zone`.

### `detected IP is a private/RFC1918 address`

You are behind double-NAT or CGNAT. The IP detection endpoint is seeing a private address. If this is intentional (e.g., you want an internal DNS record), set `allow_private_ip: true`. Otherwise, use a different IP detection service or check your network configuration.

### Timer never fires / service not starting

```bash
systemctl status "bytes-dns@youruser.timer"
journalctl -u "bytes-dns@youruser.service" -n 30
```

Ensure the timer unit was enabled: `systemctl enable "bytes-dns@youruser.timer"`.

---

## Security Notes

- `~/.bytes-dns/config.json` must be `chmod 600` — the tool enforces this on startup.
- The API token is never logged, never included in unit file `Environment=` lines, and never exposed in process lists.
- The systemd unit runs with `NoNewPrivileges`, `ProtectSystem=strict`, `PrivateTmp`, and several other hardening options.
- All API calls use HTTPS only.

---

## Assumptions

- The tool is designed for **single-record dynamic DNS** (one record per config file). For multiple records, run multiple instances with separate config directories.
- IPv6 (`AAAA`) code paths are architecturally ready. For IPv6, set `ip_source` to an IPv6-aware endpoint and `record_type` to `AAAA`.
- The installer defaults to a system-wide install (`/usr/local/bin`, `/etc/systemd/system`). User-level installs are possible by editing paths and using `systemctl --user`, but are not automated by `install.sh`.

---

## License

[MIT](LICENSE)
