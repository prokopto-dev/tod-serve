// Which circles this browser can offer, and which one the session is actually in.
//
// There are TWO sources and they answer different questions, which is why this is a merge rather
// than a pick:
//
//   `listCircles`          the server's answer, and today it is exactly one row: a principal is
//                          bound to one membership, so the operation returns the circle this
//                          session is in and nothing else. There is no list-all operation at any
//                          permission level — a circle's existence is part of what it hides.
//   `rememberedCircles()`  this browser's own record, written every time somebody signs into a
//                          circle here. It is the ONLY thing that knows about the other circles a
//                          person belongs to, because no route will tell us.
//
// Reconciled rather than kept side by side: the listed row is authoritative for the name and the
// server, so a circle renamed since the last sign-in reads correctly. What the record adds is
// existence, not detail. Two independent notions of "my circles" is how they drift, and the drift
// is silent — a switcher offering a stale name is a switcher somebody trusts.
//
// A remembered entry can be wrong in ways nothing here can see: the circle may have been deleted,
// or the membership revoked. That is why [CircleChoice.live] is on the shape rather than inferred
// — the screen says which rows the server just confirmed and which came out of a drawer.

import type { RememberedCircle } from './storage.ts'

/** CircleChoice is one row of the switcher. */
export interface CircleChoice extends RememberedCircle {
  /** current is the circle this session is bound to. Exactly one, or none while it loads. */
  current: boolean
  /**
   * live means the server named this circle on this request, so the name and the server are what
   * they are right now. A choice that is not live comes from this browser's record: it may have
   * been renamed, deleted, or had this person's membership revoked, and signing into it is the
   * only thing that finds out.
   */
  live: boolean
}

/**
 * circleChoices merges the server's answer with the browser's record, current circle first.
 *
 * `Array.prototype.sort` is stable, so everything after the current circle keeps the order it was
 * given: listed rows before remembered ones, each in its own source order — which for the record
 * is most-recently-signed-into first.
 */
export function circleChoices(
  listed: readonly RememberedCircle[],
  remembered: readonly RememberedCircle[],
  currentID: string,
): CircleChoice[] {
  const seen = new Set<string>()
  const out: CircleChoice[] = []

  const add = (circle: RememberedCircle, live: boolean) => {
    if (seen.has(circle.id)) return
    seen.add(circle.id)
    out.push({
      id: circle.id,
      name: circle.name,
      server: circle.server,
      current: circle.id === currentID,
      live,
    })
  }

  // The server first, so a listed row wins the name and the server over a remembered copy of it.
  for (const circle of listed) add(circle, true)
  for (const circle of remembered) add(circle, false)

  return out.sort((a, b) => Number(b.current) - Number(a.current))
}

/**
 * serverIsAmbiguous reports whether two of these circles sit on the same server.
 *
 * **A server does not identify a circle, and the schema is where that is settled.** `membership`
 * carries no `server` column and no per-server uniqueness — `ux_membership_identity` is unique on
 * `(circle_id, identity_id)` — so one person may hold any number of memberships, several of them
 * on one server: a guild circle and an alliance circle both on Blue is an ordinary case, not an
 * edge one. The only server-scoped uniqueness anywhere is `ux_circle_name_norm_server`, on the
 * NAME.
 *
 * The switcher shows the name and the server on every row regardless. This exists so that the one
 * case where the server chip settles nothing can say so, instead of leaving somebody to work out
 * why two rows carry the same badge.
 */
export function serverIsAmbiguous(choices: readonly CircleChoice[]): boolean {
  const seen = new Set<string>()
  for (const choice of choices) {
    if (seen.has(choice.server)) return true
    seen.add(choice.server)
  }
  return false
}
