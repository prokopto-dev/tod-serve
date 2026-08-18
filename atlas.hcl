// Atlas project configuration. ADR-0006: Atlas authors the migrations, goose applies them.
//
// `make gen` is the only supported way to run this — it post-processes what Atlas writes (see
// scripts/gen-schema.sh for why) and then re-runs the diff to prove the post-processing did not
// change what the migration means. Running `atlas migrate diff` by hand produces a file the
// repository gates will reject.

env "local" {
  // Two sources: the shape, which a human writes and reviews, and the enum CHECK predicates,
  // which are generated from internal/schemaenum because canonical §5 permits only one copy of an
  // enum's values.
  src = [
    "file://db/schema.hcl",
    "file://db/enums.hcl",
  ]

  // A throwaway in-memory database Atlas replays the migration directory into so it can compute
  // what has already been applied. It is never the instance's database.
  dev = "sqlite://dev?mode=memory"

  migration {
    dir = "file://db/migrations-sqlite"
    // goose applies these at boot from an embedded FS, so the files carry goose's own
    // `-- +goose Up` / `-- +goose Down` annotations.
    format = goose
  }
}
