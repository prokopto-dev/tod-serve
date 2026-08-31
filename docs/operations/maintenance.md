# Scheduled maintenance

Two jobs run on a schedule, and neither schedules itself. This binary has no in-process job runner
— nothing wakes up on a timer inside `serve` — so both are commands you point cron or a systemd
timer at. **That is a convention, not a mechanism: nothing in this repository fails if you never
add these entries.** What the repository does hold is that the commands exist, do what this page
says, and are safe to run; whether they run at all is yours.

| Job | Command | Cadence | Exit code |
|---|---|---|---|
| Sweep expired rows | `tod-serve sweep` | Daily | Zero whenever the sweep ran |
| Verify the state cache | `tod-serve verify-states` | Daily | **Non-zero if it repaired anything** |

The difference in the last column is the whole reason they are two commands. `verify-states` is an
alert: a repair means `target_state_cache` drifted from the report log, and somebody has to find out
why. The sweep deleting rows is the routine healthy case, and a timer that went degraded every night
because a job did its job is a timer somebody switches off.

## Sweeping expired rows

Three tables hold litter rather than history — `auth_flow`, `credential_ticket` and
`idempotency_record`. Every row carries `expires_at`, every reader already refuses a row past it,
and **without this nothing ever deletes them.** They are not a disclosure risk once expired; they
are unbounded growth.

```bash
# /etc/cron.daily/tod-serve-sweep   (root, chmod 755)
#!/bin/sh
set -eu
cd /opt/tod-serve
docker compose exec -T tod-serve /usr/local/bin/tod-serve sweep
```

The absolute path is not optional, for the same reason it is not in
[the backup runbook](backup.md): `docker exec` does not apply the image's `ENTRYPOINT`, and this
image has no shell and no `PATH` to fall back on.

It is safe against a running server and safe to run twice. It takes rows that expired more than **24
hours** ago rather than rows that merely expired, which is deliberate: a `credential_ticket` the
server can still see answers `auth_ticket_expired`, and one it cannot answers
`auth_ticket_invalid`. Sweeping at the instant of expiry would quietly downgrade the error a late
redeemer reads, and a table holding a day of litter is still bounded.

Every run says what it took, on stdout and as one structured log line:

```
swept 45 expired rows: 3 auth flows, 12 credential tickets, 26 idempotency records, 4 session revocations
```

**A run that removed nothing still logs**, so silence means the job is not running rather than
meaning there was nothing to do.

## Verifying the state cache

See [the concepts page on the projection](../concepts/invariants.md) for why the cache is never
authority. Operationally: run it daily, and treat a non-zero exit as something to investigate rather
than something to retry.

```bash
# /etc/cron.daily/tod-serve-verify   (root, chmod 755)
#!/bin/sh
set -eu
cd /opt/tod-serve
docker compose exec -T tod-serve /usr/local/bin/tod-serve verify-states
```

The repair has already happened by the time the status is set — the recomputation always wins — so
the non-zero exit says "something drifted, find out why", not "the board is broken".
