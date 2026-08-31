-- +goose Up
-- create "session_revocation" table
CREATE TABLE session_revocation (session_id text NOT NULL, expires_at integer NOT NULL, created_at integer NOT NULL, updated_at integer NOT NULL, PRIMARY KEY (session_id)) STRICT;
-- create index "ix_session_revocation_expires_at" to table: "session_revocation"
CREATE INDEX ix_session_revocation_expires_at ON session_revocation (expires_at);

-- +goose Down
-- +goose StatementBegin
SELECT RAISE(ABORT, 'migrations are forward-only; roll forward with a new migration');
-- +goose StatementEnd
