-- +goose Up
-- create "instance_setting_change" table
CREATE TABLE instance_setting_change (id text NOT NULL, setting text NOT NULL, old_value text NOT NULL, new_value text NOT NULL, changed_by_identity_id text, reason text NOT NULL DEFAULT '', prev_hash blob, hash blob NOT NULL, changed_at integer NOT NULL, PRIMARY KEY (id), CONSTRAINT fk_instance_setting_change_identity FOREIGN KEY (changed_by_identity_id) REFERENCES identity (id), CONSTRAINT ck_instance_setting_change_setting CHECK (setting IN ('self_service_circle_creation', 'name', 'timezone')), CONSTRAINT ck_instance_setting_change_moved CHECK (old_value <> new_value)) STRICT;
-- create index "ux_instance_setting_change_chain" to table: "instance_setting_change"
CREATE UNIQUE INDEX ux_instance_setting_change_chain ON instance_setting_change (prev_hash) WHERE prev_hash IS NOT NULL;
-- create index "ux_instance_setting_change_hash" to table: "instance_setting_change"
CREATE UNIQUE INDEX ux_instance_setting_change_hash ON instance_setting_change (hash);
-- create index "ix_instance_setting_change_changed_at" to table: "instance_setting_change"
CREATE INDEX ix_instance_setting_change_changed_at ON instance_setting_change (changed_at);

-- +goose Down
-- +goose StatementBegin
SELECT RAISE(ABORT, 'migrations are forward-only; roll forward with a new migration');
-- +goose StatementEnd
