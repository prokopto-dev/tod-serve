-- One namespace for every spelling of a raid target.
--
-- Hand-written for the same reason 000002 is: SQLite has no unique constraint that spans two
-- tables, and Atlas Community neither inspects nor emits triggers, so it can neither author these
-- nor plan a diff that drops one. The costs named at the top of 000002 apply here unchanged --
-- most importantly that a future table rebuild drops these silently, which is why
-- TestRaidTargetNamespace_TriggersFire_AfterAllMigrations asserts an ABORT rather than a row in
-- sqlite_master.
--
-- WHAT THIS ENFORCES, and why an index cannot:
--
--   `ux_raid_target_name_norm` makes names unique among names. `ux_raid_target_alias_norm` makes
--   aliases unique among aliases. Neither says anything about the OTHER table, so nothing stopped
--   an alias `lordnagafen` being hung on a different target -- and the resolve ladder then answers
--   that spelling with the canonical-name target, because `name_norm` is rung two and `alias_norm`
--   is rung four. The alias resolves to somebody else's mob and its owner is never told.
--
--   That is the quiet version of exactly what the ladder exists to prevent: the nParse+ plugin
--   sends a parsed name, holds no catalogue, and cannot notice being answered with the wrong mob.
--
-- The service checks the same rule inside its write transaction, where it can answer 409 with a
-- message naming the collision instead of surfacing a constraint failure. These triggers are what
-- make that a rule rather than a habit -- `tod-serve seed`, a future importer and a hand-run
-- `sqlite3` all go through them.

-- +goose Up

-- +goose StatementBegin
CREATE TRIGGER trg_raid_target_alias_not_a_name BEFORE INSERT ON raid_target_alias
WHEN EXISTS (SELECT 1 FROM raid_target WHERE name_norm = NEW.alias_norm) BEGIN
  SELECT RAISE(ABORT, 'alias collides with a raid target name: one namespace covers both, or the alias resolves to somebody else''s mob');
END;
-- +goose StatementEnd

-- +goose StatementBegin
CREATE TRIGGER trg_raid_target_alias_not_a_name_on_update BEFORE UPDATE OF alias_norm ON raid_target_alias
WHEN EXISTS (SELECT 1 FROM raid_target WHERE name_norm = NEW.alias_norm) BEGIN
  SELECT RAISE(ABORT, 'alias collides with a raid target name: one namespace covers both, or the alias resolves to somebody else''s mob');
END;
-- +goose StatementEnd

-- The other direction. Renaming a target onto an existing alias breaks the same rule from the
-- other side, and is the easier one to do by accident: the alias may belong to a mob nobody has
-- looked at in months.
-- +goose StatementBegin
CREATE TRIGGER trg_raid_target_name_not_an_alias BEFORE INSERT ON raid_target
WHEN EXISTS (SELECT 1 FROM raid_target_alias WHERE alias_norm = NEW.name_norm) BEGIN
  SELECT RAISE(ABORT, 'raid target name collides with an existing alias: one namespace covers both');
END;
-- +goose StatementEnd

-- +goose StatementBegin
CREATE TRIGGER trg_raid_target_name_not_an_alias_on_update BEFORE UPDATE OF name_norm ON raid_target
WHEN EXISTS (SELECT 1 FROM raid_target_alias WHERE alias_norm = NEW.name_norm AND target_id <> NEW.id) BEGIN
  SELECT RAISE(ABORT, 'raid target name collides with an existing alias: one namespace covers both');
END;
-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
SELECT RAISE(ABORT, 'migrations are forward-only; roll forward with a new migration');
-- +goose StatementEnd
