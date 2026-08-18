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

-- disable the enforcement of foreign-keys constraints
PRAGMA foreign_keys = off;
-- create "new_identity_provider" table
CREATE TABLE new_identity_provider (id text NOT NULL, key text NOT NULL, kind text NOT NULL, display_name text NOT NULL, enabled integer NOT NULL DEFAULT 0, verifiable_subject integer NOT NULL, issuer text, authorization_endpoint text, jwks_uri text, subject_claim text, client_id text, client_secret text, redirect_uri text, token_endpoint text, created_at integer NOT NULL, updated_at integer NOT NULL, PRIMARY KEY (id), CONSTRAINT ck_identity_provider_kind CHECK (kind IN ('discord', 'oidc', 'local')), CONSTRAINT ck_identity_provider_enabled CHECK (enabled IN (0, 1)), CONSTRAINT ck_identity_provider_verifiable_subject CHECK (verifiable_subject IN (0, 1)), CONSTRAINT ck_identity_provider_local_is_unverifiable CHECK ((kind = 'local') = (verifiable_subject = 0)), CONSTRAINT ck_identity_provider_application_matches_kind CHECK ((kind = 'local') = (client_id IS NULL))) STRICT;
-- copy rows from old table "identity_provider" to new temporary table "new_identity_provider"
INSERT INTO new_identity_provider (id, key, kind, display_name, enabled, verifiable_subject, issuer, authorization_endpoint, jwks_uri, subject_claim, client_id, client_secret, redirect_uri, token_endpoint, created_at, updated_at) SELECT id, key, kind, display_name, enabled, verifiable_subject, issuer, authorization_endpoint, jwks_uri, subject_claim, client_id, client_secret, redirect_uri, token_endpoint, created_at, updated_at FROM identity_provider;
-- drop "identity_provider" table after copying rows
DROP TABLE identity_provider;
-- rename temporary table "new_identity_provider" to "identity_provider"
ALTER TABLE new_identity_provider RENAME TO identity_provider;
-- create index "ux_identity_provider_key" to table: "identity_provider"
CREATE UNIQUE INDEX ux_identity_provider_key ON identity_provider (key);
-- create index "ux_identity_provider_discord" to table: "identity_provider"
CREATE UNIQUE INDEX ux_identity_provider_discord ON identity_provider (kind) WHERE kind = 'discord';
-- create index "ux_identity_provider_local" to table: "identity_provider"
CREATE UNIQUE INDEX ux_identity_provider_local ON identity_provider (kind) WHERE kind = 'local';
-- enable back the enforcement of foreign-keys constraints
PRAGMA foreign_keys = on;

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

-- +goose Down
-- +goose StatementBegin
SELECT RAISE(ABORT, 'migrations are forward-only; roll forward with a new migration');
-- +goose StatementEnd
