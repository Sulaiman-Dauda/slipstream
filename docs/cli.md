# CLI reference — `slipctl`

`slipctl` is a thin client for the panel API. It authenticates with an email and password and talks
to the panel over HTTPS.

## Authentication

```bash
export SLIPSTREAM_EMAIL='you@example.com'
export SLIPSTREAM_PASSWORD='your-password'
```

| Variable | Default | Purpose |
| --- | --- | --- |
| `SLIPSTREAM_EMAIL` | — | panel account |
| `SLIPSTREAM_PASSWORD` | — | its password |
| `SLIPSTREAM_URL` | `https://127.0.0.1:5252` | panel address |
| `SLIPSTREAM_INSECURE` | — | set to `1` to skip TLS verification for a non-loopback URL |

Certificate verification is skipped automatically for `127.0.0.1`, `localhost` and `::1`, because
the panel starts on a self-signed certificate. For anything else, verification applies.

If the account has 2FA enabled, use the panel UI — `slipctl` does not prompt for a TOTP code.

`slipctl --version` prints the version and exits. So do `panel-api --version` and
`panel-agent --version`; neither starts the daemon.

## Sites

```bash
slipctl sites list
slipctl sites create <domain> <type> [flags-json]
slipctl sites delete <site-id>
```

`<type>` is one of `wordpress`, `woocommerce`, `php`, `laravel`, `static`, `proxy`.

```bash
slipctl sites create blog.example.com wordpress \
  '{"admin_email":"you@example.com","admin_user":"you","admin_password":"strong-password","title":"My Blog"}'

slipctl sites create api.example.com proxy '{"proxy_upstream":"http://127.0.0.1:9000"}'
```

Site creation returns immediately with a task. Poll `slipctl sites list` until the status is
`active`.

## Cache

```bash
slipctl purge <site-id>                                # everything for this site
slipctl purge <site-id> https://example.com/a-page/    # specific URLs
```

## Releases and deployment

```bash
slipctl deploy <site-id> <source-dir>    # create an immutable release from a directory
slipctl rollback <site-id>               # point current back at the previous release
```

## Staging and Safe Push

```bash
slipctl staging create <site-id>   # clone production to a staging site
slipctl safe-push <site-id>        # measure both, promote only if it holds up
```

See [Staging and Safe Push](./staging-and-safe-push.md).

## Backups

```bash
slipctl backup run <site-id>
slipctl backup verify <backup-id>                              # actually restores it
slipctl backup restore <backup-id> <domain> [full|files|database]
```

`verify` restores into a scratch directory, checks the repository and the restored tree, and reports
how long it took. That duration is your recovery estimate.

## Databases

```bash
slipctl database import <site-id> <domain> <site-relative.sql>
```

The `.sql` file must already be inside that site's directory — upload it over SFTP first. The current
database is dumped as a rollback point before the import, and restored automatically if it fails.

## Certificates

```bash
slipctl cert issue <site-id>
```

Runs a Let's Encrypt HTTP-01 challenge. DNS for the domain must already point at the server.
Renewal is automatic, and nginx is reloaded on renewal so the new certificate is actually served.

## Cron

```bash
slipctl cron run <cron-id>     # run a scheduled job now
```

Create and list cron jobs through the panel or the API. Jobs run as the site's unprivileged user.

## Server

```bash
slipctl status     # CPU, memory, disk headroom, service states, uptime
slipctl drift      # managed files that no longer match what the panel wrote
slipctl tasks      # recent background tasks with status and logs
slipctl audit      # audit log: who did what, from where
```

`slipctl status` is the quickest health check:

```json
{
  "agent_version": "ecdd48b",
  "cpu_count": 2,
  "cpu_headroom_pct": 87,
  "disk_free_mb": 48473,
  "mem_available_mb": 1395,
  "mariadb_running": true,
  "nginx_running": true
}
```

## Settings

```bash
slipctl settings get
slipctl settings set <key> <value>
```

| Key | Default | Purpose |
| --- | --- | --- |
| `backup_repository` | — | Restic repository URL |
| `backup_password` | — | Restic repository passphrase — **keep a copy off this server** |
| `acme_email` | — | address Let's Encrypt uses for expiry notices |
| `probe_target` | `https://127.0.0.1` | where Performance Guard sends its measurement requests during a Safe Push |

These four are the only settings the API will write; `slipctl settings get` masks
`backup_password` to a presence indicator rather than returning it.

`probe_target` exists because Safe Push measures the site over HTTP with the site's `Host` header.
The default hits the loopback interface so measurement never leaves the machine — change it only if
the panel must probe through something else.

Changing `backup_repository` or `backup_password` re-points where backups go; existing snapshots stay
in the old repository, so keep its password if you might need them.

## Exit codes

`0` success. Non-zero on failure, with the error on stderr — safe to use in scripts and CI.
