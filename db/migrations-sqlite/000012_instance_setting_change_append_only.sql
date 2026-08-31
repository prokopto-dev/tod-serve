-- instance_setting_change is append-only. Canonical conventions section 10.
--
-- Hand-written for the same reason 000002 and 000007 are: Atlas Community neither inspects nor
-- emits SQLite triggers, so it can neither author this nor plan a diff that drops it. This file
-- adds no tables, columns or indexes -- anything Atlas can see belongs in db/schema.hcl.
--
-- A row here is the only record that an instance-wide policy was ever different. Turning
-- self-service circle creation on decides whether a stranger with any credential may create a
-- circle on this instance; an UPDATE would rewrite who decided that and a DELETE would erase that
-- anybody did. audit_log cannot hold the event at all -- audit_log.circle_id is NOT NULL and an
-- instance policy belongs to no circle -- so this table is its own audit record, exactly as
-- instance_grant is.
--
-- The hash chain is the second half: the trigger stops a rewrite through this connection, and the
-- chain makes a row removed by something that bypassed the trigger visible in every row after it.
-- TestAppendOnly_TriggersFire_AfterAllMigrations asserts these ABORT rather than asserting they
-- appear in sqlite_master, because a table rebuild drops a trigger silently.

-- +goose Up

-- +goose StatementBegin
CREATE TRIGGER trg_instance_setting_change_no_update BEFORE UPDATE ON instance_setting_change BEGIN
  SELECT RAISE(ABORT, 'instance_setting_change is append-only and hash-chained: a correction is a new row');
END;
-- +goose StatementEnd

-- +goose StatementBegin
CREATE TRIGGER trg_instance_setting_change_no_delete BEFORE DELETE ON instance_setting_change BEGIN
  SELECT RAISE(ABORT, 'instance_setting_change is append-only and hash-chained: a correction is a new row');
END;
-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
SELECT RAISE(ABORT, 'migrations are forward-only; roll forward with a new migration');
-- +goose StatementEnd
