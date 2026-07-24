# bytes-dns Stability, Correctness, and Autostart Design

**Date:** 2026-07-24
**Status:** Approved (pending user review of written spec)
**Scope:** Bug fixes + autostart improvements for the `bytes-dns` Hetzner DynDNS updater.

---

## Problem statement

`bytes-dns` was reported to (1) "not run stable" and (2) "not correctly update the DNS entries correctly all the time." Additionally, the installation flow does not register itself in the system autostart — `bytes-dns install` is a print-stub that points at a separate `install.sh` script.

After reviewing the code, the root causes fall into three buckets:

1. **Correctness bugs**: in-place RRSet mutation, dry-run flag last-writer-wins, zone resolution ambiguous when `cfg.Zone` is set, dead state field `LastSyncedIP`.
2. **Stability gaps**: no retry on transient failures, single IP source with no fallback, silent state corruption, unnecessary config writes on every run.
3. **Autostart UX**: `cmdInstall` does nothing; the real install lives in 148 lines of bash that is not called from the binary.

This spec addresses all three.

---

## Goals

- Make `bytes-dns install` register the systemd timer end-to-end (Linux + systemd only).
- Eliminate the stability bugs that cause missed DNS updates.
- Fix the correctness bugs that can cause wrong-zone or wrong-record writes.
- Consolidate the install logic into a single source of truth (Go) with a thin bash wrapper for backward compatibility.

## Non-goals

- Multi-platform autostart (no macOS launchd, no Windows service).
- New daemon mode, HTTP health endpoint, or pluggable interface-based IP detection.
- Adaptive intervals or other new features.

---

## Architecture

The change touches four layers, each isolated to one package so review stays local:

```
cmd/bytes-dns/main.go
   └─ cmdInstall() now delegates to internal/installer
        └─ internal/installer/installer.go  (NEW)
             ├─ install.Binary()    — installs /usr/local/bin/bytes-dns
             ├─ install.Units()     — writes *.service / *.timer
             ├─ install.Enable()    — systemctl daemon-reload + enable --now
             └─ install.Verify()    — systemctl is-active check after enable

internal/updater/updater.go (FIX)
   ├─ uses ip.Detector with multiple fallback sources
   ├─ uses dns.Client with retry/backoff
   ├─ uses state.Manager that quarantines corrupt state
   └─ removes in-place RRSet mutation; removes dead LastSyncedIP

internal/ip/detector.go (FIX)
   └─ NewWithSources([]string) — tries sources in order, returns first success

internal/dns/client.go (FIX)
   └─ sendJSON wraps with retry policy (3 attempts, 250ms/750ms/2s backoff)

internal/state/state.go (FIX)
   └─ On corrupt JSON, rename the file to state.json.broken.<timestamp> and return empty state

internal/config/config.go (FIX)
   └─ dry_run is OR-merged with CLI --dry-run flag (either set wins)
```

---

## Behavior changes

### 1. `cmdInstall` actually installs

**Current:** `cmdInstall()` in `cmd/bytes-dns/main.go:363` prints a banner pointing at `install.sh`.

**New behavior:**
- Requires root (errors out with a clear message if `EUID != 0`).
- Detects the invoking user via `SUDO_USER` (falls back to `os.Getuid()` lookup).
- Copies the running binary to `/usr/local/bin/bytes-dns` (mode 755).
- Writes `/etc/systemd/system/bytes-dns@.service` and `/etc/systemd/system/bytes-dns@.timer` from the embedded templates in `internal/installer/assets/`.
- Substitutes `INTERVAL_PLACEHOLDER` with the value of `cfg.IntervalMinutes` from the existing config (default 5).
- Runs `systemctl daemon-reload` and `systemctl enable --now bytes-dns@<user>.timer`.
- Verifies via `systemctl is-active` that the timer is now active. Prints a warning if not.
- If no config exists at `~/.bytes-dns/config.json`, prompts the user to run `bytes-dns setup` before continuing.

**Why:** eliminates the "install.sh is a separate artifact" confusion; one command does everything.

### 2. Retry policy on transient API failures

**Current:** a single 5xx / network error → `Run` returns an error, systemd logs it as a failed oneshot, next timer cycle is 5 min later.

**New behavior:**
- `internal/dns/client.go` wraps the `sendJSON` HTTP call with a retry policy.
- 3 attempts total, with exponential backoff: 250ms → 750ms → 2s.
- Retries on: HTTP 5xx, HTTP 429, network errors (DNS failure, connection refused, timeout).
- Does NOT retry on: HTTP 4xx (except 429), HTTP 401, HTTP 403, HTTP 404, HTTP 422.
- Adds a `User-Agent` identifier to the retry attempts (e.g., `bytes-dns/1.0 retry-2`).

**Why:** network blips are the most common cause of "doesn't update DNS correctly all the time."

### 3. Fallback IP detection sources

**Current:** `ip_source` is a single URL; if it fails, the entire run fails.

**New behavior:**
- `internal/ip/detector.go` defines `NewWithSources([]string)`.
- The detector tries each source in order. First source that returns a valid IP wins.
- The default (`DefaultIPSource`) becomes a comma-separated list: `https://api4.my-ip.io/ip.txt,https://ifconfig.co/ip,https://checkip.amazonaws.com`.
- `Config.IPSource` is split on commas at validation time. Single values still work (no breaking change).
- The `ip_source` validator only checks the first URL.

**Why:** single-source dependency is the second most common cause of stale records.

### 4. State corruption handling

**Current:** `state.Manager.Load` (`internal/state/state.go:32`) silently returns empty state on corrupt JSON.

**New behavior:**
- On corrupt JSON, rename the file to `state.json.broken.<unix-timestamp>` (preserving the original for debugging).
- Log a warning via `logger.Warn`.
- Return an empty `State`.

**Why:** silent corruption hides real problems; preserved file aids debugging.

### 5. `LastSyncedIP` is removed

**Current:** `State.LastSyncedIP` exists but is never read. The "skip if unchanged" check uses `LastIP`, which is set by `MarkUpdated` only on successful writes — so functionally the same.

**New behavior:** remove `LastSyncedIP` from the `State` struct and from `MarkUpdated`. Existing state files load fine — `json.Unmarshal` silently drops unknown fields in the source JSON, so a leftover `last_synced_ip` key in an old `state.json` is harmless.

**Why:** dead state is a code smell and confuses future maintainers.

### 6. `UpdateRRSet` does not mutate its input

**Current:** `internal/dns/client.go:156` mutates `rrset.Records` in place after a successful PUT, then returns the same pointer.

**New behavior:** build a copy of the RRSet (`*rrset` shallow copy), mutate the copy, return it. The caller's `rrset` is untouched.

**Why:** in-place mutation breaks the value semantics of Go and is a surprising side effect.

### 7. Dry-run flag precedence

**Current:** CLI `--dry-run` flag overrides config `dry_run` (last-writer-wins).

**New behavior:** OR-merge. If either the CLI flag or the config field is `true`, the run is dry-run. Documented in help text.

**Why:** predictable, hard to misuse.

### 8. Zone resolution safety

**Current:** `Updater.Run` (`internal/updater/updater.go:81`) uses `FindZoneByRecord` first, then falls back to `FindZone(cfg.Zone)`. The suffix-matched zone is not validated against `cfg.Zone`.

**New behavior:** when `cfg.Zone` is set, validate that the suffix-matched zone's name equals `cfg.Zone` (case-insensitive). If not, ignore the suffix match and try `FindZone(cfg.Zone)`. If that also fails, return an error.

**Why:** prevents silently updating a record in the wrong zone if the suffix match is ambiguous or the user has multiple zones with overlapping suffixes.

### 9. Config write is conditional

**Current:** `Updater.Run` calls `cfg.Save("")` every time the zone is resolved, even if the resolved zone did not change.

**New behavior:** only write when `cfg.ZoneID` actually changed from the value already in the config. Errors still log as warnings.

**Why:** reduces unnecessary file writes inside the systemd sandbox; makes the side effect explicit.

### 10. `install.sh` becomes a thin wrapper

**Current:** `install.sh` is 148 lines.

**New behavior:** `install.sh` is a ~15-line bash script that re-execs `bytes-dns install` as root. All real logic lives in `internal/installer`. Backward compatibility is preserved for anyone who directly invokes `install.sh`.

**Why:** one source of truth; install behavior is now testable via Go tests.

---

## Testing

### Unit tests

- `internal/installer` — full install flow against `t.TempDir()` as a fake root. Verify units are written with the right contents and permissions, and that the in-memory `Enable()` step records the right `systemctl` invocations.
- `internal/dns` retry policy — `httptest` server that returns 500 twice then 200, verify retry succeeds; verify it does NOT retry on 401.
- `internal/ip` fallback — `httptest` server that 500s on first, returns IP on second, verify fallback succeeds.
- `internal/state` — corrupt state file is renamed to `.broken.<ts>`.
- `internal/updater` — `UpdateRRSet` no longer mutates the input RRSet (assert `rrset.Records` is unchanged after the call).
- `internal/updater` — `--dry-run` + `cfg.DryRun=true` both result in `result.DryRun == true`.
- `internal/updater` — zone resolution falls back to `FindZone(cfg.Zone)` when the suffix match disagrees.
- `config` — `IPSource` accepts comma-separated lists and validates only the first URL.

### Manual verification (before merging)

- `make build && make test` must pass.
- On a Linux VM with systemd: `sudo ./bytes-dns install` → verify `systemctl status bytes-dns@$USER.timer` shows `active` and `journalctl -u bytes-dns@$USER.service -n 20` shows successful runs.
- `bytes-dns run --dry-run` while config is valid → verify no writes happen (check `UpdateRRSet` and `CreateRRSet` are not called).
- Inject a 500-returning IP source into the config, verify the next source is used.

---

## Backwards compatibility

- **Config schema:** unchanged. The new `ip_source` accepts comma-separated lists; existing single URLs still work.
- **State file:** the `LastSyncedIP` field is removed from the struct. Old state files load fine — `json.Unmarshal` silently drops unknown fields in the source JSON, so a leftover `last_synced_ip` key in an old `state.json` is harmless.
- **`install.sh` users:** existing invocation still works. The new `install.sh` is a thin wrapper that calls `bytes-dns install`. Same end state.
- **CLI:** all existing commands and flags work identically. Only `install` and `uninstall` change behavior.

---

## Rollout

- Single PR against `master`.
- No migrations needed.
- README updates:
  - `bytes-dns install` is now the canonical install command (no longer a stub).
  - Note the new `ip_source` comma-separated list syntax.
  - Note the retry policy and fallback behavior in the "stability" section.

---

## Open questions

None at this time. All items were clarified with the user before this spec was written.
