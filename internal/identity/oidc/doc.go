// Package oidc verifies an OIDC ID token OFFLINE.
//
// Offline rather than through the userinfo endpoint, and the reasons are in
// docs/design/04-identity-and-revocation.md §1: an ID token is verifiable against a cached JWKS
// with no per-join round trip, and it means one fewer operator-supplied URL on the SSRF surface.
// Discovery is not implemented for the same reason — `jwks_uri` is a column on
// `identity_provider`, so the operator names the one URL this package fetches and nothing a
// response says can add another.
//
// `oidc` is the provider that is structurally immune to the cross-instance replay hole ADR-0011
// had to close for Discord with an extra call: `aud` is audience binding, in the token, checked
// with no network at all.
//
// The algorithm allowlist is the security-critical part, and it is an ALLOWLIST rather than a
// deny list. `alg: none` and the HMAC family are both refused: an HS256 token verified against a
// public key the attacker can read is the classic JWT forgery, and it works precisely because the
// verifier let the TOKEN choose the algorithm family.
package oidc
