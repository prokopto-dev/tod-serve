// db/schema.hcl — the single declarative truth for the schema. ADR-0006: Atlas authors the
// migrations from this file, goose applies them at boot.
//
// A schema review is a diff of THIS FILE. Nothing here may be hand-applied to a database and
// nothing may be hand-written into db/migrations-sqlite/: `make gen` computes the difference and
// writes the migration, and it re-runs the diff afterwards to prove the migration it wrote says
// exactly what this file says.
//
// Conventions, from docs/design/00-canonical-conventions.md §8:
//
//   Every table is STRICT. STRICT permits only INT, INTEGER, REAL, TEXT, BLOB and ANY — BIGINT,
//   BOOLEAN, DATETIME, NUMERIC and DECIMAL are illegal, and INTEGER is already 64-bit.
//   Table names are singular, columns are snake_case.
//   id          TEXT    — ULID, 26 characters of Crockford base32, minted in Go
//   *_at        INTEGER — Micros: Unix MICROseconds, UTC
//   updated_at          — present ONLY on mutable tables; an append-only table has none
//   booleans    INTEGER CHECK (x IN (0,1))
//   *_json      TEXT    — validated on write, NEVER queried into
//   name_norm   TEXT    — normalised IN GO (NFKC + casefold + strip ' ` -), then indexed
//
// Enum CHECK constraints are NOT written here. They live in db/enums.hcl, which is generated from
// internal/schemaenum, and are referred to below as `local.check_<table>_<column>`. Canonical §5
// requires one source for an enum's values; a value list typed into this file would be a second.
//
// Two things this file cannot express, and where they live instead:
//
//   Triggers. Atlas Community neither inspects nor emits SQLite triggers, so the append-only and
//   immutability triggers are hand-written in their own migration. That is safe precisely BECAUSE
//   Atlas cannot see them: it will never plan a diff that drops one. It also means a table rebuild
//   silently drops them, which is why TestAppendOnly_TriggersFire_AfterAllMigrations exists and
//   why it asserts an abort rather than a row in sqlite_master.
//
//   Foreign keys are NO ACTION, deliberately, everywhere. There is no cascade in this schema. The
//   report log is append-only and there is no delete-membership operation at all, so a delete that
//   would orphan a row is a bug, and refusing it is the correct outcome.

schema "main" {}

// ---------------------------------------------------------------------------------------------
// Instance-scoped tables — no circle_id.
//
// This set is the allowlist in canonical §9. TestInstanceScopedAllowlist_MatchesTheSchema derives
// it from that document and asserts every other table in the applied schema carries
// `circle_id NOT NULL REFERENCES circle(id)`, so a new table is tenancy-checked whether or not
// anybody remembered to think about it.
// ---------------------------------------------------------------------------------------------

// tod_meta — instance key/value: schema version, pepper generation, event head.
//
// WITHOUT ROWID because the whole table is a handful of rows read by key; there is no second
// access path for a rowid to serve.
table "tod_meta" {
  schema = schema.main
  column "key" {
    null = false
    type = text
  }
  column "value" {
    null = false
    type = text
  }
  column "updated_at" {
    null = false
    type = integer
  }
  primary_key {
    columns = [column.key]
  }
  without_rowid = true
  strict        = true
}

// instance — the singleton. `CHECK (id = 1)` rather than a convention, so a second instance row is
// unrepresentable instead of merely unexpected.
table "instance" {
  schema = schema.main
  column "id" {
    null = false
    type = integer
  }
  column "name" {
    null = false
    type = text
  }
  column "public_url" {
    null = false
    type = text
  }
  column "timezone" {
    null    = false
    type    = text
    default = "UTC"
  }
  column "self_service_circle_creation" {
    null    = false
    type    = integer
    default = 0
  }
  column "created_at" {
    null = false
    type = integer
  }
  column "updated_at" {
    null = false
    type = integer
  }
  primary_key {
    columns = [column.id]
  }
  check "ck_instance_singleton" {
    expr = "id = 1"
  }
  check "ck_instance_self_service_circle_creation" {
    expr = "self_service_circle_creation IN (0, 1)"
  }
  strict = true
}

// identity_provider — the pluggable IdP registry (ADR-0003, ADR-0011).
//
// `verifiable_subject` is a CHECK against `kind`, NOT an operator toggle: everything downstream
// about revocation strength hangs off it, so an operator must not be able to claim a `local`
// provider is verifiable. `client_id` is present exactly when the provider is Discord, because
// ADR-0011 makes the instance a confidential OAuth client and a Discord row with no operator
// application would be a provider that cannot work.
//
// `client_secret` is a core.Secret in Go — never serialised, never logged, `***` in every renderer.
// The database holds it in the clear; that cost is named in ADR-0011.
table "identity_provider" {
  schema = schema.main
  column "id" {
    null = false
    type = text
  }
  column "key" {
    null = false
    type = text
  }
  column "kind" {
    null = false
    type = text
  }
  column "display_name" {
    null = false
    type = text
  }
  column "enabled" {
    null    = false
    type    = integer
    default = 0
  }
  column "verifiable_subject" {
    null = false
    type = integer
  }
  column "issuer" {
    null = true
    type = text
  }
  column "authorization_endpoint" {
    null = true
    type = text
  }
  column "jwks_uri" {
    null = true
    type = text
  }
  column "subject_claim" {
    null = true
    type = text
  }
  column "client_id" {
    null = true
    type = text
  }
  column "client_secret" {
    null = true
    type = text
  }
  column "redirect_uri" {
    null = true
    type = text
  }
  column "token_endpoint" {
    null = true
    type = text
  }
  column "created_at" {
    null = false
    type = integer
  }
  column "updated_at" {
    null = false
    type = integer
  }
  primary_key {
    columns = [column.id]
  }
  index "ux_identity_provider_key" {
    unique  = true
    columns = [column.key]
  }
  // At most one `discord` row and at most one `local` row; any number of `oidc`.
  index "ux_identity_provider_discord" {
    unique  = true
    columns = [column.kind]
    where   = "kind = 'discord'"
  }
  index "ux_identity_provider_local" {
    unique  = true
    columns = [column.kind]
    where   = "kind = 'local'"
  }
  check "ck_identity_provider_kind" {
    expr = local.check_identity_provider_kind
  }
  check "ck_identity_provider_enabled" {
    expr = "enabled IN (0, 1)"
  }
  check "ck_identity_provider_verifiable_subject" {
    expr = "verifiable_subject IN (0, 1)"
  }
  check "ck_identity_provider_local_is_unverifiable" {
    expr = "((kind = 'local') = (verifiable_subject = 0))"
  }
  // Every provider that talks to a third party is an OAuth client of it, and an OAuth client has
  // a `client_id`. `discord` needs one because the audience check compares `application.id`
  // against it; `oidc` needs one because `aud = client_id` IS the audience check, and it is what
  // makes `oidc` structurally immune to the replay hole ADR-0011 had to close for Discord with an
  // extra request. `local` talks to nobody and has none.
  //
  // This started as `((kind = 'discord') = (client_id IS NOT NULL))`, which made an `oidc` row
  // with a client id UNREPRESENTABLE — and therefore made `oidc` unconfigurable, because a
  // verifier with no audience to check is one this codebase refuses to build. Two normative
  // statements in docs/design/04-identity-and-revocation.md §1 disagreed: the column table's
  // CHECK, and the same section's `aud = client_id`. The CHECK was the wrong one.
  check "ck_identity_provider_application_matches_kind" {
    expr = "((kind = 'local') = (client_id IS NULL))"
  }
  strict = true
}

// identity — a (provider, subject) pair, instance-wide. A membership binds to an identity_id,
// never to a bare Discord id; that indirection is why an instance can offer more than one way in.
//
// `blocked_at` is the INSTANCE operator's decision, refused at join and at ticket redemption, so a
// banned identity cannot join a circle whose officers have never heard of them. It is not a
// replacement for revokeMember, which stays the officers' tool for their own circle.
table "identity" {
  schema = schema.main
  column "id" {
    null = false
    type = text
  }
  column "provider_id" {
    null = false
    type = text
  }
  column "subject" {
    null = false
    type = text
  }
  column "display_name" {
    null    = false
    type    = text
    default = ""
  }
  column "blocked_at" {
    null = true
    type = integer
  }
  column "blocked_by_membership_id" {
    null = true
    type = text
  }
  column "block_reason" {
    null = true
    type = text
  }
  column "created_at" {
    null = false
    type = integer
  }
  column "updated_at" {
    null = false
    type = integer
  }
  primary_key {
    columns = [column.id]
  }
  foreign_key "fk_identity_provider" {
    columns     = [column.provider_id]
    ref_columns = [table.identity_provider.column.id]
  }
  foreign_key "fk_identity_blocked_by" {
    columns     = [column.blocked_by_membership_id]
    ref_columns = [table.membership.column.id]
  }
  index "ux_identity_provider_subject" {
    unique  = true
    columns = [column.provider_id, column.subject]
  }
  check "ck_identity_block_is_attributed" {
    expr = "((blocked_at IS NULL) = (blocked_by_membership_id IS NULL))"
  }
  strict = true
}

// identity_link — append-only, officer-asserted equivalence between two VERIFIABLE identities.
//
// The trigger that refuses an unverifiable participant is in the trigger migration: a link to a
// `local` identity would let anyone who can assert a display name inherit another person's
// standing, and that is the entire hole this table would otherwise open.
table "identity_link" {
  schema = schema.main
  column "id" {
    null = false
    type = text
  }
  column "primary_identity_id" {
    null = false
    type = text
  }
  column "linked_identity_id" {
    null = false
    type = text
  }
  column "method" {
    null = false
    type = text
  }
  column "linked_by_membership_id" {
    null = false
    type = text
  }
  column "linked_at" {
    null = false
    type = integer
  }
  primary_key {
    columns = [column.id]
  }
  foreign_key "fk_identity_link_primary" {
    columns     = [column.primary_identity_id]
    ref_columns = [table.identity.column.id]
  }
  foreign_key "fk_identity_link_linked" {
    columns     = [column.linked_identity_id]
    ref_columns = [table.identity.column.id]
  }
  foreign_key "fk_identity_link_linked_by" {
    columns     = [column.linked_by_membership_id]
    ref_columns = [table.membership.column.id]
  }
  index "ux_identity_link_pair" {
    unique  = true
    columns = [column.primary_identity_id, column.linked_identity_id]
  }
  index "ix_identity_link_linked" {
    columns = [column.linked_identity_id]
  }
  check "ck_identity_link_distinct" {
    expr = "primary_identity_id <> linked_identity_id"
  }
  check "ck_identity_link_method" {
    expr = local.check_identity_link_method
  }
  strict = true
}

// instance_grant — who may do what at the INSTANCE level. ADR-0012.
//
// Instance-scoped by construction: the row is about an identity and the whole instance, so a
// `circle_id` would be a false statement rather than a missing one. It is on the canonical §9
// allowlist and in `INSTANCE_SCOPED` in scripts/repo-gates.sh, which one test compares.
//
// APPEND-ONLY. A row is a DECISION — `granted` or `revoked` — not a state, so the row that took a
// permission away is as durable as the one that gave it. Handing somebody the instance's identity
// providers is exactly the event an audit log exists for, and `audit_log.circle_id` is NOT NULL:
// an instance event belongs to no circle, so this table is its own audit record. It is hash-chained
// for the same reason `audit_log` is — the trigger stops a rewrite, and the chain makes a row
// removed by something that bypassed the trigger visible in everything after it.
//
// WHICH DECISION IS CURRENT IS A CONSTRAINT, NOT A SORT. Each row names the row it supersedes;
// `ux_instance_grant_supersedes` and `ux_instance_grant_head` make each (identity, permission)
// pair's decisions one chain with exactly one tail, so no clock skew and no two ULIDs minted in
// the same millisecond by two processes can make two rows both look latest.
//
// `permission` is CHECKed against the instance-realm keys, generated from internal/authz into
// db/enums.hcl. A circle-realm key here would be a grant nothing could ever consult, because a
// circle permission comes from a membership's role.
//
// `decided_by_identity_id` is NULLABLE, and NULL means the operator at the console — `tod-serve
// instance grant` holds the database and precedes every identity on a fresh instance. That is a
// different fact from a person having decided it, so it is a different value rather than a
// self-reference.
table "instance_grant" {
  schema = schema.main
  column "id" {
    null = false
    type = text
  }
  column "identity_id" {
    null = false
    type = text
  }
  column "permission" {
    null = false
    type = text
  }
  column "decision" {
    null = false
    type = text
  }
  column "supersedes_id" {
    null = true
    type = text
  }
  column "decided_by_identity_id" {
    null = true
    type = text
  }
  column "reason" {
    null    = false
    type    = text
    default = ""
  }
  column "prev_hash" {
    null = true
    type = blob
  }
  column "hash" {
    null = false
    type = blob
  }
  column "decided_at" {
    null = false
    type = integer
  }
  primary_key {
    columns = [column.id]
  }
  foreign_key "fk_instance_grant_identity" {
    columns     = [column.identity_id]
    ref_columns = [table.identity.column.id]
  }
  foreign_key "fk_instance_grant_supersedes" {
    columns     = [column.supersedes_id]
    ref_columns = [table.instance_grant.column.id]
  }
  foreign_key "fk_instance_grant_decided_by" {
    columns     = [column.decided_by_identity_id]
    ref_columns = [table.identity.column.id]
  }
  // One row may supersede one row. Without this two revocations could both claim the same grant
  // and the pair would have two tails, which is the ambiguity this design exists to remove.
  index "ux_instance_grant_supersedes" {
    unique  = true
    columns = [column.supersedes_id]
    where   = "supersedes_id IS NOT NULL"
  }
  // And one chain per pair: a second first-decision for the same identity and permission would be
  // a second head, which is the same ambiguity from the other end.
  index "ux_instance_grant_head" {
    unique  = true
    columns = [column.identity_id, column.permission]
    where   = "supersedes_id IS NULL"
  }
  // One row may name one predecessor. Two rows claiming the same `prev_hash` is a FORKED chain,
  // which is a chain that proves nothing: verification would follow one branch and never see the
  // other. `audit_log` leaves this to a single writer; here it is a constraint.
  index "ux_instance_grant_chain" {
    unique  = true
    columns = [column.prev_hash]
    where   = "prev_hash IS NOT NULL"
  }
  // And one row per hash. The tail of the chain is now DERIVED from it — the row whose hash no
  // other row names — so two rows sharing one would give the ledger two tails or none, in the same
  // way `ORDER BY id` gave it the wrong one. The chain covers the row's own id, so a duplicate is
  // either a SHA-256 collision or a hand-written INSERT; this makes the second unrepresentable.
  index "ux_instance_grant_hash" {
    unique  = true
    columns = [column.hash]
  }
  index "ix_instance_grant_identity" {
    columns = [column.identity_id]
  }
  check "ck_instance_grant_permission" {
    expr = local.check_instance_grant_permission
  }
  check "ck_instance_grant_decision" {
    expr = local.check_instance_grant_decision
  }
  // A row cannot supersede itself. The chain is otherwise well-formed and this is the one cycle a
  // single INSERT could create.
  check "ck_instance_grant_supersedes_another_row" {
    expr = "(supersedes_id IS NULL OR supersedes_id <> id)"
  }
  strict = true
}

// auth_flow — one in-flight browser OAuth authorization. Short-lived, capped per caller, swept on
// expiry: an unredeemed flow is litter, not history, and nothing reads it after `expires_at`.
//
// The PKCE verifier stays SERVER-side. A confidential client has a client_secret to bind the
// exchange, so handing the verifier to the browser would buy nothing and leak it into
// sessionStorage.
//
// `circle_id` is NULLABLE and ADVISORY — it selects the OAuth scopes and the guild to check, and
// it is populated only by resolving an invite code, never from caller input. Redemption re-derives
// the circle and is the authority. That is exactly why this table is instance-scoped: a
// circle-scoped table would need `circle_id NOT NULL`, which would be a false statement about a
// row that exists before the caller holds any membership (canonical §9).
table "auth_flow" {
  schema = schema.main
  column "id" {
    null = false
    type = text
  }
  column "state" {
    null = false
    type = text
  }
  column "pkce_verifier" {
    null = false
    type = text
  }
  column "provider_id" {
    null = false
    type = text
  }
  column "invite_code_hash" {
    null = true
    type = blob
  }
  column "circle_id" {
    null = true
    type = text
  }
  column "expires_at" {
    null = false
    type = integer
  }
  column "consumed_at" {
    null = true
    type = integer
  }
  column "created_at" {
    null = false
    type = integer
  }
  column "updated_at" {
    null = false
    type = integer
  }
  primary_key {
    columns = [column.id]
  }
  foreign_key "fk_auth_flow_provider" {
    columns     = [column.provider_id]
    ref_columns = [table.identity_provider.column.id]
  }
  foreign_key "fk_auth_flow_circle" {
    columns     = [column.circle_id]
    ref_columns = [table.circle.column.id]
  }
  index "ux_auth_flow_state" {
    unique  = true
    columns = [column.state]
  }
  index "ix_auth_flow_expires_at" {
    columns = [column.expires_at]
  }
  strict = true
}

// credential_ticket — a verified subject between the OAuth callback and /join or /sessions.
//
// SINGLE-USE with a 120-second TTL, and both are modelled rather than merely checked in Go: the
// CHECK below makes a ticket that outlives its TTL unrepresentable, and the trigger in the trigger
// migration refuses a second consumption, so a replay cannot be written at all. Go still writes
// `WHERE consumed_at IS NULL`; the trigger is what makes forgetting to survivable.
//
// It carries the provider's FACTS — subject, display name, and the gated guild's role ids —
// precisely so the Discord access token can be discarded inside the request that read them. There
// is deliberately no full guild list: one endpoint answers membership and roles for the one guild
// that gates this circle, so the subject's other guilds are never learned.
table "credential_ticket" {
  schema = schema.main
  column "id" {
    null = false
    type = text
  }
  column "ticket_hash" {
    null = false
    type = blob
  }
  column "provider_id" {
    null = false
    type = text
  }
  column "subject" {
    null = false
    type = text
  }
  column "display_name" {
    null    = false
    type    = text
    default = ""
  }
  column "guild_roles_json" {
    null    = false
    type    = text
    default = "{}"
  }
  column "expires_at" {
    null = false
    type = integer
  }
  column "consumed_at" {
    null = true
    type = integer
  }
  column "created_at" {
    null = false
    type = integer
  }
  column "updated_at" {
    null = false
    type = integer
  }
  primary_key {
    columns = [column.id]
  }
  foreign_key "fk_credential_ticket_provider" {
    columns     = [column.provider_id]
    ref_columns = [table.identity_provider.column.id]
  }
  index "ux_credential_ticket_hash" {
    unique  = true
    columns = [column.ticket_hash]
  }
  index "ix_credential_ticket_expires_at" {
    columns = [column.expires_at]
  }
  // 120 seconds in Micros. Written as the arithmetic rather than the constant so that a reader can
  // check it against the ADR without converting units in their head.
  check "ck_credential_ticket_ttl" {
    expr = "expires_at = created_at + 120 * 1000000"
  }
  check "ck_credential_ticket_guild_roles_json" {
    expr = "json_valid(guild_roles_json)"
  }
  strict = true
}

// raid_target — catalogue: a mob's identity, which is a fact about the game and ships embedded.
// Server-agnostic on purpose; the timer is the per-server half and lives in raid_target_timer.
table "raid_target" {
  schema = schema.main
  column "id" {
    null = false
    type = text
  }
  column "name" {
    null = false
    type = text
  }
  column "name_norm" {
    null = false
    type = text
  }
  column "zone" {
    null = false
    type = text
  }
  column "zone_norm" {
    null = false
    type = text
  }
  column "expansion" {
    null = false
    type = text
  }
  column "category" {
    null = false
    type = text
  }
  column "is_quake_target" {
    null    = false
    type    = integer
    default = 1
  }
  column "state" {
    null    = false
    type    = text
    default = "active"
  }
  column "created_at" {
    null = false
    type = integer
  }
  column "updated_at" {
    null = false
    type = integer
  }
  primary_key {
    columns = [column.id]
  }
  index "ux_raid_target_name_norm" {
    unique  = true
    columns = [column.name_norm]
  }
  index "ix_raid_target_zone_norm" {
    columns = [column.zone_norm]
  }
  check "ck_raid_target_expansion" {
    expr = local.check_raid_target_expansion
  }
  check "ck_raid_target_category" {
    expr = local.check_raid_target_category
  }
  check "ck_raid_target_state" {
    expr = local.check_raid_target_state
  }
  check "ck_raid_target_is_quake_target" {
    expr = "is_quake_target IN (0, 1)"
  }
  strict = true
}

// raid_target_alias — `VA`, `Naggy`, `Vox`, `Trak`. Matched on alias_norm, never a collation:
// core SQLite's lower() is ASCII-only and has no NFKC, so normalisation happens in Go.
table "raid_target_alias" {
  schema = schema.main
  column "id" {
    null = false
    type = text
  }
  column "target_id" {
    null = false
    type = text
  }
  column "alias" {
    null = false
    type = text
  }
  column "alias_norm" {
    null = false
    type = text
  }
  column "created_at" {
    null = false
    type = integer
  }
  column "updated_at" {
    null = false
    type = integer
  }
  primary_key {
    columns = [column.id]
  }
  foreign_key "fk_raid_target_alias_target" {
    columns     = [column.target_id]
    ref_columns = [table.raid_target.column.id]
  }
  index "ux_raid_target_alias_norm" {
    unique  = true
    columns = [column.alias_norm]
  }
  index "ix_raid_target_alias_target" {
    columns = [column.target_id]
  }
  strict = true
}

// raid_target_timer — the PER-SERVER respawn window, PK (target_id, server).
//
// Two offsets rather than (base_respawn, variance): an asymmetric window cannot be expressed as a
// base plus a variance without inventing a sign convention, and P99 community data is quoted both
// ways — "7 days ±12h" and "16 to 24 hours" describe the same shape and two officers would enter
// them differently. Two offsets are exactly what the API returns, so nothing has to be converted.
//
// These numbers do NOT ship. They are community-derived, genuinely disputed, and load from the
// separate tod-serve-p99-seed repository; an unseeded instance reports `no_timer` and still records
// ToDs correctly. Canonical §15.
table "raid_target_timer" {
  schema = schema.main
  column "target_id" {
    null = false
    type = text
  }
  column "server" {
    null = false
    type = text
  }
  column "window_kind" {
    null = false
    type = text
  }
  column "window_open_offset_seconds" {
    null = true
    type = integer
  }
  column "window_close_offset_seconds" {
    null = true
    type = integer
  }
  // 900 seconds. A fixed timer would otherwise make `in_window` an instant — the target would flip
  // pre_window -> overdue with no state in between and the UI could never say "spawning now".
  column "fixed_grace_seconds" {
    null    = false
    type    = integer
    default = 900
  }
  column "cluster_epsilon_seconds" {
    null = true
    type = integer
  }
  column "source" {
    null = true
    type = text
  }
  column "note" {
    null    = false
    type    = text
    default = ""
  }
  column "created_at" {
    null = false
    type = integer
  }
  column "updated_at" {
    null = false
    type = integer
  }
  primary_key {
    columns = [column.target_id, column.server]
  }
  foreign_key "fk_raid_target_timer_target" {
    columns     = [column.target_id]
    ref_columns = [table.raid_target.column.id]
  }
  check "ck_raid_target_timer_server" {
    expr = local.check_raid_target_timer_server
  }
  check "ck_raid_target_timer_window_kind" {
    expr = local.check_raid_target_timer_window_kind
  }
  // These four are written so that not one of them can evaluate to NULL. SQLite treats a CHECK
  // whose expression is NULL as SATISFIED, so the obvious spelling of the domain model's three
  // rules -- `(window_kind = 'fixed') = (open = close)` -- accepts a fixed timer with a NULL close
  // offset, an unknown timer that kept a close offset, and any row whose ordering comparison went
  // NULL. Each of those reaches the consensus derivation as a window it cannot read.
  check "ck_raid_target_timer_unknown_has_no_offsets" {
    expr = "((window_kind = 'unknown') = (window_open_offset_seconds IS NULL))"
  }
  // An offset alone is not a window. This is the branch the three domain rules were missing: with
  // it, "both present or both absent" is decided before anything compares them.
  check "ck_raid_target_timer_offsets_are_paired" {
    expr = "((window_open_offset_seconds IS NULL) = (window_close_offset_seconds IS NULL))"
  }
  // Equal offsets IFF fixed, and the IS NOT NULL terms keep it total: `1 AND 0 AND NULL` is 0 in
  // SQLite, so a half-populated row is false here rather than NULL.
  check "ck_raid_target_timer_fixed_is_a_point" {
    expr = "((window_kind = 'fixed') = (window_open_offset_seconds IS NOT NULL AND window_close_offset_seconds IS NOT NULL AND window_open_offset_seconds = window_close_offset_seconds))"
  }
  // Ordering is only a question when both offsets exist; the pairing check above owns the case
  // where one does not, so this one says so explicitly rather than going NULL and passing.
  check "ck_raid_target_timer_window_is_ordered" {
    expr = "window_open_offset_seconds IS NULL OR window_close_offset_seconds IS NULL OR window_close_offset_seconds >= window_open_offset_seconds"
  }
  check "ck_raid_target_timer_fixed_grace_seconds" {
    expr = "fixed_grace_seconds >= 0"
  }
  strict = true
}

// api_token — opaque PATs, bound to a MEMBERSHIP rather than to a service account (ADR-0005), so
// the authz path has exactly one principal kind and the audit always names a responsible human.
//
// `token_prefix` is the 8 loggable characters and is how a leaked token is found; `token_hash` is
// the secret half and never leaves the database. Membership state is re-checked on every request
// rather than cascade-revoking tokens at revocation time — one join, always correct, nothing to
// forget.
table "api_token" {
  schema = schema.main
  column "id" {
    null = false
    type = text
  }
  column "membership_id" {
    null = false
    type = text
  }
  column "token_prefix" {
    null = false
    type = text
  }
  column "token_hash" {
    null = false
    type = blob
  }
  column "name" {
    null    = false
    type    = text
    default = ""
  }
  column "scopes_json" {
    null    = false
    type    = text
    default = "[]"
  }
  column "last_used_at" {
    null = true
    type = integer
  }
  column "expires_at" {
    null = true
    type = integer
  }
  column "revoked_at" {
    null = true
    type = integer
  }
  column "revoked_by_membership_id" {
    null = true
    type = text
  }
  column "created_at" {
    null = false
    type = integer
  }
  column "updated_at" {
    null = false
    type = integer
  }
  primary_key {
    columns = [column.id]
  }
  foreign_key "fk_api_token_membership" {
    columns     = [column.membership_id]
    ref_columns = [table.membership.column.id]
  }
  foreign_key "fk_api_token_revoked_by" {
    columns     = [column.revoked_by_membership_id]
    ref_columns = [table.membership.column.id]
  }
  index "ux_api_token_hash" {
    unique  = true
    columns = [column.token_hash]
  }
  index "ix_api_token_membership" {
    columns = [column.membership_id]
  }
  index "ix_api_token_prefix" {
    columns = [column.token_prefix]
  }
  check "ck_api_token_prefix_length" {
    expr = "length(token_prefix) = 8"
  }
  check "ck_api_token_scopes_json" {
    expr = "json_valid(scopes_json)"
  }
  strict = true
}

// idempotency_record — (principal, key) -> the request that was made and the response that was
// returned. Uniqueness is on the MEMBERSHIP, never the token, so a rotation mid-retry still
// replays (canonical §7).
//
// No `state` enum: a record is in progress until `completed_at` is set, so "in progress" and
// "completed" cannot disagree with the response columns, and there is no fourth enum to keep in
// step with canonical §5.
table "idempotency_record" {
  schema = schema.main
  column "id" {
    null = false
    type = text
  }
  column "principal_membership_id" {
    null = false
    type = text
  }
  column "key" {
    null = false
    type = text
  }
  column "request_hash" {
    null = false
    type = blob
  }
  column "response_status" {
    null = true
    type = integer
  }
  column "response_body" {
    null = true
    type = text
  }
  column "completed_at" {
    null = true
    type = integer
  }
  column "expires_at" {
    null = false
    type = integer
  }
  column "created_at" {
    null = false
    type = integer
  }
  column "updated_at" {
    null = false
    type = integer
  }
  primary_key {
    columns = [column.id]
  }
  foreign_key "fk_idempotency_record_principal" {
    columns     = [column.principal_membership_id]
    ref_columns = [table.membership.column.id]
  }
  index "ux_idempotency_record_principal_key" {
    unique  = true
    columns = [column.principal_membership_id, column.key]
  }
  index "ix_idempotency_record_expires_at" {
    columns = [column.expires_at]
  }
  check "ck_idempotency_record_completed_has_a_response" {
    expr = "((completed_at IS NULL) = (response_status IS NULL))"
  }
  strict = true
}

// event_outbox — append-only, and the home of the ONE global sequence (canonical §4). `event_seq`
// appears in the SSE frame id, in X-TOD-Event-Sequence and in Last-Event-ID.
//
// AUTOINCREMENT rather than max()+1: the point of the sequence is that it never repeats, and a
// rowid reused after a delete would replay one event as another. `id` is still a ULID, because
// every row in this schema has one.
//
// `circle_id` is NULLABLE because instance-level events belong to no circle — which is why this
// table is on the instance-scoped allowlist rather than being circle-scoped.
table "event_outbox" {
  schema = schema.main
  column "event_seq" {
    null           = false
    type           = integer
    auto_increment = true
  }
  column "id" {
    null = false
    type = text
  }
  column "circle_id" {
    null = true
    type = text
  }
  column "kind" {
    null = false
    type = text
  }
  column "payload_json" {
    null    = false
    type    = text
    default = "{}"
  }
  column "created_at" {
    null = false
    type = integer
  }
  primary_key {
    columns = [column.event_seq]
  }
  foreign_key "fk_event_outbox_circle" {
    columns     = [column.circle_id]
    ref_columns = [table.circle.column.id]
  }
  index "ux_event_outbox_id" {
    unique  = true
    columns = [column.id]
  }
  index "ix_event_outbox_circle_seq" {
    columns = [column.circle_id, column.event_seq]
  }
  check "ck_event_outbox_payload_json" {
    expr = "json_valid(payload_json)"
  }
  strict = true
}

// ---------------------------------------------------------------------------------------------
// Circle-scoped tables — every row carries circle_id NOT NULL REFERENCES circle(id).
//
// `circle` itself is the tenant root: its own `id` IS the tenant key, which is the single
// exception the schema test spells out rather than infers.
// ---------------------------------------------------------------------------------------------

// circle — the tenant, pinned to ONE server, immutably (ADR-0009). A guild raiding Blue and Green
// makes two circles. There is no row in this schema where a Blue fact and a Green fact can meet,
// so "a Blue ToD says nothing about Green" is a property of the shape rather than of the queries.
//
// The immutability of `server` is a BEFORE UPDATE trigger, not a CHECK: a CHECK cannot see the old
// row.
//
// `revocation_strength` is DERIVED, never stored — `durable` iff every accepted, instance-enabled
// provider has verifiable_subject = 1. Storing it would let it drift the moment a provider is added
// to the instance.
table "circle" {
  schema = schema.main
  column "id" {
    null = false
    type = text
  }
  column "name" {
    null = false
    type = text
  }
  column "name_norm" {
    null = false
    type = text
  }
  column "description" {
    null    = false
    type    = text
    default = ""
  }
  column "server" {
    null = false
    type = text
  }
  column "timezone" {
    null    = false
    type    = text
    default = "UTC"
  }
  // Default 1, because the honest single reporter is the common case and requiring two would make
  // the product useless. A circle that has been burned can raise it. See consensus §4.
  column "min_reporters_to_supersede" {
    null    = false
    type    = integer
    default = 1
  }
  column "revoke_invalidates_invites" {
    null    = false
    type    = integer
    default = 1
  }
  column "state" {
    null    = false
    type    = text
    default = "active"
  }
  // A TOMBSTONE, not a delete. deleteCircle cannot remove the rows: tod_report, quake_event,
  // invite_redemption and audit_log are append-only by trigger, and with foreign_keys ON a circle
  // that has any of them cannot be deleted at all. The evidence outlives the circle, which is the
  // report log's whole trust argument -- so the circle stops existing to the API and the rows stay
  // exactly where they are.
  column "deleted_at" {
    null = true
    type = integer
  }
  column "created_at" {
    null = false
    type = integer
  }
  column "updated_at" {
    null = false
    type = integer
  }
  primary_key {
    columns = [column.id]
  }
  // Per server, not per instance: one guild's Blue circle and Green circle share a name by design.
  //
  // PARTIAL, so a tombstoned circle stops holding its name. An operator who deleted "Riot Blue" by
  // mistake has to be able to make it again, and a unique index over dead rows would tell them the
  // name is taken by something they cannot see.
  index "ux_circle_name_norm_server" {
    unique  = true
    columns = [column.name_norm, column.server]
    where   = "deleted_at IS NULL"
  }
  check "ck_circle_server" {
    expr = local.check_circle_server
  }
  check "ck_circle_state" {
    expr = local.check_circle_state
  }
  check "ck_circle_min_reporters_to_supersede" {
    expr = "min_reporters_to_supersede >= 1"
  }
  check "ck_circle_revoke_invalidates_invites" {
    expr = "revoke_invalidates_invites IN (0, 1)"
  }
  strict = true
}

// circle_provider — which instance providers this circle accepts, AND the Discord guild gate.
//
// The gate is circle-scoped because the INSTANCE owns the application and the CIRCLE owns the
// gate: two circles on one instance may point at two different guilds.
//
// `discord_required_role_ids_json` empty means "anyone in the guild". Discord has no
// channel-membership API — channel visibility is derived from guild membership plus roles, which
// is how the channel an officer is actually thinking of is gated, so that is what is modelled.
table "circle_provider" {
  schema = schema.main
  column "circle_id" {
    null = false
    type = text
  }
  column "provider_id" {
    null = false
    type = text
  }
  column "discord_guild_id" {
    null = true
    type = text
  }
  column "discord_required_role_ids_json" {
    null    = false
    type    = text
    default = "[]"
  }
  column "created_at" {
    null = false
    type = integer
  }
  column "updated_at" {
    null = false
    type = integer
  }
  primary_key {
    columns = [column.circle_id, column.provider_id]
  }
  foreign_key "fk_circle_provider_circle" {
    columns     = [column.circle_id]
    ref_columns = [table.circle.column.id]
  }
  foreign_key "fk_circle_provider_provider" {
    columns     = [column.provider_id]
    ref_columns = [table.identity_provider.column.id]
  }
  check "ck_circle_provider_required_role_ids_json" {
    expr = "json_valid(discord_required_role_ids_json)"
  }
  strict = true
}

// membership — (circle, identity) -> role and revocation. Mutable, because a role change and a
// revocation are STATE, not events.
//
// ux_membership_identity is THE ENTIRE REVOCATION MECHANISM. A revoked person redeeming a fresh
// invite hits the existing row, sees revoked_at IS NOT NULL and gets 403 membership_revoked. There
// is never a second row, and there is no delete-membership operation at all — reinstatement is an
// explicit, audited POST .../reinstate.
table "membership" {
  schema = schema.main
  column "id" {
    null = false
    type = text
  }
  column "circle_id" {
    null = false
    type = text
  }
  // NULL for a service membership: a bot has no identity, it has an owner.
  column "identity_id" {
    null = true
    type = text
  }
  column "kind" {
    null = false
    type = text
  }
  column "owner_membership_id" {
    null = true
    type = text
  }
  column "display_name" {
    null = false
    type = text
  }
  column "display_name_norm" {
    null = false
    type = text
  }
  column "role" {
    null = false
    type = text
  }
  column "admitted_by_invite_id" {
    null = true
    type = text
  }
  column "joined_at" {
    null = false
    type = integer
  }
  column "revoked_at" {
    null = true
    type = integer
  }
  column "revoked_by_membership_id" {
    null = true
    type = text
  }
  column "revoke_reason" {
    null = true
    type = text
  }
  column "created_at" {
    null = false
    type = integer
  }
  column "updated_at" {
    null = false
    type = integer
  }
  primary_key {
    columns = [column.id]
  }
  foreign_key "fk_membership_circle" {
    columns     = [column.circle_id]
    ref_columns = [table.circle.column.id]
  }
  foreign_key "fk_membership_identity" {
    columns     = [column.identity_id]
    ref_columns = [table.identity.column.id]
  }
  foreign_key "fk_membership_owner" {
    columns     = [column.circle_id, column.owner_membership_id]
    ref_columns = [table.membership.column.circle_id, table.membership.column.id]
  }
  foreign_key "fk_membership_invite" {
    columns     = [column.circle_id, column.admitted_by_invite_id]
    ref_columns = [table.invite.column.circle_id, table.invite.column.id]
  }
  foreign_key "fk_membership_revoked_by" {
    columns     = [column.circle_id, column.revoked_by_membership_id]
    ref_columns = [table.membership.column.circle_id, table.membership.column.id]
  }
  index "ux_membership_circle_id" {
    unique  = true
    columns = [column.circle_id, column.id]
  }
  index "ux_membership_identity" {
    unique  = true
    columns = [column.circle_id, column.identity_id]
    where   = "identity_id IS NOT NULL"
  }
  // The callback resolves a verified identity's own memberships, which is a lookup by identity
  // alone and cannot use the composite index above.
  index "ix_membership_identity_id" {
    columns = [column.identity_id]
  }
  index "ix_membership_circle_display_name_norm" {
    columns = [column.circle_id, column.display_name_norm]
  }
  check "ck_membership_kind" {
    expr = local.check_membership_kind
  }
  check "ck_membership_role" {
    expr = local.check_membership_role
  }
  check "ck_membership_human_has_an_identity" {
    expr = "((kind = 'human') = (identity_id IS NOT NULL))"
  }
  check "ck_membership_service_has_an_owner" {
    expr = "((kind = 'service') = (owner_membership_id IS NOT NULL))"
  }
  check "ck_membership_revocation_is_attributed" {
    expr = "((revoked_at IS NULL) = (revoked_by_membership_id IS NULL))"
  }
  strict = true
}

// invite — codes are INSTANCE-unique, so POST /join needs no circle id: one paste.
//
// Lookup is by code_hash on the unique index, NEVER by prefix — a prefix lookup is a brute-force
// oracle. `code_prefix` is display only.
//
// `expires_at` is NOT NULL. There are no eternal invites. `CHECK (role <> 'owner')` because an
// invite may not hand over the circle, and `CHECK (uses <= max_uses)` because exhaustion is a
// property of the row rather than a race two requests can lose.
table "invite" {
  schema = schema.main
  column "id" {
    null = false
    type = text
  }
  column "circle_id" {
    null = false
    type = text
  }
  column "code_hash" {
    null = false
    type = blob
  }
  column "code_prefix" {
    null = false
    type = text
  }
  column "role" {
    null = false
    type = text
  }
  column "max_uses" {
    null = false
    type = integer
  }
  column "uses" {
    null    = false
    type    = integer
    default = 0
  }
  column "expires_at" {
    null = false
    type = integer
  }
  column "revoked_at" {
    null = true
    type = integer
  }
  column "created_by_membership_id" {
    null = false
    type = text
  }
  column "minted_by_kind" {
    null = false
    type = text
  }
  column "note" {
    null    = false
    type    = text
    default = ""
  }
  column "created_at" {
    null = false
    type = integer
  }
  column "updated_at" {
    null = false
    type = integer
  }
  primary_key {
    columns = [column.id]
  }
  foreign_key "fk_invite_circle" {
    columns     = [column.circle_id]
    ref_columns = [table.circle.column.id]
  }
  foreign_key "fk_invite_created_by" {
    columns     = [column.circle_id, column.created_by_membership_id]
    ref_columns = [table.membership.column.circle_id, table.membership.column.id]
  }
  index "ux_invite_code_hash" {
    unique  = true
    columns = [column.code_hash]
  }
  index "ux_invite_circle_id" {
    unique  = true
    columns = [column.circle_id, column.id]
  }
  check "ck_invite_role" {
    expr = local.check_invite_role
  }
  check "ck_invite_role_is_not_owner" {
    expr = "role <> 'owner'"
  }
  check "ck_invite_minted_by_kind" {
    expr = local.check_invite_minted_by_kind
  }
  check "ck_invite_max_uses" {
    expr = "max_uses >= 1"
  }
  check "ck_invite_uses_within_max" {
    expr = "uses >= 0 AND uses <= max_uses"
  }
  strict = true
}

// invite_redemption — append-only: who redeemed what, when.
table "invite_redemption" {
  schema = schema.main
  column "id" {
    null = false
    type = text
  }
  column "circle_id" {
    null = false
    type = text
  }
  column "invite_id" {
    null = false
    type = text
  }
  column "membership_id" {
    null = false
    type = text
  }
  column "identity_id" {
    null = true
    type = text
  }
  column "created_at" {
    null = false
    type = integer
  }
  primary_key {
    columns = [column.id]
  }
  foreign_key "fk_invite_redemption_circle" {
    columns     = [column.circle_id]
    ref_columns = [table.circle.column.id]
  }
  foreign_key "fk_invite_redemption_invite" {
    columns     = [column.circle_id, column.invite_id]
    ref_columns = [table.invite.column.circle_id, table.invite.column.id]
  }
  foreign_key "fk_invite_redemption_membership" {
    columns     = [column.circle_id, column.membership_id]
    ref_columns = [table.membership.column.circle_id, table.membership.column.id]
  }
  foreign_key "fk_invite_redemption_identity" {
    columns     = [column.identity_id]
    ref_columns = [table.identity.column.id]
  }
  index "ix_invite_redemption_invite" {
    columns = [column.invite_id]
  }
  index "ix_invite_redemption_circle_created" {
    columns = [column.circle_id, column.created_at]
  }
  strict = true
}

// tod_report — APPEND-ONLY, the core log. Never updated, never deleted, in Go, in SQL or in a
// migration: corrections are new rows, and a retraction is a row with retracts_report_id set.
//
// `died_at` is GAME truth and may be backdated. `reported_at` is SYSTEM truth and never is. The
// two must never be conflated, which is why this table has both and no created_at — a third name
// for the second one would be exactly the confusion canonical §1 forbids.
//
// The natural key is a second line of defence behind Idempotency-Key: the same reporter cannot
// lodge the same kill twice even if the header is botched. A correction by the same reporter has a
// different died_at, so it is unaffected.
table "tod_report" {
  schema = schema.main
  column "id" {
    null = false
    type = text
  }
  column "circle_id" {
    null = false
    type = text
  }
  column "target_id" {
    null = false
    type = text
  }
  column "kind" {
    null = false
    type = text
  }
  column "died_at" {
    null = false
    type = integer
  }
  column "reported_at" {
    null = false
    type = integer
  }
  // NOT NULL: the log names the reporter even after their membership is revoked, because a revoked
  // member's reports still count and their retractions still apply.
  column "reporter_membership_id" {
    null = false
    type = text
  }
  column "source" {
    null = false
    type = text
  }
  column "self_confidence" {
    null = false
    type = text
  }
  column "source_line" {
    null = true
    type = text
  }
  column "source_character" {
    null = true
    type = text
  }
  column "log_character" {
    null = true
    type = text
  }
  column "killed_by_guild" {
    null = true
    type = text
  }
  column "client_clock_offset_seconds" {
    null = true
    type = integer
  }
  column "retracts_report_id" {
    null = true
    type = text
  }
  primary_key {
    columns = [column.id]
  }
  foreign_key "fk_tod_report_circle" {
    columns     = [column.circle_id]
    ref_columns = [table.circle.column.id]
  }
  foreign_key "fk_tod_report_target" {
    columns     = [column.target_id]
    ref_columns = [table.raid_target.column.id]
  }
  foreign_key "fk_tod_report_reporter" {
    columns     = [column.circle_id, column.reporter_membership_id]
    ref_columns = [table.membership.column.circle_id, table.membership.column.id]
  }
  foreign_key "fk_tod_report_retracts" {
    columns     = [column.circle_id, column.retracts_report_id]
    ref_columns = [table.tod_report.column.circle_id, table.tod_report.column.id]
  }
  index "ux_tod_report_circle_id" {
    unique  = true
    columns = [column.circle_id, column.id]
  }
  index "ux_tod_report_natural" {
    unique  = true
    columns = [column.circle_id, column.target_id, column.reporter_membership_id, column.died_at]
    where   = "kind = 'kill'"
  }
  // A report is retracted at most once; a retraction of a retraction is not supported.
  index "ux_tod_report_retracts" {
    unique  = true
    columns = [column.retracts_report_id]
    where   = "retracts_report_id IS NOT NULL"
  }
  index "ix_tod_report_circle_target_died" {
    columns = [column.circle_id, column.target_id, column.died_at]
  }
  index "ix_tod_report_circle_reporter" {
    columns = [column.circle_id, column.reporter_membership_id]
  }
  check "ck_tod_report_kind" {
    expr = local.check_tod_report_kind
  }
  check "ck_tod_report_source" {
    expr = local.check_tod_report_source
  }
  check "ck_tod_report_self_confidence" {
    expr = local.check_tod_report_self_confidence
  }
  check "ck_tod_report_retraction_names_a_report" {
    expr = "((kind = 'retraction') = (retracts_report_id IS NOT NULL))"
  }
  // Micros, with 120 seconds of clock-skew tolerance. A died_at in the future is the only hard
  // rejection on a time, because it is impossible independent of any derivation.
  check "ck_tod_report_died_at_not_in_future" {
    expr = "died_at <= reported_at + 120 * 1000000"
  }
  strict = true
}

// quake_event — APPEND-ONLY. An earthquake repops every raid target on the server at once.
//
// Modelling that as N kill reports would be a lie — nobody observed N kills — and it would corrupt
// every confidence figure on the board. Without this table every window in the circle is wrong for
// a week after a quake, and wrong CONFIDENTLY, which is the failure mode this project is built
// against.
table "quake_event" {
  schema = schema.main
  column "id" {
    null = false
    type = text
  }
  column "circle_id" {
    null = false
    type = text
  }
  column "occurred_at" {
    null = false
    type = integer
  }
  column "reported_at" {
    null = false
    type = integer
  }
  column "reported_by_membership_id" {
    null = false
    type = text
  }
  column "source" {
    null = false
    type = text
  }
  column "note" {
    null    = false
    type    = text
    default = ""
  }
  primary_key {
    columns = [column.id]
  }
  foreign_key "fk_quake_event_circle" {
    columns     = [column.circle_id]
    ref_columns = [table.circle.column.id]
  }
  foreign_key "fk_quake_event_reporter" {
    columns     = [column.circle_id, column.reported_by_membership_id]
    ref_columns = [table.membership.column.circle_id, table.membership.column.id]
  }
  index "ix_quake_event_circle_occurred" {
    columns = [column.circle_id, column.occurred_at]
  }
  check "ck_quake_event_source" {
    expr = local.check_quake_event_source
  }
  // The same rule and the same tolerance tod_report carries: a quake in the future is impossible
  // independent of any derivation.
  check "ck_quake_event_occurred_at_not_in_future" {
    expr = "occurred_at <= reported_at + 120 * 1000000"
  }
  strict = true
}

// circle_timer_override — "our guild has tracked VS for two years and the wiki is wrong" is a real
// thing an officer says, and these numbers are genuinely disputed. Resolution order is circle
// override -> catalogue timer -> unknown.
//
// No `server` column: the circle is pinned to one server, so the override is per (circle, target).
table "circle_timer_override" {
  schema = schema.main
  column "circle_id" {
    null = false
    type = text
  }
  column "target_id" {
    null = false
    type = text
  }
  column "window_kind" {
    null = false
    type = text
  }
  column "window_open_offset_seconds" {
    null = true
    type = integer
  }
  column "window_close_offset_seconds" {
    null = true
    type = integer
  }
  column "fixed_grace_seconds" {
    null    = false
    type    = integer
    default = 900
  }
  column "cluster_epsilon_seconds" {
    null = true
    type = integer
  }
  column "note" {
    null    = false
    type    = text
    default = ""
  }
  column "created_by_membership_id" {
    null = false
    type = text
  }
  column "created_at" {
    null = false
    type = integer
  }
  column "updated_at" {
    null = false
    type = integer
  }
  primary_key {
    columns = [column.circle_id, column.target_id]
  }
  foreign_key "fk_circle_timer_override_circle" {
    columns     = [column.circle_id]
    ref_columns = [table.circle.column.id]
  }
  foreign_key "fk_circle_timer_override_target" {
    columns     = [column.target_id]
    ref_columns = [table.raid_target.column.id]
  }
  foreign_key "fk_circle_timer_override_created_by" {
    columns     = [column.circle_id, column.created_by_membership_id]
    ref_columns = [table.membership.column.circle_id, table.membership.column.id]
  }
  check "ck_circle_timer_override_window_kind" {
    expr = local.check_circle_timer_override_window_kind
  }
  // These four are written so that not one of them can evaluate to NULL. SQLite treats a CHECK
  // whose expression is NULL as SATISFIED, so the obvious spelling of the domain model's three
  // rules -- `(window_kind = 'fixed') = (open = close)` -- accepts a fixed timer with a NULL close
  // offset, an unknown timer that kept a close offset, and any row whose ordering comparison went
  // NULL. Each of those reaches the consensus derivation as a window it cannot read.
  check "ck_circle_timer_override_unknown_has_no_offsets" {
    expr = "((window_kind = 'unknown') = (window_open_offset_seconds IS NULL))"
  }
  // An offset alone is not a window. This is the branch the three domain rules were missing: with
  // it, "both present or both absent" is decided before anything compares them.
  check "ck_circle_timer_override_offsets_are_paired" {
    expr = "((window_open_offset_seconds IS NULL) = (window_close_offset_seconds IS NULL))"
  }
  // Equal offsets IFF fixed, and the IS NOT NULL terms keep it total: `1 AND 0 AND NULL` is 0 in
  // SQLite, so a half-populated row is false here rather than NULL.
  check "ck_circle_timer_override_fixed_is_a_point" {
    expr = "((window_kind = 'fixed') = (window_open_offset_seconds IS NOT NULL AND window_close_offset_seconds IS NOT NULL AND window_open_offset_seconds = window_close_offset_seconds))"
  }
  // Ordering is only a question when both offsets exist; the pairing check above owns the case
  // where one does not, so this one says so explicitly rather than going NULL and passing.
  check "ck_circle_timer_override_window_is_ordered" {
    expr = "window_open_offset_seconds IS NULL OR window_close_offset_seconds IS NULL OR window_close_offset_seconds >= window_open_offset_seconds"
  }
  check "ck_circle_timer_override_fixed_grace_seconds" {
    expr = "fixed_grace_seconds >= 0"
  }
  strict = true
}

// target_state_cache — MATERIALISED CONSENSUS, and NEVER AUTHORITY.
//
// Droppable: invalidated on any insert into tod_report or quake_event for that (circle, target)
// and on any timer change, rebuilt lazily on read-miss and wholly by `tod-serve rebuild-states`. A
// nightly job recomputes every state from the reports and diffs; THE RECOMPUTATION WINS and an
// alert fires.
//
// If you find yourself reading this table to make a decision the derivation should make, that is
// the bug.
table "target_state_cache" {
  schema = schema.main
  column "circle_id" {
    null = false
    type = text
  }
  column "target_id" {
    null = false
    type = text
  }
  column "computed_at" {
    null = false
    type = integer
  }
  column "latest_report_id" {
    null = true
    type = text
  }
  column "report_count" {
    null    = false
    type    = integer
    default = 0
  }
  column "status" {
    null = false
    type = text
  }
  column "confidence" {
    null = false
    type = text
  }
  column "contested" {
    null    = false
    type    = integer
    default = 0
  }
  column "contest_reason" {
    null = true
    type = text
  }
  column "change_reason" {
    null = true
    type = text
  }
  column "died_at" {
    null = true
    type = integer
  }
  column "window_open_at" {
    null = true
    type = integer
  }
  column "window_close_at" {
    null = true
    type = integer
  }
  column "spawn_at" {
    null = true
    type = integer
  }
  column "distinct_reporter_count" {
    null    = false
    type    = integer
    default = 0
  }
  column "log_line_count" {
    null    = false
    type    = integer
    default = 0
  }
  column "spread_seconds" {
    null = true
    type = integer
  }
  column "revoked_reporter_count" {
    null    = false
    type    = integer
    default = 0
  }
  column "created_at" {
    null = false
    type = integer
  }
  column "updated_at" {
    null = false
    type = integer
  }
  primary_key {
    columns = [column.circle_id, column.target_id]
  }
  foreign_key "fk_target_state_cache_circle" {
    columns     = [column.circle_id]
    ref_columns = [table.circle.column.id]
  }
  foreign_key "fk_target_state_cache_target" {
    columns     = [column.target_id]
    ref_columns = [table.raid_target.column.id]
  }
  foreign_key "fk_target_state_cache_latest_report" {
    columns     = [column.circle_id, column.latest_report_id]
    ref_columns = [table.tod_report.column.circle_id, table.tod_report.column.id]
  }
  index "ix_target_state_cache_circle_status" {
    columns = [column.circle_id, column.status]
  }
  index "ix_target_state_cache_circle_window_open" {
    columns = [column.circle_id, column.window_open_at]
  }
  check "ck_target_state_cache_status" {
    expr = local.check_target_state_cache_status
  }
  check "ck_target_state_cache_confidence" {
    expr = local.check_target_state_cache_confidence
  }
  check "ck_target_state_cache_contest_reason" {
    expr = local.check_target_state_cache_contest_reason
  }
  check "ck_target_state_cache_change_reason" {
    expr = local.check_target_state_cache_change_reason
  }
  check "ck_target_state_cache_contested" {
    expr = "contested IN (0, 1)"
  }
  // Disagreement is surfaced, never resolved silently — so a contested state always says why.
  check "ck_target_state_cache_contested_has_a_reason" {
    expr = "((contested = 1) = (contest_reason IS NOT NULL))"
  }
  strict = true
}

// audit_log — APPEND-ONLY, hash-chained.
//
// `circle_id` is NOT NULL because this table is not on the instance-scoped allowlist. The domain
// model's sentence "circle rows carry circle_id; instance rows do not" cannot be honoured at the
// same time as canonical §9, and canonical §9 is the tie-breaker; instance-realm audit rows need
// either their own table or an allowlist decision, and that is a reviewed change rather than one
// this file makes quietly.
table "audit_log" {
  schema = schema.main
  column "id" {
    null = false
    type = text
  }
  column "circle_id" {
    null = false
    type = text
  }
  column "actor_membership_id" {
    null = true
    type = text
  }
  column "action" {
    null = false
    type = text
  }
  column "entity_type" {
    null = false
    type = text
  }
  column "entity_id" {
    null = true
    type = text
  }
  column "detail_json" {
    null    = false
    type    = text
    default = "{}"
  }
  column "prev_hash" {
    null = true
    type = blob
  }
  column "hash" {
    null = false
    type = blob
  }
  column "created_at" {
    null = false
    type = integer
  }
  primary_key {
    columns = [column.id]
  }
  foreign_key "fk_audit_log_circle" {
    columns     = [column.circle_id]
    ref_columns = [table.circle.column.id]
  }
  foreign_key "fk_audit_log_actor" {
    columns     = [column.circle_id, column.actor_membership_id]
    ref_columns = [table.membership.column.circle_id, table.membership.column.id]
  }
  index "ux_audit_log_hash" {
    unique  = true
    columns = [column.hash]
  }
  index "ix_audit_log_circle_id" {
    columns = [column.circle_id, column.id]
  }
  check "ck_audit_log_detail_json" {
    expr = "json_valid(detail_json)"
  }
  strict = true
}
