// What the console remembers across an OAuth round trip, and what it refuses to.
//
// Driven directly rather than through a renderer, like every other test here: the decision this
// covers is a pure function, and the console has no renderer in its test setup.

import assert from 'node:assert/strict'
import test from 'node:test'

import { safeReturnTo } from './storage.ts'

// A step-up interrupts something, and the console comes back to it. Nothing external writes this
// value today — [StepUpProvider] writes `location.pathname` — and this is what keeps a future
// writer from turning "come back to where you were" into an open redirect.
test('safeReturnTo takes a console path and nothing that could leave the console', () => {
  assert.equal(safeReturnTo('/members', '/board'), '/members')
  assert.equal(safeReturnTo('/settings?tab=providers', '/board'), '/settings?tab=providers')

  // Protocol-relative: two slashes is a host, and `navigate()` would follow it.
  assert.equal(safeReturnTo('//evil.example/pwn', '/board'), '/board')
  assert.equal(safeReturnTo('https://evil.example', '/board'), '/board')
  assert.equal(safeReturnTo('javascript:alert(1)', '/board'), '/board')

  // Absent, empty, and relative-without-a-slash all fall back rather than guessing.
  assert.equal(safeReturnTo(undefined, '/board'), '/board')
  assert.equal(safeReturnTo('', '/board'), '/board')
  assert.equal(safeReturnTo('members', '/board'), '/board')
})
