#!/usr/bin/env bash
# install.sh - thin wrapper that re-execs the Go-based installer.
# All real logic lives in bytes-dns (internal/installer).
set -euo pipefail

if [[ "$(uname -s)" != "Linux" ]]; then
    echo "ERROR: bytes-dns requires Linux." >&2
    exit 1
fi
if ! command -v systemctl &>/dev/null; then
    echo "ERROR: systemctl not found - is systemd running?" >&2
    exit 1
fi
if [[ $EUID -ne 0 ]]; then
    echo "ERROR: install.sh must be run as root (sudo bash install.sh)." >&2
    exit 1
fi

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"

# If there's a built binary in the source directory, use it; otherwise rely on
# whatever bytes-dns is on PATH.
BIN=""
if [[ -x "${SCRIPT_DIR}/bytes-dns" ]]; then
    BIN="${SCRIPT_DIR}/bytes-dns"
elif command -v bytes-dns &>/dev/null; then
    BIN="$(command -v bytes-dns)"
else
    echo "ERROR: bytes-dns binary not found. Build it with 'make build' or install it first." >&2
    exit 1
fi

exec "${BIN}" install