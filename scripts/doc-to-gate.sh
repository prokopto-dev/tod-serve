#!/usr/bin/env bash
# DOC003 — every mechanism docs/concepts/invariants.md NAMES actually exists.
#
# The reverse of DOC002, and the direction nothing checked until three phantoms were found by hand:
# SQL002, `scripts/licence-gate.sh` and TestAuthFlow_RateLimitedCaller_CreatesNoRows were each named
# as enforcement, each believed, and each absent.
#
# **It reads what the gates EMIT, never what their source contains.** The first implementation
# grepped the scripts, and seven review rounds each found one more lexical case it read wrong — a
# comment, a quoted string, an unquoted argument, an escaped quote, a heredoc body, a mixed
# `<<E"OF"` delimiter — with `$(...)`, backticks and line continuations still unhandled when it was
# abandoned. That is a shell lexer written in shell to answer "is this a definition", and it does
# not converge. Two of those seven were fail-CLOSED, hiding a real gate rather than admitting a
# phantom, which is the failure a reviewer is least likely to look for.
#
# A gate that RAN and printed its id definitionally exists. No quoting, escaping or nesting can fake
# that, because faking it would mean actually emitting a pass line at runtime. It is the same move
# internal/canondoc makes for the normative documents: compare against the thing, not against a copy
# of the thing.
#
# Exit 1 = a finding. Exit 2 = the gate was invoked wrongly.

set -uo pipefail
cd "$(dirname "$0")/.."

fail=0
report() { printf '\033[31m%-10s\033[0m %s\n' "$1" "$2"; fail=1; }
pass()   { printf '\033[32m%-10s\033[0m %s\n' "$1" "$2"; }

CAPTURE="${TOD_GATE_CAPTURE:-}"
INVARIANTS_PAGE="${TOD_INVARIANTS_PAGE:-docs/concepts/invariants.md}"

# The floor. This repository has had 35+ gates for months, so a capture holding a handful means the
# run that produced it was truncated, not that the gates were deleted. Five gates were caught this
# week passing green over an empty or truncated input — SQL001, NET001, ENV001, LIC001 and this
# gate's own predecessor — and every one of them looked exactly like a pass. A gate whose whole job
# is detecting phantoms must not be the sixth.
FLOOR="${TOD_GATE_FLOOR:-20}"

if [ -z "$CAPTURE" ]; then
  echo "usage: TOD_GATE_CAPTURE=<file> doc-to-gate.sh   (produced by \`make check\`)" >&2
  exit 2
fi

if [ ! -f "$CAPTURE" ]; then
  report DOC003 "the gate capture $CAPTURE does not exist; \`make check\` did not produce one, and a doc-to-gate check with nothing to check against is not a pass"
  exit 1
fi

# Ids the gates EMITTED. A vacant gate — yellow, "no code to check yet" — printed its id and
# therefore exists; it is the gate reporting that the code it guards is absent, not the gate being
# absent. Treating vacant as missing would report a real gate as a phantom.
# The escape byte is built with printf rather than written `\x1b` in the expression. `\x` is a GNU
# extension, not POSIX: some BSD seds pass it through as a literal `x`, which leaves the ESC bytes
# in place, and then the anchored extraction below matches nothing because every coloured line
# starts with ESC rather than with an id. The failure is total and silent — an empty id set, which
# the floor turns into "the capture was truncated" and points at the wrong thing entirely.
ESC=$(printf '\033')
emitted=$(sed "s/${ESC}\[[0-9;]*m//g" "$CAPTURE" | grep -oE '^[A-Z]+[0-9]{3}' | sort -u)

# Ids the Go side emitted. `go test -list` is the toolchain's own answer to "what tests exist",
# parsed by the compiler rather than by a pattern, and it is the ONLY source for the five rules that
# live in internal/repogate and no shell run ever prints: CLOCK001, RAND001, ROUTE001, SLEEP001 and
# SQL002. A shell-only capture would report all five as phantoms — the fail-closed direction, on the
# gate least able to survive a false positive.
go_emitted=$(grep -oE '^Test[A-Z]+[0-9]{3}_' "$CAPTURE" | sed -E 's/^Test([A-Z]+[0-9]{3})_/\1/' | sort -u)

# DOC003 records itself. This script is running, so the gate exists — which is the same rule it
# applies to every other gate, not an exemption from it. It cannot appear in the capture any other
# way: its own pass line is printed AFTER the capture has been read, so a gate that verifies the
# page would otherwise report itself as a phantom on every run. Recording it here is the honest
# spelling of "it ran"; the alternative, filtering DOC003 out of the page before comparing, would
# make this the one row on the page that nothing checks.
# Gates `make check` never causes to emit, each with the reason and where it does run instead.
#
# This is a waiver list, so it is checked in BOTH directions: a gate named here that DOES start
# emitting is as much a finding as one that stops, which is what stops the list quietly becoming the
# place phantoms hide. It is the discipline staleTestReferences already uses in test/repo.
#
#   ADR000   is a vacancy check. It emits only when there are NO ADRs at all, so a healthy tree
#            never prints it and no arrangement of `make check` would.
#   SPEC001  runs in `make spec-diff`, which is the `lint / openapi` CI job and not part of `make
#            check`: it needs a base branch to diff the specification against.
NOT_EMITTED_BY_CHECK='ADR000 SPEC001'

defined=$(printf '%s\n%s\nDOC003\n%s\n' "$emitted" "$go_emitted" \
            "$(printf '%s\n' $NOT_EMITTED_BY_CHECK)" | grep -v '^$' | sort -u)

# The other direction of the waiver list.
for w in $NOT_EMITTED_BY_CHECK; do
  if printf '%s\n%s\n' "$emitted" "$go_emitted" | grep -qx "$w"; then
    report DOC003 "$w is listed as never emitted by \`make check\`, but this run emitted it; remove it from NOT_EMITTED_BY_CHECK so the list stays honest"
  fi
done
n_defined=$(printf '%s\n' "$defined" | grep -c '[^[:space:]]')

if [ "$n_defined" -lt "$FLOOR" ]; then
  report DOC003 "the capture holds only $n_defined gate(s), below the floor of $FLOOR; the run that produced it was truncated, and a pass over part of the gates is not a pass"
  exit 1
fi

# Algorithm names share the shape of a gate id and are not gates. The list is deliberately short;
# adding to it is a decision somebody has to make on purpose.
NOT_GATES='SHA256 SHA384 SHA512 RS256 RS384 RS512 ES256 ES384 ES512 PS256 PS384 PS512 HS256 HS384 HS512'

if [ ! -f "$INVARIANTS_PAGE" ]; then
  report DOC003 "$INVARIANTS_PAGE is missing; it is where every gate is registered"
  exit 1
fi

# Rows starting NOT HELD are dropped for the test and path classes. The page's own header fixes the
# convention — "Some rows say the mechanism is missing, and they start `NOT HELD`" — and such a row
# names a test or a package precisely to describe what does NOT exist yet. Gate IDS are still read
# from the whole page, because they carry their own in-band marker: an id is written bare when it is
# not a claim. A path and a test name are backticked either way, that being ordinary code
# formatting, so for those two the row label is the only marker there is.
claims=$(grep -v '^| \*\*NOT HELD' "$INVARIANTS_PAGE")

named=$(grep -ohE '`[A-Z]{2,10}[0-9]{3}`' "$INVARIANTS_PAGE" | tr -d '`' | sort -u)
if [ -z "$named" ]; then
  report DOC003 "no gate ids were parsed out of $INVARIANTS_PAGE; the pattern is wrong"
  exit 1
fi

not_gates_re=$(printf '%s\n' $NOT_GATES | paste -sd'|' -)
named_gates=$(printf '%s\n' "$named" | grep -vxE "$not_gates_re")
n=$(printf '%s\n' "$named_gates" | grep -c '[^[:space:]]')

missing=$(printf '%s\n' "$named_gates" | grep -v '^$' | grep -Fxv -f <(printf '%s\n' "$defined"))
if [ -n "$missing" ]; then
  report DOC003 "$INVARIANTS_PAGE names a gate that no gate emitted when it ran:"
  printf '%s\n' "$missing" | sed 's/^/  /'
fi

# Tests, from the same capture and for the same reason: `go test -list` is the compiler's answer,
# so a comment naming a test is not the test and no quoting can invent one.
defined_tests=$(grep -oE '^Test[A-Za-z0-9_]+' "$CAPTURE" | sort -u)
if [ -z "$defined_tests" ]; then
  report DOC003 "the capture lists no tests at all; \`go test -list\` did not run, and every test this page cites would be reported absent"
else
  named_tests=$(printf '%s\n' "$claims" | grep -ohE '`Test[A-Za-z0-9_]+`' | tr -d '`' | sort -u)
  tn=$(printf '%s\n' "$named_tests" | grep -c '[^[:space:]]')
  missing_tests=$(printf '%s\n' "$named_tests" | grep -v '^$' | grep -Fxv -f <(printf '%s\n' "$defined_tests"))
  if [ -n "$missing_tests" ]; then
    report DOC003 "$INVARIANTS_PAGE names a test that no _test.go file defines:"
    printf '%s\n' "$missing_tests" | sed 's/^/  /'
  fi
fi

# Files, not directories: the header's line is that naming a test, a SCRIPT or a trigger is a claim,
# while a directory is how a row says where something lives or will live — `internal/events` is
# Phase 6 and named by rules that have landed. A path git is told to IGNORE is exempt, because its
# absence is the invariant: `deploy/.env` is named by the rule that it must never be committed, and
# demanding it exist would invert that rule.
named_paths=$(printf '%s\n' "$claims" \
              | grep -ohE '`(scripts|internal|db|deploy|test|cmd|web)/[A-Za-z0-9_./-]+`' \
              | tr -d '`' | sort -u)
missing_paths=""; pn=0
for rpath in $named_paths; do
  case "$rpath" in *'*'*) continue ;; esac
  case "$(basename "$rpath")" in *.*) ;; *) continue ;; esac
  if git check-ignore -q "$rpath" 2>/dev/null; then continue; fi
  pn=$((pn + 1))
  [ -e "$rpath" ] || missing_paths="$missing_paths  $rpath\n"
done
if [ -n "$missing_paths" ]; then
  report DOC003 "$INVARIANTS_PAGE names a repository path that does not exist:"
  printf '%b' "$missing_paths"
fi

[ $fail -eq 0 ] && pass DOC003 \
  "$n gate(s), ${tn:-0} test(s) and $pn path(s) named in $INVARIANTS_PAGE, each one emitted by a gate that actually ran"

exit $fail
