// What a screen is shown, driven.
//
// The hooks are thin wrappers around [nextSettled] and [view]; everything that decides what
// reaches a component is in those two pure functions, so this drives them directly rather than
// through a renderer. The scenarios below are the ones that have actually gone wrong.

import assert from 'node:assert/strict'
import test from 'node:test'

import { nextSettled, runOf, view, type Settled } from './resource.ts'

/** Board is a stand-in for the board's page envelope: enough to tell one answer from another. */
interface Board {
  as_of?: string
  rows: string[]
}

const board = (asOf: string, ...rows: string[]): Board => ({ as_of: asOf, rows })

/** everything and inWindow are two different QUERIES, as the board's filters produce them. */
const everything = runOf([{ status: '' }], 0)
const inWindow = runOf([{ status: 'in_window' }], 0)

/** settle folds a whole sequence, so a test reads as the thing that happened. */
function settle(start: Settled<Board> | null, steps: Array<[typeof everything, Parameters<typeof nextSettled<Board>>[2]]>) {
  return steps.reduce<Settled<Board> | null>(
    (prev, [run, outcome]) => nextSettled(prev, run, outcome),
    start,
  )
}

test('a filter change whose first request fails does not relabel the previous filter’s rows', () => {
  // THE REGRESSION. An officer is looking at the whole board, filters to `in_window`, and the
  // request fails. Before this was fixed the previous rows were carried across and retagged with
  // the new query: `loading` false, `error` null, and the UNFILTERED board rendered under the
  // filtered URL with nothing but a subtitle to say so. An officer filtering to a critical status
  // during a transient failure would act on rows that do not match what they asked for.
  const settled = settle(null, [
    [everything, { kind: 'data', data: board('T0', 'Vulak', 'Trakanon', 'Nagafen') }],
    [inWindow, { kind: 'error', error: new Error('the request did not reach the server') }],
  ])

  const shown = view(settled, inWindow)
  assert.equal(shown.data, null, 'the previous query’s rows were shown under the new filter')
  assert.equal(shown.loading, false, 'the failure IS the answer to this query')
  assert.equal(shown.stale, false, 'nothing is being kept, so nothing is stale')
  assert.equal(shown.staleError, null, 'nothing is being kept, so nothing is out of date')
  assert.match(String(shown.error?.message), /did not reach the server/)
})

test('a failed attempt at the SAME query keeps its rows and RECORDS why it failed', () => {
  // The other half, and the reason the carry exists at all: blanking a board because one poll
  // failed is how somebody loses the window they were watching.
  //
  // But keeping them SILENTLY was its own bug. Every explicit `reload()` in this console follows a
  // write — a retraction, a revocation, a role change — so a swallowed failure leaves somebody
  // looking at the state from before their own action and believing it is current. The rows stay;
  // the reason they are not current is now on the resource, and `StaleNotice` renders it.
  const settled = settle(null, [
    [everything, { kind: 'data', data: board('T0', 'Vulak', 'Trakanon') }],
    [everything, { kind: 'error', error: new Error('the request did not reach the server') }],
  ])

  const shown = view(settled, everything)
  assert.deepEqual(shown.data?.rows, ['Vulak', 'Trakanon'])
  assert.equal(shown.stale, true, 'kept rows must say they are not current')
  assert.equal(shown.error, null, 'the rows ARE the answer; nothing is missing from the screen')
  assert.match(String(shown.staleError?.message), /did not reach the server/,
    'a failed refresh that nobody can see is a failed refresh nobody acts on')
  assert.equal(shown.loading, false)
})

test('a refresh that succeeds after a failed one clears the staleness', () => {
  // Otherwise the notice outlives the problem, and a warning that is always on is a warning nobody
  // reads.
  const settled = settle(null, [
    [everything, { kind: 'data', data: board('T0', 'Vulak') }],
    [everything, { kind: 'error', error: new Error('the request did not reach the server') }],
    [everything, { kind: 'data', data: board('T1', 'Vulak', 'Trakanon') }],
  ])

  const shown = view(settled, everything)
  assert.equal(shown.stale, false)
  assert.equal(shown.staleError, null)
  assert.deepEqual(shown.data?.rows, ['Vulak', 'Trakanon'])
})

test('a 304 after a failed refresh also clears the staleness', () => {
  // A revalidation that answers "nothing changed" has confirmed the rows are current, which is
  // exactly what the stale notice was saying it could not confirm.
  const settled = settle(null, [
    [everything, { kind: 'data', data: board('T0', 'Vulak') }],
    [everything, { kind: 'error', error: new Error('the request did not reach the server') }],
    [everything, { kind: 'notModified' }],
  ])

  const shown = view(settled, everything)
  assert.equal(shown.stale, false)
  assert.equal(shown.staleError, null)
  assert.equal(shown.asOf?.label, 'T0', 'a 304 must not move as_of')
})

test('a query in flight shows nothing from a DIFFERENT query', () => {
  // The same lie one render earlier. While the new filter's first request is outstanding there is
  // no answer to it yet, so the screen loads rather than showing the old one's rows.
  const settled = settle(null, [
    [everything, { kind: 'data', data: board('T0', 'Vulak') }],
  ])

  const shown = view(settled, inWindow)
  assert.equal(shown.data, null)
  assert.equal(shown.loading, true)
  assert.equal(shown.asOf, null, 'an as_of belongs to the answer it was computed for')
})

test('a reload in flight keeps the answer it is reloading', () => {
  // Same query, next attempt — which is what `reload()` and every poll tick are. Here the carry is
  // the point: the board keeps its rows while it revalidates.
  const settled = settle(null, [[everything, { kind: 'data', data: board('T0', 'Vulak') }]])
  const reloading = runOf([{ status: '' }], 1)

  const shown = view(settled, reloading)
  assert.deepEqual(shown.data?.rows, ['Vulak'])
  assert.equal(shown.loading, true, 'the rows are on screen AND a newer answer is outstanding')
  assert.equal(shown.stale, false)
})

test('a 304 keeps the rows and does not re-pin as_of', () => {
  // The point of revalidating is to NOT re-render. Re-pinning `as_of` would claim the server had
  // recomputed when it had not, and every countdown on the board is an offset from that instant.
  const first = settle(null, [[everything, { kind: 'data', data: board('T0', 'Vulak') }]])
  const after = nextSettled(first, everything, { kind: 'notModified' })

  assert.equal(after.asOf, first?.asOf, 'a 304 must not move as_of')
  assert.deepEqual(after.data?.rows, ['Vulak'])
  assert.equal(after.stale, false)
})

test('a 304 with nothing to apply it to is reported, not rendered as an empty board', () => {
  // The first request for a new query carries no `If-None-Match` — the tag is cleared with the
  // query — so the API cannot have produced this and something in front of it is caching. An
  // empty board would be the confident mistake.
  const settled = settle(null, [
    [everything, { kind: 'data', data: board('T0', 'Vulak') }],
    [inWindow, { kind: 'notModified' }],
  ])

  const shown = view(settled, inWindow)
  assert.equal(shown.data, null, 'rows from another query must not answer a 304')
  assert.match(String(shown.error?.message), /304/)
})

test('an answer to a superseded run is not shown as the current one', () => {
  // A slow first request landing after the filters moved on. `loading` stays true because the
  // answer on hand is not the answer this render asked for.
  const settled = settle(null, [[inWindow, { kind: 'data', data: board('T0', 'Vulak') }]])

  assert.equal(view(settled, everything).loading, true)
  assert.equal(view(settled, inWindow).loading, false)
})

test('runOf tells two filter sets apart and two attempts at one filter set apart', () => {
  assert.notEqual(runOf([{ status: 'overdue' }], 0).deps, runOf([{ status: 'up' }], 0).deps)
  assert.equal(runOf([{ status: 'overdue' }], 0).deps, runOf([{ status: 'overdue' }], 1).deps)
  assert.notEqual(runOf([{ status: 'overdue' }], 0).nonce, runOf([{ status: 'overdue' }], 1).nonce)
  // A circle id and a filter object side by side, which is how the board actually calls it.
  assert.equal(runOf(['01K3TGT', { q: 'vulak' }], 0).deps, '01K3TGT|{"q":"vulak"}')
})
