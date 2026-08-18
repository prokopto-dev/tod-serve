// Package auth turns a credential into a principal, and says what that principal may do.
//
// Two credential kinds reach it and only two. A personal access token is opaque, is bound to one
// membership (ADR-0005), and is narrowed by its scopes. A browser session is a signed cookie, is
// narrowed by nothing except step-up, and is the only thing that reaches the capability floor.
//
// The rules that are not obvious, and the reason each is here rather than somewhere convenient:
//
//   - **The secret is never stored and never logged.** `api_token` holds
//     `HMAC-SHA256(pepper, secret)` and an eight-character public prefix. The prefix is loggable
//     and is how a leaked token is traced back to a device; the secret half is a [core.Secret] from
//     the moment it is minted to the moment it is discarded.
//   - **Membership state is checked on EVERY request** rather than cascade-revoking tokens when a
//     membership is revoked. One join, always correct, and nothing to forget: there is no list of
//     tokens to walk, so there is no way to miss one.
//   - **Effective capability is `role permissions ∩ token scopes`**, computed by internal/authz and
//     not reimplemented here. A token can only ever narrow what its membership's role grants.
//   - **A query-string token is rejected**, with no exception at all. There is no compat shim.
package auth
