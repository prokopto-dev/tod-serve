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

exit $fail
