# `server_mismatch`

**HTTP 422** · `type: https://docs.tod-serve.org/errors/server_mismatch`

The `server` in the request body does not match the circle's. A circle is pinned to one server —
`blue`, `green` or `red` — permanently.

## What causes it

- A fan-out client reported to the wrong destination: you are playing Blue and had the Green
  destination ticked. **This is the exact failure the echoed `server` field exists to catch.**
  Without it, the wrong data would land silently and quietly corrupt a board.
- An attempt to change `circle.server`, which is immutable — that is `field_immutable`.

## What the client should do

Fix the destination, not the payload. There is no combined view anywhere in this product and a
Blue ToD says nothing about Green, so "just accept it" would be a wrong answer, not a lenient one.
