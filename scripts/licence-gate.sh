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

# The walk's exit status IS the gate. `go list` prints the packages it managed to resolve and exits
# non-zero when any part of the graph failed to load, so a discarded status means a green LIC001 over
# whatever subset survived — a pass this gate did not earn, which is the one outcome it exists to
# refuse. The status is therefore taken before the output is used, and stderr is kept rather than
# sent to /dev/null so the finding can say WHY the walk failed; discarding it is what made this
# invisible. Watched failing: with a `go` that prints one module line and exits 1, the gate used to
# report a pass.
list_out=$(mktemp) || { echo "LIC001: cannot create a temporary file" >&2; exit 2; }
list_err=$(mktemp) || { rm -f "$list_out"; echo "LIC001: cannot create a temporary file" >&2; exit 2; }
trap 'rm -f "$list_out" "$list_err"' EXIT

go list -deps -f '{{with .Module}}{{.Path}}|{{.Dir}}{{end}}' ./... >"$list_out" 2>"$list_err"
status=$?

if [ "$status" -ne 0 ]; then
  report LIC001 "\`go list -deps ./...\` exited $status; the module graph is incomplete, and a pass over part of it is not a pass"
  sed -n '1,5p' "$list_err" | sed 's/^/           /'
  exit 1
fi

mods=$(grep -v '^$' "$list_out" | sort -u)

if [ -z "$mods" ]; then
  report LIC001 "no modules were listed; \`go list -deps ./...\` reported success over an empty module graph"
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

  # The module's OWN directory, and no ancestor of it. There was a bounded walk toward the cache
  # root here, for modules said to ship their licence one level up — but every ancestor of a module
  # directory belongs to somebody else, so what it finds is another module's licence attributed to
  # this one, and a permissive ancestor over an unlicensed dependency is a green build. That is the
  # same fail-open the classifier's UNKNOWN arm exists to prevent, reached by a different route.
  # It bought nothing either: all 19 runtime modules carry their own LICENSE, so the walk never ran
  # except in the case where it would be wrong. A module that genuinely has none is now a finding
  # somebody reads, which is what "fail closed" means here.
  lic=$(find "$dir" -maxdepth 1 -type f \
          \( -iname 'LICENSE*' -o -iname 'LICENCE*' -o -iname 'COPYING*' \) 2>/dev/null \
        | sort | head -n 1)

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

# The third and last way in: an input that arrived COMPLETE and NON-EMPTY and still classified
# nothing. `go list` can exit 0, `$mods` can be full, and every line can still be filtered out —
# the main module is skipped by design — leaving a green "0 runtime module(s), each under a
# permissive licence", which is a sentence about nothing said in the voice of a guarantee.
#
# This is the shape four gates were caught in this week: SQL001 and NET001 passed over zero files
# when their allowances excluded everything, ENV001 exited 0 with a failed name-scan, and this. In
# every one the gate passed because its input was empty or truncated and nothing said so, so the
# floor is asserted here rather than left to whoever reads the count. `vacant` is deliberately NOT
# used: an absent console is a legitimate state for WEB001, but this binary has dependencies, and a
# walk that classified none of them did not check the invariant — it just did not look.
if [ "$checked" -eq 0 ]; then
  report LIC001 "no dependency module was classified; the walk yielded only $MAIN_MODULE, so this gate checked nothing"
fi

[ $fail -eq 0 ] && pass LIC001 \
  "$checked runtime module(s), each under a permissive licence (Apache-2.0, MIT, BSD or ISC)"

exit $fail
