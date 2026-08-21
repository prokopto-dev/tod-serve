// Evidence is the contract; confidence is the convenience.
//
// These counts are shown to EVERY caller, including an `observer` who does not hold
// `tod.read.attribution` and therefore never sees `reporters[]`. That separation is the whole of
// the observer role — a board can be shared with an allied guild without handing over the identity
// of your trackers — and the counts stay visible because a confidence figure with no denominator
// is worse than no confidence figure.

import { duration } from '../lib/asof'
import { classes, plural } from '../lib/format'

/**
 * Counted is the shape both evidence representations share.
 *
 * The board carries `EvidenceCounts` and `getTargetState` carries `Evidence`, which adds
 * `report_ids[]`: rebuilding the id list for every target on every poll would mean clustering a
 * circle's whole report log to render a list. The counts are identical in both, and this component
 * renders the counts.
 */
export interface Counted {
  report_count: number
  distinct_reporter_count: number
  log_line_count: number
  spread_seconds?: number | null
  revoked_reporter_count: number
}

export function EvidenceCounts({
  evidence,
  className,
}: {
  evidence: Counted
  className?: string
}) {
  const parts: Array<{ text: string; title: string }> = [
    {
      text: `${evidence.distinct_reporter_count}/${evidence.report_count}`,
      title: `${plural(evidence.distinct_reporter_count, 'distinct reporter')} across ${plural(
        evidence.report_count,
        'report',
      )}`,
    },
  ]
  if (evidence.log_line_count > 0) {
    parts.push({
      text: `${evidence.log_line_count} log`,
      title: `${plural(evidence.log_line_count, 'report')} parsed from a log line rather than typed in`,
    })
  }
  if (evidence.spread_seconds !== null && evidence.spread_seconds !== undefined) {
    parts.push({
      text: `±${duration(evidence.spread_seconds)}`,
      title: 'Spread between the earliest and latest report in the current cluster',
    })
  }
  if (evidence.revoked_reporter_count > 0) {
    parts.push({
      // Shown rather than filtered. A revoked member's reports still count — revocation controls
      // access, never history — and hiding the row would make the counts stop adding up.
      text: `${evidence.revoked_reporter_count} revoked`,
      title:
        'Reporters who have since been revoked. Their reports still count: revocation controls ' +
        'access, never history.',
    })
  }
  return (
    <span className={classes('inline-flex flex-wrap items-center gap-x-2 gap-y-1', className)}>
      {parts.map((part) => (
        <span key={part.text} title={part.title} className="text-[11px] text-ink-400 tnum">
          {part.text}
        </span>
      ))}
    </span>
  )
}
