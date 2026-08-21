// Timer overrides — the circle disagreeing with the catalogue.
//
// Respawn numbers are community-derived, genuinely disputed and NOT bundled with this software:
// they load from a separate seed repository, and an instance without them reports `no_timer`
// everywhere and still records times of death correctly. This screen is where a circle says "we
// think Trakanon is 5 days ± 8 hours" without waiting for anybody to agree.
//
// Every write here MOVES A RESPAWN WINDOW, which changes every derived answer hanging off it with
// nothing appended to the report log. The server pushes that invalidation inside the request, so
// this screen simply reloads and the board is already right.
//
// **There is no Edit button, and that is a limitation rather than a choice.**
// `putCircleTimerOverride` both creates and replaces, and the two need different preconditions: a
// create sends `If-Match: *` meaning "and it must NOT exist", and a replace must send the entity
// tag of what it read, because honouring the wildcard for both would let one officer overwrite
// another officer's edit having read nothing. The API has no operation that returns the tag of a
// single override — there is no `getCircleTimerOverride`, and the list carries no per-row tag — so
// a replace is not something this console can perform correctly. Changing an override is therefore
// Remove and then Add, which the screen says out loud. Faking it by deleting and re-creating
// behind one button would put a window in the hands of whichever half survived a failure between
// the two writes.

import { useState } from 'react'

import { api, body, toError } from '../api'
import { usePrincipal } from '../app/principal'
import { useResource } from '../app/useResource'
import { ProblemNotice, StaleNotice } from '../components/Problem'
import { Button, Card, Empty, Field, Input, Select, Spinner, Td, Th } from '../components/ui'
import { duration } from '../lib/asof'
import { titleCase } from '../lib/format'

const HOUR = 3600

export function TimerOverrides() {
  const principal = usePrincipal()
  const circleID = principal.view.circle_id
  const [error, setError] = useState<Error | null>(null)
  const [adding, setAdding] = useState(false)

  const overrides = useResource(
    (signal) =>
      api
        .listCircleTimerOverrides({ circle_id: circleID }, { signal })
        .then((r) => r.data),
    [circleID],
  )

  const rows = overrides.data?.items ?? []

  return (
    <div className="space-y-3">
      {error && <ProblemNotice error={error} />}

      <Card
        title="Timer overrides"
        subtitle="This circle's numbers, above the instance-wide catalogue. Nothing here is shipped with the software."
        actions={<Button onClick={() => setAdding(true)}>Add override</Button>}
      >
        <StaleNotice resource={overrides} />
        {overrides.error && (
          <div className="p-4">
            <ProblemNotice error={overrides.error} onRetry={overrides.reload} />
          </div>
        )}
        {overrides.loading && !overrides.data && <Spinner label="Reading overrides" />}
        {overrides.data && rows.length === 0 && (
          <Empty title="This circle uses the catalogue's timers.">
            An unseeded instance has none, and reports <code>no_timer</code> everywhere. That is a
            degraded state and an honest one — a guessed respawn window produces silently wrong
            times of death, which is worse than saying nothing.
          </Empty>
        )}
        {rows.length > 0 && (
          <div className="overflow-x-auto">
            <table className="w-full border-collapse text-xs">
              <thead>
                <tr>
                  <Th>Target</Th>
                  <Th>Kind</Th>
                  <Th>Opens after</Th>
                  <Th>Closes after</Th>
                  <Th>Note</Th>
                  <Th />
                </tr>
              </thead>
              <tbody>
                {rows.map((override) => (
                  <tr key={override.target_id}>
                    <Td className="text-ink-100">{override.target_name}</Td>
                    <Td>{override.window_kind}</Td>
                    <Td className="tnum">
                      {override.window_open_offset_seconds === null
                        ? '—'
                        : duration(override.window_open_offset_seconds)}
                    </Td>
                    <Td className="tnum">
                      {override.window_close_offset_seconds === null
                        ? '—'
                        : duration(override.window_close_offset_seconds)}
                    </Td>
                    <Td className="text-ink-400">{override.note || '—'}</Td>
                    <Td className="text-right">
                      <Button
                        variant="danger"
                        title="Falling back to the catalogue moves the window too, so this recomputes the board."
                        onClick={() => {
                          setError(null)
                          api
                            .deleteCircleTimerOverride({
                              circle_id: circleID,
                              target_id: override.target_id,
                            })
                            .then(() => overrides.reload())
                            .catch((err: unknown) => setError(toError(err)))
                        }}
                      >
                        Remove
                      </Button>
                    </Td>
                  </tr>
                ))}
              </tbody>
            </table>
          </div>
        )}
      </Card>

      {rows.length > 0 && (
        <p className="px-1 text-[11px] text-ink-500">
          To change an override, remove it and add it again. Replacing one in place needs the entity
          tag of what you read, and this API has no operation that returns the tag of a single
          override — so the console does not offer a button that cannot send the right precondition.
        </p>
      )}

      {adding && (
        <OverrideForm
          circleID={circleID}
          onClose={() => setAdding(false)}
          onDone={() => {
            setAdding(false)
            overrides.reload()
          }}
        />
      )}
    </div>
  )
}

function OverrideForm({
  circleID,
  onClose,
  onDone,
}: {
  circleID: string
  onClose: () => void
  onDone: () => void
}) {
  const [targetQuery, setTargetQuery] = useState('')
  const [targetID, setTargetID] = useState('')
  const [kind, setKind] = useState<'fixed' | 'variance' | 'unknown'>('variance')
  const [openHours, setOpenHours] = useState(0)
  const [closeHours, setCloseHours] = useState(0)
  const [note, setNote] = useState('')
  const [candidates, setCandidates] = useState<Array<{ id: string; name: string }>>([])
  const [error, setError] = useState<Error | null>(null)
  const [busy, setBusy] = useState(false)

  const search = () => {
    setError(null)
    api
      .listRaidTargets({ q: targetQuery.trim(), limit: 20 }, {})
      .then((r) => setCandidates((body(r).items ?? []).map((t) => ({ id: t.id, name: t.name }))))
      .catch((err: unknown) => setError(toError(err)))
  }

  const submit = () => {
    if (!targetID) return
    setBusy(true)
    setError(null)
    api
      .putCircleTimerOverride(
        {
          circle_id: circleID,
          target_id: targetID,
          body: {
            window_kind: kind,
            ...(kind === 'unknown'
              ? {}
              : {
                  window_open_offset_seconds: Math.round(openHours * HOUR),
                  window_close_offset_seconds: Math.round(
                    (kind === 'fixed' ? openHours : closeHours) * HOUR,
                  ),
                }),
            note: note.trim(),
          },
        },
        // A create has no prior tag to send, so `*` is borrowed as "and it must NOT exist". The
        // server refuses `*` when the override already exists — 412, carrying the current
        // representation — which is exactly the collision this form should not paper over.
        { ifMatch: '*' },
      )
      .then(onDone)
      .catch((err: unknown) => {
        setError(toError(err))
        setBusy(false)
      })
  }

  return (
    <Card title="New override">
      <div className="space-y-3 p-4">
        {error && <ProblemNotice error={error} />}

        <div className="space-y-2">
            <Field label="Target" hint="Searched over names and aliases, matched normalised.">
              <div className="flex gap-2">
                <Input
                  value={targetQuery}
                  placeholder="Vulak"
                  onChange={(e) => setTargetQuery(e.target.value)}
                />
                <Button onClick={search}>Search</Button>
              </div>
            </Field>
          {candidates.length > 0 && (
            <Select
              value={targetID}
              className="w-full"
              onChange={(e) => setTargetID(e.target.value)}
            >
              <option value="">choose a target</option>
              {candidates.map((c) => (
                <option key={c.id} value={c.id}>
                  {c.name}
                </option>
              ))}
            </Select>
          )}
        </div>

        <div className="grid gap-3 md:grid-cols-4">
          <Field label="Window kind">
            <Select
              value={kind}
              className="w-full"
              onChange={(e) => setKind(e.target.value as typeof kind)}
            >
              <option value="variance">variance — a band</option>
              <option value="fixed">fixed — one instant, plus a grace</option>
              <option value="unknown">unknown — say so rather than guess</option>
            </Select>
          </Field>
          {kind !== 'unknown' && (
            <Field label="Opens after (hours)">
              <Input
                type="number"
                min={0}
                step="0.25"
                value={openHours}
                onChange={(e) => setOpenHours(Number(e.target.value))}
              />
            </Field>
          )}
          {kind === 'variance' && (
            <Field label="Closes after (hours)">
              <Input
                type="number"
                min={0}
                step="0.25"
                value={closeHours}
                onChange={(e) => setCloseHours(Number(e.target.value))}
              />
            </Field>
          )}
          <Field label="Note" hint="Why these numbers, and who disputes them.">
            <Input value={note} maxLength={500} onChange={(e) => setNote(e.target.value)} />
          </Field>
        </div>

        <p className="text-[11px] text-ink-500">
          {kind === 'unknown'
            ? 'This target will report no_timer for this circle. An honest degraded state.'
            : `Every board in this circle that depends on ${titleCase(kind)} timing is recomputed when this saves.`}
        </p>

        <div className="flex justify-end gap-2">
          <Button onClick={onClose} disabled={busy}>
            Cancel
          </Button>
          <Button variant="primary" onClick={submit} disabled={busy || !targetID}>
            {busy ? 'Saving…' : 'Save'}
          </Button>
        </div>
      </div>
    </Card>
  )
}
