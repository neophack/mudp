#!/usr/bin/env bash
# install-service.sh — Install mudp as a systemd service on Linux.
# Usage: sudo ./install-service.sh [/path/to/mudp]
#
# If no path is given the script looks for a 'mudp' binary in the same
# directory as this script, then in the current directory, then in PATH.

set -euo pipefail

# ---------------------------------------------------------------------------
# Helpers
# ---------------------------------------------------------------------------
die()  { echo "ERROR: $*" >&2; exit 1; }
info() { echo "  --> $*"; }

# ---------------------------------------------------------------------------
# Must run as root
# ---------------------------------------------------------------------------
if [[ $EUID -ne 0 ]]; then
    echo "Root privileges are required. Re-launching with sudo..."
    exec sudo "$0" "$@"
fi

# ---------------------------------------------------------------------------
# Locate the mudp binary
# ---------------------------------------------------------------------------
MUDP_BIN=""

if [[ $# -ge 1 ]]; then
    MUDP_BIN="$1"
    [[ -f "$MUDP_BIN" ]] || die "Binary not found at: $MUDP_BIN"
else
    # Same directory as the script
    SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
    if [[ -f "$SCRIPT_DIR/mudp" ]]; then
        MUDP_BIN="$SCRIPT_DIR/mudp"
    elif [[ -f "./mudp" ]]; then
        MUDP_BIN="$(pwd)/mudp"
    elif command -v mudp &>/dev/null; then
        MUDP_BIN="$(command -v mudp)"
    else
        die "mudp binary not found. Pass the path as the first argument:\n  sudo $0 /path/to/mudp"
    fi
fi

MUDP_BIN="$(realpath "$MUDP_BIN")"
info "Using binary: $MUDP_BIN"

# Verify it is executable
[[ -x "$MUDP_BIN" ]] || chmod +x "$MUDP_BIN"

# ---------------------------------------------------------------------------
# Configuration — edit these defaults or export the variables before running
# ---------------------------------------------------------------------------
SERVICE_NAME="${SERVICE_NAME:-mudp}"
DEFAULT_SERVICE_USER="${SUDO_USER:-$(logname 2>/dev/null || id -un)}"
MUDP_USER="${MUDP_USER:-$DEFAULT_SERVICE_USER}"
id -u "$MUDP_USER" &>/dev/null || die "User does not exist: $MUDP_USER"
MUDP_GROUP="$(id -gn "$MUDP_USER")"
MUDP_HOME="$(getent passwd "$MUDP_USER" | cut -d: -f6)"
[[ -n "$MUDP_HOME" ]] || die "Unable to determine home directory for user: $MUDP_USER"
INSTALL_DIR="${INSTALL_DIR:-/opt/mudp}"
DATA_DIR="${DATA_DIR:-${MUDP_HOME}/.mudp}"
MUDP_DB_PATH="${MUDP_DB_PATH:-${MUDP_HOME}/.mudp/mudp.db}"
MUDP_DB_DIR="$(dirname "$MUDP_DB_PATH")"
# Extra writable paths for systemd ProtectSystem=strict.
# Include DATA_DIR and DB directory by default, and /data for optional
# first-run netdisk paths like /data/admin-1.
SERVICE_READWRITE_PATHS="${SERVICE_READWRITE_PATHS:-${DATA_DIR} ${MUDP_DB_DIR} /data}"
MUDP_ADDR="${MUDP_ADDR:-0.0.0.0:9000}"
MUDP_SESSION_SECRET="${MUDP_SESSION_SECRET:-}"   # empty → generated at first start (not recommended for production)
MUDP_ADMIN_USER="${MUDP_ADMIN_USER:-admin}"
MUDP_ADMIN_PASSWORD="${MUDP_ADMIN_PASSWORD:-}"   # empty → first-run web wizard
ENV_FILE="/etc/mudp/mudp.env"
UNIT_FILE="/etc/systemd/system/${SERVICE_NAME}.service"

# Prevent a common misconfiguration: placing the binary at the same path as
# DATA_DIR (for example, passing /data/mudp as the binary while DATA_DIR is
# also /data/mudp).
if [[ "$MUDP_BIN" == "$DATA_DIR" ]]; then
    die "MUDP binary path and DATA_DIR are identical: $MUDP_BIN. Move the binary (for example /data/mudp-bin/mudp) or set DATA_DIR to another directory."
fi
if [[ "$MUDP_BIN" == "$DATA_DIR"/* ]]; then
    die "MUDP binary path is inside DATA_DIR: $MUDP_BIN (DATA_DIR=$DATA_DIR). Use separate paths for binary and data directory."
fi

if [[ -z "$MUDP_SESSION_SECRET" ]]; then
    # Generate a stable secret so cookies survive service restarts
    MUDP_SESSION_SECRET="$(openssl rand -hex 32 2>/dev/null || cat /dev/urandom | tr -dc 'a-f0-9' | head -c 64)"
fi

# ---------------------------------------------------------------------------
# Use the existing system user
# ---------------------------------------------------------------------------
info "Using service user: $MUDP_USER (group: $MUDP_GROUP, home: $MUDP_HOME)"

# Add service user to docker group so it can talk to the Docker socket
if getent group docker &>/dev/null; then
    usermod -aG docker "$MUDP_USER"
    info "Added '$MUDP_USER' to the 'docker' group."
else
    echo "WARNING: 'docker' group not found. Make sure Docker is installed and '$MUDP_USER' can access the Docker socket."
fi

# Warn early when users plan to use /data as netdisk root. systemd may allow
# writes via ReadWritePaths, but filesystem permissions still apply.
if [[ -d "/data" ]] && ! su -s /bin/sh -c 'test -w /data' "$MUDP_USER"; then
    echo "WARNING: /data is not writable by user '$MUDP_USER'."
    echo "         If setup uses /data/admin-1, grant write permission first (for example: chown -R $MUDP_USER:$MUDP_USER /data)."
fi

# ---------------------------------------------------------------------------
# Stop old service and uninstall previous files before fresh install
# NOTE: Database and data directory are PRESERVED during upgrade
# ---------------------------------------------------------------------------
if systemctl list-unit-files --type=service --all 2>/dev/null | awk '{print $1}' | grep -Fxq "${SERVICE_NAME}.service" || [[ -f "$UNIT_FILE" ]]; then
    info "Existing service detected. Stopping and upgrading ${SERVICE_NAME} service..."
    info "Database and data will be preserved."
    systemctl stop "${SERVICE_NAME}" 2>/dev/null || true
    systemctl disable "${SERVICE_NAME}" 2>/dev/null || true
    rm -f "$UNIT_FILE"
    rm -f "$ENV_FILE"
    rm -f "$INSTALL_DIR/mudp"
    systemctl daemon-reload
    systemctl reset-failed "${SERVICE_NAME}" 2>/dev/null || true
fi

# ---------------------------------------------------------------------------
# Install binary
# ---------------------------------------------------------------------------
info "Installing binary to $INSTALL_DIR/mudp ..."
mkdir -p "$INSTALL_DIR"
cp "$MUDP_BIN" "$INSTALL_DIR/mudp"
chown root:root "$INSTALL_DIR/mudp"
chmod 755 "$INSTALL_DIR/mudp"

# ---------------------------------------------------------------------------
# Create data directory
# ---------------------------------------------------------------------------
info "Creating data directory $DATA_DIR ..."
mkdir -p "$DATA_DIR"
chown "$MUDP_USER":"$MUDP_GROUP" "$DATA_DIR"
chmod 750 "$DATA_DIR"

info "Creating database directory $MUDP_DB_DIR ..."
mkdir -p "$MUDP_DB_DIR"
chown "$MUDP_USER":"$MUDP_GROUP" "$MUDP_DB_DIR"
chmod 750 "$MUDP_DB_DIR"

# ---------------------------------------------------------------------------
# Write environment file (contains secrets — mode 640)
# ---------------------------------------------------------------------------
info "Writing environment file to $ENV_FILE ..."
mkdir -p /etc/mudp
cat > "$ENV_FILE" <<EOF
MUDP_ADDR=${MUDP_ADDR}
MUDP_DB=${MUDP_DB_PATH}
MUDP_SESSION_SECRET=${MUDP_SESSION_SECRET}
EOF
if [[ -n "$MUDP_ADMIN_PASSWORD" ]]; then
    {
        echo "MUDP_ADMIN_USER=${MUDP_ADMIN_USER}"
        echo "MUDP_ADMIN_PASSWORD=${MUDP_ADMIN_PASSWORD}"
    } >> "$ENV_FILE"
fi
chown root:"$MUDP_GROUP" "$ENV_FILE"
chmod 640 "$ENV_FILE"

# ---------------------------------------------------------------------------
# Write systemd unit file
# ---------------------------------------------------------------------------
info "Writing systemd unit: $UNIT_FILE ..."
cat > "$UNIT_FILE" <<EOF
[Unit]
Description=MUDP — Multi-User Docker Management Platform
Documentation=https://github.com/your-org/mudp
After=network.target docker.service
Wants=docker.service

[Service]
Type=simple
User=${MUDP_USER}
Group=${MUDP_GROUP}
WorkingDirectory=${DATA_DIR}
EnvironmentFile=${ENV_FILE}
ExecStart=${INSTALL_DIR}/mudp
Restart=on-failure
RestartSec=5s

# Hardening
NoNewPrivileges=true
ProtectSystem=strict
ReadWritePaths=${SERVICE_READWRITE_PATHS}
PrivateTmp=true

[Install]
WantedBy=multi-user.target
EOF

chmod 644 "$UNIT_FILE"

# ---------------------------------------------------------------------------
# Enable and start the service
# ---------------------------------------------------------------------------
info "Reloading systemd daemon..."
systemctl daemon-reload

info "Enabling service '${SERVICE_NAME}' to start on boot..."
systemctl enable "${SERVICE_NAME}"

info "Starting service '${SERVICE_NAME}'..."
systemctl restart "${SERVICE_NAME}"

# ---------------------------------------------------------------------------
# Status
# ---------------------------------------------------------------------------
sleep 1
echo ""
echo "===== Service status ====="
systemctl status "${SERVICE_NAME}" --no-pager || true
echo ""
echo "Installation complete."
echo "  Web UI:        http://<host>:${MUDP_ADDR##*:}"
echo "  Logs:          journalctl -u ${SERVICE_NAME} -f"
echo "  Data dir:      ${DATA_DIR}"
echo "  Database:      ${MUDP_DB_PATH}"
echo "  Env file:      ${ENV_FILE}"
echo ""
echo "Uninstall options:"
echo "  Safe uninstall (keep data):   systemctl disable --now ${SERVICE_NAME} && rm ${UNIT_FILE} ${INSTALL_DIR}/mudp ${ENV_FILE}"
echo "  Full uninstall (remove data): systemctl disable --now ${SERVICE_NAME} && rm -rf ${UNIT_FILE} ${INSTALL_DIR}/mudp ${ENV_FILE} ${DATA_DIR}"
