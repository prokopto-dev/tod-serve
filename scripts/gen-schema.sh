#!/usr/bin/env bash
# Regenerate everything derived from the schema: the enum locals, the migration, the sqlc bindings.
#
# ADR-0006 splits two jobs between two tools. Atlas owns "what does the schema look like" —
# db/schema.hcl is the reviewable truth and Atlas computes the diff. goose owns "apply it", as a
# library the binary embeds, because an officer double-clicking tod-serve.exe has no migration CLI
# on their PATH.
#
# Three things happen between the two, and each is here rather than in a person's head:
#
#   1. Atlas quotes every identifier in backticks. sqlc's SQLite parser silently fails to register
#      a table whose name is backticked, and then every query against it is "relation does not
#      exist" — an error that points at the query rather than at the schema. The backticks are
#      stripped.
#
#   2. Atlas writes the NULL column constraint explicitly (`note text NULL`). sqlc reads the type
#      as unknown and emits `interface{}` for that column instead of `*string`. The constraint is
#      redundant in SQLite — a column with no NOT NULL is nullable — so it is dropped.
#
#   3. Atlas writes a Down block full of DROP statements. Migrations here are FORWARD-ONLY, so the
#      Down block is replaced with RAISE(ABORT). MIG001 in scripts/repo-gates.sh fails any
#      migration whose Down contains DDL, so a hand-edited file cannot reintroduce one.
#
# None of that is a transformation anybody should trust on sight, so the script does not ask you
# to: after rewriting the file it RE-RUNS THE DIFF. If the post-processed migration no longer says
# exactly what db/schema.hcl says, Atlas reports a pending change and this script fails.
#
# Usage: make gen [NAME=add_something]

set -euo pipefail
cd "$(dirname "$0")/.."

NAME="${1:-schema_change}"
MIGRATIONS=db/migrations-sqlite

step() { printf '\033[36m%-12s\033[0m %s\n' "$1" "$2"; }
die() { printf '\033[31m%-12s\033[0m %s\n' "gen" "$1" >&2; exit 1; }

need() {
  command -v "$1" >/dev/null 2>&1 || die "$1 is not on PATH. $2"
}

# A missing tool is an error, not a skip. `make gen` that quietly does nothing produces a stale
# generated tree that CI then has to catch, which is the drift ADR-0006 already names as a cost —
# there is no reason to add a second source of it.
need atlas 'Atlas authors the migrations (ADR-0006): https://atlasgo.io/docs#installation'
need sqlc  'sqlc generates internal/store/sqlitegen: https://docs.sqlc.dev/en/latest/overview/install.html'

# --- 1. the enum CHECK predicates, from the one Go catalogue -----------------------------------
step gen "db/enums.hcl from internal/schemaenum"
go test ./internal/dbschema -run TestEnumsHCL -update >/dev/null

# --- 2. the migration ---------------------------------------------------------------------------
# atlas.sum is checked in, so a migration that has shipped and then changed is a diff a reviewer
# sees. This script deliberately does NOT re-hash on its own: doing so would turn the one signal
# that an applied migration was edited into a file that silently agrees with whatever is there.
if ! atlas migrate validate --env local >/dev/null 2>&1; then
  die "$MIGRATIONS does not match its atlas.sum.
             If you HAND-WROTE a new migration (triggers only, see 000002_invariant_triggers.sql),
             re-hash it deliberately:  atlas migrate hash --env local
             If you EDITED an existing migration, do not re-hash: migrations are append-only as
             files, and a change to one that has shipped is a change to somebody's database."
fi

step gen "atlas migrate diff --env local $NAME"
atlas migrate diff --env local "$NAME"

# Atlas names a new file <timestamp>_<name>.sql. Canonical §16 wants NNNNNN_snake_case.sql, so a
# new file is renamed to the next sequence number. Nothing else in the directory matches the
# timestamp shape, so "did Atlas write one" is answerable without parsing its output.
authored=$(find "$MIGRATIONS" -maxdepth 1 -name '[0-9][0-9][0-9][0-9][0-9][0-9][0-9][0-9][0-9][0-9][0-9][0-9][0-9][0-9]_*.sql' | sort)

if [ -z "$authored" ]; then
  step gen "no schema change; db/schema.hcl and $MIGRATIONS already agree"
else
  for src in $authored; do
    last=$(find "$MIGRATIONS" -maxdepth 1 -name '[0-9][0-9][0-9][0-9][0-9][0-9]_*.sql' \
           -exec basename {} \; | sort | tail -1)
    prev="${last%%_*}"
    # 10# forces base 10: without it 000008 is an invalid octal literal and the arithmetic fails
    # on the eighth migration rather than on the first, which is the worst time to find out.
    next=$(printf '%06d' $(( 10#${prev:-0} + 1 )))
    dest="$MIGRATIONS/${next}_$(basename "$src" | cut -d_ -f2-)"

    # The Down block is replaced wholesale rather than edited: everything from the marker to the
    # end of the file goes, so a DROP cannot survive by sitting somewhere the pattern missed.
    sed -E -e 's/`//g' -e 's/(text|integer|real|blob|any) NULL([ ,)])/\1\2/g' "$src" \
      | awk '
          /^-- \+goose Down$/ {
            print
            print "-- +goose StatementBegin"
            print "SELECT RAISE(ABORT, '"'"'migrations are forward-only; roll forward with a new migration'"'"');"
            print "-- +goose StatementEnd"
            exit
          }
          { print }
        ' > "$dest"
    rm "$src"
    step gen "wrote $dest"
  done
  atlas migrate hash --env local
fi

# --- 3. prove the rewrite did not change what the migration means -------------------------------
# The transformation above is three sed expressions over generated SQL. This is the check that
# makes trusting them unnecessary: Atlas replays the rewritten directory into a throwaway database
# and diffs it against db/schema.hcl. Anything left over is a difference the rewrite introduced.
step gen "verifying $MIGRATIONS replays to exactly db/schema.hcl"
verify=$(atlas migrate diff --env local __gen_verify 2>&1 || true)
stray=$(find "$MIGRATIONS" -maxdepth 1 -name '*__gen_verify.sql')
if [ -n "$stray" ]; then
  find "$MIGRATIONS" -maxdepth 1 -name '*__gen_verify.sql' -delete
  atlas migrate hash --env local
  die "the rewritten migration no longer matches db/schema.hcl. Atlas planned: $verify"
fi

# --- 4. the typed Go bindings -------------------------------------------------------------------
step gen "sqlc generate -> internal/store/sqlitegen"
sqlc generate

printf '\033[32m%-12s\033[0m %s\n' "gen" "schema, migrations and bindings are current"
