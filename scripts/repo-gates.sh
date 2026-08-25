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

# The two directories the SQL gates walk. Overridable so that test/repo can point a gate at a
# deliberately broken fixture and assert it fires: a gate nobody has ever seen fail is a gate
# nobody knows works, which is the whole reason test/repo exists.
QUERIES_DIR="${TOD_QUERIES_DIR:-db/queries}"
MIGRATIONS_DIR="${TOD_MIGRATIONS_DIR:-db/migrations-sqlite}"
WEB_SRC_DIR="${TOD_WEB_SRC_DIR:-web/src}"

fail=0
report() { printf '\033[31m%s\033[0m  %s\n' "$1" "$2"; fail=1; }
pass()   { printf '\033[32m%-10s\033[0m %s\n' "$1" "$2"; }
vacant() { printf '\033[33m%-10s\033[0m %s\n' "$1" "$2 (no code to check yet)"; }

go_files() { find ./cmd ./internal ./test ./db -name '*.go' -not -name '*_test.go' 2>/dev/null; }
test_files() { find ./cmd ./internal ./test ./db -name '*_test.go' 2>/dev/null; }
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

# --- ACT001 / ACT002 — the GitHub Actions gates ------------------------------------------------
# The logic lives in scripts/act-gates.sh, which takes a DIRECTORY, so that test/repo/act_test.go
# can point it at deliberately broken fixtures and require it to fire. That is not tidiness: the
# first version of ACT001 — in the repository this was ported from — inspected only `run: |` and
# reported green over a workflow that could have said `run: echo "${{ github.ref_name }}"`, a
# caller's tag name substituted into a script before bash sees it, which is the one line the gate
# exists for.
#
# ACT001: an expression inside a shell script. GitHub substitutes the VALUE into the script text,
# so it is EXECUTED rather than read. The fix is always `env:` plus "$VAR", so the gate has no
# exceptions — an exception list is a thing somebody appends to at 2am.
#
# ACT002: `bash -n` over every extracted script. Workflow shell is not compiled, not linted and not
# executed until a tag is pushed or a deploy is approved, so a broken script ships and is found at
# the worst possible moment.
if compgen -G ".github/workflows/*.yml" >/dev/null; then
  if out=$(bash scripts/act-gates.sh expressions .github/workflows 2>&1); then
    pass ACT001 "no workflow interpolates an expression into a shell script"
  else
    report ACT001 "${out%%$'\n'*}"
    printf '%s\n' "${out#*$'\n'}"
  fi

  if out=$(bash scripts/act-gates.sh syntax .github/workflows 2>&1); then
    pass ACT002 "$out workflow shell script(s) parse"
  else
    report ACT002 "${out%%$'\n'*}"
    printf '%s\n' "${out#*$'\n'}"
  fi
else
  vacant ACT001 "workflows keep expressions out of shell scripts"
  vacant ACT002 "workflow shell scripts parse"
fi

# --- ENV001 / IMG001 — the deployment gates ----------------------------------------------------
# Both live in scripts/deploy-gates.sh, which takes a DIRECTORY for the same reason act-gates.sh
# does: test/repo/deploygates_test.go points them at broken fixtures and requires them to fire.
#
# ENV001 compares the TOD_* constants in cmd/tod-serve/root.go against deploy/, in BOTH directions.
# The two sides are independent hand-written lists — a Go const block and a set of deployment files
# — which is what stops it being the kind of both-directions check that reads from one derivation
# and proves nothing.
#
# IMG001 is PIN001's reasoning applied to images: a tag is mutable, so `node:24` in six months is
# not the `node:24` this was tested against.
if [ -d deploy ]; then
  if out=$(bash scripts/deploy-gates.sh env deploy cmd/tod-serve/root.go 2>&1); then
    pass ENV001 "$out TOD_* variables, documented in deploy/env.example and named nowhere else"
  else
    report ENV001 "${out%%$'\n'*}"
    printf '%s\n' "${out#*$'\n'}"
  fi

  if out=$(bash scripts/deploy-gates.sh images deploy 2>&1); then
    set -- $out
    pass IMG001 "${1} image reference(s) pinned to a digest (${2} interpolate \$TOD_DEPLOY_IMAGE, which the deploy pins)"
  else
    report IMG001 "${out%%$'\n'*}"
    printf '%s\n' "${out#*$'\n'}"
  fi
else
  vacant ENV001 "the binary's environment and deploy/ agree"
  vacant IMG001 "every image is pinned to a digest"
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
#
# PURE001 names the three things §0 of the consensus design forbids by name. PURE002 is the
# broader rule underneath it: the derivation must be a function of its arguments, so it may not
# import anything that reads a clock, touches a disk or opens a socket. `time` is on the deny list
# too — `now` is a parameter, and a package that has no way to spell a clock cannot grow one.
DENIED_IN_CONSENSUS='database/sql|net|net/http|net/url|os|os/exec|io|io/ioutil|bufio|time|math/rand|crypto/rand|github.com/prokopto-dev/tod-serve/internal/store|github.com/prokopto-dev/tod-serve/internal/clock'
if [ -n "$(find ./internal/consensus -name '*.go' 2>/dev/null)" ]; then
  c=$(find ./internal/consensus -name '*.go' -not -name '*_test.go')
  b=$(echo "$c" | xargs grep -ln 'float32\|float64' 2>/dev/null || true)
  [ -n "$b" ] && { report NOFLOAT001 "float in internal/consensus:"; echo "$b"; } || pass NOFLOAT001 "no floats in internal/consensus ($(count "$c") files)"
  b=$(echo "$c" | xargs grep -ln 'tod-serve/internal/store\|time\.Now(\|math/rand' 2>/dev/null || true)
  [ -n "$b" ] && { report PURE001 "internal/consensus is not pure:"; echo "$b"; } || pass PURE001 "internal/consensus imports no store, clock or rand ($(count "$c") files)"
  # Only whole-line quoted paths are read, so a struct tag — which ends in a backtick — is not
  # mistaken for an import.
  imports=$(echo "$c" | xargs grep -hE '^[[:space:]]*(import[[:space:]]+)?([A-Za-z0-9_.]+[[:space:]]+)?"[^"]+"[[:space:]]*$' 2>/dev/null \
            | grep -oE '"[^"]+"' | tr -d '"' | sort -u)
  b=$(echo "$imports" | grep -xE "$DENIED_IN_CONSENSUS" || true)
  if [ -n "$b" ]; then report PURE002 "internal/consensus imports something that can do I/O or read a clock:"; echo "$b"; \
  else pass PURE002 "internal/consensus imports nothing that reads a clock or does I/O ($(count "$imports") distinct imports)"; fi
else
  vacant NOFLOAT001 "no floats in internal/consensus"
  vacant PURE001 "internal/consensus is pure"
  vacant PURE002 "internal/consensus imports nothing that reads a clock or does I/O"
fi

# --- RAND001 — every injected entropy source is crypto/rand.Reader ----------------------------
# Every constructor that mints a secret takes its randomness and refuses a nil one, which makes a
# weak source a construction error rather than a review habit — but only makes the choice
# DELIBERATE at the wiring site. Nothing in the type system forces that site to say `rand.Reader`.
#
# This grep is the fast pre-check and it is not the authority: it sees a line that assigns an
# `Entropy:` field to something other than `rand.Reader`, and it is blind to an aliased import and
# to a value spread over two lines. The AST analyser in internal/repogate is the authority, run by
# TestRAND001_ProductionWiring_UsesCryptoRandReader.
if has_go; then
  scanned=$(go_files)
  bad=$(echo "$scanned" | xargs grep -nE '(Entropy|Random):[[:space:]]*' 2>/dev/null \
        | grep -vE '(Entropy|Random):[[:space:]]*[A-Za-z0-9_]*rand\.Reader' \
        | grep -vE '(Entropy|Random)[[:space:]]+io\.Reader' || true)
  if [ -n "$bad" ]; then report RAND001 "an entropy source that is not crypto/rand.Reader:"; echo "$bad"; \
  else pass RAND001 "every injected entropy source is crypto/rand.Reader ($(count "$scanned") files; the AST analyser in internal/repogate is the authority)"; fi
else
  vacant RAND001 "every injected entropy source is crypto/rand.Reader"
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

# --- NET001 — outbound HTTP only from the identity providers, through the guarded client -------
# Two halves, because the rule has two halves.
#
# NET001a: an HTTP CLIENT, TRANSPORT or DIALER may be constructed only in
# internal/identity/outbound. That is narrower than the two-package rule it replaces: previously
# internal/identity/discord and internal/identity/oidc could each build a bare http.Client with
# the default transport, and the SSRF guard would simply not be in the path. Now there is one
# client, it is guarded, and a provider cannot spell an unguarded one.
#
# NET001b: an outbound REQUEST may still only be issued from internal/identity — the confinement
# canonical conventions §14 describes, unchanged.
#
# THE ONE EXCEPTION IS internal/probe, and it is an exception to both halves. The container image is
# FROM scratch: no shell, no curl, so the binary probes its own listener for the `HEALTHCHECK`. That
# request cannot go through the guarded client, because the guard's deny list covers LOOPBACK — a
# probe of our own listener is precisely the request an SSRF guard exists to refuse. So it is
# allowed by name, in one package, and PROBE001 below is the other half: it holds that package to
# never taking a host from anywhere.
#
# The convenience helpers are matched WITH their opening parenthesis on purpose: `http.Head` is a
# prefix of `http.Header`, and a gate that reports every file constructing a header is a gate
# somebody switches off.
#
# Test files are outside both halves by construction (go_files excludes them): a provider's tests
# inject a stub transport, and the guard itself is tested directly by the deny-list unit test that
# docs/concepts/invariants.md names.
if has_go; then
  scanned=$(go_files | grep -v '^./internal/identity/outbound/' | grep -v '^./internal/probe/')
  bad=$(echo "$scanned" | xargs grep -ln 'http\.Get(\|http\.Post(\|http\.Head(\|http\.Client{\|http\.Transport{\|http\.DefaultClient\|http\.DefaultTransport\|net\.Dial' 2>/dev/null || true)
  if [ -n "$bad" ]; then report NET001 "an HTTP client, transport or dialer outside internal/identity/outbound:"; echo "$bad"; \
  else pass NET001 "the only HTTP client, transport and dialer are internal/identity/outbound's, plus internal/probe's loopback health check ($(count "$scanned") files)"; fi

  scanned=$(go_files | grep -v '^./internal/identity/' | grep -v '^./internal/probe/')
  bad=$(echo "$scanned" | xargs grep -ln 'http\.NewRequest' 2>/dev/null || true)
  if [ -n "$bad" ]; then report NET001 "an outbound request outside internal/identity:"; echo "$bad"; \
  else pass NET001 "outbound requests are issued only from internal/identity and internal/probe ($(count "$scanned") files)"; fi
else
  vacant NET001 "outbound HTTP only from internal/identity, through the guarded client"
fi

# --- PROBE001 — the health-check probe names no host but loopback -----------------------------
# The other half of NET001's one exception. internal/probe may construct a client; what it may not
# do is take its DESTINATION from anywhere — not an environment variable, not a flag, not a row.
# Only the PORT comes from the listen address; the host is a literal the package writes itself.
#
# This is the grep. The authority is TestLivenessURL_IsAlwaysLoopback in internal/probe, which
# drives the URL builder across every spelling of a listen address — including names that resolve
# somewhere else — and requires the result to address loopback every time.
if [ -d internal/probe ]; then
  scanned=$(find ./internal/probe -name '*.go' -not -name '*_test.go')
  # A quoted DOMAIN — anything with a dotted alphabetic suffix — and any read of the environment.
  # Between them they cover every way a destination could arrive from outside this package.
  bad=$( { echo "$scanned" | xargs grep -nE '"(https?://)?[a-zA-Z0-9.-]+\.[a-zA-Z]{2,}' 2>/dev/null
           echo "$scanned" | xargs grep -n 'os\.Getenv\|os\.Args' 2>/dev/null
         } | grep . || true )
  if [ -n "$bad" ]; then
    report PROBE001 "internal/probe names a host, or reads configuration; it may do neither:"
    printf '%s\n' "$bad"
  elif ! grep -q '127\.0\.0\.1' internal/probe/probe.go; then
    report PROBE001 "internal/probe no longer names 127.0.0.1; that literal is the whole guarantee"
  else
    pass PROBE001 "the health-check probe addresses loopback and nothing else ($(count "$scanned") file(s); TestLivenessURL_IsAlwaysLoopback is the authority)"
  fi
else
  vacant PROBE001 "the health-check probe addresses loopback and nothing else"
fi

# --- TEN001 - every circle-scoped query names circle_id in its WHERE --------------------------
# The allowlist is the instance-scoped table list from docs/design/00-canonical-conventions.md 9.
# Adding to it is a reviewed decision, which is why it is spelled out here rather than inferred.
# TestInstanceScopedAllowlist_MatchesRepoGates parses this line and that document and compares them
# in both directions; two hand-maintained copies of one fact is exactly the drift gated against
# everywhere else, and the copy that silently grows is the one that stops a table being checked.
INSTANCE_SCOPED='tod_meta|instance|identity_provider|identity|identity_link|instance_grant|auth_flow|credential_ticket|raid_target|raid_target_alias|raid_target_timer|api_token|idempotency_record|event_outbox'
#
# This gate reads PER QUERY, not per file: a file whose first query names circle_id and whose
# eighth does not is exactly the leak the rule exists to stop, and a `grep -q` over the file would
# call it a pass.
#
# A query that legitimately spans circles - an invite code is instance-unique, and the OAuth
# callback resolves a VERIFIED identity's own memberships - carries a `-- tenancy: <why>` line. It
# is a WAIVER, not a default: it is counted and reported below, so a directory that quietly filled
# up with them looks different from one that did not.
#
# What this gate CANNOT see: whether the circle_id it found is compared against the CALLER's
# circle. It is a text gate over SQL, and that question needs a request. The load-bearing gate is
# TestTenancy_CrossCircle_EveryOperationDenies over the route registry (AGENTS.md law 5); this one
# catches the query that never mentions the tenant at all, which is the mistake that gets written.
if compgen -G "$QUERIES_DIR/*.sql" >/dev/null; then
  total=0; waived=0; found=0
  for f in "$QUERIES_DIR"/*.sql; do
    base=$(basename "$f" .sql)
    # `circle` is the tenant root: its own `id` IS the tenancy key. Its queries still name the
    # parameter circle_id, so they are checked like every other circle-scoped file.
    echo "$base" | grep -qE "^($INSTANCE_SCOPED)$" && continue
    while IFS='|' read -r verdict name; do
      total=$((total + 1))
      case "$verdict" in
        waived)  waived=$((waived + 1)) ;;
        missing) report TEN001 "$f: query $name names no circle_id and carries no \`-- tenancy:\` waiver"; found=1 ;;
      esac
    done < <(awk '
      function check(   filter) {
        if (q == "") return
        # The statement without its comments and without its ORDER BY tail. Sorting by circle_id
        # is not filtering by it, and a comment SAYING circle_id is not naming it either - both
        # would otherwise pass a query that reads every tenant.
        filter = sql
        sub(/ORDER BY.*/, "", filter)
        if (filter ~ /circle_id/)  { print "ok|" q;     return }
        if (body ~ /-- tenancy:/)  { print "waived|" q; return }
        print "missing|" q
      }
      /^-- name: / { check(); q = $3; body = $0 "\n"; sql = ""; next }
                   { body = body $0 "\n"; if ($0 !~ /^[[:space:]]*--/) sql = sql $0 " " }
      END          { check() }
    ' "$f")
  done
  if [ "$total" -eq 0 ]; then
    report TEN001 "no queries were checked; the parse of $QUERIES_DIR is wrong"
  elif [ $found -eq 0 ]; then
    pass TEN001 "every circle-scoped query names circle_id ($total queries, $waived explicitly waived)"
  fi
else
  vacant TEN001 "every circle-scoped query names circle_id"
fi

# --- LOG001 - no UPDATE or DELETE against an append-only table --------------------------------
# Canonical conventions 10: the report log is never UPDATEd or DELETEd, in Go, in SQL, or in a
# migration. The triggers are the enforcement; this catches the statement before it is written,
# where the error message is a review comment rather than a runtime abort.
#
# Go reaches the database only through internal/store/sqlitegen, which sqlc generates from
# db/queries, so covering those two directories covers "in Go" as well.
#
# The table list is read out of the domain model's own Mutability column rather than repeated here.
APPEND_ONLY=$(grep -oE '^\| `[a-z_]+` \| \*{0,2}append-only' docs/design/01-domain-model.md \
              | grep -oE '`[a-z_]+`' | tr -d '`' | sort -u)
if [ -z "$APPEND_ONLY" ]; then
  report LOG001 "no append-only tables parsed out of docs/design/01-domain-model.md"
elif compgen -G "$QUERIES_DIR/*.sql" >/dev/null || compgen -G "$MIGRATIONS_DIR/*.sql" >/dev/null; then
  found=0
  for t in $APPEND_ONLY; do
    for f in "$QUERIES_DIR"/*.sql "$MIGRATIONS_DIR"/*.sql; do
      [ -e "$f" ] || continue
      # Comments are stripped first: these files SAY "no UPDATE and no DELETE" in prose, and the
      # trigger migration says "BEFORE UPDATE ON tod_report", neither of which is a statement.
      bad=$(grep -v '^[[:space:]]*--' "$f" \
            | grep -nEi "(^|[^[:alnum:]_])UPDATE[[:space:]]+$t([^[:alnum:]_]|$)|DELETE[[:space:]]+FROM[[:space:]]+$t([^[:alnum:]_]|$)" || true)
      if [ -n "$bad" ]; then
        report LOG001 "$f writes to the append-only table $t:"; echo "$bad"; found=1
      fi
    done
  done
  [ $found -eq 0 ] && pass LOG001 "no UPDATE or DELETE against an append-only table ($(echo "$APPEND_ONLY" | wc -w | tr -d ' ') tables)"
else
  vacant LOG001 "no UPDATE or DELETE against an append-only table"
fi

# --- MIG001 - migrations are forward-only and correctly numbered -------------------------------
# Every Down block is a RAISE(ABORT) and contains no DDL. ADR-0006 accepts that a bad migration is
# fixed forward under time pressure; a Down block that looked runnable would be reached for at
# exactly the wrong moment, and DROP TABLE is not an undo of a migration that already ran.
if compgen -G "$MIGRATIONS_DIR/*.sql" >/dev/null; then
  found=0; n=0; expected=1
  for f in $(ls "$MIGRATIONS_DIR"/*.sql | sort); do
    n=$((n + 1))
    base=$(basename "$f")
    echo "$base" | grep -qE '^[0-9]{6}_[a-z0-9_]+\.sql$' \
      || { report MIG001 "$base is not NNNNNN_snake_case.sql (canonical conventions 16)"; found=1; }
    got=$((10#${base%%_*}))
    [ "$got" -eq "$expected" ] \
      || { report MIG001 "$base is out of sequence; expected $(printf '%06d' $expected)"; found=1; }
    expected=$((got + 1))

    down=$(sed -n '/^-- +goose Down$/,$p' "$f")
    [ -n "$down" ] \
      || { report MIG001 "$base has no goose Down block"; found=1; }
    echo "$down" | grep -q 'RAISE(ABORT' \
      || { report MIG001 "$base has a Down block with no RAISE(ABORT)"; found=1; }
    ddl=$(echo "$down" | grep -v '^[[:space:]]*--' \
          | grep -nEi '(^|[^[:alnum:]_])(CREATE|ALTER|DROP)[[:space:]]' || true)
    if [ -n "$ddl" ]; then
      report MIG001 "$base has DDL in its Down block; migrations are forward-only:"; echo "$ddl"; found=1
    fi
  done
  [ $found -eq 0 ] && pass MIG001 "$n migrations, numbered in sequence, every Down a RAISE(ABORT)"
else
  vacant MIG001 "migrations are forward-only"
fi

# --- SQLC001 - db/queries is ASCII only --------------------------------------------------------
# Not a style rule. sqlc rewrites `sqlc.arg(x)` by BYTE offset while reporting positions in runes,
# so one em dash in a comment silently corrupts every query after it in the file - the generated
# code compiles and the SQL is mangled. Found once; gated so it is not found twice.
if compgen -G "$QUERIES_DIR/*.sql" >/dev/null; then
  bad=$(LC_ALL=C grep -ln '[^ -~	]' "$QUERIES_DIR"/*.sql || true)
  if [ -n "$bad" ]; then
    report SQLC001 "non-ASCII in a query file corrupts sqlc's byte offsets:"; echo "$bad"
  else
    pass SQLC001 "$QUERIES_DIR is ASCII only ($(ls "$QUERIES_DIR"/*.sql | wc -l | tr -d ' ') files)"
  fi
else
  vacant SQLC001 "$QUERIES_DIR is ASCII only"
fi

# --- WEB001 — no fetch or XMLHttpRequest in web/src outside web/src/api ------------------------
# AGENTS.md law 7. This is the SECOND half of the rule; the first is the ESLint rule
# `tod/no-network-outside-api`, which is the authority because it resolves through the scope and
# is therefore not fooled by a property named `fetch` or by a local binding that shadows the
# global.
#
# Two mechanisms rather than one, because they fail DIFFERENTLY. A lint rule is switched off by an
# `eslint-disable` comment on the offending line, and this grep is not. And this grep runs in the
# CI job that has no npm installed at all, so the rule is enforced even on a machine where the
# JavaScript toolchain was never set up.
#
# What it CANNOT see, stated rather than implied: it is a text gate, so it reads a `fetch` in a
# comment or a string as a violation, and it does not resolve scope. The lint rule is the one that
# is precise; this one is the one that cannot be waived.
#
# The rule is about a request being ISSUED, so `web/src/api` is exempt and nothing else is.
if [ -d "$WEB_SRC_DIR" ]; then
  scanned=$(find "$WEB_SRC_DIR" -type f \( -name '*.ts' -o -name '*.tsx' \) \
            -not -path "$WEB_SRC_DIR/api/*" 2>/dev/null)
  if [ -z "$scanned" ]; then
    report WEB001 "no console sources were checked; the parse of $WEB_SRC_DIR is wrong"
  else
    bad=$(echo "$scanned" | xargs grep -nE '(^|[^.A-Za-z0-9_])(fetch\(|XMLHttpRequest|EventSource\(|WebSocket\(|sendBeacon\()' 2>/dev/null || true)
    if [ -n "$bad" ]; then
      report WEB001 "the console reaches the network outside $WEB_SRC_DIR/api:"; echo "$bad"
    else
      pass WEB001 "no fetch or XMLHttpRequest outside $WEB_SRC_DIR/api ($(count "$scanned") files; the ESLint rule in web/eslint-rules is the authority)"
    fi
  fi
else
  vacant WEB001 "no fetch or XMLHttpRequest outside web/src/api"
fi

# --- WEB002 — the console never reads the browser's clock -------------------------------------
# Canonical conventions §1, applied to the client: every countdown the console renders is a signed
# offset from the RESPONSE's `as_of`, advanced by monotonic elapsed time. A machine whose clock is
# four minutes fast must not render a window that is wrong on screen and right in the database,
# which is the worst available combination — the officer trusts what they can see and nothing
# anywhere reports a discrepancy.
#
# Two allowances, each ONE FILE, each for a different reason:
#
#   web/src/lib/asof.ts    reads `performance.now()`, which is a monotonic counter of DURATION and
#                          not a reading of the wall clock. It is unaffected by a wrong system
#                          time, by an NTP correction mid-raid and by a daylight-saving jump.
#   web/src/lib/format.ts  parses instants the SERVER sent, to render them and to subtract two of
#                          them from one response. Neither is the browser's clock.
#
# `Date.now()` and a zero-argument `new Date()` are banned outright: there is nowhere in this
# console that a reading of the local wall clock is the right answer.
if [ -d "$WEB_SRC_DIR" ]; then
  scanned=$(find "$WEB_SRC_DIR" -type f \( -name '*.ts' -o -name '*.tsx' \) 2>/dev/null)
  if [ -z "$scanned" ]; then
    report WEB002 "no console sources were checked; the parse of $WEB_SRC_DIR is wrong"
  else
    # Comment lines are stripped first: this rule is DESCRIBED in the files it governs, and a gate
    # that fired on its own explanation is a gate somebody deletes the explanation to silence.
    bad=$(echo "$scanned" | xargs grep -nE 'Date\.now\(|new Date\(\)' 2>/dev/null \
          | grep -vE ':[0-9]+:[[:space:]]*(//|\*|/\*)' || true)
    allowed=$(echo "$scanned" | xargs grep -nE 'performance\.now\(|new Date\(|Date\.parse\(' 2>/dev/null \
          | grep -vE ':[0-9]+:[[:space:]]*(//|\*|/\*)' \
          | grep -vE "^$WEB_SRC_DIR/lib/(asof|format)\.ts:" || true)
    if [ -n "$bad" ]; then
      report WEB002 "the console reads the browser's wall clock; every countdown is an offset from the response's as_of:"; echo "$bad"
    elif [ -n "$allowed" ]; then
      report WEB002 "a timestamp is parsed outside $WEB_SRC_DIR/lib/{asof,format}.ts:"; echo "$allowed"
    else
      pass WEB002 "the console never reads the browser's clock ($(count "$scanned") files; time comes from each response's as_of)"
    fi
  fi
else
  vacant WEB002 "the console never reads the browser's clock"
fi

# --- WEB003 — a module that holds a resource renders its staleness ----------------------------
# Every explicit reload in this console follows a WRITE — a retraction, a revocation, a role
# change — so a refresh that fails silently leaves somebody looking at the state from before their
# own action and believing it is current. That is stale data left actionable, and it is the
# opposite of "never hide a row silently".
#
# `Resource` carries `stale` and `staleError`, so no HOOK can drop them. What a hook cannot force
# is a SCREEN rendering them, and this is that half: a file that calls `useResource(` or `usePoll(`
# must also render `<StaleNotice`.
#
# A module that holds a resource and renders no UI of its own carries a `// stale: <why>` line
# naming what does render it. It is a WAIVER, not a default: it is counted and reported below, so a
# directory that quietly filled up with them looks different from one that did not — the same shape
# TEN001's `-- tenancy:` waiver has, and for the same reason.
if [ -d "$WEB_SRC_DIR" ]; then
  holders=$(grep -rl 'useResource(\|usePoll(' "$WEB_SRC_DIR" 2>/dev/null || true)
  if [ -z "$holders" ]; then
    report WEB003 "no resource holders were found; the parse of $WEB_SRC_DIR is wrong"
  else
    found=0; waived=0; renders=0
    for f in $holders; do
      if grep -q '<StaleNotice' "$f"; then renders=$((renders + 1))
      elif grep -q '^[[:space:]]*//[[:space:]]*stale:' "$f"; then waived=$((waived + 1))
      else
        report WEB003 "$f holds a resource, renders no <StaleNotice and carries no \`// stale:\` waiver; a failed refresh there is invisible"
        found=1
      fi
    done
    [ $found -eq 0 ] && pass WEB003 "every module holding a resource accounts for its staleness ($renders render it, $waived explicitly waived)"
  fi
else
  vacant WEB003 "a module that holds a resource renders its staleness"
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
