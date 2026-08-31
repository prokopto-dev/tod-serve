#!/usr/bin/env bash
# The deployment gates, ENV001 and IMG001, over a directory of deployment files.
#
# They take the directory as an argument — and, for ENV001, the Go file that is the authority for
# what the binary reads — so that a TEST can point them at deliberately broken fixtures and require
# them to fire (test/repo/deploygates_test.go). A gate nobody has watched fail is a gate nobody
# knows works, and both of these are greps: the failure mode is reporting success over a file the
# pattern stopped matching, which looks exactly like a clean tree.
#
# Usage: deploy-gates.sh <env|images|traefik> <deploy-directory> [root.go]
#   env      ENV001 — the binary's TOD_* constants and the deployment files agree, both directions
#   images   IMG001 — every image reference is pinned to a digest, with a readable tag
#   traefik  LBL001 — every Traefik name a label REFERENCES is a name some label DEFINES
#
# Exit 0 = clean, and it prints how much it looked at. Exit 1 = a finding, printed with the
# headline first. Exit 2 = invoked wrongly.

set -uo pipefail

mode="${1:-}"
dir="${2:-}"
rootgo="${3:-}"

case "$mode" in
  env | images | traefik) ;;
  *) printf 'usage: %s <env|images|traefik> <deploy-directory> [root.go]\n' "$0" >&2; exit 2 ;;
esac
[ -d "$dir" ] || { printf 'not a directory: %s\n' "$dir" >&2; exit 2; }

# The prefix that means "docker compose reads this and the binary never does".
#
# It exists so this gate needs no allowlist. TOD_DEPLOY_HOST and TOD_DEPLOY_IMAGE are real
# variables an operator sets, and they are not configuration of the SERVER — a hand-written list of
# exceptions would be a list somebody appends to at 2am, which is the shape of rule this repository
# keeps deleting.
deploy_prefix="TOD_DEPLOY_"

case "$mode" in
  env)
    [ -n "$rootgo" ] || { printf 'usage: %s env <deploy-directory> <root.go>\n' "$0" >&2; exit 2; }
    [ -f "$rootgo" ] || { printf 'not a file: %s\n' "$rootgo" >&2; exit 2; }
    example="$dir/env.example"
    [ -f "$example" ] || { printf 'ENV001 needs %s and it is not there\n' "$example"; exit 1; }

    # The authority: the const block. Matched on the ASSIGNMENT, so a name mentioned in a comment
    # or in an error message is not mistaken for one the binary reads.
    consts=$(grep -oE '=[[:space:]]*"TOD_[A-Z0-9_]+"' "$rootgo" \
             | grep -oE 'TOD_[A-Z0-9_]+' | sort -u)
    if [ -z "$consts" ]; then
      printf 'ENV001 parsed no TOD_ constants out of %s; the pattern is wrong\n' "$rootgo"
      exit 1
    fi

    findings=""

    # NOTHING BELOW THIS LINE MAY FORK A PROCESS PER VARIABLE NAME.
    #
    # Both membership tests used to be a `grep` inside the loop, so the gate forked once per TOD_
    # name — around twenty-five processes for a repository with ten constants. A fork that fails
    # under load (EAGAIN, the per-user process limit) makes grep exit non-zero, and a non-zero grep
    # is INDISTINGUISHABLE from "no match": the gate then reports a variable as undocumented, or as
    # named-but-not-a-constant, with total confidence. Measured over 3000 parallel runs a side:
    # 11 findings that NAMED A VARIABLE before this was hoisted, 0 after, and 29 processes a run
    # down to 8 — the per-name forks are gone, so the count no longer grows with the const block.
    #
    # It surfaced far from here. test/repo's TEN001 tests shell out to the whole of
    # repo-gates.sh, so the failure arrived as a bare `exit status 1` attributed to whichever test
    # happened to fork the script — MIG001 wore the blame on 2026-08-28, ENV001 on 2026-08-29, and
    # the TEN001 tests both times. THE TEST NAME IN SUCH A FAILURE IS MEANINGLESS.
    #
    # THE DISCRIMINATOR, when this gate goes red and you need to know which kind of red it is:
    # a real defect names THE SAME variable on every run. A fork failure names a DIFFERENT one
    # each time, because which name loses the race is which name happened to be in hand when the
    # limit was hit. Run it a dozen times before you go looking at deploy/env.example.
    #
    # So: read each input ONCE, outside the loop, and do the per-name test with shell builtins.

    # 1. Every variable the binary reads is written down where an operator will look for it.
    #    Anchored to the start of a line, optionally commented: a name has to be an ENTRY in that
    #    file, not a word somewhere in a paragraph.
    #
    #    `^#? ?NAME\b` per name, done without grep. Each line is reduced ONCE to the entry name it
    #    declares, and the names are then compared as whole strings:
    #      - `^#? ?` — strip at most one leading `#`, then at most one leading space. Stripping
    #        greedily is safe because a TOD_ name can begin with neither, so no shorter prefix the
    #        regex would have tried can match where the greedy one does not.
    #      - `\b` — take the leading run of word characters. `${line%%[!A-Za-z0-9_]*}` cuts at the
    #        first character grep would call a word boundary, so `TOD_ADDR` does NOT match an entry
    #        `TOD_ADDRESS=x`: the run is the longer name, and the two are unequal.
    #    `|| [ -n "$line" ]` because grep reads a last line with no trailing newline and `read`
    #    reports failure on it.
    entries=$'\n'
    while IFS= read -r line || [ -n "$line" ]; do
      line="${line#\#}"
      line="${line# }"
      entries="${entries}${line%%[!A-Za-z0-9_]*}"$'\n'
    done < "$example"

    for name in $consts; do
      case "$entries" in
        *$'\n'"$name"$'\n'*) ;;
        *) findings="${findings}  ${name} is read by the binary and is not an entry in ${example}"$'\n' ;;
      esac
    done

    # 2. And nothing in the deployment files names a variable the binary does not read. This is the
    #    direction that catches a typo: `TOD_TOKEN_PEPER` in a compose file interpolates to empty
    #    and the server refuses to start with a message about a variable that IS set.
    scanned=$(find "$dir" -type f 2>/dev/null | sort)
    if [ -z "$scanned" ]; then
      # A checker that checked nothing must never look like a checker that found nothing — and here
      # it is worse than that. `grep -r PATTERN` with no file operand reads the CURRENT DIRECTORY,
      # so an empty list does not scan nothing, it scans the whole repository: the run that put
      # this guard here reported TOD_NEW_THING and TOD_TOKEN_PEPER, which are fixtures inside
      # test/repo/deploygates_test.go, as findings against deploy/.
      printf 'ENV001 listed no files under %s; it must never fall back to scanning the tree\n' "$dir"
      exit 1
    fi

    #    `grep -qx` per name, done without grep. `$consts` is one name per line, so wrapping it in
    #    newlines at both ends puts a `\n` on both sides of EVERY line; a pattern of the name
    #    likewise bounded then matches only a whole line, which is what `-x` means. `TOD_ADDR` does
    #    not match a `TOD_ADDRESS` line, because what follows the name there is `E` and not `\n`.
    #    The names hold `[A-Z0-9_]` only, so they are neither BRE metacharacters to grep nor glob
    #    metacharacters to `case`; quoting them inside the pattern keeps that true regardless.
    #    An empty `$consts` degenerates to `\n\n`, which matches no name — the same answer
    #    `grep -qx` gives against a single empty line, though the guard above already exited.
    #
    #    The scan is hoisted for a second reason: an empty result USED TO PASS. A failed fork left
    #    the `for` list empty, direction 2 then examined nothing, and the gate exited 0 — a silent
    #    false negative, which is worse than the red one above. env.example lives inside "$dir" and
    #    documents every constant, so a run that finds no TOD_ name there did not scan.
    deploy_names=$(grep -rhoE 'TOD_[A-Z0-9_]+' $scanned 2>/dev/null | sort -u)
    if [ -z "$deploy_names" ]; then
      printf 'ENV001 found no TOD_ names in %s; it must never pass on a scan that read nothing\n' "$dir"
      exit 1
    fi

    consts_set=$'\n'"$consts"$'\n'
    for name in $deploy_names; do
      case "$name" in
        "${deploy_prefix}"*) continue ;;
      esac
      case "$consts_set" in
        *$'\n'"$name"$'\n'*) ;;
        *) findings="${findings}  ${name} appears in ${dir} and is not a constant in ${rootgo}"$'\n' ;;
      esac
    done

    # 3. The prefix means what it says. A TOD_DEPLOY_ name reaching Go source would be a variable
    #    the gate above has been told to ignore AND the binary reads, which is the one combination
    #    that makes the whole convention worthless.
    leaked=$(grep -rlE "\"${deploy_prefix}[A-Z0-9_]+\"" ./cmd ./internal 2>/dev/null || true)
    if [ -n "$leaked" ]; then
      findings="${findings}  ${deploy_prefix}* names the binary must never read appear in: ${leaked}"$'\n'
    fi

    if [ -n "$findings" ]; then
      printf 'the binary'"'"'s environment and the deployment files disagree:\n'
      printf '%s' "$findings"
      printf '  The constants in %s are the authority for what the binary reads.\n' "$rootgo"
      printf '  Names beginning with %s are read by docker compose and by the binary never.\n' "$deploy_prefix"
      exit 1
    fi
    # Counted in the shell for the same reason: this is the LAST command of a clean run, so its
    # status is the gate's. A failed fork here made `printf '%d' ''` print 0 and return 1, which
    # repo-gates.sh renders as a finding whose entire headline is `0`.
    count=0
    while IFS= read -r _; do count=$((count + 1)); done <<< "$consts"
    printf '%d\n' "$count"
    ;;

  images)
    # PIN001's reasoning, applied to images. A tag is mutable, so pinning to one means trusting
    # whoever can move it — on files that describe what runs in production and what CI builds from.
    #
    # The digest alone is not enough: a bare digest is a pin nobody can update, because nobody can
    # tell what it was meant to be. The human-readable tag has to be recoverable, either inline as
    # `name:tag@sha256:…` or as a trailing comment.
    findings=""
    checked=0
    waived=0

    refs=$(grep -hnE '^[[:space:]]*FROM[[:space:]]' "$dir"/Dockerfile* 2>/dev/null || true)
    while IFS= read -r line; do
      [ -n "$line" ] || continue
      # Drop the line number, the FROM, and any --platform flag; take the reference.
      ref=$(printf '%s' "$line" | sed -E 's/^[0-9]+://; s/^[[:space:]]*FROM[[:space:]]+//' \
            | awk '{for (i = 1; i <= NF; i++) if ($i !~ /^--/) { print $i; exit } }')
      # `scratch` is not an image and there is nothing to pin: it is the empty filesystem, by name.
      [ "$ref" = "scratch" ] && continue
      checked=$((checked + 1))
      case "$ref" in
        *:*@sha256:????????????????????????????????????????????????????????????????) ;;
        *) findings="${findings}  FROM ${ref} is not pinned as name:tag@sha256:<64 hex>"$'\n' ;;
      esac
    done <<< "$refs"

    images=$(grep -hE '^[[:space:]]*image:[[:space:]]' "$dir"/*.yaml "$dir"/*.yml 2>/dev/null || true)
    while IFS= read -r line; do
      [ -n "$line" ] || continue
      value=$(printf '%s' "$line" | sed -E 's/^[[:space:]]*image:[[:space:]]*//')
      ref=$(printf '%s' "$value" | sed -E 's/[[:space:]]*#.*$//')
      case "$ref" in
        '${'*)
          # ONE interpolation is waived, and it is waived by NAME: ${TOD_DEPLOY_IMAGE…}, the image
          # this repository builds. It cannot be digest-pinned in a file shipping from the same
          # commit that produces it — the digest does not exist yet — and the deploy workflow
          # rewrites it to an exact tag in the host's .env before it pulls.
          #
          # Any OTHER variable is a finding, because `image: ${PROXY_IMAGE:-caddy:2}` is exactly how
          # an unpinned third-party image walks past a gate that waived "interpolations". And where
          # this one carries a DEFAULT, the default has to name our own image: a default is what
          # runs when the variable is unset, so it is a real image reference and not a placeholder.
          case "$ref" in
            '${'"${deploy_prefix}"'IMAGE'[:}]*)
              waived=$((waived + 1))
              case "$ref" in
                *:-*)
                  case "$ref" in
                    *:-ghcr.io/prokopto-dev/tod-serve*) ;;
                    *) findings="${findings}  image: ${ref} defaults to an image this repository does not build"$'\n' ;;
                  esac ;;
              esac ;;
            *) findings="${findings}  image: ${ref} interpolates ${ref%%[:}]*}, which is not the one variable the deploy workflow pins"$'\n' ;;
          esac
          continue ;;
      esac
      checked=$((checked + 1))
      case "$ref" in
        *@sha256:????????????????????????????????????????????????????????????????) ;;
        *) findings="${findings}  image: ${ref} is not pinned to @sha256:<64 hex>"$'\n' ;;
      esac
      # A digest with no readable tag is a pin nobody can update.
      case "$value" in
        *:*@sha256:* | *'#'*) ;;
        *) findings="${findings}  image: ${ref} carries no readable tag, inline or in a trailing comment"$'\n' ;;
      esac
    done <<< "$images"

    if [ "$checked" -eq 0 ]; then
      # A checker that checked nothing must never look like a checker that found nothing.
      printf 'no image references were found in %s; the patterns are not matching\n' "$dir"
      exit 1
    fi
    if [ -n "$findings" ]; then
      printf 'an image reference is not pinned to a digest:\n'
      printf '%s' "$findings"
      printf '  A tag is mutable. Pin as name:tag@sha256:<digest>, keeping the tag readable.\n'
      exit 1
    fi
    printf '%d %d\n' "$checked" "$waived"
    ;;

  traefik)
    # LBL001 — a Traefik label that NAMES a router, service or middleware must name one that some
    # other label DEFINES.
    #
    # Traefik resolves these by string. A router whose `service=` says `todserve` when the service
    # labels say `tod-serve` is not an error anybody sees: the router exists, the service does not,
    # and the host answers **404** — the same 404 Traefik gives a host it has never heard of. On a
    # deployment whose entire failure vocabulary is "404 means the container is not up yet", a typo
    # that produces the identical symptom is the worst bug available, and `docker compose config`
    # does not inspect label CONTENTS at all.
    #
    # Read from the file rather than from a rendered config, so it needs no environment: every name
    # here is a literal, and the only interpolated part of a Traefik label in this repository is the
    # host inside a `rule=`.
    #
    # Both label spellings are accepted — `key: value` mapping and `- "key=value"` list — because
    # the reference stacks on the target droplet are written the second way, and a gate that only
    # understood ours would pass a file copied from one of them without reading it.
    # nullglob, because `deploy/` has no *.yml and awk on macOS DIES on a filename that does not
    # exist — with stderr discarded, that is a gate producing no output and no finding, which is
    # exactly the shape of failure every gate in this repository is written against. It cost one
    # debugging round here.
    shopt -s nullglob
    label_files=("$dir"/*.yaml "$dir"/*.yml)
    shopt -u nullglob
    if [ ${#label_files[@]} -eq 0 ]; then
      printf 'no compose files in %s to read Traefik labels from\n' "$dir"
      exit 1
    fi

    findings=$(awk '
      {
        line = $0
        sub(/^[ \t]*-[ \t]*/, "", line)
        sub(/^[ \t]+/, "", line)
        gsub(/"/, "", line)
        if (line !~ /^traefik\.http\.(routers|services|middlewares)\./) next

        key = line; sub(/[:=].*$/, "", key)
        val = line; sub(/^[^:=]*[:=][ \t]*/, "", val); gsub(/[ \t]/, "", val)

        split(key, part, ".")
        defined[part[3] " " part[4]] = 1

        if (part[3] != "routers") next
        if (key ~ /\.service$/)     { refs[++n] = "services " val; where[n] = FILENAME ":" FNR }
        if (key ~ /\.middlewares$/) {
          m = split(val, chain, ",")
          for (i = 1; i <= m; i++)
            if (chain[i] != "") { refs[++n] = "middlewares " chain[i]; where[n] = FILENAME ":" FNR }
        }
      }
      END {
        if (n == 0) { print "VACANT"; exit }
        for (i = 1; i <= n; i++) {
          if (refs[i] in defined) continue
          split(refs[i], r, " ")
          kind = r[1]; sub(/s$/, "", kind)
          printf "  %s: names the %s %s, and no label defines it\n", where[i], kind, r[2]
        }
        printf "CHECKED %d\n", n
      }
    ' "${label_files[@]}")

    case "$findings" in
      VACANT | "")
        # A checker that checked nothing must never look like a checker that found nothing.
        printf 'no Traefik router references were found in %s; the pattern is not matching\n' "$dir"
        exit 1 ;;
    esac
    count=$(printf '%s\n' "$findings" | sed -n 's/^CHECKED //p')
    problems=$(printf '%s\n' "$findings" | grep -v '^CHECKED ' || true)
    if [ -n "$problems" ]; then
      printf 'a Traefik label points at a name nothing defines:\n'
      printf '%s\n' "$problems"
      printf '  Traefik resolves these by string. The router will exist, its target will not, and\n'
      printf '  the host answers 404 — the same 404 as a host with no router at all.\n'
      exit 1
    fi
    printf '%s\n' "$count"
    ;;
esac
