# Security policy

Slipstream runs as root on the machine it manages. A vulnerability here is a full host
compromise, so please read this before deploying it and before reporting an issue.

## Current status — read this first

Slipstream is a **release candidate**. It has had **no external security audit** and is not yet
carrying production traffic anywhere.

It has had one deliberate internal adversarial pass over the privileged surface, which found four
real issues — all fixed, all verified on a live server, and all documented in
[docs/security.md](./docs/security.md#findings-from-the-internal-review). The most serious was
cross-tenant database access through the SQL console: an operator scoped to a single site could
read and write every other tenant's database.

Before that, roughly two weeks of pressure-testing found and fixed 21 further defects, several
security-relevant (an authorization bypass between operator accounts, a path that could wipe a
site's files, a read-only SQL console bypassable by statement stacking).

That work made it considerably better, but the honest reading is that the discovery rate has not
yet flattened out, and one internal reviewer is not an audit. **Run it on servers you can afford to
rebuild, keep backups off-box, and do not put it in front of anything critical until it has had an
independent security review.**

## Verifying what you installed

Slipstream is installed with `curl | sudo bash` and its binaries run as root, so
integrity matters more here than for most projects.

Each release publishes a SHA-256 sum per binary, and the installer checks them.
On its own that is weak: the sums live in the same release as the binaries, so
anyone able to write that release could replace both.

From **v0.1.2** every binary also carries a **build provenance attestation**,
signed by GitHub for this repository's release workflow and stored outside the
release. Verify one yourself:

```bash
gh attestation verify /usr/local/bin/panel-agent --repo Sulaiman-Dauda/slipstream
```

The installer runs this automatically when the GitHub CLI is available. It is
not a dependency — if `gh` is absent the install proceeds on checksums alone and
says nothing, rather than pretending to a guarantee it did not make.

## Reporting a vulnerability


**Do not open a public issue for a security problem.**

Use GitHub's private reporting: **Security → Report a vulnerability** on
<https://github.com/Sulaiman-Dauda/slipstream>. That opens a private advisory visible only to the
maintainers.

Please include:

- what an attacker can achieve, and what access they need to start (unauthenticated? a site's
  SFTP user? a read-only panel operator?);
- the steps to reproduce, ideally against a throwaway VM;
- the Slipstream version (`slipctl --version`) and the OS release.

You will get an acknowledgement within **72 hours** and an assessment within **7 days**. This is
maintained by one person alongside other work, so please allow reasonable time for a fix before
disclosing publicly — **90 days** is the default, and it can be shortened by agreement if a fix
ships sooner or if the issue is being exploited.

Credit is given in the advisory and the changelog unless you would rather stay anonymous.

## What is in scope

Anything that crosses a trust boundary the panel is supposed to enforce:

- **Privilege escalation** — from a site's Unix user or SFTP account to another site, or to root.
- **Tenant isolation** — one site reading or writing another site's files, database or cache.
- **Authentication and session handling** — login, 2FA, session tokens, the setup token, magic
  login links.
- **Authorization** — a read-only or site-scoped operator performing actions they should not.
- **The agent RPC socket** — anything that lets an unprivileged local process reach it.
- **Command and SQL injection** through domains, database names, file paths or site config.
- **The file manager, SQL console and cron editor**, which are deliberately powerful and
  deliberately jailed.

## What is not in scope

- Attacks that need existing root access on the host.
- Anything requiring a malicious panel administrator — an admin is root by design.
- A site owner running vulnerable application code (an outdated WordPress plugin, say). The jail
  is meant to limit the blast radius, not to make bad code safe; but a *break out of that jail* is
  very much in scope.
- Missing hardening that has no exploit path. Still worth an issue, just not an advisory.
- Denial of service by simply sending enough traffic.

## The security model, briefly

Understanding this makes for better reports:

- **Every site is a separate Unix user** with its own PHP-FPM pool, socket and `open_basedir`
  jail. `/tmp` is deliberately *not* on `open_basedir` — it is shared between pools.
- **The control plane is split.** `panel-api` runs unprivileged as the `slipstream` user and holds
  no root capability. All privileged work goes to `panel-agent` over a root-owned Unix socket,
  authenticated with a shared token, as a fixed set of typed commands — never a shell string.
- **Sessions are opaque tokens in the database**, not JWTs, so they can be revoked. Passwords are
  hashed with argon2id, and an unknown account still runs a dummy verify so login timing does not
  reveal whether it exists.
- **SFTP is chrooted per site** with no shell.
- **Managed config is hashed and drift-checked**; edits made outside the panel are detected and
  can be restored.

If you find a place where one of those statements is not actually true in practice, that is a
valid and valuable report — several past bugs were exactly that.

## Supported versions

Until 1.0, only the latest release on `main` receives security fixes.
