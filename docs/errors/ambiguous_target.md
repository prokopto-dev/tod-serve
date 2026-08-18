# `ambiguous_target`

**HTTP 422** · `type: https://docs.tod-serve.org/errors/ambiguous_target`

The name matched more than one target and the server **will not guess**.

## What causes it

- A short or partial name matching several mobs at the same rung of the ladder — the tie is what
  makes it ambiguous. An exact hit is never ranked below a substring hit, so this only fires when
  the matches are genuinely equal in strength.

## What the client should do

Read `meta.candidates[]` on the problem response — it carries the tied targets — and re-send with an
explicit `target_id`. A UI should present the candidates for a person to pick. Guessing here would
attach a real kill to the wrong mob and put a confidently wrong window on the board, which is worse
than an error.
