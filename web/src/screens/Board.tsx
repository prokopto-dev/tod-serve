// The board.
//
// Every ACTIVE raid target, including the ones nobody has reported: a board that hid the targets
// with no ToD would be a board that cannot tell you what you are not tracking. Sorted by
// `window_open_at`, soonest first, with everything that has no window after everything that does.
//
// It POLLS. `listTargetStates` carries an `ETag` and answers `304`, which at a hundred-odd rows is
// cheap enough that a console with no realtime layer is a complete product rather than a degraded
// one. The two event routes do not exist yet — realtime is Phase 6 — and this does not pretend
// otherwise by opening one.
//
// An UNSEEDED instance shows `no_timer` everywhere and is still useful: it renders "died 4 hours
// ago" for every target somebody has reported, and it renders no window at all.

import { useMemo, useState } from 'react'
import { Link, useSearchParams } from 'react-router-dom'

import { api, type BoardEntry } from '../api'
import { ConfidenceSteps } from '../components/Confidence'
import { ContestedChip } from '../components/Contested'
import { EvidenceCounts } from '../components/Evidence'
import { ProblemNotice, StaleNotice } from '../components/Problem'
import { STATUS_ORDER, StatusChip, statusLabel } from '../components/StatusChip'
import { NoWindow, WindowBar, hasWindow } from '../components/WindowBar'
import { Card, Empty, Input, Select, Spinner, Td, Th } from '../components/ui'
import { countdown, offsetNow, type AsOf } from '../lib/asof'
import { hasInstant, secondsBetweenServerInstants, titleCase } from '../lib/format'
import { usePrincipal } from '../app/principal'
import { usePoll, useTick } from '../app/useResource'

/** How often the board revalidates. Fifteen seconds is a raid's attention span, not a computer's. */
const POLL_MS = 15_000

const EXPANSIONS = ['classic', 'kunark', 'velious'] as const

export function Board() {
  const principal = usePrincipal()
  const circleID = principal.view.circle_id
  const [params, setParams] = useSearchParams()

  const status = params.get('status') ?? ''
  const expansion = params.get('expansion') ?? ''
  const zone = params.get('zone') ?? ''
  const contested = params.get('contested') ?? ''
  const [query, setQuery] = useState(params.get('q') ?? '')

  const filters = useMemo(
    () => ({ status, expansion, zone, contested, q: params.get('q') ?? '' }),
    [status, expansion, zone, contested, params],
  )

  const board = usePoll(
    (etag, signal) =>
      api.listTargetStates(
        {
          circle_id: circleID,
          limit: 200,
          ...(filters.status ? { status: filters.status as BoardEntry['status'] } : {}),
          ...(filters.expansion
            ? { expansion: filters.expansion as (typeof EXPANSIONS)[number] }
            : {}),
          ...(filters.zone ? { zone: filters.zone } : {}),
          ...(filters.contested ? { contested: filters.contested as 'true' | 'false' } : {}),
          ...(filters.q ? { q: filters.q } : {}),
        },
        { signal, ...(etag ? { ifNoneMatch: etag } : {}) },
      ),
    POLL_MS,
    [circleID, filters],
  )

  // Wakes the countdowns. It carries no data and reads no clock; every offset below is computed
  // from the response's own `as_of` plus monotonic elapsed time.
  useTick(1000)

  const setFilter = (name: string, value: string) => {
    const next = new URLSearchParams(params)
    if (value) next.set(name, value)
    else next.delete(name)
    setParams(next, { replace: true })
  }

  const rows = board.data?.items ?? []

  return (
    <div className="space-y-3">
      <Card
        title="Board"
        subtitle={
          board.asOf
            ? `as of ${board.asOf.label || '—'}${board.stale ? ' · last poll failed, showing what was here' : ''}`
            : undefined
        }
        actions={
          <form
            onSubmit={(e) => {
              e.preventDefault()
              setFilter('q', query.trim())
            }}
            className="flex items-center gap-2"
          >
            <Input
              value={query}
              placeholder="name or alias"
              onChange={(e) => setQuery(e.target.value)}
              className="w-44"
            />
            <Select value={status} onChange={(e) => setFilter('status', e.target.value)}>
              <option value="">every status</option>
              {STATUS_ORDER.map((s) => (
                <option key={s} value={s}>
                  {statusLabel(s)}
                </option>
              ))}
            </Select>
            <Select value={expansion} onChange={(e) => setFilter('expansion', e.target.value)}>
              <option value="">every expansion</option>
              {EXPANSIONS.map((e) => (
                <option key={e} value={e}>
                  {e}
                </option>
              ))}
            </Select>
            <Select value={contested} onChange={(e) => setFilter('contested', e.target.value)}>
              <option value="">contested or not</option>
              <option value="true">contested only</option>
              <option value="false">uncontested only</option>
            </Select>
          </form>
        }
      >
        <StaleNotice resource={board} />
        {board.error && (
          <div className="p-4">
            <ProblemNotice error={board.error} onRetry={board.reload} />
          </div>
        )}
        {board.loading && !board.data && <Spinner label="Reading the board" />}
        {board.data && rows.length === 0 && (
          <Empty title="No targets match those filters.">
            The catalogue is instance-wide. If it is empty entirely, an operator has not run{' '}
            <code className="font-mono">tod-serve seed targets</code> yet.
          </Empty>
        )}
        {rows.length > 0 && board.asOf && <BoardTable rows={rows} asOf={board.asOf} />}
      </Card>

      {board.data?.has_more && (
        <p className="px-1 text-[11px] text-ink-500">
          Showing the first 200 targets in window order. Narrow with a filter to see the rest —
          the cursor walks this same order.
        </p>
      )}
    </div>
  )
}

function BoardTable({ rows, asOf }: { rows: BoardEntry[]; asOf: AsOf }) {
  return (
    <div className="overflow-x-auto">
      <table className="w-full border-collapse text-xs">
        <thead>
          <tr>
            <Th className="w-[22%]">Target</Th>
            <Th className="w-[8rem]">Status</Th>
            <Th className="w-[16rem]">Window</Th>
            <Th className="w-[10rem]">Died</Th>
            <Th className="w-[9rem]">Confidence</Th>
            <Th>Evidence</Th>
          </tr>
        </thead>
        <tbody>
          {rows.map((row) => (
            <BoardRow key={row.target.id} row={row} asOf={asOf} />
          ))}
        </tbody>
      </table>
    </div>
  )
}

function BoardRow({ row, asOf }: { row: BoardEntry; asOf: AsOf }) {
  // `died_at` is GAME truth and may be backdated. It is rendered as an elapsed offset from the
  // response's `as_of` — "died 4h ago" — because that is the number a raid leader reads, and it
  // stays correct on a machine whose clock is wrong.
  const diedSeconds = hasInstant(row.died_at)
    ? offsetNow(secondsBetweenServerInstants(row.died_at, asOf.label), asOf)
    : null

  return (
    <tr className="hover:bg-ink-850/60">
      <Td>
        <Link
          to={`/board/${row.target.id}`}
          className="font-medium text-ink-100 hover:text-accent-400"
        >
          {row.target.name}
        </Link>
        <div className="mt-0.5 flex flex-wrap items-center gap-1.5 text-[11px] text-ink-500">
          <span>{row.target.zone}</span>
          <span className="text-ink-500">·</span>
          <span>{row.target.expansion}</span>
          {row.timer_source === 'circle_override' && (
            <span
              className="rounded border border-accent-600/50 px-1 text-[10px] text-accent-400"
              title="This circle overrides the instance-wide catalogue timer for this target."
            >
              override
            </span>
          )}
        </div>
      </Td>
      <Td>
        <div className="flex flex-col items-start gap-1">
          <StatusChip status={row.status} />
          <ContestedChip contested={row.contested} reason={row.contest_reason} />
        </div>
      </Td>
      <Td>
        {hasWindow(row.window) ? (
          <WindowBar window={row.window} asOf={asOf} />
        ) : (
          <NoWindow status={row.status} />
        )}
      </Td>
      <Td className="text-ink-300 tnum">
        {diedSeconds === null ? '—' : countdown(diedSeconds)}
        {row.change_reason && (
          <div className="text-[11px] text-ink-500" title="Why this state last changed">
            {titleCase(row.change_reason)}
          </div>
        )}
      </Td>
      <Td>
        <ConfidenceSteps confidence={row.confidence} />
      </Td>
      <Td>
        <EvidenceCounts evidence={row.evidence} />
      </Td>
    </tr>
  )
}
