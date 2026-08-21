// The resource state machine: what a screen is shown, as a pure function of what has settled and
// what this render is asking for.
//
// It is separate from the hooks that drive it for the reason `internal/consensus` is separate from
// the services around it: everything that decides what reaches a component is here, it takes no
// dependency on the transport or on React, and it can therefore be driven directly by
// `resource.test.ts` rather than through a renderer.
//
// Two rules it exists to hold:
//
//   - **`loading` is derived, not stored.** A hook that set it in an effect reports `loading:
//     false` for the render between "a reload was asked for" and "the effect ran", and a screen
//     that decides what to do from `!loading && !data` acts in that window. That is not
//     hypothetical: it sent somebody who had just joined a circle straight back to the sign-in
//     screen, because the shell asked whether there was a principal in the one render where the
//     answer was "not yet" rather than "no".
//   - **An answer belongs to the query it answered.** Keeping the previous rows on screen while a
//     RELOAD of the same query is in flight is kind. Keeping them when the QUERY has changed is a
//     lie: an officer who filters the board to `overdue` during a transient network failure would
//     otherwise be shown the unfiltered board under the filtered URL, with nothing but a subtitle
//     to say so, and would act on rows that do not match what they asked for.

// The `.ts` is explicit because this module runs under `node --test` as well as through Vite, and
// Node's resolver does not guess an extension. Every module the test runner loads spells its
// relative imports this way; the ones only Vite ever sees do not have to.
import { markAsOf, type AsOf } from '../lib/asof.ts'

/** HasAsOf is the shape every derived response in this API has. */
export interface HasAsOf {
  as_of?: string
}

/**
 * Run names one request: which QUERY it is for, and which attempt at that query.
 *
 * The two halves are separate because they answer different questions. `deps` decides whether an
 * answer is even ABOUT what the screen is asking — a different filter set is a different question,
 * and an answer to the old one is not a stale version of the new one, it is a different fact.
 * `nonce` distinguishes attempts at the same question, which is what a reload and a poll are.
 */
export interface Run {
  readonly deps: string
  readonly nonce: number
}

/** Settled is one completed request, tagged with the run it answered. */
export interface Settled<T> extends Run {
  data: T | null
  error: Error | null
  asOf: AsOf | null
  /** stale marks data kept on screen after a later attempt at the SAME query failed. */
  stale: boolean
}

/** Outcome is what one attempt produced. */
export type Outcome<T> =
  | { kind: 'data'; data: T | null }
  | { kind: 'notModified' }
  | { kind: 'error'; error: Error }

/**
 * runOf names the run a render is in: a stable rendering of the dependencies, plus the reload
 * counter.
 */
export function runOf(deps: readonly unknown[], nonce: number): Run {
  const parts = deps.map((dep) =>
    typeof dep === 'object' && dep !== null ? JSON.stringify(dep) : String(dep),
  )
  return { deps: parts.join('|'), nonce }
}

/** sameQuery reports whether two runs are asking the same question. */
const sameQuery = (a: Run, b: Run): boolean => a.deps === b.deps

/** sameRun reports whether a settled answer is the answer to this exact attempt. */
const sameRun = (a: Run, b: Run): boolean => a.deps === b.deps && a.nonce === b.nonce

/**
 * nextSettled folds one outcome into the settled state.
 *
 * The whole of the correctness argument is the first line: an earlier answer is carried forward
 * ONLY when it answered the same query. Everything below it may then reuse `carry` freely, because
 * anything it holds is about the question being asked now.
 */
export function nextSettled<T extends HasAsOf>(
  prev: Settled<T> | null,
  run: Run,
  outcome: Outcome<T>,
): Settled<T> {
  const carry = prev && sameQuery(prev, run) ? prev : null

  switch (outcome.kind) {
    case 'data':
      return {
        ...run,
        data: outcome.data,
        error: null,
        asOf: markAsOf(outcome.data?.as_of ?? ''),
        stale: false,
      }

    case 'notModified':
      if (carry) {
        // Nothing moved. The data stands and so does its `as_of`: re-pinning it would claim the
        // server had recomputed when it had not.
        return { ...carry, ...run, error: null, stale: false }
      }
      // A `304` with nothing to apply it to. The first request for a new query carries no
      // `If-None-Match` — the tag is cleared with the query — so the API cannot have produced this
      // and something in front of it is caching. Rendering an empty board would be the confident
      // mistake; saying so is not.
      //
      // A plain Error rather than the transport's own `TransportError`: that class belongs with
      // the code that talks to the network, and this module is deliberately free of it. What the
      // reader sees is the message either way.
      return {
        ...run,
        data: null,
        error: new Error(
          'the server answered 304 to a request that carried no If-None-Match; something in ' +
            'front of the API is caching',
        ),
        asOf: null,
        stale: false,
      }

    case 'error':
      if (carry?.data) {
        // A later attempt at the SAME query failed. Keep what it has and say it is stale: blanking
        // a board because one poll failed is how somebody loses the window they were watching.
        return { ...carry, ...run, stale: true }
      }
      // Nothing on hand that answers this query — either the first attempt at it, or a query that
      // has just changed. The failure is the answer, and the screen renders it.
      return { ...run, data: null, error: outcome.error, asOf: null, stale: false }
  }
}

/** Viewed is what a screen sees: [Resource] without the reload it does not need to derive. */
export interface Viewed<T> {
  data: T | null
  error: Error | null
  loading: boolean
  asOf: AsOf | null
  stale: boolean
}

/**
 * view derives what this render should show from the settled state and the run it is in.
 *
 * `data` falls back to an earlier attempt at the SAME query and never to a different one, so a
 * screen whose filters have just changed shows a loading state rather than the previous filter's
 * rows.
 */
export function view<T>(settled: Settled<T> | null, run: Run): Viewed<T> {
  const current = settled && sameRun(settled, run) ? settled : null
  const carry = settled && sameQuery(settled, run) ? settled : null
  return {
    data: current?.data ?? carry?.data ?? null,
    error: current?.error ?? null,
    loading: current === null,
    asOf: current?.asOf ?? carry?.asOf ?? null,
    stale: current?.stale ?? false,
  }
}
