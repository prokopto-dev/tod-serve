// The two hooks every screen is built from.
//
// Neither issues a request itself: both take a loader that calls `api.<operationId>(…)`, which is
// what keeps every request in `web/src/api` and every one of them nameable by the API-parity test.
//
// Neither decides what a screen SEES either. That is `./resource.ts`, which is pure, takes no
// dependency on React or on the transport, and is driven directly by `resource.test.ts`. What is
// left here is the glue: when to fire a request, when to abandon one, and where to keep the tag a
// poll revalidates with.

import { useCallback, useEffect, useRef, useState } from 'react'

import { toError } from '../api'
import type { AsOf } from '../lib/asof'
import { nextSettled, runOf, view, type HasAsOf, type Run, type Settled } from './resource'

export interface Resource<T> {
  data: T | null
  /**
   * error is an Error rather than `unknown`, narrowed at the transport boundary by [toError].
   * A screen holding `unknown` cannot render it without a cast at every call site, and a cast at
   * every call site is a cast somebody eventually gets wrong.
   */
  error: Error | null
  /** loading is true whenever the answer on hand is not the answer this render asked for. */
  loading: boolean
  /** asOf is the instant the CURRENT data was computed by the server, pinned monotonically. */
  asOf: AsOf | null
  reload: () => void
}

/**
 * useResource loads once, and again whenever `reload` is called or a dependency changes.
 *
 * It pins the response's `as_of` at the moment it arrives, so anything the screen renders as a
 * countdown is an offset from the SERVER's instant advanced by monotonic elapsed time — never a
 * subtraction against the browser's clock. See ../lib/asof.ts.
 */
export function useResource<T extends HasAsOf>(
  // `T | null` rather than `T`, because the transport's success type is a union: a `304` carries
  // no body. A caller that never revalidates simply never sees the null.
  load: (signal: AbortSignal) => Promise<T | null>,
  deps: readonly unknown[],
): Resource<T> {
  const [nonce, setNonce] = useState(0)
  const [settled, setSettled] = useState<Settled<T> | null>(null)
  // The latest-ref pattern, synced in an effect rather than during render: a caller should not have
  // to memoise its loader, and a ref written during render is a render with a side effect.
  const loadRef = useRef(load)
  useEffect(() => {
    loadRef.current = load
  })

  const run = runOf(deps, nonce)
  const { deps: depsKey } = run

  useEffect(() => {
    const controller = new AbortController()
    let live = true
    const effectRun: Run = { deps: depsKey, nonce }
    loadRef
      .current(controller.signal)
      .then((data) => {
        if (!live) return
        setSettled((prev) => nextSettled(prev, effectRun, { kind: 'data', data }))
      })
      .catch((error: unknown) => {
        if (!live || controller.signal.aborted) return
        setSettled((prev) => nextSettled(prev, effectRun, { kind: 'error', error: toError(error) }))
      })
    return () => {
      live = false
      controller.abort()
    }
  }, [depsKey, nonce])

  const reload = useCallback(() => setNonce((n) => n + 1), [])
  return { ...view(settled, run), reload }
}

/**
 * usePoll re-runs a loader on an interval and keeps the previous data while it does.
 *
 * The board polls rather than subscribing: `listTargetStates` carries an `ETag` and answers `304`,
 * which at a hundred-odd rows is cheap enough that a console with no realtime layer is a complete
 * product rather than a degraded one. Realtime is Phase 6 and the two event routes do not exist
 * yet — a client that tried to open one would get a 404, so this does not try.
 *
 * The tag is threaded through `If-None-Match`, and a `304` leaves the data alone: the point of
 * revalidating is to NOT re-render, so a poll that replaced identical rows every fifteen seconds
 * would be paying the cost and taking none of the benefit.
 */
export function usePoll<T extends HasAsOf>(
  load: (
    etag: string | null,
    signal: AbortSignal,
  ) => Promise<{ data: T | null; etag: string | null; notModified: boolean }>,
  intervalMs: number,
  deps: readonly unknown[],
): Resource<T> & { stale: boolean } {
  const [nonce, setNonce] = useState(0)
  const [settled, setSettled] = useState<Settled<T> | null>(null)
  const etagRef = useRef<string | null>(null)
  const loadRef = useRef(load)
  useEffect(() => {
    loadRef.current = load
  })

  const run = runOf(deps, nonce)
  const { deps: depsKey } = run

  useEffect(() => {
    const controller = new AbortController()
    let live = true
    const effectRun: Run = { deps: depsKey, nonce }
    // A new query is a new resource: the tag from the previous filter set would revalidate a
    // response for rows this one is not asking for, and the server would honestly answer 304.
    etagRef.current = null

    const tick = () => {
      loadRef
        .current(etagRef.current, controller.signal)
        .then((result) => {
          if (!live) return
          etagRef.current = result.etag ?? etagRef.current
          setSettled((prev) =>
            nextSettled(
              prev,
              effectRun,
              result.notModified ? { kind: 'notModified' } : { kind: 'data', data: result.data },
            ),
          )
        })
        .catch((error: unknown) => {
          if (!live || controller.signal.aborted) return
          setSettled((prev) =>
            nextSettled(prev, effectRun, { kind: 'error', error: toError(error) }),
          )
        })
    }

    tick()
    const timer = window.setInterval(tick, intervalMs)
    return () => {
      live = false
      controller.abort()
      window.clearInterval(timer)
    }
  }, [depsKey, nonce, intervalMs])

  const reload = useCallback(() => setNonce((n) => n + 1), [])
  return { ...view(settled, run), reload }
}

/**
 * useTick re-renders on an interval so a countdown moves.
 *
 * It carries no data and reads no clock: the components it wakes up compute their own offsets from
 * the response's `as_of` plus monotonic elapsed time.
 */
export function useTick(intervalMs = 1000): void {
  const [, setTick] = useState(0)
  useEffect(() => {
    const timer = window.setInterval(() => setTick((t) => t + 1), intervalMs)
    return () => window.clearInterval(timer)
  }, [intervalMs])
}
