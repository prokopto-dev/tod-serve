// Circle settings: the Discord gate, the providers this circle accepts, its name, and its deletion.
//
// This screen exists because of a REALM mismatch. `setCircleProviders` requires
// `circle.security.manage` — a CIRCLE-realm permission an owner holds — and its only affordance
// used to sit on the instance-admin screen, behind `instance.security.manage`, which no circle
// role grants and no token reaches at any scope. A circle owner could configure their own guild
// gate over the API and not through the console. The editor MOVED here; it was not copied, because
// two gate editors is how two gate editors drift.
//
// Every control is gated on the permission ITS OWN route requires, not on one blanket check:
// providers on `circle.security.manage`, the name on `circle.manage`, deletion on `circle.delete`.
// A screen with one gate would have re-made the mistake it exists to fix, one realm down.
//
// The provider list comes from `listIdentityProviders` — the PUBLIC one — rather than from
// `listAdminIdentityProviders`, for the same reason: the admin list is instance-realm and would
// have 403'd for exactly the person this screen is for. The public list carries only ENABLED
// providers, so anything this circle already accepts that the instance has since disabled is
// merged back in from `accepted_providers` and shown as disabled. Dropping it would hide a row
// that is still gating joins and still counting towards the revocation strength.
//
// `server` is rendered and never editable: a circle is pinned to one server immutably (ADR-0009),
// a `BEFORE UPDATE` trigger enforces it, and `updateCircle` answers `422 field_immutable`. An
// input that always fails is worse than no input.

import { useState } from 'react'
import { useNavigate } from 'react-router-dom'

import { api, type Circle, type ProviderView, type PublicIdentityProvider, toError } from '../api'
import { usePrincipal } from '../app/principal'
import { useResource } from '../app/useResource'
import { ProblemNotice, StaleNotice } from '../components/Problem'
import { DiscordMark } from '../components/ProviderButton'
import { RevocationBanner } from '../components/RevocationBanner'
import { Banner, Button, Card, Empty, Field, Input, Mono, Spinner } from '../components/ui'
import { forgetCircle } from '../lib/storage'

export function CircleSettings() {
  const principal = usePrincipal()
  const circleID = principal.view.circle_id
  const [error, setError] = useState<Error | null>(null)

  const circle = useResource(
    (signal) => api.getCircle({ circle_id: circleID }, { signal }).then((r) => r.data),
    [circleID],
  )
  // Public, and deliberately so: it is what a circle owner can read. It lists the instance's
  // ENABLED providers and never a secret.
  const providers = useResource(
    (signal) => api.listIdentityProviders({}, { signal }).then((r) => r.data),
    [],
  )

  const data = circle.data
  const canSecurity = principal.can('circle.security.manage')
  const canManage = principal.can('circle.manage')
  const canDelete = principal.can('circle.delete')

  return (
    <div className="space-y-3">
      {/* Both of this screen's resources, at the top, because every card below is drawn from one
          of them and a refresh here always follows a write. */}
      <StaleNotice resource={circle} />
      <StaleNotice resource={providers} />

      {error && <ProblemNotice error={error} />}
      {circle.error && <ProblemNotice error={circle.error} onRetry={circle.reload} />}
      {providers.error && <ProblemNotice error={providers.error} onRetry={providers.reload} />}
      {circle.loading && !data && <Spinner label="Reading the circle" />}

      {data && (
        <Card
          title="This circle"
          subtitle="What it is, and the one thing about it that can never change."
        >
          <div className="grid gap-3 p-4 md:grid-cols-3">
            <Field label="Name">
              <p className="text-xs text-ink-100">{data.name}</p>
            </Field>
            <Field
              label="Server"
              hint="Immutable. A circle is pinned to one server and there is no combined view anywhere; changing it is refused by a database trigger, not just by the API."
            >
              <p className="text-xs tracking-wide text-ink-100 uppercase">{data.server}</p>
            </Field>
            <Field label="Circle id">
              <Mono>{data.id}</Mono>
            </Field>
          </div>
        </Card>
      )}

      {data && canManage && (
        // Keyed on the version, so a circle somebody else renamed re-seeds this form rather than
        // leaving a draft that would silently put the old name back.
        <CircleNameCard
          key={data.updated_at}
          circle={data}
          onDone={circle.reload}
          onError={setError}
        />
      )}

      {data && providers.data && (
        <CircleProvidersCard
          key={data.updated_at}
          circle={data}
          available={providers.data.items ?? []}
          readOnly={!canSecurity}
          onDone={circle.reload}
          onError={setError}
        />
      )}

      {data && canDelete && <DeleteCircleCard circle={data} onError={setError} />}

      {data && !canSecurity && !canManage && !canDelete && (
        <Banner tone="info" title="You can see this circle's settings and change none of them">
          Renaming needs <Mono>circle.manage</Mono>, the identity gate needs{' '}
          <Mono>circle.security.manage</Mono>, and deletion needs <Mono>circle.delete</Mono>. All
          three are circle-realm: an owner of this circle can grant them, and no instance operator
          has to be involved.
        </Banner>
      )}
    </div>
  )
}

/**
 * CircleNameCard renames the circle. `circle.manage`.
 *
 * It reads before it writes so the `If-Match` is a version somebody actually saw — see
 * [CircleProvidersCard] for why `*` is not good enough.
 */
function CircleNameCard({
  circle,
  onDone,
  onError,
}: {
  circle: Circle
  onDone: () => void
  onError: (error: Error | null) => void
}) {
  const [name, setName] = useState(circle.name)
  const [busy, setBusy] = useState(false)

  const dirty = name.trim() !== '' && name.trim() !== circle.name

  const save = () => {
    setBusy(true)
    onError(null)
    api
      .getCircle({ circle_id: circle.id })
      .then((current) =>
        api.updateCircle(
          { circle_id: circle.id, body: { name: name.trim() } },
          current.etag ? { ifMatch: current.etag } : {},
        ),
      )
      .then(onDone)
      .catch((err: unknown) => onError(toError(err)))
      .finally(() => setBusy(false))
  }

  return (
    <Card
      title="Name"
      subtitle="What members see in the header and on the sign-in screen. It changes no id and no history."
      actions={
        <Button variant="primary" onClick={save} disabled={busy || !dirty}>
          {busy ? 'Saving…' : 'Save'}
        </Button>
      }
    >
      <div className="p-4">
        <Field label="Circle name">
          <Input value={name} maxLength={80} onChange={(e) => setName(e.target.value)} />
        </Field>
      </div>
    </Card>
  )
}

/**
 * Choice is one provider this circle could accept, and the gate configured on it.
 *
 * It is a local shape rather than the wire's `ProviderView` because the two sources it is built
 * from carry different halves: the public list knows a provider exists and is enabled, and
 * `accepted_providers` knows the guild and roles this circle set on it. The write body needs only
 * the key and the gate, so nothing here invents a `provider_id` it never read.
 */
interface Choice {
  key: string
  kind: string
  display_name: string
  verifiable_subject: boolean
  /** available is false for a provider the INSTANCE has disabled since this circle accepted it. */
  available: boolean
  accepted: boolean
  discord_guild_id: string
  discord_required_role_ids: string[]
}

/** GateState is what a Discord gate actually admits. Three states, per `db/schema.hcl`. */
type GateState = 'none' | 'guild' | 'roles'

function gateState(choice: Choice): GateState {
  if (!choice.discord_guild_id.trim()) return 'none'
  return choice.discord_required_role_ids.length > 0 ? 'roles' : 'guild'
}

/**
 * choicesFor merges the instance's enabled providers with the ones this circle already accepts.
 *
 * The union rather than the public list alone: a provider the operator disabled AFTER this circle
 * accepted it is still on the circle, still stops nobody who is already in, and still counts
 * towards the revocation strength. It comes back with `available: false` and says so on the row.
 * Never hide a row silently.
 */
function choicesFor(circle: Circle, available: PublicIdentityProvider[]): Choice[] {
  const accepted = new Map<string, ProviderView>()
  for (const p of circle.accepted_providers ?? []) accepted.set(p.key, p)

  const out: Choice[] = available.map((p) => ({
    key: p.key,
    kind: p.kind,
    display_name: p.display_name,
    verifiable_subject: p.verifiable_subject,
    available: true,
    accepted: accepted.has(p.key),
    discord_guild_id: accepted.get(p.key)?.discord_guild_id ?? '',
    discord_required_role_ids: accepted.get(p.key)?.discord_required_role_ids ?? [],
  }))

  const listed = new Set(out.map((c) => c.key))
  for (const [key, p] of accepted) {
    if (listed.has(key)) continue
    out.push({
      key,
      kind: p.kind,
      display_name: p.display_name,
      verifiable_subject: p.verifiable_subject,
      available: p.available,
      accepted: true,
      discord_guild_id: p.discord_guild_id ?? '',
      discord_required_role_ids: p.discord_required_role_ids ?? [],
    })
  }
  return out
}

/**
 * CircleProvidersCard is the per-circle identity gate: which providers this circle accepts, and
 * the Discord guild and role ids that gate each one.
 *
 * MOVED here from the instance-admin screen, where it was unreachable by the only people who hold
 * the permission it needs. `circle.security.manage` — owner only, circle realm — because changing
 * which providers a circle accepts changes its revocation guarantee.
 *
 * The two things officers get backwards are stated at the point of editing rather than in a
 * document: ANY ONE listed role admits, so the list WIDENS access as it grows; and the gate is
 * checked at JOIN and re-authentication, never per request, so taking a role away does not take a
 * live token away.
 */
function CircleProvidersCard({
  circle,
  available,
  readOnly,
  onDone,
  onError,
}: {
  circle: Circle
  available: PublicIdentityProvider[]
  readOnly: boolean
  onDone: () => void
  onError: (error: Error | null) => void
}) {
  const [draft, setDraft] = useState<Choice[]>(() => choicesFor(circle, available))
  const [busy, setBusy] = useState(false)

  const patch = (key: string, change: Partial<Choice>) =>
    setDraft((current) => current.map((c) => (c.key === key ? { ...c, ...change } : c)))

  // Read the circle, then write with the entity tag that read returned. `*` would be accepted and
  // would mean "whatever is there now" — this console overwriting another owner's change having
  // read nothing, which is exactly what the precondition exists to prevent. The 412 carries the
  // current representation, so a collision costs no extra round trip to recover from.
  const save = () => {
    setBusy(true)
    onError(null)
    const chosen = draft.filter((c) => c.accepted)
    api
      .getCircle({ circle_id: circle.id })
      .then((current) =>
        api.setCircleProviders(
          {
            circle_id: circle.id,
            body: {
              providers: chosen.map((c) => ({
                key: c.key,
                ...(c.discord_guild_id.trim()
                  ? { discord_guild_id: c.discord_guild_id.trim() }
                  : {}),
                discord_required_role_ids: c.discord_required_role_ids,
              })),
              acknowledge_weak_revocation: chosen.some((c) => !c.verifiable_subject),
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
      title="Who may join this circle"
      subtitle="The identity providers this circle accepts, and the Discord gate on each. Changing this changes the circle's revocation guarantee."
      actions={
        readOnly ? null : (
          <Button variant="primary" onClick={save} disabled={busy}>
            {busy ? 'Saving…' : 'Save'}
          </Button>
        )
      }
    >
      <div className="space-y-3 p-4">
        <RevocationBanner
          strength={circle.revocation_strength}
          reasons={circle.revocation_weak_reasons}
          weakProviders={circle.weak_providers}
        />

        {readOnly && (
          <Banner tone="info" title="Read only: editing this needs circle.security.manage">
            You are seeing what the gate is, not editing it. The permission is circle-realm, so an
            owner of this circle can grant it — no instance operator is involved.
          </Banner>
        )}

        {draft.length === 0 && (
          <Empty title="This instance has no enabled identity provider.">
            Nobody can join this circle until an instance operator adds one and enables it.
          </Empty>
        )}

        {draft.map((choice) => (
          <ProviderChoice
            key={choice.key}
            choice={choice}
            readOnly={readOnly}
            onChange={(change) => patch(choice.key, change)}
          />
        ))}

        <Banner tone="warn" title="The gate decides who gets IN. It does not get anybody OUT">
          <p>
            It is checked when somebody joins and when they re-authenticate, and at no other moment.
            Removing a Discord role does <strong>not</strong> revoke a personal access token that
            has already been issued: there is no bot polling guild membership, and continuous
            enforcement is named on the roadmap as deferred rather than implied to be here.
          </p>
          <p className="mt-1 italic">
            Taking someone&rsquo;s raider role away stops them getting back in. It does not get them
            out.
          </p>
          <p className="mt-1">
            The mechanism that works immediately, on their very next request, is revoking the member
            on <strong>Members</strong>. Membership state is checked on every call.
          </p>
        </Banner>
      </div>
    </Card>
  )
}

/** ProviderChoice is one provider's row: accepted or not, and — for Discord — the gate on it. */
function ProviderChoice({
  choice,
  readOnly,
  onChange,
}: {
  choice: Choice
  readOnly: boolean
  onChange: (change: Partial<Choice>) => void
}) {
  return (
    <div className="rounded border border-ink-700 bg-ink-850 p-3">
      <label className="flex items-center gap-2 text-xs text-ink-100">
        <input
          type="checkbox"
          checked={choice.accepted}
          disabled={readOnly}
          onChange={() => onChange({ accepted: !choice.accepted })}
        />
        {choice.kind === 'discord' && <DiscordMark className="text-discord-blurple" />}
        {choice.display_name} <Mono>{choice.key}</Mono>
        {!choice.available && (
          <span
            className="text-[10px] tracking-wide text-amber-400 uppercase"
            title="The instance operator disabled this provider. It still counts towards this circle's revocation strength, and nobody new joins through it."
          >
            disabled instance-wide
          </span>
        )}
      </label>

      {choice.accepted && choice.kind === 'discord' && (
        <div className="mt-2 space-y-2">
          <GateSummary
            state={gateState(choice)}
            roleCount={choice.discord_required_role_ids.length}
          />
          <div className="grid gap-3 md:grid-cols-2">
            <Field
              label="Discord server (guild) id"
              hint="Empty means no guild gate at all: an invite is then the only thing between a Discord account and this circle."
            >
              <Input
                value={choice.discord_guild_id}
                disabled={readOnly}
                onChange={(e) => onChange({ discord_guild_id: e.target.value.trim() })}
              />
            </Field>
            <Field
              label="Required role ids"
              hint="Comma-separated. ANY ONE of them admits, so adding a role WIDENS who gets in rather than narrowing it. Empty means anyone in the server."
            >
              <Input
                value={choice.discord_required_role_ids.join(', ')}
                disabled={readOnly}
                onChange={(e) =>
                  onChange({
                    discord_required_role_ids: e.target.value
                      .split(',')
                      .map((r) => r.trim())
                      .filter(Boolean),
                  })
                }
              />
            </Field>
          </div>
        </div>
      )}
    </div>
  )
}

/**
 * GateSummary renders what the gate as configured actually admits, in one sentence.
 *
 * Three states rather than two, because "no guild" and "guild, no roles" are wildly different
 * doors and two form fields alone make them look like the same one, half filled in. It is phrased
 * as WHO GETS IN rather than as which fields are set: an officer reading "anyone in that server"
 * knows immediately whether that is what they meant.
 */
function GateSummary({ state, roleCount }: { state: GateState; roleCount: number }) {
  if (state === 'none') {
    return (
      <p className="rounded border border-amber-800/70 bg-amber-950/40 px-2.5 py-1.5 text-[11px] text-amber-200">
        <strong>No gate.</strong> Anybody with a Discord account and an invite to this circle gets
        in; Discord membership is not checked at all.
      </p>
    )
  }
  if (state === 'guild') {
    return (
      <p className="rounded border border-ink-600 bg-ink-900 px-2.5 py-1.5 text-[11px] text-ink-200">
        <strong>Anyone in that server.</strong> Guild membership is required and no role is. That is
        the role list at its most permissive, not a gate waiting to be finished.
      </p>
    )
  }
  return (
    <p className="rounded border border-accent-600/60 bg-accent-600/10 px-2.5 py-1.5 text-[11px] text-accent-400">
      <strong>
        Anyone in that server holding any one of {roleCount} role{roleCount === 1 ? '' : 's'}.
      </strong>{' '}
      Any ONE of them admits — not all of them. A second role lets MORE people in, never fewer.
    </p>
  )
}

/**
 * DeleteCircleCard deletes the circle, and says what deletion actually is.
 *
 * It is a TOMBSTONE, not an erasure: `tod_report`, `quake_event`, `invite_redemption` and
 * `audit_log` are append-only by trigger, so with foreign keys on, a circle carrying any of those
 * rows could not be row-deleted at all. The invariant wins and the circle is soft-deleted — every
 * read answers 404 afterwards, including the owner's, and their own credential stops working on
 * the next request, because a membership in a circle that does not exist is not a membership.
 *
 * The confirmation is the circle's own name, typed. A `confirm()` dialog is one keystroke from
 * yes, and this is the one button on the console that ends every membership in a raid group.
 */
function DeleteCircleCard({
  circle,
  onError,
}: {
  circle: Circle
  onError: (error: Error | null) => void
}) {
  const navigate = useNavigate()
  const [typed, setTyped] = useState('')
  const [busy, setBusy] = useState(false)

  const armed = typed.trim() === circle.name

  const remove = () => {
    setBusy(true)
    onError(null)
    api
      .deleteCircle({ circle_id: circle.id })
      .then(() => {
        // The sign-in screen offers circles this browser has signed into. Keeping a deleted one on
        // that list is an offer that answers 404.
        forgetCircle(circle.id)
        navigate('/signin', { replace: true })
      })
      .catch((err: unknown) => {
        onError(toError(err))
        setBusy(false)
      })
  }

  return (
    <Card
      title="Delete this circle"
      subtitle="Ends every membership in it, including yours, on the next request."
    >
      <div className="space-y-3 p-4">
        <Banner tone="warn" title="Deletion is a tombstone, not an erasure">
          <p>
            The circle stops existing to every reader: reads answer <Mono>404</Mono>, invites stop
            resolving, and your own credential stops working on your next request. What it does not
            do is delete history — reports, quake events, invite redemptions and the audit log are
            append-only, and the audit row naming you as the person who deleted this circle is
            written before the tombstone is.
          </p>
          <p className="mt-1">
            Revoked members&rsquo; reports counted before, and they still count now. Revocation and
            deletion control access; neither rewrites what was reported.
          </p>
          <p className="mt-1">
            If you want the circle to stop being used but stay readable, this is the wrong button —
            revoke the memberships instead.
          </p>
        </Banner>
        <Field
          label={`Type ${circle.name} to confirm`}
          hint="The circle's exact name. Nothing else arms the button."
        >
          <Input value={typed} onChange={(e) => setTyped(e.target.value)} />
        </Field>
        <div className="flex justify-end">
          <Button variant="danger" onClick={remove} disabled={!armed || busy}>
            {busy ? 'Deleting…' : 'Delete this circle'}
          </Button>
        </div>
      </div>
    </Card>
  )
}
