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

# --- DOC001 — every error code in the API design has a documentation page ---------------------
# The `type` URL's last segment IS the code, so an undocumented code ships a broken link.
if [ -d docs/errors ]; then
  bad=0
  for c in $(grep -oE '^[a-z_]+ \([0-9]{3}\)' docs/design/02-api-design.md | awk '{print $1}' | sort -u); do
    [ -f "docs/errors/$c.md" ] || { report DOC001 "error code $c has no docs/errors/$c.md"; bad=1; }
  done
  [ $bad -eq 0 ] && pass DOC001 "every error code has a documentation page"
else
  printf '\033[33m%-10s\033[0m %s\n' DOC001 "error-code pages land in Phase 1 (docs/errors/ does not exist yet)"
fi

exit $fail
