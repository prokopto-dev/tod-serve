# ADR-0018 — Sign-out ends one session, and records it server-side

**Status:** proposed · **Date:** 2026-08-31 · **Deciders:** owner

## Context and problem statement

`POST /sessions` created a browser session and nothing ended one, so a person on a shared or
borrowed machine could not make it safe, and "sign out and back in" — the universal first remedy for
a confusing auth state — was unavailable. The only existing remedy was rotating `TOD_SESSION_KEY`,
which signs out every principal on the instance.

Two questions had to be answered together. **What does sign-out end** — this session, or every
session the identity holds? And **what makes it true**: sessions here are signed, not stored
([`internal/auth/session.go`](../../internal/auth/session.go)), so there is no row to delete, and
clearing the cookie only asks the browser to forget something a copy of that cookie still knows.

## Considered options

| Option | For | Against |
|---|---|---|
| A — clear the cookie, store nothing | No schema change; keeps sessions purely stateless | A cookie copied before the sign-out still works for up to `DefaultSessionTTL`. The control that exists to make a shared machine safe would not, which is the failure it is named after |
| B — end **this** session, recorded in `session_revocation` | Matches what the button means: "I am finished on this machine". One narrow row, swept once the session would have expired anyway | One indexed read added to every session-authenticated request; one more table |
| C — end **every** session for the identity, by an epoch per identity | One row covers a lost device without enumerating sessions | Signs somebody out of their phone because they closed a browser at work. Wrong default for the control everybody clicks |

## Decision outcome

**Chosen: B.** `DELETE /api/v1/sessions` (`signOut`) ends the session that asked and no other, and
writes its id to `session_revocation`; `Authenticator.authenticateSession` refuses any cookie naming
a revoked session, so a copy taken beforehand is refused too. The cookie is cleared in the same
response — both halves, because neither alone is sign-out.

The session id comes off the **verified cookie**, so no session id travels in a request and a caller
cannot name somebody else's. The route is registered through the route registry (law 1) as `self`
with no scopes, which puts it in the capability floor: no personal access token reaches it at any
scope, asserted by `TestAPIParity_EveryConsoleRequest_IsReachableWithAScopedToken` and
`TestSignOut_APersonalAccessToken_IsRefused`.

**C is not offered behind a flag**, deliberately. A `?all=true` on the control everybody clicks is
exactly the destructive option that gets passed by accident. When sign-out-everywhere is wanted it
wants to be its own operation with its own confirmation; until then `TOD_SESSION_KEY` rotation is
the instance-wide remedy and the 12-hour TTL bounds the gap.

**Sign-out touches no personal access token.** ADR-0005 binds a PAT to a membership, and
`internal/auth` holds both, so "end this membership's credentials" is one wrong loop from being
true. A raider's nParse+ destination going silent because somebody signed out of a website is the
worst kind of surprise. `TestSignOut_APersonalAccessToken_IsUntouched` is the mechanism, and the
response says `tokens_kept` so the promise reaches the person, not only the test.

Enforcement, by name: `TestSignOut_TheSameCookie_IsRefusedAfterwards` replays the signed-out cookie
and requires a refusal; `TestSignOut_AnotherSessionOfTheSameMembership_StillWorks` is what makes
"this session only" a property; `TestSession_WithNoID_IsRefusedOnEncodeAndOnDecode` holds the codec
to minting nothing unrevocable.

### Consequences

- Good, because a signed-out session is refused by the **server**, so a copied cookie is dead too —
  which is the only version of sign-out worth having on a shared machine.
- Good, because `session_revocation` holds only sessions somebody ended, and only until they would
  have expired: `internal/sweep` takes the row, so the table is bounded by sign-outs per TTL rather
  than by sessions ever created.
- Good, because the destructive semantics are absent rather than one query parameter away.
- **Bad, because** every session-authenticated request now does one extra indexed read. It joins the
  membership read and the instance-grant read already on that path, so it is a third read rather
  than a first — but it is not free, and a future session cache has three things to invalidate.
- **Bad, because** sessions are no longer purely stateless. The claim in `Session`'s doc comment is
  now "signed, plus a revocation list", and a restore of an old database snapshot resurrects
  sessions that were signed out — for at most one TTL.
- **Bad, because** a cookie minted before this change carries no id, and `Decode` refuses one. The
  upgrade signs those sessions out once. That is the same blast radius as a key rotation, paid once,
  and it is what makes "every accepted session can be ended" true rather than usual.
- **Bad, because** sign-out-everywhere still does not exist, so a genuinely lost device is still an
  operator action with instance-wide reach.

### Reversal cost

A release. Dropping the check and the table is a forward migration and a handful of tests; the
session id in the cookie would stay, because removing a field from a signed payload signs everybody
out again.
