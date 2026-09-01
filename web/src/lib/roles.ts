// The role field on the Members screen: whose role this caller may change, and to what.
//
// Pure — no React, no transport — for the same reason `gate.ts` is: what decides the request this
// console makes is worth driving directly rather than through a renderer. `roles.test.ts` is that
// drive.
//
// It mirrors the three standing rules `internal/membership` applies to a role move, and it exists
// because a dropdown offering options the server answers `403` to is the affordance version of the
// bug those rules close. The most dangerous of them was reported from real use: an owner picked
// `officer` from their own row and the server took it. A field is not enforcement — the server is,
// and `TestUpdateMember_ChangingYourOwnRoleOrAnOwnersRole_Is403` holds it — but a control the
// server will always refuse is a trap, and this one is a trap somebody fell into.

/** ROLES is the enum, weakest first. The order IS the ranking, as it is in `internal/schemaenum`. */
export const ROLES = ['observer', 'member', 'officer', 'owner'] as const

export type Role = (typeof ROLES)[number]

/** RoleField is what the Role cell renders: a dropdown, or the role as text and why. */
export interface RoleField {
  /** options is what the dropdown may offer. EMPTY means the field is not offered at all. */
  options: Role[]
  /**
   * note is the tag shown beside the role, and reason is that tag's title.
   *
   * Both are empty for the ordinary cases — no `member.manage`, or a revoked member — because the
   * whole column is read-only there and a marker on every row is noise. They are set exactly where
   * a caller who CAN manage roles finds one they cannot, which is the moment a control that is
   * simply absent reads as a bug. Never hide a row silently: this is the same rule applied to a
   * field.
   */
  note: string
  reason: string
}

// A function rather than a shared constant: every caller gets its own `options`, so a component
// that sorted or spliced the array it was handed could not reach the next row through it.
const notOffered = (): RoleField => ({ options: [], reason: '', note: '' })

function rank(role: string): number {
  return (ROLES as readonly string[]).indexOf(role)
}

/**
 * roleField decides the Role cell for one member.
 *
 * The rules, in the order `membership.Service.Update` applies them:
 *
 *   1. You may not grant a role above your own — so the options stop at the one you hold.
 *   2. Your own role is not yours to change. Ownership is handed over by promoting somebody else,
 *      which is still offered; demoting yourself is not, and never was symmetric with it.
 *   3. You may not change the role of somebody who outranks you. An officer demoting an owner gains
 *      nothing, and is still an officer removing their own supervisor.
 *
 * An unknown role — the caller's or the member's — offers nothing and says nothing. It cannot be
 * reached from a principal this server issued, and guessing a ranking for it is how a control ends
 * up offered on the strength of a string nobody recognised.
 */
export function roleField(opts: {
  canManage: boolean
  revoked: boolean
  myRole: string
  myMembershipID: string
  member: { id: string; role: string }
}): RoleField {
  if (!opts.canManage || opts.revoked) return notOffered()

  const mine = rank(opts.myRole)
  const theirs = rank(opts.member.role)
  if (mine < 0 || theirs < 0) return notOffered()

  if (opts.member.id === opts.myMembershipID) {
    return {
      options: [],
      note: 'you',
      reason:
        'Your own role is not yours to change. Hand over ownership by promoting somebody else, ' +
        'who can then change yours.',
    }
  }
  if (theirs > mine) {
    return {
      options: [],
      note: 'outranks you',
      reason: `You hold ${opts.myRole} and cannot change the role of ${opts.member.role}: a role is changed by somebody who holds at least it.`,
    }
  }

  const options = ROLES.slice(0, mine + 1)
  // A dropdown whose only entry is the role already showing is a control that does nothing. It
  // needs no explanation either — nothing is being withheld.
  if (!options.some((role) => role !== opts.member.role)) return notOffered()
  return { options: [...options], reason: '', note: '' }
}
