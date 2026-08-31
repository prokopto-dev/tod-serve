// What a sign-out tells the next screen.
//
// The confirmation cannot be rendered by the component that performs the sign-out. Signing out
// navigates away, so that component unmounts in the same commit that would have drawn it — which
// is not hypothetical: the first version of this set the count into state one line above the
// navigation that threw the component away, so `tokens_kept` reached nobody and the promise the
// response makes was true of the API and false of the console.
//
// So the fact travels WITH the navigation, as router state, and the destination renders it. The
// shape is written down once, here, so the writer and the reader cannot disagree about it — and
// the reader is a pure function rather than a narrowing inlined into a screen, because
// `useLocation().state` is `unknown` and the only way to drive it is to drive it directly.

/** SignedOut is what one sign-out is worth saying on the screen it lands on. */
export interface SignedOut {
  /**
   * tokensKept is the response's `tokens_kept`: personal access tokens this membership still
   * holds. Signing out revokes none of them — ADR-0005 binds a PAT to a membership and a plugin
   * going silent hours later, on another device, is the surprise this number exists to pre-empt.
   */
  tokensKept: number
}

/** signedOutState builds the router state a sign-out navigates with. */
export function signedOutState(tokensKept: number): { signedOut: SignedOut } {
  return { signedOut: { tokensKept } }
}

/**
 * readSignedOut narrows `useLocation().state`.
 *
 * It is defensive on purpose. That state comes back from the browser's own history — a back
 * button, a restored tab, a session another build of this console wrote — so anything at all can
 * be there. Everything unrecognised renders nothing, because a confirmation reading "NaN API
 * tokens are untouched" is worse than no confirmation: it is the same claim, made unbelievable.
 */
export function readSignedOut(state: unknown): SignedOut | null {
  if (typeof state !== 'object' || state === null || !('signedOut' in state)) return null
  const carried: unknown = (state as { signedOut: unknown }).signedOut
  if (typeof carried !== 'object' || carried === null || !('tokensKept' in carried)) return null
  const kept: unknown = (carried as { tokensKept: unknown }).tokensKept
  if (typeof kept !== 'number' || !Number.isInteger(kept) || kept < 0) return null
  return { tokensKept: kept }
}

/**
 * signedOutMessage is the sentence the destination shows.
 *
 * It is a function rather than JSX so the wording is drivable. The count is spelled out rather
 * than rendered as "1 token(s)": somebody checking whether their raid's plugin still works is
 * reading this line for reassurance, and reassurance written by a template is not reassuring.
 */
export function signedOutMessage(signedOut: SignedOut): string {
  switch (signedOut.tokensKept) {
    case 0:
      return 'This browser session has ended. You had no API tokens; signing out never revokes one.'
    case 1:
      return 'This browser session has ended. Your 1 API token still works — signing out never revokes one.'
    default:
      return `This browser session has ended. Your ${signedOut.tokensKept} API tokens still work — signing out never revokes one.`
  }
}
