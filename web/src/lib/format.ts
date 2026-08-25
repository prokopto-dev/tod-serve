// Rendering helpers, and the ONE file permitted to parse a timestamp.
//
// Nothing here reads the browser's clock: `Date.now()` and `new Date()` with no argument appear
// nowhere in the console, and WEB002 in scripts/repo-gates.sh is what keeps that true. What this
// file does is turn an instant the SERVER sent into text, and — in exactly one function —
// subtract two instants the server sent in the SAME response. Duration since that response is
// ./asof.ts's job and is measured monotonically.

/**
 * hasInstant reports whether a timestamp field actually carries one.
 *
 * The document types several nullable instants as plain `date-time` strings — a `*core.Micros` in
 * Go reaches the schema without its null branch — so the client checks rather than trusting the
 * generated type. Rendering `Invalid Date` for a target nobody has reported is exactly the kind of
 * confident mistake this project is built against.
 */
export function hasInstant(value: string | null | undefined): value is string {
  return typeof value === 'string' && value.length > 0
}

/**
 * instant renders a server timestamp for display, in the viewer's own locale.
 *
 * This is the ONE place the browser's clock settings are allowed to matter, and only for
 * formatting an absolute the server sent — never for computing how far away it is. See ./asof.ts.
 */
export function instant(value: string | null | undefined): string {
  if (!hasInstant(value)) return '—'
  const parsed = new Date(value)
  if (Number.isNaN(parsed.getTime())) return value
  return parsed.toLocaleString(undefined, {
    year: 'numeric',
    month: 'short',
    day: '2-digit',
    hour: '2-digit',
    minute: '2-digit',
    second: '2-digit',
  })
}

/** shortInstant drops the year and the seconds, for a dense table. */
export function shortInstant(value: string | null | undefined): string {
  if (!hasInstant(value)) return '—'
  const parsed = new Date(value)
  if (Number.isNaN(parsed.getTime())) return value
  return parsed.toLocaleString(undefined, {
    month: 'short',
    day: '2-digit',
    hour: '2-digit',
    minute: '2-digit',
  })
}

/** titleCase renders a snake_case wire value as prose without inventing a translation layer. */
export function titleCase(value: string): string {
  return value.replace(/_/g, ' ')
}

/** plural renders `1 report` / `4 reports` without a library. */
export function plural(count: number, one: string, many = `${one}s`): string {
  return `${count} ${count === 1 ? one : many}`
}

/** classes joins conditional class names. */
export function classes(...parts: Array<string | false | null | undefined>): string {
  return parts.filter(Boolean).join(' ')
}

/**
 * secondsBetweenServerInstants subtracts two timestamps that came from the same response.
 *
 * Both operands are the server's, so the browser's clock is not on either side of the subtraction:
 * it is arithmetic between two facts the server stated, and the caller then advances the result by
 * monotonic elapsed time like every other offset in the console.
 *
 * It exists because the API sends no `seconds_since_death` — the board renders "died 4h ago" from
 * `died_at` against the response's own `as_of` — and computing that from `Date.now()` is precisely
 * the mistake canonical §1 exists to prevent: a machine four minutes fast would show a window that
 * is wrong on screen and right in the database.
 */
export function secondsBetweenServerInstants(instant: string, asOf: string): number | null {
  const at = Date.parse(instant)
  const reference = Date.parse(asOf)
  if (Number.isNaN(at) || Number.isNaN(reference)) return null
  return Math.trunc((at - reference) / 1000)
}
