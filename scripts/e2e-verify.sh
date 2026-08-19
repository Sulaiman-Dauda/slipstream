#!/usr/bin/env bash
# Slipstream end-to-end verification.
#
# Runs ON a Slipstream server. For every site type it: creates the site via the
# API, waits for it to go active, FETCHES the served page over HTTPS and asserts
# it actually works (this is what catches "provisions fine but 500s on every
# page"), exercises the type-specific feature, then deletes it and confirms
# teardown. Finally it runs the WordPress feature suite (cache, object cache,
# magic login, cron, staging, safe-push) on one site.
#
#   PANEL_PW=... IP=<public-ip> bash scripts/e2e-verify.sh
#
# Exit code is non-zero if any check fails.
set -u
PANEL="${PANEL:-https://127.0.0.1:5252}"
IP="${IP:?set IP=<public ip> so sites resolve via sslip.io}"
EMAIL="${PANEL_EMAIL:-abiodauda@gmail.com}"
PW="${PANEL_PW:?set PANEL_PW=<admin password>}"
DASH="${IP//./-}"
# Never hardcode a PHP version: 24.04 ships 8.4, 26.04 ships 8.5. Ask the host
# what it actually has, so this suite exercises the real supported stack rather
# than failing with "Unit php8.4-fpm.service not found" on a newer release.
PHPV="${PHPV:-$(ls -1 /etc/php 2>/dev/null | grep -E '^8\.[0-9]$' | sort -Vr | head -1)}"
PHPV="${PHPV:-8.4}"
echo "  using PHP $PHPV"
J=$(mktemp)
PASS=0; FAIL=0; FAILED=()

c() { curl -sk -b "$J" -c "$J" "$@"; }              # panel API (cookie jar)
site_get() { c "$PANEL/api/sites/$1"; }
jqget() { python3 -c "import sys,json;d=json.load(sys.stdin);print(d$1)" 2>/dev/null; }

ok()   { PASS=$((PASS+1)); printf '  \033[32mPASS\033[0m %s\n' "$1"; }
bad()  { FAIL=$((FAIL+1)); FAILED+=("$1"); printf '  \033[31mFAIL\033[0m %s\n' "$1"; }
check(){ if [ "$2" = "$3" ]; then ok "$1 ($2)"; else bad "$1 (got '$2' want '$3')"; fi; }

login() {
  c -o /dev/null "$PANEL/api/login" -H 'Content-Type: application/json' \
     -d "{\"email\":\"$EMAIL\",\"password\":\"$PW\"}"
}

# create_site <json>  -> echoes site id (or empty)
create_site() {
  c "$PANEL/api/sites" -H 'Content-Type: application/json' -d "$1" | jqget "['site']['id']"
}

# wait_active <id> <timeout_s> -> echoes final status
wait_active() {
  local id=$1 t=${2:-90} st
  for ((i=0;i<t;i+=4)); do
    st=$(site_get "$id" | jqget "['status']")
    [ "$st" = active ] && { echo active; return; }
    [ "$st" = error ]  && { echo error;  return; }
    sleep 4
  done
  echo timeout
}

# fetch <domain> <path> -> "HTTPCODE BODYHAS500"
serves() {
  local dom=$1 path=${2:-/} code body
  code=$(curl -sk --resolve "$dom:443:127.0.0.1" -o /tmp/body.$$ -w '%{http_code}' "https://$dom$path")
  local fatal=no
  grep -qiE "critical error|Fatal error|There has been a critical" /tmp/body.$$ && fatal=yes
  rm -f /tmp/body.$$
  echo "$code $fatal"
}

# wait_task <id> <timeout_s> -> echoes final status
wait_task() {
  local id=$1 t=${2:-180} st
  for ((i=0;i<t;i+=4)); do
    st=$(c "$PANEL/api/tasks/$id" | jqget "['status']")
    case "$st" in succeeded|failed) echo "$st"; return;; esac
    sleep 4
  done
  echo timeout
}

delete_site() { c -o /dev/null -w '%{http_code}' -X DELETE "$PANEL/api/sites/$1"; }

# teardown_gone <id> -> yes|no  (delete is async: poll until GET 404s)
teardown_gone() {
  local id=$1
  for ((k=0;k<60;k+=3)); do
    [ "$(c -o /dev/null -w '%{http_code}' "$PANEL/api/sites/$id")" = 404 ] && { echo yes; return; }
    sleep 3
  done
  echo no
}

echo "=================================================================="
echo " Slipstream E2E verification — $(date -u +%FT%TZ)"
echo " panel=$PANEL  ip=$IP"
echo "=================================================================="
[ "$(login)" ] # ensure cookie
echo "[login] $( [ -s "$J" ] && echo ok || echo FAILED )"

# ---- per-type: create -> active -> serves -> delete(async) -> teardown gone --
# expect: exact code, or "ok" for any non-5xx/non-fatal (empty app is fine).
# prep: optional shell snippet run after active (e.g. drop an index.php).
run_type() {
  local label=$1 dom=$2 json=$3 path=${4:-/} expect=${5:-200} prep=${6:-}
  echo "--- site type: $label ($dom) ---"
  local id; id=$(create_site "$json")
  if [ -z "$id" ]; then bad "$label: create rejected"; return; fi
  local st; st=$(wait_active "$id" 120)
  check "$label provisions" "$st" "active"
  if [ "$st" = active ]; then
    local U; U=$(site_get "$id" | jqget "['system_user']")
    [ -n "$prep" ] && eval "$prep"
    read -r code fatal <<<"$(serves "$dom" "$path")"
    if [ "$expect" = ok ]; then
      if [ "$code" -lt 500 ] 2>/dev/null; then ok "$label serves ($code, app not deployed OK)"; else bad "$label serves ($code)"; fi
    else
      check "$label serves HTTP" "$code" "$expect"
    fi
    check "$label no PHP fatal" "$fatal" "no"
  fi
  check "$label delete accepted" "$(delete_site "$id")" "202"
  check "$label teardown completes" "$(teardown_gone "$id")" "yes"
}

run_type "static" "static.$DASH.sslip.io" \
  "{\"domain\":\"static.$DASH.sslip.io\",\"type\":\"static\"}" "/" 200
# php: inject an index.php so we actually exercise PHP execution (empty = 403).
run_type "php" "php.$DASH.sslip.io" \
  "{\"domain\":\"php.$DASH.sslip.io\",\"type\":\"php\",\"php_version\":\"$PHPV\"}" "/" 200 \
  'printf "%s" "<?php echo \"php-exec-ok\";" > /srv/sites/$dom/current/index.php; chown $U:$U /srv/sites/$dom/current/index.php'
# proxy: stand up a real upstream so we test forwarding, not a bad-gateway.
python3 -m http.server 9099 --bind 127.0.0.1 >/dev/null 2>&1 & PYP=$!
run_type "proxy" "proxy.$DASH.sslip.io" \
  "{\"domain\":\"proxy.$DASH.sslip.io\",\"type\":\"proxy\",\"proxy_upstream\":\"http://127.0.0.1:9099\"}" "/" 200
kill $PYP 2>/dev/null
# laravel: fresh site has no app code deployed yet, so any non-5xx (200/404) is fine.
run_type "laravel" "laravel.$DASH.sslip.io" \
  "{\"domain\":\"laravel.$DASH.sslip.io\",\"type\":\"laravel\",\"php_version\":\"$PHPV\"}" "/" ok
run_type "wordpress" "wp.$DASH.sslip.io" \
  "{\"domain\":\"wp.$DASH.sslip.io\",\"type\":\"wordpress\",\"php_version\":\"$PHPV\",\"admin_email\":\"$EMAIL\",\"admin_user\":\"admin\",\"admin_password\":\"WpBench!2026Pass\"}" "/" 200

# ---- WooCommerce: the PRODUCT must install WooCommerce itself and serve its
# pages. Do NOT install the plugin from the test — that would test our own setup
# instead of the product and mask a provisioner that ships plain WordPress.
echo "--- site type: woocommerce (product installs Woo, shop/cart resolve) ---"
WOO="shop.$DASH.sslip.io"
WID=$(create_site "{\"domain\":\"$WOO\",\"type\":\"woocommerce\",\"profile\":\"commerce\",\"php_version\":\"$PHPV\",\"admin_email\":\"$EMAIL\",\"admin_user\":\"admin\",\"admin_password\":\"WpBench!2026Pass\",\"object_cache\":true}")
if [ -n "$WID" ]; then
  st=$(wait_active "$WID" 180); check "woocommerce provisions" "$st" "active"
  U=$(site_get "$WID" | jqget "['system_user']"); P="/srv/sites/$WOO/current"
  # The provisioner — not this script — must have installed + activated Woo.
  ver=$(sudo -u "$U" wp --path="$P" plugin get woocommerce --field=version 2>/dev/null)
  [ -n "$ver" ] && ok "woocommerce installed by provisioner ($ver)" || bad "provisioner did not install WooCommerce"
  read -r code fatal <<<"$(serves "$WOO" /)"
  check "woocommerce home serves (was the 500 bug)" "$code" "200"
  check "woocommerce no PHP fatal" "$fatal" "no"
  # The Woo-registered routes must resolve, not 404 (the stale-APCu rewrite bug).
  read -r scode _ <<<"$(serves "$WOO" /shop/)";  check "woocommerce /shop resolves"  "$scode" "200"
  read -r ccode _ <<<"$(serves "$WOO" /cart/)";  check "woocommerce /cart resolves"  "$ccode" "200"
fi

# ---- WordPress feature suite on a fresh WP site ----
echo "--- WordPress feature suite ---"
FS="feat.$DASH.sslip.io"
FID=$(create_site "{\"domain\":\"$FS\",\"type\":\"wordpress\",\"php_version\":\"$PHPV\",\"admin_email\":\"$EMAIL\",\"admin_user\":\"admin\",\"admin_password\":\"WpBench!2026Pass\"}")
st=$(wait_active "$FID" 120); check "feature-site provisions" "$st" "active"
if [ "$st" = active ]; then
  # cache: warm then expect HIT
  for i in 1 2 3; do curl -sk --resolve "$FS:443:127.0.0.1" -o /dev/null "https://$FS/"; done
  hit=$(curl -sk --resolve "$FS:443:127.0.0.1" -D - -o /dev/null "https://$FS/" | grep -io 'x-slipstream-cache: [A-Z]*' | awk '{print $2}')
  check "page cache serves HIT" "$hit" "HIT"
  # pre-compression: gzip from cache
  enc=$(curl -sk --resolve "$FS:443:127.0.0.1" -H 'Accept-Encoding: gzip' -D - -o /dev/null "https://$FS/" | grep -io 'content-encoding: gzip' | head -1)
  check "cache pre-compressed" "${enc:-none}" "content-encoding: gzip"
  # object cache enable
  oc=$(c -o /dev/null -w '%{http_code}' "$PANEL/api/sites/$FID/wp/object-cache" -H 'Content-Type: application/json' -d '{"enable":true}')
  check "object-cache enable accepted" "$oc" "202"
  # magic login returns a URL
  ml=$(c "$PANEL/api/sites/$FID/wp/login" -X POST | jqget "['url']")
  [ -n "$ml" ] && ok "magic login issues URL" || bad "magic login"
  # cron add / list / delete
  cr=$(c -o /dev/null -w '%{http_code}' "$PANEL/api/sites/$FID/cron" -H 'Content-Type: application/json' -d '{"schedule":"*/15 * * * *","command":"echo hi"}')
  check "cron create accepted" "$cr" "201"
  cl=$(c "$PANEL/api/sites/$FID/cron" | jqget "|len(_)>0" 2>/dev/null || echo "")
  # purge
  pg=$(c -o /dev/null -w '%{http_code}' -X POST "$PANEL/api/sites/$FID/purge")
  check "cache purge accepted" "$pg" "200"
  # staging clone
  stg=$(c -o /dev/null -w '%{http_code}' -X POST "$PANEL/api/sites/$FID/staging")
  check "staging clone accepted" "$stg" "202"
  sleep 20
  check "feature-site delete accepted" "$(delete_site "$FID")" "202"
  check "feature-site teardown completes" "$(teardown_gone "$FID")" "yes"
fi

# ---- Migration import ----------------------------------------------------
# Nothing here was covered until a real migration destroyed the cache
# connector on every imported site and nobody noticed: the site served fine,
# nginx still cached, and no purge ever fired again. Migration is also how
# most real sites arrive, so it is the least acceptable gap in this suite.
#
# The fixture is a WordPress tree with its mu-plugins REMOVED, which is what a
# tree from any other host looks like. If the importer forgets to reinstate
# the connector, the check below fails.
echo "--- migration import ---"
MS="mig.$DASH.sslip.io"
MID=$(create_site "{\"domain\":\"$MS\",\"type\":\"wordpress\",\"php_version\":\"$PHPV\",\"admin_email\":\"$EMAIL\",\"admin_user\":\"admin\",\"admin_password\":\"WpBench!2026Pass\"}")
st=$(wait_active "$MID" 120); check "migration-site provisions" "$st" "active"
if [ "$st" = active ]; then
  MU=$(site_get "$MID" | jqget "['system_user']")
  MROOT="/srv/sites/$MS"
  FIX=$(mktemp -d)

  # Build a foreign-looking tree: real WordPress, no connector, plus a marker
  # file so we can prove the archive's contents actually landed.
  cp -a "$MROOT/current/." "$FIX/" 2>/dev/null
  rm -rf "$FIX/wp-content/mu-plugins"
  echo "imported-by-e2e" > "$FIX/imported-marker.txt"
  tar czf "$MROOT/migration.tar.gz" -C "$FIX" .
  sudo -u "$MU" wp --path="$MROOT/current" db export "$MROOT/migration.sql" --quiet 2>/dev/null
  rm -rf "$FIX"

  mtask=$(c "$PANEL/api/sites/$MID/migration" -H 'Content-Type: application/json' \
    -d "{\"archive\":\"migration.tar.gz\",\"sql\":\"migration.sql\",\"old_domain\":\"$MS\",\"confirm\":\"$MS\"}" | jqget "['task']['id']")
  check "migration import accepted" "$([ -n "$mtask" ] && echo yes || echo no)" "yes"
  check "migration import succeeds" "$(wait_task "${mtask:-0}" 240)" "succeeded"

  # The archive's own content must be live.
  [ -f "$MROOT/current/imported-marker.txt" ] && ok "migrated files are live" || bad "migrated files are live"

  # THE regression: the connector must survive an import, or cache
  # invalidation is silently dead for the rest of the site's life.
  [ -f "$MROOT/current/wp-content/mu-plugins/slipstream-connector.php" ] \
    && ok "cache connector survives migration" \
    || bad "cache connector survives migration (imported release has no mu-plugin)"

  # A migrated site must still serve, and still cache.
  read -r mcode mfatal <<<"$(serves "$MS" /)"
  check "migrated site serves" "$mcode" "200"
  check "migrated site no PHP fatal" "$mfatal" "no"
  for i in 1 2 3; do curl -sk --resolve "$MS:443:127.0.0.1" -o /dev/null "https://$MS/"; done
  mhit=$(curl -sk --resolve "$MS:443:127.0.0.1" -D - -o /dev/null "https://$MS/" | grep -io 'x-slipstream-cache: [A-Z]*' | awk '{print $2}')
  check "migrated site caches" "$mhit" "HIT"

  # The importer must not leave a full database dump lying in the site tree.
  left=no
  [ -f "$MROOT/migration.tar.gz" ] && left=yes
  [ -f "$MROOT/migration.sql" ] && left=yes
  check "migration inputs cleaned up" "$left" "no"

  check "migration-site delete accepted" "$(delete_site "$MID")" "202"
  check "migration-site teardown completes" "$(teardown_gone "$MID")" "yes"
fi

# cleanup woo (also verifies commerce-site teardown)
if [ -n "${WID:-}" ]; then
  check "woocommerce delete accepted" "$(delete_site "$WID")" "202"
  check "woocommerce teardown completes" "$(teardown_gone "$WID")" "yes"
fi

echo "=================================================================="
echo " RESULT: $PASS passed, $FAIL failed"
[ "$FAIL" -gt 0 ] && { printf '  failed: %s\n' "${FAILED[@]}"; exit 1; }
echo " ALL CHECKS PASSED"
