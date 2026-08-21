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

import { ProblemError, TransportError } from '../api'
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
    'This action needs you to prove who you are again. Signing in once more will do it; nothing ' +
    'is lost.',
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
      {onRetry && (
        <Button className="mt-2" onClick={onRetry}>
          Try again
        </Button>
      )}
    </Banner>
  )
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
