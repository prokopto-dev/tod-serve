#!/usr/bin/env bash
# The GitHub Actions gates, ACT001 and ACT002, over a directory of workflow files.
#
# They take the directory as an argument, and live here rather than inline in repo-gates.sh, so
# that a TEST can point them at deliberately broken fixtures and require them to fire
# (test/repo/act_test.go). A gate nobody has watched fail is a gate nobody knows works — and these
# two are easy to get wrong in the direction that reports success, because they inspect YAML with
# awk. The first version matched only `run: |`, so `run: echo "${{ github.ref_name }}"` walked
# straight past the gate whose entire purpose is that line. It was green the whole time.
#
# Usage: act-gates.sh <expressions|syntax|stdin> <directory>
#   expressions  ACT001 — no `${{ … }}` inside a shell script
#   syntax       ACT002 — every shell script parses under `bash -n`
#   stdin        ACT003 — no `docker compose run`/`exec` that reads the script's own stdin
#
# Exit 0 = clean; `syntax` prints how many scripts it checked. Exit 1 = a finding, printed to
# stdout with the headline first. Exit 2 = invoked wrongly.

set -uo pipefail

here="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
scanner="$here/act-scan.awk"

mode="${1:-}"
dir="${2:-}"

case "$mode" in
  expressions | syntax | stdin) ;;
  *) printf 'usage: %s <expressions|syntax|stdin> <directory>\n' "$0" >&2; exit 2 ;;
esac
[ -d "$dir" ] || { printf 'not a directory: %s\n' "$dir" >&2; exit 2; }
[ -f "$scanner" ] || { printf 'missing scanner: %s\n' "$scanner" >&2; exit 2; }

shopt -s nullglob
files=("$dir"/*.yml "$dir"/*.yaml)
shopt -u nullglob
# No workflows at all is not a finding. The caller decides whether that is vacant or expected.
[ ${#files[@]} -gt 0 ] || exit 0

case "$mode" in
  expressions)
    findings=$(awk -v mode=expressions -f "$scanner" "${files[@]}")
    if [ -n "$findings" ]; then
      printf 'a workflow interpolates an expression into a shell script:\n'
      printf '%s\n' "$findings"
      printf '  Pass it through `env:` and reference it as "$VAR" instead.\n'
      exit 1
    fi
    ;;

  syntax)
    blocks=$(mktemp -d)
    trap 'rm -rf "$blocks"' EXIT

    awk -v mode=extract -v out="$blocks" -f "$scanner" "${files[@]}"

    shopt -s nullglob
    scripts=("$blocks"/*.sh)
    shopt -u nullglob
    # Zero extracted scripts over a non-empty directory of workflows means the scanner stopped
    # recognising `run:`, not that the workflows stopped having any. Reported as a finding, because
    # a checker that checked nothing must never look like a checker that found nothing.
    if [ ${#scripts[@]} -eq 0 ]; then
      printf 'no run: scripts were extracted from %d workflow(s) in %s; the scanner is not matching\n' \
        "${#files[@]}" "$dir"
      exit 1
    fi

    bad=""
    for f in "${scripts[@]}"; do
      # `run:` defaults to bash on ubuntu runners, and every block in this repository is bash.
      #
      # ANY DIAGNOSTIC IS A FINDING, not just a non-zero exit. For an unterminated heredoc —
      # precisely the mistake this gate was added for — bash 5 exits 0 and merely WARNS,
      # "here-document delimited by end-of-file", so a check on the exit code alone would pass it.
      # Nothing in this repository produces a diagnostic from a valid script, so the stricter rule
      # costs nothing.
      #
      # SAID PLAINLY, because it is a limit rather than a guarantee: bash 3.2, which is what macOS
      # ships and therefore what `make check` runs on a laptop, accepts that same script in
      # SILENCE. CI runs ubuntu-24.04 with bash 5, which is where this case is actually caught. An
      # unterminated heredoc wrapped in an `if` is a hard syntax error on both.
      out=$(bash -n "$f" 2>&1)
      code=$?
      if [ $code -ne 0 ] || [ -n "$out" ]; then
        bad="$bad  $(basename "$f"): ${out:-exit $code}"$'\n'
      fi
    done

    if [ -n "$bad" ]; then
      printf 'a workflow shell script does not parse cleanly (file names are <workflow>-<line>.sh):\n'
      printf '%s' "$bad"
      exit 1
    fi
    printf '%d\n' "${#scripts[@]}"
    ;;

  stdin)
    # ACT003 — a `docker compose run` or `exec` must not inherit the script's stdin.
    #
    # A workflow script routinely reaches a remote shell ON STDIN (`ssh … bash -s <<'REMOTE'`), and
    # BOTH of those compose subcommands attach stdin by default. The first one then swallows the
    # rest of the script: bash reads EOF, exits 0, and the step reports SUCCESS having silently
    # skipped everything after it.
    #
    # That is the failure this gate was written for, on 2026-08-25: `migrate` ran, `up -d` never
    # did, the deploy step went green, and the verification afterwards spent thirty attempts
    # reporting the symptom while the container had never been started.
    #
    # `-T` does NOT fix it — that disables the TTY, not the stdin attachment. `</dev/null` does, and
    # `2>/dev/null` does not, which is why the pattern below looks for a `<` and not merely for the
    # device.
    blocks=$(mktemp -d)
    trap 'rm -rf "$blocks"' EXIT

    awk -v mode=extract -v out="$blocks" -f "$scanner" "${files[@]}"

    shopt -s nullglob
    scripts=("$blocks"/*.sh)
    shopt -u nullglob
    if [ ${#scripts[@]} -eq 0 ]; then
      printf 'no run: scripts were extracted from %d workflow(s) in %s; the scanner is not matching\n' \
        "${#files[@]}" "$dir"
      exit 1
    fi

    # `run` and `exec` are matched as WHOLE FIELDS rather than as substrings, so that prose like
    # "and re-run." inside an echo is not a finding — a gate with false positives is one somebody
    # switches off. Shell comment lines are skipped for the same reason: this file's own
    # explanations name the commands they are about.
    findings=$(awk '
      { line = $0
        sub(/^[ \t]+/, "", line)
        if (line ~ /^#/) next
        if (line !~ /docker compose/) next
        subcommand = 0
        for (i = 1; i <= NF; i++) if ($i == "run" || $i == "exec") subcommand = 1
        if (!subcommand) next
        if (line ~ /<[ \t]*\/dev\/null/) next
        printf "  %s:%d: %s\n", FILENAME, FNR, line
      }' "${scripts[@]}")

    if [ -n "$findings" ]; then
      printf 'a workflow runs `docker compose run` or `exec` without redirecting stdin:\n'
      printf '%s\n' "$findings"
      printf '  Add `</dev/null`. Both attach stdin, and a script fed to `bash -s` over ssh IS\n'
      printf '  stdin — the command eats the rest of it and the step exits 0 having skipped it.\n'
      printf '  (File names are <workflow>-<line>.sh; the line number is within the script.)\n'
      exit 1
    fi
    printf '%d\n' "${#scripts[@]}"
    ;;
esac
