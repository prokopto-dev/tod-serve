// Instance administration — identity providers, and the per-circle Discord gate.
//
// This is the screen that makes the operator's Discord gate configurable without a terminal.
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

import { useState } from 'react'

import { api, type AdminIdentityProvider, type Circle, type ProviderView, toError } from '../api'
import { usePrincipal } from '../app/principal'
import { useResource } from '../app/useResource'
import { ProblemNotice } from '../components/Problem'
import { RevocationBanner } from '../components/RevocationBanner'
import { Banner, Button, Card, Empty, Field, Input, Mono, Select, Spinner, Td, Th } from '../components/ui'

export function InstanceAdmin() {
  const principal = usePrincipal()
  const [error, setError] = useState<Error | null>(null)
  const [adding, setAdding] = useState(false)

  const providers = useResource(
    (signal) => api.listAdminIdentityProviders({}, { signal }).then((r) => r.data),
    [],
  )
  const circle = useResource(
    (signal) =>
      api.getCircle({ circle_id: principal.view.circle_id }, { signal }).then((r) => r.data),
    [principal.view.circle_id],
  )

  const rows = providers.data?.items ?? []

  return (
    <div className="space-y-3">
      {error && <ProblemNotice error={error} />}

      <Card
        title="Identity providers"
        subtitle="Instance-wide. A circle chooses which of these it accepts; this is what exists to choose from."
        actions={<Button onClick={() => setAdding((v) => !v)}>{adding ? 'Cancel' : 'Add provider'}</Button>}
      >
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

      {circle.data && (
        <CircleProvidersCard
          circle={circle.data}
          available={rows}
          onDone={circle.reload}
          onError={setError}
        />
      )}
    </div>
  )
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
      <Td className="text-ink-400" title="Immutable: kind decides verifiable_subject.">
        {provider.kind}
      </Td>
      <Td className="text-ink-100">{provider.display_name}</Td>
      <Td>
        <Mono>{provider.client_id || '—'}</Mono>
      </Td>
      <Td className={provider.client_secret_set ? 'text-ink-300' : 'text-amber-400'}>
        {provider.client_secret_set ? 'set' : 'not set'}
      </Td>
      <Td>
        {provider.verifiable_subject ? (
          <span className="text-[var(--color-status-inwindow)]">durable</span>
        ) : (
          <span
            className="text-amber-400"
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

/**
 * CircleProvidersCard is the per-circle half: which providers this circle accepts, and the Discord
 * guild and role ids that gate it.
 *
 * EMPTY REQUIRED ROLES MEANS ANYONE IN THE GUILD, and holding ANY listed role admits. Both are
 * stated on the form, because getting either backwards is a gate that silently admits everybody.
 *
 * This is `circle.security.manage` — owner only — because changing which providers a circle
 * accepts changes its revocation guarantee.
 */
function CircleProvidersCard({
  circle,
  available,
  onDone,
  onError,
}: {
  circle: Circle
  available: AdminIdentityProvider[]
  onDone: () => void
  onError: (error: Error | null) => void
}) {
  const accepted = circle.accepted_providers ?? []
  const [draft, setDraft] = useState<ProviderView[]>(accepted)
  const [busy, setBusy] = useState(false)

  const toggle = (provider: AdminIdentityProvider) => {
    setDraft((current) =>
      current.some((p) => p.key === provider.key)
        ? current.filter((p) => p.key !== provider.key)
        : [
            ...current,
            {
              provider_id: provider.id,
              key: provider.key,
              kind: provider.kind,
              display_name: provider.display_name,
              verifiable_subject: provider.verifiable_subject,
              available: provider.enabled,
              discord_required_role_ids: [],
            },
          ],
    )
  }

  const setGuild = (key: string, guild: string) => {
    setDraft((current) =>
      current.map((p) => (p.key === key ? { ...p, discord_guild_id: guild } : p)),
    )
  }

  const setRoles = (key: string, roles: string) => {
    setDraft((current) =>
      current.map((p) =>
        p.key === key
          ? {
              ...p,
              discord_required_role_ids: roles
                .split(',')
                .map((r) => r.trim())
                .filter(Boolean),
            }
          : p,
      ),
    )
  }

  // Read the circle, then write with the entity tag that read returned. `*` would be accepted and
  // would mean "whatever is there now" — this console overwriting another owner's change having
  // read nothing, which is exactly what the precondition exists to prevent. The 412 carries the
  // current representation, so a collision costs no extra round trip to recover from.
  const save = () => {
    setBusy(true)
    onError(null)
    api
      .getCircle({ circle_id: circle.id })
      .then((current) =>
        api.setCircleProviders(
          {
            circle_id: circle.id,
            body: {
              providers: draft.map((p) => ({
                key: p.key,
                ...(p.discord_guild_id ? { discord_guild_id: p.discord_guild_id } : {}),
                discord_required_role_ids: p.discord_required_role_ids ?? [],
              })),
              acknowledge_weak_revocation: draft.some((p) => !p.verifiable_subject),
            },
          },
          current.etag ? { ifMatch: current.etag } : {},
        ),
      )
      .then(onDone)
      .catch((err: unknown) => onError(toError(err)))
      .finally(() => setBusy(false))
  }

  return (
    <Card
      title={`Providers accepted by ${circle.name}`}
      subtitle="Owner only: changing this changes the circle's revocation guarantee."
      actions={
        <Button variant="primary" onClick={save} disabled={busy}>
          {busy ? 'Saving…' : 'Save'}
        </Button>
      }
    >
      <div className="space-y-3 p-4">
        <RevocationBanner
          strength={circle.revocation_strength}
          reasons={circle.revocation_weak_reasons}
          weakProviders={circle.weak_providers}
        />
        {available.length === 0 && <Empty title="Add a provider above first." />}
        {available.map((provider) => {
          const chosen = draft.find((p) => p.key === provider.key)
          return (
            <div key={provider.id} className="rounded border border-ink-700 bg-ink-850 p-3">
              <label className="flex items-center gap-2 text-xs text-ink-100">
                <input type="checkbox" checked={Boolean(chosen)} onChange={() => toggle(provider)} />
                {provider.display_name} <Mono>{provider.key}</Mono>
                {!provider.enabled && (
                  <span className="text-[10px] tracking-wide text-amber-400 uppercase">
                    disabled instance-wide
                  </span>
                )}
              </label>
              {chosen && provider.kind === 'discord' && (
                <div className="mt-2 grid gap-3 md:grid-cols-2">
                  <Field
                    label="Discord server (guild) id"
                    hint="Leave empty to accept anybody with a Discord account."
                  >
                    <Input
                      value={chosen.discord_guild_id ?? ''}
                      onChange={(e) => setGuild(provider.key, e.target.value.trim())}
                    />
                  </Field>
                  <Field
                    label="Required role ids"
                    hint="Comma-separated. EMPTY MEANS ANYONE IN THE SERVER; holding ANY listed role admits."
                  >
                    <Input
                      value={(chosen.discord_required_role_ids ?? []).join(', ')}
                      onChange={(e) => setRoles(provider.key, e.target.value)}
                    />
                  </Field>
                </div>
              )}
            </div>
          )
        })}
        <p className="text-[11px] text-ink-500">
          The gate is evaluated at join AND at every re-authentication, through one evaluator. It is
          not re-checked continuously: losing a Discord role does not revoke a token that has
          already been issued — revoking the member does, on their very next request.
        </p>
      </div>
    </Card>
  )
}
