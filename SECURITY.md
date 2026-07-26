# Security policy

Slipstream runs as root on the machine it manages. A vulnerability here is a full host
compromise, so please read this before deploying it and before reporting an issue.

## Current status — read this first

Slipstream is a **release candidate**. It has not had an external security review, and it is not
yet running production traffic anywhere. During roughly two weeks of deliberate pressure-testing,
21 real defects were found and fixed, several of them security-relevant (an authorization bypass
between operator accounts, a path that could wipe a site's files, and a read-only SQL console that
could be bypassed with statement stacking).

That work made it considerably better, but the honest reading is that the discovery rate has not
yet flattened out. **Run it on servers you can afford to rebuild, keep backups off-box, and do not
put it in front of anything critical until it has had a dedicated security review.**

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
  hashed with scrypt in application code.
- **SFTP is chrooted per site** with no shell.
- **Managed config is hashed and drift-checked**; edits made outside the panel are detected and
  can be restored.

If you find a place where one of those statements is not actually true in practice, that is a
valid and valuable report — several past bugs were exactly that.

## Supported versions

Until 1.0, only the latest release on `main` receives security fixes.
