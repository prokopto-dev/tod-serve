// The audit log.
//
// `audit.read` is in the capability floor: no personal access token reaches it at any scope, and
// the session has to have re-authenticated recently. A bulk export of who did what is exactly the
// kind of thing a leaked bot token must not be able to do, so when this screen answers
// `step_up_required` it SAYS SO rather than showing an empty table.
//
// Every row carries the hash chain. `prev_hash` is rendered because that is what makes the chain
// checkable by somebody who does not trust this page.

import { useState } from 'react'

import { api } from '../api'
import { usePrincipal } from '../app/principal'
import { useResource } from '../app/useResource'
import { ProblemNotice } from '../components/Problem'
import { Card, Empty, Mono, Spinner, Td, Th } from '../components/ui'
import { instant, titleCase } from '../lib/format'

export function AuditLog() {
  const principal = usePrincipal()
  const circleID = principal.view.circle_id
  const [expanded, setExpanded] = useState<string | null>(null)

  const audit = useResource(
    (signal) =>
      api.listCircleAudit({ circle_id: circleID, limit: 100 }, { signal }).then((r) => r.data),
    [circleID],
  )

  const rows = audit.data?.items ?? []

  return (
    <Card
      title="Audit log"
      subtitle="Append-only and hash-chained. Reading it needs a re-authenticated browser session; no token reaches it."
    >
      {audit.error && (
        <div className="p-4">
          <ProblemNotice error={audit.error} onRetry={audit.reload} />
        </div>
      )}
      {audit.loading && !audit.data && <Spinner label="Reading the audit log" />}
      {audit.data && rows.length === 0 && <Empty title="Nothing has been audited yet." />}
      {rows.length > 0 && (
        <div className="overflow-x-auto">
          <table className="w-full border-collapse text-xs">
            <thead>
              <tr>
                <Th>When</Th>
                <Th>Action</Th>
                <Th>Entity</Th>
                <Th>Actor</Th>
                <Th>Chain</Th>
              </tr>
            </thead>
            <tbody>
              {rows.map((row) => (
                <>
                  <tr
                    key={row.id}
                    className="cursor-pointer hover:bg-ink-850/60"
                    onClick={() => setExpanded(expanded === row.id ? null : row.id)}
                  >
                    <Td className="tnum text-ink-400">{instant(row.created_at)}</Td>
                    <Td className="text-ink-100">{titleCase(row.action)}</Td>
                    <Td className="text-ink-400">
                      {row.entity_type}
                      {row.entity_id ? ` ${row.entity_id.slice(0, 8)}` : ''}
                    </Td>
                    <Td>
                      <Mono>{row.actor_membership_id.slice(0, 8)}</Mono>
                    </Td>
                    <Td>
                      <Mono className="text-ink-500">{row.hash.slice(0, 12)}</Mono>
                    </Td>
                  </tr>
                  {expanded === row.id && (
                    <tr key={`${row.id}-detail`}>
                      <Td className="bg-ink-950/60" />
                      <td className="border-b border-ink-800/70 bg-ink-950/60 px-3 py-2" colSpan={4}>
                        <pre className="overflow-x-auto font-mono text-[11px] text-ink-300">
                          {JSON.stringify(row.detail, null, 2)}
                        </pre>
                        <p className="mt-1 text-[11px] text-ink-500">
                          prev <Mono>{row.prev_hash ? row.prev_hash.slice(0, 24) : 'genesis'}</Mono>
                        </p>
                      </td>
                    </tr>
                  )}
                </>
              ))}
            </tbody>
          </table>
        </div>
      )}
      {audit.data?.has_more && (
        <p className="border-t border-ink-800 px-4 py-2 text-[11px] text-ink-500">
          Showing the most recent 100 entries.
        </p>
      )}
    </Card>
  )
}
