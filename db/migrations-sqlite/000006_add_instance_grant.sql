-- +goose Up
-- create "instance_grant" table
CREATE TABLE instance_grant (id text NOT NULL, identity_id text NOT NULL, permission text NOT NULL, decision text NOT NULL, supersedes_id text, decided_by_identity_id text, reason text NOT NULL DEFAULT '', prev_hash blob, hash blob NOT NULL, decided_at integer NOT NULL, PRIMARY KEY (id), CONSTRAINT fk_instance_grant_identity FOREIGN KEY (identity_id) REFERENCES identity (id), CONSTRAINT fk_instance_grant_supersedes FOREIGN KEY (supersedes_id) REFERENCES instance_grant (id), CONSTRAINT fk_instance_grant_decided_by FOREIGN KEY (decided_by_identity_id) REFERENCES identity (id), CONSTRAINT ck_instance_grant_permission CHECK (permission IN ('catalogue.manage', 'ops.read', 'instance.circle.create', 'instance.security.manage', 'instance.owner')), CONSTRAINT ck_instance_grant_decision CHECK (decision IN ('granted', 'revoked')), CONSTRAINT ck_instance_grant_supersedes_another_row CHECK (supersedes_id IS NULL OR supersedes_id <> id)) STRICT;
-- create index "ux_instance_grant_supersedes" to table: "instance_grant"
CREATE UNIQUE INDEX ux_instance_grant_supersedes ON instance_grant (supersedes_id) WHERE supersedes_id IS NOT NULL;
-- create index "ux_instance_grant_head" to table: "instance_grant"
CREATE UNIQUE INDEX ux_instance_grant_head ON instance_grant (identity_id, permission) WHERE supersedes_id IS NULL;
-- create index "ux_instance_grant_chain" to table: "instance_grant"
CREATE UNIQUE INDEX ux_instance_grant_chain ON instance_grant (prev_hash) WHERE prev_hash IS NOT NULL;
-- create index "ix_instance_grant_identity" to table: "instance_grant"
CREATE INDEX ix_instance_grant_identity ON instance_grant (identity_id);

-- +goose Down
-- +goose StatementBegin
SELECT RAISE(ABORT, 'migrations are forward-only; roll forward with a new migration');
-- +goose StatementEnd
