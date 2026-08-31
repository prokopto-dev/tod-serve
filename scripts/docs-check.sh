#!/usr/bin/env bash
# Documentation gates.
#
# The ADR word budget is a proxy for the rule that matters: over budget usually means two decisions
# in one file. Nothing counts them upstream; here it is cheap, so it is checked.

set -uo pipefail
cd "$(dirname "$0")/.."

fail=0
report() { printf '\033[31m%-10s\033[0m %s\n' "$1" "$2"; fail=1; }
pass()   { printf '\033[32m%-10s\033[0m %s\n' "$1" "$2"; }

adrs() { find docs/adr -name '0[0-9][0-9][0-9]-*.md' -not -name '0000-template.md' | sort; }

# --- ADR000 — there is at least one ADR to check ----------------------------------------------
# Without this, every gate below reports success over an empty set, which is how a rule quietly
# stops being enforced.
count=$(adrs | wc -l | tr -d ' ')
[ "$count" -eq 0 ] && { report ADR000 "no ADRs found — the glob is wrong or the tree moved"; exit 1; }

# --- ADR001 — every ADR is within the 1000-word ceiling ---------------------------------------
worst=0; worstf=""
while read -r f; do
  w=$(wc -w < "$f" | tr -d ' ')
  [ "$w" -gt "$worst" ] && { worst=$w; worstf=$f; }
  [ "$w" -gt 1000 ] && report ADR001 "$f is $w words, over the 1000-word ceiling"
done < <(adrs)
grep -q . <<<"$worstf" && [ "$worst" -le 1000 ] && pass ADR001 "$count ADRs within the 1000-word ceiling (worst: $worst, $(basename "$worstf"))"

# --- ADR002 — every ADR states its negative consequences and reversal cost ---------------------
# "An ADR with no negative consequences is rejected in review." This is that review, mechanised.
bad=0
while read -r f; do
  grep -qE '^- \*\*Bad, because' "$f" || { report ADR002 "$f states no negative consequence"; bad=1; }
  grep -qiE '^### Reversal cost' "$f"  || { report ADR002 "$f has no reversal cost section"; bad=1; }
done < <(adrs)
[ $bad -eq 0 ] && pass ADR002 "every ADR states its costs and its reversal cost"

# --- ADR003 — every ADR considered at least two options ---------------------------------------
# "An option you never seriously considered does not belong here" cuts both ways: one option is
# not a decision, it is a description.
bad=0
while read -r f; do
  grep -qE '^## Considered options' "$f" || { report ADR003 "$f has no considered-options section"; bad=1; }
done < <(adrs)
[ $bad -eq 0 ] && pass ADR003 "every ADR records the options it considered"

# --- DOC001 — the error-code list and docs/errors/ agree, in both directions -------------------
# The `type` URL's last segment IS the code, so an undocumented code ships a broken link — and a
# page for a code nobody can emit is a page nobody will ever delete.
#
# The codes are read from the "## Error codes" section onward and NOT anchored to the start of a
# line: the fenced blocks are three columns wide, and the `^` this gate used to carry meant it
# checked the first code on each line and silently ignored the other two.
#
# TestErrorCodes_EveryCode_HasADocumentationPage is the authority — it compares the Go closed enum
# against both this document and that directory. This is the copy that runs without a toolchain.
if [ -d docs/errors ]; then
  bad=0; n=0
  codes=$(sed -n '/^## Error codes/,$p' docs/design/02-api-design.md \
          | grep -oE '\b[a-z_]+ \([0-9]{3}\)' | awk '{print $1}' | sort -u)
  [ -z "$codes" ] && { report DOC001 "no error codes parsed out of docs/design/02-api-design.md"; bad=1; }
  for c in $codes; do
    n=$((n + 1))
    [ -f "docs/errors/$c.md" ] || { report DOC001 "error code $c has no docs/errors/$c.md"; bad=1; }
  done
  for f in docs/errors/*.md; do
    b=$(basename "$f" .md)
    [ "$b" = README ] && continue
    echo "$codes" | grep -qx "$b" \
      || { report DOC001 "docs/errors/$b.md documents a code the API design does not list"; bad=1; }
  done
  [ $bad -eq 0 ] && pass DOC001 "$n error codes, each with a documentation page and no page without a code"
else
  printf '\033[33m%-10s\033[0m %s\n' DOC001 "error-code pages land in Phase 1 (docs/errors/ does not exist yet)"
fi

# --- DOC002 — every gate this repository defines is recorded in invariants.md ------------------
# The reverse drift from DOC001's: a gate that exists in a script but was never written down is a
# rule nobody knows is enforced, so somebody eventually "simplifies" it away in a refactor and no
# reviewer objects.
#
# Both locations are walked — the shell gates and the Go ones in test/repo — because "where the
# gates live" is two places and a check over one of them is a check that goes quiet the first time
# somebody puts a new gate in the other.
if [ -f docs/concepts/invariants.md ]; then
  missing=""
  # NOT anchored to the start of a line. Several gates here are written inline —
  # `{ report DOC001 "..."; bad=1; }` inside an `if` — and an anchored pattern silently skipped
  # every one of them, which is precisely the shape of failure this gate exists to catch. The cost
  # of over-matching is a name in a comment asking to be written down, which is the safe direction.
  gates=$( { grep -ohE '(report|pass|vacant) [A-Z]+[0-9]{3}' scripts/*.sh
             grep -ohE 'func Test[A-Z]+[0-9]{3}_' test/repo/*.go 2>/dev/null
           } | grep -oE '[A-Z]+[0-9]{3}' | sort -u )
  if [ -z "$gates" ]; then
    report DOC002 "no gate names were parsed out of scripts/ or test/repo; the patterns are wrong"
  else
    n=0
    for g in $gates; do
      n=$((n + 1))
      grep -q "$g" docs/concepts/invariants.md || missing="$missing  $g\n"
    done
    if [ -n "$missing" ]; then
      report DOC002 "gate defined in a script or in test/repo but not recorded in invariants.md:"
      printf "%b" "$missing"
    else
      pass DOC002 "$n gates, each recorded in docs/concepts/invariants.md"
    fi
  fi
else
  report DOC002 "docs/concepts/invariants.md is missing; it is where every gate is registered"
fi

# --- DOC003 — every gate invariants.md NAMES actually exists ----------------------------------
# The reverse of DOC002, and the direction nothing checked until three phantoms were found by hand:
# SQL002, `scripts/licence-gate.sh` and `TestAuthFlow_RateLimitedCaller_CreatesNoRows` were all
# named as enforcement, all believed, and all absent. SQL002 has since been written — it is
# `internal/repogate/handle.go` — and the other two ship with this gate. DOC002 polices gate-to-doc;
# without this, doc-to-gate was policed by nobody, so invariants.md could promise a mechanism that
# does not exist and CI stayed green.
#
# DOC001 is NOT this check despite the name suggesting a pair — it compares docs/errors/ against the
# error codes in the API design and never looks at a gate id.
#
# Only BACKTICKED ids count. A gate id in this repository is always written `LIKE001` in prose, so
# a backtick is the marker for "this gate exists", and a bare id is the escape prose needs in order
# to NAME a gate without claiming one — which is how a row records a phantom it has just found
# instead of being unable to mention it. The algorithm names below are skipped because they share
# the shape and are not gates; the list is deliberately short, and adding to it is a decision
# somebody has to make on purpose.
NOT_GATES='SHA256 SHA384 SHA512 RS256 RS384 RS512 ES256 ES384 ES512 PS256 PS384 PS512 HS256 HS384 HS512'

# Overridable so test/repo can point this at a page that names a gate nobody wrote and require the
# finding — the same seam the SQL gates in repo-gates.sh take a directory for.
INVARIANTS_PAGE="${TOD_INVARIANTS_PAGE:-docs/concepts/invariants.md}"

if [ -f "$INVARIANTS_PAGE" ]; then
  named=$(grep -ohE '`[A-Z]{2,10}[0-9]{3}`' "$INVARIANTS_PAGE" \
          | tr -d '`' | sort -u)
  if [ -z "$named" ]; then
    report DOC003 "no gate ids were parsed out of $INVARIANTS_PAGE; the pattern is wrong"
  else
    # Every place a gate id is DEFINED. Three, because the gates live in three shapes: a shell
    # report, a can-fail test in test/repo, and an AST rule id in internal/repogate.
    defined=$( { grep -ohE '(report|pass|vacant) [A-Z]+[0-9]{3}' scripts/*.sh
                 grep -ohE 'func Test[A-Z]+[0-9]{3}_' test/repo/*.go 2>/dev/null
                 grep -ohE '"[A-Z]+[0-9]{3}"' internal/repogate/*.go 2>/dev/null
               } | grep -oE '[A-Z]+[0-9]{3}' | sort -u )
    missing=""; n=0
    for g in $named; do
      case " $NOT_GATES " in *" $g "*) continue ;; esac
      n=$((n + 1))
      echo "$defined" | grep -qx "$g" || missing="$missing  $g\n"
    done
    if [ -n "$missing" ]; then
      report DOC003 "$INVARIANTS_PAGE names a gate that is defined in no script, test/repo test or repogate rule:"
      printf "%b" "$missing"
    else
      pass DOC003 "$n gate(s) named in $INVARIANTS_PAGE, each one that actually exists"
    fi
  fi
else
  report DOC003 "$INVARIANTS_PAGE is missing; it is where every gate is registered"
fi

exit $fail
