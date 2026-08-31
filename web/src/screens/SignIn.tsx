// Sign in.
//
// The provider list is READ FROM THE SERVER, never hardcoded. `listIdentityProviders` is public
// and exists precisely so a client can discover them at runtime: a console that assumed Discord is
// a console an OIDC-only instance cannot log into, and the operator who deployed it finds that out
// by watching people fail.
//
// Re-authentication needs a circle, and `authenticateIdentity` is the ONE public route that takes
// a circle id — with a credential, and answering 404 for everything. So the console offers circles
// it has actually signed into before, from this browser's own storage. Somebody arriving for the
// first time comes through an invite link, which is the whole point of the link.

import { useState } from 'react'
import { Link, Navigate } from 'react-router-dom'

import { api, body, toError } from '../api'
import { useResource } from '../app/useResource'
import { ProblemNotice, StaleNotice } from '../components/Problem'
import { ProviderButton } from '../components/ProviderButton'
import { Banner, Button, Card, Empty, Field, Input, Select, Spinner } from '../components/ui'
import { forgetCircle, rememberedCircles, setPendingJoin } from '../lib/storage'

export function SignIn() {
  const meta = useResource((signal) => api.getServerMeta({}, { signal }).then((r) => r.data), [])
  const providers = useResource(
    (signal) => api.listIdentityProviders({}, { signal }).then((r) => r.data),
    [],
  )

  const [circles, setCircles] = useState(rememberedCircles)
  const [circleID, setCircleID] = useState(circles[0]?.id ?? '')
  const [displayName, setDisplayName] = useState('')
  const [error, setError] = useState<Error | null>(null)
  const [busy, setBusy] = useState(false)

  const rows = providers.data?.items ?? []

  // An instance nobody administers yet, with a setup token set, is one that has not been stood up.
  // Sending somebody here to a sign-in form they cannot complete is the worst version of that:
  // there is no circle to sign into and no provider to sign in with.
  //
  // It routes on `setup_available` and NOT on `configured`. The two differ exactly where it
  // matters — an instance row whose owner code was never redeemed is `configured` and still needs
  // this — and `setup_available` is false when no `TOD_SETUP_TOKEN` is set, so this never sends
  // anybody to a wizard that cannot work. ADR-0016.
  if (meta.data?.setup_available) return <Navigate to="/setup" replace />

  const startBrowserFlow = (providerKey: string) => {
    if (!circleID) return
    setBusy(true)
    setError(null)
    setPendingJoin({ circleId: circleID, provider: providerKey })
    api
      .createAuthorizationURL({ body: { provider: providerKey } })
      .then((r) => window.location.assign(body(r).authorization_url))
      .catch((err: unknown) => {
        setError(toError(err))
        setBusy(false)
      })
  }

  const signInLocally = (providerKey: string) => {
    if (!circleID) return
    setBusy(true)
    setError(null)
    api
      .authenticateIdentity({
        body: {
          circle_id: circleID,
          provider: providerKey,
          credential: { kind: 'none' },
          display_name: displayName.trim(),
        },
      })
      // Confirmed rather than assumed, for the same reason [Join] confirms it: `/sessions` hands
      // back a token AND a `__Host-tod_session` cookie, and a browser on a plain-HTTP origin keeps
      // the first and refuses the second. Navigating to the board there would bounce straight back
      // here, forever, with nothing on screen saying why.
      .then(() => api.getCurrentPrincipal({}))
      .then(() => window.location.assign('/board'))
      .catch((err: unknown) => {
        setError(toError(err))
        setBusy(false)
      })
  }

  return (
    <div className="mx-auto max-w-xl space-y-3 p-6">
      <header className="pb-1">
        <h1 className="text-lg font-semibold text-ink-100">
          {meta.data?.name || 'tod-serve'}
        </h1>
        <p className="text-xs text-ink-500">
          {meta.data?.configured === false
            ? 'This instance has not been set up yet, and no setup token is configured: set ' +
              'TOD_SETUP_TOKEN and restart to use the browser wizard, or run tod-serve init.'
            : 'Sign in to a circle you already belong to.'}
        </p>
      </header>

      {error && <ProblemNotice error={error} />}
      {(providers.loading || busy) && <Spinner label="Working" />}
      <StaleNotice resource={providers} />
      {providers.error && <ProblemNotice error={providers.error} onRetry={providers.reload} />}

      {!busy && circles.length === 0 && (
        <Card title="You need an invite link">
          <Empty title="This browser has not signed into a circle here before.">
            A circle’s existence is not discoverable — there is no “list every circle” operation at
            any permission level — so signing in needs either an invite link an officer posted, or
            the circle id you were given. Open the link and you will land on{' '}
            <Link to="/join" className="text-accent-400">
              the join page
            </Link>
            .
          </Empty>
          <div className="border-t border-ink-800 p-4">
            <Field label="Circle id" hint="A ULID, if somebody gave you one directly.">
              <Input
                value={circleID}
                placeholder="01K3TGT8N9M4X0Q7R2VB6C5D1E"
                onChange={(e) => setCircleID(e.target.value.trim())}
              />
            </Field>
          </div>
        </Card>
      )}

      {!busy && circles.length > 0 && (
        <Card title="Circle">
          <div className="space-y-3 p-4">
            <Field label="Sign in to">
              <Select
                value={circleID}
                className="w-full"
                onChange={(e) => setCircleID(e.target.value)}
              >
                {circles.map((circle) => (
                  <option key={circle.id} value={circle.id}>
                    {circle.name} — {circle.server}
                  </option>
                ))}
              </Select>
            </Field>
            <Button
              onClick={() => {
                forgetCircle(circleID)
                const next = rememberedCircles()
                setCircles(next)
                setCircleID(next[0]?.id ?? '')
              }}
            >
              Forget this circle
            </Button>
          </div>
        </Card>
      )}

      {!busy && rows.length > 0 && (
        <Card title="Identity provider" subtitle="Read from this instance, not assumed.">
          <div className="space-y-2 p-4">
            {rows.map((provider) => (
              <div
                key={provider.key}
                className="flex items-center justify-between gap-3 rounded border border-ink-700 bg-ink-850 px-3 py-2"
              >
                <div>
                  <p className="text-xs text-ink-100">{provider.display_name}</p>
                  <p className="text-[11px] text-ink-500">
                    {provider.verifiable_subject
                      ? 'durable revocation'
                      : 'advisory revocation — nobody can tell us this account is gone'}
                  </p>
                </div>
                <ProviderButton
                  kind={provider.kind}
                  label={provider.browser_flow ? 'Continue' : 'Sign in'}
                  disabled={!circleID || (!provider.browser_flow && !displayName.trim())}
                  onClick={() =>
                    provider.browser_flow
                      ? startBrowserFlow(provider.key)
                      : signInLocally(provider.key)
                  }
                />
              </div>
            ))}
            {rows.some((p) => !p.browser_flow) && (
              <Field label="Your name" hint="A local identity is a self-asserted name.">
                <Input
                  value={displayName}
                  maxLength={64}
                  onChange={(e) => setDisplayName(e.target.value)}
                />
              </Field>
            )}
          </div>
        </Card>
      )}

      {!busy && !providers.loading && rows.length === 0 && !providers.error && (
        <Banner tone="warn" title="This instance has no identity provider enabled">
          Nobody can sign in until an operator adds and enables one. That is done from the instance
          screen, or with <code>tod-serve init</code> at the console.
        </Banner>
      )}
    </div>
  )
}
