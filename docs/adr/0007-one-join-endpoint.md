# ADR-0007 — One join endpoint, dispatching on provider

**Status:** accepted · **Date:** 2026-08-18 · **Deciders:** Courtney Caldwell

## Context and problem statement

[ADR-0003](0003-pluggable-identity-providers.md) makes the set of identity providers an operator
choice, varying per instance. The nParse+ plugin holds destinations across several hosts, each
configured differently, and has to discover at runtime how to join each one.

The HTTP surface therefore has to express "join, using whichever of these providers you support"
without knowing at design time which providers exist.

## Considered options

| Option | For | Against |
|---|---|---|
| A — One route per provider: `/join/discord`, `/join/oidc`, `/join/local` | Three clean request bodies. Generated SDKs get three well-typed methods with no union | Adding a provider becomes a *route* change: an OpenAPI change, an SDK regeneration and a plugin release before any operator can use it. The route surface would depend on a choice that is explicitly the operator's |
| B — One `/join`, `credential` a discriminated union on `kind` | The client is data-driven — read `previewInvite`, pick a provider, POST. A new provider needs no client change | A `oneOf` with a discriminator is uglier in generated SDKs, and the union is validated in the service rather than purely in the schema |

## Decision outcome

**Chosen: B.** The entire point of ADR-0003 is that operators choose providers, so the route surface
must not encode that choice. Under option A, an operator enabling their Keycloak would be blocked on
us shipping a plugin release, which makes "pluggable" a word rather than a property.

`credential` discriminates on `kind`: `bearer_token` (discord), `id_token` + `nonce` (oidc), `none`
(local). `display_name` is required for `local` and optional elsewhere. `POST /sessions` — re-auth on
a new device without an invite — takes the identical shape minus `invite_code`, plus `circle_id`, so
there is one credential union in the system rather than two.

Validation errors come back as `validation_failed` with `errors[].location` pointing into the union
(`body.credential.token`), so a client still gets a specific message rather than "body invalid".

### Consequences

- Good, because an operator can enable a new OIDC issuer and existing plugin builds can use it
  immediately.
- Good, because there is exactly one credential shape to secure, audit and rate-limit, rather than
  one per provider.
- Good, because `previewInvite` becomes genuinely useful: it tells the client what to render before
  the user has committed to anything.
- **Bad, because generated SDKs expose a union**, which is more awkward to call than a typed method
  per provider, in every language.
- **Bad, because part of the validation moves from the schema into the service**, so the OpenAPI
  document alone no longer fully describes what a valid request is.
- **Bad, because one endpoint means one rate-limit bucket** across all providers, so a storm against
  a cheap path throttles an expensive one.

### Reversal cost

An afternoon to add per-provider routes alongside the union one, plus the 18-month v1 deprecation
window before the union route could be withdrawn.
