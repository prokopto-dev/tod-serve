# `died_at_in_future`

**HTTP 422** · `type: https://docs.tod-serve.org/errors/died_at_in_future`

`died_at` is later than now, beyond the **120-second** clock-skew tolerance.

## What causes it

- The reporting machine's clock is fast.
- A timezone bug that pushed the parsed timestamp forward.
- A hand-typed date with the wrong day.

## What the client should do

Fix the clock or the parse. **This is the only hard rejection on a time in the whole product**, and
it is hard because a death in the future is impossible independent of any derivation — unlike an
implausible ordering, which is flagged and kept. Send `client_clock_offset_seconds` so the server
can see your skew estimate.
