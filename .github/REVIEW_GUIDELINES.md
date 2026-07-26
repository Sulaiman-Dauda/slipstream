# Review guidelines

The standard a pull request is held to here. Written down so a review is the
same whoever runs it, and so a contributor can predict what will be asked
before they are asked it.

Slipstream takes root on someone's server, provisions their sites and holds
their backup keys. That raises the bar above a typical web project — not to make
contributing unpleasant, but because the failure modes are other people's
production servers.

## Before reviewing

- **Read the linked issue.** A PR with no issue and no explanation of the
  problem gets that question first, not a line-by-line review.
- **Check CI honestly.** Green is necessary, not sufficient. A red build is not
  "flaky" until someone has looked at why.
- **Run it if it touches the data plane.** nginx, PHP-FPM, MariaDB, the
  installer and the agent all get exercised on a throwaway server, not reasoned
  about. See [Verification](#verification).

## What gets checked

### Correctness

- Does it do what the description says, and only that? Unrelated changes get
  split out — a bug fix bundled with a refactor is two reviews pretending to be
  one.
- What happens on the unhappy path? Missing file, dead network, disk full,
  killed mid-operation. This codebase runs unattended.
- Is anything now silently swallowed? An error that becomes a `|| true` needs a
  reason in a comment.

### The privilege boundary

This is the part reviewers must not wave through.

- **`panel-api` runs unprivileged; `panel-agent` runs as root.** Anything new
  crossing that socket is a typed RPC command with validated parameters — never
  a shell string, never an interpolated path.
- Commands are built as **argv arrays**. A change that introduces `sh -c` with
  interpolated input is rejected on sight.
- Site-scoped file operations stay inside the site. Path handling goes through
  the existing `openBeneath` helpers rather than string concatenation.
- New SQL is parameterised, and identifiers are validated against the existing
  pattern rather than trusted.

### Security-relevant surface

- Does it widen what an operator or a compromised site can reach? Say so in the
  PR body.
- Secrets: never in argv (readable from `/proc`), never in logs, never in a
  committed file. Passwords reach MariaDB on stdin for this reason.
- Anything touching auth, sessions, TOTP or the setup token gets a second pass,
  and a test.

### Tests

- A bug fix comes with a test that **fails without the fix**. If it passes
  before and after, it is not testing the fix — this has happened here.
- New behaviour comes with coverage of at least one failure path, not only the
  happy one.
- `go test -race` must pass. It has caught a genuine data race in this codebase.

### Documentation

- Behaviour visible to a user changes `docs/`. The docs are published, so a
  change that leaves them stale ships a lie.
- New settings, CLI commands and API endpoints get documented in the same PR.
  Every one of these has been missed before and found later by audit.
- `CHANGELOG.md` gets an entry for anything a user would notice.

### Style

- `gofmt` and `go vet` clean; `shellcheck` clean for shell.
- Comments explain **why**, not what. A comment restating the code is noise; a
  comment recording a measurement, a constraint or a rejected alternative is
  the most valuable thing in the diff.
- Match the surrounding code. Consistency beats personal preference.

## Verification

Reading a diff is not review for anything that touches a running system.

| Change | How it is verified |
| --- | --- |
| Installer | Full run on a throwaway Ubuntu 24.04 **and** 26.04 server |
| nginx / caching | Response headers on the wire, not the rendered config |
| PHP-FPM / isolation | Confirm the jail actually holds, from another site's user |
| Backups | A restore that is checked, not an upload that succeeded |
| UI | Rendered pixels and computed styles, not the JSX |
| Performance | A/B on the same box, one variable at a time |

A directive present in a config file is not a directive in effect. This project
has shipped nginx security headers that were silently dropped, and a capability
probe that read stdout for a tool writing to stderr. Both looked correct in
review.

## Merging

- **Squash only.** The PR title becomes the commit subject, so it is rewritten
  to be a good one rather than left as "fix stuff".
- The commit body says what changed and **why**, and records anything measured.
- Required checks pass, conversations resolved, branch up to date with `main`.

## Turning something down

Say so early and say why. A PR left open for weeks is worse than a clear no in
a day. If the idea is right but the implementation is not, say which. If it is
out of scope, point at the scope.

Contributors are doing unpaid work. The tone is "here is what this needs",
never "this is wrong".
