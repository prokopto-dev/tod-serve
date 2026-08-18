# ADR-0001 — One Go binary and one SQLite file

**Status:** accepted · **Date:** 2026-08-18 · **Deciders:** Courtney Caldwell

## Context and problem statement

The people who will run this are P99 officers, not operators. A deployment story that requires
provisioning a database, a reverse proxy, a cron entry and a cache is a deployment story most of them
will not complete — and the ones who do will not upgrade. Whatever we choose has to survive being
run on a home raid PC by someone who does not want to learn it.

## Considered options

| Option | For | Against |
|---|---|---|
| A — Go, static binary, embedded SQLite and SPA | One file to download. No runtime to install, no database to provision. Cross-compiles to every platform an officer owns | A second language in an ecosystem whose plugin side is Python; SQLite constrains concurrent writers |
| B — Python + FastAPI + SQLite | Shares a language with nParse+ and the SDK, so domain code could in principle be reused | Self-hosters need a Python environment, and "it works on my machine" is the failure this decision exists to avoid. Nothing in the server is actually shareable with a Qt overlay anyway |

## Decision outcome

**Chosen: A**, matching Dragon Kill Party's stack exactly: Go 1.26, `huma/v2` for code-first OpenAPI,
`goose` for embedded migrations, `cobra` for the CLI, `ulid/v2` for ids, `modernc.org/sqlite` (pure
Go, so `CGO_ENABLED=0` and cross-compilation stays trivial), sqlc for typed queries, Atlas authoring
migrations from `db/schema.hcl`.

Two projects in the same ecosystem, read by the same officers and driven by the same plugin, should
not disagree about what a timestamp is. Sharing the stack means sharing the conventions, the gates
and the reviewer instincts.

The write-concurrency limit is not a real constraint here: a busy circle produces a few hundred rows
a week.

### Consequences

- Good, because installation is downloading a file, and upgrading is replacing it.
- Good, because the conventions, lint gates and repo tooling transfer from the sibling project
  wholesale rather than being reinvented.
- Good, because `CGO_ENABLED=0` makes multi-arch a cross-compile rather than a QEMU build.
- **Bad, because the ecosystem is now two languages.** A contributor comfortable with the nParse+
  plugin cannot necessarily fix the server, and vice versa.
- **Bad, because SQLite means one writer.** Fine at this volume, and a wall if the product ever grows
  a use case with sustained write concurrency.
- **Bad, because embedding the SPA couples frontend and backend release cadence** — a CSS fix ships a
  new binary.

### Reversal cost

Changing the database is a release and a migration tool. Changing the language is a rewrite.
