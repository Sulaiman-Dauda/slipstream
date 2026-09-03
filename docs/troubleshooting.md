# Troubleshooting

Symptoms first, since that is what you have when something is wrong.

## The installer refuses to run

| Message | Cause |
| --- | --- |
| `port 80 is already in use` | something else is serving. Slipstream must own 80 and 443 — use a clean server |
| `Slipstream supports Ubuntu 24.04 / 26.04 LTS only` | unsupported OS. These are the two that are tested |
| `at least 1 GB of RAM required` | it will not fit below that. A "1 GB" cloud instance reports ~955 MB, which passes |
| `another control panel is installed` | remove it, or use a fresh server |

These are checked before anything is installed, so a refusal leaves the machine untouched — no
packages, no users, no directories.

## The install failed part way through

Everything the installer runs is logged to **`/var/log/slipstream-install.log`**, and on failure it
prints the last dozen lines of that log with the error. Read the whole file for the full context:

```bash
tail -50 /var/log/slipstream-install.log
```

Re-running the installer is safe: it creates users, directories and secrets only if they are absent,
and re-renders configuration it owns.

## The install looks stuck

It is almost certainly the package step, which is the longest by far. The spinner keeps moving while
it works, and each finished phase prints its elapsed time. If you want to watch what it is actually
doing, tail the log from a second session:

```bash
tail -f /var/log/slipstream-install.log
```

## I cannot reach the panel after installing

Check nginx is listening on 443:

```bash
ss -tlnp | grep :443
curl -sk -o /dev/null -w '%{http_code}\n' https://127.0.0.1/
```

If port 443 is missing, reload nginx (`systemctl reload nginx`) and check `nginx -t`. A fresh
install binding only port 80 was a real bug, fixed — the installer now asserts 443 is listening
before it finishes. If you see it, the installer did not complete.

Also confirm the firewall allows it: `ufw status`.

## The setup link says invalid or expired

Setup tokens are **single-use** and last 24 hours. If setup already completed, the endpoint returns
"setup already complete" — sign in instead. If you genuinely need a new token, restart
`slipstream-api` and check its log:

```bash
journalctl -u slipstream-api --since '2 min ago' | grep -o '"url":"[^"]*"'
```

## Everything in the UI fails with a gateway error

The agent is down.

```bash
systemctl status slipstream-agent
journalctl -u slipstream-agent -n 50
```

The API cannot do privileged work itself by design, so if the agent is not running, nothing works.

## A site provisioned but shows an error status

```bash
slipctl tasks
```

Read the failed task's log. Common causes:

- **`PHP 8.x is not installed on this server`** — you asked for a version that is not present. The
  message lists what is available.
- **Certificate failure** — DNS for the domain does not yet resolve to this server. The site still
  works on its self-signed certificate; issue again once DNS propagates.
- **Disk full** — check `df -h`.

## A WordPress site returns 404 on real pages

Usually a stale object cache. wp-cli and PHP-FPM do hold **separate APCu segments**, and neither
can clear the other's memory directly, but that no longer means a restart. Every cache key carries
an epoch read from a file both processes see, and a flush writes a new one, so the web tier starts
computing different keys on its very next request:

```bash
wp cache flush     # as the site user, in the release directory
```

The panel's own update, restore and migration paths flush through the site's connector, so they
cross the same boundary without touching anything else on the box.

Restarting PHP-FPM also works and is what older versions of this page recommended. It is no longer
necessary, and it is heavier than it looks: the pools are per site, but a `systemctl restart` of
the service takes every site on the machine with it.

Also check permalinks are flushed and the pages actually exist.

## Cached pages are stale

```bash
slipctl purge <site-id>
```

If a specific page never updates, check it is not being served stale by design: expired pages are
served from the stale copy while refreshing in the background. The header tells you:

```bash
curl -sI https://example.com/page/ | grep -i x-slipstream-cache
```

`HIT` from cache · `MISS` generated now · `BYPASS` never cached · `STALE` stale while revalidating.

## A page that should be dynamic is being cached

Check it matches a bypass rule — logged-in cookie, cart cookie, query string, or one of the dynamic
paths (`/cart`, `/checkout`, `/my-account`, `/wp-admin`, …). If your application uses different
paths, add them to the site's bypass configuration. A custom checkout at `/basket` is not bypassed
just because it is a checkout.

## The site is slow

First establish whether it is cached at all:

```bash
curl -sI https://example.com/ | grep -i x-slipstream-cache
```

If it says `BYPASS` on pages that should be cacheable, something is setting a cookie on every
response — a plugin, an analytics script, a session started unconditionally. That defeats page
caching entirely and is the single most common cause.

If it says `HIT` and is still slow, the problem is not the server: check payload size, third-party
scripts and the client's network.

If it says `MISS` on every request, the cache is not being retained — check disk space and that
`cache_enabled` is on.

Raising `php_workers` is almost never the answer; see [Operations](./operations.md#capacity).

## Backups fail

```bash
POST /api/backups/test
```

Confirms the repository is reachable and the password is right. Common causes: wrong credentials in
the agent's environment, a bucket that does not exist, or a changed repository password. Remember
that changing `backup_password` does not re-encrypt old snapshots — they still need the old one.

## A restore did not bring the site back

Restores roll back automatically if a step fails, so a *failed* restore should leave the site as it
was. If the restore succeeded but the site looks wrong, the object cache is the usual suspect, and
a restore already flushes it through the connector. Run `wp cache flush` in the release directory
to rule it out.

Verify the snapshot itself was good:

```bash
slipctl backup verify <backup-id>
```

That does a real restore into scratch and checks it. Doing this on a schedule is how you find a
broken repository before you need it.

## Drift keeps reappearing

Something outside the panel is rewriting a managed file — often a configuration-management tool or a
hand-edited vhost. Either stop it, or accept the drift so the new content becomes expected. Restoring
repeatedly will not help if something keeps changing the file back.

## HTTP/3 is not being used

It is enabled only where nginx was built with `ngx_http_v3_module` — Ubuntu 26.04 yes, 24.04 no
(nginx 1.24 predates it). Check:

```bash
nginx -V 2>&1 | grep -o http_v3_module
grep -c quic /etc/nginx/sites-enabled/<domain>.conf
```

If the module is present but the vhost has no `quic` lines, re-render the site (change any setting)
so the vhost is regenerated. Also confirm 443/udp is open in the firewall.

## Getting help

Include the version (`slipctl --version`), the OS release, what you expected, what happened, and the
relevant log. Panel logs are in `journalctl -u slipstream-api -u slipstream-agent`; site logs are in
`/var/log/slipstream/<domain>/`.

Security issues go through the [security policy](../SECURITY.md), never the public issue tracker.
