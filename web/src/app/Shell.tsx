// The authenticated frame: a narrow rail of sections, a header that says which circle and which
// server, and the screen.
//
// A circle is pinned to ONE server, immutably, and there is no combined view anywhere — so the
// server is stated in the header rather than being a filter somebody can change. The switcher in
// that header does not widen the view either: it changes which circle this session is in, one at
// a time, by signing in again.

import { Link, NavLink, Outlet, useNavigate } from 'react-router-dom'
import { useEffect, useMemo, useState } from 'react'

import { api, body, toError } from '../api'
import { ProblemNotice, StaleNotice } from '../components/Problem'
import { RevocationBanner } from '../components/RevocationBanner'
import { Button, Spinner } from '../components/ui'
import { circleChoices, serverIsAmbiguous, type CircleChoice } from '../lib/circles'
import { classes } from '../lib/format'
import { rememberCircle, rememberedCircles } from '../lib/storage'
import { usePrincipalState } from './principal'
import { signedOutState } from './signedOut'
import { StepUpProvider } from './stepup'
import { useResource } from './useResource'

interface Section {
  to: string
  label: string
  /**
   * permissions are catalogue keys, and holding ANY ONE of them reveals the section. Absent means
   * everybody sees it.
   *
   * Any-one rather than all, because a screen whose controls are each gated on the permission its
   * OWN route requires is reachable by anybody holding one of them. Circle settings is the case
   * that made it a list: renaming is `circle.manage`, the identity gate is
   * `circle.security.manage` and deleting is `circle.delete`, and hanging the whole section off
   * one of the three would hide the other two from somebody who holds them.
   */
  permissions?: string[]
}

const SECTIONS: Section[] = [
  { to: '/board', label: 'Board', permissions: ['tod.read'] },
  { to: '/members', label: 'Members', permissions: ['member.read'] },
  { to: '/invites', label: 'Invites', permissions: ['invite.read'] },
  { to: '/timers', label: 'Timers', permissions: ['circle.manage'] },
  { to: '/audit', label: 'Audit', permissions: ['audit.read'] },
  { to: '/devices', label: 'Devices' },
  {
    to: '/settings',
    label: 'Settings',
    permissions: ['circle.manage', 'circle.security.manage', 'circle.delete'],
  },
  { to: '/admin/providers', label: 'Instance', permissions: ['instance.security.manage'] },
]

export function Shell() {
  const { principal, loading, error, stale, staleError, reload } = usePrincipalState()
  const navigate = useNavigate()

  useEffect(() => {
    if (!loading && !principal && !error) navigate('/signin', { replace: true })
  }, [loading, principal, error, navigate])

  if (loading) return <Spinner label="Reading your principal" />
  if (error) {
    return (
      <div className="mx-auto max-w-2xl p-6">
        <ProblemNotice error={error} />
      </div>
    )
  }
  if (!principal) return null

  // The step-up affordance wraps the whole authenticated frame, because the control belongs on the
  // FAILURE and a failure can be rendered by any screen. Outside this tree [useStepUp] answers
  // null, so `ProblemNotice` on the sign-in and join pages draws no button it cannot honour.
  return (
    <StepUpProvider>
    <div className="flex h-full">
      <nav className="flex w-44 shrink-0 flex-col border-r border-ink-800 bg-ink-900">
        <div className="border-b border-ink-800 px-4 py-3">
          <p className="text-sm font-semibold tracking-tight text-ink-100">tod-serve</p>
          <p className="text-[11px] text-ink-500">time of death</p>
        </div>
        <ul className="flex-1 space-y-0.5 p-2">
          {SECTIONS.filter(
            (s) => !s.permissions || s.permissions.some((p) => principal.can(p)),
          ).map((section) => (
            <li key={section.to}>
              <NavLink
                to={section.to}
                className={({ isActive }) =>
                  classes(
                    'block rounded px-2.5 py-1.5 text-xs transition-colors',
                    isActive
                      ? 'bg-accent-600/15 text-accent-400'
                      : 'text-ink-300 hover:bg-ink-800 hover:text-ink-100',
                  )
                }
              >
                {section.label}
              </NavLink>
            </li>
          ))}
        </ul>
        <div className="space-y-2 border-t border-ink-800 px-3 py-2.5">
          <div>
            <p className="truncate text-xs text-ink-200">{principal.view.display_name}</p>
            <p className="text-[11px] text-ink-500">{principal.view.role}</p>
          </div>
          <SignOut />
        </div>
      </nav>

      <div className="flex min-w-0 flex-1 flex-col">
        {/* The principal's own staleness. The nav above is drawn from `permissions`, so a stale
            one can offer a section the caller no longer holds. */}
        <StaleNotice resource={{ stale, staleError, reload }} />
        <CircleHeader />
        <main className="min-h-0 flex-1 overflow-auto p-4">
          <Outlet />
        </main>
      </div>
    </div>
    </StepUpProvider>
  )
}

/**
 * SignOut ends this browser session — this one, not every session the identity holds.
 *
 * It sits under the principal because that is where somebody looks to answer "who am I signed in
 * as", and the answer they most often want to act on is "not this person, on this machine".
 *
 * It says the session it ends is THIS one, because the identity may hold others and a control that
 * silently ended them all would be the destructive reading of the same button.
 *
 * What it does NOT do is render the confirmation. Success navigates away, so this component
 * unmounts in the same commit that would have drawn it — `tokens_kept` therefore travels WITH the
 * navigation and [SignIn] shows it. See [signedOutState]: holding it in state here is exactly the
 * bug that shape exists to make unwritable.
 *
 * A failure IS rendered here, and stays here. The one thing this control must never do is look
 * like it worked: somebody walking away from a shared machine believing they are signed out, when
 * the server never heard the request, is the exact harm sign-out exists to prevent — so on an
 * error the notice stays on screen and the navigation does not happen.
 */
function SignOut() {
  const [error, setError] = useState<Error | null>(null)
  const [busy, setBusy] = useState(false)
  const navigate = useNavigate()
  const { reload } = usePrincipalState()

  async function signOut() {
    setBusy(true)
    setError(null)
    try {
      const result = body(await api.signOut({}))
      // The principal is re-read as well as navigated away from, so anything still holding one —
      // the provider outlives this route — asks the server rather than believing what it had.
      reload()
      navigate('/signin', { replace: true, state: signedOutState(result.tokens_kept) })
    } catch (e) {
      setError(toError(e))
    } finally {
      setBusy(false)
    }
  }

  return (
    <div className="space-y-1.5">
      {error && <ProblemNotice error={error} />}
      <Button
        className="w-full justify-center"
        disabled={busy}
        onClick={() => void signOut()}
        title="Ends this browser session only. Your API tokens keep working."
      >
        {busy ? 'Signing out' : 'Sign out'}
      </Button>
    </div>
  )
}

/**
 * CircleHeader names the circle and its server, carries the weak-revocation banner, and is where
 * somebody changes which circle they are in.
 *
 * The banner is here rather than on the members screen because weak revocation is a standing fact
 * about the circle, and the failure it guards against is an officer forgetting it between the
 * screen that said so and the screen where they revoke somebody.
 *
 * It reads `listCircles` rather than `getCircle`, and the reason is the switcher below it: one
 * request answers both "what is this circle called" and "which circles did the server just
 * confirm", and a second read of the same resource is a second thing that can be stale on its own.
 * `listCircles` is `AuthSelf` — no permission at all — where `getCircle` needs `circle.read`, so
 * the header is if anything harder to lose. The representation is the same `Circle` either way;
 * what it does not carry is an `ETag`, which nothing here wanted. Circle Settings still reads
 * `getCircle`, because a write needs the tag of what it read.
 */
function CircleHeader() {
  const { principal } = usePrincipalState()
  const currentID = principal?.view.circle_id ?? ''
  const circles = useResource(
    (signal) => api.listCircles({}, { signal }).then((r) => r.data),
    [],
  )

  // Memoised on the response rather than rebuilt per render: it is the dependency of the effect
  // below, and a fresh `[]` every render is an effect that runs every render.
  const listed = useMemo(() => circles.data?.items ?? [], [circles.data])
  // Read ONCE, at mount, and deliberately not re-read. The only writer while this component is
  // alive is the effect below, and everything that effect writes is already in `listed` — which
  // wins the merge. Re-reading would be a render that learns nothing.
  const remembered = useMemo(() => rememberedCircles(), [])

  // The server's answer is written into this browser's record, which is what keeps the sign-in
  // screen's offer and the switcher's rows from being two different notions of "my circles". See
  // ../lib/circles.ts: the listed row is authoritative for the name, the record for existence.
  useEffect(() => {
    for (const circle of listed) {
      rememberCircle({ id: circle.id, name: circle.name, server: circle.server })
    }
  }, [listed])

  const choices = circleChoices(listed, remembered, currentID)
  const here = choices.find((c) => c.current) ?? null
  // The full representation, for the parts of the header only the server can answer. It is absent
  // whenever `here` came out of the browser's record instead of off this response.
  const data = listed.find((c) => c.id === currentID) ?? null

  if (!here) return <header className="h-12 border-b border-ink-800 bg-ink-900/60" />

  return (
    <header className="border-b border-ink-800 bg-ink-900/60">
      <StaleNotice resource={circles} />
      <div className="flex items-baseline gap-3 px-4 py-2.5">
        <CircleSwitcher
          here={here}
          choices={choices}
          canCreate={principal?.can('instance.circle.create') ?? false}
        />
        {data?.description && (
          <span className="truncate text-xs text-ink-500">{data.description}</span>
        )}
      </div>
      {data?.revocation_strength === 'weak' && (
        <div className="px-4 pb-2.5">
          <RevocationBanner
            strength={data.revocation_strength}
            reasons={data.revocation_weak_reasons}
            weakProviders={data.weak_providers}
          />
        </div>
      )}
    </header>
  )
}

/**
 * CircleSwitcher names the circle you are in and offers the others this browser knows about.
 *
 * **Switching is a re-authentication, and the menu says so rather than looking like a filter.** A
 * session is bound to one membership and every circle-scoped route takes `circle_id` in the path,
 * so there is no server state to flip: `authenticateIdentity` is the operation that puts somebody
 * in another circle, it needs a credential, and it lives on the sign-in screen. Sending the person
 * there with the circle already chosen is the whole of what this control does.
 *
 * The rows that are not `live` came out of this browser's record and nothing has confirmed them
 * this session — the circle may be gone, or the membership revoked. They are offered anyway,
 * because the record is the only thing that knows they exist, and marked, because an offer that
 * might 404 should not look like one that cannot.
 *
 * **A server does not identify a circle, and this list is where assuming it would show.**
 * `membership` has no per-server uniqueness — a person may hold a guild circle and an alliance
 * circle both on Blue — so two rows can carry the same badge, and the NAME is the only thing that
 * tells them apart (`ux_circle_name_norm_server` is unique on the name, not on the circle). Which
 * is why the name wraps rather than truncating: two circles called "Kittens Who Say Meep — Blue
 * Raid" and "Kittens Who Say Meep — Blue Alliance" clipped to one line are the same row twice.
 */
function CircleSwitcher({
  here,
  choices,
  canCreate,
}: {
  here: CircleChoice
  choices: CircleChoice[]
  canCreate: boolean
}) {
  const [open, setOpen] = useState(false)
  const others = choices.filter((c) => !c.current)

  return (
    <div className="relative">
      <button
        type="button"
        onClick={() => setOpen((o) => !o)}
        aria-expanded={open}
        aria-haspopup="menu"
        className={classes(
          'flex items-baseline gap-2 rounded px-1.5 py-0.5 -mx-1.5 transition-colors',
          'hover:bg-ink-800 focus:outline-none focus-visible:bg-ink-800',
        )}
        title="Which circle you are in. Switching signs you in again."
      >
        <h1 className="text-sm font-semibold text-ink-100">{here.name}</h1>
        <span
          className="rounded border border-ink-600 px-1.5 py-0.5 text-[10px] tracking-wide text-ink-300 uppercase"
          title="A circle is pinned to one server, immutably. There is no combined view."
        >
          {here.server}
        </span>
        <span aria-hidden="true" className="text-[11px] leading-none text-ink-400">
          ▾
        </span>
      </button>

      {open && (
        <>
          {/* A full-screen sibling rather than a document listener: closing on an outside press is
              a click somewhere, and a button is the thing every input device already knows how to
              press. It carries no label because it does nothing a keyboard user needs — Escape and
              the toggle above both close the menu. */}
          <button
            type="button"
            tabIndex={-1}
            aria-hidden="true"
            className="fixed inset-0 z-10 cursor-default"
            onClick={() => setOpen(false)}
          />
          <div
            role="menu"
            onKeyDown={(e) => {
              if (e.key === 'Escape') setOpen(false)
            }}
            className={classes(
              'absolute top-full left-0 z-20 mt-1.5 w-96 rounded-lg border border-ink-700',
              'bg-ink-900 shadow-lg shadow-black/50',
            )}
          >
            <div className="border-b border-ink-800 px-3 py-2">
              <p className="text-[11px] font-semibold text-ink-200">You are in {here.name}</p>
              <p className="mt-0.5 text-[11px] leading-relaxed text-ink-500">
                A session belongs to one circle, so switching means signing in again. Each circle is
                pinned to its own server and there is no combined view of two.
              </p>
              {serverIsAmbiguous(choices) && (
                <p className="mt-1 text-[11px] leading-relaxed text-ink-400">
                  Some of these share a server. That is ordinary — nothing limits how many circles
                  a server carries, or how many of them you belong to — so read the name rather
                  than the badge.
                </p>
              )}
            </div>

            {others.length > 0 && (
              <ul className="max-h-64 overflow-auto p-1.5">
                {others.map((circle) => (
                  <li key={circle.id}>
                    <Link
                      to={`/signin?circle=${encodeURIComponent(circle.id)}`}
                      onClick={() => setOpen(false)}
                      className="block rounded px-2 py-1.5 text-xs text-ink-200 hover:bg-ink-800"
                    >
                      <span className="flex items-baseline justify-between gap-2">
                        {/* Wraps rather than truncating: two circles on one server are told apart
                            by their names, and a clipped name is where two of them become one
                            row. */}
                        <span className="break-words">{circle.name}</span>
                        <span className="shrink-0 text-[10px] tracking-wide text-ink-400 uppercase">
                          {circle.server}
                        </span>
                      </span>
                      {!circle.live && (
                        <span className="mt-0.5 block text-[10px] text-ink-500">
                          from this browser’s record — nothing has confirmed it this session
                        </span>
                      )}
                    </Link>
                  </li>
                ))}
              </ul>
            )}

            {others.length === 0 && (
              <p className="px-3 py-3 text-[11px] text-ink-500">
                This browser has not signed into another circle here. A circle’s existence is not
                discoverable — no operation lists them at any permission level — so the only way to
                reach one is a link an officer sent you.
              </p>
            )}

            <div className="space-y-1 border-t border-ink-800 p-1.5">
              <Link
                to="/join"
                onClick={() => setOpen(false)}
                className="block rounded px-2 py-1.5 text-xs text-ink-300 hover:bg-ink-800 hover:text-ink-100"
              >
                Join a circle with an invite link
              </Link>
              {canCreate && (
                <Link
                  to="/circles/new"
                  onClick={() => setOpen(false)}
                  className="block rounded px-2 py-1.5 text-xs text-ink-300 hover:bg-ink-800 hover:text-ink-100"
                >
                  Create a circle
                </Link>
              )}
            </div>
          </div>
        </>
      )}
    </div>
  )
}
