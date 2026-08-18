# ADR-0003 — Identity is a `(provider, subject)` pair, not a Discord id

**Status:** accepted · **Date:** 2026-08-18 · **Deciders:** Courtney Caldwell

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
- **Bad, because the shared Discord app permits cross-instance token replay.** The same access token
  is valid at *every* tod-serve instance, so a hostile instance can present a user's token to another
  instance and impersonate them there. PKCE does not help — it is a bearer token and
  instance-agnostic. The mitigation shipped is a 60-second freshness requirement
  (`credential_stale`), which shrinks the window and does not close it. Closing it needs either
  central verification infrastructure the self-hostable property was meant to avoid, or `oidc`, whose
  `aud` makes replay structurally impossible. **This is accepted knowingly.**
- **Bad, because Discord's developer terms may not permit this arrangement.** One application used by
  arbitrary third-party self-hosted servers, receiving end-user access tokens, is not obviously within
  them. **A human must read the ToS before the `discord` provider ships.** This is a release blocker,
  not a footnote.
- **Bad, because nobody owns the shared app's operational health.** A join storm makes it a heavily
  rate-limited client, and a ban hits every instance at once.
- **Bad, because operator-supplied OIDC discovery and JWKS URLs are an SSRF surface** that would not
  exist under option A.

### Reversal cost

Collapsing to one provider is a day plus a data migration for whichever identities are dropped.
Adding a *fourth* provider is an afternoon, which is the point.
