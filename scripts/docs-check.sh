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
# strip_shell_text — the executable skeleton of a shell file: quoted text, comments and heredoc
# BODIES removed, separators and command words kept. One space replaces each string, so
# `report LIC001 "..."` stays `report LIC001 ` and adjacent words never fuse.
#
# It is a character scanner and not a `sed`, because a regex cannot pair quotes it cannot count.
# `s/"[^"]*"//g` pairs left to right, so on
#
#     echo "a\"; report NOPE999 \"b"
#
# — one argument to echo, escaped quotes and all — it removed `"a\"` and `\"b"` and left
# `echo ; report NOPE999`, a semicolon and a call present in no shell reading of that line.
#
# Four rules, each one a phantom that got through the previous version:
#
#   backslash    escapes the next character.
#   single quote takes no escapes at all; the string ends at the next quote whatever precedes it.
#   `#`          opens a comment only at a word boundary, so `${x#prefix}` is an expansion — the
#                regex truncated there and silently LOST any real call later on that line.
#   heredoc      a `<<` body is data. `<<` is counted as a RUN, because scanning `<<<` one
#                character in looks exactly like `<<` followed by a non-`<`: that mistook every
#                here-string in this repository for a heredoc and swallowed the rest of the file,
#                taking 34 real gate definitions down to zero.
strip_shell_text() {
  awk '
    BEGIN { sq = sprintf("%c", 39); dq = sprintf("%c", 34); bs = sprintf("%c", 92)
            hd = ""; hdstrip = 0 }
    {
      if (hd != "") {
        line = $0
        if (hdstrip) sub(/^\t+/, "", line)
        if (line == hd) { hd = ""; hdstrip = 0 }
        print ""
        next
      }
      out = ""; n = length($0); i = 1
      while (i <= n) {
        c = substr($0, i, 1)
        if (c == bs) { i += 2; continue }
        if (c == sq) {
          i++
          while (i <= n && substr($0, i, 1) != sq) i++
          i++; out = out " "; continue
        }
        if (c == dq) {
          i++
          while (i <= n) {
            d = substr($0, i, 1)
            if (d == bs) { i += 2; continue }
            if (d == dq) break
            i++
          }
          i++; out = out " "; continue
        }
        if (c == "<") {
          k = 0
          while (i + k <= n && substr($0, i + k, 1) == "<") k++
          if (k != 2) { for (j = 0; j < k; j++) out = out "<"; i += k; continue }
          i += 2; hdstrip = 0
          if (substr($0, i, 1) == "-") { hdstrip = 1; i++ }
          while (i <= n && substr($0, i, 1) ~ /[ \t]/) i++
          q = ""
          if (i <= n && (substr($0, i, 1) == sq || substr($0, i, 1) == dq)) { q = substr($0, i, 1); i++ }
          word = ""
          while (i <= n) {
            d = substr($0, i, 1)
            if (q != "") { if (d == q) { i++; break } }
            else { if (d ~ /[ \t;&|<>()]/) break }
            if (d == bs) { i++; d = substr($0, i, 1) }
            word = word d; i++
          }
          hd = word; out = out " "; continue
        }
        if (c == "#" && (out == "" || substr(out, length(out), 1) ~ /[ \t]/)) break
        out = out c; i++
      }
      print out
    }
  ' "$@"
}

# gate_definitions <root> — every gate id DEFINED under <root>, by the FORM a definition takes and
# never by a mention of one. A stale `# report FOO001` left behind by a deleted gate is a mention;
# `x && report FOO001 "..."` is a definition. Likewise `^func Test…` rather than a comment naming
# the test, and, in internal/repogate, the two forms that declare a rule id in a NON-test file — a
# `_test.go` asserting `require.Equal(t, "CLOCK001")` references a rule, it does not define one.
#
# DOC002 and DOC003 share it because they are the two DIRECTIONS of one rule. Two scans would be
# two answers to "does this gate exist", and the pair would eventually disagree — which is the bug
# they were both written to catch, in the gates that catch it.
gate_definitions() {
  root="$1"
  { # Quoted text and comments are REMOVED first, and what survives is then matched only where a
    # COMMAND can begin. Both halves are needed, and each was found by a phantom slipping past the
    # other: `echo "report NOPE999 was removed"` is prose in quotes, and `echo report NOPE999 was
    # removed` is the same prose with the quotes taken off — there `report` is an ARGUMENT to echo
    # and not a command at all. Matching bare words anywhere on a line cannot tell those apart, and
    # a gate that certifies a phantom is the one failure this gate exists to prevent.
    #
    # A command begins at the start of a line, after one of `; & | ( ) { }` — which covers `&&`,
    # `||`, a `case` arm's `)` and a `{ ... }` group — or after `then`, `else`, `do` or `elif`.
    # Every call in this repository sits in one of those positions and none is preceded by another
    # word, which is exactly what separates `report FOO001` from `echo report FOO001`.
    #
    # Stripping strings before comments also makes the comment rule exact rather than approximate:
    # a `#` inside a string is no longer mistaken for the start of a comment.
    strip_shell_text "$root"/scripts/*.sh 2>/dev/null \
      | grep -oE '(^|[;&|(){}]|[[:space:]](then|else|do|elif))[[:space:]]*(report|pass|vacant)[[:space:]]+[A-Z]+[0-9]{3}'
    grep -hoE '^func Test[A-Z]+[0-9]{3}_' "$root"/test/repo/*.go 2>/dev/null
    find "$root/internal/repogate" -maxdepth 1 -name '*.go' ! -name '*_test.go' -exec \
      grep -hoE '^[^/]*(ID:[[:space:]]*|=[[:space:]]*)"[A-Z]+[0-9]{3}"' {} + 2>/dev/null
  } | grep -oE '[A-Z]+[0-9]{3}' | sort -u
}

  gates=$(gate_definitions .)
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
    # A gate is DEFINED by a definition, never by a mention of one. This scan used to accept any
    # matching text, so a stale `# report FOO001 ...` left behind by a deleted gate kept a
    # backticked FOO001 on this page looking enforced — which is the exact phantom DOC003 exists to
    # catch, reintroduced by DOC003 itself. Each of the three is now pinned to a form that only a
    # real definition takes:
    #
    #   shell     a call with no `#` before it on the line, so `# report FOO001` is a mention
    #             while `[ -n "$x" ] && report FOO001 "..."` is still a call.
    #   test/repo `^func Test…`, anchored, because a comment naming a test is not the test.
    #   repogate  the two forms that declare a rule id, in NON-test files: a `_test.go` asserting
    #             `require.Equal(t, "CLOCK001")` references a rule, it does not define one.
    #
    # Tightening this changed nothing about what resolves today — 36 before and 36 after — which is
    # what says it is a tightening rather than a rewrite.
    GATE_ROOT="${TOD_GATE_ROOT:-.}"
    defined=$(gate_definitions "$GATE_ROOT")

    # The same vacancy argument the test scan makes about itself: an empty set here would report
    # every gate the page names as a phantom, burying a real finding under three dozen false ones.
    n=0
    if [ -z "$defined" ]; then
      report DOC003 "no gate definitions were found under $GATE_ROOT; this gate's own scan is wrong"
    else
      # Compared in ONE pass, not one subprocess per name. The loop this replaces ran
      # `echo "$defined" | grep -qx "$g"` per id and read ANY non-zero exit as "absent" — including
      # a grep that never ran. Under `go test ./test/repo` several copies of this script run at
      # once, each forking a few hundred greps, and the ones that lost the race were reported as
      # phantoms naming a different innocent gate every run. Findings that are sometimes fiction
      # are worse than no findings, because the first false one teaches everybody to ignore the
      # true ones. `grep -Fxv -f` answers the same question in one process, so no exit status has
      # to mean two different things.
      # Filtered with one grep rather than a `case` inside `$( )`: bash 3.2, which is what
      # /bin/bash still is on macOS, miscounts the `)` closing a case pattern there and fails to
      # parse the file at all. A developer's local `make check` is not a place to need bash 4.
      # shellcheck disable=SC2086 # deliberately word-split: NOT_GATES is a list, not one word.
      not_gates_re=$(printf '%s\n' $NOT_GATES | paste -sd'|' -)
      named_gates=$(printf '%s\n' "$named" | grep -vxE "$not_gates_re")
      n=$(printf '%s\n' "$named_gates" | grep -c '[^[:space:]]')
      missing=$(printf '%s\n' "$named_gates" | grep -v '^$' \
                | grep -Fxv -f <(printf '%s\n' "$defined"))
      if [ -n "$missing" ]; then
        report DOC003 "$INVARIANTS_PAGE names a gate that is defined in no script, test/repo test or repogate rule:"
        printf '%s\n' "$missing" | sed 's/^/  /'
      fi
    fi

    # Rows that start NOT HELD are dropped first. The page's own header fixes that convention —
    # "Some rows say the mechanism is missing, and they start `NOT HELD`" — and such a row names a
    # test or a path precisely to describe what does NOT exist yet. Checking them would make the
    # gate demand that a recorded gap be closed before it can be recorded, which is how a page
    # stops recording gaps.
    #
    # Gate ids are still read from the WHOLE page, deliberately. They carry their own in-band
    # marker: an id is bare when it is not a claim. A path or a test name is backticked either way,
    # because that is just code formatting, so the row label is the only marker those two have.
    claims=$(grep -v '^| \*\*NOT HELD' "$INVARIANTS_PAGE")

    # Backticked Test names must be functions somebody actually wrote. node_modules is excluded
    # because it is another language's dependency tree, not this module's tests.
    # Named directories rather than the whole tree: `.` walks .git and every build output, which is
    # slower, non-deterministic under a parallel `go test ./...`, and buys nothing — every _test.go
    # in this module is under one of these three, and `test/repo` asserts that stays true.
    # Overridable for the same reason the page is: test/repo has to be able to drive the vacancy
    # branch below, and it can only do that by pointing the search somewhere with no tests in it.
    TEST_ROOTS="${TOD_TEST_ROOTS:-internal cmd test}"
    # shellcheck disable=SC2086 # deliberately word-split: this is a list of roots, not one path.
    defined_tests=$(grep -rhoE '^func Test[A-Za-z0-9_]+\(' --include='*_test.go' \
                      $TEST_ROOTS 2>/dev/null \
                    | sed 's/^func //; s/(.*//' | sort -u)

    # Its OWN vacancy check, and the reason it is not optional: an empty extraction makes every
    # single test the page cites look absent, so a broken parse would not fail quietly — it would
    # fail LOUDLY and wrongly, reporting two hundred phantoms and burying the one real finding
    # somebody needed to see. A gate that cannot read the repository must say so about itself
    # rather than accuse the page.
    if [ -z "$defined_tests" ]; then
      report DOC003 "no test functions were found under internal/, cmd/ or test/; this gate's own parse is wrong"
    else
      named_tests=$(printf '%s\n' "$claims" | grep -ohE '`Test[A-Za-z0-9_]+`' | tr -d '`' | sort -u)
      tn=$(printf '%s\n' "$named_tests" | grep -c '[^[:space:]]')
      missing_tests=$(printf '%s\n' "$named_tests" | grep -v '^$' \
                      | grep -Fxv -f <(printf '%s\n' "$defined_tests"))
      if [ -n "$missing_tests" ]; then
        report DOC003 "$INVARIANTS_PAGE names a test that no _test.go file defines:"
        printf '%s\n' "$missing_tests" | sed 's/^/  /'
      fi
    fi

    # Backticked repository FILES must exist. This is the shape `scripts/licence-gate.sh` had: the
    # page named a script as its enforcement for a long time before the file existed, and nothing
    # noticed, because a path is not a gate id and nothing resolved it.
    #
    # Files, not directories, and that is the header's line rather than a convenience: "a row that
    # names a test, a SCRIPT or a trigger is a claim that it exists". A directory is how the page
    # says where something lives or will live — `internal/events` is named twice as the Phase 6
    # package that does not exist yet, in a row whose own rule is landed — so demanding one exist
    # would make the page unable to say "later" without lying. A glob is skipped rather than
    # guessed at.
    named_paths=$(printf '%s\n' "$claims" \
                  | grep -ohE '`(scripts|internal|db|deploy|test|cmd|web)/[A-Za-z0-9_./-]+`' \
                  | tr -d '`' | sort -u)
    missing_paths=""; pn=0
    for rpath in $named_paths; do
      case "$rpath" in *'*'*) continue ;; esac
      case "$(basename "$rpath")" in *.*) ;; *) continue ;; esac
      # A path git is told to ignore is one the repository is REQUIRED not to contain, so its
      # absence is the invariant rather than a broken claim: `deploy/.env` is named by the rule
      # that it must never be committed, and demanding it exist would invert that rule. A phantom
      # `scripts/nope-gate.sh` is not ignored, so this exempts nothing the check is for.
      if git check-ignore -q "$rpath" 2>/dev/null; then continue; fi
      pn=$((pn + 1))
      [ -e "$rpath" ] || missing_paths="$missing_paths  $rpath\n"
    done
    if [ -n "$missing_paths" ]; then
      report DOC003 "$INVARIANTS_PAGE names a repository path that does not exist:"
      printf '%b' "$missing_paths"
    fi

    if [ -z "$missing" ] && [ -z "$missing_tests" ] && [ -z "$missing_paths" ]; then
      pass DOC003 "$n gate(s), $tn test(s) and $pn path(s) named in $INVARIANTS_PAGE, each one that actually exists"
    fi
  fi
else
  report DOC003 "$INVARIANTS_PAGE is missing; it is where every gate is registered"
fi

exit $fail
