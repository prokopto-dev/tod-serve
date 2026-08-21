// Reading the URL fragment, and clearing it.
//
// An invite link carries its code in the FRAGMENT — `https://tod.example.com/join#TODI-4KQ7M-9XPB2`
// — because a fragment is never sent to any server: not to ours, not to a proxy, not in a
// `Referer`. That is the same reason the code travels in a POST body rather than a path segment,
// applied to the link an officer actually pastes into Discord. The OAuth callback uses the same
// rule for the `provider_ticket` it hands back.
//
// The fragment is cleared IMMEDIATELY on read, so a screenshot of the console, a shared browser
// tab or somebody's history does not carry a live credential.

/** Fragment is what a `/join` landing can be carrying. */
export type Fragment =
  | { kind: 'code'; code: string }
  | { kind: 'ticket'; ticket: string }
  | { kind: 'error'; code: string }
  | { kind: 'none' }

/**
 * takeFragment reads `location.hash` and clears it in the same turn.
 *
 * `history.replaceState` is used rather than assigning `location.hash = ''`, which leaves a bare
 * `#` in the address bar and pushes a history entry somebody can go Back into.
 */
export function takeFragment(): Fragment {
  const raw = window.location.hash.replace(/^#/, '')
  clearFragment()
  if (!raw) return { kind: 'none' }

  const params = new URLSearchParams(raw)
  const ticket = params.get('ticket')
  if (ticket) return { kind: 'ticket', ticket }
  const error = params.get('error')
  if (error) return { kind: 'error', code: error }

  // Anything else is an invite code, decoded but otherwise untouched: the server's parser is
  // deliberately generous about case and the `TODI-` prefix, and a client that normalised it
  // first would be a second, quieter copy of that rule.
  return { kind: 'code', code: decodeURIComponent(raw).trim() }
}

/** clearFragment removes the fragment without touching the path, the query or the history stack. */
export function clearFragment(): void {
  if (!window.location.hash) return
  window.history.replaceState(null, '', window.location.pathname + window.location.search)
}
