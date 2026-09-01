// Both directions, because one direction passes while the bug is open.
//
// The shipped defect was a permitted owner on a target with no reports being shown the observer's
// permission copy. A test that only asserted "an observer sees the denial" was true throughout —
// so is one that only asserts "a member with reporters sees names". The pair below is the gate:
// remove `attribution_visible` from the decision and one of them goes red whichever way the
// mistake is made.

import assert from 'node:assert/strict'
import test from 'node:test'

import { attributionOf, type AttributionSource } from './attribution.ts'

const tankguy = { membership_id: '01K3TGT8N9M4X0Q7R2VB6C5D1E', display_name: 'Tankguy', revoked: false }
const sneakco = { membership_id: '01K3TGT8N9M4X0Q7R2VB6C5D2F', display_name: 'Sneakco', revoked: true }

test('an owner on a target nobody has reported is empty, NOT denied', () => {
  // The reported bug, at the console end. `reporters` is absent for a permitted principal whenever
  // there is nothing to name, which on a fresh instance is every target.
  const state: AttributionSource = { attribution_visible: true }
  assert.deepEqual(attributionOf(state), { kind: 'empty' })
})

test('and an explicit empty list from the server is the same answer', () => {
  // The server omits the field today. It is one `omitempty` away from sending `[]` instead, and
  // this decision must not change if it does.
  assert.deepEqual(attributionOf({ attribution_visible: true, reporters: [] }), { kind: 'empty' })
  assert.deepEqual(attributionOf({ attribution_visible: true, reporters: null }), { kind: 'empty' })
})

test('an observer on a target WITH reports is denied, not empty', () => {
  // The other direction, and the one the permission copy exists for: there is real attribution
  // behind this target and this caller may not see it.
  const state: AttributionSource = { attribution_visible: false }
  assert.deepEqual(attributionOf(state), { kind: 'denied' })
})

test('a false flag wins even if the server ever sent reporters alongside it', () => {
  // Defence in depth against the inverse of the original bug: the permission is the authority, so
  // data arriving under a false flag is never rendered as attribution.
  assert.deepEqual(attributionOf({ attribution_visible: false, reporters: [tankguy] }), {
    kind: 'denied',
  })
})

test('a permitted principal with reporters gets them, revocation intact', () => {
  assert.deepEqual(attributionOf({ attribution_visible: true, reporters: [tankguy, sneakco] }), {
    kind: 'named',
    reporters: [tankguy, sneakco],
  })
})
