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

# ── TLS certificates (Let's Encrypt via certbot) ──────────────────────────
# The panel serves plain HTTP until a certificate is configured. After the
# password setup and the first service start, spoof_maybe_tls runs:
#   * a valid certificate is already configured → say so in one line and
#     never ask again; renew it when it expires within 30 days;
#   * no certificate → ask on /dev/tty whether to get a free one, ask for
#     the domain, obtain it with certbot (HTTP-01 on port 80), configure
#     the panel with --set-tls, restart, and verify the HTTPS endpoint.
#     Any failure leaves the panel on plain HTTP; nothing is left half
#     configured. Non-interactive runs (no tty) skip silently.
#   * SPOOF_TLS_SKIP=1 skips the whole step; SPOOF_TTY overrides the
#     prompt device (used by the installer self-test).
# A certificate is per server: each server needs its own domain pointing
# at its own public IP. Wildcards are not supported (HTTP-01 cannot
# prove one); the panel keeps its port and path, and advertises the
# certificate domain with https:// instead of the HTTP IP address.
SPOOF_TTY="${SPOOF_TTY:-/dev/tty}"
SPOOF_TLS_ON=0
SPOOF_TLS_URL=""

spoof_tls_status() {
  SPOOF_DATA_DIR="${SPOOF_DATA_DIR:-/etc/spoof-panel}" "${SPOOF_PANEL_BIN:-/opt/spoof-panel/spoof-panel}" --tls-status 2>/dev/null || echo 'enabled=0'
}

spoof_tls_val() {
  # spoof_tls_val <key> <status-output> — value after the first '='.
  printf '%s\n' "$2" | grep "^$1=" | cut -d= -f2- | head -n 1
}

spoof_tls_true() { [[ "$1" == true || "$1" == 1 ]]; }

spoof_tls_publish_url() {
  local domain="$1" port="$2" prefix="${3#/}"
  prefix="${prefix%/}"
  SPOOF_TLS_URL="https://$domain:$port/$prefix/"
  local creds="${SPOOF_DATA_DIR:-/etc/spoof-panel}/credentials.txt" tmp line
  [[ -f "$creds" ]] || return 0
  tmp=$(mktemp "${creds}.XXXXXX") || return 1
  chmod 600 "$tmp" || { rm -f "$tmp"; return 1; }
  while IFS= read -r line || [[ -n "$line" ]]; do
    if [[ "$line" == URL:* ]]; then printf 'URL: %s\n' "$SPOOF_TLS_URL"; else printf '%s\n' "$line"; fi
  done < "$creds" > "$tmp"
  mv -f "$tmp" "$creds"
}

spoof_tls_setup_url() {
  local url="$1" line
  while IFS= read -r line || [[ -n "$line" ]]; do
    if [[ "$line" == '║  URL:'* ]]; then printf '║  URL:      %-58s║\n' "$url"; else printf '%s\n' "$line"; fi
  done
}

spoof_tls_verify() {
  local domain="$1" port="$2" prefix="${3#/}" body code attempt log
  prefix="${prefix%/}"
  [[ "$port" =~ ^[0-9]+$ && "$port" -ge 1 && "$port" -le 65535 && -n "$prefix" ]] || { echo '  ! Invalid local HTTPS health address.'; return 1; }
  log=$(mktemp) || return 1
  local deadline=$((SECONDS + 45))
  # systemd Type=simple becomes active before database/tunnel startup completes.
  # Wait for the actual trusted HTTPS endpoint, not merely an active process.
  for attempt in $(seq 1 30); do
    code=0
    body=$(curl -q -4 --noproxy '*' --resolve "$domain:$port:127.0.0.1" -fsS --connect-timeout 1 --max-time 3 "https://$domain:$port/$prefix/api/health" 2>"$log") || code=$?
    if [[ "$code" == 0 && "$body" =~ ^[[:space:]]*\{[[:space:]]*\"version\"[[:space:]]*:[[:space:]]*\"[^\"]+\"[[:space:]]*\}[[:space:]]*$ ]]; then
      rm -f "$log"; return 0
    fi
    # A broken certificate is not a startup delay. Never bypass TLS validation.
    if [[ "$code" == 60 || "$code" == 77 ]]; then
      echo '  ! Certificate trust verification failed; check ca-certificates and the full certificate chain.'
      break
    fi
    (( SECONDS >= deadline )) && break
    sleep 1
  done
  echo "  ! Local HTTPS health check failed (curl exit $code)."
  [[ "$code" == 0 ]] && echo '    Expected a JSON version response from the panel health endpoint.'
  sed -n '1,4s/^/    /p' "$log"
  rm -f "$log"
  return 1
}

spoof_tls_expiry_human() {
  local expires_unix="$1"
  [[ "$expires_unix" =~ ^[0-9]+$ ]] || { printf 'unknown'; return 0; }
  date -d "@$expires_unix" '+%Y-%m-%d' 2>/dev/null || printf '%s' "$expires_unix"
}

spoof_resolve_domain() {
  local domain="$1" out=""
  if command -v getent >/dev/null; then
    out=$(getent hosts "$domain" 2>/dev/null | awk '{print $1}' | head -n 1)
  fi
  if [[ -z "$out" ]] && command -v python3 >/dev/null; then
    out=$(python3 -c 'import socket,sys; print(socket.gethostbyname(sys.argv[1]))' "$domain" 2>/dev/null || true)
  fi
  printf '%s' "$out"
}

spoof_public_ip() {
  local ip=""
  ip=$(curl -fsS --max-time 10 https://api.ipify.org 2>/dev/null || true)
  [[ -z "$ip" ]] && ip=$(curl -fsS --max-time 10 https://ifconfig.me/ip 2>/dev/null || true)
  printf '%s' "$ip"
}

# Server IP candidates, one per line, best first: addresses actually
# assigned to this host's interfaces (a public one is the inbound IP),
# then the IP the panel URL advertises, then outbound lookups. The
# outbound address (what ipify sees) is only the egress IP: on hosts
# behind NAT it differs from the inbound IP, so it must never be the
# only candidate — a domain correctly pointed at the server would
# otherwise be rejected as "not this server".
spoof_server_ips() {
  local ip
  if command -v hostname >/dev/null; then
    for ip in $(hostname -I 2>/dev/null || true); do
      [[ "$ip" == *:* ]] && continue
      case "$ip" in
        10.*|172.1[6-9].*|172.2[0-9].*|172.3[01].*|192.168.*|100.6[4-9].*|100.7[0-9].*|100.8[0-9].*|100.9[0-9].*|100.1[01][0-9].*|100.12[0-7].*|127.*|169.254.*) ;;
        *) printf '%s\n' "$ip" ;;
      esac
    done
  fi
  local creds="${SPOOF_DATA_DIR:-/etc/spoof-panel}/credentials.txt"
  if [[ -f "$creds" ]]; then
    local url host
    url=$(grep -m1 '^URL: ' "$creds" 2>/dev/null | sed 's/^URL: *//') || true
    host=${url#*://}
    host=${host%%:*}
    host=${host%%/*}
    [[ "$host" =~ ^[0-9]+\.[0-9]+\.[0-9]+\.[0-9]+$ ]] && printf '%s\n' "$host"
  fi
  local out
  out=$(spoof_public_ip)
  [[ -n "$out" ]] && printf '%s\n' "$out"
}

spoof_port80_free() {
  if command -v ss >/dev/null && ss -ltn 2>/dev/null | grep -q ':80 '; then
    return 1
  fi
  if (exec 3<>/dev/tcp/127.0.0.1/80) 2>/dev/null; then
    return 1
  fi
  return 0
}

spoof_ensure_certbot() {
  command -v certbot >/dev/null && return 0
  echo '  Installing certbot…'
  if command -v apt-get >/dev/null; then
    apt-get update -qq && DEBIAN_FRONTEND=noninteractive apt-get install -y -qq certbot || return 1
  elif command -v dnf >/dev/null; then
    dnf install -y -q certbot || return 1
  elif command -v yum >/dev/null; then
    yum install -y -q certbot || return 1
  else
    return 1
  fi
  command -v certbot >/dev/null
}

spoof_maybe_tls() {
  [[ "${SPOOF_TLS_SKIP:-0}" == 1 ]] && return 0
  local status enabled valid expires_unix domain port prefix now
  status=$(spoof_tls_status)
  enabled=$(spoof_tls_val enabled "$status")
  valid=$(spoof_tls_val valid "$status")
  expires_unix=$(spoof_tls_val expires_unix "$status")
  domain=$(spoof_tls_val domain "$status")
  port=$(spoof_tls_val port "$status")
  prefix=$(spoof_tls_val web_path "$status")
  now=$(date +%s)

  # Already has a usable certificate: never ask again. Renew early when
  # it expires within 30 days; the panel picks up the renewed files
  # without a restart.
  if spoof_tls_true "$enabled" && spoof_tls_true "$valid"; then
    SPOOF_TLS_ON=1
    spoof_tls_publish_url "$domain" "$port" "$prefix" || echo '  ! Could not refresh the saved panel URL.'
    if [[ -n "$expires_unix" && $((expires_unix - now)) -lt 2592000 ]]; then
      echo '  TLS certificate expires soon; attempting renewal…'
      if command -v certbot >/dev/null && certbot renew --non-interactive -q 2>/dev/null; then
        echo '  ✓ TLS certificate renewed (the panel picks it up without a restart)'
      else
        echo "  ! Renewal failed; the current certificate is still valid until $(spoof_tls_expiry_human "$expires_unix"). Run 'certbot renew' manually."
      fi
    else
      echo "  TLS: https://$domain:$port/$prefix/ (certificate valid until $(spoof_tls_expiry_human "$expires_unix"))"
    fi
    return 0
  fi

  # No usable certificate. Without a terminal there is nobody to ask.
  # The -c check matters: <> would CREATE a missing path as a regular
  # file (and then read EOF from it), so test the device first.
  if [[ ! -c "$SPOOF_TTY" ]] || ! { exec 3<>"$SPOOF_TTY"; } 2>/dev/null; then
    return 0
  fi
  printf '\nPanel TLS certificate / سرتیفیکیت پنل:\n  No certificate is configured; the panel is served over plain HTTP.\n  سرتیفیکیت نداری — نمی‌خوای بگیری؟ (رایگان، با Let'"'"'s Encrypt؛ دامنه لازم است)\nGet a free certificate now? [y/N]: ' >&3
  local choice=""
  IFS= read -r -t 120 choice <&3 || choice=""
  case "$choice" in
    y|Y|yes|YES) ;;
    *) exec 3>&-; echo '  Keeping plain HTTP. Rerun the installer any time to get a certificate.'; return 0 ;;
  esac

  local domain_input="" tries=0
  while (( tries < 3 )); do
    tries=$((tries + 1))
    printf 'Domain for the certificate / دامنه (مثل panel.example.com): ' >&3
    IFS= read -r -t 120 domain_input <&3 || domain_input=""
    domain_input=$(printf '%s' "$domain_input" | tr '[:upper:]' '[:lower:]' | sed 's/[[:space:]]//g; s/\.$//')
    if [[ "$domain_input" =~ ^([A-Za-z0-9]([A-Za-z0-9-]{0,61}[A-Za-z0-9])?\.)+[A-Za-z]{2,}$ ]] && [[ "$domain_input" != *\** ]] && [[ ${#domain_input} -le 253 ]]; then
      domain="$domain_input"
      break
    fi
    printf '  Invalid domain. Use a fully-qualified domain name, e.g. panel.example.com (no wildcard).\n  دامنه معتبر نیست؛ مثل panel.example.com وارد کن.\n' >&3
    domain=""
  done
  exec 3>&-
  [[ -n "$domain" ]] || { echo '  No valid domain given; keeping plain HTTP.'; return 0; }

  # Pre-checks: certbot would fail later with a cryptic error, so fail
  # fast here with the exact fix. The domain must point at one of this
  # server's addresses; the egress address alone is not proof, because
  # behind NAT it differs from the inbound IP.
  local resolved
  resolved=$(spoof_resolve_domain "$domain")
  if [[ -z "$resolved" ]]; then
    echo "  ! $domain does not resolve to any IP. Add an A record for it first, then rerun the installer."
    return 0
  fi
  local candidates match="" cand
  candidates=$(spoof_server_ips)
  while IFS= read -r cand; do
    [[ -n "$cand" && "$resolved" == "$cand" ]] && match="$cand"
  done <<< "$candidates"
  if [[ -z "$match" ]]; then
    echo "  ! $domain resolves to $resolved, but that is none of this server's addresses I can see:"
    if [[ -n "$candidates" ]]; then
      printf '%s\n' "$candidates" | sed 's/^/      /'
    else
      echo '      (no address found)'
    fi
    echo '    The outbound address (what ipify sees) can differ from the inbound one behind NAT,'
    echo '    so this check can be wrong. certbot itself is the real test: if the domain really'
    echo '    points elsewhere, it will fail and the panel stays on plain HTTP.'
    local proceed=""
    if [[ -c "$SPOOF_TTY" ]] && { exec 3<>"$SPOOF_TTY"; } 2>/dev/null; then
      printf '  Continue anyway and let certbot decide? [y/N]: ' >&3
      IFS= read -r -t 120 proceed <&3 || proceed=""
      exec 3>&-
    fi
    case "$proceed" in
      y|Y|yes|YES) echo '  Continuing; certbot will verify the domain.' ;;
      *) echo '  Keeping plain HTTP. Point the domain at this server, then rerun the installer.'; return 0 ;;
    esac
  fi
  if ! spoof_port80_free; then
    echo '  ! Port 80 is already in use. certbot needs it free for the HTTP-01 challenge.'
    echo '    Stop whatever listens on port 80, then rerun the installer.'
    return 0
  fi
  if ! spoof_ensure_certbot; then
    echo '  ! certbot could not be installed (no apt/dnf/yum, or the install failed).'
    echo '    Install certbot manually, then rerun the installer.'
    return 0
  fi

  echo "  Requesting a certificate for $domain (Let's Encrypt)…"
  local cert_log
  cert_log=$(mktemp)
  if ! certbot certonly --standalone --non-interactive --agree-tos --register-unsafely-without-email --preferred-challenges http -d "$domain" >"$cert_log" 2>&1; then
    echo '  ! certbot could not issue a certificate:'
    tail -n 20 "$cert_log" | sed 's/^/    /'
    rm -f "$cert_log"
    echo '  Panel stays on plain HTTP. Fix the problem above and rerun the installer.'
    return 0
  fi
  rm -f "$cert_log"

  # --set-tls validates the pair (loads, key matches, in date, SAN covers
  # the domain) and writes tls.json; a rejected pair aborts here.
  if ! SPOOF_DATA_DIR="${SPOOF_DATA_DIR:-/etc/spoof-panel}" "${SPOOF_PANEL_BIN:-/opt/spoof-panel/spoof-panel}" --set-tls --tls-domain "$domain" --tls-cert "/etc/letsencrypt/live/$domain/fullchain.pem" --tls-key "/etc/letsencrypt/live/$domain/privkey.pem"; then
    echo '  ! The issued certificate was rejected by the panel; staying on HTTP.'
    return 0
  fi

  systemctl restart spoof-panel.service || true
  local i
  for i in $(seq 1 15); do
    systemctl is-active --quiet spoof-panel.service && break
    sleep 1
  done
  if ! systemctl is-active --quiet spoof-panel.service; then
    echo '  ! Panel did not come back after enabling TLS; disabling TLS and keeping HTTP.'
    SPOOF_DATA_DIR="${SPOOF_DATA_DIR:-/etc/spoof-panel}" "${SPOOF_PANEL_BIN:-/opt/spoof-panel/spoof-panel}" --disable-tls
    systemctl restart spoof-panel.service || true
    return 0
  fi

  # Verify the real thing: the panel answers HTTPS on the domain, with a
  # certificate the system trusts. --resolve keeps the check on loopback
  # (no hairpin, no firewall) while still validating the domain name.
  status=$(spoof_tls_status)
  port=$(spoof_tls_val port "$status")
  prefix=$(spoof_tls_val web_path "$status")
  if ! spoof_tls_verify "$domain" "$port" "$prefix"; then
    echo '  ! HTTPS verification failed; disabling TLS and keeping the panel on HTTP.'
    SPOOF_DATA_DIR="${SPOOF_DATA_DIR:-/etc/spoof-panel}" "${SPOOF_PANEL_BIN:-/opt/spoof-panel/spoof-panel}" --disable-tls
    systemctl restart spoof-panel.service || true
    return 0
  fi

  local expiry
  expiry=$(openssl x509 -in "/etc/letsencrypt/live/$domain/fullchain.pem" -noout -enddate 2>/dev/null | cut -d= -f2-)
  spoof_tls_publish_url "$domain" "$port" "$prefix" || echo '  ! Could not refresh the saved panel URL.'
  SPOOF_TLS_ON=1
  echo ''
  echo '  ✓ HTTPS enabled'
  echo "  Panel URL: https://$domain:$port/$prefix/"
  echo "  Certificate: Let's Encrypt for $domain (valid until ${expiry:-unknown})"
}


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

# TLS step: offer a free certificate when the panel has none; silently
# skip (or renew when expiring soon) when it already has one.
spoof_maybe_tls
# The credentials box captured earlier still shows http:// when TLS was
# just enabled; fix the scheme so the printed URL is the real one.
if [[ "$SPOOF_TLS_ON" == 1 ]]; then
  setup_output=$(printf '%s\n' "$setup_output" | spoof_tls_setup_url "$SPOOF_TLS_URL")
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
