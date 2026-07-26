# Backups

Slipstream's position on backups: **a backup you have never restored is a guess.** So the panel
does not just take snapshots, it periodically restores them and tells you how long that took.

Backups use [Restic](https://restic.net): encrypted client-side, deduplicated, and able to write to
almost any object store.

## Set up a repository

```bash
slipctl settings set backup_repository 's3:s3.amazonaws.com/my-bucket/slipstream'
slipctl settings set backup_password 'a-long-random-passphrase'
```

Any Restic backend works — S3, Backblaze B2, Wasabi, SFTP, or a local path for testing. Provider
credentials come from the environment in the agent's systemd unit (`AWS_ACCESS_KEY_ID` and friends).

Check it before relying on it:

```bash
POST /api/backups/test      # initialises the repository if needed and confirms it is reachable
```

> **Keep the repository password somewhere other than this server.** Restic encrypts client-side,
> so without that passphrase the backups cannot be decrypted by anyone — including you. That is the
> point of it, and it is also how people lose their backups. Put it in a password manager now.

The password is passed to Restic through the environment, never in the command line, because argv
is readable by any local user via `/proc`.

## What a snapshot contains

One snapshot per site, containing the whole site tree **and** a logical database dump written to
`logs/db-latest.sql` immediately beforehand. Files and database are captured together, so a restore
is internally consistent rather than a filesystem copy plus a database from a different moment.

```bash
slipctl backup run <site-id>            # full: files + database
```

`POST /api/sites/{id}/backups` accepts `kind` of `full`, `files` or `database`.

## Scheduling

Set a per-site backup policy (`config.backups`) with a frequency and retention. The scheduler runs
every five minutes and picks up sites that are due; a newly created site is backed up without you
having to trigger it.

Concurrency is handled properly: several sites sharing one repository can come due in the same tick,
and `restic init` running twice against the same fresh repository does not fail cleanly — it
**corrupts** the repository, permanently, for every site. Repository initialisation is therefore
serialised behind a mutex. Worth knowing if you write your own tooling against Restic.

## Verified restores

This is the part that distinguishes a backup from a hope.

```bash
slipctl backup verify <backup-id>
```

A verify **actually restores** the snapshot into a scratch directory, checks the repository, sanity-
checks that the tree and the database dump are present and plausible, measures how long it took,
and then removes the scratch copy. The measured duration becomes your recovery-time estimate,
displayed in the panel — a real number from your own data and your own hardware, not a guess.

Schedule verifies as well as backups. A repository that silently stopped working three weeks ago is
the classic backup failure, and only a restore catches it.

## Restoring

```bash
slipctl backup restore <backup-id> <domain> [full|files|database]
```

In place, a restore:

1. restores the snapshot to a scratch directory first and refuses to continue if the snapshot has no
   valid `current` release;
2. dumps the **current** database as a rollback point before touching anything;
3. swaps the files and imports the database;
4. flushes that site's object cache — otherwise the restored site serves the pre-restore options and
   rewrite rules and real pages 404;
5. reloads the web server and verifies the release symlink resolves.

If any step fails, files and database are rolled back **together**. Restoring into a target
directory instead of in place is also supported, which is how you move a site between servers.

Modes: `full` (files and database), `files`, or `database` only.

## Testing it for real

Do this once, on a site you can afford to break, before you need it:

```bash
slipctl backup run <site-id>
# delete a post in wp-admin and delete a file from the site
slipctl backup restore <backup-id> <domain> full
# confirm both came back
```

That five-minute exercise is the difference between having backups and believing you do.

## What is not covered

- **Sites using an external database** — Slipstream does not manage it, so it is not in the
  snapshot. Back it up wherever it lives.
- **The panel's own state** — `/var/lib/slipstream/state.db` holds sites, users and settings. It is
  small; snapshot it separately if losing your panel configuration would hurt.
- **Anything outside `/srv/sites/<domain>/`** — files you left in `/root` or `/opt` are not part of
  any site.
