# Getting started

This walks through installing Slipstream on a fresh server, creating your first site, and making
your first safe deployment.

## Before you begin

**Use a server you can rebuild.** Slipstream rewrites nginx, PHP-FPM, MariaDB and systemd
configuration and creates system users. It is designed to own the machine. Do not install it
alongside something you care about.

You need:

| | |
| --- | --- |
| A fresh server | Ubuntu 24.04 LTS or 26.04 LTS, amd64 |
| RAM | 2 GB minimum |
| Disk | 10 GB free |
| Ports 80 and 443 | free — the installer refuses to run otherwise |
| Root access | over SSH |

A £4–6/month VPS is a perfectly good place to start.

## 1. Build

Binary releases are not published yet, so build from source on your workstation. You need
**Go 1.25+** and **Node 20+**:

```bash
git clone https://github.com/Sulaiman-Dauda/slipstream.git
cd slipstream
make dist
```

That builds the web UI and three `linux/amd64` binaries into `dist/`:

| Binary | Runs as | Purpose |
| --- | --- | --- |
| `panel-api` | `slipstream` (unprivileged) | HTTP API and the web UI |
| `panel-agent` | `root` | all privileged system work |
| `slipctl` | you | command-line client |

## 2. Install

Copy the build and the installer to the server, then run it:

```bash
scp -r dist installer scripts root@your-server:/root/slipstream/
ssh root@your-server
cd /root/slipstream
SLIPSTREAM_LOCAL_BUILD=/root/slipstream/dist bash installer/install.sh
```

The installer takes about 80 seconds. It:

- verifies the OS, architecture, memory, disk and that the web ports are free;
- installs nginx, PHP (8.4 on 24.04, 8.5 on 26.04), MariaDB, Restic, certbot and wp-cli;
- tunes the kernel and nginx workers for burst traffic;
- creates the `slipstream` user, the directory layout and the agent's shared secret;
- installs and starts the systemd services;
- opens ports 22, 80 and 443 (and 443/udp for HTTP/3);
- installs log rotation;
- prints a **one-time setup URL**.

It ends with something like:

```
[slipstream] Installation complete.

  Open:  https://203.0.113.10/setup/EquLKW699OJXmANg-y5VP32EKtlj0Xf7

  The setup link is valid for 24 hours.
```

## 3. Create your administrator account

Open that URL. Your browser will warn about the certificate — the panel starts on a self-signed
one, which is expected on first boot. Continue through the warning.

Enter an email address and a password of at least 12 characters. That creates the first
administrator and consumes the setup token, which cannot be reused.

Two things worth doing straight away:

- **Enable two-factor authentication** under your account. The panel is root on this machine.
- **Give the panel a real certificate** once you have a hostname pointing at the server, so you
  stop clicking through warnings. See [Operations](./operations.md#panel-certificate).

## 4. Add your first site

Point a domain's DNS `A` record at the server first — certificate issuance needs it to resolve.
For a quick test without DNS you can use a [sslip.io](https://sslip.io) name, which resolves any
`<anything>.<ip>.sslip.io` to that IP:

```
shop.203.0.113.10.sslip.io
```

In the panel, choose **Sites → New site**, pick a type and enter the domain. Or from the CLI:

```bash
export SLIPSTREAM_EMAIL=you@example.com SLIPSTREAM_PASSWORD='your-password'

slipctl sites create shop.example.com woocommerce \
  '{"admin_email":"you@example.com","admin_user":"you","admin_password":"a-strong-password","title":"My Shop"}'
```

Provisioning takes 15–30 seconds. Slipstream creates:

- a Unix user (`slip-site-<id>`) that owns the site and nothing else;
- a PHP-FPM pool with its own socket, `open_basedir` jail and calculated worker count;
- a MariaDB database and a user scoped to it;
- the directory layout with an immutable first release;
- the application itself — for `woocommerce`, WordPress **and** WooCommerce with its pages
  created;
- an nginx vhost with full-page caching enabled;
- a self-signed certificate, replaced when you issue a real one.

Check it:

```bash
slipctl sites list
curl -I https://shop.example.com/
```

## 5. Get a real certificate

Once DNS resolves to the server:

```bash
slipctl cert issue <site-id>
```

That runs a Let's Encrypt HTTP-01 challenge and installs the certificate. Renewal is automatic via
certbot's timer, and nginx is reloaded on renewal so the new certificate is actually served.

## 6. Confirm caching is working

```bash
curl -sI https://shop.example.com/ | grep -i x-slipstream-cache
```

The first request is a `MISS`, subsequent ones are `HIT`. Pages that must never be cached — cart,
checkout, my-account, anything with a login cookie — return `BYPASS`. See
[Caching](./caching.md).

## 7. Make your first safe deployment

This is the part worth learning early, because it is what stops a bad change reaching visitors.

```bash
# 1. Clone production to a staging copy
slipctl staging create <site-id>

# 2. Make your change on staging (code, plugin update, theme change)

# 3. Ask Performance Guard to compare staging against production
slipctl safe-push <site-id>
```

Safe Push measures production as a baseline and staging as a candidate, then compares p50/p95
latency, error rate, database query count and peak memory. The verdict is one of:

- **pass** — the code is deployed to production as a new immutable release and promoted;
- **warn** — a small regression; you must repeat with `force` to accept it;
- **block** — a real regression. Production is left untouched.

If something is wrong after a promotion:

```bash
slipctl rollback <site-id>
```

Releases are immutable and `current` is a symlink, so rollback is atomic and instant.

## 8. Set up backups

A backup you have never restored is a guess. Slipstream leans on that.

```bash
slipctl settings set backup_repository 's3:s3.amazonaws.com/my-bucket/slipstream'
slipctl settings set backup_password 'a-long-random-passphrase'
slipctl backup run <site-id>
slipctl backup verify <backup-id>       # actually restores it and checks the tree
```

Keep the repository password somewhere outside the server. Without it the backups cannot be
decrypted — that is the point. See [Backups](./backups.md).

## Where to go next

- [Concepts](./concepts.md) — the model behind sites, releases and profiles.
- [Sites](./sites.md) — the other site types and their options.
- [Security model](./security.md) — what is isolated and what is not.
- [Troubleshooting](./troubleshooting.md) — if any of the above did not behave as described.
