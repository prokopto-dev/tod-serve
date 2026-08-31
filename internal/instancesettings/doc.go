// Package instancesettings reads and changes the instance-wide policy switches, and records every
// change in a ledger nothing can rewrite.
//
// It answers one question — "what has this operator decided about the whole instance" — and it is
// the only writer of `instance_setting_change`. What follows is what a reader has to know to
// change this package safely.
//
//   - **The current state is a ROW, and the history is a LEDGER.** `instance` stays mutable, so
//     `/meta` and every `createCircle` authorization check are one cheap read; the record of how
//     it got that way is a separate append-only, hash-chained table. Folding the two — deriving
//     the flag from the log — would put a fold over an unbounded list on the hot path, and
//     dropping the log would make an instance-wide policy change the one decision here nobody
//     could attribute. ADR-0020 is the argument.
//   - **It is its own audit record, because `audit_log.circle_id` is NOT NULL.** Turning
//     self-service circle creation on decides whether any authenticated stranger may create a
//     circle, which is exactly the event an audit log exists for, and an instance policy belongs
//     to no circle. That is the wall ADR-0012 hit for `instance_grant`; this is the same answer
//     rather than a reason to skip the audit. `instance_grant` cannot hold these rows either: it
//     is keyed on `(identity, permission)` and answers who may do what, not what somebody changed.
//     The chain is [audit.ChainHash] rather than a second implementation of it.
//   - **`public_url` is not here, and its absence is the design.** It must keep matching the
//     redirect URI registered with every identity provider character for character. A mismatch is
//     a sign-in that completes at the provider and lands somewhere else, leaving NO evidence on
//     the instance it was meant to reach — the failure #26 made loud at configuration time. It is
//     resolved once at boot from `$TOD_PUBLIC_URL` before the row, so a change here would take
//     effect at the next restart, on an instance whose operator had long since stopped connecting
//     the two. `schemaenum` leaves it out of the `setting` enum, so a row claiming it changed is
//     unrepresentable rather than merely unwritten.
//   - **A read of the settings is a read of THREE things at one instant** — the row, the ledger's
//     chain head and the ledger itself — because the entity tag is computed over the first two and
//     the third is returned beside them. [Service.Describe] takes one `InReadSnapshot` for that
//     reason; pooled statements would pair old settings with a new revision and refuse the
//     caller's next write for nothing. ADR-0014, and the shape of issue #17.
//   - **Nothing here is ordered by id.** A ULID is monotonic within one generator and a change can
//     arrive from any process holding the database, so the chain's tail is the row whose hash no
//     other row names — never the greatest id. Selecting it by id is what locked the instance
//     grant ledger permanently once.
//   - **A change with no changer is the console.** `changed_by_identity_id` is nullable and NULL
//     reads as "the operator at the database", which is a different fact from a person having
//     decided it — the same convention `instance_grant` uses, and the one first-run setup writes
//     under before any identity exists.
package instancesettings
