// Create a circle. `instance.circle.create`, which is an INSTANCE-realm permission.
//
// It is a route of its own rather than a card on Circle Settings, and that is the same realm
// mismatch Circle Settings' own header describes, one level up. The Settings section is revealed
// by `circle.manage`, `circle.security.manage` or `circle.delete` — all circle-realm, all granted
// by a role in THIS circle. An instance owner who happens to be an ordinary member here holds none
// of them, and hanging the form off that section would hide it from exactly the person who is
// allowed to use it. Both screens link to it; neither owns it.
//
// **Two things this screen exists to say out loud, before the button rather than after it.**
//
// `server` is IMMUTABLE. ADR-0009: a circle is pinned to one server permanently, `updateCircle`
// answers `422 field_immutable`, and a `BEFORE UPDATE` trigger refuses it in the database even if
// the API ever forgot to. A guild raiding Blue and Green makes at least two circles — this form is
// where that should become obvious, not six weeks later when somebody tries to move one.
//
// What it must NOT imply is one circle per server. `membership` has no per-server uniqueness at
// all — `ux_membership_identity` is unique on `(circle_id, identity_id)` and there is no `server`
// column on it — so a person can hold a guild circle and an alliance circle both on Blue. The only
// server-scoped uniqueness anywhere is `ux_circle_name_norm_server`, on the NAME.
//
// Creating a circle does NOT put you in it. `POST /circles` writes the circle row and nothing
// else: no membership, no owner, no invite. That is not a bug this screen can fix from `web/`,
// and it is the difference between this form and `tod-serve circle create`, which mints the
// one-time owner code in the same breath. Saying so before the press is the whole reason the
// warning is a banner and not a footnote.

import { useState } from 'react'

import { api, body, toError, type CircleResponse, type CreateCircleInputBody } from '../api'
import { usePrincipal } from '../app/principal'
import { useStepUp } from '../app/stepup'
import { ProblemNotice } from '../components/Problem'
import { Banner, Button, Card, Field, Input, Mono, Select } from '../components/ui'

/** SERVERS is the enum the API accepts, in the order the community says them. */
const SERVERS: { value: CreateCircleInputBody['server']; label: string }[] = [
  { value: 'blue', label: 'Blue' },
  { value: 'green', label: 'Green' },
  { value: 'red', label: 'Red' },
]

interface Draft {
  name: string
  server: CreateCircleInputBody['server'] | ''
  description: string
  timezone: string
}

const EMPTY: Draft = { name: '', server: '', description: '', timezone: '' }

export function NewCircle() {
  const principal = usePrincipal()
  const stepUp = useStepUp()
  const [draft, setDraft] = useState<Draft>(EMPTY)
  // The retry key for this exact draft. The transport mints one per call when nobody supplies one,
  // which is right for an operation a duplicate of is recoverable — and this is not one of those:
  // a second circle created by a retried request has no owner, no member and no `circle.delete`
  // anybody can reach, so it is a row nobody can remove. Held in state and REROLLED on every edit,
  // because replaying a key against a changed body is `idempotency_key_reused`, which is a client
  // bug that reads on screen like a server fault.
  const [attempt, setAttempt] = useState(() => crypto.randomUUID())
  const [busy, setBusy] = useState(false)
  const [error, setError] = useState<Error | null>(null)
  const [created, setCreated] = useState<CircleResponse | null>(null)

  const canCreate = principal.can('instance.circle.create')
  const name = draft.name.trim()
  const ready = name !== '' && draft.server !== ''

  const patch = (change: Partial<Draft>) => {
    setDraft((current) => ({ ...current, ...change }))
    setAttempt(crypto.randomUUID())
  }

  const create = () => {
    if (draft.server === '') return
    setBusy(true)
    setError(null)
    api
      .createCircle(
        {
          body: {
            name,
            server: draft.server,
            ...(draft.description.trim() ? { description: draft.description.trim() } : {}),
            ...(draft.timezone.trim() ? { timezone: draft.timezone.trim() } : {}),
          },
        },
        { idempotencyKey: attempt },
      )
      .then((result) => {
        setCreated(body(result))
        setDraft(EMPTY)
        // A fresh key for whatever is created next. Keeping this one would replay THIS circle.
        setAttempt(crypto.randomUUID())
      })
      .catch((err: unknown) => setError(toError(err)))
      .finally(() => setBusy(false))
  }

  if (!canCreate) {
    return (
      <div className="space-y-3">
        <Banner tone="info" title="Creating a circle needs instance.circle.create">
          <p>
            It is an <strong>instance-realm</strong> permission, so no role in any circle grants it
            — not even owner. It comes from the instance grant ledger, keyed on your identity, and
            an instance owner adds it with <Mono>tod-serve instance grant</Mono> at the console.
          </p>
          <p className="mt-1">
            That is deliberate rather than an oversight: what the permission answers outlives any
            one circle, so it cannot hang off a membership in one.
          </p>
        </Banner>
      </div>
    )
  }

  return (
    <div className="space-y-3">
      {error && <ProblemNotice error={error} />}

      {created && <Created circle={created} />}

      {!principal.steppedUp && (
        <Banner tone="accent" title="This one needs you to have proved who you are recently">
          Creating a circle is in the capability floor: no API token reaches it at any scope, and a
          browser session has to have proved who you are inside the step-up window — the strict
          one, because it hands you a circle you own. If the save is refused with{' '}
          <Mono>step_up_required</Mono>, the banner it raises carries a{' '}
          <strong>Prove it&rsquo;s you</strong> button that keeps this session and returns you to
          this page. Do not sign out: nothing you have typed is sent until you press the button,
          and signing back in is what fills your device list. See{' '}
          <button className="underline" onClick={() => stepUp?.request()}>
            prove it now
          </button>
          .
        </Banner>
      )}

      <Banner tone="warn" title="Creating a circle does not put you in it">
        <p>
          This writes the circle and nothing else: <strong>no membership, no owner, no invite.</strong>{' '}
          The one-time owner code that makes somebody a circle’s first owner is minted by{' '}
          <Mono>tod-serve circle create</Mono> as part of creating, and there is no operation — in
          this console or in the API — that mints one for a circle that already exists.
        </p>
        <p className="mt-1">
          So a circle created here has no way in yet, and with no member holding{' '}
          <Mono>circle.delete</Mono> it cannot be removed either. If you want a circle somebody can
          actually join today, run <Mono>tod-serve circle create</Mono> on the host instead: it does
          both in one step and prints the code.
        </p>
      </Banner>

      <Card
        title="New circle"
        subtitle="A circle is the tenant: its own members, its own invites, its own board."
        actions={
          <Button variant="primary" onClick={create} disabled={!ready || busy}>
            {busy ? 'Creating…' : 'Create circle'}
          </Button>
        }
      >
        <div className="space-y-4 p-4">
          <div className="grid gap-3 md:grid-cols-2">
            <Field
              label="Name"
              hint="Unique per server. It is what members see in the header and on the sign-in screen, and it can be changed later."
            >
              <Input
                value={draft.name}
                maxLength={80}
                placeholder="Kittens Who Say Meep"
                onChange={(e) => patch({ name: e.target.value })}
              />
            </Field>
            <Field
              label="Server"
              hint="Immutable. This is the one field on a circle that can never change."
            >
              <Select
                className="w-full"
                value={draft.server}
                onChange={(e) => patch({ server: e.target.value as Draft['server'] })}
              >
                <option value="">Choose a server…</option>
                {SERVERS.map((s) => (
                  <option key={s.value} value={s.value}>
                    {s.label}
                  </option>
                ))}
              </Select>
            </Field>
          </div>

          <Banner tone="warn" title="A circle is pinned to one server, permanently">
            <p>
              Blue, Green and Red are different worlds: different spawn clocks, different guilds
              racing the same targets. A time of death on one says nothing at all about another, so
              there is no combined view anywhere in tod-serve and no way to move a circle
              afterwards — <Mono>updateCircle</Mono> answers <Mono>422 field_immutable</Mono>, and a{' '}
              <Mono>BEFORE UPDATE</Mono> trigger refuses it in the database.
            </p>
            <p className="mt-1">
              <strong>A guild raiding Blue and Green makes at least two circles.</strong> Two member
              lists, two invite sets, two sets of officers. That composes better than it sounds: the
              reporting plugin holds several destinations and ticks the ones a kill reports to, so
              “report to my Blue circle and my Green circle” is two ticked boxes rather than one
              circle spanning both.
            </p>
            <p className="mt-1">
              It does <strong>not</strong> mean one circle per server. Nothing limits how many
              circles a server carries, or how many of them one person belongs to — a raider can
              hold a guild circle and an alliance circle both on Blue. The only thing unique per
              server is the <strong>name</strong>, so pick one that says which of them this is.
            </p>
          </Banner>

          <div className="grid gap-3 md:grid-cols-2">
            <Field label="Description" hint="Free text, shown in the header. Optional.">
              <Input
                value={draft.description}
                maxLength={500}
                onChange={(e) => patch({ description: e.target.value })}
              />
            </Field>
            <Field
              label="Timezone"
              hint="An IANA name such as America/New_York. Display only — every instant is stored in UTC. Defaults to UTC."
            >
              <Input
                value={draft.timezone}
                placeholder="UTC"
                onChange={(e) => patch({ timezone: e.target.value })}
              />
            </Field>
          </div>
        </div>
      </Card>
    </div>
  )
}

/**
 * Created is what now exists, and what it is still missing.
 *
 * It deliberately does NOT remember the circle in this browser's record. That record is the list
 * the sign-in screen offers, and offering a circle nobody is a member of is an offer that answers
 * `404` — the console would be inviting somebody into a door that does not open for them.
 */
function Created({ circle }: { circle: CircleResponse }) {
  return (
    <Card title="Created" subtitle="The circle exists. Nobody is in it.">
      <div className="space-y-3 p-4">
        <div className="grid gap-3 md:grid-cols-3">
          <Field label="Name">
            <p className="text-xs text-ink-100">{circle.name}</p>
          </Field>
          <Field label="Server">
            <p className="caps text-xs text-ink-100">{circle.server}</p>
          </Field>
          <Field label="Circle id">
            <Mono>{circle.id}</Mono>
          </Field>
        </div>
        <Banner tone="warn" title="Nobody can join it yet">
          Keep that id. Giving this circle its first owner needs the one-time code that only{' '}
          <Mono>tod-serve circle create</Mono> mints, so this row is waiting on a change to the
          server rather than on anything you can do here. It is not on your circle list, and it will
          not appear on the sign-in screen — you are not a member of it.
        </Banner>
      </div>
    </Card>
  )
}
