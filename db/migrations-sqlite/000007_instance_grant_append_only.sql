-- instance_grant is append-only. ADR-0012, canonical conventions section 10.
--
-- Hand-written for the same reason 000002 is: Atlas Community neither inspects nor emits SQLite
-- triggers, so it can neither author this nor plan a diff that drops it. This file adds no tables,
-- columns or indexes -- anything Atlas can see belongs in db/schema.hcl.
--
-- A row here is a DECISION, not a state. The row that revoked a permission is the only record that
-- it was ever held, and the row that granted it is the only record of who decided. An UPDATE would
-- rewrite an authorization decision after the fact and a DELETE would erase one, which is exactly
-- what an audit log exists to make impossible -- and audit_log itself cannot hold these rows,
-- because audit_log.circle_id is NOT NULL and an instance grant belongs to no circle.
--
-- The hash chain is the second half: the trigger stops a rewrite through this connection, and the
-- chain makes a row removed by something that bypassed the trigger visible in every row after it.
-- TestAppendOnly_TriggersFire_AfterAllMigrations asserts these ABORT rather than asserting they
-- appear in sqlite_master, because a table rebuild drops a trigger silently.

-- +goose Up

-- +goose StatementBegin
CREATE TRIGGER trg_instance_grant_no_update BEFORE UPDATE ON instance_grant BEGIN
  SELECT RAISE(ABORT, 'instance_grant is append-only and hash-chained: a revocation is a new row');
END;
-- +goose StatementEnd

-- +goose StatementBegin
CREATE TRIGGER trg_instance_grant_no_delete BEFORE DELETE ON instance_grant BEGIN
  SELECT RAISE(ABORT, 'instance_grant is append-only and hash-chained: a revocation is a new row');
END;
-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
SELECT RAISE(ABORT, 'migrations are forward-only; roll forward with a new migration');
-- +goose StatementEnd
