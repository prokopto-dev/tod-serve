#!/usr/bin/env bash
# SPEC001 — the checked-in OpenAPI document introduces no breaking change against the base branch.
#
# The specific change this exists for is a RENAMED `operationId`. It is invisible to every other
# check here: the HTTP surface is unchanged, every test still passes, and the only thing that breaks
# is the method name in every generated SDK. oasdiff calls it `api-operation-id-removed` and
# classifies it as informational by default, so it is turned ON explicitly below — a check that is
# off by default is a check nobody has.
#
# Usage: make spec-diff [BASE_REF=origin/main]
#
# Exit 1 = a breaking change. Exit 2 = the gate was invoked wrongly.

set -uo pipefail
cd "$(dirname "$0")/.."

SPEC="openapi/openapi.json"
BASE_REF="${BASE_REF:-origin/main}"
OASDIFF="github.com/oasdiff/oasdiff@v1.11.7"

report() { printf '\033[31m%-10s\033[0m %s\n' "$1" "$2"; }
pass()   { printf '\033[32m%-10s\033[0m %s\n' "$1" "$2"; }
skip()   { printf '\033[33m%-10s\033[0m %s\n' "$1" "$2"; }

[ -f "$SPEC" ] || { report SPEC001 "$SPEC does not exist; run \`make gen-openapi\`"; exit 1; }

if ! git rev-parse --verify --quiet "$BASE_REF" >/dev/null; then
  skip SPEC001 "$BASE_REF is not available; nothing to compare against"
  exit 0
fi

base=$(mktemp)
trap 'rm -f "$base"' EXIT
if ! git show "$BASE_REF:$SPEC" > "$base" 2>/dev/null; then
  # The commit that first adds the document has no base to compare with. Saying so is better than
  # a green tick over an empty comparison.
  skip SPEC001 "$BASE_REF has no $SPEC yet; this is the commit that adds it"
  exit 0
fi

if go run "$OASDIFF" breaking \
     --include-checks api-operation-id-removed \
     --fail-on ERR "$base" "$SPEC"; then
  pass SPEC001 "no breaking change against $BASE_REF (operationId renames included)"
  exit 0
fi

report SPEC001 "the OpenAPI document breaks clients against $BASE_REF.
             An operationId is NEVER renamed: generated SDK method names come from it, so a
             rename breaks every client even though the HTTP surface is unchanged."
exit 1
