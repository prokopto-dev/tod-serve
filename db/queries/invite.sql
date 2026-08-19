-- invite - codes are INSTANCE-unique, so POST /join needs no circle id: one paste.

-- name: CreateInvite :one
INSERT INTO invite (
  id, circle_id, code_hash, code_prefix, role, max_uses, uses, expires_at,
  created_by_membership_id, minted_by_kind, note, created_at, updated_at
) VALUES (
  sqlc.arg(id), sqlc.arg(circle_id), sqlc.arg(code_hash), sqlc.arg(code_prefix), sqlc.arg(role),
  sqlc.arg(max_uses), 0, sqlc.arg(expires_at), sqlc.arg(created_by_membership_id),
  sqlc.arg(minted_by_kind), sqlc.arg(note), sqlc.arg(created_at), sqlc.arg(updated_at)
)
RETURNING *;

-- name: GetInviteByCodeHash :one
-- tenancy: an invite code is instance-unique and IS the capability - holding it is the evidence
-- that somebody handed it to you. previewInvite and /join resolve the circle FROM this row, so
-- naming a circle here would require the caller to supply one, which is the oracle canonical section 7
-- exists to close. Looked up by hash on the unique index, NEVER by prefix: a prefix lookup is a
-- brute-force oracle.
SELECT * FROM invite WHERE code_hash = sqlc.arg(code_hash);

-- name: GetInvite :one
SELECT * FROM invite WHERE circle_id = sqlc.arg(circle_id) AND id = sqlc.arg(id);

-- name: ListInvites :many
SELECT * FROM invite WHERE circle_id = sqlc.arg(circle_id) ORDER BY id DESC;

-- name: CountLiveInvites :one
-- The `active_invite_count` revokeMember returns, so the UI can say "you also have 2 live
-- invites" without a second warnings channel being invented for it.
SELECT COUNT(*) AS live FROM invite
WHERE circle_id = sqlc.arg(circle_id)
  AND revoked_at IS NULL AND expires_at > sqlc.arg(now) AND uses < max_uses;

-- name: RevokeInvite :one
UPDATE invite
SET revoked_at = sqlc.arg(revoked_at), updated_at = sqlc.arg(updated_at)
WHERE circle_id = sqlc.arg(circle_id) AND id = sqlc.arg(id) AND revoked_at IS NULL
RETURNING *;

-- name: RevokeLiveInvitesForCircle :execrows
-- When a weakly-revocable member is revoked and revoke_invalidates_invites = 1, every outstanding
-- invite goes in the SAME transaction. The officers' false belief that revocation worked is the
-- damage, not the re-entry.
--
-- The predicate is the same one CountLiveInvites uses, and it has to be: the row count this
-- returns is reported to an officer as "2 invites were revoked", and an UPDATE that also touched
-- expired and exhausted rows would report a number larger than the number of doors it closed.
UPDATE invite
SET revoked_at = sqlc.arg(revoked_at), updated_at = sqlc.arg(updated_at)
WHERE circle_id = sqlc.arg(circle_id)
  AND revoked_at IS NULL AND expires_at > sqlc.arg(now) AND uses < max_uses;

-- name: ConsumeInvite :one
-- The CHECK (uses <= max_uses) makes over-redemption unrepresentable; `uses < max_uses` here turns
-- the race into a returned-no-row rather than a constraint violation surfacing as a 500.
UPDATE invite
SET uses = uses + 1, updated_at = sqlc.arg(updated_at)
WHERE circle_id = sqlc.arg(circle_id) AND id = sqlc.arg(id)
  AND revoked_at IS NULL AND expires_at > sqlc.arg(now) AND uses < max_uses
RETURNING *;
