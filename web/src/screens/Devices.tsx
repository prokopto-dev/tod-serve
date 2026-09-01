// Devices — my own tokens, and nobody else's.
//
// This screen is where the complaint behind ADR-0024 was visible: one person, one browser, a page
// of rows all named `device`, one per re-authentication. Re-proving a session no longer mints one
// — `stepUpSession` keeps the session it was given — so this list now grows once per sign-in.
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

/**
 * TIER_LABEL names each step-up tier in terms of what it guards rather than by its key.
 *
 * `sensitive` and `routine` are the vocabulary of the catalogue and the problem body; neither is
 * the vocabulary of somebody reading their own session state, who is asking "can I revoke a member
 * right now" and not "which enum am I in".
 */
const TIER_LABEL: Record<string, string> = {
  sensitive: 'roles, revocations, tokens:',
  routine: 'settings, timers, invites:',
}

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
              title="How recently this session proved your identity. The bar depends on what the operation costs if it is wrong."
            >
              Proof of identity
            </dt>
            <dd className="mt-0.5 space-y-0.5 text-ink-100">
              {(principal.view.step_up ?? []).map((tier) => (
                <p key={tier.tier}>
                  <span className="text-ink-400">{TIER_LABEL[tier.tier] ?? tier.tier}</span>{' '}
                  {tier.satisfied ? 'yes' : 'needs re-proving'}
                  <span className="text-ink-500">
                    {' '}
                    · {Math.round(tier.window_seconds / 60)} min
                  </span>
                </p>
              ))}
              {(principal.view.step_up ?? []).length === 0 && <p>not applicable to a token</p>}
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
            A token is minted when you join or sign in on a device, and by the service-member form
            on Members. Re-proving your identity for a step-up mints nothing. There is no “mint me
            an arbitrary token” operation.
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
