// The authenticated frame: a narrow rail of sections, a header that says which circle and which
// server, and the screen.
//
// A circle is pinned to ONE server, immutably, and there is no combined view anywhere — so the
// server is stated in the header rather than being a filter somebody can change.

import { NavLink, Outlet, useNavigate } from 'react-router-dom'
import { useEffect } from 'react'

import { api, type Circle } from '../api'
import { ProblemNotice, StaleNotice } from '../components/Problem'
import { RevocationBanner } from '../components/RevocationBanner'
import { Spinner } from '../components/ui'
import { classes } from '../lib/format'
import { rememberCircle } from '../lib/storage'
import { usePrincipalState } from './principal'
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

  return (
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
        <div className="border-t border-ink-800 px-3 py-2.5">
          <p className="truncate text-xs text-ink-200">{principal.view.display_name}</p>
          <p className="text-[11px] text-ink-500">{principal.view.role}</p>
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
  )
}

/**
 * CircleHeader names the circle and its server, and carries the weak-revocation banner.
 *
 * The banner is here rather than on the members screen because weak revocation is a standing fact
 * about the circle, and the failure it guards against is an officer forgetting it between the
 * screen that said so and the screen where they revoke somebody.
 */
function CircleHeader() {
  const { principal } = usePrincipalState()
  const circleID = principal?.view.circle_id ?? ''
  const circle = useResource(
    (signal) =>
      circleID
        ? api.getCircle({ circle_id: circleID }, { signal }).then((r) => r.data)
        : Promise.resolve(null as unknown as Circle & { as_of: string }),
    [circleID],
  )

  const data = circle.data

  useEffect(() => {
    if (data) rememberCircle({ id: data.id, name: data.name, server: data.server })
  }, [data])

  if (!data) return <header className="h-12 border-b border-ink-800 bg-ink-900/60" />

  return (
    <header className="border-b border-ink-800 bg-ink-900/60">
      <StaleNotice resource={circle} />
      <div className="flex items-baseline gap-3 px-4 py-2.5">
        <h1 className="text-sm font-semibold text-ink-100">{data.name}</h1>
        <span
          className="rounded border border-ink-600 px-1.5 py-0.5 text-[10px] tracking-wide text-ink-300 uppercase"
          title="A circle is pinned to one server, immutably. There is no combined view."
        >
          {data.server}
        </span>
        {data.description && (
          <span className="truncate text-xs text-ink-500">{data.description}</span>
        )}
      </div>
      {data.revocation_strength === 'weak' && (
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
