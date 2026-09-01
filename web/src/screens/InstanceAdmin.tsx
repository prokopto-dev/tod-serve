// Instance administration — the instance's own policy, and its identity providers.
//
// This is the screen that makes an instance's policy and its identity providers configurable
// without a terminal.
//
// Everything here needs `instance.security.manage`, which is INSTANCE-REALM: no circle role grants
// it, no personal access token reaches it at any scope, and what does grant it is an
// `instance_grant` written by `tod-serve instance grant` at the console. That is deliberate — a
// leaked token that could add a malicious OIDC issuer is a pivot into every credential this
// instance will ever verify.
//
// `client_secret` is WRITE-ONLY. It goes in and never comes back out; the representation says only
// whether one is set. `key` and `kind` are immutable, because `kind` decides
// `verifiable_subject`, which is what every circle's revocation strength is derived from. Deleting
// a provider is refused once anybody has joined through it — DISABLING is the operation that stops
// new joins, and the difference matters: disabling leaves the history intact.
//
// The per-circle half — which of these a circle accepts, and the Discord guild and roles that gate
// it — is NOT here. It requires `circle.security.manage`, which is CIRCLE-realm: an owner holds it
// and an instance operator does not, so an editor sitting on this screen was one no circle owner
// could ever reach. It lives on Circle settings, once, and this screen links to it rather than
// carrying a second copy.
//
// The instance policy card is the SAME permission as the providers below it and a different kind of
// decision, which is why it sits here rather than on a screen of its own. Every change to it is
// recorded in `instance_setting_change` — append-only and hash-chained, because
// `audit_log.circle_id` is NOT NULL and an instance policy belongs to no circle. The ledger is
// rendered under the form rather than hidden behind a second screen: an audit record nobody can
// read is one nobody checks.
//
// `public_url` is shown and cannot be edited here. It has to match the redirect URI registered with
// every identity provider character for character, and it is read at startup from `$TOD_PUBLIC_URL`
// before the row — so a field that accepted a new one would break sign-in at some later restart,
// with nothing on this instance to show for it.

import { useState } from 'react'
import { Link } from 'react-router-dom'

import { api, type AdminIdentityProvider, type InstanceSettingsResponse, toError } from '../api'
import { useResource, type Resource } from '../app/useResource'
import { ProblemNotice, StaleNotice } from '../components/Problem'
import { DiscordMark } from '../components/ProviderButton'
import { instant } from '../lib/format'
import { Banner, Button, Card, Empty, Field, Input, Mono, Select, Spinner, Td, Th } from '../components/ui'

export function InstanceAdmin() {
  const [error, setError] = useState<Error | null>(null)
  const [adding, setAdding] = useState(false)

  const settings = useResource(
    (signal) => api.getInstanceSettings({}, { signal }).then((r) => r.data),
    [],
  )
  const providers = useResource(
    (signal) => api.listAdminIdentityProviders({}, { signal }).then((r) => r.data),
    [],
  )

  const rows = providers.data?.items ?? []

  return (
    <div className="space-y-3">
      {error && <ProblemNotice error={error} />}

      <InstancePolicyCard resource={settings} onError={setError} />

      <Card
        title="Identity providers"
        subtitle="Instance-wide. A circle chooses which of these it accepts; this is what exists to choose from."
        actions={<Button onClick={() => setAdding((v) => !v)}>{adding ? 'Cancel' : 'Add provider'}</Button>}
      >
        <StaleNotice resource={providers} />
        {providers.error && (
          <div className="p-4">
            <ProblemNotice error={providers.error} onRetry={providers.reload} />
          </div>
        )}
        {providers.loading && !providers.data && <Spinner label="Reading providers" />}
        {providers.data && rows.length === 0 && !adding && (
          <Empty title="This instance has no identity provider.">
            Nobody can sign in until one exists and is enabled.
          </Empty>
        )}
        {rows.length > 0 && (
          <div className="overflow-x-auto">
            <table className="w-full border-collapse text-xs">
              <thead>
                <tr>
                  <Th>Key</Th>
                  <Th>Kind</Th>
                  <Th>Name</Th>
                  <Th>Client id</Th>
                  <Th>Secret</Th>
                  <Th>Revocation</Th>
                  <Th>Enabled</Th>
                </tr>
              </thead>
              <tbody>
                {rows.map((provider) => (
                  <ProviderRow
                    key={provider.id}
                    provider={provider}
                    onDone={providers.reload}
                    onError={setError}
                  />
                ))}
              </tbody>
            </table>
          </div>
        )}
      </Card>

      {adding && (
        <AddProviderCard
          onDone={() => {
            setAdding(false)
            providers.reload()
          }}
        />
      )}

      <Banner tone="info" title="A circle's own gate is not configured here">
        Which of these providers a circle accepts — and the Discord guild and roles that gate it —
        needs <Mono>circle.security.manage</Mono>. That is CIRCLE-realm: a circle owner holds it and
        this instance-realm grant does not imply it. It is edited on{' '}
        <Link className="text-accent-400 underline" to="/settings">
          Circle settings
        </Link>
        , by the people who hold it.
      </Banner>
    </div>
  )
}

/**
 * InstancePolicyCard is the instance's own settings, and the ledger of every change to them.
 *
 * The switch that matters is `self_service_circle_creation`: this instance's stated answer to who
 * may create a circle here. It was answered once by the first-run wizard and could not be changed
 * afterwards, which is the worst moment to be stuck with a policy decision — the operator has the
 * least information about how their instance will actually be used.
 *
 * **It is published, not yet enforced, and the form says so rather than implying otherwise.**
 * `createCircle` declares `instance.circle.create` in the route registry unconditionally, so
 * turning this on moves what `/meta` publishes and does not widen who the API accepts a circle
 * from. A checkbox that reads as a permission change while changing no permission is exactly the
 * confident mistake this codebase is built against.
 *
 * **The ledger is rendered, not just written.** Every change is a row in `instance_setting_change`,
 * append-only and hash-chained; showing it here is what makes "who turned this on, and when" a
 * question somebody can answer without a database.
 *
 * The save reads first and writes with the entity tag that read returned. `*` would be accepted and
 * would mean "whatever is there now" — this console overwriting another administrator's change
 * having read nothing, which is exactly what the precondition exists to prevent.
 */
function InstancePolicyCard({
  resource,
  onError,
}: {
  resource: Resource<InstanceSettingsResponse>
  onError: (error: Error | null) => void
}) {
  const current = resource.data
  const [draft, setDraft] = useState<Draft | null>(null)
  const [reason, setReason] = useState('')
  const [busy, setBusy] = useState(false)

  // The form is seeded from the server's answer on first render and never re-seeded behind
  // somebody's typing: a reload that overwrote a half-edited name would lose their work silently.
  const form = draft ?? {
    name: current?.name ?? '',
    timezone: current?.timezone ?? '',
    selfService: current?.self_service_circle_creation ?? false,
  }
  const edit = (patch: Partial<Draft>) => setDraft({ ...form, ...patch })

  const dirty =
    current !== null &&
    (form.name !== current.name ||
      form.timezone !== current.timezone ||
      form.selfService !== current.self_service_circle_creation)

  const save = () => {
    setBusy(true)
    onError(null)
    api
      .getInstanceSettings({})
      .then((read) =>
        api.updateInstanceSettings(
          {
            body: {
              name: form.name,
              timezone: form.timezone,
              self_service_circle_creation: form.selfService,
              ...(reason.trim() ? { reason: reason.trim() } : {}),
            },
          },
          read.etag ? { ifMatch: read.etag } : {},
        ),
      )
      .then(() => {
        setDraft(null)
        setReason('')
        resource.reload()
      })
      .catch((err: unknown) => onError(toError(err)))
      .finally(() => setBusy(false))
  }

  const changes = current?.changes ?? []

  return (
    <Card
      title="Instance policy"
      subtitle="Instance-wide, and every change is recorded in an append-only, hash-chained ledger."
      actions={
        <Button variant="primary" onClick={save} disabled={busy || !dirty}>
          {busy ? 'Saving…' : 'Save'}
        </Button>
      }
    >
      <StaleNotice resource={resource} />
      {resource.error && (
        <div className="p-4">
          <ProblemNotice error={resource.error} onRetry={resource.reload} />
        </div>
      )}
      {resource.loading && !current && <Spinner label="Reading the instance settings" />}
      {current && (
        <div className="space-y-3 p-4">
          <label className="flex items-start gap-2 text-xs text-ink-100">
            <input
              type="checkbox"
              className="mt-0.5"
              checked={form.selfService}
              onChange={(e) => edit({ selfService: e.target.checked })}
            />
            <span>
              <span className="font-semibold">Self-service circle creation</span>
              <span className="mt-0.5 block text-ink-400">
                This instance's stated policy on who may create a circle, published on{' '}
                <Mono>/meta</Mono> so a client knows whether to offer the option.
              </span>
              <span className="mt-0.5 block text-warn">
                Published, not yet enforced: <Mono>createCircle</Mono> still requires a grant of{' '}
                <Mono>instance.circle.create</Mono> whichever way this is set.
              </span>
            </span>
          </label>

          <div className="grid gap-3 md:grid-cols-2">
            <Field label="Instance name" hint="What /meta calls this instance.">
              <Input
                value={form.name}
                maxLength={80}
                onChange={(e) => edit({ name: e.target.value })}
              />
            </Field>
            <Field label="Timezone" hint="IANA name. Display only — every countdown is server time.">
              <Input value={form.timezone} onChange={(e) => edit({ timezone: e.target.value })} />
            </Field>
            <Field
              label="Public URL"
              hint="Read-only here: it must keep matching the redirect URI registered with every provider. Change $TOD_PUBLIC_URL, re-register the URI, restart."
            >
              <Input value={current.public_url} readOnly disabled />
            </Field>
            <Field label="Reason" hint="Recorded in the ledger and shown in every listing.">
              <Input
                value={reason}
                maxLength={280}
                onChange={(e) => setReason(e.target.value)}
                placeholder="why this changed"
              />
            </Field>
          </div>

          <div>
            <h3 className="caps mb-1 text-[11px] text-ink-500">
              Recorded changes
            </h3>
            {changes.length === 0 ? (
              <Empty title="Nothing has changed since first-run setup." />
            ) : (
              <div className="overflow-x-auto">
                <table className="w-full border-collapse text-xs">
                  <thead>
                    <tr>
                      <Th>When</Th>
                      <Th>Setting</Th>
                      <Th>From</Th>
                      <Th>To</Th>
                      <Th>By</Th>
                      <Th>Reason</Th>
                    </tr>
                  </thead>
                  <tbody>
                    {changes.map((change) => (
                      <tr key={change.id}>
                        <Td className="tnum text-ink-400">{instant(change.changed_at)}</Td>
                        <Td className="text-ink-100">
                          <Mono>{change.setting}</Mono>
                        </Td>
                        <Td className="text-ink-400">{change.old_value || '—'}</Td>
                        <Td className="text-ink-100">{change.new_value || '—'}</Td>
                        <Td className="text-ink-400">
                          {/* A change with no identity was written by an operator holding the
                              database, which is a different fact from a person having decided it
                              — so it is named rather than rendered as a blank. */}
                          {change.by_console ? (
                            'the console'
                          ) : (
                            <Mono>{change.changed_by_identity_id}</Mono>
                          )}
                        </Td>
                        <Td className="text-ink-400">{change.reason || '—'}</Td>
                      </tr>
                    ))}
                  </tbody>
                </table>
              </div>
            )}
          </div>
        </div>
      )}
    </Card>
  )
}

/** Draft is the policy form's own state, held separately so a reload cannot overwrite typing. */
interface Draft {
  name: string
  timezone: string
  selfService: boolean
}

function ProviderRow({
  provider,
  onDone,
  onError,
}: {
  provider: AdminIdentityProvider
  onDone: () => void
  onError: (error: Error | null) => void
}) {
  const [busy, setBusy] = useState(false)
  const toggle = () => {
    setBusy(true)
    onError(null)
    api
      .updateIdentityProvider(
        {
          provider_id: provider.id,
          body: {
            enabled: !provider.enabled,
            // Enabling a provider with no verifiable subject needs the acknowledgement. It is not
            // a formality: it restates what revocation means for every circle that accepts it.
            ...(!provider.enabled && !provider.verifiable_subject
              ? { acknowledge_weak_revocation: true }
              : {}),
          },
        },
        // `*` — "whatever version is current" — and it is the only precondition this console can
        // send here, which is a limitation rather than a choice: the API publishes no operation
        // that returns one provider and its entity tag. `listAdminIdentityProviders` carries no
        // per-row tag, and the tag on the PATCH's own response arrives after the write. So two
        // operators toggling the same provider at once is last-write-wins, and the audit log is
        // what shows it happened.
        { ifMatch: '*' },
      )
      .then(onDone)
      .catch((err: unknown) => onError(toError(err)))
      .finally(() => setBusy(false))
  }

  return (
    <tr>
      <Td>
        <Mono>{provider.key}</Mono>
      </Td>
      {/* The same mark the join page brands its button with, from the same component and driven
          off the same field, so an operator can see which row is the one members will meet as a
          Discord button. Blurple on the console's own surface rather than white on blurple: both
          are sanctioned single-colour versions of the artwork, and this one is not a control. */}
      <Td className="text-ink-400" title="Immutable: kind decides verifiable_subject.">
        <span className="inline-flex items-center gap-1.5">
          {provider.kind === 'discord' && <DiscordMark className="text-discord-blurple" />}
          {provider.kind}
        </span>
      </Td>
      <Td className="text-ink-100">{provider.display_name}</Td>
      <Td>
        <Mono>{provider.client_id || '—'}</Mono>
      </Td>
      <Td className={provider.client_secret_set ? 'text-ink-300' : 'text-warn'}>
        {provider.client_secret_set ? 'set' : 'not set'}
      </Td>
      <Td>
        {provider.verifiable_subject ? (
          <span className="text-[var(--color-status-inwindow)]">durable</span>
        ) : (
          <span
            className="text-warn"
            title="No third party can tell us this account is gone, so revocation through it is advisory."
          >
            advisory
          </span>
        )}
      </Td>
      <Td>
        <Button onClick={toggle} disabled={busy}>
          {provider.enabled ? 'Disable' : 'Enable'}
        </Button>
      </Td>
    </tr>
  )
}

function AddProviderCard({ onDone }: { onDone: () => void }) {
  const [kind, setKind] = useState<'discord' | 'oidc' | 'local'>('discord')
  const [key, setKey] = useState('discord')
  const [displayName, setDisplayName] = useState('Discord')
  const [clientID, setClientID] = useState('')
  const [clientSecret, setClientSecret] = useState('')
  const [redirectURI, setRedirectURI] = useState(
    `${window.location.origin}/api/v1/auth/callback/discord`,
  )
  const [issuer, setIssuer] = useState('')
  const [authorizationEndpoint, setAuthorizationEndpoint] = useState('')
  const [jwksURI, setJWKSURI] = useState('')
  const [tokenEndpoint, setTokenEndpoint] = useState('')
  const [acknowledge, setAcknowledge] = useState(false)
  const [error, setError] = useState<Error | null>(null)
  const [busy, setBusy] = useState(false)

  const submit = () => {
    setBusy(true)
    setError(null)
    api
      .createIdentityProvider({
        body: {
          key: key.trim(),
          kind,
          display_name: displayName.trim(),
          enabled: false,
          client_id: kind === 'local' ? '' : clientID.trim(),
          client_secret: kind === 'local' ? '' : clientSecret,
          redirect_uri: kind === 'local' ? '' : redirectURI.trim(),
          token_endpoint: kind === 'oidc' ? tokenEndpoint.trim() : '',
          issuer: kind === 'oidc' ? issuer.trim() : '',
          authorization_endpoint: kind === 'oidc' ? authorizationEndpoint.trim() : '',
          jwks_uri: kind === 'oidc' ? jwksURI.trim() : '',
          acknowledge_weak_revocation: acknowledge,
        },
      })
      .then(onDone)
      .catch((err: unknown) => setError(toError(err)))
      .finally(() => setBusy(false))
  }

  return (
    <Card
      title="Add an identity provider"
      subtitle="Created DISABLED, so a half-configured OAuth application is never briefly live."
    >
      <div className="space-y-3 p-4">
        {error && <ProblemNotice error={error} />}

        <div className="grid gap-3 md:grid-cols-3">
          <Field label="Kind" hint="Immutable once created: it decides verifiable_subject.">
            <Select
              value={kind}
              className="w-full"
              onChange={(e) => {
                const next = e.target.value as typeof kind
                setKind(next)
                setKey(next)
                setRedirectURI(`${window.location.origin}/api/v1/auth/callback/${next}`)
              }}
            >
              <option value="discord">discord</option>
              <option value="oidc">oidc</option>
              <option value="local">local — self-asserted names, advisory revocation</option>
            </Select>
          </Field>
          <Field label="Key" hint="What /join dispatches on. Immutable.">
            <Input value={key} maxLength={40} onChange={(e) => setKey(e.target.value)} />
          </Field>
          <Field label="Display name">
            <Input
              value={displayName}
              maxLength={80}
              onChange={(e) => setDisplayName(e.target.value)}
            />
          </Field>
        </div>

        {kind !== 'local' && (
          <div className="grid gap-3 md:grid-cols-2">
            <Field
              label="Client id"
              hint="Your own OAuth application. Not a secret — it travels in every authorization URL."
            >
              <Input value={clientID} onChange={(e) => setClientID(e.target.value)} />
            </Field>
            <Field label="Client secret" hint="Write-only: no operation ever returns it.">
              <Input
                type="password"
                value={clientSecret}
                onChange={(e) => setClientSecret(e.target.value)}
              />
            </Field>
            <Field label="Redirect URI" hint="Register this exact value with the provider.">
              <Input value={redirectURI} onChange={(e) => setRedirectURI(e.target.value)} />
            </Field>
            {kind === 'oidc' && (
              <>
                <Field label="Issuer">
                  <Input value={issuer} onChange={(e) => setIssuer(e.target.value)} />
                </Field>
                <Field
                  label="Authorization endpoint"
                  hint="The browser goes here; this server never fetches it, so the https check is the only guard."
                >
                  <Input
                    value={authorizationEndpoint}
                    onChange={(e) => setAuthorizationEndpoint(e.target.value)}
                  />
                </Field>
                <Field label="Token endpoint">
                  <Input value={tokenEndpoint} onChange={(e) => setTokenEndpoint(e.target.value)} />
                </Field>
                <Field label="JWKS URI">
                  <Input value={jwksURI} onChange={(e) => setJWKSURI(e.target.value)} />
                </Field>
              </>
            )}
          </div>
        )}

        {kind === 'local' && (
          <Banner tone="warn" title="A local provider has no verifiable subject">
            Anybody can assert any name, and nobody can tell us an account is gone. Every circle
            that accepts it becomes weakly revocable and says so on every screen. Enabling it needs
            an explicit acknowledgement.
            <label className="mt-2 flex items-center gap-2">
              <input
                type="checkbox"
                checked={acknowledge}
                onChange={(e) => setAcknowledge(e.target.checked)}
              />
              <span>I understand revocation through this provider is advisory</span>
            </label>
          </Banner>
        )}

        <div className="flex justify-end">
          <Button variant="primary" onClick={submit} disabled={busy || !key.trim()}>
            {busy ? 'Creating…' : 'Create'}
          </Button>
        </div>
      </div>
    </Card>
  )
}
