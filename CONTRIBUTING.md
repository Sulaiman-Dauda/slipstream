# Contributing to Slipstream

Thanks for taking an interest. Slipstream takes root on the machines it manages, so the bar for
changes is a little higher than usual — this page explains what that means in practice.

## Before anything else

**Never run the installer against a machine you care about.** It rewrites nginx, PHP-FPM,
MariaDB and systemd configuration and creates system users. Use a throwaway VM. A £4/month cloud
instance you can rebuild is the right development environment.

## Getting set up

You need **Go 1.25+** and **Node 20+**.

```bash
git clone https://github.com/Sulaiman-Dauda/slipstream.git
cd slipstream
make build          # builds the UI, then the three binaries into bin/
make test           # go vet + go test ./...
```

The binaries:

| Binary | Runs as | Does |
| --- | --- | --- |
| `panel-api` | `slipstream` (unprivileged) | HTTP API + embedded web UI |
| `panel-agent` | `root` | all privileged system work, over a Unix socket |
| `slipctl` | you | command-line client for the API |

The frontend lives in `ui/` and its build output is committed to `ui/dist/`, because
`go:embed` needs it for the binary to build without npm. **If you change anything in `ui/src/`,
run `make ui` and commit the regenerated `ui/dist/`** — otherwise your change won't appear in
the binary.

## The house rule: measure it on a box

This is the one thing to internalise. Nearly every bug in this project's history looked correct
in review and was wrong on the wire:

- security headers were silently dropped for months, because nginx inherits `add_header` **only**
  when the inner block declares none;
- an HTTP/3 capability probe read stdout from `nginx -V`, which writes to **stderr**, so the
  feature never activated anywhere;
- a `woocommerce` site type never actually installed WooCommerce;
- kernel TLS, copied from a competitor as an obvious win, cost **28%** of cached throughput.

So: check the response headers, the negotiated protocol, the listening socket, the syscalls. A
passing config test is not evidence.

### Verify end to end

`scripts/e2e-verify.sh` provisions every site type on a real server, **fetches the served page**,
exercises the feature, deletes the site and confirms teardown:

```bash
IP=<public-ip> PANEL_EMAIL=<email> PANEL_PW=<password> bash scripts/e2e-verify.sh
```

It must be **43/43 green** before a change lands. If you add a feature, add an assertion — and
make sure that assertion tests *the product*, not the test's own setup. The WooCommerce check
used to install the plugin itself before asserting it was installed, which hid a real bug for
weeks.

### Performance changes

`bench/` exists because performance is the product claim. If you touch caching, request handling
or tuning defaults, benchmark before and after and put the numbers in the pull request.

Tuning defaults are **A/B measured, one directive at a time**, and anything that doesn't
reproduce a win is not shipped. `installer/install.sh` documents what was tried and rejected,
with numbers — add to that list rather than quietly re-adding a setting. Two servers of the same
spec have measured 2.5× apart, so confirm CPU parity before attributing a difference to code.

## Pull requests

1. Branch from `main`.
2. Keep it focused. One concern per PR.
3. `make test` and `make vet` must pass.
4. Explain **why**, not just what. The commit log here is the project's memory — describe the
   failure the change fixes and how you confirmed it.
5. Say how you verified it. "Ran e2e on Ubuntu 24.04, 43/43" is what a reviewer needs.
6. Update `docs/` if behaviour changed.

Small fixes are welcome without prior discussion. For anything structural — a new site type,
changing the agent RPC surface, altering the security model — please open an issue first so we
can agree the approach before you spend time on it.

## Code style

- Standard Go: `gofmt`, `go vet` clean, no new dependencies without a stated reason. The project
  is deliberately lean — SQLite for state, no Redis, no message broker, no ORM.
- **Comments explain why, not what.** The existing comments record hard-won reasons ("a reload
  keeps APCu alive, so restore must restart"); match that.
- The agent builds **argv arrays, never shell strings**. That boundary is what keeps a domain
  name from becoming command injection. Do not cross it.
- User-facing copy is **en-GB**.

## Reporting bugs

Open an issue with the version (`slipctl --version`), the OS release, what you expected and what
happened. Panel logs (`journalctl -u slipstream-api -u slipstream-agent`) and the relevant site
log under `/var/log/slipstream/<domain>/` usually contain the answer.

**Security issues go through the [security policy](./SECURITY.md), not the issue tracker.**

## Licence

Contributions are accepted under the [AGPL-3.0](./LICENSE), the same licence as the project. By
opening a pull request you confirm you have the right to contribute the code under that licence.
