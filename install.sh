#!/usr/bin/env bash
# install.sh — install bytes-dns as a systemd service for the current user.
# Requires: Go >= 1.22, systemd, bash, jq (for config parsing).
#
# Usage:
#   bash install.sh            # build from source and install
#   BINARY=/path/to/binary bash install.sh   # install pre-built binary
#
# Environment:
#   BYTES_DNS_USER   — user to install the service for (default: current user)
#   BINARY           — path to pre-built binary (skips go build)
#   PREFIX           — binary install prefix (default: /usr/local/bin)
#   SYSTEMD_DIR      — systemd unit directory (default: /etc/systemd/system)
set -euo pipefail

# ── Colours ──────────────────────────────────────────────────────────────────
RED='\033[0;31m'; GREEN='\033[0;32m'; YELLOW='\033[1;33m'; NC='\033[0m'
info()  { echo -e "${GREEN}[INFO]${NC}  $*"; }
warn()  { echo -e "${YELLOW}[WARN]${NC}  $*"; }
error() { echo -e "${RED}[ERROR]${NC} $*" >&2; exit 1; }

# ── Defaults ─────────────────────────────────────────────────────────────────
BYTES_DNS_USER="${BYTES_DNS_USER:-$(id -un)}"
PREFIX="${PREFIX:-/usr/local/bin}"
SYSTEMD_DIR="${SYSTEMD_DIR:-/etc/systemd/system}"
BINARY="${BINARY:-}"
SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"

# ── Sanity checks ────────────────────────────────────────────────────────────
[[ "$(uname -s)" == "Linux" ]] || error "bytes-dns requires Linux with systemd."
command -v systemctl &>/dev/null || error "systemctl not found — is systemd running?"
[[ $EUID -eq 0 ]] || error "This installer must be run as root (sudo bash install.sh)."

target_home="$(getent passwd "$BYTES_DNS_USER" | cut -d: -f6)"
[[ -n "$target_home" ]] || error "Cannot determine home directory for user '$BYTES_DNS_USER'."

config_dir="${target_home}/.bytes-dns"
config_file="${config_dir}/config.json"
state_file="${config_dir}/state.json"

# ── Build binary ─────────────────────────────────────────────────────────────
if [[ -n "$BINARY" ]]; then
    info "Using provided binary: $BINARY"
    [[ -f "$BINARY" ]] || error "Provided binary '$BINARY' not found."
else
    command -v go &>/dev/null || error "Go not found — install go >= 1.22 or set BINARY=/path/to/bytes-dns."

    go_version=$(go version | awk '{print $3}' | sed 's/go
    required_major=1; required_minor=22
    IFS='.' read -r got_major got_minor _ <<< "$go_version"
    if [[ "$got_major" -lt "$required_major" ]] || { [[ "$got_major" -eq "$required_major" ]] && [[ "${got_minor%%[^0-9]*}" -lt "$required_minor" ]]; }; then
        error "Go >= 1.22 required, found $go_version."
    fi

    info "Building bytes-dns from source..."
    build_dir="$(mktemp -d)"
    trap 'rm -rf "$build_dir"' EXIT

    # Build with version metadata embedded.
    VERSION="${VERSION:-$(git -C "$SCRIPT_DIR" describe --tags --always 2>/dev/null || echo dev)}"
    COMMIT="$(git -C "$SCRIPT_DIR" rev-parse --short HEAD 2>/dev/null || echo unknown)"
    BUILD_DATE="$(date -u +%Y-%m-%dT%H:%M:%SZ)"

    CGO_ENABLED=0 go build \
        -C "$SCRIPT_DIR" \
        -ldflags="-s -w \
            -X main.Version=${VERSION} \
            -X main.Commit=${COMMIT} \
            -X main.BuildDate=${BUILD_DATE}" \
        -o "${build_dir}/bytes-dns" \
        ./cmd/bytes-dns/

    BINARY="${build_dir}/bytes-dns"
    info "Build complete."
fi

# ── Install binary ───────────────────────────────────────────────────────────
info "Installing binary to ${PREFIX}/bytes-dns ..."
install -m 755 "$BINARY" "${PREFIX}/bytes-dns"

# ── Read interval from config (if it exists already) ─────────────────────────
interval_minutes=5
if [[ -f "$config_file" ]] && command -v jq &>/dev/null; then
    parsed_interval=$(jq -r '.interval_minutes // 5' "$config_file" 2>/dev/null || echo 5)
    if [[ "$parsed_interval" =~ ^[0-9]+$ ]] && [[ "$parsed_interval" -ge 1 ]]; then
        interval_minutes="$parsed_interval"
    fi
elif [[ -f "$config_file" ]]; then
    warn "jq not installed — using default interval of ${interval_minutes} minutes."
fi
info "Systemd timer interval: ${interval_minutes} minute(s)."

# ── Install systemd units ────────────────────────────────────────────────────
info "Installing systemd units to ${SYSTEMD_DIR} ..."

# Service unit — replace %i template variable with the target user.
service_name="bytes-dns@${BYTES_DNS_USER}.service"
timer_name="bytes-dns@${BYTES_DNS_USER}.timer"

# Install the template service (bytes-dns@.service).
install -m 644 "${SCRIPT_DIR}/systemd/bytes-dns.service" \
    "${SYSTEMD_DIR}/bytes-dns@.service"

# Install the timer template and substitute the interval placeholder.
sed "s/INTERVAL_PLACEHOLDER/${interval_minutes}min/" \
    "${SCRIPT_DIR}/systemd/bytes-dns.timer" \
    > "${SYSTEMD_DIR}/bytes-dns@.timer"
chmod 644 "${SYSTEMD_DIR}/bytes-dns@.timer"

# ── Create config directory ───────────────────────────────────────────────────
if [[ ! -d "$config_dir" ]]; then
    info "Creating config directory ${config_dir} ..."
    install -d -m 700 -o "$BYTES_DNS_USER" -g "$BYTES_DNS_USER" "$config_dir"
fi

# Install example config only if no existing config is present.
if [[ ! -f "$config_file" ]]; then
    if [[ -f "${SCRIPT_DIR}/examples/config.json" ]]; then
        install -m 600 -o "$BYTES_DNS_USER" -g "$BYTES_DNS_USER" \
            "${SCRIPT_DIR}/examples/config.json" "$config_file"
        warn "Example config installed at ${config_file}"
        warn "Edit it and set your api_token, zone, and record before the timer fires."
    fi
else
    # Enforce restrictive permissions on existing config.
    chmod 600 "$config_file"
fi

# ── Enable and start timer ───────────────────────────────────────────────────
info "Reloading systemd daemon ..."
systemctl daemon-reload

info "Enabling and starting ${timer_name} ..."
systemctl enable --now "$timer_name"

# ── Smoke-test ───────────────────────────────────────────────────────────────
echo
info "Installation complete."
echo
echo "  Config file  : ${config_file}"
echo "  State file   : ${state_file}"
echo "  Binary       : ${PREFIX}/bytes-dns"
echo "  Service      : ${service_name}"
echo "  Timer        : ${timer_name} (every ${interval_minutes} min)"
echo
if [[ ! -f "$config_file" ]] || grep -q 'YOUR_HETZNER_API_TOKEN' "$config_file" 2>/dev/null; then
    warn "ACTION REQUIRED: edit ${config_file} with your real api_token, zone, and record."
    warn "Then run: sudo systemctl start ${service_name}"
else
    echo "  To test now  : sudo systemctl start ${service_name}"
fi
echo "  View logs    : journalctl -u ${service_name} -f"
echo "  Timer status : systemctl status ${timer_name}"
echo "  CLI test     : ${PREFIX}/bytes-dns test"
