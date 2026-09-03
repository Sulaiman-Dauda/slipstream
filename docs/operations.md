# Operations

Running Slipstream day to day.

## Services

| Unit | Runs as | Purpose |
| --- | --- | --- |
| `slipstream-api` | `slipstream` | HTTP API and web UI |
| `slipstream-agent` | `root` | all privileged work |
| `nginx`, `php<ver>-fpm`, `mariadb` | — | the data plane |

```bash
systemctl status slipstream-api slipstream-agent
journalctl -u slipstream-api -u slipstream-agent -f
```

Both panel services restart on failure. If the agent is down the UI still loads but every action
fails with a bad-gateway error — that is the symptom to recognise.

## Logs

| What | Where |
| --- | --- |
| Panel | `journalctl -u slipstream-api -u slipstream-agent` |
| Per-site access and error | `/var/log/slipstream/<domain>/` |
| Per-site PHP errors | `/srv/sites/<domain>/logs/php-error.log` |
| nginx itself | `/var/log/nginx/` |

All of these are rotated daily and kept for 14 days, compressed. Rotation uses `copytruncate` so
nginx keeps writing to the same file rather than to an unlinked inode. Without rotation a busy site
fills the disk and takes every site on the box down with it.

Deleting a site removes its log directory too.

## Upgrades

The panel tells you when a newer release exists: open the dashboard and a banner names the version,
links to [the changelog](https://slipstreampanel.com/changelog) and offers an **Update now** button.
Nothing installs by itself, and the check is described in the FAQ under "Does the panel phone home?".


```bash
curl -sk -c jar -b jar https://your-panel/api/panel/update \
  -H 'Content-Type: application/json' \
  -d '{"base_url":"https://github.com/Sulaiman-Dauda/slipstream/releases/latest/download"}'
```

`base_url` is the directory the binaries and their `.sha256` files are published under — the same
release root the installer uses. It **must** be `https://`, and it is required: there is a
`update_url` setting the agent falls back to, but it is not writable through the settings API, so in
practice you pass `base_url` on each call. Add `"version"` to record which build you are moving to
in the audit log.

The self-update downloads the new binaries, verifies their checksums, **checks their build
provenance**, confirms they are valid ELF executables, stages all of them before swapping any, backs
up the current ones, then hands over to a detached systemd guard that health-checks `/healthz` and
**automatically restores the previous binaries if the new build is unhealthy**. Tested by
deliberately shipping a broken build: it rolled back to a working panel in about 27 seconds.

The provenance check is the one that matters if the release itself is the attack. Checksums are
published beside the binaries, so anyone who can write a release can replace both and the checksum
still passes: a compromised account, a leaked token or a poisoned action reaches root on every
server that updates. A provenance attestation is signed by GitHub for this project's release
workflow at a specific commit and stored outside the release, so rewriting the release cannot forge
it.

It **fails closed**. A check that runs and fails aborts the update, always. A server with no
`gh` installed cannot check at all, and is refused unless the caller explicitly accepts that:

```bash
curl -sk -c jar -b jar https://your-panel/api/panel/update \
  -H 'Content-Type: application/json' -d '{"allow_unattested": true}'
```

That waiver is recorded in the audit log. If you update unattended, install `gh` and leave the
default: an automated update that skips the check is the single point at which a compromised
release becomes root on your server, without anyone present to notice.

Upgrading the OS packages underneath (nginx, PHP, MariaDB) is ordinary `apt` work. After a PHP
upgrade, check that the version you use is still installed — Slipstream refuses to provision a site
against a PHP version with no FPM binary, and will tell you which versions are available.

## Drift

```bash
slipctl drift
```

Every file the panel renders is hashed. If something else changed one, it shows up here. Resolve it:

- `{"action":"restore"}` — discard the manual edit and re-render from desired state;
- `{"action":"accept"}` — record the current content as expected.

Drift covers only files the panel wrote. It is not a general file-integrity monitor.

## Certificates

Issued per site with Let's Encrypt HTTP-01 and renewed by certbot's timer. A deploy hook reloads
nginx after renewal — without it, nginx keeps serving the old certificate from memory until something
else restarts it, which is a silent and confusing failure.

Deleting a site also deletes its certificate and renewal configuration, so `certbot renew` does not
fail forever on a domain that no longer exists.

For the panel's own certificate, `POST /api/panel/certificate`.

## Firewall

The installer opens 22, 80, 443/tcp and 443/udp. Manage rules from the panel or:

```bash
GET  /api/firewall
POST /api/firewall/rule
```

Deleting a rule that would lock you out of SSH is refused.

## Monitoring

`GET /api/system/status` is the health endpoint worth polling: CPU, memory and disk headroom, load,
and whether nginx and MariaDB are running. `GET /api/system/metrics` returns 24 hours of samples at
five-minute intervals.

`GET /healthz` needs no authentication and is suitable for an uptime check.

Worth alerting on: disk headroom below ~15%, memory headroom below ~10%, either data-plane service
not running, and any drift you did not cause.

## Capacity

The defaults are sized from the memory actually available and are deliberately conservative. Each
site is budgeted a quarter of system RAM, and one PHP worker per 80 MB of that budget. On a 2 GB
server that is roughly six workers per site, enough to saturate two cores on uncacheable work,
because at that point the bottleneck is CPU rather than worker count. On a 1 GB server it is three.

OPcache is the same 128 MB allocation on both, since the per-site budget only reaches the larger
tier above 1 GB. Moving from 1 GB to 2 GB buys worker concurrency and InnoDB buffer pool, not a
bigger opcode cache.

Measured on a fresh 1 GB server (955 MB usable): the install takes 1m 47s and idles at 457 MB, or
384 MB with `fwupd`, `packagekit`, `udisks2` and `multipathd` disabled, none of which a server
needs. MariaDB claims a further ~238 MB when the first site's database is created.

**Do not raise `php_workers` without measuring.** More workers on a CPU-bound path makes throughput
*worse*, and enough of them will push the box into swap, which is how panels that ship
`pm.max_children = 250` fall over. The numbers are in [Benchmarks](./benchmarks.md).

The lever that actually helps is the page cache: a cache hit never runs PHP at all.

## Backups of the panel itself

Site backups do not include `/var/lib/slipstream/state.db`, which holds your sites, users and
settings. It is small. If losing your panel configuration would hurt, copy it off the box on a
schedule. The sites themselves are recoverable from their own snapshots regardless.

## Moving a site to another server

1. take a full backup of the site;
2. install Slipstream on the new server and create a site with the same domain;
3. restore the snapshot into it (`slipctl backup restore <backup-id> <domain> full`);
4. re-issue the certificate once DNS points at the new server.

The alternative is the migration import, which takes a `.tar.gz` and a `.sql` and handles URL
rewriting for you — useful when coming *from* another panel. See [Sites](./sites.md).
