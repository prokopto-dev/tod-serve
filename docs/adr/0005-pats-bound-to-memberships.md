# ADR-0005 — Bind PATs to memberships, not to service accounts

**Status:** accepted · **Date:** 2026-08-18 · **Deciders:** Courtney Caldwell

## Context and problem statement

Dragon Kill Party
[ADR-0011](https://github.com/prokopto-dev/dragonkillparty/blob/main/docs/adr/0011-opaque-pats-no-superadmin-token.md)
establishes that tokens belong to *service accounts*, not people, so that a bot survives the officer
who created it leaving the guild. That reasoning is sound for a bot.

In tod-serve the dominant token holder is not a bot. It is the nParse+ plugin on a player's desktop,
holding one token per destination. Inheriting the rule unchanged would mean every player gets a
service account, which is a concept players do not have and officers would have to administer.

## Considered options

| Option | For | Against |
|---|---|---|
| A — Inherit: tokens belong to service accounts | Consistent with the sibling project. A bot's credential outlives its creator | Every human client needs a service account nobody asked for, and a departed member's token keeps working — the precise opposite of what revocation must do here |
| B — Tokens belong to memberships; a bot is a `kind='service'` membership with a human owner | The token dies when the membership does, which is what revocation means. Still exactly one principal kind in the authz path | Diverges from the sibling project on a rule it states explicitly. A bot's token dies with its owner's membership unless ownership is transferred first |

## Decision outcome

**Chosen: B.** The reason ADR-0011 gave for service accounts does not apply to the token that
matters here: this token *is* a person's client credential, and it should die when they do.

Membership state is checked on **every request** rather than by cascade-revoking tokens at revocation
time. One join, always correct, nothing to forget — and it means a revocation takes effect on the
revoked member's next request rather than after a sweep.

A bot gets a `kind='service'` membership with a `owner_membership_id` naming a human, so ADR-0011's
actual guarantee — the audit always names a responsible person — survives intact. There is still no
`admin:*` scope and no all-powerful token.

Format and storage are inherited unchanged: `tods_pat_<8-char public prefix>_<43 chars base64url of
32 random bytes>`, stored as `HMAC-SHA256(server_pepper, secret)` — a keyed hash rather than bcrypt,
because verification is on the hot path. The 8-character prefix is loggable and is how a leaked token
is found; the secret never is.

### Consequences

- Good, because revoking a member revokes their access, with no separate token-cleanup step that can
  be forgotten or fail halfway.
- Good, because a player never encounters the words "service account".
- Good, because the authz path has one principal kind, so there is no second code path to secure.
- **Bad, because it diverges from the sibling project** on a rule that project states emphatically,
  so a reviewer moving between them carries the wrong instinct.
- **Bad, because a bot's token dies when its owning officer's membership is revoked.** That is
  correct default behaviour and it *will* surprise someone at the worst moment; ownership transfer
  has to exist and be documented before anyone relies on a bot.
- **Bad, because checking membership state on every request is a join on every request.** Cheap in
  SQLite at this scale, and still a cost the cascade-revoke design does not pay.

### Reversal cost

A day, plus a migration introducing service accounts and re-parenting existing tokens.
