// Package sweep removes rows that have outlived their expiry.
//
// Three tables hold litter rather than history: `auth_flow` (one in-flight OAuth authorization),
// `credential_ticket` (a verified subject for 120 seconds) and `idempotency_record` (a request
// and the response it replays). Every row in all three carries `expires_at`, every reader already
// refuses a row past it, and nothing deleted them — so they grew without bound. The domain model
// calls the first two "mutable, prunable"; this is the pruning, which is why those tables have a
// DELETE and `tod_report` does not.
//
// # Why a command rather than a goroutine
//
// There is no in-process job runner in this repository and this package does not add one. Periodic
// maintenance here is an operator command driven by an external scheduler — `tod-serve
// verify-states` is the established shape — so the sweep is `tod-serve sweep`, wired the same way.
// A ticker inside `serve` would need a clock that can wake up, which [clock.Clock] deliberately
// cannot do, and would put a recurring write on a process whose restarts are not the schedule
// anybody reasoned about.
//
// It is deliberately NOT folded into `verify-states`. That command exits non-zero when it repaired
// something, because a repair means the cache drifted and somebody must look; deleting expired
// litter is the routine healthy case and must never page anyone. Sharing the exit code would make
// a nightly alert fire nightly, which is the cry-wolf failure `TestVerify_AHealthyInstance_IsSilent`
// exists to prevent. The two also want different cadences — a 120-second ticket and a daily cache
// diff are not the same clock — and verify walks the projection while this walks auth litter.
//
// # Why a grace period
//
// The sweep deletes rows that expired longer ago than [Grace], not rows that merely expired. The
// reason is that for one of the three tables a deleted row and an expired row are not the same
// answer: `identity.Service.RedeemProviderTicket` reports `auth_ticket_expired` for a ticket it can
// still see and `auth_ticket_invalid` for one it cannot, and that distinction is the whole reason
// it reads before it consumes. Sweeping at the instant of expiry would quietly downgrade the error
// a late redeemer reads. `auth_flow` answers `auth_flow_expired` either way and the idempotency
// path deletes an expired record on sight, so the grace costs those two nothing and buys the third
// its error message. A table holding [Grace] of litter is still bounded, which is the point.
package sweep
