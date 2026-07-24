# bytes-dns Self-Update Design

**Date:** 2026-07-24
**Status:** Approved (pending user review of written spec)
**Scope:** New `bytes-dns update` subcommand that fetches the latest release from GitHub, verifies it, atomically replaces the running binary, and restarts the systemd timer.

---

## Problem statement

`bytes-dns` currently has no self-update mechanism. To upgrade, users must run `sudo bash install.sh` (or `sudo bytes-dns install`), which only re-installs the existing binary — it does not check whether a newer release is available. The user wants a single command, `bytes-dns update`, that fetches the latest release from the official GitHub repository (`bytes-commerce/bytes-dns`) and applies it.

---

## Goals

- Add a `bytes-dns update` command that downloads the latest release, verifies it, and replaces the running binary.
- Detect the current platform via `runtime.GOOS`/`runtime.GOARCH` and pick the matching asset.
- Verify the asset against `checksums.txt` shipped in the release.
- Run a pre-flight check on the downloaded binary before swapping it in.
- Atomically replace the binary and restart the systemd timer.
- Support `--check` and `--force` flags for monitoring and recovery flows.

## Non-goals

- macOS / Windows builds (only Linux assets are produced today).
- Auto-update on a schedule.
- Signed binaries / detached signature verification (could be a follow-up).
- A full rollback daemon. The chosen approach (atomic rename + leave old binary as `.old.<ver>`) provides manual rollback; automated rollback-on-failure is out of scope.

---

## Architecture

```
cmd/bytes-dns/main.go
   └─ cmdUpdate() — new command
        └─ internal/selfupdate/updater.go (NEW)
             ├─ Latest()         — fetch latest release from GitHub
             ├─ Download()       — fetch the asset for current GOOS/GOARCH
             ├─ VerifyChecksum() — compare against checksums.txt
             ├─ Apply()          — atomic rename + restart systemd timer
```

The `internal/selfupdate` package mirrors the testability pattern of `internal/installer`: function-typed hooks for `httpGet`, `runCommand`, `osRename` are overridable in tests, so unit tests don't actually hit GitHub or touch the filesystem.

---

## Behavior changes

### 1. New `update` subcommand
- **Current:** no `update` command exists.
- **New:** `bytes-dns update [--check] [--force]` is added to the CLI dispatcher. Same help text and exit-code conventions as other commands.
- **Why:** delivers the requested feature.

### 2. GitHub API client
- **Current:** the existing `internal/dns/client.go` makes HTTPS calls to Hetzner.
- **New:** `internal/selfupdate/updater.go` uses `net/http` directly. Calls `https://api.github.com/repos/bytes-commerce/bytes-dns/releases/latest` with a 10-second timeout. Sets `Accept: application/vnd.github+json` and `User-Agent: bytes-dns/<version>`. Reuses the existing DNS retry pattern (3 attempts, 250ms/500ms/1s backoff) for transient 5xx/429/network errors.
- **Why:** GitHub requires a User-Agent; the API is unauthenticated (public) but rate-limited to 60 req/h per IP. One call per update is fine.

### 3. Asset resolution by GOOS/GOARCH
- **Current:** N/A.
- **New:** the updater maps `runtime.GOOS` + `runtime.GOARCH` to a release asset name:
  - `linux/amd64` → `bytes-dns-linux-amd64`
  - `linux/arm64` → `bytes-dns-linux-arm64`
  - `linux/arm` (GOARM=7) → `bytes-dns-linux-armv7`
  - anything else → return an error: "no prebuilt binary for this platform; build from source"
- **Why:** matches the existing Makefile target names exactly.

### 4. Checksum verification
- **Current:** N/A.
- **New:** after downloading the asset, the updater also fetches `checksums.txt` from the same release, splits on whitespace, finds the line for the asset, and compares SHA256. If the asset is missing from `checksums.txt` or the hash doesn't match, abort.
- **Why:** protects against partial downloads, MITM, and supply-chain attacks against the release pipeline.

### 5. Version comparison and skip
- **Current:** N/A.
- **New:** after fetching the release, compare the release `tag_name` (stripped of `v` prefix) against the current `main.Version` (embedded via `-ldflags` at build time). If they match, print "already on vX.Y.Z" and exit 0.
- **Why:** avoid wasted bandwidth when up-to-date.

### 6. `--check` flag
- **Current:** N/A.
- **New:** `bytes-dns update --check` fetches the latest release and prints "current: vX, latest: vY" without downloading. Exit 0 if up-to-date, exit 1 if an update is available.
- **Why:** useful for cron / monitoring.

### 7. `--force` flag
- **Current:** N/A.
- **New:** `bytes-dns update --force` proceeds even if the current version already matches the latest. Useful for re-installing a known-good binary or after a partial-update crash.
- **Why:** recovery path.

### 8. Pre-flight: verify the downloaded binary
- **Current:** N/A.
- **New:** after download + checksum verify, the updater executes the new binary with `bytes-dns --version` (against the staged path) and parses the output. If the exit code is non-zero or the output doesn't contain the expected version string, abort.
- **Why:** catches corrupt downloads before swapping the binary in.

### 9. Atomic apply
- **Current:** the installer copies the binary over the destination.
- **New:** the updater stages the new binary to `<dest>.new`, then on success: `mv <dest> <dest>.old.<old-ver>` and `mv <dest>.new <dest>`. The old binary is preserved as a manual rollback path.
- **Why:** the user-chosen atomic rename + backup approach.

### 10. Restart systemd timer
- **Current:** the installer calls `systemctl enable --now <timer>`.
- **New:** the updater, after the atomic rename, calls `systemctl restart <timer>` (not `enable --now` — the timer is already enabled). If `restart` fails, the user is warned but the new binary is in place; manual `systemctl restart` will pick it up.
- **Why:** the next scheduled run uses the new binary without waiting for the next tick.

### 11. Root check
- **Current:** the installer requires root via `installer.ErrNotRoot`.
- **New:** the updater reuses the same root check.
- **Why:** the binary is owned by root; replacing it requires root.

### 12. Errors and exit codes
- 0: success (updated, OR already up-to-date with `--check` returning up-to-date)
- 1: `--check` returned and an update is available
- 2: any error (network, checksum, pre-flight, etc.)

---

## Testing

### Unit tests

`internal/selfupdate/updater_test.go` with stubbed `httpGet` and `runCommand`:
- `Latest()` parses the GitHub JSON correctly.
- Asset resolution returns the right name for each GOOS/GOARCH combo (table-driven).
- Checksum verification accepts matching hash, rejects mismatched, errors on missing entry.
- Pre-flight: stub `bytes-dns --version` output and verify the parser accepts/rejects.
- Atomic apply: stub `rename` and verify the old binary is renamed to `.old.<ver>` and the new one moves into place.
- Same-version skip: when current and latest match, no download is performed.
- `--check` returns the right exit code.

### Manual verification (before merging)

- `make build && make test` must pass.
- Build linux-amd64, install locally, then build a "fake latest release" against a local GitHub-mock server and verify the update flow.
- Run on a Linux VM with systemd: `sudo bytes-dns update` should download, replace, and restart the timer.

---

## Backwards compatibility

- No config schema changes.
- No existing commands change.
- The new command follows the same CLI conventions as the others.

---

## Rollout

- Single PR.
- Update README to document the new command and the `--check` / `--force` flags.

---

## Open questions

None at this time. All items were clarified with the user before this spec was written.
