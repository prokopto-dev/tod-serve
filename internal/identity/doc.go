// Package identity is the provider registry, the credential union and the browser OAuth flow.
//
// An identity is a `(provider, subject)` pair — [ADR-0003] — and a membership binds to an
// `identity_id`, never to a bare Discord id. That indirection is the whole reason an instance can
// offer more than one way in, and it is why adding a provider is a row rather than a schema
// change.
//
// # What lives where
//
//   - This package owns the vocabulary: [Provider], [Credential], the wire [Code] set, the
//     revocation-strength derivation and the two OAuth flow operations.
//   - internal/identity/discord, .../oidc and .../local own their verification, and each returns
//     its own sentinel errors. This package maps those to codes, so the dependency runs one way
//     and a provider package cannot reach for a wire code that means something to the API.
//   - internal/identity/outbound owns the socket. Nothing here opens one.
//
// # What this package deliberately does NOT do
//
// It registers no HTTP routes. Routes are declared only in internal/api (AGENTS.md law 1), so the
// two operations [ADR-0011] adds — `createAuthorizationURL` and `completeAuthorization` — are
// exposed here as [Service.CreateAuthorizationURL] and [Service.CompleteAuthorization], taking and
// returning transport-free values, and internal/api binds them.
//
// It also holds no `*sql.DB`. Everything it needs from the database is a small interface in
// ports.go, which is what makes the invariant tests possible: `TestDiscord_AccessToken_NeverPersisted`
// is a recording fake asserting no store call ever received the token, and that only works if
// "store call" is a thing the type system names.
//
// [ADR-0003]: docs/adr/0003-pluggable-identity-providers.md
// [ADR-0011]: docs/adr/0011-operator-registered-discord-application.md
package identity
