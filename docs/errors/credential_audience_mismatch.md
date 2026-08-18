# `credential_audience_mismatch`

**HTTP 401** · `type: https://docs.tod-serve.org/errors/credential_audience_mismatch`

The Discord access token you presented is valid, but it was **minted for a different application**.
`GET /oauth2/@me` reported an `application.id` that is not this instance's configured `client_id`.

## What causes it

- A token obtained from another tod-serve instance, or from any other Discord application, and
  presented here. **This is what a cross-instance replay attempt looks like**, and it is refused for
  the same reason when it is innocent.
- An operator who rotated or re-registered their Discord application without updating `client_id`,
  so tokens minted under the old one no longer match.
- A client configured against instance A's OAuth application while pointed at instance B.

## What the client should do

Authenticate against **this** instance and present the token it issues. A token from anywhere else
will never verify here, so there is nothing to retry.

This check is the reason a per-instance Discord application closes cross-instance replay at all.
Registration alone does not: `GET /users/@me` honours any valid bearer token whichever application
minted it, so the audience has to be checked explicitly. It is the same binding OIDC gets for free
from the ID token's `aud`, which is why `oidc` never needed this code.
