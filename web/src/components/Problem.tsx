// How a failure is shown.
//
// The `code` is from a closed enum and is what this branches on — never the HTTP status, and never
// the detail string. Three splits in that enum exist precisely because the two halves have
// DIFFERENT FIXES, and a console that could not tell them apart would send somebody to do the
// wrong thing:
//
//   - `forbidden` — ask an officer. `insufficient_scope` — mint a token that carries the scope.
//   - `session_required` — this is a capability-floor operation and no token reaches it at any
//     scope, so open the console in a browser. `step_up_required` — you are in a browser, and the
//     session simply has not re-proved who you are recently enough.
//   - `idempotency_key_reused` — a client bug. `idempotency_conflict` — a retry that should wait.
//
// **Step-up says so.** A capability-floor action that silently failed, or that reported a flat
// "forbidden", is the shape of failure this file exists to prevent.

import type { ReactNode } from 'react'

import { ProblemError, TransportError, type Problem } from '../api'
import { useStepUp } from '../app/stepup'
import { Banner, Button } from './ui'

/** ADVICE is what to actually do, per code. Absent means the detail already says it. */
const ADVICE: Partial<Record<ProblemError['code'], string>> = {
  forbidden: 'Your role does not hold the permission this needs. An officer or owner can grant it.',
  insufficient_scope:
    'Your token does not carry the scope this needs. Mint a device token that does, on Devices.',
  session_required:
    'No personal access token reaches this operation at any scope — it is in the capability floor. ' +
    'Sign in through the browser to do it.',
  step_up_required:
    'This action needs a recent proof that you are still here. Prove it in place — it keeps this ' +
    'session and mints no new device. Do not sign out: signing back in is what fills the Devices ' +
    'list with one browser.',
  membership_revoked: 'Your membership in this circle has been revoked.',
  precondition_required:
    'This operation overwrites something you read first, so it needs the tag of what you read. ' +
    'Reload and try again.',
  precondition_failed: 'Somebody else changed this while you were editing it. Reload to see theirs.',
  rate_limited: 'Too many attempts. Wait for the retry window and try again.',
  identity_provider_unreachable:
    'This instance could not reach the identity provider. That is a problem at the provider or ' +
    'with this server’s network, not with your credentials.',
  already_retracted: 'That report has already been retracted. A retraction is not retracted again.',
  retract_not_permitted:
    'You may retract your own reports; retracting somebody else’s needs tod.retract.any.',
}

export function ProblemNotice({
  error,
  onRetry,
  children,
}: {
  error: Error | null | undefined
  onRetry?: () => void
  children?: ReactNode
}) {
  if (!error) return null
  if (error instanceof TransportError) {
    return (
      <Banner tone="warn" title="The request did not reach the server">
        <p>{error.message}</p>
        {onRetry && (
          <Button className="mt-2" onClick={onRetry}>
            Try again
          </Button>
        )}
      </Banner>
    )
  }

  if (!(error instanceof ProblemError)) {
    return (
      <Banner tone="warn" title="Something went wrong">
        <p>{error.message}</p>
      </Banner>
    )
  }

  const stepUp = error.code === 'step_up_required'
  const fields = error.fieldErrors()
  return (
    <Banner tone={stepUp ? 'accent' : 'warn'} title={error.problem.title}>
      {error.problem.detail && <p>{error.problem.detail}</p>}
      {ADVICE[error.code] && <p className="mt-1">{ADVICE[error.code]}</p>}
      {stepUp && <StepUpTier meta={error.problem.meta} />}
      {Object.entries(fields).length > 0 && (
        <ul className="mt-1 list-disc space-y-0.5 pl-4">
          {Object.entries(fields).map(([location, message]) => (
            <li key={location}>
              <span className="font-mono text-[11px]">{location}</span>: {message}
            </li>
          ))}
        </ul>
      )}
      <p className="mt-1 font-mono text-[10px] opacity-70">
        {error.code}
        {error.problem.meta?.request_id ? ` · ${error.problem.meta.request_id}` : ''}
      </p>
      {children}
      <div className="mt-2 flex flex-wrap gap-2">
        {stepUp && <StepUpButton />}
        {onRetry && <Button onClick={onRetry}>Try again</Button>}
      </div>
    </Banner>
  )
}

/**
 * StepUpTier says WHICH bar was failed, in the words the tiers were chosen with.
 *
 * The window alone is a number somebody has to reverse-engineer a rule from, and the two rules read
 * very differently: five minutes to revoke a member is a deliberate bar, an hour to rename a circle
 * is a formality you have probably already met. `meta.step_up_tier` is the rule.
 */
function StepUpTier({ meta }: { meta: Problem['meta'] }) {
  const tier = meta?.step_up_tier
  if (!tier) return null
  const seconds = meta?.step_up_window_seconds
  const within = seconds ? ` — proved within the last ${Math.round(seconds / 60)} minutes` : ''
  return (
    <p className="mt-1">
      {tier === 'sensitive'
        ? 'This one changes who can do what, so the bar is the strict one'
        : 'This one changes no permissions, so the bar is the relaxed one'}
      {within}.
    </p>
  )
}

/**
 * StepUpButton is the way out of a `step_up_required`, rendered on the failure itself.
 *
 * It draws nothing outside an authenticated route: [useStepUp] answers null on the sign-in, join
 * and setup screens, where there is no session to re-prove. A button there would be a button that
 * cannot work, which is the shape of thing this whole change removes.
 */
function StepUpButton() {
  const stepUp = useStepUp()
  if (!stepUp) return null
  return (
    <Button variant="primary" onClick={stepUp.request}>
      Prove it&rsquo;s you
    </Button>
  )
}

/**
 * StaleNotice says that what is on screen is real but out of date.
 *
 * It is one component rather than a habit at each call site, because the habit is what failed: the
 * staleness flag used to be polling-only, and every screen that reloaded after a write kept
 * showing the state from BEFORE that write with nothing to say so. An officer who retracts a bad
 * time of death, or revokes a member, and whose refresh then fails, is looking at a screen that
 * disagrees with what they just did — and the dangerous reading is that their action did not take.
 *
 * It is deliberately NOT a [ProblemNotice]. The data below it is real and still worth reading, so
 * this is a note on that data rather than a failure banner over it.
 */
export function StaleNotice({ resource }: { resource: Stale }) {
  if (!resource.stale) return null
  return (
    <div className="border-b border-amber-900/60 bg-amber-950/30 px-4 py-2 text-[11px] text-amber-300">
      <span className="font-semibold">Not refreshed.</span> What you are looking at is real, and it
      is what the server last told us — but the most recent attempt to bring it up to date failed,
      so anything changed since then is missing.
      {resource.staleError ? (
        <span className="ml-1 opacity-80">{resource.staleError.message}</span>
      ) : null}
      {resource.reload ? (
        <Button className="ml-2 align-middle" onClick={resource.reload}>
          Refresh
        </Button>
      ) : null}
    </div>
  )
}

/**
 * Stale is the part of a resource [StaleNotice] reads.
 *
 * Structural rather than importing `Resource<T>`, so the component does not have to be generic
 * over data it never touches.
 */
export interface Stale {
  stale: boolean
  staleError: Error | null
  reload?: () => void
}

/** isStepUp reports whether a caught error is the capability floor asking for a re-authentication. */
export function isStepUp(error: unknown): boolean {
  return error instanceof ProblemError && error.code === 'step_up_required'
}

/** isUnauthenticated reports whether the console has no live credential at all. */
export function isUnauthenticated(error: unknown): boolean {
  return (
    error instanceof ProblemError &&
    (error.code === 'unauthenticated' ||
      error.code === 'token_expired' ||
      error.code === 'token_invalid')
  )
}
