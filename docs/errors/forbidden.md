# `forbidden`

**HTTP 403** · `type: https://docs.tod-serve.org/errors/forbidden`

You are authenticated, the circle is yours, and your **role** does not hold the permission this
operation requires.

## What causes it

Effective capability is `role permissions ∩ token scopes`. This is a failure of the left-hand side:
an observer calling `createTodReport`, a member calling `revokeMember`. The operation's
`x-tod-permission` in the OpenAPI document names the permission and the roles that hold it.

A wrong *circle* is [`not_found`](not_found.md), never this — see
[canonical §7](../design/00-canonical-conventions.md#cross-circle-access-returns-404-never-403).

**One family of causes is not a permission failure**, and it is the exception rather than a second
meaning: a role move through `updateMember`, refused by *who is asking* rather than by what they
hold. The caller has `member.manage` in all three cases, and `detail` says which one it is.

- **Above your own role.** You may not grant a role stronger than the one you hold, to anybody,
  yourself included. It is the guard that keeps becoming an owner deliberate.
- **Your own row.** Your own role is not yours to change. Handing over ownership is promoting
  somebody else — that leaves the circle administered at every instant, and a self-demotion is not
  its mirror image.
- **Above your own role, held by somebody else.** You may not change the role of a member who
  outranks you. Equal is not above, so an owner may still change another owner's, which is how a
  handover completes.

## What the client should do

Ask an officer for a role that holds the permission. Retrying, re-authenticating and minting a
wider token all change nothing: a token can only narrow what the role already grants.

For a refused role move, ask somebody who holds at least the role in question — and to stop being an
owner, promote a successor and ask them. Repeating the request cannot succeed: none of the three
depends on the circle's state, which is what tells them apart from
[`last_owner`](last_owner.md).
