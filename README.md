# bytes-dns

**Your own DynDNS via Hetzner DNS and Cloudflare, for free.**

`bytes-dns` keeps a [Hetzner DNS](https://hetzner.cloud/?ref=OTYBkaZD9yHx)-managed record — and optionally the same record in [Cloudflare](https://www.cloudflare.com) — in sync with the current public IP of your server or home connection. It runs as a `systemd` timer, updates only when the IP changes, and requires no external runtime — it compiles to a single static binary.

---

## Why

Hetzner offers free managed DNS. If you have a dynamic public IP (home router, cheap VPS with unstable IP, etc.) and a domain managed by Hetzner, `bytes-dns` replaces a paid DynDNS service at zero cost.

### API Note

`bytes-dns` uses the **Hetzner Cloud DNS API** (`dns.hetzner.com/api/v1`) — not the deprecated _DNS Console API_. Hetzner scheduled the old API for shutdown in May 2026; brownouts for write operations began in early 2026. This project targets only the current, supported API.

For Cloudflare the **DNS API v4** (`api.cloudflare.com/client/v4`) is used, authenticated with an API token (`Zone:Read` + `Zone:DNS:Edit`).

---

## Features

- **Automatic Zone Discovery**: Resolves the correct DNS zone by record name (longest suffix match).
- **Hetzner and Cloudflare**: Keeps the record in sync with Hetzner DNS and — if you provide a Cloudflare API token — with Cloudflare at the same time.
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
│   ├── cloudflare/        # Cloudflare DNS API v4 client
│   ├── dns/               # Hetzner Cloud DNS API client
│   ├── httpx/             # Shared HTTP client with retry policy
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
# OR, if bytes-dns is already on PATH:
sudo bytes-dns install
```

Both commands perform the same install: copy the binary to `/usr/local/bin/bytes-dns`, install systemd service and timer unit templates, enable and start the timer for the current user, and prompt for setup if no config exists.

---

## Updating

```bash
# Check whether an update is available
sudo bytes-dns update --check

# Apply the latest release
sudo bytes-dns update
```

The update command:
1. Fetches the latest release from [GitHub](https://github.com/bytes-commerce/bytes-dns/releases).
2. Picks the asset matching your platform (`runtime.GOOS`/`runtime.GOARCH`).
3. Verifies the asset against the release's `checksums.txt`.
4. Runs the new binary with `--version` as a sanity check.
5. Atomically renames the current binary to `bytes-dns.old.<version>` and the new binary into place.
6. Restarts the systemd timer so the next scheduled run uses the new binary.

If the new binary is broken, you can roll back manually:
```bash
sudo mv /usr/local/bin/bytes-dns.old.<old-version> /usr/local/bin/bytes-dns
sudo systemctl restart bytes-dns@$USER.timer
```

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
  "dry_run":          false,

  "cloudflare": {
    "api_token": "YOUR_CLOUDFLARE_API_TOKEN",
    "zone":     "example.com",
    "zone_id":  "optional_id",
    "record":   "home.example.com",
    "ttl":      60,
    "proxied":  true
  }
}
```

| Field              | Required | Default                          | Description |
|--------------------|----------|----------------------------------|-------------|
| `api_token`        | ✅¹       | —                                | Hetzner DNS API token (Project → API Tokens) |
| `zone`             | ✅        | —                                | Root DNS zone, e.g. `example.com` |
| `zone_id`          | ❌        | —                                | Optional: Hetzner Zone ID (resolved automatically if missing) |
| `record`           | ✅        | —                                | Full record name to update, e.g. `home.example.com` |
| `record_type`      | ❌        | `A`                              | Record type: `A` (IPv4) or `AAAA` (IPv6) |
| `ttl`              | ❌        | `60`                             | DNS TTL in seconds |
| `interval_minutes` | ❌        | `5`                              | Timer interval; used by `install.sh` |
| `ip_source`        | ❌        | `https://api4.my-ip.io/ip.txt,https://ifconfig.co/ip,https://checkip.amazonaws.com` | Comma-separated list of URLs (tried in order) returning the public IP as plain text |
| `log_level`        | ❌        | `info`                           | `debug`, `info`, `warn`, `error` |
| `allow_private_ip` | ❌        | `false`                          | Set `true` only for NAT/internal setups |
| `dry_run`          | ❌        | `false`                          | Preview changes without writing to the DNS provider |
| `cloudflare`       | ❌        | —                                | Optional Cloudflare settings, see below |

¹ Required unless a `cloudflare.api_token` is configured — at least one provider must be set up.

### Getting a Hetzner DNS API token

1. Go to [dns.hetzner.com](https://dns.hetzner.com?ref=OTYBkaZD9yHx)
2. Click your account → **API Tokens**
3. Create a new token with DNS read/write scope
4. Paste it into `~/.bytes-dns/config.json` as `api_token`

---

## Cloudflare

`bytes-dns` can keep the same record in sync in **Cloudflare** in addition to Hetzner. The
Cloudflare part is optional: as soon as a `cloudflare` block with an API token exists, the
record is synchronised with Cloudflare as well. Without that block, nothing changes for
existing installations.

### Getting a Cloudflare API token

1. Go to [dash.cloudflare.com](https://dash.cloudflare.com) → **My Profile** → **API Tokens**
2. Create a token with the **Edit zone DNS** template, or a custom token with these permissions:
   - `Zone → Zone → Read` — needed to look up the zone
   - `Zone → DNS → Edit` — needed to create and update records
3. Scope the token to the zone(s) you want to manage.

The Cloudflare *global API key* is not supported — use an API token.

### Enabling Cloudflare

Add a `cloudflare` block to `~/.bytes-dns/config.json`:

```json
{
  "api_token": "YOUR_HETZNER_API_TOKEN",
  "zone":      "example.com",
  "record":    "home.example.com",

  "cloudflare": {
    "api_token": "YOUR_CLOUDFLARE_API_TOKEN",
    "zone":      "example.com",
    "record":    "home.example.com"
  }
}
```

| Field                | Required | Default            | Description |
|----------------------|----------|--------------------|-------------|
| `api_token`          | ✅        | —                  | Cloudflare API token (`Zone:Read` + `Zone:DNS:Edit`) |
| `zone`               | ❌        | top-level `zone`   | Cloudflare zone, e.g. `example.com` |
| `zone_id`            | ❌        | —                  | Cloudflare Zone ID (resolved automatically if missing) |
| `record`             | ❌        | top-level `record` | Record to update in Cloudflare |
| `ttl`                | ❌        | top-level `ttl`    | Cloudflare only accepts `1` (automatic) or `60` seconds and above |
| `proxied`            | ❌        | `true`             | Whether **newly created** records are routed through Cloudflare |

`bytes-dns setup` can also configure the Cloudflare block interactively.

### Proxy behaviour (`proxied`)

- `proxied` is only applied when `bytes-dns` **creates** a record. Newly created records are
  proxied by default; set `"proxied": false` to create a DNS-only record.
- Records that already exist are **never** changed in that respect — an update only patches the
  IP address. Whatever proxy status the record has in Cloudflare stays as it is.

> With `proxied: true` Cloudflare answers with its own anycast IP instead of your public IP.
> That is the right choice for web traffic, but not if you need the DNS record to resolve to
> your actual address.

### Notes

- The zone has to be managed by Cloudflare (its nameservers must be authoritative), otherwise
  the record has no effect.
- Cloudflare cannot create zones — unlike Hetzner, the zone must already exist.
- The zone apex (`example.com` itself) is not supported, use a subdomain such as
  `home.example.com`.
- If a provider fails, the run aborts with exit code `1` so systemd reports the failure.

---

## Manual Test (before committing to systemd)

```bash
# Validate config, detect IP, authenticate, resolve zone, preview changes:
bytes-dns test
```

Expected output:
```
=== bytes-dns connection test (hetzner) ===
  public IP  : 203.0.113.42
  zone       : example.com (id=abc123def456)
  record     : A home.example.com
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
bytes-dns install          # Install binary, systemd units, and enable timer (requires root)
bytes-dns uninstall        # Remove systemd units and binary (requires root)
bytes-dns update           # Fetch latest release from GitHub and replace the binary (requires root)
bytes-dns version          # Print version, commit, and Go runtime info
```

`bytes-dns status` lists every configured provider with its zone, record, TTL, the last IP that
was written and — if the last run failed — the error that caused it:

```
  providers      : 2
    - hetzner
        zone     : example.com (id: 42)
        record   : A home.example.com
        ttl      : 60
        last IP  : 203.0.113.42
        updated  : 2026-01-01T12:00:00Z
    - cloudflare
        zone     : example.com (id: 023e105f4ecef8ad9ca31a8372d0c353)
        record   : A home.example.com
        ttl      : 60
        proxied  : true (new records only)
        last error: access denied (HTTP 403) - the Cloudflare API token may lack the Zone:DNS:Edit permission
        last IP  : (not yet cached)
```

### Environment variables

| Variable              | Description |
|-----------------------|-------------|
| `HETZNER_API_BASE`    | Overrides the Hetzner API base URL (default `https://api.hetzner.cloud/v1`) |
| `CLOUDFLARE_API_BASE` | Overrides the Cloudflare API base URL (default `https://api.cloudflare.com/client/v4`) |

Both are meant for testing and for setups that route API traffic through a proxy.

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

- Fires **1 minute** after every boot (catches IP changes from restarts)
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
