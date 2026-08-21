-- +goose Up
-- create index "ux_instance_grant_hash" to table: "instance_grant"
CREATE UNIQUE INDEX ux_instance_grant_hash ON instance_grant (hash);

-- +goose Down
-- +goose StatementBegin
SELECT RAISE(ABORT, 'migrations are forward-only; roll forward with a new migration');
-- +goose StatementEnd
