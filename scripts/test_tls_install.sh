#!/usr/bin/env bash
# Dev-only self-test for the TLS step of scripts/install.sh.
# NOT shipped in the release package (package.py ships only install.sh and
# setup-credentials.sh). It sources the real TLS functions out of
# scripts/install.sh and runs them against PATH shims (fake systemctl,
# certbot, curl, getent, ss, openssl, python3) and a fake panel binary, so
# no real certificate is ever issued and no real service is touched.
#
# Usage: bash scripts/test_tls_install.sh
set -uo pipefail

REPO=$(cd "$(dirname "$0")/.." && pwd)
FAKE_IP=203.0.113.10
DOMAIN=panel.example.com
PASSED=0
FAILED=0

fail() { echo "  ✗ $1"; FAILED=$((FAILED + 1)); }
pass() { echo "  ✓ $1"; PASSED=$((PASSED + 1)); }
check() { # check <name> <condition...>
  local name="$1"; shift
  if "$@"; then pass "$name"; else fail "$name"; fi
}
contains() { grep -qF -- "$2" "$1"; }
not_contains() { ! grep -qF -- "$2" "$1"; }

# --- PATH shims -----------------------------------------------------------
# Written once; behaviour is driven by env flags so each scenario can flip
# one thing: MOCK_DNS, MOCK_PORT80, MOCK_CERTBOT, MOCK_CURL_HEALTH.
write_shims() {
  local bin="$1"
  mkdir -p "$bin"
  cat > "$bin/systemctl" <<'SHIM'
#!/usr/bin/env bash
echo "systemctl $*" >> "$MOCK_LOG"
[[ "$1" == "is-active" ]] && exit "${MOCK_ACTIVE_FAIL:-0}"
exit 0
SHIM
  cat > "$bin/certbot" <<'SHIM'
#!/usr/bin/env bash
echo "certbot $*" >> "$MOCK_LOG"
if [[ "$1" == "certonly" ]]; then
  # Match the supported CLI instead of silently accepting arbitrary options.
  # The old permissive shim hid the invalid --register-unspecified option.
  shift
  d=""; challenge=""; standalone=0; noninteractive=0; tos=0; noemail=0
  while [[ $# -gt 0 ]]; do
    case "$1" in
      --standalone) standalone=1; shift ;;
      --non-interactive) noninteractive=1; shift ;;
      --agree-tos) tos=1; shift ;;
      --register-unsafely-without-email) noemail=1; shift ;;
      --preferred-challenges) [[ $# -gt 1 ]] || exit 2; challenge="$2"; shift 2 ;;
      -d) [[ $# -gt 1 ]] || exit 2; d="$2"; shift 2 ;;
      *) echo "certbot: error: unrecognized arguments: $1" >&2; exit 2 ;;
    esac
  done
  if [[ "$standalone$noninteractive$tos$noemail" != 1111 || "$challenge" != http || -z "$d" ]]; then
    echo 'certbot: error: incomplete non-interactive HTTP-01 request' >&2
    exit 2
  fi
  if [[ "${MOCK_CERTBOT:-ok}" == fail ]]; then
    echo "An error occurred while issuing the certificate" >&2
    echo "Failed authorization procedure" >&2
    exit 1
  fi
  mkdir -p "/etc/letsencrypt/live/$d"
  printf 'fake fullchain for %s\n' "$d" > "/etc/letsencrypt/live/$d/fullchain.pem"
  printf 'fake privkey for %s\n' "$d" > "/etc/letsencrypt/live/$d/privkey.pem"
  exit 0
fi
exit 0
SHIM
  cat > "$bin/curl" <<'SHIM'
#!/usr/bin/env bash
echo "curl $*" >> "$MOCK_LOG"
for a in "$@"; do
  case "$a" in
    *api.ipify.org*|*ifconfig.me*) printf '%s' "${MOCK_EGRESS_IP:-$FAKE_IP}"; exit 0 ;;
  esac
done
if [[ "$*" == *--resolve* ]]; then
  if [[ "${MOCK_REQUIRE_DIRECT:-0}" == 1 && ( "$*" != *"--noproxy *"* || "$1" != -q ) ]]; then
    echo 'proxy intercepted the health request' >&2; exit 5
  fi
  count_file="${MOCK_LOG}.health-count"; count=0
  [[ -f "$count_file" ]] && count=$(cat "$count_file")
  count=$((count + 1)); printf '%s' "$count" > "$count_file"
  if (( count <= ${MOCK_HEALTH_DELAY:-0} )); then echo 'connection refused during startup' >&2; exit 7; fi
  case "${MOCK_CURL_HEALTH:-ok}" in
    fail) echo 'health endpoint unavailable' >&2; exit 1 ;;
    badcert) echo 'certificate verify failed' >&2; exit 60 ;;
    html) printf '<html>{"version":"not-a-health-response"}</html>'; exit 0 ;;
  esac
  printf '{"version":"v0.4.7"}'
  exit 0
fi
exit 0
SHIM
  cat > "$bin/getent" <<'SHIM'
#!/usr/bin/env bash
if [[ "${MOCK_DNS:-ok}" == fail ]]; then exit 2; fi
printf '%s %s\n' "${MOCK_DNS_IP:-$FAKE_IP}" "$2"
SHIM
  cat > "$bin/hostname" <<'SHIM'
#!/usr/bin/env bash
if [[ "$1" == "-I" ]]; then
  printf '%s\n' "${MOCK_IFACE_IP:-203.0.113.20}"
  exit 0
fi
exit 0
SHIM
  cat > "$bin/python3" <<'SHIM'
#!/usr/bin/env bash
# Only the installer's gethostbyname fallback is faked; anything else runs
# the real interpreter.
if [[ "$*" == *gethostbyname* ]]; then
  if [[ "${MOCK_DNS:-ok}" == fail ]]; then exit 1; fi
  printf '%s' "${MOCK_DNS_IP:-$FAKE_IP}"
  exit 0
fi
exec /usr/bin/python3 "$@"
SHIM
  cat > "$bin/ss" <<'SHIM'
#!/usr/bin/env bash
if [[ "${MOCK_PORT80:-free}" == busy ]]; then
  printf 'LISTEN 0 4096 0.0.0.0:80 0.0.0.0:*\n'
fi
exit 0
SHIM
  cat > "$bin/openssl" <<'SHIM'
#!/usr/bin/env bash
if [[ "$*" == *-enddate* ]]; then
  printf 'notAfter=Feb  4 12:00:00 2027 GMT\n'
  exit 0
fi
exec /usr/bin/openssl "$@"
SHIM
  cat > "$bin/sleep" <<'SHIM'
#!/usr/bin/env bash
# Simulated HTTP attempts have no real startup latency.
exit 0
SHIM
  chmod +x "$bin"/*
}

# --- Fake panel binary ----------------------------------------------------
# Implements only the flags the installer uses, against a state file in
# $SPOOF_DATA_DIR. --set-tls requires the cert/key files to exist.
write_fake_panel() {
  local bin="$1"
  cat > "$bin/spoof-panel" <<'SHIM'
#!/usr/bin/env bash
state="$SPOOF_DATA_DIR/tls-test-state"
get() { grep "^$1=" "$state" 2>/dev/null | cut -d= -f2- | head -n 1; }
case "$1" in
  --version) echo "v0.4.7" ;;
  --tls-status)
    enabled=$(get enabled); enabled=${enabled:-0}
    valid=0; [[ "$enabled" == 1 ]] && valid=1
    if [[ "${MOCK_STATUS_NUMERIC:-0}" != 1 ]]; then
      [[ "$enabled" == 1 ]] && enabled=true || enabled=false
      [[ "$valid" == 1 ]] && valid=true || valid=false
    fi
    printf 'enabled=%s\nvalid=%s\ndomain=%s\nexpires_unix=%s\nport=18443\nweb_path=abc123\ncert_file=%s\nkey_file=%s\n' \
      "$enabled" "$valid" "$(get domain)" "$(get expires_unix)" "$(get cert_file)" "$(get key_file)"
    ;;
  --set-tls)
    domain=""; cert=""; key=""
    shift
    while [[ $# -gt 0 ]]; do
      case "$1" in
        --tls-domain) domain="$2"; shift 2 ;;
        --tls-cert) cert="$2"; shift 2 ;;
        --tls-key) key="$2"; shift 2 ;;
        *) shift ;;
      esac
    done
    [[ -f "$cert" && -f "$key" ]] || { echo "certificate pair not found" >&2; exit 1; }
    printf 'enabled=1\ndomain=%s\ncert_file=%s\nkey_file=%s\nexpires_unix=%s\n' \
      "$domain" "$cert" "$key" "${MOCK_EXPIRES_UNIX:-1893456000}" > "$state"
    echo "TLS enabled for $domain"
    ;;
  --disable-tls)
    { grep -v '^enabled=' "$state" 2>/dev/null; echo 'enabled=0'; } > "$state.tmp" && mv "$state.tmp" "$state"
    ;;
  *) echo "unknown flag $1" >&2; exit 1 ;;
esac
SHIM
  chmod +x "$bin/spoof-panel"
}

# --- Scenario runner ------------------------------------------------------
# run_case <name> <setup-fn> — the setup function receives the scenario dir
# and writes $dir/answers (canned tty input), $dir/state (initial tls state),
# and any MOCK_* flags into $dir/env. The TLS functions are sourced fresh
# per scenario so SPOOF_TLS_ON starts at 0.
#
# The prompt device is a real pty: the installer writes its prompt to fd 3
# and reads the answer from the same fd, which only works when the two
# directions are independent streams — true on a terminal, false on a plain
# file (a write would clobber the canned answers). The pty helper feeds the
# canned answers into the master side and drains the prompt text.
write_pty_helper() {
  cat > "$1/pty_run.py" <<'PYEOF'
import os, pty, subprocess, sys, threading
answers = open(sys.argv[1], 'rb').read()
pty_out = open(sys.argv[2], 'ab')
master, slave = pty.openpty()
os.write(master, answers)
env = dict(os.environ, SPOOF_TTY=os.ttyname(slave))
proc = subprocess.Popen(sys.argv[3:], env=env)
def drain():
    try:
        while True:
            chunk = os.read(master, 65536)
            if not chunk:
                break
            pty_out.write(chunk)
            pty_out.flush()
    except OSError:
        pass
threading.Thread(target=drain, daemon=True).start()
sys.exit(proc.wait())
PYEOF
}

write_tls_runner() {
  cat > "$1/run_tls.sh" <<'SHEOF'
#!/usr/bin/env bash
set -eo pipefail
# Auto-export: the env file sets MOCK_* flags the PATH shims (child
# processes) must see.
set -a
# shellcheck disable=SC1091
source "$1/env" 2>/dev/null || true
set +a
# Extract the real TLS block from install.sh (header comment through the
# end of spoof_maybe_tls) and source it.
sed -n '/TLS certificates/,/# Verify checksums/p' "$REPO/scripts/install.sh" | sed '$d' > "$1/tls_funcs.sh"
bash -n "$1/tls_funcs.sh" || { echo "extracted TLS block fails bash -n"; exit 1; }
# shellcheck disable=SC1091
source "$1/tls_funcs.sh"
spoof_maybe_tls > "$1/out.txt" 2>&1
echo "SPOOF_TLS_ON=$SPOOF_TLS_ON" >> "$1/out.txt"
printf '║  URL:      http://203.0.113.10:18443/abc123/ ║\n' | spoof_tls_setup_url "$SPOOF_TLS_URL" >> "$1/out.txt"
echo "STATE: $(cat "$SPOOF_DATA_DIR/tls-test-state" 2>/dev/null | tr '\n' ' ')" >> "$1/out.txt"
SHEOF
  chmod +x "$1/run_tls.sh"
}

run_case() {
  local name="$1" setup="$2"
  local dir; dir=$(mktemp -d)
  local bin="$dir/bin" data="$dir/data" log="$dir/mock.log"
  mkdir -p "$bin" "$data"
  : > "$log"
  write_shims "$bin"
  write_fake_panel "$bin"
  write_pty_helper "$dir"
  write_tls_runner "$dir"
  export REPO MOCK_LOG="$log" FAKE_IP PATH="$bin:$PATH"
  export SPOOF_PANEL_BIN="$bin/spoof-panel" SPOOF_DATA_DIR="$data"
  export SPOOF_TTY="$dir/answers"
  : > "$dir/answers"
  : > "$data/tls-test-state"
  printf 'URL: http://203.0.113.30:18443/abc123/\nUsername: preserved-user\nPassword: unchanged (not stored)\n' > "$data/credentials.txt"
  # shellcheck disable=SC1090
  "$setup" "$dir"
  if grep -q '^NO_PTY=1' "$dir/env" 2>/dev/null; then
    bash "$dir/run_tls.sh" "$dir"
  else
    python3 "$dir/pty_run.py" "$dir/answers" "$dir/pty.txt" bash "$dir/run_tls.sh" "$dir"
  fi
  echo "── $name"
  sed 's/^/    /' "$dir/out.txt"
  echo "    [mock calls] $(tr '\n' ';' < "$log")"
  # The assertions live in the scenario's check function.
  "check_$name" "$dir" "$log"
  rm -rf "$dir" "/etc/letsencrypt/live/$DOMAIN"
}

# --- Scenarios ------------------------------------------------------------
scenario_1_setup() { printf 'y\n%s\n' "$DOMAIN" > "$1/answers"; : > "$1/env"; }
check_scenario_1() {
  local dir="$1" log="$2"
  check "scenario 1: certbot issued the certificate" contains "$log" "certbot certonly"
  check "scenario 1: supported no-email registration option" contains "$log" "--register-unsafely-without-email"
  check "scenario 1: invalid registration option absent" not_contains "$log" "--register-unspecified"
  check "scenario 1: panel configured with --set-tls" contains "$dir/out.txt" "TLS enabled for $DOMAIN"
  check "scenario 1: panel restarted" contains "$log" "systemctl restart spoof-panel.service"
  check "scenario 1: HTTPS enabled reported" contains "$dir/out.txt" "✓ HTTPS enabled"
  check "scenario 1: SPOOF_TLS_ON=1" contains "$dir/out.txt" "SPOOF_TLS_ON=1"
  check "scenario 1: state enabled" contains "$dir/out.txt" "enabled=1"
  check "scenario 1: saved URL uses certificate domain" contains "$dir/data/credentials.txt" "URL: https://$DOMAIN:18443/abc123/"
  check "scenario 1: saved account is preserved" contains "$dir/data/credentials.txt" "Username: preserved-user"
  check "scenario 1: credentials box uses certificate domain" contains "$dir/out.txt" "║  URL:      https://$DOMAIN:18443/abc123/"
}
scenario_2_setup() {
  printf 'enabled=1\ndomain=%s\nexpires_unix=1893456000\n' "$DOMAIN" > "$1/data/tls-test-state"
  printf 'y\n%s\n' "$DOMAIN" > "$1/answers"  # must stay unconsumed
  : > "$1/env"
}
check_scenario_2() {
  local dir="$1" log="$2"
  check "scenario 2: no certbot call" not_contains "$log" "certbot"
  check "scenario 2: one quiet status line" contains "$dir/out.txt" "TLS: https://$DOMAIN:18443/abc123/"
  check "scenario 2: answers file untouched" contains "$dir/answers" "$DOMAIN"
  check "scenario 2: SPOOF_TLS_ON=1" contains "$dir/out.txt" "SPOOF_TLS_ON=1"
}
scenario_3_setup() {
  local soon=$(( $(date +%s) + 10 * 86400 ))
  printf 'enabled=1\ndomain=%s\nexpires_unix=%s\n' "$DOMAIN" "$soon" > "$1/data/tls-test-state"
  : > "$1/env"
}
check_scenario_3() {
  local dir="$1" log="$2"
  check "scenario 3: certbot renew attempted" contains "$log" "certbot renew"
  check "scenario 3: renewal reported" contains "$dir/out.txt" "TLS certificate renewed"
}
scenario_4_setup() { printf 'n\n' > "$1/answers"; : > "$1/env"; }
check_scenario_4() {
  local dir="$1" log="$2"
  check "scenario 4: no certbot call" not_contains "$log" "certbot"
  check "scenario 4: keeping HTTP reported" contains "$dir/out.txt" "Keeping plain HTTP"
  check "scenario 4: SPOOF_TLS_ON=0" contains "$dir/out.txt" "SPOOF_TLS_ON=0"
}
scenario_5_setup() { printf 'SPOOF_TTY=/nonexistent\nNO_PTY=1\n' > "$1/env"; }
check_scenario_5() {
  local dir="$1" log="$2"
  check "scenario 5: no certbot call without a tty" not_contains "$log" "certbot"
  check "scenario 5: silent return" not_contains "$dir/out.txt" "سرتیفیکیت"
  check "scenario 5: SPOOF_TLS_ON=0" contains "$dir/out.txt" "SPOOF_TLS_ON=0"
}
scenario_6_setup() { printf 'y\nnot-a-domain\n%s\n' "$DOMAIN" > "$1/answers"; : > "$1/env"; }
check_scenario_6() {
  local dir="$1" log="$2"
  check "scenario 6: bad domain rejected, good one accepted" contains "$dir/pty.txt" "Invalid domain"
  check "scenario 6: certbot called for the good domain" contains "$log" "certbot certonly"
  check "scenario 6: HTTPS enabled" contains "$dir/out.txt" "✓ HTTPS enabled"
}
scenario_7_setup() { printf 'y\n%s\n' "$DOMAIN" > "$1/answers"; echo 'MOCK_DNS=fail' > "$1/env"; }
check_scenario_7() {
  local dir="$1" log="$2"
  check "scenario 7: DNS failure aborts with a fix" contains "$dir/out.txt" "does not resolve to any IP"
  check "scenario 7: no certbot call" not_contains "$log" "certbot"
}
scenario_8_setup() { printf 'y\n%s\n' "$DOMAIN" > "$1/answers"; echo 'MOCK_PORT80=busy' > "$1/env"; }
check_scenario_8() {
  local dir="$1" log="$2"
  check "scenario 8: busy port 80 aborts with a fix" contains "$dir/out.txt" "Port 80 is already in use"
  check "scenario 8: no certbot call" not_contains "$log" "certbot"
}
scenario_9_setup() { printf 'y\n%s\n' "$DOMAIN" > "$1/answers"; echo 'MOCK_CERTBOT=fail' > "$1/env"; }
check_scenario_9() {
  local dir="$1" log="$2"
  check "scenario 9: certbot error tail shown" contains "$dir/out.txt" "Failed authorization procedure"
  check "scenario 9: no --set-tls" not_contains "$log" "TLS enabled"
  check "scenario 9: stays on HTTP" contains "$dir/out.txt" "Panel stays on plain HTTP"
  check "scenario 9: SPOOF_TLS_ON=0" contains "$dir/out.txt" "SPOOF_TLS_ON=0"
}
scenario_10_setup() { printf 'y\n%s\n' "$DOMAIN" > "$1/answers"; echo 'MOCK_CURL_HEALTH=fail' > "$1/env"; }
check_scenario_10() {
  local dir="$1" log="$2"
  check "scenario 10: verify failure disables TLS" contains "$dir/out.txt" "HTTPS verification failed"
  check "scenario 10: state disabled again" contains "$dir/out.txt" "enabled=0"
  check "scenario 10: SPOOF_TLS_ON=0" contains "$dir/out.txt" "SPOOF_TLS_ON=0"
}
# The live case: DNS points at the server's inbound IP, the egress IP
# differs (NAT). The pre-check must pass on the interface candidate.
scenario_11_setup() {
  printf 'y\n%s\n' "$DOMAIN" > "$1/answers"
  printf 'MOCK_DNS_IP=203.0.113.10\nMOCK_IFACE_IP=203.0.113.10\nMOCK_EGRESS_IP=203.0.113.99\n' > "$1/env"
}
check_scenario_11() {
  local dir="$1" log="$2"
  check "scenario 11: no continue prompt when an interface IP matches" not_contains "$dir/pty.txt" "Continue anyway"
  check "scenario 11: certbot called" contains "$log" "certbot certonly"
  check "scenario 11: HTTPS enabled" contains "$dir/out.txt" "✓ HTTPS enabled"
}
# DNS matches none of the server's addresses; answering y lets certbot
# decide, answering n keeps HTTP.
scenario_12_setup() {
  printf 'y\n%s\ny\n' "$DOMAIN" > "$1/answers"
  printf 'MOCK_DNS_IP=203.0.113.10\nMOCK_IFACE_IP=203.0.113.20\nMOCK_EGRESS_IP=203.0.113.99\n' > "$1/env"
}
check_scenario_12() {
  local dir="$1" log="$2"
  check "scenario 12: mismatch explained" contains "$dir/out.txt" "none of this server's addresses"
  check "scenario 12: continue prompt asked" contains "$dir/pty.txt" "Continue anyway"
  check "scenario 12: certbot called after y" contains "$log" "certbot certonly"
  check "scenario 12: HTTPS enabled" contains "$dir/out.txt" "✓ HTTPS enabled"
}
scenario_13_setup() {
  printf 'y\n%s\nn\n' "$DOMAIN" > "$1/answers"
  printf 'MOCK_DNS_IP=203.0.113.10\nMOCK_IFACE_IP=203.0.113.20\nMOCK_EGRESS_IP=203.0.113.99\n' > "$1/env"
}
check_scenario_13() {
  local dir="$1" log="$2"
  check "scenario 13: no certbot call after n" not_contains "$log" "certbot"
  check "scenario 13: keeping HTTP reported" contains "$dir/out.txt" "Keeping plain HTTP"
  check "scenario 13: SPOOF_TLS_ON=0" contains "$dir/out.txt" "SPOOF_TLS_ON=0"
}
# DNS matches the IP the panel URL advertises (credentials.txt).
scenario_14_setup() {
  printf 'y\n%s\n' "$DOMAIN" > "$1/answers"
  printf 'URL: http://203.0.113.10:44571/abc/\n' > "$1/data/credentials.txt"
  printf 'MOCK_DNS_IP=203.0.113.10\nMOCK_IFACE_IP=203.0.113.20\nMOCK_EGRESS_IP=203.0.113.99\n' > "$1/env"
}
check_scenario_14() {
  local dir="$1" log="$2"
  check "scenario 14: no continue prompt when the panel URL IP matches" not_contains "$dir/pty.txt" "Continue anyway"
  check "scenario 14: certbot called" contains "$log" "certbot certonly"
  check "scenario 14: HTTPS enabled" contains "$dir/out.txt" "✓ HTTPS enabled"
}

scenario_15_setup() { scenario_2_setup "$1"; echo 'MOCK_STATUS_NUMERIC=1' > "$1/env"; }
check_scenario_15() {
  check 'scenario 15: numeric legacy status still recognized' contains "$1/out.txt" 'SPOOF_TLS_ON=1'
  check 'scenario 15: legacy status does not reissue' not_contains "$2" 'certbot'
}
scenario_16_setup() { scenario_1_setup "$1"; echo 'MOCK_HEALTH_DELAY=3' > "$1/env"; }
check_scenario_16() {
  check 'scenario 16: waits for HTTPS listener after systemd becomes active' contains "$1/out.txt" '✓ HTTPS enabled'
  check 'scenario 16: delayed startup is not disabled' not_contains "$1/out.txt" 'TLS disabled'
  check 'scenario 16: actual health endpoint retried' contains "${2}.health-count" '4'
}
scenario_17_setup() { scenario_1_setup "$1"; printf 'MOCK_REQUIRE_DIRECT=1\nHTTPS_PROXY=http://127.0.0.1:9\n' > "$1/env"; }
check_scenario_17() {
  check 'scenario 17: health check ignores proxy settings' contains "$1/out.txt" '✓ HTTPS enabled'
  check 'scenario 17: health request explicitly bypasses proxies' contains "$2" '--noproxy *'
}
scenario_18_setup() { scenario_1_setup "$1"; echo 'MOCK_CURL_HEALTH=html' > "$1/env"; }
check_scenario_18() { check 'scenario 18: HTML mentioning version is not health JSON' contains "$1/out.txt" 'HTTPS verification failed'; }
scenario_19_setup() { scenario_1_setup "$1"; echo 'MOCK_CURL_HEALTH=badcert' > "$1/env"; }
check_scenario_19() {
  check 'scenario 19: trust failure is explained' contains "$1/out.txt" 'Certificate trust verification failed'
  check 'scenario 19: no insecure certificate bypass' not_contains "$2" 'curl -k'
  check 'scenario 19: invalid trusted TLS rolls back' contains "$1/out.txt" 'enabled=0'
}

run_case "scenario_1" scenario_1_setup
run_case "scenario_2" scenario_2_setup
run_case "scenario_3" scenario_3_setup
run_case "scenario_4" scenario_4_setup
run_case "scenario_5" scenario_5_setup
run_case "scenario_6" scenario_6_setup
run_case "scenario_7" scenario_7_setup
run_case "scenario_8" scenario_8_setup
run_case "scenario_9" scenario_9_setup
run_case "scenario_10" scenario_10_setup
run_case "scenario_11" scenario_11_setup
run_case "scenario_12" scenario_12_setup
run_case "scenario_13" scenario_13_setup
run_case "scenario_14" scenario_14_setup
run_case "scenario_15" scenario_15_setup
run_case "scenario_16" scenario_16_setup
run_case "scenario_17" scenario_17_setup
run_case "scenario_18" scenario_18_setup
run_case "scenario_19" scenario_19_setup


echo ""
echo "TLS installer self-test: $PASSED passed, $FAILED failed"
[[ "$FAILED" == 0 ]]
