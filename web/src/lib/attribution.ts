// Who reported a time of death — and the three genuinely different answers to that question.
//
// Pure, and a module rather than a ternary in `TargetDetail.tsx`, because the ternary it replaces
// was wrong and nothing could drive it. The screen read the Reporters card off `reporters` alone:
// present meant name them, absent meant explain that attribution needs `tod.read.attribution`.
// But the server omits that field for a principal WITHOUT the permission and for a target nobody
// has reported yet — identical wire shapes, two unrelated conditions. On a fresh instance every
// target told its owner they lacked a permission they hold (issue #52).
//
// The permission now travels as its own field, `attribution_visible`, and this module is what
// stops the next reader re-deriving it from emptiness: there is one place that decides, it takes
// the flag first and the data second, and `attribution.test.ts` drives both directions. A test
// that drove only one of them would have passed for the whole life of the bug.

import type { Reporter, TargetStateResponse } from '../api'

/**
 * Attribution is what the Reporters card should say.
 *
 * `empty` and `denied` are separate members on purpose. Collapsing them is the bug: one is a fact
 * about the target ("nobody has reported this yet") and the other is a fact about the caller
 * ("you are an observer"), and a card that says the second when the first is true is telling
 * somebody something false about their own permissions.
 */
export type Attribution =
  | { kind: 'named'; reporters: Reporter[] }
  | { kind: 'empty' }
  | { kind: 'denied' }

/** The subset of a target state this decision reads. Named so the test needs no whole response. */
export type AttributionSource = Pick<TargetStateResponse, 'attribution_visible' | 'reporters'>

/**
 * attributionOf decides from the permission first, and only then from the data.
 *
 * The order is the point. `reporters` is never consulted to answer "may this principal see
 * attribution" — `attribution_visible` is the only thing that answers that, and an empty list
 * under a true flag is a target with no reports.
 */
export function attributionOf(state: AttributionSource): Attribution {
  if (!state.attribution_visible) return { kind: 'denied' }
  const reporters = state.reporters ?? []
  return reporters.length > 0 ? { kind: 'named', reporters } : { kind: 'empty' }
}
