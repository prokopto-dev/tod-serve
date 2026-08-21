// `/join` — the landing an invite link points at, and where the OAuth callback comes back to.
//
// The code arrives in the URL FRAGMENT and is CLEARED IMMEDIATELY. A fragment is never transmitted
// to any server, which is why the code lives there rather than in a path or a query; clearing it
// is what stops a screenshot, a shared tab or somebody's history carrying a live credential.
//
// `revocation_strength` is shown BEFORE anybody commits. That field exists to be rendered: the
// damage from a weakly-revocable circle is not the re-entry, it is officers' false confidence that
// revoking somebody ended their access — and the person joining deserves to know which kind of
// circle they are joining.

import { useEffect, useMemo, useState } from 'react'
import { useNavigate } from 'react-router-dom'

import { api, body, ProblemError, type InvitePreview, type Joined, toError } from '../api'
import { usePrincipalState } from '../app/principal'
import { useResource } from '../app/useResource'
import { ProblemNotice, StaleNotice } from '../components/Problem'
import { RevocationBanner } from '../components/RevocationBanner'
import { Banner, Button, Card, Field, Input, Mono, Spinner } from '../components/ui'
import { takeFragment, type Fragment } from '../lib/hash'
import { instant, plural } from '../lib/format'
import { clearPendingJoin, pendingJoin, rememberCircle, setPendingJoin } from '../lib/storage'

/** CALLBACK_ERRORS explains what came back in `#error=`, in the vocabulary the API uses. */
const CALLBACK_ERRORS: Record<string, string> = {
  auth_flow_expired:
    'That sign-in took too long, or it had already been completed. Start it again.',
  auth_ticket_expired: 'The credential expired before it was redeemed. Start again.',
  auth_ticket_invalid: 'That credential had already been used. Start again.',
  guild_membership_required: 'This circle requires membership of a particular Discord server.',
  guild_role_required: 'This circle requires a particular role in its Discord server.',
  provider_scope_declined:
    'You declined a permission the sign-in needed, so we were never allowed to check your ' +
    'server roles. That is different from failing the check.',
  credential_audience_mismatch:
    'That credential was issued for a different instance of tod-serve and will not be accepted here.',
  identity_blocked: 'This identity has been blocked on this instance.',
  provider_not_accepted: 'This circle does not accept that identity provider.',
  provider_disabled: 'The operator has disabled that identity provider.',
  access_denied: 'You cancelled the sign-in at the provider.',
}

export function Join() {
  const navigate = useNavigate()
  const { reload: reloadPrincipal } = usePrincipalState()
  // Read once, on the first render, and cleared in the same turn. `useMemo` rather than an effect
  // so nothing renders with the code still in the address bar.
  const fragment = useMemo<Fragment>(() => takeFragment(), [])

  // Which sign-in a returning ticket belongs to. Read once, beside the fragment, so the "this
  // browser has no record" case is DERIVED and rendered rather than pushed into state from an
  // effect — a failure that arrives as a state update after a render is a failure that flickers.
  const pending = useMemo(() => (fragment.kind === 'ticket' ? pendingJoin() : null), [fragment])
  const orphanTicket = fragment.kind === 'ticket' && pending === null

  const [code, setCode] = useState(fragment.kind === 'code' ? fragment.code : '')
  // The code whose preview is on screen, which is NOT the same as what is in the input: typing
  // does not call the server. `previewInvite` shares a hard rate-limit bucket with
  // `createAuthorizationURL` — one bucket, so a code-guesser gets one guessing budget rather than
  // two — and a request per keystroke would exhaust it for the person actually joining.
  const [submitted, setSubmitted] = useState(fragment.kind === 'code' ? fragment.code.trim() : '')
  const [error, setError] = useState<Error | null>(null)
  const [busy, setBusy] = useState(fragment.kind === 'ticket' && !orphanTicket)
  const [displayName, setDisplayName] = useState('')
  // Set when the credential was created but the browser would not keep the session cookie.
  const [sessionRefused, setSessionRefused] = useState<Joined | null>(null)

  // The preview is a RESOURCE keyed on the submitted code rather than something an effect pushes
  // into state: a code arriving in the fragment and a code typed into the box are then the same
  // path, and there is one place that can be loading.
  const preview = useResource(
    (signal) =>
      submitted
        ? api.previewInvite({ body: { code: submitted } }, { signal }).then((r) => body(r))
        : Promise.resolve(null),
    [submitted],
  )

  // The one failure the screen renders, whichever half produced it.
  const failure = error ?? preview.error
  const working = busy || preview.loading

  /**
   * onJoined runs after the membership exists and the token has been minted.
   *
   * It CONFIRMS the browser session before navigating, rather than assuming it. `/join` answers
   * with two credentials — the PAT in the body and `__Host-tod_session` in a `Set-Cookie` — and a
   * browser can accept the first and refuse the second: the `__Host-` prefix requires `Secure`,
   * so an instance served over plain HTTP hands out a cookie the browser will not keep.
   *
   * Bouncing to the sign-in page there would be a lie twice over. The join SUCCEEDED — the
   * membership is real, the token is real, and on a single-use owner code there is no second
   * attempt to make — so telling somebody to sign in again would waste the code and describe the
   * wrong problem. [SessionRefused] says what actually happened and hands over the token.
   */
  const onJoined = (joined: Joined) => {
    rememberCircle({ id: joined.circle.id, name: joined.circle.name, server: joined.circle.server })
    clearPendingJoin()
    api
      .getCurrentPrincipal({})
      .then(() => {
        reloadPrincipal()
        navigate('/board', { replace: true })
      })
      .catch(() => {
        setBusy(false)
        setSessionRefused(joined)
      })
  }


  // A ticket coming back from the provider. Which operation it feeds depends on what was pending:
  // an invite code means `redeemInvite`, a remembered circle means `authenticateIdentity`. Both
  // take the SAME credential union, which is why the console has one code path here.
  useEffect(() => {
    if (fragment.kind !== 'ticket' || !pending) return
    const credential = { kind: 'provider_ticket' as const, ticket: fragment.ticket }
    const request = pending.code
      ? api.redeemInvite({
          body: { invite_code: pending.code, provider: pending.provider, credential },
        })
      : api.authenticateIdentity({
          body: { circle_id: pending.circleId ?? '', provider: pending.provider, credential },
        })
    request
      .then((r) => onJoined(body(r)))
      .catch((err: unknown) => {
        setError(toError(err))
        setBusy(false)
      })
    // The fragment is read once and this runs once with it.
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [fragment, pending])

  const startBrowserFlow = (providerKey: string) => {
    setBusy(true)
    setError(null)
    setPendingJoin({ code: submitted, provider: providerKey })
    api
      .createAuthorizationURL({ body: { provider: providerKey, invite_code: submitted } })
      .then((r) => {
        // The PKCE verifier never leaves the server; this URL is the whole of what the browser
        // needs, and it is the server's to build.
        window.location.assign(body(r).authorization_url)
      })
      .catch((err: unknown) => {
        clearPendingJoin()
        setError(toError(err))
        setBusy(false)
      })
  }

  const joinLocally = (providerKey: string) => {
    setBusy(true)
    setError(null)
    api
      .redeemInvite({
        body: {
          invite_code: submitted,
          provider: providerKey,
          credential: { kind: 'none' },
          display_name: displayName.trim(),
        },
      })
      .then((r) => onJoined(body(r)))
      .catch((err: unknown) => {
        setError(toError(err))
        setBusy(false)
      })
  }

  return (
    <div className="mx-auto max-w-2xl space-y-3 p-6">
      <header className="pb-1">
        <h1 className="text-lg font-semibold text-ink-100">Join a circle</h1>
        <p className="text-xs text-ink-500">
          tod-serve — time-of-death tracking for Project 1999 raid targets
        </p>
      </header>

      {fragment.kind === 'error' && (
        <Banner tone="warn" title="That sign-in did not complete">
          <p>{CALLBACK_ERRORS[fragment.code] ?? 'The provider reported a failure.'}</p>
          <p className="mt-1 font-mono text-[10px] opacity-70">{fragment.code}</p>
        </Banner>
      )}

      {orphanTicket ? (
        <Banner tone="warn" title="That credential does not belong to this browser">
          This tab has no record of which sign-in it was for — it may have been started somewhere
          else, or this browser cleared its session storage. Open your invite link again.
        </Banner>
      ) : null}

      <StaleNotice resource={preview} />
      <ProblemNotice error={error ?? preview.error} />
      {failure instanceof ProblemError && failure.code === 'invite_invalid' ? (
        <p className="px-1 text-[11px] text-ink-500">
          An unissued code, a revoked one and a code for a deleted circle all answer the same way,
          deliberately: a circle’s existence is part of what it is hiding.
        </p>
      ) : null}

      {sessionRefused ? <SessionRefused joined={sessionRefused} /> : null}

      {working && !sessionRefused ? <Spinner label="Working" /> : null}

      {!preview.data && !working && !sessionRefused && fragment.kind !== 'ticket' && (
        <Card title="Your invite code" subtitle="From the link an officer posted.">
          <form
            className="space-y-3 p-4"
            onSubmit={(e) => {
              e.preventDefault()
              setError(null)
              const next = code.trim()
              // Submitting the SAME code again has to re-issue the request. The preview is a
              // resource keyed on the submitted value, so setting it to what it already is changes
              // nothing and the button would appear dead — which is exactly what somebody does
              // after a failure they believe is transient.
              if (next === submitted) preview.reload()
              else setSubmitted(next)
            }}
          >
            <Field
              label="Invite code"
              hint="Case does not matter, and the TODI- prefix is optional."
            >
              <Input
                value={code}
                autoFocus
                placeholder="TODI-4KQ7M-9XPB2"
                onChange={(e) => setCode(e.target.value)}
              />
            </Field>
            <Button variant="primary" type="submit" disabled={!code.trim()}>
              Continue
            </Button>
          </form>
        </Card>
      )}

      {preview.data && !working && !sessionRefused && (
        <PreviewCard
          preview={preview.data}
          displayName={displayName}
          onDisplayName={setDisplayName}
          onBrowserFlow={startBrowserFlow}
          onLocal={joinLocally}
        />
      )}
    </div>
  )
}

function PreviewCard({
  preview,
  displayName,
  onDisplayName,
  onBrowserFlow,
  onLocal,
}: {
  preview: InvitePreview
  displayName: string
  onDisplayName: (value: string) => void
  onBrowserFlow: (providerKey: string) => void
  onLocal: (providerKey: string) => void
}) {
  const providers = preview.providers ?? []
  return (
    <Card
      title={preview.circle.name}
      subtitle={`${preview.circle.server} server · you would join as ${preview.granted_role}`}
    >
      <div className="space-y-4 p-4">
        <RevocationBanner
          strength={preview.revocation_strength}
          reasons={preview.revocation_weak_reasons}
          weakProviders={preview.weak_providers}
        />

        <dl className="grid gap-3 text-xs md:grid-cols-3">
          <div>
            <dt className="text-[11px] tracking-wide text-ink-400 uppercase">Role</dt>
            <dd className="mt-0.5 text-ink-100">{preview.granted_role}</dd>
          </div>
          <div>
            <dt className="text-[11px] tracking-wide text-ink-400 uppercase">Uses left</dt>
            <dd className="mt-0.5 text-ink-100 tnum">
              {Math.max(0, preview.max_uses - preview.uses)} of {plural(preview.max_uses, 'use')}
            </dd>
          </div>
          <div>
            <dt className="text-[11px] tracking-wide text-ink-400 uppercase">Expires</dt>
            <dd className="mt-0.5 text-ink-100 tnum">{instant(preview.expires_at)}</dd>
          </div>
        </dl>

        {preview.kind === 'owner_grant' && (
          <Banner tone="accent" title="This is the first-run owner code">
            It grants ownership of the circle and can be redeemed exactly once. An ordinary invite
            can never grant owner — that is a <Mono>CHECK</Mono> in the schema, not a policy.
          </Banner>
        )}

        <div>
          <p className="mb-2 text-[11px] tracking-wide text-ink-400 uppercase">
            Sign in with
          </p>
          {providers.length === 0 && (
            <p className="text-xs text-ink-500">
              This circle accepts no identity provider that is currently enabled. An operator has to
              fix that before anybody can join.
            </p>
          )}
          <div className="space-y-2">
            {providers.map((provider) => (
              <div
                key={provider.key}
                className="flex items-center justify-between gap-3 rounded border border-ink-700 bg-ink-850 px-3 py-2"
              >
                <div>
                  <p className="text-xs text-ink-100">{provider.display_name}</p>
                  <p className="text-[11px] text-ink-500">
                    {provider.verifiable_subject
                      ? 'revocation here is durable: the provider can tell us the account is gone'
                      : 'revocation here is advisory: nobody can tell us this account is gone'}
                    {provider.discord_guild_id ? ' · gated on a Discord server' : ''}
                  </p>
                </div>
                {provider.kind === 'local' ? (
                  <Button
                    variant="primary"
                    disabled={!displayName.trim() || !provider.available}
                    onClick={() => onLocal(provider.key)}
                  >
                    Join
                  </Button>
                ) : (
                  <Button
                    variant="primary"
                    disabled={!provider.available}
                    onClick={() => onBrowserFlow(provider.key)}
                  >
                    Continue
                  </Button>
                )}
              </div>
            ))}
          </div>

          {providers.some((p) => p.kind === 'local') && (
            <div className="mt-3">
              <Field
                label="Your name"
                hint="A local identity is a self-asserted name, so it is required."
              >
                <Input
                  value={displayName}
                  maxLength={64}
                  placeholder="Tankguy"
                  onChange={(e) => onDisplayName(e.target.value)}
                />
              </Field>
            </div>
          )}

          {providers.some((p) => !p.available) && (
            <p className="mt-2 text-[11px] text-amber-400">
              A provider marked unavailable is one this circle accepts and the operator has since
              disabled. It is shown rather than hidden: the row is marked, not dropped.
            </p>
          )}
        </div>
      </div>
    </Card>
  )
}

/**
 * SessionRefused is what the join page says when the credential exists and the browser would not
 * keep the session.
 *
 * It is deliberately not a retry prompt. Retrying cannot work — the cookie will be refused again
 * for the same reason — and on a single-use code it would spend the code. What it does instead is
 * name the cause, name the fix, and hand over the token that WAS minted, so the person can at
 * least point the plugin at this circle while the operator sorts out TLS.
 */
function SessionRefused({ joined }: { joined: Joined }) {
  return (
    <Card
      title={`You are a member of ${joined.circle.name}`}
      subtitle="But this browser would not keep the session, so the console cannot sign you in."
    >
      <div className="space-y-3 p-4 text-xs leading-relaxed text-ink-300">
        <p>
          The join worked: the membership exists, your role is{' '}
          <strong>{joined.membership.role}</strong>, and the token below was minted. What did not
          happen is the browser session.
        </p>
        <p>
          The session cookie is named <Mono>__Host-tod_session</Mono>, and the{' '}
          <Mono>__Host-</Mono> prefix means a browser will only store it over HTTPS. If you are
          reading this on an <Mono>http://</Mono> address, that is the whole of the problem — serve
          this instance over TLS and sign in again. Trying again from here will not help, and on a
          one-time code it would spend the code.
        </p>
        <Banner tone="accent" title="Your device token — copy it now">
          <p className="mt-1 font-mono text-[11px] break-all select-all">{joined.token.token}</p>
          <p className="mt-1">
            This is the only time it exists in plaintext. It reaches everything a token can reach;
            the console&rsquo;s administrative screens need a browser session, which is what the
            capability floor means.
          </p>
        </Banner>
      </div>
    </Card>
  )
}
