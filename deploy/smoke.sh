#!/usr/bin/env bash
# The deploy smoke test: the shipped IMAGE, brought up and driven end to end.
#
# `docs/operations/deployment.md` describes a first deploy in prose. THIS FILE is the executed
# version of that walkthrough, and the runbook points at it by name — so the instructions an
# operator follows are instructions CI runs on every build, rather than a page that was true once.
#
# It is deliberately the whole path and not a health check: migrate, init, seed, boot, redeem the
# one-time owner code over HTTP, use the token that comes back, report a ToD, read the board, and
# take a backup and check it. Every one of those is a step a real first deploy takes, and each has
# a different way of being broken in an image that passes `/healthz`.
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
export TOD_TOKEN_PEPPER TOD_SESSION_KEY

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

# --- init: the instance, the first circle, and the one-time owner code ---------------------------
# `--local` is the only provider a smoke run can use: discord and oidc need a real authorization
# server. It is what makes the join below possible, and it is why this circle reports
# revocation_strength=weak — which is correct and is exactly what the console shows.
"${COMPOSE[@]}" run --rm tod-serve init \
  --name "Smoke Instance" --public-url "$TOD_PUBLIC_URL" \
  --circle "Smoke Circle" --server blue \
  --local --accept-local --acknowledge-weak-revocation > "$WORK/init.txt"
CODE="$(grep -oE 'TODI-[A-Z0-9]{5}-[A-Z0-9]{5}' "$WORK/init.txt" | head -n 1)"
[ -n "$CODE" ] || { cat "$WORK/init.txt"; die "init printed no owner code"; }
ok "init printed a one-time owner code"

# --- the catalogue -------------------------------------------------------------------------------
# Target IDENTITY only. Timers are community-derived and are NOT bundled (SEED001): an instance
# without them reports `no_timer` and still records every ToD correctly, which is what the board
# check below actually observes.
"${COMPOSE[@]}" run --rm tod-serve seed targets | grep -q 'targets:' \
  || die "seed targets reported nothing"
ok "the embedded raid-target catalogue is loaded"

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

# --- redeem the one-time owner code, over HTTP ----------------------------------------------------
# The code `init` printed, used the way a person actually uses it. This is the only way into a
# fresh instance: nobody holds a credential and no route could authorise one.
curl -fsS --max-time 10 -X POST "$BASE/api/v1/join" \
  -H 'Content-Type: application/json' \
  -H "Idempotency-Key: smoke-join-$$" \
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

PASSED=1
printf '\n\033[32msmoke passed\033[0m  %s\n' "$IMAGE"
