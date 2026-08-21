// Confidence is an ORDERED ENUM and is rendered as one: four steps, one per value, with the word
// beside them.
//
// It is never a percentage, never a bar that fills, and never a number. A 0–1 float would be false
// precision, would be a float in a subsystem that bans them, and would be read as a probability
// this project cannot compute — see consensus §7. Four discrete steps cannot be mistaken for one.
//
// The evidence counts sit beside it everywhere, including for an `observer` who cannot see
// `reporters[]`: a confidence figure with no denominator is worse than no confidence figure.

import type { BoardEntry } from '../api'
import { classes } from '../lib/format'

type Confidence = BoardEntry['confidence']

/** The order IS the rule: unknown < low < medium < high. */
const ORDER: Confidence[] = ['unknown', 'low', 'medium', 'high']

const EXPLANATION: Record<Confidence, string> = {
  unknown: 'No usable report.',
  low: 'One distinct reporter, typed in by hand or posted by an API client.',
  medium: 'One reporter with a log line, or two or more reporters spread over more than 5 minutes.',
  high: 'Two or more reporters within 5 minutes, or a log line plus a corroborating reporter.',
}

const FILL: Record<Confidence, string> = {
  unknown: 'bg-ink-600',
  low: 'bg-ink-400',
  medium: 'bg-accent-500',
  high: 'bg-[var(--color-status-inwindow)]',
}

export function ConfidenceSteps({
  confidence,
  className,
}: {
  confidence: Confidence
  className?: string
}) {
  const index = ORDER.indexOf(confidence)
  return (
    <span
      className={classes('inline-flex items-center gap-1.5', className)}
      title={`${confidence} — ${EXPLANATION[confidence]}`}
    >
      <span className="flex items-center gap-[2px]" aria-hidden="true">
        {ORDER.map((step, i) => (
          <span
            key={step}
            className={classes(
              'h-3 w-1.5 rounded-[1px]',
              i <= index ? FILL[confidence] : 'bg-ink-700',
            )}
          />
        ))}
      </span>
      <span className="text-[11px] text-ink-300">{confidence}</span>
    </span>
  )
}
