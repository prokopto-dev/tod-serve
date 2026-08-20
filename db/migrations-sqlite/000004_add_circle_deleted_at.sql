-- +goose Up
-- circle.deleted_at: a TOMBSTONE for deleteCircle, and a PARTIAL unique index so a deleted circle
-- stops holding its name. See db/schema.hcl and docs/design/02-api-design.md.
--
-- HAND-EDITED throughout, for the two reasons 000003 records at length and this file inherits
-- wholesale. `make gen` re-runs the diff and proves this still replays to exactly db/schema.hcl,
-- which is what makes hand-editing it safe to trust.
--
--   1. NON-TRANSACTIONAL. SQLite's 12-step ALTER TABLE procedure needs `PRAGMA foreign_keys = OFF`,
--      and that pragma is a NO-OP while a transaction is open -- so under goose's default
--      transaction it silently does nothing, enforcement stays on, and `DROP TABLE circle`
--      (an implicit DELETE FROM while foreign keys are enforced) fails on any database holding a
--      membership, an invite, a report or an audit row. That is every populated deployment, and no
--      test here would catch it because they all migrate an EMPTY database. Atomicity is bought
--      back with an EXPLICIT transaction: the pragma sits outside it, the schema change inside.
--
--   2. THE TRIGGER. Atlas Community cannot see triggers, so the diff it wrote does not mention
--      `trg_circle_server_is_immutable` -- and a table rebuild drops it silently. Losing it is not
--      cosmetic: `circle.server` is immutable because ADR-0009 makes a Blue fact and a Green fact
--      unable to meet, and the edge's 422 is the second copy of that rule, not the first. A
--      database whose trigger had quietly gone would enforce it only for callers who came through
--      the API.
--
--      It is dropped INSIDE the explicit transaction, and that placement is the point: this
--      migration is non-transactional, so a drop above BEGIN would auto-commit on its own, and a
--      later failure would roll the table back while leaving the trigger gone.
--
-- TestAppendOnly_TriggersFire_AfterAllMigrations and
-- TestCircleServer_Update_IsRefusedByTheDatabase are what notice if either half is wrong: both
-- assert an ABORT rather than a row in sqlite_master, because a rebuild leaves the catalogue
-- looking right.

-- +goose NO TRANSACTION

PRAGMA foreign_keys = OFF;

BEGIN IMMEDIATE;

DROP TRIGGER IF EXISTS trg_circle_server_is_immutable;

CREATE TABLE new_circle (id text NOT NULL, name text NOT NULL, name_norm text NOT NULL, description text NOT NULL DEFAULT '', server text NOT NULL, timezone text NOT NULL DEFAULT 'UTC', min_reporters_to_supersede integer NOT NULL DEFAULT 1, revoke_invalidates_invites integer NOT NULL DEFAULT 1, state text NOT NULL DEFAULT 'active', deleted_at integer, created_at integer NOT NULL, updated_at integer NOT NULL, PRIMARY KEY (id), CONSTRAINT ck_circle_server CHECK (server IN ('blue', 'green', 'red')), CONSTRAINT ck_circle_state CHECK (state IN ('active', 'archived')), CONSTRAINT ck_circle_min_reporters_to_supersede CHECK (min_reporters_to_supersede >= 1), CONSTRAINT ck_circle_revoke_invalidates_invites CHECK (revoke_invalidates_invites IN (0, 1))) STRICT;

-- Every existing circle is live: `deleted_at` is absent from the column list and lands NULL, which
-- is what "not deleted" is. Spelling it as an explicit NULL would read as a decision about each
-- row rather than as the default it is.
INSERT INTO new_circle (id, name, name_norm, description, server, timezone, min_reporters_to_supersede, revoke_invalidates_invites, state, created_at, updated_at) SELECT id, name, name_norm, description, server, timezone, min_reporters_to_supersede, revoke_invalidates_invites, state, created_at, updated_at FROM circle;

DROP TABLE circle;

ALTER TABLE new_circle RENAME TO circle;

CREATE UNIQUE INDEX ux_circle_name_norm_server ON circle (name_norm, server) WHERE deleted_at IS NULL;

-- The trigger, back. Verbatim from 000002_invariant_triggers.sql: keyed on the VALUE changing
-- rather than on `UPDATE OF server`, so an update that names the column and does not move it is
-- not refused.
-- +goose StatementBegin
CREATE TRIGGER trg_circle_server_is_immutable BEFORE UPDATE ON circle
WHEN NEW.server <> OLD.server BEGIN
  SELECT RAISE(ABORT, 'circle.server is immutable: raiding a second server is a second circle');
END;
-- +goose StatementEnd

COMMIT;

PRAGMA foreign_keys = ON;

-- +goose Down
-- +goose StatementBegin
SELECT RAISE(ABORT, 'migrations are forward-only; roll forward with a new migration');
-- +goose StatementEnd
