#!/usr/bin/env bash
set -euo pipefail
[[ $EUID -eq 0 ]] || { echo 'Run with sudo bash install.sh' >&2; exit 1; }
[[ $(uname -s) == Linux ]] || { echo 'Linux required.' >&2; exit 1; }
case $(uname -m) in
  x86_64) arch=amd64 ;;
  aarch64|arm64) arch=arm64 ;;
  *) echo 'Supported architectures: amd64, arm64.' >&2; exit 1 ;;
esac
for cmd in curl tar sha256sum; do command -v "$cmd" >/dev/null || { echo "Install $cmd first." >&2; exit 1; }; done
asset="spoof-tunnel-linux-$arch.tar.gz"
if [[ -n "${SPOOF_RELEASE:-}" ]]; then
  [[ "$SPOOF_RELEASE" =~ ^(panel-|emad-)?v[0-9]+\.[0-9]+\.[0-9]+$ ]] || { echo 'Invalid release tag.' >&2; exit 1; }
  base="https://github.com/xemadx/spoof-tunnel/releases/download/$SPOOF_RELEASE"
else
  base='https://github.com/xemadx/spoof-tunnel/releases/latest/download'
fi
work=$(mktemp -d -t spoof-install.XXXXXX)
trap 'rm -rf -- "$work"' EXIT
printf 'Downloading %s\n' "$asset"
curl -fsSL --retry 2 --connect-timeout 15 --max-time 300 --proto '=https' "$base/$asset" -o "$work/$asset"

# Enforce mandatory archive checksum verification
printf 'Downloading SHA256SUMS\n'
curl -fsSL --retry 2 --connect-timeout 15 --max-time 60 --proto '=https' "$base/SHA256SUMS" -o "$work/archive-sums" || {
  echo 'Failed to download SHA256SUMS verification file. Aborting installation.' >&2
  exit 1
}
sum=$(awk -v name="$asset" '$2 == name || $2 == "*"name { print $1 }' "$work/archive-sums")
[[ "$sum" =~ ^[a-fA-F0-9]{64}$ ]] || { echo "Missing or invalid SHA-256 checksum for $asset in SHA256SUMS." >&2; exit 1; }
(cd "$work" && printf '%s  %s\n' "$sum" "$asset" | sha256sum -c -) || {
  echo 'Archive checksum verification failed! Downloaded package may be corrupted or modified.' >&2
  exit 1
}

tar -xzf "$work/$asset" -C "$work"
bash "$work/spoof-tunnel-linux-$arch/install.sh"
