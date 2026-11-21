#!/usr/bin/env bash
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
PROJECT_DIR="$(cd "${SCRIPT_DIR}/.." && pwd)"
SERVICE_NAME="antigravity-telegram-bridge.service"
TARGET_PATH="/etc/systemd/system/${SERVICE_NAME}"
BIN_TARGET="/usr/local/bin/antigravity-telegram-bridge"

echo "==> Installing ${SERVICE_NAME}..."

# Build binary
echo "==> Building binary..."
(cd "${PROJECT_DIR}" && go build -o antigravity-telegram-bridge .)

# Install binary to /usr/local/bin
echo "==> Installing binary to ${BIN_TARGET}..."
if install -m 755 "${PROJECT_DIR}/antigravity-telegram-bridge" "${BIN_TARGET}" 2>/dev/null; then
    echo "==> Binary installed to ${BIN_TARGET}"
else
    echo "==> Warning: Could not write to ${BIN_TARGET} (read-only filesystem or insufficient permissions)."
    echo "==> Please run with sudo: sudo install -m 755 ${PROJECT_DIR}/antigravity-telegram-bridge ${BIN_TARGET}"
fi

# Ensure config.json exists
if [ ! -f "${PROJECT_DIR}/config.json" ]; then
    echo "==> Warning: ${PROJECT_DIR}/config.json not found! Copying from config.example.json..."
    cp "${PROJECT_DIR}/config.example.json" "${PROJECT_DIR}/config.json"
    echo "==> Please edit ${PROJECT_DIR}/config.json with your Telegram bot token."
fi

# Copy unit file
if cp "${SCRIPT_DIR}/${SERVICE_NAME}" "${TARGET_PATH}" 2>/dev/null; then
    chmod 644 "${TARGET_PATH}"
    echo "==> Service unit installed to ${TARGET_PATH}"
else
    echo "==> Warning: Could not write to ${TARGET_PATH}. Run with sudo to install unit."
fi

# Reload systemd and enable service if systemd is available
if command -v systemctl >/dev/null 2>&1 && [ -d /run/systemd/system ]; then
    echo "==> Reloading systemd daemon..."
    systemctl daemon-reload

    echo "==> Enabling and starting ${SERVICE_NAME}..."
    systemctl enable --now "${SERVICE_NAME}"

    echo "==> Service status:"
    systemctl status "${SERVICE_NAME}" --no-pager || true
    echo "==> Done. Follow logs with: journalctl -u ${SERVICE_NAME} -f"
else
    echo "==> Note: systemd is not active in this environment (e.g. container). When running on host, run:"
    echo "    sudo systemctl daemon-reload && sudo systemctl enable --now ${SERVICE_NAME}"
fi
