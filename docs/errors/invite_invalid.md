# `invite_invalid`

**HTTP 404** · `type: https://docs.tod-serve.org/errors/invite_invalid`

No live invite matches that code. `404` rather than `403` on purpose: a `403` would confirm that a
code is *shaped* right, which turns this endpoint into an oracle.

## What causes it

- A typo. Codes are `TODI-XXXXX-XXXXX` in Crockford base32 **without `I`, `L`, `O` or `U`** — if
  you typed one of those, you have transcribed a `1`, `1`, `0` or `V`.
- The invite was deleted, or never existed on this instance. Codes are instance-unique.
- You are pointed at the wrong instance.

## What the client should do

Check the code and the instance URL. Do not retry in a loop: `previewInvite` is hard rate-limited
precisely because guessing is the attack this shape defends against.
