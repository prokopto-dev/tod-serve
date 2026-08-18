-- credential_ticket - a verified subject for 120 seconds, single-use, redeemable at either /join
-- or /sessions.

-- name: CreateCredentialTicket :one
INSERT INTO credential_ticket (
  id, ticket_hash, provider_id, subject, display_name, guild_roles_json,
  expires_at, created_at, updated_at
) VALUES (
  sqlc.arg(id), sqlc.arg(ticket_hash), sqlc.arg(provider_id), sqlc.arg(subject),
  sqlc.arg(display_name), sqlc.arg(guild_roles_json), sqlc.arg(expires_at),
  sqlc.arg(created_at), sqlc.arg(updated_at)
)
RETURNING *;

-- name: GetCredentialTicketByHash :one
-- By hash, never by circle: which circle the ticket lands in is settled at redemption.
SELECT * FROM credential_ticket WHERE ticket_hash = sqlc.arg(ticket_hash);

-- name: ConsumeCredentialTicket :one
-- A second redemption is 401 auth_ticket_invalid. This returns no row when the ticket is already
-- consumed, and the BEFORE UPDATE trigger aborts a write that tried anyway.
UPDATE credential_ticket
SET consumed_at = sqlc.arg(consumed_at), updated_at = sqlc.arg(updated_at)
WHERE ticket_hash = sqlc.arg(ticket_hash) AND consumed_at IS NULL
RETURNING *;

-- name: DeleteExpiredCredentialTickets :execrows
DELETE FROM credential_ticket WHERE expires_at < sqlc.arg(before);
