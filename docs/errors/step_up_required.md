# `step_up_required`

**HTTP 403** · `type: https://docs.tod-serve.org/errors/step_up_required`

You hold a browser session with the right permission, and it has not proved your identity recently
enough for what you asked to do.

## What causes it

Some operations ask for a **recency proof** on top of the permission: the session must have proved
the identity again within a window. A tab left open all afternoon still authenticates you; it does
not prove that you are the person now typing into it.

**The window depends on what the operation costs if it is wrong** — see
[ADR-0024](../adr/0024-step-up-is-graded-and-re-authentication-is-not-a-sign-in.md) and
[canonical §6](../design/00-canonical-conventions.md#step-up-is-a-second-question-and-it-is-graded).
There are two graded tiers and the problem names which one you failed:

| `meta.step_up_tier` | Window | What is behind it |
|---|---|---|
| `sensitive` | 5 minutes | Anything that changes **who can do what**, or destroys what nothing rebuilds: roles, revocations, minted credentials, a circle's accepted identity providers, the instance realm |
| `routine` | 1 hour | Circle state that changes no capability: renaming a circle, timer overrides, revoking an invite |

`meta.step_up_window_seconds` is that window in seconds.

**Not every session-only operation asks for one.** Reading a circle's own audit log is
[capability-floor](../concepts/glossary.md) — no personal access token reaches it at any scope — and
asks for no recency proof at all, because reading it is not a privilege escalation.

## What the client should do

**Re-authenticate in place and repeat the request. Do not sign out.**
`POST /api/v1/sessions/step-up` (`stepUpSession`) re-proves the identity behind the session you
already hold: same session, same expiry, and **no personal access token is minted**. Signing out and
back in also works and is the wrong answer — `POST /sessions` is a sign-in, so it mints a device
every time, and a device list full of one browser is what that habit produces.

It needs a provider whose subject the server can verify. A membership whose only identity is `local`
cannot step up at all, because `local` mints a new subject on every verification: the route says so
with [`provider_unverifiable`](provider_unverifiable.md) rather than letting you try forever. A
client should read `verifiable_subject` from `listIdentityProviders` and not offer a control it
knows will fail.

This is distinct from [`session_required`](session_required.md), which no amount of
re-authentication fixes because the credential is the wrong *kind*.
