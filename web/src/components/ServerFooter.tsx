// What build answered you, on every console screen.
//
// A bug report that names a version is actionable and one that does not is a conversation. The
// binary reports something like `0.0.0-edge+cedf1fb` — a semver with the build's short sha on the
// end — and until this existed nothing in the console rendered it anywhere a signed-in officer
// would look.
//
// **IT COMES FROM `/meta`, AND IT MAY NEVER COME FROM A BUILD-TIME CONSTANT.** The console is
// served from `go:embed` by the same binary, but `index.html` is the one file that must never be
// cached and everything under `assets/` is immutable and content-hashed — so a browser CAN hold a
// bundle older than the server it is talking to. A version baked into that bundle would then state,
// confidently and in a footer, the version of a binary that is no longer running. That is worse
// than showing nothing: it is the confident mistake this repository is built against, and it would
// send somebody debugging a fixed bug. `getServerMeta` is a public operation needing no credential,
// so there is no state in which the console can render a screen and not be able to ask.
//
// `api_versions` travels with it because the two answer different questions. The version says which
// BUILD; the API versions say which base paths this binary serves, and within a version the surface
// is additive only. A client author reading over somebody's shoulder wants the second one.

import { api } from '../api'
import { useResource } from '../app/useResource'
import type { ServerMeta } from '../api'
import { StaleNotice } from './Problem'

/**
 * ServerLine renders the facts, and nothing else. It is separate from the resource so a screen that
 * already holds `/meta` — the landing page reads it to decide whether to route to setup — can
 * render the same line without asking a second time.
 *
 * `null` when there is no answer yet, rather than a placeholder: a footer that says "loading" is
 * noise on a screen somebody is trying to read, and a footer that says `unknown` beside a version
 * number is indistinguishable from a server that answered `unknown`.
 */
export function ServerLine({ meta }: { meta: ServerMeta | null | undefined }) {
  if (!meta) return null
  const apis = meta.api_versions ?? []
  return (
    <p className="flex flex-wrap items-center gap-x-2 gap-y-0.5 text-[11px] text-ink-500">
      {meta.name && <span className="text-ink-400">{meta.name}</span>}
      <span>
        tod-serve <span className="font-mono text-ink-400 select-all">{meta.version}</span>
      </span>
      {apis.length > 0 && (
        <span title="The base paths this binary serves. Within a version the surface is additive only.">
          API <span className="font-mono select-all">{apis.join(', ')}</span>
        </span>
      )}
      {!meta.configured && <span className="text-warn">not set up yet</span>}
    </p>
  )
}

/**
 * ServerFooter is [ServerLine] with its own read of `/meta`.
 *
 * It holds a resource, so it renders that resource's staleness (WEB003). The case is real rather
 * than ceremonial: this is the one thing on screen whose whole job is to be current, and a footer
 * quietly showing the version from before an operator upgraded the binary is exactly the failure
 * the version is meant to prevent.
 */
export function ServerFooter({ className }: { className?: string }) {
  const meta = useResource((signal) => api.getServerMeta({}, { signal }).then((r) => r.data), [])
  return (
    <footer className={className}>
      <StaleNotice resource={meta} />
      <div className="border-t border-ink-700 px-4 py-2">
        <ServerLine meta={meta.data} />
      </div>
    </footer>
  )
}
