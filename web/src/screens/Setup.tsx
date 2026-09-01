// `/setup` — first-run setup, and the only screen in this console with no session behind it.
//
// On the database this screen exists for, nobody holds a credential and no circle exists, so there
// is no principal any route could authorise. What authorises it instead is `TOD_SETUP_TOKEN`, an
// environment variable the operator set in `.env` beside the pepper and the session key — and the
// absence of an administrator, which is derived on the server and closes this door for good the
// moment somebody redeems the code below. ADR-0016.
//
// **The token arrives in the FRAGMENT and is cleared immediately**, exactly as an invite code does
// at `/join`: a fragment is never sent to any server, not to a proxy, and not in a `Referer`. It
// then lives in this component's state and nowhere else — no `localStorage`, no `sessionStorage`,
// and never in a URL this app builds.
//
// The last thing it does is hand off to `/join#TODI-…`, so the operator never copies a code out of
// a page and back into another one.

import { useMemo, useState } from 'react'
import { useNavigate } from 'react-router-dom'

import {
  api,
  body,
  ProblemError,
  toError,
  type SetupCircleRequest,
  type SetupProviderRequest,
  type SetupState,
} from '../api'
import { useResource } from '../app/useResource'
import { ProblemNotice, StaleNotice } from '../components/Problem'
import { WeakRevocationText, WEAK_REVOCATION_TITLE } from '../components/RevocationBanner'
import { Banner, Button, Card, Field, Input, Mono, Select, Spinner } from '../components/ui'
import { instant, plural } from '../lib/format'
import { takeFragment } from '../lib/hash'

/** LOCAL is the provider kind with no third party behind it, and the one that needs a word typed. */
const LOCAL = 'local'

type ProviderKind = SetupProviderRequest['kind']
type Server = NonNullable<SetupCircleRequest['server']>

const SERVERS: Server[] = ['blue', 'green', 'red']

export function Setup() {
  const navigate = useNavigate()
  // Read once, on the first render, and cleared in the same turn — `useMemo` rather than an effect
  // so nothing ever renders with the token still in the address bar.
  const fragment = useMemo(() => takeFragment(), [])
  const [token, setToken] = useState(fragment.kind === 'code' ? fragment.code : '')
  // The token the state below was fetched with, which is NOT what is in the input: typing does not
  // call the server.
  const [submitted, setSubmitted] = useState(fragment.kind === 'code' ? fragment.code.trim() : '')

  const state = useResource(
    (signal) =>
      submitted
        ? api.getSetupState({}, { bearer: submitted, signal }).then((r) => body(r))
        : Promise.resolve(null),
    [submitted],
  )

  if (!submitted || (!state.data && !state.loading)) {
    return (
      <Frame>
        <StaleNotice resource={state} />
        <ProblemNotice error={state.error} />
        {state.error instanceof ProblemError && state.error.code === 'not_found' ? (
          <p className="px-1 text-[11px] text-ink-500">
            An unset <Mono>TOD_SETUP_TOKEN</Mono> and a wrong one answer the same way, deliberately:
            a refusal that told them apart would tell a stranger which instances are worth guessing
            at. Check the value in your <Mono>.env</Mono>, and that the server was restarted after
            you set it.
          </p>
        ) : null}
        {state.error instanceof ProblemError && state.error.code === 'conflict' ? (
          <p className="px-1 text-[11px] text-ink-500">
            This instance already has an administrator, so setup is over and cannot be re-run. Sign
            in, or use <Mono>tod-serve instance grant</Mono> at the console.
          </p>
        ) : null}
        <TokenForm
          token={token}
          onToken={setToken}
          onSubmit={() => {
            const next = token.trim()
            // Submitting the SAME value again has to re-issue the request: the resource is keyed on
            // it, so setting it to what it already is changes nothing and the button looks dead.
            if (next === submitted) state.reload()
            else setSubmitted(next)
          }}
        />
      </Frame>
    )
  }

  if (!state.data) {
    return (
      <Frame>
        <Spinner label="Reading this instance" />
      </Frame>
    )
  }

  return (
    <Frame>
      <StaleNotice resource={state} />
      <SetupForm
        token={submitted}
        state={state.data}
        onDone={(code) => {
          // The code goes in the FRAGMENT, and the join page reads it and clears it. No copy and
          // paste, and nothing in an access log between here and there.
          navigate(`/join#${code}`, { replace: true })
        }}
      />
    </Frame>
  )
}

function Frame({ children }: { children: React.ReactNode }) {
  return (
    <div className="mx-auto max-w-2xl space-y-3 p-6">
      <header className="pb-1">
        <h1 className="text-lg font-semibold text-ink-100">Set up this instance</h1>
        <p className="text-xs text-ink-500">
          tod-serve — one form, then you sign in and you are the administrator.
        </p>
      </header>
      {children}
    </div>
  )
}

function TokenForm({
  token,
  onToken,
  onSubmit,
}: {
  token: string
  onToken: (value: string) => void
  onSubmit: () => void
}) {
  return (
    <Card
      title="Setup token"
      subtitle="TOD_SETUP_TOKEN, from the .env file next to your compose file."
    >
      <form
        className="space-y-3 p-4"
        onSubmit={(e) => {
          e.preventDefault()
          onSubmit()
        }}
      >
        <Field
          label="Setup token"
          hint="It is never stored by this page and never appears in a URL this app builds."
        >
          <Input
            value={token}
            autoFocus
            type="password"
            autoComplete="off"
            onChange={(e) => onToken(e.target.value)}
          />
        </Field>
        <Button variant="primary" type="submit" disabled={!token.trim()}>
          Continue
        </Button>
      </form>
    </Card>
  )
}

function SetupForm({
  token,
  state,
  onDone,
}: {
  token: string
  state: SetupState
  onDone: (ownerCode: string) => void
}) {
  const [name, setName] = useState(state.instance_name || 'tod-serve')
  const [publicURL, setPublicURL] = useState(state.public_url || '')
  const [providerKind, setProviderKind] = useState<ProviderKind>(LOCAL)
  const [providerKey, setProviderKey] = useState(LOCAL)
  const [displayName, setDisplayName] = useState('This server')
  const [clientID, setClientID] = useState('')
  const [clientSecret, setClientSecret] = useState('')
  const [redirectURI, setRedirectURI] = useState('')
  const [tokenEndpoint, setTokenEndpoint] = useState('')
  const [issuer, setIssuer] = useState('')
  const [authorizationEndpoint, setAuthorizationEndpoint] = useState('')
  const [jwksURI, setJWKSURI] = useState('')
  const [acknowledged, setAcknowledged] = useState(false)

  const existing = state.circles ?? []
  const [circleID, setCircleID] = useState(existing[0]?.id ?? '')
  const [circleName, setCircleName] = useState('')
  const [server, setServer] = useState<Server>('blue')

  const [busy, setBusy] = useState(false)
  const [error, setError] = useState<Error | null>(null)

  const weak = providerKind === LOCAL
  const alreadyRegistered = (state.providers ?? []).some((p) => p.key === providerKey.trim())

  const submit = () => {
    setBusy(true)
    setError(null)
    api
      .runSetup(
        {
          body: {
            name,
            public_url: publicURL,
            provider: {
              key: providerKey.trim(),
              kind: providerKind,
              display_name: displayName,
              client_id: clientID,
              client_secret: clientSecret,
              redirect_uri: redirectURI,
              token_endpoint: tokenEndpoint,
              issuer,
              authorization_endpoint: authorizationEndpoint,
              jwks_uri: jwksURI,
              acknowledge_weak_revocation: acknowledged,
            },
            // Either an existing circle by id, or a new one by name and server — never both, and
            // never neither. The server refuses a request that omits `id` once this instance has a
            // circle, rather than quietly picking one.
            circle:
              existing.length > 0
                ? ({ id: circleID } satisfies SetupCircleRequest)
                : ({ name: circleName, server } satisfies SetupCircleRequest),
          },
        },
        { bearer: token },
      )
      .then((r) => onDone(body(r).owner_code))
      .catch((err: unknown) => {
        setError(toError(err))
        setBusy(false)
      })
  }

  if (busy) return <Spinner label="Setting up" />

  return (
    <div className="space-y-3">
      <ProblemNotice error={error} />

      {state.configured && (
        <Banner tone="warn" title="This instance is already part-way set up">
          <p>
            An <Mono>instance</Mono> row exists, {plural(existing.length, 'circle')} and{' '}
            {plural((state.providers ?? []).length, 'identity provider')} are configured, and
            nobody administers it yet — which is why this page still works. Submitting corrects the
            instance row and issues a fresh owner code; it will not create a second circle.
          </p>
        </Banner>
      )}

      <Card title="This instance" subtitle="Both are shown to everybody who visits.">
        <div className="space-y-3 p-4">
          <Field label="Name" hint="What the join page and the console call this instance.">
            <Input value={name} maxLength={80} onChange={(e) => setName(e.target.value)} />
          </Field>
          <Field
            label="Public URL"
            hint="Where this instance is reachable. It must match the redirect URI registered with your identity provider exactly."
          >
            <Input
              value={publicURL}
              placeholder="https://tod.example.com"
              onChange={(e) => setPublicURL(e.target.value)}
            />
          </Field>
        </div>
      </Card>

      <Card title="How people sign in" subtitle="At least one, or nobody can join at all.">
        <div className="space-y-3 p-4">
          <Field label="Provider">
            <Select
              value={providerKind}
              onChange={(e) => {
                const kind = e.target.value as ProviderKind
                setProviderKind(kind)
                setProviderKey(kind)
                setDisplayName(kind === LOCAL ? 'This server' : titleOf(kind))
              }}
            >
              <option value={LOCAL}>local — a name people type here</option>
              <option value="discord">discord — your own Discord application</option>
              <option value="oidc">oidc — your own OpenID Connect provider</option>
            </Select>
          </Field>

          {alreadyRegistered && (
            <Banner tone="accent" title={`${providerKey} is already registered`}>
              It is left exactly as it is, secret included. Submitting will not overwrite it — a
              blank secret field is what a browser sends for a value it cannot read back, and
              clearing the real one would break every sign-in this instance has.
            </Banner>
          )}

          {providerKind !== LOCAL && !alreadyRegistered && (
            <>
              <Field label="Client ID" hint="From the application you registered with the provider.">
                <Input value={clientID} onChange={(e) => setClientID(e.target.value)} />
              </Field>
              <Field label="Client secret" hint="Write-only: no operation ever returns it.">
                <Input
                  type="password"
                  autoComplete="off"
                  value={clientSecret}
                  onChange={(e) => setClientSecret(e.target.value)}
                />
              </Field>
              <Field
                label="Redirect URI"
                hint="Register this EXACT string with the provider, or the sign-in completes and lands nowhere."
              >
                <Input
                  value={redirectURI}
                  placeholder={`${publicURL || 'https://tod.example.com'}/api/v1/auth/callback/${providerKey}`}
                  onChange={(e) => setRedirectURI(e.target.value)}
                />
              </Field>
              <Field label="Token endpoint">
                <Input
                  value={tokenEndpoint}
                  onChange={(e) => setTokenEndpoint(e.target.value)}
                />
              </Field>
            </>
          )}

          {providerKind === 'oidc' && !alreadyRegistered && (
            <>
              <Field label="Issuer">
                <Input value={issuer} onChange={(e) => setIssuer(e.target.value)} />
              </Field>
              <Field label="Authorization endpoint">
                <Input
                  value={authorizationEndpoint}
                  onChange={(e) => setAuthorizationEndpoint(e.target.value)}
                />
              </Field>
              <Field label="JWKS URI">
                <Input value={jwksURI} onChange={(e) => setJWKSURI(e.target.value)} />
              </Field>
            </>
          )}

          {/* The acknowledgement, and it is not a tick-box with a slogan next to it. It says what
              choosing `local` costs, in the same words `previewInvite` uses to describe a circle
              that has already made the choice — one paragraph, one place. */}
          {weak && (
            <Banner tone="warn" title={WEAK_REVOCATION_TITLE}>
              <WeakRevocationText weakProviders={[providerKey.trim() || LOCAL]} />
              <p className="mt-1">
                Nobody can tell us a <Mono>{providerKey.trim() || LOCAL}</Mono> account is gone,
                because there is no third party to ask. Revoking a member ends the credentials they
                are holding; it does not stop that person joining again under a different name, and
                the damage is the officers&rsquo; belief that it did.
              </p>
              <label className="mt-2 flex items-start gap-2 text-xs">
                <input
                  type="checkbox"
                  className="mt-0.5"
                  checked={acknowledged}
                  onChange={(e) => setAcknowledged(e.target.checked)}
                />
                <span>
                  I understand that revoking a member who joined through this provider does not stop
                  them coming back.
                </span>
              </label>
            </Banner>
          )}
        </div>
      </Card>

      <Card
        title="The first circle"
        subtitle="A circle is pinned to one server permanently, and there is no combined view anywhere."
      >
        <div className="space-y-3 p-4">
          {existing.length > 0 ? (
            <Field
              label="Circle"
              hint="This instance already has one. Setup issues it a fresh owner code rather than creating another."
            >
              <Select value={circleID} onChange={(e) => setCircleID(e.target.value)}>
                {existing.map((c) => (
                  <option key={c.id} value={c.id}>
                    {c.name} — {c.server}
                  </option>
                ))}
              </Select>
            </Field>
          ) : (
            <>
              <Field label="Name">
                <Input
                  value={circleName}
                  maxLength={80}
                  placeholder="Riot Blue"
                  onChange={(e) => setCircleName(e.target.value)}
                />
              </Field>
              <Field label="Server" hint="Immutable after creation.">
                <Select value={server} onChange={(e) => setServer(e.target.value as Server)}>
                  {SERVERS.map((s) => (
                    <option key={s} value={s}>
                      {s}
                    </option>
                  ))}
                </Select>
              </Field>
            </>
          )}
        </div>
      </Card>

      <Card title="What happens when you submit" subtitle="Then you sign in, once.">
        <div className="space-y-2 p-4 text-xs leading-relaxed text-ink-300">
          <p>
            The instance, the provider and the circle are written, and the raid-target catalogue is
            seeded — {state.raid_targets} target{state.raid_targets === 1 ? '' : 's'} are loaded
            already. <strong>Respawn timers are not bundled</strong> and this does not load any:
            until <Mono>tod-serve seed timers --file</Mono> runs, every target reports{' '}
            <Mono>no_timer</Mono> and times of death are still recorded correctly.
          </p>
          <p>
            You are then sent to the join page with a one-time owner code, which is shown once and
            stored nowhere. Redeeming it makes you this instance&rsquo;s first administrator, and
            closes this page for good.
          </p>
          <Button
            variant="primary"
            disabled={!name.trim() || (weak && !acknowledged) || (existing.length === 0 && !circleName.trim())}
            onClick={submit}
          >
            Create and get my owner code
          </Button>
          {state.administrator_exists ? (
            <p className="text-warn">
              An administrator already exists as of {instant(state.as_of)}; this will be refused.
            </p>
          ) : null}
        </div>
      </Card>
    </div>
  )
}

function titleOf(kind: string): string {
  return kind.charAt(0).toUpperCase() + kind.slice(1)
}
