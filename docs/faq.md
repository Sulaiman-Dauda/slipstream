# FAQ

### Is this ready for production?

It is a **release candidate**. It installs and runs cleanly on Ubuntu 24.04 and 26.04, with its
55-check end-to-end suite green on both (3 September 2026). It has had one deliberate internal
security pass. It
has **not** had an external audit. Two client sites have run on it, on a single 1 GB server,
since August.

Run it on servers you can rebuild, keep backups off the box, and read
[SECURITY.md](../SECURITY.md). If you need a panel with a decade of production behind it today, use
one of those instead — that is an honest answer, not modesty.

### How is this different from CloudPanel, HestiaCP or cPanel?

Two things.

**Performance is the product, not a settings page.** Sites arrive cached and tuned from measured
defaults rather than distro defaults. The numbers are in [Benchmarks](./benchmarks.md), taken on the
same physical machine as the competitor.

**A slow deployment is a failed deployment.** Performance Guard measures a candidate against
production and refuses to promote a regression. No other panel does this as far as we know.

What the others do better: they are mature, they have far more features (email, DNS, resellers), and
they have years of production behind them. Slipstream deliberately does hosting only.

### Does it support Apache / LiteSpeed / Caddy?

nginx only today. The engine is behind an abstraction (`engine.Renderer`) so another can be added,
but only if benchmarks justify the maintenance.

### Does it do email, DNS or billing?

No, deliberately. Each is a product in its own right, and a panel that does all of them badly is
worse than one that does hosting well. Use a dedicated provider.

### Can I run it alongside my existing web server?

No. It must own ports 80 and 443, and it rewrites nginx, PHP-FPM and MariaDB configuration. Give it
its own server.

### Which operating systems are supported?

Ubuntu 24.04 LTS and 26.04 LTS, amd64. Both are tested on every change. Other distributions are not
supported — one target done well beats five done approximately.

### Do I need to know nginx?

No for normal use. Yes if you want to hand-edit a vhost, and if you do, the panel will detect the
change as drift and ask what you want to do about it rather than silently reverting you.

### What happens if I edit a config file by hand?

Nothing immediately — Slipstream does not watch and revert. Next time drift is checked it appears,
and you choose to restore the rendered version or accept yours. See
[Operations](./operations.md#drift).

### How do I move a site here from another host?

Upload a `.tar.gz` of the files and a `.sql` dump into the site over SFTP, then use the migration
import. It extracts safely, stages uploads separately, imports the database with a rollback point,
promotes, and rewrites URLs from the old domain. See [Sites](./sites.md).

### Why APCu instead of Redis?

On a single server APCu is faster: it lives in the PHP process, so there is no socket round trip and
no serialisation. Measured on a WooCommerce store, forcing Redis was *slower* — 4.13 req/s versus
4.56 with APCu. Redis is available for when you genuinely have more than one application server.

### Why is my WordPress change not showing up?

Almost always a stale object cache after using wp-cli by hand. wp-cli and PHP-FPM do hold separate
APCu segments, but a flush crosses that boundary: every key carries an epoch from a file both
processes read, and flushing writes a new one. Run `wp cache flush` in the release directory. The
panel's own update, restore and migration paths flush through the connector and need nothing from
you. See [Troubleshooting](./troubleshooting.md).

### How many sites can one server handle?

It depends entirely on traffic shape. Cached traffic is nearly free, 9,280 req/s on two cores, so
a dozen mostly-cached brochure sites on a 2 GB box is comfortable. One busy WooCommerce store with a
lot of logged-in traffic can saturate the same box on its own, because carts and checkouts cannot be
cached.

Watch memory headroom in `slipctl status`. The defaults are sized to keep you out of swap.

### Should I increase the PHP worker count?

Almost certainly not. Measured on a 2-core box: 6 workers gave 5.54 req/s of uncacheable rendering,
10 gave 4.29, 16 gave 4.24 — with the CPU already at 0% idle. The path is CPU-bound, so extra workers
only add context switching and memory pressure. This is how panels that ship
`pm.max_children = 250` end up swapping.

### Is HTTP/3 supported?

Yes, automatically, where the OS ships an nginx built with it — Ubuntu 26.04 does, 24.04 does not.
Slipstream detects the capability rather than vendoring its own nginx, so you gain HTTP/3 by
upgrading the OS.

### Are backups really encrypted?

Yes, client-side by Restic before anything leaves the machine. Which means **if you lose the
repository password, the backups are unrecoverable** — by you or anyone. Store it in a password
manager now, not on the server.

### What is a "verified restore"?

An actual restore of a snapshot into a scratch directory, plus a repository check and a sanity check
of the restored tree and database dump, with the duration measured. That duration becomes your
recovery-time estimate. A backup that has never been restored is a guess.

### Can I use it for clients, with separate logins?

Yes. The `operator` role is scoped to assigned sites — an operator sees only their own sites and gets
a 404 on anything else. `readonly` can view everything and change nothing. Note that a panel
**admin** is effectively root on the machine; the roles limit those accounts, not the admin.

### Is there an API?

The whole panel is one — the web UI is just a client. See [API reference](./api.md) and
[CLI reference](./cli.md).

### What licence is it under?

[AGPL-3.0](../LICENSE). Free to use, modify and self-host. If you run a modified Slipstream **as a
network service**, you must publish your changes.

### Can I use it commercially?

Yes — including hosting client sites on it. The AGPL only requires publishing your modifications if
you offer a modified version as a service. Hosting your own or your clients' sites on unmodified
Slipstream carries no obligation.

### How do I report a security issue?

Privately, through GitHub's security advisories. Never a public issue. See
[SECURITY.md](../SECURITY.md).

### How can I help?

Run it and report what breaks — on a throwaway server. Bug reports from real installs on hardware
and workloads we do not have are the most valuable thing right now. See
[CONTRIBUTING.md](../CONTRIBUTING.md).
