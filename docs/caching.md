# Caching — the Velocity Engine

The Velocity Engine is nginx's FastCGI cache, configured per site, plus a WordPress plugin that
tells the panel exactly which pages to invalidate. There is no third-party cache plugin and no
purge module.

The important property: **a cache hit never runs PHP**. nginx serves it from disk. That is why
cached throughput is roughly 1,500× the uncached rate on the same machine.

## What is cached

Anonymous `GET` and `HEAD` requests that return 200 or 301. That is it.

## What is never cached

A request bypasses the cache entirely if **any** of these is true:

| Condition | Why |
| --- | --- |
| the method is `POST` | it changes something |
| there is a query string | `?add-to-cart=`, `?s=search`, tracking parameters |
| a login cookie is present | `wordpress_logged_in_`, `wp-postpass_`, `comment_author_` |
| a cart cookie is present | `woocommerce_cart_hash`, `woocommerce_items_in_cart`, `wp_woocommerce_session_` |
| the path is dynamic | `/wp-admin`, `/wp-login.php`, `/wp-cron.php`, `/xmlrpc.php`, `/wp-json/`, `/feed`, `/cart`, `/checkout`, `/my-account`, `/addons`, `/wc-api` |

Check any URL:

```bash
curl -sI https://example.com/        | grep -i x-slipstream-cache   # HIT / MISS
curl -sI https://example.com/cart/   | grep -i x-slipstream-cache   # BYPASS
```

`HIT` served from cache · `MISS` generated and stored · `BYPASS` never eligible ·
`STALE` served stale while revalidating · `UPDATING` a refresh is in flight.

## Pre-compressed storage

Most panels store cached HTML uncompressed and gzip it on every request. That makes gzip the
throughput ceiling — we measured 944 req/s doing it that way versus 2,884 req/s without compression
at all.

Slipstream has PHP compress its own output (`zlib.output_compression`), so nginx caches the
**already-compressed** body and serves hits verbatim. The cache key includes a normalised
`Accept-Encoding` bucket so a gzip variant and an identity variant are stored separately and never
served to the wrong client. That single change took cached WooCommerce throughput from 944 to
8,664 req/s on a 2-core box.

## Coalescing, stale-while-revalidate, stale-if-error

Three behaviours that matter when a popular page expires:

- **Request coalescing** (`fastcgi_cache_lock`) — if 500 visitors hit an expired page at once, one
  request regenerates it and the rest wait briefly for that result. Without this you get a
  stampede: 500 simultaneous PHP renders on a box that can serve six.
- **Stale-while-revalidate** — an expired page is served immediately from the stale copy while the
  refresh happens in the background. Visitors never wait for a regeneration.
- **Stale-if-error** — if PHP returns 500/503, times out or the upstream is down, the stale copy is
  served instead of an error page. A crashed PHP pool degrades to slightly-old content rather than
  an outage.

## Invalidation

The Slipstream connector — a WordPress mu-plugin installed automatically — watches for content
changes and asks the panel to purge **exactly the affected URLs**, not the whole site:

Publishing or updating a post purges that post's permalink, the home page, its post-type archive,
the feed, each of its categories and tags, the author archive and the year/month archives. A new
comment purges only that post. Switching theme, activating or deactivating a plugin, editing a menu
or saving the customiser purges everything, because those can change any page.

Purge manually:

```bash
slipctl purge <site-id>                              # everything
slipctl purge <site-id> https://example.com/a-page/  # specific URLs
```

## Object cache

Separate from the page cache. The page cache stores finished HTML; the object cache stores the
database query results WordPress builds that HTML from, and is what speeds up logged-in traffic
and cache misses.

**APCu is the default**, and on a single server it beats Redis: it lives in the PHP process, so
there is no socket round trip and no serialisation. Measured on a WooCommerce store, forcing Redis
made things *slower* — 4.13 req/s versus 4.56 with APCu. Redis remains available for when you
genuinely have more than one application server.

One thing to know: **wp-cli and PHP-FPM have separate APCu segments.** A cache flush from the
command line cannot clear the web server's copy, and an FPM *reload* does not either — the master
process survives and keeps its shared memory. Slipstream handles this by asking the site's own
connector to flush through its FPM pool, which affects that site only. If you run wp-cli by hand
and the site seems to ignore your change, that is why; the panel's own update, restore and
migration paths do it correctly.

## Cache warming

```bash
POST /api/sites/{id}/warm
```

Crawls the site's sitemap and requests each URL so the cache is populated before visitors arrive.
Worth doing after a deployment or a full purge.

## Tuning

Per site: `cache_enabled`, `cache_ttl_sec` (0 = the profile default), and the profile itself
(Balanced 10 min / Commerce 5 min / Maximum 24 h). See [Concepts](./concepts.md#profiles).

Cache files live in `/var/cache/slipstream/<domain>/`, with an in-memory key zone sized per site
and a disk cap. Entries are evicted least-recently-used when the cap is reached, so the cache
cannot fill the disk.

## When caching is not the answer

The page cache does nothing for logged-in users, cart and checkout, or an API returning per-user
JSON. If those are your bottleneck, the levers are the object cache, the application's own query
count, and PHP's own speed — not the page cache. [Benchmarks](./benchmarks.md) has the measured
uncacheable numbers, including the 24% cost of the `open_basedir` jail we keep on purpose.
