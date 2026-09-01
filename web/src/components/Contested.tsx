// Disagreement is surfaced, never resolved silently — and never as an alarm.
//
// A contested target is a normal, expected outcome of two people seeing different things. The chip
// is deliberately quiet: it says which rule fired and it makes the alternatives reachable, because
// the useful response is to look at them, not to panic.

import type { BoardEntry } from '../api'
import { classes, titleCase } from '../lib/format'

const REASONS: Record<string, string> = {
  thin_supersede:
    'A later kill was reported, but by too few people to displace the one below it on its own.',
  implausible_ordering:
    'A report lands before the current window could physically have opened. It is flagged, never ' +
    'rejected: derived state must not veto an observation.',
  wide_spread: 'The reports in the current cluster are spread further apart than they should be.',
  pending_supersede: 'A newer cluster is forming and has not yet earned the current slot.',
}

export function ContestedChip({
  contested,
  reason,
  className,
}: {
  contested: boolean
  reason: BoardEntry['contest_reason']
  className?: string
}) {
  if (!contested) return null
  const key = reason ?? ''
  return (
    <span
      title={REASONS[key] ?? 'Reports disagree about this target.'}
      className={classes(
        'inline-flex items-center gap-1 rounded border border-warn/40 bg-warn/12',
        'px-1.5 py-0.5 text-[10px] text-warn',
        className,
      )}
    >
      contested{reason ? ` · ${titleCase(reason)}` : ''}
    </span>
  )
}
