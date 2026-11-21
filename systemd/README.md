# Systemd Service Configuration & Setup

This directory contains configuration files and helper scripts to run `antigravity-telegram-bridge` as a persistent background systemd service on Linux and WSL2.

---

## Files

- `antigravity-telegram-bridge.service` — systemd unit file.
- `install.sh` — automated installation script.
- `README.md` — this documentation.

---

## Quick Installation

Run the install script with root privileges:

```bash
chmod +x /root/antigravity-telegram-bridge/systemd/install.sh
/root/antigravity-telegram-bridge/systemd/install.sh
```

The script will:
1. Compile the Go binary.
2. Install the binary to `/usr/local/bin/antigravity-telegram-bridge`.
3. Verify `config.json` presence.
4. Install the unit file to `/etc/systemd/system/antigravity-telegram-bridge.service`.
5. Execute `systemctl daemon-reload`.
6. Enable the service on system startup and start it immediately (`systemctl enable --now`).

---

## Manual Setup

If you prefer configuring systemd manually:

1. **Copy the service file:**
   ```bash
   cp /root/antigravity-telegram-bridge/systemd/antigravity-telegram-bridge.service /etc/systemd/system/
   ```

2. **Reload systemd:**
   ```bash
   sudo systemctl daemon-reload
   ```

3. **Enable and start:**
   ```bash
   sudo systemctl enable --now antigravity-telegram-bridge
   ```

---

## Useful Systemctl Commands

| Command | Action |
| :--- | :--- |
| `systemctl status antigravity-telegram-bridge` | Check service health and current state |
| `systemctl restart antigravity-telegram-bridge` | Restart the bridge daemon |
| `systemctl stop antigravity-telegram-bridge` | Stop the bridge daemon |
| `systemctl disable antigravity-telegram-bridge` | Disable automatic startup on boot |

---

## Viewing Logs

Logs are appended both to standard output/error and to `/root/antigravity-telegram-bridge/bridge.log`.

- **Live journal logs:**
  ```bash
  journalctl -u antigravity-telegram-bridge -f
  ```

- **File logs:**
  ```bash
  tail -f /root/antigravity-telegram-bridge/bridge.log
  ```

---

## Configuration Reference (`antigravity-telegram-bridge.service`)

```ini
[Unit]
Description=Antigravity Telegram Bridge
After=network.target

[Service]
Type=simple
User=root
WorkingDirectory=/root/antigravity-telegram-bridge
ExecStart=/usr/local/bin/antigravity-telegram-bridge -config /root/antigravity-telegram-bridge/config.json
Restart=always
RestartSec=5
StandardOutput=append:/root/antigravity-telegram-bridge/bridge.log
StandardError=append:/root/antigravity-telegram-bridge/bridge.log

[Install]
WantedBy=multi-user.target
```

If you deploy `antigravity-telegram-bridge` under a non-root user or a different directory path:
1. Adjust `User=` to your local username.
2. Adjust `WorkingDirectory=` and paths in `ExecStart=`, `StandardOutput=`, and `StandardError=`.
