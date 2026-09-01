// In-place re-authentication.
//
// The failure this exists for was reported in one sentence: *"Either log me out automatically, or
// let me look. Not this half-authenticated state I'm apparently in."* A capability-floor operation
// answered `step_up_required`, the console said so and offered nothing, and the only remedy anybody
// found was signing out and back in — which mints another device every time, because `POST
// /sessions` is a sign-in and minting is what a sign-in does.
//
// So the console now asks the ONE question step-up actually asks — prove you are still there — and
// answers it with `stepUpSession`, which re-proves the session already in hand and mints nothing.
// ADR-0024.
//
// It is a context rather than a component each screen mounts, because the control belongs on the
// FAILURE, and the failure is rendered by `ProblemNotice`, which every screen already uses. One
// provider at the shell, one hook, and every `step_up_required` in the console grows a way out.

import { createContext, useCallback, useContext, useMemo, useState, type ReactNode } from 'react'
import { Link, useLocation } from 'react-router-dom'

import { api, body, toError } from '../api'
import { ProblemNotice, StaleNotice } from '../components/Problem'
import { ProviderButton } from '../components/ProviderButton'
import { Banner, Button, Card, Spinner } from '../components/ui'
import { setPendingJoin } from '../lib/storage'
import { useResource } from './useResource'

/** StepUp is what a screen can ask for: a way back to a proved identity. */
export interface StepUp {
  /** request opens the re-authentication panel. */
  request: () => void
}

const StepUpContext = createContext<StepUp | null>(null)

/**
 * useStepUp returns the re-authentication affordance, or null outside a [StepUpProvider].
 *
 * Null rather than a throw, because `ProblemNotice` renders on the signed-out screens too — the
 * sign-in page, the join page, the setup wizard — where there is no session to step up. A control
 * offered there would be a control that cannot work.
 */
export function useStepUp(): StepUp | null {
  return useContext(StepUpContext)
}

export function StepUpProvider({ children }: { children: ReactNode }) {
  const [open, setOpen] = useState(false)
  const value = useMemo<StepUp>(() => ({ request: () => setOpen(true) }), [])

  return (
    <StepUpContext.Provider value={value}>
      {children}
      {open && <StepUpPanel onClose={() => setOpen(false)} />}
    </StepUpContext.Provider>
  )
}

/**
 * StepUpPanel is the "prove it's you" surface.
 *
 * It is deliberately NOT a sign-in form. Nothing here offers to end the session, switch circle or
 * pick a membership: the only thing on offer is re-proving the identity this session already
 * belongs to, because everything else is what turned a five-minute expiry into a page of devices.
 *
 * There is no "done" callback and no automatic retry, and that is honest rather than lazy: every
 * provider that can re-prove an identity does it through a redirect, so the console leaves the page
 * and comes back to it. The screen remounts with a fresh proof and reloads its own data. A
 * callback that never fired would be a hook every screen wired up and none of them exercised.
 */
function StepUpPanel({ onClose }: { onClose: () => void }) {
  const location = useLocation()
  const [error, setError] = useState<Error | null>(null)
  const [busy, setBusy] = useState(false)

  const providers = useResource(
    (signal) => api.listIdentityProviders({}, { signal }).then((r) => r.data),
    [],
  )

  const items = providers.data?.items ?? []
  // Only a provider that can VERIFY a subject can re-prove one. `local` mints a fresh server-side
  // ULID on every verification, so a step-up through it resolves to an identity nobody has ever
  // seen: the server answers `provider_unverifiable` rather than pretending, and the console does
  // not draw the button at all. A control that always fails is the same dead end this panel exists
  // to remove, wearing a different hat.
  const usable = items.filter((p) => p.verifiable_subject && p.browser_flow)
  const unverifiable = items.filter((p) => !p.verifiable_subject)

  const start = useCallback(
    (providerKey: string) => {
      setBusy(true)
      setError(null)
      // Where to come back to. The OAuth round trip lands on `/join`, which reads this and
      // navigates here rather than dropping somebody on the board — being sent to a different page
      // than the one you were refused on is its own small betrayal.
      setPendingJoin({
        provider: providerKey,
        stepUp: true,
        returnTo: location.pathname + location.search,
      })
      api
        .createAuthorizationURL({ body: { provider: providerKey } })
        .then((r) => window.location.assign(body(r).authorization_url))
        .catch((err: unknown) => {
          setError(toError(err))
          setBusy(false)
        })
    },
    [location.pathname, location.search],
  )

  return (
    <div
      className="fixed inset-0 z-50 flex items-start justify-center overflow-auto bg-ink-950/80 p-6"
      role="dialog"
      aria-modal="true"
      aria-label="Prove it's you"
      onKeyDown={(e) => {
        if (e.key === 'Escape') onClose()
      }}
    >
      <div className="w-full max-w-lg space-y-3">
        <Card
          title="Prove it's you"
          subtitle="This does not sign you out, and it does not create a new device."
        >
          <div className="space-y-3 p-4 text-xs leading-relaxed text-ink-300">
            <p>
              You are signed in. What this operation wants is a{' '}
              <strong className="text-ink-100">recent</strong> proof that you are still the person
              at this keyboard — a tab left open all afternoon authenticates you without proving
              that.
            </p>

            {error && <ProblemNotice error={error} />}
            {/* The provider list is what this panel offers, so a refresh that failed silently
                would offer a provider the operator has since disabled — and the sign-in would fail
                at the far end of a redirect, where nothing here can explain it. */}
            <StaleNotice resource={providers} />
            {providers.error && <ProblemNotice error={providers.error} onRetry={providers.reload} />}
            {(providers.loading || busy) && <Spinner label="Working" />}

            {!busy && usable.length > 0 && (
              <div className="space-y-2">
                {usable.map((provider) => (
                  <div
                    key={provider.key}
                    className="flex items-center justify-between gap-3 rounded border border-ink-700 bg-ink-850 px-3 py-2"
                  >
                    <div>
                      <p className="text-xs text-ink-100">{provider.display_name}</p>
                      <p className="text-[11px] text-ink-500">You will come back to this page.</p>
                    </div>
                    <ProviderButton
                      kind={provider.kind}
                      label="Prove it's me"
                      onClick={() => start(provider.key)}
                    />
                  </div>
                ))}
              </div>
            )}

            {!busy && !providers.loading && !providers.error && usable.length === 0 && (
              <Banner tone="warn" title="This membership cannot re-authenticate">
                <p>
                  {unverifiable.length > 0
                    ? 'Every identity provider this instance has enabled mints a new subject each ' +
                      'time somebody signs in, so there is nothing for one to re-prove. That is ' +
                      'what a local identity IS, not a setting somebody forgot to turn on.'
                    : 'This instance has no identity provider enabled that could prove anything.'}
                </p>
                <p className="mt-1">
                  The operations behind this refusal need a durable identity. Signing out and back
                  in will not help: an operator has to enable Discord or an OIDC provider, and you
                  have to hold an identity through it, before they are reachable at all.
                </p>
              </Banner>
            )}
          </div>
          <div className="flex justify-end border-t border-ink-800 px-4 py-3">
            <Button onClick={onClose}>Not now</Button>
          </div>
        </Card>
        <p className="px-1 text-[11px] text-ink-500">
          Signing out and back in also works, and is the wrong answer: <code>POST /sessions</code>{' '}
          is a sign-in, so it mints a personal access token every time — which is why{' '}
          <Link to="/devices" className="text-accent-400 underline">
            Devices
          </Link>{' '}
          fills up with one browser.
        </p>
      </div>
    </div>
  )
}
