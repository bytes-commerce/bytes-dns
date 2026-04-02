# CLAUDE.md

This file provides guidance to Claude Code (claude.ai/code) when working with code in this repository.

## Project Overview

`bytes-dns` is a DynDNS updater for Hetzner DNS that keeps a DNS record in sync with your public IP. It runs as a systemd timer, updates only when IP changes, and compiles to a single static binary.

**API Note**: Uses Hetzner Cloud DNS API v1 (`dns.hetzner.com/api/v1`) — not the deprecated DNS Console API.

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
  dns/                    # Hetzner Cloud DNS API client
  ip/                     # Public IP detection (IPv4/IPv6)
  logger/                 # Minimal structured logger (stderr)
  state/                  # Local state file (prevents redundant API calls)
  updater/                # Core orchestration logic
```

### Key Design Patterns

- **`updater.Updater`** is the central orchestrator. It uses a `dnsClient` interface, allowing tests to inject a mock DNS client.
- **Zone resolution**: `FindZoneByRecord` uses longest-suffix match across all zones in the account — this enables records to be updated without explicitly configuring `zone_id`.
- **State caching**: `state.Manager` stores the last known IP locally. The updater skips API calls when IP hasn't changed since last check.
- **Config security**: Config file permissions (600) are enforced at load time.

### Dependency Flow

```
main.go → updater.Run()
         ├── config.Load()          # Validates config.json
         ├── state.Manager.Load()   # Reads last known IP
         ├── ip.Detector.DetectIPv4/IPv6  # Fetches current public IP
         └── dns.Client
              ├── FindZoneByRecord()  # Longest-suffix zone match
              ├── FindRRSet()         # Lookup existing record
              ├── CreateRRSet()       # Create if missing
              └── UpdateRRSet()       # Update if IP changed
```

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
