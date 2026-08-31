// The contract between the screen that signs out and the screen that says so.
//
// It is driven directly rather than through a renderer, for the same reason `resource.test.ts` is:
// everything that decides what reaches the component is a pure function, and the console has no
// renderer in its test setup. What that does NOT cover is stated plainly — that the confirmation
// is rendered by the DESTINATION rather than by the button is a structural fact about
// `Shell.tsx` and `SignIn.tsx`, and no test here can hold it. The shape below is what makes it
// cheap to keep: there is nowhere in `signedOutState` for a component to stash the count and then
// unmount before drawing it.

import assert from 'node:assert/strict'
import test from 'node:test'

import { readSignedOut, signedOutMessage, signedOutState } from './signedOut.ts'

test('what a sign-out writes is what the destination reads', () => {
  assert.deepEqual(readSignedOut(signedOutState(2)), { tokensKept: 2 })
  assert.deepEqual(readSignedOut(signedOutState(0)), { tokensKept: 0 })
})

// The state comes back out of the browser's own history, so every one of these is reachable
// without a bug: a plain navigation carries null, a back button can restore an older build's
// shape, and a hand-edited entry can carry anything at all.
test('anything that is not a sign-out renders nothing', () => {
  const notSignOuts: unknown[] = [
    null,
    undefined,
    'signedOut',
    42,
    {},
    { signedOut: null },
    { signedOut: 'yes' },
    { signedOut: {} },
    { signedOut: { tokensKept: '2' } },
    { signedOut: { tokensKept: 1.5 } },
    { signedOut: { tokensKept: -1 } },
    { signedOut: { tokensKept: Number.NaN } },
    // The shape a previous version of this console might have written.
    { tokensKept: 2 },
  ]
  for (const state of notSignOuts) {
    assert.equal(readSignedOut(state), null, `read ${JSON.stringify(state)} as a sign-out`)
  }
})

// A confirmation that reads "1 API tokens" is one somebody stops believing, which defeats the
// point of saying it: the number is there to reassure a raider that their plugin still works.
test('the sentence agrees with itself at nought, one and many', () => {
  assert.match(signedOutMessage({ tokensKept: 0 }), /no API tokens/)
  assert.match(signedOutMessage({ tokensKept: 1 }), /1 API token still works/)
  assert.match(signedOutMessage({ tokensKept: 4 }), /4 API tokens still work/)
  for (const kept of [0, 1, 4]) {
    assert.match(signedOutMessage({ tokensKept: kept }), /never revokes one/,
      'every wording says sign-out revokes no token; that is the whole reason the count is shown')
  }
})
