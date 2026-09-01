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

import { api, type Circle, type PublicIdentityProvider, toError } from '../api'
import { usePrincipal } from '../app/principal'
import { useResource } from '../app/useResource'
import { ProblemNotice, StaleNotice } from '../components/Problem'
import { DiscordMark } from '../components/ProviderButton'
import { RevocationBanner } from '../components/RevocationBanner'
import { Banner, Button, Card, Empty, Field, Input, Mono, Spinner } from '../components/ui'
import { choicesFor, gateState, saveSet, type Choice, type GateState } from '../lib/gate'
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

      {principal.can('instance.circle.create') && <AnotherCircleCard />}

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
 * AnotherCircleCard points at the create form. It is a LINK and not a second copy of that form.
 *
 * The permission is instance-realm — `instance.circle.create` comes from the grant ledger, keyed
 * on an identity, and no role in any circle grants it — so the form cannot live inside this
 * screen: the section that reveals this one is gated on three circle-realm keys, and an instance
 * owner who is an ordinary member here holds none of them. This is the neighbour, not the home.
 */
function AnotherCircleCard() {
  const navigate = useNavigate()
  return (
    <Card
      title="Another circle"
      subtitle="Nothing caps how many circles a server carries or how many you belong to. What is fixed is which server THIS one is on."
      actions={<Button onClick={() => navigate('/circles/new')}>New circle…</Button>}
    >
      <p className="px-4 py-3 text-xs text-ink-400">
        You hold <Mono>instance.circle.create</Mono>, so you can create one from here. Read what the
        form says before you do: creating a circle writes the circle and nothing else — no
        membership, no owner and no invite — and the code that gives a circle its first owner is
        minted only by <Mono>tod-serve circle create</Mono>.
      </p>
    </Card>
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

  // What this press of Save can carry, and what it costs. `dropped` is not a filter applied
  // quietly: see [saveSet] — a disabled provider can neither be re-sent nor kept, so the owner is
  // told which one a save gives up before they press it.
  const { send, dropped, acknowledgeWeak } = saveSet(draft)

  const patch = (key: string, change: Partial<Choice>) =>
    setDraft((current) => current.map((c) => (c.key === key ? { ...c, ...change } : c)))

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
              providers: send.map((c) => ({
                key: c.key,
                ...(c.discord_guild_id.trim()
                  ? { discord_guild_id: c.discord_guild_id.trim() }
                  : {}),
                discord_required_role_ids: c.discord_required_role_ids,
              })),
              acknowledge_weak_revocation: acknowledgeWeak,
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

        {!readOnly && dropped.length > 0 && (
          <Banner
            tone="warn"
            title={`Saving will stop this circle accepting ${dropped
              .map((c) => c.display_name)
              .join(', ')}`}
          >
            <p>
              This instance has disabled{' '}
              {dropped.map((c) => c.key).join(', ')}, and this operation replaces the whole set: the
              server refuses a request that names a disabled provider, and a request that omits one
              removes it. There is no third outcome, so a save gives it up — nobody joins through it
              either way, and no existing membership is revoked.
            </p>
          </Banner>
        )}

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
          // A provider the instance disabled cannot be re-sent and cannot be kept, so there is no
          // decision left to offer. Leaving it tickable would be a control whose only reachable
          // outcome is the one it appears to prevent.
          disabled={readOnly || !choice.available}
          onChange={() => onChange({ accepted: !choice.accepted })}
        />
        {choice.kind === 'discord' && <DiscordMark className="text-discord-blurple" />}
        {choice.display_name} <Mono>{choice.key}</Mono>
        {!choice.available && (
          <span className="text-[10px] tracking-wide text-amber-400 uppercase">
            disabled instance-wide
          </span>
        )}
      </label>
      {!choice.available && choice.accepted && (
        <p className="mt-1 text-[11px] text-amber-300">
          This circle still accepts it on paper, and nobody can join through it: the instance
          operator disabled it. It is not gating anything and it does not count towards this
          circle&rsquo;s revocation strength. The next save removes it — an existing member who
          joined this way keeps their membership, because removing a provider revokes nobody.
        </p>
      )}

      {/* The gate of a provider that cannot be sent is read-only for the same reason its tickbox
          is: an edit here would be typed, look accepted, and reach nothing. */}
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
                disabled={readOnly || !choice.available}
                onChange={(e) => onChange({ discord_guild_id: e.target.value.trim() })}
              />
            </Field>
            <Field
              label="Required role ids"
              hint="Comma-separated. ANY ONE of them admits, so adding a role WIDENS who gets in rather than narrowing it. Empty means anyone in the server."
            >
              <Input
                value={choice.discord_required_role_ids.join(', ')}
                disabled={readOnly || !choice.available}
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
