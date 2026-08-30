#!/usr/bin/env bash
# LIC001 — no copyleft or source-available RUNTIME dependency.
#
# The invariant is in docs/concepts/invariants.md. It named a script exactly like this one for a
# long time before anybody wrote it, which is the failure this repository is built against: a rule
# with an imagined mechanism is believed, and believed harder than one with none.
#
# RUNTIME, precisely: `go list -deps ./...` walks the import graph of the packages that build, and
# NOT their test imports. A test-only GPL dependency would not ship in the binary, and widening this
# to `-test` would fail a build over something no user receives.
#
# FAIL CLOSED. A licence this cannot classify is a finding, not a pass. The whole value of the gate
# is that somebody has to look at a new dependency; a permissive default would make it a rule that
# holds until the first vendor with an unusual LICENSE file.
#
# It needs the Go toolchain, so it runs in CI's `build / test` job rather than `lint / repo`, which
# deliberately has no Go — the same reason `verify-commands` lives there.
#
# Exit 1 = a finding. Exit 2 = the gate was invoked wrongly.

set -uo pipefail
cd "$(dirname "$0")/.."

fail=0
report() { printf '\033[31m%-10s\033[0m %s\n' "$1" "$2"; fail=1; }
pass()   { printf '\033[32m%-10s\033[0m %s\n' "$1" "$2"; }

# The module whose licence is not a dependency question. Overridable so the test fixture can point
# the classifier at a tree of its own.
MAIN_MODULE="${TOD_MAIN_MODULE:-github.com/prokopto-dev/tod-serve}"

# classify <licence-file> — echoes an SPDX-ish identifier, or UNKNOWN.
#
# Matched on the distinctive sentence of each licence rather than on a title, because plenty of
# projects retitle the file and a few omit the header entirely. Order matters: the GNU family is
# tested first, since LGPL and AGPL both contain long stretches of GPL text.
classify() {
  local f="$1" t
  t=$(tr '[:upper:]' '[:lower:]' < "$f" | tr -s '[:space:]' ' ')

  case "$t" in
    *"gnu affero general public license"*)  echo AGPL ;;
    *"gnu lesser general public license"*)  echo LGPL ;;
    *"gnu general public license"*)         echo GPL ;;
    *"server side public license"*)         echo SSPL ;;
    *"business source license"*)            echo BUSL ;;
    *"elastic license"*)                    echo ELASTIC ;;
    *"creative commons attribution-sharealike"*) echo CC-BY-SA ;;
    *"mozilla public license"*)             echo MPL-2.0 ;;
    *"apache license"*"version 2.0"*)       echo Apache-2.0 ;;
    *"permission is hereby granted, free of charge"*) echo MIT ;;
    *"permission to use, copy, modify, and/or distribute"*) echo ISC ;;
    *"redistribution and use in source and binary forms"*)
      # The third clause is the no-endorsement one; without it this is the 2-clause licence.
      case "$t" in
        *"neither the name of"*) echo BSD-3-Clause ;;
        *)                       echo BSD-2-Clause ;;
      esac ;;
    *) echo UNKNOWN ;;
  esac
}

# The licences a dependency may carry. Everything else — including anything unclassifiable — is a
# finding. MPL-2.0 is deliberately absent: it is file-level copyleft, which is what the invariant
# says no runtime dependency may be, and admitting it "because it is only weak" is the argument that
# ends with somebody admitting LGPL for the same reason.
permitted() {
  case "$1" in
    Apache-2.0|MIT|BSD-2-Clause|BSD-3-Clause|ISC) return 0 ;;
    *) return 1 ;;
  esac
}

# `licence-gate.sh classify <file>` prints the identifier and exits. That is the seam test/repo
# drives: a gate nobody has watched fail is a gate nobody knows works, and the interesting inputs
# here are licence TEXTS, which is exactly what a fixture can be. It also needs no Go toolchain,
# so the classifier is testable on a machine that cannot run the walk below.
if [ "${1:-}" = "classify" ]; then
  [ -n "${2:-}" ] && [ -f "${2:-}" ] || { echo "usage: licence-gate.sh classify <file>" >&2; exit 2; }
  classify "$2"
  exit 0
fi

if ! command -v go >/dev/null 2>&1; then
  report LIC001 "the Go toolchain is not on PATH; this gate will not report a pass it did not earn"
  exit 1
fi

mods=$(go list -deps -f '{{with .Module}}{{.Path}}|{{.Dir}}{{end}}' ./... 2>/dev/null \
       | grep -v '^$' | sort -u)

if [ -z "$mods" ]; then
  report LIC001 "no modules were listed; \`go list -deps ./...\` failed or the module graph is empty"
  exit 1
fi

checked=0
while IFS='|' read -r path dir; do
  [ -z "$path" ] && continue
  [ "$path" = "$MAIN_MODULE" ] && continue

  if [ -z "$dir" ] || [ ! -d "$dir" ]; then
    report LIC001 "$path has no module directory on disk; run \`go mod download\` first"
    continue
  fi

  # Some modules ship the licence one level up (a /v2 or a sub-module), so walk toward the cache
  # root until the tree changes owner. Bounded, because an unbounded walk finds SOMEBODY's licence.
  lic=""
  probe="$dir"
  for _ in 1 2 3; do
    lic=$(find "$probe" -maxdepth 1 -type f \
            \( -iname 'LICENSE*' -o -iname 'LICENCE*' -o -iname 'COPYING*' \) 2>/dev/null \
          | sort | head -n 1)
    [ -n "$lic" ] && break
    probe=$(dirname "$probe")
  done

  if [ -z "$lic" ]; then
    report LIC001 "$path ships no LICENSE file this gate can find — classify it by hand"
    continue
  fi

  id=$(classify "$lic")
  checked=$((checked + 1))

  if [ "$id" = "UNKNOWN" ]; then
    report LIC001 "$path: could not classify $(basename "$lic") — read it, then teach classify()"
  elif ! permitted "$id"; then
    report LIC001 "$path is $id, which this project does not ship"
  fi
done <<< "$mods"

[ $fail -eq 0 ] && pass LIC001 \
  "$checked runtime module(s), each under a permissive licence (Apache-2.0, MIT, BSD or ISC)"

exit $fail
