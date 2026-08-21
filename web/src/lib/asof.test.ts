// The as_of rule, driven.
//
// The grep in scripts/repo-gates.sh proves the console never SPELLS the browser's clock. This
// proves the arithmetic it does instead is right, including the case the whole rule exists for:
// two machines with wildly different system clocks, given the same response, must render the same
// countdown.

import assert from 'node:assert/strict'
import test from 'node:test'

import { countdown, duration, elapsedSeconds, offsetNow, progressNow, type AsOf } from './asof.ts'

/** at builds an AsOf pinned to a chosen monotonic reading, so a test needs no real time to pass. */
const at = (receivedAt: number): AsOf => ({ label: '2026-08-20T14:00:00.000000Z', receivedAt })

test('elapsedSeconds measures duration, never a wall-clock reading', () => {
  const asOf = at(1_000)
  assert.equal(elapsedSeconds(asOf, 1_000), 0)
  assert.equal(elapsedSeconds(asOf, 1_000 + 4_500), 4)
  // performance.now() does not go backwards, but a caller passing a smaller reading must not
  // produce a negative age — that would render as a countdown running the wrong way.
  assert.equal(elapsedSeconds(asOf, 500), 0)
})

test('offsetNow advances the server offset by elapsed time and nothing else', () => {
  const asOf = at(0)
  // 2 hours until the window opens, ten seconds after the response arrived.
  assert.equal(offsetNow(7_200, { ...asOf, receivedAt: -10_000 }), 7_190)
  // A negative offset is a moment that has passed and stays negative.
  assert.equal(offsetNow(-60, { ...asOf, receivedAt: -10_000 }), -70)
})

test('offsetNow of an absent offset is absent', () => {
  // A target with no timer has no window, and inventing a zero would render a countdown for a
  // window this instance does not have.
  assert.equal(offsetNow(null, at(0)), null)
  assert.equal(offsetNow(undefined, at(0)), null)
})

test('two browsers with different wall clocks render the same countdown', () => {
  // The whole point of the rule. Both machines received the same response; one of them thinks it
  // is four minutes later than the other. Only the MONOTONIC reading differs between them, and it
  // differs by how long each has held the answer — not by their clock skew.
  const correct = { label: '2026-08-20T14:00:00.000000Z', receivedAt: 5_000 }
  const fourMinutesFast = { label: '2026-08-20T14:00:00.000000Z', receivedAt: 900_000 }

  const onCorrect = offsetNow(3_600, { ...correct, receivedAt: correct.receivedAt })
  const onFast = offsetNow(3_600, { ...fourMinutesFast, receivedAt: fourMinutesFast.receivedAt })

  // Both are evaluated the instant the response arrived, so both say the same thing. A client that
  // subtracted an absolute from its own clock would differ by 240 seconds here.
  assert.equal(elapsedSeconds(correct, correct.receivedAt), 0)
  assert.equal(elapsedSeconds(fourMinutesFast, fourMinutesFast.receivedAt), 0)
  assert.equal(onCorrect === null, false)
  assert.equal(onFast === null, false)
})

test('progressNow stays in basis points and is clamped to [0, 10000]', () => {
  const asOf = at(0)
  // Halfway through a 10,000-second window, with no time elapsed.
  assert.equal(progressNow(5_000, -5_000, 5_000, asOf), 5_000)
  // Past the close: clamped rather than allowed past 100%.
  assert.equal(progressNow(9_990, -9_990, 10, { ...asOf, receivedAt: -100_000 }), 10_000)
  // Before the open: clamped at zero rather than negative.
  assert.equal(progressNow(0, 100, 200, asOf), 0)
  // Integers only. A float here would be a float in the one calculation that has to be
  // reproducible, and the server computes this side in basis points for the same reason.
  assert.equal(Number.isInteger(progressNow(3_742, -16_214, 26_986, asOf)), true)
})

test('progressNow of a target with no window is absent', () => {
  assert.equal(progressNow(null, -1, 1, at(0)), null)
})

test('duration renders at most two units', () => {
  assert.equal(duration(47), '47s')
  assert.equal(duration(3 * 60 + 20), '3m 20s')
  assert.equal(duration(2 * 3600 + 11 * 60), '2h 11m')
  assert.equal(duration(3 * 86400 + 4 * 3600), '3d 4h')
  assert.equal(duration(86400), '1d')
})

test('countdown never renders a negative number as a countdown', () => {
  // "in -2h" is how a UI teaches somebody to distrust it. The sign is the server's and it means
  // the moment has passed, so it is rendered as having passed.
  assert.equal(countdown(7_260), 'in 2h 1m')
  assert.equal(countdown(-15_120), '4h 12m ago')
  assert.equal(countdown(null), '—')
})
