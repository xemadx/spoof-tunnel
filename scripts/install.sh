#!/usr/bin/env bash
set -euo pipefail
[[ $EUID -eq 0 ]] || { echo 'Run as root.' >&2; exit 1; }
[[ $(uname -s) == Linux ]] || { echo 'Linux required.' >&2; exit 1; }
command -v systemctl >/dev/null || { echo 'systemd required.' >&2; exit 1; }
command -v ip >/dev/null || { echo 'Install iproute2 first.' >&2; exit 1; }
[[ -c /dev/net/tun ]] || { echo '/dev/net/tun unavailable; enable TUN on the VPS.' >&2; exit 1; }

src=$(cd "$(dirname "$0")" && pwd)
for file in spoof-l3 spoof-panel setup-credentials.sh; do
  [[ -f "$src/$file" ]] || { echo "Missing $src/$file; use the built dist package." >&2; exit 1; }
done

# Verify checksums. The list may carry CRLF if it was produced on Windows,
# so strip carriage returns before feeding it to sha256sum.
if [[ -f "$src/SHA256SUMS" ]]; then
  if ( cd "$src" && tr -d '\r' < SHA256SUMS | sha256sum -c - >/dev/null 2>&1 ); then
    echo "  ✓ Package checksums verified"
  else
    echo 'Checksum verification failed; refusing to install a corrupted package.' >&2
    exit 1
  fi
fi

# Reject incompatible/corrupted executables before stopping an existing service.
"$src/spoof-panel" --version >/dev/null
"$src/spoof-l3" --version >/dev/null

install -d -m 700 /etc/spoof-panel
install -d -m 755 /opt/spoof-panel

# Clean up the legacy service/binary layout from older releases.
systemctl stop emad-spoof-panel.service 2>/dev/null || true
systemctl disable emad-spoof-panel.service 2>/dev/null || true
rm -f /etc/systemd/system/emad-spoof-panel.service
rm -rf /opt/emad-spoof

# Stop the current service before replacing a running binary.
systemctl stop spoof-panel.service 2>/dev/null || true

install -m 755 "$src/spoof-l3" /etc/spoof-panel/spoof-l3
install -m 755 "$src/spoof-panel" /opt/spoof-panel/spoof-panel

# Read from /dev/tty even when this installer arrives through curl | bash.
source "$src/setup-credentials.sh"
setup_output=$(spoof_setup_credentials /opt/spoof-panel/spoof-panel /etc/spoof-panel)
# Make the documented CLI available without replacing a custom regular file.
if [[ ! -e /usr/local/bin/spoof-panel || -L /usr/local/bin/spoof-panel ]]; then
  install -d -m 755 /usr/local/bin
  ln -sfn /opt/spoof-panel/spoof-panel /usr/local/bin/spoof-panel
fi

cat > /etc/systemd/system/spoof-panel.service <<'UNIT'
[Unit]
Description=Spoof Tunnel Panel
After=network-online.target
Wants=network-online.target

[Service]
Type=simple
Environment=SPOOF_DATA_DIR=/etc/spoof-panel
ExecStart=/opt/spoof-panel/spoof-panel
Restart=on-failure
RestartSec=3
KillMode=mixed
TimeoutStopSec=30
UMask=0077
LimitNOFILE=65536
NoNewPrivileges=true
CapabilityBoundingSet=CAP_NET_ADMIN CAP_NET_RAW CAP_NET_BIND_SERVICE
AmbientCapabilities=CAP_NET_ADMIN CAP_NET_RAW CAP_NET_BIND_SERVICE
ProtectSystem=strict
ReadWritePaths=/etc/spoof-panel
ProtectHome=true
ProtectKernelTunables=true
PrivateTmp=true

[Install]
WantedBy=multi-user.target
UNIT

# A separate fixed service survives the panel's own stop/restart. It never runs
# scripts supplied through the API; the worker verifies both release binaries.
cat > /etc/systemd/system/spoof-panel-update.service <<'UPDATEUNIT'
[Unit]
Description=Verified Spoof Panel release update
After=network-online.target
Wants=network-online.target

[Service]
Type=oneshot
ExecStart=/opt/spoof-panel/spoof-panel --apply-update
TimeoutStartSec=18min
TimeoutStopSec=120
UMask=0077
NoNewPrivileges=true
CapabilityBoundingSet=
ProtectSystem=strict
ReadWritePaths=/etc/spoof-panel /opt/spoof-panel
ProtectHome=true
ProtectKernelTunables=true
PrivateTmp=true
UPDATEUNIT

systemctl daemon-reload
systemctl enable spoof-panel.service >/dev/null
systemctl restart spoof-panel.service
sleep 1

if systemctl is-active --quiet spoof-panel.service; then
  state='running'
else
  echo 'Service failed — inspect: journalctl -u spoof-panel -n 50 --no-pager' >&2
  exit 1
fi


panel_version=$(/opt/spoof-panel/spoof-panel --version 2>/dev/null || echo "unknown")

echo ""
echo "  ✓ Installation complete"
echo "  Version : Spoof Panel $panel_version"
echo "  Service : spoof-panel ($state)"
echo "  Status  : systemctl status spoof-panel"
echo "  Logs    : journalctl -u spoof-panel -f"
echo "  Creds   : cat /etc/spoof-panel/credentials.txt"
printf '%s\n' "$setup_output"
unset setup_output
printf '%s\n' '  Save a displayed new password now; it is not stored in plaintext.'
printf '%s\n' '  Forgot it? Rerun the installer and choose 2 to generate and display a new password.'
echo ""
