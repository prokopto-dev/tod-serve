# ADR-0024 — Step-up is graded, and re-authentication is not a sign-in

**Status:** proposed · **Date:** 2026-08-31 · **Deciders:** owner

## Context and problem statement

Reported from real use: *"Either log me out automatically, or let me look. Not this
half-authenticated state I'm apparently in."* Three symptoms — every sign-in added a Devices row;
five minutes in, pages answered `step_up_required` and the only remedy found was signing out and
back in, adding another device; **and the Audit page did it too.**

One root: **one boolean answered two questions.** `PermissionDef.StepUp` meant both *no personal
access token reaches this at any scope* — the capability floor, a real property — and *prove again,
within five minutes, that you are the person now typing*. They travelled together because every
floor permission happened to be a write. Then `audit.read` joined the floor, correctly, and
inherited a five-minute expiry chosen for granting a role.

`POST /sessions` mints a token, because a plugin with no browser has to get one somewhere — so
using it as the remedy minted a credential the browser never sees again. And with no in-place
re-authentication, that remedy was a sign-out: the one action that adds another device.

## Considered options

| Option | For | Against |
|---|---|---|
| A — lengthen `DefaultStepUpWindow` | One line | One bad default for another: one number gates reading an audit log and revoking an owner. Symptom 3 untouched — Audit still fails, just later |
| B — drop step-up, keep the floor | Simplest model; the floor already stops a leaked token | A tab left open in a raid hall should not revoke a member on the say-so of whoever walked past |
| C — **grade per permission, split the floor from it** | Reads stop being gated like grants; `audit.read` stays unreachable by any token and asks for no recency; five minutes stays where it was chosen for | Two fields where there was one, a second fenced block to keep honest, every route annotation names its tier |
| D — grade per *route* | Finer control | A second route reaching the same permission could ask a weaker question, which is how a model grows a back door |

## Decision outcome

**Chosen: C**, with the re-authentication half as its own operation.

**The floor and step-up are two fields.** `PermissionDef.Floor` is *no PAT reaches this*.
`PermissionDef.StepUp` is an ordered `StepUpTier` — `none`, `routine`, `sensitive`. The floor is
unchanged: the same thirteen permissions and the same fenced block in
[canonical §6](../design/00-canonical-conventions.md#the-capability-floor); being in it just no
longer implies a recency proof. The tier is on the **permission**, so a second route reaching the
same key cannot ask a weaker question, and `Route.StepUp()` takes the **strictest** tier among a
route's permissions — they are an any-of, and the weakest would let the cheapest key that opens an
operation set the bar.

Which tier is decided on **what a compromise costs**; canonical §6 lists the tiers and that
reasoning. `sensitive` is five minutes, unchanged; `routine` is one hour; `none` is `audit.read`
alone.

**`audit.read` is floored and not stepped up.** A leaked bot token still cannot bulk-export who did
what — the floor's property, untouched. Being unable to *look* was never that property, and
`TestRouteRegistry_ListCircleAudit_IsFlooredAndNotSteppedUp` pins both halves by name.

**`POST /api/v1/sessions/step-up` re-proves an existing session and mints nothing.** Same session
id, same expiry, fresh `stepped_up_at`. Session-only, no `circle_id` — the circle is the session's
— and the verified credential must belong to the identity owning the calling session, a check
`/sessions` has no analogue for because there the membership is resolved *from* the credential.
`/sessions` still mints, which is right for signing in on a device. **The expiry is not extended,
only the proof:** renewing it would make a console that re-proves hourly immortal, and that bounded
lifetime is what makes a stateless session acceptable.

**A provider that cannot verify a subject says so.** `local` mints a fresh ULID per verification, so
`stepUpSession` refuses it with [`provider_unverifiable`](../errors/provider_unverifiable.md) rather
than answering `credential_invalid`, which reads as "try again" for a request that can never
succeed. The console reads `verifiable_subject` and offers no button it knows will fail.

**The console re-authenticates in place**, through a control on every `step_up_required` that runs
`stepUpSession` and returns to the page it left. Sign-out
([ADR-0018](0018-sign-out-ends-one-session.md)) must not be the remedy.

### Consequences

- Good, because reading an audit log stops being gated like granting a role, and the
  half-authenticated state has a way out that is not a sign-out.
- Good, because five minutes stays where it was chosen for; what changed is what sits behind it.
- Good, because a console session no longer mints a device per re-authentication, and the tier
  reaches the wire — on the problem, on `/me`, on `x-tod-permission` — so a client names the bar it
  failed instead of reverse-engineering a rule from a number.
- **Bad, because** two fields where there was one: a permission can be floored and not stepped up,
  so somebody reading `Floor` as "the strict one" will be wrong.
  `TestStepUp_OutsideTheFloor_IsAlwaysNone` holds the other direction.
- **Bad, because** `routine` is a real window and will refuse somebody. An hour was picked because a
  window equal to the 12-hour session TTL refuses nothing, which reads as a control and is not one.
- **Bad, because** `permission.requires_step_up` is now the coarse question and cannot tell the
  tiers apart. Nothing reads it; widening it is a table rebuild bought for no query.
- **Bad, because** `stepUpSession` is another place that verifies a credential, so the guild gate,
  the block check and the accepted-provider read are repeated there.
- **Bad, because** an instance whose only provider is `local` still cannot reach the sensitive tier.
  Visible now rather than hidden behind a misleading refusal; fixing it means giving `local`
  something to re-present, which is what `local` is not.

### Reversal cost

A release. Collapsing the two fields back into one boolean is mechanical; `stepUpSession` stays,
because an `operationId` is never removed.
