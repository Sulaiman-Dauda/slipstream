# Staging and Safe Push

The feature Slipstream exists for: a change that makes your site slower does not reach visitors.

## The flow

```
production ──clone──▶ staging ──you change it──▶ Performance Guard ──pass──▶ promoted
                                                        │
                                                        └──block──▶ production untouched
```

```bash
slipctl staging create <site-id>    # clone production to staging
# ... make your change on the staging site ...
slipctl safe-push <site-id>         # measure both, promote only if it holds up
slipctl rollback <site-id>          # if you still don't like it
```

## Staging

A staging site is a full clone: its own Unix user, PHP-FPM pool, database, domain and certificate.
It is a real site, not a preview mode, so what you test is what you promote.

Slipstream copies the current release and the database, rewrites URLs to the staging domain, and
marks the clone as a staging environment. That last part matters, because it switches on a
safeguard: **staging never sends email.** The connector short-circuits `wp_mail()` when the
environment is staging, so a cloned WooCommerce store cannot email real customers about
test orders. The guard is keyed on environment type, so it stays correct even after the code is
promoted to production.

Sync the database again later without touching code:

```bash
POST /api/sites/{id}/staging/database
GET  /api/sites/{id}/staging/tables   # see what would be copied first
```

## Performance Guard

`slipctl safe-push <site-id>` does four things:

1. **Measures production** over loopback with the correct `Host` header — the baseline. Warm-up
   requests are discarded.
2. **Measures staging** the same way — the candidate.
3. **Compares** p50 and p95 latency, error rate, database query count and peak memory. The query
   count and memory come from the connector's own per-request metrics, not a guess.
4. **Decides.**

| Verdict | What happens |
| --- | --- |
| **pass** | staging's code is deployed to production as a new immutable release and promoted |
| **warn** | a small regression — nothing is promoted until you repeat with `force` |
| **block** | a real regression. Production is left completely untouched and the report is stored on the deployment record |

A real block, from testing this feature by deliberately adding a 0.6 s sleep:

```
BLOCK: p95 268ms → 1551ms (+479%)
```

Production was not modified.

### What it does not catch

Be clear-eyed about this. Performance Guard measures **performance**, not correctness. It will
happily promote a change that is fast and wrong. It also cannot see a regression that only appears
under real concurrency, under a cold cache, or on a page it did not sample. It is a safety net for
the specific failure of "this deployment made the site slow", which is the one that usually reaches
production unnoticed.

## Releases and rollback

Every deployment creates a new immutable release directory and moves the `current` symlink. So:

- promotion is **atomic** — no window serving half old and half new code;
- rollback is instant, because it just moves the symlink back;
- old releases stay on disk, so you can roll back more than one step.

```bash
slipctl deploy <site-id> /path/to/code    # create a release from a directory
slipctl rollback <site-id>                # back to the previous release
GET /api/sites/{id}/deployments           # history with Guard verdicts
POST /api/deployments/{id}/promote        # promote a specific release
```

Uploads and configuration live in `shared/` and are symlinked into each release, so they survive
deployments and rollbacks. Credentials are never inside a release.

## After a promotion

The panel reloads PHP-FPM so OPcache picks up the new files immediately — otherwise workers keep
serving the previous bytecode, which can fatal a site when a plugin update changed function
signatures or removed files.

It also flushes that site's object cache when a deployment changed the database, because a stale
object cache will happily serve pre-deployment options and rewrite rules and make real pages 404.
Only the affected site is touched.

You may want to warm the cache afterwards:

```bash
POST /api/sites/{id}/warm
```

## A sensible routine

For a plugin or theme update on a site that matters:

```bash
slipctl staging create <site-id>
# update the plugin on staging, click through the important pages yourself
slipctl safe-push <site-id>
# if the verdict blocks, you have learned something for free
```

For code you deploy from a repository, `slipctl deploy` creates the release directly and you use
staging only when you want the comparison.
