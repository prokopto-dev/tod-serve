-- +goose Up
-- Correcting ck_identity_provider_discord_has_application, which made `oidc` unconfigurable:
-- `aud = client_id` IS the OIDC audience check, and the old predicate made an `oidc` row with a
-- client id unrepresentable. See db/schema.hcl and docs/design/04-identity-and-revocation.md 1.
--
-- HAND-EDITED, and this is the one thing that was added to what Atlas wrote.
--
-- A CHECK cannot be altered in SQLite, so this is a table rebuild -- and AGENTS.md names table
-- rebuilds as the thing that silently drops triggers. Here it is worse than silent: it FAILS.
-- `trg_identity_link_requires_verifiable_participants` lives on `identity_link` and its body reads
-- `identity_provider`, and `ALTER TABLE ... RENAME TO` reparses every trigger in the schema so it
-- can rewrite references to the renamed table. At that moment `identity_provider` has been dropped
-- and the reparse fails with `no such table: main.identity_provider`.
--
-- So the trigger is dropped before the rebuild and recreated after it, VERBATIM from
-- 000002_invariant_triggers.sql. Atlas Community cannot see triggers, so it could not have written
-- this and `make gen`'s re-diff cannot check it; TestIdentityLink_LocalProvider_Rejected and
-- TestAppendOnly_TriggersFire_AfterAllMigrations are what prove it came back.

-- +goose StatementBegin
DROP TRIGGER IF EXISTS trg_identity_link_requires_verifiable_participants;
-- +goose StatementEnd

-- This migration is NON-TRANSACTIONAL, which is unusual here and is not a preference. SQLite's own
-- 12-step ALTER TABLE procedure requires `PRAGMA foreign_keys = OFF`, and that pragma is a NO-OP
-- while a transaction is open -- so under goose's default transaction it silently does nothing,
-- enforcement stays on, and `DROP TABLE identity_provider` (which performs an implicit DELETE FROM
-- when foreign keys are enforced) fails on any database holding an `identity`, `auth_flow` or
-- `credential_ticket` row. That is every populated deployment, and no test here caught it because
-- they all migrate an EMPTY database.
--
-- Two transactional alternatives were tried and neither works:
--
--   * `PRAGMA defer_foreign_keys = ON` DOES take effect inside a transaction, and is still not
--     enough: it defers a COUNTER rather than a re-evaluation. The implicit DELETE increments it
--     once per orphaned child and re-creating the parent never decrements it, so the COMMIT fails
--     anyway.
--   * Renaming the old table out of the way instead of dropping it does not help either: with
--     foreign keys enforced, `ALTER TABLE ... RENAME TO` rewrites every child's REFERENCES clause
--     to follow the rename, so the children point at the husk and dropping it orphans them. That
--     rewrite is conditional on foreign keys being ON, not on `legacy_alter_table`.
--
-- Atomicity is bought back with an EXPLICIT transaction inside the migration, which is legal
-- precisely because goose is not opening one: the pragma sits outside it, the schema change sits
-- inside it. A failure rolls the whole rebuild back rather than leaving a half-built table in a
-- forward-only repository.
--
-- The single connection this depends on is DB.Migrate's doing: it caps the pool to one for the
-- duration, because a per-connection pragma set on whichever connection the pool felt like is the
-- same bug wearing a different hat.

-- +goose NO TRANSACTION

PRAGMA foreign_keys = OFF;

BEGIN IMMEDIATE;

-- The trigger has to go before the rebuild and come back after it. It lives on `identity_link` but
-- its body READS `identity_provider`, and `ALTER TABLE ... RENAME TO` reparses every trigger in the
-- schema so it can rewrite references to the renamed table -- at a moment when `identity_provider`
-- has been dropped. Without this the migration fails with `no such table: main.identity_provider`.
DROP TRIGGER IF EXISTS trg_identity_link_requires_verifiable_participants;

CREATE TABLE new_identity_provider (id text NOT NULL, key text NOT NULL, kind text NOT NULL, display_name text NOT NULL, enabled integer NOT NULL DEFAULT 0, verifiable_subject integer NOT NULL, issuer text, authorization_endpoint text, jwks_uri text, subject_claim text, client_id text, client_secret text, redirect_uri text, token_endpoint text, created_at integer NOT NULL, updated_at integer NOT NULL, PRIMARY KEY (id), CONSTRAINT ck_identity_provider_kind CHECK (kind IN ('discord', 'oidc', 'local')), CONSTRAINT ck_identity_provider_enabled CHECK (enabled IN (0, 1)), CONSTRAINT ck_identity_provider_verifiable_subject CHECK (verifiable_subject IN (0, 1)), CONSTRAINT ck_identity_provider_local_is_unverifiable CHECK ((kind = 'local') = (verifiable_subject = 0)), CONSTRAINT ck_identity_provider_application_matches_kind CHECK ((kind = 'local') = (client_id IS NULL))) STRICT;

-- An `oidc` row written under version 2 was FORCED to have a NULL client_id -- that is the bug this
-- migration corrects -- and it fails the new CHECK right here. That is deliberate and it is the
-- honest outcome: such a row could never verify anything, because `aud = client_id` IS the audience
-- check and there was nothing to compare against. This migration will not invent a client id and
-- will not drop an operator's configuration. It aborts naming
-- `ck_identity_provider_application_matches_kind`, the transaction rolls back, and the fix is to
-- set the client id that provider always needed.
INSERT INTO new_identity_provider (id, key, kind, display_name, enabled, verifiable_subject, issuer, authorization_endpoint, jwks_uri, subject_claim, client_id, client_secret, redirect_uri, token_endpoint, created_at, updated_at) SELECT id, key, kind, display_name, enabled, verifiable_subject, issuer, authorization_endpoint, jwks_uri, subject_claim, client_id, client_secret, redirect_uri, token_endpoint, created_at, updated_at FROM identity_provider;

DROP TABLE identity_provider;

ALTER TABLE new_identity_provider RENAME TO identity_provider;

CREATE UNIQUE INDEX ux_identity_provider_key ON identity_provider (key);
CREATE UNIQUE INDEX ux_identity_provider_discord ON identity_provider (kind) WHERE kind = 'discord';
CREATE UNIQUE INDEX ux_identity_provider_local ON identity_provider (kind) WHERE kind = 'local';

-- The trigger, back. Verbatim from 000002_invariant_triggers.sql: a link participant must have
-- verifiable_subject = 1, counted rather than checked per side so a participant whose identity row
-- does not exist at all is refused by the same test.
-- +goose StatementBegin
CREATE TRIGGER trg_identity_link_requires_verifiable_participants
BEFORE INSERT ON identity_link
WHEN (
  SELECT COUNT(*) FROM identity i
  JOIN identity_provider p ON p.id = i.provider_id
  WHERE i.id IN (NEW.primary_identity_id, NEW.linked_identity_id)
    AND p.verifiable_subject = 1
) <> 2 BEGIN
  SELECT RAISE(ABORT, 'identity_link participants must both have verifiable_subject = 1');
END;
-- +goose StatementEnd

COMMIT;

PRAGMA foreign_keys = ON;

-- +goose Down
-- +goose StatementBegin
SELECT RAISE(ABORT, 'migrations are forward-only; roll forward with a new migration');
-- +goose StatementEnd
