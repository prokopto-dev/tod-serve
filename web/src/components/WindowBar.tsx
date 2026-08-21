// Where "now" sits in the respawn window.
//
// Every number here comes from the response: `progress_bp` in integer basis points, and the two
// SIGNED `seconds_until_*` offsets. Both are advanced only by monotonic elapsed time since the
// response arrived — see ../lib/asof.ts. The browser's clock is never consulted, because a machine
// four minutes fast would render a window that is wrong on screen and right in the database.
//
// A target with no window renders NO BAR. `window_kind: unknown` means this instance holds no
// timer, and drawing an empty track with a marker on it would be a picture of a window that does
// not exist. The board still says when it died, which is the honest half.

import type { Window } from '../api'
import { countdown, offsetNow, progressNow, type AsOf } from '../lib/asof'
import { classes, hasInstant } from '../lib/format'

/** hasWindow reports whether the response actually carries a window to draw. */
export function hasWindow(window: Window | null | undefined): window is Window {
  return Boolean(window) && window!.kind !== 'unknown' && window!.progress_bp !== null
}

export function WindowBar({
  window,
  asOf,
  className,
}: {
  window: Window
  asOf: AsOf
  className?: string
}) {
  const progress = progressNow(
    window.progress_bp,
    window.seconds_until_open,
    window.seconds_until_close,
    asOf,
  )
  if (progress === null) return null

  const untilOpen = offsetNow(window.seconds_until_open, asOf)
  const untilClose = offsetNow(window.seconds_until_close, asOf)
  const open = untilOpen !== null && untilOpen <= 0
  const closed = untilClose !== null && untilClose <= 0

  return (
    <div className={classes('min-w-[9rem]', className)}>
      <div className="relative h-1.5 overflow-hidden rounded-full bg-ink-800">
        <div
          className={classes(
            'h-full rounded-full transition-[width] duration-500',
            closed
              ? 'bg-[var(--color-status-overdue)]'
              : open
                ? 'bg-[var(--color-status-inwindow)]'
                : 'bg-[var(--color-status-prewindow)]',
          )}
          style={{ width: `${progress / 100}%` }}
        />
        {/* A fixed timer has a single spawn instant rather than a band. `spawn_at` is present
            IFF the timer is fixed, so the marker branches on its presence rather than on
            `window_kind` — the same branch a plugin makes. */}
        {hasInstant(window.spawn_at) && (
          <span
            className="absolute top-0 h-full w-0.5 bg-ink-100/70"
            style={{ left: `${progress / 100}%` }}
            title="Fixed spawn"
          />
        )}
      </div>
      <p className="mt-1 flex items-center justify-between gap-2 text-[11px] text-ink-400 tnum">
        <span>{open ? 'closes' : 'opens'} {countdown(open ? untilClose : untilOpen)}</span>
        <span className="text-ink-500">{(progress / 100).toFixed(0)}% through</span>
      </p>
    </div>
  )
}

/**
 * NoWindow says why there is nothing to draw, in the vocabulary the API uses.
 *
 * `no_timer` is a degraded state and an honest one; an instance whose operator has not loaded the
 * separate timer seed reports it for every target and still records times of death correctly.
 */
export function NoWindow({ status }: { status: string }) {
  if (status === 'unknown') {
    return <span className="text-[11px] text-ink-500">no report yet</span>
  }
  return (
    <span
      className="text-[11px] text-[var(--color-status-notimer)]"
      title="This instance holds no respawn timer for this target, so there is no window to draw. Timers load from the separate tod-serve-p99-seed repository."
    >
      no timer loaded
    </span>
  )
}
