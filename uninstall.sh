#!/usr/bin/env bash
# uninstall.sh — remove bytes-dns binary, systemd units, and optionally config.
#
# Usage:
#   sudo bash uninstall.sh              # uninstall for current user
#   BYTES_DNS_USER=alice sudo bash uninstall.sh
#   KEEP_CONFIG=1 sudo bash uninstall.sh   # preserve ~/.bytes-dns/
set -euo pipefail

RED='\033[0;31m'; GREEN='\033[0;32m'; YELLOW='\033[1;33m'; NC='\033[0m'
info()  { echo -e "${GREEN}[INFO]${NC}  $*"; }
warn()  { echo -e "${YELLOW}[WARN]${NC}  $*"; }

BYTES_DNS_USER="${BYTES_DNS_USER:-$(id -un)}"
PREFIX="${PREFIX:-/usr/local/bin}"
SYSTEMD_DIR="${SYSTEMD_DIR:-/etc/systemd/system}"
KEEP_CONFIG="${KEEP_CONFIG:-0}"

[[ $EUID -eq 0 ]] || { echo -e "${RED}[ERROR]${NC} Run as root: sudo bash uninstall.sh" >&2; exit 1; }

target_home="$(getent passwd "$BYTES_DNS_USER" | cut -d: -f6)"
config_dir="${target_home}/.bytes-dns"
service_name="bytes-dns@${BYTES_DNS_USER}"

# ── Stop and disable systemd units ───────────────────────────────────────────
for unit in "${service_name}.timer" "${service_name}.service"; do
    if systemctl is-active --quiet "$unit" 2>/dev/null; then
        info "Stopping ${unit} ..."
        systemctl stop "$unit"
    fi
    if systemctl is-enabled --quiet "$unit" 2>/dev/null; then
        info "Disabling ${unit} ..."
        systemctl disable "$unit"
    fi
done

# ── Remove unit files ─────────────────────────────────────────────────────────
for f in "${SYSTEMD_DIR}/bytes-dns@.service" "${SYSTEMD_DIR}/bytes-dns@.timer"; do
    if [[ -f "$f" ]]; then
        info "Removing ${f} ..."
        rm -f "$f"
    fi
done

systemctl daemon-reload
info "Systemd daemon reloaded."

# ── Remove binary ─────────────────────────────────────────────────────────────
binary="${PREFIX}/bytes-dns"
if [[ -f "$binary" ]]; then
    info "Removing ${binary} ..."
    rm -f "$binary"
fi

# ── Optionally remove config directory ───────────────────────────────────────
if [[ "$KEEP_CONFIG" == "1" ]]; then
    warn "Keeping config directory ${config_dir} (KEEP_CONFIG=1)."
else
    if [[ -d "$config_dir" ]]; then
        info "Removing config directory ${config_dir} ..."
        rm -rf "$config_dir"
    fi
fi

echo
info "bytes-dns has been uninstalled."
if [[ "$KEEP_CONFIG" != "1" ]] && [[ -d "$config_dir" ]]; then
    warn "Config directory not removed: ${config_dir}"
fi
