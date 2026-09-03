# Roadmap

What is planned, what is deliberately out of scope, and where help is most
useful. Kept short on purpose — a roadmap listing everything commits to nothing.

Dates are omitted because this is maintained by one person alongside other work,
and inventing them would be dishonest. Order is roughly priority.

## Before 1.0

The blockers are about confidence, not features.

- **Production traffic.** Slipstream runs correctly on both supported Ubuntu
  releases with the end-to-end suite green on each, and has served one real site
  on a 1 GB server since August 2026. One site for a few months is a start, not a
  track record, and this stays the single biggest gap between "works" and
  "trustworthy".
- **An external security review.** There has been one deliberate internal pass
  (see [SECURITY.md](./SECURITY.md)); no independent eyes. A control panel that
  takes root should not claim 1.0 without them.
- **Restore drills as routine, not a feature.** Verified restores exist; what is
  missing is evidence they hold across upgrade boundaries and repository
  migrations.
- **ARM64.** amd64 only today. The build is portable; the tuning heuristics and
  the e2e suite are what need proving.

## Wanted, not yet built

- **A demo people can click.** The panel's own surface is its dangerous surface —
  a file manager, a SQL console and cron all run as the site user — so a public
  live instance is not on the table. A build of the real UI against fixture data
  is, and would answer "what is it actually like" better than screenshots.
- **Preflight that reports every failure at once** rather than one per run
  ([#12](https://github.com/Sulaiman-Dauda/slipstream/issues/12)).
- **A third state for services**, so a deliberately-disabled service does not
  look identical to a crashed one
  ([#11](https://github.com/Sulaiman-Dauda/slipstream/issues/11)).
- **R2 and other S3 backends documented as backup targets.** Restic already
  supports them; free egress makes object storage a strong restore target and
  that deserves a worked example.
- **Flattening the release docroot.** Measured at roughly 8% on uncacheable
  renders by reducing `open_basedir` path-component checks. Deferred because the
  risk to the release/rollback model outweighed 8% — revisit with a safer design,
  not by weakening isolation.

## Deliberately out of scope

Saying no to these is a design position, not a backlog.

- **Email, DNS and domain registration.** Each is a product in its own right. A
  panel that does all three badly is worse than one that does hosting well.
- **Billing and resellers.** Slipstream manages a server, not a hosting business.
- **Multi-server orchestration.** One panel, one machine. Clustering changes
  every assumption in the security model.
- **Windows, cPanel migration, and non-Ubuntu distributions.** Two tested LTS
  releases is a promise that can be kept; a matrix is not.

## Where help is most useful

Ranked by value to the project rather than by ease:

1. **Run it and report what broke.** Especially on hardware or workloads unlike
   a 2 vCPU Vultr box. A bug report with reproduction steps against a throwaway
   server is worth more than a feature.
2. **Adversarial attention on the privilege boundary.** `panel-api` is
   unprivileged, `panel-agent` is root, and everything between them is a typed
   RPC. Finding a way across that boundary is the most valuable contribution
   anyone could make. Report it privately — see [SECURITY.md](./SECURITY.md).
3. **Documentation that fixes something that confused you.** The person who was
   just confused writes the best version of that page.
4. **The issues labelled [good first
   issue](https://github.com/Sulaiman-Dauda/slipstream/issues?q=is%3Aissue+is%3Aopen+label%3A%22good+first+issue%22).**

Before building anything structural, open an issue first. It is a short
conversation that can save a weekend.
