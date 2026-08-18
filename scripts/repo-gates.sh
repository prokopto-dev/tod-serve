#!/usr/bin/env bash
# Repository gates. Each is a rule from docs/concepts/invariants.md with a mechanism behind it.
#
# A gate that cannot fire yet says so and exits 0 — it does NOT print a green tick it has not
# earned. A gate reporting success over an empty search space is how a rule quietly stops being
# enforced the moment the code it guards is written.
#
# Exit 1 = a finding. Exit 2 = the gate was invoked wrongly.

set -uo pipefail
cd "$(dirname "$0")/.."

fail=0
report() { printf '\033[31m%s\033[0m  %s\n' "$1" "$2"; fail=1; }
pass()   { printf '\033[32m%-10s\033[0m %s\n' "$1" "$2"; }
vacant() { printf '\033[33m%-10s\033[0m %s\n' "$1" "$2 (no code to check yet)"; }

go_files() { find ./cmd ./internal ./test -name '*.go' -not -name '*_test.go' 2>/dev/null; }
test_files() { find ./cmd ./internal ./test -name '*_test.go' 2>/dev/null; }
has_go()   { [ -n "$(go_files)" ]; }

# Every gate below reports how much it looked at. A gate that says `pass` over three files reads
# exactly like one that says `pass` over three hundred, and the difference is whether the rule is
# actually being enforced.
count() { echo "$1" | grep -c . ; }

# --- PIN001 — GitHub Actions pinned to a 40-character SHA -------------------------------------
# Checks the SHAPE of a pin, not that the digest matches its trailing comment. A bare tag is the
# failure this catches; a lying comment is a review problem.
if compgen -G ".github/workflows/*.yml" >/dev/null; then
  bad=$(grep -hoE '^\s*-?\s*uses:\s*[^ ]+' .github/workflows/*.yml \
        | grep -vE '@[0-9a-f]{40}' | grep -vE 'uses:\s*\./' || true)
  if [ -n "$bad" ]; then report PIN001 "action not pinned to a 40-char SHA:"; echo "$bad"; \
  else pass PIN001 "every action is pinned to a 40-character SHA"; fi
else
  vacant PIN001 "workflows pinned to SHAs"
fi

# --- CLOCK001 — time.Now only in internal/clock -----------------------------------------------
# This grep is the fast pre-check, and it runs in the CI job that has no Go toolchain. It is NOT
# the authoritative gate: `import t "time"` defeats it. The authority is the AST analyser in
# internal/repogate, run by TestCLOCK001_Repository_HasNoTimeNowOutsideClock, which also covers
# test files. Canonical conventions §1 describes that one.
if has_go; then
  scanned=$(go_files | grep -v '^./internal/clock/')
  bad=$(echo "$scanned" | xargs grep -ln 'time\.Now(' 2>/dev/null || true)
  if [ -n "$bad" ]; then report CLOCK001 "time.Now outside internal/clock:"; echo "$bad"; \
  else pass CLOCK001 "time.Now appears only in internal/clock ($(count "$scanned") files; the AST analyser in internal/repogate is the authority)"; fi
else
  vacant CLOCK001 "time.Now only in internal/clock"
fi

# --- SLEEP001 — no time.Sleep in tests --------------------------------------------------------
# A test that sleeps is a test that is slow when it passes and flaky when the machine is busy.
# Time-dependent tests use testing/synctest, which fakes the clock for the whole bubble.
#
# Like CLOCK001 above, this grep is the pre-check and an aliased import defeats it. The authority
# is internal/repogate, run by TestSLEEP001_Tests_DoNotSleep.
# internal/repogate is excluded because its fixtures are Go source in STRING LITERALS — the tests
# that prove this rule fires. The analyser parses, so it is not fooled by them; this grep would be.
if [ -n "$(test_files)" ]; then
  scanned=$(test_files | grep -v '^./internal/repogate/')
  bad=$(echo "$scanned" | xargs grep -ln 'time\.Sleep(' 2>/dev/null || true)
  if [ -n "$bad" ]; then report SLEEP001 "time.Sleep in a test; use testing/synctest:"; echo "$bad"; \
  else pass SLEEP001 "no time.Sleep in tests ($(count "$scanned") files; the AST analyser in internal/repogate is the authority)"; fi
else
  vacant SLEEP001 "no time.Sleep in tests"
fi

# --- NOFLOAT001 / PURE001 / PURE002 — internal/consensus is pure ------------------------------
# The float ban is a REPRODUCIBILITY rule, not a money rule: the nightly projection-verify job
# diffs exact values, and a cross-platform float discrepancy would make it cry wolf.
if [ -n "$(find ./internal/consensus -name '*.go' 2>/dev/null)" ]; then
  c=$(find ./internal/consensus -name '*.go' -not -name '*_test.go')
  b=$(echo "$c" | xargs grep -ln 'float32\|float64' 2>/dev/null || true)
  [ -n "$b" ] && { report NOFLOAT001 "float in internal/consensus:"; echo "$b"; } || pass NOFLOAT001 "no floats in internal/consensus"
  b=$(echo "$c" | xargs grep -ln 'tod-serve/internal/store\|time\.Now(\|math/rand' 2>/dev/null || true)
  [ -n "$b" ] && { report PURE001 "internal/consensus is not pure:"; echo "$b"; } || pass PURE001 "internal/consensus imports no store, clock or rand"
else
  vacant NOFLOAT001 "no floats in internal/consensus"
  vacant PURE001 "internal/consensus is pure"
fi

# --- SQL001 — *sql.DB held only by internal/store ---------------------------------------------
if has_go; then
  scanned=$(go_files | grep -v '^./internal/store/')
  bad=$(echo "$scanned" | xargs grep -ln 'database/sql' 2>/dev/null || true)
  if [ -n "$bad" ]; then report SQL001 "database/sql outside internal/store:"; echo "$bad"; \
  else pass SQL001 "database/sql is imported only by internal/store ($(count "$scanned") files)"; fi
else
  vacant SQL001 "*sql.DB held only by internal/store"
fi

# --- NET001 — outbound HTTP only from internal/identity/{discord,oidc} -------------------------
if has_go; then
  scanned=$(go_files | grep -vE '^./internal/identity/(discord|oidc)/')
  bad=$(echo "$scanned" | xargs grep -ln 'http\.Get\|http\.Client{\|net\.Dial' 2>/dev/null || true)
  if [ -n "$bad" ]; then report NET001 "outbound HTTP outside internal/identity/{discord,oidc}:"; echo "$bad"; \
  else pass NET001 "outbound HTTP originates only from the identity providers ($(count "$scanned") files)"; fi
else
  vacant NET001 "outbound HTTP only from internal/identity/{discord,oidc}"
fi

# --- TEN001 — every circle-scoped query names circle_id in its WHERE --------------------------
# The allowlist is the instance-scoped table list from docs/design/00-canonical-conventions.md §9.
# Adding to it is a reviewed decision, which is why it is spelled out here rather than inferred.
INSTANCE_SCOPED='tod_meta|instance|identity_provider|identity|identity_link|auth_flow|credential_ticket|raid_target|raid_target_alias|raid_target_timer|api_token|idempotency_record|event_outbox'
if compgen -G "db/queries/*.sql" >/dev/null; then
  for f in db/queries/*.sql; do
    base=$(basename "$f" .sql)
    echo "$base" | grep -qE "^($INSTANCE_SCOPED)$" && continue
    grep -q 'circle_id' "$f" || report TEN001 "circle-scoped query file does not mention circle_id: $f"
  done
  [ $fail -eq 0 ] && pass TEN001 "every circle-scoped query names circle_id"
else
  vacant TEN001 "every circle-scoped query names circle_id"
fi

# --- SEED001 — no bundled timer data ----------------------------------------------------------
# Target identity ships as our own literals. Timers are community-derived and disputed; they load
# from the separate tod-serve-p99-seed repo. See canonical conventions §15.
if find ./internal ./db -name '*timer*seed*' -o -name '*seed*timer*' 2>/dev/null | grep -q .; then
  report SEED001 "timer data appears to be bundled; it must load from tod-serve-p99-seed"
else
  pass SEED001 "no bundled timer data"
fi

exit $fail
