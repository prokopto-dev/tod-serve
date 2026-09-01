// The merge, driven directly.
//
// The case that made it a module rather than an expression inside the header: somebody renames a
// circle from Settings, and this browser's record still holds the old name from the last sign-in.
// A switcher that preferred the record would offer "Old Name — blue" beside a header reading the
// new one, and both would look authoritative.

import assert from 'node:assert/strict'
import test from 'node:test'

import { circleChoices } from './circles.ts'

const circle = (id: string, name = id, server = 'blue') => ({ id, name, server })

test('the listed row wins the name and the server over a remembered copy', () => {
  const got = circleChoices([circle('a', 'Renamed', 'green')], [circle('a', 'Stale', 'blue')], 'a')

  assert.deepEqual(got, [{ id: 'a', name: 'Renamed', server: 'green', current: true, live: true }])
})

test('a remembered circle the server did not list is offered, and marked not live', () => {
  const got = circleChoices([circle('a')], [circle('b'), circle('a')], 'a')

  assert.deepEqual(
    got.map((c) => [c.id, c.live]),
    [
      ['a', true],
      ['b', false],
    ],
  )
})

test('the current circle comes first however the sources ordered it', () => {
  const got = circleChoices([], [circle('b'), circle('c'), circle('a')], 'a')

  assert.deepEqual(
    got.map((c) => c.id),
    ['a', 'b', 'c'],
  )
  assert.deepEqual(
    got.map((c) => c.current),
    [true, false, false],
  )
})

test('the order after the current circle is the order it was given', () => {
  const got = circleChoices([circle('a')], [circle('d'), circle('c'), circle('b')], 'c')

  assert.deepEqual(
    got.map((c) => c.id),
    ['c', 'a', 'd', 'b'],
  )
})

test('nothing is current while the principal has not loaded', () => {
  const got = circleChoices([circle('a')], [circle('b')], '')

  assert.equal(
    got.every((c) => !c.current),
    true,
  )
  assert.equal(got.length, 2)
})

test('a circle listed twice is offered once', () => {
  const got = circleChoices([circle('a'), circle('a', 'Duplicate')], [], 'a')

  assert.deepEqual(got, [{ id: 'a', name: 'a', server: 'blue', current: true, live: true }])
})

test('no sources is no choices, and not a crash', () => {
  assert.deepEqual(circleChoices([], [], 'a'), [])
})
