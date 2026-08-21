// Devices — my own tokens, and nobody else's.
//
// `listMyTokens` is a `self` operation: officers see nobody's tokens, including their own members'.
// A device list is the thing you scan to find the laptop you no longer have, and it is not an
// administrative view of other people.
//
// The 8-character public prefix is how a leaked token is FOUND — it is what the logs carry — and
// the secret is never logged, never re-readable and never stored anywhere in this browser.

import { useState } from 'react'

import { api, toError } from '../api'
import { usePrincipal } from '../app/principal'
import { useResource } from '../app/useResource'
import { ProblemNotice, StaleNotice } from '../components/Problem'
import { Button, Card, Empty, Mono, Spinner, Td, Th } from '../components/ui'
import { hasInstant, instant } from '../lib/format'

export function Devices() {
  const principal = usePrincipal()
  const [error, setError] = useState<Error | null>(null)
  const tokens = useResource(
    (signal) => api.listMyTokens({ limit: 100 }, { signal }).then((r) => r.data),
    [],
  )

  const rows = tokens.data?.items ?? []

  return (
    <div className="space-y-3">
      <Card
        title="This session"
        subtitle="What the API says you are, rather than what this browser assumed."
      >
        <dl className="grid gap-3 p-4 text-xs md:grid-cols-4">
          <div>
            <dt className="text-[11px] tracking-wide text-ink-400 uppercase">Role</dt>
            <dd className="mt-0.5 text-ink-100">{principal.view.role}</dd>
          </div>
          <div>
            <dt className="text-[11px] tracking-wide text-ink-400 uppercase">Credential</dt>
            <dd className="mt-0.5 text-ink-100">
              {principal.view.token_prefix ? (
                <Mono>{principal.view.token_prefix}</Mono>
              ) : (
                'browser session'
              )}
            </dd>
          </div>
          <div>
            <dt className="text-[11px] tracking-wide text-ink-400 uppercase">Scopes</dt>
            <dd className="mt-0.5 text-ink-100">
              {(principal.view.scopes ?? []).length > 0
                ? (principal.view.scopes ?? []).join(', ')
                : 'none — a session is not scoped'}
            </dd>
          </div>
          <div>
            <dt
              className="text-[11px] tracking-wide text-ink-400 uppercase"
              title="Capability-floor operations need a session that has re-authenticated within this window."
            >
              Stepped up
            </dt>
            <dd className="mt-0.5 text-ink-100">
              {principal.steppedUp
                ? `yes, for ${Math.round(principal.view.step_up_window_seconds / 60)} minutes from sign-in`
                : 'no — sign in again to revoke, manage or audit'}
            </dd>
          </div>
        </dl>
      </Card>

      <Card
        title="My devices"
        subtitle="Tokens bound to this membership. Officers see nobody's, including yours."
      >
        {error && (
          <div className="p-4">
            <ProblemNotice error={error} />
          </div>
        )}
        <StaleNotice resource={tokens} />
        {tokens.error && (
          <div className="p-4">
            <ProblemNotice error={tokens.error} onRetry={tokens.reload} />
          </div>
        )}
        {tokens.loading && !tokens.data && <Spinner label="Reading devices" />}
        {tokens.data && rows.length === 0 && (
          <Empty title="No device tokens.">
            A token is minted when you join or re-authenticate, and by the service-member form on
            Members. There is no “mint me an arbitrary token” operation.
          </Empty>
        )}
        {rows.length > 0 && (
          <table className="w-full border-collapse text-xs">
            <thead>
              <tr>
                <Th>Prefix</Th>
                <Th>Name</Th>
                <Th>Scopes</Th>
                <Th>Last used</Th>
                <Th>Expires</Th>
                <Th />
              </tr>
            </thead>
            <tbody>
              {rows.map((token) => {
                const revoked = hasInstant(token.revoked_at)
                return (
                  <tr key={token.id} className={revoked ? 'opacity-50' : undefined}>
                    <Td>
                      <Mono>{token.token_prefix}</Mono>
                    </Td>
                    <Td className="text-ink-100">{token.name || '—'}</Td>
                    <Td className="text-ink-400">
                      {(token.scopes ?? []).length > 0
                        ? (token.scopes ?? []).join(' ')
                        : 'every scope its role allows'}
                    </Td>
                    <Td className="tnum text-ink-400">{instant(token.last_used_at)}</Td>
                    <Td className="tnum text-ink-400">{instant(token.expires_at)}</Td>
                    <Td className="text-right">
                      {!revoked && (
                        <Button
                          variant="danger"
                          onClick={() => {
                            setError(null)
                            api
                              .revokeToken({ token_id: token.id })
                              .then(() => tokens.reload())
                              .catch((err: unknown) => setError(toError(err)))
                          }}
                        >
                          Revoke
                        </Button>
                      )}
                    </Td>
                  </tr>
                )
              })}
            </tbody>
          </table>
        )}
      </Card>
    </div>
  )
}
