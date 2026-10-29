# CLAUDE.md

This file provides guidance to Claude Code (claude.ai/code) when working with code in this repository.

## Project Overview

`bytes-dns` is a DynDNS updater for Hetzner DNS (and optionally Cloudflare) that keeps a DNS record in sync with your public IP. It runs as a systemd timer, updates only when IP changes, and compiles to a single static binary.

**API Note**: Uses Hetzner Cloud DNS API v1 (`dns.hetzner.com/api/v1`) — not the deprecated DNS Console API. Cloudflare support uses DNS API v4 (`api.cloudflare.com/client/v4`) with an API token.

## Build, Lint, and Test Commands

```bash
# Build native binary (statically linked)
make build

# Build for all release targets (linux/amd64, arm64, armv7)
make build-all

# Run all tests
make test

# Run a single test
go test ./internal/config/... -v -count=1

# Lint (go vet + staticcheck if installed)
make lint

# Format code
make fmt
```

## Architecture

```
cmd/bytes-dns/main.go     # CLI entry point, command routing
internal/
  config/                  # Config loading, validation, defaults
  cloudflare/              # Cloudflare DNS API v4 client
  dns/                     # Hetzner Cloud DNS API client + provider interface
  httpx/                   # Shared HTTP client (retry policy, typed errors)
  ip/                      # Public IP detection (IPv4/IPv6)
  logger/                  # Minimal structured logger (stderr)
  state/                   # Local state file (prevents redundant API calls)
  updater/                 # Core orchestration logic
```

### Key Design Patterns

- **`updater.Updater`** is the central orchestrator. It uses the `dns.Provider` interface, allowing tests to inject a mock DNS client.
- **Multi-provider**: `config.ProviderConfigs()` returns every configured provider (Hetzner first, Cloudflare second). The CLI runs them one after another and aborts on the first error. Each provider keeps its own state entry, so one provider never suppresses the update of another.
- **Provider neutral records**: `dns.Provider` addresses zones by their opaque ID (`dns.ZoneID`) and records by their fully qualified name. The Hetzner client converts the FQDN into the relative label the Hetzner API expects, Cloudflare uses the FQDN as is.
- **Zone resolution**: `FindZoneByRecord` uses longest-suffix match across all zones in the account — this enables records to be updated without explicitly configuring `zone_id`.
- **State caching**: `state.Manager` stores the last known IP per provider locally. The updater skips API calls when the IP hasn't changed since the last check of that provider.
- **Config security**: Config file permissions (600) are enforced at load time.
- **Cloudflare proxy flag**: `proxied` is only applied when a record is created. Updates patch the content only, so the proxy status of an existing record is never changed.

### Dependency Flow

```
main.go → updater.Run()
         ├── config.Load()          # Validates config.json
         ├── config.ProviderConfigs()  # Hetzner and/or Cloudflare
         ├── state.Manager.Load()   # Reads last known IP per provider
         ├── ip.Detector.DetectIPv4/IPv6  # Fetches current public IP (shared)
         └── dns.Provider
              ├── FindZoneByRecord()  # Longest-suffix zone match
              ├── FindRRSet()         # Lookup existing record
              ├── CreateRRSet()       # Create if missing
              └── UpdateRRSet()       # Update if IP changed
```

### Cloudflare API Concepts

- **Zone**: a DNS zone in the Cloudflare account, addressed by a 32 character hex ID
- **Record**: a single DNS record (one content value each), identified by its own ID
- **Envelope**: every Cloudflare response carries `success`, `errors` and `result` — errors are often returned with HTTP 200
- **Proxied**: whether Cloudflare proxies the traffic for a record ("orange cloud")

### Hetzner DNS API Concepts

- **Zone**: DNS zone (e.g., `example.com`)
- **RRSet**: Resource Record Set — a record name + type + values (e.g., `A home.example.com → 203.0.113.42`)
- **Records within RRSets**: Can have multiple values; bytes-dns manages single-value records

## Config Location

`~/.bytes-dns/config.json` — permissions must be `600`. The config dir and state file share the same directory.

## CLI Commands

```
bytes-dns run       # Normal operation (detect IP, update DNS if changed)
bytes-dns run --force      # Force update even if IP unchanged
bytes-dns run --dry-run    # Preview without writing
bytes-dns test      # Full connectivity diagnostic
bytes-dns setup     # Interactive configuration wizard
bytes-dns status    # Show config, state, systemd timer status
```
