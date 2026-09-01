// `/` — the first thing anybody sees, and the only screen written for somebody who has never used
// this instance.
//
// It needs NO SESSION. Everything on it comes from two public operations — `getServerMeta` and
// `listIdentityProviders` — so a visitor with no cookie gets the whole page rather than a redirect
// to a form that assumes they already know what this is. A signed-in visitor never sees it: they
// go straight to the board, which is what they came for.
//
// **The sign-in control goes to `/signin` and says so.** It is the real provider control from
// `components/ProviderButton`, Discord's own mark and wording included, because that is what
// people look for — but pressing it does not reach Discord yet, and the line under it says the
// next screen asks which circle. That is not a step this page could skip: `authenticateIdentity`
// resolves ONE circle and takes its id, the circle chooser is on the sign-in screen, and a blurple
// button that opened a form instead of Discord with nothing saying so is the confident mistake
// this repository is built against.
//
// Nothing here reads the browser's clock and nothing renders an instant. WEB002 is a grep over
// this whole directory, but the reason is older than the gate: the only time a landing page could
// show is the local one, and the local one is the one we do not trust.

import { Navigate, useNavigate } from 'react-router-dom'

import { api } from '../api'
import { usePrincipalState } from '../app/principal'
import { useResource } from '../app/useResource'
import { ProblemNotice, StaleNotice } from '../components/Problem'
import { Mark } from '../components/Mark'
import { DiscordMark, ProviderButton } from '../components/ProviderButton'
import { ServerLine } from '../components/ServerFooter'
import { Banner, Button, Spinner } from '../components/ui'

/** WHAT_IT_IS is the product in three claims, each of which the code actually keeps. */
const WHAT_IT_IS = [
  {
    title: 'One circle, one server',
    body:
      'Blue, Green and Red are different worlds with different spawn clocks. A circle is pinned ' +
      'to one of them permanently, so a Blue fact and a Green fact never meet in a row. The ' +
      'limit runs that way only: you can belong to as many circles as you like, several on one ' +
      'server included.',
  },
  {
    title: 'Nothing is ever rewritten',
    body:
      'A correction is a new report, not an edit, and a retraction is a row of its own. A ' +
      'revoked member’s reports still count: revocation controls access, never history. The ' +
      'report log is append-only in the database, not merely by convention.',
  },
  {
    title: 'It says when it does not know',
    body:
      'A target with no seeded timer reports “no timer” rather than guessing a window. A ' +
      'contested time of death says it is contested. Confidence is an ordered word, not a ' +
      'percentage nobody can compute — a confident mistake is worse than an admission.',
  },
]

export function Landing() {
  const { principal, loading } = usePrincipalState()
  const navigate = useNavigate()
  const meta = useResource((signal) => api.getServerMeta({}, { signal }).then((r) => r.data), [])
  const providers = useResource(
    (signal) => api.listIdentityProviders({}, { signal }).then((r) => r.data),
    [],
  )

  // Loading is asked BEFORE "is there a principal": the render between "the request is in flight"
  // and "it came back" answers null for both, and treating that as signed-out would flash a
  // landing page at somebody who is already signed in. See ../app/resource.ts.
  if (loading) return <Spinner label="Checking whether you are signed in" />
  if (principal) return <Navigate to="/board" replace />

  // An instance nobody administers yet, with a setup token set, has not been stood up. There is no
  // circle to sign into and no provider to sign in with, so the wizard is the only honest
  // destination — the same routing the sign-in screen does, on the same field. ADR-0016.
  if (meta.data?.setup_available) return <Navigate to="/setup" replace />

  const instance = meta.data?.name || 'tod-serve'
  const rows = providers.data?.items ?? []

  return (
    <div className="h-full overflow-auto">
      <StaleNotice resource={meta} />
      <StaleNotice resource={providers} />

      <div className="relative">
        {/* One quiet wash behind the mark — the glow VELIOUS puts under its gem (`mark_glow`,
            rgba(226,200,130,178)) at page scale, which means a far lower alpha. It is a GLOW and
            not a field: at 14% it never becomes a gold background, which is the rule the registry
            gates on. The console is read at 2am beside a game client; this is as bright as the
            surface gets. */}
        <div
          aria-hidden="true"
          className="pointer-events-none absolute inset-x-0 top-0 h-[32rem] bg-[radial-gradient(62rem_26rem_at_50%_-7rem,rgba(226,200,130,0.14),transparent)]"
        />

        <div className="relative mx-auto max-w-4xl px-6 pt-20 pb-16">
          <header className="text-center">
            <Mark className="mx-auto mb-5 h-16 w-16" title="tod-serve" />
            <p className="caps text-[11px] text-accent-400">time of death</p>
            <h1 className="mt-3 text-4xl font-semibold tracking-tight text-ink-100 sm:text-5xl">
              {instance}
            </h1>
            <p className="mx-auto mt-4 max-w-2xl text-base text-ink-200">
              A time-of-death registry for <strong className="text-ink-100">Project 1999</strong>{' '}
              raid targets.
            </p>
            <p className="mx-auto mt-3 max-w-2xl text-sm leading-relaxed text-ink-400">
              Your raiders report when a target died. tod-serve agrees the reports into one time of
              death and one spawn window, and every countdown on every screen is measured from the
              server’s clock rather than from whichever machine is looking at it.
            </p>
          </header>

          <section className="mt-14 grid gap-3 md:grid-cols-3">
            {WHAT_IT_IS.map((claim) => (
              <div
                key={claim.title}
                className="rounded-lg border border-ink-700 bg-ink-900/70 p-4"
              >
                <h2 className="text-sm font-semibold text-ink-100">{claim.title}</h2>
                <p className="mt-2 text-xs leading-relaxed text-ink-400">{claim.body}</p>
              </div>
            ))}
          </section>

          <section className="mx-auto mt-12 max-w-xl rounded-lg border border-ink-700 bg-ink-900/70">
            <div className="space-y-4 p-5">
              <div>
                <h2 className="text-sm font-semibold text-ink-100">Sign in</h2>
                <p className="mt-1 text-xs text-ink-400">
                  There is no tod-serve password. You sign in with an identity provider this
                  instance accepts, and always into one particular circle.
                </p>
              </div>

              <ProblemNotice error={meta.error} onRetry={meta.reload} />
              <ProblemNotice error={providers.error} onRetry={providers.reload} />
              {providers.loading && <Spinner label="Reading this instance’s providers" />}

              {rows.length > 0 && (
                <div className="space-y-2">
                  {rows.map((provider) => (
                    <div key={provider.key} className="flex items-center justify-between gap-3">
                      <p className="flex items-center gap-2 text-xs text-ink-200">
                        {provider.kind === 'discord' && (
                          <DiscordMark className="text-discord-blurple" />
                        )}
                        {provider.display_name}
                      </p>
                      <ProviderButton
                        kind={provider.kind}
                        label="Sign in"
                        onClick={() => navigate('/signin')}
                      />
                    </div>
                  ))}
                  <p className="text-[11px] text-ink-500">
                    The next screen asks which circle. A sign-in resolves to exactly one, so it is
                    a question this page cannot answer for you.
                  </p>
                </div>
              )}

              {!providers.loading && rows.length === 0 && !providers.error && (
                <Banner tone="warn" title="This instance has no identity provider enabled">
                  Nobody can sign in until an operator adds and enables one.
                </Banner>
              )}

              <div className="flex flex-wrap items-center gap-2 border-t border-ink-800 pt-4">
                <Button onClick={() => navigate('/join')}>I have an invite link</Button>
                <p className="text-[11px] text-ink-500">
                  A circle’s existence is not discoverable — there is no “list every circle”
                  operation at any permission level — so a first visit needs a link an officer sent
                  you.
                </p>
              </div>
            </div>
          </section>

          {/* The same line the console's footer carries, rendered from the `/meta` this page has
              ALREADY read to decide whether to route to setup — one request, not two. It is never a
              build-time constant: see components/ServerFooter.tsx. */}
          <footer className="mt-12 flex justify-center">
            <ServerLine meta={meta.data} />
          </footer>
        </div>
      </div>
    </div>
  )
}
