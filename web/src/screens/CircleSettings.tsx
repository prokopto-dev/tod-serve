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
// The Discord channel bindings are here for the same realm reason the providers are, and because
// they are the same KIND of decision: `bindCircleDiscordChannel` takes `circle.security.manage`
// rather than `circle.manage` because a binding says where this circle's data may be repeated,
// which is the family the identity gate is in. Both are in the capability floor, so no token
// reaches either at any scope and the session has to have been proved recently — which is why this
// screen offers the in-place re-authentication rather than letting an officer fill in a form and
// lose it to a `step_up_required`.
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

import {
  api,
  type Circle,
  type DiscordChannelBinding,
  type PublicIdentityProvider,
  toError,
} from '../api'
import { usePrincipal } from '../app/principal'
import { useStepUp } from '../app/stepup'
import { useResource } from '../app/useResource'
import { ProblemNotice, StaleNotice } from '../components/Problem'
import { DiscordMark } from '../components/ProviderButton'
import { RevocationBanner } from '../components/RevocationBanner'
import { Banner, Button, Card, Empty, Field, Input, Mono, Spinner } from '../components/ui'
import { isSnowflake, parseChannelReference, rebindFor, SNOWFLAKE_HINT } from '../lib/discord'
import { instant } from '../lib/format'
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

      {data && canSecurity && <DiscordChannelsCard circle={data} onError={setError} />}

      {principal.can('instance.circle.create') && <AnotherCircleCard />}

      {data && canDelete && <DeleteCircleCard circle={data} onError={setError} />}

      {data && !canSecurity && !canManage && !canDelete && (
        <Banner tone="info" title="You can see this circle's settings and change none of them">
          Renaming needs <Mono>circle.manage</Mono>, deletion needs <Mono>circle.delete</Mono>, and{' '}
          <Mono>circle.security.manage</Mono> covers both disclosure decisions — the identity gate
          and the Discord channels this circle answers in. All three are circle-realm: an owner of
          this circle can grant them, and no instance operator has to be involved.
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
 * DiscordChannelsCard is where this circle's Discord channel bindings are made and unmade.
 *
 * It is here rather than on a screen of its own because a binding is a per-circle disclosure
 * decision behind `circle.security.manage` — the same realm, the same permission and the same
 * capability floor as the identity gate above it.
 *
 * **The three things this card exists to say are all counter-intuitive**, and until now they lived
 * only in `docs/operations/discord-bot.md §6`, which is a page an officer reads once and a page an
 * officer does not read at all:
 *
 *  1. Binding a channel makes NOTHING visible. Visible replies are a second switch and it is off.
 *  2. A channel is not a circle. Guild membership is not circle membership.
 *  3. One guild may carry several circles, including two on the same P99 server.
 *
 * They are rendered beside the controls rather than summarised in a subtitle, because the decision
 * they qualify is taken here and a document cannot be read at the moment somebody ticks a box.
 */
function DiscordChannelsCard({
  circle,
  onError,
}: {
  circle: Circle
  onError: (error: Error | null) => void
}) {
  const principal = usePrincipal()
  const stepUp = useStepUp()
  const bindings = useResource(
    (signal) =>
      api.listCircleDiscordChannels({ circle_id: circle.id }, { signal }).then((r) => r.data),
    [circle.id],
  )

  const items = bindings.data?.items ?? []
  const visible = items.filter((b) => b.allow_visible)

  return (
    <Card
      title="Discord channels"
      subtitle="Which channels answer this circle's commands — and, separately, which of them a reply may be posted into where everyone can read it."
    >
      {/* The list is this card's whole subject and every reload here follows a write, so a refresh
          that failed silently would show a binding that is no longer there — or hide one that is. */}
      <StaleNotice resource={bindings} />

      <div className="space-y-3 p-4">
        <ProofNotice proved={principal.steppedUp} onProve={() => stepUp?.request()} />

        <Banner tone="warn" title="Binding a channel does not make anything visible">
          <p>
            There are <strong>two switches, not one</strong>. Binding says which circle a{' '}
            <Mono>/tod</Mono> command in that channel is about. Whether the answer is posted where
            the channel can read it is a second decision, and it is <strong>off</strong> on every
            new binding — the default is in the database, not in this form.
          </p>
          <p className="mt-1">
            The reason it is a decision rather than a setting: Discord has{' '}
            <strong>no channel-membership API</strong>. There is no call that answers &ldquo;who can
            read this channel&rdquo;, so this server cannot tell you who would see a visible message
            and does not pretend to. You can. That is the whole reason the switch is yours.
          </p>
        </Banner>

        <Banner tone="warn" title="A channel is not a circle">
          <p>
            Guild membership is not circle membership. Anybody who can read the channel reads a
            visible reply: guild members who were never in this circle, members you have{' '}
            <strong>revoked</strong> from the circle but not from Discord, and anybody a role change
            lets in next year — Discord keeps scrollback, and unbinding unsays nothing already
            posted.
          </p>
          <p className="mt-1">
            A visible reply is also composed with the <strong>invoker&rsquo;s</strong> permissions.
            An officer who can see reporter attribution and asks for a visible answer publishes that
            attribution to the channel.
          </p>
        </Banner>

        <Banner tone="info" title="One Discord server can carry several circles">
          <p>
            Including two on the same P99 server — your guild&rsquo;s own Blue roster and an
            alliance&rsquo;s Blue roster are two circles, and a person can be in both. Nothing here
            keys on the guild or on the server: a command resolves{' '}
            <strong>channel → circle id</strong>, which is the only thing that identifies a circle
            at all.
          </p>
          <p className="mt-1">
            So two circles in one guild need <strong>two channels</strong>, never one channel and a
            flag. This list is <strong>this circle&rsquo;s</strong> bindings; another circle in the
            same guild has its own and they are not readable from here. Binding a channel a live
            circle already holds is refused, and the refusal names no circle — saying which one held
            it would confirm that circle exists.
          </p>
        </Banner>

        {bindings.error && <ProblemNotice error={bindings.error} onRetry={bindings.reload} />}
        {bindings.loading && !bindings.data && <Spinner label="Reading the bindings" />}

        {bindings.data && items.length === 0 && (
          <Empty title="No channel is bound to this circle.">
            The bot still works in an unbound channel: it answers only the person who ran the
            command, and asks which of their circles they meant. Binding removes that question — it
            is not what makes the bot answer at all.
          </Empty>
        )}

        {items.map((binding) => (
          <BindingRow
            key={binding.discord_channel_id}
            binding={binding}
            onDone={bindings.reload}
            onError={onError}
          />
        ))}

        {visible.length > 0 && (
          <Banner
            tone="warn"
            title={`${visible.length} of these channels may be replied to visibly`}
          >
            Everyone who can read {visible.length === 1 ? 'that channel' : 'those channels'} sees
            those replies, for as long as Discord keeps them. Nothing on this instance can tell you
            who that is.
          </Banner>
        )}

        <BindChannelForm circle={circle} onDone={bindings.reload} onError={onError} />
      </div>
    </Card>
  )
}

/**
 * ProofNotice offers the re-authentication BEFORE the form rather than after the refusal.
 *
 * Every operation on this card is in the capability floor at the `sensitive` tier, and step-up is a
 * REDIRECT: proving it leaves the page and comes back to it. An officer who fills in two twenty-
 * digit ids, presses Bind and is answered `step_up_required` loses both of them on the way to
 * Discord and back. `ProblemNotice` still offers the way out when that happens — it is the same
 * affordance — but offering it first is the difference between one round trip and two.
 *
 * It draws only when the proof is KNOWN to be missing. `/me` is read when the shell mounts and the
 * window is minutes long, so `stepped_up: true` can go stale between that read and this render:
 * the honest thing is to warn on the direction that was true when it was read and to promise
 * nothing on the other, which is why there is no "you are proved" state here.
 */
function ProofNotice({ proved, onProve }: { proved: boolean; onProve: () => void }) {
  if (proved) return null
  return (
    <Banner tone="accent" title="These actions need a recent proof that you are still here">
      <p>
        Binding and unbinding are disclosure decisions, so they sit at the strict step-up tier.
        Prove it now and the form below will not throw your work away: proving it{' '}
        <strong>leaves this page</strong> and comes back, so doing it after you have typed two
        twenty-digit ids costs you both of them.
      </p>
      <p className="mt-1">
        It keeps this session and mints no new device. Signing out and back in is the wrong answer —
        that is a sign-in, and a sign-in mints a token.
      </p>
      <Button variant="primary" className="mt-2" onClick={onProve}>
        Prove it&rsquo;s you
      </Button>
    </Banner>
  )
}

/**
 * BindingRow is one live binding: what it discloses, and the two ways to change that.
 *
 * **The visible-reply switch is changed by unbinding and binding again, and this says so.** There
 * is no in-place edit any browser can reach: replacing a binding needs its exact entity tag, only
 * the PUT's own response carries one, and `listCircleDiscordChannels` answers no `ETag` — so a
 * console that has merely listed holds nothing to send. `If-Match: *` at a binding that exists is
 * refused with `412`, which is the concurrency rule doing its job rather than a bug to work around:
 * it is what stops an officer reversing a disclosure decision they have not read. See [rebindFor]
 * for what the two-step costs and why a failure between the halves cannot fail open.
 */
function BindingRow({
  binding,
  onDone,
  onError,
}: {
  binding: DiscordChannelBinding
  onDone: () => void
  onError: (error: Error | null) => void
}) {
  const [busy, setBusy] = useState(false)
  const [confirming, setConfirming] = useState<'visible' | 'unbind' | null>(null)
  const [unbound, setUnbound] = useState(false)

  // Narrowing is one press; widening is two. The asymmetry is the point: turning the switch off
  // reduces what this channel discloses, and a confirmation on the safe direction only teaches
  // people to click through the one that matters.
  const rebind = (allowVisible: boolean) => {
    const plan = rebindFor(binding, allowVisible)
    setBusy(true)
    setConfirming(null)
    setUnbound(false)
    onError(null)
    api
      .unbindCircleDiscordChannel({
        circle_id: binding.circle_id,
        discord_channel_id: binding.discord_channel_id,
      })
      .then(() =>
        api
          .bindCircleDiscordChannel(
            {
              circle_id: binding.circle_id,
              discord_channel_id: binding.discord_channel_id,
              body: {
                discord_guild_id: binding.discord_guild_id,
                allow_visible: plan.allow_visible,
              },
            },
            // A create, because the unbind above removed the row. The wildcard means "and it must
            // not exist yet", so a channel some other officer bound in the gap is a refusal here
            // rather than a silent overwrite of their decision.
            { ifMatch: '*' },
          )
          .catch((err: unknown) => {
            // The half-completed state, said out loud. It is the SAFE half — `worstOutcome` is
            // `unbound`, and an unbound channel discloses nothing — but somebody has to be told
            // that the channel they were reconfiguring now answers no commands.
            setUnbound(true)
            throw err
          }),
      )
      .then(() => onError(null))
      .catch((err: unknown) => onError(toError(err)))
      .finally(() => {
        setBusy(false)
        onDone()
      })
  }

  const unbind = () => {
    setBusy(true)
    setConfirming(null)
    onError(null)
    api
      .unbindCircleDiscordChannel({
        circle_id: binding.circle_id,
        discord_channel_id: binding.discord_channel_id,
      })
      .catch((err: unknown) => onError(toError(err)))
      .finally(() => {
        setBusy(false)
        onDone()
      })
  }

  return (
    <div className="rounded border border-ink-700 bg-ink-850 p-3">
      <div className="flex flex-wrap items-start justify-between gap-3">
        <div className="space-y-1">
          <p className="flex items-center gap-2 text-xs text-ink-100">
            <DiscordMark className="text-discord-blurple" />
            <span className="text-ink-400">channel</span> <Mono>{binding.discord_channel_id}</Mono>
          </p>
          <p className="text-[11px] text-ink-400">
            in server <Mono>{binding.discord_guild_id}</Mono> — an interaction carrying this channel
            from any other guild resolves to nothing.
          </p>
          <p className="text-[11px] text-ink-500">
            bound {instant(binding.created_at)} by membership{' '}
            <Mono>{binding.created_by_membership_id}</Mono>
          </p>
        </div>
        <Disclosure allowVisible={binding.allow_visible} />
      </div>

      {unbound && (
        <Banner tone="warn" title="This channel is now unbound, and the change did not land">
          The binding was removed and re-creating it failed, so the channel answers no commands
          until you bind it again below. That is the safe half of the two — an unbound channel
          discloses nothing — but it is not what you asked for.
        </Banner>
      )}

      {confirming === 'visible' && (
        <div className="mt-2 rounded border border-amber-800/70 bg-amber-950/40 p-2.5 text-[11px] text-amber-200">
          <p className="font-semibold">
            Everyone who can read this channel will see the replies. You are the only one who knows
            who that is.
          </p>
          <p className="mt-1">
            Not just this circle&rsquo;s members: anyone in the Discord server with access to the
            channel, members you have revoked from the circle but not from Discord, and anyone a
            role change admits later. Replies stay in scrollback and are searchable; unbinding
            afterwards does not unsay them.
          </p>
          <p className="mt-1">
            Only <Mono>/tod board</Mono> and <Mono>/tod status</Mono> can ever be visible.{' '}
            <Mono>/tod report</Mono> and <Mono>/tod circles</Mono> are never visible in any
            configuration, and the bot never posts unprompted.
          </p>
          <p className="mt-1 opacity-90">
            This unbinds the channel and binds it again — there is no in-place edit — so the binding
            will be re-dated and recorded as yours. If the second step fails the channel is left
            unbound, never more visible than it is now.
          </p>
          <div className="mt-2 flex gap-2">
            <Button variant="danger" onClick={() => rebind(true)} disabled={busy}>
              Yes, allow visible replies here
            </Button>
            <Button onClick={() => setConfirming(null)}>Keep it ephemeral</Button>
          </div>
        </div>
      )}

      {confirming === 'unbind' && (
        <div className="mt-2 rounded border border-ink-600 bg-ink-900 p-2.5 text-[11px] text-ink-200">
          <p className="font-semibold">
            Unbinding stops the next reply. It unsays nothing already posted.
          </p>
          <p className="mt-1">
            Messages in the channel are Discord&rsquo;s and Discord keeps them. Afterwards{' '}
            <Mono>/tod</Mono> still works there — ephemerally, asking which of their circles the
            person meant — and the channel can be bound to a different circle straight away.
          </p>
          <div className="mt-2 flex gap-2">
            <Button variant="danger" onClick={unbind} disabled={busy}>
              Yes, unbind this channel
            </Button>
            <Button onClick={() => setConfirming(null)}>Leave it bound</Button>
          </div>
        </div>
      )}

      <div className="mt-2 flex flex-wrap justify-end gap-2">
        {binding.allow_visible ? (
          <Button onClick={() => rebind(false)} disabled={busy}>
            {busy ? 'Working…' : 'Make replies ephemeral again'}
          </Button>
        ) : (
          <Button onClick={() => setConfirming('visible')} disabled={busy || confirming !== null}>
            Allow visible replies…
          </Button>
        )}
        <Button
          variant="danger"
          onClick={() => setConfirming('unbind')}
          disabled={busy || confirming !== null}
        >
          Unbind…
        </Button>
      </div>
    </div>
  )
}

/**
 * Disclosure says what a binding exposes, in the words the decision was made in.
 *
 * "allow_visible: false" is the wire value and it is the wrong thing to render: it describes a
 * field rather than an outcome, and the outcome is the only part an officer is deciding.
 */
function Disclosure({ allowVisible }: { allowVisible: boolean }) {
  if (!allowVisible) {
    return (
      <p className="rounded border border-ink-600 bg-ink-900 px-2.5 py-1.5 text-[11px] text-ink-200">
        <strong>Replies only to the person who ran the command.</strong> Nothing this channel
        answers is posted where anyone else can read it. This is the default and it is the safe one.
      </p>
    )
  }
  return (
    <p className="rounded border border-amber-800/70 bg-amber-950/40 px-2.5 py-1.5 text-[11px] text-amber-200">
      <strong>Replies here may be visible to the whole channel.</strong> Board and status answers
      can be posted where everybody who reads this channel — circle member or not — sees them and
      keeps seeing them.
    </p>
  )
}

/**
 * BindChannelForm binds a new channel.
 *
 * The paste field is generous on purpose — see [parseChannelReference]. The runbook's instruction
 * is Developer Mode → *Copy Channel ID*, which produces bare digits; what is actually on somebody's
 * clipboard is usually a channel link or a mention, and a link carries the GUILD as well. That
 * matters more than convenience: the two ids are twenty digits each, they are stored and compared
 * separately, and a transposed pair is accepted by every validation here and then resolves to
 * nothing at 2am. One paste that fills both cannot be transposed.
 */
function BindChannelForm({
  circle,
  onDone,
  onError,
}: {
  circle: Circle
  onDone: () => void
  onError: (error: Error | null) => void
}) {
  const [pasted, setPasted] = useState('')
  const [channelID, setChannelID] = useState('')
  const [guildID, setGuildID] = useState('')
  const [allowVisible, setAllowVisible] = useState(false)
  const [parseError, setParseError] = useState<string | null>(null)
  const [busy, setBusy] = useState(false)

  const accept = (raw: string) => {
    setPasted(raw)
    if (raw.trim() === '') {
      setParseError(null)
      return
    }
    const parsed = parseChannelReference(raw)
    if (!parsed.ok) {
      setParseError(parsed.reason)
      return
    }
    setParseError(null)
    setChannelID(parsed.ref.channel_id)
    // Only when the paste CARRIED one. A bare id names no guild, and overwriting a guild the
    // officer typed with an empty string would be the form losing their work silently.
    if (parsed.ref.guild_id) setGuildID(parsed.ref.guild_id)
  }

  const ready = isSnowflake(channelID) && isSnowflake(guildID)

  const bind = () => {
    setBusy(true)
    onError(null)
    api
      .bindCircleDiscordChannel(
        {
          circle_id: circle.id,
          discord_channel_id: channelID.trim(),
          body: { discord_guild_id: guildID.trim(), allow_visible: allowVisible },
        },
        // "And it must not exist yet". The server refuses `*` at a binding that is already there —
        // 412, carrying the current representation — which is exactly the collision this form must
        // not paper over: the field being overwritten decides who reads this circle's data.
        { ifMatch: '*' },
      )
      .then(() => {
        setPasted('')
        setChannelID('')
        setGuildID('')
        setAllowVisible(false)
        onDone()
      })
      .catch((err: unknown) => onError(toError(err)))
      .finally(() => setBusy(false))
  }

  return (
    <div className="rounded border border-ink-700 bg-ink-850 p-3">
      <p className="text-xs font-semibold text-ink-100">Bind a channel</p>
      <div className="mt-2 space-y-3">
        <Field
          label="Paste the channel"
          hint="Right-click the channel → Copy Link fills both ids below. A Copy Channel ID, or a <#…> mention pasted out of a message, fills the channel only."
          error={parseError ?? undefined}
        >
          <Input
            value={pasted}
            placeholder="https://discord.com/channels/…"
            onChange={(e) => accept(e.target.value)}
          />
        </Field>

        <div className="grid gap-3 md:grid-cols-2">
          <Field
            label="Channel id"
            hint={`${SNOWFLAKE_HINT}. One channel belongs to at most one circle — two circles in one Discord server need two channels.`}
          >
            <Input value={channelID} onChange={(e) => setChannelID(e.target.value.trim())} />
          </Field>
          <Field
            label="Discord server (guild) id"
            hint={`${SNOWFLAKE_HINT}. Stored and compared: an interaction naming this channel from a different server resolves to nothing.`}
          >
            <Input value={guildID} onChange={(e) => setGuildID(e.target.value.trim())} />
          </Field>
        </div>

        <label className="flex items-start gap-2 text-xs text-ink-100">
          <input
            type="checkbox"
            className="mt-0.5"
            checked={allowVisible}
            onChange={() => setAllowVisible(!allowVisible)}
          />
          <span>
            Allow replies the whole channel can read
            <span className="mt-0.5 block text-[11px] text-ink-500">
              Leave this off unless you have decided otherwise. It is the second switch, and binding
              without it is complete: the bot answers, only to the person who asked.
            </span>
          </span>
        </label>

        {allowVisible && (
          <Banner tone="warn" title="You are choosing to disclose this circle to a Discord channel">
            <p>
              Everyone who can read that channel will see board and status answers — guild members
              who are not in this circle, members you have revoked from the circle but not from
              Discord, and anyone a role change admits later. This server has no way to enumerate
              them, and unbinding later does not remove what was posted.
            </p>
            <p className="mt-1">
              <Mono>/tod report</Mono> and <Mono>/tod circles</Mono> stay private in every
              configuration, and the bot never posts unprompted.
            </p>
          </Banner>
        )}

        <div className="flex justify-end">
          <Button variant="primary" onClick={bind} disabled={!ready || busy}>
            {busy ? 'Binding…' : allowVisible ? 'Bind, with visible replies' : 'Bind this channel'}
          </Button>
        </div>
      </div>
    </div>
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
