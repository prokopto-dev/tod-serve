// Package outbound is the guarded HTTP client every outbound request in this repository goes
// through, and the only place a socket is opened at all.
//
// [Canonical §14](docs/design/00-canonical-conventions.md) confines outbound HTTP to the identity
// providers because OIDC discovery and JWKS URLs are OPERATOR-SUPPLIED, which is the classic SSRF
// pivot: an operator — or anyone who reached `instance.security.manage` — points `jwks_uri` at
// http://169.254.169.254/ and the instance fetches cloud credentials on their behalf.
//
// The confinement used to name two packages, internal/identity/discord and internal/identity/oidc,
// and let each build its own client. This package narrows that to one, which is a stronger rule
// rather than a wider one: the providers can no longer construct an unguarded [net/http.Client],
// so a guard added here cannot be bypassed by the next provider somebody writes. NET001 is what
// enforces it, in both directions.
//
// What the guard is:
//
//   - HTTPS only. A plain-http URL is refused before a socket is opened.
//   - A host allowlist, supplied by the caller. Discord's hosts are fixed; an OIDC verifier's
//     allowlist is exactly the hosts of the URLs its own provider row configures, so a response
//     cannot walk the fetch onto a host the operator never named.
//   - Resolve, check EVERY resolved address, then dial the checked address LITERAL — never the
//     name. See [guardedDialer.DialContext] for why that ordering is what defeats DNS rebinding.
//   - No redirects. A 302 is an error, not a hop.
//   - A response size cap and a timeout, both mandatory.
//
// The deny list is [DenyReason], exported so that
// TestDenyReason_EveryDeniedRange_IsRefused can drive it directly rather than inferring it from a
// dial that failed for some other reason. docs/concepts/invariants.md requires that test by name.
package outbound
