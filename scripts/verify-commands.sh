#!/usr/bin/env bash
# Every command the documentation tells somebody to run actually exists.
#
# Two kinds, and they fail for different reasons:
#
#   make <target>       renamed or deleted in the Makefile while the prose kept the old name
#   tod-serve <verb>    a runbook step written from memory, or from a design that changed
#
# The second is the one that matters operationally. A deployment runbook is read by somebody
# mid-incident who has no appetite for `unknown command "restore"`, and a verb that never existed
# reads exactly like one that does until they type it.
#
# Only occurrences INSIDE BACKTICKS or at the start of a line in a fenced block are checked. This
# repository writes every command that way, and matching bare prose would report `make sure` as a
# missing target — a gate with false positives is a gate somebody switches off.
#
# Usage: verify-commands.sh [file...]   (defaults to AGENTS.md and docs/operations/*.md)

set -uo pipefail
cd "$(dirname "$0")/.."

GO="${GO:-go}"
files=("$@")
if [ ${#files[@]} -eq 0 ]; then
  shopt -s nullglob
  files=(AGENTS.md docs/operations/*.md)
  shopt -u nullglob
fi
[ ${#files[@]} -gt 0 ] || { printf 'no documents to check\n' >&2; exit 2; }

fail=0
report() { printf '\033[31m%s\033[0m\n' "$1"; fail=1; }

# extract <pattern-word> — every `<word> …` command in the documents.
#
# Two places count and no others: INSIDE BACKTICKS anywhere, and at the start of a line inside a
# fenced code block. The fence tracking is the part that matters — a paragraph beginning
# "tod-serve is one more service on the network Traefik watches" is prose, and a gate that reported
# it as a missing verb would be switched off within a week, taking the real findings with it.
extract() {
  awk -v word="$1" '
    /^```/ { fenced = !fenced; next }
    {
      line = $0
      # Backticked, anywhere: `tod-serve seed targets`, `make check`.
      while (match(line, "`" word " [a-z][a-z-]*( [a-z][a-z-]*)?")) {
        print substr(line, RSTART + 1, RLENGTH - 1)
        line = substr(line, RSTART + RLENGTH)
      }
      # And ANYWHERE inside a fenced block, because inside a fence every line is a command —
      # including `docker compose run --rm tod-serve init …`, which is how the runbook actually
      # spells most of them and which an anchored pattern misses entirely.
      if (fenced) {
        line = $0
        while (match(line, "(^|[ \t])" word " [a-z][a-z-]*( [a-z][a-z-]*)?")) {
          hit = substr(line, RSTART, RLENGTH)
          sub(/^[ \t]+/, "", hit)
          print hit
          line = substr(line, RSTART + RLENGTH)
        }
      }
    }
  ' "${files[@]}" | sort -u
}

# --- make targets ------------------------------------------------------------------------------
targets=$(extract make | awk '{print $2}' | sort -u)
if [ -z "$targets" ]; then
  report "no \`make\` commands were found in the documentation; the pattern is wrong"
else
  for t in $targets; do
    grep -qE "^## ${t}:" Makefile \
      || report "the documentation names \`make ${t}\`, which is not a documented target"
  done
  [ $fail -eq 0 ] && printf '\033[32m%-10s\033[0m %s\n' "make" \
    "$(echo "$targets" | grep -c .) documented target(s) named in the docs, all of which exist"
fi

# --- tod-serve verbs ---------------------------------------------------------------------------
# Resolved by ASKING THE BINARY, not by grepping the source for a `Use:` field. Cobra exits
# non-zero on an unknown command and zero on `--help` for a real one, so this is the same answer an
# operator gets — including for nested verbs like `seed targets`, where a check over the top level
# alone would pass anything.
verbs=$(extract tod-serve | sed 's/^tod-serve //' | sort -u)
if [ -z "$verbs" ]; then
  report "no \`tod-serve\` commands were found in the documentation; the pattern is wrong"
else
  n=0
  while IFS= read -r verb; do
    [ -n "$verb" ] || continue
    n=$((n + 1))
    # shellcheck disable=SC2086 -- $verb is a verb and an optional subcommand, deliberately split.
    if ! out=$($GO run ./cmd/tod-serve $verb --help 2>&1); then
      report "the documentation names \`tod-serve ${verb}\`, which the binary does not resolve:"
      printf '%s\n' "$out" | head -n 3
    fi
  done <<< "$verbs"
  [ $fail -eq 0 ] && printf '\033[32m%-10s\033[0m %s\n' "tod-serve" \
    "$n verb(s) named in the docs, all of which the binary resolves"
fi

exit $fail
