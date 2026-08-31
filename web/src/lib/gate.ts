// A circle's identity gate: what it admits, and what a save is actually allowed to send.
//
// Pure — no React, no transport — for the same reason `../app/resource.ts` is: what decides the
// request this console makes is worth driving directly rather than through a renderer. `gate.test.ts`
// is that drive.
//
// The rule that is easy to get wrong, and did: `setCircleProviders` is a PUT that REPLACES the
// set, and the server refuses any entry naming a provider the instance has disabled —
// `409 provider_disabled`, `internal/circle/providers.go`. A circle can nonetheless still accept
// one, because disabling happens instance-side after the fact. So a disabled provider can neither
// be re-sent nor kept: sending it fails the whole write, and omitting it deletes the row. There is
// no third outcome available, which is why [saveSet] reports what a save will DROP rather than
// quietly dropping it.

import type { Circle, ProviderView, PublicIdentityProvider } from '../api'

/**
 * Choice is one provider a circle could accept, and the gate configured on it.
 *
 * A local shape rather than the wire's `ProviderView` because the two sources it is built from
 * carry different halves: the public provider list knows a provider exists and is enabled, and
 * `accepted_providers` knows the guild and roles this circle set on it. The write body needs only
 * the key and the gate, so nothing here invents a `provider_id` it never read.
 */
export interface Choice {
  key: string
  kind: string
  display_name: string
  verifiable_subject: boolean
  /** available is false for a provider the INSTANCE has disabled since this circle accepted it. */
  available: boolean
  accepted: boolean
  discord_guild_id: string
  discord_required_role_ids: string[]
}

/** GateState is what a Discord gate actually admits. Three states, per `db/schema.hcl`. */
export type GateState = 'none' | 'guild' | 'roles'

export function gateState(choice: Choice): GateState {
  if (!choice.discord_guild_id.trim()) return 'none'
  return choice.discord_required_role_ids.length > 0 ? 'roles' : 'guild'
}

/**
 * choicesFor merges the instance's enabled providers with the ones this circle already accepts.
 *
 * The union rather than the public list alone: `listIdentityProviders` returns only ENABLED
 * providers, so a provider the operator disabled AFTER this circle accepted it would otherwise
 * vanish from the screen while still being a row on the circle. It comes back with
 * `available: false` and the screen says so. Never hide a row silently.
 */
export function choicesFor(circle: Circle, available: PublicIdentityProvider[]): Choice[] {
  const accepted = new Map<string, ProviderView>()
  for (const p of circle.accepted_providers ?? []) accepted.set(p.key, p)

  const out: Choice[] = available.map((p) => ({
    key: p.key,
    kind: p.kind,
    display_name: p.display_name,
    verifiable_subject: p.verifiable_subject,
    available: true,
    accepted: accepted.has(p.key),
    discord_guild_id: accepted.get(p.key)?.discord_guild_id ?? '',
    discord_required_role_ids: accepted.get(p.key)?.discord_required_role_ids ?? [],
  }))

  const listed = new Set(out.map((c) => c.key))
  for (const [key, p] of accepted) {
    if (listed.has(key)) continue
    out.push({
      key,
      kind: p.kind,
      display_name: p.display_name,
      verifiable_subject: p.verifiable_subject,
      available: p.available,
      accepted: true,
      discord_guild_id: p.discord_guild_id ?? '',
      discord_required_role_ids: p.discord_required_role_ids ?? [],
    })
  }
  return out
}

/** SaveSet is what one press of Save sends, and what that press costs. */
export interface SaveSet {
  /** send is the entries the request body carries. */
  send: Choice[]
  /**
   * dropped is accepted providers the instance has disabled, which the request CANNOT carry.
   *
   * They are named to the owner rather than filtered away. A save silently un-accepting a
   * provider is the confident mistake this project is built against: the owner came to rename a
   * role list and would leave having changed which identities this circle takes.
   */
  dropped: Choice[]
  /**
   * acknowledgeWeak mirrors the server's own test, which it applies to the providers IN THE
   * REQUEST — so it is derived from `send`, not from everything ticked. Deriving it from the
   * wider set would send the acknowledgement for a weak provider that is not being accepted,
   * which is a claim about a decision nobody made.
   */
  acknowledgeWeak: boolean
}

/**
 * saveSet splits a draft into what can be written and what a write gives up.
 *
 * The exclusion is not cosmetic. Leaving a disabled provider in the body makes the server refuse
 * the WHOLE put with `409 provider_disabled`, so an owner could not save an unrelated change to
 * any other provider until they noticed the disabled row and unticked it themselves.
 */
export function saveSet(draft: Choice[]): SaveSet {
  const accepted = draft.filter((c) => c.accepted)
  const send = accepted.filter((c) => c.available)
  return {
    send,
    dropped: accepted.filter((c) => !c.available),
    acknowledgeWeak: send.some((c) => !c.verifiable_subject),
  }
}
