#!/usr/bin/env bash
# The deploy smoke test: the shipped IMAGE, brought up and driven end to end.
#
# `docs/operations/deployment.md` describes a first deploy in prose. THIS FILE is the executed
# version of that walkthrough, and the runbook points at it by name — so the instructions an
# operator follows are instructions CI runs on every build, rather than a page that was true once.
#
# It is deliberately the whole path and not a health check: migrate, boot, drive the FIRST-RUN
# WIZARD over HTTP behind `TOD_SETUP_TOKEN` — including both of the refusals that make it safe to
# have on a public port — redeem the one-time owner code it hands back, reach the admin surface
# with the session that redemption set, report a ToD, read the board, and take a backup and check
# it. Every one of those is a step a real first deploy takes, and each has a different way of being
# broken in an image that passes `/healthz`.
#
# `init`, `seed targets` and `instance grant` are NOT driven here, and their absence is the point:
# since ADR-0016 a first deploy is `.env`, `migrate`, `up`, and one form. They still exist and are
# still the way back when nobody can sign in — `cmd/tod-serve` has its own tests for them — but a
# walkthrough that ran them would be describing a path the runbook no longer tells anybody to take.
#
# It runs `deploy/compose.local.yaml` rather than plain `docker run`, so the file that says "anyone
# can run this at home" is the file being exercised. `compose.yaml` is NOT run here: it needs
# Traefik, an external network and a public DNS name for ACME, none of which exist in CI. That gap
# is stated in the runbook rather than papered over.
#
# Usage: deploy/smoke.sh [image-ref]
#   With no argument it builds the image from this checkout, which is what CI does.

set -euo pipefail
cd "$(dirname "$0")/.."

say() { printf '\033[36m==>\033[0m %s\n' "$*"; }
ok()  { printf '\033[32m  ok\033[0m      %s\n' "$*"; }
die() { printf '\033[31mFAILED\033[0m  %s\n' "$*" >&2; exit 1; }

IMAGE="${1:-${TOD_DEPLOY_IMAGE:-}}"
PROJECT="tod-serve-smoke"
COMPOSE=(docker compose -p "$PROJECT" -f deploy/compose.local.yaml)

# Every secret is generated per run. A fixed one in a repository is a fixed one in production the
# first time somebody copies this file, which is the failure `CHANGE_ME_` exists to prevent.
TOD_TOKEN_PEPPER="$(openssl rand -base64 48)"
TOD_SESSION_KEY="$(openssl rand -base64 48)"
# The third one, and the only one an operator deletes afterwards: it authorises first-run setup and
# nothing else, and it stops working the moment this instance has an administrator.
TOD_SETUP_TOKEN="$(openssl rand -base64 48)"
export TOD_TOKEN_PEPPER TOD_SESSION_KEY TOD_SETUP_TOKEN

# A port the kernel just told us was free, rather than a number this script hopes nothing else
# wants. A smoke run that fails on a port collision is a smoke run people learn to re-run.
TOD_DEPLOY_PORT="$(python3 -c 'import socket
s = socket.socket()
s.bind(("127.0.0.1", 0))
print(s.getsockname()[1])
s.close()')"
BASE="http://127.0.0.1:${TOD_DEPLOY_PORT}"
TOD_PUBLIC_URL="$BASE"
TOD_DEPLOY_TLS_PORT="$(python3 -c 'import socket
s = socket.socket()
s.bind(("127.0.0.1", 0))
print(s.getsockname()[1])
s.close()')"
export TOD_DEPLOY_PORT TOD_PUBLIC_URL TOD_DEPLOY_TLS_PORT

WORK="$(mktemp -d)"
PASSED=0
cleanup() {
  # The container log, but only when something went wrong. Printing it on a green run trains
  # people to scroll past it, which is where the one line that mattered would have been.
  if [ "$PASSED" -ne 1 ]; then
    printf '\n--- container log ---\n'
    "${COMPOSE[@]}" logs --tail 80 tod-serve 2>/dev/null || true
  fi
  "${COMPOSE[@]}" --profile tls down --volumes --remove-orphans >/dev/null 2>&1 || true
  rm -rf "$WORK"
}
trap cleanup EXIT

if [ -z "$IMAGE" ]; then
  IMAGE="tod-serve:smoke"
  say "building $IMAGE"
  docker build -f deploy/Dockerfile -t "$IMAGE" --build-arg VERSION=0.0.0-smoke .
fi
export TOD_DEPLOY_IMAGE="$IMAGE"
say "smoking $IMAGE on port $TOD_DEPLOY_PORT"

# Start from nothing. A volume left over from a previous run would let a broken `migrate` pass.
"${COMPOSE[@]}" down --volumes --remove-orphans >/dev/null 2>&1 || true

# --- the compose file itself resolves ------------------------------------------------------------
# Both of them, because `compose.yaml` is the one that reaches production and nothing else in CI
# ever parses it. `config -q` resolves every interpolation, so it fails on a malformed file AND on
# a `${VAR:?}` nothing supplies.
"${COMPOSE[@]}" config -q || die "compose.local.yaml does not resolve"
TOD_DEPLOY_HOST=smoke.example.com TOD_DEPLOY_IMAGE="$IMAGE" \
  docker compose -f deploy/compose.yaml config -q || die "compose.yaml does not resolve"
ok "both compose files resolve"

# --- migrate, deliberately, before anything serves -----------------------------------------------
"${COMPOSE[@]}" run --rm tod-serve migrate | tail -n 1 | grep -q 'schema version' \
  || die "migrate did not report a schema version"
ok "migrate reached a schema version"

# --- up, on a database with NOTHING in it but the schema ------------------------------------------
# No `init` and no `seed targets`. `serve` has to boot against a fresh database and serve the
# wizard, because that is what an operator following the runbook now does.
#
# --- up ------------------------------------------------------------------------------------------
"${COMPOSE[@]}" up -d

# attempt <what> <command...> — retry on a bounded budget; the give-up is a hard failure.
#
# `until` rather than `if !`, because `set -e` does not apply to a command in an until/while
# condition: written with a plain `if`, this would abort on the first failed try and defeat the
# retry it exists to implement.
ATTEMPTS=40
INTERVAL=2
attempt() {
  local what="$1"; shift
  local i=1
  until "$@"; do
    if [ "$i" -ge "$ATTEMPTS" ]; then
      die "$what did not succeed in $ATTEMPTS attempts"
    fi
    i=$((i + 1))
    sleep "$INTERVAL"
  done
  ok "$what (attempt $i/$ATTEMPTS)"
}

liveness() { curl -fsS --max-time 5 "$BASE/healthz" -o /dev/null; }
readiness() { curl -fsS --max-time 5 "$BASE/readyz" -o "$WORK/readyz.json"; }

attempt "/healthz answers" liveness
attempt "/readyz reports the database is ready" readiness
grep -q '"status":"ready"' "$WORK/readyz.json" || { cat "$WORK/readyz.json"; die "/readyz is not ready"; }

# The container's OWN health check, which is the binary probing itself over loopback with no shell
# and no curl in the image. Nothing else in this repository runs the shipped HEALTHCHECK.
# A function rather than an inline `bash -c`, so the project name is not spelled a second time
# where a rename would leave it inspecting a container that does not exist and reporting "not
# healthy" for the wrong reason.
container_is_healthy() {
  local id status
  id="$("${COMPOSE[@]}" ps -q tod-serve)" || return 1
  [ -n "$id" ] || return 1
  status="$(docker inspect -f '{{.State.Health.Status}}' "$id")" || return 1
  [ "$status" = healthy ]
}
attempt "the image's own HEALTHCHECK reports healthy" container_is_healthy

# --- metrics are off, and off means there is no handler ------------------------------------------
# Canonical §13. The listener is a second one and this stack publishes no port for it; what is
# checked here is that it is not quietly reachable on the API port either.
[ "$(curl -s -o /dev/null -w '%{http_code}' --max-time 5 "$BASE/metrics")" = "404" ] \
  || die "/metrics answered on the API port with metrics disabled"
ok "/metrics is not on the API listener"

# --- the console, with the headers the binary attaches -------------------------------------------
# Not the proxy's: there is no proxy here. This is the check that the CSP travels with the image.
curl -fsS -D "$WORK/console.headers" --max-time 5 "$BASE/" -o "$WORK/console.html"
grep -qi '^content-security-policy: .*frame-ancestors' "$WORK/console.headers" \
  || { cat "$WORK/console.headers"; die "the console served no Content-Security-Policy"; }
grep -qi '^x-content-type-options: nosniff' "$WORK/console.headers" \
  || die "the console served no X-Content-Type-Options"
grep -q '<div id="root">' "$WORK/console.html" || die "the console did not serve its document"
ok "the console is served, with its own security headers"

# --- first-run setup: the three refusals, then the form -------------------------------------------
# This is the walkthrough the runbook now describes, executed. ADR-0016.
#
# `/meta` first, because it is what the console routes on and it is public: a browser arriving at a
# fresh instance has to be sent to `/setup` rather than to a sign-in form it cannot complete.
curl -fsS --max-time 10 "$BASE/api/v1/meta" -o "$WORK/meta.json" || die "/meta did not answer"
grep -q '"configured":false' "$WORK/meta.json" \
  || { cat "$WORK/meta.json"; die "/meta claims a fresh database is configured"; }
grep -q '"setup_available":true' "$WORK/meta.json" \
  || { cat "$WORK/meta.json"; die "/meta does not offer first-run setup on a fresh database"; }
ok "/meta routes a fresh instance to first-run setup"

# The two refusals that make this safe to have on a public port, driven BEFORE the real thing so
# that a green run cannot be a route that authorises everybody. They answer identically on purpose:
# an instance with no token set and one with a wrong token guessed must not be tellable apart.
UNSET_STATUS="$(curl -sS --max-time 10 -o "$WORK/setup-none.json" -w '%{http_code}' \
  "$BASE/api/v1/setup")"
WRONG_STATUS="$(curl -sS --max-time 10 -o "$WORK/setup-wrong.json" -w '%{http_code}' \
  -H "Authorization: Bearer not-the-setup-token" "$BASE/api/v1/setup")"
[ "$UNSET_STATUS" = "404" ] \
  || { cat "$WORK/setup-none.json"; die "setup answered $UNSET_STATUS with no token, wanted 404"; }
[ "$WRONG_STATUS" = "404" ] \
  || { cat "$WORK/setup-wrong.json"; die "setup answered $WRONG_STATUS to a wrong token, wanted 404"; }
# Compared field by field with `meta` dropped, not byte for byte: `meta.request_id` is a fresh ULID
# on every response and is SUPPOSED to differ. Everything a caller could learn the instance's state
# from — the status, the code, the title, the detail — has to be identical.
python3 -c '
import json, sys
def shape(path):
    d = json.load(open(path))
    d.pop("meta", None)
    return d
a, b = shape(sys.argv[1]), shape(sys.argv[2])
if a != b:
    print("no token:", json.dumps(a, sort_keys=True))
    print("wrong   :", json.dumps(b, sort_keys=True))
    sys.exit(1)
' "$WORK/setup-none.json" "$WORK/setup-wrong.json" \
  || die "a missing setup token and a wrong one are distinguishable"
ok "setup refuses a missing and a wrong token identically"

# The form. One request creates the instance, the `local` provider, the first circle and the
# raid-target catalogue, and hands back a one-time owner code.
#
# `local` is the only provider a smoke run can use: discord and oidc need a real authorization
# server. It is why this circle reports revocation_strength=weak — which is correct, is what the
# console shows, and is why the acknowledgement below is not optional.
curl -fsS --max-time 60 -X POST "$BASE/api/v1/setup" \
  -H 'Content-Type: application/json' \
  -H "Authorization: Bearer $TOD_SETUP_TOKEN" \
  -H "Idempotency-Key: smoke-setup-$$" \
  -d "{\"name\":\"Smoke Instance\",\"public_url\":\"$TOD_PUBLIC_URL\",
       \"provider\":{\"key\":\"local\",\"kind\":\"local\",\"display_name\":\"This server\",
                   \"acknowledge_weak_revocation\":true},
       \"circle\":{\"name\":\"Smoke Circle\",\"server\":\"blue\"}}" \
  -o "$WORK/setup.json" || { cat "$WORK/setup.json" 2>/dev/null; die "first-run setup was refused"; }

read -r CODE JOINPATH STRENGTH SEEDED <<EOF
$(python3 -c '
import json
d = json.load(open("'"$WORK"'/setup.json"))
print(d["owner_code"], d["join_path"], d["revocation_strength"], d["raid_targets_added"])
')
EOF
[ -n "$CODE" ] || { cat "$WORK/setup.json"; die "setup returned no owner code"; }
[ "$JOINPATH" = "/join#$CODE" ] \
  || die "setup's join path is $JOINPATH; the code must travel in the FRAGMENT"
[ "$STRENGTH" = "weak" ] \
  || die "a circle accepting local reported revocation_strength=$STRENGTH, wanted weak"
# Target IDENTITY only. Timers are community-derived and are NOT bundled (SEED001): an instance
# without them reports `no_timer` and still records every ToD correctly, which is what the board
# check below actually observes.
[ "$SEEDED" -gt 0 ] || die "setup seeded no raid targets"
ok "the wizard created the instance, the provider, the circle and $SEEDED raid targets"

# --- redeem the one-time owner code, over HTTP ----------------------------------------------------
# The code the WIZARD returned, used the way a person actually uses it: the browser is redirected
# straight to `/join#<code>`, so nothing is copied out of one page and into another.
# The response HEADERS are kept as well as the body: an instance-realm route is session-only at
# every scope, so the administrator half of this walkthrough cannot be driven with the PAT.
curl -fsS --max-time 10 -X POST "$BASE/api/v1/join" \
  -H 'Content-Type: application/json' \
  -H "Idempotency-Key: smoke-join-$$" \
  -D "$WORK/join.headers" \
  -d "{\"invite_code\":\"$CODE\",\"provider\":\"local\",\"credential\":{\"kind\":\"none\"},
       \"display_name\":\"Smoke Operator\",\"client\":{\"name\":\"smoke\",\"version\":\"1.0.0\"}}" \
  -o "$WORK/join.json" || { cat "$WORK/join.json" 2>/dev/null; die "join was refused"; }

read -r TOKEN CIRCLE ROLE <<EOF
$(python3 -c '
import json
d = json.load(open("'"$WORK"'/join.json"))
print(d["token"]["token"], d["circle"]["id"], d["membership"]["role"])
')
EOF
[ "$ROLE" = "owner" ] || die "the owner code produced a $ROLE, not an owner"
ok "the owner code was redeemed and returned a personal access token"

# The same code twice must not work. Single-use is what the whole bootstrap rests on.
if curl -fsS --max-time 10 -X POST "$BASE/api/v1/join" \
     -H 'Content-Type: application/json' -H "Idempotency-Key: smoke-join-again-$$" \
     -d "{\"invite_code\":\"$CODE\",\"provider\":\"local\",\"credential\":{\"kind\":\"none\"},
          \"display_name\":\"Second\",\"client\":{\"name\":\"smoke\",\"version\":\"1.0.0\"}}" \
     -o /dev/null 2>/dev/null; then
  die "the one-time owner code was redeemed twice"
fi
ok "the owner code is single-use"

# --- the token is a real credential ---------------------------------------------------------------
curl -fsS --max-time 10 "$BASE/api/v1/me" -H "Authorization: Bearer $TOKEN" -o "$WORK/me.json" \
  || die "the token did not authenticate"
grep -q "$CIRCLE" "$WORK/me.json" || { cat "$WORK/me.json"; die "/me does not name the circle"; }
ok "the token authenticates and names the circle"

# --- the instance is administrable, with no console step at all ------------------------------------
# This is the end of docs/operations/deployment.md's first deploy. Redeeming an owner grant while
# NOBODY administers the instance grants that identity `instance.owner` in the join's own
# transaction (ADR-0016), so there is no `instance identities` and no `instance grant` here — the
# runbook used to stop two commands short of a usable instance, and now it stops at the form.
#
# The cookie is read out of the response rather than kept in a jar: it carries the `__Host-`
# prefix, so it is `Secure`, and curl will not send a Secure cookie back over the plain HTTP this
# local profile serves. The SERVER is happy to accept it — the prefix is a rule about how a cookie
# may be SET — so the header is built by hand here rather than the cookie being weakened for a test.
SESSION="$(grep -i '^set-cookie: __Host-tod_session=' "$WORK/join.headers" \
  | sed 's/.*__Host-tod_session=//' | cut -d';' -f1 | tr -d '\r')"
[ -n "$SESSION" ] || { cat "$WORK/join.headers"; die "join set no session cookie"; }

# `|| true` on the cat: with `-f` curl leaves no output file on a 403, and `set -e` applies inside
# the branch following the final `||` — so a failing `cat` here would exit the script with the
# status and none of the sentence, which is a smoke failure that says nothing about itself.
curl -fsS --max-time 10 -H "Cookie: __Host-tod_session=$SESSION" \
  "$BASE/api/v1/admin/identity-providers" -o "$WORK/providers.json" \
  || { cat "$WORK/providers.json" 2>/dev/null || true
       die "the admin surface is refused to the identity that redeemed the bootstrap code"; }
grep -q '"key":"local"' "$WORK/providers.json" \
  || { cat "$WORK/providers.json"; die "the admin listing does not name the local provider"; }
ok "redeeming the owner code made the instance administrable, from the same session"

# A PAT is still refused, and differently, because the fix is different: no token reaches an
# instance-realm permission at any scope, whatever the ledger says. Without this the 200 above
# could be a route that authorises everybody.
STATUS="$(curl -sS --max-time 10 -o /dev/null -w '%{http_code}' \
  -H "Authorization: Bearer $TOKEN" "$BASE/api/v1/admin/identity-providers")"
[ "$STATUS" = "403" ] || die "a PAT reached the admin surface with $STATUS, wanted 403"
ok "no personal access token reaches the instance realm"

# The ledger recorded a DECISION, and `doctor` reads the same expansion the request above did.
"${COMPOSE[@]}" run --rm tod-serve instance grants > "$WORK/grants.txt" \
  || { cat "$WORK/grants.txt"; die "instance grants failed"; }
grep -q 'instance.owner' "$WORK/grants.txt" \
  || { cat "$WORK/grants.txt"; die "the ledger records no instance.owner grant after the bootstrap"; }

# --- and the wizard is shut, on the ADMINISTRATOR rather than on a flag -----------------------------
# The third refusal, and the one that matters most: the token in `.env` is still set and still
# correct, and setup is over anyway. Nothing was written to say so — it is derived from the ledger,
# so it cannot be reset by editing a row.
curl -fsS --max-time 10 "$BASE/api/v1/meta" -o "$WORK/meta-after.json" || die "/meta stopped answering"
grep -q '"setup_available":false' "$WORK/meta-after.json" \
  || { cat "$WORK/meta-after.json"; die "/meta still offers setup after an administrator exists"; }
STATUS="$(curl -sS --max-time 10 -o /dev/null -w '%{http_code}' \
  -H "Authorization: Bearer $TOD_SETUP_TOKEN" "$BASE/api/v1/setup")"
[ "$STATUS" = "409" ] \
  || die "setup answered $STATUS to a correct token after an administrator existed, wanted 409"
ok "first-run setup is closed by the administrator, with the token still set"

# --- a ToD report, by NAME, which is what the plugin sends ----------------------------------------
# `target_name` runs the resolve ladder server-side, so a client never has to hold a catalogue.
DIED_AT="$(python3 -c '
import datetime
print((datetime.datetime.now(datetime.UTC) - datetime.timedelta(hours=2))
      .strftime("%Y-%m-%dT%H:%M:%SZ"))')"
curl -fsS --max-time 10 -X POST "$BASE/api/v1/circles/$CIRCLE/tod-reports" \
  -H "Authorization: Bearer $TOKEN" -H 'Content-Type: application/json' \
  -H "Idempotency-Key: smoke-report-$$" \
  -d "{\"target_name\":\"Lord Nagafen\",\"server\":\"blue\",\"died_at\":\"$DIED_AT\",
       \"source\":\"manual\",\"self_confidence\":\"certain\"}" \
  -o "$WORK/report.json" || { cat "$WORK/report.json" 2>/dev/null; die "the ToD report was refused"; }
ok "a ToD report was accepted, resolved by name"

# --- the board ------------------------------------------------------------------------------------
# The report has to come back out as derived state, which is the projection, the consensus
# derivation and the catalogue all agreeing. `no_timer` is the CORRECT status here: no timer data
# is bundled, and an unseeded instance says so rather than guessing a window.
curl -fsS --max-time 10 "$BASE/api/v1/circles/$CIRCLE/tods" \
  -H "Authorization: Bearer $TOKEN" -o "$WORK/board.json" || die "the board did not answer"
python3 -c '
import json, sys
board = json.load(open(sys.argv[1]))
rows = board["items"]
naga = [r for r in rows if r["target"]["name"] == "Lord Nagafen"]
if not naga:
    sys.exit("the board does not list the target the report named")
row = naga[0]
if not row.get("died_at"):
    sys.exit("the board lists the target with no time of death")
print("  board: %d row(s); Lord Nagafen died_at=%s status=%s"
      % (len(rows), row["died_at"], row["status"]))
' "$WORK/board.json" || die "the board did not carry the report back"
ok "the board carries the report back as derived state"

# --- the backup, taken the way the deploy takes it -------------------------------------------------
# On the STILL-RUNNING container, with `docker compose exec`, because that is what the deploy does
# and a `cp` of a live WAL-mode database is a torn read.
# The binary is named by its ABSOLUTE PATH: `docker exec` does not apply the image's ENTRYPOINT,
# and this image has no shell and no PATH lookup to fall back on, so `exec … backup` would try to
# execute a program called `backup` and fail with a message about a file that was never there.
"${COMPOSE[@]}" exec -T tod-serve /usr/local/bin/tod-serve backup --to /data/smoke-backup.db >/dev/null \
  || die "backup failed against a running server"
ok "a backup was taken against the running server"

# And it is checked with the same tool an operator would use: `doctor` runs PRAGMA integrity_check
# and PRAGMA foreign_key_check, verifies the migrations are up to date, and reports the instance
# row. A backup nobody has opened is a backup nobody knows restores.
"${COMPOSE[@]}" run --rm tod-serve doctor --db /data/smoke-backup.db > "$WORK/doctor.txt" \
  || { cat "$WORK/doctor.txt"; die "the backup did not pass doctor"; }
grep -q 'integrity check passed' "$WORK/doctor.txt" || { cat "$WORK/doctor.txt"; die "no integrity check in the doctor report"; }
grep -q 'no problems found' "$WORK/doctor.txt" || { cat "$WORK/doctor.txt"; die "doctor found problems in the backup"; }
# And the copy carries the grant, not merely the schema: doctor reports who can administer the
# instance, and a backup that lost the ledger would say nobody can.
grep -q 'identity can administer this instance' "$WORK/doctor.txt" \
  || { cat "$WORK/doctor.txt"; die "the backup has no administrator; the grant did not survive"; }
ok "the backup passes integrity_check, foreign_key_check and doctor"

# The copy holds the REPORT, not merely a schema. verify-states recomputes every target state from
# the log in the backup and diffs it against the cache in the backup, so a copy that lost the row
# fails here rather than looking fine.
"${COMPOSE[@]}" run --rm tod-serve verify-states --db /data/smoke-backup.db > "$WORK/verify.txt" \
  || { cat "$WORK/verify.txt"; die "verify-states failed against the backup" ; }
ok "the backup's report log rederives the same board"

# --- the TLS profile, which is what makes the console signable-in at home ------------------------
# `__Host-tod_session` is unconditionally Secure, and two of three browser engines refuse to store
# it over plain http (deploy/Caddyfile carries the measurement). So compose.local.yaml has a `tls`
# profile, and a profile nothing starts is a profile that rots — this is what starts it.
#
# `-k` because the certificate is from Caddy's own CA, which is exactly the situation a home
# installation is in until somebody installs the root. What is being checked is that the front
# comes up, proxies, and does not eat the binary's own headers.
say "the tls profile"
"${COMPOSE[@]}" --profile tls up -d
TLS="https://localhost:${TOD_DEPLOY_TLS_PORT}"
tls_liveness() { curl -k -fsS --max-time 5 "$TLS/healthz" -o /dev/null; }
attempt "the TLS front proxies /healthz" tls_liveness

curl -k -fsS -D "$WORK/tls.headers" --max-time 10 "$TLS/" -o /dev/null
grep -qi '^content-security-policy: .*frame-ancestors' "$WORK/tls.headers" \
  || { cat "$WORK/tls.headers"; die "the console's CSP did not survive the TLS front"; }
# HSTS is Traefik's on the droplet and is deliberately NOT set here: pinning `localhost` to https
# for a year, from a certificate the browser does not trust, would break every other project that
# ever serves on it.
if grep -qi '^strict-transport-security:' "$WORK/tls.headers"; then
  die "the local TLS front set HSTS; that pins localhost to https for every project on this machine"
fi
ok "the console is served over TLS with its own headers, and no HSTS"

# --- post-deploy: the shipped image can run the maintenance verbs it ships ------------------------
# NOT a step of a first deploy, and deliberately after the walkthrough rather than inside it —
# nobody sweeps a database they installed ninety seconds ago. It is here because this script is the
# only place anything runs in the SHIPPED environment: FROM scratch, no shell, read-only root, uid
# 65532, /tmp a 64m tmpfs, the database on a named volume. `internal/sweep`'s tests drive this same
# cobra command against real SQLite on a developer machine, which proves the logic and proves
# nothing about whether the binary can take a write lock as that uid under that root filesystem.
#
# `exec` on the RUNNING container rather than `run --rm`, for the reason the backup above uses it:
# the sweep is scheduled from cron against a LIVE instance, so a server holding the same database
# open is the contention it has to survive, not an aside. Absolute path for the same reason too —
# `docker exec` does not apply the ENTRYPOINT and there is no shell to find it.
#
# **It deletes nothing here, and that is still the assertion worth making.** Every row this run
# created is minutes old and the grace period is 24 hours. The write pool opens `_txlock=immediate`
# (see `store.dsn`), so the sweep's transaction takes the write lock at BEGIN whether or not a row
# matches: a reported zero means the binary opened the volume read-write, held the lock against a
# live server and committed. What it does NOT prove is a large delete, which nothing but a
# long-lived instance can produce — that is left to the unit tests, which seed the rows.
"${COMPOSE[@]}" exec -T tod-serve /usr/local/bin/tod-serve sweep > "$WORK/sweep.txt" \
  || { cat "$WORK/sweep.txt"; die "the shipped image could not run the sweep verb"; }
# The counts, not merely the exit code: a sweep that ran and said nothing is the failure
# internal/sweep exists to prevent, and it would be invisible to a status check.
grep -q 'swept 0 expired rows' "$WORK/sweep.txt" \
  || { cat "$WORK/sweep.txt"; die "the sweep did not report its counts"; }
ok "the shipped image runs tod-serve sweep against the live database"

PASSED=1
printf '\n\033[32msmoke passed\033[0m  %s\n' "$IMAGE"
