-- +goose Up
-- create "circle_discord_channel" table
CREATE TABLE circle_discord_channel (discord_channel_id text NOT NULL, circle_id text NOT NULL, discord_guild_id text NOT NULL, allow_visible integer NOT NULL DEFAULT 0, created_by_membership_id text NOT NULL, created_at integer NOT NULL, updated_at integer NOT NULL, PRIMARY KEY (discord_channel_id), CONSTRAINT fk_circle_discord_channel_circle FOREIGN KEY (circle_id) REFERENCES circle (id), CONSTRAINT fk_circle_discord_channel_creator FOREIGN KEY (circle_id, created_by_membership_id) REFERENCES membership (circle_id, id), CONSTRAINT ck_circle_discord_channel_allow_visible CHECK (allow_visible IN (0, 1))) STRICT;
-- create index "ix_circle_discord_channel_circle_id" to table: "circle_discord_channel"
CREATE INDEX ix_circle_discord_channel_circle_id ON circle_discord_channel (circle_id, discord_channel_id);

-- +goose Down
-- +goose StatementBegin
SELECT RAISE(ABORT, 'migrations are forward-only; roll forward with a new migration');
-- +goose StatementEnd
