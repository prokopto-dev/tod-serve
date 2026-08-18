# `died_at_too_old`

**HTTP 422** · `type: https://docs.tod-serve.org/errors/died_at_too_old`

`died_at` is more than **90 days** in the past.

## What causes it

- Almost always a timezone or epoch bug — a unit mix-up, or a date defaulting to something far
  in the past. It is very rarely a real backfill.

## What the client should do

Check the parse. Backdating is normal and supported — `died_at` is game truth and routinely lags
`reported_at` by hours — but a 90-day-old ToD is not useful intel for any raid target, so the value
is rejected rather than silently recorded.
