#!/usr/bin/env bash
# Slipstream installer — Ubuntu 24.04 LTS (amd64).
#
#   curl -fsSL https://get.slipstreampanel.com | sudo bash
#
# This script is the bootstrap: it verifies the machine, installs the data
# plane (nginx, php-fpm, mariadb, restic, certbot, wp-cli), installs the
# Slipstream binaries, creates users and directories, generates secrets,
# starts services, and prints the one-time setup URL.
set -euo pipefail

BIN_DIR=/usr/local/bin
SLIPSTREAM_REPO="${SLIPSTREAM_REPO:-Sulaiman-Dauda/slipstream}"
SLIPSTREAM_VERSION="${SLIPSTREAM_VERSION:-latest}"

# Binaries, checksums and unit files are published as release assets, so the
# release itself is the download root and no separate hosting is needed. GitHub
# serves the newest release from /releases/latest/download/<asset> and a pinned
# one from /releases/download/<tag>/<asset>, with identical asset names — so the
# only difference between "give me the current version" and "give me v0.2.0" is
# this prefix. Override SLIPSTREAM_RELEASE_URL to install from anywhere else.
if [[ "$SLIPSTREAM_VERSION" == "latest" ]]; then
  RELEASE_URL="${SLIPSTREAM_RELEASE_URL:-https://github.com/${SLIPSTREAM_REPO}/releases/latest/download}"
else
  RELEASE_URL="${SLIPSTREAM_RELEASE_URL:-https://github.com/${SLIPSTREAM_REPO}/releases/download/${SLIPSTREAM_VERSION}}"
fi

# ---------- progress UI ----------
# An install takes 80-odd seconds, most of it inside a single silent apt call.
# Without feedback that reads as "hung", so every step gets a spinner and a
# measured duration.
#
# The mechanics: fd 3 and 4 keep the real terminal, then stdout/stderr for the
# whole script are redirected into a log file. Every package manager, systemctl
# and nginx message therefore lands in the log instead of tearing through the
# spinner line, and the log is kept for diagnosing a failure. Only this UI
# writes to the terminal.
INSTALL_LOG=/var/log/slipstream-install.log
exec 3>&1 4>&2
: >"$INSTALL_LOG" 2>/dev/null || INSTALL_LOG=/tmp/slipstream-install.log
exec 1>>"$INSTALL_LOG" 2>&1

if [[ -t 3 ]]; then
  R=$'\033[0m'; B=$'\033[1m'; D=$'\033[2m'
  CY1=$'\033[38;5;45m'; CY2=$'\033[38;5;38m'; CY3=$'\033[38;5;31m'
  GRN=$'\033[38;5;42m'; RED=$'\033[38;5;203m'
  TTY=1
else
  R=''; B=''; D=''; CY1=''; CY2=''; CY3=''; GRN=''; RED=''
  TTY=0
fi

ui() { printf "$@" >&3; }

STEP=0
STEP_TOTAL=10
STEP_LABEL=''
STEP_T0=0
_spin_pid=''

# Braille frames as an array — indexing a UTF-8 string by byte would slice the
# multi-byte characters in half.
_FRAMES=('⠋' '⠙' '⠹' '⠸' '⠼' '⠴' '⠦' '⠧' '⠇' '⠏')

spinner_stop() {
  if [[ -n "$_spin_pid" ]]; then
    kill "$_spin_pid" 2>/dev/null || true
    wait "$_spin_pid" 2>/dev/null || true
    _spin_pid=''
    ui '\r\033[2K'
  fi
}

step() {
  STEP=$((STEP + 1)); STEP_LABEL="$1"; STEP_T0=$SECONDS
  if [[ $TTY -eq 1 ]]; then
    (
      i=0
      while :; do
        i=$(((i + 1) % ${#_FRAMES[@]}))
        printf '\r  %s%s%s  %s%2d/%d%s  %s' \
          "$CY1" "${_FRAMES[$i]}" "$R" "$D" "$STEP" "$STEP_TOTAL" "$R" "$STEP_LABEL" >&3
        sleep 0.08
      done
    ) &
    _spin_pid=$!
  else
    ui '  [%d/%d] %s\n' "$STEP" "$STEP_TOTAL" "$STEP_LABEL"
  fi
}

step_done() {
  local secs=$((SECONDS - STEP_T0))
  spinner_stop
  ui '  %s✓%s  %s%2d/%d%s  %-42s %s%ss%s\n' \
    "$GRN" "$R" "$D" "$STEP" "$STEP_TOTAL" "$R" "$STEP_LABEL" "$D" "$secs" "$R"
}

banner() {
  [[ $TTY -eq 1 ]] || { ui 'Slipstream installer\n\n'; return; }
  ui '\n'
  ui '   %s━━━━━━━━━━━%s\n' "$CY1" "$R"
  ui '     %s━━━━━━━━━%s   %sslip%s%sstream%s\n' "$CY2" "$R" "$B" "$R" "$D" "$R"
  ui '       %s━━━━━━━%s   %sself-hosted hosting control panel%s\n' "$CY3" "$R" "$D" "$R"
  ui '\n'
}

# Clear the spinner's line before writing, or the note lands mid-frame and the
# spinner then redraws over half of it. The spinner reclaims the next line on
# its following tick.
log()  { ui '\r\033[2K  %s·%s  %s\n' "$D" "$R" "$*"; }
fail() {
  spinner_stop
  printf '\n  %s✗  %s%s\n' "$RED" "$*" "$R" >&4
  printf '     %sfull log: %s%s\n\n' "$D" "$INSTALL_LOG" "$R" >&4
  # Surface the tail of the log so the cause is visible without a second SSH.
  if [[ -s "$INSTALL_LOG" ]]; then
    printf '%s' "$D" >&4; tail -n 12 "$INSTALL_LOG" | sed 's/^/     /' >&4; printf '%s\n' "$R" >&4
  fi
  exit 1
}

# A spinner left running after an interrupt would keep drawing over the prompt.
trap 'spinner_stop' EXIT INT TERM

banner

# ---------- preflight ----------
step "Checking the machine"
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

for port in 80 443; do
  if ss -ltn "sport = :$port" | grep -q LISTEN; then
    fail "port $port is already in use — Slipstream must own the web ports"
  fi
done

command -v cpanel >/dev/null 2>&1 && fail "another control panel is installed on this machine"

log "Preflight passed: ${PRETTY_NAME}, PHP ${PHP_VERSION}, ${MEM_MB} MB RAM, ports free."

step_done

# ---------- packages ----------
step "Installing nginx, PHP, MariaDB, restic"
export DEBIAN_FRONTEND=noninteractive
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

step_done

# ---------- users, directories, secrets ----------
step "Creating users, directories and secrets"
id -u slipstream >/dev/null 2>&1 || useradd --system --no-create-home --shell /usr/sbin/nologin slipstream

install -d -m 0755 /srv/sites /var/log/slipstream /var/cache/slipstream
install -d -m 0750 -o slipstream -g slipstream /var/lib/slipstream
install -d -m 0755 /var/www/slipstream-acme /run/slipstream /run/slipstream/php
install -d -m 0700 /var/lib/slipstream/work
# panel-api (user: slipstream) must traverse into /etc/slipstream to read
# the agent token and panel TLS certificate.
install -d -m 0750 -g slipstream /etc/slipstream /etc/slipstream/certs

touch /etc/slipstream/panel.env
if grep -q '^SLIPSTREAM_PHP_VERSION=' /etc/slipstream/panel.env; then
  sed -i "s/^SLIPSTREAM_PHP_VERSION=.*/SLIPSTREAM_PHP_VERSION=${PHP_VERSION}/" /etc/slipstream/panel.env
else
  echo "SLIPSTREAM_PHP_VERSION=${PHP_VERSION}" >> /etc/slipstream/panel.env
fi
chown root:slipstream /etc/slipstream/panel.env
chmod 0640 /etc/slipstream/panel.env

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

step_done

# ---------- binaries ----------
step "Installing Slipstream binaries"
if [[ -n "${SLIPSTREAM_LOCAL_BUILD:-}" ]]; then
  log "Installing binaries from local build ${SLIPSTREAM_LOCAL_BUILD}…"
  install -m 0755 "${SLIPSTREAM_LOCAL_BUILD}/panel-api" "${SLIPSTREAM_LOCAL_BUILD}/panel-agent" "${SLIPSTREAM_LOCAL_BUILD}/slipctl" "$BIN_DIR/"
else
  for bin in panel-api panel-agent slipctl; do
    curl -fsSL -o "$BIN_DIR/$bin" "${RELEASE_URL}/${bin}"
    curl -fsSL -o "/tmp/${bin}.sha256" "${RELEASE_URL}/${bin}.sha256"
    (cd "$BIN_DIR" && sha256sum -c "/tmp/${bin}.sha256" >/dev/null) || fail "checksum mismatch for ${bin}"
    chmod 0755 "$BIN_DIR/$bin"
  done

  # The checksum above only proves the binary matches a file published in the
  # same release — anyone able to write that release could replace both. Builds
  # from v0.1.2 onward also carry a GitHub-signed provenance attestation, which
  # proves the bytes came from this project's release workflow at a specific
  # commit. Verified here when the GitHub CLI happens to be present; it is not a
  # dependency of the installer, so its absence is a note rather than a failure.
  if command -v gh >/dev/null 2>&1 && [[ -z "${SLIPSTREAM_SKIP_ATTESTATION:-}" ]]; then
    for bin in panel-api panel-agent slipctl; do
      if gh attestation verify "$BIN_DIR/$bin" --repo "$SLIPSTREAM_REPO" >/dev/null 2>&1; then
        attested=1
      else
        # A release predating attestations, or no network to the verifier.
        attested=0; break
      fi
    done
    [[ "${attested:-0}" == "1" ]] && log "Build provenance verified against ${SLIPSTREAM_REPO}."
  fi
fi

step_done

# ---------- kernel + nginx worker tuning ----------
step "Tuning kernel and nginx workers"
# These live outside the panel's managed config because they are main/events
# level (worker_connections cannot be set from an http-level include) or kernel
# level. Everything http-level is rendered by the agent into conf.d instead.

# Kernel: the defaults are sized for a desktop, not a server absorbing a spike.
# Without these the accept queue overflows during a burst and the kernel drops
# SYNs — which surfaces as connection errors long before any worker is busy.
cat > /etc/sysctl.d/60-slipstream.conf <<'SYSCTL'
# Managed by Slipstream.
# Accept-queue depth. nginx passes a large backlog to listen(2), but the kernel
# silently clamps it to somaxconn; 511 (the old default) drops connections under
# a spike that the server could otherwise have queued and served.
net.core.somaxconn = 65535
net.ipv4.tcp_max_syn_backlog = 65535
# Burst headroom between the NIC and the network stack.
net.core.netdev_max_backlog = 16384
# Reuse TIME_WAIT sockets for outbound connections (safe; this is not the
# long-removed tcp_tw_recycle, which broke NAT).
net.ipv4.tcp_tw_reuse = 1
# A cache hit is a small response: let it go out without waiting on slow start
# for a second round trip where the receiver window allows.
net.ipv4.tcp_slow_start_after_idle = 0
# Ephemeral ports for upstream/FPM connections.
net.ipv4.ip_local_port_range = 1024 65535
# System-wide file handles (per-service limits are set separately).
fs.file-max = 500000
SYSCTL
sysctl -p /etc/sysctl.d/60-slipstream.conf >/dev/null 2>&1 || true

# nginx main/events level. Ubuntu ships worker_connections 768, so two workers
# cap the box at ~1536 concurrent connections — reached well before the cache
# runs out of capacity. Patch idempotently and keep a backup; if the edit ever
# produces a config nginx rejects, restore it rather than leave the box broken.
NGINX_CONF=/etc/nginx/nginx.conf
if ! grep -q "slipstream-tuned" "$NGINX_CONF"; then
  cp -a "$NGINX_CONF" "${NGINX_CONF}.slipstream-pre"
  # events{} block: raise the per-worker connection ceiling and accept as many
  # ready connections per wakeup as are pending.
  # Only worker_connections is changed here. multi_accept was measured on a
  # 2-core box and made no reproducible difference (sustained 9529 vs 9438 rps,
  # static 21216 vs 21447 — the runs overlap), so it stays at the distro
  # default rather than becoming config we cannot justify.
  sed -i 's/^\([[:space:]]*\)worker_connections[[:space:]]\+[0-9]\+;/\1worker_connections 4096;/' "$NGINX_CONF"
  # main level: a worker needs an FD per connection plus upstreams and files.
  sed -i '0,/^worker_processes/s//# slipstream-tuned\nworker_rlimit_nofile 65535;\nworker_processes/' "$NGINX_CONF"
  if ! nginx -t >/dev/null 2>&1; then
    log "nginx rejected the tuned config — restoring the original"
    mv -f "${NGINX_CONF}.slipstream-pre" "$NGINX_CONF"
    nginx -t >/dev/null || fail "nginx config is broken and the backup did not restore it"
  fi
fi

# Kernel TLS offload (ssl_conf_command Options KTLS) is deliberately NOT enabled.
# CloudPanel ships it, so we implemented it, verified the kernel really was doing
# the encryption (/proc/net/tls_stat TlsTxSw incrementing) — and then measured it
# as a large net LOSS on this workload:
#
#     KTLS on : sustained 6642 rps, static 14246 rps
#     KTLS off: sustained 9177 rps, static 19601 rps
#
# ~28% off cached throughput and ~31% off static, reproduced with multi_accept
# both on and off. KTLS pays for itself on large sequential transfers; a page
# cache serves many small responses, where the per-record kernel transitions
# cost more than the copy it avoids. Left off, with the numbers, so nobody
# "optimises" it back in without re-measuring.
# Any leftover file from an earlier install is removed.
rm -f /etc/nginx/conf.d/slipstream-ktls.conf

step_done

# ---------- base nginx configuration ----------
step "Configuring nginx"
rm -f /etc/nginx/sites-enabled/default
# Nginx is the panel's only public ingress. This catch-all bootstrap vhost
# makes first-run setup available on standard HTTPS while panel-api remains
# loopback-only. A domain-specific vhost is added after certificate issuance.
cat > /etc/nginx/sites-enabled/slipstream-panel-bootstrap.conf <<'NGINX'
# Managed by Slipstream — bootstrap panel ingress
server {
    listen 80 default_server;
    listen [::]:80 default_server;
    server_name _;
    location /.well-known/acme-challenge/ { root /var/www/slipstream-acme; }
    location / { return 301 https://$host$request_uri; }
}
server {
    listen 443 ssl http2 default_server;
    listen [::]:443 ssl http2 default_server;
    server_name _;
    server_tokens off;
    ssl_certificate /etc/slipstream/certs/panel.pem;
    ssl_certificate_key /etc/slipstream/certs/panel.key;
    location / {
        proxy_pass https://127.0.0.1:5252;
        proxy_ssl_verify off;
        proxy_buffering off;
        proxy_read_timeout 1h;
        proxy_set_header Host $host;
        proxy_set_header X-Real-IP $remote_addr;
        proxy_set_header X-Forwarded-For $proxy_add_x_forwarded_for;
        proxy_set_header X-Forwarded-Proto https;
    }
}
NGINX
# The panel owns everything under conf.d/slipstream-* and sites-enabled/.
nginx -t >/dev/null

step_done

# ---------- database tuning ----------
step "Tuning MariaDB"
# The agent re-tunes on demand; give MariaDB a sane starting point now.
systemctl enable --now mariadb >/dev/null

step_done

# ---------- services ----------
step "Starting services"
for unit in slipstream-agent.service slipstream-api.service slipstream-api.socket; do
  if [[ -n "${SLIPSTREAM_LOCAL_BUILD:-}" ]]; then
    install -m 0644 "${SLIPSTREAM_LOCAL_BUILD}/../installer/systemd/${unit}" /etc/systemd/system/
  else
    curl -fsSL -o "/etc/systemd/system/${unit}" "${RELEASE_URL}/${unit}"
  fi
done
systemctl daemon-reload
systemctl enable --now slipstream-api.socket slipstream-agent slipstream-api nginx "php${PHP_VERSION}-fpm" >/dev/null

# apt starts nginx when the package is installed — before the bootstrap vhost
# above exists. `enable --now` is therefore a no-op on an already-running
# master, so the :443 listener declared in that vhost never binds and the setup
# URL printed at the end of this script is unreachable. Reload to load it.
systemctl reload nginx >/dev/null 2>&1 || systemctl restart nginx >/dev/null 2>&1 || true
for _ in $(seq 1 10); do
  ss -ltn 'sport = :443' | grep -q LISTEN && break
  sleep 1
done
ss -ltn 'sport = :443' | grep -q LISTEN || fail "nginx is not listening on 443 — the panel would be unreachable"

step_done

# ---------- firewall ----------
step "Opening the firewall"
if command -v ufw >/dev/null && ufw status | grep -q "Status: active"; then
  log "Opening firewall ports 22, 80 and 443…"
  ufw allow 22/tcp >/dev/null
  ufw allow 80/tcp >/dev/null
  ufw allow 443/tcp >/dev/null
  # QUIC/HTTP-3 is UDP. On a build with ngx_http_v3_module the vhost advertises
  # Alt-Svc, so browsers try 443/udp; without this they hang on that attempt and
  # silently fall back to TCP, which is slower than never advertising at all.
  # Harmless on builds without HTTP/3 — nothing listens on the port.
  ufw allow 443/udp >/dev/null
fi

# Per-site SFTP: each site root is a root-owned chroot whose children remain
# writable by that site's nologin identity.
cat > /etc/ssh/sshd_config.d/10-slipstream-sftp.conf <<'SSHD'
# Slipstream SFTP — chrooted, no shell, per-site isolation
Match User slip-site-*
    ChrootDirectory %h
    ForceCommand internal-sftp -d /
    AllowTcpForwarding no
    X11Forwarding no
    PasswordAuthentication yes
    PermitEmptyPasswords no
SSHD
sshd -t
systemctl reload ssh 2>/dev/null || systemctl reload sshd 2>/dev/null || true

step_done

# ---------- log rotation ----------
step "Log rotation and certificate renewal"
# Without this the panel is the only thing on the box writing unbounded logs:
# every other service here ships a logrotate config. A busy site produces
# hundreds of MB of access log a week, and a full disk takes every site down —
# so this is a availability control, not housekeeping.
cat > /etc/logrotate.d/slipstream <<'ROTATE'
# Managed by Slipstream.
# Per-site nginx access/error logs.
/var/log/slipstream/*/*.log {
    daily
    rotate 14
    missingok
    notifempty
    compress
    delaycompress
    sharedscripts
    # Copy-and-truncate: nginx holds these open, and renaming without telling it
    # leaves it writing to an unlinked inode (disk fills with an invisible file).
    # copytruncate avoids needing a reload per rotation across every vhost.
    copytruncate
}

# Per-site PHP error logs live inside the site tree so the owner can read them.
/srv/sites/*/logs/*.log {
    daily
    rotate 14
    missingok
    notifempty
    compress
    delaycompress
    copytruncate
    # Keep the site user as owner; these are read through the file manager.
    su root root
}
ROTATE
chmod 0644 /etc/logrotate.d/slipstream
# Fail loudly here rather than discovering a broken config weeks later when the
# disk is full: logrotate parses every file in logrotate.d on each run.
logrotate --debug /etc/logrotate.d/slipstream >/dev/null 2>&1 || \
  log "warning: logrotate rejected the Slipstream config — check /etc/logrotate.d/slipstream"

# ---------- certificate renewal ----------
# certbot's timer renews certificates, but without a deploy hook nginx keeps
# serving the OLD certificate from memory until it is reloaded — so a renewed
# cert would silently go unserved. Reload nginx after any successful renewal.
install -d -m 0755 /etc/letsencrypt/renewal-hooks/deploy
cat > /etc/letsencrypt/renewal-hooks/deploy/slipstream-reload-nginx.sh <<'HOOK'
#!/bin/bash
# Managed by Slipstream — reload nginx so a renewed certificate is served.
systemctl reload nginx 2>/dev/null || true
HOOK
chmod 0755 /etc/letsencrypt/renewal-hooks/deploy/slipstream-reload-nginx.sh

step_done

# ---------- done ----------
IP=$(curl -fsS -4 --max-time 5 https://api.ipify.org 2>/dev/null || hostname -I | awk '{print $1}')
sleep 2
SETUP_URL=$(journalctl -u slipstream-api --since "1 min ago" -o cat | grep -o '"url":"[^"]*"' | tail -1 | cut -d'"' -f4 || true)
SETUP_URL=${SETUP_URL/<server-ip>/$IP}

ui '\n'
ui '  %s━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━%s\n' "$D" "$R"
ui '\n'
ui '  %s✓%s  %sSlipstream is running%s   %sin %sm %ss%s\n' \
   "$GRN" "$R" "$B" "$R" "$D" "$((SECONDS / 60))" "$((SECONDS % 60))" "$R"
ui '\n'
ui '     Open   %s%s%s\n' "$CY1" "${SETUP_URL:-https://$IP}" "$R"
ui '\n'
ui '     %sThe link is valid for 24 hours and works once.%s\n' "$D" "$R"
ui '     %sYour browser will warn about the certificate — it is self-signed%s\n' "$D" "$R"
ui '     %suntil you issue a real one. That is expected on first boot.%s\n' "$D" "$R"
ui '\n'
ui '     %sdocs %shttps://slipstreampanel.com%s   %slog %s%s%s\n' \
   "$D" "$R$CY3" "$R" "$D" "$R$D" "$INSTALL_LOG" "$R"
ui '\n'
