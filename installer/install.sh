#!/usr/bin/env bash
# Slipstream installer — Ubuntu 24.04 LTS (amd64).
#
#   curl -fsSL https://get.slipstream.example | sudo bash
#
# This script is the bootstrap: it verifies the machine, installs the data
# plane (nginx, php-fpm, mariadb, restic, certbot, wp-cli), installs the
# Slipstream binaries, creates users and directories, generates secrets,
# starts services, and prints the one-time setup URL.
set -euo pipefail

SLIPSTREAM_VERSION="${SLIPSTREAM_VERSION:-1.0.0}"
BIN_DIR=/usr/local/bin
RELEASE_URL="${SLIPSTREAM_RELEASE_URL:-https://releases.slipstream.example/${SLIPSTREAM_VERSION}}"

log()  { echo -e "\033[1;36m[slipstream]\033[0m $*"; }
fail() { echo -e "\033[1;31m[slipstream] ERROR:\033[0m $*" >&2; exit 1; }

# ---------- preflight ----------
[[ $EUID -eq 0 ]] || fail "run as root: curl -fsSL … | sudo bash"

source /etc/os-release || fail "cannot detect OS"
case "$ID-$VERSION_ID" in
  ubuntu-24.04) PHP_VERSION="8.4"; NEED_PHP_PPA=1 ;;  # 24.04 ships 8.3; 8.4 via ondrej PPA
  ubuntu-26.04) PHP_VERSION="8.5"; NEED_PHP_PPA=0 ;;  # 26.04 ships 8.5 natively
  *) fail "Slipstream supports Ubuntu 24.04 / 26.04 LTS only (found $PRETTY_NAME)" ;;
esac
[[ "$(uname -m)" == "x86_64" ]] || fail "Slipstream supports amd64 only for now"

MEM_MB=$(awk '/MemTotal/ {print int($2/1024)}' /proc/meminfo)
[[ $MEM_MB -ge 1500 ]] || fail "at least 2 GB of RAM required (found ${MEM_MB} MB)"

DISK_MB=$(df -m / | awk 'NR==2 {print $4}')
[[ $DISK_MB -ge 10000 ]] || fail "at least 10 GB free disk required"

for port in 80 443 5252; do
  if ss -ltn "sport = :$port" | grep -q LISTEN; then
    fail "port $port is already in use — Slipstream must own the web ports"
  fi
done

command -v cpanel >/dev/null 2>&1 && fail "another control panel is installed on this machine"

log "Preflight passed: ${PRETTY_NAME}, PHP ${PHP_VERSION}, ${MEM_MB} MB RAM, ports free."

# ---------- packages ----------
export DEBIAN_FRONTEND=noninteractive
log "Installing packages (nginx, PHP ${PHP_VERSION}, MariaDB, restic, certbot)…"
apt-get update -qq
apt-get install -y -qq software-properties-common curl gnupg ca-certificates >/dev/null
if [[ "$NEED_PHP_PPA" == "1" ]]; then
  add-apt-repository -y ppa:ondrej/php >/dev/null 2>&1 || true
  apt-get update -qq
fi
apt-get install -y -qq \
  nginx mariadb-server redis-server restic certbot \
  "php${PHP_VERSION}-fpm" "php${PHP_VERSION}-mysql" "php${PHP_VERSION}-curl" \
  "php${PHP_VERSION}-gd" "php${PHP_VERSION}-intl" "php${PHP_VERSION}-mbstring" \
  "php${PHP_VERSION}-soap" "php${PHP_VERSION}-xml" "php${PHP_VERSION}-zip" \
  "php${PHP_VERSION}-imagick" "php${PHP_VERSION}-bcmath" "php${PHP_VERSION}-redis" \
  "php${PHP_VERSION}-apcu" \
  >/dev/null

# Redis stays off until a site enables object caching.
systemctl disable --now redis-server >/dev/null 2>&1 || true

# wp-cli
if ! command -v wp >/dev/null; then
  curl -fsSL -o "$BIN_DIR/wp" https://raw.githubusercontent.com/wp-cli/builds/gh-pages/phar/wp-cli.phar
  chmod +x "$BIN_DIR/wp"
fi

# ---------- users, directories, secrets ----------
log "Creating users, directories and secrets…"
id -u slipstream >/dev/null 2>&1 || useradd --system --no-create-home --shell /usr/sbin/nologin slipstream

install -d -m 0755 /srv/sites /var/log/slipstream /var/cache/slipstream
install -d -m 0750 -o slipstream -g slipstream /var/lib/slipstream
install -d -m 0755 /var/www/slipstream-acme /run/slipstream /run/slipstream/php
install -d -m 0700 /var/lib/slipstream/work
# panel-api (user: slipstream) must traverse into /etc/slipstream to read
# the agent token and panel TLS certificate.
install -d -m 0750 -g slipstream /etc/slipstream /etc/slipstream/certs

echo "SLIPSTREAM_PHP_VERSION=${PHP_VERSION}" > /etc/slipstream/panel.env
chmod 0644 /etc/slipstream/panel.env

if [[ ! -f /etc/slipstream/agent.token ]]; then
  head -c 48 /dev/urandom | base64 | tr -d '/+=' | head -c 48 > /etc/slipstream/agent.token
  chmod 0640 /etc/slipstream/agent.token
  chgrp slipstream /etc/slipstream/agent.token
fi

# Self-signed bootstrap certificates: panel TLS + vhost fallback before ACME.
if [[ ! -f /etc/slipstream/certs/panel.pem ]]; then
  openssl req -x509 -newkey rsa:2048 -nodes -days 3650 \
    -keyout /etc/slipstream/certs/panel.key -out /etc/slipstream/certs/panel.pem \
    -subj "/CN=slipstream-panel" >/dev/null 2>&1
  chgrp slipstream /etc/slipstream/certs/panel.key /etc/slipstream/certs/panel.pem
  chmod 0640 /etc/slipstream/certs/panel.key
fi
if [[ ! -f /etc/slipstream/certs/fallback.pem ]]; then
  openssl req -x509 -newkey rsa:2048 -nodes -days 3650 \
    -keyout /etc/slipstream/certs/fallback.key -out /etc/slipstream/certs/fallback.pem \
    -subj "/CN=fallback.invalid" >/dev/null 2>&1
  chmod 0644 /etc/slipstream/certs/fallback.pem
  chmod 0640 /etc/slipstream/certs/fallback.key
  chgrp www-data /etc/slipstream/certs/fallback.key 2>/dev/null || true
fi

# ---------- binaries ----------
if [[ -n "${SLIPSTREAM_LOCAL_BUILD:-}" ]]; then
  log "Installing binaries from local build ${SLIPSTREAM_LOCAL_BUILD}…"
  install -m 0755 "${SLIPSTREAM_LOCAL_BUILD}/panel-api" "${SLIPSTREAM_LOCAL_BUILD}/panel-agent" "${SLIPSTREAM_LOCAL_BUILD}/slipctl" "$BIN_DIR/"
else
  log "Downloading Slipstream ${SLIPSTREAM_VERSION}…"
  for bin in panel-api panel-agent slipctl; do
    curl -fsSL -o "$BIN_DIR/$bin" "${RELEASE_URL}/${bin}"
    curl -fsSL -o "/tmp/${bin}.sha256" "${RELEASE_URL}/${bin}.sha256"
    (cd "$BIN_DIR" && sha256sum -c "/tmp/${bin}.sha256" >/dev/null) || fail "checksum mismatch for ${bin}"
    chmod 0755 "$BIN_DIR/$bin"
  done
fi

# ---------- base nginx configuration ----------
log "Configuring nginx base…"
rm -f /etc/nginx/sites-enabled/default
# The panel owns everything under conf.d/slipstream-* and sites-enabled/.
nginx -t >/dev/null

# ---------- database tuning ----------
# The agent re-tunes on demand; give MariaDB a sane starting point now.
systemctl enable --now mariadb >/dev/null

# ---------- services ----------
log "Installing systemd services…"
for unit in slipstream-agent slipstream-api; do
  if [[ -n "${SLIPSTREAM_LOCAL_BUILD:-}" ]]; then
    install -m 0644 "${SLIPSTREAM_LOCAL_BUILD}/../installer/systemd/${unit}.service" /etc/systemd/system/
  else
    curl -fsSL -o "/etc/systemd/system/${unit}.service" "${RELEASE_URL}/${unit}.service"
  fi
done
systemctl daemon-reload
systemctl enable --now slipstream-agent slipstream-api nginx "php${PHP_VERSION}-fpm" >/dev/null

# ---------- firewall ----------
if command -v ufw >/dev/null && ufw status | grep -q "Status: active"; then
  log "Opening firewall ports 22, 80, 443, 5252…"
  ufw allow 22/tcp >/dev/null
  ufw allow 80/tcp >/dev/null
  ufw allow 443/tcp >/dev/null
  ufw allow 5252/tcp >/dev/null
fi

# Per-site SFTP: a chrooted internal-sftp subsystem for slip-site-* users so
# customers can move files without a shell or access to other sites.
if ! grep -q "Slipstream SFTP" /etc/ssh/sshd_config 2>/dev/null; then
  cat >> /etc/ssh/sshd_config <<'SSHD'

# Slipstream SFTP — chrooted, no shell, per-site isolation
Match User slip-site-*
    ChrootDirectory %h
    ForceCommand internal-sftp
    AllowTcpForwarding no
    X11Forwarding no
    PasswordAuthentication yes
SSHD
  systemctl reload ssh 2>/dev/null || systemctl reload sshd 2>/dev/null || true
fi

# ---------- done ----------
IP=$(curl -fsS -4 --max-time 5 https://api.ipify.org 2>/dev/null || hostname -I | awk '{print $1}')
sleep 2
SETUP_URL=$(journalctl -u slipstream-api --since "1 min ago" -o cat | grep -o '"url":"[^"]*"' | tail -1 | cut -d'"' -f4 || true)
SETUP_URL=${SETUP_URL/<server-ip>/$IP}

echo
log "Installation complete."
echo
echo "  Open:  ${SETUP_URL:-https://$IP:5252}"
echo
echo "  The setup link expires in 20 minutes."
echo "  (Your browser will warn about the self-signed certificate — expected on first boot.)"
echo
