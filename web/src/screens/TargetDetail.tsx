// One target: the state, the window, the evidence, the alternatives, and the report log behind it.
//
// **There is no delete here.** `tod_report` is append-only and trigger-enforced, so the action is
// RETRACT: it writes a NEW row, the original stays visible in the history below, and correcting a
// time is retract-then-post rather than an edit. A button labelled Delete that performed a
// retraction would be the UI lying about what the API does, and the history is right there
// contradicting it.

import { useState } from 'react'
import { Link, useParams } from 'react-router-dom'

import { api, ProblemError, type Report, type TargetStateResponse, toError } from '../api'
import { usePrincipal } from '../app/principal'
import { useResource, useTick } from '../app/useResource'
import { ConfidenceSteps } from '../components/Confidence'
import { ContestedChip } from '../components/Contested'
import { EvidenceCounts } from '../components/Evidence'
import { ProblemNotice, StaleNotice, type Stale } from '../components/Problem'
import { StatusChip } from '../components/StatusChip'
import { NoWindow, WindowBar, hasWindow } from '../components/WindowBar'
import { Button, Card, Empty, Field, Input, Mono, Spinner, Td, Th } from '../components/ui'
import { countdown, offsetNow, type AsOf } from '../lib/asof'
import { hasInstant, instant, plural, titleCase } from '../lib/format'

export function TargetDetail() {
  const principal = usePrincipal()
  const circleID = principal.view.circle_id
  const { targetId = '' } = useParams()
  const [retracting, setRetracting] = useState<Report | null>(null)

  const state = useResource(
    (signal) =>
      api
        .getTargetState({ circle_id: circleID, target_id: targetId }, { signal })
        .then((r) => r.data),
    [circleID, targetId],
  )
  const reports = useResource(
    (signal) =>
      api
        .listTodReports(
          { circle_id: circleID, target_id: targetId, include_retracted: true, limit: 50 },
          { signal },
        )
        .then((r) => r.data),
    [circleID, targetId],
  )

  useTick(1000)

  const reload = () => {
    state.reload()
    reports.reload()
  }

  if (state.loading && !state.data) return <Spinner label="Deriving this target" />
  if (state.error) return <ProblemNotice error={state.error} onRetry={state.reload} />
  if (!state.data || !state.asOf) return null

  return (
    <div className="space-y-3">
      <Link to="/board" className="text-xs text-ink-400 hover:text-accent-400">
        ← Board
      </Link>

      <StaleNotice resource={state} />
      <StateCard state={state.data} asOf={state.asOf} />

      {state.data.alternatives && state.data.alternatives.length > 0 && (
        <AlternativesCard state={state.data} asOf={state.asOf} />
      )}

      {state.data.reporters ? (
        <ReportersCard reporters={state.data.reporters} />
      ) : (
        <Card title="Reporters">
          <Empty title="You can see the evidence counts but not who reported.">
            Attribution needs <Mono>tod.read.attribution</Mono>, which is what separates an{' '}
            <code>observer</code> from a member: a board can be shared with an allied guild without
            handing over the identity of your trackers.
          </Empty>
        </Card>
      )}

      <ReportsCard
        reports={reports.data?.items ?? []}
        loading={reports.loading}
        error={reports.error}
        stale={reports}
        canRetractAny={principal.can('tod.retract.any')}
        canRetract={principal.can('tod.retract')}
        myMembership={principal.view.membership_id}
        onRetract={setRetracting}
        onReload={reports.reload}
      />

      {retracting && (
        <RetractDialog
          circleID={circleID}
          report={retracting}
          onClose={() => setRetracting(null)}
          onDone={() => {
            setRetracting(null)
            reload()
          }}
        />
      )}
    </div>
  )
}

function StateCard({ state, asOf }: { state: TargetStateResponse; asOf: AsOf }) {
  return (
    <Card
      title={state.target.name}
      subtitle={`${state.target.zone} · ${state.target.expansion} · ${titleCase(state.target.category)} · ${state.server}`}
      actions={<StatusChip status={state.status} />}
    >
      <div className="grid gap-4 p-4 md:grid-cols-3">
        <div>
          <p className="text-[11px] tracking-wide text-ink-400 uppercase">Window</p>
          <div className="mt-2">
            {hasWindow(state.window) ? (
              <WindowBar window={state.window} asOf={asOf} />
            ) : (
              <NoWindow status={state.status} />
            )}
          </div>
          <p className="mt-2 text-[11px] text-ink-500">
            timer from{' '}
            <span title="A circle override sits above the instance-wide catalogue timer.">
              {titleCase(state.timer_source)}
            </span>
          </p>
        </div>

        <div>
          <p className="text-[11px] tracking-wide text-ink-400 uppercase">Died</p>
          <p className="mt-2 text-ink-100 tnum">{instant(state.died_at)}</p>
          <p className="mt-1 text-[11px] text-ink-500">
            game truth — it may be backdated, and routinely is
          </p>
          {hasInstant(state.up_since) && (
            <p className="mt-2 text-[11px] text-[var(--color-status-up)]">
              reported up since {instant(state.up_since)}
            </p>
          )}
        </div>

        <div>
          <p className="text-[11px] tracking-wide text-ink-400 uppercase">Confidence</p>
          <div className="mt-2">
            <ConfidenceSteps confidence={state.confidence} />
          </div>
          <div className="mt-2">
            <EvidenceCounts evidence={state.evidence} />
          </div>
          <div className="mt-2">
            <ContestedChip contested={state.contested} reason={state.contest_reason} />
          </div>
        </div>
      </div>
      <footer className="border-t border-ink-800 px-4 py-2 text-[11px] text-ink-500">
        as of {asOf.label || '—'} · every countdown on this page is an offset from that instant,
        not from this computer’s clock
      </footer>
    </Card>
  )
}

function AlternativesCard({ state, asOf }: { state: TargetStateResponse; asOf: AsOf }) {
  const alternatives = state.alternatives ?? []
  return (
    <Card
      title="Alternatives"
      subtitle={
        `Other clusters whose window has not closed. Disagreement is surfaced, never resolved ` +
        `silently — and averaging two conflicting kills would name a time at which nothing happened.`
      }
    >
      <table className="w-full border-collapse text-xs">
        <thead>
          <tr>
            <Th>Died</Th>
            <Th>Opens</Th>
            <Th>Reporters</Th>
            <Th>Confidence</Th>
          </tr>
        </thead>
        <tbody>
          {alternatives.map((alt) => (
            <tr key={alt.died_at}>
              <Td className="tnum">{instant(alt.died_at)}</Td>
              <Td className="tnum">{countdown(offsetNow(alt.window?.seconds_until_open, asOf))}</Td>
              <Td className="tnum">
                {alt.distinct_reporter_count}/{alt.report_count}
              </Td>
              <Td>{alt.confidence}</Td>
            </tr>
          ))}
        </tbody>
      </table>
      {state.alternatives_total > alternatives.length && (
        <p className="border-t border-ink-800 px-4 py-2 text-[11px] text-ink-500">
          {state.alternatives_total - alternatives.length} further{' '}
          {state.alternatives_total - alternatives.length === 1 ? 'cluster is' : 'clusters are'} not
          shown: only the three newest still-open ones appear here. The rest are history, one report
          list away.
        </p>
      )}
    </Card>
  )
}

function ReportersCard({ reporters }: { reporters: NonNullable<TargetStateResponse['reporters']> }) {
  return (
    <Card title="Reporters" subtitle={plural(reporters.length, 'reporter')}>
      <ul className="flex flex-wrap gap-2 p-4">
        {reporters.map((reporter) => (
          <li
            key={reporter.membership_id}
            className="rounded border border-ink-700 bg-ink-850 px-2 py-1 text-xs"
          >
            {reporter.display_name}
            {reporter.revoked && (
              <span
                className="ml-1.5 text-[10px] tracking-wide text-amber-400 uppercase"
                title="Revoked — and their reports still count. Revocation controls access, never history."
              >
                revoked
              </span>
            )}
          </li>
        ))}
      </ul>
    </Card>
  )
}

function ReportsCard({
  reports,
  loading,
  error,
  stale,
  canRetract,
  canRetractAny,
  myMembership,
  onRetract,
  onReload,
}: {
  reports: Report[]
  loading: boolean
  error: Error | null
  stale: Stale
  canRetract: boolean
  canRetractAny: boolean
  myMembership: string
  onRetract: (report: Report) => void
  onReload: () => void
}) {
  return (
    <Card
      title="Report history"
      subtitle="Append-only. A correction is a retraction plus a new report; nothing here is ever edited or removed."
    >
      <StaleNotice resource={stale} />
      {error && (
        <div className="p-4">
          <ProblemNotice error={error} onRetry={onReload} />
        </div>
      )}
      {loading && reports.length === 0 && <Spinner label="Reading the log" />}
      {!loading && reports.length === 0 && !error && (
        <Empty title="Nobody has reported this target yet." />
      )}
      {reports.length > 0 && (
        <div className="overflow-x-auto">
          <table className="w-full border-collapse text-xs">
            <thead>
              <tr>
                <Th>Died at</Th>
                <Th>Reported at</Th>
                <Th>Kind</Th>
                <Th>Source</Th>
                <Th>Reporter</Th>
                <Th />
              </tr>
            </thead>
            <tbody>
              {reports.map((report) => {
                const mine = report.reporter_membership_id === myMembership
                const retractable =
                  report.kind !== 'retraction' &&
                  !report.retracted &&
                  (canRetractAny || (canRetract && mine))
                return (
                  <tr key={report.id} className={report.retracted ? 'opacity-50' : undefined}>
                    <Td className="tnum">{instant(report.died_at)}</Td>
                    <Td className="tnum text-ink-400">{instant(report.reported_at)}</Td>
                    <Td>
                      {report.kind === 'retraction' ? (
                        <span className="text-amber-400">retraction</span>
                      ) : (
                        report.self_confidence
                      )}
                      {report.retracted && (
                        <span className="ml-1.5 text-[10px] tracking-wide text-amber-400 uppercase">
                          retracted
                        </span>
                      )}
                    </Td>
                    <Td className="text-ink-400">{titleCase(report.source)}</Td>
                    <Td>
                      <Mono>{report.reporter_membership_id.slice(0, 8)}</Mono>
                      {report.reporter_revoked && (
                        <span
                          className="ml-1.5 text-[10px] tracking-wide text-amber-400 uppercase"
                          title="Revoked. Their reports still count."
                        >
                          revoked
                        </span>
                      )}
                    </Td>
                    <Td className="text-right">
                      {retractable && (
                        <Button
                          variant="danger"
                          onClick={() => onRetract(report)}
                          title="Writes a new retraction row. The report below stays visible."
                        >
                          Retract
                        </Button>
                      )}
                    </Td>
                  </tr>
                )
              })}
            </tbody>
          </table>
        </div>
      )}
    </Card>
  )
}

/**
 * RetractDialog says exactly what retracting does before it does it.
 *
 * The wording is not decoration: somebody reaching for this expects a delete, and finding out
 * afterwards that the original is still in the log — visible to everyone, with their name on it —
 * is the kind of surprise that makes people stop trusting the tool.
 */
function RetractDialog({
  circleID,
  report,
  onClose,
  onDone,
}: {
  circleID: string
  report: Report
  onClose: () => void
  onDone: () => void
}) {
  const [reason, setReason] = useState('')
  const [error, setError] = useState<Error | null>(null)
  const [busy, setBusy] = useState(false)

  const submit = () => {
    setBusy(true)
    setError(null)
    api
      .retractTodReport({
        circle_id: circleID,
        report_id: report.id,
        body: reason.trim() ? { reason: reason.trim() } : {},
      })
      .then(onDone)
      .catch((err: unknown) => {
        setError(toError(err))
        setBusy(false)
      })
  }

  return (
    <div className="fixed inset-0 z-20 flex items-center justify-center bg-black/60 p-4">
      <Card
        className="w-full max-w-lg"
        title="Retract this report"
        subtitle={`Died at ${instant(report.died_at)}`}
      >
        <div className="space-y-3 p-4">
          <p className="text-xs leading-relaxed text-ink-300">
            Retracting writes a <strong>new row</strong> that says this report is withdrawn. The
            original stays in the history and stays attributed to whoever posted it — the report log
            is append-only, and corrections are new rows rather than edits. To correct a time,
            retract this and post the right one.
          </p>
          {error && <ProblemNotice error={error} />}
          <Field label="Reason" hint="Kept on the retraction row. Optional, and worth writing.">
            <Input
              value={reason}
              autoFocus
              maxLength={500}
              placeholder="wrong target — that was Talendor"
              onChange={(e) => setReason(e.target.value)}
            />
          </Field>
          <div className="flex justify-end gap-2">
            <Button onClick={onClose} disabled={busy}>
              Cancel
            </Button>
            <Button variant="danger" onClick={submit} disabled={busy}>
              {busy ? 'Retracting…' : 'Retract'}
            </Button>
          </div>
          {error instanceof ProblemError && error.code === 'already_retracted' && (
            <p className="text-[11px] text-ink-500">
              A retraction is not itself retracted. Post a fresh report instead.
            </p>
          )}
        </div>
      </Card>
    </div>
  )
}
