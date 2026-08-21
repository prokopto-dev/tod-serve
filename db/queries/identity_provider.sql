-- identity_provider - the pluggable IdP registry. Instance-level, mutable under
-- instance.security.manage (session + step-up).

-- name: CreateIdentityProvider :one
INSERT INTO identity_provider (
  id, key, kind, display_name, enabled, verifiable_subject,
  issuer, authorization_endpoint, jwks_uri, subject_claim,
  client_id, client_secret, redirect_uri, token_endpoint, created_at, updated_at
) VALUES (
  sqlc.arg(id), sqlc.arg(key), sqlc.arg(kind), sqlc.arg(display_name), sqlc.arg(enabled),
  sqlc.arg(verifiable_subject), sqlc.narg(issuer), sqlc.narg(authorization_endpoint),
  sqlc.narg(jwks_uri), sqlc.narg(subject_claim), sqlc.narg(client_id), sqlc.narg(client_secret),
  sqlc.narg(redirect_uri), sqlc.narg(token_endpoint), sqlc.arg(created_at), sqlc.arg(updated_at)
)
RETURNING *;

-- name: GetIdentityProvider :one
SELECT * FROM identity_provider WHERE id = sqlc.arg(id);

-- name: GetIdentityProviderByKey :one
SELECT * FROM identity_provider WHERE key = sqlc.arg(key);

-- name: ListIdentityProviders :many
SELECT * FROM identity_provider ORDER BY key;

-- name: ListEnabledIdentityProviders :many
SELECT * FROM identity_provider WHERE enabled = 1 ORDER BY key;

-- name: SetIdentityProviderEnabled :one
UPDATE identity_provider
SET enabled = sqlc.arg(enabled), updated_at = sqlc.arg(updated_at)
WHERE id = sqlc.arg(id)
RETURNING *;

-- name: UpdateIdentityProvider :one
-- `key` and `kind` are absent on purpose: kind decides verifiable_subject through a CHECK, and a
-- circle's revocation strength hangs off that, so changing it under a circle that already accepts
-- the provider would silently restate what revocation means there. The edge answers
-- 422 field_immutable instead.
UPDATE identity_provider
SET display_name = sqlc.arg(display_name),
    enabled = sqlc.arg(enabled),
    issuer = sqlc.narg(issuer),
    authorization_endpoint = sqlc.narg(authorization_endpoint),
    jwks_uri = sqlc.narg(jwks_uri),
    subject_claim = sqlc.narg(subject_claim),
    client_id = sqlc.narg(client_id),
    client_secret = sqlc.narg(client_secret),
    redirect_uri = sqlc.narg(redirect_uri),
    token_endpoint = sqlc.narg(token_endpoint),
    updated_at = sqlc.arg(updated_at)
WHERE id = sqlc.arg(id)
RETURNING *;

-- name: DeleteIdentityProvider :exec
-- Foreign keys are NO ACTION everywhere, so this is REFUSED once any identity, auth flow,
-- credential ticket or circle references the row. That is the correct outcome: removing a provider
-- people joined through would orphan their identities, and disabling it is the operation that
-- actually stops new joins.
DELETE FROM identity_provider WHERE id = sqlc.arg(id);
