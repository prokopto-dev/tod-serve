// Who is signed in, and what they may do.
//
// `/me` is how a client discovers its own capabilities rather than probing routes and reading the
// 403s. It answers for a token with no scopes at all, so a client can find out that it has none —
// and an instance grant reaches the effective permission set, which is how the console knows
// whether to show the instance-admin section at all.

import { createContext, useContext, useMemo, type ReactNode } from 'react'

import { api, ProblemError, type PrincipalView } from '../api'
import { useResource } from './useResource'

export interface Principal {
  view: PrincipalView
  /** can reports whether the caller holds a permission from the catalogue, by key. */
  can: (permission: string) => boolean
  /** steppedUp says whether the session has re-proved its identity recently enough for the floor. */
  steppedUp: boolean
}

interface PrincipalState {
  principal: Principal | null
  loading: boolean
  /** error is non-null only for a failure that is NOT "you are signed out". */
  error: Error | null
  reload: () => void
}

const PrincipalContext = createContext<PrincipalState | null>(null)

export function PrincipalProvider({ children }: { children: ReactNode }) {
  const { data, error, loading, reload } = useResource(
    (signal) => api.getCurrentPrincipal({}, { signal }).then((r) => r.data),
    [],
  )

  const state = useMemo<PrincipalState>(() => {
    // 401 is not an error condition to render: it is the signed-out state, and the router sends
    // somebody to the sign-in screen rather than showing them a red banner about it.
    const signedOut =
      error instanceof ProblemError &&
      (error.code === 'unauthenticated' ||
        error.code === 'token_invalid' ||
        error.code === 'token_expired' ||
        error.code === 'session_required')

    if (!data) {
      return { principal: null, loading, error: signedOut ? null : error, reload }
    }
    const held = new Set(data.permissions ?? [])
    return {
      principal: {
        view: data,
        can: (permission: string) => held.has(permission),
        steppedUp: data.stepped_up,
      },
      loading,
      error: null,
      reload,
    }
  }, [data, error, loading, reload])

  return <PrincipalContext.Provider value={state}>{children}</PrincipalContext.Provider>
}

export function usePrincipalState(): PrincipalState {
  const state = useContext(PrincipalContext)
  if (!state) throw new Error('usePrincipalState outside a PrincipalProvider')
  return state
}

/** usePrincipal returns the signed-in principal, or throws — use inside an authenticated route. */
export function usePrincipal(): Principal {
  const { principal } = usePrincipalState()
  if (!principal) throw new Error('usePrincipal outside an authenticated route')
  return principal
}
