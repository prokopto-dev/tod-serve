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
