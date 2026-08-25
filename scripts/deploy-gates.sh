#!/usr/bin/env bash
# The deployment gates, ENV001 and IMG001, over a directory of deployment files.
#
# They take the directory as an argument — and, for ENV001, the Go file that is the authority for
# what the binary reads — so that a TEST can point them at deliberately broken fixtures and require
# them to fire (test/repo/deploygates_test.go). A gate nobody has watched fail is a gate nobody
# knows works, and both of these are greps: the failure mode is reporting success over a file the
# pattern stopped matching, which looks exactly like a clean tree.
#
# Usage: deploy-gates.sh <env|images> <deploy-directory> [root.go]
#   env     ENV001 — the binary's TOD_* constants and the deployment files agree, both directions
#   images  IMG001 — every image reference is pinned to a digest, with a readable tag
#
# Exit 0 = clean, and it prints how much it looked at. Exit 1 = a finding, printed with the
# headline first. Exit 2 = invoked wrongly.

set -uo pipefail

mode="${1:-}"
dir="${2:-}"
rootgo="${3:-}"

case "$mode" in
  env | images) ;;
  *) printf 'usage: %s <env|images> <deploy-directory> [root.go]\n' "$0" >&2; exit 2 ;;
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
    # 1. Every variable the binary reads is written down where an operator will look for it.
    #    Anchored to the start of a line, optionally commented: a name has to be an ENTRY in that
    #    file, not a word somewhere in a paragraph.
    for name in $consts; do
      grep -qE "^#? ?${name}\b" "$example" \
        || findings="${findings}  ${name} is read by the binary and is not an entry in ${example}"$'\n'
    done

    # 2. And nothing in the deployment files names a variable the binary does not read. This is the
    #    direction that catches a typo: `TOD_TOKEN_PEPER` in a compose file interpolates to empty
    #    and the server refuses to start with a message about a variable that IS set.
    scanned=$(find "$dir" -type f 2>/dev/null | sort)
    for name in $(grep -rhoE 'TOD_[A-Z0-9_]+' $scanned 2>/dev/null | sort -u); do
      case "$name" in
        "${deploy_prefix}"*) continue ;;
      esac
      echo "$consts" | grep -qx "$name" \
        || findings="${findings}  ${name} appears in ${dir} and is not a constant in ${rootgo}"$'\n'
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
    printf '%d\n' "$(echo "$consts" | grep -c .)"
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
esac
