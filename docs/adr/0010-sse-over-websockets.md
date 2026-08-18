# ADR-0010 — SSE, not WebSockets, for live board updates

**Status:** accepted · **Date:** 2026-08-18 · **Deciders:** Courtney Caldwell

## Context and problem statement

A ToD board is only useful if it is live. When someone reports a kill, every other client in the
circle should see the window move without a refresh — and a window crossing `open_at` is exactly the
moment people care about.

The traffic is entirely server-to-client. Clients write by POSTing reports, not by pushing frames.

## Considered options

| Option | For | Against |
|---|---|---|
| A — Server-Sent Events | One-directional, which is the actual shape. Plain HTTP, so it passes proxies and needs no upgrade handling. Reconnect and replay are in the protocol via `Last-Event-ID` | Text only, and browsers historically cap concurrent connections per origin |
| B — WebSockets | Bidirectional, binary-capable, one connection for everything | We have nothing to send upstream. It brings framing, ping/pong, upgrade handling and a second auth path, and reconnect-with-replay has to be built by hand |
| C — Polling | Trivial, no connection state | Either stale or wasteful, and a board people watch during a spawn window is exactly where the trade is worst |

## Decision outcome

**Chosen: A**, matching Dragon Kill Party.

`GET /circles/{cid}/events` streams `tod.changed`, `report.created`, `quake.reported` and
`member.revoked`, with the global `event_seq` as the frame `id:`. `GET /circles/{cid}/events/replay`
takes `?since_seq=` — the only endpoint where that parameter is legal — so a client that was
disconnected catches up deterministically instead of re-fetching the board.

`tod.changed` carries `change_reason ∈ { new_kill, corroboration, retraction, quake, timer_change }`,
because the derivation is non-monotonic: a backfilled corroboration shifts the median with no new
kill, and a UI that cannot explain why the number moved is worse than one that does not move it.

Cancellation on client disconnect is what keeps fan-out bounded; every store call and every stream
takes the caller's `ctx`. `internal/events` gets `goleak.VerifyTestMain` — a leaked tailer goroutine
is invisible in a green run and shows up three weeks later as RSS growth on someone's Raspberry Pi.

### Consequences

- Good, because reconnect and replay are protocol features rather than code we own.
- Good, because it is ordinary HTTP, so a reverse proxy in front of a home server needs no special
  configuration.
- Good, because there is one auth path — the same bearer token as every other request.
- **Bad, because the browser per-origin connection cap** means a user with the board open in several
  tabs can exhaust it; a shared worker is the fix and it is not free.
- **Bad, because SSE is text-only**, so any future binary payload needs base64 or a second channel.
- **Bad, because a long-lived connection per viewer is a resource an officer's home PC pays for**,
  and nothing about the design bounds how many viewers a circle has.

### Reversal cost

A release. The event catalogue and `event_seq` transfer unchanged to any transport; only the edge and
the client reconnect logic are rewritten.
