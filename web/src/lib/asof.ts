// Every countdown in this console is a signed offset from the response's `as_of`, and never a
// subtraction against the browser's clock.
//
// This is canonical §1 and it is the single easiest thing for a frontend to get wrong. A machine
// whose clock is four minutes fast would otherwise render a window that is WRONG ON SCREEN and
// RIGHT IN THE DATABASE, which is the worst available combination: the officer trusts what they
// can see, and nothing anywhere reports a discrepancy.
//
// The server does the arithmetic. What the console adds is elapsed time since the response
// arrived, measured with `performance.now()` — a MONOTONIC counter of duration, not a reading of
// the wall clock. It is unaffected by a wrong system time, by an NTP correction mid-raid and by a
// daylight-saving jump. That distinction is the whole of this file.

/** SECONDS_PER_MINUTE and friends, spelled once. */
const MINUTE = 60
const HOUR = 60 * MINUTE
const DAY = 24 * HOUR

/**
 * AsOf is one server answer's instant, pinned to a monotonic reading taken when it arrived.
 *
 * `label` is the server's own RFC 3339 string, rendered verbatim wherever the console shows "as
 * of". It is never parsed for arithmetic.
 */
export interface AsOf {
  readonly label: string
  readonly receivedAt: number
}

/** markAsOf pins a response's `as_of` to the monotonic clock at the moment it was received. */
export function markAsOf(label: string): AsOf {
  return { label, receivedAt: performance.now() }
}

/**
 * elapsedSeconds is how long the console has held this answer.
 *
 * Monotonic, so it measures duration rather than reading a clock that may be wrong. It is never
 * negative: `performance.now()` does not go backwards.
 */
export function elapsedSeconds(asOf: AsOf, now = performance.now()): number {
  return Math.max(0, Math.floor((now - asOf.receivedAt) / 1000))
}

/**
 * offsetNow adjusts one of the server's `seconds_until_*` offsets for the time since the response
 * arrived.
 *
 * `null` in, `null` out: an absent offset means there is no window, and inventing a zero would
 * render a countdown for a target the instance has no timer for.
 */
export function offsetNow(seconds: number | null | undefined, asOf: AsOf): number | null {
  if (seconds === null || seconds === undefined) return null
  return seconds - elapsedSeconds(asOf)
}

/**
 * progressNow advances `progress_bp` across the window by the time since the response arrived.
 *
 * The server computed the ratio in integer basis points and this keeps it there: no floats, and
 * clamped to [0, 10000] exactly as canonical §3 says. It needs the window's total width, which is
 * derivable from the two offsets the same response carried.
 */
export function progressNow(
  progressBp: number | null | undefined,
  secondsUntilOpen: number | null | undefined,
  secondsUntilClose: number | null | undefined,
  asOf: AsOf,
): number | null {
  if (progressBp === null || progressBp === undefined) return null
  if (secondsUntilOpen === null || secondsUntilOpen === undefined) return progressBp
  if (secondsUntilClose === null || secondsUntilClose === undefined) return progressBp
  const width = secondsUntilClose - secondsUntilOpen
  if (width <= 0) return progressBp
  const advanced = progressBp + Math.floor((elapsedSeconds(asOf) * 10000) / width)
  return Math.min(10000, Math.max(0, advanced))
}

/**
 * duration renders a count of seconds as a coarse human span: `3d 4h`, `2h 11m`, `47s`.
 *
 * Two units at most. A raid leader reads this at a glance and the third unit is never the one that
 * changes the decision.
 */
export function duration(seconds: number): string {
  const s = Math.abs(Math.trunc(seconds))
  if (s >= DAY) {
    const days = Math.floor(s / DAY)
    const hours = Math.floor((s % DAY) / HOUR)
    return hours ? `${days}d ${hours}h` : `${days}d`
  }
  if (s >= HOUR) {
    const hours = Math.floor(s / HOUR)
    const minutes = Math.floor((s % HOUR) / MINUTE)
    return minutes ? `${hours}h ${minutes}m` : `${hours}h`
  }
  if (s >= MINUTE) {
    const minutes = Math.floor(s / MINUTE)
    const rest = s % MINUTE
    return rest ? `${minutes}m ${rest}s` : `${minutes}m`
  }
  return `${s}s`
}

/**
 * countdown renders a signed offset as something a person reads: `in 2h 11m`, or `4h 12m ago`.
 *
 * The sign is the server's: negative means the moment has passed. Rendering it as "in -2h" is how
 * a UI teaches somebody to distrust it.
 */
export function countdown(seconds: number | null): string {
  if (seconds === null) return '—'
  return seconds >= 0 ? `in ${duration(seconds)}` : `${duration(seconds)} ago`
}
