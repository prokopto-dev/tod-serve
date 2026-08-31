// The sign-in control for one identity provider.
//
// It dispatches on `kind` and on NOTHING ELSE. `display_name` is what the operator typed into the
// instance screen — the default is "Discord" and it can be anything, including "Discord" on an
// OIDC row — and `key` is free text the deployment chooses. Branding on either would put Discord's
// mark on a provider that is not Discord the first time somebody renamed a row. `kind` is the one
// field that cannot lie: the API refuses to change it after creation, because it is what decides
// `verifiable_subject`.
//
// The mark is an INLINE SVG. A remote logo URL would be a request issued from a component, which
// AGENTS.md law 7 bans outside `web/src/api` — `tod/no-network-outside-api` and `WEB001` both. It
// would also be the wrong shape of failure: an asset that 404s is a blank button on the first
// screen a new member sees and the one they cannot get past.
//
// Everything here renders through `Button`, so the height, radius, disabled treatment, transition
// and focus behaviour are the ones every other control on the screen already has. A bespoke
// `<button>` in Discord's colours would look pasted in, which is the problem this is fixing.

import { Button } from './ui'
import { classes } from '../lib/format'

/**
 * DiscordMark is Discord's logo, at Discord's own geometry.
 *
 * The path is the official mark, unredrawn and unstretched: the viewBox is 127.14 × 96.36 and the
 * width below is that ratio applied to the height, so nothing is squashed to fit a square. It is
 * filled with `currentColor` in ONE flat colour rather than being recoloured per element —
 * Discord's brand terms permit the logo on a sign-in control and do not permit a restyled logo.
 * The two colours it is ever given here are both sanctioned single-colour versions: white on
 * blurple inside the button, blurple on the console's own dark surface in the operator's list.
 */
export function DiscordMark({ className }: { className?: string }) {
  return (
    <svg
      viewBox="0 0 127.14 96.36"
      xmlns="http://www.w3.org/2000/svg"
      fill="currentColor"
      aria-hidden="true"
      focusable="false"
      className={classes('h-3.5 w-[1.155rem] shrink-0', className)}
    >
      <path d="M107.7,8.07A105.15,105.15,0,0,0,81.47,0a72.06,72.06,0,0,0-3.36,6.83A97.68,97.68,0,0,0,49,6.83,72.37,72.37,0,0,0,45.64,0,105.89,105.89,0,0,0,19.39,8.09C2.79,32.65-1.71,56.6.54,80.21h0A105.73,105.73,0,0,0,32.71,96.36,77.7,77.7,0,0,0,39.6,85.25a68.42,68.42,0,0,1-10.85-5.18c.91-.66,1.8-1.34,2.66-2a75.57,75.57,0,0,0,64.32,0c.87.71,1.76,1.39,2.66,2a68.68,68.68,0,0,1-10.87,5.19,77,77,0,0,0,6.89,11.1A105.25,105.25,0,0,0,126.6,80.22h0C129.24,52.84,122.09,29.11,107.7,8.07ZM42.45,65.69C36.18,65.69,31,60,31,53s5-12.74,11.43-12.74S54,46,53.89,53,48.84,65.69,42.45,65.69Zm42.24,0C78.41,65.69,73.25,60,73.25,53s5-12.74,11.44-12.74S96.23,46,96.12,53,91.08,65.69,84.69,65.69Z" />
    </svg>
  )
}

/**
 * ProviderButton renders the control that starts a sign-in with one provider.
 *
 * `label` is the generic wording the screen would have used anyway — "Continue", "Join", "Sign
 * in" — and it is what a non-Discord provider gets. Discord gets Discord's own sanctioned
 * wording instead, because "Continue" beside a blurple Discord button is not what anybody has
 * been taught to look for.
 */
export function ProviderButton({
  kind,
  label,
  disabled,
  onClick,
}: {
  kind: string
  label: string
  disabled?: boolean
  onClick: () => void
}) {
  // `shrink-0 whitespace-nowrap` on both, because the provider row is a `justify-between` flex
  // whose other child is a sentence: without it the LONGEST description on the join page — the
  // durable-revocation line with the Discord server gate on the end — squeezed "Sign in with
  // Discord" onto two lines, and a control a line taller than the ones under it is the exact
  // pasted-in look this is fixing. The description wraps instead, which is what it is for.
  if (kind === 'discord') {
    return (
      <Button
        variant="discord"
        className="shrink-0 whitespace-nowrap"
        disabled={disabled}
        onClick={onClick}
      >
        <DiscordMark />
        Sign in with Discord
      </Button>
    )
  }
  return (
    <Button
      variant="primary"
      className="shrink-0 whitespace-nowrap"
      disabled={disabled}
      onClick={onClick}
    >
      {label}
    </Button>
  )
}
