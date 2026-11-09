# Sites

## Site types

| Type | What it provisions | Document root |
| --- | --- | --- |
| `wordpress` | WordPress, admin account, pretty permalinks, APCu object cache, the Slipstream connector | release root |
| `woocommerce` | everything above **plus WooCommerce**, its pages, and cart-fragment suppression | release root |
| `php` | a working placeholder `index.php` you replace with a deployment | release root |
| `laravel` | the layout with `public/` as the document root, ready for a deployment | `release/public` |
| `static` | a placeholder `index.html`; served straight from disk, no PHP pool | release root |
| `proxy` | an nginx reverse proxy to an upstream you specify; no PHP, no database | n/a |

Every type except `proxy` gets a Unix user, directory layout and TLS. Every type except `proxy` and
`static` also gets a PHP-FPM pool. `wordpress`, `woocommerce`, `php` and `laravel` get a database.

### Creating a site

In the panel: **Sites → New site**. From the CLI:

```bash
# WordPress
slipctl sites create blog.example.com wordpress \
  '{"admin_email":"you@example.com","admin_user":"you","admin_password":"strong-password","title":"My Blog"}'

# WooCommerce — arrives with WooCommerce installed and /shop, /cart, /checkout working
slipctl sites create shop.example.com woocommerce \
  '{"admin_email":"you@example.com","admin_user":"you","admin_password":"strong-password","title":"My Shop"}'

# Static
slipctl sites create www.example.com static '{}'

# Reverse proxy to an app on the same box
slipctl sites create api.example.com proxy '{"proxy_upstream":"http://127.0.0.1:9000"}'

# Laravel — deploy your code afterwards
slipctl sites create app.example.com laravel '{}'
slipctl deploy <site-id> /path/to/built/app
```

If you omit `php_version` the panel uses the version it installed (8.4 on Ubuntu 24.04, 8.5 on
26.04). Asking for a version that is not installed is rejected immediately, naming what is
available, rather than failing halfway through provisioning.

## What a site looks like on disk

```
/srv/sites/example.com/
├── current -> releases/20260726-151844
├── releases/
│   ├── initial/
│   └── 20260726-151844/
├── shared/
│   ├── uploads/            symlinked into every release
│   ├── wp-config.php       symlinked into every release
│   └── exports/            database exports land here
├── logs/
│   ├── php-error.log
│   └── db-latest.sql       most recent dump, included in backups
└── tmp/
    └── sessions/           this site's PHP sessions, not shared
```

Owned by `slip-site-<id>`, with the site root itself `root:slip-site-<id>` so the user cannot
replace the directory it is jailed in.

## Per-site options

Set these in the panel under a site's settings, or with `PUT /api/sites/{id}/config`.

### Caching

| Option | Meaning |
| --- | --- |
| `cache_enabled` | master switch for the full-page cache |
| `cache_ttl_sec` | override the profile's TTL; `0` uses the profile default |
| `object_cache` | persistent object cache (APCu) — on by default for commerce |

See [Caching](./caching.md).

### PHP

| Option | Default |
| --- | --- |
| `php.memory_limit_mb` | 256, or 512 for WooCommerce |
| `php.upload_max_mb` | 64 |
| `php.max_execution_seconds` | 120 |

Change the PHP version with `PUT /api/sites/{id}/php`.

### Resources

| Option | Meaning |
| --- | --- |
| `resources.memory_high_mb` | the site's memory budget; drives worker and OPcache sizing |
| `resources.php_workers` | override the calculated `pm.max_children` |

**Leave `php_workers` alone unless you have measured.** The default is derived from the memory
budget, and on a small server raising it makes things *worse*: measured on a 2-core box, 6 workers
gave 5.54 req/s of uncacheable rendering, 10 gave 4.29 and 16 gave 4.24, with the CPU already at
0% idle throughout. The bottleneck is CPU, not worker count, so extra workers only add context
switching and memory pressure. This is the mistake behind panels that ship `pm.max_children = 250`
and then swap under load.

### Database

A managed database is created automatically. `database.external` points a site at a database
elsewhere instead, in which case Slipstream does not manage it and backups do not include it.

### SFTP

`sftp_enabled` creates a chrooted SFTP account for the site's Unix user. The chroot is the site
root, there is no shell, and the account cannot traverse above its own directory. Set the password
via `POST /api/sites/{id}/sftp`, or add SSH keys via `POST /api/sites/{id}/ssh-keys`.

## Your own nginx rules

Slipstream rewrites a site's vhost whole every time the site is re-rendered: a settings change, a
PHP settings change, a certificate issue, and the certificate renewal check every 12 hours. A rule
edited into the vhost is lost at the next one. Put rules in the site's include directory instead:

```
/etc/slipstream/nginx/example.com/*.conf
```

The HTTPS server block includes every `.conf` file in it. Slipstream creates the directory, owned
by root with mode `0750`, and never writes, changes or deletes anything inside it. To hide an
export folder in uploads, for example:

```bash
cat > /etc/slipstream/nginx/example.com/deny-exports.conf <<'EOF'
location ~* ^/wp-content/uploads/exports/ { return 404; }
EOF
nginx -t && systemctl reload nginx
```

Worth knowing:

- **Test before you reload.** While a broken file is there, `nginx -t` fails, so every site's
  changes fail too. Each failed render is rolled back to the files that were on disk and the task
  error carries nginx's message naming the file and line. nginx keeps serving its loaded
  configuration, and restarting nginx from the panel refuses until the test passes.
- **Use a regex location.** The include sits after the rule that denies dotfiles and before the
  static-asset and PHP locations. nginx checks regex locations in the order they appear, so a
  `location ~` or `location ~*` here wins over Slipstream's for `.pdf`, `.jpg`, `.php` and the
  rest. A plain prefix location such as `location /exports/` does not, because nginx prefers a
  matching regex location over a prefix one. Use `location ^~ /exports/` or a regex. A `^~`
  location also skips the dotfile rule for its paths, so only serve files from one if a
  dotfile there is meant to be public. A second `location /` duplicates the vhost's own and
  fails `nginx -t`.
- **`add_header` replaces, it does not add.** A location of yours that sets any `add_header` drops
  the security headers Slipstream sets at server level for requests it handles. Repeat them in
  your location if you need them. `return` and `deny` set no headers and are unaffected.
- **HTTPS only.** Port 80 only answers certificate challenges and redirects to HTTPS, so a request
  reaches your rules after the redirect.
- **Staging gets an empty directory.** Creating a staging site does not copy these rules. Copy them
  by hand if staging needs them.
- **Deleting a site leaves them.** They are your files. Remove the directory yourself, or a later
  site with the same domain picks the rules up.
- **Existing sites pick up the include at their next re-render.** Sites created before this release
  get the include line the next time they are rendered. Saving the site's settings does it now.
- **Keep it root-only.** nginx reads these files as root when it loads its configuration, so
  anyone who can write here can change how every site on the server is served. Do not loosen the
  directory's permissions.

## WordPress and WooCommerce tools

| Action | What it does |
| --- | --- |
| **One-click login** | mints a single-use, 5-minute token and logs you into wp-admin. The token is validated with a direct database read, so it works regardless of object cache. |
| **Plugin and theme list** | shows installed versions and available updates |
| **Update** | updates core, plugins or themes, then flushes this site's object cache so the live site does not keep serving pre-update state |
| **Object cache** | enables the APCu drop-in (default) or Redis |
| **Cache stats** | hit rate and memory of the object cache, read from the FPM pool rather than the CLI |
| **Warm cache** | crawls the sitemap to pre-populate the page cache |

## Migrating an existing site in

`POST /api/sites/{id}/migration` takes a `.tar.gz` of the site's files and optionally a `.sql`
dump, both already inside the site's own directory (upload them over SFTP). It:

1. extracts into a new release, refusing any archive entry that escapes the target;
2. stages `wp-content/uploads` separately, since uploads are shared data and not release data;
3. imports the database, having first dumped the current one as a rollback point;
4. promotes the release;
5. rewrites URLs from the old domain to the new one;
6. flushes the object cache so the live site reflects the imported database.

If any step fails, files **and** database are rolled back together.

## Deleting a site

Removes the vhost, the FPM pool, the cache zone, the database and user, the certificate and its
renewal config, the site tree, the log directory and the Unix user. FPM is reloaded before the user
is deleted, because `userdel` refuses while a process still holds the identity.
