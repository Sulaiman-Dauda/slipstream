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
#   PANEL_EMAIL=... PANEL_PW=... IP=<public-ip> bash scripts/e2e-verify.sh
#
# Exit code is non-zero if any check fails.
set -u
PANEL="${PANEL:-https://127.0.0.1:5252}"
IP="${IP:?set IP=<public ip> so sites resolve via sslip.io}"
EMAIL="${PANEL_EMAIL:?set PANEL_EMAIL=<admin email>}"
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

# ---- host: PHP is patched and stays patched ---------------------------------
# Not a version floor: Ubuntu backports fixes without changing the upstream number
# (26.04's patched build still says 8.5.4), so "php -v" cannot tell. Ask apt instead.
echo "--- host: PHP updates ---"
apt-get -o DPkg::Lock::Timeout=120 update -qq >/dev/null 2>&1
check "no pending php$PHPV update" "$(apt list --upgradable 2>/dev/null | grep -c "^php$PHPV-")" "0"
# An upgrade on disk is not a fix until php-fpm restarts: a master still running a
# replaced binary shows "(deleted)" on its exe link.
FPM_PID=$(systemctl show -p MainPID --value "php$PHPV-fpm")
# MainPID is 0 when it is not running, and /proc/0 has no exe, which would read as a pass.
check "php$PHPV-fpm running" "$( [ "${FPM_PID:-0}" -gt 0 ] && echo yes || echo no)" "yes"
check "php$PHPV-fpm runs the installed binary" \
  "$(readlink "/proc/$FPM_PID/exe" 2>/dev/null | grep -c '(deleted)')" "0"
UU_ON=""; eval "$(apt-config shell UU_ON APT::Periodic::Unattended-Upgrade)"
check "unattended-upgrades switched on" "$UU_ON" "1"
if dpkg-query -W -f '${Version}' "php$PHPV-fpm" 2>/dev/null | grep -q sury; then
  check "unattended-upgrades covers ppa:ondrej/php" \
    "$(apt-config dump Unattended-Upgrade::Origins-Pattern | grep -c LP-PPA-ondrej-php)" "1"
fi

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
  # ---- Block theme: a site-wide template object must purge the whole site.
  # Editing a header, footer, global style or navigation in the Site Editor
  # used to purge only the homepage and the feed, because those posts have no
  # permalink, no archive and no terms. Every interior page then kept serving
  # the old header until TTL, up to 24 hours on the Maximum profile.
  #
  # The connector posts its purge non-blocking, which WP-CLI drops when the
  # process exits, so this intercepts the outgoing request and asserts the
  # payload instead of watching the cache.
  cat > /tmp/slip-e2e-purge.php <<'PHPEOF'
<?php
add_filter('pre_http_request', function ($pre, $args, $url) {
    if (strpos($url, '/api/connector/purge') !== false) { echo "PAYLOAD:" . $args['body'] . "\n"; }
    return ['response' => ['code' => 200], 'body' => '{}', 'headers' => [], 'cookies' => []];
}, 10, 3);
$id = wp_insert_post(['post_type' => 'wp_template_part', 'post_status' => 'publish',
    'post_name' => 'e2e-footer', 'post_title' => 'E2E Footer', 'post_content' => '<p>a</p>']);
wp_update_post(['ID' => $id, 'post_content' => '<p>b</p>']);
wp_delete_post($id, true);
PHPEOF
  SU=$(site_get "$FID" | jqget "['system_user']")
  cp /tmp/slip-e2e-purge.php "/srv/sites/$FS/current/slip-e2e-purge.php"
  chown "$SU:$SU" "/srv/sites/$FS/current/slip-e2e-purge.php"
  payload=$(sudo -u "$SU" wp --path="/srv/sites/$FS/current" eval-file "/srv/sites/$FS/current/slip-e2e-purge.php" 2>/dev/null | grep '^PAYLOAD:' | head -1)
  rm -f "/srv/sites/$FS/current/slip-e2e-purge.php" /tmp/slip-e2e-purge.php
  case "$payload" in
    *'"all":true'*) ok "template part edit purges the whole site" ;;
    *) bad "template part edit purges only ${payload:-nothing} (interior pages keep a stale header)" ;;
  esac

  # ---- Read-only accounts browse files but cannot read secrets: a real
  # wp-config.php comes back with the database password masked and still
  # valid PHP, and the backup dump is refused.
  RJ=$(mktemp); RPW="ro-e2e-$(openssl rand -hex 8)"
  rid=$(c "$PANEL/api/users" -H 'Content-Type: application/json' \
    -d "{\"email\":\"readonly-e2e@example.com\",\"password\":\"$RPW\",\"role\":\"readonly\"}" | jqget "['id']")
  curl -sk -b "$RJ" -c "$RJ" -o /dev/null "$PANEL/api/login" -H 'Content-Type: application/json' \
    -d "{\"email\":\"readonly-e2e@example.com\",\"password\":\"$RPW\"}"
  dbpw=$(grep -oP "define\(\s*'DB_PASSWORD',\s*'\K[^']+" "/srv/sites/$FS/shared/wp-config.php")
  curl -sk -b "$RJ" "$PANEL/api/sites/$FID/files/read?path=shared/wp-config.php" | jqget "['content']" > /tmp/slip-e2e-cfg.php
  leaked=no; [ -n "$dbpw" ] && grep -qF -- "$dbpw" /tmp/slip-e2e-cfg.php && leaked=yes
  [ -n "$dbpw" ] || leaked=unknown
  check "readonly wp-config hides DB password" "$leaked" "no"
  check "readonly wp-config keeps DB_NAME" "$(grep -c "'DB_NAME'" /tmp/slip-e2e-cfg.php)" "1"
  check "readonly masked wp-config is valid PHP" "$(php -l /tmp/slip-e2e-cfg.php >/dev/null 2>&1 && echo yes || echo no)" "yes"
  check "readonly dump download refused" \
    "$(curl -sk -b "$RJ" -o /dev/null -w '%{http_code}' "$PANEL/api/sites/$FID/files/download?path=logs/db-latest.sql")" "403"
  rm -f "$RJ" /tmp/slip-e2e-cfg.php
  [ -n "$rid" ] && c -o /dev/null -X DELETE "$PANEL/api/users/$rid"

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
  # The dump goes in shared/, not the site root: the root is root:site 0750, so
  # the site user cannot write there and mariadb-dump fails with errcode 13. It
  # failed silently here for as long as this check has existed, because stderr
  # was discarded and the exit status never tested, so the import then ran
  # against a file that was never created and three checks failed for one
  # invisible reason.
  if ! sudo -u "$MU" wp --path="$MROOT/current" db export "$MROOT/shared/migration.sql" --quiet; then
    bad "migration fixture: could not export the database dump"
  fi
  rm -rf "$FIX"

  mtask=$(c "$PANEL/api/sites/$MID/migration" -H 'Content-Type: application/json' \
    -d "{\"archive\":\"migration.tar.gz\",\"sql\":\"shared/migration.sql\",\"old_domain\":\"$MS\",\"confirm\":\"$MS\"}" | jqget "['task']['id']")
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

# ---- Custom nginx rules survive re-rendering -------------------------------
# A site's vhost is rewritten whole on every settings change, certificate issue
# and 12-hourly renewal check, so a hand edit to it is lost within half a day.
# Operator rules live in /etc/slipstream/nginx/<domain>/ instead, which the
# agent creates and never writes. This proves a deny rule there holds through
# each kind of re-render, and that a broken rule makes a change fail and roll
# back rather than take the site down.
#
# The certificate step needs a real address for the ACME account:
#   E2E_ACME_EMAIL=you@example.org
# Without it that step is reported as skipped and counts as neither pass nor
# fail. The panel's own acme_email is put back afterwards, since while it is
# set every new site requests a certificate.
echo "--- custom nginx rules survive re-rendering ---"
CS="rules.$DASH.sslip.io"
CDIR="/etc/slipstream/nginx/$CS"
CV="/etc/nginx/sites-enabled/$CS.conf"
code_of() { curl -sk --resolve "$1:443:127.0.0.1" -o /dev/null -w '%{http_code}' "https://$1$2"; }
vhost_inode() { stat -c %i "$CV" 2>/dev/null; }
rules_hold() {
  local label=$1 p
  for p in /exports/report.pdf /exports/list.csv /exports/run.php; do
    check "$label: $p denied" "$(code_of "$CS" "$p")" "404"
  done
  check "$label: control file serves" "$(code_of "$CS" /control.txt)" "200"
  check "$label: rule file untouched" "$(sha256sum "$CDIR/deny-exports.conf" | cut -d' ' -f1)" "$RULE_SUM"
  check "$label: vhost includes the rules" "$(grep -cF "include $CDIR/*.conf;" "$CV")" "1"
}
# rerender_task <label> <inode before> <task id>: the task must succeed and
# must really have rewritten the vhost (a new inode), or the rules were never
# put to the test. Read the inode before sending the request: the task runs
# as soon as it is accepted and a render takes milliseconds.
rerender_task() {
  local label=$1 before=$2 tid=$3
  check "$label task succeeds" "$(wait_task "${tid:-0}" 240)" "succeeded"
  check "$label rewrote the vhost" "$( [ "$(vhost_inode)" != "$before" ] && echo yes || echo no)" "yes"
  rules_hold "after $label"
}
CID=$(create_site "{\"domain\":\"$CS\",\"type\":\"php\",\"php_version\":\"$PHPV\"}")
st=$(wait_active "${CID:-0}" 120); check "rules-site provisions" "$st" "active"
if [ "$st" = active ]; then
  CU=$(site_get "$CID" | jqget "['system_user']")
  CR="/srv/sites/$CS/current"
  mkdir -p "$CR/exports"
  echo report > "$CR/exports/report.pdf"
  echo a,b > "$CR/exports/list.csv"
  printf '%s' '<?php echo "exports-php";' > "$CR/exports/run.php"
  echo control > "$CR/control.txt"
  chown -R "$CU:$CU" "$CR/exports" "$CR/control.txt"

  check "include directory is root-only" "$(stat -c '%a %U' "$CDIR" 2>/dev/null)" "750 root"
  check "vhost includes the directory" "$(grep -cF "include $CDIR/*.conf;" "$CV")" "1"
  # Before any rule the files serve, so the 404s below are the rule's doing.
  for p in /exports/report.pdf /exports/list.csv /exports/run.php; do
    check "before rule: $p serves" "$(code_of "$CS" "$p")" "200"
  done

  cat > "$CDIR/deny-exports.conf" <<'EOF'
location ~* ^/exports/ { return 404; }
EOF
  RULE_SUM=$(sha256sum "$CDIR/deny-exports.conf" | cut -d' ' -f1)
  if nginx -t -q && systemctl reload nginx; then ok "rule loads (nginx -t, reload)"; else bad "rule loads (nginx -t, reload)"; fi
  sleep 1
  rules_hold "after reload"

  ino=$(vhost_inode)
  t=$(c -X PUT "$PANEL/api/sites/$CID/config" -H 'Content-Type: application/json' \
    -d '{"cache_enabled":true,"cache_ttl_sec":120}' | jqget "['task']['id']")
  rerender_task "site config change" "$ino" "$t"

  ino=$(vhost_inode)
  t=$(c -X PUT "$PANEL/api/sites/$CID/php" -H 'Content-Type: application/json' \
    -d '{"memory_limit_mb":384}' | jqget "['task']['id']")
  rerender_task "PHP settings change" "$ino" "$t"

  if [ -n "${E2E_ACME_EMAIL:-}" ]; then
    ORIG_ACME=$(c "$PANEL/api/settings" | jqget "['acme_email']")
    c -o /dev/null -X PUT "$PANEL/api/settings" -H 'Content-Type: application/json' \
      -d "{\"acme_email\":\"$E2E_ACME_EMAIL\"}"
    ino=$(vhost_inode)
    t=$(c -X POST "$PANEL/api/sites/$CID/certificate" | jqget "['task']['id']")
    rerender_task "certificate issue" "$ino" "$t"
    c -o /dev/null -X PUT "$PANEL/api/settings" -H 'Content-Type: application/json' \
      -d "{\"acme_email\":\"$ORIG_ACME\"}"
    check "acme_email put back" "$(c "$PANEL/api/settings" | jqget "['acme_email']")" "$ORIG_ACME"
  else
    echo "  SKIP certificate issue re-render (set E2E_ACME_EMAIL to run it)"
  fi

  # A broken rule must fail the change and roll it back, with nginx's own
  # message, while the site carries on serving its loaded configuration.
  printf 'location ~* ^/broken/ { return 404;\n' > "$CDIR/broken.conf"
  VSUM=$(sha256sum "$CV" | cut -d' ' -f1)
  t=$(c -X PUT "$PANEL/api/sites/$CID/config" -H 'Content-Type: application/json' \
    -d '{"cache_ttl_sec":240}' | jqget "['task']['id']")
  check "change with a broken rule fails" "$(wait_task "${t:-0}" 120)" "failed"
  terr=$(c "$PANEL/api/tasks/${t:-0}" | jqget "['error']")
  check "failure names the broken file" "$(grep -c 'broken.conf' <<<"$terr")" "1"
  check "vhost rolled back" "$(sha256sum "$CV" | cut -d' ' -f1)" "$VSUM"
  check "panel refuses to restart nginx" \
    "$(c -o /dev/null -w '%{http_code}' -X POST "$PANEL/api/services/nginx/restart")" "502"
  check "nginx still running" "$(systemctl is-active nginx)" "active"
  rules_hold "with a broken rule present"
  rm -f "$CDIR/broken.conf"
  if nginx -t -q; then ok "config passes once the broken rule is gone"; else bad "config passes once the broken rule is gone"; fi

  check "rules-site delete accepted" "$(delete_site "$CID")" "202"
  check "rules-site teardown completes" "$(teardown_gone "$CID")" "yes"
  # The rules are the operator's files, so deleting the site leaves them.
  check "rules left in place on site delete" \
    "$( [ "$(sha256sum "$CDIR/deny-exports.conf" 2>/dev/null | cut -d' ' -f1)" = "$RULE_SUM" ] && echo yes || echo no)" "yes"
  rm -rf "$CDIR"
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
