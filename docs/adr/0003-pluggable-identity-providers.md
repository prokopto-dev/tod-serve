# ADR-0003 — Identity is a `(provider, subject)` pair, not a Discord id

**Status:** accepted · **Date:** 2026-08-18 · **Deciders:** Courtney Caldwell

The `(provider, subject)` decision below **stands**. Its three shared-Discord-app consequences are
superseded by [ADR-0011](0011-operator-registered-discord-application.md), which makes the Discord
application per-instance and operator-registered; they are marked in place rather than deleted.

## Context and problem statement

Membership must be revocable in a way that sticks. On P99 the person most worth revoking is a rival
guild's tracker, and time-of-death data is exactly the intelligence a circle exists to protect.
Revoking a *token* is not enough — anyone holding another invite walks back in under a new name.

Durable revocation therefore needs a subject the server can verify. The obvious choice is Discord,
which every P99 group already uses. But the population this product is for includes groups who share
no Discord, and self-hosters running a LAN binary with no outbound network at all. Picking one
provider excludes one of those groups.

## Considered options

| Option | For | Against |
|---|---|---|
| A — Discord only, one project-wide app | One integration to build and document. Matches how P99 groups already organise | Excludes groups without a shared Discord and LAN-only instances entirely. Ties the product's identity story to one company's terms |
| B — Invite code → token, self-asserted name, no third party | Works offline, nothing to register, lightest possible | Revocation is advisory. A revoked person with any invite returns as a new member, and the officers believe it worked |
| C — `(provider, subject)`, with `discord`, `oidc` and `local` implementations | Each population gets a path in. Adding a provider is a row, not a schema change. OIDC covers groups with their own IdP and is immune to the replay hole below | Two verification paths plus a null one to build, document, test and keep from diverging. Revocation strength stops being a single global property |

## Decision outcome

**Chosen: C.** The indirection costs one table and buys the product a coherent answer for three
different populations, and — decisively — it means a problem with any one provider is a configuration
change rather than a redesign.

`identity_provider.verifiable_subject` is a `CHECK` against `kind`, never an operator toggle, because
everything about revocation strength hangs off it. `local` ships **disabled** and enabling it
requires `acknowledge_weak_revocation: true`.

The consequence that must not be buried: **revocation strength becomes a property of the circle, not
of the server.** Banning an identity only sticks if every provider that circle accepts has a
verifiable subject. So `circle.revocation_strength` and `membership.revocation_strength` are both
derived, both exposed as machine-readable fields, and surfaced in `previewInvite` *before anyone
joins* — because the damage from a weak revocation is not the re-entry, it is the officers' false
belief that revocation worked, and a field a client must render is the only thing that reliably
reaches them.

Identity linking is manual, officer-gated, and permitted only between verifiable identities
(trigger-enforced). Automatic linking would let anyone who can assert a display name inherit another
person's standing.

One join endpoint dispatches on provider ([ADR-0007](0007-one-join-endpoint.md)), so an operator
adding a provider does not require an SDK regeneration and a plugin release.

### Consequences

- Good, because a LAN-only instance, a Discord guild and a group running Authentik all have a real
  path in, and the choice is the operator's.
- Good, because if Discord becomes unavailable to us, `oidc` becomes the default and no other part of
  the design moves.
- Good, because `verifiable_subject` being a CHECK makes the honesty of the revocation claim
  structural rather than procedural.
- **Bad, because revocation is only as strong as the weakest accepted provider**, and a circle that
  enables `local` has advisory revocation while believing it has a member list.
- **Bad, because unlinked duplicate identities inflate consensus confidence.** One person with a
  Discord and an OIDC identity counts as two distinct reporters until an officer links them. The
  member list flags `possible_duplicate` and deliberately does not act on it.
- **Bad, because the shared Discord app permits cross-instance token replay.** A token valid at
  *every* instance lets a hostile instance impersonate a user elsewhere; PKCE does not help, and the
  60-second freshness rule (`credential_stale`) shrank the window without closing it.
  **Superseded by [ADR-0011](0011-operator-registered-discord-application.md)** — a token minted for
  the operator's own client id is worthless at any other instance.
- **Bad, because Discord's developer terms may not permit this arrangement.** One application used by
  arbitrary third-party self-hosted servers, receiving end-user access tokens, is not obviously within
  them. **Superseded by [ADR-0011](0011-operator-registered-discord-application.md)** — there is no
  shared application, so each operator's app is their own agreement with Discord.
- **Bad, because nobody owns the shared app's operational health.** A join storm makes it a heavily
  rate-limited client, and a ban hits every instance at once.
  **Superseded by [ADR-0011](0011-operator-registered-discord-application.md)** — rate limits and
  bans are now per-instance, and the operator owns theirs.
- **Bad, because operator-supplied OIDC discovery and JWKS URLs are an SSRF surface** that would not
  exist under option A.

### Reversal cost

Collapsing to one provider is a day plus a data migration for whichever identities are dropped.
Adding a *fourth* provider is an afternoon, which is the point.
