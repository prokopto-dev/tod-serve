# Backups, and restoring from one

**The append-only report log is the product.** Losing it is the one unrecoverable failure this
service has: a ToD nobody recorded is a bad raid night, and a ToD history nobody has is a circle
starting over. Everything else — the catalogue, the projection, the cache — is derivable.

Migrations are forward-only ([ADR-0006](../adr/0006-atlas-authors-goose-applies.md)). There are no
down migrations and an older binary refuses to start against a newer schema, so **a backup taken
before an upgrade is the only undo that exists.**

## Taking one

```bash
tod-serve backup --to /path/to/copy.db
```

`VACUUM INTO`, from inside the shipped binary. Two reasons it is a verb rather than a runbook step:

- **`cp` of a live database is a torn read.** The database runs in WAL mode, so the bytes at
  `tod.db` are not the database — the committed tail is in `tod.db-wal` beside it. A copy of the
  first file alone restores as a database missing whatever was written most recently, or as one that
  does not open at all. `VACUUM INTO` runs inside a read transaction and writes a complete,
  integrity-checkable file **against a server that is still taking reports**.
- **The image has no shell and no `sqlite3`.** It is `FROM scratch`. The binary is the only tool in
  there, so the tool has to be the binary.

The destination must not already exist. The one outcome worse than having no backup is a backup that
silently replaced the last good one.

## On the droplet

Every deploy takes one, into `/opt/tod-serve/backups/pre-<timestamp>.db`, immediately before it
pulls — and **stops the deploy if it fails**, unless there is no volume at all. It keeps the last
ten.

That covers "the deploy broke it" and nothing else. It is not a backup strategy, because it only
runs when you deploy. Add a daily one:

```bash
# /etc/cron.daily/tod-serve-backup   (root, chmod 755)
#!/bin/sh
set -eu
cd /opt/tod-serve
stamp="$(date -u +%Y%m%d)"
docker compose exec -T tod-serve /usr/local/bin/tod-serve backup --to "/backups/daily-${stamp}.db"
find /opt/tod-serve/backups -name 'daily-*.db' -mtime +30 -delete
```

`/backups` is a bind mount `deploy/compose.yaml` declares onto `/opt/tod-serve/backups`, so the file
lands on the host directly — there is no copy-out step, which matters because the image has no shell
and could never delete a leftover it had written into the data volume.

**That directory must be owned by uid `65532`**, because that is who the container runs as and it
matches nothing on the host:

```bash
install -d -o 65532 -g deploy -m 770 /opt/tod-serve/backups
```

A root-owned one fails loudly — `unable to open database: /backups/daily-….db` — rather than
producing a bad backup. **Both directions were checked**, against a real container: root-owned
refuses, `65532`-owned writes.

The absolute path to the binary is not optional either: `docker exec` does not apply the image's
`ENTRYPOINT`, and this image has no shell and no `PATH` lookup to fall back on. `exec` also needs
something to exec into — between deploys there always is, because a deploy ends with `up -d`, and
the deploy workflow refuses rather than skipping its snapshot when there is not.

> The ownership requirement is a Linux one, and the droplet is Linux. **On Docker Desktop for macOS
> or Windows it does not apply** — the file-sharing layer maps container writes to the host user
> whatever uid the process has, so any shared directory works. Watch out for the other trap there
> instead: a bind source OUTSIDE Docker Desktop's shared paths is not an error. The daemon creates
> an empty directory inside its VM and mounts that, so the backup "succeeds" and the file is
> nowhere on your disk.

### At home

`compose.local.yaml` declares no `backups` mount, so pass one:

```bash
docker compose -f deploy/compose.local.yaml run --rm --volume "$PWD/backups:/backups" \
  tod-serve backup --to "/backups/$(date -u +%Y%m%d).db"
```

`run` rather than `exec`, so it works whether or not the stack is up.

> **A backup on the same volume as the database is an undo, not a backup.** It survives a bad
> migration and nothing else: not a failed droplet, not a deleted volume, not the droplet being
> destroyed. **Copy them off the machine.** `rsync`, `rclone`, a Spaces bucket — anything, so long
> as it is somewhere the droplet's disk failing does not reach:
>
> ```bash
> # On your workstation, or from anywhere with the deploy key.
> rsync -avz deploy@tod.example.com:/opt/tod-serve/backups/ ./tod-serve-backups/
> ```

## Checking one

A backup nobody has opened is a backup nobody knows restores. Check it with the same tool an
operator would use:

```bash
tod-serve doctor --db /path/to/copy.db
```

`doctor` runs `PRAGMA integrity_check` and `PRAGMA foreign_key_check`, reports the schema version,
confirms the migrations are current, and names the instance. It exits non-zero on a problem.

To go further and confirm the copy holds the *reports* and not merely the schema, recompute the
whole board from its log and diff it against its own cache:

```bash
tod-serve verify-states --db /path/to/copy.db
```

`deploy/smoke.sh` runs both of these against a fresh backup on every CI build.

## Restoring

Restoring is replacing the file on the volume, with the container stopped. **There is deliberately
no verb for it.** A restore command would be a single word that destroys the append-only report log,
and this is the one operation that should be typed out in full by somebody who meant it.

```bash
# On the droplet, as the deploy user, in /opt/tod-serve.
docker compose stop tod-serve

# The volume is tod-serve_tod-data. Copy the snapshot in over the database, and REMOVE the WAL and
# shared-memory files beside it — a stale -wal against a replaced database is a database that will
# not open, and the error names neither file.
docker run --rm --label tod-serve-restore \
  -v tod-serve_tod-data:/data -v /opt/tod-serve/backups:/backups \
  --entrypoint sh alpine:3 -c \
  'rm -f /data/tod.db-wal /data/tod.db-shm && cp /backups/pre-<timestamp>.db /data/tod.db \
   && chown 65532:65532 /data/tod.db'

docker compose up -d tod-serve
docker compose run --rm tod-serve doctor
```

`alpine` is used for exactly this one job, because it needs a shell and `cp`, and the shipped image
has neither. This is a hand-run recovery step and not part of any pipeline, which is why an unpinned
image is acceptable here and is not in `deploy/`.

**If you are restoring because a release migrated and then went wrong, roll the image back too.**
The snapshot is from before the migration, so a newer binary will simply migrate it again — and land
you exactly where you started. Deploy the previous image *first*, then restore:

```bash
gh workflow run Deploy -f image_tag=sha-<previous-full-sha> -f source_ref=<previous-full-sha>
```

## What a backup does not contain

`.env`. The pepper and the session key live only on the host, and a restored database is
**unreadable without the pepper that keyed it** — every credential is
`HMAC-SHA256(pepper, secret)`, so a database restored under a different pepper has a valid schema, a
complete report log, and not one working token in it.

Back up `.env` separately, somewhere you will still have in a year, and treat it as at least as
important as the database. The report log without the pepper is recoverable — every member mints a
new token. The pepper without the report log is not.
