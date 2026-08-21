// The one file permitted to parse a timestamp, driven.

import assert from 'node:assert/strict'
import test from 'node:test'

import { hasInstant, plural, secondsBetweenServerInstants, titleCase } from './format.ts'

test('hasInstant tells an absent instant from a present one', () => {
  // The document types several nullable instants as plain `date-time` strings — a `*core.Micros`
  // in Go reaches the schema without its null branch — so the console checks rather than trusting
  // the generated type. Rendering `Invalid Date` for a target nobody has reported is exactly the
  // confident mistake this project is built against.
  assert.equal(hasInstant('2026-08-20T14:00:00.000000Z'), true)
  assert.equal(hasInstant(''), false)
  assert.equal(hasInstant(null), false)
  assert.equal(hasInstant(undefined), false)
})

test('secondsBetweenServerInstants subtracts two of the server’s own instants', () => {
  // "died 4h ago", computed from `died_at` against the SAME response's `as_of`. Neither operand
  // is the browser's clock.
  assert.equal(
    secondsBetweenServerInstants('2026-08-20T10:00:00.000000Z', '2026-08-20T14:00:00.000000Z'),
    -14_400,
  )
  // A backdated report is ordinary here: `died_at` is game truth and may be hours old.
  assert.equal(
    secondsBetweenServerInstants('2026-08-20T14:00:00.000000Z', '2026-08-20T14:00:00.000000Z'),
    0,
  )
})

test('secondsBetweenServerInstants of an unparseable instant is absent, not zero', () => {
  // Zero would render as "died just now", which is a confident mistake. Absent renders as "—".
  assert.equal(secondsBetweenServerInstants('', '2026-08-20T14:00:00.000000Z'), null)
  assert.equal(secondsBetweenServerInstants('not a date', '2026-08-20T14:00:00.000000Z'), null)
})

test('titleCase renders a snake_case wire value without a translation layer', () => {
  assert.equal(titleCase('thin_supersede'), 'thin supersede')
  assert.equal(titleCase('no_timer'), 'no timer')
})

test('plural', () => {
  assert.equal(plural(1, 'report'), '1 report')
  assert.equal(plural(4, 'report'), '4 reports')
  assert.equal(plural(0, 'use'), '0 uses')
})
