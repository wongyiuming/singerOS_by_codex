#!/usr/bin/env bash
set -euo pipefail

REPO="wongyiuming/singerOS_by_codex"
RELEASE="${1:-edge}"
INSTALL_DIR="${SINGEROS_HOME:-/opt/singeros}"
ARCH="amd64"

. /etc/os-release
case "${ID:-}:${ID_LIKE:-}" in
  ubuntu:*|debian:*) DISTRO="ubuntu" ;;
  centos:*|rhel:*|rocky:*|almalinux:*|*:rhel*|*:fedora*) DISTRO="centos9" ;;
  *) echo "Unsupported distribution: ${ID:-unknown} ${ID_LIKE:-}" >&2; exit 2 ;;
esac

ASSET="singerOS-linux-${ARCH}-${DISTRO}.tar.gz"
BASE="https://github.com/${REPO}/releases/download/${RELEASE}"
TMP="$(mktemp -d)"
trap 'rm -rf "$TMP"' EXIT

curl -fL --retry 3 -o "$TMP/$ASSET" "$BASE/$ASSET"
curl -fL --retry 3 -o "$TMP/SHA256SUMS" "$BASE/SHA256SUMS"
(
  cd "$TMP"
  grep "  $ASSET$" SHA256SUMS | sha256sum -c -
  tar -xzf "$ASSET"
)

mkdir -p "$INSTALL_DIR/recordings" "$INSTALL_DIR/karaoke/assets"
install -m 0755 "$TMP/$DISTRO/singerOS" "$INSTALL_DIR/singerOS.new"
mv -f "$INSTALL_DIR/singerOS.new" "$INSTALL_DIR/singerOS"
install -m 0644 "$TMP/$DISTRO/compose.yaml" "$INSTALL_DIR/compose.yaml"

if [[ "$DISTRO" == "centos9" ]]; then
  docker network inspect singeros_net >/dev/null 2>&1 || docker network create singeros_net >/dev/null
else
  LIVE="${TLS_LIVE_DIR:-/etc/letsencrypt/live/ml.520mall.cc}"
  mkdir -p "$INSTALL_DIR/certs"
  install -m 0644 "$(readlink -f "$LIVE/fullchain.pem")" "$INSTALL_DIR/certs/fullchain.pem"
  install -m 0600 "$(readlink -f "$LIVE/privkey.pem")" "$INSTALL_DIR/certs/privkey.pem"
fi

docker compose -f "$INSTALL_DIR/compose.yaml" up -d --remove-orphans

echo "Installed $RELEASE ($DISTRO) to $INSTALL_DIR"
