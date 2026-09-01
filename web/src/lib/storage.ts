// What the console remembers between page loads, and what it deliberately does not.
//
// It never stores a personal access token. The browser's credential is the `__Host-tod_session`
// cookie, which is `HttpOnly` precisely so that no script — including this one — can read it. The
// PAT `/join` mints in the same response is shown once, on the device-tokens screen, and is not
// kept: a token in `localStorage` is a token any XSS reads, and this console has no need of one.

/** RememberedCircle is enough to offer somebody their circle on the sign-in screen. */
export interface RememberedCircle {
  id: string
  name: string
  server: string
}

const CIRCLES_KEY = 'tod.circles'
const PENDING_KEY = 'tod.pending-join'

/**
 * PendingJoin is the invite code held across an OAuth round trip.
 *
 * It has to survive a redirect to the provider and back, and the code is a bearer credential, so
 * it goes in `sessionStorage` rather than `localStorage`: it dies with the tab, and it is cleared
 * the moment the ticket comes back. The alternative — putting it in the redirect URI — would put
 * a live invite code in a third party's logs.
 */
export interface PendingJoin {
  code?: string
  circleId?: string
  provider: string
  /**
   * stepUp marks a round trip that is re-proving an EXISTING session rather than signing in.
   *
   * The ticket that comes back feeds `stepUpSession` instead of `authenticateIdentity`, which is
   * the difference between refreshing a proof and minting another device. See ADR-0024 — a device
   * list filling up with one browser is what that distinction is for.
   */
  stepUp?: boolean
  /**
   * returnTo is the console path to land on afterwards, for a step-up.
   *
   * A step-up interrupts something: somebody was on Members, or Settings, and got refused. Sending
   * them to the board instead of back to what they were doing is a small betrayal of its own, and
   * it is why this is remembered rather than defaulted.
   *
   * It is a PATH within this console and never a URL. It is written by the console and read by the
   * console, and [safeReturnTo] refuses anything that is not a single-slash path — so a value that
   * ever did come from somewhere else cannot become an open redirect.
   */
  returnTo?: string
}

/**
 * safeReturnTo narrows a remembered path to one this console may navigate to.
 *
 * `//evil.example` is a protocol-relative URL and `https://evil.example` is an absolute one; both
 * are things `navigate()` would happily follow. Nothing writes either today — the only writer is
 * [StepUpProvider], with `location.pathname` — and that is exactly the kind of fact that stops
 * being true quietly. One slash, and no second one.
 */
export function safeReturnTo(value: string | undefined, fallback: string): string {
  if (!value || !value.startsWith('/') || value.startsWith('//')) return fallback
  return value
}

function read<T>(storage: Storage, key: string): T | null {
  try {
    const raw = storage.getItem(key)
    return raw ? (JSON.parse(raw) as T) : null
  } catch {
    // A browser with storage disabled is a browser the console still works in: it simply cannot
    // offer the sign-in shortcut. Failing the whole screen over it would be worse.
    return null
  }
}

function write(storage: Storage, key: string, value: unknown): void {
  try {
    storage.setItem(key, JSON.stringify(value))
  } catch {
    /* Storage is full or disabled. Nothing here is load-bearing. */
  }
}

export function rememberedCircles(): RememberedCircle[] {
  return read<RememberedCircle[]>(localStorage, CIRCLES_KEY) ?? []
}

/** rememberCircle records a circle somebody has actually signed into, most recent first. */
export function rememberCircle(circle: RememberedCircle): void {
  const rest = rememberedCircles().filter((c) => c.id !== circle.id)
  write(localStorage, CIRCLES_KEY, [circle, ...rest].slice(0, 8))
}

export function forgetCircle(id: string): void {
  write(
    localStorage,
    CIRCLES_KEY,
    rememberedCircles().filter((c) => c.id !== id),
  )
}

export function pendingJoin(): PendingJoin | null {
  return read<PendingJoin>(sessionStorage, PENDING_KEY)
}

export function setPendingJoin(pending: PendingJoin): void {
  write(sessionStorage, PENDING_KEY, pending)
}

export function clearPendingJoin(): void {
  try {
    sessionStorage.removeItem(PENDING_KEY)
  } catch {
    /* See write(). */
  }
}
