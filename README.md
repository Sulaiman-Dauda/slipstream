# Slipstream

**[slipstreampanel.com](https://slipstreampanel.com)** · [Documentation](https://slipstreampanel.com/guides/readme/) · [Benchmarks](https://slipstreampanel.com/guides/benchmarks/)

A hosting control panel for people who care what their sites' p99 looks like.

One command installs it. One click launches a WordPress, WooCommerce, PHP, Laravel, static or
reverse-proxy site that is already cached, already tuned, already isolated, and already backed up
off-site. Deployments are measured before they go live: if a change makes the site slower, the
promotion is refused.

Slipstream runs on your own server. There is no hosted plan, no per-site fee and no phone-home.

> **Status: release candidate.** It installs and runs cleanly on Ubuntu 24.04 and 26.04, with a
> 43-check end-to-end suite green on both. It is not yet carrying production traffic anywhere and
> has had no external security audit. Run it on servers you can afford to rebuild, and read
> [SECURITY.md](./SECURITY.md) before pointing it at anything that matters.

## Why it exists

Most panels treat performance as the operator's problem: they give you nginx, PHP-FPM and MariaDB
with distro defaults and a settings page. Slipstream ships opinionated, measured defaults and
treats a performance regression as a deployment failure.

Benchmarked against CloudPanel on the **same physical server**, one panel at a time, with an
identical WordPress + WooCommerce workload and both panels tuned to their best (CloudPanel with
Varnish, a hand-written WordPress VCL, Redis object cache and OPcache JIT):

| | Slipstream | CloudPanel |
| --- | --- | --- |
| Cached throughput, sustained 500 connections | **9,840 req/s** | 2,495 req/s |
| p99 latency under that load | **83 ms** | 6.46 s |
| 2,000-connection flood | **8,051 req/s**, 0 errors | 163 req/s, 100 timeouts |
| Static assets | **21,024 req/s** | 5,117 req/s |
| Install time | **79 s** | 410 s |
| Disk footprint | **+0.67 GB** | +4.44 GB |
| Peak RAM under load | **823 MB, no swap** | 1,754 MB + 574 MB swap |

Where CloudPanel wins, honestly: **uncacheable dynamic rendering**. A WooCommerce product page
that cannot be cached renders in ~189 ms here versus ~168 ms there. That gap is the measured cost
of running every site inside an `open_basedir` jail (72 ms of a 301 ms render), which we keep on
purpose.

Both panels' numbers, the method, and the two directives we tried and **rejected** because they
measured worse are in [docs/benchmarks.md](./docs/benchmarks.md). The suite is in `bench/` and
`scripts/`, so you can re-run it rather than take our word for it.

## What you get

- **Sites in one step.** WordPress, WooCommerce, PHP, Laravel, static and reverse-proxy site
  types. A WooCommerce site arrives with WooCommerce installed and its pages resolving, not as a
  blank WordPress you have to finish.
- **The Velocity Engine.** Full-page caching in nginx with precise invalidation from a WordPress
  mu-plugin, request coalescing, stale-while-revalidate and stale-if-error. Cached responses are
  stored pre-compressed, so a cache hit costs no CPU to gzip.
- **Tuning that reads the hardware.** PHP-FPM workers, OPcache and the InnoDB buffer pool are
  sized from the memory actually available, not from a fixed number. On a small box that means it
  stays up instead of swapping.
- **Safe Push.** Clone production to staging, make the change there, then let Performance Guard
  compare the two. A candidate that regresses p95, query count or error rate is **blocked**, and
  every release is one command away from rollback.
- **Backups you have actually tested.** Encrypted, deduplicated, off-site backups via
  [Restic](https://restic.net), with scheduled restores into a scratch directory to prove the
  snapshot works — not just that it uploaded.
- **Isolation by default.** Every site gets its own Unix user, PHP-FPM pool, socket and
  `open_basedir` jail, plus a chrooted SFTP account with no shell.
- **Config drift detection.** Managed files are hashed. Edit an nginx vhost by hand and the panel
  tells you, rather than silently overwriting your change on the next render.
- **HTTP/3.** Enabled automatically where the OS ships an nginx built with it (Ubuntu 26.04), off
  where it doesn't (24.04). No custom nginx build to maintain.
- **Let's Encrypt**, per-site cron, a jailed file manager, a database console, one-click login to
  wp-admin, and a self-update that health-checks the new binaries and rolls itself back if they
  fail.

## Requirements

| | |
| --- | --- |
| OS | Ubuntu 24.04 LTS or 26.04 LTS |
| Architecture | amd64 |
| RAM | 2 GB minimum |
| Disk | 10 GB free |
| Ports | 80 and 443 must be free — Slipstream owns them |

It configures nginx, PHP-FPM, MariaDB and systemd on the machine, so give it a server of its own.

## Install

> Slipstream takes over system configuration. **Install it on a fresh server**, not one already
> running something you care about.

On a fresh Ubuntu 24.04 or 26.04 server, as root:

```bash
curl -fsSL https://get.slipstreampanel.com | sudo bash
```

That is the whole install. It checks the machine, installs nginx, PHP-FPM, MariaDB, restic,
certbot and wp-cli, downloads the Slipstream binaries and verifies each against its published
SHA-256, creates the users and directories, generates the secrets, starts the services, and
prints a one-time setup URL. About **eighty seconds**, no questions asked along the way — and it
shows each step with a tick and its duration as it goes, so you can see it working rather than
guess. The full output is kept at `/var/log/slipstream-install.log`.

Open the URL, create the administrator account, and add your first site. Expect a browser
certificate warning until the panel has a real certificate — it starts on a self-signed one.

<details>
<summary>Building from source instead</summary>

You need Go 1.25+ and Node 20+ on your workstation:

```bash
git clone https://github.com/Sulaiman-Dauda/slipstream.git
cd slipstream
make dist                       # builds the UI and linux/amd64 binaries into dist/
scp -r dist installer scripts root@your-server:/root/slipstream/
ssh root@your-server 'cd /root/slipstream && SLIPSTREAM_LOCAL_BUILD=/root/slipstream/dist bash installer/install.sh'
```

`SLIPSTREAM_LOCAL_BUILD` skips the download and checksum step and installs the binaries you
just built. This is the path used to test unreleased changes.

</details>

Full walkthrough: [docs/getting-started.md](./docs/getting-started.md).

## Roadmap

What is planned, what is deliberately out of scope, and where help is most useful:
[ROADMAP.md](./ROADMAP.md).

## Documentation

| | |
| --- | --- |
| [Getting started](./docs/getting-started.md) | Install, first site, first deployment |
| [Concepts](./docs/concepts.md) | Sites, releases, profiles, the desired-state model |
| [Sites](./docs/sites.md) | Every site type and its options |
| [Caching](./docs/caching.md) | The Velocity Engine, what is cached, how to invalidate it |
| [Staging and Safe Push](./docs/staging-and-safe-push.md) | Clone, compare, promote, roll back |
| [Backups](./docs/backups.md) | Restic repositories, schedules, verified restores |
| [Security model](./docs/security.md) | Isolation, the privilege boundary, what is jailed |
| [Architecture](./docs/architecture.md) | How the pieces fit together |
| [CLI reference](./docs/cli.md) | `slipctl` |
| [API reference](./docs/api.md) | The HTTP API |
| [Operations](./docs/operations.md) | Upgrades, logs, drift, monitoring |
| [Troubleshooting](./docs/troubleshooting.md) | When something is wrong |
| [Benchmarks](./docs/benchmarks.md) | Numbers, method, and what we rejected |
| [FAQ](./docs/faq.md) | Common questions |

## Architecture at a glance

```
                Browser / CDN
                      │
        nginx  ── full-page cache · coalescing · SWR · stale-if-error
                      │
        PHP-FPM  ── per-site pool, user, socket and open_basedir jail
                      │
        MariaDB  ── hardware-aware tuning        APCu object cache
────────────────────────────────────────────────────────────────────
 Control plane
   panel-api     unprivileged · HTTP API + embedded UI · holds no root
   panel-agent   privileged · typed commands over a root-owned socket
   SQLite        desired state, the single source of truth
```

`panel-api` runs as an unprivileged user and can do nothing to the system directly. Every
privileged action is a **typed command** — `CreateSite`, `DeployRelease`, `RestoreSnapshot` — sent
to `panel-agent` over an authenticated Unix socket. The agent builds argv arrays, never shell
strings, which is the boundary that stops a domain name from becoming command injection.

## Development

```bash
make build     # UI + binaries into bin/
make test      # go vet + go test ./...
make dist      # linux/amd64 release binaries
```

Verify a real server end to end — this provisions every site type, fetches the served page,
exercises the features and tears everything down:

```bash
IP=<public-ip> PANEL_EMAIL=<email> PANEL_PW=<password> bash scripts/e2e-verify.sh
```

See [CONTRIBUTING.md](./CONTRIBUTING.md). The one habit that matters here: **measure it on a box,
don't trust the diff.** Nearly every bug in this project's history looked correct in review and
was wrong on the wire.

## Licence

[GNU AGPL-3.0](./LICENSE). Free to use, modify and self-host. If you run a modified Slipstream as
a network service, you must publish your changes.
